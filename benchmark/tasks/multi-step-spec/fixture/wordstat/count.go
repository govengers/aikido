// Package wordstat computes simple statistics over plain text.
package wordstat

import (
	"strings"
	"unicode"
)

// Words splits text into lower-cased words. A word is a maximal run of
// letters and digits; everything else separates words.
func Words(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for i, f := range fields {
		fields[i] = strings.ToLower(f)
	}
	return fields
}

// Count returns the number of words in text.
func Count(text string) int {
	return len(Words(text))
}
