// Package logging provides structured logging and correlation-ID helpers.
package logging

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/eum/veriproc/internal/config"
	"github.com/rs/zerolog"
)

type ctxKey struct{}

var correlationKey = ctxKey{}

// CorrelationHeader is the canonical HTTP header name for request correlation.
const CorrelationHeader = "X-Correlation-ID"

// New constructs a logger from the supplied configuration.
func New(cfg config.LogConfig) zerolog.Logger {
	level := parseLevel(cfg.Level)
	var w io.Writer = os.Stdout
	if strings.ToLower(cfg.Format) == "console" {
		w = zerolog.ConsoleWriter{Out: os.Stdout}
	}
	return zerolog.New(w).Level(level).With().Timestamp().Logger()
}

func parseLevel(s string) zerolog.Level {
	switch strings.ToLower(s) {
	case "trace":
		return zerolog.TraceLevel
	case "debug":
		return zerolog.DebugLevel
	case "warn":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	default:
		return zerolog.InfoLevel
	}
}

// WithCorrelationID returns a context carrying the supplied correlation id.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationKey, id)
}

// CorrelationIDFromContext returns the correlation id stored on the context,
// or the empty string when none is set.
func CorrelationIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(correlationKey).(string); ok {
		return v
	}
	return ""
}
