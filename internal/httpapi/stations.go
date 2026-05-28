package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/eum/veriproc/internal/httpapi/apierr"
	"github.com/eum/veriproc/internal/stations"
)

type stationHandler struct {
	svc *stations.Service
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
	items, err := h.svc.Summary(r.Context(), since)
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
	out, err := h.svc.SummaryStation(r.Context(), r.PathValue("station_id"), since)
	if err != nil {
		writeStationErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"since": since, "station": out})
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

func writeStationErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, stations.ErrUnknownStation):
		apierr.Write(w, r, http.StatusBadRequest, apierr.CodeUnknownStation, err.Error())
	default:
		apierr.Write(w, r, http.StatusInternalServerError, apierr.CodeInternal, "internal server error")
	}
}
