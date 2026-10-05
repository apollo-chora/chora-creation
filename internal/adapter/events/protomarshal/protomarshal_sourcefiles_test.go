// protomarshal_sourcefiles_test.go — RED→GREEN coverage for the Lane 1c
// (CHO-1703 / ADR-180) `source_files` repeated-message field 20 on
// chora.creation.ai_assist.started.v2.
//
// Contract: chora-contracts/proto/events/creation/ai_assist.proto
// (AiAssistStarted.source_files = 20, message SourceFileRef
// {1: blob_uri, 2: mime_type, 3: role}); flat Schema Registry copy at
// chora-contracts/proto/events-flat/creation/ai_assist/started.proto
// (rev e8bf1dfd — f20 already valid on the wire).
//
// Per the 1a lesson (commit 79d9cbbf): encoder / decoder / Schema-Registry
// must ALL move together field-by-field; the round-trip below decodes the
// hand-rolled wire bytes through the GENERATED binding so a field-number or
// wire-type drift fails loud here instead of dead-lettering in production.
package protomarshal_test

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"

	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
)

// sourceFilesStartedPayload returns a batch started payload carrying TWO
// role="source" files + ONE role="rubric" file alongside the f17/18 mirror of
// source_files[0] (back-compat rule from the proto doc comment).
func sourceFilesStartedPayload(env protomarshal.Envelope, sourceFiles any) map[string]any {
	return map[string]any{
		"assist_id":        "01971a90-cccc-7000-8000-000000000020",
		"tenant_id":        env.TenantID,
		"author_gcid":      env.GCID,
		"content_type":     "mcq",
		"prompt":           "Generate questions from the uploaded sources.",
		"started_at":       env.OccurredAt.Format("2006-01-02T15:04:05Z07:00"),
		"job_kind":         "batch",
		"requested_count":  3,
		"grounding_mode":   "strict",
		"source_blob_uri":  "gs://bucket/tenants/t/jobs/j/source-1",
		"source_mime_type": "application/pdf",
		"source_files":     sourceFiles,
	}
}

// TestMarshalAiAssistStarted_EncodesSourceFilesField20 — the canonical
// multi-file shape: []map[string]any (the handler emits this). Asserts
// field 20 appears once per file and round-trips through the generated
// binding with blob_uri / mime_type / role intact + ordered.
func TestMarshalAiAssistStarted_EncodesSourceFilesField20(t *testing.T) {
	env := fixedEnvelope()
	files := []map[string]any{
		{"blob_uri": "gs://bucket/tenants/t/jobs/j/source-1", "mime_type": "application/pdf", "role": "source"},
		{"blob_uri": "gs://bucket/tenants/t/jobs/j/source-2", "mime_type": "image/png", "role": "source"},
		{"blob_uri": "gs://bucket/tenants/t/jobs/j/rubric", "mime_type": "text/markdown", "role": "rubric"},
	}
	payload := sourceFilesStartedPayload(env, files)

	bz, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	seen := walkTopLevelTags(t, bz, 1, 20)
	if !seen[20] {
		t.Fatalf("ai_assist.started: missing source_files field 20 (seen: %v)", seen)
	}

	// Load-bearing round-trip through the generated binding.
	var m creationv1.AiAssistStartedV2
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	got := m.GetSourceFiles()
	if len(got) != 3 {
		t.Fatalf("AiAssistStarted.source_files len = %d; want 3", len(got))
	}
	wantFiles := []struct{ uri, mime, role string }{
		{"gs://bucket/tenants/t/jobs/j/source-1", "application/pdf", "source"},
		{"gs://bucket/tenants/t/jobs/j/source-2", "image/png", "source"},
		{"gs://bucket/tenants/t/jobs/j/rubric", "text/markdown", "rubric"},
	}
	for i, w := range wantFiles {
		if got[i].GetBlobUri() != w.uri {
			t.Errorf("source_files[%d].blob_uri = %q; want %q", i, got[i].GetBlobUri(), w.uri)
		}
		if got[i].GetMimeType() != w.mime {
			t.Errorf("source_files[%d].mime_type = %q; want %q", i, got[i].GetMimeType(), w.mime)
		}
		if got[i].GetRole() != w.role {
			t.Errorf("source_files[%d].role = %q; want %q", i, got[i].GetRole(), w.role)
		}
	}
	// f17/18 mirror retained alongside f20 (back-compat with pre-1c
	// orchestrator decoders during the rollout window).
	if m.GetSourceBlobUri() != "gs://bucket/tenants/t/jobs/j/source-1" {
		t.Errorf("source_blob_uri (f17) = %q; want mirror of source_files[0]", m.GetSourceBlobUri())
	}
	if m.GetSourceMimeType() != "application/pdf" {
		t.Errorf("source_mime_type (f18) = %q; want mirror of source_files[0]", m.GetSourceMimeType())
	}
}

// TestMarshalAiAssistStarted_SourceFiles_JSONDecodedShape — the bridge's
// typed-struct round-trip (encodeBridgePayload json.Marshal → Unmarshal)
// arrives as []any of map[string]any. Must encode identically.
func TestMarshalAiAssistStarted_SourceFiles_JSONDecodedShape(t *testing.T) {
	env := fixedEnvelope()
	files := []any{
		map[string]any{"blob_uri": "gs://b/k1", "mime_type": "text/plain", "role": "source"},
		map[string]any{"blob_uri": "gs://b/k2", "mime_type": "image/webp", "role": "rubric"},
	}
	payload := sourceFilesStartedPayload(env, files)

	bz, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m creationv1.AiAssistStartedV2
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	got := m.GetSourceFiles()
	if len(got) != 2 {
		t.Fatalf("source_files len = %d; want 2", len(got))
	}
	if got[0].GetBlobUri() != "gs://b/k1" || got[0].GetRole() != "source" {
		t.Errorf("source_files[0] = %v; want gs://b/k1 role=source", got[0])
	}
	if got[1].GetBlobUri() != "gs://b/k2" || got[1].GetMimeType() != "image/webp" || got[1].GetRole() != "rubric" {
		t.Errorf("source_files[1] = %v; want gs://b/k2 image/webp rubric", got[1])
	}
}

// TestMarshalAiAssistStarted_SourceFilesAbsent_ByteIdentical — proto3
// repeated default-elision: absent / empty source_files emits nothing so the
// legacy single-file producer bytes stay BYTE-IDENTICAL to pre-1c.
func TestMarshalAiAssistStarted_SourceFilesAbsent_ByteIdentical(t *testing.T) {
	env := fixedEnvelope()

	without := sourceFilesStartedPayload(env, nil)
	delete(without, "source_files")
	bzWithout, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, without)
	if err != nil {
		t.Fatalf("MarshalPayload (absent): %v", err)
	}

	empty := sourceFilesStartedPayload(env, []map[string]any{})
	bzEmpty, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, empty)
	if err != nil {
		t.Fatalf("MarshalPayload (empty): %v", err)
	}
	if !bytes.Equal(bzWithout, bzEmpty) {
		t.Error("empty source_files must be byte-identical to omitting the key (proto3 repeated default-elision)")
	}

	seen := walkTopLevelTags(t, bzWithout, 1, 20)
	if seen[20] {
		t.Error("absent source_files must not emit field 20")
	}
}

// TestMarshalAiAssistStarted_SourceFiles_RejectsBadShape — fail-loud when an
// element is not a map (a corrupted slot would silently mis-encode the wire).
func TestMarshalAiAssistStarted_SourceFiles_RejectsBadShape(t *testing.T) {
	env := fixedEnvelope()
	payload := sourceFilesStartedPayload(env, []any{"gs://not-a-map"})

	if _, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload); err == nil {
		t.Fatal("expected error for non-map source_files element; got nil")
	}

	payload2 := sourceFilesStartedPayload(env, "gs://not-a-slice")
	if _, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload2); err == nil {
		t.Fatal("expected error for non-slice source_files value; got nil")
	}
}

// walkTopLevelTags20 guard: keep the existing helper signature usable for
// field 20 (the helper takes a max-field bound — this test extends it).
var _ = protowire.Number(20)
