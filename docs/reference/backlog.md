# Cook - Backlog & Future Ideas

Random notes and ideas that don't belong in the main spec yet.

## Server Restart / Session Persistence

- What happens to running agent sessions if factory server restarts?
- Remote backends (Modal, Fly) keep running independently - factory reconnects on startup
- Local backends might need restart logic
- Need to persist session state in DB so we can reconnect
- For v1: maybe just accept that local sessions die on restart, remote ones survive

## Branch/Worktree Lifecycle

- Currently: delete branches on merge (like Forge)
- Future: keep everything for complete audit trail
- Ideal: "fossilized" branches - merged branches and agent logs are archived, queryable, but not active
- Goal: complete time travel / replay capability. Given the DB state, you could theoretically replay an entire project's history (assuming deterministic LLM output, which is a big if)
- This means: never truly delete, just archive. Agent session logs, all commits, gate results, etc.

## Event System / Pub-Sub

**Implemented in v1 with NATS JetStream.**

Streams:
- `COOK_AGENTS` - agent session events
- `COOK_GATES` - gate execution events  
- `COOK_BRANCHES` - branch lifecycle events

Future extensions:
- Webhook consumers that subscribe to events
- Slack/Discord notification consumers
- Audit log consumer that persists all events
- Plugin system: external processes subscribe to events and publish responses
- Cross-instance event federation (multiple cook servers sharing events)

## Configuration.nix Location

- It's a property of Environment
- Can be a local path or a git repo URL
- Like Rust deps - can point to crates.io, git, or local path
- User's personal configs (dotfiles, editor, agents) live in a separate repo (e.g., ~/configs)
- Projects don't define configuration.nix - that's flake.nix territory

## Staging/Deployment as Gates

- Staging branch could be modeled as a branch with gates but no AI agent
- Gates: deploy, soak (wait for health), human approval
- Merge to master only after all staging gates pass
- Open question: does this model hold up? Or do we need a separate abstraction?

## Terminal Multiplexing

- v1: web UI with xterm.js
- Future: native integration with tmux/zellij/stoa
- The hard problem: remote sandboxes need terminal attach over network
- Stoa's SplitTree is a nice abstraction for tiling layouts - could port to web

## Gates as Functions (Advanced Model)

The v1 model has gates as validators (pass/fail on a rev). A more powerful model:

```
Gate: (GitRev, Environment, Config) -> Result<GitRev, Error>
```

Gates can transform the rev, not just validate:
- **Coding agent gate**: takes rev, produces new rev with changes
- **CI gate**: takes rev, returns same rev if passes, error if fails  
- **Auto-format gate**: takes rev, produces new rev with formatting fixes
- **Review gate**: takes rev, returns same rev if approved

This makes the coding agent "just another gate" in a pipeline. A branch becomes a declarative pipeline of transformations.

Benefits:
- Uniform model for all operations
- Gates can be retried, parallelized, made conditional
- Branch is purely declarative

Deferred because:
- Adds complexity
- Need to understand the problem space better first
- v1 model (agent + validator gates) is sufficient for now

## Persistent Environments

v1: environments are ephemeral, created per-branch, destroyed on merge/abandon

Future: long-lived environments that persist across branches
- Use case: expensive setup (large datasets, trained models)
- Use case: "my dev machine" as a persistent environment
- Environments could be named and reused
- Need lifecycle management (idle timeout, manual destroy)

## GitHub/GitLab Mirroring

- Push to GitHub as a mirror (for visibility, not source of truth)
- Import issues from GitHub
- Sync PR status back to GitHub

## Multi-Repo Tasks

- Task belongs to one repo
- Environment can mount multiple repos (e.g., app + shared library)
- Cross-repo dependencies: "task X in repo Y must be closed before this task"

## Gate Definition

- Goal: single .ts or .py file can define all gates for a project
- Simplest form: list of shell commands, exit 0 = pass
- More complex: structured gate definitions with types (ci, review, approval)
- Gates run inside the branch's environment (usually)

## Auto-Discovery via Tailscale

- v1: explicit server URL in config
- Future: cook instances advertise themselves on tailscale
- Client auto-discovers available servers
- Could use tailscale tags or MagicDNS

## Notifications / Alerting

- When agent completes, gate fails, review needed
- Desktop notifications (via web push or native)
- Slack/Discord webhooks
- Email digest

## Agent Session Recording / Replay

- Record all agent interactions (prompts, tool calls, outputs)
- Store in DB alongside branch
- Replay for debugging or training
- Export for analysis

## Cost Tracking

- Track compute costs per branch (Modal, Fly usage)
- Track LLM API costs per agent session
- Dashboard showing spend by repo/task/time

## Batch Mode / Scheduled Tasks

- Create tasks that run on a schedule
- "Every night, run this agent on the latest master"
- Use case: automated dependency updates, security scans

## Branch Templates

- Pre-defined branch configurations
- "Create a branch like my last one"
- Saved environment + gate configs
