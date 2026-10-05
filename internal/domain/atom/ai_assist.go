// AI Assist domain orchestration per Comic Ch6 P12-P13 and the Phyllis MVP
// spec (docs/m13/phyllis-mvp-2026-05-08.md §5.3 / §6).
//
// AIAssistService is a domain orchestrator that depends on three ports:
//   - ModelBrokerClient — calls chora-model-broker-router /generate
//   - Repository — persists generated LearningAtoms
//   - EventPublisher — emits chora.creation.atom.created.v1 events
//
// All ports are interfaces; the domain stays adapter-free per hexagonal
// architecture. The orchestrator is pure logic (no HTTP / no DB / no
// queue libraries).
package atom

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// -----------------------------------------------------------------------------
// Screening decisions (per Comic Ch6 P13: Gatekeeper allow / rewrite / refuse)
// -----------------------------------------------------------------------------

type ScreeningDecision string

const (
	ScreeningAllow   ScreeningDecision = "allow"
	ScreeningRewrite ScreeningDecision = "rewrite"
	ScreeningRefuse  ScreeningDecision = "refuse"
)

// -----------------------------------------------------------------------------
// ModelBrokerClient port
// -----------------------------------------------------------------------------

// GenerateRequest is the broker call payload.
type GenerateRequest struct {
	TenantID    string
	Gcid        string
	CourseID    string
	Prompt      string
	ContentType AtomType
	Difficulty  int
	Count       int
	TraceParent string
	TraceState  string
}

// GeneratedItem is one of N items returned by the broker.
type GeneratedItem struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// GenerateResponse is the broker call response.
type GenerateResponse struct {
	ScreeningDecision    ScreeningDecision `json:"screening_decision"`
	ScreeningExplanation string            `json:"screening_explanation"`
	Items                []GeneratedItem   `json:"items"`
	ModelUsed            string            `json:"model_used"`
}

// ModelBrokerClient is the port for chora-model-broker-router /generate.
type ModelBrokerClient interface {
	Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error)
}

// -----------------------------------------------------------------------------
// EventPublisher port (in-memory adapter implementation lives in adapter/events)
// -----------------------------------------------------------------------------

// EventType is the canonical Pub/Sub topic suffix per topic taxonomy.
type EventType string

const (
	EventTypeAtomCreated   EventType = "chora.creation.atom.created.v1"
	EventTypeAtomRevised   EventType = "chora.creation.atom.revised.v1"
	EventTypeAtomPublished EventType = "chora.creation.atom.published.v1"
	EventTypeAtomArchived  EventType = "chora.creation.atom.archived.v1"
	// EventTypeAtomReuseVisibilityChanged — the author changed the atom's
	// reuse-consent audience (ADR-229 WS-1, CHO-2127).
	EventTypeAtomReuseVisibilityChanged EventType = "chora.creation.atom.reuse_visibility_changed.v1"
	// EventTypeAtomOrphanCreated — chora-creation materialised the singleton
	// orphan edition for a withdrawn-while-consumed atom (ADR-229 Amendment
	// A1, CHO-2132). Consumers repoint stranded refs via this event.
	EventTypeAtomOrphanCreated EventType = "chora.creation.atom.orphan_created.v1"
	// EventTypeAtomUpdated - the author moved metadata on an already-PUBLISHED
	// atom (ADR-244 D5 standing trigger). The topic, its BINARY schema binding
	// (chora-creation-atom-updated-v2), its DLQ and the chora-consumption
	// KG-invalidation consumer were all live with no producer behind them, so
	// a metadata edit reached every other domain as silence.
	EventTypeAtomUpdated EventType = "chora.creation.atom.updated.v1"
)

// Event is a domain-level event ready to be wrapped in the Protobuf envelope
// at the adapter boundary. Adapters convert this into proto messages with
// a chora.common.v1.EventEnvelope per ddd-enforcement.md.
//
// IMDA fields (ChoraImdaDimension + ImdaLifecycleStage) are optional per
// envelope.proto field 14/15 and follow ADR-141 canonical labels. Set when
// the event represents IMDA evidence (e.g., AI-assisted authoring trail).
type Event struct {
	EventID        string
	IdempotencyKey string
	Type           EventType
	TenantID       string
	Gcid           string
	OccurredAt     string // RFC3339
	PublishedAt    string // RFC3339
	TraceParent    string
	TraceState     string
	SourceProject  string
	SourceService  string
	SchemaVersion  int32
	AtomID         string
	CourseID       string
	RevisionID     string
	RevisionNumber int
	Title          string
	Stem           string
	AtomType       AtomType
	Difficulty     int
	SourceType     SourceType

	// MCQ grading ground-truth (CHO-1627, L1 grading fix). Carried on
	// chora.creation.atom.created.v1 so chora-consumption can score MCQ
	// submissions server-side. Both are proto3-default (empty / 0) for
	// non-MCQ atoms and are elided from the wire by the encoder.
	//   - CorrectOptionID: the OptionID (UUID) of the option whose
	//     IsCorrect==true. Empty for non-MCQ or when no correct option set.
	//   - AnswerCount: number of MCQ options. 0 for non-MCQ.
	CorrectOptionID string
	AnswerCount     int

	// HasOpenEndedQuestion marks a PUBLISHED atom whose question is open-ended
	// (model-answer / rubric graded) rather than MCQ. Carried on
	// chora.creation.atom.published.v1 (field 28) so chora-consumption can
	// treat the atom as answerable (playable in the daily dose) without an MCQ
	// answer key. False / elided for MCQ + non-published events.
	HasOpenEndedQuestion bool

	// CognitiveLevel — the atom's ORIGINAL-Bloom label (ADR-156), carried on
	// chora.creation.atom.published.v1 (field 22) so chora-consumption's
	// campaign question lane can level-filter retrieval (WS-C3, CHO-2082).
	// "" = unlevelled (elided on the wire).
	CognitiveLevel string

	// Tags — the atom's topic axis, carried on atom.created.v1 /
	// atom.published.v1 and encoded into wire field 7 (topic_node_ids) by the
	// payload encoder, which chora-consumption projects into
	// atom_index.topic_tags (the fog catalogue bias + dose topic pick +
	// topic-accuracy projector all key on it).
	//
	// CHO-2142: before this field existed the typed event carried NO tags, so
	// every publish (and every backfill re-emit) reached the wire tagless and a
	// source-side tag fix could never project. Empty = elided on the wire; the
	// consumption upsert is non-blanking, so an empty set preserves whatever the
	// composer path already projected.
	Tags []string

	// ReuseVisibility — the ADR-229 author-consent audience label (private |
	// friends | tenant). Carried on atom.created.v1 / atom.published.v1
	// (field 30) and as the NEW value on reuse_visibility_changed.v1
	// (field 3). "" from legacy call-sites elides on the wire; consumers
	// harden absent to private.
	ReuseVisibility string

	// PreviousReuseVisibility — the audience BEFORE an audience change.
	// Carried only on reuse_visibility_changed.v1 (field 4) so consumers
	// distinguish narrowing from widening statelessly.
	PreviousReuseVisibility string

	// OrphanedFromAtomID + Trigger — carried only on atom.orphan_created.v1
	// (ADR-229 A1, CHO-2132). For that topic AtomID is the NEW orphan
	// edition (the aggregate), OrphanedFromAtomID the withdrawn original,
	// RevisionID the pinned last-published source revision, and Trigger one
	// of narrowed|unshared|archived.
	OrphanedFromAtomID string
	Trigger            string

	// IMDA per ADR-141. Empty when the event is not IMDA evidence.
	ChoraImdaDimension string
	ImdaLifecycleStage string

	// AuthorDisplayName — the author's display name at publish time.
	// Carried on chora.creation.atom.published.v1 (field 31) so downstream
	// consumers (chora-sharing atom_projections, feed cards) render the
	// author without a cross-DB identity lookup. Empty when the identity
	// service is unavailable at publish time.
	AuthorDisplayName string

	// ---------------------------------------------------------------------
	// atom.updated.v1 metadata snapshot (ADR-244 D5). ChangedFields is the
	// field consumers branch on; the rest is the POST-patch value of every
	// metadata slot the PATCH path can move, so a consumer refreshes its
	// projection from the event instead of calling back.
	// ---------------------------------------------------------------------

	// ChangedFields names exactly the fields this update moved, in the
	// canonical PATCH order. Never empty on atom.updated.v1: a patch that
	// moved nothing emits no event at all.
	ChangedFields []string

	// Status - the atom's lifecycle label at update time (field 4 on
	// atom.updated.v1). Only "published" atoms emit that event; carried from
	// the aggregate rather than hard-coded so the wire never claims a status
	// the aggregate does not hold.
	Status string

	// Subject / AuthorNote - ADR-156 Phase 1 metadata on atom.updated.v1
	// (fields 21 / 24). Empty elides on the wire.
	Subject    string
	AuthorNote string

	// ImdaDimensionTags - the ADR-141 canonical IMDA labels carried on
	// atom.updated.v1 (field 23, packed enum). Labels, not wire ints: the
	// encoder resolves them and fails loud on an unknown one.
	ImdaDimensionTags []string

	// MediaAssets - the atom's embedded media on atom.updated.v1 (field 25).
	// Phase 1 carries at most one image entry.
	MediaAssets []MediaAsset
}

// EventPublisher is the port for emitting Pub/Sub events.
type EventPublisher interface {
	Publish(ctx context.Context, e Event) error
}

// -----------------------------------------------------------------------------
// AIAssistService — pure domain orchestrator
// -----------------------------------------------------------------------------

// AIAssistRequest is the parameters for an AI Assist generation pass.
type AIAssistRequest struct {
	TenantID    string
	Gcid        string
	CourseID    string
	Prompt      string
	ContentType AtomType
	Difficulty  int
	Count       int
	TraceParent string
	TraceState  string
}

// AIAssistResponse is the orchestrator's caller-facing summary.
type AIAssistResponse struct {
	GeneratedAtomIDs     []string          `json:"generated_atom_ids"`
	ScreeningDecision    ScreeningDecision `json:"screening_decision"`
	ScreeningExplanation string            `json:"screening_explanation"`
	ModelUsed            string            `json:"model_used,omitempty"`
}

// AIAssistService orchestrates: broker call -> screening check -> persist
// atoms -> publish events.
type AIAssistService struct {
	broker ModelBrokerClient
	repo   Repository
	pub    EventPublisher
}

// NewAIAssistService wires the orchestrator with its three ports.
func NewAIAssistService(b ModelBrokerClient, r Repository, p EventPublisher) *AIAssistService {
	return &AIAssistService{broker: b, repo: r, pub: p}
}

// Generate executes the full Comic Ch6 P12-P13 happy path.
//
// Flow:
//  1. Validate request.
//  2. Call ModelBrokerClient.Generate.
//  3. If decision=refuse, return early with 0 atoms (no persistence, no events).
//  4. Otherwise persist N atoms with SourceType=ai_assist + screening metadata.
//  5. Publish chora.creation.atom.created.v1 event per atom.
func (s *AIAssistService) Generate(ctx context.Context, req AIAssistRequest) (AIAssistResponse, error) {
	if strings.TrimSpace(req.TenantID) == "" {
		return AIAssistResponse{}, errors.New("tenant_id is required")
	}
	if strings.TrimSpace(req.Gcid) == "" {
		return AIAssistResponse{}, errors.New("gcid is required")
	}
	if strings.TrimSpace(req.CourseID) == "" {
		return AIAssistResponse{}, errors.New("course_id is required")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return AIAssistResponse{}, errors.New("prompt is required")
	}
	if !req.ContentType.Valid() {
		return AIAssistResponse{}, fmt.Errorf("invalid content_type: %q", string(req.ContentType))
	}
	if req.Difficulty < 0 || req.Difficulty > 5 {
		return AIAssistResponse{}, fmt.Errorf("difficulty out of range: %d", req.Difficulty)
	}
	if req.Count <= 0 {
		return AIAssistResponse{}, errors.New("count must be > 0")
	}
	if req.Count > 50 {
		return AIAssistResponse{}, fmt.Errorf("count too high (max 50): %d", req.Count)
	}

	bResp, err := s.broker.Generate(ctx, GenerateRequest{
		TenantID:    req.TenantID,
		Gcid:        req.Gcid,
		CourseID:    req.CourseID,
		Prompt:      req.Prompt,
		ContentType: req.ContentType,
		Difficulty:  req.Difficulty,
		Count:       req.Count,
		TraceParent: req.TraceParent,
		TraceState:  req.TraceState,
	})
	if err != nil {
		return AIAssistResponse{}, fmt.Errorf("model broker: %w", err)
	}

	// Refusal path: 0 atoms persisted, 0 events.
	if bResp.ScreeningDecision == ScreeningRefuse {
		return AIAssistResponse{
			GeneratedAtomIDs:     nil,
			ScreeningDecision:    ScreeningRefuse,
			ScreeningExplanation: bResp.ScreeningExplanation,
			ModelUsed:            bResp.ModelUsed,
		}, nil
	}

	// Allow / rewrite path: persist atoms + publish events.
	out := AIAssistResponse{
		ScreeningDecision:    bResp.ScreeningDecision,
		ScreeningExplanation: bResp.ScreeningExplanation,
		ModelUsed:            bResp.ModelUsed,
		GeneratedAtomIDs:     make([]string, 0, len(bResp.Items)),
	}
	for _, item := range bResp.Items {
		md := map[string]string{
			"model_used":            bResp.ModelUsed,
			"screening_decision":    string(bResp.ScreeningDecision),
			"screening_explanation": bResp.ScreeningExplanation,
			"prompt_hash":           hashPrompt(req.Prompt),
		}
		a, err := NewBound(NewBoundParams{
			TenantID:       req.TenantID,
			Gcid:           req.Gcid,
			CourseID:       req.CourseID,
			Title:          item.Title,
			Body:           item.Body,
			AtomType:       req.ContentType,
			Difficulty:     req.Difficulty,
			SourceType:     SourceAIAssist,
			SourceMetadata: md,
		})
		if err != nil {
			return AIAssistResponse{}, fmt.Errorf("constructing ai-assist atom: %w", err)
		}
		if err := s.repo.Save(ctx, a); err != nil {
			return AIAssistResponse{}, fmt.Errorf("persisting ai-assist atom: %w", err)
		}
		out.GeneratedAtomIDs = append(out.GeneratedAtomIDs, a.AtomID)

		ev := newAtomCreatedEvent(a, req.TraceParent, req.TraceState)
		// IMDA evidence per ADR-141 — accountability (D1) at runtime
		// stage. AI Assist outputs are by definition AI-attributable
		// content and must be tagged for the O+ governance dashboard.
		ev.ChoraImdaDimension = "accountability"
		ev.ImdaLifecycleStage = "runtime"
		if err := s.pub.Publish(ctx, ev); err != nil {
			return AIAssistResponse{}, fmt.Errorf("publishing atom.created event: %w", err)
		}
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// hashPrompt returns a deterministic short hash of the prompt for audit.
// It is intentionally non-cryptographic — we only need a short consistent
// string for grouping events emitted from the same AI Assist invocation.
func hashPrompt(p string) string {
	if p == "" {
		return ""
	}
	// FNV-1a 32-bit, hex.
	const (
		offset32 uint32 = 2166136261
		prime32  uint32 = 16777619
	)
	h := offset32
	for i := 0; i < len(p); i++ {
		h ^= uint32(p[i])
		h *= prime32
	}
	return fmt.Sprintf("%08x", h)
}
