interface Props {
  remembered: number
  total: number
  onRestart: () => void
}

export function Summary({ remembered, total, onRestart }: Props) {
  const pct = total > 0 ? Math.round((remembered / total) * 100) : 0
  return (
    <div className="app">
      <h1>Session complete</h1>
      <p className="subtitle">Nice work — FSRS has rescheduled each card.</p>

      <div className="summary">
        <div className="score">
          {remembered}
          <span className="total"> / {total}</span>
        </div>
        <div className="summary-label">{pct}% remembered (Hard, Good or Easy)</div>
      </div>

      <button className="btn-primary" onClick={onRestart}>
        New session
      </button>
    </div>
  )
}
