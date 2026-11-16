package postgresadapter_test

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/budget"
	"github.com/govengers/aikido/postgresadapter"
)

// One replica of a service: conversations and spend in PostgreSQL, runs of a session serialized
// across replicas, every model call capped per user. The pool's database has MigrationV1 applied
// by the service's goose migrations.
func Example() {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, "postgres://app@localhost:5432/app")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer pool.Close()

	history, err := postgresadapter.NewHistory(&postgresadapter.HistoryOptions{Pool: pool})
	if err != nil {
		fmt.Println(err)
		return
	}
	locker, err := postgresadapter.NewLocker(&postgresadapter.LockerOptions{ConnConfig: pool.Config().ConnConfig})
	if err != nil {
		fmt.Println(err)
		return
	}
	ledger, err := postgresadapter.NewLedger(&postgresadapter.LedgerOptions{Pool: pool})
	if err != nil {
		fmt.Println(err)
		return
	}
	spending, err := budget.New(&budget.Options{
		Ledger: ledger,
		Limits: func(context.Context) ([]budget.Limit, error) {
			return []budget.Limit{{Key: "user:42", Period: budget.PeriodMonth, CapMicroUSD: 5_000_000}}, nil
		},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	client, err := spending.Wrap(providerClient())
	if err != nil {
		fmt.Println(err)
		return
	}

	if err := history.Create(ctx, postgresadapter.Conversation{ID: "conversation-1", Owner: "user-42", Title: "New chat"}); err != nil {
		fmt.Println(err)
		return
	}
	session, err := agent.NewSession(&agent.SessionOptions{
		ID: "conversation-1", Client: client, Model: "anthropic/claude-haiku-4.5", History: history, Locker: locker,
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	events, err := session.Run(ctx, "Hello!")
	if err != nil {
		fmt.Println(err)
		return
	}
	for event := range events {
		if event.Kind == agent.EventText {
			fmt.Print(event.Text)
		}
	}

	page, err := history.List(ctx, postgresadapter.ConversationQuery{Owner: "user-42"})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, conversation := range page.Conversations {
		fmt.Println(conversation.Title, conversation.MessageCount)
	}
}

// providerClient stands for the service's model client, an openrouter.Client in production.
func providerClient() llm.Client { return nil }
