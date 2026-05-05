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
	"github.com/eum/veriproc/internal/store"
	"github.com/eum/veriproc/internal/tasks"
)

// taskHandler bundles the task service. It is mounted under /api/v1/tasks
// (Spec §5.3, §5.4).
type taskHandler struct {
	svc *tasks.Service
}

// --- Wire types (transport-only; do not leak store types to clients) ---

type submitRequest struct {
	SchemaVersion  string                 `json:"schema_version,omitempty"`
	IdempotencyKey string                 `json:"idempotency_key,omitempty"`
	Destination    tasks.Destination      `json:"destination"`
	Window         tasks.Window           `json:"window"`
	Force          bool                   `json:"force,omitempty"`
	Priority       string                 `json:"priority,omitempty"`
	Parent         *tasks.Parent          `json:"parent,omitempty"`
	SplitGroupID   string                 `json:"split_group_id,omitempty"`
	ClientMetadata map[string]any         `json:"client_metadata,omitempty"`
}

type taskWire struct {
	TaskID         string            `json:"task_id"`
	Destination    tasks.Destination `json:"destination"`
	Window         tasks.Window      `json:"window"`
	Force          bool              `json:"force"`
	State          string            `json:"state"`
	CreatedAt      time.Time         `json:"created_at"`
	LatestRunID    *string           `json:"latest_run_id"`
	CanonicalRunID *string           `json:"canonical_run_id"`
	Parent         *tasks.Parent     `json:"parent,omitempty"`
	SplitGroupID   string            `json:"split_group_id,omitempty"`
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

	in := tasks.SubmitInput{
		SchemaVersion:  req.SchemaVersion,
		IdempotencyKey: key,
		Destination:    req.Destination,
		Window:         req.Window,
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
		DestinationProcType:  q.Get("proc_type"),
		State:                q.Get("state"),
		ParentTaskID:         q.Get("parent_task_id"),
		ParentRunID:          q.Get("parent_run_id"),
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
		TaskID: t.TaskID,
		Destination: tasks.Destination{
			StationID: t.DestinationStationID,
			ProcType:  t.DestinationProcType,
		},
		Window: tasks.Window{
			Start: t.WindowStart.UTC(),
			End:   t.WindowEnd.UTC(),
		},
		Force:     t.Force,
		State:     t.State,
		CreatedAt: t.CreatedAt.UTC(),
	}
	if t.LatestRunID != "" {
		v := t.LatestRunID
		w.LatestRunID = &v
	}
	if t.CanonicalRunID != "" {
		v := t.CanonicalRunID
		w.CanonicalRunID = &v
	}
	if t.ParentTaskID != "" || t.ParentRunID != "" {
		w.Parent = &tasks.Parent{TaskID: t.ParentTaskID, RunID: t.ParentRunID}
	}
	if t.SplitGroupID != "" {
		w.SplitGroupID = t.SplitGroupID
	}
	return w
}

func linksFor(taskID string) map[string]string {
	return map[string]string{
		"self": "/api/v1/tasks/" + taskID,
		"runs": "/api/v1/runs?task_id=" + taskID,
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
