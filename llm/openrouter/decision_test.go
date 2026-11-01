package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/govengers/aikido/llm"
)

// liveDecisionsAnswer is a real answer of typesafe/jev-1.14 (22.10.2026) plus
// a box answer from the same day.
const liveDecisionsAnswer = `{"model":"typesafe/jev-1.14-20261014","answers":{
"is_bug":{"type":"noul","noul":0.95},
"team":{"type":"choice","choice":"payments","probabilities":{"payments":0.81,"frontend":0.19,"account":0},"confidence":0.72},
"urgency":{"type":"score","score":1.99,"legend":{"0":"can wait","1":"this week","2":"blocking revenue"},"probabilities":{"0":0,"1":0,"2":1},"confidence":1},
"red":{"type":"box","box":{"top":0.05,"bottom":0.96,"left":0.04,"right":0.97},"confidence":0.02}},
"usage":{"input_tokens":418,"output_tokens":70,"cost":0.000017556},"id":"gen-dec-1","provider":"TypeSafe"}`

var fakePNGBytes = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

func TestDecide_RoundTrip(t *testing.T) {
	t.Parallel()
	var (
		path string
		sent map[string]any
	)
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		path = request.URL.Path
		rawBody, _ := io.ReadAll(request.Body)
		if err := json.Unmarshal(rawBody, &sent); err != nil {
			t.Error(err)
		}
		_, _ = writer.Write([]byte(liveDecisionsAnswer))
	})
	// BaseURL with /v1 like the default: the Decisions API sits above it.
	client, err := NewClient(&Options{APIKey: "k", BaseURL: server.URL + "/v1", ProviderOrder: []string{"typesafe"}})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	response, err := client.Decide(context.Background(), llm.DecisionRequest{
		Model: "typesafe/jev-1.14",
		State: []any{"Checkout is blank after Pay.", llm.ImagePart{Data: fakePNGBytes, ContentType: "image/png"}},
		Questions: map[string]llm.DecisionQuestion{
			"is_bug":  llm.NoulQuestion("Is it a defect?"),
			"team":    llm.ChoiceQuestion("Which team?", map[string]string{"payments": "billing", "frontend": "UI", "account": "login"}),
			"urgency": llm.ScoreQuestion("How urgent?", "can wait", "this week", "blocking revenue"),
			"red":     llm.BoxQuestion("The red rectangle"),
		},
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if path != "/alpha/decisions" {
		t.Errorf("path = %s, want /alpha/decisions", path)
	}
	assertDecisionWire(t, sent)
	assertDecisionAnswers(t, response)
}

// assertDecisionWire checks criteria per kind, the image in the state and
// provider routing.
func assertDecisionWire(t *testing.T, sent map[string]any) {
	t.Helper()
	questions := sent["questions"].(map[string]any)
	if _, has := questions["is_bug"].(map[string]any)["criteria"]; has {
		t.Error("noul without options must send no criteria")
	}
	if levels := questions["urgency"].(map[string]any)["criteria"].([]any); len(levels) != 3 || levels[2] != "blocking revenue" {
		t.Errorf("score criteria = %v", levels)
	}
	if questions["team"].(map[string]any)["criteria"].(map[string]any)["payments"] != "billing" || questions["red"].(map[string]any)["type"] != "box" {
		t.Errorf("questions = %v", questions)
	}
	state := sent["state"].([]any)
	if image := state[1].(map[string]any)["image_url"].(map[string]any)["url"].(string); !strings.HasPrefix(image, "data:image/png;base64,") {
		t.Errorf("state image = %v", state[1])
	}
	if sent["provider"].(map[string]any)["order"].([]any)[0] != "typesafe" {
		t.Errorf("provider = %v", sent["provider"])
	}
}

func assertDecisionAnswers(t *testing.T, response llm.DecisionResponse) {
	t.Helper()
	answers := response.Answers
	if answers["is_bug"].Kind != llm.DecisionNoul || answers["is_bug"].Noul != 0.95 {
		t.Errorf("noul = %+v", answers["is_bug"])
	}
	if answers["team"].Choice != "payments" || answers["team"].Probabilities["frontend"] != 0.19 || answers["team"].Confidence != 0.72 {
		t.Errorf("choice = %+v", answers["team"])
	}
	if answers["urgency"].Score != 1.99 || answers["urgency"].Legend["2"] != "blocking revenue" {
		t.Errorf("score = %+v", answers["urgency"])
	}
	if box := answers["red"].Box; box == nil || box.Top != 0.05 || box.Right != 0.97 {
		t.Errorf("box = %+v", answers["red"])
	}
	if response.Model != "typesafe/jev-1.14-20261014" || response.ID != "gen-dec-1" {
		t.Errorf("model/id = %s/%s", response.Model, response.ID)
	}
	if usage := response.Usage; usage.PromptTokens != 418 || usage.CompletionTokens != 70 || usage.CostUSD != 0.000017556 || usage.Provider != "TypeSafe" || usage.Duration <= 0 || usage.TokensPerSecond != 0 {
		t.Errorf("usage = %+v", usage)
	}
}

func TestDecide_Validation(t *testing.T) {
	t.Parallel()
	client, err := NewClient(&Options{APIKey: "k", BaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	questions := map[string]llm.DecisionQuestion{"q": llm.NoulQuestion("ok?")}
	for name, request := range map[string]llm.DecisionRequest{
		"no model":                 {State: "s", Questions: questions},
		"no state":                 {Model: "m", Questions: questions},
		"no questions":             {Model: "m", State: "s"},
		"one option":               {Model: "m", State: "s", Questions: map[string]llm.DecisionQuestion{"q": llm.ChoiceQuestion("x", map[string]string{"a": "a"})}},
		"no levels":                {Model: "m", State: "s", Questions: map[string]llm.DecisionQuestion{"q": llm.ScoreQuestion("x")}},
		"half noul options":        {Model: "m", State: "s", Questions: map[string]llm.DecisionQuestion{"q": {Kind: llm.DecisionNoul, Instructions: "x", Options: map[string]string{"true": "y"}}}},
		"unknown kind":             {Model: "m", State: "s", Questions: map[string]llm.DecisionQuestion{"q": {Kind: "maybe", Instructions: "x"}}},
		"no instructions":          {Model: "m", State: "s", Questions: map[string]llm.DecisionQuestion{"q": llm.BoxQuestion("")}},
		"state image without data": {Model: "m", State: []any{&llm.ImagePart{}}, Questions: questions},
	} {
		if _, err := client.Decide(context.Background(), request); !errors.Is(err, llm.ErrInvalidRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
}

func TestDecide_ErrorsAndMissingAnswer(t *testing.T) {
	t.Parallel()
	answers := make(chan [2]string, 2)
	answers <- [2]string{"422", `{"error":{"message":"HTTP 422: criteria should have at least 1 item","code":422}}`}
	answers <- [2]string{"200", `{"model":"m","answers":{},"usage":{"input_tokens":1,"output_tokens":0}}`}
	server := newTestServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		answer := <-answers
		if answer[0] == "422" {
			writer.WriteHeader(http.StatusUnprocessableEntity)
		}
		_, _ = writer.Write([]byte(answer[1]))
	})
	client, err := NewClient(&Options{APIKey: "k", BaseURL: server.URL, MaximumAttempts: 1})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	request := llm.DecisionRequest{Model: "m", State: "s", Questions: map[string]llm.DecisionQuestion{"q": llm.NoulQuestion("ok?")}}

	if _, err := client.Decide(context.Background(), request); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Errorf("422: err = %v", err)
	}
	if _, err := client.Decide(context.Background(), request); !errors.Is(err, llm.ErrServerError) || !strings.Contains(err.Error(), `"q"`) {
		t.Errorf("missing answer: err = %v", err)
	}
}

func TestDecisionModels_Queries(t *testing.T) {
	t.Parallel()
	var query string
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		query = request.URL.RawQuery
		_, _ = writer.Write([]byte(`{"data":[{"id":"typesafe/jev-1.14"}]}`))
	})
	client := newTestClient(t, server)

	models, err := client.DecisionModels(context.Background())
	if err != nil {
		t.Fatalf("DecisionModels: %v", err)
	}
	if query != "output_modalities=decisions" || len(models) != 1 || models[0].ID != "typesafe/jev-1.14" {
		t.Errorf("query %q, models %+v", query, models)
	}
}

func TestLegendText(t *testing.T) {
	t.Parallel()
	if got := legendText("can wait"); got != "can wait" {
		t.Errorf("string level = %q", got)
	}
	if got := legendText(map[string]any{"label": "x"}); got != `{"label":"x"}` {
		t.Errorf("structured level = %q", got)
	}
}
