// Package cli wires the aikido command-line tool. The exported NewApp returns
// a *cli.Command (urfave/cli v3) parameterized by a ClientFactory so tests can
// substitute a stub llm.Client for the real OpenRouter client.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/openrouter"

	urfavecli "github.com/urfave/cli/v3"
)

// ClientFactory builds the llm.Client used by every subcommand.
//
// Production wiring uses NewOpenRouterClient (reads OPENROUTER_API_KEY from
// the env after .env is loaded). Tests inject a stub.
type ClientFactory func() (llm.Client, error)

// NewApp returns the root *cli.Command tree. factory runs only when a
// subcommand needs a client, so --help and --version work without a key.
func NewApp(factory ClientFactory) *urfavecli.Command {
	return &urfavecli.Command{
		Name:                  "aikido",
		Usage:                 "AI KIt DOing things - chat one-shots and agent runs over an in-memory VFS",
		Version:               version(),
		EnableShellCompletion: true,
		Description: "aikido is a thin CLI over the aikido Go library. Subcommands run\n" +
			"completions or agent loops against OpenRouter; the API key is read from\n" +
			"OPENROUTER_API_KEY (a .env in the working directory is loaded as a\n" +
			"fallback before the env is consulted). OPENROUTER_BASE_URL replaces the\n" +
			"API root https://openrouter.ai/api/v1, e.g. for a proxy or a test server.",
		Commands: []*urfavecli.Command{
			chatCommand(factory),
			agentCommand(factory),
			imageCommand(factory),
			videoCommand(factory),
			decideCommand(factory),
			modelsCommand(factory),
			skillCommand(),
		},
	}
}

// NewOpenRouterClient is the default ClientFactory: it reads the API key from
// the environment and constructs an openrouter.Client. It returns an error if
// OPENROUTER_API_KEY is unset. OPENROUTER_BASE_URL, when set, replaces
// openrouter.DefaultBaseURL: a proxy, or the end-to-end suite's fake server.
func NewOpenRouterClient() (llm.Client, error) {
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		return nil, errors.New("OPENROUTER_API_KEY is not set (export it or place it in .env)")
	}
	client, err := openrouter.NewClient(&openrouter.Options{
		APIKey:      apiKey,
		BaseURL:     os.Getenv("OPENROUTER_BASE_URL"),
		HTTPReferer: "https://github.com/govengers/aikido",
		XTitle:      "aikido CLI",
	})
	if err != nil {
		return nil, fmt.Errorf("openrouter.NewClient: %w", err)
	}
	return client, nil
}

// resolveWriter returns the writer the user configured on the root command.
//
// urfave/cli v3 sets Writer to os.Stdout by default on every subcommand, so we
// cannot distinguish "user-set" from "library default" there. Root() is where
// any caller-supplied Writer actually lives, so it wins.
func resolveWriter(command *urfavecli.Command) io.Writer {
	if command != nil {
		if root := command.Root(); root != nil && root.Writer != nil {
			return root.Writer
		}
		if command.Writer != nil {
			return command.Writer
		}
	}
	return os.Stdout
}

// resolveErrorWriter is resolveWriter for diagnostics: the root's ErrWriter,
// else os.Stderr.
func resolveErrorWriter(command *urfavecli.Command) io.Writer {
	if command != nil {
		if root := command.Root(); root != nil && root.ErrWriter != nil {
			return root.ErrWriter
		}
	}
	return os.Stderr
}

// resolveReader returns the root command's Reader, else os.Stdin.
func resolveReader(command *urfavecli.Command) io.Reader {
	if command != nil {
		if root := command.Root(); root != nil && root.Reader != nil {
			return root.Reader
		}
	}
	return os.Stdin
}

// promptFromArguments joins the positional arguments into one prompt.
func promptFromArguments(command *urfavecli.Command) string {
	return strings.TrimSpace(strings.Join(command.Args().Slice(), " "))
}

// version is the CLI's reported version, read from the build info the Go
// toolchain embeds: `go install github.com/govengers/aikido/cmd/aikido@v0.6.1`
// reports v0.6.1, a checkout build reports its VCS pseudo-version or revision.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "(devel)"
	}
	return versionFromBuildInfo(info)
}

// shortRevisionLength matches the revision prefix in Go pseudo-versions.
const shortRevisionLength = 12

func versionFromBuildInfo(info *debug.BuildInfo) string {
	if moduleVersion := info.Main.Version; moduleVersion != "" && moduleVersion != "(devel)" {
		return moduleVersion
	}
	var revision, modified string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value
		}
	}
	if revision == "" {
		return "(devel)"
	}
	if len(revision) > shortRevisionLength {
		revision = revision[:shortRevisionLength]
	}
	if modified == "true" {
		revision += "+dirty"
	}
	return "(devel) " + revision
}
