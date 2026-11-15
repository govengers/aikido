package llm_test

import (
	"testing"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
)

// textOnlyClient implements Client and no capability.
type textOnlyClient struct{ llm.Client }

func TestCapabilityFindsTheCapabilityBehindDecorators(t *testing.T) {
	stub := llmtest.NewStubClient()
	observed, err := llm.NewObservedClient(&llm.ObservedClientOptions{Client: stub, Name: "stub", Observers: []llm.Observer{&recordingObserver{order: &[]string{}}}})
	if err != nil {
		t.Fatal(err)
	}
	if observed.Unwrap() != stub {
		t.Error("Unwrap does not return the wrapped client")
	}
	generator, ok := llm.Capability[llm.ImageGenerator](observed)
	if !ok || generator != stub {
		t.Errorf("Capability[ImageGenerator](observed) = %v, %v; want the stub", generator, ok)
	}
	if direct, ok := llm.Capability[llm.Decider](stub); !ok || direct != stub {
		t.Error("Capability on the capable client itself does not return it")
	}
}

func TestCapabilityReportsAMissingCapability(t *testing.T) {
	plain := textOnlyClient{Client: llmtest.NewStubClient()}
	if _, ok := llm.Capability[llm.SpeechGenerator](plain); ok {
		t.Error("a client without Unwrap and without the capability reported it")
	}
	if _, ok := llm.Capability[llm.SpeechGenerator](nil); ok {
		t.Error("a nil client reported the capability")
	}
}
