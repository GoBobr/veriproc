// Package tasks implements the task-submission and query domain service
// (Spec §3.3, §5.3, §5.4).
//
// The Service is intentionally HTTP-agnostic: the REST handlers in
// internal/httpapi/tasks.go translate transport concerns (headers, JSON
// shape, status codes) into Service calls and back. Keeping the seam here
// means the same logic is exercised by both unit tests (this package) and
// HTTP contract tests (internal/httpapi).
package tasks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/eum/veriproc/internal/policy"
	"github.com/eum/veriproc/internal/stations"
	"github.com/eum/veriproc/internal/store"
)

// IdempotencyScope is the scope string used for task-submission idempotency
// records (Spec §5.3.5). Kept exported so tests and CLI may reference it.
const IdempotencyScope = "tasks.submit"

// Sentinel errors returned by the Service. Callers (HTTP handlers) translate
// these into apierr codes and statuses.
var (
	ErrInvalidRequest      = errors.New("tasks: invalid request")
	ErrUnknownStation      = errors.New("tasks: unknown station")
	ErrIdempotencyConflict = errors.New("tasks: idempotency conflict")
	ErrTaskNotFound        = errors.New("tasks: task not found")
)

// SubmitInput is the canonical, transport-independent submission payload.
type SubmitInput struct {
	SchemaVersion  string
	IdempotencyKey string
	Destination    Destination
	Window         Window
	Force          bool
	Priority       string
	Parent         *Parent
	SplitGroupID   string
	ClientMetadata map[string]any
	// History carries the pre-built routing-history chain that should be
	// embedded verbatim in the task's routing content. When non-nil each
	// element must be a map[string]any with at least a station_id and
	// run_ref key. Used by the group-complete notifier so that fan-in
	// tasks (e.g. aggregation) carry the full ancestral chain.
	History []any
	// TriggerID is an opaque string identifying the caller or upstream
	// entity that caused this task to be submitted. Used for logging only;
	// not persisted. Examples: remote IP for HTTP clients, group ID for
	// fan-in triggers, parent run_ref for programmatic submissions.
	TriggerID string
}

// Destination identifies a target station.
type Destination struct {
	StationID string `json:"station_id"`
}

// Window is the processing window.
type Window struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Parent references an upstream task and (optionally) the contributing run
// via task-scoped composite identity (task_id, retry_index). Spec §3.6.1.
type Parent struct {
	TaskID     string `json:"task_id,omitempty"`
	RetryIndex *int   `json:"retry_index,omitempty"`
	// RunRef is the readable form "<task_id>/r<retry_index>" provided for
	// presentation only; not durable identity.
	RunRef string `json:"run_ref,omitempty"`
}

// SubmitResult describes the outcome of a SubmitTask call.
type SubmitResult struct {
	Task    *store.TaskRecord
	Created bool // false if the call replayed an existing idempotency record
}

// Service implements the task-submission and query domain.
type Service struct {
	store          *store.Store
	stations       stations.Resolver
	now            func() time.Time
	idFactory      func() string
	naming         policy.Naming
	idempotencyTTL time.Duration
	logger         zerolog.Logger
}

// NewService constructs a Service. now and idFactory are injected for tests;
// pass nil to use real wall-clock time and a UUIDv7-derived id.
func NewService(s *store.Store, r stations.Resolver, now func() time.Time, idFactory func() string) *Service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{store: s, stations: r, now: now, idFactory: idFactory, naming: policy.DefaultNaming(), idempotencyTTL: 24 * time.Hour}
}

func (s *Service) SetNaming(n policy.Naming) { s.naming = n.WithDefaults() }

// SetLogger configures the logger used for task-lifecycle events.
func (s *Service) SetLogger(l zerolog.Logger) { s.logger = l }

// SetIdempotencyTTL overrides the default 24h retention window for newly
// inserted idempotency_records (Spec §3.3 retention). A non-positive value
// disables the TTL (records are kept indefinitely until explicitly swept).
func (s *Service) SetIdempotencyTTL(d time.Duration) { s.idempotencyTTL = d }

// IdempotencyTTL reports the currently configured retention window.
func (s *Service) IdempotencyTTL() time.Duration { return s.idempotencyTTL }

func (s *Service) newTaskID(now time.Time, stationID string, windowStart time.Time) string {
	hex6 := randomHex6()
	if s.idFactory != nil {
		hex6 = s.idFactory()
	}
	ts := now
	if s.naming.TaskIDTimestamp == policy.TaskIDTimestampStart {
		ts = windowStart
	}
	return policy.GenerateTaskID(stationID, ts, hex6)
}

func randomHex6() string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "000000"
	}
	return hex.EncodeToString(b[:])
}

// Submit validates the request, applies idempotency, and persists a new task
// (or replays an existing one). The returned error is one of the package-level
// sentinel errors so handlers can translate to apierr codes.
func (s *Service) Submit(ctx context.Context, in SubmitInput) (*SubmitResult, error) {
	if err := validateSubmit(in); err != nil {
		return nil, err
	}

	rev, err := s.stations.Resolve(ctx, in.Destination.StationID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnknownStation, err.Error())
	}

	routing, err := canonicalRouting(in, rev)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidRequest, err.Error())
	}
	hash := sha256Hex(routing)

	// Idempotency: if a record exists for the (scope, key), either replay or
	// report a conflict depending on whether the canonical hash matches.
	if in.IdempotencyKey != "" {
		existing, err := s.store.Idempotency().GetByKey(ctx, IdempotencyScope, in.IdempotencyKey)
		if err == nil {
			if existing.RequestHash != hash {
				return nil, fmt.Errorf("%w: key=%s", ErrIdempotencyConflict, in.IdempotencyKey)
			}
			if existing.TaskID == "" {
				// Should not happen, but treat as conflict for safety.
				return nil, fmt.Errorf("%w: key=%s (stranded record)", ErrIdempotencyConflict, in.IdempotencyKey)
			}
			t, err := s.store.Tasks().Get(ctx, existing.TaskID)
			if err != nil {
				return nil, err
			}
			return &SubmitResult{Task: t, Created: false}, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}

	now := s.now().UTC()
	taskID := s.newTaskID(now, rev.StationID, in.Window.Start)
	if err := policy.ValidateTaskID(taskID, rev.StationID); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidRequest, err.Error())
	}

	task := &store.TaskRecord{
		TaskID:               taskID,
		SchemaVersion:        defaultSchemaVersion(in.SchemaVersion),
		DestinationStationID: rev.StationID,
		WindowStart:          in.Window.Start.UTC(),
		WindowEnd:            in.Window.End.UTC(),
		Force:                in.Force,
		Priority:             in.Priority,
		ClientMetadata:       marshalMeta(in.ClientMetadata),
		RoutingContent:       routing,
		RoutingContentHash:   hash,
		SubmissionOrigin:     "client",
		State:                "accepted",
		CreatedAt:            now,
	}
	if in.Parent != nil {
		task.ParentTaskID = in.Parent.TaskID
		if in.Parent.RetryIndex != nil {
			task.ParentRunRetryIndex = sql.NullInt64{Int64: int64(*in.Parent.RetryIndex), Valid: true}
		}
	}
	if in.SplitGroupID != "" {
		task.SplitGroupID = in.SplitGroupID
	}

	// Persist task + idempotency record atomically.
	err = s.store.InTx(ctx, func(tx *store.Tx) error {
		if in.IdempotencyKey != "" {
			rec := &store.IdempotencyRecord{
				ID:          "ir-" + taskID,
				Scope:       IdempotencyScope,
				Key:         in.IdempotencyKey,
				RequestHash: hash,
				TaskID:      taskID,
				CreatedAt:   now,
			}
			if s.idempotencyTTL > 0 {
				rec.ExpiresAt = sql.NullTime{Time: now.Add(s.idempotencyTTL).UTC(), Valid: true}
			}
			if err := tx.Idempotency().Insert(ctx, rec); err != nil {
				return err
			}
			task.IdempotencyRecordID = rec.ID
		}
		return tx.Tasks().Insert(ctx, task)
	})
	if err != nil {
		// A racing submit with the same key may surface as ErrConflict here.
		if errors.Is(err, store.ErrConflict) {
			return nil, fmt.Errorf("%w: %s", ErrIdempotencyConflict, err.Error())
		}
		return nil, err
	}
	trigger := in.TriggerID
	if trigger == "" && in.Parent != nil {
		if in.Parent.RunRef != "" {
			trigger = in.Parent.RunRef
		} else if in.Parent.TaskID != "" {
			trigger = "task:" + in.Parent.TaskID
		}
	}
	if trigger == "" {
		trigger = "client"
	}
	s.logger.Info().
		Str("task_id", task.TaskID).
		Str("station", task.DestinationStationID).
		Time("window_start", task.WindowStart).
		Time("window_end", task.WindowEnd).
		Str("trigger", trigger).
		Msg("task submitted")
	return &SubmitResult{Task: task, Created: true}, nil
}

// Get returns a task by id.
func (s *Service) Get(ctx context.Context, taskID string) (*store.TaskRecord, error) {
	t, err := s.store.Tasks().Get(ctx, taskID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrTaskNotFound
	}
	return t, err
}

// List returns one page of tasks. The filter is passed through unchanged so
// the handler may translate REST query parameters directly.
func (s *Service) List(ctx context.Context, f store.ListFilter) (*store.ListPage, error) {
	return s.store.Tasks().List(ctx, f)
}

// validateSubmit enforces the submission rules from Spec §5.3.1–§5.3.4.
func validateSubmit(in SubmitInput) error {
	var fields []string
	if strings.TrimSpace(in.Destination.StationID) == "" {
		fields = append(fields, "destination.station_id required")
	}
	if in.Window.Start.IsZero() || in.Window.End.IsZero() {
		fields = append(fields, "window: start and end required")
	} else if in.Window.End.Before(in.Window.Start) {
		fields = append(fields, "window: end must be >= start")
	}
	if len(fields) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidRequest, strings.Join(fields, "; "))
	}
	return nil
}

// canonicalRouting produces the deterministic JSON used for hashing and
// persisted as routing_content (Spec §3.3 immutability + §7.4.5 replay
// equivalence). Map keys are sorted; timestamps normalized to UTC.
func canonicalRouting(in SubmitInput, rev *store.StationRevisionRecord) (json.RawMessage, error) {
	m := map[string]any{
		"schema_version": defaultSchemaVersion(in.SchemaVersion),
		"destination": map[string]any{
			"station_id": rev.StationID,
		},
		"window": map[string]any{
			"start": in.Window.Start.UTC().Format(time.RFC3339Nano),
			"end":   in.Window.End.UTC().Format(time.RFC3339Nano),
		},
		"force": in.Force,
	}
	if in.Priority != "" {
		m["priority"] = in.Priority
	}
	if in.Parent != nil && (in.Parent.TaskID != "" || in.Parent.RetryIndex != nil || in.Parent.RunRef != "") {
		p := map[string]any{}
		if in.Parent.TaskID != "" {
			p["task_id"] = in.Parent.TaskID
		}
		if in.Parent.RetryIndex != nil {
			p["retry_index"] = *in.Parent.RetryIndex
		}
		// Include run_ref so task.yaml can show the parent hop directly.
		if in.Parent.RunRef != "" {
			p["run_ref"] = in.Parent.RunRef
		}
		m["parent"] = p
	}
	if len(in.History) > 0 {
		m["history"] = in.History
	}
	if len(in.ClientMetadata) > 0 {
		m["client_metadata"] = in.ClientMetadata
	}
	return canonicalJSON(m)
}

// canonicalJSON returns a deterministic JSON encoding of v. Object keys are
// sorted recursively. Numbers are rendered by encoding/json (no custom
// formatting). Suitable for SHA-256 hashing.
func canonicalJSON(v any) (json.RawMessage, error) {
	var b strings.Builder
	if err := writeCanonical(&b, v); err != nil {
		return nil, err
	}
	return json.RawMessage(b.String()), nil
}

func writeCanonical(b *strings.Builder, v any) error {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			kj, _ := json.Marshal(k)
			b.Write(kj)
			b.WriteByte(':')
			if err := writeCanonical(b, x[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeCanonical(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		b.Write(raw)
	}
	return nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func marshalMeta(m map[string]any) json.RawMessage {
	if len(m) == 0 {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return json.RawMessage(b)
}

func defaultSchemaVersion(v string) string {
	if v == "" {
		return "veriproc.task-submission/v1"
	}
	return v
}
