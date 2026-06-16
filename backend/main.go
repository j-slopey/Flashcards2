// Command flashcards-api serves the flashcards study API.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"flashcards/backend/internal/api"
	"flashcards/backend/internal/store"
	"flashcards/backend/internal/tts"
)

func main() {
	dbPath := getenv("FLASHCARDS_DB", "../flashcards.db")
	addr := ":" + getenv("PORT", "8080")

	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("open store (%s): %v", dbPath, err)
	}
	defer st.Close()

	srv := &api.Server{Store: st, Audio: buildTTS()}
	log.Printf("flashcards-api listening on %s (db=%s)", addr, dbPath)
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}

// buildTTS wires up Gemini pronunciation if GEMINI_API_KEY is set; otherwise it
// returns nil and the frontend falls back to browser speech.
func buildTTS() *tts.Cache {
	key := os.Getenv("GEMINI_API_KEY")
	if key == "" {
		log.Printf("GEMINI_API_KEY not set: pronunciation falls back to browser TTS")
		return nil
	}
	synth, err := tts.NewGeminiSynth(context.Background(), key,
		os.Getenv("TTS_MODEL"), os.Getenv("TTS_VOICE"), os.Getenv("TTS_PROMPT"))
	if err != nil {
		log.Printf("pronunciation TTS disabled: %v", err)
		return nil
	}
	dir := getenv("AUDIO_CACHE_DIR", "audiocache")
	cache, err := tts.NewCache(dir, synth)
	if err != nil {
		log.Printf("pronunciation cache disabled: %v", err)
		return nil
	}
	log.Printf("pronunciation TTS enabled (Gemini, cache=%s)", dir)
	return cache
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
