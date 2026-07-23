package store

import "testing"

func TestPhraseCategories(t *testing.T) {
	s, _ := newTestStore(t)
	cats, err := s.PhraseCategories()
	if err != nil {
		t.Fatalf("PhraseCategories: %v", err)
	}
	got := map[string]int{}
	for _, c := range cats {
		got[c.Category] = c.Count
		if c.Learned != 0 {
			t.Errorf("%s learned = %d, want 0 before any study", c.Category, c.Learned)
		}
	}
	if got["Saluti"] != 3 || got["Cibo"] != 2 {
		t.Fatalf("category counts = %v, want Saluti:3 Cibo:2", got)
	}
}

func TestNewPhraseSessionAllCategories(t *testing.T) {
	s, _ := newTestStore(t)
	sess, err := s.NewPhraseSession("")
	if err != nil {
		t.Fatalf("NewPhraseSession: %v", err)
	}
	if len(sess.Cards) != 5 {
		t.Fatalf("cards = %d, want 5 (all phrases, all new)", len(sess.Cards))
	}
	for _, sc := range sess.Cards {
		if sc.Card.Kind != "phrase" {
			t.Errorf("card kind = %q, want phrase", sc.Card.Kind)
		}
		if !sc.IsNew {
			t.Errorf("phrase %q should be new", sc.Card.Word)
		}
		if sc.Card.Spanish.Word == "" {
			t.Errorf("phrase %q missing Spanish equivalent", sc.Card.Word)
		}
	}
}

func TestNewPhraseSessionByCategory(t *testing.T) {
	s, _ := newTestStore(t)
	sess, err := s.NewPhraseSession("Cibo")
	if err != nil {
		t.Fatalf("NewPhraseSession: %v", err)
	}
	if len(sess.Cards) != 2 {
		t.Fatalf("cards = %d, want 2 in Cibo", len(sess.Cards))
	}
	for _, sc := range sess.Cards {
		if sc.Card.Category != "Cibo" {
			t.Errorf("card category = %q, want Cibo", sc.Card.Category)
		}
	}
}

func TestPhraseReviewSchedulesAndPersists(t *testing.T) {
	s, _ := newTestStore(t)
	sess, err := s.NewPhraseSession("Cibo")
	if err != nil {
		t.Fatalf("NewPhraseSession: %v", err)
	}
	phraseID := sess.Cards[0].Card.ID

	// Rate the first phrase Easy; it should schedule into the future and persist
	// to phrase_states (independently of the vocabulary card_states table).
	if _, err := s.RecordPhraseReview(sess.ID, phraseID, 4); err != nil {
		t.Fatalf("RecordPhraseReview: %v", err)
	}
	card, _, found := s.loadState(s.db, "phrase_states", phraseID)
	if !found {
		t.Fatal("expected phrase_state after review")
	}
	if !card.Due.After(s.now()) {
		t.Errorf("Easy phrase due %v should be after now %v", card.Due, s.now())
	}

	// The vocabulary state table must be untouched by a phrase review.
	if _, _, leaked := s.loadState(s.db, "card_states", phraseID); leaked {
		t.Error("phrase review must not write to card_states")
	}

	// The reviewed phrase is now learned; category progress reflects it.
	cats, err := s.PhraseCategories()
	if err != nil {
		t.Fatalf("PhraseCategories: %v", err)
	}
	for _, c := range cats {
		if c.Category == "Cibo" && c.Learned != 1 {
			t.Errorf("Cibo learned = %d, want 1", c.Learned)
		}
	}
}
