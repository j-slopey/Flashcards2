// Package api exposes the flashcards HTTP API over a store.
package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"flashcards/backend/internal/store"
	"flashcards/backend/internal/tts"
)

// Server holds the dependencies the handlers need.
type Server struct {
	Store *store.Store
	// Audio lazily synthesizes word pronunciations. May be nil (TTS disabled),
	// in which case the frontend falls back to browser speech.
	Audio *tts.Cache
}

// Handler builds the routed, CORS-wrapped http.Handler for the API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/levels", s.levels)
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("PUT /api/settings", s.putSettings)
	mux.HandleFunc("POST /api/sessions", s.createSession)
	mux.HandleFunc("GET /api/sessions/{id}", s.getSession)
	mux.HandleFunc("POST /api/sessions/{id}/answers", s.postAnswer)
	mux.HandleFunc("GET /api/audio", s.audio)
	return cors(mux)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) levels(w http.ResponseWriter, _ *http.Request) {
	levels, err := s.Store.Levels()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, levels)
}

func (s *Server) getSettings(w http.ResponseWriter, _ *http.Request) {
	st, err := s.Store.GetSettings()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var st store.Settings
	if err := decode(r, &st); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.Store.SetSettings(st); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

type createSessionReq struct {
	Levels []string `json:"levels"`
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var req createSessionReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Levels) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("levels must not be empty"))
		return
	}
	sess, err := s.Store.NewSession(req.Levels)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	sess, err := s.Store.GetSession(id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

type answerReq struct {
	CardID int64 `json:"card_id"`
	Rating int   `json:"rating"` // 1=Again, 2=Hard, 3=Good, 4=Easy
}

func (s *Server) postAnswer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req answerReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	prog, err := s.Store.RecordReview(id, req.CardID, req.Rating)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, prog)
}

// audio serves a word's pronunciation as WAV, synthesizing+caching on first use.
func (s *Server) audio(w http.ResponseWriter, r *http.Request) {
	word := strings.TrimSpace(r.URL.Query().Get("word"))
	if word == "" || len([]rune(word)) > 64 {
		writeErr(w, http.StatusBadRequest, errors.New("missing or oversized word"))
		return
	}
	if s.Audio == nil {
		writeErr(w, http.StatusServiceUnavailable, tts.ErrUnavailable)
		return
	}
	data, err := s.Audio.Audio(r.Context(), word)
	if errors.Is(err, tts.ErrUnavailable) {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Write(data)
}

// --- helpers ---

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid session id"))
		return 0, false
	}
	return id, true
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// cors allows the Vite dev server (and any local origin) to call the API.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
