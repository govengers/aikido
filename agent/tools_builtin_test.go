package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
	"github.com/govengers/aikido/tools"
	"github.com/govengers/aikido/vfs"
	vfsmem "github.com/govengers/aikido/vfs/memory"
)

func newRegistryWithVFS(t *testing.T, options *VFSToolOptions) *tools.Registry {
	t.Helper()
	registry := tools.NewRegistry()
	if err := RegisterVFSTools(registry, options); err != nil {
		t.Fatalf("RegisterVFSTools: %v", err)
	}
	return registry
}

func dispatch(t *testing.T, registry *tools.Registry, name, arguments string) (tools.Result, error) {
	t.Helper()
	return registry.Dispatch(context.Background(), llm.ToolCall{
		ID: "c", Name: name, Arguments: arguments,
	}, tools.Environment{SessionID: "s", TurnID: uuid.New()})
}

func mustDispatch(t *testing.T, registry *tools.Registry, name, arguments string) map[string]any {
	t.Helper()
	result, err := dispatch(t, registry, name, arguments)
	if err != nil {
		t.Fatalf("%s %s: %v", name, arguments, err)
	}
	content, ok := result.Content.(map[string]any)
	if !ok {
		t.Fatalf("%s returned %T; want map[string]any", name, result.Content)
	}
	return content
}

func mustWriteFile(t *testing.T, storage vfs.Storage, filePath, content string) {
	t.Helper()
	if err := storage.WriteFile(context.Background(), filePath, []byte(content), "text/markdown"); err != nil {
		t.Fatalf("WriteFile %s: %v", filePath, err)
	}
}

func TestRegisterVFSTools_Defaults(t *testing.T) {
	storage := vfsmem.New()
	registry := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})

	for _, name := range []string{"read_file", "write_file", "list_files", "delete_file", "search"} {
		if !registry.Has(name) {
			t.Errorf("%s should be registered", name)
		}
	}
}

func TestRegisterVFSTools_ReadOnly(t *testing.T) {
	storage := vfsmem.New()
	registry := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage, ReadOnly: true})

	for _, name := range []string{"read_file", "list_files", "search"} {
		if !registry.Has(name) {
			t.Errorf("%s should be registered in read-only mode", name)
		}
	}
	for _, name := range []string{"write_file", "delete_file"} {
		if registry.Has(name) {
			t.Errorf("%s must NOT be registered in read-only mode", name)
		}
	}
}

func TestRegisterVFSTools_NoSearchWhenNotSearchable(t *testing.T) {
	registry := tools.NewRegistry()
	if err := RegisterVFSTools(registry, &VFSToolOptions{Storage: nonSearchableStorage{}}); err != nil {
		t.Fatalf("RegisterVFSTools: %v", err)
	}
	if registry.Has("search") {
		t.Error("search should not be registered when storage is not Searchable")
	}
	if !registry.Has("read_file") {
		t.Error("read_file should still be registered")
	}
}

func TestRegisterVFSTools_NilArgsRejected(t *testing.T) {
	if err := RegisterVFSTools(nil, &VFSToolOptions{Storage: vfsmem.New()}); err == nil {
		t.Error("expected error for nil registry")
	}
	if err := RegisterVFSTools(tools.NewRegistry(), nil); err == nil {
		t.Error("expected error for nil opts")
	}
	if err := RegisterVFSTools(tools.NewRegistry(), &VFSToolOptions{}); err == nil {
		t.Error("expected error for nil Storage")
	}
}

func TestWriteFile_HappyPath(t *testing.T) {
	storage := vfsmem.New()
	registry := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})

	result, err := dispatch(t, registry, "write_file", `{"path":"notes.md","content":"# hi"}`)
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	content, _ := result.Content.(map[string]any)
	if content["path"] != "notes.md" {
		t.Errorf("result = %v", content)
	}
}

func TestReadFile_HappyPath(t *testing.T) {
	storage := vfsmem.New()
	mustWriteFile(t, storage, "x.md", "# hello")
	registry := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})
	result, err := dispatch(t, registry, "read_file", `{"path":"x.md"}`)
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	content, _ := result.Content.(map[string]any)
	if content["content"] != "# hello" {
		t.Errorf("content = %v", content["content"])
	}
	if content["content_type"] != "text/markdown" {
		t.Errorf("content_type = %v", content["content_type"])
	}
}

func TestReadFile_NotFound(t *testing.T) {
	storage := vfsmem.New()
	registry := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})
	_, err := dispatch(t, registry, "read_file", `{"path":"missing.md"}`)
	if !errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("err = %v; want ErrFileNotFound", err)
	}
}

func TestReadFile_InvalidPath(t *testing.T) {
	storage := vfsmem.New()
	registry := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})
	_, err := dispatch(t, registry, "read_file", `{"path":"../escape.md"}`)
	if !errors.Is(err, vfs.ErrPathInvalid) {
		t.Errorf("err = %v; want ErrPathInvalid", err)
	}
}

func TestListFiles_HappyPath(t *testing.T) {
	storage := vfsmem.New()
	mustWriteFile(t, storage, "a.md", "x")
	mustWriteFile(t, storage, "b.md", "y")
	registry := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})
	result, err := dispatch(t, registry, "list_files", `{}`)
	if err != nil {
		t.Fatalf("list_files: %v", err)
	}
	content, _ := result.Content.(map[string]any)
	files, _ := content["files"].([]map[string]any)
	if len(files) != 2 {
		t.Errorf("len(files) = %d; want 2", len(files))
	}
}

func TestListFiles_HideHiddenPaths(t *testing.T) {
	storage := vfsmem.New()
	mustWriteFile(t, storage, "_internal/secret.md", "x")
	mustWriteFile(t, storage, "notes.md", "y")
	registry := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage, HideHiddenPaths: true})
	files, _ := mustDispatch(t, registry, "list_files", `{}`)["files"].([]map[string]any)
	if len(files) != 1 || files[0]["path"] != "notes.md" {
		t.Errorf("hidden path leaked: %v", files)
	}
}

func TestDeleteFile_HappyPath(t *testing.T) {
	storage := vfsmem.New()
	mustWriteFile(t, storage, "a.md", "x")
	registry := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})
	if _, err := dispatch(t, registry, "delete_file", `{"path":"a.md"}`); err != nil {
		t.Errorf("delete: %v", err)
	}
	if _, _, err := storage.ReadFile(context.Background(), "a.md"); !errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("file should be gone: %v", err)
	}
}

func TestDeleteFile_NotFound(t *testing.T) {
	storage := vfsmem.New()
	registry := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})
	_, err := dispatch(t, registry, "delete_file", `{"path":"missing.md"}`)
	if !errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("err = %v; want ErrFileNotFound", err)
	}
}

func TestWriteFile_AllowedExtensions(t *testing.T) {
	storage := vfsmem.New()
	registry := newRegistryWithVFS(t, &VFSToolOptions{
		Storage:           storage,
		AllowedExtensions: []string{".md"},
	})
	if _, err := dispatch(t, registry, "write_file", `{"path":"x.md","content":"ok"}`); err != nil {
		t.Errorf("md write: %v", err)
	}
	_, err := dispatch(t, registry, "write_file", `{"path":"x.bin","content":"ok"}`)
	if !errors.Is(err, vfs.ErrPathInvalid) {
		t.Errorf("bin write err = %v; want ErrPathInvalid", err)
	}
}

func TestWriteFile_MaximumFileBytes(t *testing.T) {
	storage := vfsmem.New()
	registry := newRegistryWithVFS(t, &VFSToolOptions{
		Storage: storage, MaximumFileBytes: 8,
	})
	_, err := dispatch(t, registry, "write_file", `{"path":"x.md","content":"this is too big"}`)
	if !errors.Is(err, vfs.ErrFileTooLarge) {
		t.Errorf("err = %v; want ErrFileTooLarge", err)
	}
}

func TestSearch_HappyPath(t *testing.T) {
	storage := vfsmem.New()
	mustWriteFile(t, storage, "a.md", "hello world")
	mustWriteFile(t, storage, "b.md", "hello there")
	mustWriteFile(t, storage, "c.md", "nothing")
	registry := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})
	result, err := dispatch(t, registry, "search", `{"query":"hello"}`)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	content, _ := result.Content.(map[string]any)
	paths, _ := content["paths"].([]string)
	if len(paths) != 2 {
		t.Errorf("paths = %v; want 2 hits", paths)
	}
}

func TestSearch_HidesHidden(t *testing.T) {
	storage := vfsmem.New()
	mustWriteFile(t, storage, "_internal/x.md", "hello")
	mustWriteFile(t, storage, "notes.md", "hello")
	registry := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage, HideHiddenPaths: true})
	paths, _ := mustDispatch(t, registry, "search", `{"query":"hello"}`)["paths"].([]string)
	if len(paths) != 1 || paths[0] != "notes.md" {
		t.Errorf("hidden path leaked: %v", paths)
	}
}

func TestSearch_DescriptionEmbedsSyntax(t *testing.T) {
	storage := vfsmem.New()
	registry := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})
	definitions := registry.Definitions()
	var searchDefinition llm.ToolDefinition
	for _, definition := range definitions {
		if definition.Name == "search" {
			searchDefinition = definition
		}
	}
	if !strings.Contains(searchDefinition.Description, storage.SearchSyntax()) {
		t.Errorf("search description does not embed SearchSyntax: %q", searchDefinition.Description)
	}
}

func TestVFSToolsViaSession_RoundTrip(t *testing.T) {
	storage := vfsmem.New()
	registry := tools.NewRegistry()
	if err := RegisterVFSTools(registry, &VFSToolOptions{Storage: storage}); err != nil {
		t.Fatalf("register: %v", err)
	}

	stub := llmtest.NewStubClient(
		// turn 1: write_file
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventToolCall, Tool: &llm.ToolCall{
				ID: "c1", Name: "write_file",
				Arguments: `{"path":"hello.md","content":"hello world"}`,
			}},
			{Kind: llm.EventEnd},
		}},
		// turn 2: read_file
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventToolCall, Tool: &llm.ToolCall{
				ID: "c2", Name: "read_file",
				Arguments: `{"path":"hello.md"}`,
			}},
			{Kind: llm.EventEnd},
		}},
		// turn 3: stop
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventTextDelta, Text: "all done"},
			{Kind: llm.EventEnd},
		}},
	)

	session, err := NewLocalSession(&SessionOptions{
		ID: "sx", Client: stub, Model: "m", Tools: registry,
	})
	if err != nil {
		t.Fatalf("NewLocalSession: %v", err)
	}
	events, _ := session.Run(context.Background(), "write hello world")
	for range events {
	}

	got, _, err := storage.ReadFile(context.Background(), "hello.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("content = %q", got)
	}
}

// nonSearchableStorage implements only vfs.Storage.
type nonSearchableStorage struct{}

func (nonSearchableStorage) ListFiles(_ context.Context) ([]vfs.FileMetadata, error) { return nil, nil }

func (nonSearchableStorage) ReadFile(_ context.Context, _ string) ([]byte, vfs.FileMetadata, error) {
	return nil, vfs.FileMetadata{}, vfs.ErrFileNotFound
}

func (nonSearchableStorage) WriteFile(_ context.Context, _ string, _ []byte, _ string) error {
	return nil
}
func (nonSearchableStorage) DeleteFile(_ context.Context, _ string) error { return nil }
