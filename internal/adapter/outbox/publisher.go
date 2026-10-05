// Package outbox — OutboxPublisher implementation.
//
// OutboxPublisher satisfies `atom.EventPublisher` by writing the atom
// event to the outbox_events table instead of publishing directly to
// Pub/Sub. The Dispatcher (see dispatcher.go) drains the table to Cloud
// Pub/Sub on a separate goroutine. This decouples event emission from
// Pub/Sub availability — a crash between the domain state-change and
// Pub/Sub publish no longer loses events.
//
// The wire shape of the payload matches the existing
// `internal/adapter/events.CloudPublisher` payload (JSON serialisation of
// the proto contract) so subscribers see identical bytes whether they
// receive from the legacy direct-publish path or the new outbox path.
// Migration is a constructor swap in main().
//
// Per `feedback_d6_resilience_first_class` B.6.2.a producer-side durable
// emission for chora-creation's `chora.creation.atom.*.v1` streams.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-creation/internal/adapter/events/protomarshal"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// schemaVersion is the default major version of the on-wire payload schema.
// Matches the v{N} suffix in the canonical topic names. Producers may
// override via atom.Event.SchemaVersion.
const defaultSchemaVersion = int32(1)

// PublisherConfig wires the OutboxPublisher.
type PublisherConfig struct {
	// Store is the outbox table backend. Required. Typed as Inserter (the
	// write half) so the same publisher serves both a pool-scoped Store and
	// a TxStore bound to the caller's transaction.
	Store Inserter

	// SourceProject is the project the service runs in (e.g.
	// chora-local). Defaults to "chora-local".
	SourceProject string

	// SourceService is the publisher's service name. Defaults to
	// "chora-creation".
	SourceService string

	// Now is injectable for tests; defaults to time.Now().UTC.
	Now func() time.Time
}

// Publisher satisfies atom.EventPublisher by enqueueing the event into
// outbox_events.
type Publisher struct {
	cfg PublisherConfig
}

// NewPublisher constructs an OutboxPublisher.
func NewPublisher(cfg PublisherConfig) *Publisher {
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.SourceProject == "" {
		cfg.SourceProject = "chora-local"
	}
	if cfg.SourceService == "" {
		cfg.SourceService = "chora-creation"
	}
	return &Publisher{cfg: cfg}
}

// payload mirrors `internal/adapter/events.CloudPublisher` payload shape for
// wire compatibility with existing subscribers.
type payload struct {
	EventID        string `json:"event_id"`
	Type           string `json:"type"`
	TenantID       string `json:"tenant_id"`
	GCID           string `json:"gcid,omitempty"`
	AtomID         string `json:"atom_id"`
	CourseID       string `json:"course_id,omitempty"`
	RevisionID     string `json:"revision_id,omitempty"`
	RevisionNumber int    `json:"revision_number,omitempty"`
	Title          string `json:"title,omitempty"`
	AtomType       string `json:"atom_type,omitempty"`
	Difficulty     int    `json:"difficulty,omitempty"`
	SourceType     string `json:"source_type,omitempty"`
	// MCQ grading ground-truth (CHO-1627). Empty/0 for non-MCQ atoms.
	CorrectOptionID string `json:"correct_option_id,omitempty"`
	AnswerCount     int    `json:"answer_count,omitempty"`
	// HasOpenEndedQuestion — answerability discriminator on atom.published.v1
	// (field 28). True for an OE atom (answerable without an MCQ key); false /
	// elided otherwise.
	HasOpenEndedQuestion bool `json:"has_open_ended_question,omitempty"`
	// CognitiveLevel — ORIGINAL-Bloom label on atom.published.v1 (field 22)
	// for the consumption campaign question lane (WS-C3). Elided when "".
	CognitiveLevel string `json:"cognitive_level,omitempty"`
	// Tags — the atom's topic axis on created/published. The encoder reads
	// payload["tags"] into wire field 7 (topic_node_ids) when no explicit
	// topic_node_ids is present, and chora-consumption projects field 7 into
	// atom_index.topic_tags (CHO-2142). Elided when empty.
	Tags []string `json:"tags,omitempty"`
	// ReuseVisibility — ADR-229 audience label on created/published (field 30)
	// + the NEW value on reuse_visibility_changed.v1 (field 3). Elided when "".
	ReuseVisibility string `json:"reuse_visibility,omitempty"`
	// AuthorDisplayName — denormalised on atom.published.v1 (field 31) so
	// downstream consumers render the author without a cross-DB identity
	// lookup. Elided when "".
	AuthorDisplayName string `json:"author_display_name,omitempty"`
	// Stem — denormalised on atom.published.v1 (field 20) so downstream
	// consumers render the atom preview without a cross-DB read.
	Stem string `json:"stem,omitempty"`
	// PreviousReuseVisibility — the audience BEFORE a change; carried only on
	// reuse_visibility_changed.v1 (field 4).
	PreviousReuseVisibility string `json:"previous_visibility,omitempty"`
	// OrphanedFromAtomID + Trigger — carried only on atom.orphan_created.v1
	// (ADR-229 A1, CHO-2132). On that topic AtomID is the NEW orphan edition
	// and RevisionID the pinned source revision.
	OrphanedFromAtomID string `json:"orphaned_from_atom_id,omitempty"`
	Trigger            string `json:"trigger,omitempty"`
	// atom.updated.v1 metadata snapshot (ADR-244 D5). ChangedFields (field 10)
	// is what the chora-consumption KG-invalidation consumer branches on; the
	// rest is the POST-patch value of each metadata slot the PATCH can move.
	ChangedFields     []string          `json:"changed_fields,omitempty"`
	Status            string            `json:"status,omitempty"`
	Subject           string            `json:"subject,omitempty"`
	AuthorNote        string            `json:"author_note,omitempty"`
	ImdaDimensionTags []string          `json:"imda_dimension_tags,omitempty"`
	MediaAssets       []atom.MediaAsset `json:"media_assets,omitempty"`
}

// Publish satisfies atom.EventPublisher. Writes the event as a pending row
// in outbox_events. The Dispatcher publishes to Pub/Sub asynchronously.
func (p *Publisher) Publish(ctx context.Context, e atom.Event) error {
	if p.cfg.Store == nil {
		return fmt.Errorf("outbox: store not wired")
	}

	topic := string(e.Type)
	if err := validateCanonicalTopic(topic); err != nil {
		return err
	}

	now := p.cfg.Now()

	// Mint an event_id (UUIDv7) for the envelope if the caller didn't.
	eventID := e.EventID
	if eventID == "" {
		eventID = newUUIDv7()
	}
	idemKey := e.IdempotencyKey
	if idemKey == "" {
		idemKey = eventID
	}

	occurred, _ := parseRFC3339(e.OccurredAt, now)
	publishedAt, _ := parseRFC3339(e.PublishedAt, now)

	sourceProject := e.SourceProject
	if sourceProject == "" {
		sourceProject = p.cfg.SourceProject
	}
	sourceService := e.SourceService
	if sourceService == "" {
		sourceService = p.cfg.SourceService
	}
	schemaVersion := e.SchemaVersion
	if schemaVersion < 1 {
		schemaVersion = defaultSchemaVersion
	}

	// Build the envelope as a flat string map for the JSONB column. This
	// is the on-wire attribute set published to Pub/Sub (subscribers can
	// filter without parsing the payload).
	envelope := map[string]string{
		"event_id":             eventID,
		"idempotency_key":      idemKey,
		"tenant_id":            e.TenantID,
		"gcid":                 e.Gcid,
		"occurred_at":          occurred.Format(time.RFC3339Nano),
		"published_at":         publishedAt.Format(time.RFC3339Nano),
		"traceparent":          e.TraceParent,
		"tracestate":           e.TraceState,
		"source_project":       sourceProject,
		"source_service":       sourceService,
		"schema_version":       strconv.Itoa(int(schemaVersion)),
		"chora_imda_dimension": e.ChoraImdaDimension,
		"imda_lifecycle_stage": e.ImdaLifecycleStage,
	}

	body := payload{
		EventID:                 eventID,
		Type:                    string(e.Type),
		TenantID:                e.TenantID,
		GCID:                    e.Gcid,
		AtomID:                  e.AtomID,
		CourseID:                e.CourseID,
		RevisionID:              e.RevisionID,
		RevisionNumber:          e.RevisionNumber,
		Title:                   e.Title,
		AtomType:                string(e.AtomType),
		Difficulty:              e.Difficulty,
		SourceType:              string(e.SourceType),
		CorrectOptionID:         e.CorrectOptionID,
		AnswerCount:             e.AnswerCount,
		HasOpenEndedQuestion:    e.HasOpenEndedQuestion,
		CognitiveLevel:          e.CognitiveLevel,
		Tags:                    e.Tags, // CHO-2142: field 7 topic_node_ids
		ReuseVisibility:         e.ReuseVisibility,
		AuthorDisplayName:       e.AuthorDisplayName,
		Stem:                    e.Stem,
		PreviousReuseVisibility: e.PreviousReuseVisibility,
		OrphanedFromAtomID:      e.OrphanedFromAtomID,
		Trigger:                 e.Trigger,
		ChangedFields:           e.ChangedFields, // ADR-244 D5: field 10 on atom.updated.v1
		Status:                  e.Status,
		Subject:                 e.Subject,
		AuthorNote:              e.AuthorNote,
		ImdaDimensionTags:       e.ImdaDimensionTags,
		MediaAssets:             e.MediaAssets,
	}

	// Producer-side encoding: emit canonical binary protobuf for topics
	// whose broker schema is BINARY-encoded. JSON
	// payloads on a schema-attached topic dead-letter forever with
	// "Invalid binary proto message". Per the gap surfaced in task #33
	// (outbox protobuf encoding fix, 2026-05-16). Unsupported topics fall
	// back to JSON + log a one-shot WARN — these rows WILL be rejected by
	// Schema Registry at publish, but the fallback preserves behaviour
	// for topics still pending a binary encoder (e.g. atom.revised.v1
	// where no schema is deployed).
	payloadBytes, err := encodeOutboxPayload(
		topic,
		body,
		protoEnvelope(e, eventID, idemKey, occurred, publishedAt, sourceProject, sourceService, schemaVersion),
		occurred,
	)
	if err != nil {
		return fmt.Errorf("outbox: marshal payload: %w", err)
	}

	aggregateID := e.AtomID
	if aggregateID == "" {
		aggregateID = eventID
	}

	row := Row{
		ID:             eventID,
		TenantID:       e.TenantID,
		GCID:           e.Gcid,
		AggregateType:  "atom",
		AggregateID:    aggregateID,
		EventType:      deriveEventType(topic),
		Topic:          topic,
		Payload:        payloadBytes,
		Envelope:       envelope,
		IdempotencyKey: idemKey,
		OccurredAt:     occurred.UTC(),
	}
	return p.cfg.Store.Insert(ctx, row)
}

// validateCanonicalTopic enforces `chora.creation.{aggregate}.{event_type}.v{N}`
// at the publisher boundary. Legacy topic strings (e.g. `chora.atomic.events`)
// MUST migrate to the canonical name before reaching this publisher; the
// outbox `topic` column therefore always holds canonical names.
func validateCanonicalTopic(topic string) error {
	t := strings.TrimSpace(topic)
	if t == "" {
		return errors.New("outbox: topic required")
	}
	parts := strings.Split(t, ".")
	if len(parts) < 5 {
		return fmt.Errorf("outbox: topic %q must follow chora.creation.{aggregate}.{event_type}.v{N}", topic)
	}
	if parts[0] != "chora" {
		return fmt.Errorf("outbox: topic %q must start with 'chora.'", topic)
	}
	if parts[1] != CreationDomain {
		return fmt.Errorf("outbox: topic domain segment = %q; want %q (legacy topics MUST migrate to canonical)", parts[1], CreationDomain)
	}
	last := parts[len(parts)-1]
	if !strings.HasPrefix(last, "v") || len(last) < 2 {
		return fmt.Errorf("outbox: topic %q must end with v{N} version suffix", topic)
	}
	for _, ch := range last[1:] {
		if ch < '0' || ch > '9' {
			return fmt.Errorf("outbox: topic %q version suffix must be numeric", topic)
		}
	}
	return nil
}

// deriveEventType extracts the `{aggregate}.{event_type}` part from a
// canonical topic (skips the `chora.creation.` prefix + the version suffix).
// Used as the indexable event_type column on the outbox row.
func deriveEventType(topic string) string {
	parts := strings.Split(topic, ".")
	if len(parts) < 5 {
		return topic
	}
	// chora.creation.{aggregate}.{event_type}.v{N} → creation.{aggregate}.{event_type}
	return strings.Join(parts[1:len(parts)-1], ".")
}

func parseRFC3339(s string, fallback time.Time) (time.Time, error) {
	if s == "" {
		return fallback.UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	return fallback.UTC(), fmt.Errorf("outbox: parse RFC3339 %q", s)
}

func newUUIDv7() string {
	return uuid.Must(uuid.NewV7()).String()
}

// Compile-time port assertion.
var _ atom.EventPublisher = (*Publisher)(nil)

// -----------------------------------------------------------------------------
// Payload encoding — binary protobuf for Schema-Registry-attached topics, JSON
// fallback for topics that don't yet have a binary encoder.
//
// JSON fallback exists to preserve behaviour for the small set of
// chora.creation.* topics that pre-date the protomarshal package
// (chora.creation.atom.revised.v1 has no deployed Pub/Sub topic yet).
// Each unknown topic logs a one-time WARN so its missing encoder is visible
// in production. New topics MUST add a case in protomarshal.MarshalPayload.
// -----------------------------------------------------------------------------

var (
	warnedUnknownTopicsMu sync.Mutex
	warnedUnknownTopics   = map[string]bool{}
)

// protoEnvelope projects the per-event flat envelope shape onto the encoder's
// type. Keeps protomarshal import-cycle-free.
func protoEnvelope(e atom.Event, eventID, idemKey string, occurred, published time.Time, sourceProject, sourceService string, schemaVersion int32) protomarshal.Envelope {
	return protomarshal.Envelope{
		EventID:        eventID,
		IdempotencyKey: idemKey,
		TenantID:       e.TenantID,
		GCID:           e.Gcid,
		OccurredAt:     occurred,
		PublishedAt:    published,
		Traceparent:    e.TraceParent,
		Tracestate:     e.TraceState,
		SourceProject:  sourceProject,
		SourceService:  sourceService,
		SchemaVersion:  schemaVersion,
	}
}

// encodeOutboxPayload encodes the event for the canonical Pub/Sub topic's
// Schema Registry binding. Falls through to JSON for topics that don't have
// a registered binary encoder (logs WARN once per topic so production
// surfaces the gap).
func encodeOutboxPayload(topic string, body payload, env protomarshal.Envelope, createdAt time.Time) ([]byte, error) {
	bz, err := protomarshal.MarshalPayload(topic, env, payloadAsMap(topic, body, createdAt))
	if err == nil {
		return bz, nil
	}
	if !protomarshal.IsUnsupportedTopic(err) {
		// Real encoding error (e.g. type mismatch) — fail loud.
		return nil, err
	}

	// Topic has no protobuf encoder yet. Log a one-shot WARN and fall back
	// to JSON. These rows WILL be rejected by Schema Registry on publish —
	// follow-on cleanup must add encoders for each.
	warnedUnknownTopicsMu.Lock()
	if !warnedUnknownTopics[topic] {
		warnedUnknownTopics[topic] = true
		log.Printf("WARN outbox: topic %q has no binary protobuf encoder — payload will JSON-marshal and Schema Registry will REJECT at publish; expect outbox_deadletter. Add a case to internal/adapter/events/protomarshal/MarshalPayload.", topic)
	}
	warnedUnknownTopicsMu.Unlock()

	bz, mErr := json.Marshal(body)
	if mErr != nil {
		return nil, fmt.Errorf("outbox: json fallback marshal: %w", mErr)
	}
	return bz, nil
}

// mediaAssetsAsMaps projects the typed media assets onto the loose
// []map[string]any shape the binary encoder coerces (keys match the
// AtomMediaAsset proto field names). Slice order is preserved.
func mediaAssetsAsMaps(assets []atom.MediaAsset) []map[string]any {
	out := make([]map[string]any, 0, len(assets))
	for _, a := range assets {
		out = append(out, map[string]any{
			"type":       a.Type,
			"url":        a.URL,
			"alt_text":   a.AltText,
			"mime":       a.MIME,
			"size_bytes": a.SizeBytes,
		})
	}
	return out
}

// payloadAsMap projects the typed atom.Event payload onto the loose
// map[string]any shape protomarshal expects. Mirrors the encoder schema
// field names from chora-contracts/proto/events-flat/creation/atom/{created,
// published}.proto. The topic selects the topic-specific field set:
// atom.published.v1 carries the publish-transition snapshot (status + the
// current-revision pointer at fields 10/11 + published_at at field 12 +
// answerability at field 28), whereas atom.created.v1 carries created_at at
// field 10.
func payloadAsMap(topic string, body payload, createdAt time.Time) map[string]any {
	// atom.orphan_created.v1 (ADR-229 A1, CHO-2132) — a purpose-built key set:
	// AtomID carries the NEW orphan edition (schema field 2), RevisionID the
	// pinned last-published source revision (field 4), GCID the ORIGINAL
	// author (field 5), and createdAt the mint instant (field 7).
	if topic == string(atom.EventTypeAtomOrphanCreated) {
		m := map[string]any{}
		if body.AtomID != "" {
			m["orphan_atom_id"] = body.AtomID
		}
		if body.OrphanedFromAtomID != "" {
			m["orphaned_from_atom_id"] = body.OrphanedFromAtomID
		}
		if body.RevisionID != "" {
			m["source_revision_id"] = body.RevisionID
		}
		if body.GCID != "" {
			m["author_gcid"] = body.GCID
		}
		if body.Trigger != "" {
			m["trigger"] = body.Trigger
		}
		if !createdAt.IsZero() {
			m["orphaned_at"] = createdAt.UTC()
		}
		return m
	}

	m := map[string]any{
		"atom_id":     body.AtomID,
		"author_gcid": body.GCID,
		"title":       body.Title,
		"difficulty":  int32(body.Difficulty),
	}
	if body.AtomType != "" {
		m["atom_type"] = body.AtomType
	}
	if body.RevisionID != "" {
		m["revision_id"] = body.RevisionID
	}
	if body.RevisionNumber > 0 {
		m["revision_number"] = int32(body.RevisionNumber)
	}
	if body.SourceType != "" {
		m["source_type"] = body.SourceType
	}
	if body.CourseID != "" {
		m["course_id"] = body.CourseID
	}
	// MCQ grading ground-truth (CHO-1627, fields 26/27 — shared by created +
	// published). Set only when present so non-MCQ atoms stay byte-stable (the
	// encoder also elides proto3 defaults, but skipping the map keys keeps the
	// JSON-fallback path clean).
	if body.CorrectOptionID != "" {
		m["correct_option_id"] = body.CorrectOptionID
	}
	if body.AnswerCount > 0 {
		m["answer_count"] = int32(body.AnswerCount)
	}
	// CHO-2142 — the atom's topic axis. The encoder reads payload["tags"] into
	// wire field 7 (topic_node_ids) when no explicit topic_node_ids is present
	// (protomarshal.go), and chora-consumption projects field 7 into
	// atom_index.topic_tags. Without this key the TYPED event path (publish
	// handler + backfill re-emit) reached the wire tagless every time, so a
	// source-side tag fix could never project. Set only when non-empty: the
	// consumption upsert is non-blanking (an empty EXCLUDED preserves existing
	// topic_tags), and an empty slice on the wire would be a lie.
	if len(body.Tags) > 0 {
		m["tags"] = body.Tags
	}

	// atom.reuse_visibility_changed.v1 — the ADR-229 WS-1 author audience-
	// change signal. reuse_visibility (field 3) = the NEW audience;
	// previous_visibility (field 4) lets consumers detect narrowing without
	// state; changed_at (field 6) is the occurred (mutation) instant. The
	// shared base keys (title/difficulty) are ignored by the topic's encoder.
	if topic == string(atom.EventTypeAtomReuseVisibilityChanged) {
		if body.ReuseVisibility != "" {
			m["reuse_visibility"] = body.ReuseVisibility
		}
		if body.PreviousReuseVisibility != "" {
			m["previous_visibility"] = body.PreviousReuseVisibility
		}
		if !createdAt.IsZero() {
			m["changed_at"] = createdAt.UTC()
		}
		return m
	}

	// atom.archived.v1 — the soft-delete signal (ADR-217 Debt 3). status is
	// always "archived"; archived_by_gcid (field 5) is the archiving actor
	// (carried as the Event's Gcid); archived_at (field 7) is the occurred
	// instant. reason (field 6) has no input on the soft-delete route → elided.
	if topic == string(atom.EventTypeAtomArchived) {
		m["status"] = "archived"
		if body.GCID != "" {
			m["archived_by_gcid"] = body.GCID
		}
		if !createdAt.IsZero() {
			m["archived_at"] = createdAt.UTC()
		}
		return m
	}

	// atom.updated.v1 - the metadata-edit signal (ADR-244 D5). changed_fields
	// (field 10) names exactly what moved and is what the chora-consumption
	// KG-invalidation consumer branches on; updated_at (field 11) is the
	// mutation instant, NOT created_at. status rides from the aggregate rather
	// than being hard-coded, so the wire can never claim a lifecycle state the
	// atom does not hold.
	if topic == string(atom.EventTypeAtomUpdated) {
		if body.Status != "" {
			m["status"] = body.Status
		}
		if len(body.ChangedFields) > 0 {
			m["changed_fields"] = body.ChangedFields
		}
		if !createdAt.IsZero() {
			m["updated_at"] = createdAt.UTC()
		}
		if body.Stem != "" {
			m["stem"] = body.Stem
		}
		if body.Subject != "" {
			m["subject"] = body.Subject
		}
		if body.AuthorNote != "" {
			m["author_note"] = body.AuthorNote
		}
		if body.CognitiveLevel != "" {
			m["cognitive_level"] = body.CognitiveLevel
		}
		if len(body.ImdaDimensionTags) > 0 {
			m["imda_dimension_tags"] = body.ImdaDimensionTags
		}
		if len(body.MediaAssets) > 0 {
			m["media_assets"] = mediaAssetsAsMaps(body.MediaAssets)
		}
		return m
	}

	// atom.published.v1 — the publish-transition snapshot. status is always
	// "published"; the current-revision pointer (fields 10/11) reuses the
	// Event's RevisionID/Number; published_at (field 12) is the occurred
	// instant; has_open_ended_question (field 28) is the answerability flag.
	if topic == string(atom.EventTypeAtomPublished) {
		m["status"] = "published"
		if body.RevisionID != "" {
			m["current_revision_id"] = body.RevisionID
		}
		if body.RevisionNumber > 0 {
			m["current_revision_number"] = int32(body.RevisionNumber)
		}
		if !createdAt.IsZero() {
			m["published_at"] = createdAt.UTC()
		}
		if body.HasOpenEndedQuestion {
			m["has_open_ended_question"] = true
		}
		if body.CognitiveLevel != "" {
			m["cognitive_level"] = body.CognitiveLevel // WS-C3: field 22
		}
		if body.ReuseVisibility != "" {
			m["reuse_visibility"] = body.ReuseVisibility // ADR-229: field 30
		}
		if body.AuthorDisplayName != "" {
			m["author_display_name"] = body.AuthorDisplayName // field 31
		}
		// Fields 20/21 — stem + subject (denormalised at publish time so
		// downstream consumers render the atom without a cross-DB read).
		// The Event struct carries these from the LearningAtom.
		if body.Stem != "" {
			m["stem"] = body.Stem
		}
		return m
	}

	// Schema slot: chora.creation.atom.created.v1 §10 = Timestamp created_at.
	// The handler builds atom.Event.OccurredAt from a.CreatedAt; surface it
	// under the canonical schema field name using the already-parsed
	// time.Time so a malformed input string falls back via parseRFC3339
	// upstream rather than failing here.
	if !createdAt.IsZero() {
		m["created_at"] = createdAt.UTC()
	}
	if body.ReuseVisibility != "" {
		m["reuse_visibility"] = body.ReuseVisibility // ADR-229: field 30
	}
	return m
}
