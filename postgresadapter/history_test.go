package postgresadapter_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/agent/history/historytest"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/postgresadapter"
	"github.com/govengers/aikido/postgresadapter/internal/postgrestest"
)

func newHistory(t *testing.T) *postgresadapter.History {
	t.Helper()
	history, err := postgresadapter.NewHistory(&postgresadapter.HistoryOptions{Pool: postgrestest.New(t)})
	if err != nil {
		t.Fatal(err)
	}
	return history
}

func TestHistoryConformance(t *testing.T) {
	historytest.RunConformance(t, func() agent.History { return newHistory(t) })
}

func TestNewHistoryNeedsAPool(t *testing.T) {
	if _, err := postgresadapter.NewHistory(nil); err == nil {
		t.Error("NewHistory(nil) should fail")
	}
	if _, err := postgresadapter.NewHistory(&postgresadapter.HistoryOptions{}); err == nil {
		t.Error("NewHistory without a pool should fail")
	}
}

func TestHistoryKeepsEveryFieldOfAMessage(t *testing.T) {
	history := newHistory(t)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "be brief", Cache: &llm.CacheBreakpoint{TTL: llm.CacheTTLFiveMinutes}},
		{
			Role: llm.RoleUser, Content: "what is this?\u0000 (a NUL from a binary file)",
			Images: []llm.ImagePart{{URL: "https://example.com/a.png", ContentType: "image/png", Data: []byte{0, 1, 2}}},
			Audio:  []llm.AudioPart{{Data: []byte("RIFF"), Format: "wav"}},
		},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "read_file", Arguments: `{"path":"a.md"}`}}},
		{Role: llm.RoleTool, ToolCallID: "call-1", Content: "# A\n"},
	}
	if err := history.Append(t.Context(), "fields", messages...); err != nil {
		t.Fatal(err)
	}
	read, err := history.Read(t.Context(), "fields")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read, messages) {
		t.Errorf("read = %#v\nwant %#v", read, messages)
	}
}

func TestHistoryRejectsAnEmptyID(t *testing.T) {
	history := newHistory(t)
	ctx := t.Context()
	for name, err := range map[string]error{
		"Append": history.Append(ctx, "", llm.Message{Content: "x"}),
		"Read":   func() error { _, err := history.Read(ctx, ""); return err }(),
		"Create": history.Create(ctx, postgresadapter.Conversation{}),
		"Get":    func() error { _, err := history.Get(ctx, ""); return err }(),
		"Rename": history.Rename(ctx, "", "t"),
		"Delete": history.Delete(ctx, ""),
	} {
		if !errors.Is(err, postgresadapter.ErrEmptyConversationID) {
			t.Errorf("%s = %v; want ErrEmptyConversationID", name, err)
		}
	}
}

func TestConcurrentAppendsKeepEachBatchTogether(t *testing.T) {
	pool := postgrestest.New(t)
	t.Run("read committed", func(t *testing.T) { testConcurrentAppends(t, mustHistory(t, pool)) })
	t.Run("repeatable read", func(t *testing.T) { testConcurrentAppends(t, mustHistory(t, repeatableReadPool(t, pool))) })
}

func testConcurrentAppends(t *testing.T, history *postgresadapter.History) {
	const writers, batches = 4, 5
	id := t.Name()
	var group sync.WaitGroup
	errs := make(chan error, writers*batches)
	for writer := range writers {
		group.Go(func() {
			for batch := range batches {
				tag := fmt.Sprintf("%d-%d", writer, batch)
				if err := history.Append(context.Background(), id, llm.Message{Content: tag}, llm.Message{Content: tag}); err != nil {
					errs <- err
				}
			}
		})
	}
	group.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("Append: %v", err)
	}
	messages, err := history.Read(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2*writers*batches {
		t.Fatalf("read %d messages; want %d", len(messages), 2*writers*batches)
	}
	for index := 0; index < len(messages); index += 2 {
		if messages[index].Content != messages[index+1].Content {
			t.Errorf("messages %d and %d are from different appends: %q, %q", index, index+1, messages[index].Content, messages[index+1].Content)
		}
	}
	if conversation, err := history.Get(t.Context(), id); err != nil || conversation.MessageCount != 2*writers*batches {
		t.Errorf("Get = %+v, %v; want MessageCount %d", conversation, err, 2*writers*batches)
	}
}

func mustHistory(t *testing.T, pool *pgxpool.Pool) *postgresadapter.History {
	t.Helper()
	history, err := postgresadapter.NewHistory(&postgresadapter.HistoryOptions{Pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	return history
}

func TestConversationLifecycle(t *testing.T) {
	history := newHistory(t)
	ctx := t.Context()
	if err := history.Create(ctx, postgresadapter.Conversation{ID: "c1", Owner: "user-1", Title: "New chat"}); err != nil {
		t.Fatal(err)
	}
	if err := history.Create(ctx, postgresadapter.Conversation{ID: "c1", Owner: "user-2"}); !errors.Is(err, postgresadapter.ErrConversationExists) {
		t.Errorf("second Create = %v; want ErrConversationExists", err)
	}
	created, err := history.Get(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if err := history.Append(ctx, "c1", llm.Message{Role: llm.RoleUser, Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := history.Rename(ctx, "c1", "Greetings"); err != nil {
		t.Fatal(err)
	}
	conversation, err := history.Get(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if conversation.Owner != "user-1" || conversation.Title != "Greetings" || conversation.MessageCount != 1 ||
		!conversation.CreatedAt.Equal(created.CreatedAt) || conversation.UpdatedAt.Before(created.UpdatedAt) {
		t.Errorf("conversation = %+v; created %+v", conversation, created)
	}

	if err := history.Delete(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := history.Get(ctx, "c1"); !errors.Is(err, postgresadapter.ErrConversationNotFound) {
		t.Errorf("Get after Delete = %v; want ErrConversationNotFound", err)
	}
	if messages, err := history.Read(ctx, "c1"); err != nil || len(messages) != 0 {
		t.Errorf("Read after Delete = %v, %v; want the messages gone", messages, err)
	}
	for name, err := range map[string]error{
		"Delete": history.Delete(ctx, "c1"),
		"Rename": history.Rename(ctx, "c1", "x"),
	} {
		if !errors.Is(err, postgresadapter.ErrConversationNotFound) {
			t.Errorf("%s of a deleted conversation = %v; want ErrConversationNotFound", name, err)
		}
	}
}

func TestAppendStartsAConversationWithoutOwner(t *testing.T) {
	history := newHistory(t)
	if err := history.Append(t.Context(), "anonymous", llm.Message{Content: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := history.Create(t.Context(), postgresadapter.Conversation{ID: "anonymous", Owner: "late"}); !errors.Is(err, postgresadapter.ErrConversationExists) {
		t.Errorf("Create after Append = %v; want ErrConversationExists", err)
	}
	page, err := history.List(t.Context(), postgresadapter.ConversationQuery{})
	if err != nil || len(page.Conversations) != 1 || page.Conversations[0].ID != "anonymous" {
		t.Errorf("List(no owner) = %+v, %v", page, err)
	}
}

func TestListPagesThroughAnOwnersConversationsNewestFirst(t *testing.T) {
	history := newHistory(t)
	ctx := t.Context()
	for index := range 5 {
		id := fmt.Sprintf("c%d", index)
		if err := history.Create(ctx, postgresadapter.Conversation{ID: id, Owner: "user-1"}); err != nil {
			t.Fatal(err)
		}
		if err := history.Append(ctx, id, llm.Message{Content: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := history.Create(ctx, postgresadapter.Conversation{ID: "other", Owner: "user-2"}); err != nil {
		t.Fatal(err)
	}

	var listed []string
	query := postgresadapter.ConversationQuery{Owner: "user-1", Limit: 2}
	for pages := 0; ; pages++ {
		if pages > 3 {
			t.Fatal("List does not reach the last page")
		}
		page, err := history.List(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		for _, conversation := range page.Conversations {
			listed = append(listed, conversation.ID)
		}
		if page.Next == "" {
			break
		}
		query.Cursor = page.Next
	}
	if want := []string{"c4", "c3", "c2", "c1", "c0"}; !reflect.DeepEqual(listed, want) {
		t.Errorf("listed %v; want %v", listed, want)
	}
}

func TestListRejectsABadQuery(t *testing.T) {
	history := newHistory(t)
	for name, query := range map[string]postgresadapter.ConversationQuery{
		"negative limit":    {Limit: -1},
		"limit too large":   {Limit: postgresadapter.MaximumListLimit + 1},
		"cursor not base64": {Cursor: "%%%"},
		"cursor without id": {Cursor: "MTIz"},
	} {
		if _, err := history.List(t.Context(), query); err == nil {
			t.Errorf("%s: List succeeded", name)
		}
	}
	if _, err := history.List(t.Context(), postgresadapter.ConversationQuery{Cursor: "eDp5"}); !errors.Is(err, postgresadapter.ErrInvalidCursor) {
		t.Errorf("List with a malformed cursor = %v; want ErrInvalidCursor", err)
	}
}
