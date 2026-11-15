package postgresadapter_test

import (
	"context"
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
	account := budget.Account{Key: "user:1", PeriodStart: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)}
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

func TestLedgerPruneDropsOldPeriods(t *testing.T) {
	ledger := newLedger(t, postgrestest.New(t))
	january := budget.Account{Key: "k", PeriodStart: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)}
	february := budget.Account{Key: "k", PeriodStart: time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)}
	if err := ledger.Charge(t.Context(), []budget.Account{january, february}, 7); err != nil {
		t.Fatal(err)
	}
	if pruned, err := ledger.Prune(t.Context(), february.PeriodStart); err != nil || pruned != 1 {
		t.Errorf("Prune = %d, %v; want 1", pruned, err)
	}
	if spent, err := ledger.Spent(t.Context(), []budget.Account{january, february}); err != nil || spent[0] != 0 || spent[1] != 7 {
		t.Errorf("Spent = %v, %v; want [0 7]", spent, err)
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
	if err := ledger.Charge(t.Context(), []budget.Account{{Key: "global", PeriodStart: statuses[0].PeriodStart}}, 1000); err != nil {
		t.Fatal(err)
	}
	if statuses, err = spending.Status(t.Context()); err != nil || !statuses[0].Exceeded() {
		t.Errorf("Status after the cap = %+v, %v; want exceeded", statuses, err)
	}
}
