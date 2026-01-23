package server

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/justinmoon/cook/internal/auth"
	"github.com/justinmoon/cook/internal/branch"
)

// lspConfig maps language IDs to their LSP server commands and nix package names
var lspConfig = map[string]struct {
	command  []string
	nixPkgs  []string // package names to look for in flake.nix
}{
	"go":         {[]string{"gopls"}, []string{"gopls"}},
	"rust":       {[]string{"rust-analyzer"}, []string{"rust-analyzer"}},
	"typescript": {[]string{"typescript-language-server", "--stdio"}, []string{"typescript-language-server", "nodePackages.typescript-language-server"}},
	"javascript": {[]string{"typescript-language-server", "--stdio"}, []string{"typescript-language-server", "nodePackages.typescript-language-server"}},
	"python":     {[]string{"pyright-langserver", "--stdio"}, []string{"pyright", "python3Packages.pyright"}},
	"c":          {[]string{"clangd"}, []string{"clang-tools", "clangd"}},
	"cpp":        {[]string{"clangd"}, []string{"clang-tools", "clangd"}},
	"nix":        {[]string{"nil"}, []string{"nil"}},
}

func lspCommandForLanguage(languageId string) ([]string, error) {
	if cfg, ok := lspConfig[languageId]; ok {
		return cfg.command, nil
	}
	return nil, fmt.Errorf("unsupported languageId: %s", languageId)
}

// DiscoverAvailableLSPs reads a flake.nix file and returns which LSPs are available
func DiscoverAvailableLSPs(checkoutPath string) ([]string, error) {
	flakePath := filepath.Join(checkoutPath, "flake.nix")
	content, err := os.ReadFile(flakePath)
	if err != nil {
		return nil, err
	}
	flakeContent := string(content)

	var available []string
	for langId, cfg := range lspConfig {
		for _, pkg := range cfg.nixPkgs {
			// Look for the package name in the flake.nix content
			// This is a simple heuristic - look for the package name as a word
			if strings.Contains(flakeContent, pkg) {
				available = append(available, langId)
				break
			}
		}
	}
	return available, nil
}

func buildLanguageServerCmd(checkoutPath, languageId string) (*exec.Cmd, error) {
	flakePath := filepath.Join(checkoutPath, "flake.nix")
	if _, err := os.Stat(flakePath); err != nil {
		return nil, fmt.Errorf("flake.nix required for LSP (missing in checkout)")
	}

	serverCmd, err := lspCommandForLanguage(languageId)
	if err != nil {
		return nil, err
	}

	args := []string{"develop", "--accept-flake-config", "-c"}
	args = append(args, serverCmd...)
	cmd := exec.Command("nix", args...)
	cmd.Dir = checkoutPath
	cmd.Env = os.Environ()
	return cmd, nil
}

func readLSPMessage(r *bufio.Reader) ([]byte, error) {
	contentLength := -1
	// Skip any non-LSP output (shell hook noise, etc) until we see Content-Length
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if contentLength >= 0 {
				break // End of headers, we have content-length
			}
			continue // Empty line but no content-length yet, skip
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			if contentLength < 0 {
				continue // Skip garbage lines before first header
			}
			continue
		}
		if strings.EqualFold(strings.TrimSpace(parts[0]), "content-length") {
			n, err := strconv.Atoi(strings.TrimSpace(parts[1]))
			if err != nil {
				continue // Invalid content-length, skip this line
			}
			contentLength = n
		}
	}
	if contentLength < 0 {
		return nil, fmt.Errorf("missing content-length")
	}
	if contentLength > 10<<20 { // 10 MiB
		return nil, fmt.Errorf("message too large")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

func (s *Server) handleLSPWS(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	branchName := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName

	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || owner != pubkey {
		http.Error(w, "Forbidden: LSP access requires ownership", http.StatusForbidden)
		return
	}

	languageId := r.URL.Query().Get("languageId")
	if languageId == "" {
		http.Error(w, "Missing languageId", http.StatusBadRequest)
		return
	}

	branchStore := branch.NewStore(s.db, s.cfg.Server.DataDir)
	b, err := branchStore.Get(repoRef, branchName)
	if err != nil || b == nil {
		http.Error(w, "Branch not found", http.StatusNotFound)
		return
	}
	if b.Environment.Path == "" {
		http.Error(w, "Branch has no checkout path", http.StatusBadRequest)
		return
	}

	cmd, err := buildLanguageServerCmd(b.Environment.Path, languageId)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		http.Error(w, "Failed to create stdin pipe", http.StatusInternalServerError)
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "Failed to create stdout pipe", http.StatusInternalServerError)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		http.Error(w, "Failed to create stderr pipe", http.StatusInternalServerError)
		return
	}

	if err := cmd.Start(); err != nil {
		http.Error(w, "Failed to start language server", http.StatusInternalServerError)
		return
	}
	log.Printf("Started LSP server for %s (%s): %s %v", repoRef+"/"+branchName, languageId, cmd.Path, cmd.Args)

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return
	}
	defer conn.Close()

	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			log.Printf("[lsp %s %s] %s", repoRef+"/"+branchName, languageId, scanner.Text())
		}
	}()

	go func() {
		reader := bufio.NewReader(stdout)
		for {
			msg, err := readLSPMessage(reader)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					log.Printf("LSP read error: %v", err)
				}
				_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		}
	}()

	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}
		if len(data) > 10<<20 {
			return
		}
		header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
		if _, err := io.WriteString(stdin, header); err != nil {
			return
		}
		if _, err := stdin.Write(data); err != nil {
			return
		}
	}
}
