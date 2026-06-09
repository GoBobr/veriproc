package httpapi

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gobobr/veriproc/internal/httpapi/apierr"
	"github.com/gobobr/veriproc/internal/runs"
	"github.com/gobobr/veriproc/internal/store"
)

var errInvalidRunListRequest = errors.New("httpapi: invalid run list request")

// runHandler exposes the run/job/artifact/log read endpoints (Spec §5.4.3,
// §5.4.4, §5.4.5, §5.4.6, §5.4.7).
type runHandler struct {
	svc *runs.Service
}

// --- Wire types -------------------------------------------------------------

type runWire struct {
	RunID                   string     `json:"run_id"`
	RunRef                  string     `json:"run_ref"`
	TaskID                  string     `json:"task_id"`
	StationID               string     `json:"station_id,omitempty"`
	Start                   time.Time  `json:"start,omitempty"`
	End                     time.Time  `json:"end"`
	StationRevisionID       string     `json:"station_revision_id"`
	State                   string     `json:"state"`          // simplified public state
	InternalState           string     `json:"internal_state"` // raw lifecycle state
	Canonicality            string     `json:"canonicality"`
	RetryIndex              int        `json:"retry_index"`
	ExecutorType            string     `json:"executor_type,omitempty"`
	ExecutionNode           string     `json:"execution_node,omitempty"`
	ElapsedTime             string     `json:"elapsed_time,omitempty"`
	WorkingRoot             string     `json:"working_root,omitempty"`
	ProcessingFingerprint   string     `json:"processing_fingerprint,omitempty"`
	FailureReason           string     `json:"failure_reason,omitempty"`
	FailureSummary          string     `json:"failure_summary,omitempty"`
	CreatedAt               time.Time  `json:"created_at"`
	PreparedAt              *time.Time `json:"prepared_at,omitempty"`
	DispatchedAt            *time.Time `json:"dispatched_at,omitempty"`
	StartedAt               *time.Time `json:"started_at,omitempty"`
	TerminalAt              *time.Time `json:"terminal_at,omitempty"`
	CancellationRequestedAt *time.Time `json:"cancellation_requested_at,omitempty"`
}

type runDetailWire struct {
	runWire
	ActiveJob     *jobWire       `json:"active_job,omitempty"`
	ExecutorType  string         `json:"executor_type,omitempty"`
	ExecutionNode string         `json:"execution_node,omitempty"`
	Jobs          []jobWire      `json:"jobs"`
	Artifacts     []artifactWire `json:"artifacts"`
}

type jobWire struct {
	JobID             string     `json:"job_id"`
	RunID             string     `json:"run_id"`
	ExecutorType      string     `json:"executor_type"`
	SchedulerID       string     `json:"scheduler_id,omitempty"`
	SchedulerState    string     `json:"scheduler_native_state,omitempty"`
	ExecutionNode     string     `json:"execution_node,omitempty"`
	ElapsedTime       string     `json:"elapsed_time,omitempty"`
	SubmissionAttempt int        `json:"submission_attempt"`
	SubmittedAt       *time.Time `json:"submitted_at,omitempty"`
	LastObservedAt    *time.Time `json:"last_observed_at,omitempty"`
	TerminalAt        *time.Time `json:"terminal_at,omitempty"`
}

type artifactWire struct {
	ArtifactID         string              `json:"artifact_id"`
	ProducingRunID     string              `json:"producing_run_id"`
	LogicalType        string              `json:"logical_type"`
	ObjectKind         string              `json:"object_kind"`
	FileType           string              `json:"file_type,omitempty"`
	Path               string              `json:"path,omitempty"`
	Size               int64               `json:"size,omitempty"`
	Checksum           string              `json:"checksum,omitempty"`
	ChecksumAlgo       string              `json:"checksum_algorithm,omitempty"`
	ChecksumSource     string              `json:"checksum_source,omitempty"`
	ValidationStatus   string              `json:"validation_status,omitempty"`
	Availability       string              `json:"availability"`
	CreatedAt          time.Time           `json:"created_at"`
	PublicationSummary *publicationSummary `json:"publication_summary,omitempty"`
}

// publicationSummary surfaces rolling-archive publication state on artifacts
// (Spec §5.5.6).
type publicationSummary struct {
	Publications   []publicationWire `json:"publications"`
	PublishedCount int               `json:"published_count"`
	PendingCount   int               `json:"pending_count"`
	FailedCount    int               `json:"failed_count"`
}

type publicationWire struct {
	PublicationID    string     `json:"publication_id"`
	ArchiveID        string     `json:"archive_id"`
	TargetPath       string     `json:"target_path"`
	ObjectKind       string     `json:"object_kind"`
	PublicationMode  string     `json:"publication_mode"`
	PublicationState string     `json:"publication_state"`
	Size             int64      `json:"size,omitempty"`
	Checksum         string     `json:"checksum,omitempty"`
	ChecksumAlgo     string     `json:"checksum_algorithm,omitempty"`
	ChecksumSource   string     `json:"checksum_source,omitempty"`
	FailureReason    string     `json:"failure_reason,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	PublishedAt      *time.Time `json:"published_at,omitempty"`
}

type runListResponse struct {
	Items      []runWire         `json:"items"`
	PageSize   int               `json:"page_size"`
	NextCursor string            `json:"next_cursor,omitempty"`
	Ordering   string            `json:"ordering"`
	Filters    map[string]string `json:"filters"`
}

// --- Handlers ---------------------------------------------------------------

func (h *runHandler) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("run_id")
	rec, err := h.svc.GetRun(r.Context(), id)
	if err != nil {
		writeRunErr(w, r, err)
		return
	}
	jobs, _ := h.svc.ListJobsForRun(r.Context(), id)
	arts, _ := h.svc.ListArtifacts(r.Context(), id, "")
	task, _ := h.svc.GetTask(r.Context(), rec.TaskID)
	detail := runDetailWire{runWire: toRunWire(rec, task)}
	for _, j := range jobs {
		detail.Jobs = append(detail.Jobs, toJobWire(j))
	}
	if len(jobs) > 0 {
		// active job = last non-terminal, else last
		var active *store.JobRecord
		for _, j := range jobs {
			if !j.TerminalAt.Valid {
				active = j
			}
		}
		if active == nil {
			active = jobs[len(jobs)-1]
		}
		jw := toJobWire(active)
		detail.ActiveJob = &jw
		detail.ExecutorType = active.Executor
		detail.ExecutionNode = active.Node
	}
	for _, a := range arts {
		detail.Artifacts = append(detail.Artifacts, toArtifactWire(a))
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *runHandler) list(w http.ResponseWriter, r *http.Request) {
	resp, err := h.listRunsResponse(r)
	if err != nil {
		writeRunErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *runHandler) listTaskRuns(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("task_id")
	if _, err := h.svc.GetTask(r.Context(), taskID); err != nil {
		writeRunErr(w, r, err)
		return
	}
	resp, err := h.listRunsResponse(r, func(f *store.RunListFilter) {
		f.TaskID = taskID
	})
	if err != nil {
		writeRunErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *runHandler) listRunsResponse(r *http.Request, mutate ...func(*store.RunListFilter)) (*runListResponse, error) {
	q := r.URL.Query()
	f := store.RunListFilter{
		TaskID:            q.Get("task_id"),
		StationID:         q.Get("station_id"),
		StationRevisionID: q.Get("station_revision_id"),
		Canonicality:      q.Get("canonicality"),
		Fingerprint:       q.Get("fingerprint"),
	}
	if state := q.Get("state"); state != "" {
		states := internalRunStatesForPublicFilter(state)
		if len(states) == 1 {
			f.State = states[0]
		} else {
			f.States = states
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = n
		} else {
			return nil, fmt.Errorf("%w: invalid limit", errInvalidRunListRequest)
		}
	}
	if v := q.Get("created_after"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid created_after", errInvalidRunListRequest)
		}
		f.CreatedAfter = t
	}
	if v := q.Get("created_before"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid created_before", errInvalidRunListRequest)
		}
		f.CreatedBefore = t
	}
	if v := q.Get("cursor"); v != "" {
		ts, id, err := decodeCursor(v)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid cursor", errInvalidRunListRequest)
		}
		f.CursorCreatedAt = ts
		f.CursorRunID = id
	}
	for _, fn := range mutate {
		fn(&f)
	}
	page, err := h.svc.ListRuns(r.Context(), f)
	if err != nil {
		return nil, err
	}
	resp := runListResponse{
		PageSize: f.Limit,
		Ordering: "created_at DESC, run_id DESC",
		Filters:  echoFilters(q),
		Items:    make([]runWire, 0, len(page.Items)),
	}
	if resp.PageSize == 0 {
		resp.PageSize = 50
	}
	for _, rec := range page.Items {
		task, _ := h.svc.GetTask(r.Context(), rec.TaskID)
		rw := toRunWire(rec, task)
		if jobs, jerr := h.svc.ListJobsForRun(r.Context(), rec.RunID); jerr == nil && len(jobs) > 0 {
			last := jobs[len(jobs)-1]
			rw.ExecutorType = last.Executor
			rw.ExecutionNode = last.Node
			rw.ElapsedTime = last.ElapsedTime
		}
		resp.Items = append(resp.Items, rw)
	}
	if page.HasMore {
		resp.NextCursor = encodeCursor(page.NextCreatedAt, page.NextRunID)
	}
	return &resp, nil
}

func (h *runHandler) listJobs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("run_id")
	if _, err := h.svc.GetRun(r.Context(), id); err != nil {
		writeRunErr(w, r, err)
		return
	}
	jobs, err := h.svc.ListJobsForRun(r.Context(), id)
	if err != nil {
		writeRunErr(w, r, err)
		return
	}
	out := make([]jobWire, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, toJobWire(j))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *runHandler) getJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("job_id")
	j, err := h.svc.GetJob(r.Context(), id)
	if err != nil {
		writeRunErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toJobWire(j))
}

func (h *runHandler) listArtifacts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("run_id")
	if _, err := h.svc.GetRun(r.Context(), id); err != nil {
		writeRunErr(w, r, err)
		return
	}
	logical := r.URL.Query().Get("logical_type")
	arts, err := h.svc.ListArtifacts(r.Context(), id, logical)
	if err != nil {
		writeRunErr(w, r, err)
		return
	}
	out := make([]artifactWire, 0, len(arts))
	for _, a := range arts {
		w := toArtifactWire(a)
		if pubs, perr := h.svc.ListPublicationsByArtifact(r.Context(), a.ArtifactID); perr == nil && len(pubs) > 0 {
			w.PublicationSummary = buildPublicationSummary(pubs)
		}
		out = append(out, w)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func buildPublicationSummary(pubs []*store.PublicationRecord) *publicationSummary {
	sum := &publicationSummary{Publications: make([]publicationWire, 0, len(pubs))}
	for _, p := range pubs {
		pw := publicationWire{
			PublicationID:    p.PublicationID,
			ArchiveID:        p.ArchiveID,
			TargetPath:       p.TargetPath,
			ObjectKind:       p.ObjectKind,
			PublicationMode:  p.PublicationMode,
			PublicationState: p.PublicationState,
			Size:             p.Size,
			Checksum:         p.Checksum,
			ChecksumAlgo:     p.ChecksumAlgo,
			ChecksumSource:   p.ChecksumSource,
			FailureReason:    p.FailureReason,
			CreatedAt:        p.CreatedAt.UTC(),
			PublishedAt:      nullableTime(p.PublishedAt),
		}
		sum.Publications = append(sum.Publications, pw)
		switch p.PublicationState {
		case store.PublicationStatePublished:
			sum.PublishedCount++
		case store.PublicationStatePending:
			sum.PendingCount++
		case store.PublicationStateFailed:
			sum.FailedCount++
		}
	}
	return sum
}

func (h *runHandler) listLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("run_id")
	if _, err := h.svc.GetRun(r.Context(), id); err != nil {
		writeRunErr(w, r, err)
		return
	}
	arts, err := h.svc.ListArtifacts(r.Context(), id, "log")
	if err != nil {
		writeRunErr(w, r, err)
		return
	}
	out := make([]artifactWire, 0, len(arts))
	for _, a := range arts {
		out = append(out, toArtifactWire(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *runHandler) cancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("run_id")
	out, err := h.svc.Cancel(r.Context(), id)
	if err != nil {
		writeRunErr(w, r, err)
		return
	}
	resp := map[string]any{
		"run_id":                    out.Run.RunID,
		"state":                     publicState(out.Run.State),
		"internal_state":            out.Run.State,
		"accepted":                  out.Accepted,
		"already_terminal":          out.AlreadyTerminal,
		"cancellation_complete":     out.CancellationComplete,
		"cancellation_requested_at": nullableTime(out.Run.CancellationRequestedAt),
		"terminal_at":               nullableTime(out.Run.TerminalAt),
	}
	status := http.StatusAccepted
	if out.AlreadyTerminal {
		status = http.StatusOK
	} else if out.CancellationComplete {
		status = http.StatusOK
	}
	writeJSON(w, status, resp)
}

func (h *runHandler) retry(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("task_id")
	out, err := h.svc.Retry(r.Context(), taskID)
	if err != nil {
		writeRunErr(w, r, err)
		return
	}
	task, _ := h.svc.GetTask(r.Context(), out.Run.TaskID)
	writeJSON(w, http.StatusCreated, toRunWire(out.Run, task))
}

func (h *runHandler) promote(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("run_id")
	var body struct {
		Reason   string `json:"reason"`
		Operator string `json:"operator"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		apierr.Write(w, r, http.StatusBadRequest, apierr.CodeInvalidRequest, err.Error())
		return
	}
	out, err := h.svc.PromoteCanonical(r.Context(), id, body.Reason, body.Operator)
	if err != nil {
		writeRunErr(w, r, err)
		return
	}
	resp := map[string]any{
		"run_id":            out.Run.RunID,
		"fingerprint_id":    out.FingerprintID,
		"previous_run_id":   out.PreviousRunID,
		"already_canonical": out.AlreadyCanonical,
		"canonicality":      out.Run.Canonicality,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *runHandler) artifactContent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("artifact_id")
	a, err := h.svc.GetArtifact(r.Context(), id)
	if err != nil {
		writeRunErr(w, r, err)
		return
	}
	if a.Availability != "available" || a.Path == "" {
		apierr.Write(w, r, http.StatusNotFound, apierr.CodeNotFound, "artifact content unavailable")
		return
	}
	if a.ObjectKind == store.ObjectKindDirectory {
		apierr.Write(w, r, http.StatusBadRequest, apierr.CodeInvalidRequest, "directory artifact content access is not supported")
		return
	}
	body, err := os.ReadFile(a.Path)
	if err != nil {
		apierr.Write(w, r, http.StatusNotFound, apierr.CodeNotFound, "artifact file missing")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	if a.Checksum != "" {
		w.Header().Set("X-Content-Checksum", a.ChecksumAlgo+":"+a.Checksum)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// --- Translators ------------------------------------------------------------

// publicState collapses internal lifecycle states into the operator-facing set.
func publicState(internal string) string {
	switch internal {
	case "pending", "preparing":
		return "pending"
	case "ready", "dispatched":
		return "queued"
	case "running":
		return "running"
	case "finalizing":
		return "finalizing"
	case "complete":
		return "complete"
	case "failed":
		return "failed"
	case "cancelled":
		return "cancelled"
	default:
		return internal
	}
}

func internalRunStatesForPublicFilter(state string) []string {
	switch state {
	case "pending":
		return []string{"pending", "preparing"}
	case "queued":
		return []string{"ready", "dispatched"}
	default:
		return []string{state}
	}
}

func toRunWire(r *store.RunRecord, t *store.TaskRecord) runWire {
	w := runWire{
		RunID:                 r.RunID,
		RunRef:                fmt.Sprintf("%s/r%d", r.TaskID, r.RetryIndex),
		TaskID:                r.TaskID,
		StationRevisionID:     r.StationRevisionID,
		State:                 publicState(r.State),
		InternalState:         r.State,
		Canonicality:          r.Canonicality,
		RetryIndex:            r.RetryIndex,
		WorkingRoot:           r.WorkingRoot,
		ProcessingFingerprint: r.ProcessingFingerprint,
		FailureReason:         r.FailureReason,
		FailureSummary:        r.FailureReason,
		CreatedAt:             r.CreatedAt.UTC(),
	}
	if t != nil {
		w.StationID = t.DestinationStationID
		w.Start = t.WindowStart.UTC()
		w.End = t.WindowEnd.UTC()
	}
	w.PreparedAt = nullableTime(r.PreparedAt)
	w.DispatchedAt = nullableTime(r.DispatchedAt)
	w.StartedAt = nullableTime(r.StartedAt)
	w.TerminalAt = nullableTime(r.TerminalAt)
	w.CancellationRequestedAt = nullableTime(r.CancellationRequestedAt)
	return w
}

func toJobWire(j *store.JobRecord) jobWire {
	return jobWire{
		JobID:             j.JobID,
		RunID:             j.RunID,
		ExecutorType:      j.Executor,
		SchedulerID:       j.SchedulerID,
		SchedulerState:    j.SchedulerState,
		ExecutionNode:     j.Node,
		ElapsedTime:       j.ElapsedTime,
		SubmissionAttempt: j.SubmissionAttempt,
		SubmittedAt:       nullableTime(j.SubmittedAt),
		LastObservedAt:    nullableTime(j.LastObservedAt),
		TerminalAt:        nullableTime(j.TerminalAt),
	}
}

func toArtifactWire(a *store.ArtifactRecord) artifactWire {
	return artifactWire{
		ArtifactID:       a.ArtifactID,
		ProducingRunID:   a.ProducingRunID,
		LogicalType:      a.LogicalType,
		ObjectKind:       a.ObjectKind,
		FileType:         a.FileType,
		Path:             a.Path,
		Size:             a.Size,
		Checksum:         a.Checksum,
		ChecksumAlgo:     a.ChecksumAlgo,
		ChecksumSource:   a.ChecksumSource,
		ValidationStatus: a.ValidationStatus,
		Availability:     a.Availability,
		CreatedAt:        a.CreatedAt.UTC(),
	}
}

func nullableTime(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

// --- Helpers ----------------------------------------------------------------

func writeRunErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errInvalidRunListRequest):
		apierr.Write(w, r, http.StatusBadRequest, apierr.CodeInvalidRequest, err.Error())
	case errors.Is(err, runs.ErrRunNotFound), errors.Is(err, runs.ErrJobNotFound):
		apierr.Write(w, r, http.StatusNotFound, apierr.CodeNotFound, err.Error())
	case errors.Is(err, runs.ErrInvalidStateTransition):
		apierr.Write(w, r, http.StatusConflict, apierr.CodeInvalidStateTransition, err.Error())
	case errors.Is(err, runs.ErrCancellationUnsupported):
		apierr.Write(w, r, http.StatusConflict, apierr.CodeCancellationUnsupported, err.Error())
	case errors.Is(err, runs.ErrPromoteIneligible):
		apierr.Write(w, r, http.StatusConflict, apierr.CodeInvalidStateTransition, err.Error())
	case errors.Is(err, runs.ErrReconciliationInProgress):
		apierr.Write(w, r, http.StatusConflict, apierr.CodeReconciliationInProgress, err.Error())
	case errors.Is(err, runs.ErrUnknownStation):
		apierr.Write(w, r, http.StatusBadRequest, apierr.CodeUnknownStation, err.Error())
	case errors.Is(err, runs.ErrTaskNotFound):
		apierr.Write(w, r, http.StatusNotFound, apierr.CodeNotFound, err.Error())
	case errors.Is(err, runs.ErrRetryIneligible):
		apierr.Write(w, r, http.StatusConflict, apierr.CodeInvalidStateTransition, err.Error())
	case errors.Is(err, runs.ErrFatalPrepare):
		apierr.Write(w, r, http.StatusUnprocessableEntity, apierr.CodeInputUnavailable, err.Error())
	default:
		apierr.Write(w, r, http.StatusInternalServerError, apierr.CodeInternal, "internal error")
	}
}

func echoFilters(q map[string][]string) map[string]string {
	out := map[string]string{}
	for k, v := range q {
		if k == "cursor" || k == "limit" {
			continue
		}
		if len(v) > 0 && v[0] != "" {
			out[k] = v[0]
		}
	}
	return out
}

// guard against unused-import in some build configurations
var _ = base64.StdEncoding
var _ = strings.ToLower
