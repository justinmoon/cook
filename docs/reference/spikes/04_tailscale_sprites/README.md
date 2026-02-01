# Spike 4: Tailscale in Sprites sandbox (userspace)

Goal: run `tailscaled` + `tailscale up` inside a Fly.io Sprites sandbox, reach a dev host over tailnet, keep session alive.

Prereqs (env vars):
- `SPRITES_TOKEN`
- `SPRITES_TARBALL_URL` (cook nix tarball; now includes `tailscale`)
- `TS_AUTHKEY` (tagged + reusable)
- `COOK_TAIL_IP` (dev host tailnet IPv4)
- `COOK_PORT` (dev host port; default `7420`)

## Sprites

### 0) Determine OS/arch, init system, privileges

```sh
uname -a
cat /etc/os-release || true
id
ps -p 1 -o pid=,comm=,args=
ls -l /dev/net/tun || true
grep -E '^(CapEff|NoNewPrivs):' /proc/self/status || true
```

### 1) Start userspace tailscaled (no /dev/net/tun needed)

Note: in userspace mode you generally need to use a SOCKS5/HTTP proxy to reach tailnet IPs.

If your `SPRITES_TARBALL_URL` points at an older tarball (without `tailscale`), either rebuild/re-upload it or do a one-off install in `/tmp`:

```sh
ver="1.92.5"
arch="amd64"
curl -fSL "https://pkgs.tailscale.com/stable/tailscale_${ver}_${arch}.tgz" -o /tmp/tailscale.tgz
tar -xzf /tmp/tailscale.tgz -C /tmp
bin_dir="/tmp/tailscale-bin"
rm -rf "$bin_dir" && mkdir -p "$bin_dir"
src="$(ls -d /tmp/tailscale_*_${arch} | head -n1)"
cp -a "$src/tailscale" "$src/tailscaled" "$bin_dir/"
export PATH="$bin_dir:$PATH"
```

```sh
set -euo pipefail

export TS_STATE_DIR=/tmp/tailscale
export TS_SOCK=/tmp/tailscaled.sock
mkdir -p "$TS_STATE_DIR"

nohup tailscaled \
  --tun=userspace-networking \
  --state="$TS_STATE_DIR/tailscaled.state" \
  --socket="$TS_SOCK" \
  --socks5-server=localhost:1055 \
  > /tmp/tailscaled.log 2>&1 &
sleep 1
tail -n 50 /tmp/tailscaled.log || true
```

### 2) `tailscale up` (authkey, hostname, accept-dns=false)

```sh
set -euo pipefail

TS_HOSTNAME="${TS_HOSTNAME:-sprites-${HOSTNAME:-unknown}}"
tailscale --socket="$TS_SOCK" up \
  --authkey="$TS_AUTHKEY" \
  --hostname="$TS_HOSTNAME" \
  --accept-dns=false
```

### 3) Verify

```sh
tailscale --socket="$TS_SOCK" status
tailscale --socket="$TS_SOCK" ip -4
```

### 4) Test reachability (via SOCKS5)

```sh
set -euo pipefail

curl -sf --proxy socks5h://localhost:1055 "http://${COOK_TAIL_IP}:${COOK_PORT}/healthz" \
  || curl -sf --proxy socks5h://localhost:1055 "http://${COOK_TAIL_IP}:${COOK_PORT}/"

git -c http.proxy="socks5h://localhost:1055" \
  ls-remote "http://${COOK_TAIL_IP}:${COOK_PORT}/git/<owner>/<repo>.git"
```

### 5) Keep session alive

`tailscaled` is started via `nohup … &` and should keep running for the lifetime of the sprite (until teardown/checkpoint/restore semantics stop it).

## Blockers / failure modes to record

- `/dev/net/tun` missing: expected; userspace mode should still work.
- Outbound restrictions: must reach Tailscale control plane over HTTPS (auth, DERP, etc.).
- Time skew / no CA certs: TLS failures during login.
- Process lifetime: sprite suspend/checkpoint may stop background processes; may need a startup hook to re-run tailscaled after restore.
- DNS: `accept-dns=false` avoids relying on MagicDNS inside the sandbox; use `COOK_TAIL_IP` directly.

## Recommendation

Make this a first-class “connectivity sidecar” pattern for Sprites:
- include `tailscale` in the sandbox tarball (done)
- run `tailscaled` in userspace mode with a SOCKS5 proxy
- route all curl/git traffic via proxy (explicit, predictable, no CAP_NET_ADMIN needed)
