# Cook Implementation Plan

Step-by-step plan to implement cook v1. Each step should be completable in a focused session.

## Spike Results (2026-01-20)

All spikes passed. Code in `docs/reference/spikes/` directory.

1. **PTY to WebSocket to xterm.js** - Works. Uses `github.com/creack/pty` and `github.com/gorilla/websocket`.
2. **Docker PTY via OrbStack** - Works. Uses `github.com/docker/docker` SDK v27. OrbStack provides Docker API at standard socket.
3. **NATS JetStream** - Works. Uses `github.com/nats-io/nats.go`. Stream creation, publish, consume all work as expected.

### Key Libraries Verified
- `github.com/creack/pty` - PTY management
- `github.com/gorilla/websocket` - WebSocket server
- `github.com/docker/docker` v27 - Docker API client
- `github.com/nats-io/nats.go` - NATS client with JetStream

### Notes for Implementation
- Docker SDK pulls in many transitive deps (otel, grpc) - acceptable but adds to binary size
- NATS server should run externally during dev (via `nats-server -js`), not embedded
- xterm.js v5 works fine via CDN, can bundle later if needed

## Phase 1: Foundation

### 1.1 Project Setup ✅
- Initialize Go module (`go mod init github.com/justinmoon/cook`)
- Set up directory structure: `cmd/cook/`, `internal/`, `pkg/`
- Create flake.nix with all deps (Go, NATS, SQLite, etc.)
- Add .envrc for direnv integration
- Basic CLI skeleton with cobra or similar
- `cook version` command works

**Acceptance:** `nix develop`, then `go build ./cmd/cook && ./cook version` prints version

**Done:** 2026-01-20

### 1.2 Configuration & Database ✅
- Define config structs (server config, client config)
- Load config from `~/.config/cook/config.toml` and `/etc/cook/config.toml`
- SQLite setup with migrations
- Create tables: tasks, branches, gate_runs, agent_sessions
- Basic connection pooling

**Acceptance:** `cook serve` starts, creates DB file, runs migrations, exits cleanly on SIGINT

**Done:** 2026-01-20

### 1.3 Repository Management ✅
- Bare repo storage in `data_dir/repos/*.git`
- `cook repo list` - enumerate repos
- `cook repo add <name>` - create bare repo
- `cook repo add <name> --clone=<url>` - clone from remote
- `cook repo remove <name>` - delete (with confirmation)

**Acceptance:** Can create, list, and remove repos. Repos persist across restarts.

**Done:** 2026-01-20

## Phase 2: Tasks

### 2.1 Task CRUD ✅
- Task model with YAML frontmatter parsing
- `cook task create <repo> <id> --title="..." [--priority=N] [--body="..."]`
- `cook task list [--repo=X] [--status=X]`
- `cook task show <id>`
- `cook task close <id>`
- Store in SQLite with timestamps

**Acceptance:** Full task lifecycle works. Tasks queryable by repo/status.

**Done:** 2026-01-20

### 2.2 Task Dependencies ✅
- `depends_on` field in task model
- Validation: can't start task if dependencies not closed
- `cook task list` shows blocked tasks differently

**Acceptance:** Task with open dependency shows as blocked

**Done:** 2026-01-20

## Phase 3: Branches (Local Backend Only)

### 3.1 Branch Creation - Local Path ✅
- Branch model in DB
- `cook branch create <repo> <name> --env=local:/path`
- Creates git branch from master
- Clones to specified path
- Records in DB

**Acceptance:** Branch created, files on disk, recorded in DB

**Done:** 2026-01-20

### 3.2 Branch Lifecycle ✅
- `cook branch list [--repo=X]`
- `cook branch show <name>` - shows status, path, linked task
- `cook branch abandon <name>` - deletes branch and checkout
- Link branch to task on creation (`--task=X`)

**Acceptance:** Can create, list, show, and abandon branches. Task status updates.

**Done:** 2026-01-20

### 3.3 Basic Merge ✅
- `cook branch merge <name>`
- Validates: branch exists, no uncommitted changes
- Fast-forward merge to master (or merge commit if needed)
- Deletes branch and checkout
- Updates linked task to closed

**Acceptance:** Clean branch merges to master, cleanup happens

**Done:** 2026-01-20

## Phase 4: Gates

### 4.1 Gate Configuration ✅
- Parse `cook.toml` from repo root for default gates
- Gate model in DB (gate_runs table)
- `cook gate run <branch>` - runs all gates in sequence

**Acceptance:** Gates defined in cook.toml run on command

**Done:** 2026-01-20

### 4.2 Command Gates ✅
- Execute shell command in branch's checkout
- Capture stdout/stderr to log file
- Record pass/fail, timing in DB
- `cook gate status <branch>` displays gate status

**Acceptance:** `just pre-merge` gate runs, logs captured, status shown

**Done:** 2026-01-20

### 4.3 Gate-Gated Merge ✅
- `cook branch merge` refuses if gates haven't passed
- `cook branch merge --skip-gates` to override
- Re-run gates if head changed since last run

**Acceptance:** Can't merge with failed/missing gates unless forced

**Done:** 2026-01-20

## Phase 5: HTTP Server & Web UI (Datastar) ✅

### 5.1 HTTP Server Skeleton ✅
- Chi router with middleware (logging, recovery, timeout)
- Health endpoint `/health`
- Static file serving
- HTML template rendering with embedded files

**Acceptance:** Server starts, health returns 200

**Done:** 2026-01-20

### 5.2 Datastar Setup ✅
- Datastar via CDN
- Pico CSS for styling
- Base template with nav

**Acceptance:** Pages load with Datastar and styling

**Done:** 2026-01-20

### 5.3 Task Web UI ✅
- Task list (`/tasks`) with filtering
- Task detail (`/tasks/:id`)

**Acceptance:** Can view tasks in browser

**Done:** 2026-01-20

### 5.4 Branch Web UI ✅
- Branch list (`/branches`)
- Branch detail with gate runs

**Acceptance:** Can view branches and gate status

**Done:** 2026-01-20

### 5.5 Repo Web UI ✅
- Repo list (`/repos`)
- Repo detail with branches and tasks

**Acceptance:** Can browse repos

**Done:** 2026-01-20

## Phase 6: NATS Event System ✅

### 6.1 NATS Setup ✅
- Optional NATS connection via COOK_NATS_URL env var
- JetStream streams for COOK_BRANCHES, COOK_GATES, COOK_AGENTS, COOK_TASKS
- Subject hierarchy: `cook.<type>.<id>.<event>`

**Acceptance:** Server works with or without NATS

**Done:** 2026-01-20

### 6.2 Event Publishing ✅
- Event types for branch/gate/agent/task lifecycle
- Publish helper functions in events package
- CLI actions publish events (branch create/merge/abandon, task create/close, gate run)

**Acceptance:** Events can be published to NATS

**Done:** 2026-01-20

### 6.3 SSE Bridge ✅
- `/events` endpoint for all events
- `/events/{branch}` for branch-specific events
- Graceful fallback when NATS not configured

**Acceptance:** SSE endpoints work when NATS available

**Done:** 2026-01-20

## Phase 7: Agent Integration ✅

### 7.1 Agent Session Model ✅
- agent_sessions table with pid, exit_code, status
- Track: agent type, status, branch, started_at, ended_at
- `cook branch create ... --agent=claude --prompt="..."`

**Acceptance:** Agent session recorded in DB on branch creation

**Done:** 2026-01-20

### 7.2 Agent Spawning - Local ✅
- Spawn agent process (claude/codex/opencode) in branch checkout
- Pass prompt via CLI args
- Connect stdin/stdout for interactive use

**Acceptance:** `cook branch create ... --agent=claude` spawns Claude

**Done:** 2026-01-20

### 7.3 Agent Control Commands
- `cook done` - marks task done, triggers gates (run from inside env)
- `cook ask-for-help "msg"` - pauses, marks needs_human
- (Future: inject `cook` binary into environment PATH)

**Acceptance:** Agent can call `cook done`, task closes, gates run

### 7.4 Agent Session Management ✅
- `cook agent list` - shows all sessions
- `cook agent show <id>` - session details
- `cook agent kill <id>` - kill running agent
- Track agent process status (running, exited)

**Acceptance:** Agent exit detected, status updated in DB

**Done:** 2026-01-20

## Phase 8: Terminal Attach (Web) ✅

### 8.1 PTY Management ✅
- terminal.PTY wrapper around creack/pty
- terminal.Manager for session lifecycle

**Acceptance:** PTY creation and management works

**Done:** 2026-01-20

### 8.2 WebSocket Terminal ✅
- WebSocket endpoint `/ws/terminal/{branch}`
- Bidirectional: WS→pty input, pty→WS output
- Resize support
- PTY created automatically on WebSocket connect

**Acceptance:** Can connect to WebSocket, see output

**Done:** 2026-01-20

### 8.3 xterm.js Integration ✅
- xterm.js terminal page at `/terminal/{branch}`
- Terminal link on branch detail page
- Full terminal emulation with themes

**Acceptance:** Browser shows live terminal

**Done:** 2026-01-20

## Phase 9: Docker Backend (Deferred)

Docker/container support deferred to future sprint. Sprint 1 uses local checkouts only.

### 9.1 Docker Backend Implementation (not started)
- Backend interface for execution environments
- Docker backend (container with mounted repo)

### 9.2 Docker Agent Integration (not started)
- Wire docker backend into branch create --env=docker:image
- Terminal attach through docker exec

**Acceptance:** Full agent workflow in Docker container

---

## Sprint 1 Complete

Phases 1-8 represent a usable tool. After completing these, cook can:
- Manage git repos, tasks, branches
- Run gates (CI) on branches
- Spawn coding agents (local)
- Show real-time status in web UI
- Attach to agent terminals from browser
- Real-time event streaming via NATS/SSE

---

# Future Work (Sprint 2+)

The following are moved to backlog for future sprints.

## Remote/Modal Backend

**Prior art:** `~/code/cook-legacy/cook.py` - Python prototype with Modal sandbox setup, nix config activation, agent auth injection patterns.

### Modal Backend
- Modal SDK integration
- `cook branch create ... --env=modal`
- Create sandbox, clone repo, setup environment
- Exec commands via Modal API

### Remote Host Backend
- `cook branch create ... --env=remote:hostname`
- SSH to remote cook instance
- Delegate branch creation to remote
- Proxy terminal attach via NATS

## Configuration Injection
- `--config=<git-ref>` for nix/home-manager config
- Clone config repo, run activation in environment
- Works for Docker and Modal backends

## Human Approval Gates
- `human_approval` gate type
- Web UI shows pending approvals
- Approve/reject buttons
- Publish approval events to NATS

## Agent Review Gates
- `agent_review` gate type
- Spawn review agent with diff as context
- Pass/fail based on agent output

## Polish
- Structured logging throughout
- Graceful error handling
- Clear error messages in CLI and UI
- README with quickstart
- CLI help text complete
- Example cook.toml

---

## Sprint 1 Milestones

**M1 - Local CLI (Phases 1-4):** Manage repos, tasks, branches, gates via CLI. Local path backend only.

**M2 - Web UI + Events (Phases 5-6):** Datastar web UI with NATS. Real-time updates.

**M3 - Agents (Phases 7-8):** Full agent workflow with terminal attach in browser.

**M4 - Docker (Phase 9):** Run branches in Docker via OrbStack.
