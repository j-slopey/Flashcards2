import { useState } from 'react'
import type { Card } from '../api'

interface Props {
  card: Card
  index: number // 0-based
  total: number
  correctSoFar: number
  onGrade: (correct: boolean) => void
}

const RELATION_LABEL: Record<Card['spanish']['relation'], string> = {
  cognate: 'Spanish cognate',
  false_friend: '⚠ False friend',
  none: 'No Spanish link',
}

export function Play({ card, index, total, correctSoFar, onGrade }: Props) {
  const [revealed, setRevealed] = useState(false)

  // Reset reveal state whenever a new card comes in.
  // key on the card id from the parent guarantees a fresh component instead.

  const grade = (correct: boolean) => {
    setRevealed(false)
    onGrade(correct)
  }

  const rel = card.spanish.relation
  const pct = (index / total) * 100

  return (
    <div className="app">
      <div className="progress-row">
        <span>
          Card {index + 1} / {total}
        </span>
        <span>{correctSoFar} correct</span>
      </div>
      <div className="progress-bar">
        <div className="progress-fill" style={{ width: `${pct}%` }} />
      </div>

      <div className="card">
        <div className="card-level">{card.level}</div>
        <div className="word">{card.word}</div>
        <div className="pos">{card.pos_display || card.pos}</div>

        {card.other_senses > 0 && (
          <div className="sense-hint">
            this word has {card.other_senses} other meaning
            {card.other_senses > 1 ? 's' : ''}
          </div>
        )}

        {revealed && (
          <>
            <div className="divider" />
            <div className="english">{card.english}</div>

            <div className="spanish">
              <span className={`badge ${rel}`}>{RELATION_LABEL[rel]}</span>
              {card.spanish.word && (
                <div className="spanish-word">
                  Spanish: <span className="es">{card.spanish.word}</span>
                </div>
              )}
              {card.spanish.note && (
                <div className="spanish-note">{card.spanish.note}</div>
              )}
            </div>
          </>
        )}
      </div>

      <div className="controls">
        {!revealed ? (
          <button className="btn-primary" onClick={() => setRevealed(true)}>
            Reveal answer
          </button>
        ) : (
          <div className="grade-row">
            <button className="btn-missed" onClick={() => grade(false)}>
              Missed it
            </button>
            <button className="btn-got" onClick={() => grade(true)}>
              Got it
            </button>
          </div>
        )}
      </div>
    </div>
  )
}
