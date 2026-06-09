package tasks_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gobobr/veriproc/internal/policy"
	"github.com/gobobr/veriproc/internal/stations"
	"github.com/gobobr/veriproc/internal/store"
	"github.com/gobobr/veriproc/internal/tasks"
)

// fixedClock returns a deterministic time source.
func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// seqIDs returns sequential 6-hex suffix factories suitable for the new task
// ID grammar: the tasks service now treats the idFactory return value as the
// HEX6 suffix portion (the station prefix and timestamp are added by
// policy.GenerateTaskID).
func seqIDs(_ string) func() string {
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("%06x", n)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	const digits = "0123456789"
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = digits[i%10]
		i /= 10
	}
	return string(buf[pos:])
}

// newSvc spins up an isolated SQLite store, seeds two stations, and returns a
// fresh Service plus a teardown via t.Cleanup.
func newSvc(t *testing.T) (*tasks.Service, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "tasks.db")
	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(context.Background(), st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reg := stations.NewRegistry()
	if err := reg.Seed(context.Background(), st,
		stations.Spec{StationID: "SCENE-L2", StationName: "SCE_2", ContentHash: "sha256:scene-l2", SchemaVersion: "veriproc.station/v1"},
		stations.Spec{StationID: "INGEST", StationName: "INGEST", ContentHash: "sha256:ingest", SchemaVersion: "veriproc.station/v1"},
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	now := time.Date(2025, 7, 3, 11, 50, 0, 0, time.UTC)
	svc := tasks.NewService(st, reg, fixedClock(now), seqIDs("task-test"))
	return svc, st
}

func validInput() tasks.SubmitInput {
	return tasks.SubmitInput{
		Destination: tasks.Destination{StationID: "SCENE-L2"},
		Window: tasks.Window{
			Start: time.Date(2025, 7, 3, 11, 15, 39, 0, time.UTC),
			End:   time.Date(2025, 7, 3, 11, 30, 0, 0, time.UTC),
		},
	}
}

// TestService_Submit_HappyPath_5_3_1 — valid request → created task with
// state=accepted, submission_origin=client, force defaulted to false.
func TestService_Submit_HappyPath_5_3_1(t *testing.T) {
	svc, _ := newSvc(t)
	res, err := svc.Submit(context.Background(), validInput())
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !res.Created {
		t.Error("Created = false; want true")
	}
	if res.Task.State != "accepted" {
		t.Errorf("state = %q, want accepted", res.Task.State)
	}
	if res.Task.SubmissionOrigin != "client" {
		t.Errorf("submission_origin = %q, want client", res.Task.SubmissionOrigin)
	}
	if res.Task.Force {
		t.Error("force = true, want false default")
	}
	if res.Task.RoutingContentHash == "" {
		t.Error("routing_content_hash empty")
	}
}

// TestService_Submit_InvalidWindow_5_3_3 — window end < start → invalid_request.
func TestService_Submit_InvalidWindow_5_3_3(t *testing.T) {
	svc, _ := newSvc(t)
	in := validInput()
	in.Window.End = in.Window.Start.Add(-time.Hour)
	_, err := svc.Submit(context.Background(), in)
	if !errors.Is(err, tasks.ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

// TestService_Submit_UnknownStation_5_3_2 — station_id not in registry.
func TestService_Submit_UnknownStation_5_3_2(t *testing.T) {
	svc, _ := newSvc(t)
	in := validInput()
	in.Destination.StationID = "NOPE"
	_, err := svc.Submit(context.Background(), in)
	if !errors.Is(err, tasks.ErrUnknownStation) {
		t.Fatalf("err = %v, want ErrUnknownStation", err)
	}
}

// TestService_Submit_IdempotencyReplay_5_3_5 — same key + same body returns
// the original task; Created=false; only one row in the store.
func TestService_Submit_IdempotencyReplay_5_3_5(t *testing.T) {
	svc, st := newSvc(t)
	in := validInput()
	in.IdempotencyKey = "client-req-1"

	first, err := svc.Submit(context.Background(), in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := svc.Submit(context.Background(), in)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if second.Created {
		t.Error("Created = true on replay")
	}
	if second.Task.TaskID != first.Task.TaskID {
		t.Errorf("task_id changed: %s -> %s", first.Task.TaskID, second.Task.TaskID)
	}
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("tasks rows = %d, want 1", n)
	}
}

// TestService_Submit_IdempotencyConflict_5_3_5 — same key, different body.
func TestService_Submit_IdempotencyConflict_5_3_5(t *testing.T) {
	svc, _ := newSvc(t)
	in := validInput()
	in.IdempotencyKey = "client-req-2"
	if _, err := svc.Submit(context.Background(), in); err != nil {
		t.Fatalf("first: %v", err)
	}
	in.Force = true // mutate body
	_, err := svc.Submit(context.Background(), in)
	if !errors.Is(err, tasks.ErrIdempotencyConflict) {
		t.Fatalf("err = %v, want ErrIdempotencyConflict", err)
	}
}

// TestService_Get_NotFound_5_4_1 — Get returns ErrTaskNotFound.
func TestService_Get_NotFound_5_4_1(t *testing.T) {
	svc, _ := newSvc(t)
	_, err := svc.Get(context.Background(), "nope")
	if !errors.Is(err, tasks.ErrTaskNotFound) {
		t.Fatalf("err = %v", err)
	}
}

// TestService_RoutingImmutable_7_4_1 — routing_content and routing_content_hash
// are persisted exactly as computed and never re-derived on Get.
func TestService_RoutingImmutable_7_4_1(t *testing.T) {
	svc, _ := newSvc(t)
	res, err := svc.Submit(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(context.Background(), res.Task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RoutingContentHash != res.Task.RoutingContentHash {
		t.Errorf("hash changed across reads")
	}
	if string(got.RoutingContent) != string(res.Task.RoutingContent) {
		t.Errorf("routing_content changed across reads")
	}
}

// TestService_List_PaginationDeterministic_5_4_2 — list returns most-recent
// first, with a stable cursor across pages.
func TestService_List_PaginationDeterministic_5_4_2(t *testing.T) {
	svc, st := newSvc(t)

	// Insert 5 tasks at distinct created_at to ensure deterministic order.
	for i := 0; i < 5; i++ {
		in := validInput()
		in.IdempotencyKey = "k-" + itoa(i)
		if _, err := svc.Submit(context.Background(), in); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		// Bump created_at directly via store so they sort.
		if _, err := st.DB().Exec(
			`UPDATE tasks SET created_at = ? WHERE task_id = (SELECT task_id FROM tasks ORDER BY rowid DESC LIMIT 1)`,
			time.Date(2025, 7, 3, 11, 0, i, 0, time.UTC),
		); err != nil {
			t.Fatal(err)
		}
	}

	page1, err := svc.List(context.Background(), store.ListFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1.Items) != 2 || !page1.HasMore {
		t.Fatalf("page1: items=%d hasMore=%v", len(page1.Items), page1.HasMore)
	}
	page2, err := svc.List(context.Background(), store.ListFilter{
		Limit:           2,
		CursorCreatedAt: page1.NextCreatedAt,
		CursorTaskID:    page1.NextTaskID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Items) != 2 {
		t.Fatalf("page2 items = %d", len(page2.Items))
	}
	// No overlap between pages.
	for _, a := range page1.Items {
		for _, b := range page2.Items {
			if a.TaskID == b.TaskID {
				t.Errorf("overlap: %s appears in both pages", a.TaskID)
			}
		}
	}
}

// --- TaskIDTimestamp policy tests ---

// newSvcWithNaming is like newSvc but applies a specific Naming policy.
func newSvcWithNaming(t *testing.T, n policy.Naming) (*tasks.Service, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "tasks.db")
	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(context.Background(), st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reg := stations.NewRegistry()
	if err := reg.Seed(context.Background(), st,
		stations.Spec{StationID: "SCENE-L2", StationName: "SCE_2", ContentHash: "sha256:scene-l2", SchemaVersion: "veriproc.station/v1"},
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	now := time.Date(2025, 7, 3, 11, 50, 0, 0, time.UTC)
	svc := tasks.NewService(st, reg, fixedClock(now), seqIDs("task-test"))
	svc.SetNaming(n)
	return svc, st
}

// TestService_TaskIDTimestamp_Creation — creation policy uses the creation
// timestamp in the task ID, not the window start.
func TestService_TaskIDTimestamp_Creation(t *testing.T) {
	n := policy.Naming{TaskIDTimestamp: policy.TaskIDTimestampCreation}
	svc, _ := newSvcWithNaming(t, n)
	// creation time = 2025-07-03T11:50:00Z → "20250703T115000000"
	// window start  = 2025-07-03T11:15:39Z → "20250703T111539000"
	res, err := svc.Submit(context.Background(), validInput())
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	// The task ID timestamp segment must contain the creation time prefix.
	if !strings.Contains(res.Task.TaskID, "20250703T115000") {
		t.Errorf("task_id %q does not contain creation timestamp segment 20250703T115000", res.Task.TaskID)
	}
	// created_at must be the creation time.
	wantCreated := time.Date(2025, 7, 3, 11, 50, 0, 0, time.UTC)
	if !res.Task.CreatedAt.Equal(wantCreated) {
		t.Errorf("created_at = %v, want %v", res.Task.CreatedAt, wantCreated)
	}
	// window_start must remain unchanged.
	wantWinStart := time.Date(2025, 7, 3, 11, 15, 39, 0, time.UTC)
	if !res.Task.WindowStart.Equal(wantWinStart) {
		t.Errorf("window_start = %v, want %v", res.Task.WindowStart, wantWinStart)
	}
}

// TestService_TaskIDTimestamp_Start — start policy uses the window start time
// in the task ID instead of the creation time.
func TestService_TaskIDTimestamp_Start(t *testing.T) {
	n := policy.Naming{TaskIDTimestamp: policy.TaskIDTimestampStart}
	svc, _ := newSvcWithNaming(t, n)
	// window start  = 2025-07-03T11:15:39Z → "20250703T111539000"
	// creation time = 2025-07-03T11:50:00Z → "20250703T115000000"
	res, err := svc.Submit(context.Background(), validInput())
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	// The task ID timestamp segment must contain the window start prefix.
	if !strings.Contains(res.Task.TaskID, "20250703T111539") {
		t.Errorf("task_id %q does not contain window-start timestamp segment 20250703T111539", res.Task.TaskID)
	}
	// created_at must still be the creation time, NOT the window start.
	wantCreated := time.Date(2025, 7, 3, 11, 50, 0, 0, time.UTC)
	if !res.Task.CreatedAt.Equal(wantCreated) {
		t.Errorf("created_at = %v, want %v (must remain creation time)", res.Task.CreatedAt, wantCreated)
	}
	// window_start must remain unchanged.
	wantWinStart := time.Date(2025, 7, 3, 11, 15, 39, 0, time.UTC)
	if !res.Task.WindowStart.Equal(wantWinStart) {
		t.Errorf("window_start = %v, want %v", res.Task.WindowStart, wantWinStart)
	}
}

// TestService_TaskIDTimestamp_Idempotency_Creation — idempotency replay returns
// the same task ID under the creation policy.
func TestService_TaskIDTimestamp_Idempotency_Creation(t *testing.T) {
	n := policy.Naming{TaskIDTimestamp: policy.TaskIDTimestampCreation}
	svc, _ := newSvcWithNaming(t, n)
	in := validInput()
	in.IdempotencyKey = "idem-creation"
	first, err := svc.Submit(context.Background(), in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := svc.Submit(context.Background(), in)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if second.Task.TaskID != first.Task.TaskID {
		t.Errorf("idempotency replay changed task_id: %s → %s", first.Task.TaskID, second.Task.TaskID)
	}
}

// TestService_TaskIDTimestamp_Idempotency_Start — idempotency replay returns
// the same task ID under the start policy.
func TestService_TaskIDTimestamp_Idempotency_Start(t *testing.T) {
	n := policy.Naming{TaskIDTimestamp: policy.TaskIDTimestampStart}
	svc, _ := newSvcWithNaming(t, n)
	in := validInput()
	in.IdempotencyKey = "idem-start"
	first, err := svc.Submit(context.Background(), in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := svc.Submit(context.Background(), in)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if second.Task.TaskID != first.Task.TaskID {
		t.Errorf("idempotency replay changed task_id: %s → %s", first.Task.TaskID, second.Task.TaskID)
	}
}
