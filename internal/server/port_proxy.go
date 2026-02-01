package server

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/justinmoon/cook/internal/auth"
	"github.com/justinmoon/cook/internal/branch"
)

func parsePortParam(portStr string) (int, error) {
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid port: %q", portStr)
	}
	return port, nil
}

func newStripPrefixReverseProxy(target *url.URL, stripPrefix string) *httputil.ReverseProxy {
	director := func(req *http.Request) {
		outPath := strings.TrimPrefix(req.URL.Path, stripPrefix)
		if outPath == req.URL.Path {
			// Not expected; fall back to root to avoid proxying our internal paths.
			outPath = "/"
		}
		if outPath == "" {
			outPath = "/"
		}
		if !strings.HasPrefix(outPath, "/") {
			outPath = "/" + outPath
		}

		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.URL.Path = outPath
		req.Host = target.Host
	}

	return &httputil.ReverseProxy{
		Director: director,
	}
}

func (s *Server) handleBranchPortProxy(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	branchName := chi.URLParam(r, "name")
	portStr := chi.URLParam(r, "port")

	port, err := parsePortParam(portStr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Port proxy access requires ownership (same as terminal ws)
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || owner != pubkey {
		http.Error(w, "Forbidden: WebUI access requires ownership", http.StatusForbidden)
		return
	}

	repoRef := owner + "/" + repoName
	branchStore := branch.NewStore(s.db, s.cfg.Server.DataDir)
	b, err := branchStore.Get(repoRef, branchName)
	if err != nil {
		http.Error(w, "Failed to get branch", http.StatusInternalServerError)
		return
	}
	if b == nil {
		http.Error(w, "Branch not found", http.StatusNotFound)
		return
	}
	if b.Environment.Provisioning {
		http.Error(w, "Branch provisioning in progress", http.StatusServiceUnavailable)
		return
	}
	if b.Environment.ProvisioningError != "" {
		http.Error(w, "Branch provisioning failed: "+b.Environment.ProvisioningError, http.StatusServiceUnavailable)
		return
	}
	if b.Environment.Path == "" {
		http.Error(w, "Branch has no checkout path", http.StatusBadRequest)
		return
	}

	switch b.Environment.Backend {
	case "local", "docker":
		// ok
	case "modal", "sprites", "fly-machines":
		http.Error(w, fmt.Sprintf("Port proxy not supported for backend %q yet (MVP supports local and docker only)", b.Environment.Backend), http.StatusNotImplemented)
		return
	default:
		http.Error(w, fmt.Sprintf("Port proxy not supported for backend %q", b.Environment.Backend), http.StatusNotImplemented)
		return
	}

	stripPrefix := fmt.Sprintf("/branches/%s/%s/%s/ports/%d", owner, repoName, branchName, port)
	target := &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
	}
	proxy := newStripPrefixReverseProxy(target, stripPrefix)
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		http.Error(rw, "Failed to proxy to localhost:"+strconv.Itoa(port)+": "+err.Error(), http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}
