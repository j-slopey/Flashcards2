// Typed client for the flashcards Go API. Calls go through Vite's /api proxy.

export type SpanishRelation = 'cognate' | 'false_friend' | 'none'

export interface Spanish {
  relation: SpanishRelation
  word: string
  note: string
}

export interface Card {
  id: number
  level: string
  word: string
  pos: string
  english: string
  spanish: Spanish
  other_senses: number
}

// FSRS ratings (must match the backend / go-fsrs values).
export const Rating = { Again: 1, Hard: 2, Good: 3, Easy: 4 } as const
export type RatingValue = (typeof Rating)[keyof typeof Rating]

// Predicted "next review in …" per rating, previewed when the card is served.
export interface Intervals {
  again: string
  hard: string
  good: string
  easy: string
}

export interface SessionCard {
  position: number
  card: Card
  is_new: boolean
  preview: Intervals
  answered: boolean
  rating: RatingValue | null
}

export interface Session {
  id: number
  levels: string[]
  completed: boolean
  new_count: number
  due_count: number
  cards: SessionCard[]
}

export interface Level {
  level: string
  count: number
}

export interface Progress {
  answered: number
  remembered: number
  total: number
  completed: boolean
}

export interface Settings {
  new_cards_per_day: number
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`/api${path}`, {
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
  if (!res.ok) {
    const body = await res.json().catch(() => ({}))
    throw new Error(body.error ?? `request failed: ${res.status}`)
  }
  return res.json() as Promise<T>
}

export const api = {
  levels: () => request<Level[]>('/levels'),

  getSettings: () => request<Settings>('/settings'),

  saveSettings: (settings: Settings) =>
    request<Settings>('/settings', {
      method: 'PUT',
      body: JSON.stringify(settings),
    }),

  createSession: (levels: string[]) =>
    request<Session>('/sessions', {
      method: 'POST',
      body: JSON.stringify({ levels }),
    }),

  answer: (sessionId: number, cardId: number, rating: RatingValue) =>
    request<Progress>(`/sessions/${sessionId}/answers`, {
      method: 'POST',
      body: JSON.stringify({ card_id: cardId, rating }),
    }),
}
