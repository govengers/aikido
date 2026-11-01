package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/tools"
	"github.com/govengers/aikido/vfs"
)

// textEdit is one replacement of edit_file and multi_edit.
type textEdit struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func editFileDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "edit_file",
		Description: "Replace old_string with new_string in an existing file. old_string must match exactly (whitespace included, without read_file's line-number prefix) and be unique in the file unless replace_all is true. Use write_file to create files.",
		Parameters: tools.Object(map[string]any{
			"path":        tools.String("Relative file path within the workspace."),
			"old_string":  tools.String("Exact text to replace."),
			"new_string":  tools.String("Replacement text; must differ from old_string."),
			"replace_all": tools.Boolean("Replace every occurrence instead of requiring a unique match."),
		}, "path", "old_string", "new_string"),
	}
}

func multiEditDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "multi_edit",
		Description: "Apply several edit_file replacements to one file in order, each on the result of the previous one. All or nothing: if one edit fails, the file is unchanged.",
		Parameters: tools.Object(map[string]any{
			"path": tools.String("Relative file path within the workspace."),
			"edits": tools.Array(tools.Object(map[string]any{
				"old_string":  tools.String("Exact text to replace."),
				"new_string":  tools.String("Replacement text; must differ from old_string."),
				"replace_all": tools.Boolean("Replace every occurrence instead of requiring a unique match."),
			}, "old_string", "new_string"), "Edits in the order they apply."),
		}, "path", "edits"),
	}
}

func moveFileDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "move_file",
		Description: "Move or rename a file. Fails if destination exists unless overwrite is true.",
		Parameters: tools.Object(map[string]any{
			"source":      tools.String("Relative path of the file to move."),
			"destination": tools.String("Relative path it moves to."),
			"overwrite":   tools.Boolean("Replace an existing destination file."),
		}, "source", "destination"),
	}
}

func editFileHandler(options VFSToolOptions) tools.Handler {
	return func(ctx context.Context, rawArguments json.RawMessage, _ tools.Environment) (tools.Result, error) {
		var arguments struct {
			Path string `json:"path"`
			textEdit
		}
		if err := decodeArguments("edit_file", rawArguments, &arguments); err != nil {
			return tools.Result{}, err
		}
		result, err := editFile(ctx, options, arguments.Path, []textEdit{arguments.textEdit})
		if err != nil {
			return tools.Result{}, fmt.Errorf("aikido/agent: edit_file: %w", err)
		}
		return result, nil
	}
}

func multiEditHandler(options VFSToolOptions) tools.Handler {
	return func(ctx context.Context, rawArguments json.RawMessage, _ tools.Environment) (tools.Result, error) {
		var arguments struct {
			Path  string     `json:"path"`
			Edits []textEdit `json:"edits"`
		}
		if err := decodeArguments("multi_edit", rawArguments, &arguments); err != nil {
			return tools.Result{}, err
		}
		if len(arguments.Edits) == 0 {
			return tools.Result{}, errors.New("aikido/agent: multi_edit: edits is empty")
		}
		result, err := editFile(ctx, options, arguments.Path, arguments.Edits)
		if err != nil {
			return tools.Result{}, fmt.Errorf("aikido/agent: multi_edit: %w; the file is unchanged", err)
		}
		return result, nil
	}
}

// editFile applies edits to one file in order and writes it once, so either
// every edit lands or none does.
func editFile(ctx context.Context, options VFSToolOptions, filePath string, edits []textEdit) (tools.Result, error) {
	if err := validatePolicyPath(options, filePath); err != nil {
		return tools.Result{}, err
	}
	content, metadata, err := options.Storage.ReadFile(ctx, filePath)
	if err != nil {
		return tools.Result{}, err
	}
	updated, replacements, err := applyEdits(filePath, content, edits)
	if err != nil {
		return tools.Result{}, err
	}
	if err := checkFileSize(options, len(updated)); err != nil {
		return tools.Result{}, err
	}
	contentType := metadata.ContentType
	if contentType == "" {
		contentType = inferContentType(filePath)
	}
	if err := options.Storage.WriteFile(ctx, filePath, []byte(updated), contentType); err != nil {
		return tools.Result{}, err
	}
	return tools.Result{
		Content: map[string]any{"path": filePath, "replacements": replacements, "size": int64(len(updated))},
	}, nil
}

// applyEdits applies edits to content in order. It refuses binary content
// and any edit that is empty, changes nothing, does not match, or matches
// more than once without ReplaceAll; with several edits the error names the
// failing one (1-based).
func applyEdits(filePath string, content []byte, edits []textEdit) (updated string, replacements int, err error) {
	if isBinary(content) {
		return "", 0, fmt.Errorf("%s is not a UTF-8 text file", filePath)
	}
	text := string(content)
	for index, edit := range edits {
		matches, err := countMatches(filePath, text, edit)
		if err != nil {
			if len(edits) > 1 {
				err = fmt.Errorf("edit %d: %w", index+1, err)
			}
			return "", 0, err
		}
		text = strings.ReplaceAll(text, edit.OldString, edit.NewString)
		replacements += matches
	}
	return text, replacements, nil
}

// countMatches returns how often edit replaces in text, or why it cannot.
func countMatches(filePath, text string, edit textEdit) (int, error) {
	if edit.OldString == "" {
		return 0, errors.New("old_string is empty; use write_file to create a file")
	}
	if edit.OldString == edit.NewString {
		return 0, errors.New("old_string and new_string are identical")
	}
	matches := strings.Count(text, edit.OldString)
	switch {
	case matches == 0:
		return 0, fmt.Errorf("old_string not found in %s", filePath)
	case matches > 1 && !edit.ReplaceAll:
		return 0, fmt.Errorf("old_string matches %d times in %s; add context to make it unique or set replace_all", matches, filePath)
	}
	return matches, nil
}

type moveFileArguments struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Overwrite   bool   `json:"overwrite"`
}

func moveFileHandler(options VFSToolOptions) tools.Handler {
	return func(ctx context.Context, rawArguments json.RawMessage, _ tools.Environment) (tools.Result, error) {
		var arguments moveFileArguments
		if err := decodeArguments("move_file", rawArguments, &arguments); err != nil {
			return tools.Result{}, err
		}
		size, err := moveFile(ctx, options, arguments)
		if err != nil {
			return tools.Result{}, fmt.Errorf("aikido/agent: move_file: %w", err)
		}
		return tools.Result{
			Content: map[string]any{"source": arguments.Source, "destination": arguments.Destination, "size": size},
		}, nil
	}
}

// moveFile writes the source's bytes to the destination, then deletes the
// source. The Storage has no rename, so a failed delete leaves both files.
func moveFile(ctx context.Context, options VFSToolOptions, arguments moveFileArguments) (int64, error) {
	if err := validatePolicyPath(options, arguments.Source); err != nil {
		return 0, err
	}
	if err := validatePolicyPath(options, arguments.Destination); err != nil {
		return 0, err
	}
	if arguments.Source == arguments.Destination {
		return 0, errors.New("source and destination are the same path")
	}
	content, metadata, err := options.Storage.ReadFile(ctx, arguments.Source)
	if err != nil {
		return 0, err
	}
	if err := checkFileSize(options, len(content)); err != nil {
		return 0, err
	}
	if !arguments.Overwrite {
		if err := checkAbsent(ctx, options.Storage, arguments.Destination); err != nil {
			return 0, err
		}
	}
	if err := options.Storage.WriteFile(ctx, arguments.Destination, content, movedContentType(arguments.Destination, metadata.ContentType)); err != nil {
		return 0, err
	}
	if err := options.Storage.DeleteFile(ctx, arguments.Source); err != nil {
		return 0, fmt.Errorf("copied %s to %s but could not delete the source: %w", arguments.Source, arguments.Destination, err)
	}
	return int64(len(content)), nil
}

// checkAbsent fails when filePath already holds a file.
func checkAbsent(ctx context.Context, storage vfs.Storage, filePath string) error {
	_, _, err := storage.ReadFile(ctx, filePath)
	switch {
	case err == nil:
		return fmt.Errorf("destination %s exists; set overwrite to replace it", filePath)
	case errors.Is(err, vfs.ErrFileNotFound):
		return nil
	default:
		return err
	}
}

// movedContentType is the content type the destination's extension implies,
// else the source's, else the default for unknown extensions.
func movedContentType(destination, sourceContentType string) string {
	if contentType := vfs.ContentTypeFor(destination); contentType != "" {
		return contentType
	}
	if sourceContentType != "" {
		return sourceContentType
	}
	return unknownContentType
}
