package postgresadapter_test

import (
	"testing"

	"github.com/govengers/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/goleak"

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
