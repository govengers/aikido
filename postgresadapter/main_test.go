package postgresadapter_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/govengers/aikido/postgresadapter/internal/postgrestest"
)

func TestMain(m *testing.M) { postgrestest.Main(m) }

// repeatableReadPool is a second pool on pool's schema whose transactions default to REPEATABLE
// READ, the server or role default an application may have set.
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
