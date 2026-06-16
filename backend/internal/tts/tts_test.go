package tts

import (
	"bytes"
	"context"
	"encoding/binary"
	"sync"
	"sync/atomic"
	"testing"
)

// stubSynth records how many times it's called and returns canned bytes.
type stubSynth struct{ calls atomic.Int32 }

func (s *stubSynth) Synthesize(_ context.Context, text string) ([]byte, error) {
	s.calls.Add(1)
	return []byte("audio:" + text), nil
}

func TestCacheGeneratesOnceThenServesFromDisk(t *testing.T) {
	synth := &stubSynth{}
	c, err := NewCache(t.TempDir(), synth)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	for i := 0; i < 3; i++ {
		b, err := c.Audio(context.Background(), "caffè")
		if err != nil {
			t.Fatalf("Audio: %v", err)
		}
		if string(b) != "audio:caffè" {
			t.Fatalf("got %q", b)
		}
	}
	if synth.calls.Load() != 1 {
		t.Fatalf("synth called %d times, want 1 (rest from cache)", synth.calls.Load())
	}
}

func TestCacheKeyIsCaseAndSpaceInsensitive(t *testing.T) {
	synth := &stubSynth{}
	c, _ := NewCache(t.TempDir(), synth)
	c.Audio(context.Background(), "Ciao")
	c.Audio(context.Background(), "  ciao ")
	if synth.calls.Load() != 1 {
		t.Fatalf("synth called %d times, want 1 (same key)", synth.calls.Load())
	}
}

func TestConcurrentFirstPlayGeneratesOnce(t *testing.T) {
	synth := &stubSynth{}
	c, _ := NewCache(t.TempDir(), synth)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Audio(context.Background(), "subito"); err != nil {
				t.Errorf("Audio: %v", err)
			}
		}()
	}
	wg.Wait()
	if synth.calls.Load() != 1 {
		t.Fatalf("synth called %d times under concurrency, want 1", synth.calls.Load())
	}
}

func TestNoSynthIsUnavailable(t *testing.T) {
	c, _ := NewCache(t.TempDir(), nil)
	if _, err := c.Audio(context.Background(), "ciao"); err != ErrUnavailable {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestPCMToWAVHeader(t *testing.T) {
	pcm := make([]byte, 100)
	wav := pcmToWAV(pcm, 24000, 1, 16)

	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Fatalf("bad RIFF/WAVE magic: %q %q", wav[0:4], wav[8:12])
	}
	if string(wav[36:40]) != "data" {
		t.Fatalf("missing data chunk: %q", wav[36:40])
	}
	var dataLen uint32
	binary.Read(bytes.NewReader(wav[40:44]), binary.LittleEndian, &dataLen)
	if int(dataLen) != len(pcm) {
		t.Fatalf("data length = %d, want %d", dataLen, len(pcm))
	}
	if len(wav) != 44+len(pcm) {
		t.Fatalf("total length = %d, want %d", len(wav), 44+len(pcm))
	}

	var rate uint32
	binary.Read(bytes.NewReader(wav[24:28]), binary.LittleEndian, &rate)
	if rate != 24000 {
		t.Fatalf("sample rate = %d, want 24000", rate)
	}
}

func TestBuildPrompt(t *testing.T) {
	if got := buildPrompt(DefaultPrompt, "fresco"); got != "Pronuncia in italiano: fresco" {
		t.Errorf("default template: got %q", got)
	}
	if got := buildPrompt("Di' {word} in italiano", "auto"); got != "Di' auto in italiano" {
		t.Errorf("mid-placeholder: got %q", got)
	}
	// No placeholder -> word is appended so it's never dropped.
	if got := buildPrompt("Pronuncia:", "film"); got != "Pronuncia: film" {
		t.Errorf("no placeholder: got %q", got)
	}
}

func TestPCMRateParsing(t *testing.T) {
	cases := map[string]int{
		"audio/L16;codec=pcm;rate=24000": 24000,
		"audio/L16;rate=16000":           16000,
		"audio/wav":                      24000, // default
	}
	for mime, want := range cases {
		if got := pcmRate(mime); got != want {
			t.Errorf("pcmRate(%q) = %d, want %d", mime, got, want)
		}
	}
}
