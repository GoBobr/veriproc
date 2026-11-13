package runs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gobobr/veriproc/internal/executor"
	"github.com/gobobr/veriproc/internal/stations"
	"github.com/gobobr/veriproc/internal/store"
	"github.com/gobobr/veriproc/internal/tasks"
)

// These tests pin the candidate entry inspection contract:
//   - entries failing the input's name contract are never stat'ed, so an
//     unrelated broken symlink in a shared folder cannot fail the task
//     (field regression: no2gap tasks killed by dangling SCE_2__CSM links);
//   - winner selection is metadata-only; after selection the winner's
//     readability is validated: an unreadable winner fails preparation with
//     the file type and path, an unreadable non-winner is ignored and never
//     staged (field regression: eucent-3.0 no2-l2 tasks killed while fixture
//     archives were being populated concurrently);
//   - source: upstream inputs keep their existing missing-path failure.

// newScanFixture builds a store + registry + service for one station whose
// single mandatory product input is PRIMARY_INPUT___, with the given archive
// folder as the product category folder. Naming follows structuredNaming()
// so the structured-pattern branch is exercised. The returned string is the
// working root base used by the service.
func newScanFixture(t *testing.T, archive string) (*store.Store, *tasks.Service, *Service, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open("sqlite://" + filepath.Join(dir, "scan.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(ctx, st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reg := stations.NewRegistry()
	if err := reg.Seed(ctx, st, stations.Spec{
		StationID:     "SCAN-A",
		StationName:   "SCAN-A",
		ContentHash:   "sha256:scan-a",
		SchemaVersion: "veriproc.station/v1",
		Inputs:        []stations.InputDefinition{{FileType: "PRIMARY_INPUT___", Category: "product"}},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tsvc := tasks.NewService(st, reg, nil, func() string { return "0000aa" })
	runN := 0
	workBase := filepath.Join(dir, "work")
	rsvc := NewService(Config{
		Store:             st,
		Executors:         executor.NewSingleExecutorRegistry(executor.NewLocalExecutor(nil)),
		Resolver:          reg,
		WorkingRootBase:   workBase,
		RollingArchives:   map[string]string{"hot": archive},
		ProductCategories: map[string][]string{"product": {"rolling:hot"}},
		Naming:            structuredNaming(),
		IDFactory:         func() string { runN++; return fmt.Sprintf("run-scan-%d", runN) },
	})
	return st, tsvc, rsvc, workBase
}

func submitScanTask(t *testing.T, tsvc *tasks.Service) string {
	t.Helper()
	window := defaultTask()
	res, err := tsvc.Submit(context.Background(), tasks.SubmitInput{
		Destination: tasks.Destination{StationID: "SCAN-A"},
		Window:      tasks.Window{Start: window.WindowStart, End: window.WindowEnd},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	return res.Task.TaskID
}

// TestPrepareRun_UnrelatedDanglingSymlinkIgnored verifies Scenario "Unrelated
// dangling symlink is ignored": a folder containing a dangling symlink whose
// name fails the declared input's name contract, plus a valid candidate, must
// resolve normally — the broken entry is never inspected.
func TestPrepareRun_UnrelatedDanglingSymlinkIgnored(t *testing.T) {
	ctx := context.Background()
	archive := t.TempDir()
	good := filepath.Join(archive, "CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T112000_v1.txt")
	if err := os.WriteFile(good, []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("write candidate: %v", err)
	}
	// Dangling symlink belonging to a different file type (the no2gap case).
	other := filepath.Join(archive, "CDMA_SCE_2__CSM_______20250703T110000_20250703T111500_20250703T114000_v1.txt")
	if err := os.Symlink(filepath.Join(archive, "missing-target.nc"), other); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	st, tsvc, rsvc, _ := newScanFixture(t, archive)
	taskID := submitScanTask(t, tsvc)

	run, err := rsvc.PrepareRun(ctx, taskID)
	if err != nil {
		t.Fatalf("PrepareRun must succeed despite unrelated dangling symlink: %v", err)
	}
	if run.State != "ready" {
		t.Fatalf("run state = %q, want ready", run.State)
	}
	mf, err := st.Manifests().GetByRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if len(mf.Entries) != 1 || filepath.Base(mf.Entries[0].Path) != filepath.Base(good) {
		t.Fatalf("manifest entries = %+v, want only the valid %s", mf.Entries, filepath.Base(good))
	}
}

// TestPrepareRun_UnreadableWinnerIsFatal verifies Scenario "Unreadable winner
// is fatal": the group's only candidate is a dangling symlink, so the selected
// winner cannot be inspected — preparation fails with a fatal error identifying
// the offending path and file type, and the working root created for the
// attempt is cleaned up.
func TestPrepareRun_UnreadableWinnerIsFatal(t *testing.T) {
	ctx := context.Background()
	archive := t.TempDir()
	broken := filepath.Join(archive, "CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T112000_v1.txt")
	if err := os.Symlink(filepath.Join(archive, "missing-target.nc"), broken); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, tsvc, rsvc, workBase := newScanFixture(t, archive)
	taskID := submitScanTask(t, tsvc)

	run, err := rsvc.PrepareRun(ctx, taskID)
	if err == nil {
		t.Fatalf("PrepareRun should fail for an unreadable matching candidate, got run %+v", run)
	}
	if !errors.Is(err, ErrFatalPrepare) {
		t.Fatalf("error %v must wrap ErrFatalPrepare", err)
	}
	if !strings.Contains(err.Error(), "input PRIMARY_INPUT___ candidate") || !strings.Contains(err.Error(), broken) {
		t.Fatalf("error must identify file type and path: %v", err)
	}
	// The working root materialised for the failed attempt must be removed:
	// no files or symlinks may remain under the base (empty ancestor
	// directories are tolerated).
	if leftover := nonDirEntries(t, workBase); len(leftover) != 0 {
		t.Fatalf("working root base should hold no files after fatal prepare, found: %v", leftover)
	}
}

// nonDirEntries walks root and returns the relative paths of everything that
// is not a directory (regular files and symlinks).
func nonDirEntries(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return rerr
			}
			found = append(found, rel)
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("walk %s: %v", root, err)
	}
	return found
}

// TestClassicalMatcher_NonMatchingDanglingEntryCannotFail verifies Scenario
// "Non-matching entry cannot fail preparation" for both scanning branches:
// a folder containing only a dangling symlink that fails the input's name
// contract resolves to no candidates without any error from that entry.
func TestClassicalMatcher_NonMatchingDanglingEntryCannotFail(t *testing.T) {
	dangling := "CDMA_SCE_2__CSM_______20250703T110000_20250703T111500_20250703T114000_v1.txt"
	for _, tc := range []struct {
		name string
		svc  *Service
	}{
		{"structured-pattern", &Service{naming: structuredNaming()}},
		{"unstructured-glob", &Service{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			folder := t.TempDir()
			if err := os.Symlink(filepath.Join(folder, "missing.nc"), filepath.Join(folder, dangling)); err != nil {
				t.Fatalf("symlink: %v", err)
			}
			winners, reason, err := tc.svc.classicalSelectCandidates("run-1",
				stations.InputDefinition{FileType: "PRIMARY_INPUT___", Category: "product", WindowMatch: "overlaps"},
				[]string{folder}, defaultTask(), nil)
			if err != nil {
				t.Fatalf("dangling non-matching entry must not fail the scan: %v", err)
			}
			if len(winners) != 0 {
				t.Fatalf("winners = %+v, want none", winners)
			}
			if reason != "no matching candidate" {
				t.Fatalf("missing reason = %q, want %q", reason, "no matching candidate")
			}
		})
	}
}

// TestClassicalMatcher_UnstructuredUnreadableWinnerIsFatal verifies the
// unstructured (glob) branch applies the same winner-validation rule: a
// glob-matching dangling symlink that is the group's only candidate becomes
// the winner and fails fatally instead of being silently skipped.
func TestClassicalMatcher_UnstructuredUnreadableWinnerIsFatal(t *testing.T) {
	svc := &Service{} // no instance filename_pattern → glob branch
	folder := t.TempDir()
	// Glob `*PRIMARY_INPUT___*` matches and filenameMatchesInput accepts it.
	broken := filepath.Join(folder, "granule_PRIMARY_INPUT____granule.dat")
	if err := os.Symlink(filepath.Join(folder, "missing.nc"), broken); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	_, _, err := svc.classicalSelectCandidates("run-1",
		stations.InputDefinition{FileType: "PRIMARY_INPUT___", Category: "product", WindowMatch: "overlaps"},
		[]string{folder}, defaultTask(), nil)
	if err == nil {
		t.Fatal("glob-matching dangling symlink as sole winner must fail fatally")
	}
	if !strings.Contains(err.Error(), "input PRIMARY_INPUT___ candidate") || !strings.Contains(err.Error(), broken) {
		t.Fatalf("error must identify file type and path: %v", err)
	}
}

// TestPrepareRun_UnreadableNonWinnerIgnored verifies Scenarios "Unreadable
// non-winner is ignored" and "Unreadable candidate never staged": a dangling
// candidate that loses the metadata tie-break (or is rejected by the window
// policy) never fails preparation and never appears in the manifest — the
// field regression from eucent-3.0 where a dangling products-dir symlink for
// a *different* window killed no2-l2 tasks while the rolling archive was
// being populated.
func TestPrepareRun_UnreadableNonWinnerIgnored(t *testing.T) {
	ctx := context.Background()
	archive := t.TempDir()
	// Readable winner: same interval group, newer generation time.
	winner := writeFile(t, archive,
		"CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T112000_v2.txt", time.Time{})
	// Dangling candidate in the SAME group but older generation time: loses
	// the metadata tie-break and must be ignored.
	danglingSameGroup := filepath.Join(archive,
		"CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T111000_v1.txt")
	if err := os.Symlink(filepath.Join(archive, "missing-a.nc"), danglingSameGroup); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	// Dangling candidate from a NON-overlapping window (12:00-12:15 vs task
	// window 11:05-11:10): rejected by the window policy before winner
	// selection — the exact eucent-3.0 failure shape.
	danglingOtherWindow := filepath.Join(archive,
		"CDMA_PRIMARY_INPUT____20250703T120000_20250703T121500_20250703T122000_v1.txt")
	if err := os.Symlink(filepath.Join(archive, "missing-b.nc"), danglingOtherWindow); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	st, tsvc, rsvc, _ := newScanFixture(t, archive)
	taskID := submitScanTask(t, tsvc)

	run, err := rsvc.PrepareRun(ctx, taskID)
	if err != nil {
		t.Fatalf("PrepareRun must succeed when the winner is readable: %v", err)
	}
	if run.State != "ready" {
		t.Fatalf("run state = %q, want ready", run.State)
	}
	mf, err := st.Manifests().GetByRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if len(mf.Entries) != 1 || filepath.Base(mf.Entries[0].Path) != filepath.Base(winner) {
		t.Fatalf("manifest entries = %+v, want only the readable winner %s", mf.Entries, filepath.Base(winner))
	}
	// Neither unreadable entry may be staged into the working root.
	links, err := os.ReadDir(filepath.Join(run.WorkingRoot, "input"))
	if err != nil {
		t.Fatalf("read input dir: %v", err)
	}
	if len(links) != 1 || links[0].Name() != filepath.Base(winner) {
		names := make([]string, 0, len(links))
		for _, l := range links {
			names = append(names, l.Name())
		}
		t.Fatalf("working root input links = %v, want only %s", names, filepath.Base(winner))
	}
}

// TestPrepareRun_UnreadableWinnerWithReadableAlternative verifies Scenario
// "Unreadable winner with readable alternative": selection is metadata-only,
// so the dangling candidate preferred by the tie-break (newer generation time)
// becomes the winner and preparation fails — no silent substitution of the
// readable alternative.
func TestPrepareRun_UnreadableWinnerWithReadableAlternative(t *testing.T) {
	ctx := context.Background()
	archive := t.TempDir()
	// Readable but OLDER generation time: loses the metadata tie-break.
	writeFile(t, archive,
		"CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T111000_v1.txt", time.Time{})
	// Dangling but NEWER generation time: wins selection, then fails
	// readability validation.
	danglingWinner := filepath.Join(archive,
		"CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T112000_v2.txt")
	if err := os.Symlink(filepath.Join(archive, "missing.nc"), danglingWinner); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, tsvc, rsvc, _ := newScanFixture(t, archive)
	taskID := submitScanTask(t, tsvc)

	run, err := rsvc.PrepareRun(ctx, taskID)
	if err == nil {
		t.Fatalf("PrepareRun must fail when the metadata-preferred winner is unreadable, got run %+v", run)
	}
	if !errors.Is(err, ErrFatalPrepare) {
		t.Fatalf("error %v must wrap ErrFatalPrepare", err)
	}
	if !strings.Contains(err.Error(), "input PRIMARY_INPUT___ candidate") || !strings.Contains(err.Error(), danglingWinner) {
		t.Fatalf("error must identify file type and winner path: %v", err)
	}
}

// TestResolveManifest_UpstreamMissingPathUnchanged verifies Scenario
// "Upstream-declared inputs unchanged": an upstream input whose declared file
// list references a missing path keeps the upstream-specific failure and is
// not routed through folder scanning.
func TestResolveManifest_UpstreamMissingPathUnchanged(t *testing.T) {
	ctx := context.Background()
	svc := &Service{}
	workingRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workingRoot, "input"), 0o755); err != nil {
		t.Fatalf("mkdir input: %v", err)
	}
	rev := &store.StationRevisionRecord{
		StationID:       "UP-A",
		DeclaredInputs:  `[{"file_type":"GRANULE_H5","category":"product","source":"upstream","mandatory":true}]`,
		DeclaredOutputs: "",
	}
	task := &store.TaskRecord{
		DeclaredInputs: []byte(`[{"file_type":"GRANULE_H5","files":["/nonexistent/path/granule.h5"]}]`),
	}
	_, err := svc.resolveManifest(ctx, "mf-scan", "run-scan-a/0", task, rev, workingRoot)
	if err == nil {
		t.Fatal("expected failure for missing upstream-declared path")
	}
	if !strings.Contains(err.Error(), "upstream input GRANULE_H5") || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("upstream missing path must keep its upstream failure: %v", err)
	}
}
