// Command flashcards-api serves the flashcards study API.
package main

import (
	"log"
	"net/http"
	"os"

	"flashcards/backend/internal/api"
	"flashcards/backend/internal/store"
)

func main() {
	dbPath := getenv("FLASHCARDS_DB", "../flashcards.db")
	addr := ":" + getenv("PORT", "8080")

	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("open store (%s): %v", dbPath, err)
	}
	defer st.Close()

	srv := &api.Server{Store: st}
	log.Printf("flashcards-api listening on %s (db=%s)", addr, dbPath)
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
