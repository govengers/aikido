package llm

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaximumVocabularyPromptTerms and MaximumVocabularyPromptBytes cap how much of
// Vocabulary.Terms goes into the transcription prompt. Terms past the cap are
// still used for post-correction.
const (
	MaximumVocabularyPromptTerms = 200
	MaximumVocabularyPromptBytes = 3000
)

// Limits of the matching Vocabulary.Correct documents.
const (
	// maximumTermWindow bounds how many heard words one term may span ("L I V 6 0 G").
	maximumTermWindow = 6
	// maximumFuzzyWindow bounds how many heard words a fuzzy match may span.
	maximumFuzzyWindow       = 3
	minimumFuzzyWordLength   = 4
	minimumFuzzyTermLength   = 6
	minimumTwoEditTermLength = 9
)

// Vocabulary is a dictionary of well-known terms that corrects voice input in
// two steps: Terms bias the transcription prompt ("spell exactly like this"),
// then Correct rewrites the transcript deterministically.
//
// Correct works on word tokens (letters and digits; punctuation between them
// is kept outside a match and dropped inside it), case-insensitive and whole
// word. At each position it tries, longest first:
//
//  1. Replacements: a heard variant ("pfeifer und mai") becomes its canonical
//     term ("PFEIFFER & MAY").
//  2. Terms matched exactly once spaces, punctuation and case are ignored:
//     "Liv 60 g" becomes "LIV60G", "wasch tisch" becomes "Waschtisch".
//  3. With Fuzzy, terms of at least 6 letters matched with an edit distance of
//     1 (2 from 9 letters on), same first letter, and every heard word at
//     least 4 letters long: "Duschtase" becomes "Duschtasse". Short common
//     words are never touched.
//
// A Vocabulary is read-only during Correct and may be shared between
// goroutines as long as nobody modifies it.
type Vocabulary struct {
	// Terms are the canonical spellings, in the order they go into the prompt.
	Terms []string
	// Replacements maps a heard variant to its canonical term.
	Replacements map[string]string
	// Fuzzy enables the near-miss matching of step 3.
	Fuzzy bool
}

// Correct returns text with the vocabulary applied. A nil Vocabulary returns
// text unchanged.
func (v *Vocabulary) Correct(text string) string {
	if v == nil || text == "" {
		return text
	}
	rules := correctionRules{
		replacements: compileReplacements(v.Replacements),
		terms:        compileTerms(v.Terms, v.Fuzzy),
	}
	return rules.apply(text)
}

// promptLine is the instruction block appended to the transcription prompt,
// or "" without terms.
func (v *Vocabulary) promptLine() string {
	if v == nil {
		return ""
	}
	var termList strings.Builder
	count := 0
	for _, term := range v.Terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		if count == MaximumVocabularyPromptTerms || termList.Len()+len(term)+len(", ") > MaximumVocabularyPromptBytes {
			break
		}
		if count > 0 {
			termList.WriteString(", ")
		}
		termList.WriteString(term)
		count++
	}
	if count == 0 {
		return ""
	}
	return "Expected terms; when you hear one, spell it exactly like this: " + termList.String() + "."
}

type token struct {
	start, end int // byte span in the text
	normalized string
}

type replacementRule struct {
	heardWords []string
	canonical  string
}

type termRule struct {
	key       string // the term's normalized words joined without separators
	keyRunes  []rune
	canonical string
	fuzzy     bool
}

type correctionRules struct {
	replacements []replacementRule
	terms        []termRule
}

// compileReplacements returns the replacement rules, the variant with the
// most words first, then in a fixed order so map iteration never decides.
func compileReplacements(replacements map[string]string) []replacementRule {
	var rules []replacementRule
	for heard, canonical := range replacements {
		words := normalizedWords(heard)
		if len(words) > 0 && canonical != "" {
			rules = append(rules, replacementRule{heardWords: words, canonical: canonical})
		}
	}
	slices.SortFunc(rules, func(left, right replacementRule) int {
		return cmp.Or(
			cmp.Compare(len(right.heardWords), len(left.heardWords)),
			cmp.Compare(strings.Join(left.heardWords, " "), strings.Join(right.heardWords, " ")),
			cmp.Compare(left.canonical, right.canonical),
		)
	})
	return rules
}

// compileTerms returns the term rules, the longest key first so a fuzzy match
// prefers the longer term; terms of equal length keep their order.
func compileTerms(terms []string, fuzzy bool) []termRule {
	var rules []termRule
	for _, term := range terms {
		key := joinNormalized(tokenize(term))
		if key == "" {
			continue
		}
		keyRunes := []rune(key)
		rules = append(rules, termRule{
			key:       key,
			keyRunes:  keyRunes,
			canonical: strings.TrimSpace(term),
			fuzzy:     fuzzy && len(keyRunes) >= minimumFuzzyTermLength,
		})
	}
	slices.SortStableFunc(rules, func(left, right termRule) int {
		return cmp.Compare(len(right.key), len(left.key))
	})
	return rules
}

// apply replaces every matched run of tokens with its canonical term and keeps
// the text between matches as it is.
func (r correctionRules) apply(text string) string {
	tokens := tokenize(text)
	var corrected strings.Builder
	copiedUntil := 0
	for index := 0; index < len(tokens); {
		tokenCount, canonical := r.matchAt(tokens[index:])
		if tokenCount == 0 {
			index++
			continue
		}
		corrected.WriteString(text[copiedUntil:tokens[index].start])
		corrected.WriteString(canonical)
		copiedUntil = tokens[index+tokenCount-1].end
		index += tokenCount
	}
	corrected.WriteString(text[copiedUntil:])
	return corrected.String()
}

// matchAt returns how many of the leading tokens the best rule covers and its
// canonical term, or 0 and "" when no rule matches.
func (r correctionRules) matchAt(tokens []token) (int, string) {
	if tokenCount, canonical := matchReplacement(r.replacements, tokens); tokenCount > 0 {
		return tokenCount, canonical
	}
	if tokenCount, canonical := matchExactTerm(r.terms, tokens); tokenCount > 0 {
		return tokenCount, canonical
	}
	return matchFuzzyTerm(r.terms, tokens)
}

func matchReplacement(replacements []replacementRule, tokens []token) (int, string) {
	for _, replacement := range replacements {
		if startsWithWords(tokens, replacement.heardWords) {
			return len(replacement.heardWords), replacement.canonical
		}
	}
	return 0, ""
}

// matchExactTerm tries the widest window first, so "liv 60 g" wins over "liv".
func matchExactTerm(terms []termRule, tokens []token) (int, string) {
	for width := min(maximumTermWindow, len(tokens)); width >= 1; width-- {
		heard := joinNormalized(tokens[:width])
		for _, term := range terms {
			if heard == term.key {
				return width, term.canonical
			}
		}
	}
	return 0, ""
}

func matchFuzzyTerm(terms []termRule, tokens []token) (int, string) {
	for width := min(maximumFuzzyWindow, len(tokens)); width >= 1; width-- {
		window := tokens[:width]
		if containsShortWord(window) {
			continue
		}
		heard := []rune(joinNormalized(window))
		for _, term := range terms {
			if term.fuzzy && nearMatch(heard, term.keyRunes) {
				return width, term.canonical
			}
		}
	}
	return 0, ""
}

func startsWithWords(tokens []token, words []string) bool {
	if len(words) > len(tokens) {
		return false
	}
	for index, word := range words {
		if tokens[index].normalized != word {
			return false
		}
	}
	return true
}

func containsShortWord(tokens []token) bool {
	return slices.ContainsFunc(tokens, func(word token) bool {
		return utf8.RuneCountInString(word.normalized) < minimumFuzzyWordLength
	})
}

// nearMatch reports whether heard is within the edit distance key allows and
// starts with the same letter. Both must be non-empty.
func nearMatch(heard, key []rune) bool {
	maximumDistance := allowedEditDistance(len(key))
	if heard[0] != key[0] || absoluteDifference(len(heard), len(key)) > maximumDistance {
		return false
	}
	return levenshtein(heard, key) <= maximumDistance
}

func allowedEditDistance(keyLength int) int {
	if keyLength >= minimumTwoEditTermLength {
		return 2
	}
	return 1
}

// tokenize splits text into runs of letters and digits, lowercased.
func tokenize(text string) []token {
	var tokens []token
	start := -1
	for offset, character := range text {
		inWord := unicode.IsLetter(character) || unicode.IsDigit(character)
		switch {
		case inWord && start < 0:
			start = offset
		case !inWord && start >= 0:
			tokens = append(tokens, token{start, offset, strings.ToLower(text[start:offset])})
			start = -1
		}
	}
	if start >= 0 {
		tokens = append(tokens, token{start, len(text), strings.ToLower(text[start:])})
	}
	return tokens
}

func normalizedWords(text string) []string {
	var words []string
	for _, word := range tokenize(text) {
		words = append(words, word.normalized)
	}
	return words
}

func joinNormalized(tokens []token) string {
	var joined strings.Builder
	for _, word := range tokens {
		joined.WriteString(word.normalized)
	}
	return joined.String()
}

func absoluteDifference(first, second int) int {
	if first < second {
		return second - first
	}
	return first - second
}

// levenshtein is the edit distance between source and target, computed with
// two rolling rows.
func levenshtein(source, target []rune) int {
	previousRow := make([]int, len(target)+1)
	currentRow := make([]int, len(target)+1)
	for targetIndex := range previousRow {
		previousRow[targetIndex] = targetIndex
	}
	for sourceIndex := 1; sourceIndex <= len(source); sourceIndex++ {
		currentRow[0] = sourceIndex
		for targetIndex := 1; targetIndex <= len(target); targetIndex++ {
			substitutionCost := 1
			if source[sourceIndex-1] == target[targetIndex-1] {
				substitutionCost = 0
			}
			currentRow[targetIndex] = min(
				previousRow[targetIndex]+1,
				currentRow[targetIndex-1]+1,
				previousRow[targetIndex-1]+substitutionCost,
			)
		}
		previousRow, currentRow = currentRow, previousRow
	}
	return previousRow[len(target)]
}
