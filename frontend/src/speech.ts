// Browser text-to-speech for Italian pronunciation via the Web Speech API.

export function speechSupported(): boolean {
  return typeof window !== 'undefined' && 'speechSynthesis' in window
}

let italianVoice: SpeechSynthesisVoice | null = null

function pickItalianVoice(): SpeechSynthesisVoice | null {
  if (!speechSupported()) return null
  const voices = window.speechSynthesis.getVoices()
  // Prefer a precise it-IT voice, then any Italian voice.
  return (
    voices.find((v) => v.lang === 'it-IT') ??
    voices.find((v) => v.lang.toLowerCase().startsWith('it')) ??
    null
  )
}

// Voices populate asynchronously in most browsers; refresh our pick when they do.
if (speechSupported()) {
  italianVoice = pickItalianVoice()
  window.speechSynthesis.addEventListener('voiceschanged', () => {
    italianVoice = pickItalianVoice()
  })
}

// speakItalian pronounces the given text with the browser's Italian voice if
// available. Used as a fallback when server-side audio isn't reachable.
export function speakItalian(text: string): void {
  if (!speechSupported()) return
  const synth = window.speechSynthesis
  synth.cancel() // interrupt anything already playing
  const utter = new SpeechSynthesisUtterance(text)
  utter.lang = 'it-IT'
  if (!italianVoice) italianVoice = pickItalianVoice()
  if (italianVoice) utter.voice = italianVoice
  utter.rate = 0.9
  synth.speak(utter)
}

// playPronunciation plays the backend's high-quality TTS for a word, falling
// back to the browser's speech synthesis if the server has no audio (e.g. TTS
// not configured, or offline). Resolves once playback starts (or fallback runs).
export async function playPronunciation(word: string): Promise<void> {
  try {
    const res = await fetch(`/api/audio?word=${encodeURIComponent(word)}`)
    if (!res.ok) throw new Error(`audio unavailable: ${res.status}`)
    const url = URL.createObjectURL(await res.blob())
    const audio = new Audio(url)
    audio.addEventListener('ended', () => URL.revokeObjectURL(url))
    await audio.play()
  } catch {
    speakItalian(word)
  }
}
