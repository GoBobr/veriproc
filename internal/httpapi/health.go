package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/eum/veriproc/internal/health"
	"github.com/eum/veriproc/internal/version"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func newHealthHandler(d Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		report := d.Health.Liveness()
		report.InstanceID = d.Config.InstanceID
		report.APIVersion = version.APIVersion
		report.Version = version.Version
		report.Commit = version.Commit

		status := http.StatusOK
		if report.Status != health.StateOK {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, report)
	})
}

func newReadinessHandler(d Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		report := d.Health.Readiness(r.Context())
		report.InstanceID = d.Config.InstanceID
		report.APIVersion = version.APIVersion

		status := http.StatusOK
		if report.ReadinessState != health.StateReady {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, report)
	})
}
