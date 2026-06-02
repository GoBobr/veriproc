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
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/eum/veriproc/internal/policy"
)

// Config is the resolved backend configuration.
type Config struct {
	// InstanceID is the deployment-level instance identifier (Spec 2.3.1).
	InstanceID string `yaml:"instance_id"`

	// InstanceRoot is the absolute directory of the loaded config file.
	// Injected at runtime; not read from YAML. Available as the reserved
	// context key <instance_root> in station configurations.
	InstanceRoot string `yaml:"-"`

	SchemaVersion string           `yaml:"schema_version"`
	HTTP          HTTPConfig       `yaml:"http"`
	Log           LogConfig        `yaml:"log"`
	DB            DBConfig         `yaml:"db"`
	Storage       PathsConfig      `yaml:"storage"`
	Naming        policy.Naming    `yaml:"naming"`
	Integrity     policy.Integrity `yaml:"integrity"`
	// Definitions is an arbitrary nested YAML mapping injected at the root of
	// the station context-reference namespace. Top-level keys must not collide
	// with reserved runtime context names (Spec §2.3.1).
	Definitions       map[string]any            `yaml:"definitions"`
	ExecutionEnv      map[string]string         `yaml:"execution_env"`
	Facility          map[string]string         `yaml:"facility"`
	RollingArchives   map[string]RollingArchive `yaml:"rolling_archives"`
	ProductCategories []ProductCategory         `yaml:"product_categories"`
	Executor          ExecutorConfig            `yaml:"executor"`
	Generators        map[string]Generator      `yaml:"generators"`
}

// RollingArchive describes a configured archive root (Spec §2.3.1).
type RollingArchive struct {
	Path            string `yaml:"path"`
	RetentionPolicy string `yaml:"retention"`
	Mode            string `yaml:"mode"`
}

// ProductCategory defines the ordered folder search list for an input category.
type ProductCategory struct {
	Name    string   `yaml:"name"`
	Folders []string `yaml:"folders"`
}

// ExecutorConfig configures the executor backend(s) available at this instance.
//
// Multi-executor format (preferred): populate Executors with one entry per
// executor type ("local", "stub", "slurm-native", "slurm-docker") and set
// Default to the type used when a station declares no execution.mode.
//
//	executor:
//	  default: slurm-native
//	  executors:
//	    local: {}
//	    slurm-native:
//	      connection: {mode: local}
//	      partition: batch
//
// Legacy single-executor format: set Type (and Slurm/Docker as needed).
// The daemon treats this as Executors = {Type: {Slurm: ..., Docker: ...}}
// with Default = Type for backward compatibility.
type ExecutorConfig struct {
	Default   string                  `yaml:"default"`
	Executors map[string]ExecutorSpec `yaml:"executors"`
	// Legacy single-executor fields (used when Executors is empty).
	Type   string               `yaml:"type"`
	Slurm  SlurmConfig          `yaml:"slurm"`
	Docker DockerExecutorConfig `yaml:"docker"`
}

// ExecutorSpec is the per-executor configuration entry in the executor registry.
// For "local" and "stub" types no further fields are needed; for SLURM types
// provide Slurm (and Docker for slurm-docker).
type ExecutorSpec struct {
	Slurm  SlurmConfig          `yaml:"slurm"`
	Docker DockerExecutorConfig `yaml:"docker"`
}

// SlurmConfig holds deployment-level defaults for SLURM executors.
type SlurmConfig struct {
	Connection    SlurmConnection `yaml:"connection"`
	Account       string          `yaml:"account"`
	Partition     string          `yaml:"partition"`
	QOS           string          `yaml:"qos"`
	SubmitCommand string          `yaml:"submit_command"`
	QueryCommand  string          `yaml:"query_command"`
	CancelCommand string          `yaml:"cancel_command"`
	PollInterval  string          `yaml:"poll_interval"`
	Defaults      SlurmResources  `yaml:"defaults"`
}

// SlurmConnection describes where scheduler commands are executed.
type SlurmConnection struct {
	Mode    string `yaml:"mode"`
	Host    string `yaml:"host"`
	User    string `yaml:"user"`
	KeyFile string `yaml:"key_file"`
}

// SlurmResources describes default scheduler resource requirements.
type SlurmResources struct {
	CPUsPerTask int    `yaml:"cpus_per_task"`
	MemGB       int    `yaml:"mem_gb"`
	Walltime    string `yaml:"walltime"`
}

// DockerExecutorConfig holds instance defaults for SLURM Docker mode.
type DockerExecutorConfig struct {
	DefaultMounts []string `yaml:"default_mounts"`
	User          string   `yaml:"user"`
}

// Generator records an instance-level generator identity for audit/job orders.
type Generator struct {
	Type    string `yaml:"type"`
	Version string `yaml:"version"`
	Paths   string `yaml:"paths"` // "relative" (default) or "absolute"
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
	// StationOrder lists station IDs in the desired display order. When set it
	// overrides the default directory-alphabetical order. Station IDs not
	// listed here are appended after the listed ones, sorted alphabetically.
	StationOrder []string `yaml:"station_order"`
}

// Defaults returns a Config populated with built-in defaults.
func Defaults() Config {
	return Config{
		InstanceID:    "veriproc-local",
		SchemaVersion: "veriproc.instance/v1",
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
		Naming:    policy.DefaultNaming(),
		Integrity: policy.DefaultIntegrity(),
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
		if abs, err := filepath.Abs(configPath); err == nil {
			cfg.InstanceRoot = filepath.Dir(abs)
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
		cfg.Storage.WorkingRootBase = v
	}
	if v, ok := env["VERIPROC_STATION_CONFIG_ROOT"]; ok && v != "" {
		cfg.Storage.StationConfigRoot = v
	}
	if v, ok := env["VERIPROC_EXECUTOR"]; ok && v != "" {
		cfg.Executor.Type = v
	}
}

// reservedContextNames lists top-level definition keys that are reserved
// for runtime injection and must not be set in instance definitions (Spec §2.3.1).
var reservedContextNames = map[string]bool{
	"station_id":    true,
	"station_name":  true,
	"task_id":       true,
	"retry_index":   true,
	"run_ref":       true,
	"job_id":        true,
	"start":         true,
	"end":           true,
	"working_root":  true,
	"joborder":      true,
	"instance_root": true,
}

// Validate enforces invariants on the resolved configuration.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.InstanceID) == "" {
		return errors.New("config: instance_id must not be empty")
	}
	for k := range c.Definitions {
		if reservedContextNames[k] {
			return fmt.Errorf("config: definitions key %q collides with reserved context name", k)
		}
	}
	for k := range c.ExecutionEnv {
		if err := validateExecutionEnvName(k); err != nil {
			return err
		}
	}
	c.Naming = c.Naming.WithDefaults()
	c.Integrity = c.Integrity.WithDefaults()
	switch c.Naming.TaskIDTimestamp {
	case policy.TaskIDTimestampCreation, policy.TaskIDTimestampStart:
		// valid
	default:
		return fmt.Errorf("config: naming.task_id_timestamp %q invalid; supported values are %q and %q",
			c.Naming.TaskIDTimestamp, policy.TaskIDTimestampCreation, policy.TaskIDTimestampStart)
	}
	if err := policy.ValidatePathTemplate(c.Naming.WorkingRoot.PathTemplate); err != nil {
		return fmt.Errorf("config: naming.working_root.%w", err)
	}
	switch c.Integrity.ChecksumPolicy {
	case policy.ChecksumAvailableOnly, policy.ChecksumNone, policy.ChecksumRequired:
	default:
		return fmt.Errorf("config: integrity.checksum_policy %q invalid", c.Integrity.ChecksumPolicy)
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
	if err := c.validateExecutor(); err != nil {
		return err
	}
	return nil
}

func validateExecutionEnvName(name string) error {
	if name == "" {
		return errors.New("config: execution_env contains an empty variable name")
	}
	if strings.HasPrefix(name, "VERIPROC_") {
		return fmt.Errorf("config: execution_env variable %q uses reserved VERIPROC_ prefix", name)
	}
	for i, r := range name {
		if r == '_' || ('A' <= r && r <= 'Z') || ('a' <= r && r <= 'z') || (i > 0 && '0' <= r && r <= '9') {
			continue
		}
		return fmt.Errorf("config: execution_env variable %q is invalid; use shell variable names like GEN_VERSION", name)
	}
	if '0' <= name[0] && name[0] <= '9' {
		return fmt.Errorf("config: execution_env variable %q is invalid; variable names must not start with a digit", name)
	}
	return nil
}

func (c *Config) validateExecutor() error {
	// Multi-executor format: executor.executors is populated.
	if len(c.Executor.Executors) > 0 {
		c.Executor.Default = strings.TrimSpace(strings.ToLower(c.Executor.Default))
		if c.Executor.Default != "" {
			if _, ok := c.Executor.Executors[c.Executor.Default]; !ok {
				return fmt.Errorf("config: executor.default %q is not present in executor.executors", c.Executor.Default)
			}
		}
		for typeName, spec := range c.Executor.Executors {
			typeName = strings.TrimSpace(strings.ToLower(typeName))
			switch typeName {
			case "stub", "local":
				// no additional config needed
			case "slurm-native", "slurm-docker":
				specCopy := spec
				if err := c.validateSlurmSpec(typeName, &specCopy.Slurm, &specCopy.Docker); err != nil {
					return err
				}
				c.Executor.Executors[typeName] = specCopy
			default:
				return fmt.Errorf("config: executor.executors key %q is not a recognised executor type", typeName)
			}
		}
		return nil
	}

	// Legacy single-executor format.
	c.Executor.Type = strings.TrimSpace(strings.ToLower(c.Executor.Type))
	switch c.Executor.Type {
	case "", "stub", "local":
		return nil
	case "slurm-native", "slurm-docker":
		// continue
	default:
		return fmt.Errorf("config: executor.type %q invalid", c.Executor.Type)
	}
	return c.validateSlurmSpec("executor", &c.Executor.Slurm, &c.Executor.Docker)
}

func (c *Config) validateSlurmSpec(prefix string, s *SlurmConfig, d *DockerExecutorConfig) error {
	s.Connection.Mode = strings.TrimSpace(strings.ToLower(s.Connection.Mode))
	if s.Connection.Mode == "" {
		s.Connection.Mode = "local"
	}
	s.Connection.Host = strings.TrimSpace(s.Connection.Host)
	s.Connection.User = strings.TrimSpace(s.Connection.User)
	s.Connection.KeyFile = strings.TrimSpace(os.ExpandEnv(s.Connection.KeyFile))
	s.Account = strings.TrimSpace(s.Account)
	s.Partition = strings.TrimSpace(s.Partition)
	s.QOS = strings.TrimSpace(s.QOS)
	s.SubmitCommand = strings.TrimSpace(s.SubmitCommand)
	if s.SubmitCommand == "" {
		s.SubmitCommand = "sbatch"
	}
	s.QueryCommand = strings.TrimSpace(s.QueryCommand)
	if s.QueryCommand == "" {
		s.QueryCommand = "sacct"
	}
	s.CancelCommand = strings.TrimSpace(s.CancelCommand)
	s.PollInterval = strings.TrimSpace(s.PollInterval)
	if s.PollInterval != "" {
		if _, err := parseDurationLike(s.PollInterval); err != nil {
			return fmt.Errorf("config: %s.slurm.poll_interval %q invalid: %w", prefix, s.PollInterval, err)
		}
	}
	if err := validateCommandName(prefix+".slurm.submit_command", s.SubmitCommand); err != nil {
		return err
	}
	if err := validateCommandName(prefix+".slurm.query_command", s.QueryCommand); err != nil {
		return err
	}
	if s.CancelCommand != "" {
		if err := validateCommandName(prefix+".slurm.cancel_command", s.CancelCommand); err != nil {
			return err
		}
	}
	switch s.Connection.Mode {
	case "local":
	case "ssh":
		if s.Connection.Host == "" || s.Connection.User == "" {
			return fmt.Errorf("config: %s.slurm.connection host and user are required for ssh mode", prefix)
		}
	default:
		return fmt.Errorf("config: %s.slurm.connection.mode %q invalid", prefix, s.Connection.Mode)
	}
	if s.Defaults.CPUsPerTask < 0 || s.Defaults.MemGB < 0 {
		return fmt.Errorf("config: %s.slurm.defaults resources must be non-negative", prefix)
	}
	s.Defaults.Walltime = strings.TrimSpace(s.Defaults.Walltime)
	if s.Defaults.Walltime != "" {
		if _, err := slurmWalltime(s.Defaults.Walltime); err != nil {
			return fmt.Errorf("config: %s.slurm.defaults.walltime %q invalid: %w", prefix, s.Defaults.Walltime, err)
		}
	}
	if d != nil {
		for _, mount := range d.DefaultMounts {
			if strings.TrimSpace(mount) == "" {
				return fmt.Errorf("config: %s.docker.default_mounts must not contain empty entries", prefix)
			}
		}
	}
	return nil
}

func validateCommandName(field, value string) error {
	if value == "" || strings.ContainsAny(value, " \t\n\r") {
		return fmt.Errorf("config: %s %q must be a single command path or name", field, value)
	}
	return nil
}

func parseDurationLike(value string) (time.Duration, error) {
	if d, err := time.ParseDuration(value); err == nil {
		return d, nil
	}
	return parseISODuration(value)
}

func parseISODuration(value string) (time.Duration, error) {
	if len(value) < 3 || value[0] != 'P' || value[1] != 'T' {
		return 0, fmt.Errorf("expected Go duration or ISO-8601 PT duration")
	}
	var total time.Duration
	start := 2
	for i := 2; i < len(value); i++ {
		switch value[i] {
		case 'H', 'M', 'S':
			if start == i {
				return 0, fmt.Errorf("missing number before %c", value[i])
			}
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
		return 0, fmt.Errorf("expected Go duration or ISO-8601 PT duration")
	}
	return total, nil
}

func slurmWalltime(value string) (string, error) {
	if strings.Contains(value, ":") {
		return value, nil
	}
	d, err := parseDurationLike(value)
	if err != nil {
		return "", err
	}
	totalSeconds := int64(d / time.Second)
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds), nil
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
