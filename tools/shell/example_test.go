package shell_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/tools"
	"github.com/govengers/aikido/tools/shell"
	"github.com/govengers/aikido/vfs/memory"
)

// A script runs over the files of a Storage; the working directory persists
// between runs.
func ExampleShell_Run() {
	storage := memory.New()
	_ = storage.WriteFile(context.Background(), "notes/todo.md", []byte("- ship\n- test\n"), "text/markdown")
	bash, err := shell.New(&shell.Options{Storage: storage})
	if err != nil {
		fmt.Println(err)
		return
	}
	result, _ := bash.Run(context.Background(), "cd notes && cat -n todo.md && echo done > status.txt; nosuch")
	fmt.Print(result.Output)
	fmt.Println("exit", result.ExitCode)
	result, _ = bash.Run(context.Background(), "pwd; ls")
	fmt.Print(result.Output)
	// Output:
	//      1	- ship
	//      2	- test
	// bash: line 1: nosuch: command not found
	// exit 127
	// /notes
	// status.txt
	// todo.md
}

// Register adds the shell as one tool; the model gets the output, the exit
// code and whether the output was cut.
func ExampleRegister() {
	registry := tools.NewRegistry()
	if err := shell.Register(registry, &shell.Options{Storage: memory.New()}); err != nil {
		fmt.Println(err)
		return
	}
	result, err := registry.Dispatch(context.Background(), llm.ToolCall{
		Name:      "bash",
		Arguments: `{"command": "mkdir -p src && echo 'package main' > src/main.go && find . -name '*.go'"}`,
	}, tools.Environment{})
	if err != nil {
		fmt.Println(err)
		return
	}
	encoded, _ := json.Marshal(result.Content)
	fmt.Println(string(encoded))
	// Output:
	// {"output":"./src/main.go\n","exit_code":0,"truncated":false}
}

// External registers a host program next to the built-in commands. Allow
// refuses the runs the caller does not want; here git may read but not push.
func ExampleExternal() {
	git, err := shell.External(&shell.ExternalOptions{
		Name:        "git",
		Path:        "/usr/bin/git",
		Directory:   "/srv/checkout",
		Environment: []string{"HOME=/srv/checkout", "GIT_CONFIG_NOSYSTEM=1"},
		Allow: func(arguments []string) error {
			if slices.Contains(arguments, "push") {
				return errors.New("push is not allowed")
			}
			return nil
		},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	_, _ = shell.New(&shell.Options{Storage: memory.New(), Commands: []shell.Command{git}})
}

// Builtins lists the commands every Shell has, for documentation.
func ExampleBuiltins() {
	for _, command := range shell.Builtins() {
		if command.Name == "cat" || command.Name == "mkdir" {
			fmt.Printf("%s: %s\n", command.Name, command.Summary)
		}
	}
	// Output:
	// cat: print files (-n numbers lines; - is stdin)
	// mkdir: create directories (-p parents, -v)
}
