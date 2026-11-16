package postgresadapter_test

import (
	"testing"
	"time"

	"github.com/govengers/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/goleak"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/budget"
	"github.com/govengers/aikido/postgresadapter"
)

// postgres is the test PostgreSQL of this package: postgres.Pool(t) gives a test its own database,
// a copy of a template with MigrationV1 applied, dropped when the test ends.
var postgres = postgrestest.New(&postgrestest.Options{
	Migrate:     postgrestest.Statements(postgresadapter.MigrationV1),
	Fingerprint: postgresadapter.MigrationV1,
})

// TestMain stops the PostgreSQL after the tests and fails on leaked goroutines (TST-009).
func TestMain(m *testing.M) { goleak.VerifyTestMain(postgres.Main(m)) }

// repeatableReadPool is a second pool on pool's database whose transactions default to
// REPEATABLE READ, the server or role default an application may have set.
func repeatableReadPool(t *testing.T, pool *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	config := pool.Config().Copy()
	config.ConnConfig.RuntimeParams["default_transaction_isolation"] = "repeatable read"
	repeatable, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repeatable.Close)
	return repeatable
}

// The tables may live in any schema the pool's search_path reaches: a query that names a schema
// fails here, since every test database has them in public.
func TestTablesInASchemaOnTheSearchPath(t *testing.T) {
	pool := postgres.Pool(t)
	if _, err := pool.Exec(t.Context(), `CREATE SCHEMA app;
ALTER TABLE govengers_aikido_conversations SET SCHEMA app;
ALTER TABLE govengers_aikido_messages SET SCHEMA app;
ALTER TABLE govengers_aikido_spend SET SCHEMA app`); err != nil {
		t.Fatal(err)
	}
	config := pool.Config().Copy()
	config.ConnConfig.RuntimeParams["search_path"] = "app"
	app, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)

	history, err := postgresadapter.NewHistory(&postgresadapter.HistoryOptions{Pool: app})
	if err != nil {
		t.Fatal(err)
	}
	if err := history.Append(t.Context(), "c1", llm.Message{Role: llm.RoleUser, Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if messages, err := history.Read(t.Context(), "c1"); err != nil || len(messages) != 1 || messages[0].Content != "hi" {
		t.Errorf("Read = %v, %v; want the one message", messages, err)
	}

	ledger := newLedger(t, app)
	account := budget.Account{Key: "user:1", Period: budget.PeriodMonth, PeriodStart: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)}
	if err := ledger.Charge(t.Context(), []budget.Account{account}, 3); err != nil {
		t.Fatal(err)
	}
	if spent, err := ledger.Spent(t.Context(), []budget.Account{account}); err != nil || spent[0] != 3 {
		t.Errorf("Spent = %v, %v; want 3", spent, err)
	}
}
