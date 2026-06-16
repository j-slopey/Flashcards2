// Package tts provides lazy, cached text-to-speech for Italian word
// pronunciations. A Synthesizer produces audio for a word the first time it's
// requested; the result is cached on disk and served directly thereafter.
package tts

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrUnavailable is returned when no synthesizer is configured (e.g. no API key).
var ErrUnavailable = errors.New("pronunciation synthesizer not configured")

// Synthesizer turns Italian text into a self-contained audio clip (WAV bytes).
type Synthesizer interface {
	Synthesize(ctx context.Context, text string) ([]byte, error)
}

// Cache lazily synthesizes word pronunciations and caches them as files on disk.
// Concurrent requests for the same word generate it only once.
type Cache struct {
	dir   string
	synth Synthesizer

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewCache creates the cache directory and returns a Cache backed by synth.
func NewCache(dir string, synth Synthesizer) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Cache{dir: dir, synth: synth, locks: map[string]*sync.Mutex{}}, nil
}

// key is the cache identity of a word: case/space-insensitive SHA-1, so accents
// and casing map to one stable, filesystem-safe filename.
func key(word string) string {
	sum := sha1.Sum([]byte(strings.ToLower(strings.TrimSpace(word))))
	return hex.EncodeToString(sum[:])
}

func (c *Cache) path(word string) string {
	return filepath.Join(c.dir, key(word)+".wav")
}

// Audio returns WAV bytes for word, synthesizing and caching on first use.
func (c *Cache) Audio(ctx context.Context, word string) ([]byte, error) {
	p := c.path(word)
	if b, err := os.ReadFile(p); err == nil {
		return b, nil
	}

	// Serialize concurrent first-plays of the same word.
	lock := c.wordLock(word)
	lock.Lock()
	defer lock.Unlock()
	if b, err := os.ReadFile(p); err == nil { // re-check after acquiring the lock
		return b, nil
	}

	if c.synth == nil {
		return nil, ErrUnavailable
	}
	wav, err := c.synth.Synthesize(ctx, word)
	if err != nil {
		return nil, err
	}

	// Write atomically (temp + rename) so a half-written file is never served.
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, wav, 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, p); err != nil {
		return nil, err
	}
	return wav, nil
}

func (c *Cache) wordLock(word string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := key(word)
	l, ok := c.locks[k]
	if !ok {
		l = &sync.Mutex{}
		c.locks[k] = l
	}
	return l
}
