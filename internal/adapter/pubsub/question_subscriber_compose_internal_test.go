// question_subscriber_compose_internal_test.go — ADR-195 white-box tests for the
// compose dispatch helpers. The 5-way job_type switch was collapsed into ONE
// runCompose that branches on the compose intent; the legacy job_type→intent shim
// was retired in WS9 step 3, so source-material is now keyed solely on the event's
// source_blob_uri.
package pubsub

import "testing"

func TestHasSourceMaterial(t *testing.T) {
	if !hasSourceMaterial(QuestionGenerationRequestedEvent{SourceBlobURI: "gs://b/x.pdf"}) {
		t.Error("source_blob_uri present must classify as source material (RAG)")
	}
	// No source_blob_uri ⇒ pure-prompt. The legacy batch job_type classifier was
	// retired in ADR-195 WS9 step 3; source-material is keyed solely on the URI.
	if hasSourceMaterial(QuestionGenerationRequestedEvent{}) {
		t.Error("an event without source_blob_uri must NOT be source material")
	}
}
