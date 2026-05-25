package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestConfig_Defaults — defaults are usable and validate cleanly.
func TestConfig_Defaults(t *testing.T) {
	cfg, err := Load(nil, nil)
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	if cfg.InstanceID == "" {
		t.Errorf("expected non-empty default instance_id")
	}
	if cfg.HTTP.BindAddr != "127.0.0.1:8080" {
		t.Errorf("default bind_addr: got %q", cfg.HTTP.BindAddr)
	}
	if cfg.Log.Level != "info" || cfg.Log.Format != "json" {
		t.Errorf("default log: got level=%q format=%q", cfg.Log.Level, cfg.Log.Format)
	}
}

// TestConfig_Precedence — flag > env > file > defaults.
func TestConfig_Precedence(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "veriproc.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
instance_id: from-file
http:
  bind_addr: 127.0.0.1:9001
log:
  level: debug
  format: console
`), 0o644); err != nil {
		t.Fatalf("write cfg: %v", err)
	}

	// File-only.
	cfg, err := Load([]string{"--config", cfgPath}, nil)
	if err != nil {
		t.Fatalf("file load: %v", err)
	}
	if cfg.InstanceID != "from-file" || cfg.HTTP.BindAddr != "127.0.0.1:9001" {
		t.Errorf("file precedence broken: %+v", cfg)
	}

	// Env overrides file.
	env := map[string]string{
		"VERIPROC_INSTANCE_ID": "from-env",
		"VERIPROC_HTTP_ADDR":   "127.0.0.1:9002",
	}
	cfg, err = Load([]string{"--config", cfgPath}, env)
	if err != nil {
		t.Fatalf("env load: %v", err)
	}
	if cfg.InstanceID != "from-env" || cfg.HTTP.BindAddr != "127.0.0.1:9002" {
		t.Errorf("env precedence broken: %+v", cfg)
	}

	// Flag overrides env.
	cfg, err = Load(
		[]string{"--config", cfgPath, "--instance-id", "from-flag", "--http-addr", "127.0.0.1:9003"},
		env,
	)
	if err != nil {
		t.Fatalf("flag load: %v", err)
	}
	if cfg.InstanceID != "from-flag" || cfg.HTTP.BindAddr != "127.0.0.1:9003" {
		t.Errorf("flag precedence broken: %+v", cfg)
	}
}

// TestConfig_EnvDuration — duration env vars parse correctly.
func TestConfig_EnvDuration(t *testing.T) {
	cfg, err := Load(nil, map[string]string{
		"VERIPROC_HTTP_SHUTDOWN_TIMEOUT": "3s",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.HTTP.ShutdownTimeout != 3*time.Second {
		t.Errorf("shutdown timeout = %v, want 3s", cfg.HTTP.ShutdownTimeout)
	}
}

// TestConfig_InvalidBindAddr — validation rejects malformed bind address.
func TestConfig_InvalidBindAddr(t *testing.T) {
	_, err := Load([]string{"--http-addr", "not-a-host-port"}, nil)
	if err == nil {
		t.Fatalf("expected validation error for bad bind_addr")
	}
}

// TestConfig_InvalidLogLevel — validation rejects unknown log level.
func TestConfig_InvalidLogLevel(t *testing.T) {
	_, err := Load([]string{"--log-level", "bogus"}, nil)
	if err == nil {
		t.Fatalf("expected validation error for bad log level")
	}
}

// TestConfig_EmptyInstanceID — validation rejects blank instance_id.
func TestConfig_EmptyInstanceID(t *testing.T) {
	_, err := Load([]string{"--instance-id", "   "}, nil)
	if err == nil {
		t.Fatalf("expected validation error for blank instance_id")
	}
}

// TestConfig_MissingFile — explicit --config to a missing path is an error.
func TestConfig_MissingFile(t *testing.T) {
	_, err := Load([]string{"--config", "/nonexistent/veriproc.yaml"}, nil)
	if err == nil {
		t.Fatalf("expected error for missing config file")
	}
}

// TestConfig_TaskIDTimestamp_Default — omitted key defaults to "creation".
func TestConfig_TaskIDTimestamp_Default(t *testing.T) {
	cfg, err := Load(nil, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if string(cfg.Naming.TaskIDTimestamp) != "creation" {
		t.Errorf("default TaskIDTimestamp = %q, want %q", cfg.Naming.TaskIDTimestamp, "creation")
	}
}

// TestConfig_TaskIDTimestamp_ExplicitCreation — explicit "creation" validates.
func TestConfig_TaskIDTimestamp_ExplicitCreation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(p, []byte("naming:\n  task_id_timestamp: creation\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load([]string{"--config", p}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if string(cfg.Naming.TaskIDTimestamp) != "creation" {
		t.Errorf("TaskIDTimestamp = %q, want creation", cfg.Naming.TaskIDTimestamp)
	}
}

// TestConfig_TaskIDTimestamp_ExplicitStart — explicit "start" validates.
func TestConfig_TaskIDTimestamp_ExplicitStart(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(p, []byte("naming:\n  task_id_timestamp: start\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load([]string{"--config", p}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if string(cfg.Naming.TaskIDTimestamp) != "start" {
		t.Errorf("TaskIDTimestamp = %q, want start", cfg.Naming.TaskIDTimestamp)
	}
}

// TestConfig_TaskIDTimestamp_Invalid — unknown value is rejected.
func TestConfig_TaskIDTimestamp_Invalid(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(p, []byte("naming:\n  task_id_timestamp: bogus\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load([]string{"--config", p}, nil)
	if err == nil {
		t.Fatal("expected validation error for invalid task_id_timestamp")
	}
}

// TestConfig_Definitions_RejectsReservedKeys — reserved context names must not
// appear as top-level definitions keys.
func TestConfig_Definitions_RejectsReservedKeys(t *testing.T) {
	reserved := []string{
		"station_id", "station_name", "task_id", "retry_index",
		"run_ref", "job_id", "start", "end", "working_root", "joborder",
	}
	for _, key := range reserved {
		dir := t.TempDir()
		p := filepath.Join(dir, "cfg.yaml")
		content := "definitions:\n  " + key + ": forbidden\n"
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		_, err := Load([]string{"--config", p}, nil)
		if err == nil {
			t.Errorf("expected validation error for reserved definitions key %q", key)
		}
	}
}

// TestConfig_Definitions_AcceptsUserKeys — arbitrary user-defined keys are
// allowed.
func TestConfig_Definitions_AcceptsUserKeys(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	content := "definitions:\n  facility:\n    center: SAF\n  generic_mode: NOMINAL\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load([]string{"--config", p}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Definitions["generic_mode"] != "NOMINAL" {
		t.Errorf("definitions[generic_mode] = %v, want NOMINAL", cfg.Definitions["generic_mode"])
	}
	fac, ok := cfg.Definitions["facility"].(map[string]any)
	if !ok || fac["center"] != "SAF" {
		t.Errorf("definitions[facility] = %v, want map with center=SAF", cfg.Definitions["facility"])
	}
}

func TestConfig_SlurmExecutorConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	content := `executor:
  type: slurm-docker
  slurm:
    connection:
      mode: ssh
      host: login.example.test
      user: veriproc
      key_file: /keys/id_rsa
    account: co2m
    partition: batch
    qos: normal
    poll_interval: PT30S
    defaults:
      cpus_per_task: 8
      mem_gb: 32
      walltime: PT2H
    shared_roots:
      - /shared/veriproc
  docker:
    default_mounts:
      - /shared/veriproc:/work
    user: host
`
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load([]string{"--config", p}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Executor.Type != "slurm-docker" {
		t.Fatalf("executor.type = %q", cfg.Executor.Type)
	}
	if cfg.Executor.Slurm.Connection.Mode != "ssh" || cfg.Executor.Slurm.Connection.Host != "login.example.test" {
		t.Fatalf("connection = %+v", cfg.Executor.Slurm.Connection)
	}
	if cfg.Executor.Slurm.SubmitCommand != "sbatch" || cfg.Executor.Slurm.QueryCommand != "sacct" {
		t.Fatalf("commands = submit %q query %q", cfg.Executor.Slurm.SubmitCommand, cfg.Executor.Slurm.QueryCommand)
	}
	if cfg.Executor.Slurm.Defaults.CPUsPerTask != 8 || cfg.Executor.Slurm.Defaults.MemGB != 32 {
		t.Fatalf("defaults = %+v", cfg.Executor.Slurm.Defaults)
	}
	if len(cfg.Executor.Docker.DefaultMounts) != 1 || cfg.Executor.Docker.User != "host" {
		t.Fatalf("docker = %+v", cfg.Executor.Docker)
	}
}

func TestConfig_SlurmSSHRequiresHostAndUser(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	content := "executor:\n  type: slurm-native\n  slurm:\n    connection:\n      mode: ssh\n      host: login.example.test\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load([]string{"--config", p}, nil); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestConfig_InvalidExecutorType(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(p, []byte("executor:\n  type: grid-engine\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load([]string{"--config", p}, nil); err == nil {
		t.Fatal("expected validation error")
	}
}
