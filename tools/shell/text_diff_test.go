package shell

import (
	"math/rand/v2"
	"strings"
	"testing"
)

var diffFiles = map[string]string{
	"a.txt":       "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n",
	"b.txt":       "a\nB\nc\nd\ne\nf\ng\nh\ni\nJ\nk\n",
	"same.txt":    "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n",
	"short.txt":   "x\ny\n",
	"nonl.txt":    "x\nz",
	"dup1.txt":    "a\nb\nb\n",
	"dup2.txt":    "a\nb\n",
	"bin1.dat":    "a\x00b\n",
	"bin2.dat":    "a\x00c\n",
	"d1/same.txt": "s\n",
	"d1/only1":    "o\n",
	"d1/chg.txt":  "1\n2\n",
	"d1/sub/x":    "x\n",
	"d2/same.txt": "s\n",
	"d2/only2":    "o\n",
	"d2/chg.txt":  "1\n3\n",
	"d2/sub/x":    "y\n",
	"d3/a.txt":    "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n",
}

// stripTimes drops the timestamps of the unified header lines.
const stripTimes = " | sed '1,2s/\\t.*//'"

var diffCases = []textCase{
	{name: "diff identical", files: diffFiles, script: "diff a.txt same.txt"},
	{
		name: "diff normal", files: diffFiles, script: "diff a.txt b.txt", exit: 1,
		stdout: "2c2\n< b\n---\n> B\n10c10,11\n< j\n---\n> J\n> k\n",
	},
	{name: "diff normal add delete", files: diffFiles, script: "diff dup2.txt dup1.txt", exit: 1, stdout: "2a3\n> b\n"},
	{name: "diff normal delete", files: diffFiles, script: "diff dup1.txt dup2.txt", exit: 1, stdout: "3d2\n< b\n"},
	{
		name: "diff unified", files: diffFiles, script: "diff -u a.txt b.txt" + stripTimes, exit: 0,
		stdout: "--- a.txt\n+++ b.txt\n@@ -1,5 +1,5 @@\n a\n-b\n+B\n c\n d\n e\n@@ -7,4 +7,5 @@\n g\n h\n i\n-j\n+J\n+k\n",
	},
	{
		name: "diff -U1", files: diffFiles, script: "diff -U1 a.txt b.txt" + stripTimes,
		stdout: "--- a.txt\n+++ b.txt\n@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n@@ -9,2 +9,3 @@\n i\n-j\n+J\n+k\n",
	},
	{
		name: "diff -U0", files: diffFiles, script: "diff -U0 a.txt b.txt" + stripTimes,
		stdout: "--- a.txt\n+++ b.txt\n@@ -2 +2 @@\n-b\n+B\n@@ -10 +10,2 @@\n-j\n+J\n+k\n",
	},
	{
		name: "diff --unified=10 merges", files: diffFiles, script: "diff --unified=10 a.txt b.txt" + stripTimes,
		stdout: "--- a.txt\n+++ b.txt\n@@ -1,10 +1,11 @@\n a\n-b\n+B\n c\n d\n e\n f\n g\n h\n i\n-j\n+J\n+k\n",
	},
	{
		name: "diff unified empty old", files: diffFiles, stdin: "", script: "diff -u /dev/null short.txt" + stripTimes,
		stdout: "--- /dev/null\n+++ short.txt\n@@ -0,0 +1,2 @@\n+x\n+y\n",
	},
	{
		name: "diff no newline", files: diffFiles, script: "diff short.txt nonl.txt", exit: 1,
		stdout: "2c2\n< y\n---\n> z\n\\ No newline at end of file\n",
	},
	{
		name: "diff unified no newline", files: diffFiles, script: "diff -u short.txt nonl.txt" + stripTimes,
		stdout: "--- short.txt\n+++ nonl.txt\n@@ -1,2 +1,2 @@\n x\n-y\n+z\n\\ No newline at end of file\n",
	},
	{name: "diff -q", files: diffFiles, script: "diff -q a.txt b.txt", exit: 1, stdout: "Files a.txt and b.txt differ\n"},
	{name: "diff -s", files: diffFiles, script: "diff -s a.txt same.txt", stdout: "Files a.txt and same.txt are identical\n"},
	{name: "diff binary", files: diffFiles, script: "diff bin1.dat bin2.dat", exit: 1, stdout: "Binary files bin1.dat and bin2.dat differ\n"},
	{name: "diff stdin", files: diffFiles, stdin: "x\nq\n", script: "diff - short.txt", exit: 1, stdout: "2c2\n< q\n---\n> y\n"},
	{name: "diff directory and file", files: diffFiles, script: "diff d3 a.txt"},
	{
		name: "diff directories", files: diffFiles, script: "diff d1 d2", exit: 1,
		stdout: "diff d1/chg.txt d2/chg.txt\n2c2\n< 2\n---\n> 3\nOnly in d1: only1\nOnly in d2: only2\nCommon subdirectories: d1/sub and d2/sub\n",
	},
	{
		name: "diff -r", files: diffFiles, script: "diff -r d1 d2", exit: 1,
		stdout: "diff -r d1/chg.txt d2/chg.txt\n2c2\n< 2\n---\n> 3\nOnly in d1: only1\nOnly in d2: only2\ndiff -r d1/sub/x d2/sub/x\n1c1\n< x\n---\n> y\n",
	},
	{
		name: "diff -rq", files: diffFiles, script: "diff -rq d1 d2", exit: 1,
		stdout: "Files d1/chg.txt and d2/chg.txt differ\nOnly in d1: only1\nOnly in d2: only2\nFiles d1/sub/x and d2/sub/x differ\n",
	},
	{name: "diff missing file", files: diffFiles, script: "diff nope a.txt", stderr: "diff: nope: No such file or directory\n", exit: 2},
	{name: "diff -N missing file", files: diffFiles, script: "diff -N nope short.txt", exit: 1, stdout: "0a1,2\n> x\n> y\n"},
	{name: "diff missing operand", script: "diff", stderr: "diff: missing operand after 'diff'\ndiff: Try 'diff --help' for more information.\n", exit: 2},
	{name: "diff one operand", script: "diff a", stderr: "diff: missing operand after 'a'\ndiff: Try 'diff --help' for more information.\n", exit: 2},
	{name: "diff extra operand", script: "diff a b c", stderr: "diff: extra operand 'c'\ndiff: Try 'diff --help' for more information.\n", exit: 2},
	{name: "diff invalid option", script: "diff -z a b", stderr: "diff: invalid option -- 'z'\ndiff: Try 'diff --help' for more information.\n", exit: 2},
}

// TestDiffLinesIsMinimalAndCorrect checks on random inputs that the hunks
// turn the old lines into the new ones and use as few changed lines as a
// longest common subsequence allows.
func TestDiffLinesIsMinimalAndCorrect(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // G404: deterministic test data
	alphabet := []string{"a", "b", "c", "d"}
	for range 500 {
		oldLines := randomLines(random, alphabet)
		newLines := randomLines(random, alphabet)
		hunks := diffLines(oldLines, newLines)
		var rebuilt []string
		changed, position := 0, 0
		for _, hunk := range hunks {
			rebuilt = append(rebuilt, oldLines[position:hunk.aStart]...)
			rebuilt = append(rebuilt, newLines[hunk.bStart:hunk.bEnd]...)
			changed += hunk.aEnd - hunk.aStart + hunk.bEnd - hunk.bStart
			position = hunk.aEnd
		}
		rebuilt = append(rebuilt, oldLines[position:]...)
		if strings.Join(rebuilt, ",") != strings.Join(newLines, ",") {
			t.Fatalf("diff(%v, %v) = %v rebuilds %v", oldLines, newLines, hunks, rebuilt)
		}
		if want := len(oldLines) + len(newLines) - 2*longestCommonSubsequence(oldLines, newLines); changed != want {
			t.Fatalf("diff(%v, %v) changes %d lines, minimum is %d", oldLines, newLines, changed, want)
		}
	}
}

func randomLines(random *rand.Rand, alphabet []string) []string {
	lines := make([]string, random.IntN(12))
	for index := range lines {
		lines[index] = alphabet[random.IntN(len(alphabet))]
	}
	return lines
}

func longestCommonSubsequence(left, right []string) int {
	table := make([][]int, len(left)+1)
	for index := range table {
		table[index] = make([]int, len(right)+1)
	}
	for row := len(left) - 1; row >= 0; row-- {
		for column := len(right) - 1; column >= 0; column-- {
			if left[row] == right[column] {
				table[row][column] = table[row+1][column+1] + 1
			} else {
				table[row][column] = max(table[row+1][column], table[row][column+1])
			}
		}
	}
	return table[0][0]
}
