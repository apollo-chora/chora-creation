package atom_test

import (
	"context"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// -----------------------------------------------------------------------------
// fakes
// -----------------------------------------------------------------------------

type fakeClassifier struct {
	tags map[string][]string // atomID -> tags
	err  error
	// calls records the atom ids classified, in order — proves a dry run still
	// classifies (that IS the preview) and that tagged atoms are never re-classified.
	calls []string
}

func (f *fakeClassifier) ClassifyTopics(_ context.Context, req atom.ClassifyTopicsRequest) ([]string, error) {
	f.calls = append(f.calls, req.AtomID)
	if f.err != nil {
		return nil, f.err
	}
	return f.tags[req.AtomID], nil
}

type fakeBackfillRepo struct {
	atoms   []*atom.LearningAtom
	saved   []string // atom ids Save()d
	saveErr error
}

func (f *fakeBackfillRepo) ListPublished(_ context.Context, _ string) ([]*atom.LearningAtom, error) {
	return f.atoms, nil
}

func (f *fakeBackfillRepo) Save(_ context.Context, a *atom.LearningAtom) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, a.AtomID)
	return nil
}

type fakeEventPub struct {
	events []atom.Event
	failOn string // atom id whose publish fails
}

func (f *fakeEventPub) Publish(_ context.Context, e atom.Event) error {
	if f.failOn != "" && e.AtomID == f.failOn {
		return errors.New("outbox down")
	}
	f.events = append(f.events, e)
	return nil
}

type fakeRevResolver struct{ missing string }

func (f *fakeRevResolver) ResolvePublishedRevision(_ context.Context, _, atomID string) (atom.PublishedRevision, bool) {
	if atomID == f.missing {
		return atom.PublishedRevision{}, false
	}
	return atom.PublishedRevision{
		RevisionID:      "rev-" + atomID,
		RevisionNumber:  1,
		CorrectOptionID: "opt-1",
		AnswerCount:     4,
	}, true
}

// tagless builds a PUBLISHED atom with no topic tags (the CHO-2142 shape).
func tagless(t *testing.T, title string) *atom.LearningAtom {
	t.Helper()
	a := newDraft(t, nil)
	a.Title = title
	if err := a.Publish(); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return a
}

func newBackfill(r atom.TopicTagBackfillRepo, c atom.TopicClassifier, p atom.EventPublisher, rv atom.PublishedRevisionResolver) *atom.TopicTagBackfillService {
	return atom.NewTopicTagBackfillService(r, c, p, rv)
}

// -----------------------------------------------------------------------------
// tests
// -----------------------------------------------------------------------------

func TestBackfill_ClassifiesOnlyTaglessAtoms(t *testing.T) {
	tagless1 := tagless(t, "Comparing Fractions")
	tagged := tagless(t, "Already Tagged")
	if err := tagged.SetTopicTags([]string{"algebra"}); err != nil {
		t.Fatalf("SetTopicTags: %v", err)
	}

	repo := &fakeBackfillRepo{atoms: []*atom.LearningAtom{tagless1, tagged}}
	cls := &fakeClassifier{tags: map[string][]string{tagless1.AtomID: {"Fractions", "number-sense"}}}
	pub := &fakeEventPub{}
	svc := newBackfill(repo, cls, pub, &fakeRevResolver{})

	res, err := svc.Run(t.Context(), atom.BackfillTopicTagsParams{TenantID: "t1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(cls.calls) != 1 || cls.calls[0] != tagless1.AtomID {
		t.Fatalf("only the tagless atom must be classified, got %v", cls.calls)
	}
	if res.Candidates != 1 || res.Tagged != 1 || res.Emitted != 1 {
		t.Fatalf("want 1/1/1, got candidates=%d tagged=%d emitted=%d", res.Candidates, res.Tagged, res.Emitted)
	}
	// Normalised at source.
	if len(tagless1.Tags) != 2 || tagless1.Tags[0] != "fractions" {
		t.Fatalf("source tags not normalised: %v", tagless1.Tags)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("source write missing: %v", repo.saved)
	}
	// The re-emitted event MUST carry the tags — that is the whole point.
	if len(pub.events) != 1 {
		t.Fatalf("want 1 event, got %d", len(pub.events))
	}
	ev := pub.events[0]
	if ev.Type != atom.EventTypeAtomPublished {
		t.Fatalf("want atom.published.v1, got %s", ev.Type)
	}
	if len(ev.Tags) != 2 || ev.Tags[0] != "fractions" {
		t.Fatalf("event must carry tags, got %v", ev.Tags)
	}
	// Answer key must survive the re-emit (a blanked key drops the atom from the dose).
	if ev.CorrectOptionID != "opt-1" || ev.AnswerCount != 4 {
		t.Fatalf("answerability lost: key=%q count=%d", ev.CorrectOptionID, ev.AnswerCount)
	}
	if ev.TenantID == "" || ev.OccurredAt == "" || ev.IdempotencyKey == "" {
		t.Fatal("envelope fields must be stamped")
	}
}

func TestBackfill_DryRunClassifiesButNeverWritesOrEmits(t *testing.T) {
	a := tagless(t, "Comparing Fractions")
	repo := &fakeBackfillRepo{atoms: []*atom.LearningAtom{a}}
	cls := &fakeClassifier{tags: map[string][]string{a.AtomID: {"fractions"}}}
	pub := &fakeEventPub{}
	svc := newBackfill(repo, cls, pub, &fakeRevResolver{})

	res, err := svc.Run(t.Context(), atom.BackfillTopicTagsParams{TenantID: "t1", DryRun: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(cls.calls) != 1 {
		t.Fatal("dry run must still classify — that IS the preview")
	}
	if len(repo.saved) != 0 {
		t.Fatalf("dry run must NOT write source tags, saved=%v", repo.saved)
	}
	if len(pub.events) != 0 {
		t.Fatalf("dry run must NOT emit, events=%d", len(pub.events))
	}
	if len(a.Tags) != 0 {
		t.Fatalf("dry run must not mutate the aggregate, tags=%v", a.Tags)
	}
	if res.Tagged != 0 || res.Emitted != 0 {
		t.Fatalf("dry run counters must be zero, got tagged=%d emitted=%d", res.Tagged, res.Emitted)
	}
	// The preview is the owner's gate — it must carry the proposed tags per atom.
	if len(res.Proposed) != 1 || res.Proposed[0].AtomID != a.AtomID {
		t.Fatalf("dry run must return the proposal, got %+v", res.Proposed)
	}
	if len(res.Proposed[0].Tags) != 1 || res.Proposed[0].Tags[0] != "fractions" {
		t.Fatalf("proposal must carry normalised tags, got %v", res.Proposed[0].Tags)
	}
	if !res.DryRun {
		t.Fatal("result must flag dry run")
	}
}

func TestBackfill_ClassifierErrorFailsAtomLoudly_DoesNotBlankTags(t *testing.T) {
	a := tagless(t, "Comparing Fractions")
	repo := &fakeBackfillRepo{atoms: []*atom.LearningAtom{a}}
	cls := &fakeClassifier{err: errors.New("gateway unavailable")}
	pub := &fakeEventPub{}
	svc := newBackfill(repo, cls, pub, &fakeRevResolver{})

	res, err := svc.Run(t.Context(), atom.BackfillTopicTagsParams{TenantID: "t1"})
	if err != nil {
		t.Fatalf("Run must not abort the whole run: %v", err)
	}

	if res.Tagged != 0 || res.Emitted != 0 {
		t.Fatal("a failed classification must never write or emit")
	}
	if len(res.Failed) != 1 || res.Failed[0].AtomID != a.AtomID {
		t.Fatalf("failure must be counted + surfaced, got %+v", res.Failed)
	}
	if res.Failed[0].Reason == "" {
		t.Fatal("failure must carry a loud reason")
	}
	if len(repo.saved) != 0 || len(pub.events) != 0 {
		t.Fatal("no side effects on classifier failure")
	}
}

func TestBackfill_EmptyClassificationIsAFailure_NotASilentEmptyWrite(t *testing.T) {
	a := tagless(t, "Comparing Fractions")
	repo := &fakeBackfillRepo{atoms: []*atom.LearningAtom{a}}
	// Classifier returns nothing usable — writing that back would silently
	// reproduce the exact defect the backfill exists to fix.
	cls := &fakeClassifier{tags: map[string][]string{a.AtomID: {"", "   "}}}
	pub := &fakeEventPub{}
	svc := newBackfill(repo, cls, pub, &fakeRevResolver{})

	res, err := svc.Run(t.Context(), atom.BackfillTopicTagsParams{TenantID: "t1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Failed) != 1 {
		t.Fatalf("empty classification must FAIL the atom, got %+v", res)
	}
	if len(repo.saved) != 0 || len(pub.events) != 0 {
		t.Fatal("empty classification must not write or emit")
	}
}

func TestBackfill_SkipsOrphanEditions(t *testing.T) {
	// ADR-229 A1: orphan editions are frozen AND must never emit atom.published
	// (a projection row would make them re-sharable).
	a := tagless(t, "Orphan")
	a.OrphanedFromAtomID = "019f278f-5acb-7415-a526-eb852b6409c7"
	repo := &fakeBackfillRepo{atoms: []*atom.LearningAtom{a}}
	cls := &fakeClassifier{}
	pub := &fakeEventPub{}
	svc := newBackfill(repo, cls, pub, &fakeRevResolver{})

	res, err := svc.Run(t.Context(), atom.BackfillTopicTagsParams{TenantID: "t1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(cls.calls) != 0 {
		t.Fatal("orphan editions must never be classified")
	}
	if len(pub.events) != 0 {
		t.Fatal("orphan editions must NEVER emit atom.published.v1")
	}
	if res.Candidates != 0 {
		t.Fatalf("orphan must not be a candidate, got %d", res.Candidates)
	}
}

func TestBackfill_PublishFailureIsLoudAndDistinguishable(t *testing.T) {
	// The source write committed but the outbox insert failed: the operator MUST
	// see this — tags are at source but unprojected until a re-emit.
	a := tagless(t, "Comparing Fractions")
	repo := &fakeBackfillRepo{atoms: []*atom.LearningAtom{a}}
	cls := &fakeClassifier{tags: map[string][]string{a.AtomID: {"fractions"}}}
	pub := &fakeEventPub{failOn: a.AtomID}
	svc := newBackfill(repo, cls, pub, &fakeRevResolver{})

	res, err := svc.Run(t.Context(), atom.BackfillTopicTagsParams{TenantID: "t1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Tagged != 1 {
		t.Fatalf("source write did land — it must be counted, got tagged=%d", res.Tagged)
	}
	if res.Emitted != 0 {
		t.Fatal("emit failed — must not be counted as emitted")
	}
	if len(res.Failed) != 1 || res.Failed[0].Reason == "" {
		t.Fatalf("emit failure must be surfaced loudly, got %+v", res.Failed)
	}
}

func TestBackfill_UnresolvableRevisionFailsAtom(t *testing.T) {
	a := tagless(t, "Comparing Fractions")
	repo := &fakeBackfillRepo{atoms: []*atom.LearningAtom{a}}
	cls := &fakeClassifier{tags: map[string][]string{a.AtomID: {"fractions"}}}
	pub := &fakeEventPub{}
	svc := newBackfill(repo, cls, pub, &fakeRevResolver{missing: a.AtomID})

	res, err := svc.Run(t.Context(), atom.BackfillTopicTagsParams{TenantID: "t1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(pub.events) != 0 {
		t.Fatal("must not emit a published event with no resolvable revision")
	}
	if len(res.Failed) != 1 {
		t.Fatalf("unresolvable revision must fail the atom, got %+v", res)
	}
}

func TestBackfill_LimitBoundsTheRun(t *testing.T) {
	a1 := tagless(t, "One")
	a2 := tagless(t, "Two")
	a3 := tagless(t, "Three")
	repo := &fakeBackfillRepo{atoms: []*atom.LearningAtom{a1, a2, a3}}
	cls := &fakeClassifier{tags: map[string][]string{
		a1.AtomID: {"one"}, a2.AtomID: {"two"}, a3.AtomID: {"three"},
	}}
	pub := &fakeEventPub{}
	svc := newBackfill(repo, cls, pub, &fakeRevResolver{})

	res, err := svc.Run(t.Context(), atom.BackfillTopicTagsParams{TenantID: "t1", Limit: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Candidates != 2 || len(cls.calls) != 2 {
		t.Fatalf("limit must bound the run, candidates=%d calls=%d", res.Candidates, len(cls.calls))
	}
}

func TestBackfill_ReemitSaltSaltsTheIdempotencyKey(t *testing.T) {
	// The plain deterministic key (atom:published:rev) is permanently deduped by
	// the outbox UNIQUE index for an already-published revision, so a
	// projection-seeding re-emit needs a salt (CHO-2128 F1).
	a := tagless(t, "Comparing Fractions")
	repo := &fakeBackfillRepo{atoms: []*atom.LearningAtom{a}}
	cls := &fakeClassifier{tags: map[string][]string{a.AtomID: {"fractions"}}}
	pub := &fakeEventPub{}
	svc := newBackfill(repo, cls, pub, &fakeRevResolver{})

	if _, err := svc.Run(t.Context(), atom.BackfillTopicTagsParams{TenantID: "t1", ReemitSalt: "topictags1"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(pub.events) != 1 {
		t.Fatalf("want 1 event, got %d", len(pub.events))
	}
	key := pub.events[0].IdempotencyKey
	if want := ":reemit:topictags1"; len(key) < len(want) || key[len(key)-len(want):] != want {
		t.Fatalf("idempotency key must be salted, got %q", key)
	}
}

func TestBackfill_NilClassifierFailsLoud(t *testing.T) {
	// no-stubs: an unwired classifier must never degrade to a silent no-op run.
	svc := newBackfill(&fakeBackfillRepo{}, nil, &fakeEventPub{}, &fakeRevResolver{})
	if _, err := svc.Run(t.Context(), atom.BackfillTopicTagsParams{TenantID: "t1"}); err == nil {
		t.Fatal("want loud error when the classifier is unwired, got nil")
	}
}

func TestBackfill_TenantRequired(t *testing.T) {
	svc := newBackfill(&fakeBackfillRepo{}, &fakeClassifier{}, &fakeEventPub{}, &fakeRevResolver{})
	if _, err := svc.Run(t.Context(), atom.BackfillTopicTagsParams{}); err == nil {
		t.Fatal("want loud error on missing tenant, got nil")
	}
}

type erroringRepo struct{ fakeBackfillRepo }

func (e *erroringRepo) ListPublished(_ context.Context, _ string) ([]*atom.LearningAtom, error) {
	return nil, errors.New("db down")
}

func TestBackfill_ListErrorAbortsRunLoudly(t *testing.T) {
	// A storage failure is NOT a per-atom failure — it means we never saw the
	// candidate set, so reporting a clean run would be a lie.
	svc := newBackfill(&erroringRepo{}, &fakeClassifier{}, &fakeEventPub{}, &fakeRevResolver{})
	if _, err := svc.Run(t.Context(), atom.BackfillTopicTagsParams{TenantID: "t1"}); err == nil {
		t.Fatal("want loud error when the candidate listing fails, got nil")
	}
}

func TestBackfill_NilPublisherOnRealRunFailsLoud(t *testing.T) {
	// Writing source tags with no re-emit would leave them unprojected forever.
	svc := atom.NewTopicTagBackfillService(&fakeBackfillRepo{}, &fakeClassifier{}, nil, &fakeRevResolver{})
	if _, err := svc.Run(t.Context(), atom.BackfillTopicTagsParams{TenantID: "t1"}); err == nil {
		t.Fatal("want loud error when the publisher is unwired on a real run, got nil")
	}
}

func TestBackfill_NilPublisherIsFineOnDryRun(t *testing.T) {
	// A dry run never emits — it must not be blocked on a dep it will not use.
	a := tagless(t, "Comparing Fractions")
	repo := &fakeBackfillRepo{atoms: []*atom.LearningAtom{a}}
	cls := &fakeClassifier{tags: map[string][]string{a.AtomID: {"fractions"}}}
	svc := atom.NewTopicTagBackfillService(repo, cls, nil, nil)

	res, err := svc.Run(t.Context(), atom.BackfillTopicTagsParams{TenantID: "t1", DryRun: true})
	if err != nil {
		t.Fatalf("dry run must not require a publisher: %v", err)
	}
	if len(res.Proposed) != 1 {
		t.Fatalf("want a proposal, got %+v", res.Proposed)
	}
}
