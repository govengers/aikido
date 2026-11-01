package shell

import "context"

// diffHunk is one change: lines [aStart, aEnd) of the old file become lines
// [bStart, bEnd) of the new one, 0-based.
type diffHunk struct {
	aStart, aEnd, bStart, bEnd int
}

// diffLines computes a minimal line diff (Myers' O(ND) algorithm in linear
// space) and slides each change like GNU diff, so ambiguous changes sit at
// the same place as in GNU's output. The search, quadratic for files that
// share little, stops with ctx's error.
func diffLines(ctx context.Context, oldLines, newLines []string) ([]diffHunk, error) {
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
	comparison.compare(ctx, 0, len(oldLines), 0, len(newLines))
	if comparison.err != nil {
		return nil, comparison.err
	}
	shiftBoundaries(comparison.a, comparison.changedA, comparison.changedB)
	shiftBoundaries(comparison.b, comparison.changedB, comparison.changedA)
	return collectHunks(comparison.changedA, comparison.changedB), nil
}

// diffComparison marks the lines of a and b that are not in the common
// subsequence.
type diffComparison struct {
	a, b               []int
	changedA, changedB []bool
	// err is ctx's error once the search was stopped.
	err error
}

// compare marks the changes between a[aLow:aHigh] and b[bLow:bHigh].
func (c *diffComparison) compare(ctx context.Context, aLow, aHigh, bLow, bHigh int) {
	if c.err != nil {
		return
	}
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
	splitOld, splitNew, ok := c.middleSnake(ctx, aLow, aHigh, bLow, bHigh)
	if c.err != nil {
		return
	}
	if !ok || (splitOld == aLow && splitNew == bLow) || (splitOld == aHigh && splitNew == bHigh) {
		c.markChanged(aLow, aHigh, bLow, bHigh)
		return
	}
	c.compare(ctx, aLow, splitOld, bLow, splitNew)
	c.compare(ctx, splitOld, aHigh, splitNew, bHigh)
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
func (c *diffComparison) middleSnake(ctx context.Context, aLow, aHigh, bLow, bHigh int) (int, int, bool) {
	oldLength, newLength := aHigh-aLow, bHigh-bLow
	maximum := (oldLength + newLength + 1) / 2
	search := &snakeSearch{
		comparison: c, aLow: aLow, aHigh: aHigh, bLow: bLow, bHigh: bHigh,
		oldLength: oldLength, newLength: newLength, offset: maximum + 1, delta: oldLength - newLength,
		forward: make([]int, 2*maximum+3), backward: make([]int, 2*maximum+3),
	}
	for index := range search.forward {
		search.forward[index], search.backward[index] = -1, -1
	}
	search.forward[search.offset+1], search.backward[search.offset+1] = 0, 0
	for depth := 0; depth <= maximum; depth++ {
		if err := ctx.Err(); err != nil {
			c.err = err
			return 0, 0, false
		}
		if splitOld, splitNew, found := search.step(depth, true); found {
			return splitOld, splitNew, true
		}
		if splitOld, splitNew, found := search.step(depth, false); found {
			return splitOld, splitNew, true
		}
	}
	return 0, 0, false
}

// snakeSearch is the state of one middle-snake search: the furthest
// reaching old-file positions per diagonal, from the start and from the end.
type snakeSearch struct {
	comparison               *diffComparison
	aLow, aHigh, bLow, bHigh int
	oldLength, newLength     int
	offset, delta            int
	forward, backward        []int
}

// step extends every diagonal of depth in one direction and returns the
// split point once the two searches overlap.
func (s *snakeSearch) step(depth int, forwards bool) (int, int, bool) {
	furthest, opposite := s.forward, s.backward
	if !forwards {
		furthest, opposite = s.backward, s.forward
	}
	checks := (s.delta%2 != 0) == forwards
	for diagonal := -depth; diagonal <= depth; diagonal += 2 {
		oldPosition := advance(furthest, s.offset, diagonal, depth)
		newPosition := oldPosition - diagonal
		for oldPosition < s.oldLength && newPosition >= 0 && newPosition < s.newLength && s.equal(oldPosition, newPosition, forwards) {
			oldPosition++
			newPosition++
		}
		furthest[s.offset+diagonal] = oldPosition
		if !checks || !s.overlaps(opposite, diagonal, oldPosition, newPosition) {
			continue
		}
		if forwards {
			return s.aLow + oldPosition, s.bLow + newPosition, true
		}
		return s.aHigh - oldPosition, s.bHigh - newPosition, true
	}
	return 0, 0, false
}

// equal compares the lines at the positions, counted from the end when the
// search runs backwards.
func (s *snakeSearch) equal(oldPosition, newPosition int, forwards bool) bool {
	if forwards {
		return s.comparison.a[s.aLow+oldPosition] == s.comparison.b[s.bLow+newPosition]
	}
	return s.comparison.a[s.aHigh-oldPosition-1] == s.comparison.b[s.bHigh-newPosition-1]
}

// overlaps reports whether a path end meets the opposite search's path on
// the corresponding diagonal.
func (s *snakeSearch) overlaps(opposite []int, diagonal, oldPosition, newPosition int) bool {
	other := s.offset + s.delta - diagonal
	return oldPosition <= s.oldLength && newPosition <= s.newLength && other >= 0 && other < len(opposite) &&
		opposite[other] >= 0 && oldPosition+opposite[other] >= s.oldLength
}

// advance picks the furthest reaching start on a diagonal for depth.
func advance(furthest []int, offset, diagonal, depth int) int {
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
		aligned := run.settle(otherGaps)
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

// settle slides the run up and down until it stops growing and returns the
// lowest end at which it lines up with a change in the other file, or -1.
func (r *changeRun) settle(otherGaps []bool) int {
	for {
		size := r.end - r.start
		r.slideUp()
		aligned := -1
		if otherGaps[r.unchangedBefore] {
			aligned = r.end
		}
		for r.slideDown() {
			if otherGaps[r.unchangedBefore] {
				aligned = r.end
			}
		}
		if r.end-r.start == size {
			return aligned
		}
	}
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
	oldIndex, newIndex := 0, 0
	for oldIndex < len(changedA) || newIndex < len(changedB) {
		if oldIndex < len(changedA) && newIndex < len(changedB) && !changedA[oldIndex] && !changedB[newIndex] {
			oldIndex++
			newIndex++
			continue
		}
		hunk := diffHunk{aStart: oldIndex, bStart: newIndex}
		for oldIndex < len(changedA) && changedA[oldIndex] {
			oldIndex++
		}
		for newIndex < len(changedB) && changedB[newIndex] {
			newIndex++
		}
		hunk.aEnd, hunk.bEnd = oldIndex, newIndex
		hunks = append(hunks, hunk)
	}
	return hunks
}
