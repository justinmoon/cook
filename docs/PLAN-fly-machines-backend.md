# Plan: Fly Machines Backend

## Overview

Fly Machines are fast-launching VMs on Fly.io that can be started/stopped at subsecond speeds. Unlike Sprites (which don't support custom images), Machines support any OCI/Docker image directly.

## Why Fly Machines

| Feature | Value |
|---------|-------|
| Cold start | <1 second |
| Custom images | Full OCI/Docker support |
| Go SDK | Official: `github.com/superfly/fly-go` |
| Session limits | None |
| Pricing | $0.02/hr for smallest (shared-cpu-1x) |
| Networking | Built-in private networking, public IPs |

## SDK: superfly/fly-go

Official Go client with full Machines API support:

```go
import (
    "github.com/superfly/fly-go"
    "github.com/superfly/fly-go/flaps"
)
```

**Key packages**:
- `fly` - Core types (Machine, MachineConfig, etc.)
- `flaps` - Machines REST API client

## API Overview

**Base URLs**:
- Public: `https://api.machines.dev`
- Internal: `http://_api.internal:4280` (within Fly network)

**Authentication**: `Authorization: Bearer <FLY_API_TOKEN>`

**Key Endpoints**:
```
POST   /v1/apps/{app}/machines           - Create machine
GET    /v1/apps/{app}/machines           - List machines
GET    /v1/apps/{app}/machines/{id}      - Get machine
POST   /v1/apps/{app}/machines/{id}/start  - Start machine
POST   /v1/apps/{app}/machines/{id}/stop   - Stop machine
DELETE /v1/apps/{app}/machines/{id}      - Destroy machine
POST   /v1/apps/{app}/machines/{id}/exec  - Execute command
```

## Implementation Plan

### Phase 1: Core Types

**File**: `internal/env/flymachines.go`

```go
import (
    "github.com/superfly/fly-go"
    "github.com/superfly/fly-go/flaps"
)

const (
    flyMachinesAppName = "cook-sandbox"
    flyAgentPort       = 7422
)

type FlyMachinesBackend struct {
    config      Config
    flapsClient *flaps.Client
    machine     *fly.Machine
    machineID   string
    workDir     string
    agentAddr   string  // Public URL for cook-agent
}
```

### Phase 2: Client Setup

```go
func NewFlyMachinesBackend(cfg Config) (*FlyMachinesBackend, error) {
    token := os.Getenv("FLY_API_TOKEN")
    if token == "" {
        return nil, fmt.Errorf("FLY_API_TOKEN not set")
    }

    ctx := context.Background()

    // Create flaps client
    flapsClient, err := flaps.NewWithOptions(ctx, flaps.NewClientOpts{
        AppName: flyMachinesAppName,
        Tokens:  tokens.Parse(token),
    })
    if err != nil {
        return nil, err
    }

    return &FlyMachinesBackend{
        config:      cfg,
        flapsClient: flapsClient,
        workDir:     "/workspace",
    }, nil
}
```

### Phase 3: Machine Configuration

```go
func (b *FlyMachinesBackend) buildMachineConfig() *fly.MachineConfig {
    return &fly.MachineConfig{
        // Use our pre-built OCI image
        Image: "ghcr.io/justinmoon/cook-sandbox:latest",

        // Resource allocation
        Guest: &fly.MachineGuest{
            CPUKind:  "shared",
            CPUs:     1,
            MemoryMB: 1024,  // 1GB
        },

        // Environment
        Env: map[string]string{
            "HOME":    "/root",
            "TERM":    "xterm-256color",
            "WORKDIR": b.workDir,
        },

        // Init command - keep machine running
        Init: fly.MachineInit{
            Cmd: []string{"sleep", "infinity"},
        },

        // Expose cook-agent port
        Services: []fly.MachineService{
            {
                Protocol:     "tcp",
                InternalPort: flyAgentPort,
                Ports: []fly.MachinePort{
                    {
                        Port:     fly.Pointer(flyAgentPort),
                        Handlers: []string{"tls"},
                    },
                },
            },
        },

        // Auto-destroy when stopped (optional)
        AutoDestroy: false,

        // Restart policy
        Restart: &fly.MachineRestart{
            Policy: fly.MachineRestartPolicyNo,
        },
    }
}
```

### Phase 4: Backend Interface Implementation

```go
func (b *FlyMachinesBackend) Setup(ctx context.Context) error {
    // 1. Launch machine
    config := b.buildMachineConfig()

    launchInput := fly.LaunchMachineInput{
        Config: config,
        Region: "ord",  // Chicago, or make configurable
        Name:   fmt.Sprintf("cook-%s", b.config.Name),
    }

    fmt.Printf("Creating Fly Machine...\n")
    machine, err := b.flapsClient.Launch(ctx, flyMachinesAppName, launchInput)
    if err != nil {
        return fmt.Errorf("failed to launch machine: %w", err)
    }
    b.machine = machine
    b.machineID = machine.ID
    fmt.Printf("Machine created: %s\n", b.machineID)

    // 2. Wait for machine to start
    err = b.flapsClient.Wait(ctx, flyMachinesAppName, machine, fly.MachineStateStarted, 60*time.Second)
    if err != nil {
        return fmt.Errorf("machine failed to start: %w", err)
    }

    // 3. Create workspace directory
    if _, err := b.Exec(ctx, "mkdir -p "+b.workDir); err != nil {
        return fmt.Errorf("failed to create workspace: %w", err)
    }

    // 4. Clone repo
    if err := b.cloneRepo(ctx); err != nil {
        return fmt.Errorf("failed to clone repo: %w", err)
    }

    // 5. Setup cook-agent
    if err := b.setupAgent(ctx); err != nil {
        return fmt.Errorf("failed to setup agent: %w", err)
    }

    // 6. Get public URL for agent
    b.agentAddr = fmt.Sprintf("https://%s.fly.dev:%d",
        flyMachinesAppName, flyAgentPort)

    return nil
}

func (b *FlyMachinesBackend) Exec(ctx context.Context, cmd string) ([]byte, error) {
    if b.machine == nil {
        return nil, fmt.Errorf("machine not initialized")
    }

    execReq := &fly.MachineExecRequest{
        Cmd:     fmt.Sprintf("sh -c %q", cmd),
        Timeout: 120,  // 2 minutes
    }

    resp, err := b.flapsClient.Exec(ctx, flyMachinesAppName, b.machineID, execReq)
    if err != nil {
        return nil, fmt.Errorf("exec failed: %w", err)
    }

    if resp.ExitCode != 0 {
        return nil, fmt.Errorf("command failed (exit %d): %s", resp.ExitCode, resp.StdErr)
    }

    return []byte(resp.StdOut), nil
}

func (b *FlyMachinesBackend) ReadFile(ctx context.Context, path string) ([]byte, error) {
    output, err := b.Exec(ctx, fmt.Sprintf("cat %q", path))
    if err != nil {
        return nil, err
    }
    return output, nil
}

func (b *FlyMachinesBackend) WriteFile(ctx context.Context, path string, content []byte) error {
    // Use base64 encoding to handle binary content
    encoded := base64.StdEncoding.EncodeToString(content)
    _, err := b.Exec(ctx, fmt.Sprintf("echo %q | base64 -d > %q", encoded, path))
    return err
}

func (b *FlyMachinesBackend) ListFiles(ctx context.Context, dir string) ([]FileInfo, error) {
    output, err := b.Exec(ctx, fmt.Sprintf("ls -la %q", dir))
    if err != nil {
        return nil, err
    }

    // Parse ls output (same as Modal backend)
    return parseLsOutput(output, dir), nil
}

func (b *FlyMachinesBackend) WorkDir() string {
    return b.workDir
}

func (b *FlyMachinesBackend) Status(ctx context.Context) (Status, error) {
    if b.machine == nil {
        return Status{State: StateStopped}, nil
    }

    machine, err := b.flapsClient.Get(ctx, flyMachinesAppName, b.machineID)
    if err != nil {
        return Status{State: StateError, Message: err.Error()}, nil
    }

    var state State
    switch machine.State {
    case fly.MachineStateStarted:
        state = StateRunning
    case fly.MachineStateStopped:
        state = StateStopped
    case fly.MachineStateCreated:
        state = StateStarting
    default:
        state = StateError
    }

    return Status{State: state, ID: b.machineID}, nil
}

func (b *FlyMachinesBackend) Teardown(ctx context.Context) error {
    if b.machine == nil {
        return nil
    }

    // Stop then destroy
    _ = b.flapsClient.Stop(ctx, flyMachinesAppName, fly.StopMachineInput{
        ID: b.machineID,
    }, "")

    return b.flapsClient.Destroy(ctx, flyMachinesAppName, fly.RemoveMachineInput{
        ID:   b.machineID,
        Kill: true,
    }, "")
}

func (b *FlyMachinesBackend) MachineID() string {
    return b.machineID
}

func (b *FlyMachinesBackend) AgentAddr() string {
    return b.agentAddr
}
```

### Phase 5: Reconnection Support

```go
func NewFlyMachinesBackendFromMachineID(machineID string) (*FlyMachinesBackend, error) {
    token := os.Getenv("FLY_API_TOKEN")
    if token == "" {
        return nil, fmt.Errorf("FLY_API_TOKEN not set")
    }

    ctx := context.Background()

    flapsClient, err := flaps.NewWithOptions(ctx, flaps.NewClientOpts{
        AppName: flyMachinesAppName,
        Tokens:  tokens.Parse(token),
    })
    if err != nil {
        return nil, err
    }

    // Get existing machine
    machine, err := flapsClient.Get(ctx, flyMachinesAppName, machineID)
    if err != nil {
        return nil, fmt.Errorf("failed to get machine %s: %w", machineID, err)
    }

    return &FlyMachinesBackend{
        flapsClient: flapsClient,
        machine:     machine,
        machineID:   machineID,
        workDir:     "/workspace",
        agentAddr:   fmt.Sprintf("https://%s.fly.dev:%d", flyMachinesAppName, flyAgentPort),
    }, nil
}
```

### Phase 6: Agent Setup

```go
func (b *FlyMachinesBackend) setupAgent(ctx context.Context) error {
    // Same pattern as Modal - base64 encode and transfer agent binary
    agentPath, err := findAgentBinary()
    if err != nil {
        return err
    }

    agentData, err := os.ReadFile(agentPath)
    if err != nil {
        return err
    }

    // Write in chunks via base64
    encoded := base64.StdEncoding.EncodeToString(agentData)
    chunkSize := 50000
    tmpPath := "/tmp/cook-agent.b64"

    b.Exec(ctx, fmt.Sprintf("rm -f %s", tmpPath))

    for i := 0; i < len(encoded); i += chunkSize {
        end := min(i+chunkSize, len(encoded))
        chunk := encoded[i:end]
        _, err := b.Exec(ctx, fmt.Sprintf("echo -n '%s' >> %s", chunk, tmpPath))
        if err != nil {
            return err
        }
    }

    // Decode and start
    _, err = b.Exec(ctx, fmt.Sprintf(
        "base64 -d %s > /tmp/cook-agent && chmod +x /tmp/cook-agent && rm %s",
        tmpPath, tmpPath))
    if err != nil {
        return err
    }

    _, err = b.Exec(ctx, "nohup /tmp/cook-agent > /tmp/cook-agent.log 2>&1 &")
    if err != nil {
        return err
    }

    // Wait for agent to be ready
    for i := 0; i < 10; i++ {
        output, _ := b.Exec(ctx, fmt.Sprintf("nc -z localhost %d && echo OK || echo FAIL", flyAgentPort))
        if strings.Contains(string(output), "OK") {
            fmt.Printf("cook-agent started on port %d\n", flyAgentPort)
            return nil
        }
        time.Sleep(500 * time.Millisecond)
    }

    logs, _ := b.Exec(ctx, "cat /tmp/cook-agent.log")
    return fmt.Errorf("agent failed to start: %s", logs)
}
```

### Phase 7: Factory Integration

**File**: `internal/env/factory.go`

```go
const TypeFlyMachines Type = "fly-machines"

func NewBackend(backendType Type, cfg Config) (Backend, error) {
    switch backendType {
    // ...
    case TypeFlyMachines:
        return NewFlyMachinesBackend(cfg)
    }
}
```

### Phase 8: Tests

**File**: `internal/env/flymachines_test.go`

```go
func TestFlyMachinesBackend_Integration(t *testing.T) {
    if os.Getenv("FLY_API_TOKEN") == "" {
        t.Skip("FLY_API_TOKEN not set")
    }

    ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
    defer cancel()

    cfg := Config{
        Name:       "test-fly-machines",
        RepoURL:    "https://github.com/octocat/Hello-World.git",
        BranchName: "master",
    }

    backend, err := NewFlyMachinesBackend(cfg)
    require.NoError(t, err)
    defer backend.Teardown(context.Background())

    require.NoError(t, backend.Setup(ctx))

    // Test Exec
    output, err := backend.Exec(ctx, "echo hello")
    require.NoError(t, err)
    require.Contains(t, string(output), "hello")

    // Test Status
    status, err := backend.Status(ctx)
    require.NoError(t, err)
    require.Equal(t, StateRunning, status.State)
}
```

## Volumes (Optional)

Fly Machines support persistent volumes:

```go
// Create volume
volume, err := flapsClient.CreateVolume(ctx, fly.CreateVolumeRequest{
    Name:   "workspace-data",
    Region: "ord",
    SizeGB: 10,
})

// Mount in machine config
config.Mounts = []fly.MachineMount{
    {
        Volume:    volume.ID,
        Path:      "/data",
        SizeGb:    10,
        Encrypted: true,
    },
}
```

## Files to Create/Modify

| File | Action |
|------|--------|
| `internal/env/flymachines.go` | Create - backend implementation |
| `internal/env/flymachines_test.go` | Create - unit/integration tests |
| `internal/env/factory.go` | Modify - add TypeFlyMachines |
| `internal/env/backend.go` | Modify - add type constant |
| `go.mod` | Modify - add github.com/superfly/fly-go |

## Dependencies

```go
require (
    github.com/superfly/fly-go v0.x.x
)
```

## Comparison: Fly Machines vs Modal vs Sprites

| Feature | Fly Machines | Modal | Sprites |
|---------|--------------|-------|---------|
| Cold start | <1s | ~5-10s | 1-12s (300ms from checkpoint) |
| Custom OCI | Yes | SDK only | No |
| Persistence | Volumes | No | Built-in 100GB |
| Checkpoint | No | No | Yes |
| Go SDK | Official | Official | Official |
| Session limit | None | 1 hour | None |

**Fly Machines is the fastest option for ephemeral workloads with custom images.**

## Open Questions

1. **Region selection** - Hardcode or make configurable?
2. **Resource sizing** - Default to shared-cpu-1x, allow override?
3. **Volume usage** - Mount persistent volume for workspace?
4. **Networking** - Use Flycast for private networking between services?

## References

- [Fly Machines Docs](https://fly.io/docs/machines/)
- [Fly Machines API](https://fly.io/docs/machines/api/)
- [fly-go GitHub](https://github.com/superfly/fly-go)
- [Machines API Reference](https://docs.machines.dev)
