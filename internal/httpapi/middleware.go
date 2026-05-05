package httpapi

import (
	"net/http"
	"runtime/debug"
	"time"

	"github.com/eum/veriproc/internal/logging"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// correlationIDMiddleware ensures every request has an X-Correlation-ID and
// propagates it on the request context and the response headers.
func correlationIDMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(logging.CorrelationHeader)
			if id == "" {
				id = uuid.NewString()
			}
			w.Header().Set(logging.CorrelationHeader, id)
			ctx := logging.WithCorrelationID(r.Context(), id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// statusCapturingWriter records the response status code for logging.
type statusCapturingWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusCapturingWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// requestLogMiddleware logs one structured event per request.
func requestLogMiddleware(logger zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusCapturingWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			logger.Info().
				Str("correlation_id", logging.CorrelationIDFromContext(r.Context())).
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Int("status", sw.status).
				Dur("duration", time.Since(start)).
				Msg("http_request")
		})
	}
}

// recoverMiddleware turns panics into 500 responses without crashing the server.
func recoverMiddleware(logger zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					logger.Error().
						Interface("panic", v).
						Bytes("stack", debug.Stack()).
						Str("path", r.URL.Path).
						Msg("panic recovered")
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"internal server error"}}`))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
