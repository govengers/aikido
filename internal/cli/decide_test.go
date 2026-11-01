package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
)

func TestDecideCommand_BuildsQuestionsAndPrintsAnswers(t *testing.T) {
	stub := llmtest.NewStubClient()
	stub.ScriptDecisions(llmtest.DecisionScript{Response: llm.DecisionResponse{
		Model: "typesafe/jev-1.14-20261014",
		Answers: map[string]llm.DecisionAnswer{
			"is_bug": {Kind: llm.DecisionNoul, Noul: 0.95},
			"team":   {Kind: llm.DecisionChoice, Choice: "payments", Confidence: 0.72, Probabilities: map[string]float64{"payments": 0.81, "frontend": 0.19}},
			"urgency": {
				Kind: llm.DecisionScore, Score: 1.99, Confidence: 1,
				Probabilities: map[string]float64{"0": 0, "1": 0.01, "2": 0.99}, Legend: map[string]string{"0": "can wait", "1": "this week", "2": "blocking"},
			},
		},
		Usage: &llm.Usage{PromptTokens: 418, CostUSD: 0.000017556, Provider: "TypeSafe"},
	}})
	var out bytes.Buffer
	app := NewApp(func() (llm.Client, error) { return stub, nil })
	app.Writer = &out
	err := app.Run(context.Background(), []string{
		"aikido", "decide",
		"--noul", "is_bug=Is this a bug report?",
		"--choice", "team=Which team, a=b style?=payments, frontend",
		"--score", "urgency=How urgent?=can wait,this week,blocking",
		"Checkout", "is", "blank.",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	request := stub.DecisionRequests()[0]
	if request.Model != defaultDecisionModel || request.State != "Checkout is blank." {
		t.Errorf("model/state = %q/%v", request.Model, request.State)
	}
	if question := request.Questions["team"]; question.Kind != llm.DecisionChoice || question.Instructions != "Which team, a=b style?" || len(question.Options) != 2 || question.Options["frontend"] != "frontend" {
		t.Errorf("choice = %+v", question)
	}
	if question := request.Questions["urgency"]; len(question.Levels) != 3 || question.Levels[2] != "blocking" {
		t.Errorf("score = %+v", question)
	}
	if request.Questions["is_bug"].Kind != llm.DecisionNoul {
		t.Errorf("noul = %+v", request.Questions["is_bug"])
	}
	output := out.String()
	for _, want := range []string{"is_bug   noul    0.95", "payments (confidence 0.72; payments 0.81, frontend 0.19)", "1.99 (confidence 1.00; blocking 0.99, this week 0.01, can wait 0.00)", "provider=TypeSafe", "model=typesafe/jev-1.14-20261014"} {
		if !strings.Contains(output, want) {
			t.Errorf("output lacks %q:\n%s", want, output)
		}
	}
}

func TestParseQuestions_Errors(t *testing.T) {
	for _, testCase := range []struct{ nouls, choices []string }{
		{nil, nil},
		{[]string{"no-equals"}, nil},
		{nil, []string{"team=Which team?"}},
		{[]string{"q=a"}, []string{"q=b=x,y"}},
	} {
		if _, err := parseQuestions(testCase.nouls, testCase.choices, nil, nil); err == nil {
			t.Errorf("%+v: want an error", testCase)
		}
	}
}

func TestDecideCommand_ReadsStateFromStdin(t *testing.T) {
	stub := llmtest.NewStubClient()
	stub.ScriptDecisions(llmtest.DecisionScript{Response: llm.DecisionResponse{Answers: map[string]llm.DecisionAnswer{
		"where": {Kind: llm.DecisionBox, Box: &llm.DecisionBoxArea{Top: 0.1, Bottom: 0.9, Left: 0.2, Right: 0.8}, Confidence: 0.5},
		"empty": {Kind: llm.DecisionBox},
	}}})
	app := NewApp(func() (llm.Client, error) { return stub, nil })
	var out bytes.Buffer
	app.Writer, app.Reader = &out, strings.NewReader("  piped state \n")
	if err := app.Run(context.Background(), []string{"aikido", "decide", "--box", "where=Where?", "--box", "empty=Anything?", "-"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if state := stub.DecisionRequests()[0].State; state != "piped state" {
		t.Errorf("state = %v, want the trimmed stdin", state)
	}
	for _, want := range []string{"top=0.10 bottom=0.90 left=0.20 right=0.80 (confidence 0.50)", "empty  box  -"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestDecisionState(t *testing.T) {
	image := llm.ImagePart{URL: "data:image/png;base64,AA=="}
	testCases := []struct {
		name      string
		text      string
		images    []llm.ImagePart
		want      any
		wantError bool
	}{
		{name: "text only", text: "state", want: "state"},
		{name: "nothing", wantError: true},
		{name: "image only", images: []llm.ImagePart{image}, want: []any{image}},
		{name: "text and image", text: "state", images: []llm.ImagePart{image}, want: []any{"state", image}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := decisionState(testCase.text, testCase.images)
			if (err != nil) != testCase.wantError {
				t.Fatalf("err = %v, wantError %v", err, testCase.wantError)
			}
			if fmt.Sprint(got) != fmt.Sprint(testCase.want) {
				t.Errorf("state = %v, want %v", got, testCase.want)
			}
		})
	}
}
