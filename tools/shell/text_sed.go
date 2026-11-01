package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"unicode"
)

// sedUsage is printed for --help and, as GNU sed does, after a usage error.
const sedUsage = `Usage: sed [OPTION]... {script-only-if-no-other-script} [input-file]...

  -n, --quiet, --silent
                 suppress automatic printing of pattern space
  -e script, --expression=script
                 add the script to the commands to be executed
  -f script-file, --file=script-file
                 add the contents of script-file to the commands to be executed
  -i[SUFFIX], --in-place[=SUFFIX]
                 edit files in place (makes backup if SUFFIX supplied)
  -E, -r, --regexp-extended
                 use extended regular expressions in the script
  -s, --separate
                 consider files as separate rather than as a single
                 continuous long stream.
  -u, --unbuffered
                 (accepted, no effect)
      --help     display this help and exit

Commands: s y a i c d D p P n N g G h H x b t T : = q Q z F { } #, addresses
N, $, /re/I, \cREc, first~step, addr,+N, addr,~N, 0,/re/ and !.
Not supported: l r R w W e and the s flags e and w.
`

var sedTool = &textTool{name: "sed", usageStatus: 1, help: sedUsage, hint: sedUsage}

var sedParser = &optionParser{specs: []optionSpec{
	{short: 'n', long: "quiet"},
	{long: "silent"},
	{short: 'e', long: "expression", argument: requiredArgument},
	{short: 'f', long: "file", argument: requiredArgument},
	{short: 'i', long: "in-place", argument: optionalArgument},
	{short: 'E', long: "regexp-extended"},
	{short: 'r'},
	{short: 's', long: "separate"},
	{short: 'u', long: "unbuffered"},
	helpOption,
}}

func sedCommand() Command {
	return Command{
		Name:    "sed",
		Summary: "stream editor (-n -e -f -E -r -i[SUFFIX] -s; s///gpiN, y, a i c, d D p P n N g G h H x, b t T, q Q, =, ranges, !)",
		Run:     runSed,
	}
}

// sedSettings is a parsed sed command line.
type sedSettings struct {
	quiet, extended, separate bool
	inPlace                   bool
	suffix                    string
	sources                   []sedSource
}

func runSed(ctx context.Context, invocation *Invocation) error {
	line, err := sedTool.parseOptions(invocation, sedParser)
	if line.done {
		return err
	}
	settings, operands, err := parseSedSettings(ctx, invocation, line)
	if err != nil {
		return err
	}
	program, err := parseSedProgram(settings.sources, settings.extended)
	var problem *sedScriptError
	if errors.As(err, &problem) {
		return sedTool.fatal(invocation, 1, "%s", problem.message)
	}
	if err != nil {
		return err
	}
	if strings.HasPrefix(settings.sources[0].text, "#n") && (len(settings.sources[0].text) == 2 || settings.sources[0].text[2] == '\n') {
		settings.quiet = true
	}
	editor := &sedEditor{settings: settings, program: program, invocation: invocation}
	if settings.inPlace {
		err = editor.editInPlace(ctx, operands)
	} else {
		err = editor.stream(ctx, operands)
	}
	if errors.As(err, &problem) {
		return sedTool.fatal(invocation, 1, "%s", problem.message)
	}
	return err
}

// parseSedSettings reads the options and takes the script from the first
// operand when there is no -e or -f.
func parseSedSettings(ctx context.Context, invocation *Invocation, line commandLine) (sedSettings, []string, error) {
	settings := sedSettings{}
	for _, option := range line.options {
		switch option.key {
		case "quiet", "silent":
			settings.quiet = true
		case "regexp-extended", "r":
			settings.extended = true
		case "separate":
			settings.separate = true
		case "in-place":
			settings.inPlace, settings.separate, settings.suffix = true, true, option.value
		case "expression":
			settings.sources = append(settings.sources, sedSource{text: option.value, expression: len(settings.sources) + 1})
		case "file":
			content, err := readOperand(ctx, invocation, option.value)
			if reason, ok := operandReason(err); ok {
				return settings, nil, sedTool.fatal(invocation, 1, "couldn't open file %s: %s", option.value, reason)
			}
			if err != nil {
				return settings, nil, err
			}
			settings.sources = append(settings.sources, sedSource{text: strings.TrimSuffix(string(content), "\n"), file: option.value})
		}
	}
	operands := line.operands
	if len(settings.sources) == 0 {
		if len(operands) == 0 {
			fmt.Fprint(invocation.Stderr, sedTool.help)
			return settings, nil, ExitError(1)
		}
		settings.sources = []sedSource{{text: operands[0], expression: 1}}
		operands = operands[1:]
	}
	return settings, operands, nil
}

// sedOutput writes lines, adding the newline a previous line without one
// lacked before anything else follows, as GNU sed does.
type sedOutput struct {
	writer         io.Writer
	missingNewline bool
	err            error
}

func (o *sedOutput) write(text string, newline bool) {
	if o.err != nil {
		return
	}
	if o.missingNewline {
		text = "\n" + text
		o.missingNewline = false
	}
	if newline {
		text += "\n"
	} else {
		o.missingNewline = true
	}
	if _, err := io.WriteString(o.writer, text); err != nil {
		o.err = fmt.Errorf("write output: %w", err)
	}
}

// sedFile is one input with its lines.
type sedFile struct {
	name  string
	lines []string
	// terminated is false when the last line has no newline.
	terminated bool
	content    string
}

// sedEditor runs a program over inputs.
type sedEditor struct {
	settings   sedSettings
	program    []sedInstruction
	invocation *Invocation
	output     *sedOutput
	// files are the inputs of the current stream, fileIndex and lineIndex
	// the next line to read.
	files                []sedFile
	fileIndex, lineIndex int
	lineNumber           int
	currentFile          string
	// patternTerminated says the pattern space came from a line with a
	// newline.
	patternTerminated     bool
	patternSpace, hold    string
	appendQueue           []string
	lastRegex             *posixRegexp
	substituted, quitting bool
	// failure is a run-time error found while matching, which ends the
	// cycle (errNoPreviousRegex).
	failure     error
	exitCode    int
	rangeStates []sedRangeState
}

// sedRangeState is the run-time state of a command's address range.
type sedRangeState struct {
	active, closed bool
	end            int
}

// stream runs the program over all inputs as one stream to stdout.
func (e *sedEditor) stream(ctx context.Context, operands []string) error {
	files, status, err := e.loadFiles(ctx, operandsOrStdin(operands))
	if err != nil {
		return err
	}
	e.output = &sedOutput{writer: e.invocation.Stdout}
	if e.settings.separate {
		for _, file := range files {
			e.files, e.fileIndex, e.lineIndex, e.lineNumber = []sedFile{file}, 0, 0, 0
			if err := e.run(ctx); err != nil || e.quitting {
				return e.finish(err, status)
			}
		}
		return e.finish(nil, status)
	}
	e.files = files
	return e.finish(e.run(ctx), status)
}

// finish combines a run's error, the exit code of q, and the status of
// unreadable inputs.
func (e *sedEditor) finish(err error, status ExitError) error {
	if err == nil {
		err = e.output.err
	}
	if err != nil {
		return err
	}
	if e.quitting && e.exitCode != 0 {
		return ExitError(e.exitCode) //nolint:gosec // G115: q's exit code is masked like a process status
	}
	return exitStatus(status)
}

// loadFiles reads the inputs; a missing one is reported and skipped with
// status 2, a directory stops sed with status 4.
func (e *sedEditor) loadFiles(ctx context.Context, names []string) ([]sedFile, ExitError, error) {
	var files []sedFile
	var status ExitError
	for _, name := range names {
		content, err := readOperand(ctx, e.invocation, name)
		if reason, ok := operandReason(err); ok {
			if reason == reasonIsDirectory {
				if e.settings.inPlace {
					return nil, 0, sedTool.fatal(e.invocation, 4, "couldn't edit %s: not a regular file", name)
				}
				return nil, 0, sedTool.fatal(e.invocation, 4, "read error on %s: %s", name, reason)
			}
			sedTool.complain(e.invocation, "can't read %s: %s", name, reason)
			status = 2
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		lines, terminated := splitLines(string(content))
		files = append(files, sedFile{name: name, lines: lines, terminated: terminated, content: string(content)})
	}
	return files, status, nil
}

// editInPlace runs the program over each file separately and writes the
// result back, keeping a backup when a suffix was given.
func (e *sedEditor) editInPlace(ctx context.Context, operands []string) error {
	if len(operands) == 0 {
		return sedTool.fatal(e.invocation, 1, "no input files")
	}
	e.output = &sedOutput{writer: io.Discard}
	var status ExitError
	for _, name := range operands {
		files, fileStatus, err := e.loadFiles(ctx, []string{name})
		if err != nil {
			return err
		}
		status = max(status, fileStatus)
		if len(files) == 0 {
			continue
		}
		var result strings.Builder
		e.output = &sedOutput{writer: &result}
		e.files, e.fileIndex, e.lineIndex, e.lineNumber = files, 0, 0, 0
		if err := e.run(ctx); err != nil {
			return err
		}
		if err := e.writeBack(ctx, name, files[0].content, result.String()); err != nil {
			return err
		}
		if e.quitting {
			break
		}
	}
	return e.finish(nil, status)
}

// writeBack stores the edited content and the optional backup.
func (e *sedEditor) writeBack(ctx context.Context, name, original, edited string) error {
	resolved, err := Resolve(e.invocation.Directory, name)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", name, err)
	}
	storage := e.invocation.Storage
	if e.settings.suffix != "" {
		backup := name + e.settings.suffix
		if strings.Contains(e.settings.suffix, "*") {
			backup = path.Join(path.Dir(name), strings.ReplaceAll(e.settings.suffix, "*", path.Base(name)))
		}
		backupPath, err := Resolve(e.invocation.Directory, backup)
		if err != nil {
			return fmt.Errorf("resolve %s: %w", backup, err)
		}
		if err := storage.WriteFile(ctx, backupPath, []byte(original), "text/plain"); err != nil {
			return fmt.Errorf("write %s: %w", backup, err)
		}
	}
	if err := storage.WriteFile(ctx, resolved, []byte(edited), "text/plain"); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// nextLine reads the next input line; ok is false at the end of input.
func (e *sedEditor) nextLine() (string, bool) {
	for e.fileIndex < len(e.files) {
		file := e.files[e.fileIndex]
		if e.lineIndex < len(file.lines) {
			text := file.lines[e.lineIndex]
			e.lineIndex++
			e.lineNumber++
			e.currentFile = file.name
			e.patternTerminated = e.lineIndex < len(file.lines) || file.terminated
			return text, true
		}
		e.fileIndex++
		e.lineIndex = 0
	}
	return "", false
}

// isLastLine reports whether no input line follows the current one.
func (e *sedEditor) isLastLine() bool {
	for index := e.fileIndex; index < len(e.files); index++ {
		remaining := len(e.files[index].lines)
		if index == e.fileIndex {
			remaining -= e.lineIndex
		}
		if remaining > 0 {
			return false
		}
	}
	return true
}

// run executes cycles until the input ends or q.
func (e *sedEditor) run(ctx context.Context) error {
	e.rangeStates = make([]sedRangeState, len(e.program))
	for index, command := range e.program {
		if command.address1 != nil && command.address1.kind == addressLine && command.address1.number == 0 {
			e.rangeStates[index].active = true
		}
	}
	text, ok := e.nextLine()
	for ok && !e.quitting {
		if err := ctx.Err(); err != nil {
			return err
		}
		e.patternSpace, e.substituted = text, false
		autoprint, readNext, err := e.cycle(ctx)
		if err != nil {
			return err
		}
		if autoprint && !e.settings.quiet {
			e.output.write(e.patternSpace, e.patternTerminated)
		}
		e.flushAppends()
		if !readNext {
			text = e.patternSpace
			continue
		}
		text, ok = e.nextLine()
	}
	return nil
}

// flushAppends writes the text queued by a.
func (e *sedEditor) flushAppends() {
	for _, text := range e.appendQueue {
		e.output.write(strings.TrimSuffix(text, "\n"), true)
	}
	e.appendQueue = e.appendQueue[:0]
}

// cycle runs the program once over the pattern space. print says whether
// the end of the cycle prints it, readNext whether a new line is read
// (false after D).
func (e *sedEditor) cycle(ctx context.Context) (bool, bool, error) {
	for counter := 0; counter < len(e.program); {
		command := &e.program[counter]
		selected := e.selects(ctx, counter)
		if e.failure != nil {
			return false, false, e.failure
		}
		if !selected {
			if command.name == '{' {
				counter = command.target
			}
			counter++
			continue
		}
		next, outcome, err := e.execute(ctx, counter)
		if err == nil {
			err = e.failure
		}
		if err != nil {
			return false, false, err
		}
		switch outcome {
		case cycleContinue:
			counter = next
		case cycleEnd:
			return true, true, nil
		case cycleDelete:
			return false, true, nil
		case cycleRestart:
			return false, false, nil
		case cycleQuitSilently:
			return false, true, nil
		}
	}
	return true, true, nil
}

// cycleOutcome is what a command does to the cycle.
type cycleOutcome int

const (
	cycleContinue cycleOutcome = iota
	// cycleEnd ends the cycle with the automatic print.
	cycleEnd
	// cycleDelete ends it without printing (d, c).
	cycleDelete
	// cycleRestart starts the next cycle on the remaining pattern space (D).
	cycleRestart
	cycleQuitSilently
)

// execute runs the selected command at counter and returns the next
// command index.
func (e *sedEditor) execute(ctx context.Context, counter int) (int, cycleOutcome, error) {
	command := &e.program[counter]
	switch command.name {
	case '{', '}', ':', '#', 'v':
	case '=':
		e.output.write(strconv.Itoa(e.lineNumber), true)
	case 'a':
		e.appendQueue = append(e.appendQueue, command.text)
	case 'i':
		e.output.write(strings.TrimSuffix(command.text, "\n"), true)
	case 'c':
		if command.address2 == nil || command.negate || !e.rangeStates[counter].active {
			e.output.write(strings.TrimSuffix(command.text, "\n"), true)
		}
		return 0, cycleDelete, nil
	case 'd':
		return 0, cycleDelete, nil
	case 'D':
		index := strings.IndexByte(e.patternSpace, '\n')
		if index < 0 {
			return 0, cycleDelete, nil
		}
		e.patternSpace = e.patternSpace[index+1:]
		return 0, cycleRestart, nil
	case 'n', 'N':
		return e.readInto(counter, command.name == 'N')
	case 'q':
		e.quitting, e.exitCode = true, command.exitCode
		return 0, cycleEnd, nil
	case 'Q':
		e.quitting, e.exitCode = true, command.exitCode
		return 0, cycleQuitSilently, nil
	case 'b', 't', 'T':
		return e.branch(counter)
	case 's':
		return counter + 1, cycleContinue, e.substitute(ctx, command.substitution)
	default:
		e.simpleCommand(command)
	}
	return counter + 1, cycleContinue, nil
}

// simpleCommand runs the commands that only touch the spaces or print.
func (e *sedEditor) simpleCommand(command *sedInstruction) {
	switch command.name {
	case 'g':
		e.patternSpace = e.hold
	case 'G':
		e.patternSpace += "\n" + e.hold
	case 'h':
		e.hold = e.patternSpace
	case 'H':
		e.hold += "\n" + e.patternSpace
	case 'x':
		e.patternSpace, e.hold = e.hold, e.patternSpace
	case 'p':
		e.output.write(e.patternSpace, e.patternTerminated)
	case 'P':
		first, _, _ := strings.Cut(e.patternSpace, "\n")
		e.output.write(first, true)
	case 'z':
		e.patternSpace = ""
	case 'F':
		name := e.currentFile
		e.output.write(name, true)
	case 'y':
		e.patternSpace = strings.Map(func(character rune) rune {
			if mapped, ok := command.translation[character]; ok {
				return mapped
			}
			return character
		}, e.patternSpace)
	}
}

// readInto is n and N: without a next line the cycle ends like in GNU sed,
// printing the pattern space unless -n.
func (e *sedEditor) readInto(counter int, appendLine bool) (int, cycleOutcome, error) {
	if e.isLastLine() {
		return 0, cycleEnd, nil
	}
	if !appendLine && !e.settings.quiet {
		e.output.write(e.patternSpace, e.patternTerminated)
	}
	e.flushAppends()
	text, _ := e.nextLine()
	if appendLine {
		e.patternSpace += "\n" + text
	} else {
		e.patternSpace = text
	}
	return counter + 1, cycleContinue, nil
}

// branch is b, t and T.
func (e *sedEditor) branch(counter int) (int, cycleOutcome, error) {
	command := &e.program[counter]
	jump := true
	switch command.name {
	case 't':
		jump = e.substituted
		e.substituted = false
	case 'T':
		jump = !e.substituted
		e.substituted = false
	}
	if !jump {
		return counter + 1, cycleContinue, nil
	}
	if command.target < 0 {
		return 0, cycleEnd, nil
	}
	return command.target, cycleContinue, nil
}

// selects reports whether the command at index applies to the current line,
// advancing its range state like GNU's match_address_p.
func (e *sedEditor) selects(ctx context.Context, index int) bool {
	command := &e.program[index]
	return e.matchesRange(ctx, index, command) != command.negate
}

func (e *sedEditor) matchesRange(ctx context.Context, index int, command *sedInstruction) bool {
	if command.address1 == nil {
		return true
	}
	if command.address2 == nil {
		return e.matchesAddress(ctx, command.address1)
	}
	state := &e.rangeStates[index]
	if state.active {
		return e.continueRange(ctx, state, command.address2)
	}
	if state.closed || !e.matchesAddress(ctx, command.address1) {
		return false
	}
	switch second := command.address2; second.kind {
	case addressLine:
		if second.number <= e.lineNumber {
			return true
		}
		state.end = second.number
	case addressRelative:
		if second.number == 0 {
			return true
		}
		state.end = e.lineNumber + second.number
	case addressMultiple:
		if second.number == 0 || e.lineNumber%second.number == 0 {
			return true
		}
		state.end = (e.lineNumber/second.number + 1) * second.number
	case addressLast, addressRegex, addressStep:
	}
	state.active = true
	return true
}

// continueRange decides a line inside an active range.
func (e *sedEditor) continueRange(ctx context.Context, state *sedRangeState, second *sedAddress) bool {
	switch second.kind {
	case addressLine, addressRelative, addressMultiple:
		if e.lineNumber >= state.end {
			state.active = false
		}
		return e.lineNumber <= state.end
	case addressLast, addressRegex, addressStep:
	}
	if e.matchesAddress(ctx, second) {
		state.active = false
	}
	return true
}

func (e *sedEditor) matchesAddress(ctx context.Context, address *sedAddress) bool {
	switch address.kind {
	case addressLine:
		return e.lineNumber == address.number
	case addressLast:
		return e.isLastLine()
	case addressStep:
		if address.step <= 0 {
			return e.lineNumber == address.number
		}
		return e.lineNumber >= address.number && (e.lineNumber-address.number)%address.step == 0
	case addressRegex:
		regex := e.useRegex(address.regex)
		return regex != nil && regex.matchString(ctx, e.patternSpace)
	case addressRelative, addressMultiple:
	}
	return false
}

// errNoPreviousRegex is "//" before any regex was used, in GNU's words.
var errNoPreviousRegex = &sedScriptError{"-e expression #1, char 0: no previous regular expression"}

// useRegex resolves "//" to the last regex used and remembers this one.
// Without a previous regex it records errNoPreviousRegex in failure, which
// stops the cycle, and returns nil.
func (e *sedEditor) useRegex(regex *sedRegex) *posixRegexp {
	if regex.regex == nil {
		if e.lastRegex == nil {
			e.failure = errNoPreviousRegex
		}
		return e.lastRegex
	}
	e.lastRegex = regex.regex
	return regex.regex
}

// substitute is the s command.
func (e *sedEditor) substitute(ctx context.Context, substitution *sedSubstitution) error {
	regex := e.useRegex(substitution.regex)
	if regex == nil {
		return nil
	}
	matches := regex.findAll(ctx, e.patternSpace)
	var result strings.Builder
	last, replaced := 0, false
	for count, match := range matches {
		if count+1 < substitution.occurrence {
			continue
		}
		if replaced && !substitution.global {
			break
		}
		result.WriteString(e.patternSpace[last:match[0]])
		result.WriteString(expandReplacement(e.patternSpace, match, substitution.replacement))
		last, replaced = match[1], true
	}
	if !replaced {
		return nil
	}
	result.WriteString(e.patternSpace[last:])
	e.patternSpace, e.substituted = result.String(), true
	if substitution.print {
		e.output.write(e.patternSpace, e.patternTerminated)
	}
	return nil
}

// expandReplacement builds one replacement with & \N and the case
// conversions \U \L \E (until \E) and \u \l (next character).
func expandReplacement(text string, match []int, parts []replacementPart) string {
	var output strings.Builder
	var mode, once byte
	emit := func(piece string) {
		for _, character := range piece {
			switch mode {
			case 'U':
				character = unicode.ToUpper(character)
			case 'L':
				character = unicode.ToLower(character)
			}
			switch once {
			case 'u':
				character, once = unicode.ToUpper(character), 0
			case 'l':
				character, once = unicode.ToLower(character), 0
			}
			output.WriteRune(character)
		}
	}
	for _, part := range parts {
		switch part.kind {
		case replaceLiteral:
			emit(part.literal)
		case replaceGroup:
			if start, end := match[2*part.group], match[2*part.group+1]; start >= 0 {
				emit(text[start:end])
			}
		case replaceCase:
			switch part.caseOperation {
			case 'u', 'l':
				once = part.caseOperation
			case 'E':
				mode, once = 0, 0
			default:
				mode = part.caseOperation
			}
		}
	}
	return output.String()
}
