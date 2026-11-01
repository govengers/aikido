package shell

// diffHunk is one change: lines [aStart, aEnd) of the old file become lines
// [bStart, bEnd) of the new one, 0-based.
type diffHunk struct {
	aStart, aEnd, bStart, bEnd int
}

// diffLines computes a minimal line diff (Myers' O(ND) algorithm in linear
// space) and slides each change like GNU diff, so ambiguous changes sit at
// the same place as in GNU's output.
func diffLines(oldLines, newLines []string) []diffHunk {
	ids := map[string]int{}
	identify := func(lines []string) []int {
		result := make([]int, len(lines))
		for index, line := range lines {
			id, ok := ids[line]
			if !ok {
				id = len(ids)
				ids[line] = id
			}
			result[index] = id
		}
		return result
	}
	comparison := &diffComparison{
		a: identify(oldLines), b: identify(newLines),
		changedA: make([]bool, len(oldLines)), changedB: make([]bool, len(newLines)),
	}
	comparison.compare(0, len(oldLines), 0, len(newLines))
	shiftBoundaries(comparison.a, comparison.changedA, comparison.changedB)
	shiftBoundaries(comparison.b, comparison.changedB, comparison.changedA)
	return collectHunks(comparison.changedA, comparison.changedB)
}

// diffComparison marks the lines of a and b that are not in the common
// subsequence.
type diffComparison struct {
	a, b               []int
	changedA, changedB []bool
}

// compare marks the changes between a[aLow:aHigh] and b[bLow:bHigh].
func (c *diffComparison) compare(aLow, aHigh, bLow, bHigh int) {
	for aLow < aHigh && bLow < bHigh && c.a[aLow] == c.b[bLow] {
		aLow++
		bLow++
	}
	for aLow < aHigh && bLow < bHigh && c.a[aHigh-1] == c.b[bHigh-1] {
		aHigh--
		bHigh--
	}
	if aLow == aHigh || bLow == bHigh {
		c.markChanged(aLow, aHigh, bLow, bHigh)
		return
	}
	x, y, ok := c.middleSnake(aLow, aHigh, bLow, bHigh)
	if !ok || (x == aLow && y == bLow) || (x == aHigh && y == bHigh) {
		c.markChanged(aLow, aHigh, bLow, bHigh)
		return
	}
	c.compare(aLow, x, bLow, y)
	c.compare(x, aHigh, y, bHigh)
}

func (c *diffComparison) markChanged(aLow, aHigh, bLow, bHigh int) {
	for index := aLow; index < aHigh; index++ {
		c.changedA[index] = true
	}
	for index := bLow; index < bHigh; index++ {
		c.changedB[index] = true
	}
}

// middleSnake finds a point on an optimal edit path between the two ranges
// by searching from both ends until the paths meet.
func (c *diffComparison) middleSnake(aLow, aHigh, bLow, bHigh int) (int, int, bool) {
	n, m := aHigh-aLow, bHigh-bLow
	maximum := (n + m + 1) / 2
	offset := maximum + 1
	forward := make([]int, 2*offset+1)
	backward := make([]int, 2*offset+1)
	for index := range forward {
		forward[index], backward[index] = -1, -1
	}
	forward[offset+1], backward[offset+1] = 0, 0
	delta := n - m
	odd := delta%2 != 0
	for depth := 0; depth <= maximum; depth++ {
		for diagonal := -depth; diagonal <= depth; diagonal += 2 {
			x := c.advance(forward, offset, diagonal, depth)
			y := x - diagonal
			for x < n && y >= 0 && y < m && c.a[aLow+x] == c.b[bLow+y] {
				x++
				y++
			}
			forward[offset+diagonal] = x
			if other := offset + delta - diagonal; odd && x <= n && y <= m && other >= 0 && other < len(backward) && backward[other] >= 0 && x+backward[other] >= n {
				return aLow + x, bLow + y, true
			}
		}
		for diagonal := -depth; diagonal <= depth; diagonal += 2 {
			x := c.advance(backward, offset, diagonal, depth)
			y := x - diagonal
			for x < n && y >= 0 && y < m && c.a[aHigh-x-1] == c.b[bHigh-y-1] {
				x++
				y++
			}
			backward[offset+diagonal] = x
			if other := offset + delta - diagonal; !odd && x <= n && y <= m && other >= 0 && other < len(forward) && forward[other] >= 0 && forward[other]+x >= n {
				return aHigh - x, bHigh - y, true
			}
		}
	}
	return 0, 0, false
}

// advance picks the furthest reaching start on a diagonal for depth.
func (c *diffComparison) advance(furthest []int, offset, diagonal, depth int) int {
	if diagonal == -depth || (diagonal != depth && furthest[offset+diagonal-1] < furthest[offset+diagonal+1]) {
		return max(furthest[offset+diagonal+1], 0)
	}
	return furthest[offset+diagonal-1] + 1
}

// shiftBoundaries slides every run of changed lines of one file over equal
// neighbours: first up as far as it goes, then down, and finally back to the
// lowest position where it lines up with a change in the other file, the
// placement GNU diff chooses.
func shiftBoundaries(lines []int, changed, otherChanged []bool) {
	otherGaps := changedGaps(otherChanged)
	run := &changeRun{lines: lines, changed: changed}
	for run.start < len(lines) {
		if !changed[run.start] {
			run.unchangedBefore++
			run.start++
			continue
		}
		run.end = run.start
		for run.end < len(lines) && changed[run.end] {
			run.end++
		}
		var aligned int
		for {
			size := run.end - run.start
			run.slideUp()
			aligned = -1
			if otherGaps[run.unchangedBefore] {
				aligned = run.end
			}
			for run.slideDown() {
				if otherGaps[run.unchangedBefore] {
					aligned = run.end
				}
			}
			if run.end-run.start == size {
				break
			}
		}
		for aligned >= 0 && run.end > aligned {
			run.moveUp()
		}
		run.start = run.end
	}
}

// changeRun is the run of changed lines shiftBoundaries is moving;
// unchangedBefore counts the unchanged lines before start.
type changeRun struct {
	lines           []int
	changed         []bool
	start, end      int
	unchangedBefore int
}

// moveUp shifts the run up by one line.
func (r *changeRun) moveUp() {
	r.start--
	r.end--
	r.changed[r.start], r.changed[r.end] = true, false
	r.unchangedBefore--
}

// slideUp moves the run up while the line above equals its last line,
// merging runs it reaches.
func (r *changeRun) slideUp() {
	for r.start > 0 && r.lines[r.start-1] == r.lines[r.end-1] {
		r.moveUp()
		for r.start > 0 && r.changed[r.start-1] {
			r.start--
		}
	}
}

// slideDown moves the run down one line if the line below equals its first
// line, merging a run it reaches; false when it cannot move.
func (r *changeRun) slideDown() bool {
	if r.end >= len(r.lines) || r.lines[r.start] != r.lines[r.end] {
		return false
	}
	r.changed[r.start], r.changed[r.end] = false, true
	r.start++
	r.end++
	r.unchangedBefore++
	for r.end < len(r.lines) && r.changed[r.end] {
		r.end++
	}
	return true
}

// changedGaps says for each gap between unchanged lines (gap i lies before
// the i-th unchanged line) whether changed lines sit in it.
func changedGaps(changed []bool) []bool {
	gaps := []bool{false}
	for _, isChanged := range changed {
		if isChanged {
			gaps[len(gaps)-1] = true
			continue
		}
		gaps = append(gaps, false)
	}
	return gaps
}

// collectHunks pairs the runs of changed lines of both files.
func collectHunks(changedA, changedB []bool) []diffHunk {
	var hunks []diffHunk
	a, b := 0, 0
	for a < len(changedA) || b < len(changedB) {
		if a < len(changedA) && b < len(changedB) && !changedA[a] && !changedB[b] {
			a++
			b++
			continue
		}
		hunk := diffHunk{aStart: a, bStart: b}
		for a < len(changedA) && changedA[a] {
			a++
		}
		for b < len(changedB) && changedB[b] {
			b++
		}
		hunk.aEnd, hunk.bEnd = a, b
		hunks = append(hunks, hunk)
	}
	return hunks
}
