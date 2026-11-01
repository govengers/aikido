package shell

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// translation is a POSIX pattern in Go syntax plus what RE2 cannot check by
// itself.
type translation struct {
	pattern string
	// captures counts the capture groups of pattern, word boundaries
	// included.
	captures int
	// groups maps POSIX group k (1-based) to its Go capture groups[k-1].
	groups []int
	// boundaries are the \< and \> of the pattern.
	boundaries []wordBoundary
}

// wordBoundary is a \< (start) or \>: the empty Go capture group capture
// marks its position in a match.
type wordBoundary struct {
	capture int
	start   bool
}

// literalTranslation is a pattern already in Go syntax (grep -F, -P).
func literalTranslation(pattern string) (translation, error) {
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return translation{}, &regexError{"Invalid regular expression"}
	}
	return translation{pattern: pattern, captures: compiled.NumSubexp()}, nil
}

// alternatives joins translations into one pattern that matches any of
// them; the groups of each keep their own numbering only through the
// returned captures, so the result is for matching, not for \N.
func alternatives(parts []translation) translation {
	var combined translation
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		texts = append(texts, "(?:"+part.pattern+")")
		for _, boundary := range part.boundaries {
			boundary.capture += combined.captures
			combined.boundaries = append(combined.boundaries, boundary)
		}
		combined.captures += part.captures
	}
	combined.pattern = strings.Join(texts, "|")
	return combined
}

// posixRegexp matches a translation with POSIX semantics: the leftmost
// match, the longest one there (unless leftmostFirst), searched from any
// offset with the line around it as context for ^, \b, \< and \>.
// Directional word boundaries are verified on each candidate; when one
// fails, shorter matches at the same start and then later starts are
// tried. Not safe for concurrent use (the variants compile lazily).
type posixRegexp struct {
	translation
	options regexOptions
	regex   *regexp.Regexp
	// from finds the leftmost start at or after an offset, at the longest
	// match at that start; exact checks a match of given extent. Indexed
	// by whether the text has a character before (and after) the match.
	from  *regexp.Regexp
	at    *regexp.Regexp
	exact [2][2]*regexp.Regexp
}

// compilePOSIX compiles a translation with the flags of options.
func compilePOSIX(source translation, options regexOptions) (*posixRegexp, error) {
	regex, err := compileGo(source.pattern, options)
	if err != nil {
		return nil, err
	}
	return &posixRegexp{translation: source, options: options, regex: regex}, nil
}

// numGroups is the number of POSIX groups, the largest valid \N.
func (r *posixRegexp) numGroups() int { return len(r.groups) }

// matchString reports whether text contains a match.
func (r *posixRegexp) matchString(ctx context.Context, text string) bool {
	if len(r.boundaries) == 0 {
		return r.regex.MatchString(text)
	}
	return r.findFrom(ctx, text, 0) != nil
}

// findAll returns the successive non-overlapping matches of text as POSIX
// submatch indexes (whole match, then each group), like Go's
// FindAllStringSubmatchIndex: after an empty match the search moves on one
// character, and an empty match right after a match is skipped.
func (r *posixRegexp) findAll(ctx context.Context, text string) [][]int {
	if len(r.boundaries) == 0 {
		matches := r.regex.FindAllStringSubmatchIndex(text, -1)
		for index, match := range matches {
			matches[index] = r.posixIndexes(match)
		}
		return matches
	}
	var matches [][]int
	previousEnd := -1
	for position := 0; position <= len(text); {
		match := r.findFrom(ctx, text, position)
		if match == nil {
			break
		}
		if match[0] != match[1] || match[0] != previousEnd {
			matches = append(matches, match)
			previousEnd = match[1]
		}
		position = match[1]
		if match[0] == match[1] {
			if position == len(text) {
				break
			}
			position += runeLength(text[position:])
		}
	}
	return matches
}

// findFrom returns the POSIX submatch indexes of the leftmost match that
// starts at or after offset, or nil. A cancelled ctx ends the search with
// nil.
func (r *posixRegexp) findFrom(ctx context.Context, text string, offset int) []int {
	for start := offset; start <= len(text); {
		if ctx.Err() != nil {
			return nil
		}
		match := r.search(text, start)
		if match == nil {
			return nil
		}
		if r.boundariesHold(text, match) {
			return r.posixIndexes(match)
		}
		if shorter := r.shorterMatch(ctx, text, match[0], match[1]); shorter != nil {
			return r.posixIndexes(shorter)
		}
		if match[0] == len(text) {
			return nil
		}
		start = match[0] + runeLength(text[match[0]:])
	}
	return nil
}

// shorterMatch tries the ends before end, longest first, for a match at
// begin whose word boundaries hold.
// ponytail: one RE2 run per candidate end, quadratic in the line; only a
// failing \< or \> gets here, and ctx stops it.
func (r *posixRegexp) shorterMatch(ctx context.Context, text string, begin, end int) []int {
	for candidate := end - 1; candidate >= begin; candidate-- {
		if ctx.Err() != nil {
			return nil
		}
		if candidate < len(text) && !utf8.RuneStart(text[candidate]) {
			continue
		}
		if match := r.exactMatch(text, begin, candidate); match != nil && r.boundariesHold(text, match) {
			return match
		}
	}
	return nil
}

// matchesExactly reports whether text[begin:end] is a match, with the text
// around it as context.
func (r *posixRegexp) matchesExactly(text string, begin, end int) bool {
	match := r.exactMatch(text, begin, end)
	return match != nil && r.boundariesHold(text, match)
}

// search is the leftmost(-longest) Go match starting at or after start,
// with text[:start] as context, in Go capture indexes over text.
func (r *posixRegexp) search(text string, start int) []int {
	if start == 0 {
		return r.regex.FindStringSubmatchIndex(text)
	}
	// One character of context: ^ does not match after it, \b sees it.
	r.from = r.variant(r.from, `\A(?s:.)(?s:.*?)(`, `)`, true)
	found := r.from.FindStringSubmatchIndex(text[start-1:])
	if found == nil {
		return nil
	}
	begin := found[2] + start - 1
	r.at = r.variant(r.at, `\A(?s:.)(?:`, `)`, false)
	match := r.at.FindStringSubmatchIndex(text[begin-1:])
	shift(match, begin-1)
	match[0] = begin
	return match
}

// exactMatch is the Go match of exactly text[begin:end], or nil.
func (r *posixRegexp) exactMatch(text string, begin, end int) []int {
	before, after := 0, 0
	prefix, suffix := `\A(?:`, `)\z`
	if begin > 0 {
		before, prefix = 1, `\A(?s:.)(?:`
	}
	if end < len(text) {
		after, suffix = 1, `)(?s:.)\z`
	}
	r.exact[before][after] = r.variant(r.exact[before][after], prefix, suffix, false)
	low, high := begin-before, end
	if after == 1 {
		high += runeLength(text[end:])
	}
	match := r.exact[before][after].FindStringSubmatchIndex(text[low:high])
	if match == nil {
		return nil
	}
	shift(match, low)
	match[0], match[1] = begin, end
	return match
}

// variant compiles prefix + pattern + suffix once; leftmostFirst is for the
// lazy search of a start.
func (r *posixRegexp) variant(compiled *regexp.Regexp, prefix, suffix string, leftmostFirst bool) *regexp.Regexp {
	if compiled != nil {
		return compiled
	}
	options := r.options
	options.leftmostFirst = options.leftmostFirst || leftmostFirst
	// The pattern compiled once already, so the variant compiles too.
	variant, err := compileGo(prefix+r.pattern+suffix, options)
	if err != nil {
		panic("aikido/tools/shell: variant of a valid pattern: " + strconv.Quote(r.pattern))
	}
	return variant
}

// boundariesHold checks the \< and \> a Go match passed.
func (r *posixRegexp) boundariesHold(text string, match []int) bool {
	for _, boundary := range r.boundaries {
		position := match[2*boundary.capture]
		if position < 0 {
			continue
		}
		wordBefore := position > 0 && isWordByte(text[position-1])
		wordAfter := position < len(text) && isWordByte(text[position])
		if boundary.start && (wordBefore || !wordAfter) || !boundary.start && (!wordBefore || wordAfter) {
			return false
		}
	}
	return true
}

// posixIndexes keeps the whole match and the POSIX groups of a Go match.
func (r *posixRegexp) posixIndexes(match []int) []int {
	if len(r.boundaries) == 0 && len(match) == 2*len(r.groups)+2 {
		return match
	}
	indexes := make([]int, 0, 2*len(r.groups)+2)
	indexes = append(indexes, match[0], match[1])
	for _, capture := range r.groups {
		indexes = append(indexes, match[2*capture], match[2*capture+1])
	}
	return indexes
}

// shift adds offset to the set indexes of a match over a slice.
func shift(match []int, offset int) {
	for index, value := range match {
		if value >= 0 {
			match[index] = value + offset
		}
	}
}

// runeLength is the byte length of the first character of text, at least 1.
func runeLength(text string) int {
	_, size := utf8.DecodeRuneInString(text)
	return max(size, 1)
}
