package main

import (
	"fmt"

	"github.com/justinmoon/cook/internal/auth"
	"github.com/justinmoon/cook/internal/config"
	"github.com/justinmoon/cook/internal/db"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Printf("config load error: %v\n", err)
		return
	}
	if len(cfg.Server.AllowedPubkeys) > 0 {
		fmt.Printf("warning: allowed_pubkeys set; session may be rejected\n")
	}

	database, err := db.Open(cfg.Server.DataDir)
	if err != nil {
		fmt.Printf("db open error: %v\n", err)
		return
	}
	defer database.Close()

	pubkey := "11b9a89404dbf3034e7e1886ba9dc4c6d376f239a118271bd2ec567a889850ce"
	sessionStore := auth.NewSessionStore(database)
	session, err := sessionStore.Create(pubkey)
	if err != nil {
		fmt.Printf("session create error: %v\n", err)
		return
	}

	fmt.Printf("session=%s\n", session.ID)
}
