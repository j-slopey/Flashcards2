package store

import (
	"database/sql"

	fsrs "github.com/open-spaced-repetition/go-fsrs/v3"
)

// phraseSelect reads the "basics" phrase content table produced by the Python
// pipeline (generate_phrases.py). Columns: category, italian, english, spanish.
const phraseSelect = `
SELECT p.rowid, p.category, p.italian, p.english, p.spanish
FROM phrases p`

// scanPhrase maps a phrase row onto the shared Card shape (Kind "phrase"). The
// Spanish equivalent is carried in Spanish.Word with relation "none" — phrases
// have no cognate/false-friend classification, and the frontend renders it plain.
func scanPhrase(rows *sql.Rows) (Card, error) {
	var c Card
	var cat, it, en, es sql.NullString
	if err := rows.Scan(&c.ID, &cat, &it, &en, &es); err != nil {
		return c, err
	}
	c.Kind = "phrase"
	c.Category = cat.String
	c.Word = it.String
	c.English = en.String
	c.Spanish = Spanish{Relation: "none", Word: es.String}
	return c, nil
}

func (s *Store) queryPhrases(suffix string, args ...any) ([]Card, error) {
	rows, err := s.db.Query(phraseSelect+suffix, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cards []Card
	for rows.Next() {
		c, err := scanPhrase(rows)
		if err != nil {
			return nil, err
		}
		cards = append(cards, c)
	}
	return cards, rows.Err()
}

// phrasesTableExists reports whether the pipeline has produced the phrases table.
// Until it has, the basics mode is simply empty (no error).
func (s *Store) phrasesTableExists() bool {
	var n int
	_ = s.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='phrases'`).Scan(&n)
	return n > 0
}

// PhraseCategories lists the phrase categories with the learner's progress. It
// returns an empty slice (not an error) when no phrases have been generated yet.
func (s *Store) PhraseCategories() ([]PhraseCategory, error) {
	if !s.phrasesTableExists() {
		return []PhraseCategory{}, nil
	}
	rows, err := s.db.Query(`
		SELECT p.category, COUNT(*),
		       SUM(CASE WHEN cs.state = ? THEN 1 ELSE 0 END)
		FROM phrases p
		LEFT JOIN phrase_states cs ON cs.card_id = p.rowid AND cs.user_id = ?
		GROUP BY p.category ORDER BY p.category`, int(fsrs.Review), DefaultUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PhraseCategory{}
	for rows.Next() {
		var pc PhraseCategory
		if err := rows.Scan(&pc.Category, &pc.Count, &pc.Learned); err != nil {
			return nil, err
		}
		out = append(out, pc)
	}
	return out, rows.Err()
}

// pickNewPhrases draws up to `remaining` never-seen phrases, optionally limited
// to one category.
func (s *Store) pickNewPhrases(category string, remaining int) ([]Card, error) {
	if remaining <= 0 {
		return nil, nil
	}
	suffix := ` WHERE p.rowid NOT IN (SELECT card_id FROM phrase_states WHERE user_id = ?)`
	args := []any{DefaultUserID}
	if category != "" {
		suffix += ` AND p.category = ?`
		args = append(args, category)
	}
	suffix += ` ORDER BY RANDOM() LIMIT ?`
	args = append(args, remaining)
	return s.queryPhrases(suffix, args...)
}

// NewPhraseSession builds an FSRS session over the basics phrases: every phrase
// due for review, then new phrases up to the remaining daily allowance. An empty
// category means "all categories". Returns an empty session (ID 0) when nothing
// is due and the new-card limit is spent, or when no phrases exist yet.
func (s *Store) NewPhraseSession(category string) (*Session, error) {
	label := []string{}
	if category != "" {
		label = []string{category}
	}
	if !s.phrasesTableExists() {
		return &Session{Levels: label}, nil
	}
	now := s.now()
	d := s.phraseDeck()

	// 1. Due reviews, soonest first.
	dueSuffix := ` JOIN phrase_states cs ON cs.card_id = p.rowid AND cs.user_id = ?
	               WHERE cs.due <= ?`
	args := []any{DefaultUserID, s.nowStr()}
	if category != "" {
		dueSuffix += ` AND p.category = ?`
		args = append(args, category)
	}
	dueSuffix += ` ORDER BY cs.due ASC`
	due, err := s.queryPhrases(dueSuffix, args...)
	if err != nil {
		return nil, err
	}

	// 2. New phrases up to the shared daily new-card budget.
	settings, err := s.GetSettings()
	if err != nil {
		return nil, err
	}
	introduced, err := s.newCardsIntroducedToday("phrase_states", now)
	if err != nil {
		return nil, err
	}
	fresh, err := s.pickNewPhrases(category, settings.NewCardsPerDay-introduced)
	if err != nil {
		return nil, err
	}

	sess := &Session{Levels: label, DueCount: len(due), NewCount: len(fresh)}
	if len(due)+len(fresh) == 0 {
		return sess, nil
	}

	pos := 0
	for _, group := range []struct {
		cards []Card
		isNew bool
	}{{due, false}, {fresh, true}} {
		for _, c := range group.cards {
			sess.Cards = append(sess.Cards, SessionCard{
				Position: pos, Card: c, IsNew: group.isNew,
				Preview: s.preview("phrase_states", c.ID, now),
			})
			pos++
		}
	}

	if err := s.persistSession(d, category, sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// GetPhraseSession loads a basics session by id.
func (s *Store) GetPhraseSession(id int64) (*Session, error) {
	return s.getSession(s.phraseDeck(), id)
}

// RecordPhraseReview applies an FSRS rating (1..4) to a phrase in a session.
func (s *Store) RecordPhraseReview(sessionID, cardID int64, rating int) (*Progress, error) {
	return s.recordReview(s.phraseDeck(), sessionID, cardID, rating)
}
