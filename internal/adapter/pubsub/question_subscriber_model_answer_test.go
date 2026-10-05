// question_subscriber_model_answer_test.go — CHO-1658 coverage.
//
// "generate model answer with AI" (intent=model_answer_fill) was repointed off
// the decommissioned us-central1 Vertex AI Agent Engine onto the LIVE GKE qgen
// crew (the ADR-169 move already done for ai_draft). When QGenCrewEnabled,
// runModelAnswer must:
//   - fetch the author's EXISTING question (stem + options/rubric + model_answer)
//     via the QuestionReader port (the dead path let the engine fetch the stem
//     itself; the crew gets it ONLY through the started event), and
//   - publish chora.creation.ai_assist.started.v2 carrying intent=
//     model_answer_fill (via stampComposeV2 off job.Intent) + existing_question_json
//     (the JSON the orchestrator decodes into input_obj["existing_question"]),
//   - leave the job RUNNING for the ai_assist terminal subscriber to finalise.
//
// Fail-loud + mana refund on a publish error OR a missing target question.
package pubsub_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/pubsub"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

// fakeQuestionReader satisfies pubsub.QuestionReader — returns a canned question
// (or an error) and records the (tenant, question_id) it was asked for so the
// test can assert the lookup is tenant-scoped on TargetQuestionID.
type fakeQuestionReader struct {
	q         *question.Question
	rev       *question.QuestionRevision
	err       error
	gotTenant string
	gotQID    string
	calls     int
}

func (r *fakeQuestionReader) GetByID(_ context.Context, tenantID, questionID string) (*question.Question, *question.QuestionRevision, error) {
	r.calls++
	r.gotTenant, r.gotQID = tenantID, questionID
	if r.err != nil {
		return nil, nil, r.err
	}
	return r.q, r.rev, nil
}

func maOEQuestion(tenantID, atomID string) *question.Question {
	rubric := &question.Rubric{Criteria: []question.RubricCriterion{
		{CriterionID: "c1", Description: "names chlorophyll as the pigment", WeightPercent: 60},
		{CriterionID: "c2", Description: "links absorption to light reactions", WeightPercent: 40},
	}}
	return &question.Question{
		QuestionID: "01970000-0000-7000-9000-0000000000q1",
		AtomID:     atomID,
		TenantID:   tenantID,
		Type:       question.TypeOpenEnded,
		Prompt:     "Explain the role of chlorophyll in photosynthesis.",
		OE: &question.OEPayload{
			ModelAnswer:    "Author placeholder answer.",
			WeightedRubric: rubric,
		},
	}
}

func maMCQQuestion(tenantID, atomID string) *question.Question {
	return &question.Question{
		QuestionID: "01970000-0000-7000-9000-0000000000q2",
		AtomID:     atomID,
		TenantID:   tenantID,
		Type:       question.TypeMCQ,
		Prompt:     "Which organelle performs photosynthesis?",
		MCQ: &question.MCQPayload{Options: []question.MCQOption{
			{OptionID: "o1", Label: "Chloroplast", IsCorrect: true, Explainer: "yes"},
			{OptionID: "o2", Label: "Mitochondrion", IsCorrect: false, Explainer: "no"},
		}},
	}
}

func modelAnswerEvt(job *question.ComposeJob, targetQID, settings string) pubsub.QuestionGenerationRequestedEvent {
	return pubsub.QuestionGenerationRequestedEvent{
		JobID:            job.JobID,
		AtomID:           job.AtomID,
		AuthorGCID:       job.AuthorGCID,
		TenantID:         job.TenantID,
		QuestionType:     "oe",
		Intent:           string(question.IntentModelAnswerFill),
		TargetQuestionID: targetQID,
		SettingsJSON:     settings,
		Traceparent:      "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	}
}

func TestSubscriber_ModelAnswer_CrewEnabled_OE_PublishesStartedWithExistingQuestion(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	sync := &fakeSyncPublisher{}
	job := seedJob(repo, tjAIModelAnswer, question.JobStatusRequested)
	reader := &fakeQuestionReader{q: maOEQuestion(job.TenantID, job.AtomID)}

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: &fakeSubPublisher{},
		SyncPublisher: sync, QGenCrewEnabled: true, QuestionReader: reader,
	})

	if err := sub.Handle(context.Background(), modelAnswerEvt(job, reader.q.QuestionID, `{"tone_hint":"formal"}`)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// The author's existing question was fetched tenant-scoped on TargetQuestionID.
	if reader.calls != 1 || reader.gotTenant != job.TenantID || reader.gotQID != reader.q.QuestionID {
		t.Fatalf("QuestionReader.GetByID(tenant=%q,qid=%q) calls=%d; want (tenant=%q,qid=%q) calls=1",
			reader.gotTenant, reader.gotQID, reader.calls, job.TenantID, reader.q.QuestionID)
	}

	events := sync.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 started event on the crew topic; got %d (model_answer_fill must dispatch to the crew, not the dead engine)", len(events))
	}
	if events[0].Topic != "chora.creation.ai_assist.started.v2" {
		t.Errorf("started topic = %q; want chora.creation.ai_assist.started.v2", events[0].Topic)
	}
	payload := events[0].Payload.(map[string]any)
	if payload["intent"] != string(question.IntentModelAnswerFill) {
		t.Errorf("intent = %v; want model_answer_fill (stampComposeV2 off job.Intent)", payload["intent"])
	}
	if payload["operation"] != question.OperationCompose {
		t.Errorf("operation = %v; want compose", payload["operation"])
	}
	if payload["question_type"] != "oe" || payload["content_type"] != "oe" {
		t.Errorf("question_type/content_type = %v/%v; want oe/oe", payload["question_type"], payload["content_type"])
	}
	if payload["prompt"] != reader.q.Prompt {
		t.Errorf("prompt = %v; want the author stem %q", payload["prompt"], reader.q.Prompt)
	}
	if payload["traceparent"] == nil || payload["traceparent"] == "" {
		t.Errorf("traceparent must be propagated into the crew run")
	}

	raw, ok := payload["existing_question_json"].(string)
	if !ok || raw == "" {
		t.Fatalf("existing_question_json missing/not a string: %T %v", payload["existing_question_json"], payload["existing_question_json"])
	}
	var eq struct {
		Stem       string `json:"stem"`
		MCQOptions []any  `json:"mcq_options"`
		OERubric   []struct {
			Criterion string  `json:"criterion"`
			Weight    float64 `json:"weight"`
		} `json:"oe_rubric"`
		ModelAnswer string `json:"model_answer"`
	}
	if err := json.Unmarshal([]byte(raw), &eq); err != nil {
		t.Fatalf("existing_question_json not valid JSON: %v (%s)", err, raw)
	}
	if eq.Stem != reader.q.Prompt {
		t.Errorf("existing_question.stem = %q; want %q", eq.Stem, reader.q.Prompt)
	}
	if eq.ModelAnswer != "Author placeholder answer." {
		t.Errorf("existing_question.model_answer = %q; want the author's current answer (agent refines)", eq.ModelAnswer)
	}
	if len(eq.OERubric) != 2 {
		t.Fatalf("existing_question.oe_rubric len = %d; want 2 (the agent contract is [{criterion,weight}])", len(eq.OERubric))
	}
	// WeightPercent 60/40 -> weight 0.6/0.4 (the qgen agent reads {criterion,weight float}).
	if eq.OERubric[0].Criterion != "names chlorophyll as the pigment" || eq.OERubric[0].Weight != 0.6 {
		t.Errorf("oe_rubric[0] = {%q,%v}; want {names chlorophyll as the pigment,0.6}", eq.OERubric[0].Criterion, eq.OERubric[0].Weight)
	}
	if len(eq.MCQOptions) != 0 {
		t.Errorf("oe question must NOT carry mcq_options; got %d", len(eq.MCQOptions))
	}

	// Job left RUNNING for the terminal subscriber; NOT synchronously succeeded.
	if job.Status != question.JobStatusRunning {
		t.Errorf("job status = %v; want running (terminal subscriber finalises the crew result)", job.Status)
	}
	if mana.refundCalls != 0 {
		t.Errorf("no mana refund on a successful dispatch; got %d refunds", mana.refundCalls)
	}
}

func TestSubscriber_ModelAnswer_CrewEnabled_MCQ_CarriesOptionsNotRubric(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	sync := &fakeSyncPublisher{}
	job := seedJob(repo, tjAIModelAnswer, question.JobStatusRequested)
	reader := &fakeQuestionReader{q: maMCQQuestion(job.TenantID, job.AtomID)}

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: &fakeSubMana{}, Publisher: &fakeSubPublisher{},
		SyncPublisher: sync, QGenCrewEnabled: true, QuestionReader: reader,
	})

	if err := sub.Handle(context.Background(), modelAnswerEvt(job, reader.q.QuestionID, `{}`)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	events := sync.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 started event; got %d", len(events))
	}
	payload := events[0].Payload.(map[string]any)
	if payload["question_type"] != "mcq" {
		t.Errorf("question_type = %v; want mcq (from the fetched question type)", payload["question_type"])
	}
	var eq struct {
		MCQOptions []struct {
			OptionID  string `json:"option_id"`
			Label     string `json:"label"`
			IsCorrect bool   `json:"is_correct"`
		} `json:"mcq_options"`
		OERubric    []any  `json:"oe_rubric"`
		ModelAnswer string `json:"model_answer"`
	}
	if err := json.Unmarshal([]byte(payload["existing_question_json"].(string)), &eq); err != nil {
		t.Fatalf("existing_question_json not valid JSON: %v", err)
	}
	if len(eq.MCQOptions) != 2 || eq.MCQOptions[0].OptionID != "o1" || eq.MCQOptions[0].Label != "Chloroplast" || !eq.MCQOptions[0].IsCorrect {
		t.Errorf("mcq_options = %+v; want 2 {option_id,label,is_correct} entries (agent contract)", eq.MCQOptions)
	}
	if len(eq.OERubric) != 0 || eq.ModelAnswer != "" {
		t.Errorf("mcq question must NOT carry oe_rubric/model_answer; got rubric=%d answer=%q", len(eq.OERubric), eq.ModelAnswer)
	}
}

func TestSubscriber_ModelAnswer_CrewEnabled_PublishFails_RefundsMana(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	sync := &fakeSyncPublisher{failErr: errors.New("broker down")}
	job := seedJob(repo, tjAIModelAnswer, question.JobStatusRequested)
	reader := &fakeQuestionReader{q: maOEQuestion(job.TenantID, job.AtomID)}

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: &fakeSubPublisher{},
		SyncPublisher: sync, QGenCrewEnabled: true, QuestionReader: reader,
	})

	if err := sub.Handle(context.Background(), modelAnswerEvt(job, reader.q.QuestionID, `{}`)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if job.Status != question.JobStatusFailed {
		t.Errorf("job status = %v; want failed (load-bearing publish must fail loud)", job.Status)
	}
	if mana.refundCalls != 1 || mana.lastRefund.Units != job.ManaCharged {
		t.Errorf("refund=%d units=%d; want 1 refund of %d (charge-then-fail must refund)", mana.refundCalls, mana.lastRefund.Units, job.ManaCharged)
	}
}

func TestSubscriber_ModelAnswer_CrewEnabled_QuestionNotFound_FailsLoud_NoPublish(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	sync := &fakeSyncPublisher{}
	job := seedJob(repo, tjAIModelAnswer, question.JobStatusRequested)
	reader := &fakeQuestionReader{err: question.ErrNotFound}

	sub := pubsub.NewQuestionSubscriber(pubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: &fakeSubPublisher{},
		SyncPublisher: sync, QGenCrewEnabled: true, QuestionReader: reader,
	})

	if err := sub.Handle(context.Background(), modelAnswerEvt(job, "01970000-0000-7000-9000-0000000000q9", `{}`)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(sync.snapshot()) != 0 {
		t.Errorf("must NOT publish a started event when the target question cannot be loaded")
	}
	if job.Status != question.JobStatusFailed {
		t.Errorf("job status = %v; want failed (no existing question = fail loud, not a fabricated success)", job.Status)
	}
	if mana.refundCalls != 1 {
		t.Errorf("refund=%d; want 1 (charge-then-fail must refund)", mana.refundCalls)
	}
}
