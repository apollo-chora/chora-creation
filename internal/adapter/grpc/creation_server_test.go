// creation_server_test.go — gRPC adapter tests for the SnapshotQuestionByID
// AUTHOR-SAFE projection (ATOM-1e — LEG3-D R5 root-cause fix).
//
// Per ADR-156 + atom-phase1-execution-plan-2026-05-17.md §3 ATOM-1e, the
// chora-creation SnapshotQuestionByID RPC must return the AUTHOR-SAFE shape:
// the canonical question prompt inlined into the JSON envelope as `stem`,
// PLUS the full discriminated payload (MCQ: options[] with is_correct +
// explainer per option; OE: model_answer + rubric). chora-delivery stores
// the JSON verbatim into `test_set_questions.payload_snapshot` (B3 commit
// f3e9530c); the read-time projection split into LEARNER-SAFE / AUTHOR-SAFE
// lives in chora-delivery.
//
// Once this server-side change ships:
//   - the tactical patch at bbdaa828 (chora-delivery QuestionClient merging
//     `Prompt` into the JSON as `stem`) becomes a no-op safety net.
//   - the AUTHOR-SAFE projection stored downstream finally contains
//     grader-relevant fields verbatim, so the grading + post-RELEASE
//     learner view both render correctly.
//
// Hexagonal: tests use an in-package fake QuestionRepository to drive the
// adapter without standing up pgx or Postgres. No infrastructure imports.
package creationgrpc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/creation/v1"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// -----------------------------------------------------------------------------
// Fake QuestionRepository — drives the gRPC adapter without pgx.
// -----------------------------------------------------------------------------

// fakeQuestionRepo is a minimal implementation of ports.QuestionRepository
// sized for the SnapshotQuestionByID exercise — only GetByID is exercised
// by the SUT path. The other port methods are unused stubs returning nil
// so the in-package fake satisfies the interface for compile-time checks.
type fakeQuestionRepo struct {
	// keyed by (tenant_id, question_id) -> (Question, latest Revision).
	rows map[string]questionRow
}

type questionRow struct {
	q   *question.Question
	rev *question.QuestionRevision
}

func newFakeQuestionRepo() *fakeQuestionRepo {
	return &fakeQuestionRepo{rows: map[string]questionRow{}}
}

func (f *fakeQuestionRepo) put(tenantID string, q *question.Question, rev *question.QuestionRevision) {
	f.rows[tenantID+":"+q.QuestionID] = questionRow{q: q, rev: rev}
}

func (f *fakeQuestionRepo) Save(_ context.Context, _ *question.Question, _ *question.QuestionRevision) error {
	return nil
}

func (f *fakeQuestionRepo) GetByAtomID(_ context.Context, _, _ string) (*question.Question, *question.QuestionRevision, error) {
	return nil, nil, question.ErrNotFound
}

func (f *fakeQuestionRepo) GetByID(_ context.Context, tenantID, questionID string) (*question.Question, *question.QuestionRevision, error) {
	row, ok := f.rows[tenantID+":"+questionID]
	if !ok {
		return nil, nil, question.ErrNotFound
	}
	return row.q, row.rev, nil
}

func (f *fakeQuestionRepo) AppendRevision(_ context.Context, _ *question.QuestionRevision) error {
	return nil
}

func (f *fakeQuestionRepo) SoftDelete(_ context.Context, _, _ string) error { return nil }

func (f *fakeQuestionRepo) SearchQuestions(_ context.Context, _ question.SearchFilter) ([]question.SearchResult, int, error) {
	return nil, 0, nil
}

// -----------------------------------------------------------------------------
// Helpers — construct a known-good MCQ and OE question with their revisions.
// -----------------------------------------------------------------------------

const (
	testTenantID   = "tenant-A"
	testAuthorGcid = "gcid-author-1"
	testAtomID     = "atom-001"
)

func mustMCQQuestion(t *testing.T) (*question.Question, *question.QuestionRevision) {
	t.Helper()
	payload := &question.MCQPayload{
		Options: []question.MCQOption{
			{OptionID: "opt-A", Label: "Oxygen", IsCorrect: true, Explainer: "Plants release O2 during photosynthesis."},
			{OptionID: "opt-B", Label: "Carbon Dioxide", IsCorrect: false, Explainer: "CO2 is consumed, not released."},
			{OptionID: "opt-C", Label: "Nitrogen", IsCorrect: false, Explainer: "Nitrogen is inert in the cycle."},
		},
	}
	q, err := question.New(question.NewParams{
		TenantID:   testTenantID,
		AtomID:     testAtomID,
		AuthorGcid: testAuthorGcid,
		Type:       question.TypeMCQ,
		Prompt:     "What gas do plants release as byproduct of photosynthesis?",
		SourceType: atom.SourceManual,
		MCQ:        payload,
	})
	if err != nil {
		t.Fatalf("mustMCQQuestion: new: %v", err)
	}
	rev, err := question.NewRevision(q, q.Prompt, payload, nil, q.AuthorGcid, atom.SourceManual)
	if err != nil {
		t.Fatalf("mustMCQQuestion: revision: %v", err)
	}
	q.LatestRevisionID = rev.RevisionID
	q.UpdatedAt = time.Date(2026, 5, 17, 10, 0, 0, 0, time.UTC)
	return q, rev
}

func mustOEQuestion(t *testing.T) (*question.Question, *question.QuestionRevision) {
	t.Helper()
	payload := &question.OEPayload{
		ModelAnswer: "Chlorophyll absorbs photons and uses the energy to split water and synthesise glucose.",
		WeightedRubric: &question.Rubric{
			Criteria: []question.RubricCriterion{
				{CriterionID: "crit-1", Description: "Identifies chlorophyll as the absorber", WeightPercent: 50},
				{CriterionID: "crit-2", Description: "Mentions water splitting OR glucose synthesis", WeightPercent: 50},
			},
		},
	}
	q, err := question.New(question.NewParams{
		TenantID:   testTenantID,
		AtomID:     testAtomID,
		AuthorGcid: testAuthorGcid,
		Type:       question.TypeOpenEnded,
		Prompt:     "Explain in 2-3 sentences how chlorophyll converts light into chemical energy.",
		SourceType: atom.SourceManual,
		OE:         payload,
	})
	if err != nil {
		t.Fatalf("mustOEQuestion: new: %v", err)
	}
	rev, err := question.NewRevision(q, q.Prompt, nil, payload, q.AuthorGcid, atom.SourceManual)
	if err != nil {
		t.Fatalf("mustOEQuestion: revision: %v", err)
	}
	q.LatestRevisionID = rev.RevisionID
	q.UpdatedAt = time.Date(2026, 5, 17, 11, 0, 0, 0, time.UTC)
	return q, rev
}

// -----------------------------------------------------------------------------
// SnapshotQuestionByID — AUTHOR-SAFE projection assertions
// -----------------------------------------------------------------------------

// TestSnapshotQuestionByID_MCQ_IncludesStemInline asserts the AUTHOR-SAFE
// JSON envelope inlines `stem` (the canonical Question.Prompt) at the
// top level of mcq_payload_json — NOT only on the proto top-level
// `prompt` field. The chora-delivery /me/assessments learner projection
// reads `stem` from payload_snapshot JSON, so the server-side wrapper
// must include it verbatim (LEG3-D R5 root-cause fix; tactical patch at
// bbdaa828 becomes a no-op safety net after this lands).
func TestSnapshotQuestionByID_MCQ_IncludesStemInline(t *testing.T) {
	repo := newFakeQuestionRepo()
	q, rev := mustMCQQuestion(t)
	repo.put(testTenantID, q, rev)

	srv := NewCreationServer(Deps{Questions: repo})
	resp, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: q.QuestionID,
		TenantId:   testTenantID,
	})
	if err != nil {
		t.Fatalf("SnapshotQuestionByID: %v", err)
	}
	if resp.GetMcqPayloadJson() == "" {
		t.Fatalf("mcq_payload_json: empty")
	}

	var doc map[string]any
	if err := json.Unmarshal([]byte(resp.GetMcqPayloadJson()), &doc); err != nil {
		t.Fatalf("mcq_payload_json: invalid JSON: %v (raw=%q)", err, resp.GetMcqPayloadJson())
	}
	stem, _ := doc["stem"].(string)
	if stem != q.Prompt {
		t.Errorf("mcq_payload_json.stem = %q; want %q (full=%q)", stem, q.Prompt, resp.GetMcqPayloadJson())
	}
	// The proto top-level Prompt MUST also be populated (backwards-compat with
	// chora-delivery's pre-bbdaa828 client path).
	if resp.GetPrompt() != q.Prompt {
		t.Errorf("resp.Prompt = %q; want %q", resp.GetPrompt(), q.Prompt)
	}
}

// TestSnapshotQuestionByID_MCQ_IncludesIsCorrect asserts the AUTHOR-SAFE
// projection preserves `is_correct: true/false` per option. chora-delivery's
// deterministic MCQ grader at submit time and the post-RELEASE result-page
// projection both depend on `is_correct` being present.
func TestSnapshotQuestionByID_MCQ_IncludesIsCorrect(t *testing.T) {
	repo := newFakeQuestionRepo()
	q, rev := mustMCQQuestion(t)
	repo.put(testTenantID, q, rev)

	srv := NewCreationServer(Deps{Questions: repo})
	resp, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: q.QuestionID,
		TenantId:   testTenantID,
	})
	if err != nil {
		t.Fatalf("SnapshotQuestionByID: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(resp.GetMcqPayloadJson()), &doc); err != nil {
		t.Fatalf("mcq_payload_json: invalid JSON: %v", err)
	}
	opts, _ := doc["options"].([]any)
	if len(opts) != 3 {
		t.Fatalf("options: want 3, got %d (full=%q)", len(opts), resp.GetMcqPayloadJson())
	}
	// Walk the options and confirm at least one is_correct=true is present.
	correctCount := 0
	for i, opt := range opts {
		o, ok := opt.(map[string]any)
		if !ok {
			t.Fatalf("option[%d] not an object", i)
		}
		ic, _ := o["is_correct"].(bool)
		if ic {
			correctCount++
		}
	}
	if correctCount != 1 {
		t.Errorf("options.is_correct=true count = %d; want exactly 1 (full=%q)", correctCount, resp.GetMcqPayloadJson())
	}
}

// TestSnapshotQuestionByID_MCQ_IncludesExplainer asserts the per-option
// explainer text is preserved in the AUTHOR-SAFE projection. Explainers are
// rendered on the post-RELEASE result page to surface "why your answer was
// right/wrong" feedback per design §5.
func TestSnapshotQuestionByID_MCQ_IncludesExplainer(t *testing.T) {
	repo := newFakeQuestionRepo()
	q, rev := mustMCQQuestion(t)
	repo.put(testTenantID, q, rev)

	srv := NewCreationServer(Deps{Questions: repo})
	resp, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: q.QuestionID,
		TenantId:   testTenantID,
	})
	if err != nil {
		t.Fatalf("SnapshotQuestionByID: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(resp.GetMcqPayloadJson()), &doc); err != nil {
		t.Fatalf("mcq_payload_json: invalid JSON: %v", err)
	}
	opts, _ := doc["options"].([]any)
	for i, opt := range opts {
		o, ok := opt.(map[string]any)
		if !ok {
			t.Fatalf("option[%d] not an object", i)
		}
		expl, _ := o["explainer"].(string)
		if expl == "" {
			t.Errorf("option[%d].explainer is empty; AUTHOR-SAFE must preserve per-option explainer (full=%q)", i, resp.GetMcqPayloadJson())
		}
	}
}

// TestSnapshotQuestionByID_OE_IncludesStemInline mirrors the MCQ stem
// inline assertion for the OE branch.
func TestSnapshotQuestionByID_OE_IncludesStemInline(t *testing.T) {
	repo := newFakeQuestionRepo()
	q, rev := mustOEQuestion(t)
	repo.put(testTenantID, q, rev)

	srv := NewCreationServer(Deps{Questions: repo})
	resp, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: q.QuestionID,
		TenantId:   testTenantID,
	})
	if err != nil {
		t.Fatalf("SnapshotQuestionByID: %v", err)
	}
	if resp.GetOePayloadJson() == "" {
		t.Fatalf("oe_payload_json: empty")
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(resp.GetOePayloadJson()), &doc); err != nil {
		t.Fatalf("oe_payload_json: invalid JSON: %v (raw=%q)", err, resp.GetOePayloadJson())
	}
	stem, _ := doc["stem"].(string)
	if stem != q.Prompt {
		t.Errorf("oe_payload_json.stem = %q; want %q (full=%q)", stem, q.Prompt, resp.GetOePayloadJson())
	}
	if resp.GetPrompt() != q.Prompt {
		t.Errorf("resp.Prompt = %q; want %q", resp.GetPrompt(), q.Prompt)
	}
}

// TestSnapshotQuestionByID_OE_IncludesModelAnswer asserts the OE
// `model_answer` is present in the AUTHOR-SAFE JSON. chora-delivery's LLM
// grader and the post-RELEASE result page both depend on it.
func TestSnapshotQuestionByID_OE_IncludesModelAnswer(t *testing.T) {
	repo := newFakeQuestionRepo()
	q, rev := mustOEQuestion(t)
	repo.put(testTenantID, q, rev)

	srv := NewCreationServer(Deps{Questions: repo})
	resp, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: q.QuestionID,
		TenantId:   testTenantID,
	})
	if err != nil {
		t.Fatalf("SnapshotQuestionByID: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(resp.GetOePayloadJson()), &doc); err != nil {
		t.Fatalf("oe_payload_json: invalid JSON: %v", err)
	}
	ma, _ := doc["model_answer"].(string)
	if ma == "" {
		t.Errorf("oe_payload_json.model_answer is empty; AUTHOR-SAFE must preserve model answer (full=%q)", resp.GetOePayloadJson())
	}
}

// TestSnapshotQuestionByID_OE_IncludesRubric asserts the weighted rubric
// survives into the AUTHOR-SAFE projection AS A FLAT JSON ARRAY of criterion
// objects per:
//
//   - chora-contracts/openapi/creation-questions.yaml §OEPayload.rubric (`type: array`)
//   - chora-contracts/openapi/delivery-assessments.yaml §LearnerQuestionGrade.oe_post_grade.rubric (`type: array`)
//   - chora-delivery's projectAuthorSafeSnapshot reading `raw["rubric"].([]interface{})`
//     (services/chora-delivery/internal/adapter/http/assessment_handler.go:1468)
//
// Each criterion projects to `{criterion_id, description, weight}` where
// `weight` is a float 0..1 (NOT the domain's integer `WeightPercent`). The
// chora-delivery LLM grader uses the array to scaffold its scoring prompt;
// the post-RELEASE FE result page renders it directly to the learner.
func TestSnapshotQuestionByID_OE_IncludesRubric(t *testing.T) {
	repo := newFakeQuestionRepo()
	q, rev := mustOEQuestion(t)
	repo.put(testTenantID, q, rev)

	srv := NewCreationServer(Deps{Questions: repo})
	resp, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: q.QuestionID,
		TenantId:   testTenantID,
	})
	if err != nil {
		t.Fatalf("SnapshotQuestionByID: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(resp.GetOePayloadJson()), &doc); err != nil {
		t.Fatalf("oe_payload_json: invalid JSON: %v", err)
	}
	// Flat array per the OpenAPI + chora-delivery consumer contract.
	rubric, ok := doc["rubric"].([]any)
	if !ok {
		t.Fatalf("oe_payload_json.rubric: want []any (flat array), got %T (full=%q)", doc["rubric"], resp.GetOePayloadJson())
	}
	if len(rubric) != 2 {
		t.Errorf("oe_payload_json.rubric length = %d; want 2 (full=%q)", len(rubric), resp.GetOePayloadJson())
	}
	for i, c := range rubric {
		cm, ok := c.(map[string]any)
		if !ok {
			t.Fatalf("rubric[%d] not an object", i)
		}
		// criterion_id present
		cid, _ := cm["criterion_id"].(string)
		if cid == "" {
			t.Errorf("rubric[%d].criterion_id empty (full=%q)", i, resp.GetOePayloadJson())
		}
		// description present
		desc, _ := cm["description"].(string)
		if desc == "" {
			t.Errorf("rubric[%d].description empty (full=%q)", i, resp.GetOePayloadJson())
		}
		// weight is a JSON number in [0,1] — domain WeightPercent (int 0-100)
		// MUST be converted to fractional weight on the wire. Per OpenAPI
		// `RubricCriterion.weight: { type: number, minimum: 0, maximum: 1 }`.
		w, ok := cm["weight"].(float64)
		if !ok {
			t.Fatalf("rubric[%d].weight: want number, got %T (full=%q)", i, cm["weight"], resp.GetOePayloadJson())
		}
		if w < 0 || w > 1 {
			t.Errorf("rubric[%d].weight = %v; want in [0,1] per OpenAPI (full=%q)", i, w, resp.GetOePayloadJson())
		}
	}
	// Both rubric criteria in mustOEQuestion are 50% — they should sum to 1.0
	// after the fractional conversion.
	var weightSum float64
	for _, c := range rubric {
		cm, _ := c.(map[string]any)
		if w, ok := cm["weight"].(float64); ok {
			weightSum += w
		}
	}
	if weightSum < 0.999 || weightSum > 1.001 {
		t.Errorf("rubric weight sum = %v; want ≈1.0 (full=%q)", weightSum, resp.GetOePayloadJson())
	}
}

// TestSnapshotQuestionByID_OE_RubricMatchesConsumerProjection is the
// END-TO-END contract assertion: feed the producer JSON through the
// chora-delivery `projectAuthorSafeSnapshot` shape that the OE post-RELEASE
// branch in assessment_handler.go (`proj["rubric"].([]map[string]interface{})`,
// services/chora-delivery/internal/adapter/http/assessment_handler.go:1126)
// is allowed to type-assert. If this test passes, the wire shape is
// guaranteed to flow through to the learner's result page without the
// tactical patch at bbdaa828 doing any heavy lifting.
func TestSnapshotQuestionByID_OE_RubricMatchesConsumerProjection(t *testing.T) {
	repo := newFakeQuestionRepo()
	q, rev := mustOEQuestion(t)
	repo.put(testTenantID, q, rev)

	srv := NewCreationServer(Deps{Questions: repo})
	resp, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: q.QuestionID,
		TenantId:   testTenantID,
	})
	if err != nil {
		t.Fatalf("SnapshotQuestionByID: %v", err)
	}
	// Mirror the chora-delivery projectAuthorSafeSnapshot path verbatim
	// (services/chora-delivery/internal/adapter/http/assessment_handler.go
	// :1413-1486) — it pulls the JSON, unmarshals into a `map[string]any`,
	// then asserts `raw["rubric"].([]interface{})`. If that assertion fails
	// the FE result page silently drops the rubric reveal.
	var raw map[string]any
	if err := json.Unmarshal([]byte(resp.GetOePayloadJson()), &raw); err != nil {
		t.Fatalf("consumer-side unmarshal: %v", err)
	}
	rub, ok := raw["rubric"].([]any)
	if !ok {
		t.Fatalf("chora-delivery consumer assertion `raw[\"rubric\"].([]interface{})` would FAIL: got %T — FE result page would drop the rubric reveal (full=%q)", raw["rubric"], resp.GetOePayloadJson())
	}
	if len(rub) == 0 {
		t.Errorf("rubric array empty — consumer would render no rubric criteria (full=%q)", resp.GetOePayloadJson())
	}
}

// -----------------------------------------------------------------------------
// SnapshotQuestionByID — fail-loud branches (existing behaviour preserved)
// -----------------------------------------------------------------------------

func TestSnapshotQuestionByID_NotFoundMapsToNotFound(t *testing.T) {
	repo := newFakeQuestionRepo()
	srv := NewCreationServer(Deps{Questions: repo})
	_, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: "missing-q",
		TenantId:   testTenantID,
	})
	if err == nil {
		t.Fatal("want error for missing question, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.NotFound {
		t.Errorf("code = %v; want NotFound", st.Code())
	}
}

func TestSnapshotQuestionByID_NoRepoWiredFailsPrecondition(t *testing.T) {
	srv := NewCreationServer(Deps{Questions: nil})
	_, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: "q-1",
		TenantId:   testTenantID,
	})
	if err == nil {
		t.Fatal("want error for nil repo, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.FailedPrecondition {
		t.Errorf("code = %v; want FailedPrecondition", st.Code())
	}
}

func TestSnapshotQuestionByID_InvalidArgRejected(t *testing.T) {
	repo := newFakeQuestionRepo()
	srv := NewCreationServer(Deps{Questions: repo})
	cases := []struct {
		name string
		req  *creationv1.SnapshotQuestionByIDRequest
	}{
		{"nil_request", nil},
		{"empty_question_id", &creationv1.SnapshotQuestionByIDRequest{TenantId: testTenantID}},
		{"empty_tenant_id", &creationv1.SnapshotQuestionByIDRequest{QuestionId: "q-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := srv.SnapshotQuestionByID(context.Background(), tc.req)
			if err == nil {
				t.Fatal("want error, got nil")
			}
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("expected gRPC status error, got %v", err)
			}
			if st.Code() != codes.InvalidArgument {
				t.Errorf("code = %v; want InvalidArgument", st.Code())
			}
		})
	}
}

// TestSnapshotQuestionByID_SnapshotAtPopulated asserts the snapshot_at
// timestamp is set from the question's UpdatedAt for audit traceability
// on the chora-delivery side (stored in test_set_questions.snapshot_at).
func TestSnapshotQuestionByID_SnapshotAtPopulated(t *testing.T) {
	repo := newFakeQuestionRepo()
	q, rev := mustMCQQuestion(t)
	repo.put(testTenantID, q, rev)

	srv := NewCreationServer(Deps{Questions: repo})
	resp, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: q.QuestionID,
		TenantId:   testTenantID,
	})
	if err != nil {
		t.Fatalf("SnapshotQuestionByID: %v", err)
	}
	if resp.GetSnapshotAt() == nil {
		t.Fatal("snapshot_at: nil")
	}
	got := resp.GetSnapshotAt().AsTime()
	if !got.Equal(q.UpdatedAt) {
		t.Errorf("snapshot_at = %v; want %v", got, q.UpdatedAt)
	}
}

// -----------------------------------------------------------------------------
// ADR-229 WS-2 (CHO-2133) — chokepoint 2: SnapshotQuestionByID reuse gate.
//
// caller_gcid (field 3, additive) drives the consent predicate
// owner ∨ (tenant-visible ∧ published) ∨ granted. Empty caller_gcid =
// legacy caller during the migration window (predicate skipped, loud-logged
// once). An allowed NON-OWNER tenant-visible snapshot writes the D2 audit
// grant (AuthorizeAtomUse, scope TEST_SET) BEFORE serving; a granted pass
// means the grant already exists (no re-write). Every dependency gap or
// context-read failure REFUSES loud — never an ungated serve.
// -----------------------------------------------------------------------------

type fakeAtomRepoG struct {
	atoms map[string]*atom.LearningAtom // key tenant:atomID
}

func newFakeAtomRepoG() *fakeAtomRepoG { return &fakeAtomRepoG{atoms: map[string]*atom.LearningAtom{}} }

func (f *fakeAtomRepoG) put(a *atom.LearningAtom) { f.atoms[a.TenantID+":"+a.AtomID] = a }

func (f *fakeAtomRepoG) Save(_ context.Context, _ *atom.LearningAtom) error { return nil }
func (f *fakeAtomRepoG) Get(_ context.Context, tenantID, atomID string) (*atom.LearningAtom, error) {
	a, ok := f.atoms[tenantID+":"+atomID]
	if !ok {
		return nil, atom.ErrNotFound
	}
	return a, nil
}
func (f *fakeAtomRepoG) List(_ context.Context, _ string, _ atom.ListFilter) ([]*atom.LearningAtom, error) {
	return nil, nil
}
func (f *fakeAtomRepoG) ListByCourse(_ context.Context, _, _ string) ([]*atom.LearningAtom, error) {
	return nil, nil
}

type fakeReuseCtxFetcher struct {
	granted []string
	err     error
	calls   int
}

func (f *fakeReuseCtxFetcher) GetReuseContext(_ context.Context, _, _ string) (ports.ReuseContext, error) {
	f.calls++
	if f.err != nil {
		return ports.ReuseContext{}, f.err
	}
	return ports.ReuseContext{GrantedAtomIDs: f.granted}, nil
}

type fakeAtomUseAuthorizer struct {
	err        error
	calls      int
	gotTenant  string
	gotGrantee string
	gotAtomID  string
}

func (f *fakeAtomUseAuthorizer) AuthorizeTestSetUse(_ context.Context, tenantID, granteeGCID, atomID string) error {
	f.calls++
	f.gotTenant = tenantID
	f.gotGrantee = granteeGCID
	f.gotAtomID = atomID
	return f.err
}

// snapshotGateFixture seeds one MCQ question whose parent atom carries the
// given reuse_visibility + status, and returns a server wired with the
// supplied consent deps.
func snapshotGateFixture(t *testing.T, reuseVis atom.ReuseVisibility, st atom.Status, fetcher *fakeReuseCtxFetcher, authorizer *fakeAtomUseAuthorizer) (*CreationServer, string) {
	t.Helper()
	repo := newFakeQuestionRepo()
	q, rev := mustMCQQuestion(t)
	repo.put(testTenantID, q, rev)
	atoms := newFakeAtomRepoG()
	atoms.put(&atom.LearningAtom{
		AtomID:          testAtomID,
		TenantID:        testTenantID,
		Gcid:            testAuthorGcid,
		Status:          st,
		ReuseVisibility: reuseVis,
	})
	deps := Deps{Questions: repo, Atoms: atoms}
	if fetcher != nil {
		deps.ReuseContext = fetcher
	}
	if authorizer != nil {
		deps.AtomUse = authorizer
	}
	return NewCreationServer(deps), q.QuestionID
}

const testCallerGcid = "gcid-caller-2"

func TestSnapshotQuestionByID_LegacyNoCaller_ServesUngated(t *testing.T) {
	// Migration window: an empty caller_gcid skips the predicate even with
	// NO consent deps wired (chora-delivery rolls after chora-creation).
	repo := newFakeQuestionRepo()
	q, rev := mustMCQQuestion(t)
	repo.put(testTenantID, q, rev)
	srv := NewCreationServer(Deps{Questions: repo})

	resp, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: q.QuestionID,
		TenantId:   testTenantID,
	})
	if err != nil {
		t.Fatalf("legacy snapshot must serve ungated during the migration window: %v", err)
	}
	if resp.GetMcqPayloadJson() == "" {
		t.Errorf("expected payload for legacy caller")
	}
}

func TestSnapshotQuestionByID_Owner_Allowed_NoGrantWritten(t *testing.T) {
	fetcher := &fakeReuseCtxFetcher{}
	authorizer := &fakeAtomUseAuthorizer{}
	srv, qID := snapshotGateFixture(t, atom.ReusePrivate, atom.StatusPublished, fetcher, authorizer)

	resp, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: qID,
		TenantId:   testTenantID,
		CallerGcid: testAuthorGcid, // the author
	})
	if err != nil {
		t.Fatalf("owner snapshot: %v", err)
	}
	if resp.GetMcqPayloadJson() == "" {
		t.Errorf("expected payload for owner")
	}
	if authorizer.calls != 0 {
		t.Errorf("own-atom use is not reuse — no D2 grant write; got %d calls", authorizer.calls)
	}
	if fetcher.calls != 0 {
		t.Errorf("owner pass must not need the reuse context; got %d calls", fetcher.calls)
	}
}

func TestSnapshotQuestionByID_TenantVisible_WritesAuditGrantThenServes(t *testing.T) {
	fetcher := &fakeReuseCtxFetcher{}
	authorizer := &fakeAtomUseAuthorizer{}
	srv, qID := snapshotGateFixture(t, atom.ReuseTenant, atom.StatusPublished, fetcher, authorizer)

	resp, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: qID,
		TenantId:   testTenantID,
		CallerGcid: testCallerGcid,
	})
	if err != nil {
		t.Fatalf("tenant-visible snapshot: %v", err)
	}
	if resp.GetMcqPayloadJson() == "" {
		t.Errorf("expected payload")
	}
	if authorizer.calls != 1 {
		t.Fatalf("D2 audit grant must be written exactly once; got %d", authorizer.calls)
	}
	if authorizer.gotTenant != testTenantID || authorizer.gotGrantee != testCallerGcid || authorizer.gotAtomID != testAtomID {
		t.Errorf("grant written with (%s,%s,%s); want (%s,%s,%s)",
			authorizer.gotTenant, authorizer.gotGrantee, authorizer.gotAtomID,
			testTenantID, testCallerGcid, testAtomID)
	}
}

func TestSnapshotQuestionByID_TenantVisible_GrantWriteFailure_Refuses(t *testing.T) {
	fetcher := &fakeReuseCtxFetcher{}
	authorizer := &fakeAtomUseAuthorizer{err: context.DeadlineExceeded}
	srv, qID := snapshotGateFixture(t, atom.ReuseTenant, atom.StatusPublished, fetcher, authorizer)

	_, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: qID,
		TenantId:   testTenantID,
		CallerGcid: testCallerGcid,
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v (err=%v); want Internal — the D2 audit record is not optional", status.Code(err), err)
	}
}

func TestSnapshotQuestionByID_TenantVisible_AuthorizerUnwired_Refuses(t *testing.T) {
	fetcher := &fakeReuseCtxFetcher{}
	srv, qID := snapshotGateFixture(t, atom.ReuseTenant, atom.StatusPublished, fetcher, nil)

	_, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: qID,
		TenantId:   testTenantID,
		CallerGcid: testCallerGcid,
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code = %v (err=%v); want FailedPrecondition (unwired grant writer = fail closed)", status.Code(err), err)
	}
}

func TestSnapshotQuestionByID_PrivateGranted_Allowed_NoRewrite(t *testing.T) {
	fetcher := &fakeReuseCtxFetcher{granted: []string{testAtomID}}
	authorizer := &fakeAtomUseAuthorizer{}
	srv, qID := snapshotGateFixture(t, atom.ReusePrivate, atom.StatusPublished, fetcher, authorizer)

	resp, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: qID,
		TenantId:   testTenantID,
		CallerGcid: testCallerGcid,
	})
	if err != nil {
		t.Fatalf("granted snapshot: %v", err)
	}
	if resp.GetMcqPayloadJson() == "" {
		t.Errorf("expected payload")
	}
	if fetcher.calls != 1 {
		t.Errorf("reuse context reads = %d; want 1", fetcher.calls)
	}
	if authorizer.calls != 0 {
		t.Errorf("granted pass means the grant exists — no re-write; got %d", authorizer.calls)
	}
}

func TestSnapshotQuestionByID_PrivateNotGranted_PermissionDenied(t *testing.T) {
	fetcher := &fakeReuseCtxFetcher{}
	authorizer := &fakeAtomUseAuthorizer{}
	srv, qID := snapshotGateFixture(t, atom.ReusePrivate, atom.StatusPublished, fetcher, authorizer)

	_, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: qID,
		TenantId:   testTenantID,
		CallerGcid: testCallerGcid,
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %v (err=%v); want PermissionDenied", status.Code(err), err)
	}
	// Discriminated taxonomy: the message names the gate + visibility so the
	// publisher can render an actionable refusal.
	msg := status.Convert(err).Message()
	for _, want := range []string{"ADR229_REUSE_DENIED", "private", testAtomID} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}
	if authorizer.calls != 0 {
		t.Errorf("denied snapshot must not write a grant; got %d", authorizer.calls)
	}
}

func TestSnapshotQuestionByID_TenantVisibleDraft_DeniedViaTenantLeg(t *testing.T) {
	// A draft is not a reusable export — the tenant-visible leg requires
	// published (mirrors the picker disjunct). No grant, so deny.
	fetcher := &fakeReuseCtxFetcher{}
	authorizer := &fakeAtomUseAuthorizer{}
	srv, qID := snapshotGateFixture(t, atom.ReuseTenant, atom.StatusDraft, fetcher, authorizer)

	_, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: qID,
		TenantId:   testTenantID,
		CallerGcid: testCallerGcid,
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %v (err=%v); want PermissionDenied for a non-owner draft snapshot", status.Code(err), err)
	}
	if authorizer.calls != 0 {
		t.Errorf("no grant write on deny; got %d", authorizer.calls)
	}
}

func TestSnapshotQuestionByID_ReuseContextError_Unavailable(t *testing.T) {
	fetcher := &fakeReuseCtxFetcher{err: context.DeadlineExceeded}
	authorizer := &fakeAtomUseAuthorizer{}
	srv, qID := snapshotGateFixture(t, atom.ReusePrivate, atom.StatusPublished, fetcher, authorizer)

	_, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: qID,
		TenantId:   testTenantID,
		CallerGcid: testCallerGcid,
	})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code = %v (err=%v); want Unavailable — sharing outage refuses LOUD, never serves ungated", status.Code(err), err)
	}
}

func TestSnapshotQuestionByID_ReuseContextUnwired_Refuses(t *testing.T) {
	authorizer := &fakeAtomUseAuthorizer{}
	srv, qID := snapshotGateFixture(t, atom.ReusePrivate, atom.StatusPublished, nil, authorizer)

	_, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: qID,
		TenantId:   testTenantID,
		CallerGcid: testCallerGcid,
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code = %v (err=%v); want FailedPrecondition (unwired consent context = fail closed)", status.Code(err), err)
	}
}

func TestSnapshotQuestionByID_CallerSet_AtomRepoUnwired_Refuses(t *testing.T) {
	// A gated request cannot be evaluated without the atom row (author +
	// visibility live there) — refuse, never serve ungated.
	repo := newFakeQuestionRepo()
	q, rev := mustMCQQuestion(t)
	repo.put(testTenantID, q, rev)
	srv := NewCreationServer(Deps{Questions: repo})

	_, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: q.QuestionID,
		TenantId:   testTenantID,
		CallerGcid: testCallerGcid,
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code = %v (err=%v); want FailedPrecondition", status.Code(err), err)
	}
}
