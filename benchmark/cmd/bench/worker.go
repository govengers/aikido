package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// prepareWorkspace sets run.WorkDirectory under run.Root: a copy of the
// fixture for coding tasks; for worker tasks setup.sh builds the worker repo,
// the gitops repos, their bare remotes and the cluster state (layout in
// worker-tasks/lib.sh).
func prepareWorkspace(ctx context.Context, run *job) error {
	run.WorkDirectory = filepath.Join(run.Root, "work")
	if !run.Task.Worker {
		return os.CopyFS(run.WorkDirectory, os.DirFS(filepath.Join(run.Task.Directory, "fixture")))
	}
	// The tools are copied into the run's own bin/: a PATH entry named after
	// the benchmark's fake-tools build directory told an agent (opencode, smoke
	// 01.11.2026) to go around them.
	binDirectory := filepath.Join(run.Root, "bin")
	for _, name := range []string{"kubectl", "logcli"} {
		if err := copyExecutable(filepath.Join(run.ToolsDirectory, name), filepath.Join(binDirectory, name)); err != nil {
			return err
		}
	}
	run.Env = workerEnvironment(run.Root, binDirectory)
	if ok, output := runScript(ctx, run.Task, "setup.sh", run.Root, scriptEnvironment(run)); !ok {
		return errors.New("setup.sh failed: " + tail(output, 400))
	}
	return nil
}

func copyExecutable(source, target string) error {
	data, err := os.ReadFile(source) //nolint:gosec // G304: the tools this run built
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), directoryPermissions); err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o700) //nolint:gosec // G306: an executable only the benchmark user runs
}

// workerEnvironment is what the agent and the task scripts of a worker run
// see: the fake tools first on PATH, their state and log outside the
// workspace, a git config of the run's own (no user config, hooks or signing),
// and a kubeconfig that reaches nothing, should a real kubectl ever be found.
func workerEnvironment(root, toolsDirectory string) []string {
	state := filepath.Join(root, "state")
	return []string{
		"PATH=" + toolsDirectory + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_CLUSTER_STATE=" + filepath.Join(state, "cluster.json"),
		"FAKE_LOG=" + filepath.Join(state, "tools.log"),
		"GIT_CONFIG_GLOBAL=" + filepath.Join(root, "gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1",
		"KUBECONFIG=" + os.DevNull,
	}
}

// workerVariables are the names in workerEnvironment besides PATH, the ones a
// harness that filters the environment must pass through.
var workerVariables = []string{"FAKE_CLUSTER_STATE", "FAKE_LOG", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM", "KUBECONFIG"}

// scriptEnvironment adds ROOT for setup.sh, check.sh and solve.sh of worker
// tasks; coding tasks get nothing extra.
func scriptEnvironment(run *job) []string {
	if !run.Task.Worker {
		return nil
	}
	return append(append([]string{}, run.Env...), "ROOT="+run.Root)
}

// buildFakeTools builds the fake kubectl and logcli into directory/fake-tools.
func buildFakeTools(ctx context.Context, directory string) (string, error) {
	tools := filepath.Join(directory, "fake-tools")
	for name, source := range map[string]string{"kubectl": "./cmd/fakekubectl", "logcli": "./cmd/fakelogcli"} {
		//nolint:gosec // G204: building the benchmark's own commands
		build := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(tools, name), source)
		if output, err := build.CombinedOutput(); err != nil {
			return "", fmt.Errorf("build %s: %w: %s", name, err, output)
		}
	}
	return filepath.Abs(tools)
}
