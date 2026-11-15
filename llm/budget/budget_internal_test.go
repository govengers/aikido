package budget

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestMicroUSD(t *testing.T) {
	for _, test := range []struct {
		costUSD float64
		want    int64
	}{
		{0, 0},
		{-1, 0},
		{math.NaN(), 0},
		{0.0000004, 0},
		{0.0000005, 1},
		{0.1, 100_000},
		{0.000003, 3},
		{12.345678, 12_345_678},
		{math.Inf(1), math.MaxInt64},
		{1e13, math.MaxInt64},
	} {
		if got := microUSD(test.costUSD); got != test.want {
			t.Errorf("microUSD(%v) = %d; want %d", test.costUSD, got, test.want)
		}
	}
}

func TestMemoryLedgerPruneDropsOldPeriods(t *testing.T) {
	ledger := NewMemoryLedger()
	ctx := context.Background()
	old := Account{Key: "k", Period: PeriodMonth, PeriodStart: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)}
	current := Account{Key: "k", Period: PeriodMonth, PeriodStart: time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)}
	if err := ledger.Charge(ctx, []Account{old, current}, 7); err != nil {
		t.Fatal(err)
	}
	ledger.Prune(current.PeriodStart)
	spent, err := ledger.Spent(ctx, []Account{old, current})
	if err != nil || spent[0] != 0 || spent[1] != 7 {
		t.Errorf("Spent after Prune = %v, %v; want [0 7]", spent, err)
	}
	if err := ledger.Charge(ctx, []Account{current}, 0); err == nil {
		t.Error("a charge of 0 should fail")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ledger.Spent(cancelled, []Account{current}); err == nil {
		t.Error("Spent ignored a cancelled context")
	}
	if err := ledger.Charge(cancelled, []Account{current}, 1); err == nil {
		t.Error("Charge ignored a cancelled context")
	}
}
