// Package expert_tools_test exercises the in-memory adapter implementations
// of the expert-tooling repository ports + the in-memory event recorder.
package expert_tools_test

import (
	"context"
	"testing"

	expertinmem "github.com/apollo-chora/chora-creation/internal/adapter/inmem/expert_tools"
	gs "github.com/apollo-chora/chora-creation/internal/domain/expert_tools/golden_set"
	pr "github.com/apollo-chora/chora-creation/internal/domain/expert_tools/peer_review"
	qs "github.com/apollo-chora/chora-creation/internal/domain/expert_tools/quality_score"
)

const (
	tenantA = "01970000-0000-7000-8000-000000000001"
	tenantB = "01970000-0000-7000-8000-000000000002"
	gcidA   = "01970000-0000-7000-9000-000000000001"
	atomA   = "01970000-0000-7000-aaaa-000000000001"
	revA    = "01970000-0000-7000-bbbb-000000000001"
)

// -----------------------------------------------------------------------------
// PeerReviewRepository
// -----------------------------------------------------------------------------

func TestPeerReviewRepo_SaveGetRoundtrip(t *testing.T) {
	t.Parallel()
	repo := expertinmem.NewPeerReviewRepository()
	sub, _ := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revA, AuthoredBy: gcidA, QuorumSize: 2,
	})
	if err := repo.Save(context.Background(), sub); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := repo.Get(context.Background(), tenantA, sub.ReviewID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ReviewID != sub.ReviewID {
		t.Errorf("review_id mismatch")
	}
}

func TestPeerReviewRepo_GetReturnsNotFoundOnTenantMismatch(t *testing.T) {
	t.Parallel()
	repo := expertinmem.NewPeerReviewRepository()
	sub, _ := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revA, AuthoredBy: gcidA, QuorumSize: 2,
	})
	_ = repo.Save(context.Background(), sub)
	_, err := repo.Get(context.Background(), tenantB, sub.ReviewID)
	if err != pr.ErrNotFound {
		t.Errorf("err = %v; want ErrNotFound", err)
	}
}

func TestPeerReviewRepo_ListFiltersByStatusAndPaginates(t *testing.T) {
	t.Parallel()
	repo := expertinmem.NewPeerReviewRepository()
	for i := 0; i < 5; i++ {
		s, _ := pr.Submit(pr.SubmitParams{
			TenantID: tenantA, AtomID: atomA, RevisionID: revA, AuthoredBy: gcidA, QuorumSize: 2,
		})
		_ = repo.Save(context.Background(), s)
	}
	all, err := repo.List(context.Background(), tenantA, pr.ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 5 {
		t.Errorf("len = %d; want 5", len(all))
	}
	pending, _ := repo.List(context.Background(), tenantA, pr.ListFilter{Status: pr.StatusPending})
	if len(pending) != 5 {
		t.Errorf("pending = %d; want 5", len(pending))
	}
	approved, _ := repo.List(context.Background(), tenantA, pr.ListFilter{Status: pr.StatusApproved})
	if len(approved) != 0 {
		t.Errorf("approved = %d; want 0", len(approved))
	}
	limited, _ := repo.List(context.Background(), tenantA, pr.ListFilter{Limit: 2})
	if len(limited) != 2 {
		t.Errorf("limited = %d; want 2", len(limited))
	}
	offset, _ := repo.List(context.Background(), tenantA, pr.ListFilter{Offset: 4, Limit: 10})
	if len(offset) != 1 {
		t.Errorf("offset=4 result = %d; want 1", len(offset))
	}
	beyond, _ := repo.List(context.Background(), tenantA, pr.ListFilter{Offset: 100})
	if len(beyond) != 0 {
		t.Errorf("offset beyond data: got %d; want 0", len(beyond))
	}
}

// -----------------------------------------------------------------------------
// AnchorRepository
// -----------------------------------------------------------------------------

func TestAnchorRepo_SaveGetList(t *testing.T) {
	t.Parallel()
	repo := expertinmem.NewAnchorRepository()
	a, _ := gs.NewAnchor(gs.NewAnchorParams{
		TenantID: tenantA, CreatedBy: gcidA, AtomID: atomA,
		Title: "t", Body: "b", QualityScore: 0.9,
	})
	_ = repo.Save(context.Background(), a)
	got, err := repo.Get(context.Background(), tenantA, a.AnchorID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AnchorID != a.AnchorID {
		t.Errorf("anchor_id mismatch")
	}
	all, _ := repo.List(context.Background(), tenantA)
	if len(all) != 1 {
		t.Errorf("list = %d; want 1", len(all))
	}
}

func TestAnchorRepo_SoftDeletedHidden(t *testing.T) {
	t.Parallel()
	repo := expertinmem.NewAnchorRepository()
	a, _ := gs.NewAnchor(gs.NewAnchorParams{
		TenantID: tenantA, CreatedBy: gcidA, AtomID: atomA,
		Title: "t", Body: "b", QualityScore: 0.5,
	})
	a.SoftDelete()
	_ = repo.Save(context.Background(), a)
	_, err := repo.Get(context.Background(), tenantA, a.AnchorID)
	if err != gs.ErrNotFound {
		t.Errorf("err = %v; want ErrNotFound", err)
	}
	list, _ := repo.List(context.Background(), tenantA)
	if len(list) != 0 {
		t.Errorf("list = %d; want 0 (soft-deleted)", len(list))
	}
}

// -----------------------------------------------------------------------------
// QualityScoreRepository
// -----------------------------------------------------------------------------

func TestQualityScoreRepo_AppendLatestHistory(t *testing.T) {
	t.Parallel()
	repo := expertinmem.NewQualityScoreRepository()
	for i := 0; i < 3; i++ {
		s, _ := qs.New(qs.NewParams{
			TenantID: tenantA, AtomID: atomA, RevisionID: revA, ComputedBy: gcidA,
			Clarity: 0.5 + float64(i)*0.1, PedagogicalSoundness: 0.5,
			Fairness: 0.5, Accessibility: 0.5,
		})
		if err := repo.Append(context.Background(), s); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	latest, err := repo.Latest(context.Background(), tenantA, atomA)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if latest == nil {
		t.Fatalf("latest nil")
	}
	hist, _ := repo.History(context.Background(), tenantA, atomA)
	if len(hist) != 3 {
		t.Errorf("history = %d; want 3", len(hist))
	}
	// History is descending by ComputedAt
	if !hist[0].ComputedAt.After(hist[1].ComputedAt) && !hist[0].ComputedAt.Equal(hist[1].ComputedAt) {
		t.Errorf("history not descending")
	}
}

func TestQualityScoreRepo_LatestReturnsNotFoundOnEmpty(t *testing.T) {
	t.Parallel()
	repo := expertinmem.NewQualityScoreRepository()
	_, err := repo.Latest(context.Background(), tenantA, atomA)
	if err != qs.ErrNotFound {
		t.Errorf("err = %v; want ErrNotFound", err)
	}
}

// -----------------------------------------------------------------------------
// MemRecorder
// -----------------------------------------------------------------------------

func TestMemRecorder_PublishCaptures(t *testing.T) {
	t.Parallel()
	rec := expertinmem.NewMemRecorder()
	env := expertinmem.MemEnvelope{TenantID: tenantA, EventID: "evt-1", IdempotencyKey: "evt-1"}
	if err := rec.Publish(context.Background(), "topic.test", env, map[string]string{"k": "v"}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	events := rec.Events()
	if len(events) != 1 {
		t.Errorf("events = %d; want 1", len(events))
	}
	if events[0].Topic != "topic.test" {
		t.Errorf("topic = %q; want topic.test", events[0].Topic)
	}
}
