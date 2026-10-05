// orphan_required_subscriber_test.go — ADR-229 Amendment A1 (CHO-2132):
// chora-creation's consumer of chora.sharing.atom_reuse.orphan_required.v1.
//
// Contract under test (the Jira AC table rows this leg owns):
//   - Stranding mint: load the withdrawn atom + its LAST-PUBLISHED revision
//     (authoritative in-domain; the event's revision_id is only a hint), mint
//     ONE immutable orphan edition, clone the question onto it, repoint
//     creation's own non-author collection entries, publish
//     atom.orphan_created.v1.
//   - Singleton idempotency: a second withdrawal / redelivery at the same
//     revision reuses the existing orphan and enqueues NO duplicate event
//     (the deterministic outbox idempotency key dedupes).
//   - Survives archive: the original's soft-delete cascade (atom + question
//     both deleted_at-stamped) does NOT stop the mint — the any-state loaders
//     still resolve content — and never touches the orphan.
//   - Fail loud: unknown atom / missing question / orphan-of-orphan target /
//     invalid trigger all error (NACK → retry/DLQ), never a silent ack.
package pubsub

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/apollo-chora/chora-common/envelope"
	"github.com/apollo-chora/chora-common/eventbus"
	commonv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/common/v1"
	sharingv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/sharing/v1"
	"github.com/apollo-chora/chora-creation/internal/adapter/outbox"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

const (
	orTenant   = "01970000-0000-7111-8111-000000000001"
	orAuthor   = "01970000-0000-7000-8000-0000000000aa"
	orAtomID   = "01970000-0000-7000-8000-0000000000a1"
	orEventID  = "01970000-0000-7000-8000-00000000e001"
	orRevision = "01970000-0000-7000-8000-0000000000f1"
)

// --- fakes -------------------------------------------------------------------

type fakeOrphanAtoms struct {
	byID       map[string]*atom.LearningAtom // original atoms by id
	minted     map[string]*atom.LearningAtom // singleton key -> orphan
	mintCalls  int
	repoints   []string // "original->orphan" strings
	repointErr error
}

func newFakeOrphanAtoms() *fakeOrphanAtoms {
	return &fakeOrphanAtoms{byID: map[string]*atom.LearningAtom{}, minted: map[string]*atom.LearningAtom{}}
}

func (f *fakeOrphanAtoms) GetAnyState(_ context.Context, tenantID, atomID string) (*atom.LearningAtom, error) {
	a, ok := f.byID[atomID]
	if !ok || a.TenantID != tenantID {
		return nil, atom.ErrNotFound
	}
	return a, nil
}

func (f *fakeOrphanAtoms) MintOrphan(_ context.Context, o *atom.LearningAtom) (*atom.LearningAtom, bool, error) {
	f.mintCalls++
	key := o.OrphanedFromAtomID + "|" + o.OrphanedSourceRevisionID
	if existing, ok := f.minted[key]; ok {
		return existing, false, nil
	}
	cp := *o
	f.minted[key] = &cp
	f.byID[cp.AtomID] = &cp
	return &cp, true, nil
}

func (f *fakeOrphanAtoms) RepointCollections(_ context.Context, _ string, originalAtomID, orphanAtomID, _ string) (int, error) {
	if f.repointErr != nil {
		return 0, f.repointErr
	}
	f.repoints = append(f.repoints, originalAtomID+"->"+orphanAtomID)
	return 2, nil
}

type fakeOrphanQuestions struct {
	byAtomID map[string]*question.Question // question per atom id (any state)
	revs     map[string]*question.QuestionRevision
	saved    []*question.Question
}

func newFakeOrphanQuestions() *fakeOrphanQuestions {
	return &fakeOrphanQuestions{byAtomID: map[string]*question.Question{}, revs: map[string]*question.QuestionRevision{}}
}

func (f *fakeOrphanQuestions) GetByAtomIDAnyState(_ context.Context, tenantID, atomID string) (*question.Question, *question.QuestionRevision, error) {
	q, ok := f.byAtomID[atomID]
	if !ok || q.TenantID != tenantID {
		return nil, nil, question.ErrNotFound
	}
	return q, f.revs[atomID], nil
}

func (f *fakeOrphanQuestions) Save(_ context.Context, q *question.Question, rev *question.QuestionRevision) error {
	f.byAtomID[q.AtomID] = q
	f.revs[q.AtomID] = rev
	f.saved = append(f.saved, q)
	return nil
}

type fakeOrphanPublisher struct {
	events []atom.Event
	errSeq []error
}

func (f *fakeOrphanPublisher) Publish(_ context.Context, e atom.Event) error {
	if len(f.errSeq) > 0 {
		err := f.errSeq[0]
		f.errSeq = f.errSeq[1:]
		if err != nil {
			return err
		}
	}
	f.events = append(f.events, e)
	return nil
}

// --- fixtures ----------------------------------------------------------------

func seedOriginal(atoms *fakeOrphanAtoms, questions *fakeOrphanQuestions) *atom.LearningAtom {
	src := &atom.LearningAtom{
		AtomID:          orAtomID,
		TenantID:        orTenant,
		Gcid:            orAuthor,
		Title:           "Fractions",
		Body:            "b",
		Mode:            atom.ModeStraightUp,
		Status:          atom.StatusPublished,
		Stem:            "1/2 + 1/4?",
		QuestionType:    atom.QuestionTypeMCQ,
		ReuseVisibility: atom.ReusePrivate, // already narrowed
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}
	atoms.byID[src.AtomID] = src

	q, _ := question.New(question.NewParams{
		TenantID:   orTenant,
		AtomID:     src.AtomID,
		AuthorGcid: orAuthor,
		Type:       question.TypeMCQ,
		Prompt:     "1/2 + 1/4?",
		SourceType: atom.SourceManual,
		MCQ: &question.MCQPayload{Options: []question.MCQOption{
			{OptionID: "o1", Label: "3/4", IsCorrect: true, Explainer: "yes"},
			{OptionID: "o2", Label: "2/6", IsCorrect: false, Explainer: "no"},
		}},
	})
	rev, _ := question.NewRevision(q, q.Prompt, q.MCQ, nil, orAuthor, atom.SourceManual)
	q.LatestRevisionID = rev.RevisionID
	rev.RevisionID = orRevision // pin the fixture's last-published revision id
	q.LatestRevisionID = orRevision
	questions.byAtomID[src.AtomID] = q
	questions.revs[src.AtomID] = rev
	return src
}

func orphanRequiredEvent(trigger string) OrphanRequiredEvent {
	return OrphanRequiredEvent{
		EventID:            orEventID,
		TenantID:           orTenant,
		GCID:               orAuthor,
		AtomID:             orAtomID,
		RevisionID:         "01970000-0000-7000-8000-00000000hint", // stale hint — must be ignored
		Trigger:            trigger,
		StrandedGrantCount: 2,
	}
}

func newSubscriberForTest(atoms *fakeOrphanAtoms, questions *fakeOrphanQuestions, pub *fakeOrphanPublisher) *OrphanRequiredSubscriber {
	return NewOrphanRequiredSubscriber(OrphanRequiredConfig{
		Atoms:     atoms,
		Questions: questions,
		Publisher: pub,
	})
}

// --- tests ---------------------------------------------------------------------

func TestOrphanRequired_MintsSingletonPublishesAndRepoints(t *testing.T) {
	atoms, questions, pub := newFakeOrphanAtoms(), newFakeOrphanQuestions(), &fakeOrphanPublisher{}
	src := seedOriginal(atoms, questions)
	sub := newSubscriberForTest(atoms, questions, pub)

	if err := sub.HandleOrphanRequired(context.Background(), orphanRequiredEvent("narrowed")); err != nil {
		t.Fatalf("HandleOrphanRequired: %v", err)
	}

	// Minted against the AUTHORITATIVE last-published revision, not the hint.
	orphan, ok := atoms.minted[orAtomID+"|"+orRevision]
	if !ok {
		t.Fatalf("orphan not minted against authoritative revision %s; minted keys = %v", orRevision, atoms.minted)
	}
	if orphan.Gcid != src.Gcid || orphan.ReuseVisibility != atom.ReusePrivate || orphan.Status != atom.StatusPublished {
		t.Fatalf("orphan shape wrong: %+v", orphan)
	}

	// Question cloned onto the orphan.
	oq, orev, err := questions.GetByAtomIDAnyState(context.Background(), orTenant, orphan.AtomID)
	if err != nil {
		t.Fatalf("orphan question not cloned: %v", err)
	}
	if oq.Prompt != "1/2 + 1/4?" || oq.Type != question.TypeMCQ || orev == nil {
		t.Fatalf("orphan question content wrong: %+v", oq)
	}
	if oq.QuestionID == questions.byAtomID[orAtomID].QuestionID {
		t.Fatalf("orphan question must be a NEW question row")
	}

	// Creation's own collection entries repointed.
	if len(atoms.repoints) != 1 || atoms.repoints[0] != orAtomID+"->"+orphan.AtomID {
		t.Fatalf("collections not repointed: %v", atoms.repoints)
	}

	// Exactly one orphan_created event with the right shape.
	if len(pub.events) != 1 {
		t.Fatalf("events = %d, want 1", len(pub.events))
	}
	ev := pub.events[0]
	if ev.Type != atom.EventTypeAtomOrphanCreated {
		t.Fatalf("event type = %q", ev.Type)
	}
	if ev.AtomID != orphan.AtomID || ev.OrphanedFromAtomID != orAtomID || ev.RevisionID != orRevision {
		t.Fatalf("event ids wrong: %+v", ev)
	}
	if ev.Trigger != "narrowed" {
		t.Fatalf("trigger = %q", ev.Trigger)
	}
	if ev.TenantID != orTenant {
		t.Fatalf("tenant = %q", ev.TenantID)
	}
}

func TestOrphanRequired_RepeatWithdrawalReusesOrphanNoDuplicateEvent(t *testing.T) {
	atoms, questions, pub := newFakeOrphanAtoms(), newFakeOrphanQuestions(), &fakeOrphanPublisher{}
	seedOriginal(atoms, questions)
	sub := newSubscriberForTest(atoms, questions, pub)

	if err := sub.HandleOrphanRequired(context.Background(), orphanRequiredEvent("narrowed")); err != nil {
		t.Fatalf("first: %v", err)
	}
	// The first publish enqueued the deterministic key; the conflict-path
	// re-publish must surface ErrDuplicateIdempotencyKey, which the handler
	// treats as success (the event already exists — never a duplicate).
	pub.errSeq = []error{outbox.ErrDuplicateIdempotencyKey}
	second := orphanRequiredEvent("narrowed")
	second.EventID = "01970000-0000-7000-8000-00000000e002" // a REAL second withdrawal
	if err := sub.HandleOrphanRequired(context.Background(), second); err != nil {
		t.Fatalf("second withdrawal must succeed idempotently: %v", err)
	}

	if len(atoms.minted) != 1 {
		t.Fatalf("minted %d orphans, want 1 (singleton)", len(atoms.minted))
	}
	if len(pub.events) != 1 {
		t.Fatalf("events = %d, want 1 (no duplicate orphan_created)", len(pub.events))
	}
	// Collections re-repointed idempotently on the second pass (a new wave of
	// entries may have appeared while re-widened).
	if len(atoms.repoints) != 2 {
		t.Fatalf("repoints = %d, want 2 (idempotent re-run)", len(atoms.repoints))
	}
}

// Crash between mint-commit and outbox insert: the redelivery takes the
// conflict path and MUST still attempt the publish (deterministic key was
// never enqueued — publishing it now is the recovery, not a duplicate).
func TestOrphanRequired_RedeliveryAfterCrashStillPublishes(t *testing.T) {
	atoms, questions, pub := newFakeOrphanAtoms(), newFakeOrphanQuestions(), &fakeOrphanPublisher{}
	seedOriginal(atoms, questions)
	sub := newSubscriberForTest(atoms, questions, pub)

	// First delivery: mint commits, then the publish fails (crash simulated).
	pub.errSeq = []error{errors.New("boom: process died before outbox insert")}
	if err := sub.HandleOrphanRequired(context.Background(), orphanRequiredEvent("narrowed")); err == nil {
		t.Fatalf("publish failure must NACK (error), got nil")
	}
	if len(pub.events) != 0 {
		t.Fatalf("no event should exist after the crash")
	}

	// Redelivery: mint conflicts (singleton), publish retries and succeeds.
	if err := sub.HandleOrphanRequired(context.Background(), orphanRequiredEvent("narrowed")); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if len(pub.events) != 1 {
		t.Fatalf("events = %d, want exactly 1 after recovery", len(pub.events))
	}
	if len(atoms.minted) != 1 {
		t.Fatalf("minted = %d, want 1", len(atoms.minted))
	}
}

// Torn write (atom minted, question save crashed): the conflict path heals
// the missing orphan question before publishing.
func TestOrphanRequired_ConflictPathHealsMissingOrphanQuestion(t *testing.T) {
	atoms, questions, pub := newFakeOrphanAtoms(), newFakeOrphanQuestions(), &fakeOrphanPublisher{}
	src := seedOriginal(atoms, questions)
	sub := newSubscriberForTest(atoms, questions, pub)

	// Pre-seed a minted orphan WITHOUT a question (the torn state).
	pre, err := atom.CloneOrphan(atom.OrphanCloneParams{Source: src, SourceRevisionID: orRevision})
	if err != nil {
		t.Fatalf("CloneOrphan: %v", err)
	}
	atoms.minted[orAtomID+"|"+orRevision] = pre
	atoms.byID[pre.AtomID] = pre

	if err := sub.HandleOrphanRequired(context.Background(), orphanRequiredEvent("narrowed")); err != nil {
		t.Fatalf("HandleOrphanRequired: %v", err)
	}
	if _, _, err := questions.GetByAtomIDAnyState(context.Background(), orTenant, pre.AtomID); err != nil {
		t.Fatalf("torn-write question not healed: %v", err)
	}
}

// Survives archive (A1.2): the archive cascade soft-deleted the original atom
// AND its question — the any-state loaders still resolve, the orphan mints,
// and nothing touches the (already existing) orphan on a later pass.
func TestOrphanRequired_ArchiveTrigger_MintsFromSoftDeletedOriginal(t *testing.T) {
	atoms, questions, pub := newFakeOrphanAtoms(), newFakeOrphanQuestions(), &fakeOrphanPublisher{}
	src := seedOriginal(atoms, questions)
	now := time.Now().UTC()
	src.DeletedAt = &now
	src.Status = atom.StatusArchived
	questions.byAtomID[orAtomID].DeletedAt = &now // cascade already ran
	sub := newSubscriberForTest(atoms, questions, pub)

	if err := sub.HandleOrphanRequired(context.Background(), orphanRequiredEvent("archived")); err != nil {
		t.Fatalf("archive-trigger mint: %v", err)
	}
	orphan, ok := atoms.minted[orAtomID+"|"+orRevision]
	if !ok {
		t.Fatalf("orphan not minted from archived original")
	}
	if orphan.DeletedAt != nil || orphan.Status != atom.StatusPublished {
		t.Fatalf("orphan must be alive + published (own aggregate root, cascade never crosses): %+v", orphan)
	}
	if len(pub.events) != 1 || pub.events[0].Trigger != "archived" {
		t.Fatalf("archived trigger not carried: %+v", pub.events)
	}
	// The orphan's question is ALIVE even though the source question row was
	// soft-deleted.
	oq, _, err := questions.GetByAtomIDAnyState(context.Background(), orTenant, orphan.AtomID)
	if err != nil || oq.DeletedAt != nil {
		t.Fatalf("orphan question must be alive: q=%+v err=%v", oq, err)
	}
}

// --- fail-loud paths -----------------------------------------------------------

func TestOrphanRequired_FailLoudPaths(t *testing.T) {
	t.Run("unknown atom", func(t *testing.T) {
		atoms, questions, pub := newFakeOrphanAtoms(), newFakeOrphanQuestions(), &fakeOrphanPublisher{}
		sub := newSubscriberForTest(atoms, questions, pub)
		if err := sub.HandleOrphanRequired(context.Background(), orphanRequiredEvent("narrowed")); err == nil {
			t.Fatalf("unknown atom must error (NACK)")
		}
	})
	t.Run("missing question", func(t *testing.T) {
		atoms, questions, pub := newFakeOrphanAtoms(), newFakeOrphanQuestions(), &fakeOrphanPublisher{}
		seedOriginal(atoms, questions)
		delete(questions.byAtomID, orAtomID)
		sub := newSubscriberForTest(atoms, questions, pub)
		if err := sub.HandleOrphanRequired(context.Background(), orphanRequiredEvent("narrowed")); err == nil {
			t.Fatalf("missing question must error (cannot preserve continuity without content)")
		}
	})
	t.Run("orphan-of-orphan", func(t *testing.T) {
		atoms, questions, pub := newFakeOrphanAtoms(), newFakeOrphanQuestions(), &fakeOrphanPublisher{}
		src := seedOriginal(atoms, questions)
		src.OrphanedFromAtomID = "01970000-0000-7000-8000-00000000feed"
		src.OrphanedSourceRevisionID = orRevision
		now := time.Now().UTC()
		src.OrphanedAt = &now
		sub := newSubscriberForTest(atoms, questions, pub)
		if err := sub.HandleOrphanRequired(context.Background(), orphanRequiredEvent("narrowed")); err == nil {
			t.Fatalf("orphan-of-orphan target must error")
		}
	})
	t.Run("invalid trigger", func(t *testing.T) {
		atoms, questions, pub := newFakeOrphanAtoms(), newFakeOrphanQuestions(), &fakeOrphanPublisher{}
		seedOriginal(atoms, questions)
		sub := newSubscriberForTest(atoms, questions, pub)
		if err := sub.HandleOrphanRequired(context.Background(), orphanRequiredEvent("widened")); err == nil {
			t.Fatalf("invalid trigger must error")
		}
	})
	t.Run("missing tenant", func(t *testing.T) {
		atoms, questions, pub := newFakeOrphanAtoms(), newFakeOrphanQuestions(), &fakeOrphanPublisher{}
		seedOriginal(atoms, questions)
		sub := newSubscriberForTest(atoms, questions, pub)
		ev := orphanRequiredEvent("narrowed")
		ev.TenantID = ""
		if err := sub.HandleOrphanRequired(context.Background(), ev); err == nil {
			t.Fatalf("missing tenant must error")
		}
	})
	t.Run("collection repoint failure NACKs", func(t *testing.T) {
		atoms, questions, pub := newFakeOrphanAtoms(), newFakeOrphanQuestions(), &fakeOrphanPublisher{}
		seedOriginal(atoms, questions)
		atoms.repointErr = errors.New("db down")
		sub := newSubscriberForTest(atoms, questions, pub)
		if err := sub.HandleOrphanRequired(context.Background(), orphanRequiredEvent("narrowed")); err == nil {
			t.Fatalf("repoint failure must error (NACK)")
		}
	})
}

// --- decode ----------------------------------------------------------------------

func TestDecodeOrphanRequired_BinaryProto(t *testing.T) {
	msg := &sharingv1.AtomReuseOrphanRequired{
		Envelope: &commonv1.EventEnvelope{
			EventId:  orEventID,
			TenantId: orTenant,
			Gcid:     orAuthor,
		},
		AtomId:             orAtomID,
		RevisionId:         orRevision,
		Trigger:            "narrowed",
		StrandedGrantCount: 3,
		DetectedAt:         timestamppb.New(time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)),
	}
	bz, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}

	ev, err := DecodeOrphanRequired(eventbus.Message{
		Payload:  bz,
		Envelope: envelope.Envelope{Traceparent: "00-tp-01"},
	})
	if err != nil {
		t.Fatalf("DecodeOrphanRequired: %v", err)
	}
	if ev.EventID != orEventID || ev.TenantID != orTenant || ev.GCID != orAuthor {
		t.Fatalf("envelope fields wrong: %+v", ev)
	}
	if ev.AtomID != orAtomID || ev.RevisionID != orRevision || ev.Trigger != "narrowed" || ev.StrandedGrantCount != 3 {
		t.Fatalf("payload fields wrong: %+v", ev)
	}
	if ev.Traceparent != "00-tp-01" {
		t.Fatalf("traceparent not threaded from envelope: %+v", ev)
	}

	// Envelope-only fallback (binary payload missing envelope fields).
	msg.Envelope = nil
	bz2, _ := proto.Marshal(msg)
	ev2, err := DecodeOrphanRequired(eventbus.Message{
		Payload:  bz2,
		Envelope: envelope.Envelope{EventID: orEventID, TenantID: orTenant, GCID: orAuthor},
	})
	if err != nil {
		t.Fatalf("DecodeOrphanRequired envelope fallback: %v", err)
	}
	if ev2.EventID != orEventID || ev2.TenantID != orTenant {
		t.Fatalf("envelope fallback failed: %+v", ev2)
	}

	// Garbage payload fails loud.
	if _, err := DecodeOrphanRequired(eventbus.Message{Payload: []byte("{json}")}); err == nil ||
		!strings.Contains(err.Error(), "orphan_required") {
		t.Fatalf("garbage payload must fail loud with topic context; err = %v", err)
	}
}
