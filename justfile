# First-time setup (run after clone)
setup:
    bun install
    bun run build:editor

# Build cook-agent for Linux (runs inside Docker containers)
build-agent:
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o cook-agent ./cmd/cook-agent

# Full build
build: setup build-agent
    go build ./cmd/cook

# Development server with auto-reload (default: no auth, no browser open)
dev auth="none" open="false": build-agent
    if [ "{{open}}" = "true" ]; then (sleep 1 && open http://localhost:7420) & fi
    COOK_AUTH={{auth}} air

# Development server without auto-reload
dev-no-reload auth="none" open="false":
    if [ "{{open}}" = "true" ]; then (sleep 1 && open http://localhost:7420) & fi
    COOK_AUTH={{auth}} go run ./cmd/cook serve

# Run E2E tests
test-e2e:
    npm run test:e2e

# Run E2E tests with UI
test-e2e-ui:
    npm run test:e2e:ui
