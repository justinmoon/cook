package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	sprites "github.com/superfly/sprites-go"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	token := strings.TrimSpace(os.Getenv("SPRITES_TOKEN"))
	if token == "" {
		return fmt.Errorf("missing env: SPRITES_TOKEN")
	}
	tarballURL := strings.TrimSpace(os.Getenv("SPRITES_TARBALL_URL"))
	if tarballURL == "" {
		return fmt.Errorf("missing env: SPRITES_TARBALL_URL")
	}
	tsAuthKey := strings.TrimSpace(os.Getenv("TS_AUTHKEY"))
	if tsAuthKey == "" {
		return fmt.Errorf("missing env: TS_AUTHKEY")
	}

	cookTailIP := strings.TrimSpace(os.Getenv("COOK_TAIL_IP"))
	if cookTailIP == "" {
		return fmt.Errorf("missing env: COOK_TAIL_IP")
	}
	cookPort := strings.TrimSpace(os.Getenv("COOK_PORT"))
	if cookPort == "" {
		return fmt.Errorf("missing env: COOK_PORT")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	client := sprites.New(token)
	spriteName := fmt.Sprintf("cook-ts-%d", time.Now().UnixNano())

	fmt.Printf("== sprites create ==\nname: %s\n\n", spriteName)
	sprite, err := client.CreateSprite(ctx, spriteName, nil)
	if err != nil {
		return fmt.Errorf("create sprite: %w", err)
	}

	raw := func(cmd string) ([]byte, error) {
		cctx, ccancel := context.WithTimeout(ctx, 90*time.Second)
		defer ccancel()
		c := sprite.CommandContext(cctx, "/bin/sh", "-c", cmd)
		c.Dir = "/"
		return c.CombinedOutput()
	}

	env := []string{
		"PATH=/opt/sandbox/bin:/opt/sandbox/sbin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		"TERM=xterm-256color",
		"SSL_CERT_FILE=/opt/sandbox/etc/ssl/certs/ca-bundle.crt",
		"NODE_PATH=/root/.npm-global/lib/node_modules",
	}

	runEnv := func(dir, cmd string) ([]byte, error) {
		cctx, ccancel := context.WithTimeout(ctx, 2*time.Minute)
		defer ccancel()
		c := sprite.CommandContext(cctx, "/bin/sh", "-c", cmd)
		c.Env = env
		if dir != "" {
			c.Dir = dir
		}
		return c.CombinedOutput()
	}

	fmt.Println("== sprites: os/arch/init/privs ==")
	if out, err := raw("set -e; uname -a; echo; cat /etc/os-release || true; echo; id; echo; ps -p 1 -o pid=,comm=,args=; echo; ls -l /dev/net/tun || true; echo; grep -E '^(CapEff|NoNewPrivs):' /proc/self/status || true"); err != nil {
		fmt.Print(string(out))
		return fmt.Errorf("preflight failed: %w", err)
	} else {
		fmt.Print(string(out))
	}
	fmt.Println()

	fmt.Println("== sprites: install sandbox tarball ==")
	installCmd := fmt.Sprintf(
		`set -euo pipefail
if test -x /opt/sandbox/bin/bash; then
  echo "sandbox already installed"
  exit 0
fi
url=%s
if command -v curl >/dev/null 2>&1; then
  curl -fSL --retry 3 --retry-delay 1 --retry-connrefused "$url" -o /tmp/sandbox.tar.gz
elif command -v wget >/dev/null 2>&1; then
  wget -q -O /tmp/sandbox.tar.gz "$url"
else
  echo "missing curl or wget" >&2
  exit 1
fi
if command -v tar >/dev/null 2>&1; then
  tar -xzf /tmp/sandbox.tar.gz -C /
elif command -v busybox >/dev/null 2>&1; then
  busybox tar -xzf /tmp/sandbox.tar.gz -C /
else
  echo "missing tar" >&2
  exit 1
fi
rm -f /tmp/sandbox.tar.gz
echo "sandbox installed"`,
		shQuote(tarballURL),
	)
	if out, err := raw(installCmd); err != nil {
		fmt.Print(string(out))
		return fmt.Errorf("install sandbox: %w", err)
	} else {
		fmt.Print(string(out))
	}
	fmt.Println()

	fmt.Println("== sprites: tailscale binaries ==")
	tsBinSetup := `set -euo pipefail
if command -v tailscaled >/dev/null 2>&1 && command -v tailscale >/dev/null 2>&1; then
  echo "tailscale already present"
  exit 0
fi

ver="1.92.5"
arch="amd64"
tmp="/tmp/tailscale.tgz"
dir="/tmp/tailscale-bin"
rm -rf "$dir"
mkdir -p "$dir"

url="https://pkgs.tailscale.com/stable/tailscale_${ver}_${arch}.tgz"
echo "downloading: $url"
curl -fSL --retry 3 --retry-delay 1 --retry-connrefused "$url" -o "$tmp"
tar -xzf "$tmp" -C /tmp
rm -f "$tmp"
src="$(ls -d /tmp/tailscale_*_${arch} | head -n1)"
cp -a "$src/tailscale" "$src/tailscaled" "$dir/"
echo "installed tailscale into $dir"
`
	if out, err := runEnv("/", tsBinSetup); err != nil {
		fmt.Print(string(out))
		return fmt.Errorf("tailscale download: %w", err)
	}

	if out, err := runEnv("/", "set -e; export PATH=/tmp/tailscale-bin:$PATH; command -v tailscaled; tailscaled --version; command -v tailscale; tailscale version"); err != nil {
		fmt.Print(string(out))
		return fmt.Errorf("tailscale binaries: %w", err)
	} else {
		fmt.Print(string(out))
	}
	fmt.Println()

	fmt.Println("== sprites: tailscaled (userspace) + tailscale up ==")
	tsHostname := fmt.Sprintf("sprites-%s", spriteName)
	upCmd := fmt.Sprintf(
		`set -euo pipefail
export PATH=/tmp/tailscale-bin:$PATH
TS_STATE_DIR=/tmp/tailscale
TS_SOCK=/tmp/tailscaled.sock
mkdir -p "$TS_STATE_DIR"
pkill -x tailscaled >/dev/null 2>&1 || true
rm -f "$TS_SOCK" >/dev/null 2>&1 || true

nohup tailscaled \
  --tun=userspace-networking \
  --state="$TS_STATE_DIR/tailscaled.state" \
  --socket="$TS_SOCK" \
  --socks5-server=127.0.0.1:1055 \
  > /tmp/tailscaled.log 2>&1 &

for _ in $(seq 1 80); do
  if test -S "$TS_SOCK" 2>/dev/null; then
    if tailscale --socket="$TS_SOCK" status >/dev/null 2>&1; then
      break
    fi
  fi
  sleep 0.25
done

tailscale --socket="$TS_SOCK" up --authkey=%s --hostname=%s --accept-dns=false
echo
tailscale --socket="$TS_SOCK" status
echo
tailscale --socket="$TS_SOCK" ip -4
echo
pgrep -a tailscaled || true
echo
tail -n 40 /tmp/tailscaled.log || true`,
		shQuote(tsAuthKey),
		shQuote(tsHostname),
	)
	if out, err := runEnv("/", upCmd); err != nil {
		fmt.Print(string(out))
		return fmt.Errorf("tailscale up: %w", err)
	} else {
		fmt.Print(string(out))
	}
	fmt.Println()

	fmt.Println("== sprites: curl dev host over tailnet (via SOCKS5 proxy) ==")
	curlCmd := fmt.Sprintf(
		`set -euo pipefail
curl -sf --proxy socks5h://127.0.0.1:1055 %s | head -c 200
echo`,
		shQuote(fmt.Sprintf("http://%s:%s/healthz", cookTailIP, cookPort)),
	)
	if out, err := runEnv("/", curlCmd); err != nil {
		fmt.Print(string(out))
		return fmt.Errorf("curl over tailnet failed: %w", err)
	} else {
		fmt.Print(string(out))
	}
	fmt.Println()

	fmt.Printf("OK: sprite %s connected to tailnet; tailscaled running (userspace)\n", spriteName)
	fmt.Println("note: sprite not auto-deleted (manual cleanup in fly/sprites UI or via API)")
	return nil
}

func shQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
