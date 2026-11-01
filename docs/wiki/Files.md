# Files

The agent's file tools ([Tools](Tools.md#built-in-file-tools)) work on a `vfs.Storage`. It is also the one way an agent reaches files outside the process: a directory through `vfs/osfs`, an embedded corpus through `vfs/embedfs`, your database through your own backend.

## The Storage contract

```go
type Storage interface {
	ListFiles(ctx context.Context) ([]FileMetadata, error)
	ReadFile(ctx context.Context, path string) ([]byte, FileMetadata, error)
	WriteFile(ctx context.Context, path string, content []byte, contentType string) error
	DeleteFile(ctx context.Context, path string) error
}
```

(the interface as declared in [vfs/storage.go](../../vfs/storage.go), comments left out)

- `ListFiles` returns every file sorted by path. `vfs.FileMetadata` holds `Path`, `ContentType`, `Size` (bytes) and `UpdatedAt` (zero when unknown).
- Errors match `vfs.ErrPathInvalid`, `vfs.ErrFileNotFound` (read and delete of a missing file) or `vfs.ErrReadOnly` (a backend or mount that refuses writes). `vfs.ErrFileTooLarge` comes from the tools' `MaximumFileBytes`.
- Paths are relative and `/`-separated: not empty, at most `vfs.MaximumPathBytes` (512), no NUL byte, no leading or trailing slash, no `//`, no `..` segment. `vfs.ValidatePath` checks exactly that and names the broken rule; backends call it on entry, the tools call it before the backend.
- Each call is atomic on its own; a run of the agent is not a transaction (see [Agent](Agent.md#files-are-the-authority-history-is-not-rewindable)).

```go
	err := vfs.ValidatePath("../etc/passwd")
	fmt.Println(errors.Is(err, vfs.ErrPathInvalid), err)
	// Output: true aikido/vfs: invalid path: parent-directory segment
```

(`ExampleValidatePath`, [vfs/example_test.go](../../vfs/example_test.go))

`vfs.ContentTypeFor(path)` maps the text extensions aikido knows (`.md`, `.txt`, `.json`, `.yaml`, `.html`, `.csv`, ...) to a MIME type and returns `""` otherwise, so each caller picks its fallback.

## Capabilities

- **`vfs.Searchable`**: `Search(ctx, query) ([]string, error)` plus `SearchSyntax() string`. `RegisterVFSTools` registers the `search` tool only for a searchable store and puts `SearchSyntax()` into its description.
- **`vfs.ScopedStorage`**: `Scope(scope string) Storage`. The library never passes a tenant; bind it first and hand the bound store to `RegisterVFSTools` (ADR-013).

## Scoping

A multi-tenant backend implements `ScopedStorage`; the handler of every request calls `storage.Scope(tenantID)` and registers the tools on the result. The tools capture that store by closure, so a model has no argument that reaches another tenant.

## Backends

| Package | Constructor | Writes | Search | Notes |
|---|---|---|---|---|
| `vfs/memory` | `memory.New()` | yes | case-insensitive substring | in-process, copies on the way in and out |
| `vfs/osfs` | `osfs.New(&osfs.Options{Directory: dir})` | atomic (temp file + rename), parents created | no | all access through `os.Root`: no path or symlink resolves outside the directory; regular files only, `.git` skipped; the content type comes from the extension (`application/octet-stream` otherwise); `Close` when done |
| `vfs/embedfs` | `embedfs.New(&embedfs.Options{FileSystem: fsys})` | no (`vfs.ErrReadOnly`) | case-insensitive substring | any `fs.FS`: `embed.FS`, `fs.Sub`, `fstest.MapFS`, `os.DirFS`; unknown extensions are `application/octet-stream` |

From [vfs/osfs/example_test.go](../../vfs/osfs/example_test.go):

```go
	storage, err := osfs.New(&osfs.Options{Directory: directory})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer storage.Close()

	ctx := context.Background()
	if err := storage.WriteFile(ctx, "notes/today.md", []byte("# Today"), "text/markdown"); err != nil {
```

From `ExampleNew` in [vfs/embedfs/storage_test.go](../../vfs/embedfs/storage_test.go):

```go
	knowledge := fstest.MapFS{
		"faq.md": &fstest.MapFile{Data: []byte("Opening hours: 9 to 5")},
	}
	storage, err := embedfs.New(&embedfs.Options{FileSystem: knowledge})
```

With `//go:embed knowledge/*.md`, pass `fs.Sub(corpus, "knowledge")` so the model sees `faq.md`, not `knowledge/faq.md` ([examples/chatbot](../../examples/chatbot/main.go)).

## Overlay

`vfs/overlay` is a copy-on-write `Storage` over any base: reads fall through to the base, writes and deletes stay in memory until `Commit`. It is the way to let an agent work on real files as a dry run. From [vfs/overlay/example_test.go](../../vfs/overlay/example_test.go):

```go
	workspace, err := overlay.New(&overlay.Options{Base: base})
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := workspace.WriteFile(ctx, "notes.md", []byte("# Notes\n- milk\n- eggs\n"), "text/markdown"); err != nil {
		fmt.Println(err)
		return
	}

	changes, err := workspace.Changes(ctx)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, change := range changes {
		fmt.Println(change.Kind, change.Path)
	}
	fmt.Print(overlay.UnifiedDiff(changes))

	if err := workspace.Commit(ctx); err != nil {
```

- `Changes(ctx)` lists what `Commit` would do, sorted by path: `overlay.ChangeCreate`, `ChangeModify`, `ChangeDelete`, each with `Content`, `ContentType` and the base's `Previous` bytes. Writes equal to the base and deletes of files the base never had are not listed; content types are not compared.
- `overlay.UnifiedDiff(changes)` renders them as one unified diff for reading (`a/<path>`, `b/<path>`, `/dev/null` for created and deleted files, "Binary files ... differ" for non-UTF-8 content). It is anchored on unique lines (the Go toolchain's algorithm): fast, not guaranteed minimal, not meant for `patch`.
- `Commit(ctx)` applies the changes one path at a time in path order and drops each applied entry. On the first failure it stops with an error that names the path and wraps the base's error (`errors.Is(err, vfs.ErrReadOnly)` for a read-only base); the rest stays for a retry. Other calls wait while it runs.
- `Discard()` drops every pending change.
- The overlay is `Searchable` (substring over the merged view, one base read per file it does not hold); the base's own search is not used.
- A read-only base fails at `Commit`, not at `WriteFile`.

Complete program: [examples/agent-overlay](../../examples/agent-overlay/main.go) (agent edits a directory, you see the diff and answer y/N). The CLI does the same with `aikido agent --dir` ([CLI](CLI.md#agent)).

## Mount

`vfs/mount` puts several storages into one tree by path prefix. From [vfs/mount/example_test.go](../../vfs/mount/example_test.go):

```go
	storage, err := mount.New(&mount.Options{Mounts: []mount.Mount{
		{Prefix: "", Storage: memory.New()},
		{Prefix: "docs", Storage: docs, ReadOnly: true},
	}})
```

- The longest matching prefix owns a path (`docs/a.md` goes to `docs`, `docsy.md` to the root); the mounted storage sees the path without its prefix. Files of an outer mount under an inner prefix are hidden, so every path has one owner.
- `ReadOnly` mounts refuse writes and deletes with `vfs.ErrReadOnly`, whatever their storage supports.
- A path no mount owns, or a path equal to a prefix, is `vfs.ErrPathInvalid`. Duplicate prefixes, a nil storage or an invalid prefix fail in `New`.
- `New` returns a `vfs.Storage` that is `Searchable` exactly when at least one mount is, so the `search` tool appears only when it can find something.

## Writing your own backend

Implement the four methods, call `vfs.ValidatePath` on entry, return errors that match the `vfs` sentinels, keep `ListFiles` sorted, and make it safe for concurrent use. Add `Search`/`SearchSyntax` if your store can search (full-text index, `ILIKE`), `Scope` if it holds tenants. Then run the conformance suite in your tests, as [vfs/memory/storage_test.go](../../vfs/memory/storage_test.go) does:

```go
func TestConformance(t *testing.T) {
	vfstest.RunConformance(t, func() vfs.Storage { return New() })
}
```

The factory returns a fresh, empty store per sub-test; the search sub-tests run only when the store is `Searchable`.
