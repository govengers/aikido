package agent

import (
	"errors"
	"testing"

	"github.com/govengers/aikido/tools"
	vfsmem "github.com/govengers/aikido/vfs/memory"
)

func TestDecodeArguments_NamesToolAndArgument(t *testing.T) {
	registry := newRegistryWithVFS(t, &VFSToolOptions{Storage: vfsmem.NewStorage()})

	testCases := []struct {
		name, tool, arguments, want string
	}{
		{"string expected", "read_file", `{"path":7}`, `aikido/tools: invalid tool arguments for read_file: argument "path" must be a string, got number`},
		{"integer expected", "read_file", `{"path":"a.md","limit":"ten"}`, `aikido/tools: invalid tool arguments for read_file: argument "limit" must be an integer, got string`},
		{"boolean expected", "edit_file", `{"path":"a.md","old_string":"a","new_string":"b","replace_all":"yes"}`, `aikido/tools: invalid tool arguments for edit_file: argument "replace_all" must be a boolean, got string`},
		{"missing required", "write_file", `{"path":"a.md"}`, `aikido/tools: invalid tool arguments for write_file: missing required argument "content"`},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := dispatch(t, registry, testCase.tool, testCase.arguments)
			if !errors.Is(err, tools.ErrInvalidArguments) || err.Error() != testCase.want {
				t.Errorf("err = %v; want ErrInvalidArguments %q", err, testCase.want)
			}
		})
	}
}

func TestDecodeArguments_OtherDecodeErrors(t *testing.T) {
	var target struct{ Path string }
	err := decodeArguments("read_file", []byte(`{"path":`), &target)
	if !errors.Is(err, tools.ErrInvalidArguments) {
		t.Errorf("err = %v; want ErrInvalidArguments", err)
	}
}

func TestJSONKind(t *testing.T) {
	testCases := []struct {
		name   string
		target any
		want   string
	}{
		{"float", new(struct{ X float64 }), "a number"},
		{"slice", new(struct{ X []string }), "an array"},
		{"object", new(struct{ X struct{ Y int } }), "an object"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := decodeArguments("tool", []byte(`{"X":true}`), testCase.target)
			want := `aikido/tools: invalid tool arguments for tool: argument "X" must be ` + testCase.want + `, got bool`
			if err == nil || err.Error() != want {
				t.Errorf("err = %v; want %q", err, want)
			}
		})
	}
}
