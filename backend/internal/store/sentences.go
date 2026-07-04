package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
)

// ErrSentencesUnavailable is returned when no sentence generator is configured
// (e.g. no API key), so the frontend can hide the feature gracefully.
var ErrSentencesUnavailable = errors.New("example sentences not configured")

// WordRef is one vocabulary word as it appears in a generated sentence: its
// dictionary form (Lemma) and the exact form written in the sentence (Surface).
type WordRef struct {
	Lemma   string
	Surface string
}

// SentenceDraft is a freshly generated example sentence plus every meaningful
// word it uses, so the sentence can be linked to all of them at once.
type SentenceDraft struct {
	Italian string
	English string
	Words   []WordRef // includes the word the sentence was generated for
}

// SentenceGenerator produces an example sentence featuring the given word.
type SentenceGenerator interface {
	Generate(ctx context.Context, word string) (SentenceDraft, error)
}

// SetSentenceGenerator wires in the LLM-backed generator (called once at startup).
func (s *Store) SetSentenceGenerator(g SentenceGenerator) { s.sentenceGen = g }

// Sentences returns the example sentences linked to word, generating and caching
// one on first request. Because a sentence is linked to every vocabulary word it
// contains, a word may already have sentences it never triggered itself.
func (s *Store) Sentences(ctx context.Context, word string) ([]Sentence, error) {
	word = strings.TrimSpace(word)
	if word == "" {
		return nil, errors.New("word is required")
	}

	existing, err := s.sentencesFor(word)
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		return existing, nil
	}
	if s.sentenceGen == nil {
		return nil, ErrSentencesUnavailable
	}

	// Serialize concurrent first-requests for the same word so we generate once.
	lock := s.sentenceLock(word)
	lock.Lock()
	defer lock.Unlock()
	if existing, err = s.sentencesFor(word); err != nil { // re-check under the lock
		return nil, err
	}
	if len(existing) > 0 {
		return existing, nil
	}

	draft, err := s.sentenceGen.Generate(ctx, word)
	if err != nil {
		return nil, err
	}
	if err := s.saveSentence(word, draft); err != nil {
		return nil, err
	}
	return s.sentencesFor(word)
}

// sentencesFor loads the sentences linked to a word, the focus sentence first.
func (s *Store) sentencesFor(word string) ([]Sentence, error) {
	rows, err := s.db.Query(`
		SELECT s.id, s.italian, s.english
		FROM sentences s
		JOIN sentence_words sw ON sw.sentence_id = s.id
		WHERE sw.word = ? COLLATE NOCASE
		ORDER BY sw.focus DESC, s.id ASC`, word)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sentence
	for rows.Next() {
		var s Sentence
		if err := rows.Scan(&s.ID, &s.Italian, &s.English); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// saveSentence stores a draft and links it to every word in it that is a real
// flashcard word (deduping the sentence text and re-using an existing row if the
// same sentence was generated before). The target word is always linked, marked
// as the focus.
func (s *Store) saveSentence(target string, d SentenceDraft) error {
	italian := strings.TrimSpace(d.Italian)
	english := strings.TrimSpace(d.English)
	if italian == "" || english == "" {
		return errors.New("generator returned an empty sentence")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`INSERT INTO sentences (italian, english, created_at) VALUES (?, ?, ?)
		 ON CONFLICT(italian) DO NOTHING`,
		italian, english, s.nowStr()); err != nil {
		return err
	}
	var sentenceID int64
	if err := tx.QueryRow(`SELECT id FROM sentences WHERE italian = ?`, italian).
		Scan(&sentenceID); err != nil {
		return err
	}

	// Collect surfaces by canonical flashcard word; ensure the target is present.
	surfaces := map[string]string{} // canonical word -> surface form
	add := func(lemma, surface string) {
		canon, ok := s.canonicalWord(tx, lemma)
		if !ok {
			return // not a vocabulary word; skip it
		}
		if _, seen := surfaces[canon]; !seen {
			if strings.TrimSpace(surface) == "" {
				surface = lemma
			}
			surfaces[canon] = surface
		}
	}
	for _, w := range d.Words {
		add(w.Lemma, w.Surface)
	}
	add(target, target) // guarantee the focus word is linked

	targetCanon, _ := s.canonicalWord(tx, target)
	for canon, surface := range surfaces {
		focus := 0
		if canon == targetCanon {
			focus = 1
		}
		if _, err := tx.Exec(
			`INSERT INTO sentence_words (sentence_id, word, surface, focus)
			 VALUES (?, ?, ?, ?) ON CONFLICT(sentence_id, word) DO NOTHING`,
			sentenceID, canon, surface, focus); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// canonicalWord maps a lemma to the exact flashcards.word spelling (matching
// case-insensitively), so links always join cleanly on the stored form.
func (s *Store) canonicalWord(q rowQuerier, lemma string) (string, bool) {
	lemma = strings.TrimSpace(lemma)
	if lemma == "" {
		return "", false
	}
	var canon string
	err := q.QueryRow(
		`SELECT word FROM flashcards WHERE word = ? COLLATE NOCASE LIMIT 1`, lemma).
		Scan(&canon)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return "", false
	}
	return canon, true
}

func (s *Store) sentenceLock(word string) *sync.Mutex {
	s.smu.Lock()
	defer s.smu.Unlock()
	k := strings.ToLower(strings.TrimSpace(word))
	l, ok := s.slocks[k]
	if !ok {
		l = &sync.Mutex{}
		s.slocks[k] = l
	}
	return l
}
