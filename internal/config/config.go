// Package config loads VeriProc backend configuration.
//
// Precedence (lowest to highest):
//  1. Built-in defaults
//  2. YAML configuration file (path via --config flag or VERIPROC_CONFIG env)
//  3. Environment variables (VERIPROC_*)
//  4. Command-line flags
package config

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the resolved backend configuration.
type Config struct {
	// InstanceID is the deployment-level instance identifier (Spec 2.3.1).
	InstanceID string `yaml:"instance_id"`

	HTTP  HTTPConfig  `yaml:"http"`
	Log   LogConfig   `yaml:"log"`
	DB    DBConfig    `yaml:"db"`
	Paths PathsConfig `yaml:"paths"`
}

// HTTPConfig configures the REST API server.
type HTTPConfig struct {
	BindAddr        string        `yaml:"bind_addr"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// LogConfig configures structured logging.
type LogConfig struct {
	// Level: trace|debug|info|warn|error
	Level string `yaml:"level"`
	// Format: json|console
	Format string `yaml:"format"`
}

// DBConfig configures the state-store connection (used from Milestone 1 onward).
type DBConfig struct {
	DSN string `yaml:"dsn"`
}

// PathsConfig configures filesystem roots referenced by later milestones.
type PathsConfig struct {
	WorkingRootBase   string `yaml:"working_root_base"`
	StationConfigRoot string `yaml:"station_config_root"`
}

// Defaults returns a Config populated with built-in defaults.
func Defaults() Config {
	return Config{
		InstanceID: "veriproc-local",
		HTTP: HTTPConfig{
			BindAddr:        "127.0.0.1:8080",
			ReadTimeout:     15 * time.Second,
			WriteTimeout:    30 * time.Second,
			ShutdownTimeout: 15 * time.Second,
		},
		Log: LogConfig{
			Level:  "info",
			Format: "json",
		},
	}
}

// Load resolves configuration from the supplied args and environment map.
//
// args must NOT include the program name (i.e. pass os.Args[1:]).
// env should typically be a snapshot of process environment; callers may
// supply a curated map for tests.
func Load(args []string, env map[string]string) (*Config, error) {
	cfg := Defaults()

	// Step 1: locate config file path (flag or env, flag wins).
	fs := flag.NewFlagSet("veriprocd", flag.ContinueOnError)
	fs.SetOutput(emptyWriter{})

	var (
		configPath string
		bindAddr   string
		logLevel   string
		logFormat  string
		instanceID string
		dbDSN      string
	)
	fs.StringVar(&configPath, "config", env["VERIPROC_CONFIG"], "path to YAML config file")
	fs.StringVar(&bindAddr, "http-addr", "", "HTTP bind address (host:port)")
	fs.StringVar(&logLevel, "log-level", "", "log level (trace|debug|info|warn|error)")
	fs.StringVar(&logFormat, "log-format", "", "log format (json|console)")
	fs.StringVar(&instanceID, "instance-id", "", "deployment instance identifier")
	fs.StringVar(&dbDSN, "db-dsn", "", "state-store DSN")

	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("config: parse flags: %w", err)
	}

	// Step 2: file overlay.
	if configPath != "" {
		if err := applyFile(&cfg, configPath); err != nil {
			return nil, err
		}
	}

	// Step 3: env overlay.
	applyEnv(&cfg, env)

	// Step 4: flag overlay (only if non-empty).
	if bindAddr != "" {
		cfg.HTTP.BindAddr = bindAddr
	}
	if logLevel != "" {
		cfg.Log.Level = logLevel
	}
	if logFormat != "" {
		cfg.Log.Format = logFormat
	}
	if instanceID != "" {
		cfg.InstanceID = instanceID
	}
	if dbDSN != "" {
		cfg.DB.DSN = dbDSN
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config: read %q: %w", path, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("config: parse %q: %w", path, err)
	}
	return nil
}

func applyEnv(cfg *Config, env map[string]string) {
	if v, ok := env["VERIPROC_INSTANCE_ID"]; ok && v != "" {
		cfg.InstanceID = v
	}
	if v, ok := env["VERIPROC_HTTP_ADDR"]; ok && v != "" {
		cfg.HTTP.BindAddr = v
	}
	if v, ok := env["VERIPROC_LOG_LEVEL"]; ok && v != "" {
		cfg.Log.Level = v
	}
	if v, ok := env["VERIPROC_LOG_FORMAT"]; ok && v != "" {
		cfg.Log.Format = v
	}
	if v, ok := env["VERIPROC_DB_DSN"]; ok && v != "" {
		cfg.DB.DSN = v
	}
	if v, ok := env["VERIPROC_DSN"]; ok && v != "" {
		cfg.DB.DSN = v
	}
	if v, ok := env["VERIPROC_HTTP_READ_TIMEOUT"]; ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.HTTP.ReadTimeout = d
		}
	}
	if v, ok := env["VERIPROC_HTTP_WRITE_TIMEOUT"]; ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.HTTP.WriteTimeout = d
		}
	}
	if v, ok := env["VERIPROC_HTTP_SHUTDOWN_TIMEOUT"]; ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.HTTP.ShutdownTimeout = d
		}
	}
	if v, ok := env["VERIPROC_WORKING_ROOT_BASE"]; ok && v != "" {
		cfg.Paths.WorkingRootBase = v
	}
	if v, ok := env["VERIPROC_STATION_CONFIG_ROOT"]; ok && v != "" {
		cfg.Paths.StationConfigRoot = v
	}
	if v, ok := env["VERIPROC_STATION_DIR"]; ok && v != "" {
		cfg.Paths.StationConfigRoot = v
	}
}

// Validate enforces invariants on the resolved configuration.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.InstanceID) == "" {
		return errors.New("config: instance_id must not be empty")
	}
	if _, _, err := net.SplitHostPort(c.HTTP.BindAddr); err != nil {
		return fmt.Errorf("config: http.bind_addr %q invalid: %w", c.HTTP.BindAddr, err)
	}
	if c.HTTP.ReadTimeout < 0 || c.HTTP.WriteTimeout < 0 || c.HTTP.ShutdownTimeout < 0 {
		return errors.New("config: http timeouts must be non-negative")
	}
	switch strings.ToLower(c.Log.Level) {
	case "trace", "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: log.level %q invalid", c.Log.Level)
	}
	switch strings.ToLower(c.Log.Format) {
	case "json", "console":
	default:
		return fmt.Errorf("config: log.format %q invalid", c.Log.Format)
	}
	return nil
}

// EnvSnapshot returns a snapshot of process environment variables prefixed
// with VERIPROC_ (and VERIPROC_CONFIG), suitable for passing to Load.
func EnvSnapshot() map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		k := kv[:i]
		if !strings.HasPrefix(k, "VERIPROC_") {
			continue
		}
		out[k] = kv[i+1:]
	}
	return out
}

// emptyWriter discards everything; used to silence flag.FlagSet usage prints.
type emptyWriter struct{}

func (emptyWriter) Write(p []byte) (int, error) { return len(p), nil }

// MustAtoi is a small helper; kept here in case future env values are integers.
//
//nolint:unused // reserved for later milestones
func mustAtoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
