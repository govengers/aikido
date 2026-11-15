# Postgres

`github.com/govengers/aikido/postgresadapter` keeps aikido's state on PostgreSQL 18 with pgx/v5: `History` (an `agent.History` with conversation listing), `Locker` (an `agent.Locker` across replicas) and `Ledger` (a `budget.Ledger`, [Budget](Budget.md)). It is a module of its own, so the root module does not depend on pgx (ADR-043).

```bash
go get github.com/govengers/aikido/postgresadapter
```

## Migration

Nothing runs DDL at startup. Put `postgresadapter.MigrationV1` into one of your goose migrations, in the schema your pool's `search_path` reaches:

```sql
-- +goose Up
-- the content of postgresadapter.MigrationV1
```

or `tx.ExecContext(ctx, postgresadapter.MigrationV1)` from a goose Go migration. It creates `govengers_aikido_conversations`, `govengers_aikido_messages` and `govengers_aikido_spend`. Later versions add `MigrationV2`; never edit an applied migration. The `Locker` needs no table.

## Wiring

From [postgresadapter/example_test.go](../../postgresadapter/example_test.go): `NewHistory(&HistoryOptions{Pool: pool})`, `NewLocker(&LockerOptions{ConnConfig: pool.Config().ConnConfig})`, `NewLedger(&LedgerOptions{Pool: pool})`, then the History and Locker into every `agent.NewSession` and the Ledger into `budget.New`. Share the three across the process.

## History

Stores one row per message (`json`, in a JSON shape of its own) and one row per conversation. `Append` writes a batch in one statement, all or none, and starts an unknown conversation without owner; concurrent appends to one conversation take turns, each batch stays together. Writes pin READ COMMITTED, so a REPEATABLE READ server default does not break them.

For a conversation list:

| Method | Does | Errors |
|---|---|---|
| `Create(ctx, Conversation{ID, Owner, Title})` | starts a conversation before its first run | `ErrConversationExists`, also when an `Append` came first |
| `Get(ctx, id)` | the `Conversation` (owner, title, `MessageCount`, `CreatedAt`, `UpdatedAt`), for an ownership check | `ErrConversationNotFound` |
| `Rename(ctx, id, title)` | sets the title; does not change the list order | `ErrConversationNotFound` |
| `List(ctx, ConversationQuery{Owner, Limit, Cursor})` | a page of one owner's conversations, most recently updated first; `Limit` 1 to 500, default 50; pass the page's `Next` as `Cursor` | `ErrInvalidCursor` |
| `Delete(ctx, id)` | removes the conversation and its messages | `ErrConversationNotFound` |

Every method returns `ErrEmptyConversationID` for an empty ID and `ErrInvalidText` for an ID, owner or title with a NUL character or invalid UTF-8, which PostgreSQL cannot store; a cursor with one is `ErrInvalidCursor`. `UpdatedAt` never moves back, also when an append waited for another one. Database errors wrap the pgx error with `%w`, so `errors.As` finds a `*pgconn.PgError`. Hold the session's `Locker` lock around `Delete`: a running session's next `Append` would start the conversation again without owner. A conversation updated between two `List` pages moves to the front and can be missed until the first page is reloaded.

## Locker

A lock is a session-level advisory lock (`pg_advisory_lock`) on a connection the `Locker` opens for it, outside your pool; unlock releases it and closes the connection (ADR-044).

- Every replica must use the same database. A replica that dies or loses its connection releases its locks; nothing expires.
- Calls for one session take turns in the process, so a session costs one connection per replica however many calls wait for it. `LockerOptions.MaximumConnections` (default 20) caps the lock connections of a `Locker`; further calls wait in the process until one is free or their context ends. Size `max_connections` for replicas times that, plus your pools.
- The lock connection clears `statement_timeout`, `lock_timeout`, `transaction_timeout` and the idle timeouts after connect, so the ones in your `ConnConfig` or role do not end a wait.
- A connection lost in the middle of a run releases the lock while the run goes on; another replica can then start a run of the same session. `LockerOptions.LockLost(sessionID, err)` is told afterwards, when the unlock finds the lock gone: count or log it.
- Use a direct connection or a session-mode pooler. A transaction-mode pooler (PgBouncer) breaks session-level locks.
- A waiter whose context ends leaves the server's lock queue at once.

## Ledger

`Spent` reads all accounts in one query, `Charge` upserts them in one statement (ordered, so overlapping charges never deadlock) under READ COMMITTED. `Prune(ctx, cutoff)` deletes periods that started before `cutoff`; schedule it daily.

## Tests

The module's tests start one embedded PostgreSQL 18 per test binary and give each test its own schema. The binaries are unpacked once into `~/.embedded-postgres-go`, which the CI caches. The concurrency tests also run on a pool whose transactions default to REPEATABLE READ.
