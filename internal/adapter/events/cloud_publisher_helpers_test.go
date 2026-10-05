// cloud_publisher_helpers_test.go — direct unit tests (package events) for the
// pure, unexported helpers in cloud_publisher.go. These are the string/
// parsing/encoding utilities that Publish composes; testing them directly
// avoids needing a fake outbox on every branch.
package events

import (
	"testing"
	"time"

	cgcenvelope "github.com/apollo-chora/chora-common/envelope"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

func TestValidateTopic(t *testing.T) {
	valid := []string{
		"chora.creation.atom.published.v1",
		"chora.governance.atom.archived.v3",
	}
	for _, topic := range valid {
		if err := validateTopic(topic); err != nil {
			t.Errorf("validateTopic(%q) = %v; want nil", topic, err)
		}
	}

	invalid := []string{
		"",
		"  ",
		"short",
		"chora.creation.atom",             // < 5 parts
		"atoms.creation.atom.publish.v1",  // not chora.*
		"chora.atoms.atom.publish.v1",     // domain not creation/governance
		"chora.creation.atom.publish",     // no version suffix
		"chora.creation.atom.publish.v",   // trailing v with no number
		"chora.creation.atom.publish.vx1", // non-numeric suffix
	}
	for _, topic := range invalid {
		if err := validateTopic(topic); err == nil {
			t.Errorf("validateTopic(%q) = nil; want error", topic)
		}
	}
}

func TestDeriveEventType(t *testing.T) {
	cases := []struct{ in, want string }{
		{"chora.creation.atom.published.v1", "atom.published.v1"},
		{"chora.creation.question.authored.v1", "question.authored.v1"},
		{"short", "short"},
	}
	for _, c := range cases {
		if got := deriveEventType(c.in); got != c.want {
			t.Errorf("deriveEventType(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseRFC3339(t *testing.T) {
	// Empty → now (not zero, not error).
	got, err := parseRFC3339("")
	if err != nil {
		t.Fatalf("parseRFC3339(empty) err = %v", err)
	}
	if got.IsZero() {
		t.Error("parseRFC3339(empty) returned zero time")
	}

	// RFC3339Nano accepted.
	if _, err := parseRFC3339("2026-08-08T00:00:00.123456789Z"); err != nil {
		t.Errorf("parseRFC3339(nano) err = %v", err)
	}
	// RFC3339 (no fraction) accepted.
	tm, err := parseRFC3339("2026-08-08T12:30:00Z")
	if err != nil {
		t.Fatalf("parseRFC3339(seconds) err = %v", err)
	}
	if tm.Hour() != 12 || tm.Minute() != 30 {
		t.Errorf("parseRFC3339 parsed to %v, want 12:30 UTC", tm)
	}
	// Garbage rejected.
	if _, err := parseRFC3339("not-a-time"); err == nil {
		t.Error("parseRFC3339(garbage) = nil; want error")
	}
}

func TestEnsureUUIDv7(t *testing.T) {
	s := ensureUUIDv7("")
	if s == "" {
		t.Fatal("ensureUUIDv7(empty) returned empty")
	}
	// A populated value passes through unchanged.
	orig := "still-here"
	if got := ensureUUIDv7(orig); got != orig {
		t.Errorf("ensureUUIDv7(populated) = %q, want %q", got, orig)
	}
	valid := ensureUUIDv7("")
	if len(valid) != 36 {
		t.Errorf("ensureUUIDv7 generated %q (len %d), want 36-char uuid", valid, len(valid))
	}
}

func TestDefaultIfEmpty(t *testing.T) {
	if got := defaultIfEmpty("", "fallback"); got != "fallback" {
		t.Errorf("defaultIfEmpty(empty) = %q", got)
	}
	if got := defaultIfEmpty("present", "fallback"); got != "present" {
		t.Errorf("defaultIfEmpty(present) = %q", got)
	}
}

func TestDefaultSchemaIfZero(t *testing.T) {
	if got := defaultSchemaIfZero(0); got != 1 {
		t.Errorf("defaultSchemaIfZero(0) = %d, want 1", got)
	}
	if got := defaultSchemaIfZero(7); got != 7 {
		t.Errorf("defaultSchemaIfZero(7) = %d, want 7", got)
	}
}

func TestNewRowID(t *testing.T) {
	for i := 0; i < 5; i++ {
		if got := newRowID(); got == "" {
			t.Fatal("newRowID returned empty")
		}
	}
}

func TestEncodeCloudPublisherPayload_MapAndFallback(t *testing.T) {
	e := atom.Event{
		EventID:              "11111111-1111-7111-8111-111111111111",
		IdempotencyKey:       "22222222-2222-7222-8222-222222222222",
		TenantID:             "33333333-3333-7333-8333-333333333333",
		Gcid:                 "gcid-1",
		AtomID:               "44444444-4444-7444-8444-444444444444",
		Title:                "T",
		Type:                 atom.EventTypeAtomPublished,
		Difficulty:           3,
		RevisionID:           "rev-1",
		RevisionNumber:       2,
		CourseID:             "course-1",
		Stem:                 "stem",
		CognitiveLevel:       "application",
		ReuseVisibility:      "tenant",
		AuthorDisplayName:    "Ada",
		CorrectOptionID:      "opt-5",
		AnswerCount:          4,
		HasOpenEndedQuestion: true,
	}
	env := cgcenvelope.Envelope{
		EventID:        e.EventID,
		IdempotencyKey: e.IdempotencyKey,
		TenantID:       e.TenantID,
		GCID:           e.Gcid,
		OccurredAt:     time.Now().UTC(),
		PublishedAt:    time.Now().UTC(),
		SourceProject:  "p",
		SourceService:  "s",
		SchemaVersion:  1,
	}
	created := time.Now().UTC()

	// A known binary-encoded topic.
	bz, err := encodeCloudPublisherPayload("chora.creation.atom.published.v1", e, env, created)
	if err != nil {
		t.Fatalf("encodeCloudPublisherPayload(published) err = %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("encodeCloudPublisherPayload(published) returned empty bytes")
	}

	// An unsupported topic → JSON fallback, not error.
	bz2, err := encodeCloudPublisherPayload("chora.creation.definitely.not.a.real.topic.v1", e, env, created)
	if err != nil {
		t.Fatalf("encodeCloudPublisherPayload(unsupported) err = %v", err)
	}
	if len(bz2) == 0 {
		t.Fatal("encodeCloudPublisherPayload(unsupported) returned empty bytes")
	}
}
