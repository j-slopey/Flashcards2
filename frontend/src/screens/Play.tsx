import { useState } from 'react'
import { api, Rating, type RatingValue, type Sentence, type SessionCard } from '../api'
import { playPronunciation } from '../speech'

interface Props {
  sessionCard: SessionCard
  index: number // 0-based
  total: number
  rememberedSoFar: number
  onGrade: (rating: RatingValue) => void
}

const RELATION_LABEL = {
  cognate: 'Spanish cognate',
  false_friend: '⚠ False friend',
  none: 'No Spanish link',
} as const

// The four FSRS rating buttons, in order, with the preview key they read.
const RATINGS: { value: RatingValue; label: string; cls: string; key: keyof SessionCard['preview'] }[] = [
  { value: Rating.Again, label: 'Again', cls: 'again', key: 'again' },
  { value: Rating.Hard, label: 'Hard', cls: 'hard', key: 'hard' },
  { value: Rating.Good, label: 'Good', cls: 'good', key: 'good' },
  { value: Rating.Easy, label: 'Easy', cls: 'easy', key: 'easy' },
]

export function Play({ sessionCard, index, total, rememberedSoFar, onGrade }: Props) {
  const [revealed, setRevealed] = useState(false)
  const [speaking, setSpeaking] = useState(false)
  const { card, preview, is_new } = sessionCard

  // Example sentences: lazily fetched on demand (the backend generates+caches).
  const [sentences, setSentences] = useState<Sentence[] | null>(null)
  const [sIdx, setSIdx] = useState(0)
  const [sLoading, setSLoading] = useState(false)
  const [sError, setSError] = useState<string | null>(null)

  const speak = async () => {
    setSpeaking(true)
    try {
      await playPronunciation(card.word)
    } finally {
      setSpeaking(false)
    }
  }

  const loadSentences = async () => {
    setSLoading(true)
    setSError(null)
    try {
      const list = await api.sentences(card.word)
      setSentences(list)
      setSIdx(0)
      if (list.length === 0) setSError('No example sentence available.')
    } catch {
      setSError('Example sentences are unavailable right now.')
    } finally {
      setSLoading(false)
    }
  }

  const stepSentence = (delta: number) => {
    if (!sentences || sentences.length === 0) return
    setSIdx((i) => (i + delta + sentences.length) % sentences.length)
  }

  const grade = (rating: RatingValue) => {
    setRevealed(false)
    onGrade(rating)
  }

  const rel = card.spanish.relation
  const isPhrase = card.kind === 'phrase'
  const pct = (index / total) * 100

  return (
    <div className="app">
      <div className="progress-row">
        <span>
          Card {index + 1} / {total}
        </span>
        <span>{rememberedSoFar} remembered</span>
      </div>
      <div className="progress-bar">
        <div className="progress-fill" style={{ width: `${pct}%` }} />
      </div>

      <div className="card">
        <div className="card-level">
          {isPhrase ? card.category : card.level}
          {is_new ? <span className="new-tag">new</span> : null}
        </div>
        <div className="word-row">
          <span className={isPhrase ? 'word phrase' : 'word'}>{card.word}</span>
          <button
            className={`speak-btn ${speaking ? 'speaking' : ''}`}
            onClick={speak}
            disabled={speaking}
            aria-label={`Listen to ${card.word}`}
            title="Listen to pronunciation"
          >
              <svg
                width="22"
                height="22"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
                strokeLinecap="round"
                strokeLinejoin="round"
                aria-hidden="true"
              >
                <path d="M11 5 6 9H2v6h4l5 4V5z" />
                <path d="M15.5 8.5a5 5 0 0 1 0 7" />
                <path d="M19 5a9 9 0 0 1 0 14" />
              </svg>
          </button>
        </div>
        {!isPhrase && <div className="pos">{card.pos}</div>}

        {!isPhrase && card.other_senses > 0 && (
          <div className="sense-hint">
            this word has {card.other_senses} other meaning
            {card.other_senses > 1 ? 's' : ''}
          </div>
        )}

        {revealed && isPhrase && (
          <>
            <div className="divider" />
            <div className="english">{card.english}</div>
            {card.spanish.word && (
              <div className="spanish">
                <div className="spanish-word">
                  Spanish: <span className="es">{card.spanish.word}</span>
                </div>
              </div>
            )}
          </>
        )}

        {revealed && !isPhrase && (
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

            <div className="examples">
              {sentences === null ? (
                <button
                  className="btn-ghost"
                  onClick={loadSentences}
                  disabled={sLoading}
                >
                  {sLoading ? 'Loading example…' : 'Show example sentence'}
                </button>
              ) : sentences.length > 0 ? (
                <div className="sentence">
                  <div className="sentence-it">{sentences[sIdx].italian}</div>
                  <div className="sentence-en">{sentences[sIdx].english}</div>
                  {sentences.length > 1 && (
                    <div className="sentence-nav">
                      <button
                        className="arrow"
                        onClick={() => stepSentence(-1)}
                        aria-label="Previous example"
                      >
                        ‹
                      </button>
                      <span className="sentence-count">
                        {sIdx + 1} / {sentences.length}
                      </span>
                      <button
                        className="arrow"
                        onClick={() => stepSentence(1)}
                        aria-label="Next example"
                      >
                        ›
                      </button>
                    </div>
                  )}
                </div>
              ) : null}
              {sError && <div className="sentence-error">{sError}</div>}
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
          <div className="rating-row">
            {RATINGS.map((r) => (
              <button
                key={r.value}
                className={`btn-rate ${r.cls}`}
                onClick={() => grade(r.value)}
              >
                <span className="rate-label">{r.label}</span>
                <span className="rate-interval">{preview[r.key]}</span>
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
