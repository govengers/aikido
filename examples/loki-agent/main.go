// loki-agent answers questions about logs in Grafana Loki. It discovers the
// labels, pulls log lines with LogQL into an in-memory workspace, analyzes them
// with the virtual bash (grep -c, sort | uniq -c, awk) and answers with a short
// report: what happened, when, how often, the likely cause and the next
// queries worth running. Nothing on the host is touched; the only outside
// connection is Loki's read API.
//
//	OPENROUTER_API_KEY=... LOKI_URL=https://loki.example.com \
//	  go run ./examples/loki-agent "Why did checkout requests fail in prod during the last two hours?"
//
// Authentication: LOKI_TOKEN (bearer) or LOKI_USERNAME and LOKI_PASSWORD
// (basic, e.g. Grafana Cloud); LOKI_TENANT sets X-Scope-OrgID for a
// multi-tenant Loki.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/openrouter"
	"github.com/govengers/aikido/tools"
	"github.com/govengers/aikido/tools/shell"
	"github.com/govengers/aikido/vfs"
	"github.com/govengers/aikido/vfs/memory"
)

const systemPrompt = "You are a log analyst with read access to Grafana Loki. " +
	"Find the relevant streams with loki_labels and loki_label_values, then fetch lines with loki_query: " +
	"it stores them in the workspace (one line per entry: timestamp, labels, message) and shows a preview. " +
	"Analyze the stored files with bash (grep -c, cut, sort | uniq -c | sort -rn, awk for time buckets) " +
	"instead of reading them line by line, and narrow the LogQL when a result is cut at its limit. " +
	"Answer with: Summary (two sentences), Findings (patterns with counts and time ranges), " +
	"Likely cause (with your confidence and the evidence), Next queries (LogQL worth running). " +
	"Quote only short log excerpts and never invent lines you did not fetch."

// previewLines is how many fetched lines a loki_query result shows the model;
// the rest is in the stored file.
const previewLines = 20

func main() {
	if len(os.Args) != 2 {
		log.Fatal(`usage: loki-agent "<question about the logs>"`)
	}
	if err := run(context.Background(), os.Args[1]); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, question string) error {
	loki, err := lokiFromEnvironment()
	if err != nil {
		return err
	}
	client, err := openrouter.NewClient(&openrouter.Options{APIKey: os.Getenv("OPENROUTER_API_KEY")})
	if err != nil {
		return err
	}
	session, err := newSession(client, loki, "anthropic/claude-haiku-5.5")
	if err != nil {
		return err
	}
	events, err := session.Run(ctx, question)
	if err != nil {
		return err
	}
	return printEvents(os.Stdout, events)
}

// newSession builds the agent: Loki tools, file tools and the virtual bash
// over one in-memory workspace.
func newSession(client llm.Client, loki *lokiClient, model string) (*agent.Session, error) {
	workspace := memory.New()
	registry := tools.NewRegistry()
	if err := agent.RegisterVFSTools(registry, &agent.VFSToolOptions{Storage: workspace}); err != nil {
		return nil, err
	}
	if err := shell.Register(registry, &shell.Options{Storage: workspace}); err != nil {
		return nil, err
	}
	if err := registerLokiTools(registry, loki, workspace); err != nil {
		return nil, err
	}
	return agent.NewLocalSession(&agent.SessionOptions{
		ID:            "loki-agent",
		Client:        client,
		Tools:         registry,
		Model:         model,
		SystemPrompt:  systemPrompt,
		MaximumTurns:  40,
		MaximumTokens: 8192,
		RunTimeout:    10 * time.Minute,
	})
}

func registerLokiTools(registry *tools.Registry, loki *lokiClient, workspace vfs.Storage) error {
	if err := registry.Register(llm.ToolDefinition{
		Name:        "loki_labels",
		Description: "List the label names Loki has seen in the last hours.",
		Parameters:  tools.Object(map[string]any{"hours": tools.Integer("How far back, 1 to 168. Default 6.")}),
	}, labelsHandler(loki)); err != nil {
		return err
	}
	if err := registry.Register(llm.ToolDefinition{
		Name:        "loki_label_values",
		Description: "List the values of one label (e.g. app, namespace, level) seen in the last hours.",
		Parameters: tools.Object(map[string]any{
			"label": tools.String("Label name."),
			"hours": tools.Integer("How far back, 1 to 168. Default 6."),
		}, "label"),
	}, labelValuesHandler(loki)); err != nil {
		return err
	}
	return registry.Register(llm.ToolDefinition{
		Name: "loki_query",
		Description: "Run a LogQL log query over the last hours and store the lines, oldest first, in the workspace " +
			"file logs/<name>.log. Returns the file, the line count, whether the limit cut the result, and a preview.",
		Parameters: tools.Object(map[string]any{
			"query": tools.String(`LogQL log query, e.g. {app="checkout", env="prod"} |= "error"`),
			"name":  tools.String("File name without extension, e.g. checkout-errors."),
			"hours": tools.Integer("How far back, 1 to 168. Default 2."),
			"limit": tools.Integer("Maximum lines, 1 to 5000. Default 1000."),
		}, "query", "name"),
	}, queryHandler(loki, workspace))
}

func labelsHandler(loki *lokiClient) tools.Handler {
	return func(ctx context.Context, rawArguments json.RawMessage, _ tools.Environment) (tools.Result, error) {
		var arguments struct {
			Hours int `json:"hours"`
		}
		if err := json.Unmarshal(rawArguments, &arguments); err != nil {
			return tools.Result{}, err
		}
		labels, err := loki.labels(ctx, "", window(arguments.Hours, 6, 168))
		return tools.Result{Content: labels}, err
	}
}

func labelValuesHandler(loki *lokiClient) tools.Handler {
	return func(ctx context.Context, rawArguments json.RawMessage, _ tools.Environment) (tools.Result, error) {
		var arguments struct {
			Label string `json:"label"`
			Hours int    `json:"hours"`
		}
		if err := json.Unmarshal(rawArguments, &arguments); err != nil {
			return tools.Result{}, err
		}
		values, err := loki.labels(ctx, arguments.Label, window(arguments.Hours, 6, 168))
		return tools.Result{Content: values}, err
	}
}

func queryHandler(loki *lokiClient, workspace vfs.Storage) tools.Handler {
	return func(ctx context.Context, rawArguments json.RawMessage, _ tools.Environment) (tools.Result, error) {
		var arguments struct {
			Query string `json:"query"`
			Name  string `json:"name"`
			Hours int    `json:"hours"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(rawArguments, &arguments); err != nil {
			return tools.Result{}, err
		}
		limit := bounded(arguments.Limit, 1000, 5000)
		entries, err := loki.query(ctx, arguments.Query, window(arguments.Hours, 2, 168), limit)
		if err != nil {
			return tools.Result{}, err
		}
		path := "logs/" + strings.Trim(arguments.Name, "/.") + ".log"
		lines := formatEntries(entries)
		if err := workspace.WriteFile(ctx, path, []byte(strings.Join(lines, "\n")+"\n"), "text/plain"); err != nil {
			return tools.Result{}, err
		}
		return tools.Result{Content: map[string]any{
			"file":    path,
			"lines":   len(lines),
			"cut":     len(entries) == limit,
			"preview": lines[:min(len(lines), previewLines)],
		}}, nil
	}
}

func window(hours, fallback, maximum int) time.Duration {
	return time.Duration(bounded(hours, fallback, maximum)) * time.Hour
}

// bounded is value within 1..maximum, or fallback when the model left it out.
func bounded(value, fallback, maximum int) int {
	if value <= 0 {
		return fallback
	}
	return min(value, maximum)
}

// logEntry is one log line with its stream's labels.
type logEntry struct {
	time    time.Time
	labels  map[string]string
	message string
}

// formatEntries sorts the entries oldest first and writes each as
// "RFC3339 timestamp<TAB>labels<TAB>message", one line, so grep and cut work.
func formatEntries(entries []logEntry) []string {
	sort.SliceStable(entries, func(left, right int) bool { return entries[left].time.Before(entries[right].time) })
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		message := strings.NewReplacer("\n", `\n`, "\t", " ").Replace(entry.message)
		lines = append(lines, entry.time.UTC().Format(time.RFC3339Nano)+"\t"+formatLabels(entry.labels)+"\t"+message)
	}
	return lines
}

func formatLabels(labels map[string]string) string {
	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)
	pairs := make([]string, 0, len(names))
	for _, name := range names {
		pairs = append(pairs, name+"="+labels[name])
	}
	return "{" + strings.Join(pairs, ",") + "}"
}

// lokiClient calls Loki's read API with the operator's credentials; the model
// only chooses queries and windows.
type lokiClient struct {
	http     *http.Client
	baseURL  string
	token    string
	username string
	password string
	tenant   string
	now      func() time.Time
}

func lokiFromEnvironment() (*lokiClient, error) {
	baseURL := strings.TrimRight(os.Getenv("LOKI_URL"), "/")
	if baseURL == "" {
		return nil, errors.New("LOKI_URL is required")
	}
	return &lokiClient{
		http:     &http.Client{Timeout: 30 * time.Second},
		baseURL:  baseURL,
		token:    os.Getenv("LOKI_TOKEN"),
		username: os.Getenv("LOKI_USERNAME"),
		password: os.Getenv("LOKI_PASSWORD"),
		tenant:   os.Getenv("LOKI_TENANT"),
		now:      time.Now,
	}, nil
}

// labels lists label names, or the values of label when it is set.
func (l *lokiClient) labels(ctx context.Context, label string, since time.Duration) ([]string, error) {
	path := "/loki/api/v1/labels"
	if label != "" {
		path = "/loki/api/v1/label/" + url.PathEscape(label) + "/values"
	}
	body, err := l.get(ctx, path, url.Values{"start": {l.nanoseconds(since)}})
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("loki: %w", err)
	}
	return decoded.Data, nil
}

// query runs a LogQL log query; metric queries are refused, the bash tool
// counts and buckets the fetched lines instead.
func (l *lokiClient) query(ctx context.Context, query string, since time.Duration, limit int) ([]logEntry, error) {
	body, err := l.get(ctx, "/loki/api/v1/query_range", url.Values{
		"query":     {query},
		"start":     {l.nanoseconds(since)},
		"limit":     {strconv.Itoa(limit)},
		"direction": {"backward"},
	})
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Data struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Stream map[string]string `json:"stream"`
				Values [][2]string       `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("loki: %w", err)
	}
	if decoded.Data.ResultType != "streams" {
		return nil, fmt.Errorf("loki: %q is a metric query (%s); fetch log lines and count them with bash", query, decoded.Data.ResultType)
	}
	var entries []logEntry
	for _, stream := range decoded.Data.Result {
		for _, value := range stream.Values {
			nanoseconds, err := strconv.ParseInt(value[0], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("loki: timestamp %q: %w", value[0], err)
			}
			entries = append(entries, logEntry{time: time.Unix(0, nanoseconds), labels: stream.Stream, message: value[1]})
		}
	}
	return entries, nil
}

func (l *lokiClient) nanoseconds(since time.Duration) string {
	return strconv.FormatInt(l.now().Add(-since).UnixNano(), 10)
}

func (l *lokiClient) get(ctx context.Context, path string, parameters url.Values) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, l.baseURL+path+"?"+parameters.Encode(), nil)
	if err != nil {
		return nil, err
	}
	switch {
	case l.token != "":
		request.Header.Set("Authorization", "Bearer "+l.token)
	case l.username != "":
		request.SetBasicAuth(l.username, l.password)
	}
	if l.tenant != "" {
		request.Header.Set("X-Scope-OrgID", l.tenant)
	}
	response, err := l.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("loki: %w", err)
	}
	defer func() { _ = response.Body.Close() }() // read-only response
	body, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("loki: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		// Loki's error text tells the model what is wrong with its LogQL.
		return nil, fmt.Errorf("loki: status %d: %.500s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func printEvents(out io.Writer, events <-chan agent.Event) error {
	var runError error
	for event := range events {
		switch event.Kind {
		case agent.EventText:
			fmt.Fprint(out, event.Text)
		case agent.EventToolCall:
			fmt.Fprintf(out, "\n[%s] %.200s\n", event.ToolCall.Name, event.ToolCall.Arguments)
		case agent.EventError:
			runError = event.Err
		case agent.EventThinking, agent.EventToolResult, agent.EventImage, agent.EventUsage, agent.EventEnd:
		}
	}
	return runError
}
