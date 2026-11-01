package cli

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	urfavecli "github.com/urfave/cli/v3"
)

// skillName is both the installed skill's directory name and its /slash
// invocation in Claude Code.
const skillName = "aikido"

// skillMarkdown is the SKILL.md shipped with the binary. It teaches a Claude
// Code agent to drive this CLI, chiefly for high-quality image generation.
//
//go:embed skills/SKILL.md
var skillMarkdown string

func skillCommand() *urfavecli.Command {
	return &urfavecli.Command{
		Name:  "skill",
		Usage: "Install the aikido Claude Code skill",
		Description: "Writes the bundled SKILL.md into a Claude Code skills directory so\n" +
			"Claude learns to use this CLI, especially for generating high-quality images.",
		Commands: []*urfavecli.Command{
			skillInstallCommand(),
		},
	}
}

func skillInstallCommand() *urfavecli.Command {
	return &urfavecli.Command{
		Name:  "install",
		Usage: "Install the aikido skill into a Claude Code skills directory",
		Flags: []urfavecli.Flag{
			&urfavecli.StringFlag{
				Name:  "scope",
				Value: "user",
				Usage: "Install scope: 'user' (~/.claude/skills) or 'project' (./.claude/skills)",
			},
			&urfavecli.StringFlag{
				Name:  "dir",
				Usage: "Skills root directory (overrides --scope)",
			},
			&urfavecli.BoolFlag{
				Name:    "force",
				Aliases: []string{"f"},
				Usage:   "Overwrite an existing SKILL.md",
			},
			&urfavecli.BoolFlag{
				Name:  "print",
				Usage: "Print the skill markdown to stdout instead of installing",
			},
		},
		Action: func(_ context.Context, command *urfavecli.Command) error {
			return runSkillInstall(skillInstallOptions{
				scope:     command.String("scope"),
				directory: command.String("dir"),
				force:     command.Bool("force"),
				print:     command.Bool("print"),
			}, resolveWriter(command))
		},
	}
}

type skillInstallOptions struct {
	scope     string
	directory string
	force     bool
	print     bool
}

func runSkillInstall(options skillInstallOptions, out io.Writer) error {
	if options.print {
		_, err := io.WriteString(out, skillMarkdown)
		return err
	}
	root, err := skillsRoot(options.scope, options.directory)
	if err != nil {
		return err
	}
	skillDirectory := filepath.Join(root, skillName)
	target := filepath.Join(skillDirectory, "SKILL.md")
	if !options.force {
		if err := refuseExisting(target); err != nil {
			return err
		}
	}
	//nolint:gosec // G301: a config directory holding no secrets; umask applies
	if err := os.MkdirAll(skillDirectory, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", skillDirectory, err)
	}
	//nolint:gosec // G306: the published skill text, no secret; umask applies
	if err := os.WriteFile(target, []byte(skillMarkdown), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	fmt.Fprintf(out, "installed aikido skill → %s\n", target)
	fmt.Fprintf(out, "invoke it in Claude Code with /%s, or it triggers automatically on image-generation requests\n", skillName)
	return nil
}

// refuseExisting returns an error if target exists or cannot be checked.
func refuseExisting(target string) error {
	_, err := os.Stat(target)
	switch {
	case err == nil:
		return fmt.Errorf("%s already exists; pass --force to overwrite", target)
	case errors.Is(err, os.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("stat %s: %w", target, err)
	}
}

// skillsRoot resolves the Claude Code skills directory. An explicit directory
// wins; otherwise scope selects the user-global or project-local convention.
func skillsRoot(scope, directory string) (string, error) {
	if directory != "" {
		return directory, nil
	}
	switch scope {
	case "", "user":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		return filepath.Join(home, ".claude", "skills"), nil
	case "project":
		return filepath.Join(".claude", "skills"), nil
	default:
		return "", fmt.Errorf("invalid --scope %q (want 'user' or 'project')", scope)
	}
}
