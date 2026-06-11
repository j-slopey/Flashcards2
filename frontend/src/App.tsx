import { useState } from 'react'
import { api, type Session } from './api'
import { Setup } from './screens/Setup'
import { Play } from './screens/Play'
import { Summary } from './screens/Summary'

type Phase =
  | { name: 'setup' }
  | { name: 'loading' }
  | { name: 'play'; session: Session; index: number; correct: number }
  | { name: 'done'; correct: number; total: number }

export default function App() {
  const [phase, setPhase] = useState<Phase>({ name: 'setup' })
  const [error, setError] = useState<string | null>(null)

  const start = async (levels: string[]) => {
    setError(null)
    setPhase({ name: 'loading' })
    try {
      const session = await api.createSession(levels, 20)
      setPhase({ name: 'play', session, index: 0, correct: 0 })
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setPhase({ name: 'setup' })
    }
  }

  const grade = async (correct: boolean) => {
    if (phase.name !== 'play') return
    const { session, index } = phase
    const card = session.cards[index].card

    // Record the self-graded result; advance optimistically regardless so a
    // logging hiccup never blocks studying.
    api.answer(session.id, card.id, correct).catch(() => {})

    const nextCorrect = phase.correct + (correct ? 1 : 0)
    const nextIndex = index + 1
    if (nextIndex >= session.cards.length) {
      setPhase({ name: 'done', correct: nextCorrect, total: session.cards.length })
    } else {
      setPhase({ ...phase, index: nextIndex, correct: nextCorrect })
    }
  }

  switch (phase.name) {
    case 'loading':
      return (
        <div className="app">
          <p className="subtitle">Building your session…</p>
        </div>
      )
    case 'play': {
      const sc = phase.session.cards[phase.index]
      return (
        <Play
          key={sc.card.id}
          card={sc.card}
          index={phase.index}
          total={phase.session.cards.length}
          correctSoFar={phase.correct}
          onGrade={grade}
        />
      )
    }
    case 'done':
      return (
        <Summary
          correct={phase.correct}
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
