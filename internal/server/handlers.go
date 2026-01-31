package server

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/justinmoon/cook/internal/agent"
	"github.com/justinmoon/cook/internal/auth"
	"github.com/justinmoon/cook/internal/branch"
	"github.com/justinmoon/cook/internal/dotfiles"
	"github.com/justinmoon/cook/internal/editor"
	"github.com/justinmoon/cook/internal/events"
	"github.com/justinmoon/cook/internal/gate"
	"github.com/justinmoon/cook/internal/repo"
	"github.com/justinmoon/cook/internal/task"
	"github.com/justinmoon/cook/internal/terminal"
)

// TemplateUser represents the current user for templates
type TemplateUser struct {
	Pubkey      string
	Npub        string
	ShortPubkey string
	DisplayName string
	Picture     string
	HasSSHKeys  bool
}

// getTemplateUser returns user info for templates, or nil if not logged in
func (s *Server) getTemplateUser(r *http.Request) *TemplateUser {
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" {
		return nil
	}

	user := &TemplateUser{
		Pubkey:      pubkey,
		ShortPubkey: auth.ShortPubkey(pubkey),
	}

	// Get npub
	if npub, err := auth.PubkeyToNpub(pubkey); err == nil {
		user.Npub = npub
	}

	// Get display name and picture from profile cache
	profileStore := auth.NewProfileStore(s.db)
	profile, _ := profileStore.Get(pubkey)
	if profile != nil {
		user.DisplayName = profile.DisplayName()
		user.Picture = profile.Picture
	} else {
		// Profile not cached, trigger async fetch for next time
		go profileStore.FetchAndCache(pubkey, nil)
	}
	if user.DisplayName == "" {
		user.DisplayName = auth.ShortPubkey(pubkey)
	}

	// Check if user has SSH keys
	sshKeyStore := auth.NewSSHKeyStore(s.db, s.cfg.Server.DataDir)
	keys, _ := sshKeyStore.List(pubkey)
	user.HasSSHKeys = len(keys) > 0

	return user
}

// baseTemplateData returns common template data including user info and CSRF token
func (s *Server) baseTemplateData(r *http.Request, title string) map[string]interface{} {
	return map[string]interface{}{
		"Title":     title,
		"User":      s.getTemplateUser(r),
		"CSRFToken": s.getCSRFToken(r),
	}
}

// getCSRFToken returns the CSRF token from cookie or empty string
func (s *Server) getCSRFToken(r *http.Request) string {
	if cookie, err := r.Cookie(auth.CSRFCookieName); err == nil {
		return cookie.Value
	}
	return ""
}

//go:embed templates/*
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

// pageTemplates stores a template for each page, properly combining base + page content
var pageTemplates map[string]*template.Template

func init() {
	pageTemplates = make(map[string]*template.Template)

	// Read the base template
	baseContent, err := templatesFS.ReadFile("templates/base.html")
	if err != nil {
		panic(err)
	}

	// List of page templates that use the base template
	pages := []string{
		"repos.html",
		"repo_detail.html",
		"task_detail.html",
		"new_repo.html",
		"settings.html",
		"settings_dotfiles.html",
	}

	for _, page := range pages {
		pageContent, err := templatesFS.ReadFile("templates/" + page)
		if err != nil {
			panic(err)
		}

		// Create a new template for each page by combining base + page
		tmpl := template.New(page)
		_, err = tmpl.Parse(string(baseContent))
		if err != nil {
			panic(err)
		}
		_, err = tmpl.Parse(string(pageContent))
		if err != nil {
			panic(err)
		}
		pageTemplates[page] = tmpl
	}

	// Also load standalone templates (fragments and special pages)
	standaloneTemplates := []string{
		"gate_list_fragment.html",
		"terminal.html",
		"branch_detail.html",
	}
	for _, name := range standaloneTemplates {
		content, err := templatesFS.ReadFile("templates/" + name)
		if err != nil {
			panic(err)
		}
		tmpl := template.New(name)
		_, err = tmpl.Parse(string(content))
		if err != nil {
			panic(err)
		}
		pageTemplates[name] = tmpl
	}
}

// renderTemplate renders a page template with the given data
func renderTemplate(w http.ResponseWriter, name string, data interface{}) error {
	tmpl, ok := pageTemplates[name]
	if !ok {
		return fmt.Errorf("template not found: %s", name)
	}
	// Standalone templates don't use the base template
	if name == "gate_list_fragment.html" || name == "terminal.html" || name == "branch_detail.html" {
		return tmpl.Execute(w, data)
	}
	return tmpl.ExecuteTemplate(w, "base", data)
}

// requireOwner checks if the authenticated user owns the resource.
// Returns the pubkey if authorized, or writes an error and returns empty string.
func (s *Server) requireOwner(w http.ResponseWriter, r *http.Request, repoRef string) string {
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return ""
	}

	owner, _, err := repo.ParseRepoRef(repoRef)
	if err != nil {
		http.Error(w, "Invalid repo reference", http.StatusBadRequest)
		return ""
	}

	if owner != pubkey {
		http.Error(w, "Forbidden: not owner", http.StatusForbidden)
		return ""
	}

	return pubkey
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	// Serve from embedded static files
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	// Set correct MIME types for common file extensions
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, ".js"):
		w.Header().Set("Content-Type", "application/javascript")
	case strings.HasSuffix(path, ".css"):
		w.Header().Set("Content-Type", "text/css")
	case strings.HasSuffix(path, ".ttf"):
		w.Header().Set("Content-Type", "font/ttf")
	case strings.HasSuffix(path, ".woff"):
		w.Header().Set("Content-Type", "font/woff")
	case strings.HasSuffix(path, ".woff2"):
		w.Header().Set("Content-Type", "font/woff2")
	case strings.HasSuffix(path, ".map"):
		w.Header().Set("Content-Type", "application/json")
	}

	http.StripPrefix("/static/", http.FileServer(http.FS(sub))).ServeHTTP(w, r)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/repos", http.StatusTemporaryRedirect)
}

func (s *Server) handleNewRepo(w http.ResponseWriter, r *http.Request) {
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Check if user has SSH keys
	sshKeyStore := auth.NewSSHKeyStore(s.db, s.cfg.Server.DataDir)
	keys, _ := sshKeyStore.List(pubkey)
	if len(keys) == 0 {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}

	data := s.baseTemplateData(r, "New Repository")
	if err := renderTemplate(w, "new_repo.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleNewRepoCreate(w http.ResponseWriter, r *http.Request) {
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		data := s.baseTemplateData(r, "New Repository")
		data["Error"] = "Invalid form data"
		renderTemplate(w, "new_repo.html", data)
		return
	}

	name := r.FormValue("name")
	url := r.FormValue("url")

	if name == "" {
		data := s.baseTemplateData(r, "New Repository")
		data["Error"] = "Repository name is required"
		renderTemplate(w, "new_repo.html", data)
		return
	}

	store := repo.NewStore(s.cfg.Server.DataDir)
	var rp *repo.Repo
	var err error

	if url != "" {
		rp, err = store.Clone(pubkey, name, url)
	} else {
		rp, err = store.Create(pubkey, name)
	}

	if err != nil {
		data := s.baseTemplateData(r, "New Repository")
		data["Error"] = err.Error()
		renderTemplate(w, "new_repo.html", data)
		return
	}

	http.Redirect(w, r, "/repos/"+rp.Owner+"/"+rp.Name, http.StatusSeeOther)
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	s.renderSettings(w, r, "")
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, sshKeyError string) {
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	sshKeyStore := auth.NewSSHKeyStore(s.db, s.cfg.Server.DataDir)
	sshKeys, err := sshKeyStore.List(pubkey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := s.baseTemplateData(r, "Settings")
	data["SSHKeys"] = sshKeys
	data["SSHKeyError"] = sshKeyError

	if err := renderTemplate(w, "settings.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleSettingsSSHKeyAdd(w http.ResponseWriter, r *http.Request) {
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		s.renderSettings(w, r, "Invalid form data")
		return
	}

	key := r.FormValue("key")
	name := r.FormValue("name")

	if key == "" {
		s.renderSettings(w, r, "SSH key is required")
		return
	}

	store := auth.NewSSHKeyStore(s.db, s.cfg.Server.DataDir)
	if _, err := store.Add(pubkey, key, name); err != nil {
		s.renderSettings(w, r, err.Error())
		return
	}

	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (s *Server) handleSettingsSSHKeyDelete(w http.ResponseWriter, r *http.Request) {
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		s.renderSettings(w, r, "Invalid form data")
		return
	}

	fingerprint := r.FormValue("fingerprint")
	if fingerprint == "" {
		s.renderSettings(w, r, "Fingerprint is required")
		return
	}

	store := auth.NewSSHKeyStore(s.db, s.cfg.Server.DataDir)
	if err := store.Remove(pubkey, fingerprint); err != nil {
		s.renderSettings(w, r, err.Error())
		return
	}

	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (s *Server) handleSettingsDotfiles(w http.ResponseWriter, r *http.Request) {
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	store := dotfiles.NewStore(s.db)
	list, _ := store.List(pubkey)

	data := s.baseTemplateData(r, "Dotfiles Settings")
	data["Dotfiles"] = list

	if err := renderTemplate(w, "settings_dotfiles.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleSettingsDotfilesAdd(w http.ResponseWriter, r *http.Request) {
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	url := r.FormValue("url")

	if name == "" || url == "" {
		http.Error(w, "Name and URL are required", http.StatusBadRequest)
		return
	}

	store := dotfiles.NewStore(s.db)
	d := &dotfiles.Dotfiles{
		Pubkey: pubkey,
		Name:   name,
		URL:    url,
	}
	if err := store.Create(d); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	http.Redirect(w, r, "/settings/dotfiles", http.StatusSeeOther)
}

func (s *Server) handleSettingsDotfilesDelete(w http.ResponseWriter, r *http.Request) {
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	name := chi.URLParam(r, "name")
	store := dotfiles.NewStore(s.db)
	store.Delete(pubkey, name)

	http.Redirect(w, r, "/settings/dotfiles", http.StatusSeeOther)
}

func (s *Server) handleRepoTaskCreate(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	repoRef := owner + "/" + repoName

	// Check ownership
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || pubkey != owner {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	title := r.FormValue("title")
	body := r.FormValue("body")

	if title == "" {
		http.Error(w, "Title is required", http.StatusBadRequest)
		return
	}

	// Generate slug from title
	slug := task.GenerateSlug(title)

	store := task.NewStore(s.db)
	t := &task.Task{
		Repo:  repoRef,
		Slug:  slug,
		Title: title,
		Body:  body,
	}
	if err := store.Create(t); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	http.Redirect(w, r, "/repos/"+owner+"/"+repoName, http.StatusSeeOther)
}

func (s *Server) handleRepoBranchCreate(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	repoRef := owner + "/" + repoName

	// Check ownership
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || pubkey != owner {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	taskSlug := r.FormValue("task_slug")
	dotfiles := r.FormValue("dotfiles")
	backendType := r.FormValue("backend")

	if name == "" {
		http.Error(w, "Branch name is required", http.StatusBadRequest)
		return
	}

	// Get the bare repo path
	repoStore := repo.NewStore(s.cfg.Server.DataDir)
	rp, err := repoStore.Get(owner, repoName)
	if err != nil || rp == nil {
		http.Error(w, "Repository not found", http.StatusNotFound)
		return
	}

	branchStore := branch.NewStore(s.db, s.cfg.Server.DataDir)
	b := &branch.Branch{
		Repo: repoRef,
		Name: name,
	}
	if taskSlug != "" {
		b.TaskRepo = &repoRef
		b.TaskSlug = &taskSlug
	}

	// Create branch with appropriate backend
	switch backendType {
	case "docker":
		if err := branchStore.CreateWithDockerCheckout(b, rp.Path, dotfiles); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	case "modal":
		if err := branchStore.CreateWithModalCheckout(b, rp.Path, dotfiles); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	default:
		if err := branchStore.CreateWithCheckout(b, rp.Path, dotfiles); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	// If linked to a task, set task to in_progress
	if taskSlug != "" {
		taskStore := task.NewStore(s.db)
		taskStore.UpdateStatus(repoRef, taskSlug, task.StatusInProgress)
	}

	http.Redirect(w, r, "/branches/"+owner+"/"+repoName+"/"+name, http.StatusSeeOther)
}

func (s *Server) handleRepoList(w http.ResponseWriter, r *http.Request) {
	ownerFilter := r.URL.Query().Get("owner")

	store := repo.NewStore(s.cfg.Server.DataDir)
	repos, err := store.List(ownerFilter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := s.baseTemplateData(r, "Repositories")
	data["Repos"] = repos
	data["Owner"] = ownerFilter

	if err := renderTemplate(w, "repos.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleRepoDetail(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	name := chi.URLParam(r, "repo")
	repoRef := owner + "/" + name

	store := repo.NewStore(s.cfg.Server.DataDir)
	rp, err := store.Get(owner, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rp == nil {
		http.Error(w, "Repository not found", http.StatusNotFound)
		return
	}

	branchStore := branch.NewStore(s.db, s.cfg.Server.DataDir)
	branches, err := branchStore.List(repoRef, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	taskStore := task.NewStore(s.db)
	tasks, err := taskStore.List(repoRef, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Get recent commits from master
	commits, _ := getRepoCommits(rp.Path, "master", 10)

	data := s.baseTemplateData(r, rp.FullName())
	data["Repo"] = rp
	data["Branches"] = branches
	data["Tasks"] = tasks
	data["Commits"] = commits
	// Check if current user owns this repo
	user := s.getTemplateUser(r)
	data["IsOwner"] = user != nil && user.Pubkey == owner

	// Get user's saved dotfiles for dropdown
	if user != nil {
		dotfilesStore := dotfiles.NewStore(s.db)
		userDotfiles, _ := dotfilesStore.List(user.Pubkey)
		data["Dotfiles"] = userDotfiles
	}

	if err := renderTemplate(w, "repo_detail.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleTaskDetail(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	slug := chi.URLParam(r, "slug")
	repoRef := owner + "/" + repoName

	store := task.NewStore(s.db)
	t, err := store.Get(repoRef, slug)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if t == nil {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}

	// Check for existing branch linked to this task
	branchStore := branch.NewStore(s.db, s.cfg.Server.DataDir)
	branches, _ := branchStore.List(repoRef, "")
	var linkedBranch *branch.Branch
	for i := range branches {
		if branches[i].TaskSlug != nil && *branches[i].TaskSlug == slug {
			linkedBranch = &branches[i]
			break
		}
	}

	data := s.baseTemplateData(r, t.Title)
	data["Task"] = t
	data["LinkedBranch"] = linkedBranch
	// Check if current user owns this repo
	user := s.getTemplateUser(r)
	data["IsOwner"] = user != nil && user.Pubkey == owner

	// Get user's saved dotfiles for dropdown
	if user != nil {
		dotfilesStore := dotfiles.NewStore(s.db)
		userDotfiles, _ := dotfilesStore.List(user.Pubkey)
		data["Dotfiles"] = userDotfiles
	}

	if err := renderTemplate(w, "task_detail.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleTaskEdit(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	slug := chi.URLParam(r, "slug")
	repoRef := owner + "/" + repoName

	// Check ownership
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || pubkey != owner {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	title := r.FormValue("title")
	body := r.FormValue("body")
	priorityStr := r.FormValue("priority")
	status := r.FormValue("status")

	if title == "" {
		http.Error(w, "Title is required", http.StatusBadRequest)
		return
	}

	priority := 2 // default
	if priorityStr != "" {
		fmt.Sscanf(priorityStr, "%d", &priority)
	}

	store := task.NewStore(s.db)
	t, err := store.Get(repoRef, slug)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if t == nil {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}

	t.Title = title
	t.Body = body
	t.Priority = priority
	t.Status = status

	if err := store.Update(t); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/tasks/"+owner+"/"+repoName+"/"+slug, http.StatusSeeOther)
}

func (s *Server) handleTaskDelete(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	slug := chi.URLParam(r, "slug")
	repoRef := owner + "/" + repoName

	// Check ownership
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || pubkey != owner {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	store := task.NewStore(s.db)
	if err := store.Delete(repoRef, slug); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/repos/"+owner+"/"+repoName, http.StatusSeeOther)
}

func (s *Server) handleTaskStartBranch(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	slug := chi.URLParam(r, "slug")
	repoRef := owner + "/" + repoName

	// Check ownership
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || pubkey != owner {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	agentType := r.FormValue("agent")
	dotfiles := r.FormValue("dotfiles")
	backendType := r.FormValue("backend")
	if backendType == "" {
		backendType = "local" // default to local
	}
	if agentType != "claude" && agentType != "codex" {
		http.Error(w, "Invalid agent type", http.StatusBadRequest)
		return
	}
	if backendType != "local" && backendType != "docker" && backendType != "modal" {
		http.Error(w, "Invalid backend type", http.StatusBadRequest)
		return
	}

	// Get the repo
	repoStore := repo.NewStore(s.cfg.Server.DataDir)
	rp, err := repoStore.Get(owner, repoName)
	if err != nil || rp == nil {
		http.Error(w, "Repository not found", http.StatusNotFound)
		return
	}

	// Get the task
	taskStore := task.NewStore(s.db)
	t, err := taskStore.Get(repoRef, slug)
	if err != nil || t == nil {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}

	// Check if branch already exists
	branchStore := branch.NewStore(s.db, s.cfg.Server.DataDir)
	existingBranch, _ := branchStore.Get(repoRef, slug)

	var b *branch.Branch
	if existingBranch != nil {
		if existingBranch.Status == branch.StatusActive {
			// Branch already active, just redirect to it
			http.Redirect(w, r, "/branches/"+owner+"/"+repoName+"/"+slug, http.StatusSeeOther)
			return
		}
		// Branch was abandoned/merged, clean it up and recreate
		branchStore.Delete(repoRef, slug)
	}

	// Create branch with checkout, using task slug as branch name
	b = &branch.Branch{
		Repo:     repoRef,
		Name:     slug, // Use task slug as branch name
		TaskRepo: &repoRef,
		TaskSlug: &slug,
	}

	// Create with appropriate backend
	switch backendType {
	case "docker":
		if err := branchStore.CreateWithDockerCheckout(b, rp.Path, dotfiles); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	case "modal":
		if err := branchStore.CreateWithModalCheckout(b, rp.Path, dotfiles); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	default:
		if err := branchStore.CreateWithCheckout(b, rp.Path, dotfiles); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	// Write TASK.md with task description
	taskMdContent := fmt.Sprintf("# %s\n\n%s\n", t.Title, t.Body)
	taskMdPath := filepath.Join(b.Environment.Path, "TASK.md")
	if backendType == "modal" || backendType == "docker" {
		// For remote backends, write via the backend
		backend, err := b.Backend()
		if err != nil {
			http.Error(w, "Failed to get backend: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := backend.WriteFile(r.Context(), taskMdPath, []byte(taskMdContent)); err != nil {
			http.Error(w, "Failed to write TASK.md: "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		// For local backend, write directly
		if err := os.WriteFile(taskMdPath, []byte(taskMdContent), 0644); err != nil {
			http.Error(w, "Failed to write TASK.md: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Create agent session record
	agentStore := agent.NewStore(s.db)
	session := &agent.Session{
		BranchRepo: repoRef,
		BranchName: slug,
		AgentType:  agent.AgentType(agentType),
		Prompt:     "Complete the task described in TASK.md. When done, commit your changes.",
	}
	if err := agentStore.Create(session); err != nil {
		http.Error(w, "Failed to create agent session: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Create the agent command
	cmd, err := agent.Spawn(session.AgentType, b.Environment.Path, session.Prompt, repoRef, slug)
	if err != nil {
		log.Printf("Failed to create agent command: %v", err)
		http.Error(w, "Failed to create agent command: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("Created agent command: %s %v in %s", cmd.Path, cmd.Args, cmd.Dir)

	// Start agent in a PTY so "Open Terminal" can attach to it
	sessionKey := repoRef + "/" + slug
	termSession, err := s.termMgr.Create(sessionKey, cmd)
	if err != nil {
		log.Printf("Failed to start agent PTY: %v", err)
		http.Error(w, "Failed to start agent PTY: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("Started agent terminal session for %s, PID: %d", sessionKey, termSession.PID())

	// Set initial terminal size - Claude needs this to render properly
	termSession.Resize(24, 80)

	// Update session with PID
	pid := termSession.PID()
	session.PID = &pid
	session.Status = agent.StatusRunning
	agentStore.Update(session)

	// Update task status to in_progress
	taskStore.UpdateStatus(repoRef, slug, task.StatusInProgress)

	// Redirect to branch page
	http.Redirect(w, r, "/branches/"+owner+"/"+repoName+"/"+slug, http.StatusSeeOther)
}

func (s *Server) handleBranchDetail(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	name := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName

	branchStore := branch.NewStore(s.db, s.cfg.Server.DataDir)
	b, err := branchStore.Get(repoRef, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if b == nil {
		http.Error(w, "Branch not found", http.StatusNotFound)
		return
	}

	// Get current HEAD of checkout for staleness check
	var currentHead string
	if b.Environment.Path != "" {
		currentHead, _ = getWorkdirHead(b.Environment.Path)
	}

	// Get gate status
	gateStore := gate.NewStore(s.db, s.cfg.Server.DataDir)
	allGateRuns, err := gateStore.ListRuns(repoRef, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Keep only latest run per gate
	latestByGate := make(map[string]*gate.GateRun)
	for i := range allGateRuns {
		r := &allGateRuns[i]
		if existing, ok := latestByGate[r.GateName]; !ok || r.ID > existing.ID {
			latestByGate[r.GateName] = r
		}
	}
	var gateRuns []gate.GateRun
	for _, r := range latestByGate {
		gateRuns = append(gateRuns, *r)
	}

	// Check if all gates pass on current HEAD
	gatesStale := false
	allGatesPass := len(gateRuns) > 0
	for _, r := range gateRuns {
		if r.Rev != currentHead {
			gatesStale = true
		}
		if r.Status != gate.StatusPassed {
			allGatesPass = false
		}
	}
	canMerge := allGatesPass && !gatesStale && len(gateRuns) > 0

	// Get configured gates from cook.toml
	var configuredGates []gate.Gate
	if b.Environment.Path != "" {
		if cfg, err := gate.LoadRepoConfig(b.Environment.Path); err == nil {
			configuredGates = cfg.Gates
		}
	}

	// Get linked task
	var linkedTask *task.Task
	if b.TaskRepo != nil && b.TaskSlug != nil {
		taskStore := task.NewStore(s.db)
		linkedTask, _ = taskStore.Get(*b.TaskRepo, *b.TaskSlug)
	}

	// Check if branch needs rebasing
	needsRebase := false
	if b.Environment.Path != "" {
		needsRebase = isBranchBehindMaster(b.Environment.Path)
	}

	data := s.baseTemplateData(r, b.FullName())
	data["Branch"] = b
	data["GateRuns"] = gateRuns
	data["ConfiguredGates"] = configuredGates
	data["Task"] = linkedTask
	data["Owner"] = owner
	data["Repo"] = repoName
	data["NeedsRebase"] = needsRebase
	data["CurrentHead"] = currentHead
	data["GatesStale"] = gatesStale
	data["CanMerge"] = canMerge
	// Check if current user owns this repo
	user := s.getTemplateUser(r)
	data["IsOwner"] = user != nil && user.Pubkey == owner

	if err := renderTemplate(w, "branch_detail.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleBranchRunGates(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	name := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName

	// Check ownership
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || pubkey != owner {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Get branch
	branchStore := branch.NewStore(s.db, s.cfg.Server.DataDir)
	b, err := branchStore.Get(repoRef, name)
	if err != nil || b == nil {
		http.Error(w, "Branch not found", http.StatusNotFound)
		return
	}

	if b.Environment.Path == "" {
		http.Error(w, "Branch has no checkout", http.StatusBadRequest)
		return
	}

	// Load gate config
	cfg, err := gate.LoadRepoConfig(b.Environment.Path)
	if err != nil {
		http.Error(w, "Failed to load cook.toml: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(cfg.Gates) == 0 {
		http.Error(w, "No gates configured in cook.toml", http.StatusBadRequest)
		return
	}

	// Get current HEAD rev from the actual checkout
	rev, err := getWorkdirHead(b.Environment.Path)
	if err != nil {
		http.Error(w, "Failed to get current HEAD: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Run all gates
	gateStore := gate.NewStore(s.db, s.cfg.Server.DataDir)
	for _, g := range cfg.Gates {
		if g.Command == "" {
			continue
		}
		gateStore.RunGate(g, repoRef, name, rev, b.Environment.Path)
	}

	http.Redirect(w, r, "/branches/"+owner+"/"+repoName+"/"+name, http.StatusSeeOther)
}

func (s *Server) handleBranchMerge(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	name := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName

	// Check ownership
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || pubkey != owner {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Get repo
	repoStore := repo.NewStore(s.cfg.Server.DataDir)
	rp, err := repoStore.Get(owner, repoName)
	if err != nil || rp == nil {
		http.Error(w, "Repository not found", http.StatusNotFound)
		return
	}

	// Get branch
	branchStore := branch.NewStore(s.db, s.cfg.Server.DataDir)
	b, err := branchStore.Get(repoRef, name)
	if err != nil || b == nil {
		http.Error(w, "Branch not found", http.StatusNotFound)
		return
	}

	if b.Status != branch.StatusActive {
		http.Error(w, "Branch is not active", http.StatusBadRequest)
		return
	}

	// Verify all gates pass on current HEAD
	if b.Environment.Path != "" {
		currentHead, _ := getWorkdirHead(b.Environment.Path)
		gateStore := gate.NewStore(s.db, s.cfg.Server.DataDir)
		allGateRuns, _ := gateStore.ListRuns(repoRef, name)

		// Get configured gates
		cfg, _ := gate.LoadRepoConfig(b.Environment.Path)
		requiredGates := make(map[string]bool)
		for _, g := range cfg.Gates {
			if g.Command != "" {
				requiredGates[g.Name] = true
			}
		}

		// Check latest run per gate
		latestByGate := make(map[string]*gate.GateRun)
		for i := range allGateRuns {
			r := &allGateRuns[i]
			if existing, ok := latestByGate[r.GateName]; !ok || r.ID > existing.ID {
				latestByGate[r.GateName] = r
			}
		}

		// Verify all required gates pass on current HEAD
		for gateName := range requiredGates {
			run, ok := latestByGate[gateName]
			if !ok {
				http.Error(w, fmt.Sprintf("Gate '%s' has not been run", gateName), http.StatusBadRequest)
				return
			}
			if run.Status != gate.StatusPassed {
				http.Error(w, fmt.Sprintf("Gate '%s' has not passed (status: %s)", gateName, run.Status), http.StatusBadRequest)
				return
			}
			if run.Rev != currentHead {
				http.Error(w, fmt.Sprintf("Gate '%s' was run on commit %s but current HEAD is %s - please re-run gates", gateName, run.Rev[:8], currentHead[:8]), http.StatusBadRequest)
				return
			}
		}
	}

	// Fast-forward merge: check if master hasn't changed since base rev
	// Update master to branch HEAD rev
	if err := mergeBranchFastForward(rp.Path, b); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Kill the agent PTY if running
	sessionKey := repoRef + "/" + name
	s.termMgr.Remove(sessionKey)

	// Remove the checkout
	branchStore.RemoveCheckout(b)

	// Update branch status
	branchStore.UpdateStatus(repoRef, name, branch.StatusMerged)

	// Close linked task
	if b.TaskRepo != nil && b.TaskSlug != nil {
		taskStore := task.NewStore(s.db)
		taskStore.UpdateStatus(*b.TaskRepo, *b.TaskSlug, task.StatusClosed)
	}

	http.Redirect(w, r, "/repos/"+owner+"/"+repoName, http.StatusSeeOther)
}

func (s *Server) handleBranchRebase(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	name := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName

	// Check ownership
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || pubkey != owner {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Get repo
	repoStore := repo.NewStore(s.cfg.Server.DataDir)
	rp, err := repoStore.Get(owner, repoName)
	if err != nil || rp == nil {
		http.Error(w, "Repository not found", http.StatusNotFound)
		return
	}

	// Get branch
	branchStore := branch.NewStore(s.db, s.cfg.Server.DataDir)
	b, err := branchStore.Get(repoRef, name)
	if err != nil || b == nil {
		http.Error(w, "Branch not found", http.StatusNotFound)
		return
	}

	if b.Environment.Path == "" {
		http.Error(w, "Branch has no checkout", http.StatusBadRequest)
		return
	}

	// Fetch and rebase
	fetchCmd := exec.Command("git", "-C", b.Environment.Path, "fetch", "origin")
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		http.Error(w, fmt.Sprintf("git fetch failed: %s", string(output)), http.StatusInternalServerError)
		return
	}

	rebaseCmd := exec.Command("git", "-C", b.Environment.Path, "rebase", "origin/master")
	if output, err := rebaseCmd.CombinedOutput(); err != nil {
		http.Error(w, fmt.Sprintf("git rebase failed: %s", string(output)), http.StatusInternalServerError)
		return
	}

	// Update base_rev to current master
	masterRev, err := getBareRepoHead(rp.Path, "master")
	if err != nil {
		http.Error(w, "Failed to get master HEAD", http.StatusInternalServerError)
		return
	}
	branchStore.UpdateBaseRev(repoRef, name, masterRev)

	http.Redirect(w, r, "/branches/"+owner+"/"+repoName+"/"+name, http.StatusSeeOther)
}

func (s *Server) handleBranchAbandon(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	name := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName
	log.Printf("handleBranchAbandon: owner=%s repo=%s name=%s", owner, repoName, name)

	// Check ownership
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || pubkey != owner {
		log.Printf("handleBranchAbandon: forbidden - pubkey=%s owner=%s", pubkey, owner)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Get repo
	repoStore := repo.NewStore(s.cfg.Server.DataDir)
	rp, err := repoStore.Get(owner, repoName)
	if err != nil || rp == nil {
		http.Error(w, "Repository not found", http.StatusNotFound)
		return
	}

	// Get branch
	branchStore := branch.NewStore(s.db, s.cfg.Server.DataDir)
	b, err := branchStore.Get(repoRef, name)
	if err != nil || b == nil {
		http.Error(w, "Branch not found", http.StatusNotFound)
		return
	}

	// Kill the agent PTY if running
	sessionKey := repoRef + "/" + name
	s.termMgr.Remove(sessionKey)

	// Remove the checkout (ignore errors - might not exist)
	branchStore.RemoveCheckout(b)

	// Delete related records first (FK constraints)
	s.db.Exec(`DELETE FROM gate_runs WHERE branch_repo = ? AND branch_name = ?`, repoRef, name)
	s.db.Exec(`DELETE FROM agent_sessions WHERE branch_repo = ? AND branch_name = ?`, repoRef, name)
	s.db.Exec(`DELETE FROM terminal_tabs WHERE branch_repo = ? AND branch_name = ?`, repoRef, name)

	// Delete branch from DB
	if err := branchStore.Delete(repoRef, name); err != nil {
		log.Printf("handleBranchAbandon: failed to delete branch: %v", err)
	}

	// Reset linked task to open
	if b.TaskRepo != nil && b.TaskSlug != nil {
		taskStore := task.NewStore(s.db)
		taskStore.UpdateStatus(*b.TaskRepo, *b.TaskSlug, task.StatusOpen)
	}

	http.Redirect(w, r, "/repos/"+owner+"/"+repoName, http.StatusSeeOther)
}

func (s *Server) handleBranchTabList(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	name := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName

	tabStore := terminal.NewTabStore(s.db)
	tabs, err := tabStore.ListByBranch(repoRef, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tabs)
}

func (s *Server) handleBranchTabCreate(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	name := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName

	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || pubkey != owner {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	tab := &terminal.Tab{
		ID:         req.ID,
		BranchRepo: repoRef,
		BranchName: name,
		Name:       req.Name,
	}

	tabStore := terminal.NewTabStore(s.db)
	if err := tabStore.Create(tab); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(tab)
}

func (s *Server) handleBranchTabDelete(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	tabId := chi.URLParam(r, "tabId")

	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || pubkey != owner {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	tabStore := terminal.NewTabStore(s.db)
	if err := tabStore.Delete(tabId); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleBranchPreviewList(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	name := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName

	store := terminal.NewPreviewTabStore(s.db)
	tabs, err := store.ListByBranch(repoRef, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tabs)
}

func (s *Server) handleBranchPreviewCreate(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	name := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName

	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || pubkey != owner {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	tab := &terminal.PreviewTab{
		ID:           req.ID,
		BranchRepo:   repoRef,
		BranchName:   name,
		Name:         req.Name,
		History:      []string{},
		HistoryIndex: -1,
	}

	store := terminal.NewPreviewTabStore(s.db)
	if err := store.Create(tab); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(tab)
}

func (s *Server) handleBranchPreviewUpdate(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	previewId := chi.URLParam(r, "previewId")

	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || pubkey != owner {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	var req struct {
		CurrentURL   string   `json:"current_url"`
		History      []string `json:"history"`
		HistoryIndex int      `json:"history_index"`
		Name         string   `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	store := terminal.NewPreviewTabStore(s.db)
	tab, err := store.Get(previewId)
	if err != nil || tab == nil {
		http.Error(w, "Preview tab not found", http.StatusNotFound)
		return
	}

	tab.CurrentURL = req.CurrentURL
	tab.History = req.History
	tab.HistoryIndex = req.HistoryIndex
	if req.Name != "" {
		tab.Name = req.Name
	}

	if err := store.Update(tab); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tab)
}

func (s *Server) handleBranchPreviewDelete(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	previewId := chi.URLParam(r, "previewId")

	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || pubkey != owner {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	store := terminal.NewPreviewTabStore(s.db)
	if err := store.Delete(previewId); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleBranchEditorList(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	branchName := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName

	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || pubkey != owner {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	store := editor.NewTabStore(s.db)
	tabs, err := store.ListByBranch(repoRef, branchName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tabs)
}

func (s *Server) handleBranchEditorCreate(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	branchName := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName

	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || pubkey != owner {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	tab := &editor.Tab{
		ID:         req.ID,
		BranchRepo: repoRef,
		BranchName: branchName,
		Name:       req.Name,
		Path:       "",
		ViewState:  json.RawMessage(`{}`),
	}

	store := editor.NewTabStore(s.db)
	if err := store.Create(tab); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(tab)
}

func (s *Server) handleBranchEditorUpdate(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	branchName := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName
	editorId := chi.URLParam(r, "editorId")

	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || pubkey != owner {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	var req struct {
		Name      string          `json:"name"`
		Path      string          `json:"path"`
		ViewState json.RawMessage `json:"view_state_json"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	store := editor.NewTabStore(s.db)
	tab, err := store.Get(editorId)
	if err != nil || tab == nil {
		http.Error(w, "Editor tab not found", http.StatusNotFound)
		return
	}
	if tab.BranchRepo != repoRef || tab.BranchName != branchName {
		http.Error(w, "Editor tab not found", http.StatusNotFound)
		return
	}

	if req.Name != "" {
		tab.Name = req.Name
	}
	tab.Path = req.Path
	tab.ViewState = req.ViewState

	if err := store.Update(tab); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tab)
}

func (s *Server) handleBranchEditorDelete(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	branchName := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName
	editorId := chi.URLParam(r, "editorId")

	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || pubkey != owner {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	store := editor.NewTabStore(s.db)
	tab, err := store.Get(editorId)
	if err != nil || tab == nil {
		http.Error(w, "Editor tab not found", http.StatusNotFound)
		return
	}
	if tab.BranchRepo != repoRef || tab.BranchName != branchName {
		http.Error(w, "Editor tab not found", http.StatusNotFound)
		return
	}

	if err := store.Delete(editorId); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

var errInvalidPath = errors.New("invalid path")

func cleanRelPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errInvalidPath
	}
	if strings.Contains(p, "\x00") {
		return "", errInvalidPath
	}
	p = filepath.Clean(filepath.FromSlash(p))
	if p == "." || p == ".." {
		return "", errInvalidPath
	}
	if filepath.IsAbs(p) {
		return "", errInvalidPath
	}
	if strings.HasPrefix(p, ".."+string(filepath.Separator)) {
		return "", errInvalidPath
	}
	return p, nil
}

func isWithinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (s *Server) handleBranchFileGet(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	branchName := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName

	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || pubkey != owner {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	requestedPath, err := cleanRelPath(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	branchStore := branch.NewStore(s.db, s.cfg.Server.DataDir)
	b, err := branchStore.Get(repoRef, branchName)
	if err != nil || b == nil {
		http.Error(w, "Branch not found", http.StatusNotFound)
		return
	}
	if b.Environment.Path == "" {
		http.Error(w, "Branch has no checkout", http.StatusBadRequest)
		return
	}

	root, err := filepath.EvalSymlinks(b.Environment.Path)
	if err != nil {
		http.Error(w, "Invalid checkout path", http.StatusInternalServerError)
		return
	}

	absCandidate := filepath.Join(root, requestedPath)
	absPath, err := filepath.EvalSymlinks(absCandidate)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	if !isWithinRoot(root, absPath) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	info, err := os.Stat(absPath)
	if err != nil || info.IsDir() {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	const maxBytes = 2 << 20 // 2 MiB
	if info.Size() > maxBytes {
		http.Error(w, "File too large", http.StatusRequestEntityTooLarge)
		return
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		http.Error(w, "Failed to read file", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(data)
}

func (s *Server) handleBranchFilePut(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	branchName := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName

	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || pubkey != owner {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	requestedPath, err := cleanRelPath(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	branchStore := branch.NewStore(s.db, s.cfg.Server.DataDir)
	b, err := branchStore.Get(repoRef, branchName)
	if err != nil || b == nil {
		http.Error(w, "Branch not found", http.StatusNotFound)
		return
	}
	if b.Environment.Path == "" {
		http.Error(w, "Branch has no checkout", http.StatusBadRequest)
		return
	}

	root, err := filepath.EvalSymlinks(b.Environment.Path)
	if err != nil {
		http.Error(w, "Invalid checkout path", http.StatusInternalServerError)
		return
	}

	absCandidate := filepath.Join(root, requestedPath)
	dirCandidate := filepath.Dir(absCandidate)
	resolvedDir, err := filepath.EvalSymlinks(dirCandidate)
	if err != nil || !isWithinRoot(root, resolvedDir) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if st, err := os.Stat(resolvedDir); err != nil || !st.IsDir() {
		http.Error(w, "Directory not found", http.StatusBadRequest)
		return
	}

	if info, err := os.Lstat(absCandidate); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			http.Error(w, "Refusing to write symlink", http.StatusBadRequest)
			return
		}
	}

	const maxBytes = 2 << 20 // 2 MiB
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	if int64(len(body)) > maxBytes {
		http.Error(w, "File too large", http.StatusRequestEntityTooLarge)
		return
	}

	absPath := filepath.Join(resolvedDir, filepath.Base(absCandidate))
	if !isWithinRoot(root, absPath) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if err := os.WriteFile(absPath, body, 0644); err != nil {
		http.Error(w, "Failed to write file", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleBranchFileList(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	branchName := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName

	branchStore := branch.NewStore(s.db, s.cfg.Server.DataDir)
	b, err := branchStore.Get(repoRef, branchName)
	if err != nil || b == nil {
		http.Error(w, "Branch not found", http.StatusNotFound)
		return
	}
	if b.Environment.Path == "" {
		http.Error(w, "Branch has no checkout", http.StatusBadRequest)
		return
	}

	// List all files (excluding .git and common ignored dirs)
	var files []string
	err = filepath.Walk(b.Environment.Path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}
		// Skip directories
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" || name == ".next" || name == "dist" || name == "build" {
				return filepath.SkipDir
			}
			return nil
		}
		// Get relative path
		relPath, err := filepath.Rel(b.Environment.Path, path)
		if err != nil {
			return nil
		}
		files = append(files, relPath)
		return nil
	})
	if err != nil {
		http.Error(w, "Failed to list files", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(files)
}

// SearchResult represents a single search match
type SearchResult struct {
	Path       string `json:"path"`
	LineNumber int    `json:"line_number"`
	Content    string `json:"content"`
}

// SearchResponse is the response for file search
type SearchResponse struct {
	Results   []SearchResult `json:"results"`
	Truncated bool           `json:"truncated"`
}

func (s *Server) handleBranchFileSearch(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	branchName := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName

	branchStore := branch.NewStore(s.db, s.cfg.Server.DataDir)
	b, err := branchStore.Get(repoRef, branchName)
	if err != nil || b == nil {
		http.Error(w, "Branch not found", http.StatusNotFound)
		return
	}
	if b.Environment.Path == "" {
		http.Error(w, "Branch has no checkout", http.StatusBadRequest)
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SearchResponse{Results: []SearchResult{}})
		return
	}

	useRegex := r.URL.Query().Get("regex") == "true"
	maxResults := 100

	// Try ripgrep first
	results, truncated, err := searchWithRipgrep(b.Environment.Path, query, useRegex, maxResults)
	if err != nil {
		// Fall back to Go implementation
		results, truncated = searchWithGo(b.Environment.Path, query, maxResults)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SearchResponse{Results: results, Truncated: truncated})
}

func searchWithRipgrep(rootPath, query string, useRegex bool, maxResults int) ([]SearchResult, bool, error) {
	args := []string{
		"--json",
		"--max-count", "5", // Max matches per file
		"-g", "!.git",
		"-g", "!node_modules",
		"-g", "!vendor",
		"-g", "!.next",
		"-g", "!dist",
		"-g", "!build",
	}
	if !useRegex {
		args = append(args, "-F") // Fixed string (literal)
	}
	args = append(args, query, rootPath)

	cmd := exec.Command("rg", args...)
	output, err := cmd.Output()
	if err != nil {
		// rg returns exit code 1 when no matches found, which is not an error
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return []SearchResult{}, false, nil
		}
		return nil, false, err
	}

	var results []SearchResult
	scanner := strings.NewReader(string(output))
	decoder := json.NewDecoder(scanner)
	truncated := false

	for {
		var msg map[string]interface{}
		if err := decoder.Decode(&msg); err != nil {
			break
		}

		msgType, _ := msg["type"].(string)
		if msgType != "match" {
			continue
		}

		data, ok := msg["data"].(map[string]interface{})
		if !ok {
			continue
		}

		pathObj, _ := data["path"].(map[string]interface{})
		pathText, _ := pathObj["text"].(string)

		// Get relative path
		relPath, err := filepath.Rel(rootPath, pathText)
		if err != nil {
			relPath = pathText
		}

		lineNumber := 0
		if ln, ok := data["line_number"].(float64); ok {
			lineNumber = int(ln)
		}

		linesObj, _ := data["lines"].(map[string]interface{})
		lineText, _ := linesObj["text"].(string)
		lineText = strings.TrimRight(lineText, "\n\r")

		// Truncate long lines
		if len(lineText) > 200 {
			lineText = lineText[:200] + "..."
		}

		results = append(results, SearchResult{
			Path:       relPath,
			LineNumber: lineNumber,
			Content:    lineText,
		})

		if len(results) >= maxResults {
			truncated = true
			break
		}
	}

	return results, truncated, nil
}

func searchWithGo(rootPath, query string, maxResults int) ([]SearchResult, bool) {
	var results []SearchResult
	truncated := false
	queryLower := strings.ToLower(query)

	filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || truncated {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" || name == ".next" || name == "dist" || name == "build" {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip binary files and large files
		if info.Size() > 1<<20 { // 1MB
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		relPath, _ := filepath.Rel(rootPath, path)
		lines := strings.Split(string(content), "\n")
		matchCount := 0

		for i, line := range lines {
			if strings.Contains(strings.ToLower(line), queryLower) {
				lineText := strings.TrimRight(line, "\r")
				if len(lineText) > 200 {
					lineText = lineText[:200] + "..."
				}
				results = append(results, SearchResult{
					Path:       relPath,
					LineNumber: i + 1,
					Content:    lineText,
				})
				matchCount++
				if matchCount >= 5 || len(results) >= maxResults {
					break
				}
			}
		}

		if len(results) >= maxResults {
			truncated = true
		}
		return nil
	})

	return results, truncated
}

func (s *Server) handleBranchLSPList(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	branchName := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName

	branchStore := branch.NewStore(s.db, s.cfg.Server.DataDir)
	b, err := branchStore.Get(repoRef, branchName)
	if err != nil || b == nil {
		http.Error(w, "Branch not found", http.StatusNotFound)
		return
	}
	if b.Environment.Path == "" {
		http.Error(w, "Branch has no checkout", http.StatusBadRequest)
		return
	}

	lsps, err := DiscoverAvailableLSPs(b.Environment.Path)
	if err != nil {
		// No flake.nix or error reading - return empty list
		lsps = []string{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(lsps)
}

// mergeBranchFastForward performs a fast-forward merge of branch to master
func mergeBranchFastForward(bareRepoPath string, b *branch.Branch) error {
	// Check if branch can fast-forward (origin/master is ancestor of branch HEAD)
	if !canFastForwardMerge(b.Environment.Path) {
		return fmt.Errorf("branch is behind master; please rebase first")
	}

	// Push the branch to origin/master
	cmd := exec.Command("git", "-C", b.Environment.Path, "push", "origin", b.Name+":master")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to push to master: %s: %w", string(output), err)
	}

	return nil
}

func getBareRepoHead(repoPath, ref string) (string, error) {
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", ref)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func getWorkdirHead(workdirPath string) (string, error) {
	cmd := exec.Command("git", "-C", workdirPath, "rev-parse", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// isBranchBehindMaster checks if the branch needs rebasing
// Returns true if master has commits that aren't in the branch
func isBranchBehindMaster(checkoutPath string) bool {
	// Fetch latest from origin first
	exec.Command("git", "-C", checkoutPath, "fetch", "origin").Run()

	// Check if origin/master has commits not in HEAD
	cmd := exec.Command("git", "-C", checkoutPath, "rev-list", "--count", "HEAD..origin/master")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	count := strings.TrimSpace(string(output))
	return count != "0"
}

// canFastForwardMerge checks if branch can be merged to master via fast-forward
func canFastForwardMerge(checkoutPath string) bool {
	// Check if HEAD is ahead of origin/master (i.e., origin/master is ancestor of HEAD)
	cmd := exec.Command("git", "-C", checkoutPath, "merge-base", "--is-ancestor", "origin/master", "HEAD")
	return cmd.Run() == nil
}

type Commit struct {
	Hash    string
	Short   string
	Subject string
	Author  string
	Date    string
}

func getRepoCommits(repoPath, ref string, limit int) ([]Commit, error) {
	// Format: hash|short|subject|author|date
	format := "%H|%h|%s|%an|%ar"
	cmd := exec.Command("git", "-C", repoPath, "log", ref, fmt.Sprintf("-n%d", limit), fmt.Sprintf("--format=%s", format))
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var commits []Commit
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 5)
		if len(parts) < 5 {
			continue
		}
		commits = append(commits, Commit{
			Hash:    parts[0],
			Short:   parts[1],
			Subject: parts[2],
			Author:  parts[3],
			Date:    parts[4],
		})
	}
	return commits, nil
}

// API handlers for Datastar
func (s *Server) apiTaskList(w http.ResponseWriter, r *http.Request) {
	repoFilter := r.URL.Query().Get("repo")
	statusFilter := r.URL.Query().Get("status")

	store := task.NewStore(s.db)
	tasks, err := store.List(repoFilter, statusFilter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tasks)
}

func (s *Server) apiTaskCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	repo := r.FormValue("repo")
	slug := r.FormValue("slug")
	title := r.FormValue("title")
	body := r.FormValue("body")

	if repo == "" || slug == "" || title == "" {
		http.Error(w, "repo, slug, and title are required", http.StatusBadRequest)
		return
	}

	// Check ownership
	if s.requireOwner(w, r, repo) == "" {
		return
	}

	store := task.NewStore(s.db)
	t := &task.Task{
		Repo:  repo,
		Slug:  slug,
		Title: title,
		Body:  body,
	}
	if err := store.Create(t); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Redirect to task detail page
	http.Redirect(w, r, fmt.Sprintf("/tasks/%s/%s", repo, slug), http.StatusSeeOther)
}

func (s *Server) apiBranchList(w http.ResponseWriter, r *http.Request) {
	repoFilter := r.URL.Query().Get("repo")
	statusFilter := r.URL.Query().Get("status")

	store := branch.NewStore(s.db, s.cfg.Server.DataDir)
	branches, err := store.List(repoFilter, statusFilter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(branches)
}

func (s *Server) apiActiveBranches(w http.ResponseWriter, r *http.Request) {
	branchStore := branch.NewStore(s.db, s.cfg.Server.DataDir)
	branches, err := branchStore.List("", branch.StatusActive)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type branchInfo struct {
		Repo string `json:"repo"`
		Name string `json:"name"`
	}

	result := make([]branchInfo, 0, len(branches))
	for _, b := range branches {
		result = append(result, branchInfo{Repo: b.Repo, Name: b.Name})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (s *Server) apiBranchGates(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	name := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName

	gateStore := gate.NewStore(s.db, s.cfg.Server.DataDir)
	runs, err := gateStore.ListRuns(repoRef, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Keep only the latest run per gate
	latestByGate := make(map[string]*gate.GateRun)
	for i := range runs {
		r := &runs[i]
		if existing, ok := latestByGate[r.GateName]; !ok || r.ID > existing.ID {
			latestByGate[r.GateName] = r
		}
	}
	var latestRuns []gate.GateRun
	for _, r := range latestByGate {
		latestRuns = append(latestRuns, *r)
	}

	// Return as HTML fragment for Datastar
	data := map[string]interface{}{
		"GateRuns": latestRuns,
	}

	if err := renderTemplate(w, "gate_list_fragment.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleTerminalPage(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	name := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName

	// Terminal access requires ownership
	if s.requireOwner(w, r, repoRef) == "" {
		return
	}

	data := map[string]interface{}{
		"Owner":  owner,
		"Repo":   repoName,
		"Branch": name,
	}

	if err := renderTemplate(w, "terminal.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// SSE handlers for real-time updates
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	if !s.eventBus.IsActive() {
		http.Error(w, "Event streaming not available (NATS not configured)", http.StatusServiceUnavailable)
		return
	}

	// Get authenticated user's pubkey to filter events
	pubkey := auth.GetPubkey(r.Context())

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Subscribe to all events, but filter by owner
	eventCh := make(chan events.Event, 100)
	unsubscribe, err := s.eventBus.Subscribe("cook.>", func(e events.Event) {
		// Filter: only show events for repos owned by the authenticated user
		// If auth is disabled (no pubkey), show all events
		if pubkey != "" && e.Repo != "" {
			owner, _, err := repo.ParseRepoRef(e.Repo)
			if err != nil || owner != pubkey {
				return
			}
		}
		select {
		case eventCh <- e:
		default:
			// Drop event if channel full
		}
	})
	if err != nil {
		http.Error(w, "Failed to subscribe", http.StatusInternalServerError)
		return
	}
	defer unsubscribe()

	// Send events as SSE
	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-eventCh:
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data)
			flusher.Flush()
		}
	}
}

func (s *Server) apiBranchPreviewNavigate(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	branchName := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName

	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.URL == "" {
		http.Error(w, "URL is required", http.StatusBadRequest)
		return
	}

	// Publish event to SSE subscribers
	if s.eventBus.IsActive() {
		s.eventBus.Publish(events.Event{
			Type:   "preview_navigate",
			Repo:   repoRef,
			Branch: branchName,
			Data: map[string]interface{}{
				"url": req.URL,
			},
		})
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleBranchSSE(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	branchName := chi.URLParam(r, "branch")
	repoRef := owner + "/" + repoName

	if !s.eventBus.IsActive() {
		http.Error(w, "Event streaming not available (NATS not configured)", http.StatusServiceUnavailable)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Subscribe to branch-specific events
	eventCh := make(chan events.Event, 100)
	unsubscribe, err := s.eventBus.SubscribeBranch(repoRef, branchName, func(e events.Event) {
		select {
		case eventCh <- e:
		default:
		}
	})
	if err != nil {
		http.Error(w, "Failed to subscribe", http.StatusInternalServerError)
		return
	}
	defer unsubscribe()

	// Send events as SSE
	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-eventCh:
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data)
			flusher.Flush()
		}
	}
}
