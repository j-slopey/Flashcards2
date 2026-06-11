package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// newTestStore creates a temp DB seeded with a small `flashcards` fixture that
// mirrors the real (pipeline-produced) schema, then opens a Store over it.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Quoted column names with spaces match the real table.
	_, err = db.Exec(`
		CREATE TABLE flashcards (
			"row" INTEGER, "level" TEXT, "word" TEXT, "part of speech" TEXT,
			"prep. before next infinitive" TEXT, "dictionary" TEXT,
			"pos_category" TEXT, "pos_primary" TEXT, "english" TEXT,
			"spanish_relation" TEXT, "spanish_word" TEXT, "spanish_note" TEXT
		);`)
	if err != nil {
		t.Fatalf("create flashcards: %v", err)
	}
	fixtures := []struct {
		level, word, pos, english, rel, sword, snote string
	}{
		{"Fondamentale", "abbandonare", "verb", "to abandon", "cognate", "abandonar", "Direct cognate."},
		{"Fondamentale", "attuale", "adjective", "current / present", "false_friend", "actual", "Means 'current', not 'real'."},
		{"Alto Uso", "abbandonato", "participle (past)", "abandoned", "cognate", "abandonado", "Direct cognate."},
		{"Alto Uso", "tanto", "adverb", "so much", "none", "", ""},
		{"Alta Disponibilità", "abbaiare", "verb", "to bark", "none", "", ""},
		// Two senses of the same word -> other_senses should be 1 for each.
		{"Alta Disponibilità", "secondo", "preposition", "according to", "none", "", ""},
		{"Alta Disponibilità", "secondo", "noun", "second", "cognate", "segundo", "Cognate."},
	}
	for _, f := range fixtures {
		_, err := db.Exec(
			`INSERT INTO flashcards
			 ("level","word","part of speech","pos_category","english",
			  "spanish_relation","spanish_word","spanish_note")
			 VALUES (?,?,?,?,?,?,?,?)`,
			f.level, f.word, "(pos)", f.pos, f.english, f.rel, f.sword, f.snote)
		if err != nil {
			t.Fatalf("insert fixture: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture db: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestLevels(t *testing.T) {
	s := newTestStore(t)
	levels, err := s.Levels()
	if err != nil {
		t.Fatalf("Levels: %v", err)
	}
	if len(levels) != 3 {
		t.Fatalf("want 3 levels, got %d: %+v", len(levels), levels)
	}
	counts := map[string]int{}
	for _, l := range levels {
		counts[l.Level] = l.Count
	}
	if counts["Alta Disponibilità"] != 3 {
		t.Errorf("Alta Disponibilità count = %d, want 3", counts["Alta Disponibilità"])
	}
	if counts["Fondamentale"] != 2 {
		t.Errorf("Fondamentale count = %d, want 2", counts["Fondamentale"])
	}
}

func TestNewSessionRespectsLevels(t *testing.T) {
	s := newTestStore(t)
	sess, err := s.NewSession([]string{"Fondamentale"}, 20)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// Only 2 Fondamentale cards exist, so size caps at 2.
	if len(sess.Cards) != 2 {
		t.Fatalf("want 2 cards, got %d", len(sess.Cards))
	}
	for _, sc := range sess.Cards {
		if sc.Card.Level != "Fondamentale" {
			t.Errorf("card from wrong level: %s", sc.Card.Level)
		}
		if sc.Answered {
			t.Error("new card should not be answered")
		}
	}
}

func TestNewSessionOtherSenses(t *testing.T) {
	s := newTestStore(t)
	sess, err := s.NewSession([]string{"Alta Disponibilità"}, 20)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	for _, sc := range sess.Cards {
		if sc.Card.Word == "secondo" && sc.Card.OtherSenses != 1 {
			t.Errorf("secondo other_senses = %d, want 1", sc.Card.OtherSenses)
		}
		if sc.Card.Word == "abbaiare" && sc.Card.OtherSenses != 0 {
			t.Errorf("abbaiare other_senses = %d, want 0", sc.Card.OtherSenses)
		}
	}
}

func TestSpanishRelationDefaultsToNone(t *testing.T) {
	s := newTestStore(t)
	sess, _ := s.NewSession([]string{"Alta Disponibilità"}, 20)
	for _, sc := range sess.Cards {
		if sc.Card.Spanish.Relation == "" {
			t.Errorf("%s has empty relation, want 'none'", sc.Card.Word)
		}
	}
}

func TestRecordAnswerAndCompletion(t *testing.T) {
	s := newTestStore(t)
	sess, err := s.NewSession([]string{"Fondamentale"}, 20)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Answer the first card correctly.
	prog, err := s.RecordAnswer(sess.ID, sess.Cards[0].Card.ID, true)
	if err != nil {
		t.Fatalf("RecordAnswer: %v", err)
	}
	if prog.Answered != 1 || prog.Correct != 1 || prog.Total != 2 || prog.Completed {
		t.Fatalf("after 1st answer: %+v", prog)
	}

	// Answer the second incorrectly -> session completes.
	prog, err = s.RecordAnswer(sess.ID, sess.Cards[1].Card.ID, false)
	if err != nil {
		t.Fatalf("RecordAnswer: %v", err)
	}
	if prog.Answered != 2 || prog.Correct != 1 || !prog.Completed {
		t.Fatalf("after 2nd answer: %+v", prog)
	}

	// Reload and confirm persisted state.
	got, err := s.GetSession(sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !got.Completed {
		t.Error("reloaded session should be completed")
	}
	answered := 0
	for _, sc := range got.Cards {
		if sc.Answered {
			answered++
		}
		if sc.Correct == nil {
			t.Errorf("answered card %d missing correct flag", sc.Card.ID)
		}
	}
	if answered != 2 {
		t.Errorf("answered = %d, want 2", answered)
	}
}

func TestRecordAnswerUnknownCard(t *testing.T) {
	s := newTestStore(t)
	sess, _ := s.NewSession([]string{"Fondamentale"}, 20)
	if _, err := s.RecordAnswer(sess.ID, 99999, true); err == nil {
		t.Fatal("expected error for card not in session")
	}
}

func TestGetSessionNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.GetSession(424242); err == nil {
		t.Fatal("expected ErrNotFound for missing session")
	}
}
