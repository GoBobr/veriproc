package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/eum/veriproc/internal/auth"
	"github.com/eum/veriproc/internal/httpapi/apierr"
)

// authMiddleware enforces bearer-token authentication on /api/v1/* paths
// (excluding /api/v1/health, /api/v1/readiness, /api/v1/version which remain
// open for platform probes). Spec §2.14 / M6.
//
// When authn is nil, the middleware is a no-op (M0–M5 compatibility).
func authMiddleware(authn auth.Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if authn == nil || !requiresAuth(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			tok := bearerToken(r.Header.Get("Authorization"))
			p, err := authn.Authenticate(r.Context(), tok)
			if err != nil {
				switch {
				case errors.Is(err, auth.ErrMissingCredentials):
					apierr.Write(w, r, http.StatusUnauthorized,
						apierr.CodeUnauthenticated, "missing bearer token")
				case errors.Is(err, auth.ErrInvalidToken):
					apierr.Write(w, r, http.StatusUnauthorized,
						apierr.CodeUnauthenticated, "invalid bearer token")
				default:
					apierr.Write(w, r, http.StatusUnauthorized,
						apierr.CodeUnauthenticated, "authentication failed")
				}
				return
			}
			// Reader role may not invoke mutating endpoints.
			if p.Role != auth.RoleOperator && auth.IsMutator(r.Method) {
				apierr.Write(w, r, http.StatusForbidden,
					apierr.CodeUnauthorized, "operator role required")
				return
			}
			ctx := auth.WithPrincipal(r.Context(), p)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// quotaMiddleware enforces per-principal mutating-request quotas. When q is
// nil, the middleware is a no-op.
func quotaMiddleware(q *auth.QuotaEnforcer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if q == nil || !auth.IsMutator(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			p := auth.PrincipalFromContext(r.Context())
			if p == nil {
				next.ServeHTTP(w, r)
				return
			}
			if !q.Allow(p) {
				apierr.WriteWithDetail(w, r, http.StatusTooManyRequests, apierr.Detail{
					Code:      apierr.CodeQuotaExceeded,
					Message:   "per-principal mutating-request quota exceeded",
					Retryable: apierr.BoolPtr(true),
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// requiresAuth reports whether the URL path is subject to bearer-token auth.
func requiresAuth(p string) bool {
	if !strings.HasPrefix(p, "/api/v1/") && p != "/api/v1" {
		return false
	}
	switch p {
	case "/api/v1/health", "/api/v1/readiness", "/api/v1/version":
		return false
	}
	return true
}

// bearerToken extracts the token from an "Authorization: Bearer …" header.
func bearerToken(h string) string {
	const pfx = "Bearer "
	if len(h) < len(pfx) || !strings.EqualFold(h[:len(pfx)], pfx) {
		return ""
	}
	return strings.TrimSpace(h[len(pfx):])
}
