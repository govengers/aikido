package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// gitPolicy is the Allow function of an allowed git. Git runs commands it
// reads from its configuration and options (core.sshCommand, diff.external,
// core.fsmonitor, --upload-pack), so options that set configuration or name
// programs are refused, and -C, --git-dir and --work-tree must stay inside
// directory. With --hide-hidden the shell cannot write .git/config or hooks
// either; a repository's own configuration is trusted.
func gitPolicy(directory string) func(arguments []string) error {
	return func(arguments []string) error {
		index, err := checkGitGlobalOptions(directory, arguments)
		if err != nil {
			return err
		}
		return checkGitCommandOptions(arguments[index:])
	}
}

// checkGitGlobalOptions checks the options before the git subcommand and
// returns the index of the subcommand.
func checkGitGlobalOptions(directory string, arguments []string) (int, error) {
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		name, value, hasValue := strings.Cut(argument, "=")
		switch {
		case !strings.HasPrefix(argument, "-"):
			return index, nil
		case strings.HasPrefix(argument, "-c"), name == "--config-env", name == "--exec-path":
			return 0, fmt.Errorf("%s is not allowed: it can make git run other programs", name)
		case argument == "-C", name == "--git-dir", name == "--work-tree":
			if !hasValue {
				if index+1 >= len(arguments) {
					return index, nil // git reports the missing value
				}
				index++
				value = arguments[index]
			}
			if err := checkInside(directory, value); err != nil {
				return 0, fmt.Errorf("%s %s: %w", name, value, err)
			}
		}
	}
	return len(arguments), nil
}

// gitProgramOptions name programs or configuration in subcommands (fetch,
// clone, push, ls-remote, archive).
var gitProgramOptions = []string{"--upload-pack", "--receive-pack", "--exec", "--config", "--template"}

func checkGitCommandOptions(arguments []string) error {
	if len(arguments) == 0 {
		return nil
	}
	for _, argument := range arguments[1:] {
		name, _, _ := strings.Cut(argument, "=")
		if slices.Contains(gitProgramOptions, name) || arguments[0] == "clone" && (strings.HasPrefix(argument, "-u") || strings.HasPrefix(argument, "-c")) {
			return fmt.Errorf("git %s %s is not allowed: it can make git run other programs", arguments[0], name)
		}
		if argument == "--" {
			return nil
		}
	}
	return nil
}

// checkInside accepts an absolute path inside directory, or a relative path
// that does not climb with "..".
func checkInside(directory, path string) error {
	if !filepath.IsAbs(path) {
		if slices.Contains(strings.Split(filepath.ToSlash(path), "/"), "..") {
			return errors.New("must not climb out of the working directory")
		}
		return nil
	}
	relative, err := filepath.Rel(directory, filepath.Clean(path))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("lies outside %s", directory)
	}
	return nil
}
