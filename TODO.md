# Docker Backend TODOs

## Nix Flake Image Building
- Use nix-built sandbox image for Docker backend
- Requires Linux builder for cross-compilation (macOS can't build Linux images)
- Options: remote builder, OrbStack integration, or pre-built images in registry

## Auth
- Currently extracts OAuth from macOS Keychain - won't work on Linux hosts
- Consider: environment variable injection, or mounted secrets file

## Container Improvements
- Add volume for nix store cache (faster rebuilds)
- Persist conversation history across container recreations
- Add health check for cook-agent process

## Session Management
- Detect and recover from cook-agent crashes
- Clean up stale sessions on reconnect
- Add session timeout/garbage collection

## UI
- Show container build progress in UI (not just server logs)
- Better error messages when agent connection fails

## Remote Backends (Modal / Sprites / Fly Machines)
- Modal: add sandbox create timeout + retry; avoid hanging /start requests
- Modal: install `cook-ts-up` in image or auto-install (no missing script)
- Modal/Fly: rebuild/push nix sandbox image with tailscale binaries (modal E2E fails: tailscaled missing)
- Fly: improve start stability (reuse existing machine, refresh state, handle “starting”); ensure machines don’t exit immediately
- Fly: auto-cleanup failed machines (or mark reuse-only in dev)
- Sprites: tar extraction warnings/renames should not fail if sandbox is usable
- Sprites: make tarball install robust (download + extract retries)
- Ensure `cook-ts-up` baked into nix image + sprites tarball

## E2E
- Make backend E2E reliable; reduce timeouts via async setup or polling
- Add cleanup for leaked sandboxes/machines after tests
