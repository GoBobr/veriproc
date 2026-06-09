// Package apierr defines the machine-readable error envelope used by all
// VeriProc REST responses (Spec \u00a75.5.8 and \u00a75.7).
//
// The envelope shape is intentionally stable so that clients and the CLI can
// rely on the structure across milestones. Milestone 0 introduces only the
// minimal shape needed for default 404/405/500 responses; later milestones
// (notably M5) extend the catalog of machine-readable codes and add field-level
// validation details.
package apierr

import (
	"encoding/json"
	"net/http"

	"github.com/gobobr/veriproc/internal/logging"
)

// Code is the stable, machine-readable error code clients should switch on.
type Code string

// The minimal code set introduced at M0/M2. M5 extends it to fully cover the
// error categories listed in Spec §5.7. M6 adds auth/quota codes.
const (
	CodeInvalidRequest         Code = "invalid_request"
	CodeNotFound               Code = "not_found"
	CodeMethodNotAllowed       Code = "method_not_allowed"
	CodeIdempotencyConflict    Code = "idempotency_conflict"
	CodeUnknownStation         Code = "unknown_station"
	CodeInvalidStateTransition Code = "invalid_state_transition"
	CodeCancellationUnsupported Code = "cancellation_unsupported"
	CodeReconciliationInProgress Code = "reconciliation_in_progress"
	CodeDependencyUnavailable  Code = "dependency_unavailable"
	CodeInputUnavailable       Code = "input_unavailable"
	CodeUnauthenticated        Code = "unauthenticated"
	CodeUnauthorized           Code = "unauthorized"
	CodeQuotaExceeded          Code = "quota_exceeded"
	CodeRateLimited            Code = "rate_limited"
	CodeInternal               Code = "internal"
)

// Body is the canonical JSON envelope.
type Body struct {
	Error Detail `json:"error"`
}

// Detail describes a single error.
type Detail struct {
	Code                 Code              `json:"code"`
	Message              string            `json:"message"`
	CorrelationID        string            `json:"correlation_id,omitempty"`
	Fields               []FieldError      `json:"fields,omitempty"`
	Details              map[string]string `json:"details,omitempty"`
	Retryable            *bool             `json:"retryable,omitempty"`
	Dependency           string            `json:"dependency,omitempty"`
	ConflictingResourceID string           `json:"conflicting_resource_id,omitempty"`
}

// FieldError describes one offending field for invalid_request responses.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Write renders the envelope to w with the supplied status. The correlation id
// from r's context (if any) is embedded automatically.
func Write(w http.ResponseWriter, r *http.Request, status int, code Code, message string) {
	WriteWithDetail(w, r, status, Detail{Code: code, Message: message})
}

// WriteWithDetail renders an explicit Detail (allowing field errors etc.).
func WriteWithDetail(w http.ResponseWriter, r *http.Request, status int, d Detail) {
	if d.CorrelationID == "" {
		d.CorrelationID = logging.CorrelationIDFromContext(r.Context())
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Body{Error: d})
}

// HTTPStatusFor returns a sensible default HTTP status for a given code.
// Handlers may override.
func HTTPStatusFor(code Code) int {
	switch code {
	case CodeInvalidRequest, CodeUnknownStation:
		return http.StatusBadRequest
	case CodeNotFound:
		return http.StatusNotFound
	case CodeMethodNotAllowed:
		return http.StatusMethodNotAllowed
	case CodeIdempotencyConflict, CodeInvalidStateTransition,
		CodeCancellationUnsupported, CodeReconciliationInProgress:
		return http.StatusConflict
	case CodeDependencyUnavailable:
		return http.StatusServiceUnavailable
	case CodeUnauthenticated:
		return http.StatusUnauthorized
	case CodeUnauthorized:
		return http.StatusForbidden
	case CodeQuotaExceeded, CodeRateLimited:
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}

// BoolPtr returns a pointer to b. Convenience for callers populating
// Detail.Retryable inline.
func BoolPtr(b bool) *bool { return &b }
