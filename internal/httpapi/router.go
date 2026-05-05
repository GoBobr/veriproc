// Package httpapi wires VeriProc HTTP handlers onto the standard library's
// net/http router. Milestone 0 mounts only the health and readiness endpoints
// (Spec 5.7); later milestones will register task, run, job, artifact, and
// provenance handlers.
package httpapi

import (
	"net/http"

	"github.com/eum/veriproc/internal/config"
	"github.com/eum/veriproc/internal/health"
	"github.com/rs/zerolog"
)

// Deps bundles the collaborators that handlers need.
type Deps struct {
	Config *config.Config
	Health *health.Aggregator
	Logger zerolog.Logger
}

// NewRouter returns an http.Handler with all Milestone-0 endpoints mounted.
//
// Health and readiness are exposed under both root (/health, /readiness) for
// platform probes and the API-versioned prefix (/api/v1/...) for clients that
// pin to an API version.
func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()

	healthH := newHealthHandler(d)
	readyH := newReadinessHandler(d)

	mux.Handle("GET /health", healthH)
	mux.Handle("GET /readiness", readyH)
	mux.Handle("GET /api/v1/health", healthH)
	mux.Handle("GET /api/v1/readiness", readyH)

	return chain(mux,
		correlationIDMiddleware(),
		requestLogMiddleware(d.Logger),
		recoverMiddleware(d.Logger),
	)
}

// chain wraps h with the supplied middlewares, applied in order so that the
// first listed middleware is the outermost.
func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}
