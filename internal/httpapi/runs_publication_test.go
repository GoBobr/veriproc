package httpapi_test

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/eum/veriproc/internal/publisher"
	"github.com/eum/veriproc/internal/store"
)

// TestAPI_RunArtifacts_PublicationStatus_5_5_6_M6 — once a publication has
// been written for an artifact and the publisher marks it published, the
// artifact representation returned by GET /runs/{id}/artifacts must surface
// the publication summary (publication_summary.publications, published_count)
// alongside the existing availability/logical_type fields. Spec §5.5.6.
func TestAPI_RunArtifacts_PublicationStatus_5_5_6_M6(t *testing.T) {
	a := newRunAPI(t)
	taskID := a.submitOne(t)
	a.tickN(t, 6)
	ctx := context.Background()
	tk, _ := a.st.Tasks().Get(ctx, taskID)
	arts, _ := a.st.Artifacts().ListByRun(ctx, tk.LatestRunID, "log")
	if len(arts) == 0 {
		t.Fatal("expected at least one log artifact")
	}
	art := arts[0]

	pubRec := &store.PublicationRecord{
		PublicationID:   "pub-it",
		ArtifactID:      art.ArtifactID,
		ProducingRunID:  art.ProducingRunID,
		ArchiveID:       "primary",
		TargetPath:      "logs/" + art.ArtifactID + "/run.log",
		PublicationMode: "copy",
	}
	if err := a.st.Publications().Insert(ctx, pubRec); err != nil {
		t.Fatalf("insert publication: %v", err)
	}

	// Drive the publisher once against a fresh archive directory.
	archive := filepath.Join(t.TempDir(), "archive")
	pub := publisher.New(publisher.Config{
		Store:       a.st,
		ArchiveBase: archive,
		Clock:       func() time.Time { return time.Now().UTC() },
		Logger:      zerolog.New(io.Discard),
		Interval:    time.Hour,
	})
	if _, err := pub.Tick(ctx); err != nil {
		t.Fatalf("publisher tick: %v", err)
	}

	resp, body := getJSONMap(t, a.srv, "/api/v1/runs/"+tk.LatestRunID+"/artifacts")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body=%v", resp.StatusCode, body)
	}
	items, _ := body["items"].([]any)
	if len(items) == 0 {
		t.Fatal("no artifacts in response")
	}
	var found map[string]any
	for _, it := range items {
		m := it.(map[string]any)
		if m["artifact_id"] == art.ArtifactID {
			found = m
			break
		}
	}
	if found == nil {
		t.Fatalf("artifact %s missing from response: %v", art.ArtifactID, items)
	}
	// Required §5.5.6 fields remain.
	for _, k := range []string{"availability", "logical_type", "created_at"} {
		if _, ok := found[k]; !ok {
			t.Errorf("missing core field %q", k)
		}
	}
	sum, ok := found["publication_summary"].(map[string]any)
	if !ok {
		t.Fatalf("publication_summary missing on artifact: %v", found)
	}
	if pc, _ := sum["published_count"].(float64); pc != 1 {
		t.Errorf("published_count = %v, want 1; summary=%v", sum["published_count"], sum)
	}
	if pubs, _ := sum["publications"].([]any); len(pubs) != 1 {
		t.Fatalf("publications list = %v, want one entry", pubs)
	} else {
		first := pubs[0].(map[string]any)
		if first["publication_state"] != "published" {
			t.Errorf("publication_state = %v, want published", first["publication_state"])
		}
		if first["target_path"] != pubRec.TargetPath {
			t.Errorf("target_path = %v, want %s", first["target_path"], pubRec.TargetPath)
		}
		if first["published_at"] == nil {
			t.Errorf("published_at missing on published publication")
		}
	}
}
