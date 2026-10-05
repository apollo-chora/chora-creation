// topic_tag_backfill.go — the CHO-2142 durable topic-tag backfill (domain).
//
// PROBLEM. 74 PUBLISHED atoms carry null/empty source tags, so chora-consumption
// projects atom_index.topic_tags = {} and the topic-accuracy projector acks them
// unclassified. atom_index is a PROJECTION: writing it directly is derived-state
// tampering (and creation, not consumption, owns the truth). The only durable fix
// is to set the tags AT SOURCE and re-emit the atom event so consumption
// re-projects.
//
// WHERE THE TAGS COME FROM. The 6-agent content gate's Classifier
// (chora-agent-executor) is RETIRED — ADR-145 killed the executor, ADR-146 killed
// the chora-model-broker-classifier it called, and its only implementation was a
// canned MockClassifier. It is not deployed and never went through the chokepoint.
// So classification is issued through the TopicClassifier port, whose live adapter
// dials chora-model-gateway (gRPC :9090) — the single un-bypassable LLM chokepoint
// (ADR-163/177) where Cloud Model Armor screens and mana meters CENTRALLY. Same
// role the dead Classifier had; wired to the live mandated chokepoint.
//
// DURABILITY. Source write + outbox emit are separate steps (the outbox Publisher
// inserts a pending row that a Dispatcher drains async; the existing publish path
// has the same shape). The run is therefore made SAFE BY RE-RUNNABILITY rather
// than by a single transaction: it selects ONLY tagless atoms, so a re-run
// naturally retries whatever did not land, and the event's idempotency key is
// deterministic so a duplicate emit dedupes on the outbox UNIQUE index. Every
// per-atom failure is COUNTED and SURFACED (never silently skipped) — including
// the "tags written, emit failed" case, which the operator recovers with a salted
// re-emit.
package atom

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// -----------------------------------------------------------------------------
// Ports
// -----------------------------------------------------------------------------

// TopicClassifier classifies an atom's content into topic tags. The live adapter
// calls chora-model-gateway's Invoke RPC (ADR-163/177) — Model Armor + mana are
// enforced centrally there, so this port carries no guardrail concern of its own.
type TopicClassifier interface {
	ClassifyTopics(ctx context.Context, req ClassifyTopicsRequest) ([]string, error)
}

// ClassifyTopicsRequest is the classifier input. TenantID + Gcid ride the gateway
// envelope for attribution + metering; the trace context propagates the caller's
// span across the LLM hop (OTLP-everywhere).
type ClassifyTopicsRequest struct {
	TenantID    string
	Gcid        string // the atom's author
	AtomID      string
	Title       string
	Stem        string
	Body        string
	CourseID    string
	TraceParent string
	TraceState  string
}

// TopicTagBackfillRepo is the repository slice the backfill needs: the published,
// non-deleted atoms of a tenant (under its RLS context) and a Save.
type TopicTagBackfillRepo interface {
	ListPublished(ctx context.Context, tenantID string) ([]*LearningAtom, error)
	Save(ctx context.Context, a *LearningAtom) error
}

// PublishedRevision is the answerability + revision-pin primitive set carried on
// atom.published.v1. Primitives (not the question aggregate) keep this package
// free of a cross-aggregate import — the same hexagonal reason
// NewAtomPublishedEvent takes them as scalars.
type PublishedRevision struct {
	RevisionID      string
	RevisionNumber  int
	CorrectOptionID string
	AnswerCount     int
	HasOpenEnded    bool
}

// PublishedRevisionResolver resolves an atom's live published revision. ok=false
// means the atom has no resolvable published revision — the backfill then FAILS
// that atom rather than emitting a published event that would blank its answer
// key downstream.
type PublishedRevisionResolver interface {
	ResolvePublishedRevision(ctx context.Context, tenantID, atomID string) (PublishedRevision, bool)
}

// -----------------------------------------------------------------------------
// Params + result
// -----------------------------------------------------------------------------

// BackfillTopicTagsParams drives one backfill run.
type BackfillTopicTagsParams struct {
	TenantID string

	// DryRun classifies and returns the PROPOSED tags per atom without writing
	// source tags or emitting anything. This is the owner's gate: the whole set
	// can be eyeballed before a single row is touched.
	DryRun bool

	// Limit bounds the run (0 = every candidate).
	Limit int

	// ReemitSalt salts each event's deterministic idempotency key. Required in
	// practice for an already-published revision: the plain key
	// (atom:published:rev) is permanently deduped by the outbox UNIQUE index, so
	// an unsalted re-emit would be swallowed and nothing would re-project
	// (CHO-2128 F1).
	ReemitSalt string

	TraceParent string
	TraceState  string
}

// ProposedTopicTags is one atom's classification (the dry-run preview row).
type ProposedTopicTags struct {
	AtomID string   `json:"atom_id"`
	Title  string   `json:"title"`
	Tags   []string `json:"tags"`
}

// TopicTagFailure is one atom's loud failure. Reason is operator-facing.
type TopicTagFailure struct {
	AtomID string `json:"atom_id"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

// BackfillTopicTagsResult is the run report. Failures are surfaced, never hidden.
type BackfillTopicTagsResult struct {
	TenantID string `json:"tenant_id"`
	DryRun   bool   `json:"dry_run"`

	// Scanned = published atoms examined; Candidates = those needing tags.
	Scanned    int `json:"scanned"`
	Candidates int `json:"candidates"`

	// Tagged = source tags written (0 on a dry run).
	Tagged int `json:"tagged"`
	// Emitted = atom.published.v1 events queued to the outbox (0 on a dry run).
	Emitted int `json:"emitted"`

	Proposed []ProposedTopicTags `json:"proposed,omitempty"`
	Failed   []TopicTagFailure   `json:"failed,omitempty"`
}

// -----------------------------------------------------------------------------
// Service
// -----------------------------------------------------------------------------

// TopicTagBackfillService classifies tagless published atoms, writes their tags
// at source, and re-emits atom.published.v1 (which now carries field 7) through
// the outbox so chora-consumption re-projects atom_index.topic_tags.
type TopicTagBackfillService struct {
	repo       TopicTagBackfillRepo
	classifier TopicClassifier
	pub        EventPublisher
	revisions  PublishedRevisionResolver
}

// NewTopicTagBackfillService wires the service. Missing collaborators are NOT
// tolerated at Run time (fail-loud, never a silent no-op run).
func NewTopicTagBackfillService(
	repo TopicTagBackfillRepo,
	classifier TopicClassifier,
	pub EventPublisher,
	revisions PublishedRevisionResolver,
) *TopicTagBackfillService {
	return &TopicTagBackfillService{repo: repo, classifier: classifier, pub: pub, revisions: revisions}
}

// Run executes one backfill pass.
//
// A collaborator/param error aborts the run loudly (returns error). A PER-ATOM
// error never aborts the run — it is recorded in Result.Failed with a reason and
// the pass continues, so one bad atom cannot strand the other 73.
func (s *TopicTagBackfillService) Run(ctx context.Context, p BackfillTopicTagsParams) (BackfillTopicTagsResult, error) {
	if strings.TrimSpace(p.TenantID) == "" {
		return BackfillTopicTagsResult{}, errors.New("topic-tag backfill: tenant_id is required")
	}
	if s.repo == nil {
		return BackfillTopicTagsResult{}, errors.New("topic-tag backfill: repository not wired")
	}
	// no-stubs: an unwired classifier must fail loud, never degrade to a run that
	// reports success having classified nothing.
	if s.classifier == nil {
		return BackfillTopicTagsResult{}, errors.New("topic-tag backfill: classifier not wired (set the model-gateway target)")
	}
	if s.pub == nil && !p.DryRun {
		return BackfillTopicTagsResult{}, errors.New("topic-tag backfill: event publisher not wired (a write with no re-emit would never project)")
	}
	if s.revisions == nil && !p.DryRun {
		return BackfillTopicTagsResult{}, errors.New("topic-tag backfill: revision resolver not wired")
	}

	atoms, err := s.repo.ListPublished(ctx, p.TenantID)
	if err != nil {
		return BackfillTopicTagsResult{}, fmt.Errorf("topic-tag backfill: list published atoms: %w", err)
	}

	res := BackfillTopicTagsResult{TenantID: p.TenantID, DryRun: p.DryRun, Scanned: len(atoms)}

	for _, a := range atoms {
		// ADR-229 A1: orphan editions are frozen AND must never emit
		// atom.published.v1 (a projection row would make them re-sharable).
		if a.IsOrphan() {
			continue
		}
		if !a.NeedsTopicTags() {
			continue
		}
		if p.Limit > 0 && res.Candidates >= p.Limit {
			break
		}
		res.Candidates++

		s.backfillOne(ctx, a, p, &res)
	}

	return res, nil
}

// backfillOne classifies, writes, and re-emits a single atom, recording any
// failure on the result. It never returns an error — a per-atom failure must not
// abort the pass.
func (s *TopicTagBackfillService) backfillOne(
	ctx context.Context, a *LearningAtom, p BackfillTopicTagsParams, res *BackfillTopicTagsResult,
) {
	fail := func(reason string) {
		res.Failed = append(res.Failed, TopicTagFailure{AtomID: a.AtomID, Title: a.Title, Reason: reason})
	}

	raw, err := s.classifier.ClassifyTopics(ctx, ClassifyTopicsRequest{
		TenantID:    p.TenantID,
		Gcid:        a.Gcid,
		AtomID:      a.AtomID,
		Title:       a.Title,
		Stem:        a.Stem,
		Body:        a.Body,
		CourseID:    a.CourseID,
		TraceParent: p.TraceParent,
		TraceState:  p.TraceState,
	})
	if err != nil {
		fail(fmt.Sprintf("classify: %v", err))
		return
	}

	// Normalise BEFORE any write. An empty/unusable classification is a loud
	// failure — writing it back would silently reproduce the very defect this
	// backfill exists to fix.
	tags, err := NormalizeTopicTags(raw)
	if err != nil {
		fail(fmt.Sprintf("classify: %v", err))
		return
	}

	if p.DryRun {
		res.Proposed = append(res.Proposed, ProposedTopicTags{AtomID: a.AtomID, Title: a.Title, Tags: tags})
		return
	}

	// Resolve the revision BEFORE mutating: an atom we cannot re-emit for must
	// not have its source tags written, or the tags would sit unprojected with no
	// event to carry them.
	rev, ok := s.revisions.ResolvePublishedRevision(ctx, p.TenantID, a.AtomID)
	if !ok {
		fail("no resolvable published revision — refusing to write tags that could not be re-emitted")
		return
	}

	if err := a.SetTopicTags(tags); err != nil {
		fail(fmt.Sprintf("set topic tags: %v", err))
		return
	}
	if err := s.repo.Save(ctx, a); err != nil {
		fail(fmt.Sprintf("save source tags: %v", err))
		return
	}
	res.Tagged++

	ev := NewAtomPublishedReemitEvent(
		a, rev.RevisionID, rev.RevisionNumber,
		rev.CorrectOptionID, rev.AnswerCount, rev.HasOpenEnded,
		"", p.TraceParent, p.TraceState, p.ReemitSalt,
	)
	if err := s.pub.Publish(ctx, ev); err != nil {
		// The source write COMMITTED but the event did not queue. Surface it
		// distinctly: the tags are at source but unprojected until a re-emit
		// (POST /api/internal/atoms/backfill-published?reemit_salt=<new>).
		fail(fmt.Sprintf("source tags WRITTEN but re-emit failed (atom is tagged-but-unprojected; recover with a salted re-emit): %v", err))
		return
	}
	res.Emitted++
}
