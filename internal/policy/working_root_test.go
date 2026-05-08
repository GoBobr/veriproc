package policy

import (
	"strings"
	"testing"
)

// TestValidatePathTemplate_Valid — accepted templates.
func TestValidatePathTemplate_Valid(t *testing.T) {
	cases := []string{
		"{station}/{task}/{run}",
		"{task}/{run}",
		"{task}-{run}",
		"{run}",
		"{station}-{task}-{run}",
		"runs/{station}/{task}/{run}",
	}
	for _, c := range cases {
		if err := ValidatePathTemplate(c); err != nil {
			t.Errorf("ValidatePathTemplate(%q) unexpected error: %v", c, err)
		}
	}
}

// TestValidatePathTemplate_Absolute — absolute templates must be rejected.
func TestValidatePathTemplate_Absolute(t *testing.T) {
	if err := ValidatePathTemplate("/station/task/run"); err == nil {
		t.Error("expected error for absolute template")
	}
}

// TestValidatePathTemplate_Unsupported — unsupported placeholders must be rejected.
func TestValidatePathTemplate_Unsupported(t *testing.T) {
	cases := []struct {
		tmpl string
		want string
	}{
		{"{station}/{task}/{run}/{extra}", "{extra}"},
		{"{foo}", "{foo}"},
		{"{station_id}", "{station_id}"},
	}
	for _, c := range cases {
		err := ValidatePathTemplate(c.tmpl)
		if err == nil {
			t.Errorf("ValidatePathTemplate(%q) expected error, got nil", c.tmpl)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("ValidatePathTemplate(%q) error = %q, want to contain %q", c.tmpl, err.Error(), c.want)
		}
	}
}

// TestValidatePathTemplate_Empty — empty template must be rejected.
func TestValidatePathTemplate_Empty(t *testing.T) {
	if err := ValidatePathTemplate(""); err == nil {
		t.Error("expected error for empty template")
	}
	if err := ValidatePathTemplate("   "); err == nil {
		t.Error("expected error for whitespace-only template")
	}
}

// TestValidatePathTemplate_DotSegment — templates that expand to dot-segments must be rejected.
func TestValidatePathTemplate_DotSegment(t *testing.T) {
	// A template with a literal ".." component must be rejected after dummy expansion.
	if err := ValidatePathTemplate("../escape"); err == nil {
		t.Error("expected error for dot-segment template")
	}
	if err := ValidatePathTemplate("good/../bad"); err == nil {
		t.Error("expected error for dot-segment template")
	}
}

// TestExpandWorkingRootTemplate_Nested — three-component nested layout.
func TestExpandWorkingRootTemplate_Nested(t *testing.T) {
	result := ExpandWorkingRootTemplate("{station}/{task}/{run}", "STATION-A", "task-abc", "r0")
	if result != "STATION-A/task-abc/r0" {
		t.Errorf("got %q, want %q", result, "STATION-A/task-abc/r0")
	}
}

// TestExpandWorkingRootTemplate_Flat — flat single-component layout.
func TestExpandWorkingRootTemplate_Flat(t *testing.T) {
	result := ExpandWorkingRootTemplate("{task}-{run}", "STATION-A", "task-abc", "r0")
	if result != "task-abc-r0" {
		t.Errorf("got %q, want %q", result, "task-abc-r0")
	}
}

// TestExpandWorkingRootTemplate_AllTokens — all three tokens expanded.
func TestExpandWorkingRootTemplate_AllTokens(t *testing.T) {
	result := ExpandWorkingRootTemplate("{station}-{task}-{run}", "S", "T", "R")
	if result != "S-T-R" {
		t.Errorf("got %q, want %q", result, "S-T-R")
	}
}

// TestExpandWorkingRootTemplate_Deterministic — same inputs produce identical output.
func TestExpandWorkingRootTemplate_Deterministic(t *testing.T) {
	tmpl := "{station}/{task}/{run}"
	r1 := ExpandWorkingRootTemplate(tmpl, "S", "T", "R")
	r2 := ExpandWorkingRootTemplate(tmpl, "S", "T", "R")
	if r1 != r2 {
		t.Errorf("non-deterministic: %q vs %q", r1, r2)
	}
}

// TestDefaultNaming_PathTemplate — DefaultNaming provides a path_template.
func TestDefaultNaming_PathTemplate(t *testing.T) {
	n := DefaultNaming()
	if n.WorkingRoot.PathTemplate == "" {
		t.Error("DefaultNaming().WorkingRoot.PathTemplate must not be empty")
	}
	if err := ValidatePathTemplate(n.WorkingRoot.PathTemplate); err != nil {
		t.Errorf("DefaultNaming().WorkingRoot.PathTemplate invalid: %v", err)
	}
}

// TestWithDefaults_PathTemplate — WithDefaults fills in missing path_template.
func TestWithDefaults_PathTemplate(t *testing.T) {
	n := Naming{}
	filled := n.WithDefaults()
	if filled.WorkingRoot.PathTemplate == "" {
		t.Error("WithDefaults should fill PathTemplate")
	}
	if err := ValidatePathTemplate(filled.WorkingRoot.PathTemplate); err != nil {
		t.Errorf("filled PathTemplate invalid: %v", err)
	}
}

// TestWithDefaults_PathTemplate_PreservesExisting — explicit template is kept.
func TestWithDefaults_PathTemplate_PreservesExisting(t *testing.T) {
	n := Naming{WorkingRoot: WorkingRoot{PathTemplate: "{task}-{run}"}}
	filled := n.WithDefaults()
	if filled.WorkingRoot.PathTemplate != "{task}-{run}" {
		t.Errorf("expected explicit template preserved, got %q", filled.WorkingRoot.PathTemplate)
	}
}
