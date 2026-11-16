package postgresadapter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/llm"
)

// The errors of History, matched with errors.Is.
var (
	// ErrEmptyConversationID is returned for an empty conversation (session) ID.
	ErrEmptyConversationID = errors.New("aikido/postgresadapter: empty conversation ID")
	// ErrConversationExists is returned by Create for an ID that is taken.
	ErrConversationExists = errors.New("aikido/postgresadapter: conversation exists")
	// ErrConversationNotFound is returned for an ID without a conversation.
	ErrConversationNotFound = errors.New("aikido/postgresadapter: conversation not found")
	// ErrInvalidCursor is returned by List for a Cursor that is not a Next of an earlier page.
	ErrInvalidCursor = errors.New("aikido/postgresadapter: invalid cursor")
	// ErrInvalidText is returned for an ID, owner or title PostgreSQL cannot store as text: one
	// that is not valid UTF-8 or holds a NUL character.
	ErrInvalidText = errors.New("aikido/postgresadapter: text is not valid UTF-8 or holds NUL")
)

// Conversation is the listing entry of one agent session: the History's session ID is its ID.
type Conversation struct {
	ID string
	// Owner is who the conversation belongs to, as the application names it (a user ID); empty for
	// a conversation that Append started.
	Owner string
	Title string
	// MessageCount is the number of messages stored.
	MessageCount int64
	CreatedAt    time.Time
	// UpdatedAt is the time of the last Append, or of Create.
	UpdatedAt time.Time
}

// HistoryOptions configures a History.
type HistoryOptions struct {
	Pool *pgxpool.Pool // required; its search_path must reach the tables of MigrationV1
}

// History is an agent.History on PostgreSQL with conversation listing and deletion. It is safe for
// concurrent use.
type History struct {
	pool *pgxpool.Pool
}

var _ agent.History = (*History)(nil)

// NewHistory returns a History on options.Pool.
func NewHistory(options *HistoryOptions) (*History, error) {
	if options == nil || options.Pool == nil {
		return nil, errors.New("aikido/postgresadapter: HistoryOptions.Pool is required")
	}
	return &History{pool: options.Pool}, nil
}

// appendMessages adds the messages after the conversation's earlier ones in one statement. The
// conversation row's update locks it, so concurrent appends to one conversation take turns and
// each gets a contiguous range of ordinals; the first append creates the row. updated_at is the
// time the row lock was granted, not the transaction's start (now()), so an append that waited
// for another one does not move it back.
const appendMessages = `
WITH conversation AS (
    INSERT INTO govengers_aikido_conversations AS existing (id, message_count)
    VALUES ($1, cardinality($2::text[]))
    ON CONFLICT (id) DO UPDATE
        SET message_count = existing.message_count + EXCLUDED.message_count,
            updated_at = greatest(existing.updated_at, clock_timestamp())
    RETURNING message_count
)
INSERT INTO govengers_aikido_messages (conversation_id, ordinal, message)
SELECT $1, conversation.message_count - cardinality($2::text[]) + item.ordinality - 1, item.message::json
FROM conversation, unnest($2::text[]) WITH ORDINALITY AS item (message, ordinality)`

// Append stores messages after the conversation's earlier messages, all or none, and starts the
// conversation without an owner if it does not exist. Appending no messages is a no-op.
//
// Append to a conversation that Delete removes concurrently starts it again; delete under the
// session's agent.Locker lock.
func (h *History) Append(ctx context.Context, sessionID string, messages ...llm.Message) error {
	if err := checkID(sessionID); err != nil {
		return err
	}
	if len(messages) == 0 {
		return nil
	}
	encoded := make([]string, len(messages))
	for index, message := range messages {
		stored, err := json.Marshal(storedMessageOf(message))
		if err != nil {
			return fmt.Errorf("aikido/postgresadapter: append to %s: encode message %d: %w", sessionID, index, err)
		}
		encoded[index] = string(stored)
	}
	// READ COMMITTED: under a REPEATABLE READ default, an append that waited for a concurrent one
	// would fail with a serialization error instead of continuing after it.
	err := pgx.BeginTxFunc(ctx, h.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, appendMessages, sessionID, encoded)
		return err
	})
	if err != nil {
		return fmt.Errorf("aikido/postgresadapter: append to %s: %w", sessionID, err)
	}
	return nil
}

const readMessages = `
SELECT message FROM govengers_aikido_messages WHERE conversation_id = $1 ORDER BY ordinal`

// Read returns the conversation's messages in append order, none for an unknown conversation.
func (h *History) Read(ctx context.Context, sessionID string) ([]llm.Message, error) {
	if err := checkID(sessionID); err != nil {
		return nil, err
	}
	rows, err := h.pool.Query(ctx, readMessages, sessionID)
	if err != nil {
		return nil, fmt.Errorf("aikido/postgresadapter: read %s: %w", sessionID, err)
	}
	messages, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (llm.Message, error) {
		var encoded []byte
		if err := row.Scan(&encoded); err != nil {
			return llm.Message{}, err
		}
		var stored storedMessage
		if err := json.Unmarshal(encoded, &stored); err != nil {
			return llm.Message{}, fmt.Errorf("decode a stored message: %w", err)
		}
		return stored.message(), nil
	})
	if err != nil {
		return nil, fmt.Errorf("aikido/postgresadapter: read %s: %w", sessionID, err)
	}
	return messages, nil
}

const createConversation = `
INSERT INTO govengers_aikido_conversations (id, owner, title) VALUES ($1, $2, $3)
ON CONFLICT (id) DO NOTHING`

// Create starts the conversation conversation.ID with its Owner and Title, before its first
// Append; the other fields are ignored. It returns ErrConversationExists when the ID is taken, also
// by an Append that came first.
func (h *History) Create(ctx context.Context, conversation Conversation) error {
	if err := checkID(conversation.ID, conversation.Owner, conversation.Title); err != nil {
		return err
	}
	tag, err := h.pool.Exec(ctx, createConversation, conversation.ID, conversation.Owner, conversation.Title)
	if err != nil {
		return fmt.Errorf("aikido/postgresadapter: create conversation %s: %w", conversation.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", ErrConversationExists, conversation.ID)
	}
	return nil
}

const conversationColumns = `id, owner, title, message_count, created_at, updated_at`

const selectConversation = `SELECT ` + conversationColumns + ` FROM govengers_aikido_conversations WHERE id = $1`

// Get returns the conversation id, or ErrConversationNotFound.
func (h *History) Get(ctx context.Context, id string) (Conversation, error) {
	if err := checkID(id); err != nil {
		return Conversation{}, err
	}
	rows, err := h.pool.Query(ctx, selectConversation, id)
	if err != nil {
		return Conversation{}, fmt.Errorf("aikido/postgresadapter: get conversation %s: %w", id, err)
	}
	conversation, err := pgx.CollectExactlyOneRow(rows, scanConversation)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, fmt.Errorf("%w: %s", ErrConversationNotFound, id)
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("aikido/postgresadapter: get conversation %s: %w", id, err)
	}
	return conversation, nil
}

const renameConversation = `UPDATE govengers_aikido_conversations SET title = $2 WHERE id = $1`

// Rename sets the conversation's title; it does not count as an update for List's order.
func (h *History) Rename(ctx context.Context, id, title string) error {
	return h.changeOne(ctx, "rename", renameConversation, id, title)
}

const deleteConversation = `DELETE FROM govengers_aikido_conversations WHERE id = $1`

// Delete removes the conversation and its messages, or returns ErrConversationNotFound. Hold the
// session's agent.Locker lock while deleting, or a running session's next Append starts the
// conversation again without an owner.
func (h *History) Delete(ctx context.Context, id string) error {
	return h.changeOne(ctx, "delete", deleteConversation, id)
}

func (h *History) changeOne(ctx context.Context, action, statement, id string, texts ...string) error {
	if err := checkID(id, texts...); err != nil {
		return err
	}
	arguments := []any{id}
	for _, text := range texts {
		arguments = append(arguments, text)
	}
	tag, err := h.pool.Exec(ctx, statement, arguments...)
	if err != nil {
		return fmt.Errorf("aikido/postgresadapter: %s conversation %s: %w", action, id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", ErrConversationNotFound, id)
	}
	return nil
}

// The limits of ConversationQuery.Limit.
const (
	DefaultListLimit = 50
	MaximumListLimit = 500
)

// ConversationQuery selects a page of one owner's conversations, the most recently updated first.
type ConversationQuery struct {
	// Owner matches Conversation.Owner exactly; empty lists the conversations without an owner.
	Owner string
	// Limit is the page size, 1 to MaximumListLimit; 0 is DefaultListLimit.
	Limit int
	// Cursor is the Next of the previous page; empty for the first page.
	Cursor string
}

// ConversationPage is one page of List.
type ConversationPage struct {
	Conversations []Conversation
	// Next is the Cursor of the following page; empty on the last page.
	Next string
}

const listFirstPage = `SELECT ` + conversationColumns + ` FROM govengers_aikido_conversations
WHERE owner = $1 ORDER BY updated_at DESC, id DESC LIMIT $2`

const listNextPage = `SELECT ` + conversationColumns + ` FROM govengers_aikido_conversations
WHERE owner = $1 AND (updated_at, id) < ($3, $4) ORDER BY updated_at DESC, id DESC LIMIT $2`

// List returns a page of the owner's conversations. A conversation updated between two pages moves
// to the front, so a later page can miss it; reload the first page to see it.
func (h *History) List(ctx context.Context, query ConversationQuery) (ConversationPage, error) {
	limit := query.Limit
	if limit == 0 {
		limit = DefaultListLimit
	}
	if limit < 1 || limit > MaximumListLimit {
		return ConversationPage{}, fmt.Errorf("aikido/postgresadapter: list conversations: limit %d is outside 1 to %d", query.Limit, MaximumListLimit)
	}
	if err := checkText(query.Owner); err != nil {
		return ConversationPage{}, err
	}
	statement, arguments := listFirstPage, []any{query.Owner, limit + 1}
	if query.Cursor != "" {
		updatedAt, id, err := decodeCursor(query.Cursor)
		if err != nil {
			return ConversationPage{}, err
		}
		statement, arguments = listNextPage, append(arguments, updatedAt, id)
	}
	rows, err := h.pool.Query(ctx, statement, arguments...)
	if err != nil {
		return ConversationPage{}, fmt.Errorf("aikido/postgresadapter: list conversations: %w", err)
	}
	conversations, err := pgx.CollectRows(rows, scanConversation)
	if err != nil {
		return ConversationPage{}, fmt.Errorf("aikido/postgresadapter: list conversations: %w", err)
	}
	page := ConversationPage{Conversations: conversations}
	if len(conversations) > limit {
		page.Conversations = conversations[:limit]
		last := page.Conversations[limit-1]
		page.Next = encodeCursor(last.UpdatedAt, last.ID)
	}
	return page, nil
}

func scanConversation(row pgx.CollectableRow) (Conversation, error) {
	var conversation Conversation
	err := row.Scan(&conversation.ID, &conversation.Owner, &conversation.Title, &conversation.MessageCount,
		&conversation.CreatedAt, &conversation.UpdatedAt)
	return conversation, err
}

// encodeCursor writes the sort key of the last conversation of a page: microseconds are
// PostgreSQL's precision, so the key round-trips exactly.
func encodeCursor(updatedAt time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(updatedAt.UnixMicro(), 10) + ":" + id))
}

func decodeCursor(cursor string) (time.Time, string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	micros, id, found := strings.Cut(string(decoded), ":")
	updatedAt, err := strconv.ParseInt(micros, 10, 64)
	if !found || err != nil || checkID(id) != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	return time.UnixMicro(updatedAt), id, nil
}

// checkID returns ErrEmptyConversationID for an empty id and ErrInvalidText when id or one of
// texts cannot be stored.
func checkID(id string, texts ...string) error {
	if id == "" {
		return ErrEmptyConversationID
	}
	return checkText(append(texts, id)...)
}

// checkText returns ErrInvalidText when one of texts is not valid UTF-8 or holds NUL: PostgreSQL
// rejects both in text columns, and the values can come from a remote client.
func checkText(texts ...string) error {
	for _, text := range texts {
		if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			return ErrInvalidText
		}
	}
	return nil
}

// storedMessage is the JSON form of an llm.Message in the messages table. It has its own field
// names, so a change to llm.Message's encoding cannot make stored rows unreadable.
type storedMessage struct {
	Role       llm.Role      `json:"role"`
	Content    string        `json:"content,omitempty"`
	Images     []storedImage `json:"images,omitempty"`
	Audio      []storedAudio `json:"audio,omitempty"`
	ToolCalls  []storedCall  `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Cache      *storedCache  `json:"cache,omitempty"`
}

type storedImage struct {
	URL         string `json:"url,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Data        []byte `json:"data,omitempty"`
}

type storedAudio struct {
	Data   []byte `json:"data,omitempty"`
	Format string `json:"format,omitempty"`
}

type storedCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type storedCache struct {
	TTL llm.CacheTTL `json:"ttl,omitempty"`
}

func storedMessageOf(message llm.Message) storedMessage {
	stored := storedMessage{Role: message.Role, Content: message.Content, ToolCallID: message.ToolCallID}
	for _, image := range message.Images {
		stored.Images = append(stored.Images, storedImage(image))
	}
	for _, audio := range message.Audio {
		stored.Audio = append(stored.Audio, storedAudio(audio))
	}
	for _, call := range message.ToolCalls {
		stored.ToolCalls = append(stored.ToolCalls, storedCall(call))
	}
	if message.Cache != nil {
		stored.Cache = &storedCache{TTL: message.Cache.TTL}
	}
	return stored
}

func (s storedMessage) message() llm.Message {
	message := llm.Message{Role: s.Role, Content: s.Content, ToolCallID: s.ToolCallID}
	for _, image := range s.Images {
		message.Images = append(message.Images, llm.ImagePart(image))
	}
	for _, audio := range s.Audio {
		message.Audio = append(message.Audio, llm.AudioPart(audio))
	}
	for _, call := range s.ToolCalls {
		message.ToolCalls = append(message.ToolCalls, llm.ToolCall(call))
	}
	if s.Cache != nil {
		message.Cache = &llm.CacheBreakpoint{TTL: s.Cache.TTL}
	}
	return message
}
