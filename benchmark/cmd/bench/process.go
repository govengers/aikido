package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// killGroup puts the command in its own process group and kills the whole
// group on cancel, so `go test` children or background jobs do not linger.
func killGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = 5 * time.Second
}

// streamParser reads an agent CLI's stdout; transcript names the file in the
// run's log directory the raw stream is kept in.
type streamParser struct {
	transcript string
	parse      func(io.Reader) error
}

// runStreaming runs an agent CLI: stderr goes to stderr.log, stdout is teed
// into the transcript while the parser reads it. The end reason is the
// parser's, else timeout, error or stop.
func runStreaming(ctx context.Context, run *job, command *exec.Cmd, parser streamParser, observed *metrics) error {
	stderr, err := os.Create(filepath.Join(run.LogDirectory, "stderr.log"))
	if err != nil {
		return err
	}
	defer stderr.Close()
	transcript, err := os.Create(filepath.Join(run.LogDirectory, parser.transcript))
	if err != nil {
		return err
	}
	defer transcript.Close()
	command.Stderr = stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return err
	}
	parseError := parser.parse(io.TeeReader(stdout, transcript))
	waitError := command.Wait()
	switch {
	case ctx.Err() != nil:
		observed.EndReason = "timeout"
		return fmt.Errorf("timeout after %s", run.Timeout)
	case waitError != nil:
		if observed.EndReason == "" {
			observed.EndReason = "error"
		}
		stderrText, _ := os.ReadFile(filepath.Join(run.LogDirectory, "stderr.log"))
		return errors.Join(parseError, fmt.Errorf("%s: %w: %s", filepath.Base(command.Path), waitError, tail(string(stderrText), 300)))
	case parseError != nil:
		if observed.EndReason == "" {
			observed.EndReason = "error"
		}
		return parseError
	}
	if observed.EndReason == "" {
		observed.EndReason = "stop"
	}
	return nil
}
