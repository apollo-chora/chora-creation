// orphan_required_subscriber.go — ADR-229 Amendment A1 (CHO-2132): the
// creation leg of the two-leg orphan saga.
//
// chora-sharing owns the AtomUsageGrant table (the authoritative registry of
// consumed reuse) and publishes chora.sharing.atom_reuse.orphan_required.v1
// when a withdrawal (reuse-visibility narrowing / archive) strands >=1 active
// grant. This subscriber consumes it and:
//
//  1. loads the withdrawn atom + its LAST-PUBLISHED revision AUTHORITATIVELY
//     in-domain via the any-state loaders (the archive cascade soft-deletes
//     both the atom and its question BEFORE this event arrives; the event's
//     revision_id is only an observability hint),
//  2. mints the SINGLETON immutable orphan edition per (atom,
//     last-published-revision) — DB-enforced by the partial unique index from
//     migration 0030; a conflict returns the existing orphan,
//  3. clones the question + latest revision onto the orphan (heal-on-
//     redelivery: a torn write from a crash between the atom insert and the
//     question save is repaired on the conflict path),
//  4. repoints creation's OWN non-author collection entries (same DB,
//     idempotent — the author's entries keep the live atom),
//  5. publishes chora.creation.atom.orphan_created.v1 via the durable outbox
//     with the DETERMINISTIC idempotency key (orphaned_from + ":orphan_created:"
//     + source_revision). The publish is attempted on BOTH the inserted and
//     the conflict path: outbox UNIQUE(idempotency_key) collapses it to
//     exactly one event (ErrDuplicateIdempotencyKey == already enqueued ==
//     success), which simultaneously guarantees "no duplicate orphan_created"
//     AND heals a crash that committed the mint but lost the enqueue.
//
// Errors are returned (NACK → broker retry → DLQ when configured) — a silent
// ack would strand consumers forever. The mint itself never re-emits
// atom.published.v1 and never touches the embedding index: orphans are NOT
// discoverable — reachable only via repointed grants.
package pubsub

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/apollo-chora/chora-common/eventbus"

	sharingv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/sharing/v1"
	"github.com/apollo-chora/chora-creation/internal/adapter/outbox"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

// TopicAtomReuseOrphanRequired is the consumed sharing topic.
const TopicAtomReuseOrphanRequired = "chora.sharing.atom_reuse.orphan_required.v1"

// Consumed event topics for the ai-assist terminal + grading subscribers.
const (
	TopicAiAssistCompleted      = "chora.creation.ai_assist.completed.v1"
	TopicAiAssistRefused        = "chora.creation.ai_assist.refused.v1"
	TopicAiAssistProgress       = "chora.creation.ai_assist.progress.v1"
	TopicAiAssistChunkCompleted = "chora.creation.ai_assist.chunk_completed.v1"
	TopicModelAnswerAmended     = "chora.delivery.grading.model_answer_amended.v1"
)

// DefaultOrphanRequiredSubscription is the consumer-owned pull-subscription
// name (chora-{service}-{purpose} convention); override via
// CHORA_ORPHAN_REQUIRED_SUBSCRIPTION.
const DefaultOrphanRequiredSubscription = "chora-creation-atom-reuse-orphan-required"

// OrphanRequiredEvent is the decoded orphan_required.v1 payload + envelope.
type OrphanRequiredEvent struct {
	EventID  string
	TenantID string
	GCID     string

	AtomID             string
	RevisionID         string // sharing's pinned-revision HINT (observability only)
	Trigger            string // narrowed | unshared | archived
	StrandedGrantCount int32

	Traceparent string
	Tracestate  string
}

// OrphanAtomRepository is the creation-side atom port for the mint.
type OrphanAtomRepository interface {
	// GetAnyState loads the atom INCLUDING soft-deleted rows (archive trigger).
	GetAnyState(ctx context.Context, tenantID, atomID string) (*atom.LearningAtom, error)
	// MintOrphan idempotently persists the singleton orphan; conflict returns
	// the existing edition with inserted=false.
	MintOrphan(ctx context.Context, o *atom.LearningAtom) (*atom.LearningAtom, bool, error)
	// RepointCollections repoints creation's own non-author collection
	// entries original→orphan (idempotent).
	RepointCollections(ctx context.Context, tenantID, originalAtomID, orphanAtomID, authorGCID string) (int, error)
}

// OrphanQuestionRepository is the question port for the content clone.
type OrphanQuestionRepository interface {
	// GetByAtomIDAnyState loads the question + LATEST revision INCLUDING
	// soft-deleted questions (the archive cascade runs before this event).
	GetByAtomIDAnyState(ctx context.Context, tenantID, atomID string) (*question.Question, *question.QuestionRevision, error)
	// Save persists a new question + its first revision.
	Save(ctx context.Context, q *question.Question, rev *question.QuestionRevision) error
}

// OrphanRequiredConfig bundles the subscriber's dependencies.
type OrphanRequiredConfig struct {
	Atoms     OrphanAtomRepository
	Questions OrphanQuestionRepository
	Publisher atom.EventPublisher
	Logger    *log.Logger
}

// OrphanRequiredSubscriber handles orphan_required.v1 deliveries.
type OrphanRequiredSubscriber struct {
	cfg OrphanRequiredConfig
}

// NewOrphanRequiredSubscriber constructs the subscriber.
func NewOrphanRequiredSubscriber(cfg OrphanRequiredConfig) *OrphanRequiredSubscriber {
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	return &OrphanRequiredSubscriber{cfg: cfg}
}

// HandleOrphanRequired processes one orphan_required.v1 event. Idempotent by
// construction: the mint converges on the DB singleton, the question clone
// heals on redelivery, the collection repoint matches zero rows on re-run,
// and the orphan_created enqueue dedupes on its deterministic key.
func (s *OrphanRequiredSubscriber) HandleOrphanRequired(ctx context.Context, ev OrphanRequiredEvent) error {
	if s.cfg.Atoms == nil || s.cfg.Questions == nil || s.cfg.Publisher == nil {
		return errors.New("orphan_required: missing dependency (Atoms | Questions | Publisher)")
	}
	if strings.TrimSpace(ev.TenantID) == "" {
		return errors.New("orphan_required: tenant_id required on the envelope")
	}
	if strings.TrimSpace(ev.AtomID) == "" {
		return errors.New("orphan_required: atom_id required")
	}
	if !atom.ValidOrphanTrigger(ev.Trigger) {
		return fmt.Errorf("orphan_required: invalid trigger %q (want narrowed|unshared|archived)", ev.Trigger)
	}

	// 1. Load the withdrawn original — INCLUDING soft-deleted (archive path).
	original, err := s.cfg.Atoms.GetAnyState(ctx, ev.TenantID, ev.AtomID)
	if err != nil {
		return fmt.Errorf("orphan_required: load atom %s: %w", ev.AtomID, err)
	}
	if original.IsOrphan() {
		// Can't-happen state: orphans are frozen (never narrowed/archived) so
		// no withdrawal event can legitimately target one. NACK → DLQ parks
		// it visibly rather than minting an orphan-of-orphan.
		return fmt.Errorf("orphan_required: target atom %s is itself an orphan edition (poison event)", ev.AtomID)
	}

	// 2. Authoritative last-published revision = the question's LATEST
	// revision (revisions auto-publish at PATCH/POST per A22 Option α). The
	// event's revision_id is only sharing's projection hint.
	q, rev, err := s.cfg.Questions.GetByAtomIDAnyState(ctx, ev.TenantID, ev.AtomID)
	if err != nil {
		return fmt.Errorf("orphan_required: load question for atom %s (continuity needs content): %w", ev.AtomID, err)
	}
	if rev == nil {
		return fmt.Errorf("orphan_required: atom %s has a question but no revision (corrupt aggregate)", ev.AtomID)
	}
	sourceRevisionID := rev.RevisionID
	if hint := strings.TrimSpace(ev.RevisionID); hint != "" && hint != sourceRevisionID {
		s.cfg.Logger.Printf(`{"event":"orphan_required_revision_hint_diverged","atom_id":"%s","hint":"%s","authoritative":"%s"}`,
			ev.AtomID, hint, sourceRevisionID)
	}

	// 3. Build + mint the singleton orphan.
	candidate, err := atom.CloneOrphan(atom.OrphanCloneParams{
		Source:           original,
		SourceRevisionID: sourceRevisionID,
	})
	if err != nil {
		return fmt.Errorf("orphan_required: clone orphan for atom %s: %w", ev.AtomID, err)
	}
	// OE atoms may carry their prompt on the question, not atom.Stem (mirror
	// the clone handler's derivation so the orphan renders a stem).
	if strings.TrimSpace(candidate.Stem) == "" && strings.TrimSpace(q.Prompt) != "" {
		candidate.Stem = q.Prompt
	}

	orphan, inserted, err := s.cfg.Atoms.MintOrphan(ctx, candidate)
	if err != nil {
		return fmt.Errorf("orphan_required: mint orphan for (%s, %s): %w", ev.AtomID, sourceRevisionID, err)
	}

	// 4. Clone the question onto the orphan. Inserted → always; conflict →
	// heal-check (a crash between atom insert and question save leaves a
	// content-less orphan, which would break consumers at snapshot time).
	if inserted {
		if err := s.cloneQuestionOnto(ctx, orphan, q, rev); err != nil {
			return fmt.Errorf("orphan_required: clone question onto orphan %s: %w", orphan.AtomID, err)
		}
	} else {
		if _, _, qerr := s.cfg.Questions.GetByAtomIDAnyState(ctx, ev.TenantID, orphan.AtomID); qerr != nil {
			if !errors.Is(qerr, question.ErrNotFound) {
				return fmt.Errorf("orphan_required: verify orphan %s question: %w", orphan.AtomID, qerr)
			}
			s.cfg.Logger.Printf(`{"event":"orphan_torn_write_healed","orphan_atom_id":"%s","orphaned_from":"%s"}`,
				orphan.AtomID, ev.AtomID)
			if err := s.cloneQuestionOnto(ctx, orphan, q, rev); err != nil {
				return fmt.Errorf("orphan_required: heal orphan %s question: %w", orphan.AtomID, err)
			}
		}
	}

	// 5. Repoint creation's OWN non-author collection entries (idempotent;
	// runs on BOTH paths — a repeat withdrawal may strand a new wave of
	// entries added while the atom was re-widened).
	if _, err := s.cfg.Atoms.RepointCollections(ctx, ev.TenantID, original.AtomID, orphan.AtomID, original.Gcid); err != nil {
		return fmt.Errorf("orphan_required: repoint collections %s->%s: %w", original.AtomID, orphan.AtomID, err)
	}

	// 6. Publish orphan_created via the durable outbox. Attempted on BOTH
	// paths — the DETERMINISTIC idempotency key makes the store collapse it
	// to exactly one event: ErrDuplicateIdempotencyKey means the event
	// already exists (a repeat withdrawal at the same revision → correct
	// no-op), while a fresh insert after a crash-redelivery is the recovery
	// publish. Never a duplicate on the wire.
	created := atom.NewAtomOrphanCreatedEvent(orphan, ev.Trigger, ev.Traceparent, ev.Tracestate)
	if err := s.cfg.Publisher.Publish(ctx, created); err != nil {
		if errors.Is(err, outbox.ErrDuplicateIdempotencyKey) {
			s.cfg.Logger.Printf(`{"event":"orphan_created_already_enqueued","orphan_atom_id":"%s","orphaned_from":"%s","source_revision":"%s","trigger":"%s"}`,
				orphan.AtomID, ev.AtomID, sourceRevisionID, ev.Trigger)
		} else {
			return fmt.Errorf("orphan_required: enqueue orphan_created for %s: %w", orphan.AtomID, err)
		}
	}

	s.cfg.Logger.Printf(`{"event":"orphan_required_handled","source_event_id":"%s","tenant_id":"%s","orphaned_from":"%s","orphan_atom_id":"%s","source_revision":"%s","trigger":"%s","minted":%t,"stranded_grant_count":%d}`,
		ev.EventID, ev.TenantID, ev.AtomID, orphan.AtomID, sourceRevisionID, ev.Trigger, inserted, ev.StrandedGrantCount)
	return nil
}

// cloneQuestionOnto re-creates the source question (type + prompt + payload)
// as a NEW question on the orphan, authored by the ORIGINAL author (mirrors
// the clone handler's substrate).
func (s *OrphanRequiredSubscriber) cloneQuestionOnto(ctx context.Context, orphan *atom.LearningAtom, srcQ *question.Question, srcRev *question.QuestionRevision) error {
	// Prefer the LATEST revision's payload (the pinned content) over the
	// parent question's cached payload.
	mcq, oe := srcQ.MCQ, srcQ.OE
	prompt := srcQ.Prompt
	if srcRev != nil {
		if srcRev.MCQPayload != nil {
			mcq = srcRev.MCQPayload
		}
		if srcRev.OEPayload != nil {
			oe = srcRev.OEPayload
		}
		if strings.TrimSpace(srcRev.Prompt) != "" {
			prompt = srcRev.Prompt
		}
	}
	newQ, err := question.New(question.NewParams{
		TenantID:   orphan.TenantID,
		AtomID:     orphan.AtomID,
		AuthorGcid: orphan.Gcid, // the ORIGINAL author (attribution R1)
		Type:       srcQ.Type,
		Prompt:     prompt,
		SourceType: atom.SourceManual,
		MCQ:        mcq,
		OE:         oe,
	})
	if err != nil {
		return err
	}
	rev, err := question.NewRevision(newQ, newQ.Prompt, newQ.MCQ, newQ.OE, orphan.Gcid, atom.SourceManual)
	if err != nil {
		return err
	}
	newQ.LatestRevisionID = rev.RevisionID
	return s.cfg.Questions.Save(ctx, newQ, rev)
}

// -----------------------------------------------------------------------------
// Wire decode
// -----------------------------------------------------------------------------

// DecodeOrphanRequired parses the BINARY protobuf orphan_required.v1 payload
// (schema chora-sharing-atom_reuse-orphan_required-v1) via the generated
// binding, with the event envelope as the fallback (the publisher mirrors
// envelope fields onto the envelope).
func DecodeOrphanRequired(msg eventbus.Message) (OrphanRequiredEvent, error) {
	var m sharingv1.AtomReuseOrphanRequired
	if err := proto.Unmarshal(msg.Payload, &m); err != nil {
		return OrphanRequiredEvent{}, fmt.Errorf("decode atom_reuse.orphan_required: not binary protobuf: %w", err)
	}
	ev := OrphanRequiredEvent{
		AtomID:             m.GetAtomId(),
		RevisionID:         m.GetRevisionId(),
		Trigger:            m.GetTrigger(),
		StrandedGrantCount: m.GetStrandedGrantCount(),
	}
	if env := m.GetEnvelope(); env != nil {
		ev.EventID = env.GetEventId()
		ev.TenantID = env.GetTenantId()
		ev.GCID = env.GetGcid()
		ev.Traceparent = env.GetTraceparent()
		ev.Tracestate = env.GetTracestate()
	}
	// Envelope fallback — the producer mirrors the envelope onto the message
	// envelope headers.
	if msg.Envelope.EventID != "" {
		ev.EventID = msg.Envelope.EventID
	}
	if msg.Envelope.TenantID != "" {
		ev.TenantID = msg.Envelope.TenantID
	}
	if msg.Envelope.GCID != "" {
		ev.GCID = msg.Envelope.GCID
	}
	if msg.Envelope.Traceparent != "" {
		ev.Traceparent = msg.Envelope.Traceparent
	}
	if msg.Envelope.Tracestate != "" {
		ev.Tracestate = msg.Envelope.Tracestate
	}
	// A structurally-empty decode (no atom, no envelope) means the payload
	// was not really this message (proto3 tolerates foreign bytes that
	// happen to parse) — fail loud instead of NACK-looping downstream on
	// validation.
	if ev.AtomID == "" && ev.EventID == "" && ev.Trigger == "" {
		return OrphanRequiredEvent{}, errors.New("decode atom_reuse.orphan_required: structurally empty payload (wrong topic wiring?)")
	}
	return ev, nil
}
