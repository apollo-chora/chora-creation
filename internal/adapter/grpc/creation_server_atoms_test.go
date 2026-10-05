// creation_server_atoms_test.go — gRPC adapter tests for the atom-surface
// RPCs (CreateAtom / GetAtom / ValidateAtomID / ListAtomsByCourse), the
// intentionally-UNIMPLEMENTED RPCs (GenerateAtomsViaAI / AppendRevision /
// QueryKnowledgeGraph), and the domain→proto enum mapping helpers.
//
// Same harness style as creation_server_test.go: in-package fakes drive the
// adapter without pgx or Postgres. No infrastructure imports.
package creationgrpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/creation/v1"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

// -----------------------------------------------------------------------------
// Fake atom.Repository — drives the atom RPCs with per-method error injection.
// Distinct from fakeAtomRepoG (snapshot-gate fixture) because these tests need
// ListByCourse results + injected failures on Save/Get/ListByCourse.
// -----------------------------------------------------------------------------

type fakeAtomStore struct {
	saved         []*atom.LearningAtom
	byID          map[string]*atom.LearningAtom // key tenant:atomID
	byCourse      []*atom.LearningAtom
	saveErr       error
	getErr        error
	listCourseErr error
}

func newFakeAtomStore() *fakeAtomStore { return &fakeAtomStore{byID: map[string]*atom.LearningAtom{}} }

func (f *fakeAtomStore) put(a *atom.LearningAtom) { f.byID[a.TenantID+":"+a.AtomID] = a }

func (f *fakeAtomStore) Save(_ context.Context, a *atom.LearningAtom) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, a)
	return nil
}

func (f *fakeAtomStore) Get(_ context.Context, tenantID, atomID string) (*atom.LearningAtom, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	a, ok := f.byID[tenantID+":"+atomID]
	if !ok {
		return nil, atom.ErrNotFound
	}
	return a, nil
}

func (f *fakeAtomStore) List(_ context.Context, _ string, _ atom.ListFilter) ([]*atom.LearningAtom, error) {
	return nil, nil
}

func (f *fakeAtomStore) ListByCourse(_ context.Context, _, _ string) ([]*atom.LearningAtom, error) {
	if f.listCourseErr != nil {
		return nil, f.listCourseErr
	}
	return f.byCourse, nil
}

// mustStoredAtom builds a known-good LearningAtom via the domain constructor
// (DRAFT status, Revision 1, private reuse visibility).
func mustStoredAtom(t *testing.T, tenantID, gcid, title string) *atom.LearningAtom {
	t.Helper()
	la, err := atom.New(atom.NewParams{
		TenantID: tenantID,
		Gcid:     gcid,
		Title:    title,
		Mode:     atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("mustStoredAtom: %v", err)
	}
	return la
}

// -----------------------------------------------------------------------------
// CreateAtom
// -----------------------------------------------------------------------------

func TestCreateAtom_HappyPath_SavesDraftAtom(t *testing.T) {
	store := newFakeAtomStore()
	srv := NewCreationServer(Deps{Atoms: store})

	resp, err := srv.CreateAtom(context.Background(), &creationv1.CreateAtomRequest{
		TenantId:   testTenantID,
		AuthorGcid: testAuthorGcid,
		Title:      "Photosynthesis basics",
	})
	if err != nil {
		t.Fatalf("CreateAtom: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("saved atoms = %d; want 1", len(store.saved))
	}
	saved := store.saved[0]
	if saved.Status != atom.StatusDraft {
		t.Errorf("saved status = %q; want draft", saved.Status)
	}
	if saved.Mode != atom.ModeStraightUp {
		t.Errorf("saved mode = %q; want %q", saved.Mode, atom.ModeStraightUp)
	}
	got := resp.GetAtom()
	if got == nil {
		t.Fatal("resp.Atom: nil")
	}
	if got.GetAtomId() != saved.AtomID {
		t.Errorf("resp.Atom.atom_id = %q; want %q", got.GetAtomId(), saved.AtomID)
	}
	if got.GetTenantId() != testTenantID || got.GetAuthorGcid() != testAuthorGcid {
		t.Errorf("resp.Atom tenant/author = (%q,%q); want (%q,%q)",
			got.GetTenantId(), got.GetAuthorGcid(), testTenantID, testAuthorGcid)
	}
	if got.GetTitle() != "Photosynthesis basics" {
		t.Errorf("resp.Atom.title = %q", got.GetTitle())
	}
	if got.GetStatus() != creationv1.AtomStatus_ATOM_STATUS_DRAFT {
		t.Errorf("resp.Atom.status = %v; want DRAFT", got.GetStatus())
	}
	if got.GetCurrentRevisionNumber() != 1 {
		t.Errorf("resp.Atom.current_revision_number = %d; want 1", got.GetCurrentRevisionNumber())
	}
	if got.GetCreatedAt() == nil || got.GetUpdatedAt() == nil {
		t.Errorf("resp.Atom timestamps must be populated (created_at=%v updated_at=%v)", got.GetCreatedAt(), got.GetUpdatedAt())
	}
}

func TestCreateAtom_NoRepoWiredFailsPrecondition(t *testing.T) {
	srv := NewCreationServer(Deps{Atoms: nil})
	_, err := srv.CreateAtom(context.Background(), &creationv1.CreateAtomRequest{
		TenantId:   testTenantID,
		AuthorGcid: testAuthorGcid,
		Title:      "t",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code = %v (err=%v); want FailedPrecondition", status.Code(err), err)
	}
}

func TestCreateAtom_InvalidArgRejected(t *testing.T) {
	srv := NewCreationServer(Deps{Atoms: newFakeAtomStore()})
	cases := []struct {
		name string
		req  *creationv1.CreateAtomRequest
	}{
		{"nil_request", nil},
		{"empty_tenant_id", &creationv1.CreateAtomRequest{AuthorGcid: testAuthorGcid, Title: "t"}},
		{"empty_author_gcid", &creationv1.CreateAtomRequest{TenantId: testTenantID, Title: "t"}},
		// Domain invariant violation surfaces as InvalidArgument via atom.New.
		{"empty_title", &creationv1.CreateAtomRequest{TenantId: testTenantID, AuthorGcid: testAuthorGcid}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := srv.CreateAtom(context.Background(), tc.req)
			if status.Code(err) != codes.InvalidArgument {
				t.Errorf("code = %v (err=%v); want InvalidArgument", status.Code(err), err)
			}
		})
	}
}

func TestCreateAtom_SaveFailureMapsToInternal(t *testing.T) {
	store := newFakeAtomStore()
	store.saveErr = errors.New("pg down")
	srv := NewCreationServer(Deps{Atoms: store})

	_, err := srv.CreateAtom(context.Background(), &creationv1.CreateAtomRequest{
		TenantId:   testTenantID,
		AuthorGcid: testAuthorGcid,
		Title:      "t",
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v (err=%v); want Internal", status.Code(err), err)
	}
}

// -----------------------------------------------------------------------------
// GetAtom
// -----------------------------------------------------------------------------

func TestGetAtom_HappyPath_ReturnsAtom(t *testing.T) {
	store := newFakeAtomStore()
	la := mustStoredAtom(t, testTenantID, testAuthorGcid, "Cell structure")
	store.put(la)
	srv := NewCreationServer(Deps{Atoms: store})

	resp, err := srv.GetAtom(context.Background(), &creationv1.GetAtomRequest{
		TenantId: testTenantID,
		AtomId:   la.AtomID,
	})
	if err != nil {
		t.Fatalf("GetAtom: %v", err)
	}
	got := resp.GetAtom()
	if got == nil {
		t.Fatal("resp.Atom: nil")
	}
	if got.GetAtomId() != la.AtomID || got.GetTitle() != la.Title {
		t.Errorf("resp.Atom = (%q,%q); want (%q,%q)", got.GetAtomId(), got.GetTitle(), la.AtomID, la.Title)
	}
}

func TestGetAtom_NoRepoWiredFailsPrecondition(t *testing.T) {
	srv := NewCreationServer(Deps{Atoms: nil})
	_, err := srv.GetAtom(context.Background(), &creationv1.GetAtomRequest{
		TenantId: testTenantID,
		AtomId:   "atom-1",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code = %v (err=%v); want FailedPrecondition", status.Code(err), err)
	}
}

func TestGetAtom_InvalidArgRejected(t *testing.T) {
	srv := NewCreationServer(Deps{Atoms: newFakeAtomStore()})
	cases := []struct {
		name string
		req  *creationv1.GetAtomRequest
	}{
		{"nil_request", nil},
		{"empty_atom_id", &creationv1.GetAtomRequest{TenantId: testTenantID}},
		{"empty_tenant_id", &creationv1.GetAtomRequest{AtomId: "atom-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := srv.GetAtom(context.Background(), tc.req)
			if status.Code(err) != codes.InvalidArgument {
				t.Errorf("code = %v (err=%v); want InvalidArgument", status.Code(err), err)
			}
		})
	}
}

func TestGetAtom_NotFoundMapsToNotFound(t *testing.T) {
	srv := NewCreationServer(Deps{Atoms: newFakeAtomStore()})
	_, err := srv.GetAtom(context.Background(), &creationv1.GetAtomRequest{
		TenantId: testTenantID,
		AtomId:   "missing",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v (err=%v); want NotFound", status.Code(err), err)
	}
}

func TestGetAtom_RepoFailureMapsToInternal(t *testing.T) {
	store := newFakeAtomStore()
	store.getErr = errors.New("pg down")
	srv := NewCreationServer(Deps{Atoms: store})

	_, err := srv.GetAtom(context.Background(), &creationv1.GetAtomRequest{
		TenantId: testTenantID,
		AtomId:   "atom-1",
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v (err=%v); want Internal", status.Code(err), err)
	}
}

// -----------------------------------------------------------------------------
// ValidateAtomID — anti-fabrication guard (single bool false on the wire for
// unknown / wrong-tenant atoms; no NotFound oracle leak).
// -----------------------------------------------------------------------------

func TestValidateAtomID_KnownAtom_ExistsWithRevisionPointer(t *testing.T) {
	store := newFakeAtomStore()
	la := mustStoredAtom(t, testTenantID, testAuthorGcid, "Osmosis")
	store.put(la)
	srv := NewCreationServer(Deps{Atoms: store})

	resp, err := srv.ValidateAtomID(context.Background(), &creationv1.ValidateAtomIDRequest{
		TenantId: testTenantID,
		AtomId:   la.AtomID,
	})
	if err != nil {
		t.Fatalf("ValidateAtomID: %v", err)
	}
	if !resp.GetExists() {
		t.Error("exists = false; want true for a known atom")
	}
	// The revision pointer is the atom_id itself (caller cites the latest).
	if resp.GetCurrentRevisionId() != la.AtomID {
		t.Errorf("current_revision_id = %q; want %q", resp.GetCurrentRevisionId(), la.AtomID)
	}
}

func TestValidateAtomID_UnknownAtom_FalseNoError(t *testing.T) {
	srv := NewCreationServer(Deps{Atoms: newFakeAtomStore()})
	resp, err := srv.ValidateAtomID(context.Background(), &creationv1.ValidateAtomIDRequest{
		TenantId: testTenantID,
		AtomId:   "fabricated",
	})
	if err != nil {
		t.Fatalf("unknown atom must NOT surface an error (oracle leak): %v", err)
	}
	if resp.GetExists() {
		t.Error("exists = true; want false for an unknown atom")
	}
	if resp.GetCurrentRevisionId() != "" {
		t.Errorf("current_revision_id = %q; want empty for an unknown atom", resp.GetCurrentRevisionId())
	}
}

func TestValidateAtomID_NoRepoWiredFailsPrecondition(t *testing.T) {
	srv := NewCreationServer(Deps{Atoms: nil})
	_, err := srv.ValidateAtomID(context.Background(), &creationv1.ValidateAtomIDRequest{
		TenantId: testTenantID,
		AtomId:   "atom-1",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code = %v (err=%v); want FailedPrecondition", status.Code(err), err)
	}
}

func TestValidateAtomID_InvalidArgRejected(t *testing.T) {
	srv := NewCreationServer(Deps{Atoms: newFakeAtomStore()})
	cases := []struct {
		name string
		req  *creationv1.ValidateAtomIDRequest
	}{
		{"nil_request", nil},
		{"empty_atom_id", &creationv1.ValidateAtomIDRequest{TenantId: testTenantID}},
		{"empty_tenant_id", &creationv1.ValidateAtomIDRequest{AtomId: "atom-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := srv.ValidateAtomID(context.Background(), tc.req)
			if status.Code(err) != codes.InvalidArgument {
				t.Errorf("code = %v (err=%v); want InvalidArgument", status.Code(err), err)
			}
		})
	}
}

func TestValidateAtomID_RepoFailureMapsToInternal(t *testing.T) {
	store := newFakeAtomStore()
	store.getErr = errors.New("pg down")
	srv := NewCreationServer(Deps{Atoms: store})

	_, err := srv.ValidateAtomID(context.Background(), &creationv1.ValidateAtomIDRequest{
		TenantId: testTenantID,
		AtomId:   "atom-1",
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v (err=%v); want Internal", status.Code(err), err)
	}
}

// -----------------------------------------------------------------------------
// ListAtomsByCourse
// -----------------------------------------------------------------------------

func TestListAtomsByCourse_HappyPath_ReturnsAll(t *testing.T) {
	store := newFakeAtomStore()
	a1 := mustStoredAtom(t, testTenantID, testAuthorGcid, "Atom one")
	a2 := mustStoredAtom(t, testTenantID, testAuthorGcid, "Atom two")
	store.byCourse = []*atom.LearningAtom{a1, a2}
	srv := NewCreationServer(Deps{Atoms: store})

	resp, err := srv.ListAtomsByCourse(context.Background(), &creationv1.ListAtomsByCourseRequest{
		TenantId: testTenantID,
		CourseId: "course-1",
	})
	if err != nil {
		t.Fatalf("ListAtomsByCourse: %v", err)
	}
	if len(resp.GetAtoms()) != 2 {
		t.Fatalf("atoms = %d; want 2", len(resp.GetAtoms()))
	}
	if resp.GetAtoms()[0].GetAtomId() != a1.AtomID || resp.GetAtoms()[1].GetAtomId() != a2.AtomID {
		t.Errorf("atoms out of order: %q, %q", resp.GetAtoms()[0].GetAtomId(), resp.GetAtoms()[1].GetAtomId())
	}
	// MVP — no cursor pagination yet.
	if resp.GetNextCursor() != "" {
		t.Errorf("next_cursor = %q; want empty (no pagination yet)", resp.GetNextCursor())
	}
}

func TestListAtomsByCourse_NoRepoWiredFailsPrecondition(t *testing.T) {
	srv := NewCreationServer(Deps{Atoms: nil})
	_, err := srv.ListAtomsByCourse(context.Background(), &creationv1.ListAtomsByCourseRequest{
		TenantId: testTenantID,
		CourseId: "course-1",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code = %v (err=%v); want FailedPrecondition", status.Code(err), err)
	}
}

func TestListAtomsByCourse_InvalidArgRejected(t *testing.T) {
	srv := NewCreationServer(Deps{Atoms: newFakeAtomStore()})
	cases := []struct {
		name string
		req  *creationv1.ListAtomsByCourseRequest
	}{
		{"nil_request", nil},
		{"empty_course_id", &creationv1.ListAtomsByCourseRequest{TenantId: testTenantID}},
		{"empty_tenant_id", &creationv1.ListAtomsByCourseRequest{CourseId: "course-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := srv.ListAtomsByCourse(context.Background(), tc.req)
			if status.Code(err) != codes.InvalidArgument {
				t.Errorf("code = %v (err=%v); want InvalidArgument", status.Code(err), err)
			}
		})
	}
}

func TestListAtomsByCourse_RepoFailureMapsToInternal(t *testing.T) {
	store := newFakeAtomStore()
	store.listCourseErr = errors.New("pg down")
	srv := NewCreationServer(Deps{Atoms: store})

	_, err := srv.ListAtomsByCourse(context.Background(), &creationv1.ListAtomsByCourseRequest{
		TenantId: testTenantID,
		CourseId: "course-1",
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v (err=%v); want Internal", status.Code(err), err)
	}
}

// -----------------------------------------------------------------------------
// Intentionally-UNIMPLEMENTED RPCs — fail-loud contract assertions.
// -----------------------------------------------------------------------------

func TestGenerateAtomsViaAI_ReturnsUnimplemented(t *testing.T) {
	srv := NewCreationServer(Deps{})
	_, err := srv.GenerateAtomsViaAI(context.Background(), &creationv1.GenerateAtomsViaAIRequest{})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("code = %v (err=%v); want Unimplemented (async via atoms_ready events)", status.Code(err), err)
	}
}

func TestAppendRevision_ReturnsUnimplemented(t *testing.T) {
	srv := NewCreationServer(Deps{})
	_, err := srv.AppendRevision(context.Background(), &creationv1.AppendRevisionRequest{})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("code = %v (err=%v); want Unimplemented (Phyllis revision port)", status.Code(err), err)
	}
}

func TestQueryKnowledgeGraph_ReturnsUnimplemented(t *testing.T) {
	srv := NewCreationServer(Deps{})
	_, err := srv.QueryKnowledgeGraph(context.Background(), &creationv1.QueryKnowledgeGraphRequest{})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("code = %v (err=%v); want Unimplemented (KG traversal lives in chora-consumption per ADR-143)", status.Code(err), err)
	}
}

// -----------------------------------------------------------------------------
// Domain→proto mapping helpers — atomToProto / atomTypeToProto /
// atomStatusToProto. Table-driven over every supported leg plus the
// UNSPECIFIED fall-throughs.
// -----------------------------------------------------------------------------

func TestAtomToProto_NilReturnsNil(t *testing.T) {
	if got := atomToProto(nil); got != nil {
		t.Errorf("atomToProto(nil) = %v; want nil", got)
	}
}

func TestAtomToProto_ZeroTimestampsOmitted(t *testing.T) {
	// A bare struct (not via atom.New) carries zero CreatedAt/UpdatedAt —
	// the proto timestamps must stay nil rather than a year-1 sentinel.
	got := atomToProto(&atom.LearningAtom{
		AtomID:   "atom-z",
		TenantID: testTenantID,
		Gcid:     testAuthorGcid,
		Title:    "zero ts",
	})
	if got.GetCreatedAt() != nil || got.GetUpdatedAt() != nil {
		t.Errorf("zero timestamps must stay nil (created_at=%v updated_at=%v)", got.GetCreatedAt(), got.GetUpdatedAt())
	}
}

func TestAtomToProto_FieldMapping(t *testing.T) {
	ts := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	got := atomToProto(&atom.LearningAtom{
		AtomID:       "atom-9",
		TenantID:     testTenantID,
		Gcid:         testAuthorGcid,
		Title:        "mapped",
		QuestionType: atom.TypeMCQ,
		Status:       atom.StatusPublished,
		Difficulty:   3,
		Revision:     7,
		CreatedAt:    ts,
		UpdatedAt:    ts,
	})
	if got.GetAtomId() != "atom-9" || got.GetTenantId() != testTenantID ||
		got.GetAuthorGcid() != testAuthorGcid || got.GetTitle() != "mapped" {
		t.Errorf("identity fields wrong: %+v", got)
	}
	if got.GetAtomType() != creationv1.AtomType_ATOM_TYPE_MULTIPLE_CHOICE {
		t.Errorf("atom_type = %v; want MULTIPLE_CHOICE", got.GetAtomType())
	}
	if got.GetStatus() != creationv1.AtomStatus_ATOM_STATUS_PUBLISHED {
		t.Errorf("status = %v; want PUBLISHED", got.GetStatus())
	}
	if got.GetDifficulty() != 3 {
		t.Errorf("difficulty = %d; want 3", got.GetDifficulty())
	}
	if got.GetCurrentRevisionNumber() != 7 {
		t.Errorf("current_revision_number = %d; want 7", got.GetCurrentRevisionNumber())
	}
	// Phyllis MVP doesn't track locale on the atom — wire stays empty.
	if got.GetLocale() != "" {
		t.Errorf("locale = %q; want empty", got.GetLocale())
	}
	if !got.GetCreatedAt().AsTime().Equal(ts) || !got.GetUpdatedAt().AsTime().Equal(ts) {
		t.Errorf("timestamps wrong: created_at=%v updated_at=%v", got.GetCreatedAt(), got.GetUpdatedAt())
	}
}

func TestAtomTypeToProto_Mapping(t *testing.T) {
	cases := []struct {
		name string
		in   atom.AtomType
		want creationv1.AtomType
	}{
		{"mcq", atom.TypeMCQ, creationv1.AtomType_ATOM_TYPE_MULTIPLE_CHOICE},
		// No FLASHCARD in the proto enum — closest neighbour is SHORT_ANSWER.
		{"flashcard", atom.TypeFlashcard, creationv1.AtomType_ATOM_TYPE_SHORT_ANSWER},
		{"video", atom.TypeVideo, creationv1.AtomType_ATOM_TYPE_MULTIMEDIA},
		{"essay", atom.TypeEssay, creationv1.AtomType_ATOM_TYPE_ESSAY},
		{"unknown", atom.AtomType("carrier-pigeon"), creationv1.AtomType_ATOM_TYPE_UNSPECIFIED},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := atomTypeToProto(tc.in); got != tc.want {
				t.Errorf("atomTypeToProto(%q) = %v; want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestAtomStatusToProto_Mapping(t *testing.T) {
	cases := []struct {
		name string
		in   atom.Status
		want creationv1.AtomStatus
	}{
		{"draft", atom.StatusDraft, creationv1.AtomStatus_ATOM_STATUS_DRAFT},
		{"published", atom.StatusPublished, creationv1.AtomStatus_ATOM_STATUS_PUBLISHED},
		{"archived", atom.StatusArchived, creationv1.AtomStatus_ATOM_STATUS_ARCHIVED},
		{"unknown", atom.Status("limbo"), creationv1.AtomStatus_ATOM_STATUS_UNSPECIFIED},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := atomStatusToProto(tc.in); got != tc.want {
				t.Errorf("atomStatusToProto(%q) = %v; want %v", tc.in, got, tc.want)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// SnapshotQuestionByID — remaining fail-loud + fallback branches.
// -----------------------------------------------------------------------------

// errQuestionRepo injects a non-NotFound GetByID failure (pg outage shape).
type errQuestionRepo struct {
	*fakeQuestionRepo
	err error
}

func (f *errQuestionRepo) GetByID(_ context.Context, _, _ string) (*question.Question, *question.QuestionRevision, error) {
	return nil, nil, f.err
}

func TestSnapshotQuestionByID_NilQuestionMapsToNotFound(t *testing.T) {
	// Defensive branch: a repository may return (nil, nil, nil) for a row the
	// caller believes exists — surface NotFound, never panic.
	repo := newFakeQuestionRepo()
	repo.rows[testTenantID+":ghost-q"] = questionRow{q: nil, rev: nil}
	srv := NewCreationServer(Deps{Questions: repo})

	_, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: "ghost-q",
		TenantId:   testTenantID,
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v (err=%v); want NotFound", status.Code(err), err)
	}
}

func TestSnapshotQuestionByID_RepoFailureMapsToInternal(t *testing.T) {
	srv := NewCreationServer(Deps{Questions: &errQuestionRepo{fakeQuestionRepo: newFakeQuestionRepo(), err: errors.New("pg down")}})
	_, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: "q-1",
		TenantId:   testTenantID,
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v (err=%v); want Internal", status.Code(err), err)
	}
}

func TestSnapshotQuestionByID_ReservedType_Unimplemented(t *testing.T) {
	// Reserved_* wire types are valid enum values but out of Phyllis scope —
	// the client must get a fail-loud 501, never a silent empty payload.
	repo := newFakeQuestionRepo()
	repo.rows[testTenantID+":q-reserved"] = questionRow{
		q: &question.Question{
			QuestionID: "q-reserved",
			AtomID:     testAtomID,
			TenantID:   testTenantID,
			Type:       question.TypeReservedTrueFalse,
			Prompt:     "True or false?",
		},
	}
	srv := NewCreationServer(Deps{Questions: repo})

	_, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: "q-reserved",
		TenantId:   testTenantID,
	})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("code = %v (err=%v); want Unimplemented", status.Code(err), err)
	}
}

func TestSnapshotQuestionByID_MCQ_MissingPayload_FailedPrecondition(t *testing.T) {
	// Corrupt-revision guard: an MCQ question with neither parent payload nor
	// revision payload must refuse loud rather than marshal an empty envelope.
	repo := newFakeQuestionRepo()
	repo.rows[testTenantID+":q-corrupt"] = questionRow{
		q: &question.Question{
			QuestionID: "q-corrupt",
			AtomID:     testAtomID,
			TenantID:   testTenantID,
			Type:       question.TypeMCQ,
			Prompt:     "Payload-less MCQ",
		},
	}
	srv := NewCreationServer(Deps{Questions: repo})

	_, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: "q-corrupt",
		TenantId:   testTenantID,
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code = %v (err=%v); want FailedPrecondition", status.Code(err), err)
	}
}

func TestSnapshotQuestionByID_OE_MissingPayload_FailedPrecondition(t *testing.T) {
	repo := newFakeQuestionRepo()
	repo.rows[testTenantID+":q-corrupt-oe"] = questionRow{
		q: &question.Question{
			QuestionID: "q-corrupt-oe",
			AtomID:     testAtomID,
			TenantID:   testTenantID,
			Type:       question.TypeOpenEnded,
			Prompt:     "Payload-less OE",
		},
	}
	srv := NewCreationServer(Deps{Questions: repo})

	_, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: "q-corrupt-oe",
		TenantId:   testTenantID,
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code = %v (err=%v); want FailedPrecondition", status.Code(err), err)
	}
}

func TestSnapshotQuestionByID_ParentPayloadFallback_ZeroUpdatedAt(t *testing.T) {
	// Two fallback legs in one exercise: a nil revision makes preferMCQ /
	// preferOE serve the PARENT payload, and a zero UpdatedAt stamps
	// snapshot_at with time.Now() instead of the question row.
	repo := newFakeQuestionRepo()
	repo.rows[testTenantID+":q-parent-mcq"] = questionRow{
		q: &question.Question{
			QuestionID: "q-parent-mcq",
			AtomID:     testAtomID,
			TenantID:   testTenantID,
			Type:       question.TypeMCQ,
			Prompt:     "Parent-payload MCQ",
			MCQ: &question.MCQPayload{
				Options: []question.MCQOption{{OptionID: "opt-A", Label: "A", IsCorrect: true}},
			},
			// UpdatedAt zero on purpose.
		},
	}
	repo.rows[testTenantID+":q-parent-oe"] = questionRow{
		q: &question.Question{
			QuestionID: "q-parent-oe",
			AtomID:     testAtomID,
			TenantID:   testTenantID,
			Type:       question.TypeOpenEnded,
			Prompt:     "Parent-payload OE",
			OE:         &question.OEPayload{ModelAnswer: "the model answer"},
		},
	}
	srv := NewCreationServer(Deps{Questions: repo})
	before := time.Now()

	mcqResp, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: "q-parent-mcq",
		TenantId:   testTenantID,
	})
	if err != nil {
		t.Fatalf("mcq parent fallback: %v", err)
	}
	if mcqResp.GetMcqPayloadJson() == "" {
		t.Error("mcq_payload_json: empty — parent payload fallback failed")
	}

	oeResp, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: "q-parent-oe",
		TenantId:   testTenantID,
	})
	if err != nil {
		t.Fatalf("oe parent fallback: %v", err)
	}
	if oeResp.GetOePayloadJson() == "" {
		t.Error("oe_payload_json: empty — parent payload fallback failed")
	}

	// snapshot_at must be a sane "now" stamp, not the zero time.
	snap := oeResp.GetSnapshotAt()
	if snap == nil {
		t.Fatal("snapshot_at: nil")
	}
	if snap.AsTime().Before(before.Add(-time.Second)) {
		t.Errorf("snapshot_at = %v; want ≈ now (>= %v)", snap.AsTime(), before)
	}
}

// -----------------------------------------------------------------------------
// enforceSnapshotReuseGate — atom-row load failure legs (non-NotFound and
// NotFound) that SnapshotQuestionByID only reaches with a wired atom repo.
// -----------------------------------------------------------------------------

func TestSnapshotQuestionByID_GateAtomNotFound_MapsToNotFound(t *testing.T) {
	// Gated caller, atom row absent → NotFound (distinct from the question's
	// own NotFound so the publisher can tell which lookup failed).
	repo := newFakeQuestionRepo()
	q, rev := mustMCQQuestion(t)
	repo.put(testTenantID, q, rev)
	srv := NewCreationServer(Deps{Questions: repo, Atoms: newFakeAtomStore()})

	_, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: q.QuestionID,
		TenantId:   testTenantID,
		CallerGcid: testCallerGcid,
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v (err=%v); want NotFound", status.Code(err), err)
	}
}

func TestSnapshotQuestionByID_GateAtomLoadFailure_MapsToInternal(t *testing.T) {
	repo := newFakeQuestionRepo()
	q, rev := mustMCQQuestion(t)
	repo.put(testTenantID, q, rev)
	store := newFakeAtomStore()
	store.getErr = errors.New("pg down")
	srv := NewCreationServer(Deps{Questions: repo, Atoms: store})

	_, err := srv.SnapshotQuestionByID(context.Background(), &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: q.QuestionID,
		TenantId:   testTenantID,
		CallerGcid: testCallerGcid,
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v (err=%v); want Internal", status.Code(err), err)
	}
}
