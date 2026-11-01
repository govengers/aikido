// voicebot is the chatbot example with a voice: an agent over an embedded
// markdown corpus (a small German bathroom-planning knowledge base), served
// as a web page with push-to-talk, a text box and spoken answers.
//
// All four combinations of voice or text in and text or voice out run through
// one voice.Pipeline and one POST /api/turn endpoint that streams NDJSON.
//
//	go run ./examples/voicebot
//
// Then open http://localhost:8080. Set OPENROUTER_API_KEY first; ADDR
// overrides the listen address. The page and its script are embedded, there
// is no build step. One conversation per process, shared by every tab.
package main

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/openrouter"
	"github.com/govengers/aikido/tools"
	"github.com/govengers/aikido/vfs/embedfs"
	"github.com/govengers/aikido/voice"
)

//go:embed knowledge/*.md
var knowledgeFS embed.FS

//go:embed index.html
var indexHTML []byte

const systemPrompt = `Du bist ein Badplaner. In deinem Arbeitsbereich liegen Markdown-Dateien mit ` +
	`Planungswissen und Produkten. Nutze list_files, search und read_file, bevor du antwortest, ` +
	`und antworte nur mit dem, was dort steht. Steht es nicht dort, sag das offen. Antworte in der ` +
	`Sprache des Nutzers.`

// newVocabulary is what the speech recognizer should spell exactly: brand,
// series and article numbers from the corpus, plus trade words.
func newVocabulary() *llm.Vocabulary {
	return &llm.Vocabulary{
		Terms: []string{"Liv", "PFEIFFER & MAY", "LIV60G", "LIV80W", "LIV80U", "Duschtasse", "Waschtisch", "Unterschrank", "Vorwandinstallation", "Walk-in-Dusche"},
		Replacements: map[string]string{
			"pfeifer und mai":  "PFEIFFER & MAY",
			"pfeiffer und mai": "PFEIFFER & MAY",
			"pfeiffer und may": "PFEIFFER & MAY",
		},
		Fuzzy: true,
	}
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		return errors.New("OPENROUTER_API_KEY not set")
	}
	address := os.Getenv("ADDR")
	if address == "" {
		address = "localhost:8080"
	}
	client, err := openrouter.NewClient(&openrouter.Options{
		APIKey:      apiKey,
		HTTPReferer: "https://github.com/govengers/aikido",
		XTitle:      "aikido voicebot example",
	})
	if err != nil {
		return err
	}
	session, err := newSession(client)
	if err != nil {
		return err
	}
	turn, err := newTurnHandler(client, session)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("POST /api/turn", turn)
	mux.HandleFunc("GET /{$}", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = writer.Write(indexHTML) // the browser went away; nothing left to tell it
	})
	// LAY-006. No WriteTimeout: a turn streams its answer for as long as the
	// model and the speech synthesis take.
	server := &http.Server{
		Addr:              address,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	fmt.Printf("voicebot on http://%s\n", address)
	return server.ListenAndServe()
}

func newSession(client *openrouter.Client) (*agent.Session, error) {
	knowledge, err := fs.Sub(knowledgeFS, "knowledge")
	if err != nil {
		return nil, err
	}
	storage, err := embedfs.New(&embedfs.Options{FileSystem: knowledge})
	if err != nil {
		return nil, err
	}
	registry := tools.NewRegistry()
	if err := agent.RegisterVFSTools(registry, &agent.VFSToolOptions{
		Storage:  storage,
		ReadOnly: true,
	}); err != nil {
		return nil, err
	}
	return agent.NewLocalSession(&agent.SessionOptions{
		ID:           "voicebot",
		Client:       client,
		Tools:        registry,
		Model:        "anthropic/claude-haiku-4.5",
		SystemPrompt: systemPrompt,
		MaximumTurns: 10,
	})
}

// newTurnHandler serves POST /api/turn: voice or text in, text or voice out,
// all on the one session.
func newTurnHandler(client *openrouter.Client, session *agent.Session) (*voice.Handler, error) {
	pipeline, err := voice.New(&voice.Options{
		Transcriber:           client,
		Vocabulary:            newVocabulary(),
		Speech:                client,
		SpeechModel:           voice.DefaultSpeechModel, // elevenlabs/eleven-v4-turbo
		Voice:                 "sarah",
		SpeechUSDPerCharacter: 0.00002,
		SpokenStyle:           voice.DefaultSpokenStyle,
	})
	if err != nil {
		return nil, err
	}
	return voice.NewHandler(&voice.HandlerOptions{
		Pipeline: pipeline,
		Session:  func(*http.Request) (voice.Runner, error) { return session, nil },
	})
}
