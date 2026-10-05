// Package atom_test exercises the AI-Assist domain orchestration logic per
// Comic Ch6 P12-P13 and docs/m13/phyllis-mvp-2026-05-08.md §5.3 / §6.
//
// The AIAssistService is a domain orchestrator (still pure: it depends only
// on ports — ModelBrokerClient + Repository + EventPublisher — never on
// concrete adapter types). This keeps the domain dependency-free.
package atom_test

import (
	"context"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// fakeBroker is a stub ModelBrokerClient used to drive the AIAssistService
// through the three documented paths: allow / rewrite / refuse.
type fakeBroker struct {
	resp atom.GenerateResponse
	err  error
	last atom.GenerateRequest
}

func (f *fakeBroker) Generate(_ context.Context, req atom.GenerateRequest) (atom.GenerateResponse, error) {
	f.last = req
	if f.err != nil {
		return atom.GenerateResponse{}, f.err
	}
	return f.resp, nil
}

// fakeRepo records Save calls so we can assert how many atoms were persisted.
type fakeRepo struct {
	saved []*atom.LearningAtom
}

func (f *fakeRepo) Save(_ context.Context, a *atom.LearningAtom) error {
	clone := *a
	f.saved = append(f.saved, &clone)
	return nil
}
func (f *fakeRepo) Get(_ context.Context, _, _ string) (*atom.LearningAtom, error) {
	return nil, atom.ErrNotFound
}
func (f *fakeRepo) List(_ context.Context, _ string, _ atom.ListFilter) ([]*atom.LearningAtom, error) {
	return nil, nil
}
func (f *fakeRepo) ListByCourse(_ context.Context, _, _ string) ([]*atom.LearningAtom, error) {
	return nil, nil
}

// fakePublisher records published events.
type fakePublisher struct {
	events []atom.Event
}

func (f *fakePublisher) Publish(_ context.Context, e atom.Event) error {
	f.events = append(f.events, e)
	return nil
}

func makeBrokerItems(n int) []atom.GeneratedItem {
	out := make([]atom.GeneratedItem, n)
	for i := 0; i < n; i++ {
		out[i] = atom.GeneratedItem{
			Title: "MCQ-" + itoa(i+1),
			Body:  "Question " + itoa(i+1) + " body",
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// -----------------------------------------------------------------------------
// Happy path: decision=allow → 10 atoms generated and persisted, 10 events
// -----------------------------------------------------------------------------

func TestAIAssist_AllowDecision_CreatesNAtoms(t *testing.T) {
	t.Parallel()

	broker := &fakeBroker{
		resp: atom.GenerateResponse{
			ScreeningDecision:    atom.ScreeningAllow,
			ScreeningExplanation: "ok",
			Items:                makeBrokerItems(10),
			ModelUsed:            "vertex/gemini-1.5-pro",
		},
	}
	repo := &fakeRepo{}
	pub := &fakePublisher{}
	svc := atom.NewAIAssistService(broker, repo, pub)

	resp, err := svc.Generate(context.Background(), atom.AIAssistRequest{
		TenantID:    tenantA,
		Gcid:        gcidA,
		CourseID:    courseA,
		Prompt:      "Generate 10 MCQ on Agile Estimation",
		ContentType: atom.TypeMCQ,
		Difficulty:  3,
		Count:       10,
		TraceParent: "00-aaaa-bbbb-01",
	})
	if err != nil {
		t.Fatalf("Generate unexpected error: %v", err)
	}
	if resp.ScreeningDecision != atom.ScreeningAllow {
		t.Errorf("ScreeningDecision = %q; want allow", resp.ScreeningDecision)
	}
	if len(resp.GeneratedAtomIDs) != 10 {
		t.Errorf("GeneratedAtomIDs len = %d; want 10", len(resp.GeneratedAtomIDs))
	}
	if len(repo.saved) != 10 {
		t.Errorf("repo Save calls = %d; want 10", len(repo.saved))
	}
	if len(pub.events) != 10 {
		t.Errorf("published events = %d; want 10 (one per atom)", len(pub.events))
	}
	for _, e := range pub.events {
		if e.Type != atom.EventTypeAtomCreated {
			t.Errorf("event type = %q; want atom.created.v1", e.Type)
		}
		if e.TenantID != tenantA {
			t.Errorf("event tenant_id = %q; want %s", e.TenantID, tenantA)
		}
		if e.TraceParent != "00-aaaa-bbbb-01" {
			t.Errorf("event traceparent missing: %q", e.TraceParent)
		}
	}
	for _, a := range repo.saved {
		cur := a.CurrentRevision()
		if cur == nil {
			t.Errorf("saved atom has no CurrentRevision: %+v", a)
			continue
		}
		if cur.SourceType != atom.SourceAIAssist {
			t.Errorf("saved atom SourceType = %q; want ai_assist", cur.SourceType)
		}
		if cur.SourceMetadata["screening_decision"] != "allow" {
			t.Errorf("saved atom screening_decision missing or wrong: %v", cur.SourceMetadata)
		}
		if cur.SourceMetadata["model_used"] != "vertex/gemini-1.5-pro" {
			t.Errorf("saved atom model_used = %q; want vertex/gemini-1.5-pro",
				cur.SourceMetadata["model_used"])
		}
		if a.CourseID != courseA {
			t.Errorf("saved atom course_id = %q; want %s", a.CourseID, courseA)
		}
	}
}

// -----------------------------------------------------------------------------
// Refusal path: decision=refuse → 0 atoms persisted, no events
// -----------------------------------------------------------------------------

func TestAIAssist_RefuseDecision_CreatesZeroAtoms(t *testing.T) {
	t.Parallel()

	broker := &fakeBroker{
		resp: atom.GenerateResponse{
			ScreeningDecision:    atom.ScreeningRefuse,
			ScreeningExplanation: "policy violation",
			Items:                nil,
		},
	}
	repo := &fakeRepo{}
	pub := &fakePublisher{}
	svc := atom.NewAIAssistService(broker, repo, pub)

	resp, err := svc.Generate(context.Background(), atom.AIAssistRequest{
		TenantID: tenantA, Gcid: gcidA, CourseID: courseA,
		Prompt: "Make me a hateful question", ContentType: atom.TypeMCQ,
		Difficulty: 3, Count: 5,
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if resp.ScreeningDecision != atom.ScreeningRefuse {
		t.Errorf("ScreeningDecision = %q; want refuse", resp.ScreeningDecision)
	}
	if len(resp.GeneratedAtomIDs) != 0 {
		t.Errorf("GeneratedAtomIDs len = %d; want 0 on refuse", len(resp.GeneratedAtomIDs))
	}
	if len(repo.saved) != 0 {
		t.Errorf("repo Save calls = %d; want 0 on refuse", len(repo.saved))
	}
	if len(pub.events) != 0 {
		t.Errorf("published events = %d; want 0 on refuse", len(pub.events))
	}
	if resp.ScreeningExplanation == "" {
		t.Errorf("ScreeningExplanation empty; want a reason on refuse")
	}
}

// -----------------------------------------------------------------------------
// Rewrite path: decision=rewrite → atoms still created, but with rewrite
// metadata stamped in the AtomRevision SourceMetadata.
// -----------------------------------------------------------------------------

func TestAIAssist_RewriteDecision_CreatesAtomsWithMetadata(t *testing.T) {
	t.Parallel()

	broker := &fakeBroker{
		resp: atom.GenerateResponse{
			ScreeningDecision:    atom.ScreeningRewrite,
			ScreeningExplanation: "balanced version provided",
			Items:                makeBrokerItems(3),
			ModelUsed:            "vertex/gemini-1.5-flash",
		},
	}
	repo := &fakeRepo{}
	pub := &fakePublisher{}
	svc := atom.NewAIAssistService(broker, repo, pub)

	resp, err := svc.Generate(context.Background(), atom.AIAssistRequest{
		TenantID: tenantA, Gcid: gcidA, CourseID: courseA,
		Prompt: "What if Waterfall is better than Agile?", ContentType: atom.TypeMCQ,
		Difficulty: 3, Count: 3,
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if resp.ScreeningDecision != atom.ScreeningRewrite {
		t.Errorf("ScreeningDecision = %q; want rewrite", resp.ScreeningDecision)
	}
	if len(resp.GeneratedAtomIDs) != 3 {
		t.Errorf("GeneratedAtomIDs len = %d; want 3 on rewrite", len(resp.GeneratedAtomIDs))
	}
	if len(repo.saved) != 3 {
		t.Errorf("repo Save calls = %d; want 3", len(repo.saved))
	}
	for _, a := range repo.saved {
		md := a.CurrentRevision().SourceMetadata
		if md["screening_decision"] != "rewrite" {
			t.Errorf("rewrite atom missing screening_decision metadata: %v", md)
		}
		if md["screening_explanation"] != "balanced version provided" {
			t.Errorf("rewrite atom missing screening_explanation: %v", md)
		}
	}
}

// -----------------------------------------------------------------------------
// Broker error: bubbles up; no atoms persisted; no events published
// -----------------------------------------------------------------------------

func TestAIAssist_BrokerError_BubblesUp(t *testing.T) {
	t.Parallel()

	broker := &fakeBroker{err: errors.New("model broker unavailable")}
	repo := &fakeRepo{}
	pub := &fakePublisher{}
	svc := atom.NewAIAssistService(broker, repo, pub)

	_, err := svc.Generate(context.Background(), atom.AIAssistRequest{
		TenantID: tenantA, Gcid: gcidA, CourseID: courseA,
		Prompt: "x", ContentType: atom.TypeMCQ, Difficulty: 3, Count: 1,
	})
	if err == nil {
		t.Errorf("expected error from broker; got nil")
	}
	if len(repo.saved) != 0 {
		t.Errorf("repo Save calls = %d; want 0 on broker error", len(repo.saved))
	}
	if len(pub.events) != 0 {
		t.Errorf("published events = %d; want 0 on broker error", len(pub.events))
	}
}

func TestAIAssist_PassesRequestParamsToBroker(t *testing.T) {
	t.Parallel()

	broker := &fakeBroker{
		resp: atom.GenerateResponse{
			ScreeningDecision: atom.ScreeningAllow,
			Items:             makeBrokerItems(1),
			ModelUsed:         "vertex/gemini-1.5-flash",
		},
	}
	svc := atom.NewAIAssistService(broker, &fakeRepo{}, &fakePublisher{})

	_, err := svc.Generate(context.Background(), atom.AIAssistRequest{
		TenantID: tenantA, Gcid: gcidA, CourseID: courseA,
		Prompt: "test prompt", ContentType: atom.TypeFlashcard,
		Difficulty: 4, Count: 2, TraceParent: "tp-abc",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if broker.last.Prompt != "test prompt" {
		t.Errorf("broker.Prompt = %q", broker.last.Prompt)
	}
	if broker.last.Count != 2 {
		t.Errorf("broker.Count = %d; want 2", broker.last.Count)
	}
	if broker.last.ContentType != atom.TypeFlashcard {
		t.Errorf("broker.ContentType = %q; want flashcard", broker.last.ContentType)
	}
	if broker.last.Difficulty != 4 {
		t.Errorf("broker.Difficulty = %d; want 4", broker.last.Difficulty)
	}
	if broker.last.TenantID != tenantA {
		t.Errorf("broker.TenantID = %q; want %s", broker.last.TenantID, tenantA)
	}
	if broker.last.TraceParent != "tp-abc" {
		t.Errorf("broker.TraceParent = %q; want tp-abc", broker.last.TraceParent)
	}
}

func TestAIAssist_RejectsZeroCount(t *testing.T) {
	t.Parallel()
	svc := atom.NewAIAssistService(&fakeBroker{}, &fakeRepo{}, &fakePublisher{})
	_, err := svc.Generate(context.Background(), atom.AIAssistRequest{
		TenantID: tenantA, Gcid: gcidA, CourseID: courseA,
		Prompt: "x", ContentType: atom.TypeMCQ, Difficulty: 3, Count: 0,
	})
	if err == nil {
		t.Errorf("expected error for count=0; got nil")
	}
}

func TestAIAssist_RejectsCountAbove50(t *testing.T) {
	t.Parallel()
	svc := atom.NewAIAssistService(&fakeBroker{}, &fakeRepo{}, &fakePublisher{})
	_, err := svc.Generate(context.Background(), atom.AIAssistRequest{
		TenantID: tenantA, Gcid: gcidA, CourseID: courseA,
		Prompt: "x", ContentType: atom.TypeMCQ, Difficulty: 3, Count: 51,
	})
	if err == nil {
		t.Errorf("expected error for count>50; got nil")
	}
}
