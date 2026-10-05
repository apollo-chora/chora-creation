// Package httpadapter wires net/http handlers to the LearningAtom domain.
//
// Middleware:
//   - logging: per-request method/path log + structured trace correlation.
//   - tenantContext: extracts X-Tenant-Id and gcid from headers; rejects
//     /api/* requests that lack either — EXCEPT cluster-internal /api/internal/*
//     endpoints, which are not exposed via the public gateway and carry their
//     tenant explicitly in the request (query/body).
package httpadapter

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/apollo-chora/chora-common/tracing"
)

type ctxKey string

const (
	ctxKeyTenantID    ctxKey = "tenant_id"
	ctxKeyGcid        ctxKey = "gcid"
	ctxKeyReqID       ctxKey = "request_id"
	ctxKeyTraceparent ctxKey = "traceparent"
)

// tenantContext extracts tenant_id + gcid from headers and stores them on the
// request context. Rejects protected paths (/api/*) that omit either header
// with HTTP 400 + a JSON error envelope.
func tenantContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Health endpoints bypass tenant extraction.
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
		gcid := strings.TrimSpace(r.Header.Get("gcid"))
		if gcid == "" {
			gcid = strings.TrimSpace(r.Header.Get("X-Chora-GCID"))
		}

		if tenantID == "" {
			writeError(w, http.StatusBadRequest, "CREATION_TENANT_REQUIRED",
				"X-Tenant-Id header is required")
			return
		}
		if gcid == "" {
			writeError(w, http.StatusBadRequest, "CREATION_GCID_REQUIRED",
				"gcid header is required")
			return
		}

		ctx := r.Context()
		ctx = context.WithValue(ctx, ctxKeyTenantID, tenantID)
		ctx = context.WithValue(ctx, ctxKeyGcid, gcid)
		if tp := strings.TrimSpace(r.Header.Get("traceparent")); tp != "" {
			ctx = context.WithValue(ctx, ctxKeyTraceparent, tp)
		}
		// ADR-191 O3 — bridge the caller's mesh role set onto the tracing
		// context so the pg tenant-tx seam emits SET LOCAL chora.user_roles and
		// the restrictive exam_content_embargo RLS policy can match a PROCTOR
		// claim. Backward-compatible: absent header ⇒ nothing set ⇒ existing
		// tenant-only reads unaffected.
		if roles := meshRolesForRLS(r); roles != "" {
			ctx = tracing.WithUserRoles(ctx, roles)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// meshRolesForRLS extracts the caller's mesh role set from `x-mesh-user-roles`
// as a sanitised, lowercase, comma-joined token list safe for a SET LOCAL
// (mirrors chora-gateway/chora-delivery meshRolesForRLS + pg.validateUserRoles):
// only [a-z0-9-_] tokens survive; spaces/empties are dropped. Empty when no
// roles header is present.
func meshRolesForRLS(r *http.Request) string {
	raw := strings.ToLower(strings.TrimSpace(r.Header.Get("x-mesh-user-roles")))
	if raw == "" {
		return ""
	}
	clean := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		ok := true
		for _, c := range p {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				ok = false
				break
			}
		}
		if ok {
			clean = append(clean, p)
		}
	}
	return strings.Join(clean, ",")
}

// logging logs each request once it has dispatched.
func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gcid := r.Header.Get("gcid")
		if gcid == "" {
			gcid = r.Header.Get("X-Chora-GCID")
		}
		if gcid != "" {
			log.Printf("method=%s path=%s gcid=%s", r.Method, r.URL.Path, gcid)
		} else {
			log.Printf("method=%s path=%s", r.Method, r.URL.Path)
		}
		next.ServeHTTP(w, r)
	})
}

func isPublicPath(p string) bool {
	switch p {
	case "/", "/healthz", "/healthz/", "/health", "/readyz":
		return true
	}
	// Cluster-internal endpoints (/api/internal/*) are NOT routed through the
	// public gateway and carry their tenant explicitly in the request body /
	// query string, so they bypass the X-Tenant-Id / gcid header gate. The
	// handlers themselves enforce a REQUIRED tenant (fail-loud 400 if absent).
	if strings.HasPrefix(p, "/api/internal/") {
		return true
	}
	return false
}

// tenantFromContext returns the tenant_id stored by tenantContext; empty if absent.
func tenantFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyTenantID).(string)
	return v
}

// gcidFromContext returns the gcid stored by tenantContext; empty if absent.
func gcidFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyGcid).(string)
	return v
}

// effectiveTraceparent returns the W3C traceparent for an inbound request.
//
// Trace context is established upstream by commonobs.HTTPMiddleware
// (== tracing.Middleware), which mints a valid root traceparent when the
// client omits one and stamps it on the request context. Handlers MUST read
// the traceparent from there rather than re-reading the raw header: the event
// envelope's traceparent is a mandatory field, and a raw-header read yields ""
// when the client omits it — which the Pub/Sub outbox publisher rejects,
// stranding the event as `failed` (the OPEN-1 atom_index-empty root cause).
// Falls back to the inbound header (ensuring validity, minting if absent or
// malformed) for call sites reached without the tracing middleware, e.g. tests.
func effectiveTraceparent(r *http.Request) string {
	if tp := tracing.TraceparentFromContext(r.Context()); tp != "" {
		return tp
	}
	return tracing.EnsureTraceparent(r.Header.Get(tracing.HeaderTraceparent))
}
