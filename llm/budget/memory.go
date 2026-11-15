package budget

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MemoryLedger is a Ledger in the process: the spend is lost when the process ends and is not
// shared between replicas. It is safe for concurrent use; the zero value is not usable, use
// NewMemoryLedger.
type MemoryLedger struct {
	mutex sync.Mutex
	spent map[accountKey]int64
}

// accountKey is an Account as a map key: time.Time values of the same instant can differ in
// location and monotonic reading.
type accountKey struct {
	key         string
	period      Period
	periodStart int64
}

func keyOf(account Account) accountKey {
	return accountKey{key: account.Key, period: account.Period, periodStart: account.PeriodStart.UnixNano()}
}

var _ Ledger = (*MemoryLedger)(nil)

// NewMemoryLedger returns an empty MemoryLedger.
func NewMemoryLedger() *MemoryLedger {
	return &MemoryLedger{spent: make(map[accountKey]int64)}
}

// Spent returns the spend of each account, 0 for an account never charged.
func (l *MemoryLedger) Spent(ctx context.Context, accounts []Account) ([]int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.mutex.Lock()
	defer l.mutex.Unlock()
	spent := make([]int64, len(accounts))
	for index, account := range accounts {
		spent[index] = l.spent[keyOf(account)]
	}
	return spent, nil
}

// Charge adds microUSD to every account.
func (l *MemoryLedger) Charge(ctx context.Context, accounts []Account, microUSD int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if microUSD <= 0 {
		return fmt.Errorf("aikido/llm/budget: charge of %d micro-USD; want more than 0", microUSD)
	}
	l.mutex.Lock()
	defer l.mutex.Unlock()
	for _, account := range accounts {
		l.spent[keyOf(account)] += microUSD
	}
	return nil
}

// Prune drops the accounts of periods that started before cutoff, so the ledger does not grow
// without bound in a long-running process.
func (l *MemoryLedger) Prune(cutoff time.Time) {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	for key := range l.spent {
		if key.periodStart < cutoff.UnixNano() {
			delete(l.spent, key)
		}
	}
}
