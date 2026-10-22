package stations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gobobr/veriproc/internal/stations"
)

func TestSpecFromDefinition_CarriesDescription(t *testing.T) {
	spec, err := stations.SpecFromDefinition(stations.Definition{
		StationID: "STATION-A", StationName: "SCE_2", Description: "scene generation",
	})
	if err != nil {
		t.Fatalf("SpecFromDefinition: %v", err)
	}
	if spec.Description != "scene generation" {
		t.Fatalf("spec.Description = %q, want %q", spec.Description, "scene generation")
	}
}

func TestRegistry_Description(t *testing.T) {
	ctx := context.Background()
	reg := stations.NewRegistry()
	spec, err := stations.SpecFromDefinition(stations.Definition{
		StationID: "STATION-A", StationName: "SCE_2", Description: "demo",
	})
	if err != nil {
		t.Fatalf("SpecFromDefinition: %v", err)
	}
	if err := reg.Seed(ctx, nil, spec); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if d, ok := reg.Description("STATION-A"); !ok || d != "demo" {
		t.Fatalf("Description = (%q, %v), want (%q, true)", d, ok, "demo")
	}
	if _, ok := reg.Description("nope"); ok {
		t.Fatal("Description for unknown station should not be found")
	}
}

func TestService_ConfigView(t *testing.T) {
	ctx := context.Background()

	newService := func(env map[string]string) *stations.Service {
		reg := stations.NewRegistry()
		spec, err := stations.SpecFromDefinition(stations.Definition{
			StationID:   "SCECLI",
			StationName: "Scenario Clim",
			Description: "scene climatology station",
			Execution: stations.Execution{
				Mode: "slurm-docker",
				Args: []string{"--center", "<facility.center>", "--start", "<start>"},
				Container: stations.ContainerConfig{
					Image:  "harbor.example/co2m.swlib_scene:mt.3.0-1",
					Mounts: []string{"/data:/data"},
				},
			},
			JobOrder: stations.JobOrderConfig{
				Renderer: "template",
				Template: "{{.mission}}",
				Params: map[string]any{
					// Params carries template variables; values here stand in
					// for already env-expanded strings at load time.
					"vars": map[string]any{"mission": "<mission_id>", "token": "Bearer opensesame"},
				},
			},
			Outputs: stations.OutputDefinitions{{Name: "scene.nc", FileType: "SCENE", Mandatory: true}},
		})
		if err != nil {
			t.Fatalf("SpecFromDefinition: %v", err)
		}
		st := openStore(t)
		if err := reg.Seed(ctx, st, spec); err != nil {
			t.Fatalf("Seed: %v", err)
		}
		svc := stations.NewService(st, nil)
		svc.SetConfigSource(reg, reg, map[string]any{
			"facility":   map[string]any{"center": "eucent"},
			"mission_id": "CO2M",
		}, env)
		return svc
	}

	t.Run("resolved contents and image", func(t *testing.T) {
		svc := newService(nil)
		view, err := svc.ConfigView(ctx, "SCECLI")
		if err != nil {
			t.Fatalf("ConfigView: %v", err)
		}
		exec := view.Config.(map[string]any)["execution"].(map[string]any)
		container := exec["container"].(map[string]any)
		if got := container["image"]; got != "harbor.example/co2m.swlib_scene:mt.3.0-1" {
			t.Fatalf("container.image = %v, want fully expanded image tag", got)
		}
		args := exec["args"].([]any)
		// Definitions reference resolved, runtime reference left literal.
		if args[1] != "eucent" {
			t.Fatalf("args[1] = %v, want resolved definitions value %q", args[1], "eucent")
		}
		if args[3] != "<start>" {
			t.Fatalf("args[3] = %v, want literal runtime reference %q", args[3], "<start>")
		}
		jo := view.Config.(map[string]any)["joborder"].(map[string]any)
		params := jo["params"].(map[string]any)["vars"].(map[string]any)
		if params["mission"] != "CO2M" {
			t.Fatalf("joborder.params.vars.mission = %v, want %q", params["mission"], "CO2M")
		}
		if view.Config.(map[string]any)["description"] != "scene climatology station" {
			t.Fatalf("description missing from config document")
		}
	})

	t.Run("freshness metadata", func(t *testing.T) {
		svc := newService(nil)
		view, err := svc.ConfigView(ctx, "SCECLI")
		if err != nil {
			t.Fatalf("ConfigView: %v", err)
		}
		if !strings.HasPrefix(view.ContentHash, "sha256:") {
			t.Fatalf("content_hash = %q, want sha256:...", view.ContentHash)
		}
		if view.SchemaVersion == "" {
			t.Fatal("schema_version is empty")
		}
		if view.EffectiveAt.IsZero() {
			t.Fatal("effective_at is zero")
		}
		if view.StationID != "SCECLI" || view.StationName != "Scenario Clim" {
			t.Fatalf("identity = (%q, %q)", view.StationID, view.StationName)
		}
	})

	t.Run("secret redaction", func(t *testing.T) {
		svc := newService(map[string]string{
			"IMAGE_SCENE":   "harbor.example/co2m.swlib_scene:mt.3.0-1",
			"SLURM_TOKEN":   "opensesame",
			"SCENE_VERSION": "mt.3.0-1",
		})
		view, err := svc.ConfigView(ctx, "SCECLI")
		if err != nil {
			t.Fatalf("ConfigView: %v", err)
		}
		jo := view.Config.(map[string]any)["joborder"].(map[string]any)
		params := jo["params"].(map[string]any)["vars"].(map[string]any)
		if got := params["token"]; got != "Bearer [REDACTED]" {
			t.Fatalf("joborder.params.vars.token = %v, want redacted value", got)
		}
		// Non-secret-named variables must not be redacted.
		exec := view.Config.(map[string]any)["execution"].(map[string]any)
		container := exec["container"].(map[string]any)
		if got := container["image"]; got != "harbor.example/co2m.swlib_scene:mt.3.0-1" {
			t.Fatalf("container.image = %v, want unredacted (non-secret var)", got)
		}
	})

	t.Run("yaml round-trip", func(t *testing.T) {
		svc := newService(nil)
		view, err := svc.ConfigView(ctx, "SCECLI")
		if err != nil {
			t.Fatalf("ConfigView: %v", err)
		}
		for _, want := range []string{
			"image: harbor.example/co2m.swlib_scene:mt.3.0-1",
			"--center", "eucent", "<start>", "scene climatology station",
		} {
			if !strings.Contains(view.ConfigYAML, want) {
				t.Fatalf("config_yaml missing %q:\n%s", want, view.ConfigYAML)
			}
		}
	})

	t.Run("unknown station", func(t *testing.T) {
		svc := newService(nil)
		_, err := svc.ConfigView(ctx, "NOPE")
		if err == nil || !strings.Contains(err.Error(), "unknown station") {
			t.Fatalf("err = %v, want unknown station error", err)
		}
	})

	t.Run("no config source", func(t *testing.T) {
		svc := stations.NewService(openStore(t), nil)
		if _, err := svc.ConfigView(ctx, "SCECLI"); err == nil {
			t.Fatal("expected error when config source is not wired")
		}
	})
}
