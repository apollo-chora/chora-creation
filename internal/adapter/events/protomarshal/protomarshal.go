// Package protomarshal encodes chora-creation outbox event payloads to
// canonical binary protobuf wire format so GCP Pub/Sub Schema Registry
// validation (BINARY encoding) passes at publish time.
//
// Why hand-rolled
// ---------------
// chora-contracts/gen/go bindings exist but adding the runtime dependency
// here would couple chora-creation's outbox to BSR-rate-limited codegen.
// We use google.golang.org/protobuf/encoding/protowire to emit canonical
// wire bytes for the exact subset of fields each Schema Registry schema
// expects, with zero dependency on generated bindings.
//
// Field numbers + wire types are pinned to the deployed Schema Registry
// schemas + chora-contracts/proto/events-flat/creation/* — those flat
// protos ARE the canonical schemas attached to the live Pub/Sub topics.
//
// Invariants per the BINARY-encoded chora.creation.* schemas:
//
//   - Field 1 = Envelope (length-delimited nested message)
//   - Envelope nested fields 1..15 follow chora.common.v1.EventEnvelope layout
//   - Timestamps are nested messages: int64 seconds (field 1) + int32 nanos
//     (field 2)
//   - Unknown topics fail loud (ErrUnsupportedTopic) so the dispatcher
//     dead-letters rather than retrying forever against a schema mismatch
//
// Per CLAUDE.md §6 — wire format MUST be binary protobuf for Pub/Sub-attached
// topics. JSON encoding is rejected at publish time with "Invalid binary proto
// message". The fix for chora-creation lands task #33 (2026-05-16).
//
// Mirror of services/chora-consumption/internal/adapter/events/protomarshal —
// the canonical reference implementation. Topic coverage differs (creation
// publishes a different set), but the wire-format helpers + coercion shape
// match exactly.
package protomarshal

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"
)

// Envelope is the producer-side flat shape of chora.common.v1.EventEnvelope
// that the encoder needs. Mirrors the flat envelope shape used by the
// chora-consumption protomarshal package + libs/chora-go-common/envelope —
// defined locally to keep this package import-cycle-free (outbox.Publisher +
// events.CloudPublisher both depend on protomarshal).
type Envelope struct {
	EventID        string
	IdempotencyKey string
	TenantID       string
	GCID           string
	OccurredAt     time.Time
	PublishedAt    time.Time
	Traceparent    string
	Tracestate     string
	SourceProject  string
	SourceService  string
	SchemaVersion  int32
}

// ErrUnsupportedTopic is returned by MarshalPayload when the topic has no
// registered binary encoder. The dispatcher / caller should dead-letter rows
// that surface this error rather than retrying forever — the row is
// structurally incompatible with its destination schema.
var ErrUnsupportedTopic = errors.New("protomarshal: topic has no binary encoder; outbox row will dead-letter")

// IsUnsupportedTopic reports whether err is (or wraps) ErrUnsupportedTopic.
func IsUnsupportedTopic(err error) bool { return errors.Is(err, ErrUnsupportedTopic) }

// MarshalPayload converts a topic + envelope + loose payload map into the
// canonical binary protobuf wire bytes for that topic's Schema Registry
// schema. Returns ErrUnsupportedTopic if no encoder is registered for the
// supplied topic.
//
// Topics with registered encoders (per the deployed Schema Registry as of
// task #33 close):
//
//   - chora.creation.atom.created.v1
//   - chora.creation.question.authored.v1
//   - chora.creation.question.generation_requested.v1 (+ .v2, ADR-195 WS7)
//   - chora.creation.question.generation_completed.v1 (+ .v2, ADR-195 WS7)
//   - chora.creation.ai_assist.started.v1 (+ .v2, ADR-195 WS7)
//
// Adding more topics: append a case to the switch + implement
// encode{X}(env, payload) returning the wire bytes.
func MarshalPayload(topic string, env Envelope, payload map[string]any) ([]byte, error) {
	switch topic {
	case "chora.creation.atom.created.v1":
		return encodeAtomCreated(env, payload)
	case "chora.creation.atom.published.v1":
		return encodeAtomPublished(env, payload)
	case "chora.creation.atom.archived.v1":
		return encodeAtomArchived(env, payload)
	case "chora.creation.atom.updated.v1":
		return encodeAtomUpdated(env, payload)
	case "chora.creation.atom.reuse_visibility_changed.v1":
		return encodeAtomReuseVisibilityChanged(env, payload)
	case "chora.creation.atom.orphan_created.v1":
		return encodeAtomOrphanCreated(env, payload)
	case "chora.creation.question.authored.v1":
		return encodeQuestionAuthored(env, payload)
	case "chora.creation.question.generation_requested.v2":
		return encodeQuestionGenerationRequestedV2(env, payload)
	case "chora.creation.question.generation_completed.v2":
		return encodeQuestionGenerationCompletedV2(env, payload)
	case "chora.creation.ai_assist.started.v2":
		return encodeAiAssistStartedV2(env, payload)
	case "chora.creation.question_batch.accepted.v1":
		return encodeQuestionBatchAccepted(env, payload)
	case "chora.creation.collection.converted_to_study_list.v1":
		return encodeCollectionConvertedToStudyList(env, payload)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedTopic, topic)
	}
}

// -----------------------------------------------------------------------------
// CollectionConvertedToStudyList
// (chora.creation.collection.converted_to_study_list.v1)
// Schema — proto/events-flat/creation/collection/converted_to_study_list.proto
// (ADR-233 / spec-001 US5)
//
//	1 Envelope        envelope
//	2 string          collection_id
//	3 string          owner_gcid
//	4 repeated string atom_ids            (ENTITLED atoms, in curated order)
//	5 string          study_list_event_id (= this event's event_id; the key
//	                                       chora-consumption dedups the
//	                                       LearningPath creation on)
//
// This is the ONLY collection topic with a binary encoder — and it needs one.
// Its five siblings (created / updated / atom_added / atom_removed / deleted)
// are deliberately schemaless JSON-wire because nothing consumes them. This one
// has a REAL cross-domain consumer, so the topic is Schema-Registry bound: a
// JSON payload here is rejected at publish with "Invalid binary proto message"
// and dead-letters (the in_app.created lesson).
//
// NB atom_ids ORDER is load-bearing — the learner's curated sequence rides it
// into chora_consumption. protowire emits repeated strings in slice order, and
// the aggregate has already sorted them by Position.
// -----------------------------------------------------------------------------
func encodeCollectionConvertedToStudyList(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)

	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)

	if payload == nil {
		return out, nil
	}

	stringField := func(field protowire.Number, key string) error {
		v, ok, err := stringPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok && v != "" {
			out = appendString(out, field, v)
		}
		return nil
	}

	for _, step := range []func() error{
		func() error { return stringField(2, "collection_id") },
		func() error { return stringField(3, "owner_gcid") },
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}

	// Field 4: repeated string atom_ids. Accepts []string or []any (the shape
	// that arrives when the payload was JSON-decoded upstream). A malformed
	// list must FAIL LOUD — a silently-dropped atom is a study list quietly
	// missing content the learner is entitled to.
	if raw, present := payload["atom_ids"]; present && raw != nil {
		ids, ok := stringSlice(raw)
		if !ok {
			return nil, fmt.Errorf("field atom_ids: expected a string slice, got %T", raw)
		}
		for _, id := range ids {
			if id != "" {
				out = appendString(out, 4, id)
			}
		}
	}

	if err := stringField(5, "study_list_event_id"); err != nil {
		return nil, err
	}

	return out, nil
}

// -----------------------------------------------------------------------------
// AtomCreated (chora.creation.atom.created.v1)
// Schema — chora-contracts/proto/events-flat/creation/atom/created.proto
//
//	1  Envelope envelope
//	2  string   atom_id
//	3  AtomType atom_type            (enum varint)
//	4  AtomStatus status             (enum varint)
//	5  string   title
//	6  string   author_gcid
//	7  repeated string topic_node_ids
//	8  int32    difficulty
//	9  string   locale
//	10 Timestamp created_at
//
// -----------------------------------------------------------------------------
func encodeAtomCreated(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)

	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)

	if payload == nil {
		return out, nil
	}

	stringField := func(field protowire.Number, key string) error {
		v, ok, err := stringPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok && v != "" {
			out = appendString(out, field, v)
		}
		return nil
	}
	int32Field := func(field protowire.Number, key string) error {
		v, ok, err := int32PayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok && v != 0 {
			out = appendVarint(out, field, uint64(uint32(v)))
		}
		return nil
	}
	enumField := func(field protowire.Number, key string, lookup func(string) (int32, bool)) error {
		v, ok, err := enumPayloadField(payload, key, lookup)
		if err != nil {
			return err
		}
		if ok && v != 0 {
			out = appendVarint(out, field, uint64(uint32(v)))
		}
		return nil
	}
	timestampField := func(field protowire.Number, key string) error {
		t, ok, err := timestampPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok {
			out = appendLengthDelimited(out, field, encodeTimestamp(t))
		}
		return nil
	}

	for _, step := range []func() error{
		func() error { return stringField(2, "atom_id") },
		func() error { return enumField(3, "atom_type", lookupAtomType) },
		func() error { return enumField(4, "status", lookupAtomStatus) },
		func() error { return stringField(5, "title") },
		func() error { return stringField(6, "author_gcid") },
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}

	// field 7: repeated string topic_node_ids — composer-side "tags" is the
	// fallback: every live composer (questions PATCH + batch accept) emits the
	// atom's content tags under "tags", and chora-consumption projects field 7
	// into atom_index.topic_tags — its topic-matching axis (fog catalogue bias,
	// dose topics, W3-derived). Without the fallback, tags never reached the
	// wire and every atom projected tagless. An explicit topic_node_ids (KG
	// node linkage) wins when present.
	ids, ok := stringSlice(payload["topic_node_ids"])
	if !ok || len(ids) == 0 {
		ids, _ = stringSlice(payload["tags"])
	}
	for _, id := range ids {
		out = appendString(out, 7, id)
	}

	for _, step := range []func() error{
		func() error { return int32Field(8, "difficulty") },
		func() error { return stringField(9, "locale") },
		func() error { return timestampField(10, "created_at") },
		// field 11: course_id — restores the course association dropped by the
		// JSON→binary flip (Task #33). chora-consumption keys its course-scoped
		// atom_index projection + LearningPath bootstrap on this.
		func() error { return stringField(11, "course_id") },
		// fields 26/27: MCQ grading ground-truth (CHO-1627, L1 grading fix).
		// correct_option_id = the OptionID of the IsCorrect option;
		// answer_count = number of MCQ options. Empty/zero for non-MCQ atoms —
		// stringField / int32Field elide proto3 defaults so the wire bytes stay
		// byte-compatible with producers that never set them. chora-consumption
		// scores MCQ submissions against correct_option_id server-side.
		func() error { return stringField(26, "correct_option_id") },
		func() error { return int32Field(27, "answer_count") },
		// field 30: reuse_visibility (ADR-229 WS-1) — the author-consent
		// audience at creation time (private by construction on New; the
		// PRIVATE=1 varint is emitted, only UNSPECIFIED elides).
		func() error { return enumField(30, "reuse_visibility", lookupReuseVisibility) },
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// -----------------------------------------------------------------------------
// AtomArchived (chora.creation.atom.archived.v1) — the atom soft-delete/archive
// signal (ADR-217 Debt 3). Wire layout:
//
//	1 Envelope   envelope
//	2 string     atom_id
//	3 AtomType   question_type      (enum varint; map key "atom_type")
//	4 AtomStatus status             (enum varint; always ARCHIVED=3)
//	5 string     archived_by_gcid
//	6 string     reason             (optional; elided when empty)
//	7 Timestamp  archived_at
func encodeAtomArchived(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)

	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)

	if payload == nil {
		return out, nil
	}

	stringField := func(field protowire.Number, key string) error {
		v, ok, err := stringPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok && v != "" {
			out = appendString(out, field, v)
		}
		return nil
	}
	enumField := func(field protowire.Number, key string, lookup func(string) (int32, bool)) error {
		v, ok, err := enumPayloadField(payload, key, lookup)
		if err != nil {
			return err
		}
		if ok && v != 0 {
			out = appendVarint(out, field, uint64(uint32(v)))
		}
		return nil
	}
	timestampField := func(field protowire.Number, key string) error {
		t, ok, err := timestampPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok {
			out = appendLengthDelimited(out, field, encodeTimestamp(t))
		}
		return nil
	}

	for _, step := range []func() error{
		func() error { return stringField(2, "atom_id") },
		func() error { return enumField(3, "atom_type", lookupAtomType) },
		func() error { return enumField(4, "status", lookupAtomStatus) },
		func() error { return stringField(5, "archived_by_gcid") },
		func() error { return stringField(6, "reason") },
		func() error { return timestampField(7, "archived_at") },
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// AtomPublished (chora.creation.atom.published.v1)
// Schema — chora-contracts/proto/events/creation/atom.proto §AtomPublished
//
//	1  Envelope envelope
//	2  string   atom_id
//	3  AtomType question_type            (enum varint; "atom_type" map key)
//	4  AtomStatus status                 (enum varint; always PUBLISHED)
//	5  string   title
//	6  string   author_gcid
//	7  repeated string topic_node_ids    ("tags" fallback, mirror created)
//	8  int32    difficulty
//	9  string   locale
//	10 string   current_revision_id
//	11 int32    current_revision_number
//	12 Timestamp published_at
//	20 string   stem
//	21 string   subject
//	24 string   author_note
//	26 string   correct_option_id        (MCQ answer key; elided for non-MCQ)
//	27 int32    answer_count             (MCQ option count; elided for non-MCQ)
//	28 bool     has_open_ended_question  (answerability for OE atoms)
//
// Field 22 (cognitive_level) IS encoded when the publish snapshot carries a
// level (WS-C3, CHO-2082 — chora-consumption's campaign question lane
// level-filters on it). Fields 23 (imda_dimension_tags) and 25 (media_assets)
// remain intentionally NOT encoded: no producer on the publish path populates
// them and proto3 default-elision keeps the wire bytes valid. The load-bearing
// answerability contract (4/10/11/12/26/27/28) is fully encoded.
// -----------------------------------------------------------------------------
func encodeAtomPublished(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)

	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)

	if payload == nil {
		return out, nil
	}

	stringField := func(field protowire.Number, key string) error {
		v, ok, err := stringPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok && v != "" {
			out = appendString(out, field, v)
		}
		return nil
	}
	int32Field := func(field protowire.Number, key string) error {
		v, ok, err := int32PayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok && v != 0 {
			out = appendVarint(out, field, uint64(uint32(v)))
		}
		return nil
	}
	enumField := func(field protowire.Number, key string, lookup func(string) (int32, bool)) error {
		v, ok, err := enumPayloadField(payload, key, lookup)
		if err != nil {
			return err
		}
		if ok && v != 0 {
			out = appendVarint(out, field, uint64(uint32(v)))
		}
		return nil
	}
	timestampField := func(field protowire.Number, key string) error {
		t, ok, err := timestampPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok {
			out = appendLengthDelimited(out, field, encodeTimestamp(t))
		}
		return nil
	}
	boolField := func(field protowire.Number, key string) error {
		v, ok, err := boolPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok && v {
			out = appendVarint(out, field, 1)
		}
		return nil
	}

	for _, step := range []func() error{
		func() error { return stringField(2, "atom_id") },
		func() error { return enumField(3, "atom_type", lookupAtomType) },
		func() error { return enumField(4, "status", lookupAtomStatus) },
		func() error { return stringField(5, "title") },
		func() error { return stringField(6, "author_gcid") },
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}

	// field 7: repeated string topic_node_ids — explicit KG node linkage wins;
	// otherwise fall back to composer-side "tags" (mirror encodeAtomCreated).
	ids, ok := stringSlice(payload["topic_node_ids"])
	if !ok || len(ids) == 0 {
		ids, _ = stringSlice(payload["tags"])
	}
	for _, id := range ids {
		out = appendString(out, 7, id)
	}

	for _, step := range []func() error{
		func() error { return int32Field(8, "difficulty") },
		func() error { return stringField(9, "locale") },
		func() error { return stringField(10, "current_revision_id") },
		func() error { return int32Field(11, "current_revision_number") },
		func() error { return timestampField(12, "published_at") },
		func() error { return stringField(20, "stem") },
		func() error { return stringField(21, "subject") },
		func() error { return stringField(24, "author_note") },
		// fields 26/27/28: answerability — correct_option_id + answer_count
		// (MCQ ground-truth) + has_open_ended_question (OE). Proto3-elided when
		// empty/zero/false so a non-MCQ / MCQ atom stays byte-stable.
		func() error { return stringField(26, "correct_option_id") },
		func() error { return int32Field(27, "answer_count") },
		func() error { return boolField(28, "has_open_ended_question") },
		// field 22: cognitive_level (WS-C3, CHO-2082) — the ORIGINAL-Bloom
		// level for chora-consumption's campaign question lane. NB the PROTO
		// numbering (synthesis=5, evaluation=6) differs from the campaign
		// ladder ORDER — levels always travel as labels at the edges.
		func() error { return enumField(22, "cognitive_level", lookupCognitiveLevel) },
		// field 30: reuse_visibility (ADR-229 WS-1) — the author-consent
		// audience at publish time (the sharing atom_projections read-model
		// maps it onto its column).
		func() error { return enumField(30, "reuse_visibility", lookupReuseVisibility) },
		// field 31: author_display_name — denormalised so downstream consumers
		// (chora-sharing feed cards) render the author without a cross-DB
		// identity lookup. Empty when identity is unavailable at publish time.
		func() error { return stringField(31, "author_display_name") },
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// -----------------------------------------------------------------------------
// AtomUpdated (chora.creation.atom.updated.v1)
// Schema - chora-contracts/proto/events-flat/creation/atom/updated.v2.proto
// (ADR-244 D5)
//
//	1  Envelope   envelope
//	2  string     atom_id
//	3  AtomType   question_type          (enum varint; "atom_type" map key)
//	4  AtomStatus status                 (enum varint; always PUBLISHED today)
//	5  string     title
//	6  string     author_gcid
//	7  repeated string topic_node_ids    ("tags" fallback, mirror published)
//	8  int32      difficulty
//	9  string     locale
//	10 repeated string changed_fields
//	11 Timestamp  updated_at
//	20 string     stem
//	21 string     subject
//	22 CognitiveLevel cognitive_level    (enum varint)
//	23 repeated ImdaDimensionTag imda_dimension_tags (PACKED enum)
//	24 string     author_note
//	25 repeated AtomMediaAsset media_assets
//
// changed_fields (10) is the load-bearing field: chora-consumption's KG
// invalidation branches on it, so a silently-dropped entry reads as "nothing
// changed". It therefore fails loud on a malformed list rather than skipping.
//
// Field 9 (locale) has no producer: the aggregate carries no locale, so it
// stays at its proto3 default rather than being invented here.
// -----------------------------------------------------------------------------
func encodeAtomUpdated(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 384)

	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)

	if payload == nil {
		return out, nil
	}

	stringField := func(field protowire.Number, key string) error {
		v, ok, err := stringPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok && v != "" {
			out = appendString(out, field, v)
		}
		return nil
	}
	int32Field := func(field protowire.Number, key string) error {
		v, ok, err := int32PayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok && v != 0 {
			w, werr := nonNegativeVarint(int64(v), key)
			if werr != nil {
				return werr
			}
			out = appendVarint(out, field, w)
		}
		return nil
	}
	enumField := func(field protowire.Number, key string, lookup func(string) (int32, bool)) error {
		v, ok, err := enumPayloadField(payload, key, lookup)
		if err != nil {
			return err
		}
		if ok && v != 0 {
			w, werr := nonNegativeVarint(int64(v), key)
			if werr != nil {
				return werr
			}
			out = appendVarint(out, field, w)
		}
		return nil
	}
	timestampField := func(field protowire.Number, key string) error {
		t, ok, err := timestampPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok {
			out = appendLengthDelimited(out, field, encodeTimestamp(t))
		}
		return nil
	}

	for _, step := range []func() error{
		func() error { return stringField(2, "atom_id") },
		func() error { return enumField(3, "atom_type", lookupAtomType) },
		func() error { return enumField(4, "status", lookupAtomStatus) },
		func() error { return stringField(5, "title") },
		func() error { return stringField(6, "author_gcid") },
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}

	// field 7: repeated string topic_node_ids - explicit KG node linkage wins;
	// otherwise fall back to composer-side "tags" (mirror encodeAtomPublished).
	ids, ok := stringSlice(payload["topic_node_ids"])
	if !ok || len(ids) == 0 {
		ids, _ = stringSlice(payload["tags"])
	}
	for _, id := range ids {
		out = appendString(out, 7, id)
	}

	for _, step := range []func() error{
		func() error { return int32Field(8, "difficulty") },
		func() error { return stringField(9, "locale") },
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}

	// field 10: repeated string changed_fields. A malformed list is a producer
	// bug that would reach the consumer as "no fields changed", so it fails
	// loud instead of encoding an empty list.
	if raw, present := payload["changed_fields"]; present && raw != nil {
		fields, ok := stringSlice(raw)
		if !ok {
			return nil, fmt.Errorf("field changed_fields: expected a string slice, got %T", raw)
		}
		for i, f := range fields {
			if f == "" {
				return nil, fmt.Errorf("field changed_fields[%d]: empty field name", i)
			}
			out = appendString(out, 10, f)
		}
	}

	for _, step := range []func() error{
		func() error { return timestampField(11, "updated_at") },
		func() error { return stringField(20, "stem") },
		func() error { return stringField(21, "subject") },
		func() error { return enumField(22, "cognitive_level", lookupCognitiveLevel) },
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}

	// field 23: repeated ImdaDimensionTag is PACKED, matching what proto.Marshal
	// emits for a proto3 repeated enum.
	labels, present := payload["imda_dimension_tags"]
	if present && labels != nil {
		tags, ok := stringSlice(labels)
		if !ok {
			return nil, fmt.Errorf("field imda_dimension_tags: expected a string slice, got %T", labels)
		}
		out, err = appendPackedEnums(out, 23, "imda_dimension_tags", tags, lookupImdaDimensionTag)
		if err != nil {
			return nil, err
		}
	}

	if err := stringField(24, "author_note"); err != nil {
		return nil, err
	}

	// field 25: repeated AtomMediaAsset.
	if err := appendAtomMediaAssets(&out, 25, payload, "media_assets"); err != nil {
		return nil, err
	}

	return out, nil
}

// appendPackedEnums writes a proto3 packed repeated enum field: one
// length-delimited run of varints in slice order. An unresolvable label is a
// HARD failure: encoding it as UNSPECIFIED would blank the value on the wire
// and leave the producer bug invisible.
func appendPackedEnums(out []byte, field protowire.Number, key string, labels []string, lookup func(string) (int32, bool)) ([]byte, error) {
	if len(labels) == 0 {
		return out, nil
	}
	packed := make([]byte, 0, len(labels)*2)
	for i, label := range labels {
		v, ok := lookup(label)
		if !ok {
			return nil, fmt.Errorf("field %s[%d]: unknown enum label %q", key, i, label)
		}
		w, err := nonNegativeVarint(int64(v), key)
		if err != nil {
			return nil, err
		}
		packed = protowire.AppendVarint(packed, w)
	}
	return appendLengthDelimited(out, field, packed), nil
}

// nonNegativeVarint converts a descriptor-resolved enum / small int payload
// value to its varint wire form. Chora's contract enums and counted fields are
// non-negative by construction; a negative would need the 10-byte
// sign-extended encoding this encoder deliberately does not implement, so it
// refuses loudly rather than truncating (gosec G115 posture: guard, then
// convert).
func nonNegativeVarint(v int64, key string) (uint64, error) {
	if v < 0 {
		return 0, fmt.Errorf("field %s: negative wire value %d refused (sign-extended varint unsupported)", key, v)
	}
	return uint64(v), nil
}

// appendAtomMediaAssets writes a repeated AtomMediaAsset field (nested message
// {1: type (enum), 2: url, 3: alt_text, 4: mime, 5: size_bytes (int64)}) with
// the supplied field number, one length-delimited entry per asset in slice
// order. Absent/nil emits nothing.
//
// Accepts payload[key] as []map[string]any or []any of maps via coerceMapSlice.
// An unknown asset type fails loud: an asset encoded as UNSPECIFIED tells the
// consumer nothing about what it is holding.
func appendAtomMediaAssets(out *[]byte, field protowire.Number, payload map[string]any, key string) error {
	elements, err := coerceMapSlice(payload, key)
	if err != nil {
		return err
	}
	for i, m := range elements {
		typeLabel, err := quotaString(m, key, i, "type")
		if err != nil {
			return err
		}
		url, err := quotaString(m, key, i, "url")
		if err != nil {
			return err
		}
		altText, err := quotaString(m, key, i, "alt_text")
		if err != nil {
			return err
		}
		mime, err := quotaString(m, key, i, "mime")
		if err != nil {
			return err
		}
		sizeBytes, err := quotaInt64(m, key, i, "size_bytes")
		if err != nil {
			return err
		}
		typeWire, ok := lookupAtomMediaAssetType(typeLabel)
		if !ok {
			return fmt.Errorf("field %s[%d].type: unknown media asset type %q", key, i, typeLabel)
		}
		entry := make([]byte, 0, 32+len(url)+len(altText)+len(mime))
		if typeWire != 0 {
			w, werr := nonNegativeVarint(int64(typeWire), key)
			if werr != nil {
				return werr
			}
			entry = appendVarint(entry, 1, w)
		}
		if url != "" {
			entry = appendString(entry, 2, url)
		}
		if altText != "" {
			entry = appendString(entry, 3, altText)
		}
		if mime != "" {
			entry = appendString(entry, 4, mime)
		}
		if sizeBytes != 0 {
			// A negative byte size is corrupt data, not a wire shape.
			w, werr := nonNegativeVarint(sizeBytes, key)
			if werr != nil {
				return werr
			}
			entry = appendVarint(entry, 5, w)
		}
		*out = appendLengthDelimited(*out, field, entry)
	}
	return nil
}

// quotaInt64 reads an int64 sub-field from a coerced map. Absent/nil → 0.
// Fails loud on a non-numeric value. Distinct from quotaInt32 because
// AtomMediaAsset.size_bytes is int64 and truncating it to 32 bits would
// misreport any asset above 2 GiB.
func quotaInt64(m map[string]any, key string, i int, field string) (int64, error) {
	v, present := m[field]
	if !present || v == nil {
		return 0, nil
	}
	n, ok := asInt64(v)
	if !ok {
		return 0, fmt.Errorf("field %s[%d].%s: expected int64-convertible, got %T", key, i, field, v)
	}
	return n, nil
}

// -----------------------------------------------------------------------------
// AtomReuseVisibilityChanged (chora.creation.atom.reuse_visibility_changed.v1)
// Schema — chora-contracts/proto/events-flat/creation/atom/
// reuse_visibility_changed.proto (ADR-229 WS-1, CHO-2127)
//
//	1 Envelope        envelope
//	2 string          atom_id
//	3 ReuseVisibility reuse_visibility     (enum varint; the NEW audience)
//	4 ReuseVisibility previous_visibility  (enum varint; before the change)
//	5 string          author_gcid
//	6 Timestamp       changed_at
//
// -----------------------------------------------------------------------------
func encodeAtomReuseVisibilityChanged(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 192)

	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)

	if payload == nil {
		return out, nil
	}

	stringField := func(field protowire.Number, key string) error {
		v, ok, err := stringPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok && v != "" {
			out = appendString(out, field, v)
		}
		return nil
	}
	enumField := func(field protowire.Number, key string, lookup func(string) (int32, bool)) error {
		v, ok, err := enumPayloadField(payload, key, lookup)
		if err != nil {
			return err
		}
		if ok && v != 0 {
			out = appendVarint(out, field, uint64(uint32(v)))
		}
		return nil
	}
	timestampField := func(field protowire.Number, key string) error {
		t, ok, err := timestampPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok {
			out = appendLengthDelimited(out, field, encodeTimestamp(t))
		}
		return nil
	}

	for _, step := range []func() error{
		func() error { return stringField(2, "atom_id") },
		func() error { return enumField(3, "reuse_visibility", lookupReuseVisibility) },
		func() error { return enumField(4, "previous_visibility", lookupReuseVisibility) },
		func() error { return stringField(5, "author_gcid") },
		func() error { return timestampField(6, "changed_at") },
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// -----------------------------------------------------------------------------
// AtomOrphanCreated (chora.creation.atom.orphan_created.v1)
// Schema — chora-contracts/proto/events-flat/creation/atom/
// orphan_created.proto (ADR-229 Amendment A1, CHO-2132)
//
//	1 Envelope  envelope
//	2 string    orphan_atom_id
//	3 string    orphaned_from_atom_id
//	4 string    source_revision_id
//	5 string    author_gcid
//	6 string    trigger              (narrowed | unshared | archived)
//	7 Timestamp orphaned_at
//
// -----------------------------------------------------------------------------
func encodeAtomOrphanCreated(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)

	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)

	if payload == nil {
		return out, nil
	}

	stringField := func(field protowire.Number, key string) error {
		v, ok, err := stringPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok && v != "" {
			out = appendString(out, field, v)
		}
		return nil
	}
	timestampField := func(field protowire.Number, key string) error {
		t, ok, err := timestampPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok {
			out = appendLengthDelimited(out, field, encodeTimestamp(t))
		}
		return nil
	}

	for _, step := range []func() error{
		func() error { return stringField(2, "orphan_atom_id") },
		func() error { return stringField(3, "orphaned_from_atom_id") },
		func() error { return stringField(4, "source_revision_id") },
		func() error { return stringField(5, "author_gcid") },
		func() error { return stringField(6, "trigger") },
		func() error { return timestampField(7, "orphaned_at") },
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// -----------------------------------------------------------------------------
// QuestionAuthored (chora.creation.question.authored.v1)
// Schema — chora-contracts/proto/events-flat/creation/question/authored.proto
//
//	1  Envelope envelope
//	2  string   question_id
//	3  string   atom_id
//	4  QuestionType question_type      (enum varint)
//	5  QuestionSourceType source_type  (enum varint)
//	6  string   author_gcid
//	7  int32    revision_number
//	8  string   revision_id
//	9  string   source_job_id
//	10 Timestamp authored_at
//
// -----------------------------------------------------------------------------
func encodeQuestionAuthored(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)

	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)

	if payload == nil {
		return out, nil
	}

	stringField := func(field protowire.Number, key string) error {
		v, ok, err := stringPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok && v != "" {
			out = appendString(out, field, v)
		}
		return nil
	}
	int32Field := func(field protowire.Number, key string) error {
		v, ok, err := int32PayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok && v != 0 {
			out = appendVarint(out, field, uint64(uint32(v)))
		}
		return nil
	}
	enumField := func(field protowire.Number, key string, lookup func(string) (int32, bool)) error {
		v, ok, err := enumPayloadField(payload, key, lookup)
		if err != nil {
			return err
		}
		if ok && v != 0 {
			out = appendVarint(out, field, uint64(uint32(v)))
		}
		return nil
	}
	timestampField := func(field protowire.Number, key string) error {
		t, ok, err := timestampPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok {
			out = appendLengthDelimited(out, field, encodeTimestamp(t))
		}
		return nil
	}

	for _, step := range []func() error{
		func() error { return stringField(2, "question_id") },
		func() error { return stringField(3, "atom_id") },
		func() error { return enumField(4, "question_type", lookupQuestionType) },
		func() error { return enumField(5, "source_type", lookupQuestionSourceType) },
		func() error { return stringField(6, "author_gcid") },
		func() error { return int32Field(7, "revision_number") },
		func() error { return stringField(8, "revision_id") },
		func() error { return stringField(9, "source_job_id") },
		func() error { return timestampField(10, "authored_at") },
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// =============================================================================
// ADR-195 WS7 (D7) — .v2 compose encoders. Mirror their v1 counterparts but
// DROP the legacy discriminant (job_type / job_kind) and carry the unified
// compose model {operation, intent, input_kind} as trailing string fields. The
// non-discriminant fields keep their v1 tags so v1<->v2 parallel-publish is
// wire-safe.
// =============================================================================

// -----------------------------------------------------------------------------
// QuestionGenerationRequestedV2 (chora.creation.question.generation_requested.v2)
// Schema — proto/events-flat/creation/question/generation_requested.v2.proto
//
//	1  Envelope envelope
//	2  string   job_id
//	3  string   atom_id
//	4  string   author_gcid
//	(5 reserved — was job_type; retired ADR-195 D1/D7)
//	6  QuestionType question_type           (enum varint)
//	7  string   target_question_id
//	8  string   source_blob_uri
//	9  string   source_mime_type
//	10 string   mana_action_code
//	11 int32    mana_charged
//	12 string   settings_json
//	13 Timestamp requested_at
//	14 string   operation   (always "compose")
//	15 string   intent
//	16 string   input_kind
//
// -----------------------------------------------------------------------------
func encodeQuestionGenerationRequestedV2(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 384)

	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)

	if payload == nil {
		return out, nil
	}

	stringField := func(field protowire.Number, key string) error {
		v, ok, err := stringPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok && v != "" {
			out = appendString(out, field, v)
		}
		return nil
	}
	int32Field := func(field protowire.Number, key string) error {
		v, ok, err := int32PayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok && v != 0 {
			out = appendVarint(out, field, uint64(uint32(v)))
		}
		return nil
	}
	enumField := func(field protowire.Number, key string, lookup func(string) (int32, bool)) error {
		v, ok, err := enumPayloadField(payload, key, lookup)
		if err != nil {
			return err
		}
		if ok && v != 0 {
			out = appendVarint(out, field, uint64(uint32(v)))
		}
		return nil
	}
	timestampField := func(field protowire.Number, key string) error {
		t, ok, err := timestampPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok {
			out = appendLengthDelimited(out, field, encodeTimestamp(t))
		}
		return nil
	}

	for _, step := range []func() error{
		func() error { return stringField(2, "job_id") },
		func() error { return stringField(3, "atom_id") },
		func() error { return stringField(4, "author_gcid") },
		// field 5 (job_type) RETIRED — carried by intent/input_kind below.
		func() error { return enumField(6, "question_type", lookupQuestionType) },
		func() error { return stringField(7, "target_question_id") },
		func() error { return stringField(8, "source_blob_uri") },
		func() error { return stringField(9, "source_mime_type") },
		func() error { return stringField(10, "mana_action_code") },
		func() error { return int32Field(11, "mana_charged") },
		func() error { return stringField(12, "settings_json") },
		func() error { return timestampField(13, "requested_at") },
		func() error { return stringField(14, "operation") },
		func() error { return stringField(15, "intent") },
		func() error { return stringField(16, "input_kind") },
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// -----------------------------------------------------------------------------
// QuestionGenerationCompletedV2 (chora.creation.question.generation_completed.v2)
// Schema — proto/events-flat/creation/question/generation_completed.v2.proto
//
//	1  Envelope envelope
//	2  string   job_id
//	3  string   atom_id
//	4  string   author_gcid
//	(5 reserved — was job_type; retired ADR-195 D1/D7)
//	6  QuestionGenerationJobStatus status    (enum varint)
//	7  int32    candidate_count
//	8  string   failure_category
//	9  string   failure_message
//	10 int32    mana_refunded
//	11 Timestamp completed_at
//	12 string   operation   (always "compose")
//	13 string   intent
//	14 string   input_kind
//
// -----------------------------------------------------------------------------
func encodeQuestionGenerationCompletedV2(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)

	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)

	if payload == nil {
		return out, nil
	}

	stringField := func(field protowire.Number, key string) error {
		v, ok, err := stringPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok && v != "" {
			out = appendString(out, field, v)
		}
		return nil
	}
	int32Field := func(field protowire.Number, key string) error {
		v, ok, err := int32PayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok && v != 0 {
			out = appendVarint(out, field, uint64(uint32(v)))
		}
		return nil
	}
	enumField := func(field protowire.Number, key string, lookup func(string) (int32, bool)) error {
		v, ok, err := enumPayloadField(payload, key, lookup)
		if err != nil {
			return err
		}
		if ok && v != 0 {
			out = appendVarint(out, field, uint64(uint32(v)))
		}
		return nil
	}
	timestampField := func(field protowire.Number, key string) error {
		t, ok, err := timestampPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok {
			out = appendLengthDelimited(out, field, encodeTimestamp(t))
		}
		return nil
	}

	for _, step := range []func() error{
		func() error { return stringField(2, "job_id") },
		func() error { return stringField(3, "atom_id") },
		func() error { return stringField(4, "author_gcid") },
		// field 5 (job_type) RETIRED — carried by intent/input_kind below.
		func() error { return enumField(6, "status", lookupQuestionGenerationJobStatus) },
		func() error { return int32Field(7, "candidate_count") },
		func() error { return stringField(8, "failure_category") },
		func() error { return stringField(9, "failure_message") },
		func() error { return int32Field(10, "mana_refunded") },
		func() error { return timestampField(11, "completed_at") },
		func() error { return stringField(12, "operation") },
		func() error { return stringField(13, "intent") },
		func() error { return stringField(14, "input_kind") },
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// -----------------------------------------------------------------------------
// AiAssistStartedV2 (chora.creation.ai_assist.started.v2)
// Schema — proto/events-flat/creation/ai_assist/started.v2.proto
//
// Mirrors encodeAiAssistStarted but DROPS job_kind (reserved tag 15) and adds
// operation(23)/intent(24)/input_kind(25). All other fields keep their v1 tags.
// -----------------------------------------------------------------------------
func encodeAiAssistStartedV2(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 512)

	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)

	if payload == nil {
		return out, nil
	}

	stringField := func(field protowire.Number, key string) error {
		v, ok, err := stringPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok && v != "" {
			out = appendString(out, field, v)
		}
		return nil
	}
	int32Field := func(field protowire.Number, key string) error {
		v, ok, err := int32PayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok && v != 0 {
			out = appendVarint(out, field, uint64(uint32(v)))
		}
		return nil
	}
	timestampField := func(field protowire.Number, key string) error {
		t, ok, err := timestampPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok {
			out = appendLengthDelimited(out, field, encodeTimestamp(t))
		}
		return nil
	}
	boolField := func(field protowire.Number, key string) error {
		v, ok, err := boolPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok && v {
			out = appendVarint(out, field, 1)
		}
		return nil
	}

	for _, step := range []func() error{
		func() error { return stringField(2, "assist_id") },
		func() error { return stringField(3, "tenant_id") },
		func() error { return stringField(4, "author_gcid") },
		func() error { return stringField(5, "atom_id") },
		func() error { return stringField(6, "content_type") },
		func() error { return stringField(7, "prompt") },
		func() error { return int32Field(8, "requested_count") },
		func() error { return int32Field(9, "difficulty") },
		func() error { return timestampField(10, "started_at") },
		func() error { return int32Field(11, "max_retries") },
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}

	// Field 12: map<string,string> metadata.
	if err := appendStringMapField(&out, 12, payload, "metadata"); err != nil {
		return nil, err
	}

	// Fields 13/14: author-opt-in image flags (proto3-false elision).
	for _, step := range []func() error{
		func() error { return boolField(13, "image_for_stem") },
		func() error { return boolField(14, "image_for_answer") },
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}

	// Field 15 (job_kind) RETIRED — the single/batch/image_regen distinction is
	// carried by intent + input_kind + the presence of type_plan (ADR-195 D1/D7).

	// Fields 16-18: grounding (strings, default-elided).
	for _, step := range []func() error{
		func() error { return stringField(16, "grounding_mode") },
		func() error { return stringField(17, "source_blob_uri") },
		func() error { return stringField(18, "source_mime_type") },
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}

	// Field 19: repeated string target_growth_edges.
	if edges, ok := stringSlice(payload["target_growth_edges"]); ok {
		for _, e := range edges {
			out = appendString(out, 19, e)
		}
	}

	// Field 20: repeated SourceFileRef source_files.
	if err := appendSourceFileRefs(&out, 20, payload, "source_files"); err != nil {
		return nil, err
	}

	// Field 21: repeated GenerationTypeQuota type_plan.
	if err := appendTypePlanQuotas(&out, 21, payload, "type_plan"); err != nil {
		return nil, err
	}

	// Field 22: ImageRegenSpec regen.
	if err := appendImageRegenSpec(&out, 22, payload, "regen"); err != nil {
		return nil, err
	}

	// Fields 23-25: ADR-195 WS7 compose model (replaces the retired job_kind).
	// Field 26 (CHO-1658): existing_question_json — the author's current question
	// content for intent=model_answer_fill (JSON string; proto3-elided when empty,
	// so byte-stable for every non-fill event).
	for _, step := range []func() error{
		func() error { return stringField(23, "operation") },
		func() error { return stringField(24, "intent") },
		func() error { return stringField(25, "input_kind") },
		func() error { return stringField(26, "existing_question_json") },
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// appendTypePlanQuotas writes a repeated GenerationTypeQuota field (nested
// message {1: question_type (string), 2: count (int32), 3: max_images (int32)})
// with the supplied field number, one length-delimited entry per quota in slice
// order. Proto3 default-elision applies WITHIN each quota: a zero count /
// max_images is omitted (the validated domain TypePlan guarantees count >= 1).
//
// Accepts payload[key] as []map[string]any OR []any whose elements are maps
// (the JSON-decoded bridge shape) via coerceMapSlice. Fails loud on any other
// shape or a non-coercible scalar per [[feedback-no-stubs-real-wiring]] — a
// silently mis-encoded quota would mis-route the mixed batch.
func appendTypePlanQuotas(out *[]byte, field protowire.Number, payload map[string]any, key string) error {
	elements, err := coerceMapSlice(payload, key)
	if err != nil {
		return err
	}
	for i, m := range elements {
		questionType, err := quotaString(m, key, i, "question_type")
		if err != nil {
			return err
		}
		count, err := quotaInt32(m, key, i, "count")
		if err != nil {
			return err
		}
		maxImages, err := quotaInt32(m, key, i, "max_images")
		if err != nil {
			return err
		}
		imageForStem, err := quotaBool(m, key, i, "image_for_stem")
		if err != nil {
			return err
		}
		imageForAnswer, err := quotaBool(m, key, i, "image_for_answer")
		if err != nil {
			return err
		}
		entry := make([]byte, 0, 16+len(questionType))
		if questionType != "" {
			entry = appendString(entry, 1, questionType)
		}
		if count != 0 {
			entry = appendVarint(entry, 2, uint64(uint32(count)))
		}
		if maxImages != 0 {
			entry = appendVarint(entry, 3, uint64(uint32(maxImages)))
		}
		// CHO-1825 2b: per-type deterministic image opt-in (proto bool = varint).
		// Default-elision: false omits the field (legacy quotas byte-identical).
		if imageForStem {
			entry = appendVarint(entry, 4, 1)
		}
		if imageForAnswer {
			entry = appendVarint(entry, 5, 1)
		}
		*out = appendLengthDelimited(*out, field, entry)
	}
	return nil
}

// appendImageRegenSpec writes a SINGULAR ImageRegenSpec field (nested message
// {1: draft_id, 2: placement, 3: prompt, 4: mode, 5: current_stem,
// 6: current_model_answer, 7: original_source, 8: original_image_gcs_uri (all
// string)}) with the supplied
// field number. Accepts payload[key] as map[string]any (the bridge /
// subscriber shape). Absent / nil / a map with no populated sub-field emits
// nothing (proto3 message default-elision) so non-regen started events stay
// byte-identical. Fails loud on a non-map value or a non-string sub-field per
// [[feedback-no-stubs-real-wiring]] — a silently mis-encoded regen would render
// the wrong image (or none).
//
// Fields 5-7 (I2) carry the author's CURRENT edited question context so the
// image regenerate prompt reflects unsaved review-UI edits; each elides on the
// empty string so a regen spec that omits them stays byte-identical to a pre-I2
// producer.
func appendImageRegenSpec(out *[]byte, field protowire.Number, payload map[string]any, key string) error {
	raw, present := payload[key]
	if !present || raw == nil {
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("field %s: expected map, got %T", key, raw)
	}
	draftID, err := quotaString(m, key, 0, "draft_id")
	if err != nil {
		return err
	}
	placement, err := quotaString(m, key, 0, "placement")
	if err != nil {
		return err
	}
	prompt, err := quotaString(m, key, 0, "prompt")
	if err != nil {
		return err
	}
	mode, err := quotaString(m, key, 0, "mode")
	if err != nil {
		return err
	}
	currentStem, err := quotaString(m, key, 0, "current_stem")
	if err != nil {
		return err
	}
	currentModelAnswer, err := quotaString(m, key, 0, "current_model_answer")
	if err != nil {
		return err
	}
	originalSource, err := quotaString(m, key, 0, "original_source")
	if err != nil {
		return err
	}
	originalImageGcsURI, err := quotaString(m, key, 0, "original_image_gcs_uri")
	if err != nil {
		return err
	}
	// Default-elision: an empty spec (no populated sub-field) emits nothing so
	// the bytes stay identical to omitting the key entirely.
	if draftID == "" && placement == "" && prompt == "" && mode == "" &&
		currentStem == "" && currentModelAnswer == "" && originalSource == "" &&
		originalImageGcsURI == "" {
		return nil
	}
	entry := make([]byte, 0, 32)
	if draftID != "" {
		entry = appendString(entry, 1, draftID)
	}
	if placement != "" {
		entry = appendString(entry, 2, placement)
	}
	if prompt != "" {
		entry = appendString(entry, 3, prompt)
	}
	if mode != "" {
		entry = appendString(entry, 4, mode)
	}
	if currentStem != "" {
		entry = appendString(entry, 5, currentStem)
	}
	if currentModelAnswer != "" {
		entry = appendString(entry, 6, currentModelAnswer)
	}
	if originalSource != "" {
		entry = appendString(entry, 7, originalSource)
	}
	if originalImageGcsURI != "" {
		entry = appendString(entry, 8, originalImageGcsURI)
	}
	*out = appendLengthDelimited(*out, field, entry)
	return nil
}

// quotaString reads a string sub-field from a coerced quota map. Absent/nil →
// "". Fails loud on a non-string value.
func quotaString(m map[string]any, key string, i int, field string) (string, error) {
	v, present := m[field]
	if !present || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("field %s[%d].%s: expected string, got %T", key, i, field, v)
	}
	return s, nil
}

// quotaInt32 reads an int32 sub-field from a coerced quota map. Absent/nil → 0.
// Fails loud on a non-numeric value.
func quotaInt32(m map[string]any, key string, i int, field string) (int32, error) {
	v, present := m[field]
	if !present || v == nil {
		return 0, nil
	}
	n, ok := asInt32(v)
	if !ok {
		return 0, fmt.Errorf("field %s[%d].%s: expected int32-convertible, got %T", key, i, field, v)
	}
	return n, nil
}

// quotaBool reads a bool sub-field from a coerced quota map. Absent/nil → false
// (proto3 default-elision). Fails loud on a non-bool value (CHO-1825 2b).
func quotaBool(m map[string]any, key string, i int, field string) (bool, error) {
	v, present := m[field]
	if !present || v == nil {
		return false, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("field %s[%d].%s: expected bool, got %T", key, i, field, v)
	}
	return b, nil
}

// appendSourceFileRefs writes a repeated SourceFileRef field (nested message
// {1: blob_uri, 2: mime_type, 3: role}) with the supplied field number, one
// length-delimited entry per file, in slice order.
//
// Accepts payload[key] as []map[string]any, []map[string]string, OR []any
// whose elements are map[string]any / map[string]string (the latter shapes
// arrive when the payload was JSON-decoded upstream — e.g. the bridge's
// typed-struct round-trip). Fails loud on any other shape per
// [[feedback-no-stubs-real-wiring]] — a silently-skipped grounding file would
// strand the crew without its source material.
func appendSourceFileRefs(out *[]byte, field protowire.Number, payload map[string]any, key string) error {
	raw, present := payload[key]
	if !present || raw == nil {
		return nil
	}

	coerceEntry := func(i int, el any) (blobURI, mimeType, role string, err error) {
		var m map[string]any
		switch e := el.(type) {
		case map[string]any:
			m = e
		case map[string]string:
			m = make(map[string]any, len(e))
			for k, v := range e {
				m[k] = v
			}
		default:
			return "", "", "", fmt.Errorf("field %s[%d]: expected map element, got %T", key, i, el)
		}
		read := func(k string) (string, error) {
			v, present := m[k]
			if !present || v == nil {
				return "", nil
			}
			s, ok := v.(string)
			if !ok {
				return "", fmt.Errorf("field %s[%d].%s: expected string, got %T", key, i, k, v)
			}
			return s, nil
		}
		if blobURI, err = read("blob_uri"); err != nil {
			return
		}
		if mimeType, err = read("mime_type"); err != nil {
			return
		}
		role, err = read("role")
		return
	}

	var elements []any
	switch s := raw.(type) {
	case []any:
		elements = s
	case []map[string]any:
		elements = make([]any, len(s))
		for i, m := range s {
			elements[i] = m
		}
	case []map[string]string:
		elements = make([]any, len(s))
		for i, m := range s {
			elements[i] = m
		}
	default:
		return fmt.Errorf("field %s: expected slice of maps, got %T", key, raw)
	}

	for i, el := range elements {
		blobURI, mimeType, role, err := coerceEntry(i, el)
		if err != nil {
			return err
		}
		entry := make([]byte, 0, 16+len(blobURI)+len(mimeType)+len(role))
		if blobURI != "" {
			entry = appendString(entry, 1, blobURI)
		}
		if mimeType != "" {
			entry = appendString(entry, 2, mimeType)
		}
		if role != "" {
			entry = appendString(entry, 3, role)
		}
		*out = appendLengthDelimited(*out, field, entry)
	}
	return nil
}

// -----------------------------------------------------------------------------
// QuestionBatchAccepted (chora.creation.question_batch.accepted.v1)
// Schema — chora-contracts/proto/events-flat/creation/question_batch/accepted.proto
//
//	1  Envelope envelope
//	2  string   job_id
//	3  string   host_atom_id
//	4  string   tenant_id
//	5  string   author_gcid
//	6  TestSetSpec test_set        {1: title, 2: description}
//	7  repeated Item items         {1: question_atom_id, 2: question_id,
//	                                3: question_type, 4: points (int32),
//	                                5: display_order (int32)}
//	8  repeated SourceFile source_files {1: blob_uri, 2: mime_type, 3: role}
//	9  Timestamp accepted_at
//
// Lane 1c (CHO-1703 / ADR-180 D10). BINARY-encoded per the in_app.created
// dead-letter lesson — an unhandled binary topic falls through to JSON and
// silently DLQs at the Schema Registry.
// -----------------------------------------------------------------------------
func encodeQuestionBatchAccepted(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 512)

	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)

	if payload == nil {
		return out, nil
	}

	stringField := func(field protowire.Number, key string) error {
		v, ok, err := stringPayloadField(payload, key)
		if err != nil {
			return err
		}
		if ok && v != "" {
			out = appendString(out, field, v)
		}
		return nil
	}

	for _, step := range []func() error{
		func() error { return stringField(2, "job_id") },
		func() error { return stringField(3, "host_atom_id") },
		func() error { return stringField(4, "tenant_id") },
		func() error { return stringField(5, "author_gcid") },
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}

	// Field 6: TestSetSpec {1: title, 2: description}.
	if rawTS, present := payload["test_set"]; present && rawTS != nil {
		tsMap, ok := rawTS.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("field test_set: expected map, got %T", rawTS)
		}
		entry := make([]byte, 0, 64)
		if title, ok := tsMap["title"].(string); ok && title != "" {
			entry = appendString(entry, 1, title)
		}
		if desc, ok := tsMap["description"].(string); ok && desc != "" {
			entry = appendString(entry, 2, desc)
		}
		out = appendLengthDelimited(out, 6, entry)
	}

	// Field 7: repeated Item.
	items, err := coerceMapSlice(payload, "items")
	if err != nil {
		return nil, err
	}
	for i, item := range items {
		entry := make([]byte, 0, 96)
		readStr := func(field protowire.Number, key string) error {
			v, present := item[key]
			if !present || v == nil {
				return nil
			}
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("field items[%d].%s: expected string, got %T", i, key, v)
			}
			if s != "" {
				entry = appendString(entry, field, s)
			}
			return nil
		}
		readInt := func(field protowire.Number, key string) error {
			v, present := item[key]
			if !present || v == nil {
				return nil
			}
			n, ok := asInt32(v)
			if !ok {
				return fmt.Errorf("field items[%d].%s: expected int32-convertible, got %T", i, key, v)
			}
			if n != 0 {
				entry = appendVarint(entry, field, uint64(uint32(n)))
			}
			return nil
		}
		for _, step := range []func() error{
			func() error { return readStr(1, "question_atom_id") },
			func() error { return readStr(2, "question_id") },
			func() error { return readStr(3, "question_type") },
			func() error { return readInt(4, "points") },
			func() error { return readInt(5, "display_order") },
		} {
			if err := step(); err != nil {
				return nil, err
			}
		}
		out = appendLengthDelimited(out, 7, entry)
	}

	// Field 8: repeated SourceFile — same nested shape as the ai_assist f20
	// SourceFileRef (blob_uri / mime_type / role), reuse the helper.
	if err := appendSourceFileRefs(&out, 8, payload, "source_files"); err != nil {
		return nil, err
	}

	// Field 9: Timestamp accepted_at.
	if t, ok, err := timestampPayloadField(payload, "accepted_at"); err != nil {
		return nil, err
	} else if ok {
		out = appendLengthDelimited(out, 9, encodeTimestamp(t))
	}

	return out, nil
}

// coerceMapSlice reads payload[key] as a slice of maps ([]map[string]any or
// []any of maps). Absent/nil → nil. Fails loud on other shapes.
func coerceMapSlice(payload map[string]any, key string) ([]map[string]any, error) {
	raw, present := payload[key]
	if !present || raw == nil {
		return nil, nil
	}
	switch s := raw.(type) {
	case []map[string]any:
		return s, nil
	case []any:
		out := make([]map[string]any, 0, len(s))
		for i, el := range s {
			m, ok := el.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("field %s[%d]: expected map element, got %T", key, i, el)
			}
			out = append(out, m)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("field %s: expected slice of maps, got %T", key, raw)
	}
}

// -----------------------------------------------------------------------------
// Envelope (nested in every event message; field layout matches
// chora.common.v1.EventEnvelope as flattened by chora-contracts/internal/
// protoflatten and embedded as a NESTED type in every events-flat schema).
// -----------------------------------------------------------------------------
//
//	1  string event_id
//	2  string idempotency_key
//	3  string tenant_id
//	4  string gcid
//	5  bytes  Timestamp occurred_at
//	6  bytes  Timestamp published_at
//	7  string traceparent
//	8  string tracestate
//	9  string source_project
//	10 string source_service
//	11 varint int32 schema_version
//	12 string correlation_id
//	13 string causation_id
//	14 string chora_imda_dimension
//	15 string imda_lifecycle_stage
func encodeEnvelope(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)

	if env.EventID != "" {
		out = appendString(out, 1, env.EventID)
	}
	if env.IdempotencyKey != "" {
		out = appendString(out, 2, env.IdempotencyKey)
	}
	if env.TenantID != "" {
		out = appendString(out, 3, env.TenantID)
	}
	if env.GCID != "" {
		out = appendString(out, 4, env.GCID)
	}
	if !env.OccurredAt.IsZero() {
		out = appendLengthDelimited(out, 5, encodeTimestamp(env.OccurredAt))
	}
	if !env.PublishedAt.IsZero() {
		out = appendLengthDelimited(out, 6, encodeTimestamp(env.PublishedAt))
	}
	if env.Traceparent != "" {
		out = appendString(out, 7, env.Traceparent)
	}
	if env.Tracestate != "" {
		out = appendString(out, 8, env.Tracestate)
	}
	if env.SourceProject != "" {
		out = appendString(out, 9, env.SourceProject)
	}
	if env.SourceService != "" {
		out = appendString(out, 10, env.SourceService)
	}
	if env.SchemaVersion > 0 {
		out = appendVarint(out, 11, uint64(uint32(env.SchemaVersion)))
	}

	// Optional IMDA evidence fields sourced from the payload. Several
	// chora-creation handlers attach them on the loose-map payload (the
	// outbox row's envelope JSONB carries them separately too).
	if payload != nil {
		if v, ok := payload["chora_imda_dimension"].(string); ok && v != "" {
			out = appendString(out, 14, v)
		}
		if v, ok := payload["imda_lifecycle_stage"].(string); ok && v != "" {
			out = appendString(out, 15, v)
		}
	}

	return out, nil
}

// encodeTimestamp emits the nested google.protobuf.Timestamp wire shape:
//
//	1 varint int64  seconds
//	2 varint int32  nanos
func encodeTimestamp(t time.Time) []byte {
	out := make([]byte, 0, 16)
	t = t.UTC()
	secs := t.Unix()
	nanos := int32(t.Nanosecond())
	if secs != 0 {
		out = appendVarint(out, 1, uint64(secs))
	}
	if nanos != 0 {
		out = appendVarint(out, 2, uint64(uint32(nanos)))
	}
	return out
}

// -----------------------------------------------------------------------------
// Wire-format helpers (thin protowire wrappers).
// -----------------------------------------------------------------------------

func appendString(b []byte, field protowire.Number, v string) []byte {
	b = protowire.AppendTag(b, field, protowire.BytesType)
	b = protowire.AppendString(b, v)
	return b
}

func appendVarint(b []byte, field protowire.Number, v uint64) []byte {
	b = protowire.AppendTag(b, field, protowire.VarintType)
	b = protowire.AppendVarint(b, v)
	return b
}

func appendLengthDelimited(b []byte, field protowire.Number, payload []byte) []byte {
	b = protowire.AppendTag(b, field, protowire.BytesType)
	b = protowire.AppendBytes(b, payload)
	return b
}

// appendStringMapField writes a proto3 map<string,string> field as repeated
// length-delimited MapEntry submessages, one per (key,value) pair, with the
// supplied field number. Each MapEntry is the canonical proto map shape:
//
//	1  string key
//	2  string value
//
// Keys are sorted for deterministic encoding (helps Schema Registry
// validation + makes wire bytes diff-stable in tests).
//
// Accepts payload[key] as either map[string]string OR map[string]any whose
// values are strings (the latter shape arrives when payload was JSON-decoded
// upstream — e.g. the bridge's typed-struct round-trip).
func appendStringMapField(out *[]byte, field protowire.Number, payload map[string]any, key string) error {
	raw, present := payload[key]
	if !present || raw == nil {
		return nil
	}

	var entries map[string]string
	switch m := raw.(type) {
	case map[string]string:
		entries = m
	case map[string]any:
		entries = make(map[string]string, len(m))
		for k, v := range m {
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("field %s[%q]: expected string value, got %T", key, k, v)
			}
			entries[k] = s
		}
	default:
		return fmt.Errorf("field %s: expected map[string]string or map[string]any, got %T", key, raw)
	}

	if len(entries) == 0 {
		return nil
	}

	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		entry := make([]byte, 0, 32+len(k)+len(entries[k]))
		entry = appendString(entry, 1, k)
		entry = appendString(entry, 2, entries[k])
		*out = appendLengthDelimited(*out, field, entry)
	}
	return nil
}

// -----------------------------------------------------------------------------
// Loose-typed payload coercion (in: map[string]any).
//
// The Go publishers + JobEventPublisher emit payloads as map[string]any with
// values sourced from either typed structs (string / int / time.Time) or
// already-formatted JSON-decoded values. The coercion helpers absorb the
// flux + fail loud when a field carries the wrong shape for its proto slot.
// -----------------------------------------------------------------------------

// stringPayloadField reads payload[key]. Returns (value, present, err).
// present=false when the key is absent or value nil. err non-nil when present
// but wrong type.
func stringPayloadField(payload map[string]any, key string) (string, bool, error) {
	raw, present := payload[key]
	if !present || raw == nil {
		return "", false, nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", true, fmt.Errorf("field %s: expected string, got %T", key, raw)
	}
	return s, true, nil
}

// int32PayloadField reads payload[key] as int32. Returns (value, present, err).
func int32PayloadField(payload map[string]any, key string) (int32, bool, error) {
	raw, present := payload[key]
	if !present || raw == nil {
		return 0, false, nil
	}
	v, ok := asInt32(raw)
	if !ok {
		return 0, true, fmt.Errorf("field %s: expected int32-convertible, got %T", key, raw)
	}
	return v, true, nil
}

// boolPayloadField reads payload[key] as a bool. Returns (value, present, err).
// present=false when the key is absent or value nil. err non-nil when present
// but the value is not a bool — the proto slot is bool, so coercing a string /
// int there would corrupt the wire format (fail loud per
// [[feedback-no-stubs-real-wiring]]).
func boolPayloadField(payload map[string]any, key string) (bool, bool, error) {
	raw, present := payload[key]
	if !present || raw == nil {
		return false, false, nil
	}
	b, ok := raw.(bool)
	if !ok {
		return false, true, fmt.Errorf("field %s: expected bool, got %T", key, raw)
	}
	return b, true, nil
}

// timestampPayloadField reads payload[key] as time.Time. Accepts time.Time,
// *time.Time, RFC3339Nano string, or RFC3339 string.
func timestampPayloadField(payload map[string]any, key string) (time.Time, bool, error) {
	raw, present := payload[key]
	if !present || raw == nil {
		return time.Time{}, false, nil
	}
	t, ok := asTime(raw)
	if !ok {
		return time.Time{}, true, fmt.Errorf("field %s: expected time.Time / RFC3339 string, got %T", key, raw)
	}
	return t, true, nil
}

// enumPayloadField reads payload[key] as an enum value. Accepts int / int32
// numeric values directly OR a string name to be resolved via the supplied
// lookup func. Returns ErrUnsupportedTopic-like behaviour: unknown string
// names raise an error so producers know the schema rejected their value.
func enumPayloadField(payload map[string]any, key string, lookup func(string) (int32, bool)) (int32, bool, error) {
	raw, present := payload[key]
	if !present || raw == nil {
		return 0, false, nil
	}
	if v, ok := asInt32(raw); ok {
		return v, true, nil
	}
	if s, ok := raw.(string); ok {
		if v, ok := lookup(s); ok {
			return v, true, nil
		}
		return 0, true, fmt.Errorf("field %s: unknown enum name %q", key, s)
	}
	return 0, true, fmt.Errorf("field %s: expected int32 or enum-name string, got %T", key, raw)
}

// asInt32 converts the value to int32. Returns false (not 0) if v is nil OR
// is a non-numeric type — caller decides whether to fail or skip.
func asInt32(v any) (int32, bool) {
	if v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return int32(n), true
	case int32:
		return n, true
	case int64:
		return int32(n), true
	case float32:
		return int32(n), true
	case float64:
		return int32(n), true
	case uint:
		return int32(n), true
	case uint32:
		return int32(n), true
	case uint64:
		return int32(n), true
	default:
		return 0, false
	}
}

// asInt64 converts the value to int64 without the 32-bit truncation asInt32
// applies. Returns false if v is nil OR is a non-numeric type.
func asInt64(v any) (int64, bool) {
	if v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case float32:
		return int64(n), true
	case float64:
		return int64(n), true
	case uint:
		if uint64(n) > math.MaxInt64 {
			return 0, false
		}
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		if n > math.MaxInt64 {
			return 0, false
		}
		return int64(n), true
	default:
		return 0, false
	}
}

// asTime coerces a value to time.Time. Accepts time.Time, *time.Time, +
// RFC3339Nano / RFC3339 strings (common when payloads flow from JSON-decoded
// envelopes such as the question_jobs_handler emissions).
func asTime(v any) (time.Time, bool) {
	if v == nil {
		return time.Time{}, false
	}
	switch t := v.(type) {
	case time.Time:
		return t, true
	case *time.Time:
		if t == nil {
			return time.Time{}, false
		}
		return *t, true
	case string:
		if t == "" {
			return time.Time{}, false
		}
		if parsed, err := time.Parse(time.RFC3339Nano, t); err == nil {
			return parsed, true
		}
		if parsed, err := time.Parse(time.RFC3339, t); err == nil {
			return parsed, true
		}
		return time.Time{}, false
	default:
		return time.Time{}, false
	}
}

// stringSlice coerces []string or []any-of-string to a string slice.
func stringSlice(v any) ([]string, bool) {
	if v == nil {
		return nil, false
	}
	switch s := v.(type) {
	case []string:
		return s, true
	case []any:
		out := make([]string, 0, len(s))
		for _, e := range s {
			str, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, str)
		}
		return out, true
	default:
		return nil, false
	}
}

// -----------------------------------------------------------------------------
// Enum lookups — string name → wire integer. The schemas use snake_case
// short names in domain code (e.g. "mcq", "ai_assist") + the full enum
// name (e.g. "QUESTION_TYPE_MCQ") in some call sites. We accept both forms.
// -----------------------------------------------------------------------------

// atomTypeWire resolves an AtomType label to its wire int. It is DERIVED from
// the generated descriptor, not hand-listed (CHO-2178).
//
// The old hand-written switch knew ten names and was missing three of the five
// flavours chora-creation actually authors. `outline`, `flashcard` and `video`
// each fell through to `return 0, false`, the outbox marshal errored, and the
// atom never emitted atom.published.v1 — so no other domain ever learned it
// existed. Four `outline` atoms sat unreachable in prod while the publish
// endpoint answered 200 OK. A table that must be edited in lockstep with a
// contract is a table that will fall out of lockstep with a contract.
//
// Deriving from creationv1.AtomType_name means a value added to the contract is
// accepted here with NO code change, and the naming law (see atom.proto) lets
// chora-sharing / chora-consumption derive the label back symmetrically.
var atomTypeWire = buildAtomTypeWire()

func buildAtomTypeWire() map[string]int32 {
	m := make(map[string]int32, len(creationv1.AtomType_name)*2+3)
	for value, name := range creationv1.AtomType_name {
		m[name] = value // full constant, e.g. "ATOM_TYPE_ESSAY"
		// Domain label per the naming law: lower(trim("ATOM_TYPE_")).
		m[strings.ToLower(strings.TrimPrefix(name, "ATOM_TYPE_"))] = value
	}
	// The three labels the naming law cannot produce.
	m["mcq"] = int32(creationv1.AtomType_ATOM_TYPE_MULTIPLE_CHOICE) // the one exception
	m["oe"] = int32(creationv1.AtomType_ATOM_TYPE_ESSAY)            // legacy alias; still one live row
	m[""] = int32(creationv1.AtomType_ATOM_TYPE_UNSPECIFIED)        // absent == unset, not an error
	return m
}

// lookupAtomType resolves an AtomType enum string to its wire int. An unknown
// label is a HARD failure: it means the producer holds a flavour the contract
// cannot carry, and encoding it as UNSPECIFIED would strand the atom silently.
func lookupAtomType(s string) (int32, bool) {
	v, ok := atomTypeWire[s]
	return v, ok
}

// imdaDimensionTagWire resolves an IMDA dimension label to its wire int,
// DERIVED from the generated descriptor rather than hand-listed (the CHO-2178
// lesson: a table maintained in lockstep with a contract falls out of lockstep
// with that contract).
//
// The naming law lower(trim("IMDA_DIMENSION_TAG_")) reproduces all four domain
// labels exactly (accountability / transparency / safety_robustness /
// fairness_human_oversight), so no hand-written alias is needed. UNSPECIFIED is
// deliberately absent: a repeated field has no use for it, and the aggregate
// rejects an invalid label long before the encoder sees one.
var imdaDimensionTagWire = buildImdaDimensionTagWire()

func buildImdaDimensionTagWire() map[string]int32 {
	m := make(map[string]int32, len(creationv1.ImdaDimensionTag_name)*2)
	for value, name := range creationv1.ImdaDimensionTag_name {
		if value == int32(creationv1.ImdaDimensionTag_IMDA_DIMENSION_TAG_UNSPECIFIED) {
			continue
		}
		m[name] = value // full constant, e.g. "IMDA_DIMENSION_TAG_TRANSPARENCY"
		m[strings.ToLower(strings.TrimPrefix(name, "IMDA_DIMENSION_TAG_"))] = value
	}
	return m
}

// lookupImdaDimensionTag resolves an ADR-141 IMDA label to its wire int. An
// unknown label is a HARD failure: a governance tag silently blanked to
// UNSPECIFIED is precisely the evidence gap the IMDA dimensions exist to close.
func lookupImdaDimensionTag(s string) (int32, bool) {
	v, ok := imdaDimensionTagWire[s]
	return v, ok
}

// atomMediaAssetTypeWire resolves a media asset kind to its wire int, derived
// from the generated descriptor. The naming law
// lower(trim("ATOM_MEDIA_ASSET_TYPE_")) yields "image", the only kind Phase 1
// supports (ADR-156 Decision #5); a Phase 4 audio/video value added to the
// contract is accepted here with no code change.
var atomMediaAssetTypeWire = buildAtomMediaAssetTypeWire()

func buildAtomMediaAssetTypeWire() map[string]int32 {
	m := make(map[string]int32, len(creationv1.AtomMediaAssetType_name)*2)
	for value, name := range creationv1.AtomMediaAssetType_name {
		m[name] = value
		m[strings.ToLower(strings.TrimPrefix(name, "ATOM_MEDIA_ASSET_TYPE_"))] = value
	}
	return m
}

// lookupAtomMediaAssetType resolves a media asset kind label to its wire int.
func lookupAtomMediaAssetType(s string) (int32, bool) {
	v, ok := atomMediaAssetTypeWire[s]
	return v, ok
}

// lookupAtomStatus resolves an AtomStatus enum string.
//
//	ATOM_STATUS_UNSPECIFIED = 0
//	ATOM_STATUS_DRAFT       = 1
//	ATOM_STATUS_PUBLISHED   = 2
//	ATOM_STATUS_ARCHIVED    = 3
func lookupAtomStatus(s string) (int32, bool) {
	switch s {
	case "ATOM_STATUS_UNSPECIFIED", "unspecified", "":
		return 0, true
	case "ATOM_STATUS_DRAFT", "draft":
		return 1, true
	case "ATOM_STATUS_PUBLISHED", "published":
		return 2, true
	case "ATOM_STATUS_ARCHIVED", "archived":
		return 3, true
	}
	return 0, false
}

// lookupCognitiveLevel maps the ORIGINAL-Bloom label (or the proto enum name)
// onto the CognitiveLevel PROTO number. NB: the proto numbering has
// synthesis=5 / evaluation=6 — deliberately different from the campaign
// ladder ORDER (evaluation before synthesis), which is why levels travel as
// labels everywhere outside wire encoding. Unknown labels fail loud.
func lookupCognitiveLevel(s string) (int32, bool) {
	switch s {
	case "COGNITIVE_LEVEL_UNSPECIFIED", "unspecified", "":
		return 0, true
	case "COGNITIVE_LEVEL_KNOWLEDGE", "knowledge":
		return 1, true
	case "COGNITIVE_LEVEL_COMPREHENSION", "comprehension":
		return 2, true
	case "COGNITIVE_LEVEL_APPLICATION", "application":
		return 3, true
	case "COGNITIVE_LEVEL_ANALYSIS", "analysis":
		return 4, true
	case "COGNITIVE_LEVEL_SYNTHESIS", "synthesis":
		return 5, true
	case "COGNITIVE_LEVEL_EVALUATION", "evaluation":
		return 6, true
	}
	return 0, false
}

// lookupReuseVisibility maps the ADR-229 audience label (or proto enum name)
// onto the ReuseVisibility PROTO number (chora-contracts atom.proto):
//
//	REUSE_VISIBILITY_UNSPECIFIED = 0
//	REUSE_VISIBILITY_PRIVATE     = 1
//	REUSE_VISIBILITY_FRIENDS     = 2
//	REUSE_VISIBILITY_TENANT      = 3
//
// Unknown labels fail loud (via enumPayloadField) — a silently-UNSPECIFIED
// audience would harden to private downstream and mask the producer bug.
func lookupReuseVisibility(s string) (int32, bool) {
	switch s {
	case "REUSE_VISIBILITY_UNSPECIFIED", "unspecified", "":
		return 0, true
	case "REUSE_VISIBILITY_PRIVATE", "private":
		return 1, true
	case "REUSE_VISIBILITY_FRIENDS", "friends":
		return 2, true
	case "REUSE_VISIBILITY_TENANT", "tenant":
		return 3, true
	}
	return 0, false
}

// lookupQuestionType resolves a QuestionType enum string.
//
//	QUESTION_TYPE_UNSPECIFIED = 0
//	QUESTION_TYPE_MCQ         = 1
//	QUESTION_TYPE_OE          = 2
//	(others reserved 3..16)
func lookupQuestionType(s string) (int32, bool) {
	switch s {
	case "QUESTION_TYPE_UNSPECIFIED", "unspecified", "":
		return 0, true
	case "QUESTION_TYPE_MCQ", "mcq":
		return 1, true
	case "QUESTION_TYPE_OE", "oe":
		return 2, true
	case "QUESTION_TYPE_RESERVED_CODE_EXECUTION", "code_execution":
		return 3, true
	case "QUESTION_TYPE_RESERVED_COMPLETION", "completion":
		return 4, true
	case "QUESTION_TYPE_RESERVED_DRAG_DROP", "drag_drop":
		return 5, true
	case "QUESTION_TYPE_RESERVED_FILL_BLANK", "fill_blank":
		return 6, true
	case "QUESTION_TYPE_RESERVED_MATCHING", "matching":
		return 7, true
	case "QUESTION_TYPE_RESERVED_MULTI_SELECT", "multi_select":
		return 8, true
	case "QUESTION_TYPE_RESERVED_MULTIMEDIA", "multimedia":
		return 9, true
	case "QUESTION_TYPE_RESERVED_ORAL", "oral":
		return 10, true
	case "QUESTION_TYPE_RESERVED_ORDERING", "ordering":
		return 11, true
	case "QUESTION_TYPE_RESERVED_PEER_GRADED", "peer_graded":
		return 12, true
	case "QUESTION_TYPE_RESERVED_SHORT_ANSWER", "short_answer":
		return 13, true
	case "QUESTION_TYPE_RESERVED_SIMULATION", "simulation":
		return 14, true
	case "QUESTION_TYPE_RESERVED_TABLE_COMPLETION", "table_completion":
		return 15, true
	case "QUESTION_TYPE_RESERVED_TRUE_FALSE", "true_false":
		return 16, true
	}
	return 0, false
}

// lookupQuestionSourceType resolves a QuestionSourceType enum string.
//
//	QUESTION_SOURCE_TYPE_UNSPECIFIED          = 0
//	QUESTION_SOURCE_TYPE_MANUAL               = 1
//	QUESTION_SOURCE_TYPE_AI_ASSIST            = 2
//	QUESTION_SOURCE_TYPE_COMMUNITY_ATOM_BANK  = 3
func lookupQuestionSourceType(s string) (int32, bool) {
	switch s {
	case "QUESTION_SOURCE_TYPE_UNSPECIFIED", "unspecified", "":
		return 0, true
	case "QUESTION_SOURCE_TYPE_MANUAL", "manual":
		return 1, true
	case "QUESTION_SOURCE_TYPE_AI_ASSIST", "ai_assist":
		return 2, true
	case "QUESTION_SOURCE_TYPE_COMMUNITY_ATOM_BANK", "community_atom_bank":
		return 3, true
	}
	return 0, false
}

// lookupQuestionGenerationJobStatus resolves a QuestionGenerationJobStatus
// enum string.
//
//	QUESTION_GENERATION_JOB_STATUS_UNSPECIFIED          = 0
//	QUESTION_GENERATION_JOB_STATUS_REQUESTED            = 1
//	QUESTION_GENERATION_JOB_STATUS_RUNNING              = 2
//	QUESTION_GENERATION_JOB_STATUS_SUCCEEDED            = 3
//	QUESTION_GENERATION_JOB_STATUS_FAILED               = 4
//	QUESTION_GENERATION_JOB_STATUS_ACCEPTED             = 5
//	QUESTION_GENERATION_JOB_STATUS_PARTIALLY_ACCEPTED   = 6
//	QUESTION_GENERATION_JOB_STATUS_CANCELLED            = 7
func lookupQuestionGenerationJobStatus(s string) (int32, bool) {
	switch s {
	case "QUESTION_GENERATION_JOB_STATUS_UNSPECIFIED", "unspecified", "":
		return 0, true
	case "QUESTION_GENERATION_JOB_STATUS_REQUESTED", "requested":
		return 1, true
	case "QUESTION_GENERATION_JOB_STATUS_RUNNING", "running":
		return 2, true
	case "QUESTION_GENERATION_JOB_STATUS_SUCCEEDED", "succeeded":
		return 3, true
	case "QUESTION_GENERATION_JOB_STATUS_FAILED", "failed":
		return 4, true
	case "QUESTION_GENERATION_JOB_STATUS_ACCEPTED", "accepted":
		return 5, true
	case "QUESTION_GENERATION_JOB_STATUS_PARTIALLY_ACCEPTED", "partially_accepted":
		return 6, true
	case "QUESTION_GENERATION_JOB_STATUS_CANCELLED", "cancelled":
		return 7, true
	}
	return 0, false
}
