package budget_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/budget"
	"github.com/govengers/aikido/llm/llmtest"
)

type userKey struct{}

// A per-user cap of 0.01 USD a month and a global cap of 50 USD a day. The user comes from the
// request context; the provider here is a stub that bills 0.006 USD per call.
func ExampleBudget_Wrap() {
	limits := func(ctx context.Context) ([]budget.Limit, error) {
		user, ok := ctx.Value(userKey{}).(string)
		if !ok {
			return nil, errors.New("no user in the context")
		}
		return []budget.Limit{
			{Key: "user:" + user, Period: budget.PeriodMonth, CapMicroUSD: 10_000},
			{Key: "global", Period: budget.PeriodDay, CapMicroUSD: 50_000_000},
		}, nil
	}
	spending, err := budget.New(&budget.Options{Ledger: budget.NewMemoryLedger(), Limits: limits})
	if err != nil {
		fmt.Println(err)
		return
	}
	turn := llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "Hello."},
		{Kind: llm.EventUsage, Usage: &llm.Usage{CostUSD: 0.006}},
		{Kind: llm.EventEnd},
	}}
	client, err := spending.Wrap(llmtest.NewStubClient(turn, turn, turn))
	if err != nil {
		fmt.Println(err)
		return
	}

	ctx := context.WithValue(context.Background(), userKey{}, "42")
	for call := 1; call <= 3; call++ {
		_, err := llm.Collect(ctx, client, llm.Request{Model: "anthropic/claude-haiku-4.5"})
		fmt.Printf("call %d refused: %v\n", call, errors.Is(err, budget.ErrBudgetExceeded))
	}
	// Output:
	// call 1 refused: false
	// call 2 refused: false
	// call 3 refused: true
}
