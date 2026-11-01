package llm

import "testing"

func TestThinkingByEffort_RoundTrip(t *testing.T) {
	configuration := ThinkingByEffort(ThinkingEffortHigh)
	if configuration.Effort() != ThinkingEffortHigh {
		t.Errorf("Effort() = %q, want %q", configuration.Effort(), ThinkingEffortHigh)
	}
	if configuration.Budget() != 0 {
		t.Errorf("Budget() = %d, want 0 when only effort set", configuration.Budget())
	}
}

func TestThinkingByBudget_RoundTrip(t *testing.T) {
	configuration := ThinkingByBudget(8192)
	if configuration.Budget() != 8192 {
		t.Errorf("Budget() = %d, want 8192", configuration.Budget())
	}
	if configuration.Effort() != "" {
		t.Errorf("Effort() = %q, want empty when only budget set", configuration.Effort())
	}
}

func TestThinkingConfiguration_NilSafe(t *testing.T) {
	var configuration *ThinkingConfiguration
	if configuration.Effort() != "" {
		t.Error("nil ThinkingConfiguration.Effort should return empty")
	}
	if configuration.Budget() != 0 {
		t.Error("nil ThinkingConfiguration.Budget should return 0")
	}
}
