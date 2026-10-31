package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gobobr/veriproc/internal/cleaner"
	"github.com/gobobr/veriproc/internal/httpapi/apierr"
	"github.com/gobobr/veriproc/internal/stations"
)

type stationHandler struct {
	svc     *stations.Service
	cleaner *cleaner.Service
}

func (h *stationHandler) list(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.List(r.Context())
	if err != nil {
		writeStationErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *stationHandler) summary(w http.ResponseWriter, r *http.Request) {
	since, ok := parseStationSummarySince(w, r)
	if !ok {
		return
	}
	slotCap := parseSlotCount(r)
	items, err := h.svc.Summary(r.Context(), since, slotCap)
	if err != nil {
		writeStationErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"since": since, "items": items})
}

func (h *stationHandler) summaryOne(w http.ResponseWriter, r *http.Request) {
	since, ok := parseStationSummarySince(w, r)
	if !ok {
		return
	}
	slotCap := parseSlotCount(r)
	out, err := h.svc.SummaryStation(r.Context(), r.PathValue("station_id"), since, slotCap)
	if err != nil {
		writeStationErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"since": since, "station": *out})
}

func parseSlotCount(r *http.Request) int {
	if v := r.URL.Query().Get("slot_count"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 12
}

func parseStationSummarySince(w http.ResponseWriter, r *http.Request) (time.Time, bool) {
	since := time.Now().UTC().Add(-24 * time.Hour)
	if v := r.URL.Query().Get("since"); v != "" {
		parsed, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			apierr.Write(w, r, http.StatusBadRequest, apierr.CodeInvalidRequest, "invalid since")
			return time.Time{}, false
		}
		since = parsed.UTC()
	}
	return since, true
}

// remove retires a station whose config folder no longer exists (Station
// Retirement Endpoint requirement). With ?purge=true the station's tasks,
// runs, and working roots are deleted first via the cleaner cascade.
// Mutating endpoint: only operator credentials pass the auth middleware.
func (h *stationHandler) remove(w http.ResponseWriter, r *http.Request) {
	stationID := r.PathValue("station_id")
	// Guard first so nothing is purged for a live or unknown station.
	if err := h.svc.GuardRetirable(r.Context(), stationID); err != nil {
		writeRetireErr(w, r, err)
		return
	}
	var purgeReport *cleaner.Report
	if r.URL.Query().Get("purge") == "true" {
		if h.cleaner == nil {
			apierr.Write(w, r, http.StatusServiceUnavailable, apierr.CodeDependencyUnavailable, "cleaner service not configured")
			return
		}
		rep, err := h.cleaner.PurgeStation(r.Context(), stationID)
		if err != nil {
			writeCleanerErr(w, r, err)
			return
		}
		purgeReport = rep
	}
	changed, err := h.svc.Retire(r.Context(), stationID)
	if err != nil {
		writeRetireErr(w, r, err)
		return
	}
	resp := map[string]any{
		"station_id": stationID,
		"retired":    changed,
	}
	if purgeReport != nil {
		resp["purge"] = purgeReport
	}
	writeJSON(w, http.StatusOK, resp)
}

// missing lists store-known, non-retired stations without a config folder
// (prune discovery for the Station Prune CLI Command requirement).
func (h *stationHandler) missing(w http.ResponseWriter, r *http.Request) {
	ids, err := h.svc.ListMissing(r.Context())
	if err != nil {
		writeStationErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ids})
}

// writeRetireErr maps retirement errors onto the API error envelope.
func writeRetireErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, stations.ErrUnknownStation):
		apierr.Write(w, r, http.StatusNotFound, apierr.CodeNotFound, err.Error())
	case errors.Is(err, stations.ErrStationStillLoaded):
		apierr.Write(w, r, http.StatusConflict, apierr.CodeInvalidStateTransition, err.Error())
	default:
		apierr.Write(w, r, http.StatusInternalServerError, apierr.CodeInternal, "internal server error")
	}
}

func (h *stationHandler) pause(w http.ResponseWriter, r *http.Request) {
	stationID := r.PathValue("station_id")
	out, err := h.svc.Pause(r.Context(), stationID)
	if err != nil {
		writeStationErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *stationHandler) unpause(w http.ResponseWriter, r *http.Request) {
	stationID := r.PathValue("station_id")
	out, err := h.svc.Unpause(r.Context(), stationID)
	if err != nil {
		writeStationErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// config serves the station's resolved configuration view (Station Config
// Endpoint requirement). Read-only: readable with reader or operator
// credentials via the method-based auth middleware.
func (h *stationHandler) config(w http.ResponseWriter, r *http.Request) {
	view, err := h.svc.ConfigView(r.Context(), r.PathValue("station_id"))
	if err != nil {
		switch {
		case errors.Is(err, stations.ErrUnknownStation):
			apierr.Write(w, r, http.StatusNotFound, apierr.CodeNotFound, "station not found")
		case errors.Is(err, stations.ErrNoConfigSource):
			apierr.Write(w, r, http.StatusServiceUnavailable, apierr.CodeDependencyUnavailable, "station config source not configured")
		default:
			apierr.Write(w, r, http.StatusInternalServerError, apierr.CodeInternal, "internal server error")
		}
		return
	}
	writeJSON(w, http.StatusOK, view)
}
func writeStationErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, stations.ErrUnknownStation):
		apierr.Write(w, r, http.StatusBadRequest, apierr.CodeUnknownStation, err.Error())
	default:
		apierr.Write(w, r, http.StatusInternalServerError, apierr.CodeInternal, "internal server error")
	}
}
