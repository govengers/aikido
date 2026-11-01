package shell

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
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
		Summary: "search text (-r -n -l -L -c -i -v -w -x -o -q -s -E -F -P -e -f -A -B -C -m -h -H --include --exclude --exclude-dir)",
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
	filenames                      int
	listing                        grepListing
	maximum                        int
	before, after                  int
	recursive                      bool
	includes, excludes, excludeDir []string
	text, skipBinary               bool
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
			switch option.key {
			case "before-context":
				settings.before = value
			case "after-context":
				settings.after = value
			default:
				contextLines = value
			}
		case "include":
			settings.includes = append(settings.includes, option.value)
		case "exclude":
			settings.excludes = append(settings.excludes, option.value)
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
	regex *regexp.Regexp
	// whole matches a candidate that must be a complete match, for -w.
	whole *regexp.Regexp
	word  bool
}

// newGrepMatcher compiles all patterns into one alternation.
func newGrepMatcher(settings *grepSettings) (*grepMatcher, error) {
	if len(settings.patterns) == 0 {
		return &grepMatcher{}, nil
	}
	alternatives := make([]string, 0, len(settings.patterns))
	for _, pattern := range settings.patterns {
		translated, err := translateGrepPattern(pattern, settings)
		if err != nil {
			return nil, err
		}
		alternatives = append(alternatives, "(?:"+translated+")")
	}
	combined := strings.Join(alternatives, "|")
	if settings.line {
		combined = "^(?:" + combined + ")$"
	}
	longest := settings.syntax != grepPerl
	regex, err := compileGo(combined, settings.ignoreCase, longest)
	if err != nil {
		return nil, err
	}
	matcher := &grepMatcher{regex: regex, word: settings.word && !settings.line}
	if matcher.word {
		if matcher.whole, err = compileGo("^(?:"+combined+")$", settings.ignoreCase, longest); err != nil {
			return nil, err
		}
	}
	return matcher, nil
}

func translateGrepPattern(pattern string, settings *grepSettings) (string, error) {
	switch settings.syntax {
	case grepFixed:
		return regexp.QuoteMeta(pattern), nil
	case grepPerl:
		if _, err := regexp.Compile(pattern); err != nil {
			return "", &regexError{"Invalid regular expression"}
		}
		return pattern, nil
	case grepExtended:
		return translatePOSIX(pattern, regexOptions{extended: true, grepQuirks: true})
	default:
		return translatePOSIX(pattern, regexOptions{})
	}
}

// matches reports whether line contains a match.
func (m *grepMatcher) matches(line string) bool {
	if m.regex == nil {
		return false
	}
	if !m.word {
		return m.regex.MatchString(line)
	}
	return len(m.wordMatches(line, false)) > 0
}

// find returns the non-empty matches in line, for -o.
func (m *grepMatcher) find(line string) [][]int {
	if m.regex == nil {
		return nil
	}
	if m.word {
		return m.wordMatches(line, true)
	}
	var found [][]int
	for _, location := range m.regex.FindAllStringIndex(line, -1) {
		if location[1] > location[0] {
			found = append(found, location)
		}
	}
	return found
}

// wordMatches finds matches that have no word character on either side,
// trying shorter matches at the same start and then later starts, as GNU
// grep -w does.
// ponytail: later starts search a suffix of the line, so ^ and \b see the
// cut as the line start; GNU keeps the context.
func (m *grepMatcher) wordMatches(line string, all bool) [][]int {
	var found [][]int
	for start := 0; start <= len(line); {
		location := m.regex.FindStringIndex(line[start:])
		if location == nil {
			break
		}
		begin, end := start+location[0], start+location[1]
		matchedEnd := m.wordEnd(line, begin, end)
		if matchedEnd < 0 {
			start = begin + 1
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
		start = max(matchedEnd, begin+1)
	}
	return found
}

// wordEnd is the longest end at or before end where line[begin:end] is a
// whole match with non-word characters around it, or -1.
func (m *grepMatcher) wordEnd(line string, begin, end int) int {
	if begin > 0 && isWordByte(line[begin-1]) {
		return -1
	}
	for candidate := end; candidate >= begin; candidate-- {
		if candidate < len(line) && isWordByte(line[candidate]) {
			continue
		}
		if candidate == end || m.whole.MatchString(line[begin:candidate]) {
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
	case failed && !(g.settings.quiet && selected):
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
	if len(operands) == 0 {
		operands = []string{"-"}
		if implicit {
			operands = []string{"."}
		}
	}
	var inputs []grepInput
	failed := false
	directories := 0
	for _, operand := range operands {
		if operand == "-" {
			inputs = append(inputs, grepInput{name: "(standard input)", stdin: true})
			continue
		}
		expanded, isDirectory, err := g.expandOperand(ctx, operand, implicit)
		if err != nil {
			return nil, false, err
		}
		if isDirectory {
			directories++
		}
		if expanded == nil && !isDirectory {
			if !g.excluded(operand) {
				inputs = append(inputs, grepInput{name: operand, path: operand})
			}
			continue
		}
		if isDirectory && !g.settings.recursive {
			g.complain("%s: %s", operand, reasonIsDirectory)
			failed = true
		}
		inputs = append(inputs, expanded...)
	}
	g.withFilename = g.settings.filenames == 1 || (g.settings.filenames == 0 && (len(operands) > 1 || directories > 0))
	return inputs, failed, nil
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

// excluded applies --include and --exclude to a file's base name.
func (g *grepSearcher) excluded(name string) bool {
	base := path.Base(name)
	for _, pattern := range g.settings.excludes {
		if matched, _ := path.Match(pattern, base); matched {
			return true
		}
	}
	if len(g.settings.includes) == 0 {
		return false
	}
	for _, pattern := range g.settings.includes {
		if matched, _ := path.Match(pattern, base); matched {
			return false
		}
	}
	return true
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
	content, err := readOperand(ctx, g.invocation, name)
	if reason, ok := operandReason(err); ok {
		g.complain("%s: %s", input.name, reason)
		return false, true, nil
	}
	if err != nil {
		return false, false, err
	}
	binary := !g.settings.text && bytes.IndexByte(content, 0) >= 0
	if binary && g.settings.skipBinary {
		return false, false, nil
	}
	lines, _ := splitLines(string(content))
	return g.searchLines(input.name, lines, binary), false, nil
}

// searchLines prints the selected lines of one file in the chosen mode and
// reports whether any line was selected.
func (g *grepSearcher) searchLines(name string, lines []string, binary bool) bool {
	settings := g.settings
	printLines := !settings.quiet && !settings.count && settings.listing == listNone
	selectedCount := 0
	lastPrinted := -1
	afterRemaining := 0
	for index, text := range lines {
		if settings.maximum >= 0 && selectedCount >= settings.maximum {
			if printLines && !binary && afterRemaining > 0 {
				g.printLine(name, index, text, '-', &lastPrinted)
				afterRemaining--
				continue
			}
			break
		}
		if g.matcher.matches(text) == settings.invert {
			if printLines && !binary && afterRemaining > 0 {
				g.printLine(name, index, text, '-', &lastPrinted)
				afterRemaining--
			}
			continue
		}
		selectedCount++
		if settings.quiet || settings.listing != listNone && !settings.count {
			break
		}
		if !printLines {
			continue
		}
		if binary {
			g.binaryMatches(name)
			break
		}
		g.printBefore(name, lines, index, &lastPrinted)
		g.printSelected(name, index, text, &lastPrinted)
		afterRemaining = settings.after
	}
	g.printSummary(name, selectedCount)
	return selectedCount > 0
}

// binaryMatches reports a match in a binary file on stderr, after the lines
// already printed so the order matches a terminal's.
func (g *grepSearcher) binaryMatches(name string) {
	if err := g.output.Flush(); err == nil {
		grepTool.complain(g.invocation, "%s: binary file matches", name)
	}
}

// printBefore prints the -B context of a selected line.
func (g *grepSearcher) printBefore(name string, lines []string, index int, lastPrinted *int) {
	for context := max(index-g.settings.before, *lastPrinted+1); context < index; context++ {
		g.printLine(name, context, lines[context], '-', lastPrinted)
	}
}

// printSelected prints a selected line, or its matches with -o.
func (g *grepSearcher) printSelected(name string, index int, text string, lastPrinted *int) {
	if !g.settings.onlyMatches {
		g.printLine(name, index, text, ':', lastPrinted)
		return
	}
	if g.settings.invert {
		return
	}
	for _, location := range g.matcher.find(text) {
		g.printLine(name, index, text[location[0]:location[1]], ':', lastPrinted)
	}
}

// printLine writes one output line with its prefixes, preceded by "--" when
// it does not continue the previous group.
func (g *grepSearcher) printLine(name string, index int, text string, separator byte, lastPrinted *int) {
	contextual := g.settings.before > 0 || g.settings.after > 0
	if contextual && g.printedLines && (*lastPrinted < 0 || index > *lastPrinted+1) && !g.settings.onlyMatches {
		g.output.WriteString("--\n")
	}
	if g.withFilename {
		g.output.WriteString(name)
		g.output.WriteByte(separator)
	}
	if g.settings.lineNumbers {
		g.output.WriteString(strconv.Itoa(index + 1))
		g.output.WriteByte(separator)
	}
	g.output.WriteString(text)
	g.output.WriteByte('\n')
	*lastPrinted = index
	g.printedLines = true
}

// printSummary prints the -c count and the -l / -L name of one file.
func (g *grepSearcher) printSummary(name string, selectedCount int) {
	settings := g.settings
	if settings.quiet {
		return
	}
	if settings.count && settings.listing == listNone {
		if g.withFilename {
			fmt.Fprintf(g.output, "%s:", name)
		}
		fmt.Fprintf(g.output, "%d\n", selectedCount)
	}
	if settings.listing == listMatching && selectedCount > 0 || settings.listing == listNonMatching && selectedCount == 0 {
		fmt.Fprintf(g.output, "%s\n", name)
	}
}
