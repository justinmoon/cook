# Loom Backend

Run Cook environments on the decentralized Loom compute marketplace.

## Overview

Loom is a decentralized compute marketplace built on Nostr, Cashu, and Blossom. Workers advertise capabilities and pricing, clients submit jobs with prepaid Cashu tokens, and results are stored on Blossom.

Cook can use Loom as an execution backend alongside Docker and Modal. The key insight: we already build a complete Nix-based sandbox image for Modal - the same image can run on Loom workers.

## How It Works

### Image Distribution via Blossom

The `nix/sandbox-image.nix` builds an OCI image containing:
- claude-code CLI
- cook-agent (PTY session manager)
- git, ripgrep, neovim, node, etc.
- CA certificates and base utilities

For Loom, we build this as a tarball and upload to Blossom:

```bash
# Build tarball instead of OCI image
nix build .#sandbox-tarball

# Upload to Blossom, get hash
SHA256=$(sha256sum result | cut -d' ' -f1)
curl -X PUT "https://blossom.example.com/$SHA256" \
  -H "Authorization: Nostr $(nostr-sign)" \
  --data-binary @result
```

Workers advertise support via software tag:
```json
["S", "cook-sandbox", "v3", "blossom:$SHA256"]
```

On first job, worker downloads and caches the tarball. Subsequent jobs start instantly.

### Session Lifecycle

1. **Start**: Cook submits a Loom job that runs `cook-agent` in reverse-connect mode
2. **Connect**: Agent dials out to Cook server via WebSocket
3. **Work**: PTY sessions (terminals, claude-code) flow through this connection
4. **End**: Job completes when branch is abandoned or payment expires

```
┌─────────────┐         ┌─────────────┐         ┌─────────────┐
│   Browser   │◄───────►│ Cook Server │◄───────►│ Loom Worker │
│  (xterm.js) │   WS    │             │   WS    │ (cook-agent)│
└─────────────┘         └─────────────┘         └─────────────┘
                              ▲
                              │ Agent connects OUT
                              │ (no inbound ports needed)
```

### Reverse-Connect Protocol

Loom doesn't expose inbound ports. Instead, cook-agent connects outward:

```go
// cook-agent startup in Loom environment
cook-agent \
  -connect wss://cook.example.com/agent/connect \
  -token $COOK_AGENT_TOKEN \
  -branch $COOK_BRANCH_ID
```

Cook server accepts the connection:
```json
{"type": "hello", "branch": "myrepo/feature-x", "token": "..."}
```

After authentication, Cook routes PTY traffic over this WebSocket. The protocol is identical to the current Modal integration - same xterm.js frontend, same UX.

## Implementation

### Backend Interface

`internal/env/loom.go` implements the standard `Backend` interface:

```go
type LoomBackend struct {
    config      Config
    jobID       string        // Loom job event ID
    workerPubkey string       // Worker's nostr pubkey
    agentConn   *websocket.Conn // Reverse-connect from agent
}

func (b *LoomBackend) Setup(ctx context.Context) error
func (b *LoomBackend) Exec(ctx context.Context, cmd string) ([]byte, error)
func (b *LoomBackend) ReadFile(ctx context.Context, path string) ([]byte, error)
func (b *LoomBackend) WriteFile(ctx context.Context, path string, content []byte) error
func (b *LoomBackend) Teardown(ctx context.Context) error
```

### Loom Client

Shell out to `loom-cli` initially:

```go
func (b *LoomBackend) submitJob(ctx context.Context, cmd string, stdin []byte) (string, error) {
    args := []string{
        "execute",
        "--worker", b.workerPubkey,
        "--mint", b.config.Mint,
        "--payment", fmt.Sprintf("%d", b.config.PaymentSats),
        "--cmd", cmd,
    }

    out, err := exec.CommandContext(ctx, "loom", args...).Output()
    // Parse job ID from output
}
```

Later: native Go client using `go-nostr` + `cashu-go`.

### Bootstrap Script

The Loom job runs a bootstrap script:

```bash
#!/bin/bash
set -e

# Extract sandbox environment
tar -xf /blossom/$SANDBOX_HASH -C /

# Clone repo
git clone --branch $BRANCH_NAME $REPO_URL /workspace

# Setup dotfiles if configured
if [ -n "$DOTFILES_URL" ]; then
    git clone $DOTFILES_URL ~/.dotfiles
    ~/.dotfiles/install.sh
fi

# Copy Claude credentials (passed via NIP-44 encrypted secret)
echo "$CLAUDE_CREDENTIALS" > ~/.claude/.credentials.json
chmod 600 ~/.claude/.credentials.json

# Start agent in reverse-connect mode
exec cook-agent \
    -connect "$COOK_SERVER_URL/agent/connect" \
    -token "$COOK_AGENT_TOKEN" \
    -branch "$COOK_BRANCH_ID"
```

### Data Model

Branch environment JSON:

```json
{
  "backend": "loom",
  "path": "/workspace",
  "loom": {
    "job_id": "nostr-event-id",
    "worker_pubkey": "hex-pubkey",
    "relays": ["wss://relay.example.com"],
    "mint": "https://mint.example.com",
    "sandbox_hash": "sha256-of-sandbox-tarball",
    "agent_token": "random-auth-token"
  }
}
```

## Worker Requirements

Workers must:

1. **Support the sandbox tarball**: Advertise `["S", "cook-sandbox", "v3", "..."]`
2. **Allow outbound WebSocket**: cook-agent connects to Cook server
3. **Have sufficient resources**: ~1GB RAM for claude-code, reasonable CPU

Workers can be:
- **Self-hosted**: Run your own Loom worker with the sandbox pre-installed
- **Public market**: Find workers advertising cook-sandbox support

## Blossom Considerations

### Image Size

The sandbox tarball is ~300MB. Blossom servers have varying limits:
- blossom.nostr.build: 100MB (paid tiers may be higher)
- Self-hosted: No limit

Options:
1. Run your own Blossom server for large files
2. Use a paid Blossom service
3. Have workers pre-cache by advertising the hash they support

### Caching

Workers should cache downloaded tarballs by SHA256. First job downloads, subsequent jobs start from cache.

## Payment & Session Duration

Loom uses pay-per-second billing:

```
timeout_seconds = payment_sats / price_per_second
```

For a 1-hour session at 10 sats/second = 36,000 sats (~$36 at 100k sats/$).

### Session Extension

Long Claude Code sessions may need payment top-ups. Options:
1. **Large initial payment**: Pay for 4+ hours upfront
2. **Extension protocol**: Submit new payment before timeout
3. **Session checkpoint**: Save state to Blossom, restore on new worker

## Comparison with Other Backends

| Aspect | Docker | Modal | Loom |
|--------|--------|-------|------|
| Where it runs | Local machine | Modal cloud | Decentralized workers |
| Pricing | Free (your hardware) | Modal pricing | Market rate (Cashu) |
| Setup | Docker daemon | Modal account | Nostr keys + Cashu |
| Censorship | N/A | Modal ToS | Resistant |
| Privacy | Full | Modal sees traffic | Encrypted (NIP-44) |

## Security

- **Secrets**: Use NIP-44 encryption for Claude credentials and API keys
- **Agent token**: Random per-branch, validates reverse-connect
- **Worker trust**: Workers see decrypted secrets during execution - use trusted workers or self-host for sensitive work

## Future Enhancements

- **Worker pools**: Curated list of trusted workers for teams
- **Snapshot/restore**: Save workspace to Blossom, resume on different worker
- **GPU workers**: For AI inference workloads
- **Batch gates**: Run gate checks as one-off Loom jobs (no PTY needed)
