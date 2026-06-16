import { useEffect, useState } from 'react'
import { api, type Level } from '../api'

interface Props {
  onStart: (levels: string[]) => void
}

export function Setup({ onStart }: Props) {
  const [levels, setLevels] = useState<Level[]>([])
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [newPerDay, setNewPerDay] = useState<number>(20)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    api.levels().then(setLevels).catch((e) => setError(String(e.message ?? e)))
    api.getSettings().then((s) => setNewPerDay(s.new_cards_per_day)).catch(() => {})
  }, [])

  const toggle = (name: string) => {
    setSelected((prev) => {
      const next = new Set(prev)
      next.has(name) ? next.delete(name) : next.add(name)
      return next
    })
  }

  const commitSetting = (value: number) => {
    const v = Math.max(0, Math.min(999, Math.floor(value) || 0))
    setNewPerDay(v)
    api.saveSettings({ new_cards_per_day: v }).catch(() => {})
  }

  return (
    <div className="app">
      <h1>Italiano flashcards</h1>
      <p className="subtitle">
        Pick the vocabulary level(s) to study. Each session reviews everything
        that's due, then introduces new words — spaced with FSRS.
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

      <div className="setting-row">
        <label htmlFor="new-per-day">New cards per day</label>
        <input
          id="new-per-day"
          className="number-input"
          type="number"
          min={0}
          max={999}
          value={newPerDay}
          onChange={(e) => setNewPerDay(Number(e.target.value))}
          onBlur={(e) => commitSetting(Number(e.target.value))}
        />
      </div>
      <p className="setting-hint">
        Due reviews are never capped — this only limits brand-new words.
      </p>

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
