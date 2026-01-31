# Plan: Sprites Backend with Nix Tarball

## Goal
Add Fly.io Sprites as a sandbox backend alongside Modal, using a nix-built tarball to provision the environment.

## Why Sprites
- **Checkpoint/restore in ~300ms** - fast warm starts
- **Persistent 100GB NVMe storage** - agent can resume work
- **WebSocket exec API** - fits our cook-agent model
- **Go SDK available** - `github.com/superfly/sprites-go`

## Why Tarball (not OCI image)
Sprites don't support custom OCI images. Options:
1. Install tools via apt/npm during Setup (~slow)
2. `nix develop` in Sprite (~slow first boot)
3. **Extract pre-built nix tarball** (~faster, reuses our nix config)

## Implementation Plan

### Phase 1: Nix Tarball Build

**File:** `nix/sandbox-tarball.nix`

Build a tarball containing our sandbox environment (same packages as `sandbox-image.nix`):

```nix
{ pkgs }:
pkgs.buildEnv {
  name = "cook-sandbox-env";
  paths = with pkgs; [
    coreutils bashInteractive git curl wget jq
    vim neovim ripgrep fd nodejs_20
    # ... same as sandbox-image.nix
  ];
  pathsToLink = [ "/bin" "/lib" "/share" ];
}
```

Then tar it: `nix build .#sandbox-tarball && tar -czf sandbox.tar.gz -C result .`

**Output:** `sandbox.tar.gz` (~200-400MB) uploaded to object storage or embedded.

### Phase 2: Sprites Backend Implementation

**File:** `internal/env/sprites.go`

```go
type SpritesBackend struct {
    config      Config
    client      *sprites.Client
    sprite      *sprites.Sprite
    spriteName  string
    workDir     string
    agentAddr   string
}
```

**Key methods:**

1. **Setup(ctx)**
   - Create Sprite via API
   - Download/extract nix tarball to `/nix` or `/opt/sandbox`
   - Set PATH to include tarball bins
   - Clone repo to /workspace
   - Copy and start cook-agent
   - Checkpoint the Sprite (for fast future restores)

2. **Exec(ctx, cmd)**
   - Use Sprites WebSocket exec API
   - Or HTTP POST `/v1/sprites/{name}/exec`

3. **AgentAddr()**
   - Use Sprites TCP proxy: `wss://api.sprites.dev/v1/sprites/{name}/proxy`

4. **Teardown(ctx)**
   - Delete Sprite (or keep for resume)

### Phase 3: Factory Integration

**File:** `internal/env/factory.go`

```go
const TypeSprites Type = "sprites"

func NewBackend(backendType Type, cfg Config) (Backend, error) {
    switch backendType {
    // ...
    case TypeSprites:
        return NewSpritesBackend(cfg)
    }
}
```

### Phase 4: Checkpoint Management

Add checkpoint support for fast warm starts:

```go
func (b *SpritesBackend) Checkpoint(ctx context.Context) (string, error)
func (b *SpritesBackend) RestoreFromCheckpoint(ctx context.Context, checkpointID string) error
```

Consider auto-checkpointing after Setup completes.

### Phase 5: Tests

**File:** `internal/env/sprites_test.go`
- Skip if `SPRITES_TOKEN` not set
- Test: Create, Exec, ReadFile, WriteFile, Teardown
- Test: Checkpoint and restore

**File:** `e2e/sprites-backend.spec.ts`
- Full user journey with Sprites backend selection

## Files to Create/Modify

| File | Action |
|------|--------|
| `nix/sandbox-tarball.nix` | Create - tarball build |
| `flake.nix` | Modify - add sandbox-tarball output |
| `internal/env/sprites.go` | Create - backend impl |
| `internal/env/sprites_test.go` | Create - unit tests |
| `internal/env/factory.go` | Modify - add TypeSprites |
| `internal/env/backend.go` | Modify - add type constant |
| `e2e/sprites-backend.spec.ts` | Create - e2e test |
| `go.mod` | Modify - add sprites-go dependency |

## Open Questions

1. **Tarball hosting** - Where to store the tarball? Options:
   - Fly.io object storage
   - GitHub releases
   - S3/R2
   - Embed in cook binary (if small enough after compression)

2. **Checkpoint lifecycle** - When to create/delete checkpoints?
   - Per-user base checkpoint?
   - Per-repo checkpoint?
   - TTL on checkpoints?

3. **Fallback** - If Sprite creation fails, fall back to Modal?

## Next Steps

1. Build and test nix tarball locally
2. Upload tarball to hosting
3. Implement SpritesBackend
4. Test manually with Sprites API
5. Add to factory and tests
