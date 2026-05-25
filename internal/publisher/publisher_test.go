package publisher_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/eum/veriproc/internal/publisher"
	"github.com/eum/veriproc/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "pub.db")
	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(context.Background(), st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func writeArtifact(t *testing.T, st *store.Store, runID, payload string) *store.ArtifactRecord {
	t.Helper()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "log.txt")
	if err := os.WriteFile(srcPath, []byte(payload), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	a := &store.ArtifactRecord{
		ArtifactID:     "art-1",
		ProducingRunID: "",
		LogicalType:    "log",
		Path:           srcPath,
		Size:           int64(len(payload)),
		Availability:   "available",
	}
	if err := st.Artifacts().Insert(context.Background(), a); err != nil {
		t.Fatalf("insert artifact: %v", err)
	}
	return a
}

// TestPublisher_Happy — Tick copies the source file to archiveBase and
// transitions the publication to "published".
func TestPublisher_Happy(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := writeArtifact(t, st, "run-x", "hello world")
	archive := t.TempDir()
	pub := &store.PublicationRecord{
		PublicationID:   "pub-1",
		ArtifactID:      a.ArtifactID,
		ProducingRunID:  a.ProducingRunID,
		ArchiveID:       "archive-1",
		TargetPath:      "2025/07/run-x/log.txt",
		PublicationMode: "copy",
	}
	if err := st.Publications().Insert(ctx, pub); err != nil {
		t.Fatalf("insert pub: %v", err)
	}
	svc := publisher.New(publisher.Config{
		Store:       st,
		ArchiveBase: archive,
		Clock:       func() time.Time { return time.Unix(1700000000, 0).UTC() },
		Logger:      zerolog.New(io.Discard),
		Interval:    time.Hour, // not used for Tick-only tests
	})
	if _, err := svc.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	got, err := st.Publications().Get(ctx, "pub-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PublicationState != store.PublicationStatePublished {
		t.Errorf("state = %q, want published", got.PublicationState)
	}
	if !got.PublishedAt.Valid {
		t.Errorf("published_at not stamped")
	}
	bytes, err := os.ReadFile(filepath.Join(archive, pub.TargetPath))
	if err != nil {
		t.Fatalf("read archived: %v", err)
	}
	if string(bytes) != "hello world" {
		t.Errorf("archived contents = %q, want %q", bytes, "hello world")
	}
}

func TestPublisher_DirectoryArtifact(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	srcDir := filepath.Join(t.TempDir(), "dir-artifact")
	if err := os.MkdirAll(filepath.Join(srcDir, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "nested", "data.nc"), []byte("directory payload"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	a := &store.ArtifactRecord{ArtifactID: "art-dir", LogicalType: "output", ObjectKind: store.ObjectKindDirectory, Path: srcDir, Availability: "available"}
	if err := st.Artifacts().Insert(ctx, a); err != nil {
		t.Fatalf("insert artifact: %v", err)
	}
	pub := &store.PublicationRecord{PublicationID: "pub-dir", ArtifactID: a.ArtifactID, ArchiveID: "archive-1", TargetPath: "published/dir-artifact", ObjectKind: store.ObjectKindDirectory, PublicationMode: "copy"}
	if err := st.Publications().Insert(ctx, pub); err != nil {
		t.Fatalf("insert pub: %v", err)
	}
	archive := t.TempDir()
	svc := publisher.New(publisher.Config{Store: st, ArchiveBase: archive, Logger: zerolog.New(io.Discard)})
	if _, err := svc.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	got, err := st.Publications().Get(ctx, "pub-dir")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PublicationState != store.PublicationStatePublished || got.ObjectKind != store.ObjectKindDirectory {
		t.Fatalf("publication = %#v", got)
	}
	body, err := os.ReadFile(filepath.Join(archive, pub.TargetPath, "nested", "data.nc"))
	if err != nil {
		t.Fatalf("read archived dir payload: %v", err)
	}
	if string(body) != "directory payload" {
		t.Fatalf("archived payload = %q", body)
	}
}

// TestPublisher_FailureMissingSource — when the artifact's source file is
// missing, the publication is marked failed with a recorded reason.
func TestPublisher_FailureMissingSource(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := &store.ArtifactRecord{
		ArtifactID:     "art-2",
		ProducingRunID: "",
		LogicalType:    "log",
		Path:           filepath.Join(t.TempDir(), "missing.txt"),
		Availability:   "available",
	}
	if err := st.Artifacts().Insert(ctx, a); err != nil {
		t.Fatalf("insert artifact: %v", err)
	}
	pub := &store.PublicationRecord{
		PublicationID:  "pub-2",
		ArtifactID:     a.ArtifactID,
		ProducingRunID: a.ProducingRunID,
		ArchiveID:      "archive-1",
		TargetPath:     "fail/log.txt",
	}
	if err := st.Publications().Insert(ctx, pub); err != nil {
		t.Fatalf("insert pub: %v", err)
	}
	svc := publisher.New(publisher.Config{
		Store:       st,
		ArchiveBase: t.TempDir(),
		Logger:      zerolog.New(io.Discard),
	})
	if _, err := svc.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	got, err := st.Publications().Get(ctx, "pub-2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PublicationState != store.PublicationStateFailed {
		t.Errorf("state = %q, want failed", got.PublicationState)
	}
	if got.FailureReason == "" {
		t.Errorf("failure_reason empty")
	}
}
