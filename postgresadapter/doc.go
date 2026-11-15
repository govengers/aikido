// Package postgresadapter keeps aikido's state on PostgreSQL 18 with pgx/v5: History is an
// agent.History with conversation listing for a UI, Locker an agent.Locker that serializes the runs
// of a session across replicas with an advisory lock, and Ledger a budget.Ledger for the spend
// budget of llm/budget.
//
// Errors that come from the database wrap the pgx error with %w, so errors.As finds a
// *pgconn.PgError (its SQLSTATE) or a connect error through them; that is part of the API, as the
// options already take pgx types. Context errors are returned as they are.
//
// Nothing runs DDL at startup. MigrationV1 holds the tables and indexes for the application's own
// goose migrations; the Locker needs none.
//
// It is a module of its own, github.com/govengers/aikido/postgresadapter, so the root module of
// aikido does not depend on pgx.
package postgresadapter
