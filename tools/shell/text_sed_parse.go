package shell

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// sedAddressKind is the form of one sed address.
type sedAddressKind int

const (
	addressLine sedAddressKind = iota
	addressLast
	addressRegex
	// addressStep is first~step.
	addressStep
	// addressRelative is the end address +N.
	addressRelative
	// addressMultiple is the end address ~N.
	addressMultiple
)

// sedAddress is one address of a command.
type sedAddress struct {
	kind sedAddressKind
	// number is the line, first~step's first, or N of +N and ~N.
	number int
	step   int
	regex  *sedRegex
}

// sedRegex is a compiled address or s regex; a nil regex means "//", the
// last regex used at run time.
type sedRegex struct {
	regex *posixRegexp
}

// replacementKind is one piece of an s replacement.
type replacementKind int

const (
	replaceLiteral replacementKind = iota
	replaceGroup
	// replaceCase is \U \L \E \u \l; caseOperation holds the letter.
	replaceCase
)

type replacementPart struct {
	kind          replacementKind
	literal       string
	group         int
	caseOperation byte
}

// sedSubstitution is a parsed s command.
type sedSubstitution struct {
	regex       *sedRegex
	replacement []replacementPart
	global      bool
	// occurrence is the N flag, 1 by default.
	occurrence int
	print      bool
}

// sedInstruction is one command of a sed program.
type sedInstruction struct {
	address1, address2 *sedAddress
	negate             bool
	name               byte
	// text is the text of a, i and c with its final newline, or the label
	// of : b t T.
	text     string
	exitCode int
	// target is the index of the command a branch jumps to, or of the "}"
	// closing a "{"; -1 for a branch to the end of the script.
	target       int
	substitution *sedSubstitution
	translation  map[rune]rune
}

// sedSource is one piece of script: a -e expression or a -f file.
type sedSource struct {
	text string
	// file is the -f file name, "" for an expression.
	file string
	// expression is the 1-based number of the -e expression.
	expression int
}

// sedScriptError is a script GNU sed would refuse; the message carries
// GNU's location prefix.
type sedScriptError struct{ message string }

func (e *sedScriptError) Error() string { return e.message }

// sedScriptParser compiles the sources into one program, like GNU sed compiling
// each -e and -f in turn with blocks open across them.
type sedScriptParser struct {
	extended bool
	source   sedSource
	text     string
	// position is the number of characters of text consumed so far.
	position int
	program  []sedInstruction
	// openBlocks are the indexes of the "{" commands not yet closed.
	openBlocks []int
}

// parseSedProgram compiles the sources; errors are *sedScriptError.
func parseSedProgram(sources []sedSource, extended bool) ([]sedInstruction, error) {
	parser := &sedScriptParser{extended: extended}
	for _, source := range sources {
		parser.source, parser.text, parser.position = source, source.text, 0
		for {
			done, err := parser.parseCommand()
			if err != nil {
				return nil, err
			}
			if done {
				break
			}
		}
	}
	if len(parser.openBlocks) > 0 {
		parser.position = 0
		return nil, parser.fail("unmatched `{'")
	}
	if err := parser.resolveLabels(); err != nil {
		return nil, err
	}
	return parser.program, nil
}

// fail reports message at the current position, as GNU locates it.
func (p *sedScriptParser) fail(message string) error {
	if p.source.file != "" {
		line := strings.Count(p.text[:min(p.position, len(p.text))], "\n") + 1
		return &sedScriptError{fmt.Sprintf("file %s line %d: %s", p.source.file, line, message)}
	}
	return &sedScriptError{fmt.Sprintf("-e expression #%d, char %d: %s", p.source.expression, p.position, message)}
}

const sedEOF = -1

// charByte is the byte of a character next returned (not sedEOF).
func charByte(character int) byte { return byte(character & 0xff) }

// errNoAddress says no address starts at the position.
var errNoAddress = errors.New("no address")

// next consumes one character, sedEOF at the end.
func (p *sedScriptParser) next() int {
	if p.position >= len(p.text) {
		return sedEOF
	}
	p.position++
	return int(p.text[p.position-1])
}

// back un-reads the last character next returned (not sedEOF).
func (p *sedScriptParser) back(character int) {
	if character != sedEOF {
		p.position--
	}
}

// nextNonBlank skips spaces and tabs and consumes the next character.
func (p *sedScriptParser) nextNonBlank() int {
	for {
		character := p.next()
		if character != ' ' && character != '\t' {
			return character
		}
	}
}

// parseCommand parses one command with its addresses; done is true at the
// end of the source.
func (p *sedScriptParser) parseCommand() (bool, error) {
	character := p.next()
	for character == ' ' || character == '\t' || character == '\n' || character == ';' {
		character = p.next()
	}
	if character == sedEOF {
		return true, nil
	}
	command := sedInstruction{target: -1}
	p.back(character)
	if err := p.parseAddresses(&command); err != nil {
		return false, err
	}
	character = p.nextNonBlank()
	for character == '!' {
		if command.negate {
			return false, p.fail("multiple `!'s")
		}
		command.negate = true
		character = p.nextNonBlank()
	}
	if character == sedEOF || character == '\n' || character == ';' {
		return false, p.fail("missing command")
	}
	command.name = charByte(character)
	if err := p.parseArguments(&command); err != nil {
		return false, err
	}
	p.program = append(p.program, command)
	return false, nil
}

// parseAddresses reads addr1[,addr2].
func (p *sedScriptParser) parseAddresses(command *sedInstruction) error {
	first, err := p.parseAddress(false)
	if errors.Is(err, errNoAddress) {
		return nil
	}
	if err != nil {
		return err
	}
	command.address1 = first
	character := p.nextNonBlank()
	if character != ',' {
		p.back(character)
		if first.kind == addressLine && first.number == 0 {
			return p.fail("invalid usage of line address 0")
		}
		return nil
	}
	for p.position < len(p.text) && (p.text[p.position] == ' ' || p.text[p.position] == '\t') {
		p.position++
	}
	second, err := p.parseAddress(true)
	if errors.Is(err, errNoAddress) {
		return p.fail("unexpected `,'")
	}
	if err != nil {
		return err
	}
	if first.kind == addressLine && first.number == 0 && second.kind != addressRegex {
		return p.fail("invalid usage of line address 0")
	}
	command.address2 = second
	return nil
}

// parseAddress reads one address; errNoAddress when none starts here.
func (p *sedScriptParser) parseAddress(second bool) (*sedAddress, error) {
	character := p.next()
	switch {
	case character == sedEOF:
		return nil, errNoAddress
	case isDigit(charByte(character)):
		p.back(character)
		number := p.readNumber()
		if p.position < len(p.text) && p.text[p.position] == '~' && !second {
			p.position++
			return &sedAddress{kind: addressStep, number: number, step: p.readNumber()}, nil
		}
		return &sedAddress{kind: addressLine, number: number}, nil
	case character == '$':
		return &sedAddress{kind: addressLast}, nil
	case second && (character == '+' || character == '~'):
		if p.position >= len(p.text) || !isDigit(p.text[p.position]) {
			p.back(character)
			return nil, errNoAddress
		}
		kind := addressRelative
		if character == '~' {
			kind = addressMultiple
		}
		return &sedAddress{kind: kind, number: p.readNumber()}, nil
	case character == '/' || character == '\\':
		return p.parseRegexAddress(character)
	}
	p.back(character)
	return nil, errNoAddress
}

// parseRegexAddress reads /re/ or \cREc with its I and M flags.
func (p *sedScriptParser) parseRegexAddress(character int) (*sedAddress, error) {
	delimiter := character
	if character == '\\' {
		delimiter = p.next()
		if delimiter == sedEOF || delimiter == '\n' {
			return nil, p.fail("unexpected `,'")
		}
	}
	pattern, ok := p.readDelimited(charByte(delimiter), true)
	if !ok {
		return nil, p.fail("unterminated address regex")
	}
	ignoreCase, multiline := false, false
	for {
		flag := p.next()
		if flag == 'I' {
			ignoreCase = true
			continue
		}
		if flag == 'M' {
			multiline = true
			continue
		}
		p.back(flag)
		break
	}
	regex, err := p.compileRegex(pattern, ignoreCase, multiline)
	if err != nil {
		return nil, err
	}
	return &sedAddress{kind: addressRegex, regex: regex}, nil
}

// readNumber reads decimal digits at the position (0 when there are none).
func (p *sedScriptParser) readNumber() int {
	start := p.position
	for p.position < len(p.text) && isDigit(p.text[p.position]) {
		p.position++
	}
	value, err := strconv.Atoi(p.text[start:p.position])
	if err != nil {
		return 0
	}
	return value
}

// readDelimited reads up to an unescaped delimiter. In a regex a bracket
// expression may contain the delimiter, and "\delimiter" becomes the bare
// delimiter; "\n" stays for the regex translator. ok is false when the
// delimiter is missing.
func (p *sedScriptParser) readDelimited(delimiter byte, regex bool) (string, bool) {
	var text strings.Builder
	for {
		character := p.next()
		switch {
		case character == sedEOF:
			return "", false
		case character == '\n' && regex:
			return "", false
		case charByte(character) == delimiter:
			return text.String(), true
		case character == '\\':
			escaped := p.next()
			switch {
			case escaped == sedEOF:
				return "", false
			case charByte(escaped) == delimiter:
				text.WriteByte(delimiter)
			case escaped == '\n' && !regex:
				text.WriteString("\\\n")
			default:
				text.WriteByte('\\')
				text.WriteByte(charByte(escaped))
			}
		case character == '[' && regex:
			text.WriteByte('[')
			if !p.copyBracket(&text) {
				return "", false
			}
		default:
			text.WriteByte(charByte(character))
		}
	}
}

// copyBracket copies a bracket expression after its "[" through its "]".
func (p *sedScriptParser) copyBracket(text *strings.Builder) bool {
	start := p.position
	if p.position < len(p.text) && p.text[p.position] == '^' {
		p.position++
	}
	if p.position < len(p.text) && p.text[p.position] == ']' {
		p.position++
	}
	for p.position < len(p.text) {
		character := p.text[p.position]
		p.position++
		switch {
		case character == '[' && p.position < len(p.text) && strings.IndexByte(":=.", p.text[p.position]) >= 0:
			closing := string(p.text[p.position]) + "]"
			end := strings.Index(p.text[p.position+1:], closing)
			if end >= 0 {
				p.position += end + 3
			}
		case character == ']':
			text.WriteString(p.text[start:p.position])
			return true
		case character == '\n':
			return false
		}
	}
	return false
}

// compileRegex compiles a sed regex; "" is the run-time "last regex".
func (p *sedScriptParser) compileRegex(pattern string, ignoreCase, multiline bool) (*sedRegex, error) {
	if pattern == "" {
		return &sedRegex{}, nil
	}
	options := regexOptions{extended: p.extended, sedEscapes: true, multiline: multiline, ignoreCase: ignoreCase}
	translated, err := translatePOSIX(pattern, options)
	var compiled *posixRegexp
	if err == nil {
		compiled, err = compilePOSIX(translated, options)
	}
	var problem *regexError
	if errors.As(err, &problem) {
		return nil, p.fail(problem.message)
	}
	if err != nil {
		return nil, err
	}
	return &sedRegex{regex: compiled}, nil
}

// parseArguments reads what follows the command letter.
func (p *sedScriptParser) parseArguments(command *sedInstruction) error {
	switch command.name {
	case '{':
		p.openBlocks = append(p.openBlocks, len(p.program))
		return nil
	case '}':
		return p.closeBlock(command)
	case '#':
		if command.address1 != nil {
			return p.fail("comments don't accept any addresses")
		}
		p.skipLine()
		return nil
	case ':':
		return p.parseLabelDefinition(command)
	case 'a', 'i', 'c':
		return p.parseText(command)
	case 'b', 't', 'T':
		command.text = p.readLabel()
		return nil
	case 'q', 'Q':
		return p.parseExit(command)
	case 's':
		return p.parseSubstitution(command)
	case 'y':
		return p.parseTranslation(command)
	case '=', 'd', 'D', 'g', 'G', 'h', 'H', 'n', 'N', 'p', 'P', 'x', 'z', 'F':
		return p.endOfCommand()
	case 'l', 'r', 'R', 'w', 'W', 'e', 'L':
		return p.fail(fmt.Sprintf("command `%c' is not supported by this sed", command.name))
	case 'v':
		p.readLabel()
		return p.endOfCommand()
	}
	return p.fail(fmt.Sprintf("unknown command: `%c'", command.name))
}

// closeBlock pairs a "}" with its "{".
func (p *sedScriptParser) closeBlock(command *sedInstruction) error {
	if command.address1 != nil {
		return p.fail("`}' doesn't want any addresses")
	}
	if len(p.openBlocks) == 0 {
		return p.fail("unexpected `}'")
	}
	opening := p.openBlocks[len(p.openBlocks)-1]
	p.openBlocks = p.openBlocks[:len(p.openBlocks)-1]
	p.program[opening].target = len(p.program)
	return p.endOfCommand()
}

func (p *sedScriptParser) skipLine() {
	for character := p.next(); character != sedEOF && character != '\n'; character = p.next() {
	}
}

// endOfCommand accepts blanks and then the end, a newline, ";", "}" or "#".
func (p *sedScriptParser) endOfCommand() error {
	character := p.nextNonBlank()
	switch character {
	case sedEOF, '\n', ';':
		return nil
	case '}', '#':
		p.back(character)
		return nil
	}
	return p.fail("extra characters after command")
}

// readLabel reads a label up to blanks, ";" or the end of the line.
func (p *sedScriptParser) readLabel() string {
	character := p.nextNonBlank()
	var label strings.Builder
	for character != sedEOF && character != '\n' && character != ';' && character != ' ' && character != '\t' {
		label.WriteByte(charByte(character))
		character = p.next()
	}
	if character == ';' || character == '\n' {
		p.back(character)
	}
	return label.String()
}

func (p *sedScriptParser) parseLabelDefinition(command *sedInstruction) error {
	if command.address1 != nil {
		return p.fail(": doesn't want any addresses")
	}
	command.text = p.readLabel()
	if command.text == "" {
		return p.fail("\":\" lacks a label")
	}
	return nil
}

func (p *sedScriptParser) parseExit(command *sedInstruction) error {
	if command.address2 != nil {
		return p.fail("command only uses one address")
	}
	character := p.nextNonBlank()
	p.back(character)
	if character != sedEOF && isDigit(charByte(character)) {
		command.exitCode = p.readNumber()
	}
	return p.endOfCommand()
}

// parseText reads the text of a, i or c: "a text" or "a\" and lines, a
// backslash continuing the text onto the next line.
func (p *sedScriptParser) parseText(command *sedInstruction) error {
	character := p.nextNonBlank()
	if character == sedEOF {
		return p.fail("expected \\ after `a', `c' or `i'")
	}
	if character == '\\' {
		// "a\" then the text on the next line, or GNU's "a\text" keeping
		// the blanks after the backslash.
		if character = p.next(); character == sedEOF {
			return p.fail("expected \\ after `a', `c' or `i'")
		}
		if character == '\n' {
			character = p.next()
		}
	}
	var text strings.Builder
	for ; character != sedEOF && character != '\n'; character = p.next() {
		if character == '\\' {
			if character = p.next(); character == sedEOF {
				break
			}
			if control, ok := sedControlEscapes[charByte(character)]; ok && character != 'n' {
				character = int(control)
			}
		}
		text.WriteByte(charByte(character))
	}
	command.text = text.String() + "\n"
	return nil
}

// parseSubstitution reads s/regex/replacement/flags.
func (p *sedScriptParser) parseSubstitution(command *sedInstruction) error {
	delimiter := p.next()
	if delimiter == sedEOF || delimiter == '\n' || delimiter == '\\' {
		return p.fail("unterminated `s' command")
	}
	pattern, ok := p.readDelimited(charByte(delimiter), true)
	if !ok {
		return p.fail("unterminated `s' command")
	}
	replacementText, ok := p.readDelimited(charByte(delimiter), false)
	if !ok {
		return p.fail("unterminated `s' command")
	}
	substitution := &sedSubstitution{occurrence: 1}
	ignoreCase, multiline, err := p.parseSubstitutionFlags(substitution)
	if err != nil {
		return err
	}
	if substitution.regex, err = p.compileRegex(pattern, ignoreCase, multiline); err != nil {
		return err
	}
	substitution.replacement = parseReplacement(replacementText)
	if substitution.regex.regex != nil {
		groups := substitution.regex.regex.numGroups()
		for _, part := range substitution.replacement {
			if part.kind == replaceGroup && part.group > groups {
				return p.fail(fmt.Sprintf("invalid reference \\%d on `s' command's RHS", part.group))
			}
		}
	}
	command.substitution = substitution
	return nil
}

// substitutionFlags are the s flags that shape the regex.
type substitutionFlags struct {
	ignoreCase, multiline, numbered bool
}

// parseSubstitutionFlags reads g p N i I m M up to the end of the command.
func (p *sedScriptParser) parseSubstitutionFlags(substitution *sedSubstitution) (bool, bool, error) {
	flags := substitutionFlags{}
	for {
		character := p.next()
		switch character {
		case '}', '#':
			p.back(character)
			return flags.ignoreCase, flags.multiline, nil
		case sedEOF, '\n', ';':
			return flags.ignoreCase, flags.multiline, nil
		case ' ', '\t':
			return flags.ignoreCase, flags.multiline, p.endOfCommand()
		}
		if err := p.substitutionFlag(character, substitution, &flags); err != nil {
			return false, false, err
		}
	}
}

// substitutionFlag applies one s flag letter or number.
func (p *sedScriptParser) substitutionFlag(character int, substitution *sedSubstitution, flags *substitutionFlags) error {
	switch {
	case character == 'i' || character == 'I':
		flags.ignoreCase = true
	case character == 'm' || character == 'M':
		flags.multiline = true
	case character == 'g':
		if substitution.global {
			return p.fail("multiple `g' options to `s' command")
		}
		substitution.global = true
	case character == 'p':
		if substitution.print {
			return p.fail("multiple `p' options to `s' command")
		}
		substitution.print = true
	case isDigit(charByte(character)):
		if flags.numbered {
			return p.fail("multiple number options to `s' command")
		}
		p.back(character)
		flags.numbered = true
		if substitution.occurrence = p.readNumber(); substitution.occurrence == 0 {
			return p.fail("number option to `s' command may not be zero")
		}
	case character == 'e' || character == 'w':
		return p.fail(fmt.Sprintf("the `%c' option to `s' is not supported by this sed", character))
	default:
		return p.fail("unknown option to `s'")
	}
	return nil
}

// parseReplacement splits an s replacement into literals, groups (& is 0)
// and GNU's case conversions.
func parseReplacement(text string) []replacementPart {
	var parts []replacementPart
	var literal strings.Builder
	flush := func() {
		if literal.Len() > 0 {
			parts = append(parts, replacementPart{kind: replaceLiteral, literal: literal.String()})
			literal.Reset()
		}
	}
	for index := 0; index < len(text); index++ {
		character := text[index]
		if character == '&' {
			flush()
			parts = append(parts, replacementPart{kind: replaceGroup})
			continue
		}
		if character != '\\' || index+1 == len(text) {
			literal.WriteByte(character)
			continue
		}
		index++
		escaped := text[index]
		switch {
		case isDigit(escaped):
			flush()
			parts = append(parts, replacementPart{kind: replaceGroup, group: int(escaped - '0')})
		case strings.IndexByte("ULEul", escaped) >= 0:
			flush()
			parts = append(parts, replacementPart{kind: replaceCase, caseOperation: escaped})
		default:
			if value, consumed, ok := sedEscape(text[index:]); ok {
				literal.WriteByte(value)
				index += consumed - 1
			} else {
				literal.WriteByte(escaped)
			}
		}
	}
	flush()
	return parts
}

// parseTranslation reads y/source/target/.
func (p *sedScriptParser) parseTranslation(command *sedInstruction) error {
	delimiter := p.next()
	if delimiter == sedEOF || delimiter == '\n' || delimiter == '\\' {
		return p.fail("unterminated `y' command")
	}
	source, ok := p.readDelimited(charByte(delimiter), false)
	if !ok {
		return p.fail("unterminated `y' command")
	}
	target, ok := p.readDelimited(charByte(delimiter), false)
	if !ok {
		return p.fail("unterminated `y' command")
	}
	sourceRunes, targetRunes := []rune(unescapeTranslation(source)), []rune(unescapeTranslation(target))
	if len(sourceRunes) != len(targetRunes) {
		return p.fail("strings for `y' command are different lengths")
	}
	command.translation = make(map[rune]rune, len(sourceRunes))
	for index, character := range sourceRunes {
		command.translation[character] = targetRunes[index]
	}
	return p.endOfCommand()
}

// unescapeTranslation resolves \\ \n and the control escapes of a y set.
func unescapeTranslation(text string) string {
	var output strings.Builder
	for index := 0; index < len(text); index++ {
		if text[index] != '\\' || index+1 == len(text) {
			output.WriteByte(text[index])
			continue
		}
		index++
		if value, consumed, ok := sedEscape(text[index:]); ok {
			output.WriteByte(value)
			index += consumed - 1
			continue
		}
		if text[index] != '\\' {
			output.WriteByte('\\')
		}
		output.WriteByte(text[index])
	}
	if !utf8.ValidString(output.String()) {
		return text
	}
	return output.String()
}

// resolveLabels points every branch at its label.
func (p *sedScriptParser) resolveLabels() error {
	labels := map[string]int{}
	for index, command := range p.program {
		if command.name == ':' {
			labels[command.text] = index
		}
	}
	for index := range p.program {
		command := &p.program[index]
		if command.name != 'b' && command.name != 't' && command.name != 'T' || command.text == "" {
			continue
		}
		target, ok := labels[command.text]
		if !ok {
			return &sedScriptError{fmt.Sprintf("-e expression #1, char 0: can't find label for jump to `%s'", command.text)}
		}
		command.target = target
	}
	return nil
}
