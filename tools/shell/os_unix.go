//go:build unix

package shell

import (
	"os"
	"os/exec"
	"syscall"
)

// ownerStat is fileInfo.Sys: the interpreter's test -O and -G read the
// owner from a *syscall.Stat_t (and panic on anything else). Every workspace
// file belongs to the user the shell runs as.
func ownerStat() any {
	return &syscall.Stat_t{Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())} //nolint:gosec // G115: ids fit in uint32 on unix
}

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
