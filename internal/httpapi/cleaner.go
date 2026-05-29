package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/eum/veriproc/internal/cleaner"
	"github.com/eum/veriproc/internal/httpapi/apierr"
	"github.com/eum/veriproc/internal/policy"
	"github.com/eum/veriproc/internal/store"
)

// cleanerHandler exposes the destructive maintenance endpoints: deleting a
// task or run together with its dependent state and on-disk working roots, and
// the time-bounded "clean" sweep (Spec §6 maintenance commands).
type cleanerHandler struct {
	svc *cleaner.Service
}

// cleanRequest is the body of POST /api/v1/maintenance/clean. Timestamps accept
// RFC 3339 and compact UTC forms (e.g. 20250529T100000), matching submit.
//
// Basis selects which task timestamps the cutoffs are compared against:
// "processing-time" (default) uses created_at; "processing-window" uses the
// data sensing window (window_start / window_end).
type cleanRequest struct {
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	Basis  string `json:"basis,omitempty"`
	DryRun bool   `json:"dry_run,omitempty"`
}

func (h *cleanerHandler) deleteTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("task_id")
	rep, err := h.svc.DeleteTask(r.Context(), taskID, dryRunRequested(r))
	if err != nil {
		writeCleanerErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (h *cleanerHandler) deleteRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run_id")
	rep, err := h.svc.DeleteRun(r.Context(), runID, dryRunRequested(r))
	if err != nil {
		writeCleanerErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (h *cleanerHandler) clean(w http.ResponseWriter, r *http.Request) {
	var req cleanRequest
	if r.Body != nil {
		defer r.Body.Close()
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		// An empty body is tolerated (treated as no cutoffs → ErrNoCutoff below).
		if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			apierr.Write(w, r, http.StatusBadRequest, apierr.CodeInvalidRequest, "malformed JSON: "+err.Error())
			return
		}
	}

	var f cleaner.CleanFilter
	basis, err := parseCleanupBasis(req.Basis)
	if err != nil {
		apierr.Write(w, r, http.StatusBadRequest, apierr.CodeInvalidRequest, err.Error())
		return
	}
	f.Basis = basis
	if req.Before != "" {
		t, err := policy.ParseWindowTimestamp(req.Before)
		if err != nil {
			apierr.Write(w, r, http.StatusBadRequest, apierr.CodeInvalidRequest, "before: "+err.Error())
			return
		}
		f.Before = t
	}
	if req.After != "" {
		t, err := policy.ParseWindowTimestamp(req.After)
		if err != nil {
			apierr.Write(w, r, http.StatusBadRequest, apierr.CodeInvalidRequest, "after: "+err.Error())
			return
		}
		f.After = t
	}

	rep, err := h.svc.Clean(r.Context(), f, req.DryRun)
	if err != nil {
		writeCleanerErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// parseCleanupBasis maps the request basis string to a store.CleanupBasis.
// An empty value defaults to processing time.
func parseCleanupBasis(s string) (store.CleanupBasis, error) {
	switch s {
	case "", "processing-time", "processing_time":
		return store.BasisProcessingTime, nil
	case "processing-window", "processing_window", "window":
		return store.BasisProcessingWindow, nil
	default:
		return store.BasisProcessingTime, fmt.Errorf("basis: must be %q or %q", "processing-time", "processing-window")
	}
}

// dryRunRequested reports whether the request carries ?dry_run=true.
func dryRunRequested(r *http.Request) bool {
	switch r.URL.Query().Get("dry_run") {
	case "1", "true", "yes":
		return true
	}
	return false
}

func writeCleanerErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, cleaner.ErrTaskNotFound), errors.Is(err, cleaner.ErrRunNotFound):
		apierr.Write(w, r, http.StatusNotFound, apierr.CodeNotFound, err.Error())
	case errors.Is(err, cleaner.ErrNoCutoff):
		apierr.Write(w, r, http.StatusBadRequest, apierr.CodeInvalidRequest, err.Error())
	default:
		apierr.Write(w, r, http.StatusInternalServerError, apierr.CodeInternal, err.Error())
	}
}
