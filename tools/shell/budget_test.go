package shell

import (
	"sort"
	"strings"
	"testing"
)

// budgetUsed is what the Shell's memory budget holds right now.
func budgetUsed(shell *Shell) int {
	shell.budget.mutex.Lock()
	defer shell.budget.mutex.Unlock()
	return shell.budget.used
}

// TestClosedRedirectsAreReleased replays the review's "while :; do cat seed
// >/tmp/same; done": every closed redirect stayed registered with its
// content until Run ended, so replacing one file over and over grew memory
// without bound.
func TestClosedRedirectsAreReleased(t *testing.T) {
	seed := strings.Repeat("x", 1<<20)
	shell, _ := newTestShell(t, map[string]string{"seed": seed}, nil)
	shell.budget = newMemoryBudget(3 << 20)
	shell.scratch = newScratchStorage(shell.budget)
	result := mustRun(t, shell, "for i in $(seq 30); do cat seed >/tmp/same; done; wc -c </tmp/same")
	if result.Output != "1048576\n" {
		t.Fatalf("output = %q", result.Output)
	}
	if used := budgetUsed(shell); used != 1<<20 {
		t.Fatalf("budget holds %d bytes after the Run, want only /tmp/same", used)
	}
}

// TestProcessFilesAreTheShells replays the review's "for i in {1..10000};
// do cp seed /dev/fd/$i; done": /dev/fd took any write, outside every cap.
func TestProcessFilesAreTheShells(t *testing.T) {
	shell, _ := newTestShell(t, map[string]string{"seed": "x\n"}, nil)
	result := mustRun(t, shell, "cp seed /dev/fd/5; echo x > /dev/fd/6; echo $?; rm /dev/fd/63; cat <(echo own)")
	want := "cp: cannot create regular file '/dev/fd/5': Permission denied\nbash: line 1: /dev/fd/6: Permission denied\n1\n" +
		"rm: cannot remove '/dev/fd/63': No such file or directory\nown\n"
	if result.Output != want {
		t.Fatalf("output = %q, want %q", result.Output, want)
	}
}

// TestProcessFilesCountAgainstTheBudget replays the review's "for i in $(seq
// 1 500); do eval 'cat <(seq 1 8000000) >/dev/null'; done": every eval
// parse stored a new uncapped /dev/fd file for the whole Run. They now count
// against the Shell's budget, a redirect that reads one drops it, and they
// are gone after the Run.
func TestProcessFilesCountAgainstTheBudget(t *testing.T) {
	shell, _ := newTestShell(t, map[string]string{"seed": strings.Repeat("0", 600)}, nil)
	shell.budget = newMemoryBudget(1000)
	shell.scratch = newScratchStorage(shell.budget)
	result := mustRun(t, shell, "for i in 1 2 3 4 5; do while read -r l; do :; done < <(cat seed); done; "+
		"for i in 1 2 3; do eval 'cat <(cat seed) >/dev/null'; done; echo done")
	full := "cat: No space left on device\n"
	if result.Output != full+full+"done\n" {
		t.Fatalf("output = %q", result.Output)
	}
	if used := budgetUsed(shell); used != 0 {
		t.Fatalf("budget holds %d bytes after the Run", used)
	}
}

// TestProcessFilesPerEvaluation replays the review's concurrent function
// calls: the /dev/fd name was chosen when the function was parsed, so the
// second call overwrote the first one's file before it was read.
func TestProcessFilesPerEvaluation(t *testing.T) {
	shell, _ := newTestShell(t, nil, nil)
	result := mustRun(t, shell, `f(){ cat <(echo "$1") <(sleep "$2"); }; f A .2 & sleep .05; f B .1 & wait`)
	lines := strings.Split(strings.TrimSpace(result.Output), "\n")
	sort.Strings(lines)
	if strings.Join(lines, ",") != "A,B" {
		t.Fatalf("output = %q", result.Output)
	}
}
