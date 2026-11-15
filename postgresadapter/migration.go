package postgresadapter

// MigrationV1 creates the tables of History and Ledger. Run it from one of the application's goose
// migrations, in the schema the pools of History and Ledger use:
//
//	-- +goose Up
//	-- the content of postgresadapter.MigrationV1
//
// or from a goose Go migration with tx.ExecContext(ctx, postgresadapter.MigrationV1). Later
// versions of the module add MigrationV2 and so on; never edit an applied migration. It runs inside
// the migration's transaction (SET LOCAL).
//
// A message is stored as json, not jsonb: jsonb rejects the escape \u0000 a model or a tool result
// can produce, and the store never queries inside a message.
const MigrationV1 = `
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

CREATE TABLE govengers_aikido_conversations (
    id            text PRIMARY KEY CHECK (id <> ''),
    owner         text NOT NULL DEFAULT '',
    title         text NOT NULL DEFAULT '',
    message_count bigint NOT NULL DEFAULT 0 CHECK (message_count >= 0),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX govengers_aikido_conversations_owner
    ON govengers_aikido_conversations (owner, updated_at DESC, id DESC);

CREATE TABLE govengers_aikido_messages (
    conversation_id text NOT NULL REFERENCES govengers_aikido_conversations (id) ON DELETE CASCADE,
    ordinal         bigint NOT NULL CHECK (ordinal >= 0),
    message         json NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (conversation_id, ordinal)
);

CREATE TABLE govengers_aikido_spend (
    key             text NOT NULL CHECK (key <> ''),
    period          text NOT NULL,
    period_start    timestamptz NOT NULL,
    spent_micro_usd bigint NOT NULL CHECK (spent_micro_usd >= 0),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (key, period, period_start)
);

CREATE INDEX govengers_aikido_spend_period ON govengers_aikido_spend (period_start);
`
