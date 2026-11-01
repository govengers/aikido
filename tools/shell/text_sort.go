package shell

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

var sortTool = &textTool{
	name:        "sort",
	hint:        "Try 'sort --help' for more information.\n",
	usageStatus: 2,
	help: `Usage: sort [OPTION]... [FILE]...
Write sorted concatenation of all FILE(s) to standard output, in byte order (LC_ALL=C).
  -b, --ignore-leading-blanks  ignore leading blanks
  -c, --check                  check whether input is sorted; do not sort
  -f, --ignore-case            fold lower case to upper case characters
  -k, --key=KEYDEF             sort via a key; KEYDEF is F[.C][OPTS][,F[.C][OPTS]], OPTS from bfnr
  -n, --numeric-sort           compare according to string numerical value
  -o, --output=FILE            write result to FILE instead of standard output
  -r, --reverse                reverse the result of comparisons
  -s, --stable                 disable last-resort comparison
  -t, --field-separator=SEP    use SEP instead of non-blank to blank transition
  -u, --unique                 output only the first of an equal run
`,
}

var sortParser = &optionParser{specs: []optionSpec{
	{short: 'b', long: "ignore-leading-blanks"},
	{short: 'c', long: "check"},
	{short: 'f', long: "ignore-case"},
	{short: 'k', long: "key", argument: requiredArgument},
	{short: 'n', long: "numeric-sort"},
	{short: 'o', long: "output", argument: requiredArgument},
	{short: 'r', long: "reverse"},
	{short: 's', long: "stable"},
	{short: 't', long: "field-separator", argument: requiredArgument},
	{short: 'u', long: "unique"},
	helpOption,
}}

func sortCommand() Command {
	return Command{Name: "sort", Summary: "sort lines in byte order (-n -r -u -f -b -s -c -k F[.C][,F[.C]] -t SEP -o FILE)", Run: runSort}
}

// sortOrdering are the per-key ordering options.
type sortOrdering struct {
	blanksAtStart, blanksAtEnd, fold, numeric, reverse bool
}

func (o sortOrdering) isDefault() bool { return o == sortOrdering{} }

// sortKey is one -k field range; fields and characters are 0-based, endField
// is -1 for the end of the line and endCharacter 0 for the end of the field.
type sortKey struct {
	startField, startCharacter int
	endField, endCharacter     int
	ordering                   sortOrdering
}

// sortSettings is a parsed sort command line.
type sortSettings struct {
	global sortOrdering
	keys   []sortKey
	// separator is the -t byte, or -1 for blank-to-non-blank transitions.
	separator      int
	unique, stable bool
	check          bool
	output         string
}

func runSort(ctx context.Context, invocation *Invocation) error {
	line, err := sortTool.parseOptions(invocation, sortParser)
	if line.done {
		return err
	}
	settings, err := parseSortSettings(invocation, line.options, line.operands)
	if err != nil {
		return err
	}
	var lines []string
	var budget inputBudget
	for _, name := range operandsOrStdin(line.operands) {
		content, readErr := readOperand(ctx, invocation, name)
		if reason, ok := operandReason(readErr); ok {
			if reason == reasonIsDirectory {
				return sortTool.fatal(invocation, 2, "read failed: %s: %s", name, reason)
			}
			return sortTool.fatal(invocation, 2, "cannot read: %s: %s", name, reason)
		}
		if readErr != nil {
			return readErr
		}
		if err := budget.charge(len(content)); err != nil {
			return err
		}
		fileLines, _ := splitLines(string(content))
		lines = append(lines, fileLines...)
	}
	if settings.check {
		return checkSorted(invocation, settings, operandsOrStdin(line.operands)[0], lines)
	}
	slices.SortStableFunc(lines, settings.compare)
	if settings.unique {
		lines = slices.CompactFunc(lines, func(left, right string) bool { return settings.compareKeys(left, right) == 0 })
	}
	return writeSorted(ctx, invocation, settings, joinLines(lines))
}

// parseSortSettings reads the options of one sort command line.
func parseSortSettings(invocation *Invocation, options []parsedOption, operands []string) (sortSettings, error) {
	settings := sortSettings{separator: -1}
	for _, option := range options {
		switch option.key {
		case "ignore-leading-blanks":
			settings.global.blanksAtStart, settings.global.blanksAtEnd = true, true
		case "check":
			settings.check = true
		case "ignore-case":
			settings.global.fold = true
		case "numeric-sort":
			settings.global.numeric = true
		case "reverse":
			settings.global.reverse = true
		case "stable":
			settings.stable = true
		case "unique":
			settings.unique = true
		case "output":
			settings.output = option.value
		case "field-separator":
			if len(option.value) != 1 {
				return settings, sortTool.fatal(invocation, 2, "multi-character tab '%s'", option.value)
			}
			settings.separator = int(option.value[0])
		case "key":
			key, message := parseSortKey(option.value)
			if message != "" {
				return settings, sortTool.fatal(invocation, 2, "%s", message)
			}
			settings.keys = append(settings.keys, key)
		}
	}
	for index := range settings.keys {
		if settings.keys[index].ordering.isDefault() {
			settings.keys[index].ordering = settings.global
		}
	}
	if len(settings.keys) == 0 {
		settings.keys = []sortKey{{endField: -1, ordering: settings.global}}
	}
	if settings.check && len(operands) > 1 {
		return settings, sortTool.usageFailure(invocation, fmt.Sprintf("extra operand '%s' not allowed with -c", operands[1]))
	}
	return settings, nil
}

// parseSortKey reads F[.C][OPTS][,F[.C][OPTS]]; the message is GNU's when
// the definition is invalid.
func parseSortKey(definition string) (sortKey, string) {
	key := sortKey{endField: -1}
	invalid := func(reason string) (sortKey, string) {
		return sortKey{}, fmt.Sprintf("%s: invalid field specification '%s'", reason, definition)
	}
	field, rest, ok := leadingNumber(definition)
	if !ok {
		return sortKey{}, fmt.Sprintf("invalid number at field start: invalid count at start of '%s'", definition)
	}
	if field == 0 {
		return invalid("field number is zero")
	}
	key.startField = field - 1
	if strings.HasPrefix(rest, ".") {
		character, after, ok := leadingNumber(rest[1:])
		if !ok {
			return sortKey{}, fmt.Sprintf("invalid number after '.': invalid count at start of '%s'", rest[1:])
		}
		if character == 0 {
			return invalid("character offset is zero")
		}
		key.startCharacter, rest = character-1, after
	}
	rest = key.ordering.parseLetters(rest, true)
	if strings.HasPrefix(rest, ",") {
		var message string
		if rest, message = key.parseEnd(rest[1:], definition); message != "" {
			return sortKey{}, message
		}
	}
	if rest != "" {
		return invalid("stray character in field spec")
	}
	return key, ""
}

// parseEnd reads the F[.C][OPTS] after a key's comma and returns the rest.
func (k *sortKey) parseEnd(text, definition string) (string, string) {
	field, rest, ok := leadingNumber(text)
	if !ok {
		return "", fmt.Sprintf("invalid number after ',': invalid count at start of '%s'", text)
	}
	if field == 0 {
		return "", fmt.Sprintf("field number is zero: invalid field specification '%s'", definition)
	}
	k.endField = field - 1
	if strings.HasPrefix(rest, ".") {
		character, after, ok := leadingNumber(rest[1:])
		if !ok {
			return "", fmt.Sprintf("invalid number after '.': invalid count at start of '%s'", rest[1:])
		}
		k.endCharacter, rest = character, after
	}
	return k.ordering.parseLetters(rest, false), ""
}

// parseLetters consumes the ordering letters b f n r; start says whether a
// "b" applies to the start or the end of the key.
func (o *sortOrdering) parseLetters(text string, start bool) string {
	for text != "" {
		switch text[0] {
		case 'b':
			if start {
				o.blanksAtStart = true
			} else {
				o.blanksAtEnd = true
			}
		case 'f':
			o.fold = true
		case 'n':
			o.numeric = true
		case 'r':
			o.reverse = true
		default:
			return text
		}
		text = text[1:]
	}
	return text
}

// leadingNumber splits a leading decimal number off text.
func leadingNumber(text string) (int, string, bool) {
	end := 0
	for end < len(text) && isDigit(text[end]) {
		end++
	}
	if end == 0 {
		return 0, text, false
	}
	value, err := strconv.Atoi(text[:end])
	if err != nil {
		return 0, text, false
	}
	return value, text[end:], true
}

// compare orders two lines like GNU sort: by the keys, then, unless -s or
// -u, by the whole line in byte order (reversed with a global -r).
func (s *sortSettings) compare(left, right string) int {
	if result := s.compareKeys(left, right); result != 0 || s.stable || s.unique {
		return result
	}
	result := strings.Compare(left, right)
	if s.global.reverse {
		return -result
	}
	return result
}

// compareKeys compares the keys only; without -k the whole line is the key.
func (s *sortSettings) compareKeys(left, right string) int {
	for _, key := range s.keys {
		leftKey := s.extract(left, key)
		rightKey := s.extract(right, key)
		if result := compareOrdered(leftKey, rightKey, key.ordering); result != 0 {
			return result
		}
	}
	return 0
}

// compareOrdered compares two key texts under one ordering.
func compareOrdered(left, right string, ordering sortOrdering) int {
	var result int
	switch {
	case ordering.numeric:
		result = compareNumeric(left, right)
	case ordering.fold:
		result = strings.Compare(upperASCII(left), upperASCII(right))
	default:
		result = strings.Compare(left, right)
	}
	if ordering.reverse {
		return -result
	}
	return result
}

// upperASCII folds a-z only, like toupper in the C locale.
func upperASCII(text string) string {
	return strings.Map(func(character rune) rune {
		if character >= 'a' && character <= 'z' {
			return character - 'a' + 'A'
		}
		return character
	}, text)
}

func isSortBlank(character byte) bool {
	return character == ' ' || character == '\t' || character == '\n'
}

// extract is the text of key in line, after GNU's begfield and limfield.
func (s *sortSettings) extract(line string, key sortKey) string {
	start := s.fieldStart(line, key.startField)
	if key.ordering.blanksAtStart {
		for start < len(line) && isSortBlank(line[start]) {
			start++
		}
	}
	start = min(len(line), start+key.startCharacter)
	end := len(line)
	if key.endField >= 0 {
		end = s.fieldLimit(line, key)
	}
	if end <= start {
		return ""
	}
	return line[start:end]
}

// fieldStart is the offset of field (0-based); a field includes its leading
// blanks when there is no -t.
func (s *sortSettings) fieldStart(line string, field int) int {
	position := 0
	for ; position < len(line) && field > 0; field-- {
		if s.separator < 0 {
			position = skipBlankThenWord(line, position)
			continue
		}
		if position = s.skipToSeparator(line, position); position < len(line) {
			position++
		}
	}
	return position
}

// fieldLimit is the end offset of the key's end position.
func (s *sortSettings) fieldLimit(line string, key sortKey) int {
	field, character := key.endField, key.endCharacter
	if character == 0 {
		field++
	}
	position := 0
	for ; position < len(line) && field > 0; field-- {
		if s.separator < 0 {
			position = skipBlankThenWord(line, position)
			continue
		}
		position = s.skipToSeparator(line, position)
		if position < len(line) && (field > 1 || character != 0) {
			position++
		}
	}
	if character != 0 {
		if key.ordering.blanksAtEnd {
			for position < len(line) && isSortBlank(line[position]) {
				position++
			}
		}
		position = min(len(line), position+character)
	}
	return position
}

// skipToSeparator advances to the next -t separator or the end.
func (s *sortSettings) skipToSeparator(line string, position int) int {
	for position < len(line) && int(line[position]) != s.separator {
		position++
	}
	return position
}

func skipBlankThenWord(line string, position int) int {
	for position < len(line) && isSortBlank(line[position]) {
		position++
	}
	for position < len(line) && !isSortBlank(line[position]) {
		position++
	}
	return position
}

// compareNumeric compares the leading numbers of two keys like sort -n:
// optional blanks, an optional "-", digits and one "."; no number is zero.
func compareNumeric(left, right string) int {
	leftNegative, leftInteger, leftFraction := parseSortNumber(left)
	rightNegative, rightInteger, rightFraction := parseSortNumber(right)
	if leftNegative != rightNegative {
		if leftNegative {
			return -1
		}
		return 1
	}
	result := cmpLength(leftInteger, rightInteger)
	if result == 0 {
		result = strings.Compare(leftInteger, rightInteger)
	}
	if result == 0 {
		result = strings.Compare(leftFraction, rightFraction)
	}
	if leftNegative {
		return -result
	}
	return result
}

func cmpLength(left, right string) int {
	switch {
	case len(left) < len(right):
		return -1
	case len(left) > len(right):
		return 1
	}
	return 0
}

// parseSortNumber returns the sign and the digits without leading zeros of
// the integer part and without trailing zeros of the fraction; zero is never
// negative.
func parseSortNumber(text string) (bool, string, string) {
	position := 0
	for position < len(text) && isSortBlank(text[position]) {
		position++
	}
	negative := position < len(text) && text[position] == '-'
	if negative {
		position++
	}
	start := position
	for position < len(text) && isDigit(text[position]) {
		position++
	}
	integer := strings.TrimLeft(text[start:position], "0")
	fraction := ""
	if position < len(text) && text[position] == '.' {
		position++
		fractionStart := position
		for position < len(text) && isDigit(text[position]) {
			position++
		}
		fraction = strings.TrimRight(text[fractionStart:position], "0")
	}
	if integer == "" && fraction == "" {
		negative = false
	}
	return negative, integer, fraction
}

// checkSorted is sort -c: report the first line out of order.
func checkSorted(invocation *Invocation, settings sortSettings, name string, lines []string) error {
	for index := 1; index < len(lines); index++ {
		result := settings.compare(lines[index-1], lines[index])
		if result > 0 || (settings.unique && result == 0) {
			sortTool.complain(invocation, "%s:%d: disorder: %s", name, index+1, lines[index])
			return ExitError(1)
		}
	}
	return nil
}

// writeSorted writes the result to stdout or the -o file.
func writeSorted(ctx context.Context, invocation *Invocation, settings sortSettings, text string) error {
	if settings.output == "" || settings.output == "-" {
		if _, err := fmt.Fprint(invocation.Stdout, text); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
		return nil
	}
	if err := storeOutput(ctx, invocation, settings.output, []byte(text)); err != nil {
		return sortTool.fatal(invocation, 2, "open failed: %s: %v", settings.output, err)
	}
	return nil
}
