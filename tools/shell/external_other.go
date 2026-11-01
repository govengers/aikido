//go:build !unix

package shell

import "os/exec"

// killProcessGroup keeps exec's default on systems without process groups:
// cancellation kills the program itself.
func killProcessGroup(*exec.Cmd) {}

// signalOf is 0 where exit statuses carry no signal.
func signalOf(*exec.ExitError) uint8 { return 0 }
