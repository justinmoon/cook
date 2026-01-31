package main

import (
	"fmt"

	"github.com/justinmoon/cook/internal/config"
	"github.com/justinmoon/cook/internal/db"
)

func openDatabase(cfg *config.Config) (*db.DB, error) {
	if cfg.Server.DatabaseURL == "" {
		return nil, fmt.Errorf("COOK_DATABASE_URL is required")
	}
	return db.Open(cfg.Server.DatabaseURL)
}
