// protomarshal_lookups_test.go — branch coverage for the enum lookup tables
// (lookupCognitiveLevel, lookupQuestionGenerationJobStatus), the loose-typed
// coercion helpers (asTime via timestampPayloadField), the map<string,string>
// encoder (appendStringMapField), and the quota bool flags (quotaBool).
//
// All exercised through the public MarshalPayload surface — the lookups are
// package-private by design, so the tests drive them with the payload shapes
// the real producers emit (typed structs AND JSON-decoded values).
package protomarshal_test

import (
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
)

// -----------------------------------------------------------------------------
// lookupCognitiveLevel — via chora.creation.atom.published.v1 field 22.
// -----------------------------------------------------------------------------

func TestMarshalAtomPublished_CognitiveLevelLabels(t *testing.T) {
	env := fixedEnvelope()
	labels := map[string]bool{
		"COGNITIVE_LEVEL_UNSPECIFIED":   true,
		"unspecified":                   true,
		"COGNITIVE_LEVEL_KNOWLEDGE":     true,
		"knowledge":                     true,
		"COGNITIVE_LEVEL_COMPREHENSION": true,
		"comprehension":                 true,
		"COGNITIVE_LEVEL_APPLICATION":   true,
		"application":                   true,
		"COGNITIVE_LEVEL_ANALYSIS":      true,
		"analysis":                      true,
		"COGNITIVE_LEVEL_SYNTHESIS":     true,
		"synthesis":                     true,
		"COGNITIVE_LEVEL_EVALUATION":    true,
		"evaluation":                    true,
	}
	for label := range labels {
		t.Run(label, func(t *testing.T) {
			payload := map[string]any{
				"atom_id":         "01971a90-aaaa-7000-8000-0000000000aa",
				"cognitive_level": label,
			}
			if _, err := protomarshal.MarshalPayload("chora.creation.atom.published.v1", env, payload); err != nil {
				t.Fatalf("MarshalPayload(%q): %v", label, err)
			}
		})
	}
}

func TestMarshalAtomPublished_CognitiveLevelUnknownFailsLoud(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"atom_id":         "01971a90-aaaa-7000-8000-0000000000aa",
		"cognitive_level": "genius",
	}
	_, err := protomarshal.MarshalPayload("chora.creation.atom.published.v1", env, payload)
	if err == nil {
		t.Fatal("expected unknown cognitive_level to fail loud")
	}
	if !strings.Contains(err.Error(), "cognitive_level") {
		t.Fatalf("error should name the field; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// lookupQuestionGenerationJobStatus — via
// chora.creation.question.generation_completed.v2 field 6.
// -----------------------------------------------------------------------------

func TestMarshalQuestionGenerationCompletedV2_StatusLabels(t *testing.T) {
	env := fixedEnvelope()
	labels := []string{
		"QUESTION_GENERATION_JOB_STATUS_UNSPECIFIED", "unspecified",
		"QUESTION_GENERATION_JOB_STATUS_REQUESTED", "requested",
		"QUESTION_GENERATION_JOB_STATUS_RUNNING", "running",
		"QUESTION_GENERATION_JOB_STATUS_SUCCEEDED", "succeeded",
		"QUESTION_GENERATION_JOB_STATUS_FAILED", "failed",
		"QUESTION_GENERATION_JOB_STATUS_ACCEPTED", "accepted",
		"QUESTION_GENERATION_JOB_STATUS_PARTIALLY_ACCEPTED", "partially_accepted",
		"QUESTION_GENERATION_JOB_STATUS_CANCELLED", "cancelled",
	}
	for _, label := range labels {
		t.Run(label, func(t *testing.T) {
			payload := map[string]any{
				"job_id": "01971a90-dddd-7000-8000-000000000001",
				"status": label,
			}
			if _, err := protomarshal.MarshalPayload("chora.creation.question.generation_completed.v2", env, payload); err != nil {
				t.Fatalf("MarshalPayload(%q): %v", label, err)
			}
		})
	}
}

func TestMarshalQuestionGenerationCompletedV2_StatusUnknownFailsLoud(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"job_id": "01971a90-dddd-7000-8000-000000000001",
		"status": "exploded",
	}
	_, err := protomarshal.MarshalPayload("chora.creation.question.generation_completed.v2", env, payload)
	if err == nil {
		t.Fatal("expected unknown status to fail loud")
	}
	if !strings.Contains(err.Error(), "status") {
		t.Fatalf("error should name the field; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// asTime — via question.generation_completed.v2 field 11 (completed_at).
// The question_jobs_handler emissions arrive JSON-decoded, so the encoder
// must accept RFC3339 strings + *time.Time in addition to time.Time.
// -----------------------------------------------------------------------------

func TestMarshalQuestionGenerationCompletedV2_CompletedAtCoercions(t *testing.T) {
	env := fixedEnvelope()
	ts := time.Date(2026, 5, 18, 9, 30, 0, 123456789, time.UTC)

	t.Run("RFC3339Nano string", func(t *testing.T) {
		payload := map[string]any{"completed_at": ts.Format(time.RFC3339Nano)}
		if _, err := protomarshal.MarshalPayload("chora.creation.question.generation_completed.v2", env, payload); err != nil {
			t.Fatalf("MarshalPayload: %v", err)
		}
	})
	t.Run("RFC3339 string (no fractional)", func(t *testing.T) {
		payload := map[string]any{"completed_at": ts.Format(time.RFC3339)}
		if _, err := protomarshal.MarshalPayload("chora.creation.question.generation_completed.v2", env, payload); err != nil {
			t.Fatalf("MarshalPayload: %v", err)
		}
	})
	t.Run("*time.Time", func(t *testing.T) {
		payload := map[string]any{"completed_at": &ts}
		if _, err := protomarshal.MarshalPayload("chora.creation.question.generation_completed.v2", env, payload); err != nil {
			t.Fatalf("MarshalPayload: %v", err)
		}
	})
	t.Run("nil *time.Time fails loud", func(t *testing.T) {
		// A non-nil interface wrapping a nil *time.Time is NOT treated as
		// absent — asTime cannot coerce it and the encoder fails loud.
		var nilTs *time.Time
		payload := map[string]any{"completed_at": nilTs}
		_, err := protomarshal.MarshalPayload("chora.creation.question.generation_completed.v2", env, payload)
		if err == nil {
			t.Fatal("expected nil *time.Time to fail loud")
		}
	})
	t.Run("non-time value fails loud", func(t *testing.T) {
		payload := map[string]any{"completed_at": 42}
		_, err := protomarshal.MarshalPayload("chora.creation.question.generation_completed.v2", env, payload)
		if err == nil {
			t.Fatal("expected non-time completed_at to fail loud")
		}
		if !strings.Contains(err.Error(), "completed_at") {
			t.Fatalf("error should name the field; got %v", err)
		}
	})
}

// -----------------------------------------------------------------------------
// appendStringMapField — via ai_assist.started.v2 field 12 (metadata).
// Accepts map[string]string OR map[string]any-of-strings (JSON-decoded
// upstream shape); every other shape fails loud.
// -----------------------------------------------------------------------------

func TestMarshalAiAssistStartedV2_MetadataShapes(t *testing.T) {
	env := fixedEnvelope()
	base := func() map[string]any {
		return map[string]any{
			"assist_id": "01971a90-cccc-7000-8000-0000000000cc",
			"tenant_id": env.TenantID,
		}
	}

	t.Run("map[string]string", func(t *testing.T) {
		payload := base()
		payload["metadata"] = map[string]string{"b": "2", "a": "1"}
		bz, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload)
		if err != nil {
			t.Fatalf("MarshalPayload: %v", err)
		}
		seen := walkTopLevelTags(t, bz, 1, 12)
		if !seen[12] {
			t.Fatalf("expected metadata field 12 (seen: %v)", seen)
		}
	})
	t.Run("map[string]any of strings", func(t *testing.T) {
		payload := base()
		payload["metadata"] = map[string]any{"key": "value"}
		if _, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload); err != nil {
			t.Fatalf("MarshalPayload: %v", err)
		}
	})
	t.Run("empty map writes nothing", func(t *testing.T) {
		payload := base()
		payload["metadata"] = map[string]string{}
		bz, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload)
		if err != nil {
			t.Fatalf("MarshalPayload: %v", err)
		}
		seen := walkTopLevelTags(t, bz, 1, 12)
		if seen[12] {
			t.Fatalf("empty metadata must be proto3-elided (seen: %v)", seen)
		}
	})
	t.Run("non-string map value fails loud", func(t *testing.T) {
		payload := base()
		payload["metadata"] = map[string]any{"key": 7}
		_, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload)
		if err == nil {
			t.Fatal("expected non-string metadata value to fail loud")
		}
	})
	t.Run("non-map value fails loud", func(t *testing.T) {
		payload := base()
		payload["metadata"] = "not-a-map"
		_, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload)
		if err == nil {
			t.Fatal("expected non-map metadata to fail loud")
		}
	})
}

// -----------------------------------------------------------------------------
// quotaBool — via ai_assist.started.v2 field 21 (type_plan) sub-fields
// image_for_stem / image_for_answer. Absent/nil → false; non-bool fails
// loud (CHO-1825 2b).
// -----------------------------------------------------------------------------

func TestMarshalAiAssistStartedV2_TypePlanImageFlags(t *testing.T) {
	env := fixedEnvelope()

	t.Run("bool flags encoded", func(t *testing.T) {
		payload := typePlanStartedPayload(env, []map[string]any{
			{"question_type": "mcq", "count": 4, "max_images": 1, "image_for_stem": true, "image_for_answer": false},
		})
		if _, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload); err != nil {
			t.Fatalf("MarshalPayload: %v", err)
		}
	})
	t.Run("nil flag treated as false", func(t *testing.T) {
		payload := typePlanStartedPayload(env, []map[string]any{
			{"question_type": "mcq", "count": 4, "max_images": 1, "image_for_stem": nil},
		})
		if _, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload); err != nil {
			t.Fatalf("MarshalPayload: %v", err)
		}
	})
	t.Run("non-bool image_for_stem fails loud", func(t *testing.T) {
		payload := typePlanStartedPayload(env, []map[string]any{
			{"question_type": "mcq", "count": 4, "max_images": 1, "image_for_stem": "yes"},
		})
		_, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload)
		if err == nil {
			t.Fatal("expected non-bool image_for_stem to fail loud")
		}
	})
	t.Run("non-bool image_for_answer fails loud", func(t *testing.T) {
		payload := typePlanStartedPayload(env, []map[string]any{
			{"question_type": "mcq", "count": 4, "max_images": 1, "image_for_answer": 1},
		})
		_, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload)
		if err == nil {
			t.Fatal("expected non-bool image_for_answer to fail loud")
		}
	})
}
