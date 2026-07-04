// Package sentence generates example sentences for Italian vocabulary words via
// the Gemini API. It implements store.SentenceGenerator; the store owns caching
// and the many-to-many linking of sentences to words.
package sentence

import (
	"context"
	"encoding/json"
	"fmt"

	"flashcards/backend/internal/store"
	"google.golang.org/genai"
)

// DefaultModel is a fast, inexpensive text model suited to short generations.
const DefaultModel = "gemini-3.1-flash-lite"

// Gemini generates example sentences with structured JSON output.
type Gemini struct {
	client *genai.Client
	model  string
}

// NewGemini builds a Gemini-backed sentence generator. apiKey is required.
func NewGemini(ctx context.Context, apiKey, model string) (*Gemini, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("sentence: missing API key")
	}
	if model == "" {
		model = DefaultModel
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("sentence: new client: %w", err)
	}
	return &Gemini{client: client, model: model}, nil
}

// result mirrors the JSON schema we ask Gemini to fill.
type result struct {
	Italian string `json:"italian"`
	English string `json:"english"`
	Words   []struct {
		Lemma   string `json:"lemma"`
		Surface string `json:"surface"`
	} `json:"words"`
}

// schema constrains the model to the JSON shape we parse.
var schema = &genai.Schema{
	Type: genai.TypeObject,
	Properties: map[string]*genai.Schema{
		"italian": {Type: genai.TypeString},
		"english": {Type: genai.TypeString},
		"words": {
			Type: genai.TypeArray,
			Items: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"lemma":   {Type: genai.TypeString},
					"surface": {Type: genai.TypeString},
				},
				Required: []string{"lemma", "surface"},
			},
		},
	},
	Required: []string{"italian", "english", "words"},
}

// Generate asks Gemini for one short Italian sentence featuring word, with an
// English translation and the lemma+surface of every meaningful word in it.
func (g *Gemini) Generate(ctx context.Context, word string) (store.SentenceDraft, error) {
	prompt := fmt.Sprintf(`You help an English speaker who also knows Spanish learn Italian.
Write ONE short, natural Italian example sentence (A2–B1 level, at most ~12 words)
that clearly shows how the Italian word %q is used in everyday context.
Return JSON with:
- "italian": the sentence.
- "english": a faithful, natural English translation.
- "words": the meaningful vocabulary words in the sentence — nouns, verbs,
  adjectives, adverbs; skip articles, prepositions, and pronouns. For each, give
  its dictionary base form as "lemma" and the exact form written in the sentence
  as "surface". Always include %q itself.`, word, word)

	resp, err := g.client.Models.GenerateContent(ctx, g.model, genai.Text(prompt),
		&genai.GenerateContentConfig{
			ResponseMIMEType: "application/json",
			ResponseSchema:   schema,
		})
	if err != nil {
		return store.SentenceDraft{}, fmt.Errorf("sentence: generate: %w", err)
	}

	var r result
	if err := json.Unmarshal([]byte(resp.Text()), &r); err != nil {
		return store.SentenceDraft{}, fmt.Errorf("sentence: parse response: %w", err)
	}
	if r.Italian == "" || r.English == "" {
		return store.SentenceDraft{}, fmt.Errorf("sentence: empty generation")
	}

	draft := store.SentenceDraft{Italian: r.Italian, English: r.English}
	for _, w := range r.Words {
		if w.Lemma == "" {
			continue
		}
		surface := w.Surface
		if surface == "" {
			surface = w.Lemma
		}
		draft.Words = append(draft.Words, store.WordRef{Lemma: w.Lemma, Surface: surface})
	}
	return draft, nil
}
