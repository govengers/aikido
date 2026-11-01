package shell

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
)

var grepTool = &textTool{
	name:        "grep",
	hint:        "Usage: grep [OPTION]... PATTERNS [FILE]...\nTry 'grep --help' for more information.\n",
	usageStatus: 2,
	help: `Usage: grep [OPTION]... PATTERNS [FILE]...
Search for PATTERNS in each FILE.
  -E, --extended-regexp     PATTERNS are extended regular expressions
  -F, --fixed-strings       PATTERNS are strings
  -G, --basic-regexp        PATTERNS are basic regular expressions (default)
  -P, --perl-regexp         PATTERNS are Go RE2 expressions (no lookaround, no back-references)
  -e, --regexp=PATTERNS     use PATTERNS for matching
  -f, --file=FILE           take PATTERNS from FILE
  -i, --ignore-case         ignore case distinctions in patterns and data
  -w, --word-regexp         match only whole words
  -x, --line-regexp         match only whole lines
  -s, --no-messages         suppress error messages
  -v, --invert-match        select non-matching lines
  -m, --max-count=NUM       stop after NUM selected lines
  -n, --line-number         print line number with output lines
  -H, --with-filename       print file name with output lines
  -h, --no-filename         suppress the file name prefix on output
  -o, --only-matching       show only nonempty parts of lines that match
  -q, --quiet, --silent     suppress all normal output
  -r, -R, --recursive       search directories recursively
      --include=GLOB        search only files that match GLOB
      --exclude=GLOB        skip files that match GLOB
      --exclude-dir=GLOB    skip directories that match GLOB
  -a, --text                process a binary file as if it were text
  -I                        do not match binary files
  -L, --files-without-match print only names of FILEs with no selected lines
  -l, --files-with-matches  print only names of FILEs with selected lines
  -c, --count               print only a count of selected lines per FILE
  -Z, --null                print 0 byte after FILE name
  -z, --null-data           a data line ends in 0 byte, not newline
  -B, --before-context=NUM  print NUM lines of leading context
  -A, --after-context=NUM   print NUM lines of trailing context
  -C, --context=NUM, -NUM   print NUM lines of output context
      --color[=WHEN]        accepted; output is never coloured
`,
}

var grepParser = &optionParser{digitsKey: "context", specs: []optionSpec{
	{short: 'E', long: "extended-regexp"},
	{short: 'F', long: "fixed-strings"},
	{short: 'G', long: "basic-regexp"},
	{short: 'P', long: "perl-regexp"},
	{short: 'e', long: "regexp", argument: requiredArgument},
	{short: 'f', long: "file", argument: requiredArgument},
	{short: 'i', long: "ignore-case"},
	{short: 'y'},
	{long: "no-ignore-case"},
	{short: 'w', long: "word-regexp"},
	{short: 'x', long: "line-regexp"},
	{short: 's', long: "no-messages"},
	{short: 'v', long: "invert-match"},
	{short: 'm', long: "max-count", argument: requiredArgument},
	{short: 'n', long: "line-number"},
	{short: 'H', long: "with-filename"},
	{short: 'h', long: "no-filename"},
	{short: 'o', long: "only-matching"},
	{short: 'q', long: "quiet"},
	{long: "silent"},
	{short: 'r', long: "recursive"},
	{short: 'R', long: "dereference-recursive"},
	{long: "include", argument: requiredArgument},
	{long: "exclude", argument: requiredArgument},
	{long: "exclude-dir", argument: requiredArgument},
	{short: 'a', long: "text"},
	{short: 'I'},
	{short: 'L', long: "files-without-match"},
	{short: 'l', long: "files-with-matches"},
	{short: 'c', long: "count"},
	{short: 'Z', long: "null"},
	{short: 'z', long: "null-data"},
	{short: 'B', long: "before-context", argument: requiredArgument},
	{short: 'A', long: "after-context", argument: requiredArgument},
	{short: 'C', long: "context", argument: requiredArgument},
	{long: "color", argument: optionalArgument},
	{long: "colour", argument: optionalArgument},
	{long: "line-buffered"},
	helpOption,
}}

func grepCommand() Command {
	return Command{
		Name:    "grep",
		Summary: "search text (-r -n -l -L -c -i -v -w -x -o -q -s -E -F -P -e -f -A -B -C -m -h -H -Z -z --include --exclude --exclude-dir)",
		Run:     runGrep,
	}
}

// grepSyntax is the pattern language.
type grepSyntax int

const (
	grepBasic grepSyntax = iota
	grepExtended
	grepFixed
	grepPerl
)

// grepListing is -l or -L.
type grepListing int

const (
	listNone grepListing = iota
	listMatching
	listNonMatching
)

// grepSettings is a parsed grep command line.
type grepSettings struct {
	syntax                     grepSyntax
	patterns                   []string
	patternFiles               []string
	hasPatterns                bool
	ignoreCase, word, line     bool
	invert, count, onlyMatches bool
	quiet, noMessages          bool
	lineNumbers                bool
	// filenames: 0 automatic, 1 -H, -1 -h.
	filenames     int
	listing       grepListing
	maximum       int
	before, after int
	// context says -A, -B or -C was given, even with 0: groups are then
	// separated by "--".
	context    bool
	recursive  bool
	excludeDir []string
	// fileRules are --include and --exclude in command-line order.
	fileRules        []grepFileRule
	text, skipBinary bool
	// nullNames (-Z) ends a printed file name with a NUL byte instead of
	// ":" or a newline; nullData (-z) makes NUL the line terminator of
	// input and output.
	nullNames, nullData bool
}

// grepFileRule is one --include (include) or --exclude glob.
type grepFileRule struct {
	pattern string
	include bool
}

func runGrep(ctx context.Context, invocation *Invocation) error {
	line, err := grepTool.parseOptions(invocation, grepParser)
	if line.done {
		return err
	}
	settings, err := parseGrepSettings(invocation, line.options)
	if err != nil {
		return err
	}
	operands := line.operands
	if !settings.hasPatterns {
		if len(operands) == 0 {
			fmt.Fprint(invocation.Stderr, grepTool.hint)
			return ExitError(2)
		}
		settings.patterns = strings.Split(operands[0], "\n")
		operands = operands[1:]
	}
	if settings.maximum == 0 {
		// GNU stops before reading anything: no output, status 1.
		return ExitError(1)
	}
	if err := settings.readPatternFiles(ctx, invocation); err != nil {
		return err
	}
	matcher, err := newGrepMatcher(&settings)
	var problem *regexError
	if errors.As(err, &problem) {
		return grepTool.fatal(invocation, 2, "%s", problem.message)
	}
	if err != nil {
		return err
	}
	searcher := &grepSearcher{settings: &settings, matcher: matcher, invocation: invocation, output: bufio.NewWriter(invocation.Stdout)}
	status, err := searcher.searchAll(ctx, operands)
	if flushErr := searcher.output.Flush(); flushErr != nil && err == nil {
		err = fmt.Errorf("write output: %w", flushErr)
	}
	if err != nil {
		return err
	}
	return exitStatus(status)
}

// parseGrepSettings reads the options; a bad number is reported like GNU.
func parseGrepSettings(invocation *Invocation, options []parsedOption) (grepSettings, error) {
	settings := grepSettings{maximum: -1, before: -1, after: -1}
	contextLines := -1
	for _, option := range options {
		if settings.applyFlag(option.key) {
			continue
		}
		switch option.key {
		case "regexp":
			settings.patterns = append(settings.patterns, strings.Split(option.value, "\n")...)
			settings.hasPatterns = true
		case "file":
			settings.patternFiles = append(settings.patternFiles, option.value)
			settings.hasPatterns = true
		case "max-count":
			value, err := strconv.Atoi(option.value)
			if err != nil {
				return settings, grepTool.fatal(invocation, 2, "invalid max count")
			}
			settings.maximum = value
		case "before-context", "after-context", "context":
			value, err := strconv.Atoi(option.value)
			if err != nil || value < 0 {
				return settings, grepTool.fatal(invocation, 2, "%s: invalid context length argument", option.value)
			}
			settings.context = true
			switch option.key {
			case "before-context":
				settings.before = value
			case "after-context":
				settings.after = value
			default:
				contextLines = value
			}
		case "include":
			settings.fileRules = append(settings.fileRules, grepFileRule{pattern: option.value, include: true})
		case "exclude":
			settings.fileRules = append(settings.fileRules, grepFileRule{pattern: option.value})
		case "exclude-dir":
			settings.excludeDir = append(settings.excludeDir, option.value)
		}
	}
	if settings.before < 0 {
		settings.before = max(contextLines, 0)
	}
	if settings.after < 0 {
		settings.after = max(contextLines, 0)
	}
	return settings, nil
}

// applyFlag records an option without a value; false for other options.
func (s *grepSettings) applyFlag(key string) bool {
	flags := map[string]*bool{
		"ignore-case": &s.ignoreCase, "y": &s.ignoreCase, "word-regexp": &s.word, "line-regexp": &s.line,
		"no-messages": &s.noMessages, "invert-match": &s.invert, "line-number": &s.lineNumbers,
		"only-matching": &s.onlyMatches, "quiet": &s.quiet, "silent": &s.quiet, "recursive": &s.recursive,
		"dereference-recursive": &s.recursive, "text": &s.text, "I": &s.skipBinary, "count": &s.count,
		"null": &s.nullNames, "null-data": &s.nullData,
	}
	if flag, ok := flags[key]; ok {
		*flag = true
		return true
	}
	switch key {
	case "extended-regexp":
		s.syntax = grepExtended
	case "fixed-strings":
		s.syntax = grepFixed
	case "basic-regexp":
		s.syntax = grepBasic
	case "perl-regexp":
		s.syntax = grepPerl
	case "no-ignore-case":
		s.ignoreCase = false
	case "with-filename":
		s.filenames = 1
	case "no-filename":
		s.filenames = -1
	case "files-with-matches":
		s.listing = listMatching
	case "files-without-match":
		s.listing = listNonMatching
	case "color", "colour", "line-buffered":
	default:
		return false
	}
	return true
}

// readPatternFiles appends the patterns of every -f file, one per line.
func (s *grepSettings) readPatternFiles(ctx context.Context, invocation *Invocation) error {
	for _, name := range s.patternFiles {
		content, err := readOperand(ctx, invocation, name)
		if reason, ok := operandReason(err); ok {
			return grepTool.fatal(invocation, 2, "%s: %s", name, reason)
		}
		if err != nil {
			return err
		}
		lines, _ := splitLines(string(content))
		s.patterns = append(s.patterns, lines...)
	}
	return nil
}

// grepMatcher decides which lines match and where.
type grepMatcher struct {
	// regex is nil when there are no patterns at all (an empty -f file).
	regex *posixRegexp
	word  bool
}

// newGrepMatcher compiles all patterns into one alternation.
func newGrepMatcher(settings *grepSettings) (*grepMatcher, error) {
	if len(settings.patterns) == 0 {
		return &grepMatcher{}, nil
	}
	parts := make([]translation, 0, len(settings.patterns))
	for _, pattern := range settings.patterns {
		translated, err := translateGrepPattern(pattern, settings)
		if err != nil {
			return nil, err
		}
		parts = append(parts, translated)
	}
	combined := alternatives(parts)
	if settings.line {
		combined.pattern = "^(?:" + combined.pattern + ")$"
	}
	regex, err := compilePOSIX(combined, regexOptions{ignoreCase: settings.ignoreCase, leftmostFirst: settings.syntax == grepPerl})
	if err != nil {
		return nil, err
	}
	return &grepMatcher{regex: regex, word: settings.word && !settings.line}, nil
}

func translateGrepPattern(pattern string, settings *grepSettings) (translation, error) {
	switch settings.syntax {
	case grepFixed:
		return translation{pattern: regexp.QuoteMeta(pattern)}, nil
	case grepPerl:
		return literalTranslation(pattern)
	case grepExtended:
		return translatePOSIX(pattern, regexOptions{extended: true, grepQuirks: true})
	default:
		return translatePOSIX(pattern, regexOptions{})
	}
}

// matches reports whether line contains a match.
func (m *grepMatcher) matches(ctx context.Context, line string) bool {
	if m.regex == nil {
		return false
	}
	if !m.word {
		return m.regex.matchString(ctx, line)
	}
	return len(m.wordMatches(ctx, line, false)) > 0
}

// find returns the non-empty matches in line, for -o.
func (m *grepMatcher) find(ctx context.Context, line string) [][]int {
	if m.regex == nil {
		return nil
	}
	if m.word {
		return m.wordMatches(ctx, line, true)
	}
	var found [][]int
	for _, location := range m.regex.findAll(ctx, line) {
		if location[1] > location[0] {
			found = append(found, location[:2])
		}
	}
	return found
}

// wordMatches finds matches that have no word character on either side,
// trying shorter matches at the same start and then later starts, as GNU
// grep -w does; every attempt sees the whole line as context.
func (m *grepMatcher) wordMatches(ctx context.Context, line string, all bool) [][]int {
	var found [][]int
	for start := 0; start <= len(line); {
		location := m.regex.findFrom(ctx, line, start)
		if location == nil {
			break
		}
		begin, end := location[0], location[1]
		next := begin + 1
		if begin < len(line) {
			next = begin + runeLength(line[begin:])
		}
		matchedEnd := m.wordEnd(ctx, line, begin, end)
		if matchedEnd < 0 {
			start = next
			continue
		}
		if matchedEnd > begin {
			found = append(found, []int{begin, matchedEnd})
		} else if !all {
			return [][]int{{begin, begin}}
		}
		if !all {
			return found
		}
		start = max(matchedEnd, next)
	}
	return found
}

// wordEnd is the longest end at or before end where line[begin:end] is a
// whole match with non-word characters around it, or -1.
func (m *grepMatcher) wordEnd(ctx context.Context, line string, begin, end int) int {
	if begin > 0 && isWordByte(line[begin-1]) {
		return -1
	}
	for candidate := end; candidate >= begin; candidate-- {
		if ctx.Err() != nil {
			return -1
		}
		if candidate < len(line) && isWordByte(line[candidate]) {
			continue
		}
		if candidate == end || m.regex.matchesExactly(line, begin, candidate) {
			return candidate
		}
	}
	return -1
}

func isWordByte(character byte) bool {
	return isASCIILetter(character) || isDigit(character) || character == '_'
}

// grepSearcher runs the search over all inputs and owns the output state.
type grepSearcher struct {
	settings   *grepSettings
	matcher    *grepMatcher
	invocation *Invocation
	output     *bufio.Writer
	// withFilename is decided once from the operands.
	withFilename bool
	// printedLines says some line was printed, so a later non-adjacent
	// context group needs a "--" separator.
	printedLines bool
}

// grepInput is one file to search: the name grep prints and how to read it.
type grepInput struct {
	name, path string
	stdin      bool
}

// searchAll searches every operand and returns grep's exit status.
func (g *grepSearcher) searchAll(ctx context.Context, operands []string) (ExitError, error) {
	inputs, failed, err := g.collectInputs(ctx, operands)
	if err != nil {
		return 0, err
	}
	selected := false
	for _, input := range inputs {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		found, readFailed, err := g.searchInput(ctx, input)
		if err != nil {
			return 0, err
		}
		failed = failed || readFailed
		selected = selected || found
		if found && g.settings.quiet {
			return 0, nil
		}
	}
	switch {
	case failed && (!g.settings.quiet || !selected):
		return 2, nil
	case selected:
		return 0, nil
	}
	return 1, nil
}

// collectInputs expands the operands into files, walking directories with
// -r; failed reports an operand that could not be used.
func (g *grepSearcher) collectInputs(ctx context.Context, operands []string) ([]grepInput, bool, error) {
	implicit := len(operands) == 0 && g.settings.recursive
	switch {
	case implicit:
		operands = []string{"."}
	case len(operands) == 0:
		operands = []string{"-"}
	}
	var inputs []grepInput
	failed, directories := false, 0
	for _, operand := range operands {
		expanded, isDirectory, err := g.operandInputs(ctx, operand, implicit)
		if err != nil {
			return nil, false, err
		}
		if isDirectory {
			directories++
			failed = failed || !g.settings.recursive
		}
		inputs = append(inputs, expanded...)
	}
	g.withFilename = g.settings.filenames == 1 || (g.settings.filenames == 0 && (len(operands) > 1 || directories > 0))
	return inputs, failed, nil
}

// operandInputs is the input list of one operand: stdin, the file, or with
// -r the files below a directory (without -r a directory is reported).
func (g *grepSearcher) operandInputs(ctx context.Context, operand string, implicit bool) ([]grepInput, bool, error) {
	if operand == "-" {
		return []grepInput{{name: "(standard input)", stdin: true}}, false, nil
	}
	expanded, isDirectory, err := g.expandOperand(ctx, operand, implicit)
	switch {
	case err != nil:
		return nil, false, err
	case isDirectory && !g.settings.recursive:
		g.complain("%s: %s", operand, reasonIsDirectory)
		return nil, true, nil
	case isDirectory:
		return expanded, true, nil
	case g.excluded(operand):
		return nil, false, nil
	}
	return []grepInput{{name: operand, path: operand}}, false, nil
}

// expandOperand lists the files below a directory operand for -r; it
// returns nil for an operand that is not a directory.
func (g *grepSearcher) expandOperand(ctx context.Context, operand string, implicit bool) ([]grepInput, bool, error) {
	resolved, err := Resolve(g.invocation.Directory, operand)
	if err != nil {
		return nil, false, nil //nolint:nilerr // an invalid path is reported as a missing file when it is read
	}
	files, err := g.invocation.Storage.ListFiles(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("list files: %w", err)
	}
	prefix := resolved + "/"
	if resolved == "" {
		prefix = ""
	}
	isDirectory := false
	var inputs []grepInput
	for _, file := range files {
		if !strings.HasPrefix(file.Path, prefix) || file.Path == resolved {
			continue
		}
		isDirectory = true
		relative := strings.TrimPrefix(file.Path, prefix)
		if !g.settings.recursive || g.excludedDirectory(relative) || g.excluded(path.Base(relative)) {
			continue
		}
		name := strings.TrimSuffix(operand, "/") + "/" + relative
		if implicit {
			name = relative
		}
		inputs = append(inputs, grepInput{name: name, path: name})
	}
	return inputs, isDirectory, nil
}

// excluded applies --include and --exclude to a file's base name like GNU:
// the last matching option decides; when none matches, the file is skipped
// only if the first option is an --include.
func (g *grepSearcher) excluded(name string) bool {
	return excludedByRules(g.settings.fileRules, path.Base(name))
}

func excludedByRules(rules []grepFileRule, base string) bool {
	for index := len(rules) - 1; index >= 0; index-- {
		if matched, _ := path.Match(rules[index].pattern, base); matched {
			return !rules[index].include
		}
	}
	return len(rules) > 0 && rules[0].include
}

// excludedDirectory applies --exclude-dir to every directory of a path
// below the operand.
func (g *grepSearcher) excludedDirectory(relative string) bool {
	parts := strings.Split(relative, "/")
	for _, directory := range parts[:len(parts)-1] {
		for _, pattern := range g.settings.excludeDir {
			if matched, _ := path.Match(pattern, directory); matched {
				return true
			}
		}
	}
	return false
}

func (g *grepSearcher) complain(format string, arguments ...any) {
	if !g.settings.noMessages {
		grepTool.complain(g.invocation, format, arguments...)
	}
}

// searchInput searches one file; found says a line was selected, failed
// that the file could not be read.
func (g *grepSearcher) searchInput(ctx context.Context, input grepInput) (bool, bool, error) {
	name := input.path
	if input.stdin {
		name = "-"
	}
	reader, err := openOperand(ctx, g.invocation, name)
	if reason, ok := operandReason(err); ok {
		g.complain("%s: %s", input.name, reason)
		return false, true, nil
	}
	if err != nil {
		return false, false, err
	}
	lines := newLineReader(reader)
	if g.settings.nullData {
		lines.terminator = 0
	}
	// With -z a NUL byte ends a line, so it does not make the input binary.
	binary := !g.settings.text && !g.settings.nullData && lines.startsBinary()
	if binary && g.settings.skipBinary {
		return false, false, nil
	}
	found, err := g.searchLines(ctx, input.name, lines, binary)
	return found, false, err
}

// grepScan is the state of searching one file.
type grepScan struct {
	name string
	// printsLines is false in -c, -l, -L and -q modes; printLines also
	// for a binary file.
	printsLines, printLines bool
	binary                  bool
	selected                int
	lastPrinted             int
	afterRemaining          int
	// recent are the last lines not selected, for -B.
	recent []grepLine
	// stop ends the search of the file (-q, -l, -m, a binary match).
	stop bool
}

// grepLine is a line with its 0-based index.
type grepLine struct {
	index int
	text  string
}

// searchLines streams the lines of one file, prints the selected ones in
// the chosen mode and reports whether any line was selected. It stops
// reading as soon as the answer is known (-q, -l, -m).
func (g *grepSearcher) searchLines(ctx context.Context, name string, lines *lineReader, binary bool) (bool, error) {
	printsLines := !g.settings.quiet && !g.settings.count && g.settings.listing == listNone
	scan := &grepScan{name: name, printsLines: printsLines, printLines: printsLines && !binary, binary: binary, lastPrinted: -1}
	for index := 0; !scan.stop; index++ {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		text, _, err := lines.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return false, err
		}
		if !scan.binary && !g.settings.text && strings.IndexByte(text, 0) >= 0 {
			// A NUL after the first buffer makes the rest binary, as in GNU.
			scan.binary, scan.printLines = true, false
		}
		g.scanLine(ctx, scan, grepLine{index: index, text: text})
	}
	g.printSummary(name, scan.selected)
	return scan.selected > 0, nil
}

// scanLine decides one line: selected, context or nothing.
func (g *grepSearcher) scanLine(ctx context.Context, scan *grepScan, line grepLine) {
	settings := g.settings
	if settings.maximum >= 0 && scan.selected >= settings.maximum {
		scan.stop = !g.trailingContext(scan, line.index, line.text)
		return
	}
	if g.matcher.matches(ctx, line.text) == settings.invert {
		if !g.trailingContext(scan, line.index, line.text) && settings.before > 0 {
			scan.recent = append(scan.recent, line)
			if len(scan.recent) > settings.before {
				scan.recent = scan.recent[1:]
			}
		}
		return
	}
	scan.selected++
	switch {
	case settings.quiet || settings.listing != listNone && !settings.count:
		scan.stop = true
	case scan.binary && scan.printsLines:
		g.binaryMatches(scan.name)
		scan.stop = true
	case scan.printLines:
		g.printBefore(scan)
		g.printSelected(ctx, scan, line.index, line.text)
		scan.afterRemaining = settings.after
	}
}

// trailingContext prints a line of -A context if one is due.
func (g *grepSearcher) trailingContext(scan *grepScan, index int, text string) bool {
	if !scan.printLines || scan.afterRemaining == 0 {
		return false
	}
	g.printLine(scan, index, text, '-')
	scan.afterRemaining--
	return true
}

// binaryMatches reports a match in a binary file on stderr, after the lines
// already printed so the order matches a terminal's.
func (g *grepSearcher) binaryMatches(name string) {
	if err := g.output.Flush(); err == nil {
		grepTool.complain(g.invocation, "%s: binary file matches", name)
	}
}

// printBefore prints the -B context of a selected line.
func (g *grepSearcher) printBefore(scan *grepScan) {
	for _, line := range scan.recent {
		if line.index > scan.lastPrinted {
			g.printLine(scan, line.index, line.text, '-')
		}
	}
	scan.recent = scan.recent[:0]
}

// printSelected prints a selected line, or its matches with -o.
func (g *grepSearcher) printSelected(ctx context.Context, scan *grepScan, index int, text string) {
	if !g.settings.onlyMatches {
		g.printLine(scan, index, text, ':')
		return
	}
	if g.settings.invert {
		return
	}
	g.startLine(scan, index)
	for _, location := range g.matcher.find(ctx, text) {
		g.printLine(scan, index, text[location[0]:location[1]], ':')
	}
}

// startLine records line index as output, preceded by "--" when a context
// option was given and the line does not continue the previous group. -o
// suppresses the text of context lines, not their place in a group.
func (g *grepSearcher) startLine(scan *grepScan, index int) {
	if g.settings.context && g.printedLines && (scan.lastPrinted < 0 || index > scan.lastPrinted+1) {
		fmt.Fprint(g.output, "--\n")
	}
	scan.lastPrinted = index
	g.printedLines = true
}

// printLine writes one output line with its prefixes. Write errors surface
// at Flush.
func (g *grepSearcher) printLine(scan *grepScan, index int, text string, separator byte) {
	g.startLine(scan, index)
	if g.settings.onlyMatches && separator == '-' {
		return
	}
	var line strings.Builder
	if g.withFilename {
		line.WriteString(scan.name)
		line.WriteByte(g.nameEnd(separator))
	}
	if g.settings.lineNumbers {
		line.WriteString(strconv.Itoa(index + 1))
		line.WriteByte(separator)
	}
	line.WriteString(text)
	line.WriteByte(g.lineEnd())
	fmt.Fprint(g.output, line.String())
}

// printSummary prints the -c count and the -l / -L name of one file.
func (g *grepSearcher) printSummary(name string, selectedCount int) {
	settings := g.settings
	if settings.quiet {
		return
	}
	if settings.count && settings.listing == listNone {
		if g.withFilename {
			fmt.Fprintf(g.output, "%s%c", name, g.nameEnd(':'))
		}
		fmt.Fprintf(g.output, "%d\n", selectedCount)
	}
	if settings.listing == listMatching && selectedCount > 0 || settings.listing == listNonMatching && selectedCount == 0 {
		fmt.Fprintf(g.output, "%s%c", name, g.nameEnd('\n'))
	}
}

// nameEnd is the byte after a printed file name: separator, or NUL with -Z.
func (g *grepSearcher) nameEnd(separator byte) byte {
	if g.settings.nullNames {
		return 0
	}
	return separator
}

// lineEnd is the line terminator of input and output: NUL with -z.
func (g *grepSearcher) lineEnd() byte {
	if g.settings.nullData {
		return 0
	}
	return '\n'
}
