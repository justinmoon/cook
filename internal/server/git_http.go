package server

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/justinmoon/cook/internal/auth"
)

func (s *Server) handleGitHTTP(w http.ResponseWriter, r *http.Request) {
	pathInfo := strings.TrimPrefix(r.URL.Path, "/git")
	if pathInfo == "" {
		pathInfo = "/"
	}

	env := append(os.Environ(),
		"GIT_PROJECT_ROOT="+filepath.Join(s.cfg.Server.DataDir, "repos"),
		"GIT_HTTP_EXPORT_ALL=1",
		"PATH_INFO="+pathInfo,
		"QUERY_STRING="+r.URL.RawQuery,
		"REQUEST_METHOD="+r.Method,
		"REMOTE_ADDR="+r.RemoteAddr,
		"SERVER_PROTOCOL="+r.Proto,
	)

	if ct := r.Header.Get("Content-Type"); ct != "" {
		env = append(env, "CONTENT_TYPE="+ct)
	}
	if r.ContentLength >= 0 {
		env = append(env, "CONTENT_LENGTH="+strconv.FormatInt(r.ContentLength, 10))
	}
	if proto := r.Header.Get("Git-Protocol"); proto != "" {
		env = append(env, "GIT_PROTOCOL="+proto)
	}
	if pubkey := auth.GetPubkey(r.Context()); pubkey != "" {
		env = append(env, "REMOTE_USER="+pubkey)
	}

	cmd := exec.Command("git", "http-backend")
	cmd.Env = env
	cmd.Stdin = r.Body
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "git http-backend failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := cmd.Start(); err != nil {
		http.Error(w, "git http-backend start failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	statusCode := http.StatusOK
	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			http.Error(w, "git http-backend read failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Status:") {
			statusCode = parseStatusCode(line)
			continue
		}
		if parts := strings.SplitN(line, ":", 2); len(parts) == 2 {
			w.Header().Add(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
		}
	}

	w.WriteHeader(statusCode)
	if _, err := io.Copy(w, reader); err != nil {
		http.Error(w, "git http-backend copy failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := cmd.Wait(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		http.Error(w, "git http-backend error: "+msg, http.StatusInternalServerError)
	}
}

func parseStatusCode(line string) int {
	fields := strings.Fields(strings.TrimPrefix(line, "Status:"))
	if len(fields) == 0 {
		return http.StatusOK
	}
	code, err := strconv.Atoi(fields[0])
	if err != nil {
		return http.StatusOK
	}
	return code
}
