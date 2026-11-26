package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/govengers/aikido/llm"
)

// ErrRepeatedToolCall is the error a tool call gets that the run refused to
// run because it continued a cycle of calls that kept returning the same
// results (SessionOptions.RepeatedToolCallLimit).
var ErrRepeatedToolCall = errors.New("aikido/agent: repeated tool call")

// maximumCyclePeriod is the longest cycle repeatGuard detects: a model stuck
// between a few calls (A, B, A, B) loops as surely as one repeating A.
const maximumCyclePeriod = 4

// repeatGuard detects a model that cannot make progress (common with small
// models on greedy decoding): it repeats one call, or a short cycle of calls,
// verbatim, and every result is the same as last time. Running the cycle
// again only burns model calls and context. A call whose result changes
// (polling a process, a test after an edit) breaks the cycle.
type repeatGuard struct {
	limit    int
	ran      []callOutcome // the calls run, newest last, at most limit*maximumCyclePeriod
	refusals int           // refused calls in this run
}

// callOutcome is one call that ran and what the model read back.
type callOutcome struct {
	call   string // name and canonical arguments
	result string
}

func newRepeatGuard(limit int) *repeatGuard {
	return &repeatGuard{limit: limit}
}

// refusal is the error for a call of a cycle that ran limit times with the
// same results, nil for a call to run. Refused calls are not recorded, so the
// cycle stays detected until the model calls something else.
func (g *repeatGuard) refusal(call llm.ToolCall) error {
	if g.limit < 0 {
		return nil
	}
	period := g.cycle()
	if period == 0 || !slices.ContainsFunc(g.ran[len(g.ran)-period:], func(ran callOutcome) bool { return ran.call == callKey(call) }) {
		return nil
	}
	g.refusals++
	if period == 1 {
		return fmt.Errorf("%w: %s ran %d times in a row with these arguments and returned the same result each time; "+
			"running it again changes nothing. Use what it returned, call something else, or answer", ErrRepeatedToolCall, call.Name, g.limit)
	}
	return fmt.Errorf("%w: the last %d calls repeated the same %d calls %d times with the same results, and %s is one of them; "+
		"it changes nothing. Use what they returned, do something else, or answer", ErrRepeatedToolCall, period*g.limit, period, g.limit, call.Name)
}

// cycle is the shortest period p for which the last limit*p calls that ran
// are limit copies of the same p calls with the same results, 0 when there
// is none.
func (g *repeatGuard) cycle() int {
	for period := 1; period <= maximumCyclePeriod; period++ {
		span := period * g.limit
		if span > len(g.ran) {
			return 0
		}
		recent := g.ran[len(g.ran)-span:]
		repeats := true
		for index := period; index < span && repeats; index++ {
			repeats = recent[index] == recent[index-period]
		}
		if repeats {
			return period
		}
	}
	return 0
}

// observe records a call that ran and what it returned to the model.
func (g *repeatGuard) observe(call llm.ToolCall, modelContent string) {
	if g.limit < 0 {
		return
	}
	g.ran = append(g.ran, callOutcome{call: callKey(call), result: modelContent})
	if keep := g.limit * maximumCyclePeriod; len(g.ran) > keep {
		g.ran = g.ran[len(g.ran)-keep:]
	}
}

// exhausted says whether the run refused limit calls: the model keeps trying
// to loop despite being told.
func (g *repeatGuard) exhausted() bool {
	return g.limit >= 0 && g.refusals >= g.limit
}

// callKey identifies a call by its name and its arguments with object keys
// sorted and whitespace dropped, so formatting does not hide a repeat.
func callKey(call llm.ToolCall) string {
	var arguments any
	if err := json.Unmarshal([]byte(call.Arguments), &arguments); err != nil {
		return call.Name + "\x00" + string(bytes.TrimSpace([]byte(call.Arguments)))
	}
	canonical, err := json.Marshal(arguments)
	if err != nil {
		return call.Name + "\x00" + call.Arguments
	}
	return call.Name + "\x00" + string(canonical)
}
