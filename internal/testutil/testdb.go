package testutil

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/justinmoon/cook/internal/db"
)

// OpenTestDB creates a temporary database for tests.
// Requires COOK_TEST_DATABASE_URL to point at a postgres instance with CREATE DATABASE privileges.
func OpenTestDB(t *testing.T) (*db.DB, func()) {
	t.Helper()

	adminURL := os.Getenv("COOK_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("COOK_TEST_DATABASE_URL not set; skipping postgres-backed test")
	}

	admin, err := sql.Open("pgx", adminURL)
	if err != nil {
		t.Fatalf("failed to open admin database: %v", err)
	}

	if err := admin.Ping(); err != nil {
		admin.Close()
		t.Fatalf("failed to ping admin database: %v", err)
	}

	dbName := fmt.Sprintf("cook_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE " + quoteIdent(dbName)); err != nil {
		admin.Close()
		t.Fatalf("failed to create test database: %v", err)
	}

	cleanupAdmin := func() {
		admin.Exec("DROP DATABASE IF EXISTS " + quoteIdent(dbName))
		admin.Close()
	}

	u, err := url.Parse(adminURL)
	if err != nil {
		cleanupAdmin()
		t.Fatalf("failed to parse COOK_TEST_DATABASE_URL: %v", err)
	}
	u.Path = "/" + dbName

	database, err := db.Open(u.String())
	if err != nil {
		cleanupAdmin()
		t.Fatalf("failed to open test database: %v", err)
	}

	cleanup := func() {
		database.Close()
		cleanupAdmin()
	}

	return database, cleanup
}

func quoteIdent(name string) string {
	return `"` + escapeQuotes(name) + `"`
}

func escapeQuotes(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '"' {
			out = append(out, '"', '"')
		} else {
			out = append(out, r)
		}
	}
	return string(out)
}
