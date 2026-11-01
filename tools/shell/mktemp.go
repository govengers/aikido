package shell

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
)

func mktempCommand() Command {
	return Command{Name: "mktemp", Summary: "create a temporary file or directory in /tmp and print its name (-d -u -q -p DIR -t --suffix=SUFF [TEMPLATE])", Run: runMktemp}
}

const mktempAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// mktempSettings is a parsed mktemp command line.
type mktempSettings struct {
	directory, dryRun, quiet bool
	template, parent, suffix string
	inTemporary              bool // -p, -t, --tmpdir or no template
}

func runMktemp(ctx context.Context, invocation *Invocation) error {
	settings, err := parseMktemp(invocation)
	if err != nil {
		return usage(invocation, "mktemp", err, errFailed)
	}
	name, err := settings.name()
	if err != nil {
		return settings.fail(invocation, err)
	}
	files := workspaceOf(invocation)
	for attempt := 0; ; attempt++ {
		candidate, err := fillTemplate(name)
		if err != nil {
			return err
		}
		created, err := settings.create(ctx, files, invocation.Directory, candidate)
		if errors.Is(err, errExists) && attempt < 100 {
			continue
		}
		if err != nil {
			return settings.fail(invocation, fmt.Errorf("failed to create %s via template '%s': %w", settings.kind(), name, err))
		}
		if created {
			fmt.Fprintln(invocation.Stdout, candidate)
		}
		return nil
	}
}

func parseMktemp(invocation *Invocation) (mktempSettings, error) {
	parsed, operands, err := optionSet{flags: "dqtu", valued: "p", long: map[string]longOption{
		"directory": {name: "d"}, "quiet": {name: "q"}, "dry-run": {name: "u"}, "tmpdir": {name: "tmpdir", valued: true},
		"suffix": {name: "suffix", valued: true},
	}}.parse(tmpdirWithoutValue(invocation.Arguments))
	if err != nil {
		return mktempSettings{}, err
	}
	settings := mktempSettings{parent: temporaryDirectory(invocation.Environment)}
	for _, option := range parsed {
		switch option.name {
		case "d":
			settings.directory = true
		case "q":
			settings.quiet = true
		case "u":
			settings.dryRun = true
		case "t":
			settings.inTemporary = true
		case "p", "tmpdir":
			settings.inTemporary = true
			if option.value != "" {
				settings.parent = option.value
			}
		case "suffix":
			settings.suffix = option.value
		}
	}
	switch len(operands) {
	case 0:
		settings.template, settings.inTemporary = "tmp.XXXXXXXXXX", true
	case 1:
		settings.template = operands[0]
	default:
		return settings, fmt.Errorf("too many templates")
	}
	return settings, nil
}

// tmpdirWithoutValue gives a bare --tmpdir an empty value, so the option
// parser (which needs a value for valued options) accepts it like GNU.
func tmpdirWithoutValue(arguments []string) []string {
	rewritten := make([]string, len(arguments))
	for index, argument := range arguments {
		if argument == "--tmpdir" {
			argument = "--tmpdir="
		}
		rewritten[index] = argument
	}
	return rewritten
}

// temporaryDirectory is $TMPDIR, or /tmp.
func temporaryDirectory(environment []string) string {
	for _, variable := range environment {
		if value, ok := strings.CutPrefix(variable, "TMPDIR="); ok && value != "" {
			return value
		}
	}
	return "/tmp"
}

// name is the path template with the suffix, checked like GNU.
func (s mktempSettings) name() (string, error) {
	template := s.template + s.suffix
	base := strings.TrimSuffix(s.template, "/")
	if count := len(base) - len(strings.TrimRight(base, "X")); count < 3 {
		return "", fmt.Errorf("too few X's in template '%s'", template)
	}
	if !s.inTemporary {
		return template, nil
	}
	if strings.Contains(s.template, "/") {
		return "", fmt.Errorf("invalid template, '%s', contains directory separator", s.template)
	}
	return strings.TrimSuffix(s.parent, "/") + "/" + template, nil
}

func (s mktempSettings) kind() string {
	if s.directory {
		return "directory"
	}
	return "file"
}

// create makes the file or directory unless -u; it reports errExists when
// the name is taken.
func (s mktempSettings) create(ctx context.Context, files workspace, directory, name string) (bool, error) {
	resolved, err := Resolve(directory, name)
	if err != nil {
		return false, errNotFound
	}
	found, err := files.stat(ctx, resolved)
	switch {
	case err != nil:
		return false, err
	case found.kind != entryMissing:
		return false, errExists
	case s.dryRun:
		return true, nil
	case s.directory:
		_, err = makeDirectory(ctx, files, directory, name, false)
		return err == nil, err
	}
	return true, files.writeFile(ctx, resolved, nil)
}

func (s mktempSettings) fail(invocation *Invocation, err error) error {
	if !s.quiet {
		complain(invocation, "mktemp", "%v", err)
	}
	return errFailed
}

// fillTemplate replaces the trailing X's of the last path segment (before
// the suffix) with random letters and digits.
func fillTemplate(name string) (string, error) {
	end := strings.LastIndex(name, "XXX") + 3
	start := end
	for start > 0 && name[start-1] == 'X' {
		start--
	}
	random := make([]byte, end-start)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("random name: %w", err)
	}
	for index, value := range random {
		random[index] = mktempAlphabet[int(value)%len(mktempAlphabet)]
	}
	return name[:start] + string(random) + name[end:], nil
}
