# Flashcards app

A web flashcard trainer over the Italian vocabulary in `flashcards.db`, with a
Spanish-cognate / false-friend bridge for Spanish speakers learning Italian.

```
flashcards.db        # content (Python pipeline owns the `flashcards` table)
backend/             # Go API + app tables (users, sessions, session_cards, reviews)
frontend/            # React + Vite + TypeScript study UI
```

The Go backend opens the **same** `flashcards.db`, never rewriting the
pipeline's `flashcards` table, and adds its own progress tables via an
idempotent migration on startup.

## Run it

**Backend** (default port 8080, db `../flashcards.db`):

```bash
cd backend
go run .
# overrides: FLASHCARDS_DB=/abs/path/flashcards.db PORT=9000 go run .
```

**Frontend** (default port 5173, proxies `/api` -> `localhost:8080`):

```bash
cd frontend
npm install      # first time only
npm run dev
```

Open http://localhost:5173, pick one or more vocabulary levels, and study.

## Tests

```bash
cd backend && go test ./...
```

## API

| Method | Path                         | Body / notes                              |
| ------ | ---------------------------- | ----------------------------------------- |
| GET    | `/api/health`                | liveness                                  |
| GET    | `/api/levels`                | levels + translated-card counts           |
| POST   | `/api/sessions`              | `{ "levels": ["Fondamentale"], "size": 20 }` |
| GET    | `/api/sessions/{id}`         | resume a session                          |
| POST   | `/api/sessions/{id}/answers` | `{ "card_id": 123, "correct": true }`     |

A **card** is one (word, part-of-speech sense). Each card carries `other_senses`
(how many other senses the same word has) so the UI can flag words with
multiple meanings.

## Extending toward the full vision

- **Spaced repetition:** every answer is logged to `reviews(user_id, card_id,
  correct, reviewed_at)`. Replace the random `ORDER BY RANDOM()` query in
  `store.NewSession` with a picker that consults `reviews` to choose ~20 new
  words plus occasional due reviews. No API or frontend change needed.
- **Real accounts:** the schema already keys everything on `user_id` (currently
  the seeded `DefaultUserID = 1`). Add auth + a real user id resolver.
- **Answer choices / auto-grading:** the card payload already has everything
  needed to generate distractors; add a new endpoint and a quiz screen.
