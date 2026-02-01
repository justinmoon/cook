# Spike 4: Tailscale on Fly Machines

Goal: run `tailscaled` inside the Fly Machines env image and confirm the machine can reach a dev host over tailnet (and vice versa).

## Build the Fly env image

This uses the nix sandbox image (same image used by the Fly Machines backend).

```bash
nix build .#sandbox-image
docker load < result
docker tag ghcr.io/justinmoon/cook-sandbox:latest registry.fly.io/<app>:cook-env
docker push registry.fly.io/<app>:cook-env
```

## On the Fly machine

### 1) Inspect base image capabilities

```bash
cook-ts-preflight
```

Things to check:
- `/dev/net/tun` exists
- process has `cap_net_admin` (or you can `sudo` to root and re-check)

### 2) Start tailscaled (system; fallback to userspace)

```bash
export TS_AUTHKEY="<tailscale auth key>"
cook-ts-up
```

This starts `tailscaled` with:
- state: `/tmp/ts.state`
- socket: `/tmp/ts.sock` (use `tailscale --socket=/tmp/ts.sock ...`)

If it falls back to userspace networking, it prints a proxy env you can use for `curl`/`git`:
`ALL_PROXY=socks5://127.0.0.1:1055`.

### 3) Verify tailnet connectivity

```bash
sudo tailscale --socket=/tmp/ts.sock status
sudo tailscale --socket=/tmp/ts.sock ping "$COOK_TAIL_IP"
```

### 4) Verify inbound (dev host -> Fly machine)

This is the “can I reach the machine over the tailnet” check.

On the Fly machine:

```bash
nc -lk -p 9999
```

From the dev host:

```bash
nc <fly-machine-tail-ip> 9999
```

If system-mode (tun) works, the direct `nc` should work. If you’re in userspace mode, use `tailscale serve`:

```bash
sudo TAILSCALE_SOCKET=/tmp/ts.sock tailscale serve --tcp 9999 --yes 127.0.0.1:9999
```

System-mode (tun) check:

```bash
curl -sf "http://$COOK_TAIL_IP:$COOK_PORT/"
git ls-remote "http://$COOK_TAIL_IP:$COOK_PORT/git/<owner>/<repo>.git"
```

Userspace-mode check:

```bash
ALL_PROXY=socks5://127.0.0.1:1055 curl -sf "http://$COOK_TAIL_IP:$COOK_PORT/"
ALL_PROXY=socks5://127.0.0.1:1055 git ls-remote "http://$COOK_TAIL_IP:$COOK_PORT/git/<owner>/<repo>.git"
```

## Report

### Fly Machines

**Date**: 2026-02-01

**Results**
- Fly Machine has `/dev/net/tun` and `cap_net_admin` (system/tun mode viable).
- `tailscale up` with `tailscaled --socket=/tmp/ts.sock` works; machine gets tailnet IP `100.108.174.9`.
- Outbound to dev host tail IP works in system mode (no proxy needed):
  - `curl http://100.67.51.109:7441/health` ✅
  - `git ls-remote http://100.67.51.109:7441/git/justinmoon/cook.git` ✅
- Inbound from dev host to Fly Machine tail IP works in system mode:
  - `nc 100.108.174.9 9998` -> machine receives bytes ✅

**Notes / Gotchas**
- `tailscale` CLI here uses `--socket` flag; `TAILSCALE_SOCKET` env did not take effect in practice. Use `tailscale --socket=/tmp/ts.sock ...`.
- Dev host Cook server must be reachable on tailnet. Default bind is `127.0.0.1`; for this spike I ran a separate instance bound to `0.0.0.0:7441` and added `justinmoon/cook` to its data dir.

**Blockers**
- None on Fly for system-mode Tailscale; main prerequisite is making the dev host service reachable on tailnet (bind or proxy).

**Needed image changes**
- Installed: `tailscale` + `tailscaled` plus basic networking/debug tools (`iproute2`, `iptables`, `iputils-ping`, `libcap2-bin`, `netcat-openbsd`).
