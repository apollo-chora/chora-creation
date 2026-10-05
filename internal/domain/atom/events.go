// Domain-level event builders for chora.creation.atom.{created,revised}.v1.
//
// These functions take an aggregate + trace context and return a fully-
// populated Event ready for the publisher port. They do not import any
// adapter package and stay dependency-free.
package atom

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	envSourceProject = "chora-content"
	envSourceService = "chora-creation"
	envSchemaVersion = int32(1)
)

// newAtomCreatedEvent builds a chora.creation.atom.created.v1 event for an
// atom that has just been persisted. Per the envelope contract:
//   - event_id and idempotency_key are UUIDv7 (idempotency_key = event_id).
//   - traceparent / tracestate flow from the caller.
//   - source_project + source_service are static for this service.
func newAtomCreatedEvent(a *LearningAtom, traceparent, tracestate string) Event {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id, _ := uuid.NewV7()
	cur := a.CurrentRevision()
	revID := ""
	revNum := 0
	srcType := SourceManual
	if cur != nil {
		revID = cur.RevisionID
		revNum = cur.RevisionNumber
		srcType = cur.SourceType
	}
	return Event{
		EventID:         id.String(),
		IdempotencyKey:  id.String(),
		Type:            EventTypeAtomCreated,
		TenantID:        a.TenantID,
		Gcid:            a.Gcid,
		OccurredAt:      a.CreatedAt.UTC().Format(time.RFC3339Nano),
		PublishedAt:     now,
		TraceParent:     traceparent,
		TraceState:      tracestate,
		SourceProject:   envSourceProject,
		SourceService:   envSourceService,
		SchemaVersion:   envSchemaVersion,
		AtomID:          a.AtomID,
		CourseID:        a.CourseID,
		RevisionID:      revID,
		RevisionNumber:  revNum,
		Title:           a.Title,
		AtomType:        a.QuestionType, // ADR-156 Decision #2: struct field renamed; Event keeps legacy name
		Difficulty:      a.Difficulty,
		SourceType:      srcType,
		Tags:            copyTags(a.Tags),          // CHO-2142: field 7 topic_node_ids
		ReuseVisibility: string(a.ReuseVisibility), // ADR-229 WS-1: field 30
	}
}

// NewAtomCreatedEvent is the public wrapper used by HTTP handlers when a
// learner creates a new atom outside the AI Assist path.
func NewAtomCreatedEvent(a *LearningAtom, traceparent, tracestate string) Event {
	return newAtomCreatedEvent(a, traceparent, tracestate)
}

// NewAtomArchivedEvent builds a chora.creation.atom.archived.v1 event for an
// atom that has just been soft-deleted (status=ARCHIVED, deleted_at set). This
// is the decrement signal for the tenancy atom-count projection (ADR-217 Debt
// 3). The archiving actor's GCID rides the envelope gcid (== archived_by_gcid);
// occurred_at is the archive instant (deleted_at, falling back to updated_at).
// reason is intentionally empty — the soft-delete route carries no reason input
// — and is proto3-elided by the encoder.
func NewAtomArchivedEvent(a *LearningAtom, archivedByGcid, traceparent, tracestate string) Event {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id, _ := uuid.NewV7()
	occurred := a.UpdatedAt
	if a.DeletedAt != nil {
		occurred = *a.DeletedAt
	}
	return Event{
		EventID:        id.String(),
		IdempotencyKey: id.String(),
		Type:           EventTypeAtomArchived,
		TenantID:       a.TenantID,
		Gcid:           archivedByGcid,
		OccurredAt:     occurred.UTC().Format(time.RFC3339Nano),
		PublishedAt:    now,
		TraceParent:    traceparent,
		TraceState:     tracestate,
		SourceProject:  envSourceProject,
		SourceService:  envSourceService,
		SchemaVersion:  envSchemaVersion,
		AtomID:         a.AtomID,
		AtomType:       a.QuestionType,
	}
}

// NewAtomPublishedEvent builds a chora.creation.atom.published.v1 event for an
// atom that has just transitioned DRAFT -> PUBLISHED. Mirrors
// newAtomCreatedEvent but:
//   - Type = EventTypeAtomPublished.
//   - OccurredAt = the atom's UpdatedAt (the publish instant set by Publish());
//     falls back to CreatedAt only if UpdatedAt is somehow zero.
//   - RevisionID / RevisionNumber pin the AtomRevision that is current at
//     publish time (consumers pin version-stable reads against it).
//   - CorrectOptionID / AnswerCount carry the MCQ grading ground-truth;
//     HasOpenEndedQuestion marks an OE atom as answerable without an MCQ key.
//     The created event NEVER carries these (it fires at draft-creation before
//     a question exists), so atom.published.v1 is the authoritative source.
//   - IdempotencyKey is DETERMINISTIC (atom_id + ":published:" + revision_id)
//     so re-publishing the SAME live revision dedupes on the outbox
//     idempotency_key UNIQUE index instead of emitting a duplicate. EventID
//     stays a fresh UUIDv7.
//
// Answerability is passed as primitives so the atom package never imports the
// question aggregate (hexagonal boundary).
func NewAtomPublishedEvent(a *LearningAtom, revisionID string, revisionNumber int, correctOptionID string, answerCount int, hasOE bool, authorDisplayName, traceparent, tracestate string) Event {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id, _ := uuid.NewV7()
	occurred := a.UpdatedAt
	if occurred.IsZero() {
		occurred = a.CreatedAt
	}
	return Event{
		EventID:              id.String(),
		IdempotencyKey:       a.AtomID + ":published:" + revisionID,
		Type:                 EventTypeAtomPublished,
		TenantID:             a.TenantID,
		Gcid:                 a.Gcid,
		OccurredAt:           occurred.UTC().Format(time.RFC3339Nano),
		PublishedAt:          now,
		TraceParent:          traceparent,
		TraceState:           tracestate,
		SourceProject:        envSourceProject,
		SourceService:        envSourceService,
		SchemaVersion:        envSchemaVersion,
		AtomID:               a.AtomID,
		CourseID:             a.CourseID,
		RevisionID:           revisionID,
		RevisionNumber:       revisionNumber,
		Title:                a.Title,
		Stem:                 a.Stem,
		AtomType:             a.QuestionType, // ADR-156 Decision #2: struct field renamed; Event keeps legacy name
		Difficulty:           a.Difficulty,
		CorrectOptionID:      correctOptionID,
		AnswerCount:          answerCount,
		HasOpenEndedQuestion: hasOE,
		CognitiveLevel:       string(a.CognitiveLevel),  // WS-C3: field 22 on the wire
		Tags:                 copyTags(a.Tags),          // CHO-2142: field 7 topic_node_ids
		ReuseVisibility:      string(a.ReuseVisibility), // ADR-229 WS-1: field 30
		AuthorDisplayName:    authorDisplayName,         // field 31
	}
}

// NewAtomUpdatedEvent builds a chora.creation.atom.updated.v1 event for an
// atom whose METADATA just moved (ADR-244 D5). Call it ONLY when the patch
// actually changed something on a PUBLISHED atom: changedFields is the field
// consumers branch on, so an event carrying an empty (or padded) list is worse
// than no event at all.
//
//   - OccurredAt = a.UpdatedAt, the instant ApplyUpdate stamped on the patch.
//   - Gcid = the atom's author (the PATCH route is author-only).
//   - The remaining fields are the POST-patch snapshot, so a consumer refreshes
//     its projection from the event rather than calling back.
//   - IdempotencyKey is DETERMINISTIC per update
//     (atom_id + ":updated:" + the RFC3339Nano update instant): a crash
//     redelivery of the SAME update dedupes on the outbox
//     UNIQUE(idempotency_key) index, while the next real edit carries a new
//     instant and enqueues. EventID stays a fresh UUIDv7.
//
// The key is stamped from UpdatedAt and NOT from Revision: phyllis.go
// re-assigns Revision from a revision number (a.Revision = r.RevisionNumber),
// so the counter is not monotonic per edit and two edits could collide on it,
// which would silently drop the second.
func NewAtomUpdatedEvent(a *LearningAtom, changedFields []string, traceparent, tracestate string) Event {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id, _ := uuid.NewV7()
	occurred := a.UpdatedAt
	if occurred.IsZero() {
		occurred = a.CreatedAt
	}
	occurredAt := occurred.UTC().Format(time.RFC3339Nano)
	return Event{
		EventID:           id.String(),
		IdempotencyKey:    a.AtomID + ":updated:" + occurredAt,
		Type:              EventTypeAtomUpdated,
		TenantID:          a.TenantID,
		Gcid:              a.Gcid,
		OccurredAt:        occurredAt,
		PublishedAt:       now,
		TraceParent:       traceparent,
		TraceState:        tracestate,
		SourceProject:     envSourceProject,
		SourceService:     envSourceService,
		SchemaVersion:     envSchemaVersion,
		AtomID:            a.AtomID,
		CourseID:          a.CourseID,
		Title:             a.Title,
		Stem:              a.Stem,
		Subject:           a.Subject,
		AuthorNote:        a.AuthorNote,
		AtomType:          a.QuestionType,
		Difficulty:        a.Difficulty,
		Status:            string(a.Status),
		CognitiveLevel:    string(a.CognitiveLevel),
		Tags:              copyTags(a.Tags),
		ImdaDimensionTags: copyImdaTags(a.ImdaDimensionTags),
		MediaAssets:       copyMediaAssets(a.MediaAssets),
		ChangedFields:     copyTags(changedFields),
		ReuseVisibility:   string(a.ReuseVisibility),
	}
}

// copyImdaTags defensively copies the atom's IMDA labels onto the event as
// plain strings (the encoder resolves them to wire ints). Returns nil for an
// empty set so the encoder elides field 23.
func copyImdaTags(tags []ImdaDimTag) []string {
	if len(tags) == 0 {
		return nil
	}
	out := make([]string, len(tags))
	for i, t := range tags {
		out[i] = string(t)
	}
	return out
}

// copyMediaAssets defensively copies the atom's media assets onto the event.
// MediaAsset is a value type with no inner pointers, so a shallow element copy
// is a full copy. Returns nil for an empty set so the encoder elides field 25.
func copyMediaAssets(assets []MediaAsset) []MediaAsset {
	if len(assets) == 0 {
		return nil
	}
	return append([]MediaAsset(nil), assets...)
}

// copyTags defensively copies the atom's tag slice onto the event so a later
// mutation of the aggregate cannot retro-edit an already-queued event. Returns
// nil for an empty set so the encoder elides field 7 (proto3 default).
func copyTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	return append([]string(nil), tags...)
}

// NewAtomReuseVisibilityChangedEvent builds a
// chora.creation.atom.reuse_visibility_changed.v1 event after a successful
// author-only ChangeReuseVisibility (ADR-229 WS-1, CHO-2127). Call it ONLY
// when the mutation reported changed=true — a same-value no-op must emit
// nothing. The caller supplies the audience BEFORE the change so consumers
// can distinguish narrowing from widening statelessly (Amendment A1.1:
// narrowing never revokes grants — the orphan-edition machinery handles
// stranded consumers in its own event).
//
//   - OccurredAt = the mutation instant (UpdatedAt stamped by the domain).
//   - Gcid = the atom's author (the mutation is author-only by construction).
//   - EventID/IdempotencyKey = fresh UUIDv7 — every real change is a distinct
//     event (an a->b->a flip is two real changes, not a duplicate).
func NewAtomReuseVisibilityChangedEvent(a *LearningAtom, previous ReuseVisibility, traceparent, tracestate string) Event {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id, _ := uuid.NewV7()
	return Event{
		EventID:                 id.String(),
		IdempotencyKey:          id.String(),
		Type:                    EventTypeAtomReuseVisibilityChanged,
		TenantID:                a.TenantID,
		Gcid:                    a.Gcid,
		OccurredAt:              a.UpdatedAt.UTC().Format(time.RFC3339Nano),
		PublishedAt:             now,
		TraceParent:             traceparent,
		TraceState:              tracestate,
		SourceProject:           envSourceProject,
		SourceService:           envSourceService,
		SchemaVersion:           envSchemaVersion,
		AtomID:                  a.AtomID,
		ReuseVisibility:         string(a.ReuseVisibility),
		PreviousReuseVisibility: string(previous),
	}
}

// NewAtomOrphanCreatedEvent builds a chora.creation.atom.orphan_created.v1
// event for a freshly-minted orphan edition (ADR-229 Amendment A1, CHO-2132).
//
//   - AtomID (the aggregate) = the NEW orphan edition; OrphanedFromAtomID =
//     the withdrawn original; RevisionID = the pinned last-published source
//     revision (the singleton key's second half); Trigger =
//     narrowed|unshared|archived.
//   - Gcid = the ORIGINAL author (attribution; pseudonymised at closure).
//   - IdempotencyKey is DETERMINISTIC — the singleton key itself
//     (orphaned_from + ":orphan_created:" + source_revision) — so the
//     creation outbox UNIQUE(idempotency_key) is the DB-level "no duplicate
//     orphan_created event" enforcement: a crash-redelivery re-publish
//     dedupes instead of double-emitting, and a repeat mint at the same
//     revision can never enqueue a second event. EventID stays a fresh
//     UUIDv7.
//   - OccurredAt = the mint instant (OrphanedAt).
func NewAtomOrphanCreatedEvent(orphan *LearningAtom, trigger, traceparent, tracestate string) Event {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id, _ := uuid.NewV7()
	occurred := orphan.UpdatedAt
	if orphan.OrphanedAt != nil {
		occurred = *orphan.OrphanedAt
	}
	return Event{
		EventID:            id.String(),
		IdempotencyKey:     orphan.OrphanedFromAtomID + ":orphan_created:" + orphan.OrphanedSourceRevisionID,
		Type:               EventTypeAtomOrphanCreated,
		TenantID:           orphan.TenantID,
		Gcid:               orphan.Gcid,
		OccurredAt:         occurred.UTC().Format(time.RFC3339Nano),
		PublishedAt:        now,
		TraceParent:        traceparent,
		TraceState:         tracestate,
		SourceProject:      envSourceProject,
		SourceService:      envSourceService,
		SchemaVersion:      envSchemaVersion,
		AtomID:             orphan.AtomID, // the NEW orphan edition (aggregate id)
		RevisionID:         orphan.OrphanedSourceRevisionID,
		OrphanedFromAtomID: orphan.OrphanedFromAtomID,
		Trigger:            trigger,
	}
}

// NewAtomPublishedReemitEvent builds the same atom.published.v1 payload as
// NewAtomPublishedEvent but salts the deterministic idempotency key
// (atom:published:rev + ":reemit:" + salt). The plain key means the outbox
// UNIQUE index permanently dedupes any re-emit of an already-published
// revision — which is exactly right for the publish path and exactly wrong for
// an operator-driven projection-seeding backfill (CHO-2128 F1). Salting keeps
// the key deterministic PER SALT: re-running the SAME salt still dedupes; a
// NEW salt re-enqueues. A blank salt falls back to the unsalted key (callers
// must reject blank salts fail-loud before reaching here).
func NewAtomPublishedReemitEvent(a *LearningAtom, revisionID string, revisionNumber int, correctOptionID string, answerCount int, hasOE bool, authorDisplayName, traceparent, tracestate, reemitSalt string) Event {
	ev := NewAtomPublishedEvent(a, revisionID, revisionNumber, correctOptionID, answerCount, hasOE, authorDisplayName, traceparent, tracestate)
	if s := strings.TrimSpace(reemitSalt); s != "" {
		ev.IdempotencyKey += ":reemit:" + s
	}
	return ev
}

// NewAtomRevisedEvent builds a chora.creation.atom.revised.v1 event for the
// supplied atom + appended revision. Used when a learner adds a new revision
// to an existing atom.
func NewAtomRevisedEvent(a *LearningAtom, r *AppendOnlyRevision, traceparent, tracestate string) Event {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id, _ := uuid.NewV7()
	return Event{
		EventID:        id.String(),
		IdempotencyKey: id.String(),
		Type:           EventTypeAtomRevised,
		TenantID:       a.TenantID,
		Gcid:           r.AuthoredBy,
		OccurredAt:     r.AuthoredAt.UTC().Format(time.RFC3339Nano),
		PublishedAt:    now,
		TraceParent:    traceparent,
		TraceState:     tracestate,
		SourceProject:  envSourceProject,
		SourceService:  envSourceService,
		SchemaVersion:  envSchemaVersion,
		AtomID:         a.AtomID,
		CourseID:       a.CourseID,
		RevisionID:     r.RevisionID,
		RevisionNumber: r.RevisionNumber,
		Title:          a.Title,
		AtomType:       a.QuestionType, // ADR-156 Decision #2: struct field renamed; Event keeps legacy name
		Difficulty:     a.Difficulty,
		SourceType:     r.SourceType,
	}
}
