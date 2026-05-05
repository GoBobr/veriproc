package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestConfig_Defaults_M0 — defaults are usable and validate cleanly.
func TestConfig_Defaults_M0(t *testing.T) {
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

// TestConfig_Precedence_M0 — flag > env > file > defaults.
func TestConfig_Precedence_M0(t *testing.T) {
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

// TestConfig_EnvDuration_M0 — duration env vars parse correctly.
func TestConfig_EnvDuration_M0(t *testing.T) {
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

// TestConfig_InvalidBindAddr_M0 — validation rejects malformed bind address.
func TestConfig_InvalidBindAddr_M0(t *testing.T) {
	_, err := Load([]string{"--http-addr", "not-a-host-port"}, nil)
	if err == nil {
		t.Fatalf("expected validation error for bad bind_addr")
	}
}

// TestConfig_InvalidLogLevel_M0 — validation rejects unknown log level.
func TestConfig_InvalidLogLevel_M0(t *testing.T) {
	_, err := Load([]string{"--log-level", "bogus"}, nil)
	if err == nil {
		t.Fatalf("expected validation error for bad log level")
	}
}

// TestConfig_EmptyInstanceID_M0 — validation rejects blank instance_id.
func TestConfig_EmptyInstanceID_M0(t *testing.T) {
	_, err := Load([]string{"--instance-id", "   "}, nil)
	if err == nil {
		t.Fatalf("expected validation error for blank instance_id")
	}
}

// TestConfig_MissingFile_M0 — explicit --config to a missing path is an error.
func TestConfig_MissingFile_M0(t *testing.T) {
	_, err := Load([]string{"--config", "/nonexistent/veriproc.yaml"}, nil)
	if err == nil {
		t.Fatalf("expected error for missing config file")
	}
}
