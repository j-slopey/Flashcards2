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
  pos_display: string
  english: string
  spanish: Spanish
  other_senses: number
}

export interface SessionCard {
  position: number
  card: Card
  answered: boolean
  correct: boolean | null
}

export interface Session {
  id: number
  levels: string[]
  size: number
  completed: boolean
  cards: SessionCard[]
}

export interface Level {
  level: string
  count: number
}

export interface Progress {
  answered: number
  correct: number
  total: number
  completed: boolean
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

  createSession: (levels: string[], size = 20) =>
    request<Session>('/sessions', {
      method: 'POST',
      body: JSON.stringify({ levels, size }),
    }),

  answer: (sessionId: number, cardId: number, correct: boolean) =>
    request<Progress>(`/sessions/${sessionId}/answers`, {
      method: 'POST',
      body: JSON.stringify({ card_id: cardId, correct }),
    }),
}
