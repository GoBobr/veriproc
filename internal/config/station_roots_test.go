package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "veriproc.yaml")
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write cfg: %v", err)
	}
	return cfgPath
}

func rootIDs(cfg *Config) []string {
	ids := make([]string, len(cfg.Storage.StationConfigRoots))
	for i, r := range cfg.Storage.StationConfigRoots {
		ids[i] = r.Root
	}
	return ids
}

// List form with per-root include lists (Requirement: Multiple station
// config roots).
func TestConfig_StationConfigRoots_ListForm(t *testing.T) {
	cfgPath := writeCfg(t, `
instance_id: t
storage:
  working_root_base: /tmp/wr
  station_config_roots:
    - root: /tmp/rootA
      include: [gap-a, gap-b]
    - root: /tmp/rootB
`)
	cfg, err := Load([]string{"--config", cfgPath}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(rootIDs(cfg), []string{"/tmp/rootA", "/tmp/rootB"}) {
		t.Fatalf("roots = %v", rootIDs(cfg))
	}
	if !reflect.DeepEqual(cfg.Storage.StationConfigRoots[0].Include, []string{"gap-a", "gap-b"}) {
		t.Fatalf("include = %v", cfg.Storage.StationConfigRoots[0].Include)
	}
	if len(cfg.Storage.StationConfigRoots[1].Include) != 0 {
		t.Fatalf("absent include must be empty, got %v", cfg.Storage.StationConfigRoots[1].Include)
	}
}

// Scalar include (comma-separated, whitespace trimmed) — the form produced
// by <env:VAR> expansion (Requirement: Per-root include filter by station
// ID).
func TestConfig_StationConfigRoots_ScalarInclude(t *testing.T) {
	cfgPath := writeCfg(t, `
instance_id: t
storage:
  station_config_roots:
    - root: /tmp/rootA
      include: " cl2ref , cl2gap "
    - root: /tmp/rootB
      include: "   "
`)
	cfg, err := Load([]string{"--config", cfgPath}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(cfg.Storage.StationConfigRoots[0].Include, []string{"cl2ref", "cl2gap"}) {
		t.Fatalf("scalar include = %v", cfg.Storage.StationConfigRoots[0].Include)
	}
	// Whitespace-only scalar means no filter (load all), not "include nothing".
	if len(cfg.Storage.StationConfigRoots[1].Include) != 0 {
		t.Fatalf("blank include must be empty filter, got %v", cfg.Storage.StationConfigRoots[1].Include)
	}
}

// Empty root entries (e.g. env var unset) are dropped silently
// (Requirement: Missing root skipped with warning — Scenario: Empty root
// path after expansion is dropped).
func TestConfig_StationConfigRoots_EmptyRootDropped(t *testing.T) {
	cfgPath := writeCfg(t, `
instance_id: t
storage:
  station_config_roots:
    - root: ""
      include: [x]
    - root: /tmp/rootB
`)
	cfg, err := Load([]string{"--config", cfgPath}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(rootIDs(cfg), []string{"/tmp/rootB"}) {
		t.Fatalf("roots = %v, want empty entries dropped", rootIDs(cfg))
	}
}

// Legacy singular key only — unchanged behavior, one unfiltered root
// (Requirement: Multiple station config roots — Scenario: Legacy singular
// root keeps working).
func TestConfig_StationConfigRoots_LegacyOnly(t *testing.T) {
	cfgPath := writeCfg(t, `
instance_id: t
storage:
  station_config_root: /tmp/legacy
`)
	cfg, err := Load([]string{"--config", cfgPath}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(rootIDs(cfg), []string{"/tmp/legacy"}) {
		t.Fatalf("roots = %v, want [legacy] with no filter", rootIDs(cfg))
	}
	if len(cfg.Storage.StationConfigRoots[0].Include) != 0 {
		t.Fatalf("legacy root must be unfiltered")
	}
}

// Singular + list both set — singular appended as extra unfiltered root;
// identical path deduplicated with the filter cleared
// (Scenario: Singular root set alongside the list).
func TestConfig_StationConfigRoots_LegacyAndListCoexist(t *testing.T) {
	t.Run("distinct paths", func(t *testing.T) {
		cfgPath := writeCfg(t, `
instance_id: t
storage:
  station_config_root: /tmp/legacy
  station_config_roots:
    - root: /tmp/rootA
      include: [a]
`)
		cfg, err := Load([]string{"--config", cfgPath}, nil)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		want := []string{"/tmp/rootA", "/tmp/legacy"}
		if !reflect.DeepEqual(rootIDs(cfg), want) {
			t.Fatalf("roots = %v, want %v", rootIDs(cfg), want)
		}
		if len(cfg.Storage.StationConfigRoots[1].Include) != 0 {
			t.Fatalf("legacy root must be unfiltered")
		}
	})
	t.Run("same path deduplicated", func(t *testing.T) {
		cfgPath := writeCfg(t, `
instance_id: t
storage:
  station_config_root: /tmp/rootA
  station_config_roots:
    - root: /tmp/rootA
      include: [a, b]
`)
		cfg, err := Load([]string{"--config", cfgPath}, nil)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if !reflect.DeepEqual(rootIDs(cfg), []string{"/tmp/rootA"}) {
			t.Fatalf("roots = %v, want single deduplicated entry", rootIDs(cfg))
		}
		if len(cfg.Storage.StationConfigRoots[0].Include) != 0 {
			t.Fatalf("identical legacy path must clear the include filter, got %v",
				cfg.Storage.StationConfigRoots[0].Include)
		}
	})
}

// VERIPROC_STATION_CONFIG_ROOT env override keeps working with the new
// normalization (env overlay happens before normalization).
func TestConfig_StationConfigRoots_EnvOverrideLegacy(t *testing.T) {
	cfgPath := writeCfg(t, `
instance_id: t
storage:
  station_config_root: /tmp/from-file
`)
	cfg, err := Load([]string{"--config", cfgPath}, map[string]string{
		"VERIPROC_STATION_CONFIG_ROOT": "/tmp/from-env",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(rootIDs(cfg), []string{"/tmp/from-env"}) {
		t.Fatalf("roots = %v, want env override to win", rootIDs(cfg))
	}
}

// storage.station_sort: default folder_name, accepted values, defaulting of
// empty, and rejection of unknown values (Requirement: Station order remains
// global).
func TestConfig_StationSort(t *testing.T) {
	valid := map[string]string{
		"":            "folder_name", // unset -> default
		"folder_name": "folder_name",
		"station_id":  "station_id",
	}
	for in, want := range valid {
		body := "instance_id: t\nstorage:\n  working_root_base: /tmp/wr\n"
		if in != "" {
			body += "  station_sort: " + in + "\n"
		}
		cfgPath := writeCfg(t, body)
		cfg, err := Load([]string{"--config", cfgPath}, nil)
		if err != nil {
			t.Fatalf("station_sort %q: %v", in, err)
		}
		if cfg.Storage.StationSort != want {
			t.Errorf("station_sort %q = %q, want %q", in, cfg.Storage.StationSort, want)
		}
	}

	cfgPath := writeCfg(t, "instance_id: t\nstorage:\n  station_sort: bogus\n")
	if _, err := Load([]string{"--config", cfgPath}, nil); err == nil {
		t.Fatal("bogus station_sort must be rejected")
	}
}
