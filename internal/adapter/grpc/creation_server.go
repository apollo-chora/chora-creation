// Package creationgrpc — the gRPC server-side adapter for the Creation
// service contract (`chora-contracts/proto/services/creation/v1/creation.proto`).
//
// This adapter satisfies the `chora.services.creation.v1.CreationServer`
// interface by translating proto request/response into the domain ports
// (`atom.Repository`, `ports.QuestionRepository`). Per hexagonal architecture
// the gRPC adapter depends on the domain, never the reverse.
//
// Wave-1 gRPC mass remediation (2026-05-16, owner C-FULL) — registers the
// 8 RPCs the proto defines so chora-delivery's QuestionSnapshotter wire-up
// (services/chora-delivery/cmd/server/main.go:340-398 — already client-
// complete) finally has a server to dial. Closes the chain-break filed as
// `E2E-INFRA-LEG3-D`.
//
// Per `feedback_no_stubs_real_wiring`: the server registers
// unconditionally. When a dependent repo is nil (e.g. pgx pool not wired),
// the RPC returns gRPC FAILED_PRECONDITION with a fail-loud message so the
// caller can distinguish "method exists, dependency missing" from
// "method not registered".
package creationgrpc

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/creation/v1"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// CreationServer adapts the Creation gRPC contract to the Content Creation
// domain ports. Construct via NewCreationServer.
type CreationServer struct {
	creationv1.UnimplementedCreationServer

	atoms     atom.Repository          // required for atom RPCs
	questions ports.QuestionRepository // required for SnapshotQuestionByID

	// OT#4 — download signer for MintAtomMediaDownloadURL. Nil when the
	// atom-media bucket is unwired (env ATOM_MEDIA_BUCKET unset); the RPC
	// then returns FAILED_PRECONDITION per feedback_no_stubs_real_wiring.
	downloadSigner ports.AtomMediaDownloadSigner

	// ADR-229 WS-2 (CHO-2133) chokepoint 2 — the SnapshotQuestionByID reuse
	// gate. reuseContext reads the caller's granted atom ids from
	// chora-sharing; atomUse writes the D2 audit grant (scope TEST_SET) for
	// an allowed non-owner tenant-visible snapshot. Nil deps make gated
	// (caller_gcid-bearing) requests refuse FAILED_PRECONDITION — never an
	// ungated serve.
	reuseContext ports.ReuseContextFetcher
	atomUse      ports.AtomUseAuthorizer
}

// Deps captures the constructor arguments. Tests build the in-memory variant;
// production wires the pgx-backed repositories from cmd/server/main.go.
type Deps struct {
	Atoms          atom.Repository
	Questions      ports.QuestionRepository
	DownloadSigner ports.AtomMediaDownloadSigner
	// ReuseContext + AtomUse — the ADR-229 consent gate deps (both served
	// by clients.SharingReuseClient over SVC_SHARING_GRPC_URL).
	ReuseContext ports.ReuseContextFetcher
	AtomUse      ports.AtomUseAuthorizer
}

// NewCreationServer constructs the gRPC server-side adapter.
//
// Any dependency may be nil — RPCs that need a missing dep return
// FAILED_PRECONDITION at call time per `feedback_no_stubs_real_wiring`.
func NewCreationServer(deps Deps) *CreationServer {
	return &CreationServer{
		atoms:          deps.Atoms,
		questions:      deps.Questions,
		downloadSigner: deps.DownloadSigner,
		reuseContext:   deps.ReuseContext,
		atomUse:        deps.AtomUse,
	}
}

// -----------------------------------------------------------------------------
// CreateAtom
// -----------------------------------------------------------------------------

// CreateAtom — DRAFT-status LearningAtom creation. Idempotent on
// (author_gcid, title, locale) within a 5-second window via the atom
// repository's Save (upsert by AtomID).
func (s *CreationServer) CreateAtom(ctx context.Context, in *creationv1.CreateAtomRequest) (*creationv1.CreateAtomResponse, error) {
	if s.atoms == nil {
		return nil, status.Error(codes.FailedPrecondition, "creation: AtomRepository not wired")
	}
	if in == nil {
		return nil, status.Error(codes.InvalidArgument, "creation: nil request")
	}
	if strings.TrimSpace(in.GetTenantId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "creation: tenant_id required")
	}
	if strings.TrimSpace(in.GetAuthorGcid()) == "" {
		return nil, status.Error(codes.InvalidArgument, "creation: author_gcid required")
	}
	la, err := atom.New(atom.NewParams{
		TenantID: in.GetTenantId(),
		Gcid:     in.GetAuthorGcid(),
		Title:    in.GetTitle(),
		// Body is wire-empty on CreateAtom — initial_revision_content_json
		// stays in the revision append flow (separate RPC).
		Mode: atom.ModeStraightUp,
	})
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "creation: new atom: %v", err)
	}
	if err := s.atoms.Save(ctx, la); err != nil {
		return nil, status.Errorf(codes.Internal, "creation: save atom: %v", err)
	}
	return &creationv1.CreateAtomResponse{Atom: atomToProto(la)}, nil
}

// -----------------------------------------------------------------------------
// GenerateAtomsViaAI — long-running AI-assist generation.
//
// The 6-agent content gate runs out-of-band (chora-ai-kernel-orchestrator on
// Vertex AI Agent Engine). The gRPC server side surfaces UNIMPLEMENTED until
// the orchestrator emits atoms_ready events and the cmd/server bootstrap
// wires those into a generation_id correlation cache. See
// `cmd/server/main.go` AI Kernel Orchestrator wiring + ADR-145.
// -----------------------------------------------------------------------------

func (s *CreationServer) GenerateAtomsViaAI(_ context.Context, _ *creationv1.GenerateAtomsViaAIRequest) (*creationv1.GenerateAtomsViaAIResponse, error) {
	return nil, status.Error(codes.Unimplemented, "creation: GenerateAtomsViaAI is async via chora.ai_kernel.crew.atoms_ready.v1 — call /api/atoms/ai-assist HTTP endpoint or POST /v1/atoms/generate")
}

// -----------------------------------------------------------------------------
// GetAtom
// -----------------------------------------------------------------------------

func (s *CreationServer) GetAtom(ctx context.Context, in *creationv1.GetAtomRequest) (*creationv1.GetAtomResponse, error) {
	if s.atoms == nil {
		return nil, status.Error(codes.FailedPrecondition, "creation: AtomRepository not wired")
	}
	if in == nil || strings.TrimSpace(in.GetAtomId()) == "" || strings.TrimSpace(in.GetTenantId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "creation: atom_id + tenant_id required")
	}
	la, err := s.atoms.Get(ctx, in.GetTenantId(), in.GetAtomId())
	if err != nil {
		if errors.Is(err, atom.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "creation: atom not found")
		}
		return nil, status.Errorf(codes.Internal, "creation: get atom: %v", err)
	}
	return &creationv1.GetAtomResponse{Atom: atomToProto(la)}, nil
}

// -----------------------------------------------------------------------------
// ValidateAtomID — anti-fabrication guard for Familiar Companion cite_atom.
// -----------------------------------------------------------------------------

func (s *CreationServer) ValidateAtomID(ctx context.Context, in *creationv1.ValidateAtomIDRequest) (*creationv1.ValidateAtomIDResponse, error) {
	if s.atoms == nil {
		return nil, status.Error(codes.FailedPrecondition, "creation: AtomRepository not wired")
	}
	if in == nil || strings.TrimSpace(in.GetAtomId()) == "" || strings.TrimSpace(in.GetTenantId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "creation: atom_id + tenant_id required")
	}
	la, err := s.atoms.Get(ctx, in.GetTenantId(), in.GetAtomId())
	if err != nil {
		// Per proto contract: single bool false on the wire for unknown /
		// wrong-tenant / soft-deleted — prevents oracle leaks for
		// cross-tenant probing.
		if errors.Is(err, atom.ErrNotFound) {
			return &creationv1.ValidateAtomIDResponse{Exists: false}, nil
		}
		return nil, status.Errorf(codes.Internal, "creation: validate atom: %v", err)
	}
	// "current revision" — chora-creation's existing AtomRevision domain
	// is per-atom; the proto field is opaque revision pointer. Since the
	// in-memory + pgx adapters track revision number on the atom itself,
	// we return atom_id as the revision pointer (caller cites the latest).
	return &creationv1.ValidateAtomIDResponse{
		Exists:            true,
		CurrentRevisionId: la.AtomID,
	}, nil
}

// -----------------------------------------------------------------------------
// ListAtomsByCourse
// -----------------------------------------------------------------------------

func (s *CreationServer) ListAtomsByCourse(ctx context.Context, in *creationv1.ListAtomsByCourseRequest) (*creationv1.ListAtomsByCourseResponse, error) {
	if s.atoms == nil {
		return nil, status.Error(codes.FailedPrecondition, "creation: AtomRepository not wired")
	}
	if in == nil || strings.TrimSpace(in.GetCourseId()) == "" || strings.TrimSpace(in.GetTenantId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "creation: course_id + tenant_id required")
	}
	atoms, err := s.atoms.ListByCourse(ctx, in.GetTenantId(), in.GetCourseId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "creation: list by course: %v", err)
	}
	out := make([]*creationv1.Atom, 0, len(atoms))
	for _, a := range atoms {
		out = append(out, atomToProto(a))
	}
	// MVP — no cursor pagination yet. The repository returns the full
	// course slice; the proto cursor stays empty.
	return &creationv1.ListAtomsByCourseResponse{
		Atoms:      out,
		NextCursor: "",
	}, nil
}

// -----------------------------------------------------------------------------
// AppendRevision — append-only AtomRevision insert.
//
// Per ddd-enforcement aggregate-invariant #4: AtomRevision is APPEND-ONLY —
// never UPDATE / DELETE in place. The repository handles the dedup via the
// idempotency_key.
//
// MVP: the v1 atom repository doesn't yet expose AppendRevision through the
// `atom.Repository` port — it lives on the Phyllis-side aggregate. Surface
// as UNIMPLEMENTED until the port is widened (tracked separately).
// -----------------------------------------------------------------------------

func (s *CreationServer) AppendRevision(_ context.Context, _ *creationv1.AppendRevisionRequest) (*creationv1.AppendRevisionResponse, error) {
	return nil, status.Error(codes.Unimplemented, "creation: AppendRevision requires Phyllis revision port — use POST /v1/atoms/{id}/revisions HTTP endpoint until the port is widened")
}

// -----------------------------------------------------------------------------
// QueryKnowledgeGraph
//
// MVP: chora-creation's KnowledgeGraph traversal lives in chora-consumption
// per ADR-143 (per-user KG, not per-tenant). Per ADR-143 the authoring-side
// atom-to-atom edges in `atom_semantic_edges` are inert until a Consumption
// caller asks; the Creation gRPC surface returns UNIMPLEMENTED to make the
// boundary explicit.
// -----------------------------------------------------------------------------

func (s *CreationServer) QueryKnowledgeGraph(_ context.Context, _ *creationv1.QueryKnowledgeGraphRequest) (*creationv1.QueryKnowledgeGraphResponse, error) {
	return nil, status.Error(codes.Unimplemented, "creation: per-user KG traversal lives in chora-consumption per ADR-143; chora-creation only owns authoring-time atom_semantic_edges")
}

// -----------------------------------------------------------------------------
// SnapshotQuestionByID — chain-break close for chora-delivery's
// QuestionSnapshotter (E2E-INFRA-LEG3-D).
//
// At TestSet.Publish() time chora-delivery calls this RPC to capture the
// canonical MCQ / OE payload from chora_creation.questions into
// chora_delivery.test_set_questions.payload_snapshot. The runtime grading
// path reads the snapshot from chora_delivery only — never touches
// chora_creation per ddd-enforcement #3 (cross-DB queries FORBIDDEN).
//
// Deterministic + idempotent + side-effect-free. NO LLM involvement.
// -----------------------------------------------------------------------------

func (s *CreationServer) SnapshotQuestionByID(ctx context.Context, in *creationv1.SnapshotQuestionByIDRequest) (*creationv1.SnapshotQuestionByIDResponse, error) {
	if s.questions == nil {
		return nil, status.Error(codes.FailedPrecondition, "creation: QuestionRepository not wired (CHORA_DB_DSN unset?)")
	}
	if in == nil || strings.TrimSpace(in.GetQuestionId()) == "" || strings.TrimSpace(in.GetTenantId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "creation: question_id + tenant_id required")
	}

	q, rev, err := s.questions.GetByID(ctx, in.GetTenantId(), in.GetQuestionId())
	if err != nil {
		if errors.Is(err, question.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "creation: question %s not found", in.GetQuestionId())
		}
		return nil, status.Errorf(codes.Internal, "creation: get question: %v", err)
	}
	if q == nil {
		return nil, status.Errorf(codes.NotFound, "creation: question %s not found (nil)", in.GetQuestionId())
	}

	// ADR-229 WS-2 chokepoint 2 (CHO-2133) — the reuse-consent gate at the
	// moment content is exported into another domain's snapshot. Enforced
	// BEFORE any payload marshalling.
	if err := s.enforceSnapshotReuseGate(ctx, in.GetTenantId(), strings.TrimSpace(in.GetCallerGcid()), q.AtomID); err != nil {
		return nil, err
	}

	resp := &creationv1.SnapshotQuestionByIDResponse{
		QuestionId:   q.QuestionID,
		QuestionType: string(q.Type),
		Prompt:       q.Prompt,
	}

	// Discriminated payload — exactly one of mcq / oe per the proto contract.
	//
	// ATOM-1e (2026-05-17) — AUTHOR-SAFE projection per ADR-156 + the
	// atom-phase1-execution-plan-2026-05-17.md §3 lock. The JSON envelope
	// inlines `stem` (the canonical Question.Prompt) at the top level
	// AND preserves the full discriminated payload verbatim:
	//   - MCQ: options[] with is_correct + explainer per option
	//   - OE:  model_answer + rubric (criteria + weights)
	//
	// Why both `resp.Prompt` (proto top-level) AND `stem` (inlined into the
	// JSON envelope): chora-delivery stores `mcq_payload_json` / `oe_payload_json`
	// verbatim into `test_set_questions.payload_snapshot` (B3 commit f3e9530c).
	// The /me/assessments learner-projection reads `stem` from the payload
	// snapshot JSON — without the inline stem, the working canvas would
	// render with no question prompt. The tactical patch at bbdaa828
	// (chora-delivery QuestionClient merging Prompt into JSON as stem)
	// becomes a no-op safety net once this lands.
	//
	// Per `feedback_no_stubs_real_wiring`: we keep `resp.Prompt` populated
	// for backwards compatibility (Option A — minimum blast radius); the
	// tactical patch is the proven fallback path, and we never want to
	// silently downgrade existing client behaviour.
	switch q.Type {
	case question.TypeMCQ:
		payload := preferMCQ(q.MCQ, rev)
		if payload == nil {
			return nil, status.Errorf(codes.FailedPrecondition, "creation: mcq question %s missing payload (corrupt revision)", q.QuestionID)
		}
		b, jerr := marshalAuthorSafeMCQ(q.Prompt, payload)
		if jerr != nil {
			return nil, status.Errorf(codes.Internal, "creation: marshal mcq payload: %v", jerr)
		}
		resp.McqPayloadJson = string(b)
	case question.TypeOpenEnded:
		payload := preferOE(q.OE, rev)
		if payload == nil {
			return nil, status.Errorf(codes.FailedPrecondition, "creation: oe question %s missing payload (corrupt revision)", q.QuestionID)
		}
		b, jerr := marshalAuthorSafeOE(q.Prompt, payload)
		if jerr != nil {
			return nil, status.Errorf(codes.Internal, "creation: marshal oe payload: %v", jerr)
		}
		resp.OePayloadJson = string(b)
	default:
		// Reserved_* types — per the proto, return UNIMPLEMENTED so the
		// chora-delivery client can surface a fail-loud
		// "question type unsupported" to the publisher.
		return nil, status.Errorf(codes.Unimplemented, "creation: question type %q reserved — only mcq + oe in Phyllis scope", string(q.Type))
	}

	// snapshot_at = authoring-side updated_at. chora-delivery stores this in
	// test_set_questions.snapshot_at for audit traceability.
	if !q.UpdatedAt.IsZero() {
		resp.SnapshotAt = timestamppb.New(q.UpdatedAt)
	} else {
		resp.SnapshotAt = timestamppb.New(time.Now().UTC())
	}
	return resp, nil
}

// legacySnapshotCallerOnce rate-limits the migration-window log: exactly one
// loud line per process when a caller_gcid-less (legacy) snapshot request is
// served ungated. Remove together with the empty-caller branch once every
// SnapshotQuestionByID caller threads caller_gcid (chora-delivery WS-2).
var legacySnapshotCallerOnce sync.Once

// enforceSnapshotReuseGate applies the ADR-229 D4.2 predicate
//
//	owner ∨ (tenant-visible ∧ published) ∨ granted
//
// for a caller_gcid-bearing snapshot request, mirroring the picker disjunct
// (chokepoint 1) leg-for-leg:
//
//   - owner           — the author reuses their own atom: allowed, no D2
//     grant (own-atom use is not "reuse").
//   - tenant-visible  — reuse_visibility='tenant' AND status published (a
//     draft/archived atom is not a reusable export). The D2 audit grant
//     (AuthorizeAtomUse, scope TEST_SET, free-license v1) is written
//     idempotently BEFORE serving; a write failure refuses — the audit
//     record is not optional.
//   - granted         — an ACTIVE AtomUsageGrant already covers the atom
//     (read via GetReuseContext; includes grants repointed to orphan
//     editions per A1.1). The grant IS the audit record — no re-write.
//
// Empty callerGCID = legacy caller during the migration window: the
// predicate is skipped and one loud log line records it. Every dependency
// gap (atom row unloadable, fetcher/authorizer unwired) or context-read
// failure REFUSES loud — never an ungated serve, never a silent fallback.
func (s *CreationServer) enforceSnapshotReuseGate(ctx context.Context, tenantID, callerGCID, atomID string) error {
	if callerGCID == "" {
		legacySnapshotCallerOnce.Do(func() {
			log.Printf("creation: SnapshotQuestionByID served WITHOUT caller_gcid — ADR-229 reuse predicate SKIPPED (migration window; upgrade the caller to thread the publish actor)")
		})
		return nil
	}
	if s.atoms == nil {
		return status.Error(codes.FailedPrecondition, "creation: snapshot gate needs AtomRepository (unwired) — refusing gated snapshot")
	}
	la, err := s.atoms.Get(ctx, tenantID, atomID)
	if err != nil {
		if errors.Is(err, atom.ErrNotFound) {
			return status.Errorf(codes.NotFound, "creation: atom %s not found for snapshot gate", atomID)
		}
		return status.Errorf(codes.Internal, "creation: snapshot gate atom load: %v", err)
	}

	// owner — allowed, no grant write.
	if strings.EqualFold(strings.TrimSpace(la.Gcid), callerGCID) {
		return nil
	}

	// tenant-visible ∧ published — allowed AFTER the D2 audit grant lands.
	if la.ReuseVisibility == atom.ReuseTenant && la.Status == atom.StatusPublished {
		if s.atomUse == nil {
			return status.Error(codes.FailedPrecondition, "creation: snapshot gate needs the AtomUse grant writer (SVC_SHARING_GRPC_URL unset?) — refusing non-owner snapshot")
		}
		if err := s.atomUse.AuthorizeTestSetUse(ctx, tenantID, callerGCID, atomID); err != nil {
			return status.Errorf(codes.Internal, "creation: snapshot gate D2 audit grant write failed (refusing — the audit record is not optional): %v", err)
		}
		return nil
	}

	// granted — the caller already holds an ACTIVE grant (any scope).
	if s.reuseContext == nil {
		return status.Error(codes.FailedPrecondition, "creation: snapshot gate needs the reuse-context fetcher (SVC_SHARING_GRPC_URL unset?) — refusing non-owner snapshot")
	}
	rc, err := s.reuseContext.GetReuseContext(ctx, callerGCID, tenantID)
	if err != nil {
		return status.Errorf(codes.Unavailable, "creation: snapshot gate reuse-context read failed (refusing LOUD, never serving ungated): %v", err)
	}
	for _, gid := range rc.GrantedAtomIDs {
		if strings.EqualFold(strings.TrimSpace(gid), atomID) {
			return nil
		}
	}

	return status.Errorf(codes.PermissionDenied,
		"creation: ADR229_REUSE_DENIED atom=%s reuse_visibility=%s status=%s — caller %s is not the author, holds no active grant, and the atom is not tenant-visible (ADR-229 D4.2)",
		atomID, la.ReuseVisibility, la.Status, callerGCID)
}

// -----------------------------------------------------------------------------
// AUTHOR-SAFE envelope marshalling (ATOM-1e)
//
// The envelope wraps the canonical Question.Prompt as `stem` at the top level
// of the JSON payload AND preserves the discriminated payload's grader-
// relevant fields (MCQ: is_correct + explainer per option; OE: model_answer
// + rubric). chora-delivery stores the JSON verbatim; its read-time
// projection split (LEARNER-SAFE vs AUTHOR-SAFE) lives downstream.
// -----------------------------------------------------------------------------

// authorSafeMCQEnvelope is the AUTHOR-SAFE wire shape for MCQ snapshots.
// JSON tag `stem` is inlined at the top level so chora-delivery's
// payload_snapshot consumer can read it without the bbdaa828 safety-net
// merge of resp.Prompt. The remaining fields mirror MCQPayload verbatim.
type authorSafeMCQEnvelope struct {
	Stem         string               `json:"stem"`
	Options      []question.MCQOption `json:"options"`
	XPOnCorrect  int                  `json:"xp_on_correct,omitempty"`
	TimerSeconds int                  `json:"timer_seconds,omitempty"`
	// W8 image-gen persistence — preserve the QUESTION/STEM + MODEL-ANSWER
	// illustration URLs into the test-set snapshot verbatim so they survive
	// into chora_delivery.test_set_questions.payload_snapshot and reach the
	// learner. Pointer+omitempty keeps the no-image snapshot byte-stable.
	// Wire names match MCQPayload json tags exactly.
	ImageURL       *string `json:"image_url,omitempty"`
	AnswerImageURL *string `json:"answer_image_url,omitempty"`
}

// authorSafeOEEnvelope is the AUTHOR-SAFE wire shape for OE snapshots.
//
// JSON tag `stem` is inlined at the top level. ModelAnswer is preserved
// verbatim. Rubric is FLATTENED from the domain's nested
// `{criteria: [...]}` shape into a top-level array of criterion objects per
// the wire contract:
//
//   - chora-contracts/openapi/creation-questions.yaml §OEPayload.rubric
//     (`type: array`)
//   - chora-contracts/openapi/delivery-assessments.yaml
//     §LearnerQuestionGrade.oe_post_grade.rubric (`type: array`)
//   - chora-delivery's `projectAuthorSafeSnapshot` reading
//     `raw["rubric"].([]interface{})` at
//     services/chora-delivery/internal/adapter/http/assessment_handler.go:1468
//
// Each criterion projects to `{criterion_id, description, weight}` where
// `weight` is a float 0..1. The domain stores `WeightPercent` as int 0..100
// for deterministic equality checks (per oe.go §Rubric doc) — the wire
// layer divides by 100.0 to honour the OpenAPI `minimum: 0, maximum: 1`
// constraint.
type authorSafeOEEnvelope struct {
	Stem        string                      `json:"stem"`
	ModelAnswer string                      `json:"model_answer"`
	Rubric      []authorSafeRubricCriterion `json:"rubric,omitempty"`
	// W8 image-gen persistence — preserve the QUESTION/STEM + MODEL-ANSWER
	// illustration URLs into the test-set snapshot verbatim (see
	// authorSafeMCQEnvelope). Wire names match OEPayload json tags exactly.
	ImageURL       *string `json:"image_url,omitempty"`
	AnswerImageURL *string `json:"answer_image_url,omitempty"`
}

// authorSafeRubricCriterion mirrors the OpenAPI `RubricCriterion` shape
// (`criterion_id, title, description, weight`). The domain currently has
// no `title` field on `question.RubricCriterion` (see oe.go) — we omit it
// rather than synthesise a fake. chora-delivery's post-RELEASE projection
// at assessment_handler.go:1476 reads via a key whitelist, so an absent
// `title` is safe (the consumer keeps whatever keys are present).
type authorSafeRubricCriterion struct {
	CriterionID string  `json:"criterion_id"`
	Description string  `json:"description,omitempty"`
	Weight      float64 `json:"weight"`
}

func marshalAuthorSafeMCQ(stem string, p *question.MCQPayload) ([]byte, error) {
	env := authorSafeMCQEnvelope{
		Stem:           stem,
		Options:        p.Options,
		XPOnCorrect:    p.XPOnCorrect,
		TimerSeconds:   p.TimerSeconds,
		ImageURL:       p.ImageURL,
		AnswerImageURL: p.AnswerImageURL,
	}
	return json.Marshal(env)
}

func marshalAuthorSafeOE(stem string, p *question.OEPayload) ([]byte, error) {
	env := authorSafeOEEnvelope{
		Stem:           stem,
		ModelAnswer:    p.ModelAnswer,
		ImageURL:       p.ImageURL,
		AnswerImageURL: p.AnswerImageURL,
	}
	if p.WeightedRubric != nil && len(p.WeightedRubric.Criteria) > 0 {
		env.Rubric = make([]authorSafeRubricCriterion, 0, len(p.WeightedRubric.Criteria))
		for _, c := range p.WeightedRubric.Criteria {
			env.Rubric = append(env.Rubric, authorSafeRubricCriterion{
				CriterionID: c.CriterionID,
				Description: c.Description,
				// Convert int percent 0-100 → float fraction 0-1 per the
				// wire contract (`weight: { type: number, minimum: 0, maximum: 1 }`).
				Weight: float64(c.WeightPercent) / 100.0,
			})
		}
	}
	return json.Marshal(env)
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// atomToProto maps a domain.LearningAtom to the proto Atom projection.
//
// Note: the wire-format AtomType / AtomStatus enums diverge from the
// domain's MVP atom-type strings; we map the MVP types we support and
// fall through to UNSPECIFIED for the rest.
func atomToProto(a *atom.LearningAtom) *creationv1.Atom {
	if a == nil {
		return nil
	}
	// ADR-156 Decision #2 renamed the domain struct field
	// `AtomType` → `QuestionType` (with the type alias `type AtomType =
	// QuestionType` preserved one cycle for callers that still reference
	// the type). The struct-field reference must migrate to the new name;
	// the wire-format proto enum mapping `atomTypeToProto` keeps the same
	// type-alias signature.
	out := &creationv1.Atom{
		AtomId:                a.AtomID,
		TenantId:              a.TenantID,
		AtomType:              atomTypeToProto(a.QuestionType),
		Status:                atomStatusToProto(a.Status),
		Title:                 a.Title,
		AuthorGcid:            a.Gcid,
		Difficulty:            int32(a.Difficulty),
		Locale:                "", // Phyllis MVP doesn't track locale on the atom
		CurrentRevisionNumber: int32(a.Revision),
	}
	if !a.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(a.CreatedAt)
	}
	if !a.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(a.UpdatedAt)
	}
	return out
}

func atomTypeToProto(t atom.AtomType) creationv1.AtomType {
	switch t {
	case atom.TypeMCQ:
		return creationv1.AtomType_ATOM_TYPE_MULTIPLE_CHOICE
	case atom.TypeFlashcard:
		// No FLASHCARD in proto enum — closest neighbour is SHORT_ANSWER.
		return creationv1.AtomType_ATOM_TYPE_SHORT_ANSWER
	case atom.TypeVideo:
		return creationv1.AtomType_ATOM_TYPE_MULTIMEDIA
	case atom.TypeEssay:
		return creationv1.AtomType_ATOM_TYPE_ESSAY
	default:
		return creationv1.AtomType_ATOM_TYPE_UNSPECIFIED
	}
}

func atomStatusToProto(s atom.Status) creationv1.AtomStatus {
	switch s {
	case atom.StatusDraft:
		return creationv1.AtomStatus_ATOM_STATUS_DRAFT
	case atom.StatusPublished:
		return creationv1.AtomStatus_ATOM_STATUS_PUBLISHED
	case atom.StatusArchived:
		return creationv1.AtomStatus_ATOM_STATUS_ARCHIVED
	default:
		return creationv1.AtomStatus_ATOM_STATUS_UNSPECIFIED
	}
}

// preferMCQ returns the payload from the parent Question if set, falling
// back to the latest revision payload (the revision is the source-of-truth
// for the snapshot; parent is a cached projection).
func preferMCQ(parent *question.MCQPayload, rev *question.QuestionRevision) *question.MCQPayload {
	if rev != nil && rev.MCQPayload != nil {
		return rev.MCQPayload
	}
	return parent
}

func preferOE(parent *question.OEPayload, rev *question.QuestionRevision) *question.OEPayload {
	if rev != nil && rev.OEPayload != nil {
		return rev.OEPayload
	}
	return parent
}

// -----------------------------------------------------------------------------
// MintAtomMediaDownloadURL (OT#4 — W8 durable image re-home)
// -----------------------------------------------------------------------------

// MintAtomMediaDownloadURL mints fresh short-lived V4 signed GET URLs for the
// durable atom-media gs:// refs chora-delivery froze in the test-set snapshot.
// Batched + per-URI error isolation: one bad/stale ref returns an error in
// its entry without failing the whole assessment load.
func (s *CreationServer) MintAtomMediaDownloadURL(ctx context.Context, in *creationv1.MintAtomMediaDownloadURLRequest) (*creationv1.MintAtomMediaDownloadURLResponse, error) {
	if s.downloadSigner == nil {
		return nil, status.Error(codes.FailedPrecondition, "creation: atom-media download signer not wired (ATOM_MEDIA_BUCKET unset?)")
	}
	if in == nil || strings.TrimSpace(in.GetTenantId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "creation: tenant_id required")
	}

	resp := &creationv1.MintAtomMediaDownloadURLResponse{
		Urls: make([]*creationv1.MintedMediaURL, 0, len(in.GetGsUris())),
	}
	for _, gsURI := range in.GetGsUris() {
		entry := &creationv1.MintedMediaURL{GsUri: gsURI}
		out, err := s.downloadSigner.SignDownloadURL(ctx, gsURI)
		if err != nil {
			// Per-URI failure — record + continue (do not fail the batch).
			entry.Error = err.Error()
		} else {
			entry.SignedUrl = out.URL
			entry.ExpiresAt = timestamppb.New(out.ExpiresAt)
		}
		resp.Urls = append(resp.Urls, entry)
	}
	return resp, nil
}

// Compile-time conformance check.
var _ creationv1.CreationServer = (*CreationServer)(nil)
