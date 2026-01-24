package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/justinmoon/cook/internal/auth"
	"github.com/justinmoon/cook/internal/config"
	"github.com/justinmoon/cook/internal/db"
	"github.com/justinmoon/cook/internal/events"
	"github.com/justinmoon/cook/internal/terminal"
)

// timeoutMiddleware applies timeout to all routes except streaming endpoints
func timeoutMiddleware(timeout time.Duration) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip timeout for streaming routes (SSE, WebSocket, terminal)
			path := r.URL.Path
			if strings.HasPrefix(path, "/events") ||
				strings.HasPrefix(path, "/ws/") ||
				strings.HasPrefix(path, "/terminal/") {
				next.ServeHTTP(w, r)
				return
			}
			// Apply timeout to all other routes
			middleware.Timeout(timeout)(next).ServeHTTP(w, r)
		})
	}
}

type Server struct {
	cfg            *config.Config
	db             *db.DB
	router         *chi.Mux
	server         *http.Server
	eventBus       *events.Bus
	termMgr        *terminal.Manager
	sessionStore   *auth.SessionStore
	challengeStore *auth.ChallengeStore
}

func New(cfg *config.Config, database *db.DB) (*Server, error) {
	eventBus, err := events.NewBus(cfg.Server.NatsURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create event bus: %w", err)
	}

	s := &Server{
		cfg:            cfg,
		db:             database,
		router:         chi.NewRouter(),
		eventBus:       eventBus,
		termMgr:        terminal.NewManager(),
		sessionStore:   auth.NewSessionStore(database),
		challengeStore: auth.NewChallengeStore(),
	}

	s.setupRoutes()
	return s, nil
}

func (s *Server) setupRoutes() {
	s.router.Use(middleware.Logger)
	s.router.Use(middleware.Recoverer)
	// Custom timeout middleware that excludes streaming routes
	s.router.Use(timeoutMiddleware(60 * time.Second))

	// Auth middleware (if enabled)
	authMiddleware := auth.NewMiddleware(s.sessionStore, s.cfg.AuthEnabled(), s.cfg.Server.AllowedPubkeys)
	s.router.Use(authMiddleware.Handler)

	// CSRF middleware (for session-auth POSTs)
	s.router.Use(auth.CSRFMiddleware)

	// Health check
	s.router.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	// Static files (for Datastar JS)
	s.router.Get("/static/*", s.handleStatic)

	// Auth routes (public)
	s.router.Get("/login", s.handleLogin)
	s.router.Get("/logout", s.handleLogout)
	s.router.Get("/auth/challenge", s.handleAuthChallenge)
	s.router.Post("/auth/verify", s.handleAuthVerify)

	// HTML pages
	s.router.Get("/", s.handleIndex)
	s.router.Get("/new", s.handleNewRepo)
	s.router.Post("/new", s.handleNewRepoCreate)
	s.router.Get("/settings", s.handleSettings)
	s.router.Post("/settings/ssh-keys", s.handleSettingsSSHKeyAdd)
	s.router.Post("/settings/ssh-keys/delete", s.handleSettingsSSHKeyDelete)
	s.router.Get("/settings/dotfiles", s.handleSettingsDotfiles)
	s.router.Post("/settings/dotfiles", s.handleSettingsDotfilesAdd)
	s.router.Post("/settings/dotfiles/{name}/delete", s.handleSettingsDotfilesDelete)
	s.router.Get("/repos", s.handleRepoList)
	s.router.Get("/repos/{owner}/{repo}", s.handleRepoDetail)
	s.router.Post("/repos/{owner}/{repo}/tasks", s.handleRepoTaskCreate)
	s.router.Post("/repos/{owner}/{repo}/branches", s.handleRepoBranchCreate)
	s.router.Get("/tasks/{owner}/{repo}/{slug}", s.handleTaskDetail)
	s.router.Post("/tasks/{owner}/{repo}/{slug}/edit", s.handleTaskEdit)
	s.router.Post("/tasks/{owner}/{repo}/{slug}/delete", s.handleTaskDelete)
	s.router.Post("/tasks/{owner}/{repo}/{slug}/start", s.handleTaskStartBranch)
	s.router.Get("/branches/{owner}/{repo}/{name}", s.handleBranchDetail)
	s.router.Post("/branches/{owner}/{repo}/{name}/gates/run", s.handleBranchRunGates)
	s.router.Post("/branches/{owner}/{repo}/{name}/merge", s.handleBranchMerge)
	s.router.Post("/branches/{owner}/{repo}/{name}/rebase", s.handleBranchRebase)
	s.router.Post("/branches/{owner}/{repo}/{name}/abandon", s.handleBranchAbandon)
	s.router.Get("/branches/{owner}/{repo}/{name}/tabs", s.handleBranchTabList)
	s.router.Post("/branches/{owner}/{repo}/{name}/tabs", s.handleBranchTabCreate)
	s.router.Delete("/branches/{owner}/{repo}/{name}/tabs/{tabId}", s.handleBranchTabDelete)
	s.router.Get("/branches/{owner}/{repo}/{name}/previews", s.handleBranchPreviewList)
	s.router.Post("/branches/{owner}/{repo}/{name}/previews", s.handleBranchPreviewCreate)
	s.router.Put("/branches/{owner}/{repo}/{name}/previews/{previewId}", s.handleBranchPreviewUpdate)
	s.router.Delete("/branches/{owner}/{repo}/{name}/previews/{previewId}", s.handleBranchPreviewDelete)
	s.router.Get("/branches/{owner}/{repo}/{name}/editors", s.handleBranchEditorList)
	s.router.Post("/branches/{owner}/{repo}/{name}/editors", s.handleBranchEditorCreate)
	s.router.Put("/branches/{owner}/{repo}/{name}/editors/{editorId}", s.handleBranchEditorUpdate)
	s.router.Delete("/branches/{owner}/{repo}/{name}/editors/{editorId}", s.handleBranchEditorDelete)
	s.router.Get("/branches/{owner}/{repo}/{name}/files", s.handleBranchFileGet)
	s.router.Put("/branches/{owner}/{repo}/{name}/files", s.handleBranchFilePut)
	s.router.Get("/branches/{owner}/{repo}/{name}/files/list", s.handleBranchFileList)
	s.router.Get("/branches/{owner}/{repo}/{name}/files/search", s.handleBranchFileSearch)
	s.router.Get("/branches/{owner}/{repo}/{name}/lsp/available", s.handleBranchLSPList)
	s.router.Get("/terminal/{owner}/{repo}/{name}", s.handleTerminalPage)

	// API endpoints (for Datastar)
	s.router.Route("/api", func(r chi.Router) {
		r.Get("/tasks", s.apiTaskList)
		r.Post("/tasks", s.apiTaskCreate)
		r.Get("/branches", s.apiBranchList)
		r.Get("/branches/active", s.apiActiveBranches)
		r.Get("/branches/{owner}/{repo}/{name}/gates", s.apiBranchGates)
	})

	// JSON API v1 (for CLI, uses NIP-98 or session auth)
	s.router.Route("/api/v1", func(r chi.Router) {
		// Whoami - uses existing session or NIP-98
		r.Get("/whoami", s.apiWhoami)

		// Repos
		r.Get("/repos", s.apiRepoList)
		r.Post("/repos", s.apiRepoCreate)
		r.Get("/repos/{owner}/{repo}", s.apiRepoGet)
		r.Delete("/repos/{owner}/{repo}", s.apiRepoDelete)

		// Tasks
		r.Get("/tasks", s.apiTaskListJSON)
		r.Post("/tasks", s.apiTaskCreateJSON)
		r.Get("/tasks/{owner}/{repo}/{slug}", s.apiTaskGet)

		// SSH Keys
		r.Get("/ssh-keys", s.apiSSHKeyList)
		r.Post("/ssh-keys", s.apiSSHKeyAdd)
		r.Delete("/ssh-keys/{fingerprint}", s.apiSSHKeyDelete)

		// Branch preview control
		r.Post("/branches/{owner}/{repo}/{name}/preview", s.apiBranchPreviewNavigate)
	})

	// SSE endpoint for real-time updates
	s.router.Get("/events", s.handleSSE)
	s.router.Get("/events/{owner}/{repo}/{branch}", s.handleBranchSSE)

	// WebSocket for terminal
	s.router.Get("/ws/terminal/{owner}/{repo}/{name}", s.handleTerminalWS)
	s.router.Get("/ws/lsp/{owner}/{repo}/{name}", s.handleLSPWS)
}

func (s *Server) TerminalManager() *terminal.Manager {
	return s.termMgr
}

func (s *Server) Start() error {
	addr := fmt.Sprintf("%s:%d", s.cfg.Server.Host, s.cfg.Server.Port)
	s.server = &http.Server{
		Addr:    addr,
		Handler: s.router,
	}

	fmt.Printf("Server starting on http://%s\n", addr)
	return s.server.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.termMgr != nil {
		s.termMgr.CloseAll()
	}
	if s.eventBus != nil {
		s.eventBus.Close()
	}
	if s.server != nil {
		return s.server.Shutdown(ctx)
	}
	return nil
}

func (s *Server) EventBus() *events.Bus {
	return s.eventBus
}
