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

# Development server with auto-reload (default: nostr auth, no browser open)
dev auth="nostr" open="false" expose="false": build-agent
    #!/usr/bin/env bash
    set -euo pipefail
    PORT="${COOK_PORT:-7420}"
    if [ "{{expose}}" = "true" ]; then
        SUBDOMAIN="cook-$(openssl rand -hex 4)"
        USER="u$(openssl rand -hex 3)"
        PASS="p$(openssl rand -hex 8)"
        export COOK_PUBLIC_URL="https://${USER}:${PASS}@${SUBDOMAIN}.justinmoon.com"
        echo "COOK_PUBLIC_URL=${COOK_PUBLIC_URL}"
        expose "$PORT" --subdomain "$SUBDOMAIN" --user "$USER" --pass "$PASS" &
        EXPOSE_PID=$!
        trap "kill $EXPOSE_PID 2>/dev/null || true" EXIT
    fi
    if [ "{{open}}" = "true" ]; then (sleep 1 && open "http://localhost:${PORT}") & fi
    COOK_AUTH={{auth}} air

# Development server without auto-reload
dev-no-reload auth="nostr" open="false" expose="false":
    #!/usr/bin/env bash
    set -euo pipefail
    PORT="${COOK_PORT:-7420}"
    if [ "{{expose}}" = "true" ]; then
        SUBDOMAIN="cook-$(openssl rand -hex 4)"
        USER="u$(openssl rand -hex 3)"
        PASS="p$(openssl rand -hex 8)"
        export COOK_PUBLIC_URL="https://${USER}:${PASS}@${SUBDOMAIN}.justinmoon.com"
        echo "COOK_PUBLIC_URL=${COOK_PUBLIC_URL}"
        expose "$PORT" --subdomain "$SUBDOMAIN" --user "$USER" --pass "$PASS" &
        EXPOSE_PID=$!
        trap "kill $EXPOSE_PID 2>/dev/null || true" EXIT
    fi
    if [ "{{open}}" = "true" ]; then (sleep 1 && open "http://localhost:${PORT}") & fi
    COOK_AUTH={{auth}} go run ./cmd/cook serve

# Run E2E tests
test-e2e:
    npm run test:e2e

# Run backend E2E tests (sprites, modal, fly-machines)
test-e2e-backends:
    npm run test:e2e -- e2e/sprites-backend.spec.ts e2e/modal-backend.spec.ts e2e/fly-machines-backend.spec.ts

# Run backend E2E tests individually
test-e2e-sprites:
    npm run test:e2e -- e2e/sprites-backend.spec.ts

test-e2e-modal:
    npm run test:e2e -- e2e/modal-backend.spec.ts

test-e2e-fly:
    npm run test:e2e -- e2e/fly-machines-backend.spec.ts

# Run E2E tests with UI
test-e2e-ui:
    npm run test:e2e:ui

# Tailnet integration tests (requires env vars, see docs)
test-tailnet:
    COOK_TAILNET_TEST=1 go test ./internal/env -run Integration -count=1

# All CI checks
pre-merge: build
    go test ./...
