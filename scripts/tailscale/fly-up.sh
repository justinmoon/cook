#!/usr/bin/env bash
set -euo pipefail

export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:${PATH:-}"

maybe_sudo() {
  if [ "$(id -u)" = "0" ]; then
    "$@"
    return $?
  fi
  if command -v sudo >/dev/null 2>&1; then
    sudo "$@"
    return $?
  fi
  "$@"
}

state_path="${TS_STATE_PATH:-/tmp/ts.state}"
socket_path="${TS_SOCKET_PATH:-/tmp/ts.sock}"
log_path="${TS_LOG_PATH:-/tmp/tailscaled.log}"
socks_port="${TS_SOCKS_PORT:-1055}"

if [ -z "${TS_AUTHKEY:-}" ]; then
  echo "missing env: TS_AUTHKEY"
  exit 2
fi

hostname_flag=""
if [ -n "${TS_HOSTNAME:-}" ]; then
  hostname_flag="--hostname=${TS_HOSTNAME}"
elif [ -n "${FLY_APP_NAME:-}" ] && [ -n "${FLY_MACHINE_ID:-}" ]; then
  hostname_flag="--hostname=fly-${FLY_APP_NAME}-${FLY_MACHINE_ID}"
fi

tailscale_bin="$(command -v tailscale || true)"
tailscaled_bin="$(command -v tailscaled || true)"
if [ -z "$tailscale_bin" ] && [ -x /bin/tailscale ]; then
  tailscale_bin="/bin/tailscale"
fi
if [ -z "$tailscaled_bin" ] && [ -x /bin/tailscaled ]; then
  tailscaled_bin="/bin/tailscaled"
fi
if [ -z "$tailscale_bin" ] || [ -z "$tailscaled_bin" ]; then
  echo "tailscale binaries not found (tailscale: ${tailscale_bin:-missing}, tailscaled: ${tailscaled_bin:-missing})" >&2
  exit 1
fi

tailscaled_help="$("$tailscaled_bin" --help 2>&1 || true)"

pkill -x tailscaled >/dev/null 2>&1 || true
rm -f "$socket_path" >/dev/null 2>&1 || true

tailscale_cmd() {
  maybe_sudo "$tailscale_bin" --socket="$socket_path" "$@"
}

if [ -S "$socket_path" ]; then
  if tailscale_cmd status >/dev/null 2>&1; then
    echo "tailscale already running"
    echo
    tailscale_cmd status
    exit 0
  fi
fi

start_system() {
  maybe_sudo nohup "$tailscaled_bin" --state="$state_path" --socket="$socket_path" >"$log_path" 2>&1 &
}

start_userspace() {
  args=(--tun=userspace-networking --state="$state_path" --socket="$socket_path")
  if printf '%s\n' "$tailscaled_help" | rg -q -- '--socks5-server'; then
    args+=(--socks5-server="127.0.0.1:${socks_port}")
  fi
  if printf '%s\n' "$tailscaled_help" | rg -q -- '--outbound-http-proxy-listen'; then
    args+=(--outbound-http-proxy-listen="127.0.0.1:${socks_port}")
  fi
  maybe_sudo nohup "$tailscaled_bin" "${args[@]}" >"$log_path" 2>&1 &
}

wait_ready() {
  for _ in $(seq 1 80); do
    if [ -S "$socket_path" ]; then
      if tailscale_cmd status >/dev/null 2>&1; then
        return 0
      fi
    fi
    sleep 0.25
  done
  return 1
}

mode="system"
if [ "${TS_FORCE_USERSPACE:-}" = "1" ]; then
  mode="userspace"
elif [ ! -e /dev/net/tun ]; then
  mode="userspace"
fi

if [ "$mode" = "system" ]; then
  start_system
  if ! wait_ready; then
    pkill -x tailscaled >/dev/null 2>&1 || true
    mode="userspace"
  fi
fi

if [ "$mode" = "userspace" ]; then
  start_userspace
  if ! wait_ready; then
    echo "tailscaled failed to start (userspace). log: $log_path"
    tail -n 200 "$log_path" || true
    exit 1
  fi
fi

up_timeout="${TS_UP_TIMEOUT:-120}"
if command -v timeout >/dev/null 2>&1; then
  maybe_sudo timeout "$up_timeout" "$tailscale_bin" --socket="$socket_path" up \
    --authkey="${TS_AUTHKEY}" \
    ${hostname_flag} \
    --accept-dns=false \
    ${TS_EXTRA_UP_FLAGS:-}
else
  tailscale_cmd up \
    --authkey="${TS_AUTHKEY}" \
    ${hostname_flag} \
    --accept-dns=false \
    ${TS_EXTRA_UP_FLAGS:-}
fi

echo
tailscale_cmd status

echo
echo "tailscale mode: $mode"
echo "tailscale socket: $socket_path (use: tailscale --socket=$socket_path ...)"
if [ "$mode" = "userspace" ]; then
  echo "userspace proxy: export ALL_PROXY=socks5://127.0.0.1:${socks_port}"
fi
