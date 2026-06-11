// Package store is the data layer for the flashcards app. It wraps the SQLite
// database that the Python pipeline produces (the read-only `flashcards` table)
// and the app's own progress tables (users, sessions, session_cards, reviews).
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DefaultUserID is the single-user MVP's implicit user. The schema already
// supports multiple users so real accounts can be added later.
const DefaultUserID int64 = 1

// ErrNotFound is returned when a session (or card within a session) is missing.
var ErrNotFound = errors.New("not found")

// Store is the application's handle on the database.
type Store struct {
	db *sql.DB
}

// Open opens the SQLite database at path and applies the app migrations.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// SQLite handles concurrency best with a single writer connection plus WAL.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set pragmas: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// migrate creates the app's own tables (never touching the pipeline-owned
// `flashcards` table) and seeds the default user. It is idempotent.
func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS users (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    levels       TEXT NOT NULL,          -- newline-joined level names
    size         INTEGER NOT NULL,
    created_at   TEXT NOT NULL,
    completed_at TEXT                    -- NULL until every card is answered
);
CREATE TABLE IF NOT EXISTS session_cards (
    session_id INTEGER NOT NULL REFERENCES sessions(id),
    position   INTEGER NOT NULL,
    card_id    INTEGER NOT NULL,         -- flashcards.rowid
    answered   INTEGER NOT NULL DEFAULT 0,
    correct    INTEGER,                  -- NULL until answered
    PRIMARY KEY (session_id, position)
);
CREATE TABLE IF NOT EXISTS reviews (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL REFERENCES users(id),
    card_id     INTEGER NOT NULL,        -- flashcards.rowid
    session_id  INTEGER REFERENCES sessions(id),
    correct     INTEGER NOT NULL,
    reviewed_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_reviews_user_card ON reviews(user_id, card_id);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO users (id, name, created_at) VALUES (?, ?, ?)`,
		DefaultUserID, "default", now(),
	)
	if err != nil {
		return fmt.Errorf("seed user: %w", err)
	}
	return nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// cardSelect is the column list + computed other_senses used everywhere a card
// is read. The flashcards table has columns with spaces, hence the quoting.
const cardSelect = `
SELECT f.rowid, f.level, f.word, f.pos_category, f.pos_primary,
       f.english, f.spanish_relation, f.spanish_word, f.spanish_note,
       (SELECT COUNT(*) FROM flashcards o WHERE o.word = f.word) - 1
FROM flashcards f`

// scanCard reads one row produced by cardSelect.
func scanCard(rows *sql.Rows) (Card, error) {
	var c Card
	var rel, sw, sn sql.NullString
	err := rows.Scan(&c.ID, &c.Level, &c.Word, &c.POS, &c.POSDisplay,
		&c.English, &rel, &sw, &sn, &c.OtherSenses)
	if err != nil {
		return c, err
	}
	c.Spanish = Spanish{Relation: rel.String, Word: sw.String, Note: sn.String}
	if c.Spanish.Relation == "" {
		c.Spanish.Relation = "none"
	}
	return c, nil
}

// Levels returns the vocabulary levels and how many translated cards each has.
func (s *Store) Levels() ([]Level, error) {
	rows, err := s.db.Query(`
		SELECT level, COUNT(*) FROM flashcards
		WHERE english IS NOT NULL AND english <> ''
		GROUP BY level ORDER BY level`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Level
	for rows.Next() {
		var l Level
		if err := rows.Scan(&l.Level, &l.Count); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// NewSession picks `size` random translated cards from the given levels, stores
// the session, and returns it with the cards loaded.
//
// Card selection lives here so a smarter (spaced-repetition) picker can replace
// the random query later without changing callers.
func (s *Store) NewSession(levels []string, size int) (*Session, error) {
	if len(levels) == 0 {
		return nil, errors.New("at least one level is required")
	}
	if size <= 0 {
		size = 20
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(levels)), ",")
	args := make([]any, 0, len(levels)+1)
	for _, l := range levels {
		args = append(args, l)
	}
	args = append(args, size)

	cardRows, err := s.db.Query(cardSelect+`
		WHERE f.level IN (`+placeholders+`)
		  AND f.english IS NOT NULL AND f.english <> ''
		ORDER BY RANDOM() LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer cardRows.Close()

	var cards []Card
	for cardRows.Next() {
		c, err := scanCard(cardRows)
		if err != nil {
			return nil, err
		}
		cards = append(cards, c)
	}
	if err := cardRows.Err(); err != nil {
		return nil, err
	}
	if len(cards) == 0 {
		return nil, fmt.Errorf("%w: no cards for the requested levels", ErrNotFound)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO sessions (user_id, levels, size, created_at) VALUES (?, ?, ?, ?)`,
		DefaultUserID, strings.Join(levels, "\n"), len(cards), now())
	if err != nil {
		return nil, err
	}
	sessionID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	sess := &Session{ID: sessionID, Levels: levels, Size: len(cards)}
	for i, c := range cards {
		if _, err := tx.Exec(
			`INSERT INTO session_cards (session_id, position, card_id) VALUES (?, ?, ?)`,
			sessionID, i, c.ID); err != nil {
			return nil, err
		}
		sess.Cards = append(sess.Cards, SessionCard{Position: i, Card: c})
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return sess, nil
}

// GetSession loads a session, its ordered cards, and each card's result so far.
func (s *Store) GetSession(id int64) (*Session, error) {
	var levels string
	var size int
	var completedAt sql.NullString
	err := s.db.QueryRow(
		`SELECT levels, size, completed_at FROM sessions WHERE id = ?`, id).
		Scan(&levels, &size, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	sess := &Session{
		ID:        id,
		Levels:    strings.Split(levels, "\n"),
		Size:      size,
		Completed: completedAt.Valid,
	}

	rows, err := s.db.Query(cardSelect+`
		JOIN session_cards sc ON sc.card_id = f.rowid
		WHERE sc.session_id = ?
		ORDER BY sc.position`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// scanCard expects exactly cardSelect's columns; answered/correct are read
	// separately to keep that helper reusable.
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		sess.Cards = append(sess.Cards, SessionCard{Card: c})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Overlay position + answered/correct state.
	stateRows, err := s.db.Query(
		`SELECT position, card_id, answered, correct FROM session_cards
		 WHERE session_id = ? ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	defer stateRows.Close()

	i := 0
	for stateRows.Next() {
		var pos int
		var cardID int64
		var answered int
		var correct sql.NullInt64
		if err := stateRows.Scan(&pos, &cardID, &answered, &correct); err != nil {
			return nil, err
		}
		if i < len(sess.Cards) {
			sess.Cards[i].Position = pos
			sess.Cards[i].Answered = answered != 0
			if correct.Valid {
				b := correct.Int64 != 0
				sess.Cards[i].Correct = &b
			}
		}
		i++
	}
	return sess, stateRows.Err()
}

// RecordAnswer stores a self-graded result for one card in a session, logs it to
// the review history, marks the session complete once every card is answered,
// and returns the updated progress.
func (s *Store) RecordAnswer(sessionID, cardID int64, correct bool) (*Progress, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	correctInt := 0
	if correct {
		correctInt = 1
	}

	res, err := tx.Exec(
		`UPDATE session_cards SET answered = 1, correct = ?
		 WHERE session_id = ? AND card_id = ?`,
		correctInt, sessionID, cardID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("%w: card %d not in session %d", ErrNotFound, cardID, sessionID)
	}

	if _, err := tx.Exec(
		`INSERT INTO reviews (user_id, card_id, session_id, correct, reviewed_at)
		 VALUES (?, ?, ?, ?, ?)`,
		DefaultUserID, cardID, sessionID, correctInt, now()); err != nil {
		return nil, err
	}

	var p Progress
	if err := tx.QueryRow(
		`SELECT COUNT(*),
		        COALESCE(SUM(answered), 0),
		        COALESCE(SUM(CASE WHEN correct = 1 THEN 1 ELSE 0 END), 0)
		 FROM session_cards WHERE session_id = ?`, sessionID).
		Scan(&p.Total, &p.Answered, &p.Correct); err != nil {
		return nil, err
	}
	p.Completed = p.Total > 0 && p.Answered == p.Total

	if p.Completed {
		if _, err := tx.Exec(
			`UPDATE sessions SET completed_at = ? WHERE id = ? AND completed_at IS NULL`,
			now(), sessionID); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &p, nil
}
