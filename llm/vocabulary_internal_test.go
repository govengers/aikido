package llm

import "testing"

func TestLevenshtein(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		source, target string
		want           int
	}{
		{source: "", target: "", want: 0},
		{source: "", target: "abc", want: 3},
		{source: "duschtase", target: "duschtasse", want: 1},
		{source: "waschtich", target: "waschtisch", want: 1},
		{source: "kitten", target: "sitting", want: 3},
		{source: "größe", target: "grösse", want: 2},
	}
	for _, testCase := range testCases {
		if got := levenshtein([]rune(testCase.source), []rune(testCase.target)); got != testCase.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", testCase.source, testCase.target, got, testCase.want)
		}
	}
}

func TestNearMatch(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name       string
		heard, key string
		want       bool
	}{
		{name: "one edit", heard: "duschtase", key: "duschtasse", want: true},
		{name: "two edits below nine letters", heard: "dschbd", key: "duschbad", want: false},
		{name: "two edits from nine letters on", heard: "unterschrng", key: "unterschrank", want: true},
		{name: "other first letter", heard: "raschtisch", key: "waschtisch", want: false},
		{name: "length too far apart", heard: "wasch", key: "waschtisch", want: false},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := nearMatch([]rune(testCase.heard), []rune(testCase.key)); got != testCase.want {
				t.Errorf("nearMatch(%q, %q) = %v, want %v", testCase.heard, testCase.key, got, testCase.want)
			}
		})
	}
}
