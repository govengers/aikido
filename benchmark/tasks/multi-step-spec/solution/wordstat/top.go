package wordstat

import "sort"

// WordCount is one word with its number of occurrences.
type WordCount struct {
	Word  string
	Count int
}

// TopWords returns the n most frequent words, ties broken alphabetically.
func TopWords(text string, n int) []WordCount {
	out := []WordCount{}
	if n <= 0 {
		return out
	}
	counts := map[string]int{}
	for _, w := range Words(text) {
		counts[w]++
	}
	for w, c := range counts {
		out = append(out, WordCount{Word: w, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Word < out[j].Word
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}
