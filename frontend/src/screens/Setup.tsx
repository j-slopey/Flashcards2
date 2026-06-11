import { useEffect, useState } from 'react'
import { api, type Level } from '../api'

interface Props {
  onStart: (levels: string[]) => void
}

const SESSION_SIZE = 20

export function Setup({ onStart }: Props) {
  const [levels, setLevels] = useState<Level[]>([])
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    api
      .levels()
      .then((ls) => setLevels(ls))
      .catch((e) => setError(String(e.message ?? e)))
  }, [])

  const toggle = (name: string) => {
    setSelected((prev) => {
      const next = new Set(prev)
      next.has(name) ? next.delete(name) : next.add(name)
      return next
    })
  }

  return (
    <div className="app">
      <h1>Italiano flashcards</h1>
      <p className="subtitle">
        Pick the vocabulary level(s) you want to study, then start a session of{' '}
        {SESSION_SIZE} cards.
      </p>

      {error && <div className="error">{error}</div>}

      <div className="levels">
        {levels.map((l) => (
          <label
            key={l.level}
            className={`level-option ${selected.has(l.level) ? 'selected' : ''}`}
          >
            <input
              type="checkbox"
              checked={selected.has(l.level)}
              onChange={() => toggle(l.level)}
            />
            <span className="level-name">{l.level}</span>
            <span className="level-count">{l.count.toLocaleString()} words</span>
          </label>
        ))}
      </div>

      <button
        className="btn-primary"
        disabled={selected.size === 0}
        onClick={() => onStart([...selected])}
      >
        Start session
      </button>
    </div>
  )
}
