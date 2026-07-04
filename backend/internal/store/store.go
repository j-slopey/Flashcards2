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
	"math"
	"strings"
	"sync"
	"time"

	fsrs "github.com/open-spaced-repetition/go-fsrs/v3"
	_ "modernc.org/sqlite"
)

// DefaultUserID is the single-user MVP's implicit user. The schema already
// supports multiple users so real accounts can be added later.
const DefaultUserID int64 = 1

// DefaultNewCardsPerDay seeds a new user's daily new-card allowance.
const DefaultNewCardsPerDay = 20

// curriculum is the fixed teaching order of vocabulary levels. The learner works
// through them front to back: a level only starts contributing new cards once
// every earlier level is mastered (see masteryThreshold).
var curriculum = []string{"Fondamentale", "Alto Uso", "Alta Disponibilità"}

// masteryThreshold is the fraction of a level that must be learned (graduated to
// FSRS Review state) before the next level unlocks. Even past this gate the next
// level only trickles in, ramping to full as the prior level approaches 100%.
const masteryThreshold = 0.95

// ErrNotFound is returned when a session (or card within a session) is missing.
var ErrNotFound = errors.New("not found")

// Store is the application's handle on the database and FSRS scheduler.
type Store struct {
	db    *sql.DB
	fsrs  *fsrs.FSRS
	clock func() time.Time // overridable in tests

	// sentenceGen lazily generates example sentences (nil = feature disabled).
	sentenceGen SentenceGenerator
	smu         sync.Mutex
	slocks      map[string]*sync.Mutex // per-word generation locks
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
		db:     db,
		fsrs:   fsrs.NewFSRS(fsrs.DefaultParam()),
		clock:  time.Now,
		slocks: map[string]*sync.Mutex{},
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
-- Example sentences (shared content, not per-user). One sentence can serve many
-- words; the join table records the exact surface form of each word as written
-- in the sentence, which a future fill-in-the-blank / multiple-choice mode needs.
CREATE TABLE IF NOT EXISTS sentences (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    italian    TEXT NOT NULL UNIQUE,
    english    TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sentence_words (
    sentence_id INTEGER NOT NULL REFERENCES sentences(id),
    word        TEXT NOT NULL,    -- matches flashcards.word (the lemma)
    surface     TEXT NOT NULL,    -- exact form as written in the sentence
    focus       INTEGER NOT NULL DEFAULT 0, -- 1 = the word this was generated for
    PRIMARY KEY (sentence_id, word)
);
CREATE INDEX IF NOT EXISTS idx_sentence_words_word ON sentence_words(word);
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

// levelStat is the raw progress through one level: translatable cards and how
// many of them the learner has graduated to FSRS Review state.
type levelStat struct{ total, learned int }

// levelStats counts, per level, the translatable cards and the learned ones
// (FSRS Review state) for the current user.
func (s *Store) levelStats() (map[string]levelStat, error) {
	rows, err := s.db.Query(`
		SELECT f.level,
		       SUM(CASE WHEN f.english IS NOT NULL AND f.english <> '' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN cs.state = ? THEN 1 ELSE 0 END)
		FROM flashcards f
		LEFT JOIN card_states cs
		  ON cs.card_id = f.rowid AND cs.user_id = ?
		GROUP BY f.level`, int(fsrs.Review), DefaultUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := map[string]levelStat{}
	for rows.Next() {
		var level string
		var st levelStat
		if err := rows.Scan(&level, &st.total, &st.learned); err != nil {
			return nil, err
		}
		stats[level] = st
	}
	return stats, rows.Err()
}

// masteryOf is the learned fraction of a level (0 when it has no cards).
func masteryOf(st levelStat) float64 {
	if st.total == 0 {
		return 0
	}
	return float64(st.learned) / float64(st.total)
}

// curriculumProgress returns the levels in teaching order, each annotated with
// its mastery and a cumulative unlock flag: a level unlocks only once every
// earlier level is at or above masteryThreshold.
func curriculumProgress(stats map[string]levelStat) []Level {
	out := make([]Level, 0, len(curriculum))
	prevMastered := true // there is no level before the first, so it's open
	for i, name := range curriculum {
		st := stats[name]
		m := masteryOf(st)
		unlocked := i == 0 || prevMastered
		out = append(out, Level{
			Level: name, Count: st.total, Learned: st.learned,
			Mastery: m, Unlocked: unlocked, Order: i,
		})
		prevMastered = unlocked && m >= masteryThreshold
	}
	return out
}

// rampShare returns the fraction (0..1) of the daily new-card budget a level may
// use, given the mastery of the level before it. At the unlock threshold it is 0
// (so the next level barely trickles in) and climbs to 1 as the prior level
// approaches fully learned, keeping higher levels rare until lower ones are solid.
func rampShare(prevMastery float64) float64 {
	r := (prevMastery - masteryThreshold) / (1 - masteryThreshold)
	return math.Max(0, math.Min(1, r))
}

// Levels returns the curriculum levels with the learner's progress and which
// are currently unlocked.
func (s *Store) Levels() ([]Level, error) {
	stats, err := s.levelStats()
	if err != nil {
		return nil, err
	}
	return curriculumProgress(stats), nil
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

// pickNewCards draws up to `remaining` brand-new cards, walking the curriculum
// front to back. The first level is unlimited within the budget; each later
// (unlocked) level is capped at its rampShare of the full daily budget, so
// higher levels stay rare until the level before them is nearly fully learned.
// Lower levels are filled first, so leftovers there are always preferred.
func (s *Store) pickNewCards(progress []Level, dailyBudget, remaining int) ([]Card, error) {
	if remaining <= 0 {
		return nil, nil
	}
	var fresh []Card
	for i, lvl := range progress {
		if remaining <= 0 {
			break
		}
		if !lvl.Unlocked {
			break // levels are cumulative: nothing past a locked one is open
		}
		limit := remaining
		if i > 0 {
			// Trickle in: at most this level's ramped share of the day's budget,
			// but always allow at least one so an unlocked level can appear.
			cap := int(math.Round(rampShare(progress[i-1].Mastery) * float64(dailyBudget)))
			if cap < 1 {
				cap = 1
			}
			if cap < limit {
				limit = cap
			}
		}
		cards, err := s.queryCards(`
			WHERE f.level = ? AND f.english <> ''
			  AND f.rowid NOT IN (SELECT card_id FROM card_states WHERE user_id = ?)
			ORDER BY RANDOM() LIMIT ?`, lvl.Level, DefaultUserID, limit)
		if err != nil {
			return nil, fmt.Errorf("new cards (%s): %w", lvl.Level, err)
		}
		fresh = append(fresh, cards...)
		remaining -= len(cards)
	}
	return fresh, nil
}

// NewSession builds an FSRS-native session: every card currently due for review,
// followed by new cards up to the remaining daily allowance. The levels new
// cards are drawn from are decided automatically by curriculum progress — the
// learner advances to a level only once the previous one is mastered.
//
// If nothing is due and the new-card limit is exhausted, it returns a session
// with no cards (ID 0) and no row is written.
func (s *Store) NewSession() (*Session, error) {
	now := s.now()

	// 1. Every due review (all started cards live in unlocked levels already),
	//    soonest-due first.
	due, err := s.queryCards(`
		JOIN card_states cs ON cs.card_id = f.rowid AND cs.user_id = ?
		WHERE cs.due <= ?
		ORDER BY cs.due ASC`, DefaultUserID, s.nowStr())
	if err != nil {
		return nil, fmt.Errorf("due cards: %w", err)
	}

	// 2. New cards, gated and ramped by curriculum progress.
	settings, err := s.GetSettings()
	if err != nil {
		return nil, err
	}
	introduced, err := s.newCardsIntroducedToday(now)
	if err != nil {
		return nil, err
	}
	stats, err := s.levelStats()
	if err != nil {
		return nil, err
	}
	progress := curriculumProgress(stats)
	fresh, err := s.pickNewCards(progress, settings.NewCardsPerDay, settings.NewCardsPerDay-introduced)
	if err != nil {
		return nil, err
	}

	var unlocked []string
	for _, l := range progress {
		if l.Unlocked {
			unlocked = append(unlocked, l.Level)
		}
	}

	sess := &Session{Levels: unlocked, DueCount: len(due), NewCount: len(fresh)}
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
		DefaultUserID, strings.Join(unlocked, "\n"), s.nowStr())
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
