package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/tools"
	"github.com/govengers/aikido/vfs"
)

const (
	grepMaximumLines     = 200      // grep output lines (or files / counts)
	grepMaximumLineChars = 500      // runes kept per grep output line
	grepMaximumContext   = 5        // context lines around a match
	grepScanBudget       = 32 << 20 // bytes grep reads per call before stopping
	globMaximumPaths     = 500      // glob result paths
)

const globSyntax = "Glob syntax: `*` matches within one path segment, `**` across segments (`**/*.md` = every markdown file), `?` one character, `{a,b}` alternatives. Paths are relative, without a leading slash."

// grepMode is the result shape the grep tool returns.
type grepMode string

const (
	grepModeContent grepMode = "content"
	grepModeFiles   grepMode = "files_with_matches"
	grepModeCount   grepMode = "count"
)

func grepDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name: "grep",
		Description: fmt.Sprintf("Search file contents with a regular expression (Go RE2 syntax, no backreferences or lookaround). "+
			"output_mode content (default) returns `path:line:text` for matches and `path-line-text` for context lines; "+
			"files_with_matches returns matching paths; count returns matches per file. "+
			"Returns at most %d entries, lines cut at %d characters; truncated is true when results were cut or the scan stopped early. "+
			"Binary files are skipped. %s", grepMaximumLines, grepMaximumLineChars, globSyntax),
		Parameters: tools.Object(map[string]any{
			"pattern":          tools.String("Regular expression to search for."),
			"path_glob":        tools.String("Optional glob limiting which files are searched, e.g. `docs/**/*.md`."),
			"case_insensitive": tools.Boolean("Match case-insensitively."),
			"context":          tools.Integer(fmt.Sprintf("Lines of context before and after each match in content mode (0-%d).", grepMaximumContext)),
			"output_mode":      tools.Enum("Result shape.", string(grepModeContent), string(grepModeFiles), string(grepModeCount)),
		}, "pattern"),
	}
}

func globDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "glob",
		Description: fmt.Sprintf("List file paths matching a glob pattern, sorted by path, at most %d. %s", globMaximumPaths, globSyntax),
		Parameters: tools.Object(map[string]any{
			"pattern": tools.String("Glob pattern, e.g. `**/*.md`."),
		}, "pattern"),
	}
}

func globHandler(options VFSToolOptions) tools.Handler {
	return func(ctx context.Context, rawArguments json.RawMessage, _ tools.Environment) (tools.Result, error) {
		var arguments struct {
			Pattern string `json:"pattern"`
		}
		if err := decodeArguments("glob", rawArguments, &arguments); err != nil {
			return tools.Result{}, err
		}
		pattern, err := globToRegexp(arguments.Pattern)
		if err != nil {
			return tools.Result{}, err
		}
		files, err := options.Storage.ListFiles(ctx)
		if err != nil {
			return tools.Result{}, err
		}
		paths := []string{}
		truncated := false
		for _, file := range files {
			if !visible(options, file.Path) || !pattern.MatchString(file.Path) {
				continue
			}
			if len(paths) == globMaximumPaths {
				truncated = true
				break
			}
			paths = append(paths, file.Path)
		}
		return tools.Result{
			Content: map[string]any{"paths": paths, "truncated": truncated},
		}, nil
	}
}

func grepHandler(options VFSToolOptions) tools.Handler {
	return func(ctx context.Context, rawArguments json.RawMessage, _ tools.Environment) (tools.Result, error) {
		query, err := parseGrepQuery(rawArguments)
		if err != nil {
			return tools.Result{}, err
		}
		files, err := options.Storage.ListFiles(ctx)
		if err != nil {
			return tools.Result{}, err
		}
		result, err := grepFiles(ctx, options, query, files)
		if err != nil {
			return tools.Result{}, err
		}
		return tools.Result{Content: result.content(query.mode)}, nil
	}
}

// grepQuery is a validated grep tool call.
type grepQuery struct {
	pattern      *regexp.Regexp
	pathPattern  *regexp.Regexp // nil searches every visible file
	mode         grepMode
	contextLines int
}

func parseGrepQuery(rawArguments json.RawMessage) (grepQuery, error) {
	var arguments struct {
		Pattern         string `json:"pattern"`
		PathGlob        string `json:"path_glob"`
		CaseInsensitive bool   `json:"case_insensitive"`
		Context         int    `json:"context"`
		OutputMode      string `json:"output_mode"`
	}
	if err := decodeArguments("grep", rawArguments, &arguments); err != nil {
		return grepQuery{}, err
	}
	expression := arguments.Pattern
	if arguments.CaseInsensitive {
		expression = "(?i)" + expression
	}
	pattern, err := regexp.Compile(expression)
	if err != nil {
		return grepQuery{}, fmt.Errorf("aikido/agent: grep: invalid pattern: %w", err)
	}
	query := grepQuery{
		pattern:      pattern,
		contextLines: min(max(arguments.Context, 0), grepMaximumContext),
	}
	if arguments.PathGlob != "" {
		if query.pathPattern, err = globToRegexp(arguments.PathGlob); err != nil {
			return grepQuery{}, err
		}
	}
	if query.mode, err = parseGrepMode(arguments.OutputMode); err != nil {
		return grepQuery{}, err
	}
	return query, nil
}

func parseGrepMode(outputMode string) (grepMode, error) {
	switch mode := grepMode(outputMode); mode {
	case "":
		return grepModeContent, nil
	case grepModeContent, grepModeFiles, grepModeCount:
		return mode, nil
	default:
		return "", fmt.Errorf("aikido/agent: grep: unknown output_mode %q", outputMode)
	}
}

func (q grepQuery) searches(options VFSToolOptions, filePath string) bool {
	return visible(options, filePath) && (q.pathPattern == nil || q.pathPattern.MatchString(filePath))
}

// grepFiles scans files in order until the result is full or the scan budget
// is spent; both mark the result truncated.
//
// ponytail: brute force over ListFiles + ReadFile (one storage round trip
// per file), bounded by grepScanBudget. Add an optional vfs.Greppable
// capability once a network backend needs native grep.
func grepFiles(ctx context.Context, options VFSToolOptions, query grepQuery, files []vfs.FileMetadata) (*grepResult, error) {
	result := &grepResult{lines: []string{}, counts: []map[string]any{}}
	var scanned int64
	for _, file := range files {
		if !query.searches(options, file.Path) {
			continue
		}
		if scanned >= grepScanBudget {
			result.truncated = true
			break
		}
		content, _, err := options.Storage.ReadFile(ctx, file.Path)
		if err != nil {
			return nil, err
		}
		scanned += int64(len(content))
		if isBinary(content) {
			continue
		}
		if !result.addFile(query, file.Path, splitLines(string(content))) {
			result.truncated = true
			break
		}
	}
	return result, nil
}

// grepResult collects grep output. Only one of lines (content and
// files_with_matches modes) and counts (count mode) fills per query.
type grepResult struct {
	lines     []string
	counts    []map[string]any
	truncated bool
}

func (r *grepResult) full() bool {
	return len(r.lines)+len(r.counts) == grepMaximumLines
}

// addFile records the matches of one file. It reports false when the result
// filled up before the file was fully recorded.
func (r *grepResult) addFile(query grepQuery, filePath string, lines []string) (recorded bool) {
	hits, isHit := matchingLines(query.pattern, lines)
	if len(hits) == 0 {
		return true
	}
	if r.full() {
		return false
	}
	switch query.mode {
	case grepModeFiles:
		r.lines = append(r.lines, filePath)
	case grepModeCount:
		r.counts = append(r.counts, map[string]any{"path": filePath, "count": len(hits)})
	case grepModeContent:
		return r.addContent(filePath, lines, hits, isHit, query.contextLines)
	}
	return true
}

// addContent emits each hit with its context lines; context shared by
// neighbouring hits is emitted once.
func (r *grepResult) addContent(filePath string, lines []string, hits []int, isHit []bool, contextLines int) (recorded bool) {
	next := 0 // first line not yet emitted for this file
	for _, hit := range hits {
		first := max(hit-contextLines, next)
		last := min(hit+contextLines, len(lines)-1)
		for lineIndex := first; lineIndex <= last; lineIndex++ {
			if r.full() {
				return false
			}
			r.lines = append(r.lines, grepLine(filePath, lineIndex, lines[lineIndex], isHit[lineIndex]))
		}
		next = last + 1
	}
	return true
}

func (r *grepResult) content(mode grepMode) map[string]any {
	content := map[string]any{"truncated": r.truncated}
	switch mode {
	case grepModeFiles:
		content["files"] = r.lines
	case grepModeCount:
		content["counts"] = r.counts
	case grepModeContent:
		content["matches"] = r.lines
	}
	return content
}

// matchingLines returns the indexes of the lines pattern matches, and per
// line whether it matched.
func matchingLines(pattern *regexp.Regexp, lines []string) (hits []int, isHit []bool) {
	isHit = make([]bool, len(lines))
	for lineIndex, line := range lines {
		if pattern.MatchString(line) {
			hits = append(hits, lineIndex)
			isHit[lineIndex] = true
		}
	}
	return hits, isHit
}

// grepLine formats a content-mode line as `path:line:text` for a match and
// `path-line-text` for context, with a 1-based line number.
func grepLine(filePath string, lineIndex int, line string, matched bool) string {
	separator := "-"
	if matched {
		separator = ":"
	}
	return fmt.Sprintf("%s%s%d%s%s", filePath, separator, lineIndex+1, separator, clip(strings.TrimRight(line, "\r\n")))
}

// clip cuts a grep output line to grepMaximumLineChars runes.
func clip(line string) string {
	runes := []rune(line)
	if len(runes) <= grepMaximumLineChars {
		return line
	}
	return string(runes[:grepMaximumLineChars]) + "…"
}

// globToRegexp compiles a glob (`*`, `**`, `?`, `{a,b}`) into an anchored
// regexp over slash-separated paths. `**/` also matches zero segments.
func globToRegexp(glob string) (*regexp.Regexp, error) {
	if glob == "" {
		return nil, errors.New("aikido/agent: empty glob pattern")
	}
	var expression strings.Builder
	expression.WriteString("^")
	inBraces := false
	for index := 0; index < len(glob); index++ {
		character := glob[index]
		switch {
		case character == '*' && strings.HasPrefix(glob[index:], "**/"):
			expression.WriteString("(?:.*/)?")
			index += 2
		case character == '*' && strings.HasPrefix(glob[index:], "**"):
			expression.WriteString(".*")
			index++
		case character == '*':
			expression.WriteString("[^/]*")
		case character == '?':
			expression.WriteString("[^/]")
		case character == '{' && !inBraces:
			expression.WriteString("(?:")
			inBraces = true
		case character == '}' && inBraces:
			expression.WriteString(")")
			inBraces = false
		case character == ',' && inBraces:
			expression.WriteString("|")
		default:
			expression.WriteString(regexp.QuoteMeta(string(character)))
		}
	}
	if inBraces {
		return nil, fmt.Errorf("aikido/agent: glob %q: unclosed {", glob)
	}
	expression.WriteString("$")
	return regexp.Compile(expression.String())
}
