import { useEffect, useState } from 'react'
import { api, type Level } from '../api'

interface Props {
  onStart: () => void
  onBack?: () => void
}

export function Setup({ onStart, onBack }: Props) {
  const [levels, setLevels] = useState<Level[]>([])
  const [newPerDay, setNewPerDay] = useState<number>(20)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    api.levels().then(setLevels).catch((e) => setError(String(e.message ?? e)))
    api.getSettings().then((s) => setNewPerDay(s.new_cards_per_day)).catch(() => {})
  }, [])

  const commitSetting = (value: number) => {
    const v = Math.max(0, Math.min(999, Math.floor(value) || 0))
    setNewPerDay(v)
    api.saveSettings({ new_cards_per_day: v }).catch(() => {})
  }

  return (
    <div className="app">
      {onBack && (
        <button className="btn-link back" onClick={onBack}>
          ← Back
        </button>
      )}
      <h1>Italiano flashcards</h1>
      <p className="subtitle">
        Each session reviews everything that's due, then introduces new words —
        spaced with FSRS. New words follow the vocabulary curriculum: a level
        opens up only once you've learned the one before it.
      </p>

      {error && <div className="error">{error}</div>}

      <div className="levels">
        {levels.map((l) => {
          const pct = Math.round(l.mastery * 100)
          return (
            <div
              key={l.level}
              className={`level-progress ${l.unlocked ? 'unlocked' : 'locked'}`}
            >
              <div className="level-progress-head">
                <span className="level-name">
                  {l.unlocked ? l.level : `🔒 ${l.level}`}
                </span>
                <span className="level-count">
                  {l.learned.toLocaleString()} / {l.count.toLocaleString()} learned
                </span>
              </div>
              <div className="bar">
                <div className="bar-fill" style={{ width: `${pct}%` }} />
              </div>
            </div>
          )
        })}
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

      <button className="btn-primary" onClick={onStart}>
        Start session
      </button>
    </div>
  )
}
