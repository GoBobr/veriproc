package stations_test

import (
	"testing"

	"github.com/eum/veriproc/internal/stations"
)

func TestResolveString_NoRefs(t *testing.T) {
	v, err := stations.ResolveString("plain text", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "plain text" {
		t.Fatalf("got %v", v)
	}
}

func TestResolveString_FullRef_Scalar(t *testing.T) {
	ctx := map[string]any{"facility": "SAF"}
	v, err := stations.ResolveString("<facility>", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "SAF" {
		t.Fatalf("got %v", v)
	}
}

func TestResolveString_FullRef_Map(t *testing.T) {
	ctx := map[string]any{"facility": map[string]any{"center": "SAF"}}
	v, err := stations.ResolveString("<facility>", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := v.(map[string]any)
	if !ok || m["center"] != "SAF" {
		t.Fatalf("got %v", v)
	}
}

func TestResolveString_DotPath(t *testing.T) {
	ctx := map[string]any{"facility": map[string]any{"center": "SAF"}}
	v, err := stations.ResolveString("<facility.center>", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "SAF" {
		t.Fatalf("got %v", v)
	}
}

func TestResolveString_EmbeddedRef(t *testing.T) {
	ctx := map[string]any{"env": "PROD"}
	v, err := stations.ResolveString("mode-<env>", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "mode-PROD" {
		t.Fatalf("got %v", v)
	}
}

func TestResolveString_UppercaseNotResolved(t *testing.T) {
	// Uppercase-first placeholders like <MISSION_ID> are not context refs.
	v, err := stations.ResolveString("<MISSION_ID>", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "<MISSION_ID>" {
		t.Fatalf("uppercase placeholder should be passed through, got %v", v)
	}
}

func TestResolveString_MissingKey(t *testing.T) {
	_, err := stations.ResolveString("<missing>", map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestResolveString_EmbeddedNonScalarFails(t *testing.T) {
	ctx := map[string]any{"obj": map[string]any{"a": 1}}
	_, err := stations.ResolveString("prefix-<obj>", ctx)
	if err == nil {
		t.Fatal("expected error when embedded ref resolves to non-scalar")
	}
}

func TestResolveMap_ResolvesLeaves(t *testing.T) {
	ctx := map[string]any{"env": "TEST", "center": "SAF"}
	m := map[string]any{
		"environment": "<env>",
		"center":      "<center>",
		"static":      "unchanged",
	}
	out, err := stations.ResolveMap(m, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["environment"] != "TEST" || out["center"] != "SAF" || out["static"] != "unchanged" {
		t.Fatalf("got %#v", out)
	}
}

func TestResolveMap_NestedMap(t *testing.T) {
	ctx := map[string]any{"val": "deep"}
	m := map[string]any{
		"outer": map[string]any{
			"inner": "<val>",
		},
	}
	out, err := stations.ResolveMap(m, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	outer, ok := out["outer"].(map[string]any)
	if !ok || outer["inner"] != "deep" {
		t.Fatalf("nested resolve failed: %#v", out)
	}
}

func TestResolveArgs_Scalars(t *testing.T) {
	ctx := map[string]any{"mode": "nominal", "cores": 8}
	args := []string{"--mode=<mode>", "--cores=<cores>"}
	out, err := stations.ResolveArgs(args, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out[0] != "--mode=nominal" || out[1] != "--cores=8" {
		t.Fatalf("got %v", out)
	}
}

func TestResolveArgs_NonScalarFails(t *testing.T) {
	ctx := map[string]any{"obj": map[string]any{"a": 1}}
	_, err := stations.ResolveArgs([]string{"<obj>"}, ctx)
	if err == nil {
		t.Fatal("expected error when arg resolves to non-scalar")
	}
}

func TestResolveArgs_NoRefs(t *testing.T) {
	out, err := stations.ResolveArgs([]string{"--flag", "value"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out[0] != "--flag" || out[1] != "value" {
		t.Fatalf("got %v", out)
	}
}
