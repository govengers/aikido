//go:build !unix

package shell

import "os/exec"

// ownerStat is fileInfo.Sys; the interpreter reads no owner here.
func ownerStat() any { return nil }

// killProcessGroup keeps exec's default on systems without process groups:
// cancellation kills the program itself.
func killProcessGroup(*exec.Cmd) {}

// endProcessGroup does nothing without process groups.
func endProcessGroup(*exec.Cmd) {}

// signalOf is 0 where exit statuses carry no signal.
func signalOf(*exec.ExitError) uint8 { return 0 }
