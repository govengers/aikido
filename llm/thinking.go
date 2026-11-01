package llm

type ThinkingEffort string

const (
	ThinkingEffortLow    ThinkingEffort = "low"
	ThinkingEffortMedium ThinkingEffort = "medium"
	ThinkingEffortHigh   ThinkingEffort = "high"
)

// ThinkingConfiguration configures provider-side thinking. Use ThinkingByEffort or
// ThinkingByBudget — the unexported fields prevent setting both.
type ThinkingConfiguration struct {
	effort ThinkingEffort
	budget int
}

func ThinkingByEffort(e ThinkingEffort) *ThinkingConfiguration {
	return &ThinkingConfiguration{effort: e}
}

func ThinkingByBudget(n int) *ThinkingConfiguration {
	return &ThinkingConfiguration{budget: n}
}

// Effort returns the configured coarse effort, empty string if budget-based.
func (t *ThinkingConfiguration) Effort() ThinkingEffort {
	if t == nil {
		return ""
	}
	return t.effort
}

// Budget returns the configured token budget, 0 if effort-based.
func (t *ThinkingConfiguration) Budget() int {
	if t == nil {
		return 0
	}
	return t.budget
}
