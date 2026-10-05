// AI Assist subscriber per audit `docs/m13/audit-content-fillgaps.md` §3.1.
//
// Topic subscribed: chora.ai_kernel.crew.atoms_ready.v1 — emitted by the
// chora-ai-kernel-orchestrator (S3.4) when the AI Assist crew finalises a
// batch of atoms. Each atom carries a Reporter governance verdict
// (`approved` / `remediated` / `blocked`).
//
// Pipeline:
//
//  1. Validate event fields (tenant_id, course_id, content_type, ...).
//  2. De-dup on event_id (or business idempotency_key) — a re-delivery of
//     the SAME event must NOT double-persist. Backed by
//     `libs/chora-go-common/idempotent.Store` (W1.8, 2026-05-12): production
//     wires PostgresStore against chora_creation idempotency_keys; dev /
//     tests use MemoryStore. The store survives pod-death and is shared
//     across replicas — the previous in-process seen map did NOT.
//  3. For each atom in the payload:
//     a. Skip if governance_status = "blocked".
//     b. Construct a LearningAtom via NewBound with SourceType=ai_assist
//     and source_metadata carrying the Reporter trail.
//     c. Persist via Repository.
//     d. Emit chora.creation.atom.created.v1 with the canonical IMDA
//     accountability label (D1 per ADR-141) + lifecycle_stage=runtime.
//
// Domain stays adapter-free: ports come from this package + idempotent.Store
// from chora-go-common.
package atom

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
)

// AtomsReadyInboxTTL is the dedupe-key retention window for the AI Assist
// subscriber's inbox. 24h covers Pub/Sub's max redelivery window reduced for
// the AI Assist crew's typical end-to-end latency. Production may tune per
// deployment via WithInboxTTL.
const AtomsReadyInboxTTL = 24 * time.Hour

// AtomsReadyAtom is one atom in the inbound batch.
type AtomsReadyAtom struct {
	Title            string `json:"title"`
	Body             string `json:"body"`
	GovernanceStatus string `json:"governance_status"` // approved | remediated | blocked
	GuardrailRemarks string `json:"guardrail_remarks,omitempty"`
}

// AtomsReadyEvent is the payload of chora.ai_kernel.crew.atoms_ready.v1.
type AtomsReadyEvent struct {
	EventID        string           `json:"event_id"`
	IdempotencyKey string           `json:"idempotency_key"`
	TenantID       string           `json:"tenant_id"`
	Gcid           string           `json:"gcid"`
	CourseID       string           `json:"course_id"`
	Traceparent    string           `json:"traceparent,omitempty"`
	Tracestate     string           `json:"tracestate,omitempty"`
	ContentType    string           `json:"content_type"` // mcq | flashcard | ...
	Difficulty     int              `json:"difficulty"`
	ModelUsed      string           `json:"model_used,omitempty"`
	PromptHash     string           `json:"prompt_hash,omitempty"`
	OrchestratorID string           `json:"orchestrator_run_id,omitempty"`
	Atoms          []AtomsReadyAtom `json:"atoms"`
}

// AtomsReadyResult is the subscriber's processing summary.
type AtomsReadyResult struct {
	PersistedAtomIDs []string `json:"persisted_atom_ids"`
	SkippedBlocked   int      `json:"skipped_blocked"`
	AlreadyProcessed bool     `json:"already_processed"`
}

// AtomsReadySubscriber is the domain orchestrator. Production wiring binds it
// to a Pub/Sub Subscription receiver in chora-creation/cmd/server.
//
// Idempotency (W1.8): keyed on IdempotencyKey || EventID. Production wires
// `idempotent.PostgresStore` against the chora_creation database's
// idempotency_keys table; dev / tests use MemoryStore. Re-delivery within
// AtomsReadyInboxTTL hits the same key + returns AlreadyProcessed=true
// without re-running the handler body.
type AtomsReadySubscriber struct {
	repo  Repository
	pub   EventPublisher
	inbox idempotent.Store
	ttl   time.Duration

	mu sync.Mutex
}

// NewAtomsReadySubscriber wires the subscriber.
//
// inbox is OPTIONAL — passing nil triggers a defensive MemoryStore fallback
// for backward compatibility with test fixtures that pre-date W1.8.
// Production callers SHOULD pass a PostgresStore-backed inbox so dedup
// survives pod restart + works across replicas.
func NewAtomsReadySubscriber(repo Repository, pub EventPublisher, inbox idempotent.Store) *AtomsReadySubscriber {
	if inbox == nil {
		inbox = idempotent.NewMemoryStore()
	}
	return &AtomsReadySubscriber{
		repo:  repo,
		pub:   pub,
		inbox: inbox,
		ttl:   AtomsReadyInboxTTL,
	}
}

// WithInboxTTL overrides the default dedupe-key retention window. Mostly
// useful in tests that want a short TTL to exercise expiry / reprocess.
func (s *AtomsReadySubscriber) WithInboxTTL(ttl time.Duration) *AtomsReadySubscriber {
	if s == nil || ttl <= 0 {
		return s
	}
	s.ttl = ttl
	return s
}

// Handle processes one inbound event. Safe to invoke concurrently.
//
// Idempotency: dedupe key is IdempotencyKey || EventID, scoped by the
// "atoms_ready:" prefix to avoid collision with other subscribers in the
// same idempotency_keys table. A duplicate re-delivery returns
// AtomsReadyResult{AlreadyProcessed: true}, nil and does NOT re-persist /
// re-publish.
//
// Per the no-inline-config rule, this domain-level subscriber does not depend
// on any infra config — the calling adapter (Pub/Sub Receiver) supplies the
// context with tenant + traceparent stamped on it.
func (s *AtomsReadySubscriber) Handle(ctx context.Context, ev AtomsReadyEvent) (AtomsReadyResult, error) {
	if err := validateAtomsReady(ev); err != nil {
		return AtomsReadyResult{}, err
	}

	dedupKey := ev.IdempotencyKey
	if dedupKey == "" {
		dedupKey = ev.EventID
	}
	key := "atoms_ready:" + dedupKey

	var res AtomsReadyResult
	var ran bool
	processErr := s.inbox.Process(ctx, key, s.ttl, func() error {
		ran = true
		out, err := s.processBatch(ctx, ev)
		if err != nil {
			return err
		}
		res = out
		return nil
	})
	if processErr != nil {
		return res, processErr
	}
	if !ran {
		return AtomsReadyResult{AlreadyProcessed: true}, nil
	}
	return res, nil
}

// processBatch is the inner handler body — runs once per unique event under
// the inbox guard. Persists atoms + publishes atom.created.v1 events.
func (s *AtomsReadySubscriber) processBatch(ctx context.Context, ev AtomsReadyEvent) (AtomsReadyResult, error) {
	res := AtomsReadyResult{
		PersistedAtomIDs: make([]string, 0, len(ev.Atoms)),
	}
	srcType := SourceAIAssist
	atomType := AtomType(ev.ContentType)

	for i, a := range ev.Atoms {
		gs := strings.ToLower(strings.TrimSpace(a.GovernanceStatus))
		if gs == "blocked" {
			res.SkippedBlocked++
			continue
		}
		md := map[string]string{
			"governance_status":   gs,
			"guardrail_remarks":   a.GuardrailRemarks,
			"orchestrator_run_id": ev.OrchestratorID,
			"model_used":          ev.ModelUsed,
			"prompt_hash":         ev.PromptHash,
			"upstream_event_id":   ev.EventID,
		}
		la, err := NewBound(NewBoundParams{
			TenantID:       ev.TenantID,
			Gcid:           ev.Gcid,
			CourseID:       ev.CourseID,
			Title:          a.Title,
			Body:           a.Body,
			AtomType:       atomType,
			Difficulty:     ev.Difficulty,
			SourceType:     srcType,
			SourceMetadata: md,
		})
		if err != nil {
			return res, fmt.Errorf("constructing atom #%d: %w", i, err)
		}
		if err := s.repo.Save(ctx, la); err != nil {
			return res, fmt.Errorf("persisting atom #%d: %w", i, err)
		}
		res.PersistedAtomIDs = append(res.PersistedAtomIDs, la.AtomID)

		out := newAtomCreatedEvent(la, ev.Traceparent, ev.Tracestate)
		// IMDA evidence per ADR-141 — accountability (D1) at runtime stage.
		out.ChoraImdaDimension = "accountability"
		out.ImdaLifecycleStage = "runtime"

		if err := s.pub.Publish(ctx, out); err != nil {
			return res, fmt.Errorf("publishing atom.created event for #%d: %w", i, err)
		}
	}
	return res, nil
}

func validateAtomsReady(e AtomsReadyEvent) error {
	if strings.TrimSpace(e.EventID) == "" {
		return errors.New("event_id is required")
	}
	if strings.TrimSpace(e.TenantID) == "" {
		return errors.New("tenant_id is required")
	}
	if strings.TrimSpace(e.Gcid) == "" {
		return errors.New("gcid is required")
	}
	if strings.TrimSpace(e.CourseID) == "" {
		return errors.New("course_id is required")
	}
	if !AtomType(e.ContentType).Valid() {
		return fmt.Errorf("invalid content_type: %q", e.ContentType)
	}
	if e.Difficulty < 0 || e.Difficulty > 5 {
		return fmt.Errorf("difficulty out of range: %d", e.Difficulty)
	}
	return nil
}
