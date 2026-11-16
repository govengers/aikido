package budget

import (
	"context"
	"math"
	"slices"
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

// Prune with a week's retention on 16.11. drops October and the days before 9.11., and keeps the
// running month although it started before the cutoff.
func TestMemoryLedgerPruneDropsOldPeriods(t *testing.T) {
	ledger := NewMemoryLedger()
	ctx := context.Background()
	day := func(month time.Month, day int) time.Time { return time.Date(2026, month, day, 0, 0, 0, 0, time.UTC) }
	accounts := []Account{
		{Key: "k", Period: PeriodMonth, PeriodStart: day(time.October, 1)},
		{Key: "k", Period: PeriodDay, PeriodStart: day(time.November, 8)},
		{Key: "k", Period: PeriodMonth, PeriodStart: day(time.November, 1)},
		{Key: "k", Period: PeriodDay, PeriodStart: day(time.November, 9)},
		{Key: "k", Period: PeriodDay, PeriodStart: day(time.November, 16)},
	}
	if err := ledger.Charge(ctx, accounts, 7); err != nil {
		t.Fatal(err)
	}
	ledger.Prune(time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC))
	spent, err := ledger.Spent(ctx, accounts)
	if err != nil || !slices.Equal(spent, []int64{0, 0, 7, 7, 7}) {
		t.Errorf("Spent after Prune = %v, %v; want [0 0 7 7 7]", spent, err)
	}
	current := accounts[2]
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
