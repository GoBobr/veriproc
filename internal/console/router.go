package console

import (
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/gobobr/veriproc/internal/auth"
)

// Router builds the console gateway HTTP handler. webapp is an optional
// embedded filesystem; when nil the gateway returns 404 for non-API paths.
func (g *Gateway) Router(webapp fs.FS) http.Handler {
	mux := http.NewServeMux()

	// ── public endpoints ──────────────────────────────────────────────
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UTC().Format(time.RFC3339)})
	})
	// Identity introspection sits outside the authenticated API: it is how a
	// login submission learns the effective subject and role for a credential,
	// and how an anonymous visitor learns it is anonymous. It never accepts a
	// role from the caller.
	mux.HandleFunc("GET /api/console/identity", g.handleIdentity)
	// ── api ──────────────────────────────────────────────────────────
	api := http.NewServeMux()
	api.HandleFunc("GET /api/console/instances", g.handleListInstances)
	api.HandleFunc("GET /api/console/info", g.handleSystemInfo)
	api.HandleFunc("GET /api/console/instances/{instance_id}/dashboard", g.handleDashboard)
	api.HandleFunc("GET /api/console/instances/{instance_id}/stations/{station_id}/runs", g.handleListStationRuns)
	api.HandleFunc("GET /api/console/instances/{instance_id}/stations/{station_id}/config", g.handleStationConfig)
	api.HandleFunc("POST /api/console/instances/{instance_id}/stations/{station_id}/pause", g.handlePauseStation)
	api.HandleFunc("POST /api/console/instances/{instance_id}/stations/{station_id}/unpause", g.handleUnpauseStation)
	api.HandleFunc("POST /api/console/instances/{instance_id}/stations/{station_id}/hide-failed", g.handleHideStationFailures)
	api.HandleFunc("POST /api/console/instances/{instance_id}/stations/{station_id}/unhide-all", g.handleUnhideStation)
	api.HandleFunc("POST /api/console/instances/{instance_id}/stations/{station_id}/submissions", g.handleSubmit)
	api.HandleFunc("POST /api/console/instances/{instance_id}/tasks/{task_id}/runs/{retry_index}/cancel", g.handleCancelRun)
	api.HandleFunc("POST /api/console/instances/{instance_id}/tasks/{task_id}/runs/{retry_index}/hide", g.handleHideRun)
	api.HandleFunc("POST /api/console/instances/{instance_id}/tasks/{task_id}/retry", g.handleRetryTask)
	api.HandleFunc("GET /api/console/instances/{instance_id}/tasks/{task_id}", g.handleGetTask)
	api.HandleFunc("GET /api/console/instances/{instance_id}/tasks/{task_id}/runs", g.handleTaskRuns)
	api.HandleFunc("GET /api/console/instances/{instance_id}/tasks/{task_id}/runs/{retry_index}/tree", g.handleRunTree)
	api.HandleFunc("GET /api/console/instances/{instance_id}/tasks/{task_id}/runs/{retry_index}/file", g.handleRunFile)
	api.HandleFunc("GET /api/console/instances/{instance_id}/tasks/{task_id}/runs/{retry_index}/image", g.handleRunImage)

	mux.Handle("/api/console/", g.authMiddleware(api))

	// ── static frontend ──────────────────────────────────────────────
	if webapp != nil {
		fsrv := http.FileServer(http.FS(webapp))
		mux.Handle("/", spaHandler(webapp, fsrv))
	}

	return chainMiddleware(mux, g.corsMiddleware(), g.loggingMiddleware())
}

// authMiddleware enforces console authentication on /api/console/* routes.
//
// Every endpoint requires a valid bearer token unless anonymous browsing is
// enabled, in which case a credential-less request is admitted as a read-only
// principal on read endpoints. Mutating endpoints always require the operator
// role, so anonymous browsing can never grant write capability.
//
// A credential that is presented but matches no access-profile entry is
// downgraded to the anonymous read-only principal on read endpoints, exactly
// like a request carrying no credential, and the response reports the
// rejection through the CredentialRejectedHeader header.
func (g *Gateway) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, err := g.authenticateOrAnonymous(r)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="veriproc-console"`)
			writeErr(w, http.StatusUnauthorized, "unauthenticated",
				"missing or invalid bearer token: supply Authorization: Bearer <token> with a reader or operator credential")
			return
		}
		if isMutatingMethod(r.Method) {
			if session.Anonymous {
				w.Header().Set("WWW-Authenticate", `Bearer realm="veriproc-console"`)
				writeErr(w, http.StatusUnauthorized, "unauthenticated",
					"missing bearer token: anonymous browsing is read-only; supply a reader or operator credential to mutate state")
				return
			}
			if !session.Principal.CanMutate() {
				writeErr(w, http.StatusForbidden, "unauthorized", "operator role required")
				return
			}
		}
		if session.CredentialRejected {
			w.Header().Set(CredentialRejectedHeader, CredentialRejectedValue)
		}
		ctx := WithPrincipal(r.Context(), session.Principal)
		// Relay the caller's own credential upstream (D15) so the daemon sees
		// the logged-in user's rights rather than only the gateway's service
		// credential. Anonymous callers (including a rejected credential
		// downgraded to guest) get no relay token, so the upstream hop falls
		// back to the configured per-instance service credential — or to no
		// credential at all when none is configured.
		if !session.Anonymous {
			if tok := bearerToken(r.Header.Get("Authorization")); tok != "" {
				ctx = WithRelayToken(ctx, tok)
			}
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// CredentialRejectedHeader and CredentialRejectedValue are re-exported from
// internal/auth so they match the daemon's header of the same name and a
// client can detect the guest downgrade the same way at either scope.
const (
	// CredentialRejectedHeader is set on a response served to a caller whose
	// presented credential matched no access-profile entry.
	CredentialRejectedHeader = auth.CredentialRejectedHeader
	// CredentialRejectedValue is the value of CredentialRejectedHeader.
	CredentialRejectedValue = auth.CredentialRejectedValue
)

func (g *Gateway) corsMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (g *Gateway) loggingMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
			next.ServeHTTP(rw, r)
			ev := g.logger.Debug()
			if rw.code >= 500 {
				ev = g.logger.Error()
			} else if rw.code >= 400 {
				ev = g.logger.Warn()
			}
			ev.Str("method", r.Method).
				Str("path", r.URL.Path).
				Int("status", rw.code).
				Dur("duration", time.Since(start)).
				Msg("console_request")
		})
	}
}

// statusRecorder wraps http.ResponseWriter to capture the written status code.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

func chainMiddleware(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// spaHandler serves built frontend assets. Unknown paths fall back to
// index.html so client-side routing works.
func spaHandler(webapp fs.FS, fsrv http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean == "" {
			clean = "index.html"
		}
		if _, err := fs.Stat(webapp, clean); err == nil {
			fsrv.ServeHTTP(w, r)
			return
		}
		// SPA fallback to index.html for unknown paths (TanStack Router).
		f, err := webapp.Open("index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		st, _ := fs.Stat(webapp, "index.html")
		http.ServeContent(w, r, "index.html", st.ModTime(), f.(interface {
			Seek(offset int64, whence int) (int64, error)
			Read([]byte) (int, error)
		}))
	})
}
