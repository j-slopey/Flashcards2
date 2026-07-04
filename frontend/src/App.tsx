import { useState } from 'react'
import { api, Rating, type RatingValue, type Session } from './api'
import { Setup } from './screens/Setup'
import { Play } from './screens/Play'
import { Summary } from './screens/Summary'

type Phase =
  | { name: 'setup' }
  | { name: 'loading' }
  | { name: 'empty' }
  | { name: 'play'; session: Session; index: number; remembered: number }
  | { name: 'done'; remembered: number; total: number }

export default function App() {
  const [phase, setPhase] = useState<Phase>({ name: 'setup' })
  const [error, setError] = useState<string | null>(null)

  const start = async () => {
    setError(null)
    setPhase({ name: 'loading' })
    try {
      const session = await api.createSession()
      if (session.cards.length === 0) {
        setPhase({ name: 'empty' })
      } else {
        setPhase({ name: 'play', session, index: 0, remembered: 0 })
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setPhase({ name: 'setup' })
    }
  }

  const grade = (rating: RatingValue) => {
    if (phase.name !== 'play') return
    const { session, index } = phase
    const card = session.cards[index].card

    // Log the rating; advance optimistically so a logging hiccup never blocks
    // studying (the FSRS state is the source of truth and will catch up).
    api.answer(session.id, card.id, rating).catch(() => {})

    const nextRemembered = phase.remembered + (rating !== Rating.Again ? 1 : 0)
    const nextIndex = index + 1
    if (nextIndex >= session.cards.length) {
      setPhase({ name: 'done', remembered: nextRemembered, total: session.cards.length })
    } else {
      setPhase({ ...phase, index: nextIndex, remembered: nextRemembered })
    }
  }

  switch (phase.name) {
    case 'loading':
      return (
        <div className="app">
          <p className="subtitle">Building your session…</p>
        </div>
      )
    case 'empty':
      return (
        <div className="app">
          <h1>All caught up 🎉</h1>
          <p className="subtitle">
            Nothing is due right now and you've hit today's new-card limit. Come
            back later, or raise the daily limit.
          </p>
          <button className="btn-primary" onClick={() => setPhase({ name: 'setup' })}>
            Back
          </button>
        </div>
      )
    case 'play': {
      const sc = phase.session.cards[phase.index]
      return (
        <Play
          key={sc.card.id}
          sessionCard={sc}
          index={phase.index}
          total={phase.session.cards.length}
          rememberedSoFar={phase.remembered}
          onGrade={grade}
        />
      )
    }
    case 'done':
      return (
        <Summary
          remembered={phase.remembered}
          total={phase.total}
          onRestart={() => setPhase({ name: 'setup' })}
        />
      )
    case 'setup':
    default:
      return (
        <>
          {error && (
            <div className="app">
              <div className="error">{error}</div>
            </div>
          )}
          <Setup onStart={start} />
        </>
      )
  }
}
