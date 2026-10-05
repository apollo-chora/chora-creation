package httpadapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

type stubTopicClassifier struct {
	tags []string
	err  error
}

func (s *stubTopicClassifier) ClassifyTopics(_ context.Context, _ atom.ClassifyTopicsRequest) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.tags, nil
}

func postBackfill(t *testing.T, h http.Handler, url string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, url, nil))
	return rec
}

func TestBackfillTopicTags_TenantRequired(t *testing.T) {
	h := NewRouterWithDeps(RouterDeps{
		Repo:            inmem.NewAtomRepository(),
		TopicClassifier: &stubTopicClassifier{tags: []string{"fractions"}},
	})
	rec := postBackfill(t, h, "/api/internal/atoms/backfill-topic-tags")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 without tenant_id, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestBackfillTopicTags_UnwiredClassifierFailsLoud(t *testing.T) {
	// no-stubs: the route must NOT report a successful run having classified
	// nothing. An unwired classifier is a loud 500.
	h := NewRouterWithDeps(RouterDeps{Repo: inmem.NewAtomRepository()})
	rec := postBackfill(t, h, "/api/internal/atoms/backfill-topic-tags?tenant_id=t1&dry_run=true")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 with an unwired classifier, got %d (%s)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !contains(body, "CREATION_TOPIC_CLASSIFIER_NOT_WIRED") {
		t.Fatalf("want the precise fail-loud envelope, got %s", body)
	}
}

func TestBackfillTopicTags_RealRunRequiresReemitSalt(t *testing.T) {
	// The plain deterministic idempotency key is permanently deduped by the
	// outbox for an already-published revision, so an UNSALTED re-emit would be
	// swallowed and nothing would re-project. Refuse rather than run a backfill
	// that silently no-ops.
	h := NewRouterWithDeps(RouterDeps{
		Repo:            inmem.NewAtomRepository(),
		TopicClassifier: &stubTopicClassifier{tags: []string{"fractions"}},
	})
	rec := postBackfill(t, h, "/api/internal/atoms/backfill-topic-tags?tenant_id=t1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 without reemit_salt on a real run, got %d (%s)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !contains(body, "CREATION_REEMIT_SALT_REQUIRED") {
		t.Fatalf("want CREATION_REEMIT_SALT_REQUIRED, got %s", body)
	}
}

func TestBackfillTopicTags_DryRunNeedsNoSalt(t *testing.T) {
	// CHO-2159: a dry run is now ACCEPTED (202) and executed detached — the
	// report is polled, not held open past the 15s WriteTimeout. The salt rule
	// is unchanged: meaningless on a dry run, mandatory on a real one.
	runs := newStubBackfillRuns()
	rec := postBackfill(t, asyncRouter(runs), "/api/internal/atoms/backfill-topic-tags?tenant_id=t1&dry_run=true")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202 on dry run, got %d (%s)", rec.Code, rec.Body.String())
	}
	ack := decodeAck(t, rec)
	if !ack.DryRun {
		t.Fatal("the ack must flag dry_run")
	}
}

func TestBackfillTopicTags_RejectsBadSalt(t *testing.T) {
	h := NewRouterWithDeps(RouterDeps{
		Repo:            inmem.NewAtomRepository(),
		TopicClassifier: &stubTopicClassifier{tags: []string{"fractions"}},
	})
	rec := postBackfill(t, h, "/api/internal/atoms/backfill-topic-tags?tenant_id=t1&reemit_salt=bad%20salt")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 on an invalid salt, got %d", rec.Code)
	}
}

func TestBackfillTopicTags_RejectsBadLimit(t *testing.T) {
	h := NewRouterWithDeps(RouterDeps{
		Repo:            inmem.NewAtomRepository(),
		TopicClassifier: &stubTopicClassifier{tags: []string{"fractions"}},
	})
	rec := postBackfill(t, h, "/api/internal/atoms/backfill-topic-tags?tenant_id=t1&dry_run=true&limit=-1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 on a negative limit, got %d", rec.Code)
	}
}

func TestBackfillTopicTags_MethodNotAllowed(t *testing.T) {
	h := NewRouterWithDeps(RouterDeps{
		Repo:            inmem.NewAtomRepository(),
		TopicClassifier: &stubTopicClassifier{tags: []string{"fractions"}},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/internal/atoms/backfill-topic-tags?tenant_id=t1", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405 on GET, got %d", rec.Code)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
