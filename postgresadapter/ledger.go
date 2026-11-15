package postgresadapter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/govengers/aikido/llm/budget"
)

// LedgerOptions configures a Ledger.
type LedgerOptions struct {
	Pool *pgxpool.Pool // required; its search_path must reach the tables of MigrationV1
}

// Ledger is a budget.Ledger on PostgreSQL, shared by every replica on the same database. It is safe
// for concurrent use.
type Ledger struct {
	pool *pgxpool.Pool
}

var _ budget.Ledger = (*Ledger)(nil)

// NewLedger returns a Ledger on options.Pool.
func NewLedger(options *LedgerOptions) (*Ledger, error) {
	if options == nil || options.Pool == nil {
		return nil, errors.New("aikido/postgresadapter: LedgerOptions.Pool is required")
	}
	return &Ledger{pool: options.Pool}, nil
}

const selectSpent = `
SELECT spend.key, spend.period, spend.period_start, spend.spent_micro_usd
FROM govengers_aikido_spend AS spend
JOIN unnest($1::text[], $2::text[], $3::timestamptz[]) AS account (key, period, period_start)
    ON spend.key = account.key AND spend.period = account.period AND spend.period_start = account.period_start`

// Spent returns the spend of each account in micro-USD, 0 for an account never charged.
func (l *Ledger) Spent(ctx context.Context, accounts []budget.Account) ([]int64, error) {
	spent := make([]int64, len(accounts))
	if len(accounts) == 0 {
		return spent, nil
	}
	keys, periods, starts := accountColumns(accounts)
	rows, err := l.pool.Query(ctx, selectSpent, keys, periods, starts)
	if err != nil {
		return nil, fmt.Errorf("aikido/postgresadapter: read the spend: %w", err)
	}
	byAccount := map[ledgerKey]int64{}
	var (
		key         string
		period      string
		periodStart time.Time
		amount      int64
	)
	_, err = pgx.ForEachRow(rows, []any{&key, &period, &periodStart, &amount}, func() error {
		byAccount[ledgerKeyOf(budget.Account{Key: key, Period: budget.Period(period), PeriodStart: periodStart})] = amount
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("aikido/postgresadapter: read the spend: %w", err)
	}
	for index, account := range accounts {
		spent[index] = byAccount[ledgerKeyOf(account)]
	}
	return spent, nil
}

// chargeAccounts adds the amount to every account in one statement. The rows are written in one
// order, so two charges of overlapping accounts wait for each other instead of deadlocking.
const chargeAccounts = `
INSERT INTO govengers_aikido_spend AS spend (key, period, period_start, spent_micro_usd)
SELECT account.key, account.period, account.period_start, $4
FROM unnest($1::text[], $2::text[], $3::timestamptz[]) AS account (key, period, period_start)
ORDER BY account.key COLLATE "C", account.period COLLATE "C", account.period_start
ON CONFLICT (key, period, period_start) DO UPDATE
    SET spent_micro_usd = spend.spent_micro_usd + EXCLUDED.spent_micro_usd, updated_at = now()`

// Charge adds microUSD to every account, all or none.
func (l *Ledger) Charge(ctx context.Context, accounts []budget.Account, microUSD int64) error {
	if microUSD <= 0 {
		return fmt.Errorf("aikido/postgresadapter: charge of %d micro-USD; want more than 0", microUSD)
	}
	if len(accounts) == 0 {
		return nil
	}
	keys, periods, starts := accountColumns(distinctAccounts(accounts))
	// READ COMMITTED: under a REPEATABLE READ default, a charge that waited for a concurrent one
	// would fail with a serialization error instead of adding to it.
	err := pgx.BeginTxFunc(ctx, l.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, chargeAccounts, keys, periods, starts, microUSD)
		return err
	})
	if err != nil {
		return fmt.Errorf("aikido/postgresadapter: charge %d micro-USD: %w", microUSD, err)
	}
	return nil
}

const pruneSpend = `DELETE FROM govengers_aikido_spend WHERE period_start < $1`

// Prune deletes the accounts of periods that started before cutoff and returns how many; schedule it
// (a daily job) so the table holds only the periods the limits still look at.
func (l *Ledger) Prune(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := l.pool.Exec(ctx, pruneSpend, cutoff)
	if err != nil {
		return 0, fmt.Errorf("aikido/postgresadapter: prune the spend before %s: %w", cutoff.Format(time.RFC3339), err)
	}
	return tag.RowsAffected(), nil
}

// ledgerKey is an account as a map key at PostgreSQL's microsecond precision.
type ledgerKey struct {
	key         string
	period      budget.Period
	periodStart int64
}

func ledgerKeyOf(account budget.Account) ledgerKey {
	return ledgerKey{key: account.Key, period: account.Period, periodStart: account.PeriodStart.UnixMicro()}
}

// distinctAccounts drops repeated accounts: one statement cannot update a row twice.
func distinctAccounts(accounts []budget.Account) []budget.Account {
	seen := make(map[ledgerKey]bool, len(accounts))
	distinct := make([]budget.Account, 0, len(accounts))
	for _, account := range accounts {
		if key := ledgerKeyOf(account); !seen[key] {
			seen[key] = true
			distinct = append(distinct, account)
		}
	}
	return distinct
}

func accountColumns(accounts []budget.Account) (keys, periods []string, starts []time.Time) {
	keys = make([]string, len(accounts))
	periods = make([]string, len(accounts))
	starts = make([]time.Time, len(accounts))
	for index, account := range accounts {
		keys[index], periods[index], starts[index] = account.Key, string(account.Period), account.PeriodStart
	}
	return keys, periods, starts
}
