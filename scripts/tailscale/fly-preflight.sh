#!/usr/bin/env bash
set -euo pipefail

export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:${PATH:-}"

echo "== cook tailscale preflight (fly machines) =="
echo "date: $(date -Is)"
echo "whoami: $(whoami)"
echo "id: $(id)"
echo "uname: $(uname -a)"
echo

echo "== /dev/net/tun =="
if [ -e /dev/net/tun ]; then
  ls -la /dev/net/tun
else
  echo "missing: /dev/net/tun"
fi
echo

echo "== caps (this process) =="
cap_eff="$(awk '/CapEff:/ {print $2}' /proc/self/status || true)"
if [ -n "$cap_eff" ]; then
  echo "CapEff: $cap_eff"
  if command -v capsh >/dev/null 2>&1; then
    capsh --decode="$cap_eff" || true
  fi
else
  echo "CapEff: (unavailable)"
fi
echo

echo "== tailscale binaries =="
command -v tailscaled >/dev/null 2>&1 && tailscaled --version || echo "tailscaled: not found"
command -v tailscale >/dev/null 2>&1 && tailscale version || echo "tailscale: not found"
