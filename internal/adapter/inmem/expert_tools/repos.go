// Package expert_tools provides in-memory adapter implementations of the
// expert-tooling repository ports + an in-memory event recorder. Used in
// tests + the M11 skeleton; replaced by Cloud SQL + Cloud Pub/Sub
// publishers in M12.
//
// Hexagonal: this is an OUTER layer. The domain packages
// (internal/domain/expert_tools/*) own the ports; this package implements
// them.
package expert_tools

import (
	"context"
	"sort"
	"sync"
	"time"

	gs "github.com/apollo-chora/chora-creation/internal/domain/expert_tools/golden_set"
	pr "github.com/apollo-chora/chora-creation/internal/domain/expert_tools/peer_review"
	qs "github.com/apollo-chora/chora-creation/internal/domain/expert_tools/quality_score"
)

// -----------------------------------------------------------------------------
// PeerReviewRepository
// -----------------------------------------------------------------------------

// PeerReviewRepository is an in-memory implementation of pr.Repository.
type PeerReviewRepository struct {
	mu      sync.RWMutex
	reviews map[string]*pr.ReviewSubmission // keyed by ReviewID
}

// NewPeerReviewRepository constructs an empty in-memory repository.
func NewPeerReviewRepository() *PeerReviewRepository {
	return &PeerReviewRepository{reviews: make(map[string]*pr.ReviewSubmission)}
}

// Save persists or updates the submission keyed by ReviewID.
func (r *PeerReviewRepository) Save(_ context.Context, sub *pr.ReviewSubmission) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	clone := *sub
	clone.Votes = append([]*pr.Vote(nil), sub.Votes...)
	r.reviews[sub.ReviewID] = &clone
	return nil
}

// Get returns the submission for (tenantID, reviewID) or pr.ErrNotFound.
func (r *PeerReviewRepository) Get(_ context.Context, tenantID, reviewID string) (*pr.ReviewSubmission, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sub, ok := r.reviews[reviewID]
	if !ok || sub.TenantID != tenantID {
		return nil, pr.ErrNotFound
	}
	clone := *sub
	clone.Votes = append([]*pr.Vote(nil), sub.Votes...)
	return &clone, nil
}

// List returns submissions for a tenant, optionally filtered by status.
func (r *PeerReviewRepository) List(_ context.Context, tenantID string, f pr.ListFilter) ([]*pr.ReviewSubmission, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*pr.ReviewSubmission, 0)
	for _, s := range r.reviews {
		if s.TenantID != tenantID {
			continue
		}
		if f.Status != "" && s.Status != f.Status {
			continue
		}
		clone := *s
		clone.Votes = append([]*pr.Vote(nil), s.Votes...)
		out = append(out, &clone)
	}
	// Determinism: order by CreatedAt ascending.
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if f.Offset > 0 {
		if f.Offset >= len(out) {
			return []*pr.ReviewSubmission{}, nil
		}
		out = out[f.Offset:]
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// AnchorRepository (golden set)
// -----------------------------------------------------------------------------

// AnchorRepository is an in-memory implementation of gs.Repository.
type AnchorRepository struct {
	mu      sync.RWMutex
	anchors map[string]*gs.Anchor // keyed by AnchorID
}

// NewAnchorRepository constructs an empty in-memory anchor repository.
func NewAnchorRepository() *AnchorRepository {
	return &AnchorRepository{anchors: make(map[string]*gs.Anchor)}
}

// Save persists or updates the anchor.
func (r *AnchorRepository) Save(_ context.Context, a *gs.Anchor) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	clone := *a
	clone.Tags = append([]string(nil), a.Tags...)
	r.anchors[a.AnchorID] = &clone
	return nil
}

// Get returns the active anchor for (tenantID, anchorID) or gs.ErrNotFound.
func (r *AnchorRepository) Get(_ context.Context, tenantID, anchorID string) (*gs.Anchor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.anchors[anchorID]
	if !ok || a.TenantID != tenantID || !a.IsActive() {
		return nil, gs.ErrNotFound
	}
	clone := *a
	clone.Tags = append([]string(nil), a.Tags...)
	return &clone, nil
}

// List returns active anchors for the given tenant.
func (r *AnchorRepository) List(_ context.Context, tenantID string) ([]*gs.Anchor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*gs.Anchor, 0)
	for _, a := range r.anchors {
		if a.TenantID != tenantID || !a.IsActive() {
			continue
		}
		clone := *a
		clone.Tags = append([]string(nil), a.Tags...)
		out = append(out, &clone)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// -----------------------------------------------------------------------------
// QualityScoreRepository (append-only)
// -----------------------------------------------------------------------------

// QualityScoreRepository is an in-memory append-only implementation of
// qs.Repository.
type QualityScoreRepository struct {
	mu     sync.RWMutex
	scores []*qs.QualityScore
}

// NewQualityScoreRepository constructs an empty repository.
func NewQualityScoreRepository() *QualityScoreRepository {
	return &QualityScoreRepository{scores: make([]*qs.QualityScore, 0, 16)}
}

// Append persists a fresh score record.
func (r *QualityScoreRepository) Append(_ context.Context, q *qs.QualityScore) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	clone := *q
	r.scores = append(r.scores, &clone)
	return nil
}

// Latest returns the most recent score for (tenantID, atomID), or qs.ErrNotFound.
func (r *QualityScoreRepository) Latest(_ context.Context, tenantID, atomID string) (*qs.QualityScore, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var latest *qs.QualityScore
	for _, s := range r.scores {
		if s.TenantID != tenantID || s.AtomID != atomID {
			continue
		}
		if latest == nil || s.ComputedAt.After(latest.ComputedAt) {
			latest = s
		}
	}
	if latest == nil {
		return nil, qs.ErrNotFound
	}
	clone := *latest
	return &clone, nil
}

// History returns all scores for (tenantID, atomID), descending.
func (r *QualityScoreRepository) History(_ context.Context, tenantID, atomID string) ([]*qs.QualityScore, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*qs.QualityScore, 0)
	for _, s := range r.scores {
		if s.TenantID != tenantID || s.AtomID != atomID {
			continue
		}
		clone := *s
		out = append(out, &clone)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ComputedAt.After(out[j].ComputedAt) })
	return out, nil
}

// -----------------------------------------------------------------------------
// MemRecorder — in-memory EventPublisher
// -----------------------------------------------------------------------------

// MemEnvelope mirrors chora.common.v1.EventEnvelope mandatory fields per
// chora-contracts/proto/common/envelope.proto. This is a minimal stand-in
// for the proto message until M11.4 lands the generated Go code.
type MemEnvelope struct {
	EventID        string
	IdempotencyKey string
	TenantID       string
	Gcid           string
	OccurredAt     time.Time
	PublishedAt    time.Time
	Traceparent    string
	Tracestate     string
	SourceProject  string
	SourceService  string
	SchemaVersion  int32
}

// MemRecordedEvent is one captured publish call.
type MemRecordedEvent struct {
	Topic    string
	Envelope MemEnvelope
	Payload  any
}

// MemRecorder records publish calls in-memory.
type MemRecorder struct {
	mu     sync.Mutex
	events []MemRecordedEvent
}

// NewMemRecorder returns an empty MemRecorder.
func NewMemRecorder() *MemRecorder {
	return &MemRecorder{events: make([]MemRecordedEvent, 0, 8)}
}

// Publish appends an event. Always succeeds.
func (m *MemRecorder) Publish(_ context.Context, topic string, env MemEnvelope, payload any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, MemRecordedEvent{Topic: topic, Envelope: env, Payload: payload})
	return nil
}

// Events returns a snapshot copy of all recorded events.
func (m *MemRecorder) Events() []MemRecordedEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]MemRecordedEvent, len(m.events))
	copy(out, m.events)
	return out
}
