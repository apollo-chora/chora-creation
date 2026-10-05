// coverage_gaps_test.go — coverage for the uncovered branches of the pubsub
// adapter: QuestionSubscriber.Handle guard rails (malformed event, missing
// job, repo failures), batch-settings edge shapes (invalid settings_json,
// mixed type_plan without explicit grounding), the subscriber option seams
// (WithLogger / WithInbox), the collection outbox publisher defaults
// (event-id minting, timestamp fallbacks, envelope defaults), and the
// looksLikeBatch payload-shape probe via the ai_assist terminal subscriber.
package pubsub_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-common/eventbus"
	"github.com/apollo-chora/chora-common/idempotent"

	creationoutbox "github.com/apollo-chora/chora-creation/internal/adapter/outbox"
	creationpubsub "github.com/apollo-chora/chora-creation/internal/adapter/pubsub"
	"github.com/apollo-chora/chora-creation/internal/domain/collection"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

// -----------------------------------------------------------------------------
// QuestionSubscriber.Handle — guard rails
// -----------------------------------------------------------------------------

func TestSubscriber_Handle_MalformedEvent_AckSkips(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}

	sub := creationpubsub.NewQuestionSubscriber(creationpubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: pub,
	})
	// Missing JobID / AtomID / AuthorGCID / TenantID — ack to avoid poisoning.
	if err := sub.Handle(context.Background(), creationpubsub.QuestionGenerationRequestedEvent{}); err != nil {
		t.Fatalf("Handle: %v; want nil (ack-skip)", err)
	}
	if len(repo.updateCalls) != 0 {
		t.Errorf("repo touched %d times on malformed event; want 0", len(repo.updateCalls))
	}
	if len(pub.snapshot()) != 0 {
		t.Errorf("events emitted on malformed event; want none")
	}
}

func TestSubscriber_Handle_JobNotFound_AckSkips(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}

	sub := creationpubsub.NewQuestionSubscriber(creationpubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: pub,
	})
	evt := creationpubsub.QuestionGenerationRequestedEvent{
		JobID: "no-such-job", AtomID: "atom", AuthorGCID: "gcid", TenantID: "tenant",
		QuestionType: "mcq",
	}
	if err := sub.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v; want nil (ack-skip)", err)
	}
	if len(repo.updateCalls) != 0 {
		t.Errorf("repo touched %d times on missing job; want 0", len(repo.updateCalls))
	}
	if mana.refundCalls != 0 {
		t.Errorf("Mana refunded %d times on missing job; want 0", mana.refundCalls)
	}
}

func TestSubscriber_Handle_RepoGetError_ReturnsError(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	repo.getErr = errors.New("db down")
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}

	sub := creationpubsub.NewQuestionSubscriber(creationpubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: pub,
	})
	evt := creationpubsub.QuestionGenerationRequestedEvent{
		JobID: "job", AtomID: "atom", AuthorGCID: "gcid", TenantID: "tenant",
		QuestionType: "mcq",
	}
	err := sub.Handle(context.Background(), evt)
	if err == nil {
		t.Fatal("Handle: nil error; want transient error so the receiver NACKs")
	}
	if !strings.Contains(err.Error(), "job lookup") {
		t.Errorf("error = %v; want job lookup context", err)
	}
}

func TestSubscriber_Handle_RunningTransitionError_ReturnsError(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	repo.updateErr = errors.New("db down")
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	job := seedJob(repo, tjAIDraft, question.JobStatusRequested)

	sub := creationpubsub.NewQuestionSubscriber(creationpubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: pub,
	})
	evt := creationpubsub.QuestionGenerationRequestedEvent{
		JobID: job.JobID, AtomID: job.AtomID, AuthorGCID: job.AuthorGCID,
		TenantID: job.TenantID, QuestionType: "mcq",
	}
	err := sub.Handle(context.Background(), evt)
	if err == nil {
		t.Fatal("Handle: nil error; want transient error so the receiver NACKs")
	}
	if !strings.Contains(err.Error(), "transition to running") {
		t.Errorf("error = %v; want transition context", err)
	}
	if mana.refundCalls != 0 {
		t.Errorf("Mana refunded %d times; want 0 (dispatch never happened)", mana.refundCalls)
	}
}

// -----------------------------------------------------------------------------
// Batch settings edge shapes
// -----------------------------------------------------------------------------

func TestSubscriber_Batch_InvalidSettingsJSON_UsesDefaults(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	sync := &fakeSyncPublisher{}
	pub := &fakeSubPublisher{}
	job := seedBatchJob(repo)

	sub := creationpubsub.NewQuestionSubscriber(creationpubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: pub,
		SyncPublisher: sync, QGenCrewEnabled: true,
	})
	// Malformed settings_json decodes to an empty map — count/grounding/prompt
	// all fall back to defaults rather than failing the dispatch.
	if err := sub.Handle(context.Background(), batchEvt(job, `{"count":`)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if job.Status != question.JobStatusRunning {
		t.Errorf("Status = %q; want running", job.Status)
	}
	events := sync.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 started event; got %d", len(events))
	}
	p := events[0].Payload.(map[string]any)
	if p["prompt"] != "Generate multiple-choice questions using the provided source material as a starting point." {
		t.Errorf("prompt = %v; want default batch prompt", p["prompt"])
	}
	if p["grounding_mode"] != "starting_point" {
		t.Errorf("grounding_mode = %v; want starting_point default", p["grounding_mode"])
	}
}

func TestSubscriber_Batch_MixedTypePlan_StartingPointPrompt(t *testing.T) {
	t.Parallel()
	repo := newSubJobRepo()
	mana := &fakeSubMana{}
	sync := &fakeSyncPublisher{}
	pub := &fakeSubPublisher{}
	job := seedBatchJob(repo)

	sub := creationpubsub.NewQuestionSubscriber(creationpubsub.QuestionSubscriberDeps{
		JobRepo: repo, Mana: mana, Publisher: pub,
		SyncPublisher: sync, QGenCrewEnabled: true,
	})
	// type_plan present, no context, no explicit grounding_mode → the mixed
	// default prompt takes the starting-point (open) framing.
	settings := `{"count":3,"type_plan":[` +
		`{"question_type":"mcq","count":2},` +
		`{"question_type":"oe","count":1}]}`
	if err := sub.Handle(context.Background(), batchEvt(job, settings)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	events := sync.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 started event; got %d", len(events))
	}
	p := events[0].Payload.(map[string]any)
	if p["content_type"] != "mixed" {
		t.Errorf("content_type = %v; want mixed", p["content_type"])
	}
	if p["prompt"] != "Generate a mixed set of questions per the requested type plan, using the provided source material as a starting point." {
		t.Errorf("prompt = %v; want mixed starting-point default", p["prompt"])
	}
}

// -----------------------------------------------------------------------------
// Subscriber option seams
// -----------------------------------------------------------------------------

func TestTerminalSubscriber_WithLogger_ReturnsSubscriber(t *testing.T) {
	t.Parallel()
	legacy := &fakeAiAssistJobsRepo{}
	sub := creationpubsub.NewAiAssistTerminalSubscriber(legacy).WithLogger(slog.Default())
	if sub == nil {
		t.Fatal("WithLogger returned nil")
	}
}

func TestModelAnswerAmended_WithLoggerAndInbox_ReturnSubscriber(t *testing.T) {
	t.Parallel()
	sub := creationpubsub.NewModelAnswerAmendedSubscriber(&stubAmendRepo{}).
		WithLogger(slog.Default()).
		WithInbox(idempotent.NewMemoryStore())
	if sub == nil {
		t.Fatal("WithLogger/WithInbox returned nil")
	}
	// nil inbox keeps the default store.
	sub = creationpubsub.NewModelAnswerAmendedSubscriber(&stubAmendRepo{}).WithInbox(nil)
	if sub == nil {
		t.Fatal("WithInbox(nil) returned nil")
	}
}

// -----------------------------------------------------------------------------
// Collection outbox publisher — guard rails + envelope defaults
// -----------------------------------------------------------------------------

func TestCollectionPublisher_NilStore_FailsLoud(t *testing.T) {
	t.Parallel()
	pub := creationpubsub.NewCollectionOutboxPublisher(creationpubsub.CollectionOutboxConfig{})
	c, err := collection.New(collection.NewParams{
		TenantID: wsTenantID, OwnerGcid: wsGcid, Title: "Set",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ev := collection.NewCollectionCreatedEvent(c, "", "")
	if err := pub.Publish(context.Background(), ev); err == nil {
		t.Fatal("Publish: nil error; want 'store not wired'")
	}
}

func TestCollectionPublisher_MintsDefaultsForSparseEvent(t *testing.T) {
	t.Parallel()
	store := &captureStore{}
	pub := creationpubsub.NewCollectionOutboxPublisher(creationpubsub.CollectionOutboxConfig{
		Store: store,
	})

	c, err := collection.New(collection.NewParams{
		TenantID: wsTenantID, OwnerGcid: wsGcid, Title: "Set",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ev := collection.NewCollectionCreatedEvent(c, "", "")
	// Force the sparse shape: no event id / idempotency key, an UNPARSABLE
	// occurred_at (fallback to Now), a non-nano RFC3339 published_at (second
	// parse branch), no source project/service, schema_version 0, and no
	// collection id (aggregate falls back to the minted event id).
	ev.EventID = ""
	ev.IdempotencyKey = ""
	ev.OccurredAt = "not-a-timestamp"
	ev.PublishedAt = "2026-05-26T10:00:00Z"
	ev.SourceProject = ""
	ev.SourceService = ""
	ev.SchemaVersion = 0
	ev.CollectionID = ""

	if err := pub.Publish(context.Background(), ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(store.rows) != 1 {
		t.Fatalf("rows len = %d; want 1", len(store.rows))
	}
	row := store.rows[0]
	if row.ID == "" {
		t.Errorf("event id not minted")
	}
	if row.IdempotencyKey != row.ID {
		t.Errorf("IdempotencyKey = %q; want minted event id %q", row.IdempotencyKey, row.ID)
	}
	if row.AggregateID != row.ID {
		t.Errorf("AggregateID = %q; want fallback to event id %q", row.AggregateID, row.ID)
	}
	if row.OccurredAt.IsZero() {
		t.Errorf("OccurredAt zero; want Now fallback")
	}
	if got := row.Envelope["published_at"]; got != "2026-05-26T10:00:00Z" {
		t.Errorf("envelope published_at = %q; want parsed RFC3339", got)
	}
	if row.Envelope["source_project"] != "chora-local" {
		t.Errorf("envelope source_project = %q; want default", row.Envelope["source_project"])
	}
	if row.Envelope["source_service"] != "chora-creation" {
		t.Errorf("envelope source_service = %q; want default", row.Envelope["source_service"])
	}
	if row.Envelope["schema_version"] != "1" {
		t.Errorf("envelope schema_version = %q; want 1", row.Envelope["schema_version"])
	}
}

// -----------------------------------------------------------------------------
// looksLikeBatch — probed via the ai_assist terminal subscriber. A payload
// that is neither a JSON array nor a `{candidates:[...]}` envelope routes to
// the single-candidate normalizer and fails the job (fail + refund).
// -----------------------------------------------------------------------------

func TestTerminal_Completed_MalformedObjectPayload_FailsAndRefunds(t *testing.T) {
	t.Parallel()
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	job := seedQuestionJob(qjobs, tjAIDraft, question.JobStatusRunning)

	sub := creationpubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	// '{' prefix but unparseable — not a batch envelope, falls to the single
	// normalizer which cannot resolve a question type.
	bz := completedWire(job.TenantID, job.JobID, `{broken`, false, 10)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if job.Status != question.JobStatusFailed {
		t.Errorf("Status = %q; want failed", job.Status)
	}
	if mana.refundCalls != 1 {
		t.Errorf("Mana refunded %d times; want 1", mana.refundCalls)
	}
}

func TestTerminal_Completed_NullCandidatesEnvelope_FailsAndRefunds(t *testing.T) {
	t.Parallel()
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	job := seedQuestionJob(qjobs, tjAIDraft, question.JobStatusRunning)

	sub := creationpubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	// `{candidates:null}` parses but carries no batch — single-candidate path.
	bz := completedWire(job.TenantID, job.JobID, `{"candidates":null}`, false, 10)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if job.Status != question.JobStatusFailed {
		t.Errorf("Status = %q; want failed", job.Status)
	}
	if mana.refundCalls != 1 {
		t.Errorf("Mana refunded %d times; want 1", mana.refundCalls)
	}
}

func TestTerminal_Completed_ScalarStringPayload_FailsAndRefunds(t *testing.T) {
	t.Parallel()
	legacy := &fakeAiAssistJobsRepo{}
	qjobs := newSubJobRepo()
	mana := &fakeSubMana{}
	pub := &fakeSubPublisher{}
	job := seedQuestionJob(qjobs, tjAIDraft, question.JobStatusRunning)

	sub := creationpubsub.NewAiAssistTerminalSubscriber(legacy).WithQuestionJobs(qjobs, mana, pub)

	// Non-JSON scalar — looksLikeBatch default branch → single-candidate path.
	bz := completedWire(job.TenantID, job.JobID, `zzz`, false, 10)
	if err := sub.HandleCompleted(context.Background(), eventbus.Message{Payload: bz}); err != nil {
		t.Fatalf("HandleCompleted: %v", err)
	}
	if job.Status != question.JobStatusFailed {
		t.Errorf("Status = %q; want failed", job.Status)
	}
	if mana.refundCalls != 1 {
		t.Errorf("Mana refunded %d times; want 1", mana.refundCalls)
	}
}

// Ensure the creationoutbox import stays referenced (Row shape assertions are
// made on store rows above).
var _ = creationoutbox.Row{}
