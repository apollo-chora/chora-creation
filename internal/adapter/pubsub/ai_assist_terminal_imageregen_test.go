// ai_assist_terminal_imageregen_test.go — RED→GREEN coverage for the CHO-1819
// P3c review-image-regenerate terminal path.
//
// An image_regen completed.v1 (published by the orchestrator's ImageRegenRunner)
// carries candidate_payload_json as a 1-element image-patch array
// [{draft_id, placement(stem|answer), image_url}] and assist_id = the
// image_regen job's OWN id. The image_regen job's settings carry
// {parent_job_id, draft_id, placement}. The terminal subscriber:
//
//   - resolves assist_id → an image_regen job (running)
//   - reads the parent_job_id from its settings, loads the PARENT batch job
//   - rewrites the targeted candidate's image (stem→image_url,
//     answer→answer_image_url) on the parent IN PLACE — parent stays succeeded
//   - marks the image_regen job succeeded (+ stamps the patch on its own
//     candidates so the FE poll surfaces the new url without a parent re-fetch)
//
// Fail-soft: a missing parent, an unusable payload, or a vanished draft fails
// the image_regen job (ACK) without touching the parent. A repo-write failure
// NACKs (broker retries).
package pubsub_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-common/envelope"
	"github.com/apollo-chora/chora-common/eventbus"

	"github.com/apollo-chora/chora-creation/internal/adapter/pubsub"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// imageRegenSettings builds the image_regen job's settings JSONB (written by
// createImageRegenJob): parent + the originally-requested target.
func imageRegenSettings(parentJobID, draftID, placement string) []byte {
	return []byte(`{"parent_job_id":"` + parentJobID + `","draft_id":"` + draftID +
		`","placement":"` + placement + `","prompt":"a clearer diagram","mode":""}`)
}

// imagePatchArray is the ImageRegenRunner completed.v1 candidate_payload_json:
// a 1-element image-patch array.
func imagePatchArray(draftID, placement, url string) string {
	return `[{"draft_id":"` + draftID + `","placement":"` + placement + `","image_url":"` + url + `"}]`
}

// parentMCQCandidate builds one persisted candidateDraft (mcq) with optional
// pre-existing stem/answer image urls at the CANONICAL top-level location
// (CHO-1825 — siblings of mcq_payload, not nested inside it).
func parentMCQCandidate(draftID, imageURL, answerImageURL string) string {
	imgs := ""
	if imageURL != "" {
		imgs += `,"image_url":"` + imageURL + `"`
	}
	if answerImageURL != "" {
		imgs += `,"answer_image_url":"` + answerImageURL + `"`
	}
	return `{"draft_id":"` + draftID + `","type":"mcq","prompt":"Q?",` +
		`"mcq_payload":{"options":[{"option_id":"1","label":"A","is_correct":true,"explainer":"e"},` +
		`{"option_id":"2","label":"B","is_correct":false,"explainer":"e"}]}` + imgs + `}`
}

// parentOECandidate builds one persisted candidateDraft (oe). Image url is
// top-level (CHO-1825), a sibling of oe_payload.
func parentOECandidate(draftID, imageURL string) string {
	imgs := ""
	if imageURL != "" {
		imgs += `,"image_url":"` + imageURL + `"`
	}
	return `{"draft_id":"` + draftID + `","type":"oe","prompt":"Discuss.",` +
		`"oe_payload":{"model_answer":"A full model answer."}` + imgs + `}`
}

// draftImages decodes a persisted candidates array and returns the targeted
// draft's stem + answer image urls (nil when absent) from the CANONICAL
// top-level location (CHO-1825). found is true on a draft_id match.
func draftImages(t *testing.T, candJSON []byte, draftID string) (stem, answer *string, found bool) {
	t.Helper()
	var drafts []struct {
		DraftID        string  `json:"draft_id"`
		ImageURL       *string `json:"image_url"`
		AnswerImageURL *string `json:"answer_image_url"`
	}
	if err := json.Unmarshal(candJSON, &drafts); err != nil {
		t.Fatalf("parent candidates not a JSON array: %v (%s)", err, candJSON)
	}
	for _, d := range drafts {
		if d.DraftID != draftID {
			continue
		}
		return d.ImageURL, d.AnswerImageURL, true
	}
	return nil, nil, false
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// seedImageRegenPair seeds a succeeded parent batch job (with the given
// candidates) + a running image_regen job whose settings target (draftID,
// placement) on that parent. Both share the seed tenant.
func seedImageRegenPair(repo *fakeSubJobRepo, parentCandidates, draftID, placement string) (parent, imageRegen *question.ComposeJob) {
	parent = seedQuestionJob(repo, tjBatchSourceMaterial, question.JobStatusSucceeded)
	parent.CandidateQuestionsJSON = []byte(parentCandidates)
	imageRegen = seedQuestionJob(repo, tjImageRegen, question.JobStatusRunning)
	imageRegen.SettingsJSON = imageRegenSettings(parent.JobID, draftID, placement)
	return parent, imageRegen
}

// -----------------------------------------------------------------------------
// happy paths
// -----------------------------------------------------------------------------

func TestTerminal_ImageRegen_Completed_PatchesParentStem_MCQ(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	parent, irj := seedImageRegenPair(qjobs, "["+parentMCQCandidate("D1", "", "")+"]", "D1", "stem")

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	bz := completedWire(irj.TenantID, irj.JobID, imagePatchArray("D1", "stem", "https://cdn/new-stem.png"), false, 0)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz, Envelope: envelope.Envelope{TenantID: irj.TenantID}}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}

	// Parent: stem image set, status unchanged.
	if parent.Status != question.JobStatusSucceeded {
		t.Errorf("parent Status = %q; want succeeded (must be preserved)", parent.Status)
	}
	stem, answer, found := draftImages(t, parent.CandidateQuestionsJSON, "D1")
	if !found {
		t.Fatal("draft D1 missing from parent candidates after patch")
	}
	if deref(stem) != "https://cdn/new-stem.png" {
		t.Errorf("parent D1 image_url = %q; want https://cdn/new-stem.png", deref(stem))
	}
	if answer != nil {
		t.Errorf("parent D1 answer_image_url = %q; want untouched (nil)", deref(answer))
	}

	// image_regen job: succeeded; carries the patch on its own candidates (FE poll).
	if irj.Status != question.JobStatusSucceeded {
		t.Errorf("image_regen Status = %q; want succeeded", irj.Status)
	}
	if len(irj.CandidateQuestionsJSON) == 0 {
		t.Error("image_regen job's own candidates not stamped (FE poll can't read the new url)")
	}

	// Legacy ai_assist_jobs path NOT touched.
	if len(legacy.completedCalls) != 0 {
		t.Errorf("legacy UpdateCompleted called %d times; want 0", len(legacy.completedCalls))
	}
	// generation_completed.v1 emitted.
	if ev := pub.snapshot(); len(ev) != 1 || ev[0].Topic != "chora.creation.question.generation_completed.v2" {
		t.Errorf("expected 1 generation_completed event; got %+v", ev)
	}
}

// ADR-210 — a regen patch carries the NEW durable gs:// object path; the
// terminal subscriber persists it (image_gcs_uri for a stem regen) so a
// SUBSEQUENT regen edits THIS result, not the stale original.
func TestTerminal_ImageRegen_Completed_PersistsNewImageGcsURI_Stem(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	parent, irj := seedImageRegenPair(qjobs, "["+parentMCQCandidate("D1", "", "")+"]", "D1", "stem")
	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	patch := `[{"draft_id":"D1","placement":"stem","image_url":"https://cdn/new-stem.png",` +
		`"image_gcs_uri":"gs://chora-atom-media-dev/t/a/new-stem.png"}]`
	bz := completedWire(irj.TenantID, irj.JobID, patch, false, 0)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}

	var drafts []map[string]any
	if err := json.Unmarshal(parent.CandidateQuestionsJSON, &drafts); err != nil {
		t.Fatalf("unmarshal parent candidates: %v", err)
	}
	found := false
	for _, d := range drafts {
		if d["draft_id"] != "D1" {
			continue
		}
		found = true
		if d["image_gcs_uri"] != "gs://chora-atom-media-dev/t/a/new-stem.png" {
			t.Errorf("D1 image_gcs_uri = %v; want the new durable gs://", d["image_gcs_uri"])
		}
		if _, ok := d["answer_image_gcs_uri"]; ok {
			t.Errorf("answer_image_gcs_uri must stay absent for a stem patch")
		}
	}
	if !found {
		t.Fatal("draft D1 missing after patch")
	}
}

func TestTerminal_ImageRegen_Completed_PatchesParentAnswer_MCQ(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	// Pre-existing stem image must survive an answer-placement regen.
	parent, irj := seedImageRegenPair(qjobs, "["+parentMCQCandidate("D1", "https://cdn/old-stem.png", "")+"]", "D1", "answer")

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	bz := completedWire(irj.TenantID, irj.JobID, imagePatchArray("D1", "answer", "https://cdn/new-answer.png"), false, 0)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	stem, answer, found := draftImages(t, parent.CandidateQuestionsJSON, "D1")
	if !found {
		t.Fatal("draft D1 missing after answer patch")
	}
	if deref(answer) != "https://cdn/new-answer.png" {
		t.Errorf("parent D1 answer_image_url = %q; want https://cdn/new-answer.png", deref(answer))
	}
	if deref(stem) != "https://cdn/old-stem.png" {
		t.Errorf("parent D1 image_url = %q; want unchanged old-stem", deref(stem))
	}
}

func TestTerminal_ImageRegen_Completed_PatchesParent_OE(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	// Two-draft parent; only D2 (oe) is targeted — D1 must be untouched.
	cands := "[" + parentMCQCandidate("D1", "https://cdn/d1.png", "") + "," + parentOECandidate("D2", "") + "]"
	parent, irj := seedImageRegenPair(qjobs, cands, "D2", "stem")

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	bz := completedWire(irj.TenantID, irj.JobID, imagePatchArray("D2", "stem", "https://cdn/oe-stem.png"), false, 0)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	stem, _, found := draftImages(t, parent.CandidateQuestionsJSON, "D2")
	if !found || deref(stem) != "https://cdn/oe-stem.png" {
		t.Errorf("parent D2 oe image_url = %q; want https://cdn/oe-stem.png", deref(stem))
	}
	// D1 untouched.
	d1stem, _, _ := draftImages(t, parent.CandidateQuestionsJSON, "D1")
	if deref(d1stem) != "https://cdn/d1.png" {
		t.Errorf("untargeted draft D1 image_url = %q; want unchanged https://cdn/d1.png", deref(d1stem))
	}
}

// -----------------------------------------------------------------------------
// fail-soft (ACK, parent untouched, image_regen job failed)
// -----------------------------------------------------------------------------

func TestTerminal_ImageRegen_Completed_DraftNotFound_FailsImageRegen(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	parent, irj := seedImageRegenPair(qjobs, "["+parentMCQCandidate("D1", "", "")+"]", "GONE", "stem")

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, &fakeSubPublisher{})

	// Payload targets a draft that is no longer in the parent.
	bz := completedWire(irj.TenantID, irj.JobID, imagePatchArray("GONE", "stem", "https://cdn/x.png"), false, 0)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: vanished draft must ACK (fail-soft); got %v", err)
	}
	if irj.Status != question.JobStatusFailed {
		t.Errorf("image_regen Status = %q; want failed", irj.Status)
	}
	if parent.Status != question.JobStatusSucceeded {
		t.Errorf("parent Status = %q; want untouched succeeded", parent.Status)
	}
	// Parent candidate D1 must be byte-identical (no spurious patch).
	if _, _, ok := draftImages(t, parent.CandidateQuestionsJSON, "D1"); !ok {
		t.Error("parent D1 lost during a failed regen")
	}
	d1stem, _, _ := draftImages(t, parent.CandidateQuestionsJSON, "D1")
	if d1stem != nil {
		t.Errorf("parent D1 image_url = %q; want nil (untouched)", deref(d1stem))
	}
}

func TestTerminal_ImageRegen_Completed_NoParentInSettings_FailsImageRegen(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	irj := seedQuestionJob(qjobs, tjImageRegen, question.JobStatusRunning)
	irj.SettingsJSON = []byte(`{"draft_id":"D1","placement":"stem","prompt":"p"}`) // no parent_job_id

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	bz := completedWire(irj.TenantID, irj.JobID, imagePatchArray("D1", "stem", "https://cdn/x.png"), false, 0)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: misconfigured image_regen must ACK; got %v", err)
	}
	if irj.Status != question.JobStatusFailed {
		t.Errorf("image_regen Status = %q; want failed (no parent_job_id)", irj.Status)
	}
}

func TestTerminal_ImageRegen_Completed_ParentGone_FailsImageRegen(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	irj := seedQuestionJob(qjobs, tjImageRegen, question.JobStatusRunning)
	// parent_job_id points at a job that does not exist.
	irj.SettingsJSON = imageRegenSettings("00000000-0000-7000-8000-0000000bbbbb", "D1", "stem")

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	bz := completedWire(irj.TenantID, irj.JobID, imagePatchArray("D1", "stem", "https://cdn/x.png"), false, 0)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: vanished parent must ACK; got %v", err)
	}
	if irj.Status != question.JobStatusFailed {
		t.Errorf("image_regen Status = %q; want failed (parent gone)", irj.Status)
	}
}

func TestTerminal_ImageRegen_Completed_UnusablePayload_FailsImageRegen(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	parent, irj := seedImageRegenPair(qjobs, "["+parentMCQCandidate("D1", "", "")+"]", "D1", "stem")

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	// Empty patch array — nothing to apply.
	bz := completedWire(irj.TenantID, irj.JobID, `[]`, false, 0)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: unusable payload must ACK; got %v", err)
	}
	if irj.Status != question.JobStatusFailed {
		t.Errorf("image_regen Status = %q; want failed (empty patch)", irj.Status)
	}
	if parent.Status != question.JobStatusSucceeded {
		t.Errorf("parent Status = %q; want untouched", parent.Status)
	}
}

func TestTerminal_ImageRegen_Completed_MissingURL_FailsImageRegen(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	_, irj := seedImageRegenPair(qjobs, "["+parentMCQCandidate("D1", "", "")+"]", "D1", "stem")

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	// Patch entry without image_url — render produced no usable url.
	bz := completedWire(irj.TenantID, irj.JobID, `[{"draft_id":"D1","placement":"stem"}]`, false, 0)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: missing url must ACK; got %v", err)
	}
	if irj.Status != question.JobStatusFailed {
		t.Errorf("image_regen Status = %q; want failed (missing image_url)", irj.Status)
	}
}

func TestTerminal_ImageRegen_Completed_BareObjectPayload_Patches(t *testing.T) {
	// The runner emits a 1-element array today, but a bare object is tolerated.
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	parent, irj := seedImageRegenPair(qjobs, "["+parentMCQCandidate("D1", "", "")+"]", "D1", "stem")

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	bz := completedWire(irj.TenantID, irj.JobID, `{"draft_id":"D1","placement":"stem","image_url":"https://cdn/bare.png"}`, false, 0)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	stem, _, _ := draftImages(t, parent.CandidateQuestionsJSON, "D1")
	if deref(stem) != "https://cdn/bare.png" {
		t.Errorf("D1 image_url = %q; want https://cdn/bare.png (bare-object tolerated)", deref(stem))
	}
}

func TestTerminal_ImageRegen_Completed_PayloadOmitsTarget_FallsBackToSettings(t *testing.T) {
	// A payload missing draft_id/placement falls back to the originally-requested
	// target carried in the image_regen job's settings.
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	parent, irj := seedImageRegenPair(qjobs, "["+parentMCQCandidate("D1", "", "")+"]", "D1", "answer")

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	bz := completedWire(irj.TenantID, irj.JobID, `[{"image_url":"https://cdn/fallback.png"}]`, false, 0)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	_, answer, _ := draftImages(t, parent.CandidateQuestionsJSON, "D1")
	if deref(answer) != "https://cdn/fallback.png" {
		t.Errorf("D1 answer_image_url = %q; want fallback (placement from settings)", deref(answer))
	}
}

func TestTerminal_ImageRegen_Completed_ParentCandidateNoPayload_FailsImageRegen(t *testing.T) {
	// A parent candidate with neither mcq nor oe payload can't be patched →
	// fail-soft (ACK), parent untouched.
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	parent, irj := seedImageRegenPair(qjobs, `[{"draft_id":"D1","type":"mcq","prompt":"Q?"}]`, "D1", "stem")

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	bz := completedWire(irj.TenantID, irj.JobID, imagePatchArray("D1", "stem", "https://cdn/x.png"), false, 0)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if irj.Status != question.JobStatusFailed {
		t.Errorf("image_regen Status = %q; want failed (candidate has no payload)", irj.Status)
	}
	if parent.Status != question.JobStatusSucceeded {
		t.Errorf("parent Status = %q; want untouched", parent.Status)
	}
}

func TestTerminal_ImageRegen_Completed_ParentNoCandidates_FailsImageRegen(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	parent := seedQuestionJob(qjobs, tjBatchSourceMaterial, question.JobStatusSucceeded)
	// parent.CandidateQuestionsJSON intentionally left empty.
	irj := seedQuestionJob(qjobs, tjImageRegen, question.JobStatusRunning)
	irj.SettingsJSON = imageRegenSettings(parent.JobID, "D1", "stem")

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	bz := completedWire(irj.TenantID, irj.JobID, imagePatchArray("D1", "stem", "https://cdn/x.png"), false, 0)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if irj.Status != question.JobStatusFailed {
		t.Errorf("image_regen Status = %q; want failed (parent has no candidates)", irj.Status)
	}
}

func TestTerminal_ImageRegen_Completed_ParentNotReviewable_FailsImageRegen(t *testing.T) {
	// A regen completing AFTER the parent left review (accepted/cancelled) must
	// not patch the now-historical row — fail-soft (ACK), parent untouched.
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	parent, irj := seedImageRegenPair(qjobs, "["+parentMCQCandidate("D1", "https://cdn/old.png", "")+"]", "D1", "stem")
	parent.Status = question.JobStatusAccepted // the accept won the race

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	bz := completedWire(irj.TenantID, irj.JobID, imagePatchArray("D1", "stem", "https://cdn/new.png"), false, 0)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: parent-not-reviewable must ACK; got %v", err)
	}
	if irj.Status != question.JobStatusFailed {
		t.Errorf("image_regen Status = %q; want failed (parent not reviewable)", irj.Status)
	}
	stem, _, _ := draftImages(t, parent.CandidateQuestionsJSON, "D1")
	if deref(stem) != "https://cdn/old.png" {
		t.Errorf("parent D1 image_url = %q; want unchanged (no patch after review)", deref(stem))
	}
}

// -----------------------------------------------------------------------------
// NACK (transient repo failure → broker retries)
// -----------------------------------------------------------------------------

func TestTerminal_ImageRegen_Completed_PatchWriteErr_NACKs(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	_, irj := seedImageRegenPair(qjobs, "["+parentMCQCandidate("D1", "", "")+"]", "D1", "stem")
	qjobs.updateErr = errors.New("db down") // PatchCandidates → transient

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	bz := completedWire(irj.TenantID, irj.JobID, imagePatchArray("D1", "stem", "https://cdn/x.png"), false, 0)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err == nil {
		t.Fatal("expected PatchCandidates write error to propagate for NACK")
	}
}

// -----------------------------------------------------------------------------
// idempotency / replay guard + refused
// -----------------------------------------------------------------------------

func TestTerminal_ImageRegen_Completed_AlreadyTerminal_FallsThroughToLegacy(t *testing.T) {
	// A duplicate completed.v1 after the image_regen job already succeeded must
	// NOT re-claim (status != running) — it falls through to the (idempotent)
	// legacy path. The parent stays as it was.
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	parent, irj := seedImageRegenPair(qjobs, "["+parentMCQCandidate("D1", "https://cdn/already.png", "")+"]", "D1", "stem")
	irj.Status = question.JobStatusSucceeded // already terminal

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, &fakeSubMana{}, &fakeSubPublisher{})

	bz := completedWire(irj.TenantID, irj.JobID, imagePatchArray("D1", "stem", "https://cdn/dup.png"), false, 0)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if len(legacy.completedCalls) != 1 {
		t.Errorf("legacy UpdateCompleted called %d times; want 1 (replay falls through)", len(legacy.completedCalls))
	}
	// Parent unchanged by the duplicate.
	stem, _, _ := draftImages(t, parent.CandidateQuestionsJSON, "D1")
	if deref(stem) != "https://cdn/already.png" {
		t.Errorf("parent D1 image_url = %q; want unchanged (no re-patch on replay)", deref(stem))
	}
}

func TestTerminal_ImageRegen_Refused_FailsImageRegen_ParentUntouched(t *testing.T) {
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	parent, irj := seedImageRegenPair(qjobs, "["+parentMCQCandidate("D1", "https://cdn/keep.png", "")+"]", "D1", "stem")

	sub := pubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	// ImageRegenRunner refuses with a domain-specific (non-aiassist) reason.
	bz := refusedWire(irj.TenantID, irj.JobID, "image_regen_render_failed", "The image could not be regenerated.", 10)
	if err := sub.HandleRefused(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleRefused: %v", err)
	}
	if irj.Status != question.JobStatusFailed {
		t.Errorf("image_regen Status = %q; want failed", irj.Status)
	}
	if parent.Status != question.JobStatusSucceeded {
		t.Errorf("parent Status = %q; want untouched succeeded", parent.Status)
	}
	stem, _, _ := draftImages(t, parent.CandidateQuestionsJSON, "D1")
	if deref(stem) != "https://cdn/keep.png" {
		t.Errorf("parent D1 image_url = %q; want unchanged on refusal", deref(stem))
	}
	// Refund-on-failure (AC: insufficient → refund on failure).
	if mana.refundCalls != 1 {
		t.Errorf("refund fired %d times; want 1 (refund on failure)", mana.refundCalls)
	}
	// Legacy refused path NOT touched (the domain reason would fail its enum).
	if len(legacy.refusedCalls) != 0 {
		t.Errorf("legacy UpdateRefused called %d times; want 0", len(legacy.refusedCalls))
	}
}
