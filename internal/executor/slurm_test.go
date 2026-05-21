package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type commandCall struct {
	name string
	args []string
}

type fakeRunner struct {
	calls []commandCall
	fn    func(name string, args ...string) (string, string, error)
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, string, error) {
	f.calls = append(f.calls, commandCall{name: name, args: append([]string(nil), args...)})
	if f.fn != nil {
		return f.fn(name, args...)
	}
	return "", "", nil
}

func TestSlurmExecutorSubmitBuildsSbatchArgsAndWrapper(t *testing.T) {
	runner := &fakeRunner{fn: func(name string, args ...string) (string, string, error) {
		if name != "sbatch" {
			t.Fatalf("command = %q, want sbatch", name)
		}
		return "12345;cluster\n", "", nil
	}}
	dir := t.TempDir()
	e := NewSlurmExecutorWithRunner(SlurmConfig{
		Type:          SlurmNative,
		SubmitCommand: "sbatch",
		QueryCommand:  "sacct",
		Partition:     "batch",
		Account:       "co2m",
		Defaults:      ResourceRequest{CPUsPerTask: 2, MemGB: 8, Walltime: "PT30M"},
	}, runner, nil)
	submission, err := e.Submit(context.Background(), JobDescription{
		RunID:       "run-1",
		TaskID:      "task-1",
		RunRef:      "task-1/r0",
		StationID:   "station-1",
		WorkingRoot: dir,
		Executable:  "/opt/proc/run.sh",
		Args:        []string{"--task", "task-1"},
		WindowStart: time.Unix(0, 0).UTC(),
		WindowEnd:   time.Unix(3600, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if submission.SchedulerID != "12345" || submission.ExecutorType != SlurmNative {
		t.Fatalf("submission = %+v", submission)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d", len(runner.calls))
	}
	args := runner.calls[0].args
	for _, want := range []string{
		"--parsable", "--job-name=vp-task-1-r0", "--partition=batch", "--account=co2m",
		"--cpus-per-task=2", "--mem=8G", "--time=00:30:00",
		"--output=" + filepath.Join(dir, "logs", "run_out.log"),
		"--error=" + filepath.Join(dir, "logs", "run_err.log"),
	} {
		if !contains(args, want) {
			t.Fatalf("sbatch args missing %q: %v", want, args)
		}
	}
	wrapperPath := args[len(args)-1]
	if wrapperPath != filepath.Join(dir, ".veriproc", "slurm-wrapper.sh") {
		t.Fatalf("wrapper path = %q", wrapperPath)
	}
	wrapper, err := os.ReadFile(wrapperPath)
	if err != nil {
		t.Fatalf("read wrapper: %v", err)
	}
	content := string(wrapper)
	for _, want := range []string{"VERIPROC_RUN_DIR", ">> \"$WORKDIR/logs/run_out.log\"", ">> \"$WORKDIR/logs/run_err.log\"", ".exit_code", "trap handle_sigterm SIGTERM", "'/opt/proc/run.sh' \"${ARGS[@]}\"", "=== VeriProc run:", "SLURM_JOB_ID"} {
		if !strings.Contains(content, want) {
			t.Fatalf("wrapper missing %q:\n%s", want, content)
		}
	}
}

func TestSlurmExecutorDockerWrapper(t *testing.T) {
	runner := &fakeRunner{fn: func(string, ...string) (string, string, error) { return "67890\n", "", nil }}
	dir := t.TempDir()
	e := NewSlurmExecutorWithRunner(SlurmConfig{
		Type:          SlurmDocker,
		SubmitCommand: "sbatch",
		QueryCommand:  "sacct",
		Docker:        DockerDefaults{DefaultMounts: []string{"/shared:/shared"}, User: "host"},
	}, runner, nil)
	_, err := e.Submit(context.Background(), JobDescription{
		RunID:       "run-docker",
		TaskID:      "task-docker",
		RunRef:      "task-docker/r0",
		StationID:   "station-docker",
		WorkingRoot: dir,
		Executable:  "/work/run.sh",
		WindowStart: time.Unix(0, 0).UTC(),
		WindowEnd:   time.Unix(1, 0).UTC(),
		Container:   ContainerConfig{Image: "registry.example.test/proc:latest"},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	wrapper, err := os.ReadFile(filepath.Join(dir, ".veriproc", "slurm-wrapper.sh"))
	if err != nil {
		t.Fatalf("read wrapper: %v", err)
	}
	content := string(wrapper)
	for _, want := range []string{"docker run --rm", "docker stop", "registry.example.test/proc:latest", "-v '/shared:/shared'", "-u \"$(id -u):$(id -g)\""} {
		if !strings.Contains(content, want) {
			t.Fatalf("docker wrapper missing %q:\n%s", want, content)
		}
	}
}

func TestSlurmExecutorDockerRequiresImage(t *testing.T) {
	e := NewSlurmExecutorWithRunner(SlurmConfig{Type: SlurmDocker}, &fakeRunner{}, nil)
	_, err := e.Submit(context.Background(), JobDescription{RunID: "r", WorkingRoot: t.TempDir(), Executable: "/bin/true"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSlurmExecutorPollMapsSacctStates(t *testing.T) {
	runner := &fakeRunner{fn: func(name string, args ...string) (string, string, error) {
		if name != "sacct" {
			t.Fatalf("command = %q, want sacct", name)
		}
		return "FAILED|7:0|00:01:00|node1\n", "", nil
	}}
	e := NewSlurmExecutorWithRunner(SlurmConfig{Type: SlurmNative, QueryCommand: "sacct"}, runner, func() time.Time { return time.Unix(10, 0).UTC() })
	obs, err := e.Poll(context.Background(), "12345")
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if obs.Status != StatusFailed || obs.ExitCode != 7 || !strings.Contains(obs.NativeState, "FAILED") {
		t.Fatalf("obs = %+v", obs)
	}
	if obs.Node != "node1" {
		t.Fatalf("obs.Node = %q, want %q", obs.Node, "node1")
	}
}

func TestSlurmExecutorCancel(t *testing.T) {
	runner := &fakeRunner{}
	e := NewSlurmExecutorWithRunner(SlurmConfig{Type: SlurmNative, CancelCommand: "scancel"}, runner, nil)
	if err := e.Cancel(context.Background(), "12345"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if len(runner.calls) != 1 || runner.calls[0].name != "scancel" || runner.calls[0].args[0] != "12345" {
		t.Fatalf("calls = %+v", runner.calls)
	}
	unsupported := NewSlurmExecutorWithRunner(SlurmConfig{Type: SlurmNative}, &fakeRunner{}, nil)
	if err := unsupported.Cancel(context.Background(), "12345"); !errors.Is(err, ErrCancellationUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
