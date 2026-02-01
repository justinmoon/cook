# Environment Backends

## Overview

An **Environment** is where code runs. Cook supports multiple backend types so agents can work in local checkouts, Docker containers, or cloud sandboxes (Modal, Sprites, Fly Machines). 

Each environment has:
- A **backend** (local, docker, modal, sprites, fly-machines) that handles execution
- A **dotfiles repo** (optional) that defines tools and configs
- One or more **project repos** to work on

## Core Concepts

### Backend

A backend handles the lifecycle of an execution environment:
- **Setup**: Provision the environment, clone repos, install tools, apply dotfiles
- **Exec**: Run commands inside the environment
- **Attach**: Get interactive terminal access (PTY)
- **Teardown**: Destroy the environment

### Dotfiles

A dotfiles repo provides CLI tools and configuration. It's a regular git repo with:

**Minimal example** (just config files):
```
github.com/user/dotfiles/
├── .zshrc
├── .vimrc
└── .gitconfig
```

**With custom packages** (optional flake.nix):
```
github.com/user/dotfiles/
├── flake.nix      # If present, `nix build` for packages
├── .zshrc
├── .vimrc
└── .config/
    └── nvim/
        └── init.lua
```

**Setup logic:**
1. Clone dotfiles repo into environment
2. If `flake.nix` exists → `nix build` it, add to PATH
3. If no `flake.nix` → use cook's default package set
4. Symlink all non-nix files (`flake.nix`, `flake.lock`, `.git`) into `$HOME`

**Cook's default packages** (when no flake.nix):
- Shell: zsh, bash
- Tools: git, ripgrep, fd, jq, curl, wget
- Editor: neovim, vim

**Live editing:** Since dotfiles is a cloned repo in the workspace, users can:
- Edit configs directly via terminal or editor tab
- Commit and push improvements
- Future environments automatically get updates

### Backend Types

| Backend | Use Case | Provisioning | Persistence |
|---------|----------|--------------|-------------|
| **local** | Development on your machine | Git clone to local path | Persistent until branch merged/abandoned |
| **docker** | Isolated containers via OrbStack | Docker container with volume | Container lifecycle |
| **modal** | Cloud sandboxes | Modal Sandbox API | Sandbox lifecycle + volumes |
| **sprites** | Cloud sandboxes | Sprites API + nix tarball | Sprite lifecycle + checkpoints |
| **fly-machines** | Cloud VMs | Fly Machines API + OCI image | Machine lifecycle + volumes |

### Remote Backends Need a Public Git URL

Modal/Sprites/Fly Machines clone repos inside remote sandboxes, so they need a public URL to the bare repos.

Set:
- `COOK_PUBLIC_URL` to the **public base URL** of your Cook server.

In dev, you can expose the local server with:
```
expose 7420
```
Then set `COOK_PUBLIC_URL` to the printed URL (include basic auth if used).

---

## Tailnet Dev Tests (optional)

If you're using Tailscale to reach your dev host, run the integration tests with tailnet checks:

Required env:
- `COOK_TAILNET_TEST=1`
- `TS_AUTHKEY`
- `COOK_TAIL_IP`

Optional:
- `COOK_TAIL_PORT` (defaults to `COOK_PORT` or `7420`)
- `COOK_TAIL_GIT_URL` (if set, tests git over tailnet)
- `COOK_TAIL_HOSTNAME` (override node hostname)

Run:
```
just test-tailnet
```

## Fly Machines Setup (single-user)

Required:
- `FLY_API_TOKEN` (or `FLY_TOKEN`) for Fly auth
- A Fly app (default: `cook-sandbox`)
- Image in Fly registry, built from the nix sandbox image:
  - `nix build .#sandbox-image`
  - `docker load < result`
  - `docker tag ghcr.io/justinmoon/cook-sandbox:latest registry.fly.io/<app>:cook-env`
  - `docker push registry.fly.io/<app>:cook-env`
- Public IPs for DNS (`fly ips allocate-v6` and `fly ips allocate-v4`)

Optional overrides:
- `FLY_MACHINES_APP` (app name)
- `FLY_MACHINES_IMAGE` (override image, default `registry.fly.io/<app>:cook-env`)
- `FLY_MACHINES_AGENT_ADDR` (override agent URL)
- `FLY_MACHINES_REUSE=1` / `COOK_FLY_MACHINES_REUSE=1` (reuse an existing machine for dev/testing)
- `COOK_AGENT_DNS_SERVER` (custom DNS server, e.g. `8.8.8.8:53`)

## Web UI Flow

### Branch Creation (on Repo Detail Page)

Current flow:
1. User clicks "New Branch" 
2. Enters branch name, optionally links task
3. Branch created with local checkout

**New flow:**
1. User clicks "New Branch"
2. Form shows:
   - Branch name (required)
   - Link to task (optional dropdown)
   - **Backend** (dropdown: Local / Docker / Modal / Sprites / Fly Machines)
   - **Dotfiles** (optional text field, e.g., `github.com/me/dotfiles`)
3. On submit:
   - Backend.Setup() called
   - Progress shown (especially for Docker/Modal/Sprites/Fly Machines which take longer)
   - Redirect to branch detail page when ready

### Branch Detail Page

The branch detail page already has terminals, editors, previews. These need to work across backends:

| Feature | Local | Docker | Modal | Sprites | Fly Machines |
|---------|-------|--------|-------|---------|--------------|
| Terminal tabs | ✅ PTY in checkout | PTY via `docker exec` | PTY via Modal streaming | PTY via cook-agent proxy | PTY via cook-agent public URL |
| Editor tabs | ✅ Read/write files | Read/write via docker cp or mount | Read/write via Modal volumes | Read/write via Sprites FS API | Read/write via Fly exec |
| Preview tabs | ✅ localhost URLs | Container port mapping | Modal tunnel URLs | Sprites proxy URLs | Fly proxy URLs |

### Backend Status Indicator

Show backend status in sidebar:
- 🟢 Running (healthy)
- 🟡 Starting (provisioning)
- 🔴 Error (failed to provision)
- ⚫ Stopped (teardown complete)

For Docker/Modal/Sprites/Fly Machines, show resource info:
- Container ID / Sandbox ID / Sprite name / Machine ID
- CPU/Memory usage (if available)
- Uptime

---

## Implementation Plan

### Phase 1: Backend Interface + Local Refactor

**Goal:** Extract current local checkout logic into a proper Backend interface.

**Changes:**
1. Create `internal/env/backend.go` with Backend interface
2. Create `internal/env/local.go` implementing LocalBackend
3. Refactor `internal/branch/branch.go` to use Backend
4. Update handlers to use Backend for terminal/file operations

**Acceptance Criteria:**
- [ ] Existing branch creation works exactly as before
- [ ] Existing terminal attach works exactly as before
- [ ] Existing file read/write in editor works exactly as before
- [ ] Backend interface is defined and LocalBackend implements it
- [ ] All existing tests pass

### Phase 2: Dotfiles Integration

**Goal:** Support dotfiles repos for environment configuration.

**Changes:**
1. Add `dotfiles` field to branch model
2. Add dotfiles input to branch creation form
3. Implement dotfiles setup logic:
   - Clone dotfiles repo to `/workspace/dotfiles`
   - If `flake.nix` exists, `nix build` and add to PATH
   - If no `flake.nix`, use cook's default package set
   - Symlink all config files to `$HOME`
4. Create cook's default packages flake (fallback when no flake.nix)

**Acceptance Criteria:**
- [ ] Can specify dotfiles repo when creating branch from web UI
- [ ] Dotfiles repo is cloned into environment
- [ ] Config files are symlinked to $HOME
- [ ] If flake.nix exists, packages are built and available
- [ ] If no flake.nix, default tools (git, ripgrep, neovim, etc.) are available
- [ ] Can edit dotfiles via editor tab, commit and push changes

### Phase 3: Docker Backend

**Goal:** Run branches in Docker containers via OrbStack.

**Changes:**
1. Create `internal/env/docker.go` implementing DockerBackend
2. Use Docker SDK (`github.com/docker/docker`) - already verified in spikes
3. Add "Docker" option to branch creation form
4. Implement PTY attach via Docker exec
5. Implement file operations (volume mount)
6. Integrate dotfiles setup in container

**Acceptance Criteria:**
- [ ] Can create branch with `backend=docker` from web UI
- [ ] Branch detail page shows Docker container status
- [ ] Terminal tabs work (can type commands, see output)
- [ ] Editor tabs work (can open, edit, save files)
- [ ] Abandoning branch stops and removes container
- [ ] Container uses nixos/nix base image
- [ ] Dotfiles are applied if specified

### Phase 4: Modal Backend

**Goal:** Run branches in Modal cloud sandboxes.

**Changes:**
1. Create `internal/env/modal.go` implementing ModalBackend
2. Port logic from `cook-legacy/cook.py` to Go
3. Add "Modal" option to branch creation form
4. Implement PTY attach via Modal's streaming API
5. Implement file operations via Modal volumes
6. Integrate dotfiles setup in sandbox
7. Handle agent auth injection (claude.json, codex config)

**Acceptance Criteria:**
- [ ] Can create branch with `backend=modal` from web UI
- [ ] Branch detail page shows Modal sandbox status
- [ ] Terminal tabs work
- [ ] Editor tabs work
- [ ] Dotfiles are applied if specified
- [ ] Agent auth is available in sandbox
- [ ] Abandoning branch terminates sandbox

### Phase 5: Polish & Reliability

**Goal:** Production-ready backend management.

**Changes:**
1. Backend health checks and auto-recovery
2. Proper error handling and user feedback
3. Resource cleanup on server restart
4. Logging and observability

**Acceptance Criteria:**
- [ ] Server restart reconnects to existing Docker containers
- [ ] Server restart reconnects to existing Modal sandboxes
- [ ] Clear error messages when backend operations fail
- [ ] Orphaned resources are cleaned up

---

## Data Model Changes

### branches table

Add columns:
```sql
ALTER TABLE branches ADD COLUMN backend_type TEXT DEFAULT 'local';
ALTER TABLE branches ADD COLUMN backend_id TEXT;  -- container ID, sandbox ID
ALTER TABLE branches ADD COLUMN dotfiles TEXT;    -- git URL for dotfiles repo
```

### Branch struct

```go
type Branch struct {
    // ... existing fields ...
    BackendType string  `json:"backend_type"` // "local", "docker", "modal"
    BackendID   string  `json:"backend_id"`   // container/sandbox ID
    Dotfiles    string  `json:"dotfiles"`     // e.g., "github.com/me/dotfiles"
}
```

---

## Backend Interface

```go
package env

import (
    "context"
    "io"
)

type Backend interface {
    // Setup provisions the environment
    Setup(ctx context.Context) error
    
    // Exec runs a command and returns combined output
    Exec(ctx context.Context, cmd string) ([]byte, error)
    
    // AttachPTY returns reader/writer for interactive terminal
    AttachPTY(ctx context.Context, rows, cols int) (io.ReadWriteCloser, error)
    
    // ResizePTY resizes the terminal
    ResizePTY(rows, cols int) error
    
    // ReadFile reads a file from the environment
    ReadFile(ctx context.Context, path string) ([]byte, error)
    
    // WriteFile writes a file to the environment
    WriteFile(ctx context.Context, path string, content []byte) error
    
    // ListFiles lists files in a directory
    ListFiles(ctx context.Context, dir string) ([]string, error)
    
    // WorkDir returns the working directory path
    WorkDir() string
    
    // Status returns backend health
    Status(ctx context.Context) (BackendStatus, error)
    
    // Teardown destroys the environment
    Teardown(ctx context.Context) error
}

type BackendStatus struct {
    State   string // "running", "starting", "stopped", "error"
    Message string // error message or status info
    ID      string // container ID, sandbox ID, etc.
}

type Config struct {
    Name       string            // unique name (typically branch name)
    RepoURL    string            // bare repo URL to clone from
    BranchName string            // git branch to checkout
    Dotfiles   string            // optional dotfiles repo git URL
    Secrets    map[string]string // agent auth, etc.
}
```

---

## Open Questions

1. **Git worktrees vs clones for local backend?**
   - Worktrees: faster, less disk space, git-native
   - Clones: simpler, fully isolated
   - Recommendation: Keep clones for now, consider worktrees later

2. **How to handle Modal auth?**
   - Need MODAL_TOKEN_ID and MODAL_TOKEN_SECRET
   - Store in server config or env vars
   - Don't expose to users

3. **Preview URLs for Docker/Modal?**
   - Docker: Port mapping (localhost:random -> container:port)
   - Modal: Modal provides tunnel URLs
   - Need to surface these in the UI

4. **Master branch / scratchpad use case?**
   - Could be a "persistent local backend" with ref=master
   - Or a special "workspace" concept separate from branches
   - Defer until backends are solid

5. **Default packages flake location?**
   - Ship with cook binary (embed)?
   - Separate repo (github.com/justinmoon/cook-defaults)?
   - Let user configure in cook.toml?

---

## Spike Reports

### Modal

Tailscale userspace networking spike: `docs/reference/spikes/04_tailscale_modal/`.
