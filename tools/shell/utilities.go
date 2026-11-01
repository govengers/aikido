package shell

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
)

func basenameCommand() Command {
	return Command{Name: "basename", Summary: "strip directory and suffix from names (-a, -s suffix)", Run: runBasename}
}

func runBasename(_ context.Context, invocation *Invocation) error {
	parsed, operands, err := optionSet{flags: "a", valued: "s", long: map[string]longOption{
		"multiple": {name: "a"}, "suffix": {name: "s", valued: true},
	}}.parse(invocation.Arguments)
	if err != nil {
		return usage(invocation, "basename", err, errFailed)
	}
	if len(operands) == 0 {
		return usage(invocation, "basename", errors.New("missing operand"), errFailed)
	}
	suffix := ""
	multiple := parsed.has("a")
	for _, given := range parsed {
		if given.name == "s" {
			suffix, multiple = given.value, true
		}
	}
	if !multiple {
		switch len(operands) {
		case 1:
		case 2:
			suffix, operands = operands[1], operands[:1]
		default:
			return usage(invocation, "basename", fmt.Errorf("extra operand '%s'", operands[2]), errFailed)
		}
	}
	for _, operand := range operands {
		fmt.Fprintln(invocation.Stdout, basename(operand, suffix))
	}
	return nil
}

// basename is GNU basename: the last segment, trailing slashes ignored,
// suffix removed unless it is the whole segment.
func basename(name, suffix string) string {
	trimmed := strings.TrimRight(name, "/")
	if trimmed == "" {
		if name == "" {
			return ""
		}
		return "/"
	}
	base := trimmed[strings.LastIndex(trimmed, "/")+1:]
	if suffix != "" && base != suffix {
		base = strings.TrimSuffix(base, suffix)
	}
	return base
}

func dirnameCommand() Command {
	return Command{Name: "dirname", Summary: "strip the last segment from names", Run: runDirname}
}

func runDirname(_ context.Context, invocation *Invocation) error {
	_, operands, err := optionSet{}.parse(invocation.Arguments)
	if err != nil {
		return usage(invocation, "dirname", err, errFailed)
	}
	if len(operands) == 0 {
		return usage(invocation, "dirname", errors.New("missing operand"), errFailed)
	}
	for _, operand := range operands {
		fmt.Fprintln(invocation.Stdout, dirname(operand))
	}
	return nil
}

// dirname is GNU dirname: everything before the last segment, "." without
// a slash, "/" at the root.
func dirname(name string) string {
	trimmed := strings.TrimRight(name, "/")
	if trimmed == "" {
		if strings.HasPrefix(name, "/") {
			return "/"
		}
		return "."
	}
	slash := strings.LastIndex(trimmed, "/")
	if slash < 0 {
		return "."
	}
	if parent := strings.TrimRight(trimmed[:slash], "/"); parent != "" {
		return parent
	}
	return "/"
}

func realpathCommand() Command {
	return Command{
		Name:    "realpath",
		Summary: "print the absolute workspace path, / being the root (-e all exist, -m none need to, -s, --relative-to=dir)",
		Run:     runRealpath,
	}
}

func runRealpath(ctx context.Context, invocation *Invocation) error {
	parsed, operands, err := optionSet{flags: "ems", long: map[string]longOption{
		"canonicalize-existing": {name: "e"}, "canonicalize-missing": {name: "m"}, "no-symlinks": {name: "s"},
		"strip": {name: "s"}, "relative-to": {name: "relative-to", valued: true},
	}}.parse(invocation.Arguments)
	if err != nil {
		return usage(invocation, "realpath", err, errFailed)
	}
	if len(operands) == 0 {
		return usage(invocation, "realpath", errors.New("missing operand"), errFailed)
	}
	base := ""
	for _, given := range parsed {
		if given.name == "relative-to" {
			if base, err = resolve(invocation, given.value); err != nil {
				complain(invocation, "realpath", "%s: %v", given.value, err)
				return errFailed
			}
		}
	}
	files := workspaceOf(invocation)
	var status error
	for _, operand := range operands {
		name, err := resolve(invocation, operand)
		if err == nil {
			err = checkRealpath(ctx, files, name, parsed.has("e"), parsed.has("m"))
		}
		if err != nil {
			complain(invocation, "realpath", "%s: %v", operand, err)
			status = errFailed
			continue
		}
		if parsed.has("relative-to") {
			fmt.Fprintln(invocation.Stdout, relativePath(base, name))
		} else {
			fmt.Fprintln(invocation.Stdout, "/"+name)
		}
	}
	return status
}

// checkRealpath applies realpath's existence rule: by default every segment
// but the last must exist, with -e all, with -m none.
func checkRealpath(ctx context.Context, files workspace, name string, existing, missing bool) error {
	if missing {
		return nil
	}
	if !existing {
		name = parentOf(name)
	}
	found, err := files.stat(ctx, name)
	switch {
	case err != nil:
		return err
	case found.kind == entryMissing:
		return errNotFound
	case !existing && found.kind != entryDirectory:
		return errNotDirectory
	}
	return nil
}

// relativePath is the path from directory base to target, both workspace
// paths.
func relativePath(base, target string) string {
	split := func(name string) []string {
		if name == "" {
			return nil
		}
		return strings.Split(name, "/")
	}
	baseSegments, targetSegments := split(base), split(target)
	common := 0
	for common < len(baseSegments) && common < len(targetSegments) && baseSegments[common] == targetSegments[common] {
		common++
	}
	segments := slices.Repeat([]string{".."}, len(baseSegments)-common)
	segments = append(segments, targetSegments[common:]...)
	if len(segments) == 0 {
		return "."
	}
	return strings.Join(segments, "/")
}

func envCommand() Command {
	return Command{
		Name:    "env",
		Summary: "print the environment, or run a command with changes (-i, -u name, NAME=value)",
		Run:     runEnv,
	}
}

func runEnv(ctx context.Context, invocation *Invocation) error {
	parsed, operands, err := optionSet{flags: "i", valued: "u", inOrder: true, long: map[string]longOption{
		"ignore-environment": {name: "i"}, "unset": {name: "u", valued: true},
	}}.parse(invocation.Arguments)
	if err != nil {
		return usage(invocation, "env", err, ExitError(statusInternalError))
	}
	environment := slices.Clone(invocation.Environment)
	if parsed.has("i") || (len(operands) > 0 && operands[0] == "-") {
		environment = nil
		if len(operands) > 0 && operands[0] == "-" {
			operands = operands[1:]
		}
	}
	for _, given := range parsed {
		if given.name == "u" {
			environment = withoutVariable(environment, given.value)
		}
	}
	for len(operands) > 0 && strings.Contains(operands[0], "=") {
		name, _, _ := strings.Cut(operands[0], "=")
		environment = append(withoutVariable(environment, name), operands[0])
		operands = operands[1:]
	}
	if len(operands) == 0 {
		slices.Sort(environment)
		for _, variable := range environment {
			fmt.Fprintln(invocation.Stdout, variable)
		}
		return nil
	}
	changed := *invocation
	changed.Environment = environment
	if status := runSubcommand(ctx, &changed, "env", operands, invocation.Stdin); status != 0 {
		return ExitError(status)
	}
	return nil
}

func withoutVariable(environment []string, name string) []string {
	return slices.DeleteFunc(environment, func(variable string) bool { return strings.HasPrefix(variable, name+"=") })
}

func whichCommand() Command {
	return Command{Name: "which", Summary: "print the names of the commands that exist (-a)", Run: runWhich}
}

func runWhich(_ context.Context, invocation *Invocation) error {
	_, operands, err := optionSet{flags: "a"}.parse(invocation.Arguments)
	if err != nil {
		return usage(invocation, "which", err, ExitError(statusInternalError))
	}
	var status error
	for _, name := range operands {
		if invocation.session != nil && invocation.session.known(name) {
			fmt.Fprintln(invocation.Stdout, name)
			continue
		}
		status = errFailed
	}
	return status
}

func seqCommand() Command {
	return Command{Name: "seq", Summary: "print integer sequences: seq [-s sep] [-w] [first [step]] last", Run: runSeq}
}

// sequence is a parsed seq invocation.
type sequence struct {
	first, step, last int64
	separator         string
	equalWidth        bool
}

func runSeq(ctx context.Context, invocation *Invocation) error {
	parsed, err := parseSeq(invocation.Arguments)
	if err != nil {
		return usage(invocation, "seq", err, errFailed)
	}
	width := 0
	if parsed.equalWidth {
		width = max(len(strconv.FormatInt(parsed.first, 10)), len(strconv.FormatInt(parsed.last, 10)))
	}
	written := false
	for value, count := parsed.first, 0; (parsed.step > 0 && value <= parsed.last) || (parsed.step < 0 && value >= parsed.last); value, count = value+parsed.step, count+1 {
		if count%1024 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		prefix := parsed.separator
		if !written {
			prefix = ""
		}
		if _, err := fmt.Fprintf(invocation.Stdout, "%s%0*d", prefix, width, value); err != nil {
			return err
		}
		written = true
	}
	if written {
		fmt.Fprintln(invocation.Stdout)
	}
	return nil
}

// parseSeq reads seq's arguments; negative numbers are operands, not
// options.
func parseSeq(arguments []string) (sequence, error) {
	parsed := sequence{first: 1, step: 1, separator: "\n"}
	numbers := []string{}
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		_, numberErr := strconv.ParseFloat(argument, 64)
		switch {
		case numberErr == nil:
			numbers = append(numbers, argument)
		case argument == "-w" || argument == "--equal-width":
			parsed.equalWidth = true
		case argument == "-s" && index+1 < len(arguments):
			parsed.separator = arguments[index+1]
			index++
		case strings.HasPrefix(argument, "--separator="):
			parsed.separator = strings.TrimPrefix(argument, "--separator=")
		case strings.HasPrefix(argument, "-s") && len(argument) > 2:
			parsed.separator = argument[2:]
		case strings.HasPrefix(argument, "-") && argument != "-":
			return parsed, &usageError{message: fmt.Sprintf("invalid option -- '%s'", strings.TrimLeft(argument, "-")[:1])}
		default:
			return parsed, fmt.Errorf("invalid floating point argument: '%s'", argument)
		}
	}
	return parsed.withNumbers(numbers)
}

func (s sequence) withNumbers(numbers []string) (sequence, error) {
	values := make([]int64, len(numbers))
	for index, number := range numbers {
		value, err := strconv.ParseInt(number, 10, 64)
		if err != nil {
			return s, fmt.Errorf("only integer arguments are supported here: '%s'", number)
		}
		values[index] = value
	}
	switch len(values) {
	case 0:
		return s, errors.New("missing operand")
	case 1:
		s.last = values[0]
	case 2:
		s.first, s.last = values[0], values[1]
	case 3:
		s.first, s.step, s.last = values[0], values[1], values[2]
	default:
		return s, fmt.Errorf("extra operand '%s'", numbers[3])
	}
	if s.step == 0 {
		return s, fmt.Errorf("invalid Zero increment value: '%s'", numbers[1])
	}
	return s, nil
}

func sleepCommand() Command {
	return Command{Name: "sleep", Summary: "wait N seconds (suffixes s m h d, decimals)", Run: runSleep}
}

func runSleep(ctx context.Context, invocation *Invocation) error {
	if len(invocation.Arguments) == 0 {
		return usage(invocation, "sleep", errors.New("missing operand"), errFailed)
	}
	total := time.Duration(0)
	for _, argument := range invocation.Arguments {
		duration, err := parseSleep(argument)
		if err != nil {
			return usage(invocation, "sleep", fmt.Errorf("invalid time interval '%s'", argument), errFailed)
		}
		total += duration
	}
	timer := time.NewTimer(total)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// parseSleep reads one sleep operand: a non-negative number with an
// optional unit s, m, h or d.
func parseSleep(argument string) (time.Duration, error) {
	unit, number := time.Second, argument
	if last := len(argument) - 1; last > 0 {
		switch argument[last] {
		case 's':
			number = argument[:last]
		case 'm':
			unit, number = time.Minute, argument[:last]
		case 'h':
			unit, number = time.Hour, argument[:last]
		case 'd':
			unit, number = 24*time.Hour, argument[:last]
		}
	}
	value, err := strconv.ParseFloat(number, 64)
	if err != nil || value < 0 {
		return 0, errors.New("invalid time interval")
	}
	return time.Duration(value * float64(unit)), nil
}

func base64Command() Command {
	return Command{Name: "base64", Summary: "encode or decode base64 (-d, -w cols, -i)", Run: runBase64}
}

func runBase64(ctx context.Context, invocation *Invocation) error {
	parsed, operands, err := optionSet{flags: "di", valued: "w", long: map[string]longOption{
		"decode": {name: "d"}, "ignore-garbage": {name: "i"}, "wrap": {name: "w", valued: true},
	}}.parse(invocation.Arguments)
	if err != nil {
		return usage(invocation, "base64", err, errFailed)
	}
	if len(operands) > 1 {
		return usage(invocation, "base64", fmt.Errorf("extra operand '%s'", operands[1]), errFailed)
	}
	wrap := 76
	for _, given := range parsed {
		if given.name == "w" {
			if wrap, err = strconv.Atoi(given.value); err != nil || wrap < 0 {
				complain(invocation, "base64", "invalid wrap size: '%s'", given.value)
				return errFailed
			}
		}
	}
	source := "-"
	if len(operands) == 1 {
		source = operands[0]
	}
	input, err := readInput(ctx, invocation, source)
	if err != nil {
		complain(invocation, "base64", "%s: %v", source, err)
		return errFailed
	}
	if parsed.has("d") {
		return decodeBase64(invocation, input, parsed.has("i"))
	}
	_, err = io.WriteString(invocation.Stdout, wrapText(base64.StdEncoding.EncodeToString(input), wrap))
	return err
}

// wrapText breaks encoded text into lines of width characters (0: one
// line) and ends it with a newline unless it is empty.
func wrapText(encoded string, width int) string {
	if encoded == "" {
		return ""
	}
	if width == 0 {
		return encoded + "\n"
	}
	var wrapped strings.Builder
	for len(encoded) > width {
		wrapped.WriteString(encoded[:width] + "\n")
		encoded = encoded[width:]
	}
	wrapped.WriteString(encoded + "\n")
	return wrapped.String()
}

func decodeBase64(invocation *Invocation, input []byte, ignoreGarbage bool) error {
	cleaned := strings.Map(func(character rune) rune {
		switch {
		case character == '\n' || character == '\r':
			return -1
		case ignoreGarbage && !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=", character):
			return -1
		}
		return character
	}, string(input))
	decoded, err := base64.StdEncoding.DecodeString(cleaned)
	if _, writeErr := invocation.Stdout.Write(decoded); writeErr != nil {
		return writeErr
	}
	if err != nil {
		complain(invocation, "base64", "invalid input")
		return errFailed
	}
	return nil
}

func sha256sumCommand() Command {
	return Command{Name: "sha256sum", Summary: "print SHA-256 checksums of files or stdin", Run: runSha256sum}
}

func runSha256sum(ctx context.Context, invocation *Invocation) error {
	_, operands, err := optionSet{flags: "bt", long: map[string]longOption{"binary": {name: "b"}, "text": {name: "t"}}}.parse(invocation.Arguments)
	if err != nil {
		return usage(invocation, "sha256sum", err, errFailed)
	}
	if len(operands) == 0 {
		operands = []string{"-"}
	}
	var status error
	for _, operand := range operands {
		content, err := readInput(ctx, invocation, operand)
		if err != nil {
			complain(invocation, "sha256sum", "%s: %v", operand, err)
			status = errFailed
			continue
		}
		fmt.Fprintf(invocation.Stdout, "%x  %s\n", sha256.Sum256(content), operand)
	}
	return status
}
