//go:build unix

package shell

import (
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestExternalChildrenDoNotOutliveTheProgram covers the review's S9: a
// program that starts a background child and exits left the child running.
func TestExternalChildrenDoNotOutliveTheProgram(t *testing.T) {
	sh := hostProgram(t, "/bin/sh")
	shell := externalShell(t, &ExternalOptions{Name: "spawn", Path: sh, Directory: t.TempDir()})
	result := mustRun(t, shell, "spawn -c 'sleep 30 >/dev/null 2>&1 & echo $!'")
	pid, err := strconv.Atoi(strings.TrimSpace(result.Output))
	if err != nil {
		t.Fatalf("output = %q", result.Output)
	}
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the child %d is still running after the program ended", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
