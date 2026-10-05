// Trace-context establishment on the Phyllis atom-create path.
//
// Regression guard for the OPEN-1 data-fix root cause (2026-06-01): the
// atom-create handler read the raw inbound `traceparent` header, so a client
// that omitted it produced an event envelope with traceparent="". The Pub/Sub
// outbox publisher rejects that (W3C traceparent is a mandatory envelope
// field), stranding chora.creation.atom.created.v1 events as `failed` and
// leaving chora-consumption's atom_index empty.
//
// The DDD-correct contract: the inbound HTTP adapter (commonobs.HTTPMiddleware
// == tracing.Middleware) establishes a valid traceparent on the request
// context; handlers consume it from there (with a mint fallback), never
// emitting an empty/invalid traceparent.
package httpadapter_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// When the inbound request carries NO traceparent header, the published
// atom.created event MUST still carry a valid, non-empty W3C traceparent.
func TestPhyllis_PostAtoms_MintsTraceparentWhenAbsent(t *testing.T) {
	t.Parallel()

	pub := &recordingPub{}
	srv, _ := newPhyllisServer(t, nil, pub)

	r := httptest.NewRequest(http.MethodPost, "/v1/atoms", strings.NewReader(`{
		"course_id":"`+courseA+`","title":"x","body":"y","type":"mcq","difficulty":1
	}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Tenant-Id", tenantA)
	r.Header.Set("gcid", gcidA)
	// Deliberately NO traceparent header — the client omitted it.
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	if len(pub.events) != 1 {
		t.Fatalf("published events = %d; want 1", len(pub.events))
	}
	if tp := pub.events[0].TraceParent; !isValidW3CTraceparent(tp) {
		t.Errorf("published event TraceParent = %q; want a valid non-empty W3C traceparent (envelope mandates it)", tp)
	}
}

// isValidW3CTraceparent mirrors the structural check the envelope/publisher
// enforces: 4 dash chunks of lengths 2-32-16-2, all hex, non-zero trace+span.
func isValidW3CTraceparent(s string) bool {
	parts := strings.Split(s, "-")
	if len(parts) != 4 {
		return false
	}
	for i, want := range []int{2, 32, 16, 2} {
		if len(parts[i]) != want {
			return false
		}
		if strings.TrimLeft(parts[i], "0123456789abcdefABCDEF") != "" {
			return false
		}
	}
	return strings.Trim(parts[1], "0") != "" && strings.Trim(parts[2], "0") != ""
}
