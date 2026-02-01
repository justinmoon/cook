# Spike 4: Tailscale inside Modal Sandbox (userspace)

Goal: confirm Tailscale can run inside a Modal sandbox without `/dev/net/tun` by using userspace networking.

## Run

Required env:
- `MODAL_TOKEN_ID`, `MODAL_TOKEN_SECRET` (Modal creds)
- `TS_AUTHKEY` (Tailscale auth key; ephemeral recommended)

Optional env:
- `COOK_TAIL_IP` (tailnet IP to ping, e.g. your dev host)
- `COOK_HTTP_URL` (URL to fetch over tailnet, e.g. `http://100.x.y.z:7420/health`)
- `COOK_GIT_URL` (git remote URL reachable over tailnet)
- `TS_HOSTNAME` (override; default `modal-<sandboxid>`)
- `KEEP_SANDBOX=1` (don’t terminate sandbox on exit)

Run:
```bash
cd docs/reference/spikes/04_tailscale_modal
go run .
```

## What It Does

- Prints sandbox OS/runtime info (incl. `/dev/net/tun` existence).
- Downloads the Tailscale static tarball into `/tmp/tailscale/` if missing.
- Starts `tailscaled` in userspace mode:
  - `--tun=userspace-networking`
  - socket: `/tmp/ts.sock`
  - state: `/tmp/ts.state`
  - SOCKS5 proxy: `localhost:1055`
- Runs `tailscale up` (if `TS_AUTHKEY` provided), then verifies:
  - `tailscale status`
  - `tailscale ping $COOK_TAIL_IP` (if set)
  - `curl --proxy socks5h://127.0.0.1:1055 $COOK_HTTP_URL` (if set)
  - `git ls-remote $COOK_GIT_URL` with `ALL_PROXY=socks5h://127.0.0.1:1055` (if set)

Note: With userspace networking, non-Tailscale-aware processes typically need the SOCKS5 proxy to reach tailnet IPs.

## Modal

**Run:** 2026-02-01

**Results**
- `/dev/net/tun`: present
- `tailscaled --tun=userspace-networking`: starts (yes)
- `tailscale up` (authkey, hostname, accept-dns=false): succeeds (yes)
- `tailscale status`: succeeds (yes), node gets tailnet IPv4
- `tailscale ping $COOK_TAIL_IP`: succeeds (tested against `100.73.239.5`)
- HTTP over SOCKS5: not tested (COOK_HTTP_URL not set)
- Git over SOCKS5: not tested (COOK_GIT_URL not set)

**Notes**
- Sandbox image is Nix-ish and missing `tar`/`gzip`/`python`; spike uploads `tailscale`/`tailscaled` directly via Modal filesystem APIs.
- Tailscale binaries from pkgs.tailscale.com are dynamically linked; need to exec via the sandbox’s glibc loader (found under `/nix/store/*glibc*/lib/ld-linux-x86-64.so.2`).
- Outbound UDP works (DERP connection established); kernel refused bumping UDP recv buffer size (throughput warning only).
- Background daemon via `nohup … &` works for the life of the sandbox.
- Image used for the run: `ghcr.io/justinmoon/cook-sandbox:latest` from `nix/sandbox-image.nix` (PATH=/bin). For extra packages, add to `nix/sandbox-image.nix` and rebuild/push, or point Modal at a different image via `MODAL_IMAGE`.
