package pubsub_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/envelope"
	"github.com/apollo-chora/chora-common/eventbus"

	"github.com/apollo-chora/chora-creation/internal/adapter/pubsub"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

// ---- stub QuestionAmendPort --------------------------------------------------

type stubAmendRepo struct {
	q         *question.Question
	getErr    error
	appendErr error
	appended  []*question.QuestionRevision
	getCalls  int
}

func (r *stubAmendRepo) GetByID(_ context.Context, _, _ string) (*question.Question, *question.QuestionRevision, error) {
	r.getCalls++
	if r.getErr != nil {
		return nil, nil, r.getErr
	}
	return r.q, nil, nil
}

func (r *stubAmendRepo) AppendRevision(_ context.Context, rev *question.QuestionRevision) error {
	if r.appendErr != nil {
		return r.appendErr
	}
	r.appended = append(r.appended, rev)
	return nil
}

// ---- fixtures ----------------------------------------------------------------

func oeQuestion() *question.Question {
	return &question.Question{
		QuestionID: "q-1", AtomID: "atom-1", TenantID: "tenant-1", AuthorGcid: "author-1",
		Type: question.TypeOpenEnded, Prompt: "Explain photosynthesis.",
		SourceType: atom.SourceManual, Revision: 1,
		OE: &question.OEPayload{
			ModelAnswer: "Old reference answer about light reactions.",
			WeightedRubric: &question.Rubric{Criteria: []question.RubricCriterion{
				{CriterionID: "c1", Description: "Accuracy", WeightPercent: 100},
			}},
		},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
}

func amendEventBytes(t *testing.T, newAnswer string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"submission_id":        "sub-1",
		"assessment_id":        "assess-1",
		"test_set_question_id": "tsq-1",
		"question_id":          "q-1",
		"atom_id":              "atom-1",
		"new_model_answer":     newAnswer,
		"actor_gcid":           "instructor-9",
		"amended_at":           "2026-06-02T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func attrs() envelope.Envelope {
	return envelope.Envelope{TenantID: "tenant-1", EventID: "evt-abc"}
}

// ---- tests -------------------------------------------------------------------

func TestModelAnswerAmended_AppendsRevisionPreservingRubric(t *testing.T) {
	repo := &stubAmendRepo{q: oeQuestion()}
	sub := pubsub.NewModelAnswerAmendedSubscriber(repo)

	err := sub.Handle(context.Background(), eventbus.Message{Payload: amendEventBytes(t, "Corrected: chlorophyll absorbs photons; ATP+NADPH form."), Envelope: attrs()})
	if err != nil {
		t.Fatalf("Handle: unexpected error: %v", err)
	}
	if len(repo.appended) != 1 {
		t.Fatalf("want 1 appended revision, got %d", len(repo.appended))
	}
	rev := repo.appended[0]
	if rev.OEPayload == nil || rev.OEPayload.ModelAnswer != "Corrected: chlorophyll absorbs photons; ATP+NADPH form." {
		t.Errorf("model_answer not updated: %+v", rev.OEPayload)
	}
	// Rubric preserved from the prior revision.
	if rev.OEPayload.WeightedRubric == nil || len(rev.OEPayload.WeightedRubric.Criteria) != 1 ||
		rev.OEPayload.WeightedRubric.Criteria[0].CriterionID != "c1" {
		t.Errorf("rubric not preserved: %+v", rev.OEPayload.WeightedRubric)
	}
	// Provenance: HUMAN author + grading-amendment source tag + refs.
	if rev.AuthoredByGCID != "instructor-9" {
		t.Errorf("AuthoredByGCID = %q, want instructor-9", rev.AuthoredByGCID)
	}
	if rev.SourceMetadata["source"] != pubsub.RevisionSourceGradingAmendment {
		t.Errorf("source tag = %q, want %q", rev.SourceMetadata["source"], pubsub.RevisionSourceGradingAmendment)
	}
	if rev.SourceMetadata["submission_id"] != "sub-1" || rev.SourceMetadata["assessment_id"] != "assess-1" {
		t.Errorf("provenance refs missing: %+v", rev.SourceMetadata)
	}
}

func TestModelAnswerAmended_IdempotentOnEventID(t *testing.T) {
	repo := &stubAmendRepo{q: oeQuestion()}
	sub := pubsub.NewModelAnswerAmendedSubscriber(repo)
	ev := amendEventBytes(t, "New answer.")

	for i := 0; i < 2; i++ {
		if err := sub.Handle(context.Background(), eventbus.Message{Payload: ev, Envelope: attrs()}); err != nil {
			t.Fatalf("Handle #%d: %v", i, err)
		}
	}
	if len(repo.appended) != 1 {
		t.Fatalf("idempotency broken: want 1 append, got %d", len(repo.appended))
	}
}

func TestModelAnswerAmended_UnknownQuestionAcks(t *testing.T) {
	repo := &stubAmendRepo{getErr: question.ErrNotFound}
	sub := pubsub.NewModelAnswerAmendedSubscriber(repo)

	if err := sub.Handle(context.Background(), eventbus.Message{Payload: amendEventBytes(t, "New."), Envelope: attrs()}); err != nil {
		t.Fatalf("want ACK (nil) on unknown question, got %v", err)
	}
	if len(repo.appended) != 0 {
		t.Errorf("want no append on unknown question, got %d", len(repo.appended))
	}
}

func TestModelAnswerAmended_NonOEQuestionAcks(t *testing.T) {
	q := oeQuestion()
	q.Type = question.TypeMCQ
	q.OE = nil
	repo := &stubAmendRepo{q: q}
	sub := pubsub.NewModelAnswerAmendedSubscriber(repo)

	if err := sub.Handle(context.Background(), eventbus.Message{Payload: amendEventBytes(t, "New."), Envelope: attrs()}); err != nil {
		t.Fatalf("want ACK (nil) on non-OE question, got %v", err)
	}
	if len(repo.appended) != 0 {
		t.Errorf("want no append on non-OE question, got %d", len(repo.appended))
	}
}

func TestModelAnswerAmended_EmptyAnswerAcks(t *testing.T) {
	repo := &stubAmendRepo{q: oeQuestion()}
	sub := pubsub.NewModelAnswerAmendedSubscriber(repo)

	if err := sub.Handle(context.Background(), eventbus.Message{Payload: amendEventBytes(t, "   "), Envelope: attrs()}); err != nil {
		t.Fatalf("want ACK (nil) on empty model answer, got %v", err)
	}
	if len(repo.appended) != 0 {
		t.Errorf("want no append on empty answer, got %d", len(repo.appended))
	}
}

func TestModelAnswerAmended_DecodeFailureNacks(t *testing.T) {
	repo := &stubAmendRepo{q: oeQuestion()}
	sub := pubsub.NewModelAnswerAmendedSubscriber(repo)

	if err := sub.Handle(context.Background(), eventbus.Message{Payload: []byte("{not json"), Envelope: attrs()}); err == nil {
		t.Fatal("want NACK (error) on decode failure, got nil")
	}
}

func TestModelAnswerAmended_MissingTenantNacks(t *testing.T) {
	repo := &stubAmendRepo{q: oeQuestion()}
	sub := pubsub.NewModelAnswerAmendedSubscriber(repo)

	if err := sub.Handle(context.Background(), eventbus.Message{Payload: amendEventBytes(t, "New."), Envelope: envelope.Envelope{EventID: "e1"}}); err == nil {
		t.Fatal("want NACK (error) on missing tenant_id, got nil")
	}
}
