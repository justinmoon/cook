# Plan: E2B Sandbox Backend

## Overview

E2B (Environment to Binary) is an open-source platform for running AI-generated code in secure Firecracker microVMs. It's the most widely adopted sandbox for AI coding agents (used by Claude, Cursor, etc.).

## Why E2B

| Feature | Value |
|---------|-------|
| Cold start | ~150-200ms (Firecracker microVMs) |
| Isolation | Hardware-level via Firecracker (same as AWS Lambda) |
| Custom images | Yes, via Docker templates |
| Max session | 24 hours (Pro), 1 hour (Base) |
| Open source | Yes - can self-host |
| Pricing | Pay-per-use, ~$0.10/hour |

## SDK Status

- **Official SDKs**: Python, TypeScript/JavaScript only
- **No official Go SDK** - must implement from scratch or use third-party
- **Third-party Go**: `github.com/conneroisu/groq-go/extensions/e2b` (limited)

## API Architecture

E2B uses two APIs:

### 1. Control Plane REST API (`api.e2b.app`)

Sandbox lifecycle management:

```
POST   /sandboxes                    - Create sandbox
GET    /sandboxes                    - List sandboxes
GET    /sandboxes/{id}               - Get sandbox details
DELETE /sandboxes/{id}               - Kill sandbox
POST   /sandboxes/{id}/timeout       - Set timeout
POST   /sandboxes/{id}/pause         - Pause sandbox
POST   /sandboxes/{id}/connect       - Connect to existing
POST   /sandboxes/{id}/refreshes     - Refresh TTL
GET    /sandboxes/{id}/metrics       - Get resource usage
GET    /sandboxes/{id}/logs          - Get logs
```

**Authentication**: `X-API-Key` header

**Create Request**:
```json
{
  "templateID": "base",
  "timeout": 300,
  "envVars": {"KEY": "value"},
  "metadata": {"user": "123"},
  "network": {"egressCIDRs": ["0.0.0.0/0"]}
}
```

**Response**:
```json
{
  "sandboxID": "abc123",
  "templateID": "base",
  "clientID": "xyz",
  "envdVersion": "0.1.0",
  "envdAccessToken": "token",
  "domain": "e2b.app"
}
```

### 2. Sandbox Internal API (envd)

Two protocols for sandbox operations:

#### A. Connect RPC (gRPC-compatible over HTTP/2)

**Process Service** - Command execution:
```protobuf
service Process {
    rpc Start(StartRequest) returns (stream StartResponse);
    rpc Connect(ConnectRequest) returns (stream ConnectResponse);
    rpc SendInput(SendInputRequest) returns (SendInputResponse);
    rpc SendSignal(SendSignalRequest) returns (SendSignalResponse);
    rpc List(ListRequest) returns (ListResponse);
}
```

**Filesystem Service** - File operations:
```protobuf
service Filesystem {
    rpc Stat(StatRequest) returns (StatResponse);
    rpc MakeDir(MakeDirRequest) returns (MakeDirResponse);
    rpc ListDir(ListDirRequest) returns (ListDirResponse);
    rpc Remove(RemoveRequest) returns (RemoveResponse);
    rpc Move(MoveRequest) returns (MoveResponse);
    rpc WatchDir(WatchDirRequest) returns (stream WatchDirResponse);
}
```

#### B. HTTP REST (file uploads/downloads)

```
GET  /files?path=/app/file.txt     - Download file
POST /files?path=/app/file.txt     - Upload file (multipart)
GET  /health                        - Health check
GET  /metrics                       - Resource metrics
```

## Implementation Plan

### Phase 1: Core Types

**File**: `internal/env/e2b.go`

```go
type E2BBackend struct {
    config       Config
    apiKey       string
    sandboxID    string
    clientID     string
    envdToken    string
    domain       string
    httpClient   *http.Client
    rpcClient    *connect.Client  // connectrpc/connect-go
    workDir      string
}

type E2BSandboxInfo struct {
    SandboxID   string
    TemplateID  string
    ClientID    string
    EnvdVersion string
    EnvdToken   string
    Domain      string
    StartedAt   time.Time
    State       string  // "running" | "paused"
}
```

### Phase 2: Control Plane Client

```go
const e2bAPIBase = "https://api.e2b.app"

func (b *E2BBackend) createSandbox(ctx context.Context) (*E2BSandboxInfo, error) {
    req := map[string]any{
        "templateID": b.config.Template,  // or "base"
        "timeout":    300,
        "envVars":    b.config.Secrets,
        "metadata":   map[string]string{"name": b.config.Name},
    }

    resp, err := b.doRequest(ctx, "POST", "/sandboxes", req)
    // Parse response into E2BSandboxInfo
}

func (b *E2BBackend) killSandbox(ctx context.Context) error {
    _, err := b.doRequest(ctx, "DELETE", "/sandboxes/"+b.sandboxID, nil)
    return err
}

func (b *E2BBackend) setTimeout(ctx context.Context, seconds int) error {
    req := map[string]int{"timeout": seconds}
    _, err := b.doRequest(ctx, "POST", "/sandboxes/"+b.sandboxID+"/timeout", req)
    return err
}
```

### Phase 3: Connect RPC Client

Using `connectrpc/connect-go` for gRPC-like communication:

```go
import (
    "connectrpc.com/connect"
    processv1 "github.com/example/e2b/gen/process/v1"
    filesystemv1 "github.com/example/e2b/gen/filesystem/v1"
)

func (b *E2BBackend) initRPCClient() error {
    baseURL := fmt.Sprintf("https://%s-%s.%s", b.envdPort, b.sandboxID, b.domain)

    b.processClient = processv1connect.NewProcessServiceClient(
        b.httpClient,
        baseURL,
        connect.WithInterceptors(b.authInterceptor()),
    )

    b.filesystemClient = filesystemv1connect.NewFilesystemServiceClient(
        b.httpClient,
        baseURL,
    )
    return nil
}
```

### Phase 4: Backend Interface Implementation

```go
func (b *E2BBackend) Setup(ctx context.Context) error {
    // 1. Create sandbox via REST API
    info, err := b.createSandbox(ctx)
    if err != nil {
        return err
    }
    b.sandboxID = info.SandboxID
    b.envdToken = info.EnvdToken

    // 2. Initialize RPC client
    if err := b.initRPCClient(); err != nil {
        return err
    }

    // 3. Clone repo
    if err := b.cloneRepo(ctx); err != nil {
        return err
    }

    // 4. Setup cook-agent
    if err := b.setupAgent(ctx); err != nil {
        return err
    }

    return nil
}

func (b *E2BBackend) Exec(ctx context.Context, cmd string) ([]byte, error) {
    req := &processv1.StartRequest{
        Config: &processv1.ProcessConfig{
            Cmd:  "sh",
            Args: []string{"-c", cmd},
            Cwd:  b.workDir,
        },
    }

    stream, err := b.processClient.Start(ctx, connect.NewRequest(req))
    if err != nil {
        return nil, err
    }

    var stdout, stderr bytes.Buffer
    for stream.Receive() {
        msg := stream.Msg()
        if data := msg.GetData(); data != nil {
            if data.Stdout != nil {
                stdout.Write(data.Stdout)
            }
            if data.Stderr != nil {
                stderr.Write(data.Stderr)
            }
        }
        if end := msg.GetEnd(); end != nil {
            if end.ExitCode != 0 {
                return nil, fmt.Errorf("exit code %d: %s", end.ExitCode, stderr.String())
            }
            break
        }
    }

    return stdout.Bytes(), stream.Err()
}

func (b *E2BBackend) ReadFile(ctx context.Context, path string) ([]byte, error) {
    // Use HTTP endpoint for file downloads
    url := fmt.Sprintf("https://%s-%s.%s/files?path=%s",
        b.envdPort, b.sandboxID, b.domain, url.QueryEscape(path))

    req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
    req.Header.Set("Authorization", "Bearer "+b.envdToken)

    resp, err := b.httpClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    return io.ReadAll(resp.Body)
}

func (b *E2BBackend) WriteFile(ctx context.Context, path string, content []byte) error {
    // Use HTTP endpoint with multipart upload
    url := fmt.Sprintf("https://%s-%s.%s/files?path=%s",
        b.envdPort, b.sandboxID, b.domain, url.QueryEscape(path))

    body := &bytes.Buffer{}
    writer := multipart.NewWriter(body)
    part, _ := writer.CreateFormFile("file", filepath.Base(path))
    part.Write(content)
    writer.Close()

    req, _ := http.NewRequestWithContext(ctx, "POST", url, body)
    req.Header.Set("Content-Type", writer.FormDataContentType())
    req.Header.Set("Authorization", "Bearer "+b.envdToken)

    resp, err := b.httpClient.Do(req)
    if err != nil {
        return err
    }
    resp.Body.Close()
    return nil
}

func (b *E2BBackend) ListFiles(ctx context.Context, dir string) ([]FileInfo, error) {
    req := &filesystemv1.ListDirRequest{Path: dir}
    resp, err := b.filesystemClient.ListDir(ctx, connect.NewRequest(req))
    if err != nil {
        return nil, err
    }

    var files []FileInfo
    for _, entry := range resp.Msg.Entries {
        files = append(files, FileInfo{
            Name:  entry.Name,
            Path:  filepath.Join(dir, entry.Name),
            IsDir: entry.Type == filesystemv1.FileType_DIRECTORY,
        })
    }
    return files, nil
}

func (b *E2BBackend) Teardown(ctx context.Context) error {
    return b.killSandbox(ctx)
}
```

### Phase 5: Custom Template Support

E2B allows custom Docker templates:

```go
// Template creation (done once, stored in E2B)
type E2BTemplate struct {
    ID         string
    Dockerfile string
}

// Use template when creating sandbox
func (b *E2BBackend) Setup(ctx context.Context) error {
    templateID := "base"  // or custom template ID
    if b.config.E2BTemplate != "" {
        templateID = b.config.E2BTemplate
    }
    // Create sandbox with template
}
```

**Template Dockerfile** (similar to our sandbox-image):
```dockerfile
FROM e2b/base:latest
RUN apt-get update && apt-get install -y git curl nodejs npm ripgrep
RUN npm install -g @anthropic-ai/claude-code
```

### Phase 6: Factory Integration

**File**: `internal/env/factory.go`

```go
const TypeE2B Type = "e2b"

func NewBackend(backendType Type, cfg Config) (Backend, error) {
    switch backendType {
    // ...
    case TypeE2B:
        return NewE2BBackend(cfg)
    }
}

func NewE2BBackendFromSandboxID(sandboxID string) (*E2BBackend, error) {
    // Reconnect to existing sandbox
    // POST /sandboxes/{id}/connect
}
```

### Phase 7: Tests

**File**: `internal/env/e2b_test.go`

```go
func TestE2BBackend_Integration(t *testing.T) {
    if os.Getenv("E2B_API_KEY") == "" {
        t.Skip("E2B_API_KEY not set")
    }

    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
    defer cancel()

    cfg := Config{
        Name:       "test-e2b",
        RepoURL:    "https://github.com/octocat/Hello-World.git",
        BranchName: "master",
    }

    backend, err := NewE2BBackend(cfg)
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
}
```

## Files to Create/Modify

| File | Action |
|------|--------|
| `internal/env/e2b.go` | Create - backend implementation |
| `internal/env/e2b_test.go` | Create - unit/integration tests |
| `internal/env/factory.go` | Modify - add TypeE2B |
| `internal/env/backend.go` | Modify - add type constant |
| `go.mod` | Modify - add connectrpc/connect-go |

## Dependencies

```go
require (
    connectrpc.com/connect v1.x.x
    google.golang.org/protobuf v1.x.x
)
```

## Protobuf Generation

Need to generate Go code from E2B's proto files:

```bash
# Clone E2B spec
git clone https://github.com/e2b-dev/E2B /tmp/e2b

# Generate Go code
protoc --go_out=. --connect-go_out=. \
    /tmp/e2b/spec/envd/process/process.proto \
    /tmp/e2b/spec/envd/filesystem/filesystem.proto
```

## Open Questions

1. **Template management** - Build custom template with our tools, or install at runtime?
2. **24-hour limit** - Acceptable for our use case? Need auto-refresh?
3. **Self-hosting** - Worth running our own E2B infrastructure?
4. **Pause/resume** - Use pause feature to reduce costs during inactivity?

## References

- [E2B Documentation](https://e2b.dev/docs)
- [E2B GitHub](https://github.com/e2b-dev/E2B)
- [E2B OpenAPI Spec](https://github.com/e2b-dev/E2B/blob/main/spec/openapi.yml)
- [E2B Proto Definitions](https://github.com/e2b-dev/E2B/tree/main/spec/envd)
- [Connect RPC Go](https://connectrpc.com/docs/go/getting-started)
