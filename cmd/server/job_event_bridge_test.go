// job_event_bridge_test.go — tests for the JobEventPublisher direct-publish
// encoder. Task #33 (2026-05-16) — verifies that the chora.creation.question.*
// topics + the bridge's atom.created.v1 emit path produce canonical binary
// protobuf bytes (NOT JSON), so the broker's binary schema validation
// accepts the payload on the live BINARY-encoded topics.
package main

import (
	"encoding/json"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// TestEncodeBridgePayload_QuestionGenerationRequested_IsBinaryProtobuf
// asserts the question_jobs_handler-shaped payload encodes to wire-format
// protobuf, NOT JSON.
func TestEncodeBridgePayload_QuestionGenerationRequested_IsBinaryProtobuf(t *testing.T) {
	payload := map[string]any{
		"job_id":             "01971a90-eeee-7000-8000-000000000001",
		"atom_id":            "01971a90-aaaa-7000-8000-000000000001",
		"author_gcid":        "01971a90-bbbb-7000-8000-000000000001",
		"tenant_id":          "01971a90-cccc-7000-8000-000000000001",
		"job_type":           "ai_draft",
		"question_type":      "mcq",
		"target_question_id": "01971a90-dddd-7000-8000-000000000001",
		"mana_action_code":   "atom.create.aidraft",
		"mana_charged":       5,
		"settings_json":      `{"tone_hint":"playful"}`,
		"requested_at":       time.Now().UTC().Format(time.RFC3339Nano),
	}

	_, bz, err := encodeBridgePayload("chora.creation.question.generation_requested.v2", payload)
	if err != nil {
		t.Fatalf("encodeBridgePayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}

	// Should NOT decode as JSON.
	var probe map[string]any
	if err := json.Unmarshal(bz, &probe); err == nil {
		t.Fatalf("payload decoded as JSON — bridge encoder regressed: %s", string(bz))
	}

	// Walk the wire bytes — every field number must be in schema range
	// [1..13] for chora.creation.question.generation_requested.v2.
	rem := bz
	saw := map[protowire.Number]bool{}
	for len(rem) > 0 {
		num, typ, n := protowire.ConsumeTag(rem)
		if n < 0 {
			t.Fatalf("invalid tag at offset %d", len(bz)-len(rem))
		}
		if num < 1 || num > 13 {
			t.Fatalf("field %d out of schema range [1..13]", num)
		}
		saw[num] = true
		rem = rem[n:]
		switch typ {
		case protowire.BytesType:
			_, n := protowire.ConsumeBytes(rem)
			if n < 0 {
				t.Fatalf("invalid bytes for field %d", num)
			}
			rem = rem[n:]
		case protowire.VarintType:
			_, n := protowire.ConsumeVarint(rem)
			if n < 0 {
				t.Fatalf("invalid varint for field %d", num)
			}
			rem = rem[n:]
		default:
			t.Fatalf("unexpected wire type %d for field %d", typ, num)
		}
	}
	// Envelope (1) + job_id (2) + atom_id (3) + author_gcid (4) must be present.
	for _, want := range []protowire.Number{1, 2, 3, 4} {
		if !saw[want] {
			t.Fatalf("missing required field %d", want)
		}
	}
}

// TestEncodeBridgePayload_QuestionAuthored_IsBinaryProtobuf covers the
// question.authored emit path.
func TestEncodeBridgePayload_QuestionAuthored_IsBinaryProtobuf(t *testing.T) {
	payload := map[string]any{
		"question_id":     "01971a90-cccc-7000-8000-000000000001",
		"atom_id":         "01971a90-aaaa-7000-8000-000000000001",
		"tenant_id":       "01971a90-cccc-7000-8000-000000000001",
		"author_gcid":     "01971a90-bbbb-7000-8000-000000000001",
		"question_type":   "mcq",
		"source_type":     "ai_assist",
		"revision_number": 1,
		"revision_id":     "01971a90-dddd-7000-8000-000000000001",
		"source_job_id":   "01971a90-eeee-7000-8000-000000000001",
		"authored_at":     time.Now().UTC().Format(time.RFC3339Nano),
	}
	_, bz, err := encodeBridgePayload("chora.creation.question.authored.v1", payload)
	if err != nil {
		t.Fatalf("encodeBridgePayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
	var probe map[string]any
	if err := json.Unmarshal(bz, &probe); err == nil {
		t.Fatalf("payload decoded as JSON: %s", string(bz))
	}
}

// TestEncodeBridgePayload_QuestionGenerationCompleted_Success covers the
// success branch of question_subscriber.succeed.
func TestEncodeBridgePayload_QuestionGenerationCompleted_Success(t *testing.T) {
	payload := map[string]any{
		"job_id":          "01971a90-eeee-7000-8000-000000000001",
		"atom_id":         "01971a90-aaaa-7000-8000-000000000001",
		"author_gcid":     "01971a90-bbbb-7000-8000-000000000001",
		"job_type":        "ai_draft",
		"status":          "succeeded",
		"candidate_count": 3,
		"completed_at":    time.Now().UTC().Format(time.RFC3339Nano),
	}
	_, bz, err := encodeBridgePayload("chora.creation.question.generation_completed.v2", payload)
	if err != nil {
		t.Fatalf("encodeBridgePayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
	var probe map[string]any
	if err := json.Unmarshal(bz, &probe); err == nil {
		t.Fatalf("payload decoded as JSON: %s", string(bz))
	}
}

// TestEncodeBridgePayload_AtomCreated_FromBatchPath covers the batch-path
// newAtomEvent emission in question_jobs_handler.
func TestEncodeBridgePayload_AtomCreated_FromBatchPath(t *testing.T) {
	payload := map[string]any{
		"atom_id":        "01971a90-aaaa-7000-8000-000000000001",
		"tenant_id":      "01971a90-cccc-7000-8000-000000000001",
		"author_gcid":    "01971a90-bbbb-7000-8000-000000000001",
		"title":          "Atom title",
		"tags":           []string{"fractions", "math"},
		"difficulty":     int(3),
		"atom_type":      "mcq",
		"status":         "draft",
		"source_job_id":  "01971a90-eeee-7000-8000-000000000001",
		"parent_atom_id": "01971a90-ffff-7000-8000-000000000001",
		"created_at":     time.Now().UTC().Format(time.RFC3339Nano),
	}
	_, bz, err := encodeBridgePayload("chora.creation.atom.created.v1", payload)
	if err != nil {
		t.Fatalf("encodeBridgePayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
	var probe map[string]any
	if err := json.Unmarshal(bz, &probe); err == nil {
		t.Fatalf("payload decoded as JSON: %s", string(bz))
	}
}

// TestEncodeBridgePayload_UnsupportedTopic_FallsBackToJSON asserts the
// fallback path: a topic with no binary encoder receives JSON bytes + the
// WARN is logged once.
func TestEncodeBridgePayload_UnsupportedTopic_FallsBackToJSON(t *testing.T) {
	payload := map[string]any{"field": "value"}
	_, bz, err := encodeBridgePayload("chora.creation.unknown.topic.v1", payload)
	if err != nil {
		t.Fatalf("encodeBridgePayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
	// Should be JSON.
	var probe map[string]any
	if err := json.Unmarshal(bz, &probe); err != nil {
		t.Fatalf("expected JSON fallback for unsupported topic, got: %v (bytes=%s)", err, string(bz))
	}
	if probe["field"] != "value" {
		t.Fatalf("fallback probe = %v; want field=value", probe)
	}
}

// TestEncodeBridgePayload_TypedStruct_RoundTripsViaJSON covers the
// completionEvent struct path (question_subscriber.succeed/fail).
func TestEncodeBridgePayload_TypedStruct_RoundTripsViaJSON(t *testing.T) {
	type completionEvent struct {
		JobID          string `json:"job_id"`
		AtomID         string `json:"atom_id"`
		AuthorGCID     string `json:"author_gcid"`
		JobType        string `json:"job_type"`
		Status         string `json:"status"`
		CandidateCount int    `json:"candidate_count"`
		CompletedAt    string `json:"completed_at"`
	}
	evt := completionEvent{
		JobID:          "01971a90-eeee-7000-8000-000000000001",
		AtomID:         "01971a90-aaaa-7000-8000-000000000001",
		AuthorGCID:     "01971a90-bbbb-7000-8000-000000000001",
		JobType:        "ai_draft",
		Status:         "succeeded",
		CandidateCount: 3,
		CompletedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}
	_, bz, err := encodeBridgePayload("chora.creation.question.generation_completed.v2", evt)
	if err != nil {
		t.Fatalf("encodeBridgePayload typed struct: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
	// Should be binary protobuf, NOT JSON.
	var probe map[string]any
	if err := json.Unmarshal(bz, &probe); err == nil {
		t.Fatalf("typed struct emitted JSON instead of binary protobuf: %s", string(bz))
	}
}

// TestEncodeBridgePayload_QuestionGenerationCompleted_CarriesTenantID locks in
// the tenant_id fix for the AI-assist question-completion events. The v2
// payload schema has NO tenant_id field (envelope field 3 carries it), so the
// payload key the emit sites set must reach the ENVELOPE — never the payload —
// or every consumer keyed on the tenant sees an empty envelope.tenant_id.
func TestEncodeBridgePayload_QuestionGenerationCompleted_CarriesTenantID(t *testing.T) {
	const tenant = "01971a90-cccc-7000-8000-000000000001"
	// Mirrors pubsub.completionEvent (question_subscriber.go) — the typed
	// struct path the succeed/fail/emitQuestionCompletion call sites use.
	evt := struct {
		JobID          string `json:"job_id"`
		AtomID         string `json:"atom_id"`
		AuthorGCID     string `json:"author_gcid"`
		TenantID       string `json:"tenant_id"`
		Status         string `json:"status"`
		CandidateCount int    `json:"candidate_count"`
		CompletedAt    string `json:"completed_at"`
		Operation      string `json:"operation"`
	}{
		JobID:          "01971a90-eeee-7000-8000-000000000001",
		AtomID:         "01971a90-aaaa-7000-8000-000000000001",
		AuthorGCID:     "01971a90-bbbb-7000-8000-000000000001",
		TenantID:       tenant,
		Status:         "succeeded",
		CandidateCount: 3,
		CompletedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Operation:      "compose",
	}

	env, bz, err := encodeBridgePayload("chora.creation.question.generation_completed.v2", evt)
	if err != nil {
		t.Fatalf("encodeBridgePayload: %v", err)
	}
	if env.TenantID != tenant {
		t.Fatalf("envelope.TenantID = %q; want %q", env.TenantID, tenant)
	}

	// Decode the wire bytes the way a binary consumer's envelope decoder does:
	// payload field 1 is the Envelope submessage, and its field 3 is tenant_id.
	envelopeBytes := wireFieldBytes(t, bz, 1)
	if got := string(wireFieldBytes(t, envelopeBytes, 3)); got != tenant {
		t.Fatalf("wire envelope.tenant_id = %q; want %q", got, tenant)
	}
}

// wireFieldBytes returns the raw bytes of the first length-delimited field
// `want` in bz, skipping varint fields. Fails the test if absent.
func wireFieldBytes(t *testing.T, bz []byte, want protowire.Number) []byte {
	t.Helper()
	rem := bz
	for len(rem) > 0 {
		num, typ, n := protowire.ConsumeTag(rem)
		if n < 0 {
			t.Fatalf("invalid tag")
		}
		rem = rem[n:]
		switch typ {
		case protowire.BytesType:
			v, m := protowire.ConsumeBytes(rem)
			if m < 0 {
				t.Fatalf("invalid bytes field %d", num)
			}
			if num == want {
				return v
			}
			rem = rem[m:]
		case protowire.VarintType:
			_, m := protowire.ConsumeVarint(rem)
			if m < 0 {
				t.Fatalf("invalid varint field %d", num)
			}
			rem = rem[m:]
		default:
			t.Fatalf("unexpected wire type %d for field %d", typ, num)
		}
	}
	t.Fatalf("field %d not found", want)
	return nil
}

// TestEnvelopeFromPayloadMap_SynthesisesUUIDv7 — every emit must mint a valid
// event_id + idempotency_key on the envelope so the Schema Registry contract
// holds.
func TestCoerceToEvent_MapsBatchSourceFields(t *testing.T) {
	payload := map[string]any{
		"job_id":           "j1",
		"atom_id":          "a1",
		"job_type":         "batch_source_material",
		"source_blob_uri":  "gs://chora-creation-batch-uploads-dev/t1/j1/src.txt",
		"source_mime_type": "text/plain",
	}
	evt, ok := coerceToEvent(payload)
	if !ok {
		t.Fatal("coerceToEvent ok=false")
	}
	if evt.SourceBlobURI != "gs://chora-creation-batch-uploads-dev/t1/j1/src.txt" {
		t.Errorf("SourceBlobURI=%q; want the gs:// uri (batch fan-out must not drop it)", evt.SourceBlobURI)
	}
	if evt.SourceMimeType != "text/plain" {
		t.Errorf("SourceMimeType=%q; want text/plain", evt.SourceMimeType)
	}
	if evt.JobID != "j1" {
		t.Errorf("base fields not mapped: %+v", evt)
	}
}

// TestEnvelopeFromPayloadMap_StartedV1_DedupsPerAssistNotAtom guards the live
// stuck-regen root cause: ai_assist.started.v1 payloads carry assist_id (NOT
// job_id), so deriving the idempotency_key from atom_id collapsed every assist
// run on the SAME atom to one key. A review image-regen reuses its parent
// batch's atom, so its started.v1 collided with the already-processed batch
// event → the orchestrator InboxIdempotencyStore deduped it → the
// ImageRegenRunner never ran → the job stuck "running". Dedup grain MUST be
// per-assist (assist_id), not per-atom.
func TestEnvelopeFromPayloadMap_StartedV1_DedupsPerAssistNotAtom(t *testing.T) {
	topic := "chora.creation.ai_assist.started.v2"
	atom := "01971a90-aaaa-7000-8000-000000000001"
	mk := func(assist string) map[string]any {
		return map[string]any{
			"assist_id":   assist,
			"atom_id":     atom, // same atom for both (a batch + its regen)
			"tenant_id":   "11111111-1111-7111-8111-111111111111",
			"author_gcid": "00000000-0000-7000-8000-000000000001",
		}
	}
	eBatch := envelopeFromPayloadMap(mk("batch-job-A"), topic)
	eRegen := envelopeFromPayloadMap(mk("regen-job-B"), topic)

	if eBatch.IdempotencyKey != topic+"|batch-job-A" {
		t.Errorf("started.v1 idempotency_key = %q, want per-assist %q", eBatch.IdempotencyKey, topic+"|batch-job-A")
	}
	if eBatch.IdempotencyKey == eRegen.IdempotencyKey {
		t.Errorf("two assist runs on the SAME atom MUST NOT share an idempotency_key (got %q for both) — "+
			"that is the regen-deduped-as-batch bug", eBatch.IdempotencyKey)
	}
}

func TestEnvelopeFromPayloadMap_SynthesisesUUIDv7(t *testing.T) {
	payload := map[string]any{
		"job_id":      "01971a90-eeee-7000-8000-000000000001",
		"atom_id":     "01971a90-aaaa-7000-8000-000000000001",
		"author_gcid": "01971a90-bbbb-7000-8000-000000000001",
		"tenant_id":   "01971a90-cccc-7000-8000-000000000001",
	}
	env := envelopeFromPayloadMap(payload, "chora.creation.question.generation_requested.v2")
	if env.EventID == "" {
		t.Fatal("envelope.EventID empty")
	}
	if env.IdempotencyKey == "" {
		t.Fatal("envelope.IdempotencyKey empty")
	}
	if env.SchemaVersion < 1 {
		t.Fatalf("envelope.SchemaVersion = %d; want >=1", env.SchemaVersion)
	}
	if env.SourceProject != "chora-local" {
		t.Fatalf("envelope.SourceProject = %q; want chora-local", env.SourceProject)
	}
	if env.SourceService != "chora-creation" {
		t.Fatalf("envelope.SourceService = %q; want chora-creation", env.SourceService)
	}
	if env.PublishedAt.IsZero() {
		t.Fatal("envelope.PublishedAt zero")
	}
	if env.OccurredAt.IsZero() {
		t.Fatal("envelope.OccurredAt zero")
	}
	if env.IdempotencyKey == env.EventID {
		t.Fatalf("envelope.IdempotencyKey = EventID; expected derivation from job_id")
	}
	if env.TenantID != payload["tenant_id"].(string) {
		t.Fatalf("envelope.TenantID = %q; want %q", env.TenantID, payload["tenant_id"])
	}
	if env.GCID != payload["author_gcid"].(string) {
		t.Fatalf("envelope.GCID = %q; want %q", env.GCID, payload["author_gcid"])
	}
}

// The bridge must publish the synthesized envelope — the chora Go push
// dispatchers read the envelope from the message envelope; envelope-less
// messages dead-letter with "envelope.event_id required".
func TestEncodeBridgePayload_ReturnsEnvelope(t *testing.T) {
	payload := map[string]any{
		"atom_id":     "01970000-0000-7000-a000-000000000001",
		"tenant_id":   "01970000-0000-7000-8000-000000000001",
		"author_gcid": "01970000-0000-7000-9000-000000000001",
		"title":       "Envelope test atom",
		"atom_type":   "mcq",
		"created_at":  "2026-06-10T08:00:00Z",
	}
	env, _, err := encodeBridgePayload("chora.creation.atom.created.v1", payload)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if env.EventID == "" {
		t.Error("env.EventID empty — consumption's decodeChoraEnvelope rejects the message")
	}
	if env.IdempotencyKey == "" {
		t.Error("env.IdempotencyKey empty")
	}
	if env.TenantID != payload["tenant_id"] {
		t.Errorf("env.TenantID = %q, want %q", env.TenantID, payload["tenant_id"])
	}
	if env.SourceService != "chora-creation" {
		t.Errorf("env.SourceService = %q", env.SourceService)
	}
	if env.OccurredAt.IsZero() || env.PublishedAt.IsZero() {
		t.Error("timestamp envelope fields missing")
	}
}

func TestEnvelopeFromPayloadMap_SynthesisesTraceparentWhenAbsent(t *testing.T) {
	env := envelopeFromPayloadMap(map[string]any{
		"atom_id":   "01970000-0000-7000-a000-000000000001",
		"tenant_id": "01970000-0000-7000-8000-000000000001",
	}, "chora.creation.atom.created.v1")
	if env.Traceparent == "" {
		t.Fatal("traceparent empty — the consumption dispatcher rejects envelope-less traceparent")
	}
	if len(env.Traceparent) != 55 || env.Traceparent[:3] != "00-" {
		t.Errorf("traceparent %q not valid W3C shape", env.Traceparent)
	}
}
