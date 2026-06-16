// Package store is the data layer for the flashcards app. It wraps the SQLite
// database that the Python pipeline produces (the read-only `flashcards` table)
// and the app's own progress tables (users, sessions, session_cards, reviews,
// card_states, user_settings), and drives FSRS spaced-repetition scheduling.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	fsrs "github.com/open-spaced-repetition/go-fsrs/v3"
	_ "modernc.org/sqlite"
)

// DefaultUserID is the single-user MVP's implicit user. The schema already
// supports multiple users so real accounts can be added later.
const DefaultUserID int64 = 1

// DefaultNewCardsPerDay seeds a new user's daily new-card allowance.
const DefaultNewCardsPerDay = 20

// ErrNotFound is returned when a session (or card within a session) is missing.
var ErrNotFound = errors.New("not found")

// Store is the application's handle on the database and FSRS scheduler.
type Store struct {
	db    *sql.DB
	fsrs  *fsrs.FSRS
	clock func() time.Time // overridable in tests
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
	s := &Store{
		db:    db,
		fsrs:  fsrs.NewFSRS(fsrs.DefaultParam()),
		clock: time.Now,
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) now() time.Time { return s.clock() }

func (s *Store) nowStr() string { return ftime(s.now()) }

// ftime formats a time as UTC RFC3339, or "" for the zero time.
func ftime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ptime parses an RFC3339 string, returning the zero time for "".
func ptime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// migrate creates the app's own tables (never touching the pipeline-owned
// `flashcards` table), backfills new columns on existing installs, and seeds
// the default user + settings. It is idempotent.
func (s *Store) migrate() error {
	if err := s.dropLegacyProgress(); err != nil {
		return err
	}

	const schema = `
CREATE TABLE IF NOT EXISTS users (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS user_settings (
    user_id           INTEGER PRIMARY KEY REFERENCES users(id),
    new_cards_per_day INTEGER NOT NULL DEFAULT 20
);
CREATE TABLE IF NOT EXISTS sessions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    levels       TEXT NOT NULL,          -- newline-joined level names
    created_at   TEXT NOT NULL,
    completed_at TEXT                    -- NULL until every card is answered
);
CREATE TABLE IF NOT EXISTS session_cards (
    session_id INTEGER NOT NULL REFERENCES sessions(id),
    position   INTEGER NOT NULL,
    card_id    INTEGER NOT NULL,         -- flashcards.rowid
    is_new     INTEGER NOT NULL DEFAULT 0,
    answered   INTEGER NOT NULL DEFAULT 0,
    rating     INTEGER,                  -- 1..4 once answered
    PRIMARY KEY (session_id, position)
);
CREATE TABLE IF NOT EXISTS reviews (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL REFERENCES users(id),
    card_id     INTEGER NOT NULL,        -- flashcards.rowid
    session_id  INTEGER REFERENCES sessions(id),
    rating      INTEGER NOT NULL,        -- 1..4 (Again/Hard/Good/Easy)
    reviewed_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_reviews_user_card ON reviews(user_id, card_id);
-- Per-(user, card) FSRS memory state.
CREATE TABLE IF NOT EXISTS card_states (
    user_id        INTEGER NOT NULL REFERENCES users(id),
    card_id        INTEGER NOT NULL,     -- flashcards.rowid
    due            TEXT NOT NULL,
    stability      REAL NOT NULL,
    difficulty     REAL NOT NULL,
    elapsed_days   INTEGER NOT NULL,
    scheduled_days INTEGER NOT NULL,
    reps           INTEGER NOT NULL,
    lapses         INTEGER NOT NULL,
    state          INTEGER NOT NULL,     -- fsrs.State 0..3
    last_review    TEXT,
    introduced_at  TEXT NOT NULL,        -- first time the card was seen
    updated_at     TEXT NOT NULL,
    PRIMARY KEY (user_id, card_id)
);
CREATE INDEX IF NOT EXISTS idx_card_states_user_due ON card_states(user_id, due);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	// Backfill columns added after the first release (no-op on fresh installs).
	s.addColumn("session_cards", "is_new", "INTEGER NOT NULL DEFAULT 0")
	s.addColumn("session_cards", "rating", "INTEGER")

	if _, err := s.db.Exec(
		`INSERT OR IGNORE INTO users (id, name, created_at) VALUES (?, ?, ?)`,
		DefaultUserID, "default", s.nowStr()); err != nil {
		return fmt.Errorf("seed user: %w", err)
	}
	if _, err := s.db.Exec(
		`INSERT OR IGNORE INTO user_settings (user_id, new_cards_per_day) VALUES (?, ?)`,
		DefaultUserID, DefaultNewCardsPerDay); err != nil {
		return fmt.Errorf("seed settings: %w", err)
	}
	return nil
}

// dropLegacyProgress resets the pre-FSRS progress tables when detected. The old
// schema (sessions.size, reviews.correct, no rating/is_new) is incompatible and
// carried no FSRS memory state, so nothing of value is lost.
func (s *Store) dropLegacyProgress() error {
	legacy, err := s.hasColumn("sessions", "size")
	if err != nil {
		return err
	}
	if !legacy {
		return nil
	}
	log.Printf("store: resetting legacy pre-FSRS progress tables (sessions/session_cards/reviews)")
	for _, t := range []string{"reviews", "session_cards", "sessions"} {
		if _, err := s.db.Exec("DROP TABLE IF EXISTS " + t); err != nil {
			return fmt.Errorf("drop legacy %s: %w", t, err)
		}
	}
	return nil
}

// hasColumn reports whether table has a column named col.
func (s *Store) hasColumn(table, col string) (bool, error) {
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

// addColumn adds a column if it isn't already present (SQLite has no
// ADD COLUMN IF NOT EXISTS, so the duplicate-column error is ignored).
func (s *Store) addColumn(table, col, decl string) {
	_, err := s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, col, decl))
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		// A genuine error here is non-fatal for migrations on fresh installs
		// where the column already exists from CREATE TABLE.
		_ = err
	}
}

// --- Settings ---

// GetSettings returns the user's study settings.
func (s *Store) GetSettings() (Settings, error) {
	var st Settings
	err := s.db.QueryRow(
		`SELECT new_cards_per_day FROM user_settings WHERE user_id = ?`, DefaultUserID).
		Scan(&st.NewCardsPerDay)
	if errors.Is(err, sql.ErrNoRows) {
		return Settings{NewCardsPerDay: DefaultNewCardsPerDay}, nil
	}
	return st, err
}

// SetSettings updates the user's study settings.
func (s *Store) SetSettings(st Settings) error {
	if st.NewCardsPerDay < 0 {
		return errors.New("new_cards_per_day must be >= 0")
	}
	_, err := s.db.Exec(
		`INSERT INTO user_settings (user_id, new_cards_per_day) VALUES (?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET new_cards_per_day = excluded.new_cards_per_day`,
		DefaultUserID, st.NewCardsPerDay)
	return err
}

// --- Cards ---

// cardSelect is the column list + computed other_senses used everywhere a card
// is read. The flashcards table has columns with spaces, hence the quoting.
const cardSelect = `
SELECT f.rowid, f.level, f.word, f.pos_category,
       f.english, f.spanish_relation, f.spanish_word, f.spanish_note,
       (SELECT COUNT(*) FROM flashcards o WHERE o.word = f.word) - 1
FROM flashcards f`

// scanCard reads one row produced by cardSelect.
func scanCard(rows *sql.Rows) (Card, error) {
	var c Card
	var rel, sw, sn sql.NullString
	err := rows.Scan(&c.ID, &c.Level, &c.Word, &c.POS,
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

// --- FSRS card state persistence ---

// rowQuerier is satisfied by both *sql.DB and *sql.Tx, so state reads work
// inside or outside a transaction (important: the pool has a single connection,
// so reading via s.db while a tx is open would deadlock).
type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// loadState returns the stored FSRS card and its introduced_at time, or
// (NewCard, false) if the user has never seen the card.
func (s *Store) loadState(q rowQuerier, cardID int64) (fsrs.Card, time.Time, bool) {
	var (
		due, lastReview, introduced      string
		stability, difficulty            float64
		elapsed, scheduled, reps, lapses int64
		state                            int
	)
	err := q.QueryRow(`
		SELECT due, stability, difficulty, elapsed_days, scheduled_days,
		       reps, lapses, state, last_review, introduced_at
		FROM card_states WHERE user_id = ? AND card_id = ?`,
		DefaultUserID, cardID).
		Scan(&due, &stability, &difficulty, &elapsed, &scheduled,
			&reps, &lapses, &state, &lastReview, &introduced)
	if err != nil {
		return fsrs.NewCard(), time.Time{}, false
	}
	card := fsrs.Card{
		Due:           ptime(due),
		Stability:     stability,
		Difficulty:    difficulty,
		ElapsedDays:   uint64(elapsed),
		ScheduledDays: uint64(scheduled),
		Reps:          uint64(reps),
		Lapses:        uint64(lapses),
		State:         fsrs.State(state),
		LastReview:    ptime(lastReview),
	}
	return card, ptime(introduced), true
}

// saveState upserts the FSRS card state for the current user.
func (s *Store) saveState(tx *sql.Tx, cardID int64, c fsrs.Card, introduced time.Time) error {
	_, err := tx.Exec(`
		INSERT INTO card_states
		    (user_id, card_id, due, stability, difficulty, elapsed_days,
		     scheduled_days, reps, lapses, state, last_review, introduced_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, card_id) DO UPDATE SET
		    due = excluded.due, stability = excluded.stability,
		    difficulty = excluded.difficulty, elapsed_days = excluded.elapsed_days,
		    scheduled_days = excluded.scheduled_days, reps = excluded.reps,
		    lapses = excluded.lapses, state = excluded.state,
		    last_review = excluded.last_review, updated_at = excluded.updated_at`,
		DefaultUserID, cardID, ftime(c.Due), c.Stability, c.Difficulty,
		int64(c.ElapsedDays), int64(c.ScheduledDays), int64(c.Reps), int64(c.Lapses),
		int(c.State), ftime(c.LastReview), ftime(introduced), s.nowStr())
	return err
}

// preview computes the human-readable next interval for each rating, as it
// would be if the card were reviewed right now.
func (s *Store) preview(cardID int64, now time.Time) Intervals {
	card, _, _ := s.loadState(s.db, cardID)
	log := s.fsrs.Repeat(card, now)
	return Intervals{
		Again: humanize(log[fsrs.Again].Card.Due.Sub(now)),
		Hard:  humanize(log[fsrs.Hard].Card.Due.Sub(now)),
		Good:  humanize(log[fsrs.Good].Card.Due.Sub(now)),
		Easy:  humanize(log[fsrs.Easy].Card.Due.Sub(now)),
	}
}

// humanize renders a scheduling interval compactly (e.g. "<1m", "10m", "3h", "4d").
func humanize(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// newCardsIntroducedToday counts how many cards were first seen on the current
// (UTC) day, to enforce the daily new-card limit.
func (s *Store) newCardsIntroducedToday(now time.Time) (int, error) {
	today := now.UTC().Format("2006-01-02")
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM card_states
		 WHERE user_id = ? AND substr(introduced_at, 1, 10) = ?`,
		DefaultUserID, today).Scan(&n)
	return n, err
}

// --- Sessions ---

// queryCards runs cardSelect (with the given suffix and args) and returns cards.
func (s *Store) queryCards(suffix string, args ...any) ([]Card, error) {
	rows, err := s.db.Query(cardSelect+suffix, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cards []Card
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		cards = append(cards, c)
	}
	return cards, rows.Err()
}

// NewSession builds an FSRS-native session for the chosen levels: every card
// currently due for review, followed by new cards up to the remaining daily
// allowance. Both are filtered to the selected levels.
//
// If nothing is due and the new-card limit is exhausted, it returns a session
// with no cards (ID 0) and no row is written.
func (s *Store) NewSession(levels []string) (*Session, error) {
	if len(levels) == 0 {
		return nil, errors.New("at least one level is required")
	}
	now := s.now()
	ph := placeholders(len(levels))
	levelArgs := toAny(levels)

	// 1. Due reviews within the selected levels, soonest-due first.
	dueArgs := append([]any{DefaultUserID}, levelArgs...)
	dueArgs = append(dueArgs, s.nowStr())
	due, err := s.queryCards(`
		JOIN card_states cs ON cs.card_id = f.rowid AND cs.user_id = ?
		WHERE f.level IN (`+ph+`) AND cs.due <= ?
		ORDER BY cs.due ASC`, dueArgs...)
	if err != nil {
		return nil, fmt.Errorf("due cards: %w", err)
	}

	// 2. New cards, capped by the remaining daily allowance.
	settings, err := s.GetSettings()
	if err != nil {
		return nil, err
	}
	introduced, err := s.newCardsIntroducedToday(now)
	if err != nil {
		return nil, err
	}
	remaining := settings.NewCardsPerDay - introduced
	var fresh []Card
	if remaining > 0 {
		newArgs := append(append([]any{}, levelArgs...), DefaultUserID, remaining)
		fresh, err = s.queryCards(`
			WHERE f.level IN (`+ph+`) AND f.english <> ''
			  AND f.rowid NOT IN (SELECT card_id FROM card_states WHERE user_id = ?)
			ORDER BY RANDOM() LIMIT ?`, newArgs...)
		if err != nil {
			return nil, fmt.Errorf("new cards: %w", err)
		}
	}

	sess := &Session{Levels: levels, DueCount: len(due), NewCount: len(fresh)}
	if len(due)+len(fresh) == 0 {
		return sess, nil // nothing to study right now
	}

	// Build the ordered cards (due reviews, then new) with interval previews.
	// Previews read card_states via s.db, so they must be computed *before* the
	// transaction below opens (single-connection pool would otherwise deadlock).
	pos := 0
	for _, group := range []struct {
		cards []Card
		isNew bool
	}{{due, false}, {fresh, true}} {
		for _, c := range group.cards {
			sess.Cards = append(sess.Cards, SessionCard{
				Position: pos, Card: c, IsNew: group.isNew, Preview: s.preview(c.ID, now),
			})
			pos++
		}
	}

	// Persist the session and its cards.
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO sessions (user_id, levels, created_at) VALUES (?, ?, ?)`,
		DefaultUserID, strings.Join(levels, "\n"), s.nowStr())
	if err != nil {
		return nil, err
	}
	sess.ID, err = res.LastInsertId()
	if err != nil {
		return nil, err
	}
	for _, sc := range sess.Cards {
		if _, err := tx.Exec(
			`INSERT INTO session_cards (session_id, position, card_id, is_new)
			 VALUES (?, ?, ?, ?)`, sess.ID, sc.Position, sc.Card.ID, b2i(sc.IsNew)); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return sess, nil
}

// GetSession loads a session, its ordered cards, each card's rating so far, and
// a fresh interval preview.
func (s *Store) GetSession(id int64) (*Session, error) {
	var levels string
	var completedAt sql.NullString
	err := s.db.QueryRow(
		`SELECT levels, completed_at FROM sessions WHERE id = ?`, id).
		Scan(&levels, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	sess := &Session{
		ID:        id,
		Levels:    strings.Split(levels, "\n"),
		Completed: completedAt.Valid,
	}

	rows, err := s.db.Query(`
		SELECT sc.position, sc.card_id, sc.is_new, sc.answered, sc.rating
		FROM session_cards sc WHERE sc.session_id = ? ORDER BY sc.position`, id)
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
		cards, err := s.queryCards(` WHERE f.rowid = ?`, st.cardID)
		if err != nil {
			return nil, err
		}
		if len(cards) == 0 {
			continue
		}
		sc := SessionCard{
			Position: st.pos,
			Card:     cards[0],
			IsNew:    st.isNew,
			Answered: st.answered,
			Preview:  s.preview(st.cardID, now),
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

// RecordReview applies an FSRS rating (1..4) to a card in a session: it updates
// the card's memory state, logs the review, advances the session, completes it
// once every card is answered, and returns the updated progress.
func (s *Store) RecordReview(sessionID, cardID int64, rating int) (*Progress, error) {
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
		`SELECT 1 FROM session_cards WHERE session_id = ? AND card_id = ?`,
		sessionID, cardID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: card %d not in session %d", ErrNotFound, cardID, sessionID)
	}
	if err != nil {
		return nil, err
	}

	// Apply FSRS scheduling (read state through the tx; pool has one connection).
	card, introduced, found := s.loadState(tx, cardID)
	if !found {
		introduced = now
	}
	info := s.fsrs.Next(card, now, fsrs.Rating(rating))
	if err := s.saveState(tx, cardID, info.Card, introduced); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(
		`INSERT INTO reviews (user_id, card_id, session_id, rating, reviewed_at)
		 VALUES (?, ?, ?, ?, ?)`,
		DefaultUserID, cardID, sessionID, rating, s.nowStr()); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(
		`UPDATE session_cards SET answered = 1, rating = ?
		 WHERE session_id = ? AND card_id = ?`,
		rating, sessionID, cardID); err != nil {
		return nil, err
	}

	var p Progress
	if err := tx.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(answered), 0),
		       COALESCE(SUM(CASE WHEN rating IS NOT NULL AND rating <> 1 THEN 1 ELSE 0 END), 0)
		FROM session_cards WHERE session_id = ?`, sessionID).
		Scan(&p.Total, &p.Answered, &p.Remembered); err != nil {
		return nil, err
	}
	p.Completed = p.Total > 0 && p.Answered == p.Total

	if p.Completed {
		if _, err := tx.Exec(
			`UPDATE sessions SET completed_at = ? WHERE id = ? AND completed_at IS NULL`,
			s.nowStr(), sessionID); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &p, nil
}

// --- small helpers ---

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func countKinds(cards []SessionCard) (due, new int) {
	for _, c := range cards {
		if c.IsNew {
			new++
		} else {
			due++
		}
	}
	return
}
