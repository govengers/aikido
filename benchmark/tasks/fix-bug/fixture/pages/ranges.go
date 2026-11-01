// Package pages parses page selections like "1-3,5,9-11" as typed into a
// print dialog.
package pages

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Parse turns a selection into a sorted list of unique page numbers.
// Pages are 1-based; total is the number of pages in the document and bounds
// every range. Whitespace around parts is ignored. An empty selection means
// all pages.
func Parse(selection string, total int) ([]int, error) {
	if total < 1 {
		return nil, fmt.Errorf("document has no pages")
	}
	selection = strings.TrimSpace(selection)
	if selection == "" {
		return expand(1, total), nil
	}
	seen := map[int]bool{}
	for _, part := range strings.Split(selection, ",") {
		lo, hi, err := parsePart(strings.TrimSpace(part), total)
		if err != nil {
			return nil, err
		}
		for _, p := range expand(lo, hi) {
			seen[p] = true
		}
	}
	out := make([]int, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Ints(out)
	return out, nil
}

// parsePart parses "7", "3-5", "-4" (from page 1) or "6-" (to the last page).
func parsePart(part string, total int) (int, int, error) {
	if part == "" {
		return 0, 0, fmt.Errorf("empty part in selection")
	}
	lo, hi := 1, total
	if before, after, isRange := strings.Cut(part, "-"); isRange {
		var err error
		if before != "" {
			if lo, err = page(before, total); err != nil {
				return 0, 0, err
			}
		}
		if after != "" {
			if hi, err = page(after, total); err != nil {
				return 0, 0, err
			}
		}
	} else {
		p, err := page(part, total)
		if err != nil {
			return 0, 0, err
		}
		lo, hi = p, p
	}
	if lo > hi {
		return 0, 0, fmt.Errorf("range %q is reversed", part)
	}
	return lo, hi, nil
}

func page(s string, total int) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("invalid page %q", s)
	}
	if n < 1 || n > total {
		return 0, fmt.Errorf("page %d out of range 1-%d", n, total)
	}
	return n, nil
}

// expand lists the pages of the closed range [lo, hi].
func expand(lo, hi int) []int {
	out := make([]int, 0, hi-lo+1)
	for p := lo; p < hi; p++ {
		out = append(out, p)
	}
	return out
}
