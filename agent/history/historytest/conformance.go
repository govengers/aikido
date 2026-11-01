// Package historytest is the conformance suite every agent.History backend
// runs in its tests.
package historytest

import (
	"context"
	"slices"
	"testing"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/llm"
)

// RunConformance executes the standard suite against a History factory.
//
// factory must return a new, empty History on every call. Sub-tests are
// serial. Implementations test their backend by calling this from a TestX
// function in their package.
func RunConformance(t *testing.T, factory func() agent.History) {
	t.Helper()
	conformanceTests := []struct {
		name string
		run  func(t *testing.T, history agent.History)
	}{
		{"ReadEmpty", testReadEmpty},
		{"AppendThenRead", testAppendThenRead},
		{"AppendsAccumulate", testAppendsAccumulate},
		{"PerSessionIsolation", testPerSessionIsolation},
		{"ReadIsolatedFromMutation", testReadIsolatedFromMutation},
		{"EmptyAppendIsNoop", testEmptyAppendIsNoop},
	}
	for _, conformanceTest := range conformanceTests {
		t.Run(conformanceTest.name, func(t *testing.T) {
			conformanceTest.run(t, factory())
		})
	}
}

func testReadEmpty(t *testing.T, history agent.History) {
	expectContents(t, mustRead(t, history, "s"))
}

func testAppendThenRead(t *testing.T, history agent.History) {
	mustAppend(t, history, "s",
		llm.Message{Role: llm.RoleUser, Content: "hi"},
		llm.Message{Role: llm.RoleAssistant, Content: "hello"},
	)
	expectContents(t, mustRead(t, history, "s"), "hi", "hello")
}

func testAppendsAccumulate(t *testing.T, history agent.History) {
	mustAppend(t, history, "s", llm.Message{Role: llm.RoleUser, Content: "a"})
	mustAppend(t, history, "s", llm.Message{Role: llm.RoleUser, Content: "b"})
	expectContents(t, mustRead(t, history, "s"), "a", "b")
}

func testPerSessionIsolation(t *testing.T, history agent.History) {
	mustAppend(t, history, "s1", llm.Message{Role: llm.RoleUser, Content: "x"})
	mustAppend(t, history, "s2", llm.Message{Role: llm.RoleUser, Content: "y"})
	expectContents(t, mustRead(t, history, "s1"), "x")
	expectContents(t, mustRead(t, history, "s2"), "y")
}

func testReadIsolatedFromMutation(t *testing.T, history agent.History) {
	mustAppend(t, history, "s", llm.Message{Role: llm.RoleUser, Content: "a"})
	if messages := mustRead(t, history, "s"); len(messages) > 0 {
		messages[0].Content = "MUTATED"
	}
	expectContents(t, mustRead(t, history, "s"), "a")
}

func testEmptyAppendIsNoop(t *testing.T, history agent.History) {
	mustAppend(t, history, "s")
	expectContents(t, mustRead(t, history, "s"))
}

func mustAppend(t *testing.T, history agent.History, sessionID string, messages ...llm.Message) {
	t.Helper()
	if err := history.Append(context.Background(), sessionID, messages...); err != nil {
		t.Fatalf("Append(%s): %v", sessionID, err)
	}
}

// expectContents checks that messages hold exactly the given contents, in
// order.
func expectContents(t *testing.T, messages []llm.Message, contents ...string) {
	t.Helper()
	got := make([]string, 0, len(messages))
	for _, message := range messages {
		got = append(got, message.Content)
	}
	if !slices.Equal(got, contents) {
		t.Errorf("message contents = %q; want %q", got, contents)
	}
}

func mustRead(t *testing.T, history agent.History, sessionID string) []llm.Message {
	t.Helper()
	messages, err := history.Read(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("Read(%s): %v", sessionID, err)
	}
	return messages
}
