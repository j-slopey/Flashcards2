package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"flashcards/backend/internal/store"
	"flashcards/backend/internal/tts"
	_ "modernc.org/sqlite"
)

// stubSynth returns canned audio so the audio endpoint can be tested without a
// real TTS provider or API key.
type stubSynth struct{}

func (stubSynth) Synthesize(_ context.Context, text string) ([]byte, error) {
	return []byte("wav:" + text), nil
}

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
		INSERT INTO flashcards ("level","word","pos_primary","pos_category","english","spanish_relation","spanish_word","spanish_note")
		VALUES ('Fondamentale','abbandonare','v.tr.','verb','to abandon','cognate','abandonar','Direct cognate.');
		INSERT INTO flashcards ("level","word","pos_primary","pos_category","english","spanish_relation","spanish_word","spanish_note")
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
	// The curriculum always reports every level in order; the fixture only fills
	// Fondamentale (the first, always unlocked).
	if len(levels) != 3 {
		t.Fatalf("want 3 curriculum levels, got %+v", levels)
	}
	if levels[0].Level != "Fondamentale" || levels[0].Count != 2 || !levels[0].Unlocked {
		t.Fatalf("first level = %+v", levels[0])
	}
	if levels[1].Unlocked {
		t.Fatalf("second level should be locked until the first is mastered: %+v", levels[1])
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Handler()

	body, _ := json.Marshal(store.Settings{NewCardsPerDay: 7})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/settings", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/settings", nil))
	var st store.Settings
	json.Unmarshal(rec.Body.Bytes(), &st)
	if st.NewCardsPerDay != 7 {
		t.Fatalf("new/day = %d, want 7", st.NewCardsPerDay)
	}
}

func TestSessionLifecycle(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Handler()

	// Create a session (no body; the backend picks levels by curriculum progress,
	// and the default new-card limit covers the 2 fixture cards).
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/sessions", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body)
	}
	var sess store.Session
	if err := json.Unmarshal(rec.Body.Bytes(), &sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if len(sess.Cards) != 2 || sess.NewCount != 2 {
		t.Fatalf("want 2 new cards, got new=%d cards=%d", sess.NewCount, len(sess.Cards))
	}
	if sess.Cards[0].Preview.Good == "" {
		t.Fatalf("expected interval preview, got %+v", sess.Cards[0].Preview)
	}

	// Rate one card "Good".
	ans, _ := json.Marshal(answerReq{CardID: sess.Cards[0].Card.ID, Rating: 3})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/sessions/"+strconv.FormatInt(sess.ID, 10)+"/answers", bytes.NewReader(ans)))
	if rec.Code != http.StatusOK {
		t.Fatalf("answer status = %d body=%s", rec.Code, rec.Body)
	}
	var prog store.Progress
	json.Unmarshal(rec.Body.Bytes(), &prog)
	if prog.Answered != 1 || prog.Remembered != 1 {
		t.Fatalf("progress = %+v", prog)
	}

	// Fetch the session back.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/sessions/"+strconv.FormatInt(sess.ID, 10), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d", rec.Code)
	}
}

func TestAnswerRejectsBadRating(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/sessions", nil))
	var sess store.Session
	json.Unmarshal(rec.Body.Bytes(), &sess)

	ans, _ := json.Marshal(answerReq{CardID: sess.Cards[0].Card.ID, Rating: 9})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/sessions/"+strconv.FormatInt(sess.ID, 10)+"/answers", bytes.NewReader(ans)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for bad rating, got %d", rec.Code)
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

func TestAudioServesWhenConfigured(t *testing.T) {
	srv := newTestServer(t)
	cache, err := tts.NewCache(t.TempDir(), stubSynth{})
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	srv.Audio = cache

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/audio?word=ciao", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Fatalf("content-type = %q", ct)
	}
	if rec.Body.String() != "wav:ciao" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestAudioBadRequest(t *testing.T) {
	srv := newTestServer(t)
	srv.Audio, _ = tts.NewCache(t.TempDir(), stubSynth{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/audio?word=", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestAudioUnavailableWithoutSynth(t *testing.T) {
	srv := newTestServer(t) // Audio is nil
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/audio?word=ciao", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rec.Code)
	}
}

// stubGen returns a canned sentence referencing the requested word.
type stubGen struct{}

func (stubGen) Generate(_ context.Context, word string) (store.SentenceDraft, error) {
	return store.SentenceDraft{
		Italian: "Una frase con " + word + ".",
		English: "A sentence with " + word + ".",
		Words:   []store.WordRef{{Lemma: word, Surface: word}},
	}, nil
}

func TestSentencesEndpoint(t *testing.T) {
	srv := newTestServer(t)
	srv.Store.SetSentenceGenerator(stubGen{})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/sentences?word=abbandonare", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body)
	}
	var resp struct {
		Sentences []store.Sentence `json:"sentences"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Sentences) != 1 || resp.Sentences[0].English == "" {
		t.Fatalf("sentences = %+v", resp.Sentences)
	}
}

func TestSentencesUnavailable(t *testing.T) {
	srv := newTestServer(t) // no generator configured
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/sentences?word=ciao", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rec.Code)
	}
}
