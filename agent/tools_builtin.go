package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/tools"
	"github.com/govengers/aikido/vfs"
)

const (
	defaultReadLimit = 2000     // read_file lines per call
	readMaximumBytes = 40 << 10 // read_file content bytes per call, below the default tool result cap
	binarySniffBytes = 8 << 10  // bytes isBinary checks for a NUL byte
	// unknownContentType is the content type of a written file whose
	// extension vfs.ContentTypeFor does not know (planning question Q26).
	unknownContentType = "text/markdown"
)

func readFileDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "read_file",
		Description: fmt.Sprintf("Read a file by path. Returns up to %d lines (%d KiB) from offset, each prefixed with its line number and a tab that are not part of the file, plus content_type, size, total_lines and truncated (true when lines follow; continue at the next line number).", defaultReadLimit, readMaximumBytes>>10),
		Parameters: tools.Object(map[string]any{
			"path":   tools.String("Relative file path within the workspace."),
			"offset": tools.Integer("Optional 1-based line to start reading at."),
			"limit":  tools.Integer(fmt.Sprintf("Optional maximum number of lines to return (default %d).", defaultReadLimit)),
		}, "path"),
	}
}

func writeFileDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "write_file",
		Description: "Create or overwrite a file at the given path. Use markdown by default; binary writes are not supported.",
		Parameters: tools.Object(map[string]any{
			"path":         tools.String("Relative file path within the workspace."),
			"content":      tools.String("UTF-8 file contents."),
			"content_type": tools.String("Optional MIME type (e.g., text/markdown). Defaults are inferred from the extension."),
		}, "path", "content"),
	}
}

func listFilesDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "list_files",
		Description: "List every file in the workspace, sorted by path. Hidden paths are filtered when configured.",
		Parameters:  tools.Object(map[string]any{}),
	}
}

func deleteFileDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "delete_file",
		Description: "Delete a file by path. Errors if the file does not exist.",
		Parameters: tools.Object(map[string]any{
			"path": tools.String("Relative file path within the workspace."),
		}, "path"),
	}
}

func searchDefinition(searchable vfs.Searchable) llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "search",
		Description: "Search the workspace for files matching a query. " + searchable.SearchSyntax(),
		Parameters: tools.Object(map[string]any{
			"query": tools.String("Search query in the syntax described above."),
		}, "query"),
	}
}

func readFileHandler(options VFSToolOptions) tools.Handler {
	return func(ctx context.Context, rawArguments json.RawMessage, _ tools.Environment) (tools.Result, error) {
		var arguments struct {
			Path   string `json:"path"`
			Offset int    `json:"offset"`
			Limit  int    `json:"limit"`
		}
		if err := decodeArguments("read_file", rawArguments, &arguments); err != nil {
			return tools.Result{}, err
		}
		if err := validatePolicyPath(options, arguments.Path); err != nil {
			return tools.Result{}, err
		}
		content, metadata, err := options.Storage.ReadFile(ctx, arguments.Path)
		if err != nil {
			return tools.Result{}, err
		}
		offset := max(arguments.Offset, 1)
		limit := arguments.Limit
		if limit < 1 {
			limit = defaultReadLimit
		}
		lines := splitLines(string(content))
		if offset > 1 && offset > len(lines) {
			return tools.Result{}, fmt.Errorf("aikido/agent: read_file: offset %d is past the end of %s (%d lines)", offset, arguments.Path, len(lines))
		}
		numbered, end := numberLines(lines, offset-1, min(offset-1+limit, len(lines)))
		return tools.Result{
			Content: map[string]any{
				"content":      numbered,
				"content_type": metadata.ContentType,
				"size":         metadata.Size,
				"offset":       offset,
				"total_lines":  len(lines),
				"truncated":    end < len(lines),
			},
		}, nil
	}
}

// numberLines renders lines[start:end] prefixed with their 1-based line
// number and a tab, stopping early once the text reaches readMaximumBytes
// (always after at least one line). It returns the text and the index after
// the last rendered line.
func numberLines(lines []string, start, end int) (numbered string, rendered int) {
	width := len(strconv.Itoa(end))
	var text strings.Builder
	rendered = start
	for rendered < end && (rendered == start || text.Len()+width+1+len(lines[rendered]) <= readMaximumBytes) {
		fmt.Fprintf(&text, "%*d\t%s", width, rendered+1, lines[rendered])
		rendered++
	}
	return text.String(), rendered
}

func writeFileHandler(options VFSToolOptions) tools.Handler {
	return func(ctx context.Context, rawArguments json.RawMessage, _ tools.Environment) (tools.Result, error) {
		var arguments struct {
			Path        string `json:"path"`
			Content     string `json:"content"`
			ContentType string `json:"content_type"`
		}
		if err := decodeArguments("write_file", rawArguments, &arguments); err != nil {
			return tools.Result{}, err
		}
		if err := validatePolicyPath(options, arguments.Path); err != nil {
			return tools.Result{}, err
		}
		if err := checkFileSize(options, len(arguments.Content)); err != nil {
			return tools.Result{}, err
		}
		contentType := arguments.ContentType
		if contentType == "" {
			contentType = inferContentType(arguments.Path)
		}
		if err := options.Storage.WriteFile(ctx, arguments.Path, []byte(arguments.Content), contentType); err != nil {
			return tools.Result{}, err
		}
		return tools.Result{
			Content: map[string]any{"path": arguments.Path, "size": int64(len(arguments.Content))},
		}, nil
	}
}

func listFilesHandler(options VFSToolOptions) tools.Handler {
	return func(ctx context.Context, _ json.RawMessage, _ tools.Environment) (tools.Result, error) {
		files, err := options.Storage.ListFiles(ctx)
		if err != nil {
			return tools.Result{}, err
		}
		listed := make([]map[string]any, 0, len(files))
		for _, file := range files {
			if !visible(options, file.Path) {
				continue
			}
			listed = append(listed, map[string]any{
				"path":       file.Path,
				"size":       file.Size,
				"updated_at": file.UpdatedAt,
			})
		}
		return tools.Result{
			Content: map[string]any{"files": listed},
		}, nil
	}
}

func deleteFileHandler(options VFSToolOptions) tools.Handler {
	return func(ctx context.Context, rawArguments json.RawMessage, _ tools.Environment) (tools.Result, error) {
		var arguments struct {
			Path string `json:"path"`
		}
		if err := decodeArguments("delete_file", rawArguments, &arguments); err != nil {
			return tools.Result{}, err
		}
		if err := validatePolicyPath(options, arguments.Path); err != nil {
			return tools.Result{}, err
		}
		if err := options.Storage.DeleteFile(ctx, arguments.Path); err != nil {
			return tools.Result{}, err
		}
		return tools.Result{
			Content: map[string]any{"path": arguments.Path},
		}, nil
	}
}

func searchHandler(options VFSToolOptions, searchable vfs.Searchable) tools.Handler {
	return func(ctx context.Context, rawArguments json.RawMessage, _ tools.Environment) (tools.Result, error) {
		var arguments struct {
			Query string `json:"query"`
		}
		if err := decodeArguments("search", rawArguments, &arguments); err != nil {
			return tools.Result{}, err
		}
		paths, err := searchable.Search(ctx, arguments.Query)
		if err != nil {
			return tools.Result{}, err
		}
		visiblePaths := paths[:0:0] // nil stays nil, so the JSON shape matches the storage's answer
		for _, filePath := range paths {
			if visible(options, filePath) {
				visiblePaths = append(visiblePaths, filePath)
			}
		}
		return tools.Result{
			Content: map[string]any{"paths": visiblePaths},
		}, nil
	}
}

// decodeArguments parses a tool's arguments into target. Its errors match
// tools.ErrInvalidArguments and name the tool and, for a wrong type, the
// argument, so the model can correct the call.
func decodeArguments(tool string, rawArguments json.RawMessage, target any) error {
	err := json.Unmarshal(rawArguments, target)
	var typeError *json.UnmarshalTypeError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &typeError) && typeError.Field != "":
		return fmt.Errorf("%w for %s: argument %q must be %s, got %s", tools.ErrInvalidArguments, tool, typeError.Field, jsonKind(typeError.Type.Kind()), typeError.Value)
	default:
		return fmt.Errorf("%w for %s: %w", tools.ErrInvalidArguments, tool, err)
	}
}

// jsonKind names the JSON type a Go kind decodes from.
func jsonKind(kind reflect.Kind) string {
	switch kind {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "an integer"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "an array"
	default:
		return "an object"
	}
}

// validatePolicyPath enforces structural and caller-policy invariants.
func validatePolicyPath(options VFSToolOptions, filePath string) error {
	if err := vfs.ValidatePath(filePath); err != nil {
		return err
	}
	if !extensionAllowed(options, filePath) {
		return fmt.Errorf("%w: extension %q not in AllowedExtensions", vfs.ErrPathInvalid, strings.ToLower(path.Ext(filePath)))
	}
	if options.HideHiddenPaths && isHidden(filePath) {
		return fmt.Errorf("%w: hidden path %q is not addressable when HideHiddenPaths is set", vfs.ErrPathInvalid, filePath)
	}
	return nil
}

func checkFileSize(options VFSToolOptions, size int) error {
	if int64(size) > options.MaximumFileBytes {
		return fmt.Errorf("%w: %d > %d (caller-policy MaximumFileBytes)", vfs.ErrFileTooLarge, size, options.MaximumFileBytes)
	}
	return nil
}

// visible reports whether a path listed by the storage may be shown to the
// model under the caller policy (hidden paths, allowed extensions).
func visible(options VFSToolOptions, filePath string) bool {
	return (!options.HideHiddenPaths || !isHidden(filePath)) && extensionAllowed(options, filePath)
}

func extensionAllowed(options VFSToolOptions, filePath string) bool {
	if len(options.AllowedExtensions) == 0 {
		return true
	}
	extension := path.Ext(filePath)
	for _, allowed := range options.AllowedExtensions {
		if strings.EqualFold(allowed, extension) {
			return true
		}
	}
	return false
}

// splitLines splits text after each newline; joining the result gives text
// back. A trailing newline does not start an extra empty line.
func splitLines(text string) []string {
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// isBinary treats content as binary when its first 8 KiB hold a NUL byte or
// it is not valid UTF-8.
func isBinary(content []byte) bool {
	return bytes.IndexByte(content[:min(len(content), binarySniffBytes)], 0) >= 0 || !utf8.Valid(content)
}

func isHidden(filePath string) bool {
	for _, segment := range strings.Split(filePath, "/") {
		if segment != "" && (segment[0] == '_' || segment[0] == '.') {
			return true
		}
	}
	return false
}

func inferContentType(filePath string) string {
	if contentType := vfs.ContentTypeFor(filePath); contentType != "" {
		return contentType
	}
	return unknownContentType
}
