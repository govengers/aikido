// Package budget caps what model calls may spend. A Budget wraps an llm.Client: before each call
// it looks up the limits that apply to the call (per user, per tenant, global, whatever the
// application's Limits function returns), refuses the call with ErrBudgetExceeded once one of them
// is spent, and after the call charges the provider's billed cost (llm.Usage.CostUSD) to all of
// them in a Ledger. NewMemoryLedger keeps the spend in the process; the Postgres ledger is in the
// module github.com/govengers/aikido/postgresadapter.
//
// The cap is checked before a call and charged after it, since the cost is known only then: calls
// running at the same time all pass the check, so the spend can end above the cap by what those
// calls cost. Calls the provider reports no cost for are not charged.
package budget

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"
)

// ErrBudgetExceeded is returned, wrapped, instead of making a call once a limit that applies to it
// is spent. The message names the limit's key.
var ErrBudgetExceeded = errors.New("aikido/llm/budget: spend budget exceeded")

// Period is the window a limit's spend accumulates in, in UTC.
type Period string

// The periods of a Limit.
const (
	// PeriodDay starts at 00:00 UTC.
	PeriodDay Period = "day"
	// PeriodMonth starts at 00:00 UTC on the first day of the month.
	PeriodMonth Period = "month"
)

// Start returns the start of the period that contains instant, in UTC.
func (p Period) Start(instant time.Time) (time.Time, error) {
	year, month, day := instant.UTC().Date()
	switch p {
	case PeriodDay:
		return time.Date(year, month, day, 0, 0, 0, 0, time.UTC), nil
	case PeriodMonth:
		return time.Date(year, month, 1, 0, 0, 0, 0, time.UTC), nil
	default:
		return time.Time{}, fmt.Errorf("aikido/llm/budget: unknown period %q", p)
	}
}

// Limit caps the spend of one key in one period.
type Limit struct {
	// Key names what is limited ("user:42", "tenant:7", "global"); it is the Ledger's key. Two
	// limits with the same Key and Period share one account; a day and a month limit of one key
	// have separate accounts.
	Key string
	// Period is the window the spend accumulates in.
	Period Period
	// CapMicroUSD is the spend in millionths of a US dollar at which calls are refused: 5_000_000
	// is 5 USD. 0 refuses every call.
	CapMicroUSD int64
}

// Account is where the Ledger keeps one key's spend in one period. Key, Period and PeriodStart
// together name it: the day and the month that start on the same instant are two accounts.
type Account struct {
	Key    string
	Period Period
	// PeriodStart is the start of the period in UTC, as Period.Start returns it.
	PeriodStart time.Time
}

// Ledger stores the spend per account. Implementations must be safe for concurrent use and add
// concurrent charges without losing any. The accounts of one call are distinct.
type Ledger interface {
	// Spent returns the spend of each account in micro-USD, in the order of accounts; an account
	// that was never charged has spent 0.
	Spent(ctx context.Context, accounts []Account) ([]int64, error)
	// Charge adds microUSD (greater than 0) to every account, all or none.
	Charge(ctx context.Context, accounts []Account, microUSD int64) error
}

// Options configures a Budget.
type Options struct {
	// Ledger stores the spend; required.
	Ledger Ledger
	// Limits returns the limits that apply to a call made with ctx, usually derived from the
	// identity in ctx; required. No limits means the call is neither checked nor charged. An error
	// refuses the call with that error.
	Limits func(ctx context.Context) ([]Limit, error)
	// ChargeFailed is told when charging a finished call failed; the call's result has reached
	// the caller and the cost is not recorded. Optional; nil drops the error.
	ChargeFailed func(ctx context.Context, err error)
	// Now is the clock that picks the period; nil is time.Now.
	Now func() time.Time
}

// chargeTimeout bounds a charge: it runs after the call, also when the caller gave up, so it
// cannot use the caller's context alone.
const chargeTimeout = 10 * time.Second

// Budget checks and charges the spend of the clients it wraps. It is safe for concurrent use.
type Budget struct {
	ledger       Ledger
	limits       func(ctx context.Context) ([]Limit, error)
	chargeFailed func(ctx context.Context, err error)
	now          func() time.Time
}

// New returns a Budget.
func New(options *Options) (*Budget, error) {
	if options == nil || options.Ledger == nil || options.Limits == nil {
		return nil, errors.New("aikido/llm/budget: Options needs a Ledger and a Limits function")
	}
	budget := &Budget{ledger: options.Ledger, limits: options.Limits, chargeFailed: options.ChargeFailed, now: options.Now}
	if budget.now == nil {
		budget.now = time.Now
	}
	return budget, nil
}

// Status is a limit with the spend of its current period.
type Status struct {
	Limit         Limit
	PeriodStart   time.Time
	SpentMicroUSD int64
}

func (s Status) account() Account {
	return Account{Key: s.Limit.Key, Period: s.Limit.Period, PeriodStart: s.PeriodStart}
}

// Exceeded reports whether the limit refuses calls.
func (s Status) Exceeded() bool { return s.SpentMicroUSD >= s.Limit.CapMicroUSD }

// Status returns the limits that apply to a call made with ctx and their spend in the current
// period, for showing what is left.
func (b *Budget) Status(ctx context.Context) ([]Status, error) {
	statuses, _, err := b.status(ctx)
	return statuses, err
}

// admit returns the accounts to charge a call made with ctx, or ErrBudgetExceeded.
func (b *Budget) admit(ctx context.Context) ([]Account, error) {
	statuses, accounts, err := b.status(ctx)
	if err != nil {
		return nil, err
	}
	for _, status := range statuses {
		if status.Exceeded() {
			return nil, fmt.Errorf("%w: %s spent %d of %d micro-USD this %s", ErrBudgetExceeded,
				status.Limit.Key, status.SpentMicroUSD, status.Limit.CapMicroUSD, status.Limit.Period)
		}
	}
	return accounts, nil
}

// status reads the limits and the spend of their accounts; accounts holds each account once.
func (b *Budget) status(ctx context.Context) ([]Status, []Account, error) {
	limits, err := b.limits(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("aikido/llm/budget: limits: %w", err)
	}
	if len(limits) == 0 {
		return nil, nil, nil
	}
	now := b.now()
	statuses := make([]Status, len(limits))
	accounts := make([]Account, 0, len(limits))
	for index, limit := range limits {
		if limit.Key == "" || limit.CapMicroUSD < 0 {
			return nil, nil, fmt.Errorf("aikido/llm/budget: invalid limit %+v: it needs a Key and a CapMicroUSD of 0 or more", limit)
		}
		start, err := limit.Period.Start(now)
		if err != nil {
			return nil, nil, err
		}
		statuses[index] = Status{Limit: limit, PeriodStart: start}
		if account := statuses[index].account(); !slices.Contains(accounts, account) {
			accounts = append(accounts, account)
		}
	}
	spent, err := b.ledger.Spent(ctx, accounts)
	if err != nil {
		return nil, nil, fmt.Errorf("aikido/llm/budget: read the spend: %w", err)
	}
	if len(spent) != len(accounts) {
		return nil, nil, fmt.Errorf("aikido/llm/budget: the ledger returned %d spends for %d accounts", len(spent), len(accounts))
	}
	for index := range statuses {
		statuses[index].SpentMicroUSD = spent[slices.Index(accounts, statuses[index].account())]
	}
	return statuses, accounts, nil
}

// charge books costUSD on accounts. It runs after the call, so it outlives the caller's
// cancellation, bounded by chargeTimeout.
func (b *Budget) charge(ctx context.Context, accounts []Account, costUSD float64) {
	amount := microUSD(costUSD)
	if len(accounts) == 0 || amount <= 0 {
		return
	}
	chargeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), chargeTimeout)
	defer cancel()
	if err := b.ledger.Charge(chargeContext, accounts, amount); err != nil && b.chargeFailed != nil {
		b.chargeFailed(ctx, fmt.Errorf("aikido/llm/budget: charge %d micro-USD: %w", amount, err))
	}
}

// microUSD converts a provider's cost to whole micro-USD, rounded to the nearest; costs that are
// not a positive number count as 0, costs beyond the int64 range as its maximum.
func microUSD(costUSD float64) int64 {
	switch {
	case !(costUSD > 0):
		return 0
	case costUSD >= math.MaxInt64/1e6:
		return math.MaxInt64
	default:
		return int64(math.Round(costUSD * 1e6))
	}
}
