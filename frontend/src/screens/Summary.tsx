interface Props {
  correct: number
  total: number
  onRestart: () => void
}

export function Summary({ correct, total, onRestart }: Props) {
  const pct = total > 0 ? Math.round((correct / total) * 100) : 0
  return (
    <div className="app">
      <h1>Session complete</h1>
      <p className="subtitle">Nice work — here's how you did.</p>

      <div className="summary">
        <div className="score">
          {correct}
          <span className="total"> / {total}</span>
        </div>
        <div className="summary-label">{pct}% recalled correctly</div>
      </div>

      <button className="btn-primary" onClick={onRestart}>
        New session
      </button>
    </div>
  )
}
