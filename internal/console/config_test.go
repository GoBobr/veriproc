package console

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfig_Defaults(t *testing.T) {
	dir := t.TempDir()
	body := `schema_version: veriproc.console/v1
http:
  bind_addr: 127.0.0.1:9000
db:
  dsn: file:` + filepath.Join(dir, "console.db") + `
instances:
  - id: vp1
    base_url: http://localhost:8080
    working_root_base: ` + dir + `
`
	p := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.UI.VisibleSlotCount != 12 {
		t.Errorf("visible_slot_count default = %d, want 12", cfg.UI.VisibleSlotCount)
	}
	if cfg.UI.CompletedVisibility.D != 30*time.Second {
		t.Errorf("completed_visibility default = %s", cfg.UI.CompletedVisibility.D)
	}
	if cfg.UI.PreviewMaxBytes != 1<<20 {
		t.Errorf("preview_max_bytes default = %d", cfg.UI.PreviewMaxBytes)
	}
	if cfg.UI.CardMinWidth != 380 {
		t.Errorf("card_min_width_px default = %d, want 380", cfg.UI.CardMinWidth)
	}
	if cfg.UI.CardMaxWidth != 520 {
		t.Errorf("card_max_width_px default = %d, want 520", cfg.UI.CardMaxWidth)
	}
	if len(cfg.Instances[0].AllowedRoots) != 1 {
		t.Errorf("allowed_roots should default from working_root_base")
	}
}

func TestLoadConfig_LongerCompletedTimeoutNowAllowed(t *testing.T) {
	dir := t.TempDir()
	body := `schema_version: veriproc.console/v1
ui:
  completed_visibility_timeout: 60s
  upstream_summary_timeout: 30s
instances:
  - id: vp1
    base_url: http://localhost:8080
    working_root_base: ` + dir + `
`
	p := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(p); err != nil {
		t.Errorf("expected no error with completed_visibility_timeout > upstream_summary_timeout, got: %v", err)
	}
}

func TestLoadConfig_RejectsBadSchema(t *testing.T) {
	dir := t.TempDir()
	body := `schema_version: veriproc.console/v0
instances:
  - id: vp1
    base_url: http://x
`
	p := filepath.Join(dir, "cfg.yaml")
	_ = os.WriteFile(p, []byte(body), 0o644)
	if _, err := LoadConfig(p); err == nil {
		t.Error("expected schema rejection")
	}
}

func TestAuth_PrincipalRoles(t *testing.T) {
	cases := []struct {
		in        string
		want      Role
		wantError bool
	}{
		{"viewer", RoleViewer, false},
		{"reader", RoleViewer, false},
		{"operator", RoleOperator, false},
		{"OPERATOR", RoleOperator, false},
		{"unknown", "", true},
	}
	for _, c := range cases {
		got, err := NormalizeRole(c.in)
		if c.wantError {
			if err == nil {
				t.Errorf("NormalizeRole(%q) expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeRole(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("NormalizeRole(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
