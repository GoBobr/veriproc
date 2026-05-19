// Package logging provides structured logging and correlation-ID helpers.
package logging

import (
	"context"
	"fmt"
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

// consoleTimeStore is the zerolog field format used when console mode is
// active. It must carry enough precision to round-trip through the display
// format (five fractional-second digits).
const consoleTimeStore = "2006-01-02T15:04:05.000000Z07:00"

// consoleTimeDisplay is the timestamp format shown in console output.
const consoleTimeDisplay = "2006-01-02T15:04:05.00000"

// ANSI colour codes used for console level formatting.
const (
	ansiReset   = "\x1b[0m"
	ansiMagenta = "\x1b[35m"
	ansiYellow  = "\x1b[33m"
	ansiGreen   = "\x1b[32m"
	ansiRed     = "\x1b[31m"
	ansiBoldRed = "\x1b[1;31m"
)

// New constructs a logger from the supplied configuration.
func New(cfg config.LogConfig) zerolog.Logger {
	level := parseLevel(cfg.Level)
	var w io.Writer = os.Stdout
	if strings.ToLower(cfg.Format) == "console" {
		// Store timestamps with microsecond precision so the ConsoleWriter
		// can reformat them with the desired fractional-second display.
		zerolog.TimeFieldFormat = consoleTimeStore

		// Detect colour support once: honour NO_COLOR and check for a TTY.
		useColor := stdoutIsTerminal() && os.Getenv("NO_COLOR") == ""

		w = zerolog.ConsoleWriter{
			Out:        os.Stdout,
			TimeFormat: consoleTimeDisplay,
			NoColor:    !useColor,
			FormatLevel: func(i interface{}) string {
				return consoleLevelFormatted(fmt.Sprintf("%s", i), useColor)
			},
		}
	}
	return zerolog.New(w).Level(level).With().Timestamp().Logger()
}

// stdoutIsTerminal reports whether os.Stdout is an interactive terminal.
func stdoutIsTerminal() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// consoleLevelFormatted returns the bracketed level abbreviation, optionally
// wrapped in ANSI colour codes matching zerolog's default colour scheme.
func consoleLevelFormatted(l string, useColor bool) string {
	abbr := consoleLevelAbbr(l)
	bracket := "[" + abbr + "]"
	if !useColor {
		return bracket
	}
	switch l {
	case "trace":
		return ansiMagenta + bracket + ansiReset
	case "debug":
		return ansiYellow + bracket + ansiReset
	case "info":
		return ansiGreen + bracket + ansiReset
	case "warn":
		return ansiRed + bracket + ansiReset
	case "error", "fatal", "panic":
		return ansiBoldRed + bracket + ansiReset
	default:
		return bracket
	}
}

// consoleLevelAbbr maps a zerolog level string to a 3-letter abbreviation.
func consoleLevelAbbr(l string) string {
	switch l {
	case "trace":
		return "TRC"
	case "debug":
		return "DBG"
	case "info":
		return "INF"
	case "warn":
		return "WRN"
	case "error":
		return "ERR"
	case "fatal":
		return "FAT"
	case "panic":
		return "PNC"
	default:
		return strings.ToUpper(l)
	}
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
