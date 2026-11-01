package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
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

// textEditArguments is one entry of multi_edit's edits as the model sent it.
// The registry checks only top-level required fields, so a nil string marks
// a nested field that is missing or null, as opposed to an explicit "".
type textEditArguments struct {
	OldString  *string `json:"old_string"`
	NewString  *string `json:"new_string"`
	ReplaceAll bool    `json:"replace_all"`
}

// requireEdits turns multi_edit's entries into edits; an entry without
// old_string or new_string is an error matching tools.ErrInvalidArguments.
func requireEdits(entries []textEditArguments) ([]textEdit, error) {
	edits := make([]textEdit, 0, len(entries))
	for index, entry := range entries {
		switch {
		case entry.OldString == nil:
			return nil, fmt.Errorf("%w for multi_edit: edit %d is missing old_string", tools.ErrInvalidArguments, index+1)
		case entry.NewString == nil:
			return nil, fmt.Errorf("%w for multi_edit: edit %d is missing new_string", tools.ErrInvalidArguments, index+1)
		}
		edits = append(edits, textEdit{OldString: *entry.OldString, NewString: *entry.NewString, ReplaceAll: entry.ReplaceAll})
	}
	return edits, nil
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
			Path  string              `json:"path"`
			Edits []textEditArguments `json:"edits"`
		}
		if err := decodeArguments("multi_edit", rawArguments, &arguments); err != nil {
			return tools.Result{}, err
		}
		if len(arguments.Edits) == 0 {
			return tools.Result{}, errors.New("aikido/agent: multi_edit: edits is empty")
		}
		edits, err := requireEdits(arguments.Edits)
		if err != nil {
			return tools.Result{}, err
		}
		result, err := editFile(ctx, options, arguments.Path, edits)
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
	updated, replacements, err := applyEdits(ctx, filePath, content, edits, options.MaximumFileBytes)
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
// and any edit that is empty, changes nothing, does not match, matches more
// than once without ReplaceAll, or would grow the text beyond maximumBytes
// (checked before replacing, so a chain of replace_all edits cannot grow
// without bound); with several edits the error names the failing one
// (1-based). It returns ctx's error once ctx is done.
func applyEdits(ctx context.Context, filePath string, content []byte, edits []textEdit, maximumBytes int64) (updated string, replacements int, err error) {
	if isBinary(content) {
		return "", 0, fmt.Errorf("%s is not a UTF-8 text file", filePath)
	}
	text := string(content)
	for index, edit := range edits {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		matches, err := countMatches(filePath, text, edit)
		if err == nil {
			err = checkGrowth(len(text), matches, edit, maximumBytes)
		}
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

// checkGrowth fails with an error matching vfs.ErrFileTooLarge when
// replacing matches occurrences in a text of textBytes would make it longer
// than maximumBytes. It divides instead of multiplying, so it cannot
// overflow.
func checkGrowth(textBytes, matches int, edit textEdit, maximumBytes int64) error {
	growth := int64(len(edit.NewString) - len(edit.OldString))
	if growth > 0 && int64(matches) > (maximumBytes-int64(textBytes))/growth {
		return fmt.Errorf("%w: the edit would grow the file beyond %d bytes (caller-policy MaximumFileBytes)", vfs.ErrFileTooLarge, maximumBytes)
	}
	return nil
}

// countMatches returns how often edit replaces in text, or why it cannot.
// Without ReplaceAll, old_string must start at exactly one position, so
// overlapping matches are ambiguous too.
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
	case !edit.ReplaceAll && strings.Index(text, edit.OldString) != strings.LastIndex(text, edit.OldString):
		// strings.Count skips overlaps: "aa" counts once in "aaa" but starts at two positions.
		return 0, fmt.Errorf("old_string matches at overlapping positions in %s; add context to make it unique or set replace_all", filePath)
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
// source. The Storage has no rename, so a failed delete leaves both files. A
// destination that differs from the source only in case goes through
// moveCaseOnly.
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
	caseOnly := strings.EqualFold(arguments.Source, arguments.Destination)
	if !arguments.Overwrite {
		if err := checkAbsent(ctx, options.Storage, arguments.Destination, caseOnly); err != nil {
			return 0, err
		}
	}
	contentType := movedContentType(arguments.Destination, metadata.ContentType)
	if caseOnly {
		return int64(len(content)), moveCaseOnly(ctx, options.Storage, arguments, content, contentType)
	}
	if err := options.Storage.WriteFile(ctx, arguments.Destination, content, contentType); err != nil {
		return 0, err
	}
	if err := options.Storage.DeleteFile(ctx, arguments.Source); err != nil {
		return 0, fmt.Errorf("copied %s to %s but could not delete the source: %w", arguments.Source, arguments.Destination, err)
	}
	return int64(len(content)), nil
}

// caseOnlyCleanupTimeout bounds each cleanup step of moveCaseOnly, which
// ignores the tool call's cancellation and deadline.
const caseOnlyCleanupTimeout = 30 * time.Second

// moveCaseOnly moves source to a destination that differs only in case. On a
// case-insensitive backend (vfs/osfs on macOS or Windows) both name one file,
// so writing the destination and then deleting the source would delete the
// moved file. The bytes go through a staging file next to the destination
// instead: write staging, delete source, write destination, delete staging.
// Cleanup after a failed step ignores ctx's cancellation, so a cancelled move
// does not lose the file, but each cleanup step ends after
// caseOnlyCleanupTimeout, so a stalled Storage cannot hold the run; if even
// restoring the source fails, the error names the staging file that still
// holds the content.
func moveCaseOnly(ctx context.Context, storage vfs.Storage, arguments moveFileArguments, content []byte, contentType string) error {
	staging := path.Join(path.Dir(arguments.Destination), ".aikido-move-"+uuid.NewString()+path.Ext(arguments.Destination))
	if err := storage.WriteFile(ctx, staging, content, contentType); err != nil {
		return fmt.Errorf("stage %s: %w", arguments.Source, err)
	}
	deleteStaging := func(cleanupContext context.Context) error { return storage.DeleteFile(cleanupContext, staging) }
	if err := storage.DeleteFile(ctx, arguments.Source); err != nil {
		return errors.Join(fmt.Errorf("delete %s: %w", arguments.Source, err), cleanup(ctx, deleteStaging))
	}
	if err := storage.WriteFile(ctx, arguments.Destination, content, contentType); err != nil {
		restoreError := cleanup(ctx, func(cleanupContext context.Context) error {
			return storage.WriteFile(cleanupContext, arguments.Source, content, contentType)
		})
		if restoreError != nil {
			return fmt.Errorf("write %s: %w; restoring %s failed too (%w), its content is in %s", arguments.Destination, err, arguments.Source, restoreError, staging)
		}
		return errors.Join(fmt.Errorf("write %s: %w", arguments.Destination, err), cleanup(ctx, deleteStaging))
	}
	if err := cleanup(ctx, deleteStaging); err != nil {
		return fmt.Errorf("moved %s to %s but could not delete the staging file %s: %w", arguments.Source, arguments.Destination, staging, err)
	}
	return nil
}

// cleanup runs one cleanup step on a context that keeps ctx's values but not
// its cancellation or deadline, and ends after caseOnlyCleanupTimeout.
func cleanup(ctx context.Context, step func(context.Context) error) error {
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), caseOnlyCleanupTimeout)
	defer cancel()
	return step(cleanupContext)
}

// checkAbsent fails when filePath already holds a file. For a case-only move
// it looks for filePath in the listing instead of reading it, since a read on
// a case-insensitive backend finds the source itself.
func checkAbsent(ctx context.Context, storage vfs.Storage, filePath string, caseOnly bool) error {
	exists, err := fileExists(ctx, storage, filePath, caseOnly)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("destination %s exists; set overwrite to replace it", filePath)
	}
	return nil
}

// fileExists reports whether storage holds filePath, by reading it or, with
// fromListing, by looking for the exact path in ListFiles.
func fileExists(ctx context.Context, storage vfs.Storage, filePath string, fromListing bool) (bool, error) {
	if fromListing {
		files, err := storage.ListFiles(ctx)
		if err != nil {
			return false, err
		}
		return slices.ContainsFunc(files, func(file vfs.FileMetadata) bool { return file.Path == filePath }), nil
	}
	_, _, err := storage.ReadFile(ctx, filePath)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, vfs.ErrFileNotFound):
		return false, nil
	default:
		return false, err
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
