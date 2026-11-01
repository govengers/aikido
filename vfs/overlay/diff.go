package overlay

// The line diff below is ported from the Go standard library's internal/diff
// (Copyright 2022 The Go Authors, BSD-3-Clause, https://go.dev/LICENSE),
// renamed to this repository's naming rules and split into named steps.

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// diffContextLines is the number of unchanged lines shown around each change.
const diffContextLines = 3

// UnifiedDiff renders changes as one unified diff for human review, in the
// order given (Changes returns them sorted by path). Files are named a/<path>
// and b/<path>; a created file is diffed against /dev/null, a deleted one to
// /dev/null. Content that is not valid UTF-8 or contains a NUL byte is
// reported as "Binary files ... differ" instead of line by line. No changes
// yield "".
//
// The diff is anchored on lines that occur exactly once on each side (the
// algorithm of the Go toolchain's internal/diff): it runs in O(n log n) and is
// meant for reading, not guaranteed to be minimal or to apply with patch.
func UnifiedDiff(changes []Change) string {
	var out strings.Builder
	for _, change := range changes {
		oldName, newName := "a/"+change.Path, "b/"+change.Path
		var oldContent, newContent []byte
		switch change.Kind {
		case ChangeCreate:
			oldName, newContent = "/dev/null", change.Content
		case ChangeDelete:
			newName, oldContent = "/dev/null", change.Previous
		default:
			oldContent, newContent = change.Previous, change.Content
		}
		if isBinary(oldContent) || isBinary(newContent) {
			fmt.Fprintf(&out, "Binary files %s and %s differ\n", oldName, newName)
			continue
		}
		out.WriteString(lineDiff(oldName, oldContent, newName, newContent))
	}
	return out.String()
}

func isBinary(content []byte) bool {
	return !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0
}

// linePair is a pair of line indexes, one into the old and one into the new
// text.
type linePair struct{ old, new int }

// hunkWriter accumulates the hunks of one file's diff.
type hunkWriter struct {
	oldLines, newLines []string
	out                strings.Builder
	done               linePair // printed up to oldLines[:done.old] and newLines[:done.new]
	hunkStart          linePair
	hunkCount          linePair
	hunkLines          []string
}

// lineDiff returns the unified diff of one file, "" when the texts are equal.
func lineDiff(oldName string, oldContent []byte, newName string, newContent []byte) string {
	if bytes.Equal(oldContent, newContent) {
		return ""
	}
	writer := &hunkWriter{oldLines: splitLines(oldContent), newLines: splitLines(newContent)}
	fmt.Fprintf(&writer.out, "--- %s\n+++ %s\n", oldName, newName)
	// anchors starts with {0,0} and ends with {len(old),len(new)}, so the loop
	// needs no setup or teardown.
	for _, anchor := range uniqueAnchors(writer.oldLines, writer.newLines) {
		if anchor.old < writer.done.old {
			continue // already covered while scanning forward from an earlier anchor
		}
		start, end := writer.expandMatch(anchor)
		writer.addChangedLinesBefore(start)
		if writer.hunkContinues(start, end) {
			writer.addCommonLines(start.old, end.old)
			writer.done = end
			continue
		}
		writer.flushHunk(start, end)
		if end.old >= len(writer.oldLines) && end.new >= len(writer.newLines) {
			break
		}
		writer.startHunk(end)
	}
	return writer.out.String()
}

// expandMatch grows the matching region around anchor in both directions,
// so that oldLines[start.old:end.old] equals newLines[start.new:end.new].
func (w *hunkWriter) expandMatch(anchor linePair) (start, end linePair) {
	start = anchor
	for start.old > w.done.old && start.new > w.done.new && w.oldLines[start.old-1] == w.newLines[start.new-1] {
		start.old--
		start.new--
	}
	end = anchor
	for end.old < len(w.oldLines) && end.new < len(w.newLines) && w.oldLines[end.old] == w.newLines[end.new] {
		end.old++
		end.new++
	}
	return start, end
}

// addChangedLinesBefore adds the removed and added lines between done and
// start to the current hunk.
func (w *hunkWriter) addChangedLinesBefore(start linePair) {
	for _, line := range w.oldLines[w.done.old:start.old] {
		w.hunkLines = append(w.hunkLines, "-"+line)
		w.hunkCount.old++
	}
	for _, line := range w.newLines[w.done.new:start.new] {
		w.hunkLines = append(w.hunkLines, "+"+line)
		w.hunkCount.new++
	}
}

// hunkContinues reports whether the common run start..end is too short to
// close the hunk, so the hunk takes all of it and continues. At the end of
// both texts the hunk always closes.
func (w *hunkWriter) hunkContinues(start, end linePair) bool {
	atEnd := end.old >= len(w.oldLines) && end.new >= len(w.newLines)
	common := end.old - start.old
	return !atEnd && (common < diffContextLines || (len(w.hunkLines) > 0 && common < 2*diffContextLines))
}

// addCommonLines adds oldLines[from:to] as context lines.
func (w *hunkWriter) addCommonLines(from, to int) {
	for _, line := range w.oldLines[from:to] {
		w.hunkLines = append(w.hunkLines, " "+line)
		w.hunkCount.old++
		w.hunkCount.new++
	}
}

// flushHunk closes the current hunk with up to diffContextLines of trailing
// context and writes it; an empty hunk writes nothing.
func (w *hunkWriter) flushHunk(start, end linePair) {
	if len(w.hunkLines) == 0 {
		return
	}
	trailing := min(end.old-start.old, diffContextLines)
	w.addCommonLines(start.old, start.old+trailing)
	w.done = linePair{start.old + trailing, start.new + trailing}
	// Line numbers are 1-based; an empty side shows as 0,0.
	if w.hunkCount.old > 0 {
		w.hunkStart.old++
	}
	if w.hunkCount.new > 0 {
		w.hunkStart.new++
	}
	fmt.Fprintf(&w.out, "@@ -%d,%d +%d,%d @@\n", w.hunkStart.old, w.hunkCount.old, w.hunkStart.new, w.hunkCount.new)
	for _, line := range w.hunkLines {
		w.out.WriteString(line)
	}
	w.hunkCount = linePair{}
	w.hunkLines = w.hunkLines[:0]
}

// startHunk opens the next hunk with diffContextLines of leading context
// before end.
func (w *hunkWriter) startHunk(end linePair) {
	w.hunkStart = linePair{end.old - diffContextLines, end.new - diffContextLines}
	w.addCommonLines(w.hunkStart.old, end.old)
	w.done = end
}

// splitLines returns the lines of content, each with its newline. A last line
// without one gets the "\ No newline at end of file" marker of BSD and GNU
// diff.
func splitLines(content []byte) []string {
	lines := strings.SplitAfter(string(content), "\n")
	if lines[len(lines)-1] == "" {
		return lines[:len(lines)-1]
	}
	lines[len(lines)-1] += "\n\\ No newline at end of file\n"
	return lines
}

// lineCount tracks how often a line occurs, 0, 1 or many, on each side.
type lineCount struct{ old, new int }

// uniqueAnchors returns the index pairs of the longest common subsequence of
// the lines that occur exactly once in old and once in new, framed by the
// sentinels {0,0} and {len(old),len(new)}. The subsequence is computed with
// Algorithm A of Thomas G. Szymanski, "A Special Case of the Maximal Common
// Subsequence Problem", Princeton TR #170 (1975).
func uniqueAnchors(oldLines, newLines []string) []linePair {
	oldIndexes, newIndexes := uniqueLineIndexes(oldLines, newLines)
	lengths := increasingRunLengths(newIndexes)
	longest := 0
	for _, length := range lengths {
		longest = max(longest, length)
	}
	sequence := make([]linePair, longest+2)
	sequence[0] = linePair{0, 0}
	sequence[longest+1] = linePair{len(oldLines), len(newLines)}
	next, lastNewIndex := longest, len(newLines)
	for position := len(oldIndexes) - 1; position >= 0; position-- {
		if lengths[position] == next && newIndexes[position] < lastNewIndex {
			sequence[next] = linePair{oldIndexes[position], newIndexes[position]}
			lastNewIndex = newIndexes[position]
			next--
		}
	}
	return sequence
}

// uniqueLineIndexes returns, for every line unique on both sides in old
// order, its index in old and its index in new.
func uniqueLineIndexes(oldLines, newLines []string) (oldIndexes, newIndexes []int) {
	counts := make(map[string]lineCount)
	for _, line := range oldLines {
		count := counts[line]
		count.old = min(count.old+1, 2)
		counts[line] = count
	}
	newIndexOf := make(map[string]int)
	for index, line := range newLines {
		count := counts[line]
		count.new = min(count.new+1, 2)
		counts[line] = count
		newIndexOf[line] = index
	}
	for index, line := range oldLines {
		if count := counts[line]; count.old == 1 && count.new == 1 {
			oldIndexes = append(oldIndexes, index)
			newIndexes = append(newIndexes, newIndexOf[line])
		}
	}
	return oldIndexes, newIndexes
}

// increasingRunLengths returns, for every position of values, the length of
// the longest strictly increasing subsequence of values ending there
// (patience sorting).
func increasingRunLengths(values []int) []int {
	tails := make([]int, 0, len(values))
	lengths := make([]int, len(values))
	for position, value := range values {
		slot := sort.SearchInts(tails, value)
		if slot == len(tails) {
			tails = append(tails, value)
		} else {
			tails[slot] = value
		}
		lengths[position] = slot + 1
	}
	return lengths
}
