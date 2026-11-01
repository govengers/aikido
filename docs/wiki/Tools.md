# Tools

A `tools.Registry` holds tool definitions and their handlers; the [agent](Agent.md) offers `registry.Definitions()` to the model and runs `registry.Dispatch` for every call. The built-in file tools and the plan tool are registered into the same registry.

## Registry and handlers

From [tools/example_test.go](../../tools/example_test.go) (`ExampleRegistry`):

```go
	registry := tools.NewRegistry()
	err := registry.Register(llm.ToolDefinition{
		Name:        "greet",
		Description: "Greet a person by name.",
		Parameters:  tools.Object(map[string]any{"name": tools.String("Who to greet.")}, "name"),
	}, func(_ context.Context, arguments json.RawMessage, _ tools.Environment) (tools.Result, error) {
		var parsed struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(arguments, &parsed); err != nil {
			return tools.Result{}, fmt.Errorf("greet arguments: %w", err)
		}
		return tools.Result{Content: "Hello, " + parsed.Name + "!"}, nil
	})
```

- `Register` fails for an empty name, a nil handler, or a taken name (`tools.ErrDuplicateTool`). `Definitions` returns the tools in registration order; `Has(name)` checks one.
- A `tools.Handler` is `func(ctx, arguments json.RawMessage, environment tools.Environment) (tools.Result, error)`. `Result.Content` is JSON-encoded for the model (nil is `null`). A returned error is shown to the model as the tool's failure, so write it for the model: what was wrong and what to do instead.
- `tools.Environment` carries what changes per call: `SessionID` and `TurnID` (one UUID per model reply). Everything else (storage, clock, database) the handler captures by closure when you register it (ADR-021).
- Honour `ctx`: it carries the session's `ToolCallTimeout` as a deadline.

## Schemas

Schemas are explicit JSON Schema: `tools.Object(properties, required...)` with `tools.String`, `tools.Integer`, `tools.Number`, `tools.Boolean`, `tools.Enum(description, values...)` and `tools.Array(items, description)`. Output is deterministic (keys sorted). `Object` panics on a value `encoding/json` cannot encode, since schemas are literals written at registration. There is no reflection helper; struct-driven schemas come from `github.com/invopop/jsonschema` (`json.Marshal(jsonschema.Reflect(&Arguments{}))`) passed as `Parameters`.

## What Dispatch checks

Before the handler runs, `Dispatch` checks the arguments: empty arguments count as `{}`, anything else must be a JSON object (a bare `null` is refused) holding every field the schema lists as `required` (a JSON `null` counts as missing). Only the top-level `required` list is enforced; types, enums and nested objects are not, so the handler still parses and validates. A `Parameters` value that is not an object schema enforces nothing.

From `ExampleRegistry_Dispatch`:

```go
	_, err = registry.Dispatch(context.Background(), llm.ToolCall{Name: "divide", Arguments: `{}`}, tools.Environment{})
	fmt.Println(errors.Is(err, tools.ErrInvalidArguments))
	_, err = registry.Dispatch(context.Background(), llm.ToolCall{Name: "divide", Arguments: `{"by":0}`}, tools.Environment{})
	fmt.Println(errors.Is(err, tools.ErrToolPanicked))
	_, err = registry.Dispatch(context.Background(), llm.ToolCall{Name: "multiply"}, tools.Environment{})
	fmt.Println(errors.Is(err, tools.ErrUnknownTool))
```

| Error | When | Message |
|---|---|---|
| `tools.ErrUnknownTool` | no tool of that name | names the tool |
| `tools.ErrInvalidArguments` | not a JSON object, or a required field missing | names the tool and the field |
| `tools.ErrToolPanicked` | the handler panicked | names the tool and the panic value |

In an agent run all three are tool errors: the model sees them and the run continues.

## Built-in file tools

`agent.RegisterVFSTools(registry, &agent.VFSToolOptions{Storage: storage})` registers tools over any `vfs.Storage` ([Files](Files.md)). With every option at its default the registry holds, in this order: `read_file`, `list_files`, `write_file`, `delete_file`, `grep`, `glob`, `edit_file`, `multi_edit`, `move_file`, and `search` when the storage is `vfs.Searchable`.

| Tool | Parameters (required in bold) | Result and rules |
|---|---|---|
| `read_file` | **`path`**, `offset` (1-based line), `limit` (lines, default 2000), `column` (1-based byte in the `offset` line) | `content` with every line prefixed by its number and a tab, plus `content_type`, `size`, `offset`, `total_lines`, `truncated`. Stops at 2000 lines or at 40 KiB of `content` as JSON-encoded (escapes counted), so the result always fits the default 64 KiB tool result cap; continue at the next line number. A line too long for one call is cut and ends in a notice `[line N is cut here; read on with offset N and column C]`. An offset past the end, or a column past the end of its line, is an error |
| `list_files` | none | `files`: `path`, `size`, `updated_at`, sorted by path |
| `write_file` | **`path`**, **`content`**, `content_type` | creates or replaces; the content type defaults from the extension, `text/markdown` for unknown ones |
| `delete_file` | **`path`** | fails for a missing file |
| `grep` | **`pattern`** (Go RE2), `path_glob`, `case_insensitive`, `context` (0-5), `output_mode` (`content`, `files_with_matches`, `count`) | `path:line:text` matches, `path-line-text` context; at most 200 entries, lines cut at 500 characters, a scan stops before a file that would pass 32 MiB read; `truncated` says so. Binary files are skipped, and so are files listed larger than `MaximumFileBytes` (unread, `truncated` is then true). `$` anchors at the end of each line |
| `glob` | **`pattern`** (`*` in a segment, `**` across, `?`, `{a,b}`) | matching paths, sorted, at most 500 |
| `edit_file` | **`path`**, **`old_string`**, **`new_string`**, `replace_all` | exact replacement; `old_string` must be unique unless `replace_all`, and differ from `new_string`. Without the line-number prefix of `read_file` |
| `multi_edit` | **`path`**, **`edits`** (array of `old_string`, `new_string`, `replace_all`) | the edits apply in order, each on the previous result; all or nothing. Every edit needs `old_string` and `new_string` (an explicit `""` deletes, a missing or null one is an argument error); an edit that would grow the file past `MaximumFileBytes` fails before it is applied |
| `move_file` | **`source`**, **`destination`**, `overwrite` | writes the destination, then deletes the source (the Storage has no rename; a failed delete leaves both and says so). Fails on an existing destination unless `overwrite`. A rename that only changes case (`Readme.md` to `README.md`) goes through a hidden staging file in the same directory, so it is safe on case-insensitive disks (macOS, Windows); its cleanup after a failed step ignores cancellation but gives up after 30 s per step |
| `search` | **`query`** | paths matching the backend's query syntax; the description embeds `SearchSyntax()` |

Options:

| `VFSToolOptions` | Effect |
|---|---|
| `Storage` | required; multi-tenant callers pass a store already bound with `Scope` |
| `ReadOnly` | drops `write_file`, `delete_file`, `edit_file`, `multi_edit`, `move_file` |
| `DisableExtended` | drops `grep`, `glob`, `edit_file`, `multi_edit`, `move_file`: the v0.3 set |
| `HideHiddenPaths` | paths with a segment starting with `_` or `.` disappear from listings and are refused as arguments |
| `AllowedExtensions` | e.g. `[]string{".md"}`, case-insensitive; other files disappear from listings and are refused as arguments of every tool, reads included. Nil allows all |
| `MaximumFileBytes` | cap for written (and moved) files, default 1 MiB (`vfs.ErrFileTooLarge`) |

From `ExampleRegisterVFSTools`, a read-only set over a searchable store:

```go
	err := agent.RegisterVFSTools(registry, &agent.VFSToolOptions{
		Storage:  vfsmemory.New(),
		ReadOnly: true,
	})
```

registers `read_file`, `list_files`, `grep`, `glob`, `search`.

Every path argument goes through `vfs.ValidatePath` and the policy above before the storage is called. Argument errors match `tools.ErrInvalidArguments` and name the tool and the argument (`... for read_file: argument "offset" must be an integer, got string`), so the model can correct the call.

## Plan tool

`agent.RegisterPlanTool(registry)` adds `todo_write`: the model sends the whole task list (**`todos`**: array of **`content`**, **`status`** `pending`, `in_progress` or `completed`) and gets it back validated as `{"todos": [...]}`. Each call replaces the previous list. The list lives in the conversation only, so it needs no storage; show progress from `EventToolResult`. It is not registered by `RegisterVFSTools`.

## Names your code may guard

A host that filters or audits tool names must know the full default set above. To keep the v0.3 set (`read_file`, `write_file`, `list_files`, `delete_file`, `search`) set `DisableExtended: true`.
