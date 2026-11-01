package shell

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// regexOptions selects the POSIX dialect a pattern is written in.
type regexOptions struct {
	// extended is ERE (grep -E, sed -E); otherwise BRE with GNU's \+ \? \|.
	extended bool
	// grepQuirks are GNU grep's ERE leniencies sed does not have: an
	// unmatched ")" and an invalid "{" are ordinary characters, a leading
	// repetition operator repeats nothing.
	grepQuirks bool
	// sedEscapes turns \t \n \r \f \v \a into the control characters, as GNU
	// sed does; grep reads \t as "t".
	sedEscapes bool
}

// regexError is a pattern GNU's regcomp refuses; the message is regcomp's.
type regexError struct{ message string }

func (e *regexError) Error() string { return e.message }

// errBackReference is GNU's error for a reference to a group that is not
// closed yet; errBackReferenceUnsupported is a valid one RE2 cannot match.
var (
	errBackReference            = &regexError{"Invalid back reference"}
	errBackReferenceUnsupported = &regexError{"back-references are not supported by this grep/sed"}
)

// maximumRepetition is the largest count of an interval {m,n}: Go's
// limit (GNU allows 32767).
const maximumRepetition = 1000

// compileGo compiles Go regexp syntax; "." matches a newline like in POSIX.
// Go matches text as UTF-8, so a pattern with other bytes is refused.
func compileGo(pattern string, ignoreCase, longest bool) (*regexp.Regexp, error) {
	if !utf8.ValidString(pattern) {
		return nil, &regexError{"pattern is not valid UTF-8"}
	}
	flags := "(?s)"
	if ignoreCase {
		flags = "(?si)"
	}
	compiled, err := regexp.Compile(flags + pattern)
	if err != nil {
		return nil, &regexError{"Invalid regular expression"}
	}
	if longest {
		compiled.Longest()
	}
	return compiled, nil
}

// posixTranslator rewrites one POSIX pattern (BRE or ERE with GNU
// extensions) into Go syntax for compileGo. Go's RE2 has no
// back-references; a pattern that uses one fails with
// errBackReferenceUnsupported.
type posixTranslator struct {
	options regexOptions
	pattern string
	output  strings.Builder
	// position is the index of the next unread byte of pattern.
	position int
	// groups holds the output offsets of the open groups.
	groups []int
	// closedGroups counts the groups closed so far, the ones a
	// back-reference may name.
	closedGroups int
	// atStart is true where a repetition operator has nothing to repeat:
	// at the start, after a group opens, after an alternation.
	atStart bool
	// atomStart is the output offset of the last atom, so a stacked
	// repetition (a**) can wrap it in a group; -1 when there is none.
	atomStart int
	// quantified says the last atom already carries a repetition.
	quantified bool
}

func translatePOSIX(pattern string, options regexOptions) (string, error) {
	translator := &posixTranslator{options: options, pattern: pattern, atStart: true, atomStart: -1}
	for translator.position < len(pattern) {
		if err := translator.step(); err != nil {
			return "", err
		}
	}
	if len(translator.groups) > 0 {
		return "", &regexError{`Unmatched ( or \(`}
	}
	return translator.output.String(), nil
}

// step translates the next token.
func (t *posixTranslator) step() error {
	character := t.pattern[t.position]
	t.position++
	switch character {
	case '\\':
		return t.escape()
	case '[':
		return t.bracket()
	case '*':
		return t.repeat("*")
	case '^':
		if t.options.extended || t.atStart {
			t.output.WriteString("^")
			return nil
		}
	case '$':
		if t.options.extended || t.dollarIsAnchor() {
			t.output.WriteString("$")
			t.atomStart = -1
			return nil
		}
	case '.':
		t.atom(".")
		return nil
	}
	if t.options.extended {
		if handled, err := t.extendedOperator(character); handled || err != nil {
			return err
		}
	}
	t.atom(regexp.QuoteMeta(t.literalAt(t.position - 1)))
	return nil
}

// literalAt returns the character starting at byte index start, a whole
// UTF-8 sequence for a multibyte one, and moves position past it.
func (t *posixTranslator) literalAt(start int) string {
	_, size := utf8.DecodeRuneInString(t.pattern[start:])
	t.position = start + size
	return t.pattern[start:t.position]
}

// dollarIsAnchor reports whether a BRE "$" ends the pattern or a group or an
// alternative; elsewhere it is an ordinary character.
func (t *posixTranslator) dollarIsAnchor() bool {
	rest := t.pattern[t.position:]
	return rest == "" || strings.HasPrefix(rest, `\)`) || strings.HasPrefix(rest, `\|`)
}

// extendedOperator handles the characters that are operators only in an ERE.
func (t *posixTranslator) extendedOperator(character byte) (bool, error) {
	switch character {
	case '(':
		t.openGroup()
	case ')':
		if len(t.groups) == 0 {
			if t.options.grepQuirks {
				return false, nil
			}
			return true, &regexError{`Unmatched ) or \)`}
		}
		t.closeGroup()
	case '|':
		t.alternate()
	case '+', '?':
		return true, t.repeat(string(character))
	case '{':
		return true, t.interval(false)
	default:
		return false, nil
	}
	return true, nil
}

// escape translates the token after a backslash.
func (t *posixTranslator) escape() error {
	if t.position >= len(t.pattern) {
		return &regexError{"Trailing backslash"}
	}
	character := t.pattern[t.position]
	t.position++
	if !t.options.extended {
		if handled, err := t.basicOperator(character); handled || err != nil {
			return err
		}
	}
	switch character {
	case '<', '>':
		// ponytail: Go has no \< \>; \b also matches at the other word edge.
		t.output.WriteString(`\b`)
	case 'b', 'B':
		t.output.WriteString(`\` + string(character))
	case 'w', 'W', 's', 'S':
		t.atom(`\` + string(character))
	case '`':
		t.output.WriteString(`\A`)
	case '\'':
		t.output.WriteString(`\z`)
	case 'n':
		t.atom(`\n`)
	default:
		if character >= '1' && character <= '9' {
			if int(character-'0') > t.closedGroups {
				return errBackReference
			}
			return errBackReferenceUnsupported
		}
		t.atom(t.escapedLiteral(character))
	}
	return nil
}

// escapedLiteral is the Go form of an escaped ordinary character.
func (t *posixTranslator) escapedLiteral(character byte) string {
	if t.options.sedEscapes {
		if control, ok := sedControlEscapes[character]; ok {
			return regexp.QuoteMeta(string(control))
		}
	}
	return regexp.QuoteMeta(t.literalAt(t.position - 1))
}

var sedControlEscapes = map[byte]byte{'t': '\t', 'r': '\r', 'f': '\f', 'v': '\v', 'a': '\a', 'n': '\n'}

// basicOperator handles the escaped operators of a BRE: \( \) \| \{ \+ \?.
func (t *posixTranslator) basicOperator(character byte) (bool, error) {
	switch character {
	case '(':
		t.openGroup()
	case ')':
		if len(t.groups) == 0 {
			return true, &regexError{`Unmatched ) or \)`}
		}
		t.closeGroup()
	case '|':
		t.alternate()
	case '+', '?':
		return true, t.repeat(string(character))
	case '{':
		return true, t.interval(true)
	default:
		return false, nil
	}
	return true, nil
}

func (t *posixTranslator) openGroup() {
	t.groups = append(t.groups, t.output.Len())
	t.output.WriteString("(")
	t.atStart = true
	t.atomStart = -1
}

func (t *posixTranslator) closeGroup() {
	t.output.WriteString(")")
	t.atStart = false
	// The whole group is the atom a following repetition applies to.
	t.atomStart = t.groups[len(t.groups)-1]
	t.groups = t.groups[:len(t.groups)-1]
	t.closedGroups++
	t.quantified = false
}

func (t *posixTranslator) alternate() {
	t.output.WriteString("|")
	t.atStart = true
	t.atomStart = -1
}

// atom appends one matchable unit.
func (t *posixTranslator) atom(text string) {
	t.atomStart = t.output.Len()
	t.output.WriteString(text)
	t.atStart = false
	t.quantified = false
}

// repeat appends a repetition operator to the last atom. With nothing to
// repeat, BRE reads "*" as an ordinary character, grep's ERE drops it and
// sed's ERE refuses it like GNU's regcomp.
func (t *posixTranslator) repeat(operator string) error {
	if t.atStart || t.atomStart < 0 {
		switch {
		case t.options.extended && t.atStart && !t.options.grepQuirks:
			return &regexError{"Invalid preceding regular expression"}
		case t.options.extended:
			return nil
		}
		t.atom(regexp.QuoteMeta(operator))
		return nil
	}
	if t.quantified {
		// a** is (a*)*: Go refuses stacked repetitions and reads *? as lazy.
		wrapped := "(?:" + t.output.String()[t.atomStart:] + ")"
		prefix := t.output.String()[:t.atomStart]
		t.output.Reset()
		t.output.WriteString(prefix + wrapped)
	}
	t.output.WriteString(operator)
	t.quantified = true
	return nil
}

// interval parses {m}, {m,}, {m,n} or {,n} after the opening brace. In a BRE
// the braces are escaped.
func (t *posixTranslator) interval(basic bool) error {
	closing := "}"
	if basic {
		closing = `\}`
	}
	end := strings.Index(t.pattern[t.position:], closing)
	if end < 0 {
		if !basic && t.options.grepQuirks {
			return t.literalBrace()
		}
		return &regexError{`Unmatched \{`}
	}
	minimum, maximum, ok := parseInterval(t.pattern[t.position : t.position+end])
	if !ok {
		if !basic && t.options.grepQuirks {
			return t.literalBrace()
		}
		return &regexError{`Invalid content of \{\}`}
	}
	t.position += end + len(closing)
	if minimum > maximumRepetition || maximum > maximumRepetition {
		return &regexError{"Regular expression too big"}
	}
	bounds := strconv.Itoa(minimum)
	switch {
	case maximum == minimum:
	case maximum < 0:
		bounds += ","
	default:
		bounds += "," + strconv.Itoa(maximum)
	}
	return t.repeat("{" + bounds + "}")
}

func (t *posixTranslator) literalBrace() error {
	t.atom(`\{`)
	return nil
}

// parseInterval reads the inside of an interval; maximum is -1 for "m,".
func parseInterval(content string) (int, int, bool) {
	low, high, hasComma := strings.Cut(content, ",")
	minimum := 0
	if low != "" {
		value, err := strconv.Atoi(low)
		if err != nil || value < 0 {
			return 0, 0, false
		}
		minimum = value
	}
	if !hasComma {
		return minimum, minimum, low != ""
	}
	if high == "" {
		return minimum, -1, true
	}
	maximum, err := strconv.Atoi(high)
	if err != nil || maximum < minimum {
		return 0, 0, false
	}
	return minimum, maximum, true
}

var posixClasses = map[string]bool{
	"alpha": true, "digit": true, "alnum": true, "upper": true, "lower": true, "space": true,
	"blank": true, "punct": true, "print": true, "graph": true, "cntrl": true, "xdigit": true,
}

// bracket translates a bracket expression after its "[". Inside it a
// backslash is an ordinary character, as POSIX says.
func (t *posixTranslator) bracket() error {
	var class strings.Builder
	class.WriteString("[")
	if t.position < len(t.pattern) && t.pattern[t.position] == '^' {
		class.WriteString("^")
		t.position++
	}
	hasClass := false
	for first := true; ; first = false {
		if t.position >= len(t.pattern) {
			// GNU's regcomp names the bracket only once a class was opened.
			if hasClass {
				return &regexError{"Unmatched [, [^, [:, [., or [="}
			}
			return &regexError{"Invalid regular expression"}
		}
		character := t.pattern[t.position]
		t.position++
		switch {
		case character == ']' && !first:
			class.WriteString("]")
			t.atom(class.String())
			return nil
		case character == '[' && t.position < len(t.pattern) && strings.IndexByte(":=.", t.pattern[t.position]) >= 0:
			if err := t.bracketClass(&class); err != nil {
				return err
			}
			hasClass = true
		case strings.IndexByte(`\[]^`, character) >= 0:
			class.WriteString(`\` + string(character))
		default:
			class.WriteByte(character)
		}
	}
}

// bracketClass translates [:name:], [=c=] or [.c.] inside a bracket
// expression; position is at the ":", "=" or ".".
func (t *posixTranslator) bracketClass(class *strings.Builder) error {
	kind := t.pattern[t.position]
	end := strings.Index(t.pattern[t.position+1:], string(kind)+"]")
	if end < 0 {
		return &regexError{"Unmatched [, [^, [:, [., or [="}
	}
	name := t.pattern[t.position+1 : t.position+1+end]
	t.position += end + 3
	if kind == ':' {
		if !posixClasses[name] {
			return &regexError{"Invalid character class name"}
		}
		class.WriteString("[:" + name + ":]")
		return nil
	}
	if utf8.RuneCountInString(name) != 1 {
		return &regexError{"Invalid collation character"}
	}
	class.WriteString(regexp.QuoteMeta(name))
	return nil
}
