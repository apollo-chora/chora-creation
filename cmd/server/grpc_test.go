// Wave-1 C-FULL TDD coverage for the chora-creation gRPC server registration.
//
// Boots an in-process *grpc.Server on a bufconn listener with the same
// composition cmd/server/main.go performs (Creation server + Health), dials
// it, and round-trips Health/Check + the two RPCs the chain-break close
// depends on: ValidateAtomID (anti-fabrication guard for Familiar cite_atom)
// + SnapshotQuestionByID (the actual chora-delivery QuestionSnapshotter
// chain-break close per E2E-INFRA-LEG3-D).
//
// Per docs/m13/grpc-mass-remediation-2026-05-16.md §3.e the acceptance test
// must: (1) boot a server with the new registrations, (2) dial via a
// ClientConn, (3) hit Health/Check + a domain RPC, (4) assert response.
//
// The bufconn pattern (mirrors chora-identity mana_grpc_bufconn_test.go and
// chora-sharing grpc_test.go) is the closest in-test fidelity to the Cloud
// Service Mesh wire path the chora-delivery QuestionSnapshotter + Wave-2
// chora-gateway BFF cutover will use.
package main_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthgrpc "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/creation/v1"

	creationgrpc "github.com/apollo-chora/chora-creation/internal/adapter/grpc"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

const bufconnSize = 1024 * 1024

// startBufconnCreationServer mirrors the gRPC composition cmd/server/main.go
// performs: Creation server bound + Health bound on the same *grpc.Server.
//
// Returns the dial conn + (optionally) the atom + question repos so tests
// can pre-seed state.
func startBufconnCreationServer(t *testing.T, atoms atom.Repository, questions ports.QuestionRepository) (*grpc.ClientConn, func()) {
	t.Helper()
	lis := bufconn.Listen(bufconnSize)
	srv := grpc.NewServer()

	creationSrv := creationgrpc.NewCreationServer(creationgrpc.Deps{
		Atoms:     atoms,
		Questions: questions,
	})
	creationv1.RegisterCreationServer(srv, creationSrv)

	healthSrv := healthgrpc.NewServer()
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus("chora.services.creation.v1.Creation", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(srv, healthSrv)

	go func() {
		if err := srv.Serve(lis); err != nil {
			t.Logf("bufconn creation server stopped: %v", err)
		}
	}()

	//nolint:staticcheck // bufconn requires the legacy DialContext API.
	conn, err := grpc.DialContext(
		context.Background(),
		"bufconn",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("bufconn dial: %v", err)
	}
	cleanup := func() {
		_ = conn.Close()
		srv.GracefulStop()
		_ = lis.Close()
	}
	return conn, cleanup
}

// TestBufconn_HealthCheck verifies the gRPC Health service is bound and
// reports SERVING for the Creation service. Cloud Service Mesh probe
// routing depends on this contract.
func TestBufconn_HealthCheck(t *testing.T) {
	t.Parallel()
	conn, cleanup := startBufconnCreationServer(t, inmem.NewAtomRepository(), nil)
	defer cleanup()

	client := healthpb.NewHealthClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Generic SERVING for the gRPC server (empty service name).
	resp, err := client.Check(ctx, &healthpb.HealthCheckRequest{Service: ""})
	if err != nil {
		t.Fatalf("Health/Check (default): %v", err)
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("default health status=%v want SERVING", resp.Status)
	}

	// Service-scoped SERVING for the Creation service.
	resp, err = client.Check(ctx, &healthpb.HealthCheckRequest{Service: "chora.services.creation.v1.Creation"})
	if err != nil {
		t.Fatalf("Health/Check (Creation): %v", err)
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("Creation health status=%v want SERVING", resp.Status)
	}
}

// TestBufconn_ValidateAtomID_RoundTrip exercises the anti-fabrication guard
// the Familiar Companion cite_atom plugin will call. Pre-seeds an atom in
// the in-memory repository, then asserts:
//   - existing atom returns exists=true with a current_revision_id
//   - unknown atom_id returns exists=false (NOT NotFound — single-bool wire
//     contract per proto comment, to prevent oracle leaks)
//   - wrong tenant returns exists=false
func TestBufconn_ValidateAtomID_RoundTrip(t *testing.T) {
	t.Parallel()
	repo := inmem.NewAtomRepository()

	const (
		tenantA = "01970000-0000-7000-9000-tenant-aaaaa"
		tenantB = "01970000-0000-7000-9000-tenant-bbbbb"
		alice   = "01970000-0000-7000-9000-000000000001"
	)
	la, err := atom.New(atom.NewParams{
		TenantID: tenantA,
		Gcid:     alice,
		Title:    "Newton's First Law",
		Body:     "An object at rest stays at rest.",
		Mode:     atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("atom.New: %v", err)
	}
	if err := repo.Save(context.Background(), la); err != nil {
		t.Fatalf("repo.Save: %v", err)
	}

	conn, cleanup := startBufconnCreationServer(t, repo, nil)
	defer cleanup()

	client := creationv1.NewCreationClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Existing atom — exists=true.
	resp, err := client.ValidateAtomID(ctx, &creationv1.ValidateAtomIDRequest{
		AtomId:   la.AtomID,
		TenantId: tenantA,
	})
	if err != nil {
		t.Fatalf("ValidateAtomID (existing): %v", err)
	}
	if !resp.GetExists() {
		t.Errorf("expected exists=true for seeded atom, got false")
	}
	if resp.GetCurrentRevisionId() == "" {
		t.Errorf("expected non-empty current_revision_id for seeded atom")
	}

	// Unknown atom_id — exists=false (single-bool wire, no NotFound code).
	resp, err = client.ValidateAtomID(ctx, &creationv1.ValidateAtomIDRequest{
		AtomId:   "01970000-0000-7000-9000-deaddeaddead",
		TenantId: tenantA,
	})
	if err != nil {
		t.Fatalf("ValidateAtomID (unknown): %v", err)
	}
	if resp.GetExists() {
		t.Errorf("expected exists=false for unknown atom_id, got true")
	}

	// Wrong-tenant probe — must NOT leak oracle (return exists=false, NOT NotFound).
	resp, err = client.ValidateAtomID(ctx, &creationv1.ValidateAtomIDRequest{
		AtomId:   la.AtomID,
		TenantId: tenantB,
	})
	if err != nil {
		t.Fatalf("ValidateAtomID (cross-tenant): %v", err)
	}
	if resp.GetExists() {
		t.Errorf("expected exists=false for cross-tenant probe, got true (ORACLE LEAK)")
	}
}

// TestBufconn_SnapshotQuestionByID_RoundTrip exercises the chain-break close
// for chora-delivery's QuestionSnapshotter (E2E-INFRA-LEG3-D). Pre-seeds an
// MCQ Question + its first revision in a fake repo, then asserts the gRPC
// response carries the canonical MCQ payload JSON that chora-delivery will
// stash in chora_delivery.test_set_questions.payload_snapshot.
func TestBufconn_SnapshotQuestionByID_RoundTrip(t *testing.T) {
	t.Parallel()

	const (
		tenantA = "01970000-0000-7000-9000-tenant-aaaaa"
		alice   = "01970000-0000-7000-9000-000000000001"
		atomID  = "01970000-0000-7000-9000-atomatomatom"
	)

	// Seed an MCQ Question.
	mcqPayload := &question.MCQPayload{
		Options: []question.MCQOption{
			{OptionID: "opt-1", Label: "True", IsCorrect: true, Explainer: "By definition of inertia."},
			{OptionID: "opt-2", Label: "False", IsCorrect: false, Explainer: "Inertia is the resistance."},
		},
	}
	q, err := question.New(question.NewParams{
		TenantID:   tenantA,
		AtomID:     atomID,
		AuthorGcid: alice,
		Type:       question.TypeMCQ,
		Prompt:     "Newton's First Law states that an object at rest stays at rest.",
		SourceType: atom.SourceManual,
		MCQ:        mcqPayload,
	})
	if err != nil {
		t.Fatalf("question.New: %v", err)
	}
	rev, err := question.NewRevision(q, q.Prompt, mcqPayload, nil, alice, atom.SourceManual)
	if err != nil {
		t.Fatalf("question.NewRevision: %v", err)
	}

	qrepo := newFakeQuestionRepo()
	if err := qrepo.Save(context.Background(), q, rev); err != nil {
		t.Fatalf("qrepo.Save: %v", err)
	}

	conn, cleanup := startBufconnCreationServer(t, inmem.NewAtomRepository(), qrepo)
	defer cleanup()

	client := creationv1.NewCreationClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Happy path — canonical MCQ payload returned.
	resp, err := client.SnapshotQuestionByID(ctx, &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: q.QuestionID,
		TenantId:   tenantA,
	})
	if err != nil {
		t.Fatalf("SnapshotQuestionByID: %v", err)
	}
	if got := resp.GetQuestionId(); got != q.QuestionID {
		t.Errorf("question_id=%q want %q", got, q.QuestionID)
	}
	if got := resp.GetQuestionType(); got != string(question.TypeMCQ) {
		t.Errorf("question_type=%q want %q", got, string(question.TypeMCQ))
	}
	if got := resp.GetPrompt(); got != q.Prompt {
		t.Errorf("prompt=%q want %q", got, q.Prompt)
	}
	if resp.GetMcqPayloadJson() == "" {
		t.Errorf("mcq_payload_json empty — chora-delivery cannot snapshot")
	}
	if resp.GetOePayloadJson() != "" {
		t.Errorf("oe_payload_json non-empty for MCQ question — discriminator violated")
	}

	// Unknown question_id → NotFound.
	_, err = client.SnapshotQuestionByID(ctx, &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: "01970000-0000-7000-9000-deaddeaddead",
		TenantId:   tenantA,
	})
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.NotFound {
		t.Errorf("expected NotFound for unknown question_id, got err=%v code=%v", err, st.Code())
	}

	// Missing tenant_id → InvalidArgument.
	_, err = client.SnapshotQuestionByID(ctx, &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: q.QuestionID,
		TenantId:   "",
	})
	st, ok = status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument for missing tenant_id, got err=%v code=%v", err, st.Code())
	}
}

// TestBufconn_SnapshotQuestionByID_NotWired verifies the fail-loud contract
// (per feedback_no_stubs_real_wiring) when the QuestionRepository is nil at
// boot — production dev env without the pgx pool. The server still
// REGISTERS the RPC; the call returns FAILED_PRECONDITION so the chora-
// delivery client can distinguish "method exists, dependency missing" from
// "method not registered" (UNIMPLEMENTED).
func TestBufconn_SnapshotQuestionByID_NotWired(t *testing.T) {
	t.Parallel()
	conn, cleanup := startBufconnCreationServer(t, inmem.NewAtomRepository(), nil)
	defer cleanup()

	client := creationv1.NewCreationClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := client.SnapshotQuestionByID(ctx, &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: "01970000-0000-7000-9000-anyanyanyany",
		TenantId:   "01970000-0000-7000-9000-tenant-aaaaa",
	})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.FailedPrecondition {
		t.Errorf("expected FailedPrecondition for nil QuestionRepository, got code=%v err=%v", st.Code(), err)
	}
}

// -----------------------------------------------------------------------------
// fakeQuestionRepo — minimal in-memory ports.QuestionRepository for the test.
//
// Mirrors the shape http/questions_handler_test.go fakeQuestionRepository uses
// — Save persists Q + first revision; GetByID returns Q + latest revision.
// The remaining methods are unused by the gRPC happy-path test but must
// satisfy the interface so the compiler is happy.
// -----------------------------------------------------------------------------

type fakeQuestionRepo struct {
	mu        sync.Mutex
	questions map[string]*question.Question
	revisions map[string][]*question.QuestionRevision
}

func newFakeQuestionRepo() *fakeQuestionRepo {
	return &fakeQuestionRepo{
		questions: map[string]*question.Question{},
		revisions: map[string][]*question.QuestionRevision{},
	}
}

func (r *fakeQuestionRepo) Save(_ context.Context, q *question.Question, rev *question.QuestionRevision) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	q.LatestRevisionID = rev.RevisionID
	r.questions[q.QuestionID] = q
	r.revisions[q.QuestionID] = []*question.QuestionRevision{rev}
	return nil
}

func (r *fakeQuestionRepo) GetByAtomID(_ context.Context, tenantID, atomID string) (*question.Question, *question.QuestionRevision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, q := range r.questions {
		if q.AtomID == atomID && q.TenantID == tenantID && q.DeletedAt == nil {
			revs := r.revisions[q.QuestionID]
			if len(revs) == 0 {
				return nil, nil, question.ErrNotFound
			}
			return q, revs[len(revs)-1], nil
		}
	}
	return nil, nil, question.ErrNotFound
}

func (r *fakeQuestionRepo) GetByID(_ context.Context, tenantID, questionID string) (*question.Question, *question.QuestionRevision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	q, ok := r.questions[questionID]
	if !ok || q.TenantID != tenantID || q.DeletedAt != nil {
		return nil, nil, question.ErrNotFound
	}
	revs := r.revisions[q.QuestionID]
	if len(revs) == 0 {
		return nil, nil, question.ErrNotFound
	}
	return q, revs[len(revs)-1], nil
}

func (r *fakeQuestionRepo) AppendRevision(_ context.Context, rev *question.QuestionRevision) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revisions[rev.QuestionID] = append(r.revisions[rev.QuestionID], rev)
	if q, ok := r.questions[rev.QuestionID]; ok {
		q.LatestRevisionID = rev.RevisionID
		q.Revision = rev.RevisionNumber
	}
	return nil
}

func (r *fakeQuestionRepo) SoftDelete(_ context.Context, tenantID, questionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	q, ok := r.questions[questionID]
	if !ok || q.TenantID != tenantID {
		return question.ErrNotFound
	}
	q.SoftDelete()
	return nil
}

func (r *fakeQuestionRepo) SearchQuestions(_ context.Context, _ question.SearchFilter) ([]question.SearchResult, int, error) {
	return nil, 0, nil
}

// Compile-time conformance check.
var _ ports.QuestionRepository = (*fakeQuestionRepo)(nil)
