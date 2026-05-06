// Package httpapi wires VeriProc HTTP handlers onto the standard library's
// net/http router. Milestone 0 mounts only the health and readiness endpoints
// (Spec 5.7); later milestones will register task, run, job, artifact, and
// provenance handlers.
package httpapi

import (
	"net/http"

	"github.com/eum/veriproc/internal/auth"
	"github.com/eum/veriproc/internal/config"
	"github.com/eum/veriproc/internal/groups"
	"github.com/eum/veriproc/internal/health"
	"github.com/eum/veriproc/internal/httpapi/apierr"
	"github.com/eum/veriproc/internal/runs"
	"github.com/eum/veriproc/internal/tasks"
	"github.com/rs/zerolog"
)

// Deps bundles the collaborators that handlers need.
type Deps struct {
	Config *config.Config
	Health *health.Aggregator
	Logger zerolog.Logger
	// Tasks is optional at M0; required from M2 onward to expose /tasks.
	Tasks *tasks.Service
	// Runs is optional at M0–M2; required from M3 onward to expose run/job/artifact reads.
	Runs *runs.Service
	// Groups is optional; required from M7 to expose split-group endpoints (§5.5.7).
	Groups *groups.Service
	// Authn enforces bearer-token auth on /api/v1/* (M6). When nil, the API
	// is open (preserves M0–M5 behavior for tests / local dev).
	Authn auth.Authenticator
	// Quota enforces per-principal mutating-request quotas (M6). Optional.
	Quota *auth.QuotaEnforcer
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

	if d.Tasks != nil {
		th := &taskHandler{svc: d.Tasks}
		mux.HandleFunc("POST /api/v1/tasks", th.submit)
		mux.HandleFunc("GET /api/v1/tasks", th.list)
		mux.HandleFunc("GET /api/v1/tasks/{task_id}", th.get)
	}

	if d.Runs != nil {
		rh := &runHandler{svc: d.Runs}
		mux.HandleFunc("GET /api/v1/runs", rh.list)
		mux.HandleFunc("GET /api/v1/runs/{run_id}", rh.get)
		mux.HandleFunc("POST /api/v1/tasks/{task_id}/retry", rh.retry)
		mux.HandleFunc("POST /api/v1/runs/{run_id}/cancel", rh.cancel)
		mux.HandleFunc("POST /api/v1/runs/{run_id}/promote", rh.promote)
		mux.HandleFunc("GET /api/v1/runs/{run_id}/jobs", rh.listJobs)
		mux.HandleFunc("GET /api/v1/runs/{run_id}/artifacts", rh.listArtifacts)
		mux.HandleFunc("GET /api/v1/runs/{run_id}/logs", rh.listLogs)
		mux.HandleFunc("GET /api/v1/jobs/{job_id}", rh.getJob)
		mux.HandleFunc("GET /api/v1/artifacts/{artifact_id}/content", rh.artifactContent)
	}

	if d.Groups != nil {
		gh := &groupHandler{svc: d.Groups}
		mux.HandleFunc("GET /api/v1/groups", gh.list)
		mux.HandleFunc("GET /api/v1/groups/{group_id}", gh.get)
		mux.HandleFunc("POST /api/v1/groups/{group_id}/close", gh.close)
	}

	// Catch-all that distinguishes 404 (no route at all) from 405 (route exists
	// for a different method). The standard mux already returns 405 for known
	// paths with the wrong method via its own default handler, but it returns
	// a plain-text body. We wrap the mux so all default error responses use
	// the apierr envelope (Spec \u00a75.5.8 / \u00a75.7).
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &errCapturingWriter{ResponseWriter: w}
		mux.ServeHTTP(rec, r)
		if rec.status == http.StatusNotFound && !rec.wroteBody {
			apierr.Write(w, r, http.StatusNotFound, apierr.CodeNotFound, "resource not found")
			return
		}
		if rec.status == http.StatusMethodNotAllowed && !rec.wroteBody {
			apierr.Write(w, r, http.StatusMethodNotAllowed, apierr.CodeMethodNotAllowed, "method not allowed")
			return
		}
	})

	return chain(wrapped,
		correlationIDMiddleware(),
		requestLogMiddleware(d.Logger),
		recoverMiddleware(d.Logger),
		authMiddleware(d.Authn),
		quotaMiddleware(d.Quota),
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

// errCapturingWriter intercepts the status code and tracks whether the body
// has been written so we can substitute the default error envelope for empty
// 404/405 responses without double-writing real handler output.
type errCapturingWriter struct {
	http.ResponseWriter
	status     int
	wroteBody  bool
	wroteHead  bool
}

func (w *errCapturingWriter) WriteHeader(code int) {
	if w.wroteHead {
		return
	}
	w.status = code
	w.wroteHead = true
	if code == http.StatusNotFound || code == http.StatusMethodNotAllowed {
		// Defer header write; outer wrapper may replace the response entirely.
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *errCapturingWriter) Write(b []byte) (int, error) {
	if w.status == http.StatusNotFound || w.status == http.StatusMethodNotAllowed {
		// Suppress default plain-text body so the wrapper can write the envelope.
		w.wroteBody = false
		return len(b), nil
	}
	w.wroteBody = true
	return w.ResponseWriter.Write(b)
}
