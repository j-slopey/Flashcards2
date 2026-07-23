package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	fsrs "github.com/open-spaced-repetition/go-fsrs/v3"
)

// deck describes one studyable content set and its FSRS progress/session tables.
// The vocabulary and "basics" phrase modes share all the session-loading and
// review-recording logic; they differ only in these table names and how a card
// is loaded by id.
type deck struct {
	states    string // FSRS state table
	sessions  string // sessions table
	sessCards string // session_cards table
	reviews   string // reviews table
	labelCol  string // label column on the sessions table ("levels" or "category")

	// cardByID loads one card by its content-table rowid.
	cardByID func(id int64) (Card, bool, error)
}

func (s *Store) vocabDeck() deck {
	return deck{
		states: "card_states", sessions: "sessions",
		sessCards: "session_cards", reviews: "reviews", labelCol: "levels",
		cardByID: func(id int64) (Card, bool, error) {
			cards, err := s.queryCards(` WHERE f.rowid = ?`, id)
			if err != nil {
				return Card{}, false, err
			}
			if len(cards) == 0 {
				return Card{}, false, nil
			}
			return cards[0], true, nil
		},
	}
}

func (s *Store) phraseDeck() deck {
	return deck{
		states: "phrase_states", sessions: "phrase_sessions",
		sessCards: "phrase_session_cards", reviews: "phrase_reviews", labelCol: "category",
		cardByID: func(id int64) (Card, bool, error) {
			cards, err := s.queryPhrases(` WHERE p.rowid = ?`, id)
			if err != nil {
				return Card{}, false, err
			}
			if len(cards) == 0 {
				return Card{}, false, nil
			}
			return cards[0], true, nil
		},
	}
}

// persistSession writes a session row and its ordered cards, filling sess.ID.
func (s *Store) persistSession(d deck, label string, sess *Session) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO `+d.sessions+` (user_id, `+d.labelCol+`, created_at) VALUES (?, ?, ?)`,
		DefaultUserID, label, s.nowStr())
	if err != nil {
		return err
	}
	if sess.ID, err = res.LastInsertId(); err != nil {
		return err
	}
	for _, sc := range sess.Cards {
		if _, err := tx.Exec(
			`INSERT INTO `+d.sessCards+` (session_id, position, card_id, is_new)
			 VALUES (?, ?, ?, ?)`, sess.ID, sc.Position, sc.Card.ID, b2i(sc.IsNew)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// getSession loads a session from the given deck, its ordered cards, each card's
// rating so far, and a fresh interval preview.
func (s *Store) getSession(d deck, id int64) (*Session, error) {
	var label string
	var completedAt sql.NullString
	err := s.db.QueryRow(
		`SELECT `+d.labelCol+`, completed_at FROM `+d.sessions+` WHERE id = ?`, id).
		Scan(&label, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	sess := &Session{
		ID:        id,
		Levels:    strings.Split(label, "\n"),
		Completed: completedAt.Valid,
	}

	rows, err := s.db.Query(
		`SELECT position, card_id, is_new, answered, rating
		 FROM `+d.sessCards+` WHERE session_id = ? ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := s.now()
	type state struct {
		pos             int
		cardID          int64
		isNew, answered bool
		rating          sql.NullInt64
	}
	var states []state
	for rows.Next() {
		var st state
		var isNew, answered int
		if err := rows.Scan(&st.pos, &st.cardID, &isNew, &answered, &st.rating); err != nil {
			return nil, err
		}
		st.isNew, st.answered = isNew != 0, answered != 0
		states = append(states, st)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, st := range states {
		card, ok, err := d.cardByID(st.cardID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		sc := SessionCard{
			Position: st.pos,
			Card:     card,
			IsNew:    st.isNew,
			Answered: st.answered,
			Preview:  s.preview(d.states, st.cardID, now),
		}
		if st.rating.Valid {
			r := int(st.rating.Int64)
			sc.Rating = &r
		}
		sess.Cards = append(sess.Cards, sc)
	}
	sess.DueCount, sess.NewCount = countKinds(sess.Cards)
	return sess, nil
}

// recordReview applies an FSRS rating (1..4) to a card in a session of the given
// deck: it updates the card's memory state, logs the review, advances the
// session, completes it once every card is answered, and returns the progress.
func (s *Store) recordReview(d deck, sessionID, cardID int64, rating int) (*Progress, error) {
	if rating < 1 || rating > 4 {
		return nil, errors.New("rating must be between 1 (Again) and 4 (Easy)")
	}
	now := s.now()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// The card must belong to the session.
	var dummy int
	err = tx.QueryRow(
		`SELECT 1 FROM `+d.sessCards+` WHERE session_id = ? AND card_id = ?`,
		sessionID, cardID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: card %d not in session %d", ErrNotFound, cardID, sessionID)
	}
	if err != nil {
		return nil, err
	}

	// Apply FSRS scheduling (read state through the tx; pool has one connection).
	card, introduced, found := s.loadState(tx, d.states, cardID)
	if !found {
		introduced = now
	}
	info := s.fsrs.Next(card, now, fsrs.Rating(rating))
	if err := s.saveState(tx, d.states, cardID, info.Card, introduced); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(
		`INSERT INTO `+d.reviews+` (user_id, card_id, session_id, rating, reviewed_at)
		 VALUES (?, ?, ?, ?, ?)`,
		DefaultUserID, cardID, sessionID, rating, s.nowStr()); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(
		`UPDATE `+d.sessCards+` SET answered = 1, rating = ?
		 WHERE session_id = ? AND card_id = ?`,
		rating, sessionID, cardID); err != nil {
		return nil, err
	}

	var p Progress
	if err := tx.QueryRow(
		`SELECT COUNT(*),
		        COALESCE(SUM(answered), 0),
		        COALESCE(SUM(CASE WHEN rating IS NOT NULL AND rating <> 1 THEN 1 ELSE 0 END), 0)
		 FROM `+d.sessCards+` WHERE session_id = ?`, sessionID).
		Scan(&p.Total, &p.Answered, &p.Remembered); err != nil {
		return nil, err
	}
	p.Completed = p.Total > 0 && p.Answered == p.Total

	if p.Completed {
		if _, err := tx.Exec(
			`UPDATE `+d.sessions+` SET completed_at = ? WHERE id = ? AND completed_at IS NULL`,
			s.nowStr(), sessionID); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &p, nil
}
