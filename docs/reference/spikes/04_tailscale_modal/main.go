// Spike 4: Tailscale inside Modal Sandbox (userspace networking)
// Goal: verify Tailscale can run in Modal even without /dev/net/tun.

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modal-labs/libmodal/modal-go"
)

const (
	defaultModalAppName = "cook-sandbox"
	defaultModalImage   = "ghcr.io/justinmoon/cook-sandbox:latest"

	tailscaleDir      = "/tmp/tailscale"
	tailscaledState   = "/tmp/ts.state"
	tailscaleSocket   = "/tmp/ts.sock"
	tailscaledLogPath = "/tmp/tailscaled.log"
	socks5Addr        = "localhost:1055"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	appName := getenvDefault("MODAL_APP_NAME", defaultModalAppName)
	imageRef := getenvDefault("MODAL_IMAGE", defaultModalImage)

	client, err := modal.NewClient()
	if err != nil {
		return fmt.Errorf("modal.NewClient: %w", err)
	}

	app, err := client.Apps.FromName(ctx, appName, &modal.AppFromNameParams{CreateIfMissing: true})
	if err != nil {
		return fmt.Errorf("modal app: %w", err)
	}

	image := client.Images.FromRegistry(imageRef, nil)

	fmt.Printf("Creating Modal sandbox (app=%q image=%q)...\n", appName, imageRef)
	sb, err := client.Sandboxes.Create(ctx, app, image, &modal.SandboxCreateParams{
		Env: map[string]string{
			"HOME": "/root",
			"TERM": "xterm-256color",
		},
		Timeout: time.Hour,
	})
	if err != nil {
		return fmt.Errorf("create sandbox: %w", err)
	}

	sandboxID := sb.SandboxID
	fmt.Printf("Sandbox: %s\n", sandboxID)

	if os.Getenv("KEEP_SANDBOX") == "" {
		defer terminate(sb, sandboxID)
	} else {
		fmt.Println("KEEP_SANDBOX=1 set; leaving sandbox running.")
	}

	fmt.Println("\n== Identify base image + permissions ==")
	tryRun(ctx, sb, nil, "id; uname -a; (cat /etc/os-release || true); ls -la /dev/net || true; ls -l /dev/net/tun || true; (grep -E '^(Cap(Prm|Eff|Bnd)|NoNewPrivs):' /proc/self/status || true)")

	fmt.Println("\n== Download tailscale static build to /tmp ==")
	if err := ensureTailscale(ctx, sb); err != nil {
		return err
	}

	fmt.Println("\n== Start tailscaled (userspace) ==")
	if err := runChecked(ctx, sb, nil, startTailscaledUserspaceCmd()); err != nil {
		return err
	}

	tsHostname := getenvDefault("TS_HOSTNAME", fmt.Sprintf("modal-%s", shortID(sandboxID)))

	if os.Getenv("TS_AUTHKEY") != "" {
		fmt.Println("\n== tailscale up ==")
		if err := runChecked(ctx, sb, map[string]string{
			"TS_AUTHKEY": os.Getenv("TS_AUTHKEY"),
		}, tailscaleLocalAPICmd(fmt.Sprintf("up --authkey \"$TS_AUTHKEY\" --hostname %q --accept-dns=false --reset", tsHostname))); err != nil {
			tryRun(ctx, sb, nil, fmt.Sprintf("tail -n 200 %s || true", tailscaledLogPath))
			return err
		}
	} else {
		fmt.Println("\n== tailscale up ==")
		fmt.Println("TS_AUTHKEY not set; skipping tailscale up (can still verify tailscaled started).")
	}

	fmt.Println("\n== Verify tailscale status ==")
	if err := runChecked(ctx, sb, nil, tailscaleLocalAPICmd("status")); err != nil {
		tryRun(ctx, sb, nil, fmt.Sprintf("tail -n 200 %s || true", tailscaledLogPath))
		return err
	}

	if ip := os.Getenv("COOK_TAIL_IP"); ip != "" {
		fmt.Println("\n== tailscale ping ==")
		tryRun(ctx, sb, nil, tailscaleLocalAPICmd(fmt.Sprintf("ping %q", ip)))
	}

	if url := os.Getenv("COOK_HTTP_URL"); url != "" {
		fmt.Println("\n== HTTP over tailnet via SOCKS5 ==")
		tryRun(ctx, sb, nil, fmt.Sprintf("curl -fsS --proxy socks5h://127.0.0.1:1055 %q | head -c 200; echo", url))
	}

	if remote := os.Getenv("COOK_GIT_URL"); remote != "" {
		fmt.Println("\n== Git over tailnet via SOCKS5 ==")
		tryRun(ctx, sb, nil, fmt.Sprintf("ALL_PROXY=socks5h://127.0.0.1:1055 GIT_TERMINAL_PROMPT=0 git ls-remote %q", remote))
	}

	fmt.Println("\n== tailscaled log (tail) ==")
	tryRun(ctx, sb, nil, fmt.Sprintf("tail -n 200 %s", tailscaledLogPath))

	return nil
}

func ensureTailscale(ctx context.Context, sb *modal.Sandbox) error {
	archOut, err := execText(ctx, sb, "uname -m")
	if err != nil {
		return err
	}
	arch := strings.TrimSpace(archOut)

	tsArch := ""
	switch arch {
	case "x86_64":
		tsArch = "amd64"
	case "aarch64", "arm64":
		tsArch = "arm64"
	default:
		return fmt.Errorf("unsupported arch: %q", arch)
	}

	url := fmt.Sprintf("https://pkgs.tailscale.com/stable/tailscale_latest_%s.tgz", tsArch)
	fmt.Printf("Downloading Tailscale: %s\n", url)

	tailscaleBin, tailscaledBin, err := downloadTailscaleTarball(ctx, url)
	if err != nil {
		return err
	}

	if err := runChecked(ctx, sb, nil, fmt.Sprintf("rm -rf %s && mkdir -p %s", tailscaleDir, tailscaleDir)); err != nil {
		return err
	}
	if err := writeSandboxFile(ctx, sb, tailscaleDir+"/tailscale", tailscaleBin, 0o755); err != nil {
		return err
	}
	if err := writeSandboxFile(ctx, sb, tailscaleDir+"/tailscaled", tailscaledBin, 0o755); err != nil {
		return err
	}

	tryRun(ctx, sb, nil, tailscaleCmd("version"))
	return nil
}

func startTailscaledUserspaceCmd() string {
	return fmt.Sprintf(`
set -euo pipefail

LD="$(ls -1 /nix/store/*glibc*/lib/ld-linux-x86-64.so.2 2>/dev/null | head -n 1 || true)"
if [ -z "$LD" ]; then
  echo "missing glibc loader in /nix/store" >&2
  exit 1
fi
LIB="$(dirname "$LD")"
TS=%s/tailscale
TSD=%s/tailscaled
TS_SOCK=%s

ts()  { "$LD" --library-path "$LIB" "$TS" --socket="$TS_SOCK" "$@"; }
ts0() { "$LD" --library-path "$LIB" "$TS" "$@"; }
tsd() { "$LD" --library-path "$LIB" "$TSD" "$@"; }

if command -v pgrep >/dev/null 2>&1; then
  if pgrep -x tailscaled >/dev/null 2>&1; then
    echo "tailscaled already running"
    ts status || true
    exit 0
  fi
fi

rm -f %s %s
nohup "$LD" --library-path "$LIB" "$TSD" --tun=userspace-networking --state=%s --socket=%s --socks5-server=%s > %s 2>&1 &

for i in $(seq 1 100); do
  if [ -S %s ]; then
    echo "tailscaled socket ready: %s"
    break
  fi
  sleep 0.1
done

if [ ! -S %s ]; then
  echo "tailscaled socket not ready" >&2
  tail -n 200 %s || true
  exit 1
fi

ts0 version
`, tailscaleDir, tailscaleDir, tailscaleSocket, tailscaleSocket, tailscaledState, tailscaledState, tailscaleSocket, socks5Addr, tailscaledLogPath, tailscaleSocket, tailscaleSocket, tailscaleSocket, tailscaledLogPath)
}

func runWithEnv(ctx context.Context, sb *modal.Sandbox, env map[string]string, cmd string) (int, error) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return 0, nil
	}

	params := &modal.SandboxExecParams{}
	if len(env) > 0 {
		params.Env = env
	}

	proc, err := sb.Exec(ctx, []string{"sh", "-lc", cmd}, params)
	if err != nil {
		return 0, fmt.Errorf("exec: %w", err)
	}

	stdout, _ := io.ReadAll(proc.Stdout)
	stderr, _ := io.ReadAll(proc.Stderr)
	exitCode, err := proc.Wait(ctx)
	if err != nil {
		return 0, fmt.Errorf("wait: %w", err)
	}

	if len(stdout) > 0 {
		fmt.Print(string(stdout))
		if stdout[len(stdout)-1] != '\n' {
			fmt.Println()
		}
	}
	if len(stderr) > 0 {
		fmt.Fprint(os.Stderr, string(stderr))
		if stderr[len(stderr)-1] != '\n' {
			fmt.Fprintln(os.Stderr)
		}
	}
	return exitCode, nil
}

func runChecked(ctx context.Context, sb *modal.Sandbox, env map[string]string, cmd string) error {
	exitCode, err := runWithEnv(ctx, sb, env, cmd)
	if err != nil {
		return fmt.Errorf("command error: %w\ncmd=%s", err, strings.TrimSpace(cmd))
	}
	if exitCode != 0 {
		return fmt.Errorf("command failed (exit=%d)\ncmd=%s", exitCode, strings.TrimSpace(cmd))
	}
	return nil
}

func tryRun(ctx context.Context, sb *modal.Sandbox, env map[string]string, cmd string) {
	exitCode, err := runWithEnv(ctx, sb, env, cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "command error: %v\ncmd=%s\n", err, cmd)
		return
	}
	if exitCode != 0 {
		fmt.Fprintf(os.Stderr, "command failed (exit=%d)\ncmd=%s\n", exitCode, cmd)
	}
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func shortID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func terminate(sb *modal.Sandbox, sandboxID string) {
	fmt.Printf("Terminating sandbox %s...\n", sandboxID)
	_ = sb.Terminate(context.Background())
}

func tailscaleCmd(args string) string {
	return fmt.Sprintf(`
set -euo pipefail

LD="$(ls -1 /nix/store/*glibc*/lib/ld-linux-x86-64.so.2 2>/dev/null | head -n 1 || true)"
if [ -z "$LD" ]; then
  echo "missing glibc loader in /nix/store" >&2
  exit 1
fi
LIB="$(dirname "$LD")"
TS=%s/tailscale

"$LD" --library-path "$LIB" "$TS" %s
`, tailscaleDir, strings.TrimSpace(args))
}

func tailscaleLocalAPICmd(args string) string {
	return fmt.Sprintf(`
set -euo pipefail

LD="$(ls -1 /nix/store/*glibc*/lib/ld-linux-x86-64.so.2 2>/dev/null | head -n 1 || true)"
if [ -z "$LD" ]; then
  echo "missing glibc loader in /nix/store" >&2
  exit 1
fi
LIB="$(dirname "$LD")"
TS=%s/tailscale

"$LD" --library-path "$LIB" "$TS" --socket=%s %s
`, tailscaleDir, tailscaleSocket, strings.TrimSpace(args))
}

func execText(ctx context.Context, sb *modal.Sandbox, cmd string) (string, error) {
	proc, err := sb.Exec(ctx, []string{"sh", "-lc", cmd}, nil)
	if err != nil {
		return "", fmt.Errorf("exec %q: %w", cmd, err)
	}
	stdout, _ := io.ReadAll(proc.Stdout)
	stderr, _ := io.ReadAll(proc.Stderr)
	code, err := proc.Wait(ctx)
	if err != nil {
		return "", fmt.Errorf("wait %q: %w", cmd, err)
	}
	if code != 0 {
		return "", fmt.Errorf("command failed (exit=%d): %s", code, strings.TrimSpace(string(stderr)))
	}
	return string(stdout), nil
}

func downloadTailscaleTarball(ctx context.Context, url string) ([]byte, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}

	httpClient := &http.Client{Timeout: 2 * time.Minute}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, nil, fmt.Errorf("download failed: %s", resp.Status)
	}

	gzr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	var tailscaleBin []byte
	var tailscaledBin []byte

	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		if h.FileInfo().IsDir() {
			continue
		}
		if strings.HasSuffix(h.Name, "/tailscale") {
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, nil, err
			}
			tailscaleBin = b
			continue
		}
		if strings.HasSuffix(h.Name, "/tailscaled") {
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, nil, err
			}
			tailscaledBin = b
			continue
		}
	}

	if len(tailscaleBin) == 0 || len(tailscaledBin) == 0 {
		return nil, nil, fmt.Errorf("tailscale binaries not found in tarball")
	}

	return tailscaleBin, tailscaledBin, nil
}

func writeSandboxFile(ctx context.Context, sb *modal.Sandbox, path string, content []byte, mode int) error {
	f, err := sb.Open(ctx, path, "w")
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}

	const chunkSize = 512 * 1024
	r := bytes.NewReader(content)
	buf := make([]byte, chunkSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return fmt.Errorf("write %s: %w", path, werr)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read chunk: %w", err)
		}
	}

	if err := f.Flush(); err != nil {
		return fmt.Errorf("flush %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}

	_, exitErr := runWithEnv(ctx, sb, nil, fmt.Sprintf("chmod %o %q", mode, path))
	if exitErr != nil {
		return fmt.Errorf("chmod %s: %w", path, exitErr)
	}
	return nil
}
