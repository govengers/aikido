package shell

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
)

var trTool = &textTool{
	name:        "tr",
	hint:        "Try 'tr --help' for more information.\n",
	usageStatus: 1,
	help: `Usage: tr [OPTION]... STRING1 [STRING2]
Translate, squeeze, and/or delete bytes from standard input, writing to standard output.
  -c, -C, --complement    use the complement of STRING1
  -d, --delete            delete characters in STRING1, do not translate
  -s, --squeeze-repeats   replace each sequence of a repeated character in the last
                          specified STRING with a single occurrence
  -t, --truncate-set1     first truncate STRING1 to length of STRING2
STRINGs use a-z ranges, [:class:], [=c=], [c*n], [c*] and \n \t \\ \NNN escapes.
`,
}

var trParser = &optionParser{specs: []optionSpec{
	{short: 'c', long: "complement"},
	{short: 'C'},
	{short: 'd', long: "delete"},
	{short: 's', long: "squeeze-repeats"},
	{short: 't', long: "truncate-set1"},
	helpOption,
}}

func trCommand() Command {
	return Command{Name: "tr", Summary: "translate or delete bytes (-d -s -c -t, a-z, [:upper:])", Run: runTr}
}

// trSettings is a parsed tr command line.
type trSettings struct {
	complement, delete, squeeze, truncate bool
}

func runTr(_ context.Context, invocation *Invocation) error {
	line, err := trTool.parseOptions(invocation, trParser)
	if line.done {
		return err
	}
	settings := trSettings{}
	for _, option := range line.options {
		switch option.key {
		case "complement", "C":
			settings.complement = true
		case "delete":
			settings.delete = true
		case "squeeze-repeats":
			settings.squeeze = true
		case "truncate-set1":
			settings.truncate = true
		}
	}
	if message := settings.checkOperands(line.operands); message != "" {
		return trTool.usageFailure(invocation, message)
	}
	program, message := settings.compile(line.operands)
	if message != "" {
		return trTool.fatal(invocation, 1, "%s", message)
	}
	return program.run(invocation.Stdin, invocation.Stdout)
}

// checkOperands validates the number of sets for the mode, with GNU's text.
func (s *trSettings) checkOperands(operands []string) string {
	switch {
	case len(operands) == 0:
		return "missing operand"
	case len(operands) == 1 && !s.delete && !s.squeeze:
		return fmt.Sprintf("missing operand after '%s'\nTwo strings must be given when translating.", operands[0])
	case len(operands) == 1 && s.delete && s.squeeze:
		return fmt.Sprintf("missing operand after '%s'\nTwo strings must be given when both deleting and squeezing repeats.", operands[0])
	case len(operands) >= 2 && s.delete && !s.squeeze:
		return fmt.Sprintf("extra operand '%s'\nOnly one string may be given when deleting without squeezing repeats.", operands[1])
	case len(operands) > 2:
		return fmt.Sprintf("extra operand '%s'", operands[2])
	}
	return ""
}

// trProgram is what tr does to each byte.
type trProgram struct {
	translation [256]byte
	deleted     [256]bool
	squeezed    [256]bool
	squeeze     bool
}

// compile builds the byte tables from the sets; the message is GNU's for an
// invalid set.
func (s *trSettings) compile(operands []string) (*trProgram, string) {
	program := &trProgram{squeeze: s.squeeze}
	for index := range program.translation {
		program.translation[index] = byte(index)
	}
	first, message := parseTrSet(operands[0], false, 0)
	if message != "" {
		return nil, message
	}
	if s.complement {
		first = complementSet(first)
	}
	var second []byte
	if len(operands) == 2 {
		if second, message = parseTrSet(operands[1], true, len(first)); message != "" {
			return nil, message
		}
	}
	switch {
	case s.delete:
		markAll(&program.deleted, first)
		markAll(&program.squeezed, second)
	case len(second) > 0 || len(operands) == 2:
		if message := s.translate(program, first, second); message != "" {
			return nil, message
		}
		markAll(&program.squeezed, second)
	default:
		markAll(&program.squeezed, first)
	}
	return program, ""
}

// translate maps first onto second, padding second with its last byte
// unless -t truncates first.
func (s *trSettings) translate(program *trProgram, first, second []byte) string {
	if len(second) == 0 {
		if len(first) == 0 {
			return ""
		}
		return "when not truncating set1, string2 must be non-empty"
	}
	if s.truncate {
		first = first[:min(len(first), len(second))]
	}
	for index, character := range first {
		program.translation[character] = second[min(index, len(second)-1)]
	}
	return ""
}

func markAll(table *[256]bool, set []byte) {
	for _, character := range set {
		table[character] = true
	}
}

// complementSet is every byte not in set, ascending.
func complementSet(set []byte) []byte {
	var present [256]bool
	markAll(&present, set)
	var complement []byte
	for character := range 256 {
		if !present[character] {
			complement = append(complement, byte(character))
		}
	}
	return complement
}

func (p *trProgram) run(input io.Reader, output io.Writer) error {
	reader := bufio.NewReader(input)
	writer := bufio.NewWriter(output)
	last := -1
	for {
		character, err := reader.ReadByte()
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("read input: %w", err)
		}
		if p.deleted[character] {
			continue
		}
		translated := p.translation[character]
		if p.squeeze && p.squeezed[translated] && int(translated) == last {
			continue
		}
		last = int(translated)
		if err := writer.WriteByte(translated); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// trToken is one element of a set before ranges are resolved.
type trToken struct {
	character byte
	// literal is false for an unescaped character, which may form a range.
	escaped bool
	// expansion is a class or repeat already expanded; fill marks [c*].
	expansion []byte
	isGroup   bool
	fill      bool
}

var trClasses = map[string]func(byte) bool{
	"alnum":  func(c byte) bool { return isASCIILetter(c) || isDigit(c) },
	"alpha":  isASCIILetter,
	"blank":  func(c byte) bool { return c == ' ' || c == '\t' },
	"cntrl":  func(c byte) bool { return c < 32 || c == 127 },
	"digit":  isDigit,
	"graph":  func(c byte) bool { return c > 32 && c < 127 },
	"lower":  func(c byte) bool { return c >= 'a' && c <= 'z' },
	"print":  func(c byte) bool { return c >= 32 && c < 127 },
	"punct":  func(c byte) bool { return c > 32 && c < 127 && !isASCIILetter(c) && !isDigit(c) },
	"space":  func(c byte) bool { return c == ' ' || (c >= '\t' && c <= '\r') },
	"upper":  func(c byte) bool { return c >= 'A' && c <= 'Z' },
	"xdigit": func(c byte) bool { return isDigit(c) || (c|0x20 >= 'a' && c|0x20 <= 'f') },
}

func isASCIILetter(c byte) bool { return (c|0x20) >= 'a' && (c|0x20) <= 'z' }

// parseTrSet expands a set. In the second set [c*] fills up to firstLength.
func parseTrSet(text string, second bool, firstLength int) ([]byte, string) {
	tokens, message := tokenizeTrSet(text, second)
	if message != "" {
		return nil, message
	}
	var set []byte
	fillAt := -1
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		switch {
		case token.fill:
			fillAt = len(set)
			set = append(set, token.character)
		case token.isGroup:
			set = append(set, token.expansion...)
		case index+2 < len(tokens) && !tokens[index+1].escaped && !tokens[index+1].isGroup && tokens[index+1].character == '-' && !tokens[index+2].isGroup:
			low, high := token.character, tokens[index+2].character
			if high < low {
				return nil, fmt.Sprintf("range-endpoints of '%s-%s' are in reverse collating sequence order", trDisplay(low), trDisplay(high))
			}
			for character := int(low); character <= int(high); character++ {
				set = append(set, byte(character&0xff))
			}
			index += 2
		default:
			set = append(set, token.character)
		}
	}
	if fillAt >= 0 && firstLength > len(set) {
		fill := bytes.Repeat(set[fillAt:fillAt+1], firstLength-len(set)+1)
		set = append(set[:fillAt], append(fill, set[fillAt+1:]...)...)
	}
	return set, ""
}

func trDisplay(character byte) string {
	if character < 32 || character >= 127 {
		return fmt.Sprintf("\\%03o", character)
	}
	return string(character)
}

// tokenizeTrSet splits a set into characters, escapes and bracket groups.
func tokenizeTrSet(text string, second bool) ([]trToken, string) {
	var tokens []trToken
	for position := 0; position < len(text); {
		if text[position] == '[' {
			token, consumed, message := parseTrGroup(text[position:], second)
			if message != "" {
				return nil, message
			}
			if consumed > 0 {
				tokens = append(tokens, token)
				position += consumed
				continue
			}
		}
		if text[position] == '\\' {
			character, consumed := trEscape(text[position:])
			tokens = append(tokens, trToken{character: character, escaped: true})
			position += consumed
			continue
		}
		tokens = append(tokens, trToken{character: text[position]})
		position++
	}
	return tokens, ""
}

// trEscape decodes a backslash escape at the start of text.
func trEscape(text string) (byte, int) {
	if len(text) == 1 {
		return '\\', 1
	}
	if control, ok := map[byte]byte{'a': '\a', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t', 'v': '\v'}[text[1]]; ok {
		return control, 2
	}
	end := 1
	for end < len(text) && end < 4 && text[end] >= '0' && text[end] <= '7' {
		end++
	}
	if end > 1 {
		value, _ := strconv.ParseUint(text[1:end], 8, 16)
		if value > 255 {
			end--
			value, _ = strconv.ParseUint(text[1:end], 8, 16)
		}
		return byte(value & 0xff), end
	}
	return text[1], 2
}

// parseTrGroup parses [:class:], [=c=] or [c*n] at the start of text;
// consumed is 0 when the "[" is an ordinary character.
func parseTrGroup(text string, second bool) (trToken, int, string) {
	if end := strings.Index(text, ":]"); len(text) >= 4 && text[1] == ':' && end >= 2 {
		return parseTrClass(text[2:end], second)
	}
	if len(text) >= 5 && text[1] == '=' && text[3] == '=' && text[4] == ']' {
		return trToken{isGroup: true, expansion: []byte{text[2]}}, 5, ""
	}
	return parseTrRepeat(text, second)
}

// parseTrClass expands [:name:]; in the second set only upper and lower
// may appear.
func parseTrClass(name string, second bool) (trToken, int, string) {
	test, ok := trClasses[name]
	if !ok {
		return trToken{}, 0, fmt.Sprintf("invalid character class '%s'", name)
	}
	if second && name != "upper" && name != "lower" {
		return trToken{}, 0, "when translating, the only character classes that may appear in\nstring2 are 'upper' and 'lower'"
	}
	var expansion []byte
	for character := range 256 {
		if test(byte(character)) {
			expansion = append(expansion, byte(character))
		}
	}
	return trToken{isGroup: true, expansion: expansion}, len(name) + 4, ""
}

// parseTrRepeat parses [c*n] or [c*] (second set only); a "[" that ends
// the set is an ordinary character.
func parseTrRepeat(text string, second bool) (trToken, int, string) {
	if len(text) < 2 {
		return trToken{}, 0, ""
	}
	character, consumed := text[1], 2
	if text[1] == '\\' {
		var escapeLength int
		character, escapeLength = trEscape(text[1:])
		consumed = 1 + escapeLength
	}
	if consumed >= len(text) || text[consumed] != '*' {
		return trToken{}, 0, ""
	}
	end := strings.IndexByte(text[consumed:], ']')
	if end < 0 {
		return trToken{}, 0, ""
	}
	count := text[consumed+1 : consumed+end]
	if !second {
		return trToken{}, 0, "the [c*] repeat construct may not appear in string1"
	}
	if count == "" {
		return trToken{character: character, fill: true}, consumed + end + 1, ""
	}
	base := 10
	if strings.HasPrefix(count, "0") {
		base = 8
	}
	repeat, err := strconv.ParseInt(count, base, 32)
	if err != nil {
		return trToken{}, 0, fmt.Sprintf("invalid repeat count '%s' in [c*n] construct", count)
	}
	if repeat == 0 {
		return trToken{character: character, fill: true}, consumed + end + 1, ""
	}
	return trToken{isGroup: true, expansion: bytes.Repeat([]byte{character}, int(repeat))}, consumed + end + 1, ""
}
