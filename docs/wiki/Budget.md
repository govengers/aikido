# Budget

`llm/budget` caps what model calls may spend, per user, per tenant or overall, in the billed cost the provider reports (`llm.Usage.CostUSD`). A `Budget` wraps an `llm.Client`: before each call it reads the spend of the limits that apply to the call and refuses the call with `budget.ErrBudgetExceeded` once one is spent; after the call it charges the cost to every limit (ADR-045).

## Wiring

From [llm/budget/example_test.go](../../llm/budget/example_test.go):

```go
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
```

then `client, err := spending.Wrap(provider)` and use `client` wherever a model is called (sessions, `llm.Collect`, `llm.Transcribe`, images).

| Option | Default | Meaning |
|---|---|---|
| `Ledger` | required | where the spend is kept: `budget.NewMemoryLedger()` (one process) or `postgresadapter.NewLedger` ([Postgres](Postgres.md)) |
| `Limits` | required | `func(ctx) ([]Limit, error)`: the limits of a call made with `ctx`, usually from the identity in it. None: the call is neither checked nor charged. An error refuses the call with it |
| `ChargeFailed` | none | `func(ctx, err)`: told when charging a finished call failed; the result reached the caller, the cost is not recorded |
| `CancelledCostUSD` | none | `func(ctx, model) float64`: charged for a call whose context ended before the provider reported its cost (a stopped stream, a timeout). The provider bills what it generated until then; without it such calls cost nothing, so a caller that always stops early is never charged |
| `Now` | `time.Now` | the clock that picks the period |

`Limit{Key, Period, CapMicroUSD}`: `Key` names the account ("user:42", "tenant:7", "global"), `Period` is `budget.PeriodDay` or `budget.PeriodMonth` (UTC), `CapMicroUSD` the spend in millionths of a US dollar at which calls are refused (`5_000_000` is 5 USD, `0` refuses every call). Two limits with the same key and period share one account and are charged once; a day and a month limit of one key have separate accounts, also on the first of a month.

`spending.Status(ctx)` returns the limits of a call made with `ctx` with `PeriodStart`, `SpentMicroUSD` and `Exceeded()`, for showing what is left.

## What is charged

- `Stream`: the cost of the stream's `EventUsage`, charged before the channel closes, so the next call of the same caller sees it. `Complete`: the response's `Usage`.
- `GenerateImage`, `GenerateVideo`, `Decide`: their response's `Usage`, also when the call returned an error with a usage.
- `Speak` is checked but never charged: the speech endpoint reports no cost.
- A call that reports no cost is not charged. A call whose context ended before the provider reported its cost (a stream stopped before its usage event, a timeout) is charged `CancelledCostUSD`, or nothing without it. A usage event the provider had already queued when the stream was stopped is charged as reported.
- The charge runs after the call with the caller's context values but not its cancellation (at most 10 s): a call the caller gave up on was billed anyway.

The cap is checked before a call and charged after it, so calls admitted at the same time can end above the cap by what they cost.

## Capabilities and composition

The wrapped client implements exactly the optional capabilities `llm.Capability` finds on the client you pass (`llm.ImageGenerator`, `llm.SpeechGenerator`, `llm.VideoGenerator`, `llm.Decider`), each checked and charged, and `Unwrap()` returns that client. A type assertion and its fallback work as on the original.

With [Observability](Observability.md), wrap the observed client: `spending.Wrap(observed)`. Refused calls then never reach the provider or its metrics, and the capabilities behind the observed client stay reachable (unobserved, as without the budget).

## Ledger

`budget.Ledger` is `Spent(ctx, accounts) ([]int64, error)` and `Charge(ctx, accounts, microUSD) error`; an `Account` is a `Key`, a `Period` and the `PeriodStart` of that period. Implementations add concurrent charges without losing any. `MemoryLedger.Prune(cutoff)` and `postgresadapter.Ledger.Prune(ctx, cutoff)` drop the periods that ended before the one holding `cutoff`; the running day and month always stay, so `Prune(now - retention)` never resets a limit. Your own ledger runs `ledgertest.RunConformance` ([Testing](Testing.md#conformance-suites)).
