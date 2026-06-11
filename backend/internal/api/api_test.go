package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"flashcards/backend/internal/store"
	_ "modernc.org/sqlite"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE flashcards (
			"row" INTEGER, "level" TEXT, "word" TEXT, "part of speech" TEXT,
			"prep. before next infinitive" TEXT, "dictionary" TEXT,
			"pos_category" TEXT, "pos_primary" TEXT, "english" TEXT,
			"spanish_relation" TEXT, "spanish_word" TEXT, "spanish_note" TEXT
		);
		INSERT INTO flashcards ("level","word","part of speech","pos_category","english","spanish_relation","spanish_word","spanish_note")
		VALUES ('Fondamentale','abbandonare','v.tr.','verb','to abandon','cognate','abandonar','Direct cognate.');
		INSERT INTO flashcards ("level","word","part of speech","pos_category","english","spanish_relation","spanish_word","spanish_note")
		VALUES ('Fondamentale','attuale','agg.','adjective','current','false_friend','actual','Means current.');`,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	db.Close()

	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return &Server{Store: st}
}

func TestHealth(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestLevelsEndpoint(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/levels", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var levels []store.Level
	if err := json.Unmarshal(rec.Body.Bytes(), &levels); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(levels) != 1 || levels[0].Count != 2 {
		t.Fatalf("levels = %+v", levels)
	}
}

func TestSessionLifecycle(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Handler()

	// Create.
	body, _ := json.Marshal(createSessionReq{Levels: []string{"Fondamentale"}, Size: 20})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/sessions", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body)
	}
	var sess store.Session
	if err := json.Unmarshal(rec.Body.Bytes(), &sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if len(sess.Cards) != 2 {
		t.Fatalf("want 2 cards, got %d", len(sess.Cards))
	}

	// Answer one card.
	ans, _ := json.Marshal(answerReq{CardID: sess.Cards[0].Card.ID, Correct: true})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/sessions/"+strconv.FormatInt(sess.ID, 10)+"/answers", bytes.NewReader(ans)))
	if rec.Code != http.StatusOK {
		t.Fatalf("answer status = %d body=%s", rec.Code, rec.Body)
	}
	var prog store.Progress
	json.Unmarshal(rec.Body.Bytes(), &prog)
	if prog.Answered != 1 || prog.Correct != 1 {
		t.Fatalf("progress = %+v", prog)
	}

	// Fetch the session back.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/sessions/"+strconv.FormatInt(sess.ID, 10), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d", rec.Code)
	}
}

func TestCreateSessionValidation(t *testing.T) {
	srv := newTestServer(t)
	body, _ := json.Marshal(createSessionReq{Levels: []string{}})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/sessions", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestGetMissingSession(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/sessions/9999", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}
