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

# Development server with auto-reload
dev: build-agent
    (sleep 1 && open http://localhost:7420) &
    air

# Development server without auto-reload
dev-no-reload:
    (sleep 1 && open http://localhost:7420) &
    COOK_AUTH=nostr go run ./cmd/cook serve

# Run E2E tests
test-e2e:
    npm run test:e2e

# Run E2E tests with UI
test-e2e-ui:
    npm run test:e2e:ui
