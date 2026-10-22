package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/gobobr/veriproc/internal/auth"
	"github.com/gobobr/veriproc/internal/config"
	"github.com/gobobr/veriproc/internal/health"
	"github.com/gobobr/veriproc/internal/httpapi"
	stationssvc "github.com/gobobr/veriproc/internal/stations"
	"github.com/gobobr/veriproc/internal/store"
)

// newConfigAPI starts a test server whose station service is wired to a
// registry seeded with a fully-specified station (execution + container
// image), instance definitions, and a startup env snapshot.
func newConfigAPI(t *testing.T, withAuth bool) *stationAPI {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "config-api.db")
	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(context.Background(), st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reg := stationssvc.NewRegistry()
	spec, err := stationssvc.SpecFromDefinition(stationssvc.Definition{
		StationID:   "SCECLI",
		StationName: "Scenario Clim",
		Description: "scene climatology station",
		Execution: stationssvc.Execution{
			Mode: "slurm-docker",
			Args: []string{"--center", "<facility.center>", "--start", "<start>"},
			Container: stationssvc.ContainerConfig{
				Image: "harbor.example/co2m.swlib_scene:mt.3.0-1",
			},
		},
	})
	if err != nil {
		t.Fatalf("SpecFromDefinition: %v", err)
	}
	if err := reg.Seed(context.Background(), st, spec); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ssvc := stationssvc.NewService(st, nil)
	ssvc.SetConfigSource(reg, reg, map[string]any{
		"facility": map[string]any{"center": "eucent"},
	}, map[string]string{"SLURM_TOKEN": "opensesame"})

	deps := httpapi.Deps{
		Config:   &config.Config{InstanceID: "test"},
		Health:   health.NewAggregator(time.Second),
		Logger:   zerolog.Nop(),
		Stations: ssvc,
	}
	if withAuth {
		deps.Authn = auth.NewStaticAuthenticator(map[string]auth.Principal{
			"op-secret":     {Subject: "alice", Role: auth.RoleOperator},
			"reader-secret": {Subject: "bob", Role: auth.RoleReader},
		})
	}
	srv := httptest.NewServer(httpapi.NewRouter(deps))
	t.Cleanup(func() { srv.Close(); _ = st.Close() })
	return &stationAPI{srv: srv, st: st, tok: "op-secret", tok2: "reader-secret"}
}

func TestAPI_StationConfig_View(t *testing.T) {
	a := newConfigAPI(t, false)

	resp, body := getJSONMap(t, a.srv, "/api/v1/stations/SCECLI/config")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%v", resp.StatusCode, body)
	}
	if body["station_id"] != "SCECLI" || body["station_name"] != "Scenario Clim" {
		t.Fatalf("identity = %v / %v", body["station_id"], body["station_name"])
	}
	if !strings.HasPrefix(body["content_hash"].(string), "sha256:") {
		t.Fatalf("content_hash = %v", body["content_hash"])
	}
	if body["schema_version"] == nil || body["effective_at"] == nil {
		t.Fatalf("freshness metadata missing: %v", body)
	}
	cfg := body["config"].(map[string]any)
	exec := cfg["execution"].(map[string]any)
	container := exec["container"].(map[string]any)
	if container["image"] != "harbor.example/co2m.swlib_scene:mt.3.0-1" {
		t.Fatalf("container.image = %v", container["image"])
	}
	args := exec["args"].([]any)
	if args[1] != "eucent" {
		t.Fatalf("definitions reference not resolved: args[1] = %v", args[1])
	}
	if args[3] != "<start>" {
		t.Fatalf("runtime reference not literal: args[3] = %v", args[3])
	}
	if !strings.Contains(body["config_yaml"].(string), "harbor.example/co2m.swlib_scene:mt.3.0-1") {
		t.Fatalf("config_yaml missing image: %v", body["config_yaml"])
	}
}

func TestAPI_StationConfig_UnknownStation(t *testing.T) {
	a := newConfigAPI(t, false)
	resp, body := getJSONMap(t, a.srv, "/api/v1/stations/NOPE/config")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d body=%v", resp.StatusCode, body)
	}
	if errObj, ok := body["error"].(map[string]any); !ok || errObj["code"] != "not_found" {
		t.Fatalf("error envelope = %v", body["error"])
	}
}

func TestAPI_StationConfig_NoConfigSource(t *testing.T) {
	// newStationAPI does not wire SetConfigSource: the config endpoint must
	// report the dependency as unavailable rather than crash.
	a := newStationAPI(t, false)
	resp, body := getJSONMap(t, a.srv, "/api/v1/stations/SCENE-L2/config")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%v", resp.StatusCode, body)
	}
}

func TestAPI_StationConfig_ReaderAllowed(t *testing.T) {
	a := newConfigAPI(t, true)

	// Reader may read the config endpoint (Station Config Endpoint → Reader
	// May Read Config).
	resp, body := doReqJSONMap(t, http.MethodGet, a.srv.URL+"/api/v1/stations/SCECLI/config", a.tok2)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reader config status = %d body=%v", resp.StatusCode, body)
	}
	// Sanity: reader still cannot pause (mutating endpoints are operator-only).
	resp, body = doReqJSONMap(t, http.MethodPost, a.srv.URL+"/api/v1/stations/SCECLI/pause", a.tok2)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reader pause status = %d body=%v", resp.StatusCode, body)
	}
}
