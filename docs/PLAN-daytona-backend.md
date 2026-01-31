# Plan: Daytona Sandbox Backend

## Overview

Daytona is a secure infrastructure platform for running AI-generated code with sub-90ms cold starts. It has an official Go SDK with comprehensive features including PTY support, code interpreter, and file operations.

## Why Daytona

| Feature | Value |
|---------|-------|
| Cold start | <90ms (fastest available) |
| Custom images | Yes, via DockerImage builder or image string |
| Go SDK | Official: `github.com/daytonaio/daytona/libs/sdk-go` |
| PTY support | Native WebSocket-based PTY sessions |
| Code interpreter | Built-in Python interpreter with streaming |
| Volumes | Persistent volume support |
| Snapshots | Create and restore from snapshots |

## SDK: daytonaio/daytona

Official Go SDK with full feature support:

```go
import (
    "github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
    "github.com/daytonaio/daytona/libs/sdk-go/pkg/types"
    "github.com/daytonaio/daytona/libs/sdk-go/pkg/options"
)
```

**Key packages**:
- `daytona` - Client, Sandbox, services
- `types` - SandboxBaseParams, Resources, etc.
- `options` - Functional options for API calls

## API Overview

**Base URL**: `https://app.daytona.io/api`

**Authentication**: `Authorization: Bearer <DAYTONA_API_KEY>`

**Key Endpoints**:
```
POST   /sandbox              - Create sandbox
GET    /sandbox              - List sandboxes
GET    /sandbox/{id}         - Get sandbox
DELETE /sandbox/{id}         - Delete sandbox
POST   /sandbox/{id}/start   - Start sandbox
POST   /sandbox/{id}/stop    - Stop sandbox
POST   /sandbox/{id}/archive - Archive sandbox
```

**Toolbox API** (sandbox-internal):
```
POST   /process/execute      - Execute command
POST   /process/session      - Create session
WS     /process/pty          - PTY WebSocket
POST   /fs/upload            - Upload file
GET    /fs/download          - Download file
POST   /git/clone            - Clone repo
```

## Implementation Plan

### Phase 1: Core Types

**File**: `internal/env/daytona.go`

```go
import (
    "github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
    "github.com/daytonaio/daytona/libs/sdk-go/pkg/types"
    "github.com/daytonaio/daytona/libs/sdk-go/pkg/options"
)

const daytonaAgentPort = 7422

type DaytonaBackend struct {
    config    Config
    client    *daytona.Daytona
    sandbox   *daytona.Sandbox
    sandboxID string
    workDir   string
    agentAddr string
}
```

### Phase 2: Client Setup

```go
func NewDaytonaBackend(cfg Config) (*DaytonaBackend, error) {
    // Uses DAYTONA_API_KEY env var by default
    client, err := daytona.NewClient()
    if err != nil {
        return nil, fmt.Errorf("failed to create daytona client: %w", err)
    }

    return &DaytonaBackend{
        config:  cfg,
        client:  client,
        workDir: "/workspace",
    }, nil
}
```

### Phase 3: Sandbox Configuration

Daytona supports two creation modes:

#### Option A: From Docker Image String

```go
func (b *DaytonaBackend) createFromImage(ctx context.Context) error {
    params := types.ImageParams{
        SandboxBaseParams: types.SandboxBaseParams{
            Name:     b.config.Name,
            Language: types.CodeLanguagePython,
            EnvVars: map[string]string{
                "HOME":    "/root",
                "TERM":    "xterm-256color",
                "WORKDIR": b.workDir,
            },
            Labels: map[string]string{
                "app": "cook",
            },
        },
        Image: "ghcr.io/justinmoon/cook-sandbox:latest",
        Resources: &types.Resources{
            CPU:    2,
            Memory: 2048,  // 2GB
            Disk:   10,    // 10GB
        },
    }

    sandbox, buildLogs, err := b.client.Create(ctx, params,
        daytona.WithTimeout(2*time.Minute))
    if err != nil {
        return err
    }

    // Stream build logs
    go func() {
        for log := range buildLogs {
            fmt.Printf("[BUILD] %s\n", log)
        }
    }()

    b.sandbox = sandbox
    b.sandboxID = sandbox.ID
    return nil
}
```

#### Option B: From DockerImage Builder

```go
func (b *DaytonaBackend) createFromBuilder(ctx context.Context) error {
    // Build custom image using fluent API
    image := daytona.Base("node:20-slim").
        AptGet([]string{"git", "curl", "vim", "ripgrep"}).
        Run("npm install -g @anthropic-ai/claude-code").
        Workdir("/workspace").
        Env("HOME", "/root").
        Env("TERM", "xterm-256color")

    params := types.ImageParams{
        SandboxBaseParams: types.SandboxBaseParams{
            Name: b.config.Name,
        },
        Image: image,
        Resources: &types.Resources{
            CPU:    2,
            Memory: 2048,
        },
    }

    sandbox, buildLogs, err := b.client.Create(ctx, params)
    // ...
}
```

### Phase 4: Backend Interface Implementation

```go
func (b *DaytonaBackend) Setup(ctx context.Context) error {
    // 1. Create sandbox
    fmt.Printf("Creating Daytona sandbox...\n")
    if err := b.createFromImage(ctx); err != nil {
        return fmt.Errorf("failed to create sandbox: %w", err)
    }
    fmt.Printf("Sandbox created: %s\n", b.sandboxID)

    // 2. Wait for sandbox to start
    if err := b.sandbox.WaitForStart(ctx, 2*time.Minute); err != nil {
        return fmt.Errorf("sandbox failed to start: %w", err)
    }

    // 3. Clone repo using built-in Git service
    if err := b.cloneRepo(ctx); err != nil {
        return fmt.Errorf("failed to clone repo: %w", err)
    }

    // 4. Setup cook-agent
    if err := b.setupAgent(ctx); err != nil {
        return fmt.Errorf("failed to setup agent: %w", err)
    }

    // 5. Get preview URL for agent port
    url, err := b.sandbox.GetPreviewLink(ctx, daytonaAgentPort)
    if err != nil {
        return fmt.Errorf("failed to get agent URL: %w", err)
    }
    b.agentAddr = url

    return nil
}

func (b *DaytonaBackend) Exec(ctx context.Context, cmd string) ([]byte, error) {
    if b.sandbox == nil {
        return nil, fmt.Errorf("sandbox not initialized")
    }

    resp, err := b.sandbox.Process.ExecuteCommand(ctx, cmd,
        options.WithCwd(b.workDir),
        options.WithExecuteTimeout(2*time.Minute))
    if err != nil {
        return nil, err
    }

    if resp.ExitCode != 0 {
        return nil, fmt.Errorf("command failed (exit %d): %s", resp.ExitCode, resp.Result)
    }

    return []byte(resp.Result), nil
}

func (b *DaytonaBackend) ReadFile(ctx context.Context, path string) ([]byte, error) {
    return b.sandbox.FileSystem.DownloadFile(ctx, path, nil)
}

func (b *DaytonaBackend) WriteFile(ctx context.Context, path string, content []byte) error {
    return b.sandbox.FileSystem.UploadFile(ctx, content, path)
}

func (b *DaytonaBackend) ListFiles(ctx context.Context, dir string) ([]FileInfo, error) {
    files, err := b.sandbox.FileSystem.ListFiles(ctx, dir)
    if err != nil {
        return nil, err
    }

    var result []FileInfo
    for _, f := range files {
        result = append(result, FileInfo{
            Name:  f.Name,
            Path:  filepath.Join(dir, f.Name),
            IsDir: f.IsDirectory,
            Size:  f.Size,
        })
    }
    return result, nil
}

func (b *DaytonaBackend) WorkDir() string {
    return b.workDir
}

func (b *DaytonaBackend) Status(ctx context.Context) (Status, error) {
    if b.sandbox == nil {
        return Status{State: StateStopped}, nil
    }

    if err := b.sandbox.RefreshData(ctx); err != nil {
        return Status{State: StateError, Message: err.Error()}, nil
    }

    var state State
    switch b.sandbox.State {
    case "STARTED":
        state = StateRunning
    case "STOPPED":
        state = StateStopped
    case "PENDING_BUILD":
        state = StateStarting
    default:
        state = StateError
    }

    return Status{State: state, ID: b.sandboxID}, nil
}

func (b *DaytonaBackend) Teardown(ctx context.Context) error {
    if b.sandbox == nil {
        return nil
    }
    return b.sandbox.Delete(ctx)
}

func (b *DaytonaBackend) SandboxID() string {
    return b.sandboxID
}

func (b *DaytonaBackend) AgentAddr() string {
    return b.agentAddr
}
```

### Phase 5: Git Integration

Daytona has native Git support:

```go
func (b *DaytonaBackend) cloneRepo(ctx context.Context) error {
    return b.sandbox.Git.Clone(ctx,
        b.config.RepoURL,
        b.workDir,
        options.WithBranch(b.config.BranchName))
}
```

### Phase 6: PTY Support (Bonus)

Daytona has excellent native PTY support:

```go
// Optional: Implement PTYAttacher interface
func (b *DaytonaBackend) AttachPTY(ctx context.Context, rows, cols int) (io.ReadWriteCloser, error) {
    handle, err := b.sandbox.Process.CreatePty(ctx, "terminal",
        options.WithCreatePtySize(types.PtySize{Rows: rows, Cols: cols}),
        options.WithCreatePtyEnv(map[string]string{
            "TERM": "xterm-256color",
        }))
    if err != nil {
        return nil, err
    }

    if err := handle.WaitForConnection(ctx); err != nil {
        return nil, err
    }

    return &daytonaPTYWrapper{handle: handle}, nil
}

type daytonaPTYWrapper struct {
    handle *daytona.PtyHandle
}

func (w *daytonaPTYWrapper) Read(p []byte) (n int, err error) {
    select {
    case data := <-w.handle.DataChan():
        return copy(p, data), nil
    }
}

func (w *daytonaPTYWrapper) Write(p []byte) (n int, err error) {
    w.handle.SendInput(p)
    return len(p), nil
}

func (w *daytonaPTYWrapper) Close() error {
    w.handle.Disconnect()
    return nil
}
```

### Phase 7: Reconnection Support

```go
func NewDaytonaBackendFromSandboxID(sandboxID string) (*DaytonaBackend, error) {
    client, err := daytona.NewClient()
    if err != nil {
        return nil, err
    }

    ctx := context.Background()
    sandbox, err := client.Get(ctx, sandboxID)
    if err != nil {
        return nil, fmt.Errorf("failed to get sandbox %s: %w", sandboxID, err)
    }

    url, _ := sandbox.GetPreviewLink(ctx, daytonaAgentPort)

    return &DaytonaBackend{
        client:    client,
        sandbox:   sandbox,
        sandboxID: sandboxID,
        workDir:   "/workspace",
        agentAddr: url,
    }, nil
}
```

### Phase 8: Snapshot Support (Bonus)

Daytona supports snapshots for fast restores:

```go
// Create snapshot after setup
func (b *DaytonaBackend) CreateSnapshot(ctx context.Context, name string) (*types.Snapshot, error) {
    return b.client.Snapshot.Create(ctx, &types.CreateSnapshotParams{
        Name:  name,
        Image: b.sandbox.ID,  // Create from running sandbox
    })
}

// Create sandbox from snapshot
func (b *DaytonaBackend) createFromSnapshot(ctx context.Context, snapshotID string) error {
    params := types.SnapshotParams{
        SandboxBaseParams: types.SandboxBaseParams{
            Name: b.config.Name,
        },
        Snapshot: snapshotID,
    }

    sandbox, _, err := b.client.Create(ctx, params)
    if err != nil {
        return err
    }

    b.sandbox = sandbox
    b.sandboxID = sandbox.ID
    return nil
}
```

### Phase 9: Factory Integration

**File**: `internal/env/factory.go`

```go
const TypeDaytona Type = "daytona"

func NewBackend(backendType Type, cfg Config) (Backend, error) {
    switch backendType {
    // ...
    case TypeDaytona:
        return NewDaytonaBackend(cfg)
    }
}
```

### Phase 10: Tests

**File**: `internal/env/daytona_test.go`

```go
func TestDaytonaBackend_Integration(t *testing.T) {
    if os.Getenv("DAYTONA_API_KEY") == "" {
        t.Skip("DAYTONA_API_KEY not set")
    }

    ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
    defer cancel()

    cfg := Config{
        Name:       "test-daytona",
        RepoURL:    "https://github.com/octocat/Hello-World.git",
        BranchName: "master",
    }

    backend, err := NewDaytonaBackend(cfg)
    require.NoError(t, err)
    defer backend.Teardown(context.Background())

    require.NoError(t, backend.Setup(ctx))

    // Test Exec
    output, err := backend.Exec(ctx, "echo hello")
    require.NoError(t, err)
    require.Contains(t, string(output), "hello")

    // Test file operations
    require.NoError(t, backend.WriteFile(ctx, "/tmp/test.txt", []byte("content")))
    data, err := backend.ReadFile(ctx, "/tmp/test.txt")
    require.NoError(t, err)
    require.Equal(t, "content", string(data))

    // Test ListFiles
    files, err := backend.ListFiles(ctx, "/tmp")
    require.NoError(t, err)
    require.NotEmpty(t, files)

    // Test Status
    status, err := backend.Status(ctx)
    require.NoError(t, err)
    require.Equal(t, StateRunning, status.State)
}
```

## Files to Create/Modify

| File | Action |
|------|--------|
| `internal/env/daytona.go` | Create - backend implementation |
| `internal/env/daytona_test.go` | Create - unit/integration tests |
| `internal/env/factory.go` | Modify - add TypeDaytona |
| `internal/env/backend.go` | Modify - add type constant |
| `go.mod` | Modify - add daytona SDK |

## Dependencies

```go
require (
    github.com/daytonaio/daytona/libs/sdk-go v0.x.x
)
```

## Comparison: Daytona vs Others

| Feature | Daytona | Modal | Fly Machines | E2B | Sprites |
|---------|---------|-------|--------------|-----|---------|
| Cold start | **<90ms** | ~5-10s | <1s | ~200ms | 1-12s |
| Go SDK | **Official** | Official | Official | None | Official |
| Custom images | **Yes** | SDK only | Yes | Docker | No |
| Native PTY | **Yes** | No | No | Limited | Yes |
| Git service | **Yes** | No | No | No | No |
| Snapshots | **Yes** | No | No | Yes (pause) | Yes |
| Code interpreter | **Yes** | No | No | Yes | No |

**Daytona is the best choice for fast starts and rich SDK features.**

## Unique Daytona Features

1. **Fastest cold start** (<90ms) - best for ephemeral workloads
2. **Native Git service** - no need to shell out to git
3. **Built-in PTY** - WebSocket-based terminal without cook-agent
4. **Code interpreter** - Python execution with matplotlib chart support
5. **DockerImage builder** - fluent API for image construction
6. **Snapshots** - save and restore sandbox state

## Open Questions

1. **Use native PTY?** - Could bypass cook-agent for terminal, but would need to adapt our architecture
2. **Snapshot strategy** - Pre-build snapshot with tools for faster starts?
3. **Code interpreter** - Useful for data science use cases?
4. **Volume usage** - Mount persistent volume for workspace data?

## References

- [Daytona Documentation](https://www.daytona.io/docs/en/)
- [Daytona GitHub](https://github.com/daytonaio/daytona)
- [Go SDK Source](https://github.com/daytonaio/daytona/tree/main/libs/sdk-go)
- [Daytona API Spec](https://github.com/daytonaio/daytona/tree/main/libs/api-client-go)
