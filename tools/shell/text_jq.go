package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/itchyny/gojq"
)

const jqHint = "Use jq --help for help with command-line options,\nor see the jq manpage, or online docs  at https://jqlang.org\n"

const jqHelp = `Usage:	jq [OPTIONS] FILTER [FILES...]
jq is a JSON processor (gojq inside; object keys print sorted).
  -n, --null-input          use null as the single input value
  -R, --raw-input           read each line as string instead of JSON
  -s, --slurp               read all inputs into an array and use it as the single input value
  -c, --compact-output      compact instead of pretty-printed output
  -r, --raw-output          output strings without escapes and quotes
  -j, --join-output         implies -r and output without newline after each output
  -S, --sort-keys           sort keys of each object on output (always on)
  -M, -C                    accepted; output is never coloured
      --tab                 use tabs for indentation
      --indent n            use n spaces for indentation (max 7 spaces)
  -e, --exit-status         set exit status code based on the output
      --arg name value      set $name to the string value
      --argjson name value  set $name to the JSON value
  -h, --help                show the help
`

func jqCommand() Command {
	return Command{Name: "jq", Summary: "JSON processor (-r -j -c -n -s -e -R --tab --indent N --arg --argjson; object keys always sorted)", Run: runJq}
}

// jqSettings is a parsed jq command line.
type jqSettings struct {
	nullInput, rawInput, slurp, compact, raw, join, exitStatus bool
	indent                                                     string
	program                                                    string
	files                                                      []string
	variableNames                                              []string
	variableValues                                             []any
	named                                                      map[string]any
}

// jqFlags are the single-letter flags jq accepts, also combined (-rc).
var jqFlags = map[byte]string{
	'n': "null-input", 'R': "raw-input", 's': "slurp", 'c': "compact-output", 'r': "raw-output",
	'j': "join-output", 'S': "sort-keys", 'M': "monochrome-output", 'C': "color-output", 'e': "exit-status", 'h': "help",
}

func runJq(ctx context.Context, invocation *Invocation) error {
	settings, message := parseJqArguments(invocation.Arguments)
	switch {
	case message == "help":
		if _, err := io.WriteString(invocation.Stdout, jqHelp); err != nil {
			return fmt.Errorf("write help: %w", err)
		}
		return nil
	case message != "":
		fmt.Fprintf(invocation.Stderr, "jq: %s\n%s", message, jqHint)
		return ExitError(2)
	}
	query, err := gojq.Parse(settings.program)
	if err != nil {
		fmt.Fprintf(invocation.Stderr, "jq: error: %v\njq: 1 compile error\n", err)
		return ExitError(3)
	}
	inputs, err := loadJqInputs(ctx, invocation, settings)
	if err != nil {
		return err
	}
	code, err := compileJq(invocation, settings, query, inputs)
	if err != nil {
		fmt.Fprintf(invocation.Stderr, "jq: error: %v\njq: 1 compile error\n", err)
		return ExitError(3)
	}
	runner := &jqRunner{settings: settings, code: code, invocation: invocation, inputs: inputs, lastOutput: jqNoOutput}
	return runner.run(ctx)
}

// parseJqArguments reads jq's command line; the message is jq's complaint,
// or "help".
func parseJqArguments(arguments []string) (*jqSettings, string) {
	settings := &jqSettings{indent: "  ", named: map[string]any{}}
	var positional []string
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		switch {
		case argument == "--":
			positional = append(positional, arguments[index+1:]...)
			index = len(arguments)
		case strings.HasPrefix(argument, "--"):
			consumed, message := settings.longOption(argument[2:], arguments[index+1:])
			if message != "" {
				return nil, message
			}
			index += consumed
		case len(argument) > 1 && argument[0] == '-':
			for _, letter := range []byte(argument[1:]) {
				name, ok := jqFlags[letter]
				if !ok {
					return nil, "Unknown option " + argument
				}
				if _, message := settings.longOption(name, nil); message != "" {
					return nil, message
				}
			}
		default:
			positional = append(positional, argument)
		}
	}
	settings.program = "."
	if len(positional) > 0 {
		settings.program, settings.files = positional[0], positional[1:]
	}
	return settings, ""
}

// longOption applies one long option; it consumes values from rest and
// returns how many.
func (s *jqSettings) longOption(name string, rest []string) (int, string) {
	flags := map[string]*bool{
		"null-input": &s.nullInput, "raw-input": &s.rawInput, "slurp": &s.slurp, "compact-output": &s.compact,
		"raw-output": &s.raw, "join-output": &s.join, "exit-status": &s.exitStatus,
	}
	if flag, ok := flags[name]; ok {
		*flag = true
		return 0, ""
	}
	switch name {
	case "sort-keys", "monochrome-output", "color-output":
		return 0, ""
	case "help":
		return 0, "help"
	case "tab":
		s.indent = "\t"
		return 0, ""
	case "indent":
		if len(rest) == 0 {
			return 0, "--indent takes one parameter"
		}
		width, err := strconv.Atoi(rest[0])
		if err != nil || width < 0 || width > 7 {
			return 0, "Cannot indent more than 7 characters"
		}
		s.indent = strings.Repeat(" ", width)
		return 1, ""
	case "arg", "argjson":
		return s.variable(name, rest)
	}
	return 0, "Unknown option --" + name
}

// variable is --arg name value or --argjson name json.
func (s *jqSettings) variable(kind string, rest []string) (int, string) {
	if len(rest) < 2 {
		return 0, fmt.Sprintf("--%s takes two parameters (e.g. --%s varname %s)", kind, kind, map[string]string{"arg": "value", "argjson": "text"}[kind])
	}
	var value any = rest[1]
	if kind == "argjson" {
		decoder := json.NewDecoder(strings.NewReader(rest[1]))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return 0, "invalid JSON text passed to --argjson"
		}
	}
	s.variableNames = append(s.variableNames, "$"+rest[0])
	s.variableValues = append(s.variableValues, value)
	s.named[rest[0]] = value
	return 2, ""
}

// compileJq compiles the parsed filter with the variables, the shell's
// environment for $ENV and the inputs for input and inputs.
func compileJq(invocation *Invocation, settings *jqSettings, query *gojq.Query, inputs *jqInputs) (*gojq.Code, error) {
	names := append(slices.Clone(settings.variableNames), "$ARGS")
	settings.variableValues = append(settings.variableValues, map[string]any{"positional": []any{}, "named": settings.named})
	code, err := gojq.Compile(query, gojq.WithVariables(names), gojq.WithInputIter(inputs),
		gojq.WithEnvironLoader(func() []string { return invocation.Environment }))
	if err != nil {
		return nil, err
	}
	return code, nil
}

// jqInputs are the decoded input values, read lazily so input and inputs
// see the rest of the stream.
type jqInputs struct {
	decoder *json.Decoder
	content []byte
	// boundaries are the end offsets of each file within content, names
	// their names as jq reports them.
	boundaries []int
	names      []string
	rawLines   []string
	raw        bool
	slurped    bool
	done       bool
}

// loadJqInputs reads the files (or stdin); an unreadable file is reported
// and yields ExitError 2.
func loadJqInputs(ctx context.Context, invocation *Invocation, settings *jqSettings) (*jqInputs, error) {
	inputs := &jqInputs{raw: settings.rawInput}
	names := settings.files
	if len(names) == 0 {
		names = []string{"-"}
	}
	var content bytes.Buffer
	var budget inputBudget
	for _, name := range names {
		data, err := readOperand(ctx, invocation, name)
		if reason, ok := operandReason(err); ok {
			fmt.Fprintf(invocation.Stderr, "jq: error: Could not open %s: %s\n", name, reason)
			return nil, ExitError(2)
		}
		if err != nil {
			return nil, err
		}
		if err := budget.charge(len(data)); err != nil {
			return nil, err
		}
		content.Write(data)
		inputs.boundaries = append(inputs.boundaries, content.Len())
		if name == "-" {
			name = "<stdin>"
		}
		inputs.names = append(inputs.names, name)
	}
	inputs.content = content.Bytes()
	if settings.rawInput {
		lines, _ := splitLines(content.String())
		inputs.rawLines = lines
		if settings.slurp {
			inputs.rawLines = []string{content.String()}
		}
		return inputs, nil
	}
	inputs.decoder = json.NewDecoder(bytes.NewReader(inputs.content))
	inputs.decoder.UseNumber()
	inputs.slurped = settings.slurp
	return inputs, nil
}

// Next returns the next input value, an error value for invalid JSON, or
// false at the end; it makes jqInputs a gojq.Iter.
func (i *jqInputs) Next() (any, bool) {
	if i.done {
		return nil, false
	}
	if i.raw {
		if len(i.rawLines) == 0 {
			i.done = true
			return nil, false
		}
		line := i.rawLines[0]
		i.rawLines = i.rawLines[1:]
		return line, true
	}
	if i.slurped {
		i.done = true
		var all []any
		for {
			value, ok := i.decode()
			if !ok {
				return all, true
			}
			if err, isError := value.(error); isError {
				return err, true
			}
			all = append(all, value)
		}
	}
	return i.decode()
}

func (i *jqInputs) decode() (any, bool) {
	var value any
	err := i.decoder.Decode(&value)
	if errors.Is(err, io.EOF) {
		i.done = true
		return nil, false
	}
	if err != nil {
		i.done = true
		return &jqParseError{err.Error()}, true
	}
	return value, true
}

// location is jq's "<stdin>:N" for the input read so far; after a parse
// error the whole input counts as read.
func (i *jqInputs) location(afterParseError bool) string {
	if i.decoder == nil {
		return "<unknown>"
	}
	offset := int(i.decoder.InputOffset())
	if afterParseError {
		offset = len(i.content)
	}
	if offset < len(i.content) && i.content[offset] == '\n' {
		offset++
	}
	start := 0
	for index, boundary := range i.boundaries {
		if offset <= boundary || index == len(i.boundaries)-1 {
			return fmt.Sprintf("%s:%d", i.names[index], bytes.Count(i.content[start:offset], []byte("\n")))
		}
		start = boundary
	}
	return "<unknown>"
}

// jqParseError is input that is not valid JSON.
type jqParseError struct{ message string }

func (e *jqParseError) Error() string { return e.message }

// jqOutputState is the kind of the last value printed, for -e.
type jqOutputState int

const (
	jqNoOutput jqOutputState = iota
	jqTruthy
	jqFalsy
)

// jqRunner runs the filter over the inputs and prints the results.
type jqRunner struct {
	settings   *jqSettings
	code       *gojq.Code
	invocation *Invocation
	inputs     *jqInputs
	output     bytes.Buffer
	lastOutput jqOutputState
	status     ExitError
	// writeError is the first failed write to stdout.
	writeError error
}

func (r *jqRunner) run(ctx context.Context) error {
	if r.settings.nullInput {
		r.apply(ctx, nil, "<unknown>")
	} else if err := r.applyInputs(ctx); err != nil {
		return err
	}
	if r.flush(); r.writeError != nil {
		return r.writeError
	}
	return exitStatus(r.finalStatus())
}

// applyInputs runs the filter on each input until the inputs end, one is
// not valid JSON, or the output is gone.
func (r *jqRunner) applyInputs(ctx context.Context) error {
	for value, ok := r.inputs.Next(); ok && r.writeError == nil; value, ok = r.inputs.Next() {
		if problem, isParseError := value.(*jqParseError); isParseError {
			r.flush()
			fmt.Fprintf(r.invocation.Stderr, "jq: error (at %s): %s\n", r.inputs.location(true), problem.message)
			r.status = 2
			return nil
		}
		r.apply(ctx, value, "")
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

// finalStatus is the exit status, with -e judging the last output.
func (r *jqRunner) finalStatus() ExitError {
	if r.status != 0 || !r.settings.exitStatus {
		return r.status
	}
	switch r.lastOutput {
	case jqNoOutput:
		return 4
	case jqFalsy:
		return 1
	case jqTruthy:
	}
	return 0
}

// apply runs the filter on one input and prints every result; errors are
// reported like jq and make the exit status 5.
func (r *jqRunner) apply(ctx context.Context, input any, location string) {
	values := append([]any{input}, r.settings.variableValues...)
	iterator := r.code.RunWithContext(ctx, values[0], values[1:]...)
	for r.writeError == nil {
		value, ok := iterator.Next()
		if !ok {
			return
		}
		if err, isError := value.(error); isError {
			r.report(err, location)
			if halted := (*gojq.HaltError)(nil); errors.As(err, &halted) {
				return
			}
			continue
		}
		r.print(value)
	}
}

// report prints a runtime error or halt_error.
func (r *jqRunner) report(err error, location string) {
	r.flush()
	if location == "" {
		location = r.inputs.location(false)
	}
	var halted *gojq.HaltError
	if errors.As(err, &halted) {
		r.status = ExitError(halted.ExitCode() & 0xff)
		switch value := halted.Value().(type) {
		case nil:
		case string:
			fmt.Fprint(r.invocation.Stderr, value)
		default:
			encoded, _ := gojq.Marshal(value)
			fmt.Fprintf(r.invocation.Stderr, "%s\n", encoded)
		}
		return
	}
	fmt.Fprintf(r.invocation.Stderr, "jq: error (at %s): %v\n", location, err)
	r.status = 5
}

// print writes one result in the selected format.
func (r *jqRunner) print(value any) {
	switch value {
	case nil, false:
		r.lastOutput = jqFalsy
	default:
		r.lastOutput = jqTruthy
	}
	if text, isString := value.(string); isString && (r.settings.raw || r.settings.join) {
		r.output.WriteString(text)
	} else {
		r.encode(value, 0)
	}
	if !r.settings.join {
		r.output.WriteByte('\n')
	}
	if r.output.Len() >= jqFlushBytes {
		r.flush()
	}
}

// jqFlushBytes is how much output collects before it is written, so an
// endless filter (jq -n 'repeat(1)') streams instead of growing.
const jqFlushBytes = 64 << 10

// encode writes value as jq does: pretty with the indent, or compact.
func (r *jqRunner) encode(value any, depth int) {
	switch typed := value.(type) {
	case []any:
		if len(typed) == 0 {
			r.output.WriteString("[]")
			return
		}
		r.output.WriteByte('[')
		for index, element := range typed {
			r.separate(index, depth+1)
			r.encode(element, depth+1)
		}
		r.newline(depth)
		r.output.WriteByte(']')
	case map[string]any:
		r.encodeObject(typed, depth)
	default:
		encoded, _ := gojq.Marshal(value)
		r.output.Write(encoded)
	}
}

// encodeObject writes an object with its keys sorted, as gojq holds them.
func (r *jqRunner) encodeObject(object map[string]any, depth int) {
	if len(object) == 0 {
		r.output.WriteString("{}")
		return
	}
	r.output.WriteByte('{')
	for index, key := range slices.Sorted(maps.Keys(object)) {
		r.separate(index, depth+1)
		encodedKey, _ := gojq.Marshal(key)
		r.output.Write(encodedKey)
		r.output.WriteByte(':')
		if r.pretty() {
			r.output.WriteByte(' ')
		}
		r.encode(object[key], depth+1)
	}
	r.newline(depth)
	r.output.WriteByte('}')
}

func (r *jqRunner) pretty() bool { return !r.settings.compact && r.settings.indent != "" }

// separate starts the element at index of a container.
func (r *jqRunner) separate(index, depth int) {
	if index > 0 {
		r.output.WriteByte(',')
	}
	r.newline(depth)
}

// newline breaks the line and indents to depth when pretty-printing.
func (r *jqRunner) newline(depth int) {
	if r.pretty() {
		r.output.WriteString("\n" + strings.Repeat(r.settings.indent, depth))
	}
}

// flush writes the buffered results, remembering the first write error.
func (r *jqRunner) flush() {
	if r.output.Len() == 0 || r.writeError != nil {
		return
	}
	if _, err := r.invocation.Stdout.Write(r.output.Bytes()); err != nil {
		r.writeError = fmt.Errorf("write output: %w", err)
	}
	r.output.Reset()
}
