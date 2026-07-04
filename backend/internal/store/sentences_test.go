package store

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// stubGen returns a deterministic draft per word and counts generations. Every
// sentence also references "parola1", so it can be re-used by that word.
type stubGen struct{ calls atomic.Int32 }

func (g *stubGen) Generate(_ context.Context, word string) (SentenceDraft, error) {
	g.calls.Add(1)
	return SentenceDraft{
		Italian: "Frase con " + word + ".",
		English: "Sentence with " + word + ".",
		Words: []WordRef{
			{Lemma: word, Surface: word},
			{Lemma: "parola1", Surface: "parola1"},
		},
	}, nil
}

func TestSentencesUnavailableWithoutGenerator(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Sentences(context.Background(), "parola0"); !errors.Is(err, ErrSentencesUnavailable) {
		t.Fatalf("err = %v, want ErrSentencesUnavailable", err)
	}
}

func TestSentencesGenerateCacheAndReuse(t *testing.T) {
	s, _ := newTestStore(t)
	gen := &stubGen{}
	s.SetSentenceGenerator(gen)
	ctx := context.Background()

	// First request generates and caches.
	got, err := s.Sentences(ctx, "parola0")
	if err != nil {
		t.Fatalf("Sentences: %v", err)
	}
	if len(got) != 1 || got[0].Italian != "Frase con parola0." {
		t.Fatalf("unexpected sentences: %+v", got)
	}
	if gen.calls.Load() != 1 {
		t.Fatalf("generations = %d, want 1", gen.calls.Load())
	}

	// Same word again: served from cache, no new generation.
	if _, err := s.Sentences(ctx, "parola0"); err != nil {
		t.Fatalf("Sentences (2nd): %v", err)
	}
	if gen.calls.Load() != 1 {
		t.Fatalf("generations = %d after re-request, want 1", gen.calls.Load())
	}

	// "parola1" appeared in parola0's sentence, so it's already linked — the
	// whole point of the many-to-many design: no generation for it.
	reused, err := s.Sentences(ctx, "parola1")
	if err != nil {
		t.Fatalf("Sentences(parola1): %v", err)
	}
	if gen.calls.Load() != 1 {
		t.Fatalf("generations = %d, want 1 (parola1 reused)", gen.calls.Load())
	}
	if len(reused) != 1 || reused[0].ID != got[0].ID {
		t.Fatalf("parola1 should reuse the same sentence, got %+v", reused)
	}

	// A different unlinked word generates a fresh sentence...
	if _, err := s.Sentences(ctx, "parola5"); err != nil {
		t.Fatalf("Sentences(parola5): %v", err)
	}
	if gen.calls.Load() != 2 {
		t.Fatalf("generations = %d, want 2", gen.calls.Load())
	}

	// ...which also references parola1, so parola1 now has two sentences to
	// click through.
	multi, err := s.Sentences(ctx, "parola1")
	if err != nil {
		t.Fatalf("Sentences(parola1) again: %v", err)
	}
	if len(multi) != 2 {
		t.Fatalf("parola1 sentences = %d, want 2", len(multi))
	}
	if gen.calls.Load() != 2 {
		t.Fatalf("generations = %d, want 2 (no extra)", gen.calls.Load())
	}
}

func TestSentencesSkipsNonVocabularyWords(t *testing.T) {
	s, _ := newTestStore(t)
	// A generator that references a word not in the flashcards table.
	s.sentenceGen = genFunc(func(_ context.Context, word string) (SentenceDraft, error) {
		return SentenceDraft{
			Italian: "Io e " + word + " mangiamo.",
			English: "Me and " + word + " eat.",
			Words: []WordRef{
				{Lemma: word, Surface: word},
				{Lemma: "xyzzy-not-a-word", Surface: "xyzzy"},
			},
		}, nil
	})
	if _, err := s.Sentences(context.Background(), "parola0"); err != nil {
		t.Fatalf("Sentences: %v", err)
	}
	// The bogus lemma must not have produced a link.
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sentence_words WHERE word = ?`, "xyzzy-not-a-word").
		Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("non-vocabulary word was linked %d times, want 0", n)
	}
}

// genFunc adapts a function to the SentenceGenerator interface.
type genFunc func(context.Context, string) (SentenceDraft, error)

func (f genFunc) Generate(ctx context.Context, word string) (SentenceDraft, error) {
	return f(ctx, word)
}
