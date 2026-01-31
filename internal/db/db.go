package db

import (
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type DB struct {
	*sql.DB
}

func Open(databaseURL string) (*DB, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, fmt.Errorf("COOK_DATABASE_URL is required")
	}

	sqlDB, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	db := &DB{sqlDB}

	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	if err := db.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return db, nil
}

func (db *DB) migrate() error {
	// Check if we need to migrate from old schema
	var hasOldSchema bool
	row := db.QueryRow(`
		SELECT COUNT(*)
		FROM information_schema.columns
		WHERE table_schema = 'public'
			AND table_name = 'tasks'
			AND column_name = 'id'
			AND data_type = 'text'
	`)
	var count int
	if err := row.Scan(&count); err == nil && count > 0 {
		hasOldSchema = true
	}

	if hasOldSchema {
		if err := db.migrateToRepoScoped(); err != nil {
			return fmt.Errorf("migration to repo-scoped IDs failed: %w", err)
		}
	}

	migrations := []string{
		// Tasks: repo-scoped with slug
		`CREATE TABLE IF NOT EXISTS tasks (
			id BIGSERIAL PRIMARY KEY,
			repo TEXT NOT NULL,
			slug TEXT NOT NULL,
			title TEXT NOT NULL,
			body TEXT NOT NULL DEFAULT '',
			priority INTEGER DEFAULT 3,
			status TEXT DEFAULT 'open',
			depends_on TEXT DEFAULT '[]',
			created_at TIMESTAMPTZ DEFAULT NOW(),
			updated_at TIMESTAMPTZ DEFAULT NOW(),
			UNIQUE(repo, slug)
		)`,

		// Branches: repo-scoped with composite FK to tasks
		`CREATE TABLE IF NOT EXISTS branches (
			id BIGSERIAL PRIMARY KEY,
			repo TEXT NOT NULL,
			name TEXT NOT NULL,
			task_repo TEXT,
			task_slug TEXT,
			base_rev TEXT NOT NULL,
			head_rev TEXT NOT NULL,
			environment_json TEXT NOT NULL DEFAULT '{}',
			status TEXT DEFAULT 'active',
			created_at TIMESTAMPTZ DEFAULT NOW(),
			merged_at TIMESTAMPTZ,
			UNIQUE(repo, name),
			FOREIGN KEY (task_repo, task_slug) REFERENCES tasks(repo, slug)
		)`,

		// Gate runs: reference branches by (repo, name)
		`CREATE TABLE IF NOT EXISTS gate_runs (
			id BIGSERIAL PRIMARY KEY,
			branch_repo TEXT NOT NULL,
			branch_name TEXT NOT NULL,
			gate_name TEXT NOT NULL,
			rev TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			started_at TIMESTAMPTZ,
			finished_at TIMESTAMPTZ,
			exit_code INTEGER,
			log_path TEXT,
			FOREIGN KEY (branch_repo, branch_name) REFERENCES branches(repo, name)
		)`,

		// Agent sessions: reference branches by (repo, name)
		`CREATE TABLE IF NOT EXISTS agent_sessions (
			id BIGSERIAL PRIMARY KEY,
			branch_repo TEXT NOT NULL,
			branch_name TEXT NOT NULL,
			agent_type TEXT NOT NULL,
			prompt TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'starting',
			pid INTEGER,
			exit_code INTEGER,
			started_at TIMESTAMPTZ DEFAULT NOW(),
			ended_at TIMESTAMPTZ,
			FOREIGN KEY (branch_repo, branch_name) REFERENCES branches(repo, name)
		)`,

		`CREATE INDEX IF NOT EXISTS idx_tasks_repo_slug ON tasks(repo, slug)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status)`,
		`CREATE INDEX IF NOT EXISTS idx_branches_repo_name ON branches(repo, name)`,
		`CREATE INDEX IF NOT EXISTS idx_branches_status ON branches(status)`,
		`CREATE INDEX IF NOT EXISTS idx_gate_runs_branch ON gate_runs(branch_repo, branch_name)`,
		`CREATE INDEX IF NOT EXISTS idx_agent_sessions_branch ON agent_sessions(branch_repo, branch_name)`,

		// Auth: sessions for web/API authentication
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			pubkey TEXT NOT NULL,
			created_at BIGINT NOT NULL,
			expires_at BIGINT NOT NULL,
			last_accessed BIGINT
		)`,

		// Auth: SSH keys linked to nostr identity
		`CREATE TABLE IF NOT EXISTS ssh_keys (
			id BIGSERIAL PRIMARY KEY,
			pubkey TEXT NOT NULL,
			ssh_pubkey TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			name TEXT,
			created_at BIGINT DEFAULT (EXTRACT(EPOCH FROM NOW())::bigint)
		)`,

		// Auth: cached nostr profile metadata
		`CREATE TABLE IF NOT EXISTS profiles (
			pubkey TEXT PRIMARY KEY,
			name TEXT,
			picture TEXT,
			fetched_at BIGINT
		)`,

		`CREATE INDEX IF NOT EXISTS idx_sessions_pubkey ON sessions(pubkey)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_ssh_keys_pubkey ON ssh_keys(pubkey)`,
		`CREATE INDEX IF NOT EXISTS idx_ssh_keys_fingerprint ON ssh_keys(fingerprint)`,

		// Terminal tabs: persisted tabs per branch
		`CREATE TABLE IF NOT EXISTS terminal_tabs (
			id TEXT PRIMARY KEY,
			branch_repo TEXT NOT NULL,
			branch_name TEXT NOT NULL,
			name TEXT NOT NULL,
			created_at TIMESTAMPTZ DEFAULT NOW(),
			FOREIGN KEY (branch_repo, branch_name) REFERENCES branches(repo, name) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_terminal_tabs_branch ON terminal_tabs(branch_repo, branch_name)`,

		// Preview tabs: browser-style preview tabs per branch
		`CREATE TABLE IF NOT EXISTS preview_tabs (
			id TEXT PRIMARY KEY,
			branch_repo TEXT NOT NULL,
			branch_name TEXT NOT NULL,
			name TEXT NOT NULL,
			current_url TEXT NOT NULL DEFAULT '',
			history_json TEXT NOT NULL DEFAULT '[]',
			history_index INTEGER NOT NULL DEFAULT -1,
			created_at TIMESTAMPTZ DEFAULT NOW(),
			FOREIGN KEY (branch_repo, branch_name) REFERENCES branches(repo, name) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_preview_tabs_branch ON preview_tabs(branch_repo, branch_name)`,

		// Editor tabs: persisted editor windows per branch
		`CREATE TABLE IF NOT EXISTS editor_tabs (
			id TEXT PRIMARY KEY,
			branch_repo TEXT NOT NULL,
			branch_name TEXT NOT NULL,
			name TEXT NOT NULL,
			path TEXT NOT NULL DEFAULT '',
			view_state_json TEXT NOT NULL DEFAULT '{}',
			created_at TIMESTAMPTZ DEFAULT NOW(),
			FOREIGN KEY (branch_repo, branch_name) REFERENCES branches(repo, name) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_editor_tabs_branch ON editor_tabs(branch_repo, branch_name)`,

		// Dotfiles: user's saved dotfiles repos
		`CREATE TABLE IF NOT EXISTS dotfiles (
			id BIGSERIAL PRIMARY KEY,
			pubkey TEXT NOT NULL,
			name TEXT NOT NULL,
			url TEXT NOT NULL,
			created_at BIGINT DEFAULT (EXTRACT(EPOCH FROM NOW())::bigint),
			UNIQUE(pubkey, name)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_dotfiles_pubkey ON dotfiles(pubkey)`,
	}

	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil {
			return fmt.Errorf("migration failed: %s: %w", m[:50], err)
		}
	}

	// Migrate data from old tables if they exist
	if err := db.MigrateData(); err != nil {
		return fmt.Errorf("data migration failed: %w", err)
	}

	return nil
}

// migrateToRepoScoped migrates from old global-ID schema to repo-scoped schema
func (db *DB) migrateToRepoScoped() error {
	// Rename old tables
	renames := []string{
		`ALTER TABLE tasks RENAME TO tasks_old`,
		`ALTER TABLE branches RENAME TO branches_old`,
		`ALTER TABLE gate_runs RENAME TO gate_runs_old`,
		`ALTER TABLE agent_sessions RENAME TO agent_sessions_old`,
	}
	for _, sql := range renames {
		if _, err := db.Exec(sql); err != nil {
			// Table might not exist, continue
			continue
		}
	}

	// Create new tables (will be done by main migration)
	// Migrate data after tables are created
	return nil
}

// MigrateData migrates data from old tables to new (call after schema migration)
func (db *DB) MigrateData() error {
	// Check if old tables exist
	var oldTable sql.NullString
	if err := db.QueryRow(`SELECT to_regclass('public.tasks_old')`).Scan(&oldTable); err != nil {
		return err
	}
	if !oldTable.Valid {
		return nil // No migration needed
	}

	// Migrate tasks: id becomes slug
	_, err := db.Exec(`
		INSERT INTO tasks (repo, slug, title, body, priority, status, depends_on, created_at, updated_at)
		SELECT repo, id, title, body, priority, status, depends_on, created_at, updated_at FROM tasks_old
		ON CONFLICT DO NOTHING
	`)
	if err != nil {
		return fmt.Errorf("migrate tasks: %w", err)
	}

	// Migrate branches: name stays, add task_repo/task_slug from task_id
	_, err = db.Exec(`
		INSERT INTO branches (repo, name, task_repo, task_slug, base_rev, head_rev, environment_json, status, created_at, merged_at)
		SELECT b.repo, b.name, t.repo, t.id, b.base_rev, b.head_rev, b.environment_json, b.status, b.created_at, b.merged_at
		FROM branches_old b
		LEFT JOIN tasks_old t ON b.task_id = t.id
		ON CONFLICT DO NOTHING
	`)
	if err != nil {
		return fmt.Errorf("migrate branches: %w", err)
	}

	// Migrate gate_runs: branch becomes branch_repo/branch_name
	_, err = db.Exec(`
		INSERT INTO gate_runs (branch_repo, branch_name, gate_name, rev, status, started_at, finished_at, exit_code, log_path)
		SELECT b.repo, b.name, g.gate_name, g.rev, g.status, g.started_at, g.finished_at, g.exit_code, g.log_path
		FROM gate_runs_old g
		JOIN branches_old b ON g.branch = b.name
	`)
	if err != nil {
		return fmt.Errorf("migrate gate_runs: %w", err)
	}

	// Migrate agent_sessions
	_, err = db.Exec(`
		INSERT INTO agent_sessions (branch_repo, branch_name, agent_type, prompt, status, pid, exit_code, started_at, ended_at)
		SELECT b.repo, b.name, a.agent_type, a.prompt, a.status, a.pid, a.exit_code, a.started_at, a.ended_at
		FROM agent_sessions_old a
		JOIN branches_old b ON a.branch = b.name
	`)
	if err != nil {
		return fmt.Errorf("migrate agent_sessions: %w", err)
	}

	// Drop old tables
	drops := []string{
		`DROP TABLE IF EXISTS agent_sessions_old`,
		`DROP TABLE IF EXISTS gate_runs_old`,
		`DROP TABLE IF EXISTS branches_old`,
		`DROP TABLE IF EXISTS tasks_old`,
	}
	for _, sql := range drops {
		db.Exec(sql)
	}

	return nil
}
