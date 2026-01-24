# Docker Backend TODOs

## Nix Flake Image Building
- Replace Dockerfile.env with nix flake in dotfiles
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
