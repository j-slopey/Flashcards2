package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newTestStore creates a temp DB seeded with a `flashcards` fixture that mirrors
// the real (pipeline-produced) schema, then opens a Store with a fixed clock.
func newTestStore(t *testing.T) (*Store, *time.Time) {
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
		);`); err != nil {
		t.Fatalf("create flashcards: %v", err)
	}
	// 8 Fondamentale + 2 Alto Uso translated cards so new-card limits can bite.
	for i := 0; i < 8; i++ {
		mustInsertCard(t, db, "Fondamentale", fmt.Sprintf("parola%d", i), "noun")
	}
	mustInsertCard(t, db, "Alto Uso", "altro", "verb")
	mustInsertCard(t, db, "Alto Uso", "secondo", "noun")
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture db: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	clock := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	s.clock = func() time.Time { return clock }
	t.Cleanup(func() { s.Close() })
	return s, &clock
}

func mustInsertCard(t *testing.T, db *sql.DB, level, word, pos string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO flashcards
		 ("level","word","pos_primary","pos_category","english",
		  "spanish_relation","spanish_word","spanish_note")
		 VALUES (?,?,?,?,?,?,?,?)`,
		level, word, "n.", pos, "meaning of "+word, "cognate", word+"-es", "note")
	if err != nil {
		t.Fatalf("insert card: %v", err)
	}
}

func TestLevels(t *testing.T) {
	s, _ := newTestStore(t)
	levels, err := s.Levels()
	if err != nil {
		t.Fatalf("Levels: %v", err)
	}
	counts := map[string]int{}
	for _, l := range levels {
		counts[l.Level] = l.Count
	}
	if counts["Fondamentale"] != 8 || counts["Alto Uso"] != 2 {
		t.Fatalf("unexpected counts: %+v", counts)
	}
}

func TestDefaultSettings(t *testing.T) {
	s, _ := newTestStore(t)
	st, err := s.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if st.NewCardsPerDay != DefaultNewCardsPerDay {
		t.Fatalf("default new/day = %d, want %d", st.NewCardsPerDay, DefaultNewCardsPerDay)
	}
}

func TestNewSessionAllNewWhenNothingSeen(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.SetSettings(Settings{NewCardsPerDay: 3}); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	sess, err := s.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if sess.NewCount != 3 || sess.DueCount != 0 || len(sess.Cards) != 3 {
		t.Fatalf("want 3 new / 0 due, got new=%d due=%d cards=%d", sess.NewCount, sess.DueCount, len(sess.Cards))
	}
	for _, sc := range sess.Cards {
		if !sc.IsNew {
			t.Errorf("card %d should be new", sc.Card.ID)
		}
		if sc.Preview.Again == "" || sc.Preview.Good == "" {
			t.Errorf("card %d missing interval preview: %+v", sc.Card.ID, sc.Preview)
		}
	}
}

// markLearned forces every translatable card in a level into FSRS Review state
// with a far-future due date, simulating a learner who has mastered that level
// without those cards showing up as "due" or "introduced today".
func markLearned(t *testing.T, s *Store, level string) {
	t.Helper()
	future := s.now().AddDate(1, 0, 0)
	past := s.now().AddDate(0, 0, -30)
	_, err := s.db.Exec(`
		INSERT INTO card_states
		    (user_id, card_id, due, stability, difficulty, elapsed_days,
		     scheduled_days, reps, lapses, state, last_review, introduced_at, updated_at)
		SELECT ?, f.rowid, ?, 100, 5, 0, 100, 5, 0, ?, ?, ?, ?
		FROM flashcards f WHERE f.level = ? AND f.english <> ''`,
		DefaultUserID, ftime(future), int(2 /* fsrs.Review */), ftime(past),
		ftime(past), ftime(past), level)
	if err != nil {
		t.Fatalf("markLearned: %v", err)
	}
}

func TestHigherLevelsLockedUntilMastery(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.SetSettings(Settings{NewCardsPerDay: 50}); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	sess, err := s.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// Nothing learned yet: only the first level should ever appear.
	if sess.NewCount != 8 {
		t.Fatalf("want 8 Fondamentale new cards, got %d", sess.NewCount)
	}
	for _, sc := range sess.Cards {
		if sc.Card.Level != "Fondamentale" {
			t.Errorf("locked level leaked: %s", sc.Card.Level)
		}
	}
}

func TestHigherLevelUnlocksAfterMastery(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.SetSettings(Settings{NewCardsPerDay: 50}); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	// Master all of Fondamentale; its cards are now introduced + learned, so the
	// only un-introduced cards left live in the (now unlocked) Alto Uso level.
	markLearned(t, s, "Fondamentale")

	sess, err := s.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if sess.NewCount != 2 {
		t.Fatalf("want 2 Alto Uso new cards once unlocked, got %d", sess.NewCount)
	}
	for _, sc := range sess.Cards {
		if sc.Card.Level != "Alto Uso" {
			t.Errorf("unexpected level %s", sc.Card.Level)
		}
	}
}

func TestDailyNewLimitAcrossSessions(t *testing.T) {
	s, clock := newTestStore(t)
	if err := s.SetSettings(Settings{NewCardsPerDay: 2}); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}

	sess, err := s.NewSession()
	if err != nil {
		t.Fatalf("NewSession 1: %v", err)
	}
	if sess.NewCount != 2 {
		t.Fatalf("session 1 new = %d, want 2", sess.NewCount)
	}
	// Introduce both with a passing grade.
	for _, sc := range sess.Cards {
		if _, err := s.RecordReview(sess.ID, sc.Card.ID, 3); err != nil {
			t.Fatalf("RecordReview: %v", err)
		}
	}

	// Same day: daily new allowance exhausted, nothing due yet -> empty session.
	sess2, err := s.NewSession()
	if err != nil {
		t.Fatalf("NewSession 2: %v", err)
	}
	if sess2.NewCount != 0 {
		t.Errorf("session 2 new = %d, want 0 (daily limit)", sess2.NewCount)
	}

	// Next day: the two introduced cards are now due for review.
	*clock = clock.AddDate(0, 0, 1)
	sess3, err := s.NewSession()
	if err != nil {
		t.Fatalf("NewSession 3: %v", err)
	}
	if sess3.DueCount != 2 {
		t.Errorf("session 3 due = %d, want 2", sess3.DueCount)
	}
	if sess3.NewCount != 2 {
		t.Errorf("session 3 new = %d, want 2 (allowance resets next day)", sess3.NewCount)
	}
}

func TestRecordReviewSchedulesAndCompletes(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.SetSettings(Settings{NewCardsPerDay: 2}); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	sess, err := s.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	prog, err := s.RecordReview(sess.ID, sess.Cards[0].Card.ID, 1) // Again
	if err != nil {
		t.Fatalf("RecordReview: %v", err)
	}
	if prog.Answered != 1 || prog.Remembered != 0 || prog.Completed {
		t.Fatalf("after Again: %+v", prog)
	}

	prog, err = s.RecordReview(sess.ID, sess.Cards[1].Card.ID, 4) // Easy
	if err != nil {
		t.Fatalf("RecordReview: %v", err)
	}
	if prog.Answered != 2 || prog.Remembered != 1 || !prog.Completed {
		t.Fatalf("after Easy: %+v", prog)
	}

	// The Easy card should now have FSRS state with a future due date.
	card, _, found := s.loadState(s.db, sess.Cards[1].Card.ID)
	if !found {
		t.Fatal("expected card_state after review")
	}
	if !card.Due.After(s.now()) {
		t.Errorf("Easy card due %v should be after now %v", card.Due, s.now())
	}
	if card.Reps == 0 {
		t.Error("expected Reps > 0 after a review")
	}
}

func TestGetSessionReflectsRatings(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.SetSettings(Settings{NewCardsPerDay: 2}); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	sess, _ := s.NewSession()
	if _, err := s.RecordReview(sess.ID, sess.Cards[0].Card.ID, 3); err != nil {
		t.Fatalf("RecordReview: %v", err)
	}

	got, err := s.GetSession(sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	answered := 0
	for _, sc := range got.Cards {
		if sc.Answered {
			answered++
			if sc.Rating == nil || *sc.Rating != 3 {
				t.Errorf("answered card rating = %v, want 3", sc.Rating)
			}
		}
	}
	if answered != 1 {
		t.Errorf("answered = %d, want 1", answered)
	}
}

func TestNewSessionEmptyWhenNothingToStudy(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.SetSettings(Settings{NewCardsPerDay: 0}); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	sess, err := s.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if sess.ID != 0 || len(sess.Cards) != 0 {
		t.Fatalf("want empty session, got id=%d cards=%d", sess.ID, len(sess.Cards))
	}
}

func TestRecordReviewRejectsBadRating(t *testing.T) {
	s, _ := newTestStore(t)
	sess, _ := s.NewSession()
	if _, err := s.RecordReview(sess.ID, sess.Cards[0].Card.ID, 5); err == nil {
		t.Fatal("expected error for rating 5")
	}
}

func TestRecordReviewUnknownCard(t *testing.T) {
	s, _ := newTestStore(t)
	sess, _ := s.NewSession()
	if _, err := s.RecordReview(sess.ID, 999999, 3); err == nil {
		t.Fatal("expected ErrNotFound for card not in session")
	}
}

func TestGetSessionNotFound(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.GetSession(424242); err == nil {
		t.Fatal("expected ErrNotFound for missing session")
	}
}
