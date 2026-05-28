// Package console implements the VeriProc operator web console gateway
// described in Spec §8. The gateway aggregates state from one or more
// upstream veriprocd instances, persists console-local state in its own
// SQLite database, serves UI-oriented endpoints to a browser frontend, and
// provides direct read-only filesystem access to run working roots.
package console

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level console-gateway configuration loaded from YAML.
//
// Spec §8.6.
type Config struct {
	SchemaVersion string           `yaml:"schema_version"`
	HTTP          HTTPConfig       `yaml:"http"`
	UI            UIConfig         `yaml:"ui"`
	DB            DBConfig         `yaml:"db"`
	Auth          AuthConfig       `yaml:"auth"`
	Instances     []InstanceConfig `yaml:"instances"`
	WebappDir     string           `yaml:"webapp_dir"`
}

// HTTPConfig configures the console gateway HTTP server.
type HTTPConfig struct {
	BindAddr        string        `yaml:"bind_addr"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// UIConfig contains dashboard rendering defaults.
type UIConfig struct {
	RefreshInterval         time.Duration `yaml:"refresh_interval"`
	VisibleSlotCount        int           `yaml:"visible_slot_count"`
	CompletedVisibility     time.Duration `yaml:"completed_visibility_timeout"`
	DefaultStatsSince       time.Duration `yaml:"default_stats_since"`
	UpstreamSummaryTimeout  time.Duration `yaml:"upstream_summary_timeout"`
	PreviewMaxBytes         int64         `yaml:"preview_max_bytes"`
}

// DBConfig is the console-local persistence handle.
type DBConfig struct {
	DSN string `yaml:"dsn"`
}

// AuthConfig configures console-local users and tokens.
//
// Two source modes are supported:
//   - Static: users declared inline with a hashed token reference
//   - File: a separate tokens YAML loaded at startup
//
// Tokens may also be provided via the VERIPROC_CONSOLE_TOKENS environment
// variable using the format "subject:role:token;...".
type AuthConfig struct {
	TokensEnv  string       `yaml:"tokens_env"`
	TokensFile string       `yaml:"tokens_file"`
	Tokens     []TokenEntry `yaml:"tokens"`
}

// TokenEntry binds a literal bearer token to a console subject and role.
//
// Plaintext tokens are accepted for local sandbox setups; production
// deployments should prefer file-backed secrets or an external manager.
type TokenEntry struct {
	Subject string `yaml:"subject"`
	Role    string `yaml:"role"`
	Token   string `yaml:"token"`
}

// InstanceConfig declares a single upstream veriprocd instance the console
// gateway should poll and act against.
type InstanceConfig struct {
	ID                string   `yaml:"id"`
	Title             string   `yaml:"title"`
	BaseURL           string   `yaml:"base_url"`
	Token             string   `yaml:"token"`
	TokenEnv          string   `yaml:"token_env"`
	TokenFile         string   `yaml:"token_file"`
	WorkingRootBase   string   `yaml:"working_root_base"`
	AllowedRoots      []string `yaml:"allowed_roots"`
	UpstreamTimeoutMS int      `yaml:"upstream_timeout_ms"`
	InsecureSkipTLS   bool     `yaml:"insecure_skip_tls"`
}

// LoadConfig reads and validates the supplied YAML configuration file.
func LoadConfig(path string) (*Config, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("console: read config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("console: parse config %s: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// applyDefaults fills in the documented default values for missing fields.
func (c *Config) applyDefaults() {
	if c.SchemaVersion == "" {
		c.SchemaVersion = "veriproc.console/v1"
	}
	if c.HTTP.BindAddr == "" {
		c.HTTP.BindAddr = "127.0.0.1:8090"
	}
	if c.HTTP.ReadTimeout == 0 {
		c.HTTP.ReadTimeout = 15 * time.Second
	}
	if c.HTTP.WriteTimeout == 0 {
		c.HTTP.WriteTimeout = 30 * time.Second
	}
	if c.HTTP.ShutdownTimeout == 0 {
		c.HTTP.ShutdownTimeout = 15 * time.Second
	}
	if c.UI.RefreshInterval == 0 {
		c.UI.RefreshInterval = 10 * time.Second
	}
	if c.UI.VisibleSlotCount == 0 {
		c.UI.VisibleSlotCount = 12
	}
	if c.UI.CompletedVisibility == 0 {
		c.UI.CompletedVisibility = 30 * time.Second
	}
	if c.UI.DefaultStatsSince == 0 {
		c.UI.DefaultStatsSince = 24 * time.Hour
	}
	if c.UI.UpstreamSummaryTimeout == 0 {
		c.UI.UpstreamSummaryTimeout = 30 * time.Second
	}
	if c.UI.PreviewMaxBytes == 0 {
		c.UI.PreviewMaxBytes = 1 << 20
	}
}

// SchemaVersionExpected is the only schema string this gateway accepts.
const SchemaVersionExpected = "veriproc.console/v1"

// Validate enforces the documented invariants required by Spec §8.6.
func (c *Config) Validate() error {
	if c.SchemaVersion != SchemaVersionExpected {
		return fmt.Errorf("console: unsupported schema_version %q (want %q)", c.SchemaVersion, SchemaVersionExpected)
	}
	if c.UI.VisibleSlotCount <= 0 {
		return errors.New("console: ui.visible_slot_count must be positive")
	}
	if c.UI.PreviewMaxBytes <= 0 {
		return errors.New("console: ui.preview_max_bytes must be positive")
	}
	// Spec §8.9.1: if the operator asks for a completed-visibility window
	// longer than the upstream summary's, the gateway must either supplement
	// (not implemented for an MVP) or reject the configuration.
	if c.UI.CompletedVisibility > c.UI.UpstreamSummaryTimeout {
		return fmt.Errorf("console: completed_visibility_timeout (%s) exceeds upstream_summary_timeout (%s); supplementing from task/run lists is not implemented", c.UI.CompletedVisibility, c.UI.UpstreamSummaryTimeout)
	}
	if len(c.Instances) == 0 {
		return errors.New("console: at least one instance must be configured")
	}
	seen := map[string]bool{}
	for i := range c.Instances {
		inst := &c.Instances[i]
		if inst.ID == "" {
			return fmt.Errorf("console: instance #%d missing id", i)
		}
		if seen[inst.ID] {
			return fmt.Errorf("console: duplicate instance id %q", inst.ID)
		}
		seen[inst.ID] = true
		if inst.BaseURL == "" {
			return fmt.Errorf("console: instance %q missing base_url", inst.ID)
		}
		if inst.Title == "" {
			inst.Title = inst.ID
		}
		inst.BaseURL = strings.TrimRight(inst.BaseURL, "/")
		if inst.UpstreamTimeoutMS <= 0 {
			inst.UpstreamTimeoutMS = 10_000
		}
		// Allowed roots default to the working root base when explicitly empty.
		if len(inst.AllowedRoots) == 0 && inst.WorkingRootBase != "" {
			inst.AllowedRoots = []string{inst.WorkingRootBase}
		}
		// Normalize roots to absolute, cleaned paths.
		for j, r := range inst.AllowedRoots {
			abs, err := filepath.Abs(r)
			if err != nil {
				return fmt.Errorf("console: instance %q allowed_roots[%d]: %w", inst.ID, j, err)
			}
			inst.AllowedRoots[j] = filepath.Clean(abs)
		}
	}
	return nil
}

// ResolveInstanceToken returns the effective bearer token for the supplied
// instance, evaluating literal, env, and file sources in that order.
func ResolveInstanceToken(inst InstanceConfig) (string, error) {
	if inst.Token != "" {
		return inst.Token, nil
	}
	if inst.TokenEnv != "" {
		if v := os.Getenv(inst.TokenEnv); v != "" {
			return v, nil
		}
	}
	if inst.TokenFile != "" {
		body, err := os.ReadFile(inst.TokenFile)
		if err != nil {
			return "", fmt.Errorf("console: read upstream token file %s: %w", inst.TokenFile, err)
		}
		return strings.TrimSpace(string(body)), nil
	}
	return "", nil
}
