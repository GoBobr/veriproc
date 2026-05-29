package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	SlurmNative = "slurm-native"
	SlurmDocker = "slurm-docker"
)

// SlurmConfig configures a SLURM-backed executor.
type SlurmConfig struct {
	Type          string
	Connection    SlurmConnection
	Account       string
	Partition     string
	QOS           string
	SubmitCommand string
	QueryCommand  string
	CancelCommand string
	Defaults      ResourceRequest
	Docker        DockerDefaults
}

// SlurmConnection describes where scheduler commands run.
type SlurmConnection struct {
	Mode    string
	Host    string
	User    string
	KeyFile string
}

// DockerDefaults contains instance-level Docker settings.
type DockerDefaults struct {
	DefaultMounts []string
	User          string
}

// CommandRunner executes external commands. Tests can replace it with a fake.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) (stdout string, stderr string, err error)
}

type localCommandRunner struct{}

func (localCommandRunner) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 - operator-configured scheduler command
	var stdout strings.Builder
	var stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// SlurmExecutor submits prepared runs to SLURM.
type SlurmExecutor struct {
	mode   string
	cfg    SlurmConfig
	runner CommandRunner
	clock  func() time.Time
}

// NewSlurmExecutor returns a SLURM executor using local OS commands.
func NewSlurmExecutor(cfg SlurmConfig) *SlurmExecutor {
	return NewSlurmExecutorWithRunner(cfg, localCommandRunner{}, nil)
}

// NewSlurmExecutorWithRunner returns a SLURM executor with an injected runner.
func NewSlurmExecutorWithRunner(cfg SlurmConfig, runner CommandRunner, clock func() time.Time) *SlurmExecutor {
	if runner == nil {
		runner = localCommandRunner{}
	}
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	mode := strings.TrimSpace(strings.ToLower(cfg.Type))
	if mode == "" {
		mode = SlurmNative
	}
	cfg.Type = mode
	cfg.Connection.Mode = strings.TrimSpace(strings.ToLower(cfg.Connection.Mode))
	if cfg.Connection.Mode == "" {
		cfg.Connection.Mode = "local"
	}
	if cfg.SubmitCommand == "" {
		cfg.SubmitCommand = "sbatch"
	}
	if cfg.QueryCommand == "" {
		cfg.QueryCommand = "sacct"
	}
	return &SlurmExecutor{mode: mode, cfg: cfg, runner: runner, clock: clock}
}

func (e *SlurmExecutor) Type() string { return e.mode }

func (e *SlurmExecutor) SupportsCancellation() bool {
	return strings.TrimSpace(e.cfg.CancelCommand) != ""
}

func (e *SlurmExecutor) Submit(ctx context.Context, desc JobDescription) (Submission, error) {
	effective, err := e.effective(desc)
	if err != nil {
		return Submission{}, err
	}
	if desc.Executable == "" {
		return Submission{}, fmt.Errorf("%w: %s executor: executable is empty for run %s", ErrFatalSubmit, effective.Mode, desc.RunID)
	}
	if err := os.MkdirAll(filepath.Join(desc.WorkingRoot, "logs"), 0o755); err != nil {
		return Submission{}, fmt.Errorf("slurm executor: mkdir logs: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(desc.WorkingRoot, "output"), 0o755); err != nil {
		return Submission{}, fmt.Errorf("slurm executor: mkdir output: %w", err)
	}
	wrapperPath, err := e.writeWrapper(desc, effective)
	if err != nil {
		return Submission{}, err
	}
	args := e.sbatchArgs(desc, effective, wrapperPath)
	stdout, stderr, err := e.runCommand(ctx, e.cfg.SubmitCommand, args...)
	if err != nil {
		stderrMsg := strings.TrimSpace(stderr)
		// SSH exit status 255 means the SSH transport or authentication itself
		// failed (user not found, host unreachable, auth failure, etc.).
		// This is a permanent infrastructure misconfiguration; wrap as fatal.
		// Transient DNS failures ("Try again") are excluded and remain retryable.
		if e.cfg.Connection.Mode == "ssh" && isSshTransportError(err, stderrMsg) {
			return Submission{}, fmt.Errorf("%w: slurm submit: %w: %s", ErrFatalSubmit, err, stderrMsg)
		}
		return Submission{}, fmt.Errorf("slurm submit: %w: %s", err, stderrMsg)
	}
	schedulerID := parseSbatchID(stdout)
	if schedulerID == "" {
		return Submission{}, fmt.Errorf("slurm submit: could not parse scheduler id from %q", strings.TrimSpace(stdout))
	}
	return Submission{SchedulerID: schedulerID, ExecutorType: effective.Mode}, nil
}

func (e *SlurmExecutor) Poll(ctx context.Context, schedulerID string) (Observation, error) {
	if schedulerID == "" {
		return Observation{}, ErrUnknownJob
	}
	args := []string{"-j", schedulerID, "--format=State,ExitCode,Elapsed,NodeList", "-n", "-P"}
	stdout, stderr, err := e.runCommand(ctx, e.cfg.QueryCommand, args...)
	if err != nil {
		return Observation{}, fmt.Errorf("slurm query: %w: %s", err, strings.TrimSpace(stderr))
	}
	obs := parseSacctObservation(stdout, e.clock())
	if obs.NativeState == "" {
		obs.NativeState = "UNKNOWN"
	}
	return obs, nil
}

func (e *SlurmExecutor) Cancel(ctx context.Context, schedulerID string) error {
	if !e.SupportsCancellation() {
		return ErrCancellationUnsupported
	}
	if schedulerID == "" {
		return ErrUnknownJob
	}
	_, stderr, err := e.runCommand(ctx, e.cfg.CancelCommand, schedulerID)
	if err != nil {
		return fmt.Errorf("slurm cancel: %w: %s", err, strings.TrimSpace(stderr))
	}
	return nil
}

type effectiveSlurm struct {
	Mode      string
	Resources ResourceRequest
	Slurm     SlurmOverrides
	Container ContainerConfig
}

func (e *SlurmExecutor) effective(desc JobDescription) (effectiveSlurm, error) {
	mode := strings.TrimSpace(strings.ToLower(desc.Mode))
	if mode == "" {
		mode = e.mode
	}
	switch mode {
	case SlurmNative, SlurmDocker:
	case "local", "stub":
		// Station requested a non-SLURM mode; fall back to the instance default.
		mode = e.mode
	default:
		return effectiveSlurm{}, fmt.Errorf("slurm executor: unsupported execution mode %q", mode)
	}
	resources := e.cfg.Defaults
	if desc.Resources.CPUsPerTask != 0 {
		resources.CPUsPerTask = desc.Resources.CPUsPerTask
	}
	if desc.Resources.MemGB != 0 {
		resources.MemGB = desc.Resources.MemGB
	}
	if desc.Resources.Walltime != "" {
		resources.Walltime = desc.Resources.Walltime
	}
	slurm := SlurmOverrides{Partition: e.cfg.Partition, Account: e.cfg.Account, QOS: e.cfg.QOS}
	if desc.Slurm.Partition != "" {
		slurm.Partition = desc.Slurm.Partition
	}
	if desc.Slurm.Account != "" {
		slurm.Account = desc.Slurm.Account
	}
	if desc.Slurm.QOS != "" {
		slurm.QOS = desc.Slurm.QOS
	}
	slurm.ExtraArgs = append(slurm.ExtraArgs, desc.Slurm.ExtraArgs...)
	container := ContainerConfig{Mounts: append([]string(nil), e.cfg.Docker.DefaultMounts...), User: e.cfg.Docker.User}
	container.Image = desc.Container.Image
	if len(desc.Container.Mounts) > 0 {
		container.Mounts = append(container.Mounts, desc.Container.Mounts...)
	}
	if desc.Container.User != "" {
		container.User = desc.Container.User
	}
	if mode == SlurmDocker && container.Image == "" {
		return effectiveSlurm{}, errors.New("slurm-docker executor requires execution.container.image")
	}
	return effectiveSlurm{Mode: mode, Resources: resources, Slurm: slurm, Container: container}, nil
}

func (e *SlurmExecutor) sbatchArgs(desc JobDescription, effective effectiveSlurm, wrapperPath string) []string {
	args := []string{"--parsable", "--job-name=" + safeJobName("vp-"+desc.RunRef)}
	if effective.Slurm.Account != "" {
		args = append(args, "--account="+effective.Slurm.Account)
	}
	if effective.Slurm.Partition != "" {
		args = append(args, "--partition="+effective.Slurm.Partition)
	}
	if effective.Slurm.QOS != "" {
		args = append(args, "--qos="+effective.Slurm.QOS)
	}
	if effective.Resources.CPUsPerTask > 0 {
		args = append(args, "--cpus-per-task="+strconv.Itoa(effective.Resources.CPUsPerTask))
	}
	if effective.Resources.MemGB > 0 {
		args = append(args, fmt.Sprintf("--mem=%dG", effective.Resources.MemGB))
	}
	if effective.Resources.Walltime != "" {
		args = append(args, "--time="+normalizeSlurmWalltime(effective.Resources.Walltime))
	}
	// Redirect SLURM's own stdout/stderr into the standard run log files so
	// no extra slurm-JOBID.out files accumulate anywhere and all output is
	// consolidated in one place.
	logsDir := filepath.Join(desc.WorkingRoot, "logs")
	args = append(args, "--output="+filepath.Join(logsDir, "run_out.log"))
	args = append(args, "--error="+filepath.Join(logsDir, "run_err.log"))
	args = append(args, effective.Slurm.ExtraArgs...)
	args = append(args, wrapperPath)
	return args
}

func (e *SlurmExecutor) writeWrapper(desc JobDescription, effective effectiveSlurm) (string, error) {
	dir := filepath.Join(desc.WorkingRoot, ".veriproc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("slurm executor: mkdir wrapper dir: %w", err)
	}
	path := filepath.Join(dir, "slurm-wrapper.sh")
	content := renderWrapper(desc, effective)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		return "", fmt.Errorf("slurm executor: write wrapper: %w", err)
	}
	return path, nil
}

func renderWrapper(desc JobDescription, effective effectiveSlurm) string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	b.WriteString("set -u\n\n")
	b.WriteString("RETCODE=0\n")
	b.WriteString("DOCKER_CONTAINER_NAME=\"\"\n")
	b.WriteString("WORKDIR=" + shellQuote(desc.WorkingRoot) + "\n")
	b.WriteString("mkdir -p \"$WORKDIR/logs\" \"$WORKDIR/output\"\n\n")
	b.WriteString("cleanup() {\n")
	b.WriteString("  local exit_code=$?\n")
	b.WriteString("  if [[ $RETCODE -ne 0 ]]; then exit_code=$RETCODE; fi\n")
	if effective.Mode == SlurmDocker {
		b.WriteString("  if [[ -n \"$DOCKER_CONTAINER_NAME\" ]]; then\n")
		b.WriteString("    if docker ps -q -f name=\"$DOCKER_CONTAINER_NAME\" 2>/dev/null | grep -q .; then\n")
		b.WriteString("      docker stop \"$DOCKER_CONTAINER_NAME\" >/dev/null 2>&1 || true\n")
		b.WriteString("    fi\n")
		b.WriteString("  fi\n")
	}
	b.WriteString("  echo \"${exit_code}\" > \"$WORKDIR/.exit_code\"\n")
	b.WriteString("}\n")
	b.WriteString("trap cleanup EXIT\n")
	b.WriteString("handle_sigterm() { RETCODE=143; exit 143; }\n")
	b.WriteString("handle_sigint() { RETCODE=130; exit 130; }\n")
	b.WriteString("trap handle_sigterm SIGTERM\n")
	b.WriteString("trap handle_sigint SIGINT\n\n")
	// Print execution context banner into run_out.log before the workload starts.
	b.WriteString("{\n")
	b.WriteString("  echo \"=== VeriProc run: " + shellQuote(desc.RunRef) + " ===\"\n")
	b.WriteString("  if [[ -n \"${SLURM_JOB_ID:-}\" ]]; then\n")
	b.WriteString("    echo \"executor:  " + effective.Mode + "\"\n")
	b.WriteString("    echo \"slurm_job: ${SLURM_JOB_ID}\"\n")
	b.WriteString("    echo \"node:      $(hostname)\"\n")
	b.WriteString("    echo \"cpus:      ${SLURM_CPUS_PER_TASK:-N/A}\"\n")
	b.WriteString("    echo \"mem_mb:    ${SLURM_MEM_PER_NODE:-N/A}\"\n")
	b.WriteString("  else\n")
	b.WriteString("    echo \"executor:  " + effective.Mode + "\"\n")
	b.WriteString("    echo \"host:      $(hostname)\"\n")
	b.WriteString("  fi\n")
	b.WriteString("  echo \"started:   $(date -u +%Y-%m-%dT%H:%M:%SZ)\"\n")
	b.WriteString("  echo \"===\"\n")
	b.WriteString("} >> \"$WORKDIR/logs/run_out.log\"\n\n")
	for _, env := range runtimeEnv(desc) {
		b.WriteString("export " + env[0] + "=" + shellQuote(env[1]) + "\n")
	}
	b.WriteString("\nARGS=(")
	for _, arg := range desc.Args {
		b.WriteString(" " + shellQuote(arg))
	}
	b.WriteString(" )\n")
	b.WriteString("set +e\n")
	if effective.Mode == SlurmDocker {
		containerName := "veriproc-" + sanitizeName(desc.RunRef)
		b.WriteString("DOCKER_CONTAINER_NAME=" + shellQuote(containerName) + "\n")
		b.WriteString("docker run --rm \\\n")
		b.WriteString("  --name \"$DOCKER_CONTAINER_NAME\" \\\n")
		for _, mount := range effective.Container.Mounts {
			b.WriteString("  -v " + shellQuote(mount) + " \\\n")
		}
		user := effective.Container.User
		if user == "host" {
			b.WriteString("  -u \"$(id -u):$(id -g)\" \\\n")
		} else if user != "" {
			b.WriteString("  -u " + shellQuote(user) + " \\\n")
		}
		for _, env := range runtimeEnv(desc) {
			b.WriteString("  -e " + shellQuote(env[0]+"="+env[1]) + " \\\n")
		}
		b.WriteString("  " + shellQuote(effective.Container.Image) + " \\\n")
		b.WriteString("  " + shellQuote(desc.Executable) + " \"${ARGS[@]}\" >> \"$WORKDIR/logs/run_out.log\" 2>> \"$WORKDIR/logs/run_err.log\" &\n")
	} else {
		b.WriteString(shellQuote(desc.Executable) + " \"${ARGS[@]}\" >> \"$WORKDIR/logs/run_out.log\" 2>> \"$WORKDIR/logs/run_err.log\" &\n")
	}
	b.WriteString("PID=$!\n")
	b.WriteString("wait $PID\n")
	b.WriteString("RETCODE=$?\n")
	b.WriteString("exit $RETCODE\n")
	return b.String()
}

func runtimeEnv(desc JobDescription) [][2]string {
	env := [][2]string{
		{"VERIPROC_RUN_DIR", filepath.Join(desc.WorkingRoot, "output")},
		{"VERIPROC_TASK_ID", desc.TaskID},
		{"VERIPROC_RETRY_INDEX", strconv.Itoa(desc.RetryIndex)},
		{"VERIPROC_RUN_REF", desc.RunRef},
		{"VERIPROC_STATION_ID", desc.StationID},
		{"VERIPROC_WORKING_ROOT", desc.WorkingRoot},
		{"VERIPROC_WINDOW_START", desc.WindowStart.UTC().Format("20060102T150405")},
		{"VERIPROC_WINDOW_END", desc.WindowEnd.UTC().Format("20060102T150405")},
	}
	if desc.SplitGroupID != "" {
		env = append(env, [2]string{"VERIPROC_SPLIT_GROUP_ID", desc.SplitGroupID})
	}
	if desc.JobOrderPath != "" {
		env = append(env, [2]string{"VERIPROC_JOBORDER_PATH", desc.JobOrderPath})
	}
	return env
}

func (e *SlurmExecutor) runCommand(ctx context.Context, name string, args ...string) (string, string, error) {
	if e.cfg.Connection.Mode != "ssh" {
		return e.runner.Run(ctx, name, args...)
	}
	sshArgs := []string{}
	if e.cfg.Connection.KeyFile != "" {
		sshArgs = append(sshArgs, "-i", e.cfg.Connection.KeyFile)
	}
	sshArgs = append(sshArgs, e.cfg.Connection.User+"@"+e.cfg.Connection.Host, name)
	sshArgs = append(sshArgs, args...)
	return e.runner.Run(ctx, "ssh", sshArgs...)
}

// isSshTransportError reports whether err represents a permanent SSH
// transport-level failure (exit code 255). SSH uses exit code 255 exclusively
// for its own errors, distinct from exit codes of remote commands.
//
// Transient conditions such as DNS resolution failures ("Try again") are
// excluded so the dispatcher can retry the run after the transient clears.
func isSshTransportError(err error, stderr string) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 255 {
		return false
	}
	// "Try again" is the EAI_AGAIN suffix appended by getaddrinfo when DNS
	// lookup returns a temporary failure. Treat as transient, not fatal.
	if strings.Contains(stderr, "Try again") {
		return false
	}
	return true
}

func parseSbatchID(stdout string) string {
	line := strings.TrimSpace(stdout)
	if line == "" {
		return ""
	}
	line = strings.Split(line, "\n")[0]
	line = strings.Split(line, ";")[0]
	return strings.TrimSpace(line)
}

func parseSacctObservation(stdout string, observedAt time.Time) Observation {
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "|")
		state := strings.TrimSpace(fields[0])
		exitField := ""
		if len(fields) > 1 {
			exitField = strings.TrimSpace(fields[1])
		}
		nodeList := ""
		if len(fields) > 3 {
			nodeList = strings.TrimSpace(fields[3])
			if nodeList == "None assigned" {
				nodeList = ""
			}
		}
		status, exitCode, failure := mapSlurmState(state, exitField)
		return Observation{Status: status, ObservedAt: observedAt, ExitCode: exitCode, FailureMsg: failure, NativeState: line, Node: nodeList}
	}
	return Observation{Status: StatusUnknown, ObservedAt: observedAt, NativeState: "UNKNOWN"}
}

func mapSlurmState(state, exitField string) (Status, int, string) {
	upper := strings.ToUpper(strings.TrimSpace(state))
	exitCode := parseSlurmExitCode(exitField)
	switch upper {
	case "PENDING", "CONFIGURING", "REQUEUED", "RESIZING", "SUSPENDED":
		return StatusQueued, exitCode, ""
	case "RUNNING", "COMPLETING":
		return StatusRunning, exitCode, ""
	case "COMPLETED":
		if exitCode == 0 {
			return StatusSucceeded, exitCode, ""
		}
		return StatusFailed, exitCode, fmt.Sprintf("slurm completed with exit code %d", exitCode)
	case "CANCELLED", "CANCELLED+":
		return StatusCancelled, exitCode, "slurm job cancelled"
	case "FAILED", "TIMEOUT", "NODE_FAIL", "OUT_OF_MEMORY", "BOOT_FAIL", "DEADLINE", "PREEMPTED":
		return StatusFailed, exitCode, "slurm job " + strings.ToLower(upper)
	default:
		return StatusUnknown, exitCode, ""
	}
}

func parseSlurmExitCode(exitField string) int {
	if exitField == "" {
		return 0
	}
	parts := strings.Split(exitField, ":")
	n, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0
	}
	return n
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func safeJobName(value string) string {
	if value == "" {
		return "veriproc"
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
		if b.Len() >= 120 {
			break
		}
	}
	if b.Len() == 0 {
		return "veriproc"
	}
	return b.String()
}

func sanitizeName(value string) string {
	return strings.ToLower(safeJobName(value))
}

func normalizeSlurmWalltime(value string) string {
	if strings.Contains(value, ":") {
		return value
	}
	d, err := parseSlurmDuration(value)
	if err != nil {
		return value
	}
	totalSeconds := int64(d / time.Second)
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
}

func parseSlurmDuration(value string) (time.Duration, error) {
	if d, err := time.ParseDuration(value); err == nil {
		return d, nil
	}
	if len(value) < 3 || value[0] != 'P' || value[1] != 'T' {
		return 0, fmt.Errorf("invalid duration")
	}
	var total time.Duration
	start := 2
	for i := 2; i < len(value); i++ {
		switch value[i] {
		case 'H', 'M', 'S':
			n, err := strconv.Atoi(value[start:i])
			if err != nil {
				return 0, err
			}
			switch value[i] {
			case 'H':
				total += time.Duration(n) * time.Hour
			case 'M':
				total += time.Duration(n) * time.Minute
			case 'S':
				total += time.Duration(n) * time.Second
			}
			start = i + 1
		}
	}
	if start != len(value) || total == 0 {
		return 0, fmt.Errorf("invalid duration")
	}
	return total, nil
}
