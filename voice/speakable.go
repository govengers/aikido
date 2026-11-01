package voice

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// minimumSentenceRunes keeps very short fragments ("Ja.", "z. B.") together
// with the next sentence: one synthesis call per word sounds choppy and costs
// a round trip each.
// ponytail: no abbreviation list, "z. B." past 12 runes still splits; add
// one if the prosody break is audible.
const minimumSentenceRunes = 12

// splitSentences cuts complete sentences off the front of text and returns
// them with the unfinished rest. A sentence ends at . ! ? or an ellipsis
// followed by whitespace, or at a line break. A terminator at the very end of
// text is not a boundary yet: the next delta may be "5 Euro" after "3.".
func splitSentences(text string) (sentences []string, rest string) {
	start := 0
	for index, character := range text {
		end := sentenceEnd(text, index, character)
		if end < 0 {
			continue
		}
		sentence := text[start:end]
		if utf8.RuneCountInString(strings.TrimSpace(sentence)) < minimumSentenceRunes && character != '\n' {
			continue
		}
		if strings.TrimSpace(sentence) != "" {
			sentences = append(sentences, sentence)
		}
		start = end
	}
	return sentences, text[start:]
}

// sentenceEnd is the byte offset after the sentence that character at index
// ends, or -1 if it ends none.
func sentenceEnd(text string, index int, character rune) int {
	switch character {
	case '\n':
		return index + 1
	case '.', '!', '?', '…':
		next := index + utf8.RuneLen(character)
		if next >= len(text) {
			return -1
		}
		following, _ := utf8.DecodeRuneInString(text[next:])
		if unicode.IsSpace(following) {
			return next
		}
	}
	return -1
}

var (
	codeFencePattern  = regexp.MustCompile("(?s)```.*?(```|$)")
	imagePattern      = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	linkPattern       = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	urlPattern        = regexp.MustCompile(`(?i)\b(?:https?://|www\.)\S+`)
	linePrefixPattern = regexp.MustCompile(`(?m)^\s*(?:#{1,6}\s+|>\s?|[-*+]\s+|\d+[.)]\s+)`)
	tableRulePattern  = regexp.MustCompile(`(?m)^\s*\|?\s*:?-{3,}.*$`)
	emphasisPattern   = regexp.MustCompile(`\*\*|__|~~|\*|` + "`")
	whitespacePattern = regexp.MustCompile(`\s+`)
)

// Speakable turns a markdown reply into plain text for text-to-speech: code
// blocks, URLs and table rules are dropped, links and images keep their
// text, headings, quotes, list markers and emphasis lose their markup, table
// cells are joined with commas, and whitespace collapses to single spaces.
// Text without a letter or digit becomes "". It is safe for concurrent use.
func Speakable(markdown string) string {
	text := codeFencePattern.ReplaceAllString(markdown, " ")
	text = imagePattern.ReplaceAllString(text, "$1")
	text = linkPattern.ReplaceAllString(text, "$1")
	text = urlPattern.ReplaceAllString(text, "")
	text = tableRulePattern.ReplaceAllString(text, "")
	text = linePrefixPattern.ReplaceAllString(text, "")
	text = emphasisPattern.ReplaceAllString(text, "")
	if strings.Contains(text, "|") {
		text = strings.Trim(strings.ReplaceAll(text, "|", ", "), ", ")
	}
	text = whitespacePattern.ReplaceAllString(text, " ")
	text = strings.TrimSpace(text)
	text = strings.ReplaceAll(text, " ,", ",")
	text = strings.ReplaceAll(text, " .", ".")
	if !strings.ContainsFunc(text, func(character rune) bool { return unicode.IsLetter(character) || unicode.IsDigit(character) }) {
		return ""
	}
	return text
}
