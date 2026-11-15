// Package ledgertest is the conformance suite every budget.Ledger backend runs in its tests.
package ledgertest

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/govengers/aikido/llm/budget"
)

// RunConformance executes the standard suite against a Ledger factory.
//
// factory must return an empty Ledger on every call. Sub-tests are serial. A backend calls this
// from a TestX function in its package.
func RunConformance(t *testing.T, factory func() budget.Ledger) {
	t.Helper()
	conformanceTests := []struct {
		name string
		run  func(t *testing.T, ledger budget.Ledger)
	}{
		{"UnchargedAccountsSpentNothing", testUnchargedAccountsSpentNothing},
		{"ChargeAddsToEveryAccount", testChargeAddsToEveryAccount},
		{"KeyAndPeriodSeparateAccounts", testKeyAndPeriodSeparateAccounts},
		{"PeriodStartIsAnInstant", testPeriodStartIsAnInstant},
		{"NoAccountsIsANoop", testNoAccountsIsANoop},
		{"ConcurrentChargesAddUp", testConcurrentChargesAddUp},
	}
	for _, conformanceTest := range conformanceTests {
		t.Run(conformanceTest.name, func(t *testing.T) {
			conformanceTest.run(t, factory())
		})
	}
}

var (
	january  = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	february = time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
)

func testUnchargedAccountsSpentNothing(t *testing.T, ledger budget.Ledger) {
	expectSpent(t, ledger, []budget.Account{{Key: "user:1", PeriodStart: january}, {Key: "global", PeriodStart: january}}, 0, 0)
}

func testChargeAddsToEveryAccount(t *testing.T, ledger budget.Ledger) {
	user, global := budget.Account{Key: "user:1", PeriodStart: january}, budget.Account{Key: "global", PeriodStart: january}
	mustCharge(t, ledger, []budget.Account{user, global}, 1500)
	mustCharge(t, ledger, []budget.Account{global}, 250)
	expectSpent(t, ledger, []budget.Account{global, user}, 1750, 1500)
}

func testKeyAndPeriodSeparateAccounts(t *testing.T, ledger budget.Ledger) {
	mustCharge(t, ledger, []budget.Account{{Key: "user:1", PeriodStart: january}}, 10)
	mustCharge(t, ledger, []budget.Account{{Key: "user:2", PeriodStart: january}}, 20)
	mustCharge(t, ledger, []budget.Account{{Key: "user:1", PeriodStart: february}}, 30)
	expectSpent(t, ledger, []budget.Account{
		{Key: "user:1", PeriodStart: january},
		{Key: "user:2", PeriodStart: january},
		{Key: "user:1", PeriodStart: february},
		{Key: "user:2", PeriodStart: february},
	}, 10, 20, 30, 0)
}

func testPeriodStartIsAnInstant(t *testing.T, ledger budget.Ledger) {
	elsewhere := january.In(time.FixedZone("UTC+2", 2*60*60))
	mustCharge(t, ledger, []budget.Account{{Key: "user:1", PeriodStart: elsewhere}}, 5)
	expectSpent(t, ledger, []budget.Account{{Key: "user:1", PeriodStart: january}}, 5)
}

func testNoAccountsIsANoop(t *testing.T, ledger budget.Ledger) {
	mustCharge(t, ledger, nil, 5)
	spent, err := ledger.Spent(context.Background(), nil)
	if err != nil || len(spent) != 0 {
		t.Errorf("Spent(no accounts) = %v, %v; want none", spent, err)
	}
}

func testConcurrentChargesAddUp(t *testing.T, ledger budget.Ledger) {
	const writers, charges = 4, 10
	accounts := []budget.Account{{Key: "user:1", PeriodStart: january}, {Key: "global", PeriodStart: january}}
	reversed := slices.Clone(accounts)
	slices.Reverse(reversed)
	var group sync.WaitGroup
	errs := make(chan error, writers*charges)
	for writer := range writers {
		order := accounts
		if writer%2 == 1 {
			order = reversed // opposite orders must not deadlock
		}
		group.Go(func() {
			for range charges {
				if err := ledger.Charge(context.Background(), order, 1); err != nil {
					errs <- err
				}
			}
		})
	}
	group.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("Charge: %v", err)
	}
	expectSpent(t, ledger, accounts, writers*charges, writers*charges)
}

func mustCharge(t *testing.T, ledger budget.Ledger, accounts []budget.Account, microUSD int64) {
	t.Helper()
	if err := ledger.Charge(context.Background(), accounts, microUSD); err != nil {
		t.Fatalf("Charge(%v, %d): %v", accounts, microUSD, err)
	}
}

func expectSpent(t *testing.T, ledger budget.Ledger, accounts []budget.Account, want ...int64) {
	t.Helper()
	spent, err := ledger.Spent(context.Background(), accounts)
	if err != nil {
		t.Fatalf("Spent(%v): %v", accounts, err)
	}
	if !slices.Equal(spent, want) {
		t.Errorf("Spent(%v) = %v; want %v", accounts, spent, want)
	}
}
