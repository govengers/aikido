package agent

import (
	"errors"
	"fmt"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/tools"
	"github.com/govengers/aikido/vfs"
)

// defaultMaximumFileBytes is the file size limit when VFSToolOptions leaves
// MaximumFileBytes unset.
const defaultMaximumFileBytes = 1 << 20

// VFSToolOptions configure the built-in VFS tools.
type VFSToolOptions struct {
	// Storage is the file store the tools work on. Required; multi-tenant
	// callers pass a store already bound to the tenant's scope.
	Storage vfs.Storage
	// HideHiddenPaths hides paths with a segment beginning with "_" or "."
	// from listings and rejects them as tool arguments.
	HideHiddenPaths bool
	// AllowedExtensions limits the tools to files with these extensions
	// (".md", compared case-insensitively). Nil allows every file.
	AllowedExtensions []string
	// MaximumFileBytes caps the size of a file the tools write; zero or
	// negative means 1 MiB.
	MaximumFileBytes int64

	// ReadOnly disables registration of write_file, delete_file, edit_file,
	// multi_edit and move_file. Use this for fixed-corpus deployments (e.g.
	// an embedded knowledge base over vfs/embedfs). Search and list/read
	// remain registered.
	ReadOnly bool

	// DisableExtended skips registration of grep, glob, edit_file,
	// multi_edit and move_file, leaving the original
	// read_file/write_file/list_files/delete_file/search set.
	DisableExtended bool
}

// RegisterVFSTools registers read_file, list_files, grep and glob into the
// supplied registry, plus write_file, delete_file, edit_file, multi_edit and
// move_file when not read-only (grep, glob, edit_file, multi_edit and
// move_file are skipped with DisableExtended). If
// options.Storage satisfies vfs.Searchable, it also registers search. Tool
// handlers capture options.Storage via closure (ADR-021).
//
// It returns an error for a nil registry or Storage, and an error matching
// tools.ErrDuplicateTool if the registry already holds one of the names.
func RegisterVFSTools(registry *tools.Registry, options *VFSToolOptions) error {
	if registry == nil {
		return errors.New("aikido/agent: RegisterVFSTools: nil registry")
	}
	if options == nil || options.Storage == nil {
		return errors.New("aikido/agent: RegisterVFSTools: Storage is required")
	}
	configured := *options
	if configured.MaximumFileBytes <= 0 {
		configured.MaximumFileBytes = defaultMaximumFileBytes
	}
	for _, tool := range vfsTools(configured) {
		if err := registry.Register(tool.definition, tool.handler); err != nil {
			return fmt.Errorf("aikido/agent: RegisterVFSTools: %w", err)
		}
	}
	return nil
}

type vfsTool struct {
	definition llm.ToolDefinition
	handler    tools.Handler
}

// vfsTools selects the tools the options enable, in registration order.
func vfsTools(options VFSToolOptions) []vfsTool {
	selected := []vfsTool{
		{readFileDefinition(), readFileHandler(options)},
		{listFilesDefinition(), listFilesHandler(options)},
	}
	if !options.ReadOnly {
		selected = append(selected,
			vfsTool{writeFileDefinition(), writeFileHandler(options)},
			vfsTool{deleteFileDefinition(), deleteFileHandler(options)},
		)
	}
	if !options.DisableExtended {
		selected = append(selected,
			vfsTool{grepDefinition(), grepHandler(options)},
			vfsTool{globDefinition(), globHandler(options)},
		)
		if !options.ReadOnly {
			selected = append(selected,
				vfsTool{editFileDefinition(), editFileHandler(options)},
				vfsTool{multiEditDefinition(), multiEditHandler(options)},
				vfsTool{moveFileDefinition(), moveFileHandler(options)},
			)
		}
	}
	if searchable, ok := options.Storage.(vfs.Searchable); ok {
		selected = append(selected, vfsTool{searchDefinition(searchable), searchHandler(options, searchable)})
	}
	return selected
}
