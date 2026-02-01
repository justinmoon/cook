package env

import (
	"context"
	"fmt"
	"os"
	"testing"
)

type tailnetTestConfig struct {
	authKey  string
	tailIP   string
	tailPort string
	hostname string
	gitURL   string
}

func tailnetConfigFromEnv(t *testing.T) (tailnetTestConfig, bool) {
	t.Helper()

	if os.Getenv("COOK_TAILNET_TEST") == "" {
		return tailnetTestConfig{}, false
	}

	authKey := os.Getenv("TS_AUTHKEY")
	if authKey == "" {
		t.Skip("TS_AUTHKEY not set, skipping tailnet test")
	}

	tailIP := os.Getenv("COOK_TAIL_IP")
	if tailIP == "" {
		t.Skip("COOK_TAIL_IP not set, skipping tailnet test")
	}

	tailPort := os.Getenv("COOK_TAIL_PORT")
	if tailPort == "" {
		tailPort = os.Getenv("COOK_PORT")
	}
	if tailPort == "" {
		tailPort = "7420"
	}

	hostname := os.Getenv("COOK_TAIL_HOSTNAME")
	if hostname == "" {
		hostname = os.Getenv("TS_HOSTNAME")
	}

	return tailnetTestConfig{
		authKey:  authKey,
		tailIP:   tailIP,
		tailPort: tailPort,
		hostname: hostname,
		gitURL:   os.Getenv("COOK_TAIL_GIT_URL"),
	}, true
}

func runTailnetUserspace(t *testing.T, ctx context.Context, exec func(context.Context, string) ([]byte, error), label string, cfg tailnetTestConfig) {
	t.Helper()

	targetURL := fmt.Sprintf("http://%s:%s/health", cfg.tailIP, cfg.tailPort)
	cmd := fmt.Sprintf(`
set -euo pipefail

TS_AUTHKEY=%s
TS_HOSTNAME=%s
TAIL_GIT_URL=%s
TARGET_URL=%s

state_dir=/tmp/tailscale
sock=/tmp/ts.sock
log=/tmp/tailscaled.log
socks_port=1055

mkdir -p "$state_dir"
pkill -x tailscaled >/dev/null 2>&1 || true
rm -f "$sock" >/dev/null 2>&1 || true

nohup tailscaled --tun=userspace-networking --state="$state_dir/tailscaled.state" --socket="$sock" --socks5-server="127.0.0.1:${socks_port}" >"$log" 2>&1 &
sleep 1

hostname_flag=""
if [ -n "$TS_HOSTNAME" ]; then
  hostname_flag="--hostname=$TS_HOSTNAME"
fi

tailscale --socket="$sock" up --authkey="$TS_AUTHKEY" $hostname_flag --accept-dns=false
tailscale --socket="$sock" status >/dev/null

curl -sf --proxy "socks5h://127.0.0.1:${socks_port}" "$TARGET_URL"
if [ -n "$TAIL_GIT_URL" ]; then
  git -c http.proxy="socks5h://127.0.0.1:${socks_port}" ls-remote "$TAIL_GIT_URL" >/dev/null
fi
`, shellEscape(cfg.authKey), shellEscape(cfg.hostname), shellEscape(cfg.gitURL), shellEscape(targetURL))

	output, err := exec(ctx, cmd)
	if err != nil {
		t.Fatalf("%s tailnet userspace test failed: %v\n%s", label, err, string(output))
	}
}

func runTailnetAuto(t *testing.T, ctx context.Context, exec func(context.Context, string) ([]byte, error), label string, cfg tailnetTestConfig) {
	t.Helper()

	targetURL := fmt.Sprintf("http://%s:%s/health", cfg.tailIP, cfg.tailPort)
	cmd := fmt.Sprintf(`
set -euo pipefail

TS_AUTHKEY=%s
TS_HOSTNAME=%s
TAIL_GIT_URL=%s
TARGET_URL=%s

export TS_AUTHKEY
export TS_HOSTNAME

if command -v cook-ts-up >/dev/null 2>&1; then
  cook-ts-up
else
  state_dir=/tmp/tailscale
  sock=/tmp/ts.sock
  log=/tmp/tailscaled.log
  socks_port=1055

  mkdir -p "$state_dir"
  pkill -x tailscaled >/dev/null 2>&1 || true
  rm -f "$sock" >/dev/null 2>&1 || true

  nohup tailscaled --tun=userspace-networking --state="$state_dir/tailscaled.state" --socket="$sock" --socks5-server="127.0.0.1:${socks_port}" >"$log" 2>&1 &
  sleep 1

  hostname_flag=""
  if [ -n "$TS_HOSTNAME" ]; then
    hostname_flag="--hostname=$TS_HOSTNAME"
  fi

  tailscale --socket="$sock" up --authkey="$TS_AUTHKEY" $hostname_flag --accept-dns=false
  tailscale --socket="$sock" status >/dev/null
fi

if curl -sf "$TARGET_URL"; then
  if [ -n "$TAIL_GIT_URL" ]; then
    git ls-remote "$TAIL_GIT_URL" >/dev/null
  fi
else
  ALL_PROXY="socks5://127.0.0.1:1055" curl -sf "$TARGET_URL"
  if [ -n "$TAIL_GIT_URL" ]; then
    git -c http.proxy="socks5h://127.0.0.1:1055" ls-remote "$TAIL_GIT_URL" >/dev/null
  fi
fi
`, shellEscape(cfg.authKey), shellEscape(cfg.hostname), shellEscape(cfg.gitURL), shellEscape(targetURL))

	output, err := exec(ctx, cmd)
	if err != nil {
		t.Fatalf("%s tailnet auto test failed: %v\n%s", label, err, string(output))
	}
}
