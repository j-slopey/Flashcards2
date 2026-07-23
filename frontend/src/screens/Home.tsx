interface Props {
  onVocab: () => void
  onBasics: () => void
}

export function Home({ onVocab, onBasics }: Props) {
  return (
    <div className="app">
      <h1>Italiano</h1>
      <p className="subtitle">Choose how you want to study today.</p>

      <div className="mode-grid">
        <button className="mode-card" onClick={onVocab}>
          <span className="mode-title">Vocabulary</span>
          <span className="mode-desc">
            Work through the core Italian word list with FSRS spaced repetition,
            following the curriculum level by level.
          </span>
        </button>

        <button className="mode-card" onClick={onBasics}>
          <span className="mode-title">Basics · travel phrases</span>
          <span className="mode-desc">
            ~200 of the most useful phrases for your study abroad, grouped by
            situation — greetings, getting around, food, emergencies, and more.
          </span>
        </button>
      </div>
    </div>
  )
}
