//go:build unix

package shell

import (
	"os/exec"
	"syscall"
)

// killProcessGroup starts the program in its own process group and makes
// cancellation kill the whole group, so children it started die with it.
func killProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
}

// signalOf returns the number of the signal that ended the program.
func signalOf(exitErr *exec.ExitError) uint8 {
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return uint8(status.Signal()) //nolint:gosec // G115: signal numbers are below 128
	}
	return 0
}
