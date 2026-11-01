//go:build smoke

package openrouter

import (
	"bytes"
	"context"
	"encoding/binary"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/govengers/aikido/llm"
)

// TestSmoke_Transcribe sends faint noise, which must come back empty (models
// like to invent a sentence for it), and, where macOS `say` is available, a
// spoken sentence, which must come back as text.
func TestSmoke_Transcribe(t *testing.T) {
	if os.Getenv("AIKIDO_LIVE_SMOKE") != "1" {
		t.Skip("set AIKIDO_LIVE_SMOKE=1 to run the live smoke test")
	}
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		t.Skip("OPENROUTER_API_KEY is not set")
	}
	client, err := NewClient(&Options{APIKey: apiKey, XTitle: "aikido-smoke"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	text, usage, err := llm.Transcribe(ctx, client, llm.TranscribeRequest{Audio: noiseWAV(2 * 16000), Format: "wav"})
	if err != nil || text != "" {
		t.Errorf("noise: %q, %v", text, err)
	}
	t.Logf("noise: usage %+v", usage)

	if _, err := exec.LookPath("say"); err != nil {
		t.Log("no `say` on PATH, speech clip skipped")
		return
	}
	path := filepath.Join(t.TempDir(), "speech.wav")
	if output, err := exec.CommandContext(ctx, "say", "-o", path, "--data-format=LEI16@16000", "The quick brown fox jumps over the lazy dog.").CombinedOutput(); err != nil {
		t.Fatalf("say: %v: %s", err, output)
	}
	clip, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text, _, err = llm.Transcribe(ctx, client, llm.TranscribeRequest{Audio: clip, Format: "wav"})
	if err != nil || !strings.Contains(strings.ToLower(text), "brown fox") {
		t.Errorf("speech: %q, %v", text, err)
	}
	t.Logf("speech: %q", text)
}

// TestSmoke_Speak speaks a short German answer with the ElevenLabs turbo
// model, then transcribes it back: the audio must be valid mp3 that a
// listener (here a model) understands. About 0.002 USD.
func TestSmoke_Speak(t *testing.T) {
	if os.Getenv("AIKIDO_LIVE_SMOKE") != "1" {
		t.Skip("set AIKIDO_LIVE_SMOKE=1 to run the live smoke test")
	}
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		t.Skip("OPENROUTER_API_KEY is not set")
	}
	client, err := NewClient(&Options{APIKey: apiKey, XTitle: "aikido-smoke"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	response, err := client.Speak(ctx, llm.SpeechRequest{
		Model:  "elevenlabs/eleven-v4-turbo",
		Input:  "Die Dusche kommt an die Fensterwand.",
		Voice:  "sarah",
		Format: "mp3",
	})
	if err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if response.ContentType != "audio/mpeg" || len(response.Audio) < 1000 || response.GenerationID == "" {
		t.Fatalf("response: %q, %d bytes, id %q", response.ContentType, len(response.Audio), response.GenerationID)
	}
	text, _, err := llm.Transcribe(ctx, client, llm.TranscribeRequest{Audio: response.Audio, Format: "mp3", Hint: "The speaker speaks German."})
	if err != nil || !strings.Contains(strings.ToLower(text), "dusche") {
		t.Errorf("round trip: %q, %v", text, err)
	}
	t.Logf("speak: %d bytes, %s, transcript %q", len(response.Audio), response.GenerationID, text)
}

// noiseWAV returns that many samples of faint 16 kHz mono PCM noise as a WAV file.
func noiseWAV(samples int) []byte {
	var wav bytes.Buffer
	write := func(value any) { _ = binary.Write(&wav, binary.LittleEndian, value) }
	wav.WriteString("RIFF")
	write(uint32(36 + 2*samples))
	wav.WriteString("WAVEfmt ")
	write(uint32(16))
	write(uint16(1))     // PCM
	write(uint16(1))     // mono
	write(uint32(16000)) // sample rate
	write(uint32(32000)) // byte rate
	write(uint16(2))     // block align
	write(uint16(16))    // bits per sample
	wav.WriteString("data")
	write(uint32(2 * samples))
	noise := rand.New(rand.NewPCG(1, 2))
	for range samples {
		write(int16(noise.IntN(200) - 100))
	}
	return wav.Bytes()
}
