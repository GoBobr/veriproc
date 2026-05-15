package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eum/veriproc/internal/httpapi/apierr"
	"github.com/eum/veriproc/internal/policy"
	"github.com/eum/veriproc/internal/store"
	"github.com/eum/veriproc/internal/tasks"
)

// taskHandler bundles the task service. It is mounted under /api/v1/tasks
// (Spec §5.3, §5.4).
type taskHandler struct {
	svc *tasks.Service
}

// --- Wire types (transport-only; do not leak store types to clients) ---

// windowSubmitWire accepts window timestamps as strings so the API can
// support RFC 3339 and compact UTC forms (Spec §5.3.1).
type windowSubmitWire struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type submitRequest struct {
	SchemaVersion  string            `json:"schema_version,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	Destination    tasks.Destination `json:"destination"`
	Window         windowSubmitWire  `json:"window"`
	Force          bool              `json:"force,omitempty"`
	Priority       string            `json:"priority,omitempty"`
	Parent         *tasks.Parent     `json:"parent,omitempty"`
	SplitGroupID   string            `json:"split_group_id,omitempty"`
	ClientMetadata map[string]any    `json:"client_metadata,omitempty"`
}

type taskWire struct {
	TaskID              string            `json:"task_id"`
	StationID           string            `json:"station_id,omitempty"`
	Start               time.Time         `json:"start"`
	End                 time.Time         `json:"end"`
	Destination         tasks.Destination `json:"destination"`
	Window              tasks.Window      `json:"window"`
	Force               bool              `json:"force"`
	State               string            `json:"state"`
	FailureSummary      string            `json:"failure_summary,omitempty"`
	CreatedAt           time.Time         `json:"created_at"`
	LatestRetryIndex    *int64            `json:"latest_retry_index"`
	LatestRunRef        *string           `json:"latest_run_ref"`
	CanonicalRetryIndex *int64            `json:"canonical_retry_index"`
	CanonicalRunRef     *string           `json:"canonical_run_ref"`
	Parent              *tasks.Parent     `json:"parent,omitempty"`
	SplitGroupID        string            `json:"split_group_id,omitempty"`
}

type submitResponse struct {
	Task  taskWire          `json:"task"`
	Links map[string]string `json:"links"`
}

type listResponse struct {
	Items      []taskWire `json:"items"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

// ServeHTTP routes the request among the supported sub-paths. Method-aware
// dispatch is handled by the parent mux; this handler only sees GET/POST.
func (h *taskHandler) submit(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil {
		apierr.Write(w, r, http.StatusBadRequest, apierr.CodeInvalidRequest, "empty request body")
		return
	}
	defer r.Body.Close()

	var req submitRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		apierr.Write(w, r, http.StatusBadRequest, apierr.CodeInvalidRequest, "malformed JSON: "+err.Error())
		return
	}

	// Idempotency-Key header takes precedence over body field (Spec §5.3.5
	// "may be supplied in a header, request field, or both").
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		key = req.IdempotencyKey
	}

	// Parse window timestamps; accept RFC 3339 and compact UTC forms (Spec §5.3.1).
	winStart, err := policy.ParseWindowTimestamp(req.Window.Start)
	if err != nil {
		apierr.Write(w, r, http.StatusBadRequest, apierr.CodeInvalidRequest, "window.start: "+err.Error())
		return
	}
	winEnd, err := policy.ParseWindowTimestamp(req.Window.End)
	if err != nil {
		apierr.Write(w, r, http.StatusBadRequest, apierr.CodeInvalidRequest, "window.end: "+err.Error())
		return
	}

	in := tasks.SubmitInput{
		SchemaVersion:  req.SchemaVersion,
		IdempotencyKey: key,
		Destination:    req.Destination,
		Window:         tasks.Window{Start: winStart, End: winEnd},
		Force:          req.Force,
		Priority:       req.Priority,
		Parent:         req.Parent,
		SplitGroupID:   req.SplitGroupID,
		ClientMetadata: req.ClientMetadata,
	}
	res, err := h.svc.Submit(r.Context(), in)
	if err != nil {
		writeTaskErr(w, r, err)
		return
	}
	status := http.StatusCreated
	if !res.Created {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(submitResponse{
		Task:  toWire(res.Task),
		Links: linksFor(res.Task.TaskID),
	})
}

func (h *taskHandler) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("task_id")
	t, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeTaskErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(toWire(t))
}

func (h *taskHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ListFilter{
		DestinationStationID: q.Get("station_id"),
		State:                q.Get("state"),
		ParentTaskID:         q.Get("parent_task_id"),
		SplitGroupID:         q.Get("split_group_id"),
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = n
		}
	}
	if v := q.Get("force"); v != "" {
		b := v == "true" || v == "1"
		f.Force = &b
	}
	if v := q.Get("cursor"); v != "" {
		ts, id, err := decodeCursor(v)
		if err != nil {
			apierr.Write(w, r, http.StatusBadRequest, apierr.CodeInvalidRequest, "invalid cursor")
			return
		}
		f.CursorCreatedAt = ts
		f.CursorTaskID = id
	}

	page, err := h.svc.List(r.Context(), f)
	if err != nil {
		writeTaskErr(w, r, err)
		return
	}
	resp := listResponse{Items: make([]taskWire, 0, len(page.Items))}
	for _, t := range page.Items {
		resp.Items = append(resp.Items, toWire(t))
	}
	if page.HasMore {
		resp.NextCursor = encodeCursor(page.NextCreatedAt, page.NextTaskID)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// writeTaskErr maps tasks.* sentinels to apierr codes (Spec §5.7).
func writeTaskErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, tasks.ErrInvalidRequest):
		apierr.Write(w, r, http.StatusBadRequest, apierr.CodeInvalidRequest, err.Error())
	case errors.Is(err, tasks.ErrUnknownStation):
		apierr.Write(w, r, http.StatusBadRequest, apierr.CodeUnknownStation, err.Error())
	case errors.Is(err, tasks.ErrIdempotencyConflict):
		apierr.Write(w, r, http.StatusConflict, apierr.CodeIdempotencyConflict, err.Error())
	case errors.Is(err, tasks.ErrTaskNotFound):
		apierr.Write(w, r, http.StatusNotFound, apierr.CodeNotFound, "task not found")
	default:
		apierr.Write(w, r, http.StatusInternalServerError, apierr.CodeInternal, "internal error")
	}
}

func toWire(t *store.TaskRecord) taskWire {
	w := taskWire{
		TaskID:    t.TaskID,
		StationID: t.DestinationStationID,
		Start:     t.WindowStart.UTC(),
		End:       t.WindowEnd.UTC(),
		Destination: tasks.Destination{
			StationID: t.DestinationStationID,
		},
		Window: tasks.Window{
			Start: t.WindowStart.UTC(),
			End:   t.WindowEnd.UTC(),
		},
		Force:          t.Force,
		State:          t.State,
		FailureSummary: t.FailureSummary,
		CreatedAt:      t.CreatedAt.UTC(),
	}
	if t.LatestRetryIndex.Valid {
		v := t.LatestRetryIndex.Int64
		w.LatestRetryIndex = &v
		ref := runRef(t.TaskID, int(v))
		w.LatestRunRef = &ref
	}
	if t.CanonicalRetryIndex.Valid {
		v := t.CanonicalRetryIndex.Int64
		w.CanonicalRetryIndex = &v
		ref := runRef(t.TaskID, int(v))
		w.CanonicalRunRef = &ref
	}
	if t.ParentTaskID != "" || t.ParentRunRetryIndex.Valid {
		p := &tasks.Parent{TaskID: t.ParentTaskID}
		if t.ParentRunRetryIndex.Valid {
			ri := int(t.ParentRunRetryIndex.Int64)
			p.RetryIndex = &ri
			p.RunRef = runRef(t.ParentTaskID, ri)
		}
		w.Parent = p
	}
	if t.SplitGroupID != "" {
		w.SplitGroupID = t.SplitGroupID
	}
	return w
}

func runRef(taskID string, retryIndex int) string {
	return taskID + "/r" + strconv.Itoa(retryIndex)
}

func linksFor(taskID string) map[string]string {
	return map[string]string{
		"self": "/api/v1/tasks/" + taskID,
		"runs": "/api/v1/tasks/" + taskID + "/runs",
	}
}

func encodeCursor(ts time.Time, id string) string {
	raw := ts.UTC().Format(time.RFC3339Nano) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(s string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, "", err
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", errors.New("malformed cursor")
	}
	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", err
	}
	return ts, parts[1], nil
}

// _ ensures context is referenced even on builds that prune unused imports.
var _ = context.Background
