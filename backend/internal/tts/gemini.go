package tts

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/genai"
)

// DefaultModel, DefaultVoice and DefaultPrompt are used when not overridden.
const (
	DefaultModel = "gemini-2.5-flash-preview-tts"
	DefaultVoice = "Kore"
	// DefaultPrompt wraps the word in a short Italian instruction. Gemini TTS
	// infers the language/accent from the input text and ignores SpeechConfig's
	// LanguageCode for accent, so a bare word that's also an English word (e.g.
	// "fresco", "auto", "film") gets an English accent. Priming with adjacent
	// Italian text forces Italian phonetics. The "instruction: text" form makes
	// the model speak only the word that replaces {word}.
	DefaultPrompt = "Pronuncia in italiano: {word}"
)

// GeminiSynth synthesizes speech with the Gemini TTS model, forcing Italian.
type GeminiSynth struct {
	client *genai.Client
	model  string
	voice  string
	prompt string // template containing "{word}"
}

// NewGeminiSynth builds a Gemini-backed synthesizer. apiKey is required.
func NewGeminiSynth(ctx context.Context, apiKey, model, voice, prompt string) (*GeminiSynth, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("tts: missing API key")
	}
	if model == "" {
		model = DefaultModel
	}
	if voice == "" {
		voice = DefaultVoice
	}
	if prompt == "" {
		prompt = DefaultPrompt
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("tts: new client: %w", err)
	}
	return &GeminiSynth{client: client, model: model, voice: voice, prompt: prompt}, nil
}

// buildPrompt injects the word into the prompt template, forcing Italian
// context. If the template lacks a "{word}" placeholder, the word is appended.
func buildPrompt(tmpl, word string) string {
	if strings.Contains(tmpl, "{word}") {
		return strings.ReplaceAll(tmpl, "{word}", word)
	}
	return tmpl + " " + word
}

// Synthesize asks Gemini to pronounce text in Italian and returns WAV bytes.
func (g *GeminiSynth) Synthesize(ctx context.Context, text string) ([]byte, error) {
	resp, err := g.client.Models.GenerateContent(ctx, g.model, genai.Text(buildPrompt(g.prompt, text)),
		&genai.GenerateContentConfig{
			ResponseModalities: []string{"AUDIO"},
			SpeechConfig: &genai.SpeechConfig{
				LanguageCode: "it-IT",
				VoiceConfig: &genai.VoiceConfig{
					PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{VoiceName: g.voice},
				},
			},
		})
	if err != nil {
		return nil, fmt.Errorf("tts: generate: %w", err)
	}

	blob := firstAudio(resp)
	if blob == nil {
		return nil, fmt.Errorf("tts: no audio in response")
	}
	// Gemini returns raw little-endian PCM (e.g. "audio/L16;codec=pcm;rate=24000").
	return pcmToWAV(blob.Data, pcmRate(blob.MIMEType), 1, 16), nil
}

func firstAudio(resp *genai.GenerateContentResponse) *genai.Blob {
	for _, c := range resp.Candidates {
		if c.Content == nil {
			continue
		}
		for _, p := range c.Content.Parts {
			if p.InlineData != nil && strings.HasPrefix(p.InlineData.MIMEType, "audio/") {
				return p.InlineData
			}
		}
	}
	return nil
}

// pcmRate extracts the sample rate from a MIME like "audio/L16;codec=pcm;rate=24000".
func pcmRate(mime string) int {
	for _, part := range strings.Split(mime, ";") {
		if r, ok := strings.CutPrefix(strings.TrimSpace(part), "rate="); ok {
			if n, err := strconv.Atoi(r); err == nil && n > 0 {
				return n
			}
		}
	}
	return 24000
}

// pcmToWAV wraps raw little-endian PCM samples in a minimal WAV container.
func pcmToWAV(pcm []byte, sampleRate, channels, bitsPerSample int) []byte {
	byteRate := sampleRate * channels * bitsPerSample / 8
	blockAlign := channels * bitsPerSample / 8

	var buf bytes.Buffer
	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, uint32(36+len(pcm)))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(16))
	binary.Write(&buf, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(&buf, binary.LittleEndian, uint16(channels))
	binary.Write(&buf, binary.LittleEndian, uint32(sampleRate))
	binary.Write(&buf, binary.LittleEndian, uint32(byteRate))
	binary.Write(&buf, binary.LittleEndian, uint16(blockAlign))
	binary.Write(&buf, binary.LittleEndian, uint16(bitsPerSample))
	buf.WriteString("data")
	binary.Write(&buf, binary.LittleEndian, uint32(len(pcm)))
	buf.Write(pcm)
	return buf.Bytes()
}
