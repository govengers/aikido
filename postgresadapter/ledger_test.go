package postgresadapter_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/govengers/aikido/llm/budget"
	"github.com/govengers/aikido/llm/budget/ledgertest"
	"github.com/govengers/aikido/postgresadapter"
	"github.com/govengers/aikido/postgresadapter/internal/postgrestest"
)

func newLedger(t *testing.T, pool *pgxpool.Pool) *postgresadapter.Ledger {
	t.Helper()
	ledger, err := postgresadapter.NewLedger(&postgresadapter.LedgerOptions{Pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	return ledger
}

func TestLedgerConformance(t *testing.T) {
	t.Run("read committed", func(t *testing.T) {
		ledgertest.RunConformance(t, func() budget.Ledger { return newLedger(t, postgrestest.New(t)) })
	})
	t.Run("repeatable read", func(t *testing.T) {
		ledgertest.RunConformance(t, func() budget.Ledger { return newLedger(t, repeatableReadPool(t, postgrestest.New(t))) })
	})
}

func TestNewLedgerNeedsAPool(t *testing.T) {
	if _, err := postgresadapter.NewLedger(nil); err == nil {
		t.Error("NewLedger(nil) should fail")
	}
	if _, err := postgresadapter.NewLedger(&postgresadapter.LedgerOptions{}); err == nil {
		t.Error("NewLedger without a pool should fail")
	}
}

func TestLedgerChargesARepeatedAccountOnceAndRefusesNothing(t *testing.T) {
	ledger := newLedger(t, postgrestest.New(t))
	account := budget.Account{Key: "user:1", Period: budget.PeriodMonth, PeriodStart: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)}
	if err := ledger.Charge(t.Context(), []budget.Account{account, account}, 3); err != nil {
		t.Fatal(err)
	}
	if spent, err := ledger.Spent(t.Context(), []budget.Account{account}); err != nil || spent[0] != 3 {
		t.Errorf("Spent = %v, %v; want 3", spent, err)
	}
	if err := ledger.Charge(t.Context(), []budget.Account{account}, 0); err == nil {
		t.Error("a charge of 0 should fail")
	}
}

// Prune with a week's retention on 16.11. drops October and the days before 9.11., and keeps the
// running month although it started before the cutoff.
func TestLedgerPruneDropsOldPeriods(t *testing.T) {
	ledger := newLedger(t, postgrestest.New(t))
	day := func(month time.Month, day int) time.Time { return time.Date(2026, month, day, 0, 0, 0, 0, time.UTC) }
	accounts := []budget.Account{
		{Key: "k", Period: budget.PeriodMonth, PeriodStart: day(time.October, 1)},
		{Key: "k", Period: budget.PeriodDay, PeriodStart: day(time.November, 8)},
		{Key: "k", Period: budget.PeriodMonth, PeriodStart: day(time.November, 1)},
		{Key: "k", Period: budget.PeriodDay, PeriodStart: day(time.November, 9)},
		{Key: "k", Period: budget.PeriodDay, PeriodStart: day(time.November, 16)},
	}
	if err := ledger.Charge(t.Context(), accounts, 7); err != nil {
		t.Fatal(err)
	}
	if pruned, err := ledger.Prune(t.Context(), time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)); err != nil || pruned != 2 {
		t.Errorf("Prune = %d, %v; want 2", pruned, err)
	}
	if spent, err := ledger.Spent(t.Context(), accounts); err != nil || !slices.Equal(spent, []int64{0, 0, 7, 7, 7}) {
		t.Errorf("Spent = %v, %v; want [0 0 7 7 7]", spent, err)
	}
}

// A Budget on the Postgres ledger refuses the call after the one that reached the cap.
func TestBudgetOnTheLedger(t *testing.T) {
	ledger := newLedger(t, postgrestest.New(t))
	spending, err := budget.New(&budget.Options{
		Ledger: ledger,
		Limits: func(context.Context) ([]budget.Limit, error) {
			return []budget.Limit{{Key: "global", Period: budget.PeriodDay, CapMicroUSD: 1000}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := spending.Status(t.Context())
	if err != nil || len(statuses) != 1 || statuses[0].Exceeded() {
		t.Fatalf("Status = %+v, %v", statuses, err)
	}
	if err := ledger.Charge(t.Context(), []budget.Account{{Key: "global", Period: statuses[0].Limit.Period, PeriodStart: statuses[0].PeriodStart}}, 1000); err != nil {
		t.Fatal(err)
	}
	if statuses, err = spending.Status(t.Context()); err != nil || !statuses[0].Exceeded() {
		t.Errorf("Status after the cap = %+v, %v; want exceeded", statuses, err)
	}
}
