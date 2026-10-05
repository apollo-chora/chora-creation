// fabric_guardrail_test — the Flag-1 CI guardrail from the 2026-07-01
// event-fabric audit, adapted for the cloud-neutral standalone repo.
//
// Original: a topic that chora-creation PRODUCES and that is bound to a
// BINARY Pub/Sub Schema Registry schema, but for which protomarshal has NO
// encoder case — so encodeOutboxPayload silently JSON-falls-back and the
// binary schema rejects every publish → dead-letter forever.
//
// Cloud-neutral adaptation: the NATS eventbus has no Schema Registry, so
// the binary-bound filter no longer applies at runtime. The guardrail's
// intent is preserved by embedding the original TF topic lists: only
// topics that were in the broker's pubsub_topics AND not in
// schemaless_topics are checked. This matches the original test's
// semantics without the GCP Terraform dependency.
package protomarshal_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
)

// creationTopicRe matches a fully-qualified creation topic that is a STANDALONE
// double-quoted string literal (Go string constant). Requiring the surrounding
// quotes distinguishes a real producer literal from a topic merely mentioned
// in a // comment or embedded in a larger log-format string. `.v[0-9]`
// because creation has both .v1 and .v2 topics.
var creationTopicRe = regexp.MustCompile(`"(chora\.creation\.[a-z0-9_.]+\.v[0-9])"`)

func creationTopicLiterals(s string) map[string]bool {
	out := map[string]bool{}
	for _, m := range creationTopicRe.FindAllStringSubmatch(s, -1) {
		out[m[1]] = true
	}
	return out
}

// brokerTopics is the cloud-neutral equivalent of the original TF
// pubsub_topics list — the topics actually wired in the broker.
var brokerTopics = map[string]bool{
	"chora.creation.atom.created.v1":                       true,
	"chora.creation.atom.updated.v1":                       true,
	"chora.creation.atom.published.v1":                     true,
	"chora.creation.atom.archived.v1":                      true,
	"chora.creation.atom.reuse_visibility_changed.v1":      true,
	"chora.creation.atom.orphan_created.v1":                true,
	"chora.creation.revision.published.v1":                 true,
	"chora.creation.media.uploaded.v1":                     true,
	"chora.creation.media.processed.v1":                    true,
	"chora.creation.media.processing_failed.v1":            true,
	"chora.creation.question.generated.v1":                 true,
	"chora.creation.question.evaluated.v1":                 true,
	"chora.creation.ai_assist.completed.v1":                true,
	"chora.creation.ai_assist.refused.v1":                  true,
	"chora.creation.ai_assist.chunk_completed.v1":          true,
	"chora.creation.question_batch.accepted.v1":            true,
	"chora.creation.collection.created.v1":                 true,
	"chora.creation.collection.updated.v1":                 true,
	"chora.creation.collection.atom_added.v1":              true,
	"chora.creation.collection.atom_removed.v1":            true,
	"chora.creation.collection.deleted.v1":                 true,
	"chora.creation.collection.converted_to_study_list.v1": true,
	"chora.creation.question.authored.v1":                  true,
	"chora.creation.question.generation_requested.v2":      true,
	"chora.creation.question.generation_completed.v2":      true,
	"chora.creation.ai_assist.started.v2":                  true,
}

// schemalessTopics is the cloud-neutral equivalent of the original TF
// schemaless_topics list — topics that legitimately publish JSON.
var schemalessTopics = map[string]bool{
	"chora.creation.collection.created.v1":         true,
	"chora.creation.collection.updated.v1":         true,
	"chora.creation.collection.atom_added.v1":      true,
	"chora.creation.collection.atom_removed.v1":    true,
	"chora.creation.collection.deleted.v1":         true,
	"chora.creation.account.pseudonymised.v1":      true,
	"chora.creation.pii.pseudonymise.failed.v1":    true,
	"chora.creation.pii.pseudonymise.requested.v1": true,
}

// creationRepoRoot walks up from this test's source file (stable at build
// time, independent of the working directory) to the standalone repo root —
// the directory holding internal/ and migrations/.
func creationRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed — cannot locate repo root")
	}
	dir := filepath.Dir(file)
	for i := 0; i < 15; i++ {
		if isDir(filepath.Join(dir, "internal")) && isDir(filepath.Join(dir, "migrations")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("repo root (internal/ + migrations/) not found walking up from %s", file)
	return ""
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// producedCreationTopics scans chora-creation's Go source for creation topic
// string literals, EXCLUDING the encoder+decoder (the protomarshal package),
// the *_subscriber.go consume surfaces, and test files. What remains is the
// set of topics the service actually emits.
func producedCreationTopics(t *testing.T, root string) map[string]bool {
	t.Helper()
	svcRoot := filepath.Join(root, "internal")
	produced := map[string]bool{}
	err := filepath.WalkDir(svcRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if !strings.HasSuffix(base, ".go") || strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, "_subscriber.go") {
			return nil
		}
		if strings.Contains(path, string(filepath.Separator)+"protomarshal"+string(filepath.Separator)) {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for topic := range creationTopicLiterals(string(raw)) {
			produced[topic] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk chora-creation source: %v", err)
	}
	if len(produced) == 0 {
		t.Fatal("0 produced creation topics found — source scan is broken (would make the guardrail vacuously green)")
	}
	return produced
}

// TestFabricGuardrail_EveryProducedBinaryTopicHasEncoder is the Flag-1 gate,
// adapted: for every creation topic that chora-creation PRODUCES and that is
// bound to a binary schema (broker topic, not schemaless), MarshalPayload
// MUST route to a real encoder (not ErrUnsupportedTopic → JSON fallback).
func TestFabricGuardrail_EveryProducedBinaryTopicHasEncoder(t *testing.T) {
	root := creationRepoRoot(t)
	produced := producedCreationTopics(t, root)
	env := fixedEnvelope()
	// A minimal payload with the common id keys the encoders reach for; absent
	// fields are proto3-default-omitted, so this is enough to prove routing.
	minimal := map[string]any{
		"atom_id":     "a",
		"question_id": "q",
		"job_id":      "j",
		"batch_id":    "b",
		"title":       "t",
		"author_gcid": "g",
	}

	checked := 0
	for topic := range brokerTopics {
		if schemalessTopics[topic] {
			continue
		}
		if !produced[topic] {
			t.Logf("declared-only (binary schema, no chora-creation producer yet): %s", topic)
			continue
		}
		_, err := protomarshal.MarshalPayload(topic, env, minimal)
		if protomarshal.IsUnsupportedTopic(err) {
			t.Errorf("topic %q is PRODUCED by chora-creation and bound to a binary "+
				"schema, but protomarshal has NO encoder case → JSON fallback. "+
				"Add a MarshalPayload case.", topic)
			continue
		}
		if err != nil {
			t.Errorf("topic %q: MarshalPayload returned a non-routing error: %v", topic, err)
			continue
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("guardrail asserted 0 produced binary-bound topics — the source scan is broken")
	}
	t.Logf("Flag-1 guardrail: %d produced binary-bound creation topics all have encoders", checked)
}
