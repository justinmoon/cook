package server

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/justinmoon/cook/internal/auth"
	"github.com/justinmoon/cook/internal/repo"
	"github.com/justinmoon/cook/internal/task"
)

// API response helpers

func jsonResponse(w http.ResponseWriter, data interface{}, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func apiError(w http.ResponseWriter, message string, status int) {
	jsonResponse(w, map[string]string{"error": message}, status)
}

// Repo API handlers

func (s *Server) apiRepoList(w http.ResponseWriter, r *http.Request) {
	ownerFilter := r.URL.Query().Get("owner")

	store := repo.NewStore(s.cfg.Server.DataDir)
	repos, err := store.List(ownerFilter)
	if err != nil {
		apiError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Convert to API response
	result := make([]map[string]interface{}, len(repos))
	for i, rp := range repos {
		result[i] = map[string]interface{}{
			"owner":     rp.Owner,
			"name":      rp.Name,
			"full_name": rp.FullName(),
		}
	}

	jsonResponse(w, result, http.StatusOK)
}

func (s *Server) apiRepoCreate(w http.ResponseWriter, r *http.Request) {
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" {
		apiError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Name string `json:"name"`
		URL  string `json:"url"` // optional - for cloning
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apiError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		apiError(w, "Name is required", http.StatusBadRequest)
		return
	}

	store := repo.NewStore(s.cfg.Server.DataDir)
	var rp *repo.Repo
	var err error

	if req.URL != "" {
		rp, err = store.Clone(pubkey, req.Name, req.URL)
	} else {
		rp, err = store.Create(pubkey, req.Name)
	}

	if err != nil {
		apiError(w, err.Error(), http.StatusBadRequest)
		return
	}

	jsonResponse(w, map[string]interface{}{
		"owner":     rp.Owner,
		"name":      rp.Name,
		"full_name": rp.FullName(),
	}, http.StatusCreated)
}

func (s *Server) apiRepoGet(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	name := chi.URLParam(r, "repo")

	store := repo.NewStore(s.cfg.Server.DataDir)
	rp, err := store.Get(owner, name)
	if err != nil {
		apiError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rp == nil {
		apiError(w, "Repository not found", http.StatusNotFound)
		return
	}

	jsonResponse(w, map[string]interface{}{
		"owner":     rp.Owner,
		"name":      rp.Name,
		"full_name": rp.FullName(),
	}, http.StatusOK)
}

func (s *Server) apiRepoDelete(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	name := chi.URLParam(r, "repo")
	repoRef := owner + "/" + name

	// Check ownership
	if s.requireOwner(w, r, repoRef) == "" {
		return
	}

	store := repo.NewStore(s.cfg.Server.DataDir)
	if err := store.Remove(owner, name); err != nil {
		apiError(w, err.Error(), http.StatusBadRequest)
		return
	}

	jsonResponse(w, map[string]string{"status": "deleted"}, http.StatusOK)
}

// Task API handlers

func (s *Server) apiTaskListJSON(w http.ResponseWriter, r *http.Request) {
	repoFilter := r.URL.Query().Get("repo")
	statusFilter := r.URL.Query().Get("status")

	store := task.NewStore(s.db)
	tasks, err := store.List(repoFilter, statusFilter)
	if err != nil {
		apiError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	result := make([]map[string]interface{}, len(tasks))
	for i, t := range tasks {
		result[i] = map[string]interface{}{
			"repo":   t.Repo,
			"slug":   t.Slug,
			"title":  t.Title,
			"body":   t.Body,
			"status": t.Status,
		}
	}

	jsonResponse(w, result, http.StatusOK)
}

func (s *Server) apiTaskCreateJSON(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Repo  string `json:"repo"`
		Slug  string `json:"slug"`
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apiError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Repo == "" || req.Slug == "" || req.Title == "" {
		apiError(w, "repo, slug, and title are required", http.StatusBadRequest)
		return
	}

	// Check ownership
	if s.requireOwner(w, r, req.Repo) == "" {
		return
	}

	store := task.NewStore(s.db)
	t := &task.Task{
		Repo:  req.Repo,
		Slug:  req.Slug,
		Title: req.Title,
		Body:  req.Body,
	}
	if err := store.Create(t); err != nil {
		apiError(w, err.Error(), http.StatusBadRequest)
		return
	}

	jsonResponse(w, map[string]interface{}{
		"repo":   t.Repo,
		"slug":   t.Slug,
		"title":  t.Title,
		"body":   t.Body,
		"status": t.Status,
	}, http.StatusCreated)
}

func (s *Server) apiTaskGet(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	slug := chi.URLParam(r, "slug")
	repoRef := owner + "/" + repoName

	store := task.NewStore(s.db)
	t, err := store.Get(repoRef, slug)
	if err != nil {
		apiError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if t == nil {
		apiError(w, "Task not found", http.StatusNotFound)
		return
	}

	jsonResponse(w, map[string]interface{}{
		"repo":   t.Repo,
		"slug":   t.Slug,
		"title":  t.Title,
		"body":   t.Body,
		"status": t.Status,
	}, http.StatusOK)
}

// SSH Key API handlers

func (s *Server) apiSSHKeyList(w http.ResponseWriter, r *http.Request) {
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" {
		apiError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	store := auth.NewSSHKeyStore(s.db, s.cfg.Server.DataDir)
	keys, err := store.List(pubkey)
	if err != nil {
		apiError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	result := make([]map[string]interface{}, len(keys))
	for i, k := range keys {
		result[i] = map[string]interface{}{
			"fingerprint": k.Fingerprint,
			"name":        k.Name,
			"created_at":  k.CreatedAt.Format("2006-01-02T15:04:05Z"),
		}
	}

	jsonResponse(w, result, http.StatusOK)
}

func (s *Server) apiSSHKeyAdd(w http.ResponseWriter, r *http.Request) {
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" {
		apiError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Key  string `json:"key"`  // SSH public key content
		Name string `json:"name"` // optional label
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apiError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Key == "" {
		apiError(w, "key is required", http.StatusBadRequest)
		return
	}

	store := auth.NewSSHKeyStore(s.db, s.cfg.Server.DataDir)
	key, err := store.Add(pubkey, req.Key, req.Name)
	if err != nil {
		apiError(w, err.Error(), http.StatusBadRequest)
		return
	}

	jsonResponse(w, map[string]interface{}{
		"fingerprint": key.Fingerprint,
		"name":        key.Name,
		"created_at":  key.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}, http.StatusCreated)
}

func (s *Server) apiSSHKeyDelete(w http.ResponseWriter, r *http.Request) {
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" {
		apiError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	fingerprint := chi.URLParam(r, "fingerprint")
	if fingerprint == "" {
		apiError(w, "fingerprint is required", http.StatusBadRequest)
		return
	}

	store := auth.NewSSHKeyStore(s.db, s.cfg.Server.DataDir)
	if err := store.Remove(pubkey, fingerprint); err != nil {
		apiError(w, err.Error(), http.StatusBadRequest)
		return
	}

	jsonResponse(w, map[string]string{"status": "deleted"}, http.StatusOK)
}

// Whoami endpoint - returns current user info
func (s *Server) apiWhoami(w http.ResponseWriter, r *http.Request) {
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" {
		apiError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	npub, _ := auth.PubkeyToNpub(pubkey)

	jsonResponse(w, map[string]interface{}{
		"pubkey": pubkey,
		"npub":   npub,
		"owner":  s.cfg.IsOwner(pubkey),
	}, http.StatusOK)
}
