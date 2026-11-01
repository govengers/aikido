package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/govengers/aikido/tools"
	"github.com/govengers/aikido/tools/shell"
	"github.com/govengers/aikido/vfs"
)

// workerOptions are the flags that turn `aikido agent` into an unattended
// worker: a virtual bash, host programs it may call, and the variables those
// programs get.
type workerOptions struct {
	shell       bool
	direct      bool
	allowed     []string // name or name=path
	environment []string // NAME (copied from the host) or NAME=value
}

// validateWorkerOptions rejects flag combinations that would give the agent
// two views of one directory or nothing to work on.
func validateWorkerOptions(options agentOptions) error {
	worker := options.worker
	switch {
	case worker.direct && options.directory == "":
		return errors.New("--direct needs --dir")
	case worker.direct && options.apply:
		return errors.New("--direct writes every change as it happens; --apply has nothing left to write")
	case len(worker.allowed) > 0 && !worker.shell:
		return errors.New("--allow needs --shell: host programs run from the agent's bash tool")
	case len(worker.allowed) > 0 && !worker.direct:
		return errors.New("--allow needs --direct: host programs see the directory on disk, not the overlay")
	case len(worker.allowed) > 0 && len(options.mounts) > 0:
		return errors.New("--allow cannot be combined with --mount: host programs see --dir on disk, where the mounts do not exist")
	case len(worker.environment) > 0 && len(worker.allowed) == 0:
		return errors.New("--env needs --allow: the variables are passed to the allowed host programs only")
	default:
		return nil
	}
}

// registerShell adds the virtual bash with the allowed host programs. They run
// in the --dir directory with only PATH and the --env variables.
func registerShell(registry *tools.Registry, options agentOptions, storage vfs.Storage) error {
	if !options.worker.shell {
		return nil
	}
	commands, err := hostCommands(options)
	if err != nil {
		return err
	}
	shellOptions := &shell.Options{Storage: storage, Commands: commands, HideHiddenPaths: options.hideHidden}
	if options.worker.direct {
		// The workspace is the directory on disk: the shell shows it at its
		// host path, so pwd and the paths a host program gets agree.
		directory, err := filepath.Abs(options.directory)
		if err != nil {
			return fmt.Errorf("--dir: %w", err)
		}
		shellOptions.MountPoint = filepath.ToSlash(directory)
	}
	if err := shell.Register(registry, shellOptions); err != nil {
		return fmt.Errorf("--shell: %w", err)
	}
	return nil
}

func hostCommands(options agentOptions) ([]shell.Command, error) {
	if len(options.worker.allowed) == 0 {
		return nil, nil
	}
	directory, err := filepath.Abs(options.directory)
	if err != nil {
		return nil, fmt.Errorf("--dir: %w", err)
	}
	environment, err := hostEnvironment(options.worker.environment)
	if err != nil {
		return nil, err
	}
	commands := make([]shell.Command, 0, len(options.worker.allowed))
	for _, allowed := range options.worker.allowed {
		name, path, err := resolveAllowed(allowed)
		if err != nil {
			return nil, err
		}
		external := &shell.ExternalOptions{
			Name:                   name,
			Path:                   path,
			Directory:              directory,
			FollowWorkingDirectory: true,
			Environment:            environment,
			Summary:                "host program " + name,
		}
		if name == "git" || filepath.Base(path) == "git" {
			external.Allow = gitPolicy(directory)
		}
		command, err := shell.External(external)
		if err != nil {
			return nil, fmt.Errorf("--allow %s: %w", allowed, err)
		}
		commands = append(commands, command)
	}
	return commands, nil
}

// resolveAllowed reads "name" (looked up in the host PATH) or "name=path".
func resolveAllowed(allowed string) (name, path string, err error) {
	name, path, explicit := strings.Cut(allowed, "=")
	if name == "" {
		return "", "", fmt.Errorf("--allow %q: want name or name=path", allowed)
	}
	if !explicit {
		path, err = exec.LookPath(name)
		if err != nil {
			return "", "", fmt.Errorf("--allow %s: %w", name, err)
		}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", "", fmt.Errorf("--allow %s: %w", name, err)
	}
	return name, absolute, nil
}

// hostEnvironment is PATH plus every --env entry: NAME copies the host value
// (an unset NAME is an error), NAME=value sets it.
func hostEnvironment(entries []string) ([]string, error) {
	environment := []string{"PATH=" + os.Getenv("PATH")}
	for _, entry := range entries {
		if strings.Contains(entry, "=") {
			environment = append(environment, entry)
			continue
		}
		value, set := os.LookupEnv(entry)
		if !set {
			return nil, fmt.Errorf("--env %s: not set in this environment", entry)
		}
		environment = append(environment, entry+"="+value)
	}
	return environment, nil
}

// systemPrompt is --system, followed by the content of --system-file when
// given.
func systemPrompt(system, systemFile string) (string, error) {
	if systemFile == "" {
		return system, nil
	}
	content, err := os.ReadFile(systemFile) // #nosec G304 -- the user names the file on the command line
	if err != nil {
		return "", fmt.Errorf("--system-file: %w", err)
	}
	if system == "" {
		return string(content), nil
	}
	return system + "\n\n" + string(content), nil
}
