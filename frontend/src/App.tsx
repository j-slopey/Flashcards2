import { useState } from 'react'
import { api, Rating, type RatingValue, type Session } from './api'
import { Home } from './screens/Home'
import { Setup } from './screens/Setup'
import { BasicsSetup } from './screens/BasicsSetup'
import { Play } from './screens/Play'
import { Summary } from './screens/Summary'

type Mode = 'vocab' | 'basics'

type Phase =
  | { name: 'home' }
  | { name: 'setup' }
  | { name: 'basics-setup' }
  | { name: 'loading' }
  | { name: 'empty'; mode: Mode }
  | { name: 'play'; mode: Mode; session: Session; index: number; remembered: number }
  | { name: 'done'; remembered: number; total: number }

export default function App() {
  const [phase, setPhase] = useState<Phase>({ name: 'home' })
  const [error, setError] = useState<string | null>(null)

  // begin runs a session-creating call and moves into play (or empty).
  const begin = async (mode: Mode, create: () => Promise<Session>) => {
    setError(null)
    setPhase({ name: 'loading' })
    try {
      const session = await create()
      if (session.cards.length === 0) {
        setPhase({ name: 'empty', mode })
      } else {
        setPhase({ name: 'play', mode, session, index: 0, remembered: 0 })
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setPhase({ name: mode === 'basics' ? 'basics-setup' : 'setup' })
    }
  }

  const startVocab = () => begin('vocab', () => api.createSession())
  const startBasics = (category: string) =>
    begin('basics', () => api.createPhraseSession(category))

  const grade = (rating: RatingValue) => {
    if (phase.name !== 'play') return
    const { session, index, mode } = phase
    const card = session.cards[index].card

    // Log the rating; advance optimistically so a logging hiccup never blocks
    // studying (the FSRS state is the source of truth and will catch up).
    const log = mode === 'basics' ? api.answerPhrase : api.answer
    log(session.id, card.id, rating).catch(() => {})

    const nextRemembered = phase.remembered + (rating !== Rating.Again ? 1 : 0)
    const nextIndex = index + 1
    if (nextIndex >= session.cards.length) {
      setPhase({ name: 'done', remembered: nextRemembered, total: session.cards.length })
    } else {
      setPhase({ ...phase, index: nextIndex, remembered: nextRemembered })
    }
  }

  switch (phase.name) {
    case 'home':
      return (
        <>
          {error && (
            <div className="app">
              <div className="error">{error}</div>
            </div>
          )}
          <Home
            onVocab={() => setPhase({ name: 'setup' })}
            onBasics={() => setPhase({ name: 'basics-setup' })}
          />
        </>
      )
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
          <button
            className="btn-primary"
            onClick={() =>
              setPhase({ name: phase.mode === 'basics' ? 'basics-setup' : 'setup' })
            }
          >
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
          onRestart={() => setPhase({ name: 'home' })}
        />
      )
    case 'basics-setup':
      return (
        <>
          {error && (
            <div className="app">
              <div className="error">{error}</div>
            </div>
          )}
          <BasicsSetup
            onStart={startBasics}
            onBack={() => setPhase({ name: 'home' })}
          />
        </>
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
          <Setup onStart={startVocab} onBack={() => setPhase({ name: 'home' })} />
        </>
      )
  }
}
