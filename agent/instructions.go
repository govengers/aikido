package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/govengers/aikido/vfs"
)

// ProjectInstructionsOptions configure ReadProjectInstructions. Names are the
// files read, as paths in the storage, in order (default AGENTS.md, then
// CLAUDE.md); a name that does not exist is skipped unless MissingIsError is
// set (for names a user gave explicitly). MaximumBytes caps the text returned
// (default 32 KiB; the rest is cut with a notice).
type ProjectInstructionsOptions struct {
	Names          []string
	MissingIsError bool
	MaximumBytes   int
}

// Defaults of ReadProjectInstructions.
var defaultInstructionNames = []string{"AGENTS.md", "CLAUDE.md"}

const defaultInstructionBytes = 32 << 10

// ReadProjectInstructions returns the instruction files at the root of
// storage, each under a line naming it, for a caller to give the model at the
// start of a conversation: an instruction repository (a routing table in
// CLAUDE.md, workbooks it points to) only works when the model reads its
// entry file first. Files that do not exist are skipped, and a file whose
// content repeats an earlier one is left out; none found returns "". Pass it
// as SessionOptions.Instructions: it then opens the conversation's first user
// message, which keeps one cached system prompt across directories.
func ReadProjectInstructions(ctx context.Context, storage vfs.Storage, options *ProjectInstructionsOptions) (string, error) {
	if storage == nil {
		return "", errors.New("aikido/agent: ReadProjectInstructions: nil storage")
	}
	names, maximumBytes, missingIsError := defaultInstructionNames, defaultInstructionBytes, false
	if options != nil && len(options.Names) > 0 {
		names, missingIsError = options.Names, options.MissingIsError
	}
	if options != nil && options.MaximumBytes > 0 {
		maximumBytes = options.MaximumBytes
	}
	var text strings.Builder
	seen := map[string]bool{}
	for _, name := range names {
		content, _, err := storage.ReadFile(ctx, name)
		if errors.Is(err, vfs.ErrFileNotFound) && !missingIsError {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("aikido/agent: ReadProjectInstructions: %s: %w", name, err)
		}
		body := strings.TrimSpace(string(content))
		if body == "" || seen[body] {
			continue
		}
		seen[body] = true
		if text.Len() > 0 {
			text.WriteString("\n\n")
		}
		fmt.Fprintf(&text, "Project instructions from %s:\n\n%s", name, body)
	}
	return boundedContent(text.String(), maximumBytes), nil
}
