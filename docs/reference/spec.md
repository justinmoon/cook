# Cook v1 Specification

## Overview

Cook is an AI-native software factory - a single Go binary that manages git repos, coding agents, and CI/merge workflows. It replaces GitHub for personal/small-team use with a focus on agent-driven development.

## Development Environment

Cook uses a Nix flake for all dependencies. The flake provides:
- Go toolchain
- NATS server (for development/testing)
- SQLite
- Any other build/dev dependencies

```bash
# Enter dev shell
nix develop

# Or with direnv
echo "use flake" > .envrc && direnv allow
```

All contributors and CI use the same flake to ensure reproducible builds.

## Core Concepts

### Repo

A bare git repository stored on the factory server. Source of truth for all branches.

```
/var/lib/cook/repos/<name>.git
```

Repos are discovered by enumerating this directory. No separate metadata store - everything derived from git.

### Branch

A git branch extended with:
- **Environment**: where the work happens (local path, docker, modal, etc.)
- **Agent session**: optional coding agent working on the branch
- **Task**: optional linked issue/task
- **Gates**: validation checks (CI, review) that must pass before merge

Branches are the primary unit of work. Creating a branch spins up an environment, optionally starts an agent, and runs gates when ready.

### Environment

Where code runs. Defined by:
- **Backend**: local path, local docker, modal, fly, or remote host (via tailscale)
- **Configuration**: nix config (home-manager or configuration.nix) for the machine setup
- **Repos**: git repos to clone into the environment (the main repo + any dependencies)

```go
type Environment struct {
    Backend       Backend
    Configuration string   // git ref, e.g., github.com/user/configs#modal
    Repos         []string // git refs to clone
}

type Backend interface {
    Setup(env *Environment) error
    Exec(cmd string) error
    Attach() error
    Teardown() error
}

// Sprint 1 implementations: LocalPath
// Future: Docker, Modal, Fly, RemoteHost
```

**Sprint 1 backends:**
- `LocalPath` - just a directory on the current machine

**Future backends:**
- `Docker` - Docker container via OrbStack
- `Modal` - Modal serverless containers

### Task

A markdown file with YAML frontmatter, stored in SQLite:

```yaml
---
id: fix-login-bug
title: Fix login redirect loop
priority: 3
status: open
repo: myapp
depends_on: []
---

When users log in from the /settings page, they get stuck in a redirect loop.

## Acceptance Criteria
- Login from any page redirects to the original page
- Add regression test
```

Task statuses: `open`, `in_progress`, `needs_human`, `closed`

Tasks belong to a repo. When a branch is created for a task, the task moves to `in_progress`. When the branch merges, the task moves to `closed`.

### Gate

A validation step that must pass before merge. Gates run in sequence.

```go
type Gate struct {
    Name        string
    Kind        string      // "command", "agent_review", "human_approval"
    Command     string      // for command gates
    Agent       string      // for agent_review
    Prompt      string      // for agent_review
    Reviewers   []string    // for human_approval
    Environment *Environment // if nil, runs in branch's environment
}
```

Gates take the current branch head as input. Command gates pass if exit code is 0. Review gates pass when approved.

Default gates can be defined per-repo in `cook.toml`:

```toml
[[gates]]
name = "ci"
command = "just pre-merge"

[[gates]]
name = "review"
kind = "human_approval"
```

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                      cook server                            │
├─────────────────────────────────────────────────────────────┤
│  Repos          Tasks         Branches       Environments   │
│  (bare git)     (postgres)    (postgres)     (spawned)      │
├─────────────────────────────────────────────────────────────┤
│                     HTTP + WebSocket API                    │
├─────────────────────────────────────────────────────────────┤
│                        Web UI                               │
│  - Task inbox                                               │
│  - Branch list with gate status                             │
│  - Terminal attach (xterm.js)                               │
│  - Simple code viewer                                       │
└─────────────────────────────────────────────────────────────┘
          │
          │ tailscale / local
          ▼
┌─────────────────┐
│  cook client    │  (same binary, client mode)
│                 │
│  cook branch    │
│  cook task      │
│  cook gate      │
└─────────────────┘
```

### Single Binary

`cook` is one binary that can run as:
- **Server**: `cook serve` - runs the factory server
- **Client**: `cook <command>` - CLI that talks to a server (local or remote)

Instances find each other over tailscale. The CLI resolves which server to talk to based on:
1. `COOK_SERVER` env var
2. `~/.config/cook/config.toml`
3. Auto-discovery via tailscale (future)

### Database

PostgreSQL via `COOK_DATABASE_URL` (required for server/CLI operations).

Example (local socket):

```
postgresql:///cook?host=/run/postgresql
```

Schema is created on startup in `internal/db/db.go`.

## CLI Interface

### Repository Management

```bash
cook repo list                      # list all repos
cook repo add <name> [--clone=<url>]  # create bare repo, optionally clone from url
cook repo remove <name>             # remove repo (requires confirmation)
```

### Task Management

```bash
cook task list [--repo=<repo>] [--status=<status>]
cook task create <repo> --title="..." [--priority=N] [--body="..."]
cook task show <id>
cook task edit <id>
cook task close <id>
```

### Branch Management

```bash
# Create branch with optional agent
cook branch create <repo> <name> \
    [--task=<id>] \
    [--agent=<claude|codex|opencode>] \
    [--prompt="..."] \
    [--env=<local|docker|modal>] \
    [--config=<git-ref>]

# List branches
cook branch list [--repo=<repo>]

# Show branch status (gates, agent, etc.)
cook branch show <name>

# Attach terminal to branch's environment
cook branch attach <name>

# Run gates manually
cook branch gate <name> [--gate=<name>]

# Merge branch (if all gates pass)
cook branch merge <name>

# Abandon branch
cook branch abandon <name>
```

### Agent Control

```bash
# These run inside an environment, used by agents or humans

cook done                  # mark current branch's task as done, triggers gates
cook ask-for-help "msg"    # pause agent, mark task as needs_human
```

### Server

```bash
cook serve [--host=127.0.0.1] [--port=7420]
```

## Web UI

Server-rendered HTML using **Datastar** for reactivity:
- **Task inbox**: list tasks, create new, filter by repo/status/priority
- **Branch dashboard**: list active branches, show gate status, merge buttons
- **Terminal view**: xterm.js connected to branch environment via WebSocket
- **Code viewer**: read-only file browser for branch's current state

### Datastar Approach

Datastar enables reactive UIs with server-side rendering. The backend drives the frontend via SSE.

Key patterns:
- **SSE for real-time updates**: Branch status, gate progress, agent output stream to UI
- **Signals for local state**: `_isMenuOpen`, form inputs before submission
- **Server as source of truth**: All meaningful state lives in DB, frontend reflects it

```html
<!-- Branch status updates via SSE -->
<div id="branch-status" data-on-load="@get('/branches/foo/status')">
  Loading...
</div>

<!-- Gate progress streams in real-time -->
<div data-on-load="@get('/branches/foo/gates/stream')">
  <!-- Server pushes gate updates as they complete -->
</div>
```

### NATS for Internal Pub/Sub

NATS JetStream handles internal event distribution:
- Agent session events (started, output, completed, failed)
- Gate events (started, passed, failed)
- Branch lifecycle events (created, merged, abandoned)

This enables:
- Multiple UI clients seeing same updates
- Decoupled components (agent runner doesn't know about web UI)
- Future: webhooks, notifications, plugins subscribe to events

```go
// Agent runner publishes
js.Publish(ctx, "cook.agent.output", outputChunk)

// Web UI subscribes
cons.Consume(func(msg jetstream.Msg) {
    // Push to SSE connection
    sse.PatchElements("#terminal", string(msg.Data()))
})
```

## Workflows

### Create Branch for Task

```bash
# Human creates task
cook task create myapp --title="Fix login bug" --priority=4

# Human or automation creates branch with agent
cook branch create myapp fix-login \
    --task=fix-login-bug \
    --agent=claude \
    --prompt="Fix the bug described in TASK.md" \
    --env=modal
```

This:
1. Creates git branch `fix-login` from master
2. Spins up Modal environment with user's config
3. Clones repo into environment
4. Copies task to `TASK.md` in worktree
5. Starts Claude agent with prompt
6. Updates task status to `in_progress`

### Agent Completes Work

When agent runs `cook done`:
1. Agent session marked complete
2. Gates run in sequence (CI, review, etc.)
3. If all gates pass, branch is ready to merge
4. Notification sent to user

### Merge

```bash
cook branch merge fix-login
```

Or via web UI. This:
1. Verifies all gates passed
2. Fast-forward or merge commit to master
3. Deletes the branch
4. Updates task status to `closed`
5. Tears down environment

### Interactive Development

```bash
# Create branch without agent for manual work
cook branch create myapp experiment --env=local

# Attach terminal
cook branch attach experiment

# Work manually, then run gates
cook branch gate experiment

# Merge when ready
cook branch merge experiment
```

## Environment Backends

### LocalPath

Simplest backend. Just a directory path.

```bash
cook branch create myapp foo --env=local:/tmp/myapp-foo
```

Creates directory, clones repo, runs agent in current shell context.

### LocalDocker

```bash
cook branch create myapp foo --env=docker:nixos/nix
```

Spins up container with volume mount, runs agent inside.

### Modal

```bash
cook branch create myapp foo --env=modal --config=github.com/user/configs#modal
```

Creates Modal sandbox with:
- NixOS base image
- User's home-manager config activated
- Repo cloned
- Agent auth (Claude/Codex credentials) injected

### RemoteHost

```bash
cook branch create myapp foo --env=remote:homeserver
```

SSH to another machine running `cook`, execute there. Machines find each other via tailscale.

## Configuration

### Server Config (`/etc/cook/config.toml`)

```toml
[server]
host = "0.0.0.0"
port = 7420
data_dir = "/var/lib/cook"

[defaults]
environment = "modal"
configuration = "github.com/justinmoon/configs#modal"
```

### Client Config (`~/.config/cook/config.toml`)

```toml
[server]
url = "http://homeserver:7420"
# or auto-discover via tailscale (future)

[defaults]
agent = "claude"
```

### Per-Repo Config (`cook.toml` in repo root)

```toml
[defaults]
environment = "docker:myimage"

[[gates]]
name = "ci"
command = "just pre-merge"

[[gates]]
name = "lint"
command = "just lint"
```

## Security & Discovery

### Server Discovery

Environments need to find the cook server to call `cook done`, `cook ask-for-help`, etc.

**Sprint 1 approach:** Inject `COOK_SERVER` env var when spawning environments.

```go
// When spawning agent in environment
env := []string{
    fmt.Sprintf("COOK_SERVER=http://%s:%d", serverHost, serverPort),
    fmt.Sprintf("COOK_BRANCH=%s", branchName),
}
```

The `cook` CLI checks `COOK_SERVER` env var first, then falls back to config file.

### Authentication

**Sprint 1:** Trust localhost. Server binds to `127.0.0.1` by default. Docker containers access host via `host.docker.internal` (OrbStack supports this).

**Future:** Tailscale-based auth. Server only accepts connections from tailscale IPs. Use tailscale node identity for auth.

### Agent Credentials

Agent auth (Claude, Codex API keys) handled by:
1. Local backend: uses host's `~/.claude.json`, `~/.codex/` directly
2. Docker backend: mount credentials as read-only volumes

```go
// Docker backend mounts
volumes := []string{
    fmt.Sprintf("%s/.claude.json:/root/.claude.json:ro", homeDir),
    fmt.Sprintf("%s/.codex:/root/.codex:ro", homeDir),
}
```

## NATS Subject Schema

All cook events use the `cook.` prefix.

### Branch Events
```
cook.branch.<repo>.<branch>.created    # branch created
cook.branch.<repo>.<branch>.merged     # branch merged to master
cook.branch.<repo>.<branch>.abandoned  # branch abandoned
```

### Agent Events
```
cook.agent.<repo>.<branch>.started     # agent session started
cook.agent.<repo>.<branch>.output      # terminal output chunk (binary)
cook.agent.<repo>.<branch>.completed   # agent finished (success)
cook.agent.<repo>.<branch>.failed      # agent errored
cook.agent.<repo>.<branch>.help        # agent called ask-for-help
```

### Gate Events
```
cook.gate.<repo>.<branch>.<gate>.started   # gate started running
cook.gate.<repo>.<branch>.<gate>.passed    # gate passed
cook.gate.<repo>.<branch>.<gate>.failed    # gate failed
cook.gate.<repo>.<branch>.<gate>.output    # gate log output chunk
```

### Event Payloads

All events are JSON:
```go
type Event struct {
    Type      string    `json:"type"`
    Repo      string    `json:"repo"`
    Branch    string    `json:"branch"`
    Timestamp time.Time `json:"timestamp"`
    Data      any       `json:"data,omitempty"`
}

// For output events, Data is base64-encoded bytes
// For status events, Data contains relevant metadata
```

### JetStream Streams

```
COOK_EVENTS - all events, retention: 7 days, replicas: 1
```

Single stream for simplicity in Sprint 1. Can split later if needed.

## Non-Goals for v1

- Multi-user / multi-tenant
- GitHub/GitLab integration
- Complex gate pipelines (parallel, conditional)
- Persistent environments (environments are ephemeral per-branch)
- Mobile app (web UI works on mobile browser)
