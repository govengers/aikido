package llm

// ThinkingEffort is a coarse reasoning level, passed to the provider as is.
type ThinkingEffort string

// The coarse reasoning levels.
const (
	ThinkingEffortLow    ThinkingEffort = "low"
	ThinkingEffortMedium ThinkingEffort = "medium"
	ThinkingEffortHigh   ThinkingEffort = "high"
)

// ThinkingConfiguration configures provider-side thinking. Use ThinkingByEffort or
// ThinkingByBudget; the unexported fields prevent setting both. A nil
// configuration leaves the provider default.
type ThinkingConfiguration struct {
	effort ThinkingEffort
	budget int
}

// ThinkingByEffort asks for reasoning at a coarse level.
func ThinkingByEffort(effort ThinkingEffort) *ThinkingConfiguration {
	return &ThinkingConfiguration{effort: effort}
}

// ThinkingByBudget asks for reasoning with a budget in tokens. Each provider
// decides how to honor it: llm/openrouter maps it onto an effort (below 2048
// low, below 16384 medium, else high) and treats 0 or negative as unset.
func ThinkingByBudget(budgetTokens int) *ThinkingConfiguration {
	return &ThinkingConfiguration{budget: budgetTokens}
}

// Effort returns the configured coarse effort, empty if budget-based or nil.
func (t *ThinkingConfiguration) Effort() ThinkingEffort {
	if t == nil {
		return ""
	}
	return t.effort
}

// Budget returns the configured token budget, 0 if effort-based or nil.
func (t *ThinkingConfiguration) Budget() int {
	if t == nil {
		return 0
	}
	return t.budget
}
