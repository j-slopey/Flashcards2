import { useEffect, useState } from 'react'
import { api, type PhraseCategory } from '../api'

interface Props {
  onStart: (category: string) => void
  onBack: () => void
}

export function BasicsSetup({ onStart, onBack }: Props) {
  const [cats, setCats] = useState<PhraseCategory[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    api
      .phraseCategories()
      .then(setCats)
      .catch((e) => setError(String(e.message ?? e)))
  }, [])

  const total = (cats ?? []).reduce((n, c) => n + c.count, 0)
  const learned = (cats ?? []).reduce((n, c) => n + c.learned, 0)

  return (
    <div className="app">
      <button className="btn-link back" onClick={onBack}>
        ← Back
      </button>
      <h1>Basics · travel phrases</h1>
      <p className="subtitle">
        Essential Italian for your trip, with English and Spanish. Reviewed with
        spaced repetition, just like the vocabulary deck.
      </p>

      {error && <div className="error">{error}</div>}

      {cats && cats.length === 0 && (
        <div className="empty-note">
          <p>No phrases have been generated yet.</p>
          <p className="setting-hint">
            Run <code>python generate_phrases.py</code> (with{' '}
            <code>GEMINI_API_KEY</code> set) to create them, then come back.
          </p>
        </div>
      )}

      {cats && cats.length > 0 && (
        <>
          <button className="phrase-cat all" onClick={() => onStart('')}>
            <div className="phrase-cat-head">
              <span className="level-name">All categories</span>
              <span className="level-count">
                {learned} / {total} learned
              </span>
            </div>
          </button>

          <div className="levels">
            {cats.map((c) => {
              const pct = c.count > 0 ? Math.round((c.learned / c.count) * 100) : 0
              return (
                <button
                  key={c.category}
                  className="phrase-cat"
                  onClick={() => onStart(c.category)}
                >
                  <div className="phrase-cat-head">
                    <span className="level-name">{c.category}</span>
                    <span className="level-count">
                      {c.learned} / {c.count} learned
                    </span>
                  </div>
                  <div className="bar">
                    <div className="bar-fill" style={{ width: `${pct}%` }} />
                  </div>
                </button>
              )
            })}
          </div>
        </>
      )}
    </div>
  )
}
