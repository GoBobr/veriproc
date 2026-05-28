package console

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// writeJSON encodes body to w with the supplied status.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeErr writes an error envelope compatible with the upstream apierr
// shape so the frontend can render messages uniformly.
func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": msg,
		},
	})
}

// handleListInstances returns the configured instances. Tokens and other
// secret references are stripped.
func (g *Gateway) handleListInstances(w http.ResponseWriter, _ *http.Request) {
	items := make([]map[string]any, 0, len(g.cfg.Instances))
	for _, inst := range g.cfg.Instances {
		items = append(items, map[string]any{
			"id":                  inst.ID,
			"title":               inst.Title,
			"base_url":            inst.BaseURL,
			"working_root_base":   inst.WorkingRootBase,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"ui": map[string]any{
			"refresh_interval_ms":          g.cfg.UI.RefreshInterval.Milliseconds(),
			"visible_slot_count":           g.cfg.UI.VisibleSlotCount,
			"completed_visibility_ms":      g.cfg.UI.CompletedVisibility.Milliseconds(),
			"default_stats_since_ms":       g.cfg.UI.DefaultStatsSince.Milliseconds(),
			"preview_max_bytes":            g.cfg.UI.PreviewMaxBytes,
		},
	})
}

func (g *Gateway) parseSince(r *http.Request) time.Time {
	if v := r.URL.Query().Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t.UTC()
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t.UTC()
		}
	}
	return g.now().Add(-g.cfg.UI.DefaultStatsSince).UTC()
}

func (g *Gateway) handleDashboard(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("instance_id")
	if _, ok := g.instance(instanceID); !ok {
		writeErr(w, http.StatusNotFound, "unknown_instance", "instance not configured")
		return
	}
	since := g.parseSince(r)
	view := g.BuildInstanceDashboard(r.Context(), instanceID, since)
	writeJSON(w, http.StatusOK, view)
}

func (g *Gateway) handlePauseStation(w http.ResponseWriter, r *http.Request) {
	g.stationControl(w, r, "pause", func(inst *upstreamInstance, stationID string) (map[string]any, error) {
		return inst.client.PauseStation(r.Context(), stationID)
	})
}

func (g *Gateway) handleUnpauseStation(w http.ResponseWriter, r *http.Request) {
	g.stationControl(w, r, "unpause", func(inst *upstreamInstance, stationID string) (map[string]any, error) {
		return inst.client.UnpauseStation(r.Context(), stationID)
	})
}

func (g *Gateway) stationControl(w http.ResponseWriter, r *http.Request, action string, op func(*upstreamInstance, string) (map[string]any, error)) {
	instanceID := r.PathValue("instance_id")
	stationID := r.PathValue("station_id")
	inst, ok := g.instance(instanceID)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown_instance", "instance not configured")
		return
	}
	p, _ := PrincipalFromContext(r.Context())
	out, err := op(inst, stationID)
	if err != nil {
		g.audit(r.Context(), AuditEntry{
			Subject: p.Subject, InstanceID: instanceID, StationID: stationID,
			Action: action, Status: "error", Message: err.Error(),
		})
		writeErr(w, httpStatusFromUpstream(err), "upstream_failed", err.Error())
		return
	}
	g.audit(r.Context(), AuditEntry{
		Subject: p.Subject, InstanceID: instanceID, StationID: stationID, Action: action, Status: "ok",
	})
	writeJSON(w, http.StatusOK, out)
}

// submitRequest is the request body accepted by the station submit endpoint.
type submitRequest struct {
	Start   string         `json:"start"`
	End     string         `json:"end"`
	Force   bool           `json:"force"`
	Inputs  map[string]any `json:"inputs,omitempty"`
	Client  map[string]any `json:"client,omitempty"`
}

func (g *Gateway) handleSubmit(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("instance_id")
	stationID := r.PathValue("station_id")
	inst, ok := g.instance(instanceID)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown_instance", "instance not configured")
		return
	}
	var body submitRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	start, err := time.Parse(time.RFC3339, body.Start)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "invalid start time (RFC3339 required)")
		return
	}
	end, err := time.Parse(time.RFC3339, body.End)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "invalid end time (RFC3339 required)")
		return
	}
	if !end.After(start) {
		writeErr(w, http.StatusBadRequest, "invalid_request", "end must be after start")
		return
	}
	payload := map[string]any{
		"station_id": stationID,
		"start":      start.UTC().Format(time.RFC3339Nano),
		"end":        end.UTC().Format(time.RFC3339Nano),
		"force":      body.Force,
	}
	if body.Inputs != nil {
		payload["inputs"] = body.Inputs
	}
	if body.Client != nil {
		payload["client"] = body.Client
	}
	p, _ := PrincipalFromContext(r.Context())
	out, err := inst.client.SubmitTask(r.Context(), payload)
	if err != nil {
		g.audit(r.Context(), AuditEntry{
			Subject: p.Subject, InstanceID: instanceID, StationID: stationID,
			Action: "submit", Status: "error", Message: err.Error(),
		})
		writeErr(w, httpStatusFromUpstream(err), "upstream_failed", err.Error())
		return
	}
	g.audit(r.Context(), AuditEntry{
		Subject: p.Subject, InstanceID: instanceID, StationID: stationID,
		Action: "submit", Status: "ok",
	})
	writeJSON(w, http.StatusAccepted, out)
}

func (g *Gateway) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("instance_id")
	taskID := r.PathValue("task_id")
	retryIndex, err := strconv.Atoi(r.PathValue("retry_index"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "retry_index must be integer")
		return
	}
	inst, ok := g.instance(instanceID)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown_instance", "instance not configured")
		return
	}
	p, _ := PrincipalFromContext(r.Context())
	runID, err := g.resolveUpstreamRunID(r.Context(), instanceID, taskID, retryIndex)
	if err != nil || runID == "" {
		g.audit(r.Context(), AuditEntry{Subject: p.Subject, InstanceID: instanceID, TaskID: taskID, RetryIndex: &retryIndex, Action: "cancel", Status: "error", Message: errString(err)})
		writeErr(w, http.StatusNotFound, "not_found", "no run for task/retry")
		return
	}
	out, err := inst.client.CancelRun(r.Context(), runID, nil)
	if err != nil {
		g.audit(r.Context(), AuditEntry{Subject: p.Subject, InstanceID: instanceID, TaskID: taskID, RetryIndex: &retryIndex, Action: "cancel", Status: "error", Message: err.Error()})
		writeErr(w, httpStatusFromUpstream(err), "upstream_failed", err.Error())
		return
	}
	g.audit(r.Context(), AuditEntry{Subject: p.Subject, InstanceID: instanceID, TaskID: taskID, RetryIndex: &retryIndex, Action: "cancel", Status: "ok"})
	writeJSON(w, http.StatusOK, out)
}

func (g *Gateway) handleHideRun(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("instance_id")
	taskID := r.PathValue("task_id")
	retryIndex, err := strconv.Atoi(r.PathValue("retry_index"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "retry_index must be integer")
		return
	}
	if _, ok := g.instance(instanceID); !ok {
		writeErr(w, http.StatusNotFound, "unknown_instance", "instance not configured")
		return
	}
	// We need the station id for the hidden-run key. Look it up from the task.
	stationID := r.URL.Query().Get("station_id")
	if stationID == "" {
		// Best-effort: fetch from upstream task.
		inst, _ := g.instance(instanceID)
		if t, err := inst.client.GetTask(r.Context(), taskID); err == nil {
			if s, ok := t["station_id"].(string); ok {
				stationID = s
			}
		}
	}
	if stationID == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request", "station_id is required (query param)")
		return
	}
	p, _ := PrincipalFromContext(r.Context())
	key := HiddenRunKey{InstanceID: instanceID, StationID: stationID, TaskID: taskID, RetryIndex: retryIndex}
	if err := g.db.HideRun(r.Context(), key, p.Subject, g.now()); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	g.audit(r.Context(), AuditEntry{Subject: p.Subject, InstanceID: instanceID, StationID: stationID, TaskID: taskID, RetryIndex: &retryIndex, Action: "hide", Status: "ok"})
	writeJSON(w, http.StatusOK, map[string]any{"hidden": true, "task_id": taskID, "retry_index": retryIndex})
}

func (g *Gateway) handleRetryTask(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("instance_id")
	taskID := r.PathValue("task_id")
	inst, ok := g.instance(instanceID)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown_instance", "instance not configured")
		return
	}
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	}
	p, _ := PrincipalFromContext(r.Context())
	out, err := inst.client.RetryTask(r.Context(), taskID, body)
	if err != nil {
		g.audit(r.Context(), AuditEntry{Subject: p.Subject, InstanceID: instanceID, TaskID: taskID, Action: "retry", Status: "error", Message: err.Error()})
		writeErr(w, httpStatusFromUpstream(err), "upstream_failed", err.Error())
		return
	}
	g.audit(r.Context(), AuditEntry{Subject: p.Subject, InstanceID: instanceID, TaskID: taskID, Action: "retry", Status: "ok"})
	writeJSON(w, http.StatusAccepted, out)
}

func (g *Gateway) handleGetTask(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("instance_id")
	taskID := r.PathValue("task_id")
	inst, ok := g.instance(instanceID)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown_instance", "instance not configured")
		return
	}
	out, err := inst.client.GetTask(r.Context(), taskID)
	if err != nil {
		writeErr(w, httpStatusFromUpstream(err), "upstream_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (g *Gateway) handleTaskRuns(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("instance_id")
	taskID := r.PathValue("task_id")
	inst, ok := g.instance(instanceID)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown_instance", "instance not configured")
		return
	}
	runs, err := inst.client.ListTaskRuns(r.Context(), taskID)
	if err != nil {
		writeErr(w, httpStatusFromUpstream(err), "upstream_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": runs})
}

// handleRunTree returns one directory listing for a run working root.
func (g *Gateway) handleRunTree(w http.ResponseWriter, r *http.Request) {
	wr, rel, ok := g.openRunWorkingRoot(w, r)
	if !ok {
		return
	}
	out, err := ListTree(wr, rel)
	if err != nil {
		switch {
		case errors.Is(err, ErrPathEscape) || errors.Is(err, ErrSymlinkEsc):
			writeErr(w, http.StatusBadRequest, "path_escape", err.Error())
		case errors.Is(err, ErrNotFoundFS):
			writeErr(w, http.StatusNotFound, "not_found", "directory not found")
		default:
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRunFile previews one file inside a run working root.
func (g *Gateway) handleRunFile(w http.ResponseWriter, r *http.Request) {
	wr, _, ok := g.openRunWorkingRoot(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request", "path query param required")
		return
	}
	out, err := PreviewFile(wr, rel, g.cfg.UI.PreviewMaxBytes)
	if err != nil {
		switch {
		case errors.Is(err, ErrPathEscape) || errors.Is(err, ErrSymlinkEsc):
			writeErr(w, http.StatusBadRequest, "path_escape", err.Error())
		case errors.Is(err, ErrNotFoundFS):
			writeErr(w, http.StatusNotFound, "not_found", "file not found")
		case errors.Is(err, ErrIsDirectory):
			writeErr(w, http.StatusBadRequest, "is_directory", "target is a directory")
		default:
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// openRunWorkingRoot resolves the upstream run's working_root and validates
// it against the configured allowed roots. It returns (workingRoot, rel,
// true) on success. On failure it has already written an error response.
func (g *Gateway) openRunWorkingRoot(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	instanceID := r.PathValue("instance_id")
	taskID := r.PathValue("task_id")
	retryIndex, err := strconv.Atoi(r.PathValue("retry_index"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "retry_index must be integer")
		return "", "", false
	}
	inst, ok := g.instance(instanceID)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown_instance", "instance not configured")
		return "", "", false
	}
	run, err := ResolveRunByTaskAndRetry(r.Context(), inst.client, taskID, retryIndex)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", err.Error())
		return "", "", false
	}
	wr, _ := run["working_root"].(string)
	if wr == "" {
		writeErr(w, http.StatusBadRequest, "invalid_state", "run has no working_root")
		return "", "", false
	}
	abs, err := ValidateWorkingRoot(inst.cfg.AllowedRoots, wr)
	if err != nil {
		writeErr(w, http.StatusForbidden, "forbidden", "working root not in allowed bases")
		return "", "", false
	}
	rel := strings.TrimPrefix(r.URL.Query().Get("path"), "/")
	if r.URL.Path != "" && strings.HasSuffix(r.URL.Path, "/tree") {
		// tree endpoint uses ?path= for the relative subpath.
		rel = strings.TrimPrefix(r.URL.Query().Get("path"), "/")
	}
	return abs, rel, true
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
