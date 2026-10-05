// Package questionbank_test — Service tests exercising the Repository +
// QuestionLookup composition (W3.B.1 TDD layer).
//
// Tests use lightweight in-memory stubs so the suite stays pure-domain (no
// infrastructure imports). The pg adapter has its own test file in
// internal/adapter/pg/question_bank_repository_test.go.
//
// B.1 scope: the EventPublisher port is a forward seam — the service ships it
// UNWIRED (nil). These tests therefore pass a nil publisher and assert the
// service is nil-tolerant (no emission, no panic). Event emission lands in a
// later sub-phase once the chora.creation.question_bank.* contracts exist.
package questionbank_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/questionbank"
)

// -----------------------------------------------------------------------------
// In-memory stubs
// -----------------------------------------------------------------------------

type memRepo struct {
	mu      sync.Mutex
	byID    map[string]*questionbank.QuestionBank
	saved   int
	getErr  error
	saveErr error
}

func newMemRepo() *memRepo {
	return &memRepo{byID: map[string]*questionbank.QuestionBank{}}
}

func (m *memRepo) Save(_ context.Context, b *questionbank.QuestionBank) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	clone := *b
	clone.Items = append([]*questionbank.QuestionBankItem(nil), b.Items...)
	m.byID[b.QuestionBankID] = &clone
	m.saved++
	return nil
}

func (m *memRepo) Get(_ context.Context, tenantID, id string) (*questionbank.QuestionBank, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, m.getErr
	}
	b, ok := m.byID[id]
	if !ok || b.TenantID != tenantID || b.DeletedAt != nil {
		return nil, questionbank.ErrNotFound
	}
	clone := *b
	clone.Items = append([]*questionbank.QuestionBankItem(nil), b.Items...)
	return &clone, nil
}

func (m *memRepo) GetVisible(_ context.Context, readerTenantID, id string) (*questionbank.QuestionBank, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.byID[id]
	if !ok || b.DeletedAt != nil || b.TenantID != readerTenantID {
		return nil, questionbank.ErrNotFound
	}
	clone := *b
	clone.Items = append([]*questionbank.QuestionBankItem(nil), b.Items...)
	return &clone, nil
}

func (m *memRepo) List(_ context.Context, tenantID string, f questionbank.ListFilter) ([]*questionbank.QuestionBank, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*questionbank.QuestionBank{}
	for _, b := range m.byID {
		if b.TenantID != tenantID || b.DeletedAt != nil {
			continue
		}
		if f.OwnerGCID != "" && b.OwnerGCID != f.OwnerGCID {
			continue
		}
		if f.Visibility != "" && b.Visibility != f.Visibility {
			continue
		}
		out = append(out, b)
	}
	return out, nil
}

// memQuestions stubs questionbank.QuestionLookup. It records the (tenantID,
// questionID) it was last asked to Resolve so a test can assert the lookup is
// TENANT-SCOPED (RLS + exam-security: a pool only references same-tenant
// questions).
type memQuestions struct {
	mu             sync.Mutex
	existsByID     map[string]bool
	detailsByID    map[string]qDetail // atom_id + canonical type per question_id
	lastTenantID   string
	lastQuestionID string
	calls          int
	err            error
	// Details-path probes (W3.B.2 — assemble-test-set resolution).
	detailsTenant string
	detailsCalls  int
	detailsErr    error
}

// qDetail is the resolved (atom_id, question_type) a bank item carries into the
// assembled test-set event.
type qDetail struct {
	atomID string
	qType  string
	prompt string
}

func (m *memQuestions) Resolve(_ context.Context, tenantID, questionID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.lastTenantID = tenantID
	m.lastQuestionID = questionID
	if m.err != nil {
		return false, m.err
	}
	return m.existsByID[questionID], nil
}

// Details resolves a question's atom_id + canonical question_type (TENANT-SCOPED).
func (m *memQuestions) Details(_ context.Context, tenantID, questionID string) (string, string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.detailsCalls++
	m.detailsTenant = tenantID
	if m.detailsErr != nil {
		return "", "", "", m.detailsErr
	}
	if d, ok := m.detailsByID[questionID]; ok {
		return d.atomID, d.qType, d.prompt, nil
	}
	return "", "", "", errors.New("memQuestions: no details for " + questionID)
}

// fakeTestSetAssembler captures the AssembledTestSet a Service publishes so the
// assemble-test-set tests can assert the event shape (host_atom_id, items,
// default points + sequential order). Satisfies questionbank.TestSetAssemblyPublisher.
type fakeTestSetAssembler struct {
	mu    sync.Mutex
	last  *questionbank.AssembledTestSet
	calls int
	err   error
}

func (f *fakeTestSetAssembler) PublishAssembledTestSet(_ context.Context, evt questionbank.AssembledTestSet) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return f.err
	}
	cp := evt
	cp.Items = append([]questionbank.AssembledTestSetItem(nil), evt.Items...)
	f.last = &cp
	return nil
}

// -----------------------------------------------------------------------------
// Create / Get / List
// -----------------------------------------------------------------------------

func TestService_Create_PersistsBank_NilPublisherTolerant(t *testing.T) {
	t.Parallel()

	repo := newMemRepo()
	svc := questionbank.NewService(repo, &memQuestions{}, nil) // publisher UNWIRED in B.1

	b, err := svc.Create(context.Background(), questionbank.CreateInput{
		TenantID:  tenantA,
		OwnerGCID: ownerA,
		Name:      "My pool",
		Tags:      []string{"algebra"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if b.QuestionBankID == "" {
		t.Errorf("QuestionBankID empty")
	}
	if repo.saved != 1 {
		t.Errorf("saved = %d; want 1", repo.saved)
	}
	if svc.PublisherWired() {
		t.Errorf("PublisherWired() = true; want false (B.1 ships unwired)")
	}
}

func TestService_Create_RejectsBadName(t *testing.T) {
	t.Parallel()

	svc := questionbank.NewService(newMemRepo(), &memQuestions{}, nil)
	_, err := svc.Create(context.Background(), questionbank.CreateInput{
		TenantID: tenantA, OwnerGCID: ownerA, Name: "   ",
	})
	if err == nil {
		t.Fatalf("expected error for blank name")
	}
}

func TestService_Get_ReturnsBank(t *testing.T) {
	t.Parallel()

	repo := newMemRepo()
	svc := questionbank.NewService(repo, &memQuestions{}, nil)
	created, _ := svc.Create(context.Background(), questionbank.CreateInput{
		TenantID: tenantA, OwnerGCID: ownerA, Name: "x",
	})
	got, err := svc.Get(context.Background(), tenantA, created.QuestionBankID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.QuestionBankID != created.QuestionBankID {
		t.Errorf("id mismatch")
	}
}

func TestService_List_FiltersByOwner(t *testing.T) {
	t.Parallel()

	repo := newMemRepo()
	svc := questionbank.NewService(repo, &memQuestions{}, nil)
	_, _ = svc.Create(context.Background(), questionbank.CreateInput{TenantID: tenantA, OwnerGCID: ownerA, Name: "Mine"})
	_, _ = svc.Create(context.Background(), questionbank.CreateInput{TenantID: tenantA, OwnerGCID: "01970000-0000-7000-9000-000000000099", Name: "Theirs"})

	items, err := svc.List(context.Background(), tenantA, questionbank.ListFilter{OwnerGCID: ownerA})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].Name != "Mine" {
		t.Errorf("items = %+v; want 1 'Mine'", items)
	}
}

// -----------------------------------------------------------------------------
// UpdateMetadata
// -----------------------------------------------------------------------------

func TestService_UpdateMetadata_RejectsNonOwner(t *testing.T) {
	t.Parallel()

	repo := newMemRepo()
	svc := questionbank.NewService(repo, &memQuestions{}, nil)
	b, _ := svc.Create(context.Background(), questionbank.CreateInput{TenantID: tenantA, OwnerGCID: ownerA, Name: "x"})
	newName := "by other"
	_, err := svc.UpdateMetadata(context.Background(), questionbank.UpdateMetadataInput{
		TenantID:       tenantA,
		QuestionBankID: b.QuestionBankID,
		OwnerGCID:      "01970000-0000-7000-9000-000000000999",
		Params:         questionbank.UpdateParams{Name: &newName},
	})
	if !errors.Is(err, questionbank.ErrForbidden) {
		t.Errorf("err = %v; want ErrForbidden", err)
	}
}

func TestService_UpdateMetadata_Updates(t *testing.T) {
	t.Parallel()

	repo := newMemRepo()
	svc := questionbank.NewService(repo, &memQuestions{}, nil)
	b, _ := svc.Create(context.Background(), questionbank.CreateInput{TenantID: tenantA, OwnerGCID: ownerA, Name: "old"})
	newName := "new"
	got, err := svc.UpdateMetadata(context.Background(), questionbank.UpdateMetadataInput{
		TenantID:       tenantA,
		QuestionBankID: b.QuestionBankID,
		OwnerGCID:      ownerA,
		Params:         questionbank.UpdateParams{Name: &newName},
	})
	if err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if got.Name != "new" {
		t.Errorf("Name = %q; want new", got.Name)
	}
}

// -----------------------------------------------------------------------------
// AddQuestion (validated via QuestionLookup, tenant-scoped)
// -----------------------------------------------------------------------------

func TestService_AddQuestion_404WhenLookupMissing(t *testing.T) {
	t.Parallel()

	repo := newMemRepo()
	lookup := &memQuestions{existsByID: map[string]bool{}} // nothing exists
	svc := questionbank.NewService(repo, lookup, nil)
	b, _ := svc.Create(context.Background(), questionbank.CreateInput{TenantID: tenantA, OwnerGCID: ownerA, Name: "x"})

	_, err := svc.AddQuestion(context.Background(), questionbank.AddQuestionInput{
		TenantID:       tenantA,
		QuestionBankID: b.QuestionBankID,
		OwnerGCID:      ownerA,
		QuestionID:     "01970000-0000-7000-b000-000000000001",
	})
	if !errors.Is(err, questionbank.ErrQuestionDoesNotExist) {
		t.Errorf("err = %v; want ErrQuestionDoesNotExist", err)
	}
}

func TestService_AddQuestion_PersistsWhenExists(t *testing.T) {
	t.Parallel()

	const qid = "01970000-0000-7000-b000-000000000001"
	repo := newMemRepo()
	lookup := &memQuestions{existsByID: map[string]bool{qid: true}}
	svc := questionbank.NewService(repo, lookup, nil)
	b, _ := svc.Create(context.Background(), questionbank.CreateInput{TenantID: tenantA, OwnerGCID: ownerA, Name: "x"})

	got, err := svc.AddQuestion(context.Background(), questionbank.AddQuestionInput{
		TenantID:       tenantA,
		QuestionBankID: b.QuestionBankID,
		OwnerGCID:      ownerA,
		QuestionID:     qid,
	})
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].QuestionID != qid {
		t.Errorf("Items = %+v; want 1 with qid=%s", got.Items, qid)
	}
	// The lookup MUST be tenant-scoped to the bank's tenant (RLS +
	// exam-security: a pool references only same-tenant questions).
	if lookup.lastTenantID != tenantA {
		t.Errorf("lookup tenant = %q; want %q (tenant-scoped resolve)", lookup.lastTenantID, tenantA)
	}
	if lookup.lastQuestionID != qid {
		t.Errorf("lookup question = %q; want %q", lookup.lastQuestionID, qid)
	}
}

func TestService_AddQuestion_RejectsNonOwner(t *testing.T) {
	t.Parallel()

	const qid = "01970000-0000-7000-b000-000000000001"
	repo := newMemRepo()
	lookup := &memQuestions{existsByID: map[string]bool{qid: true}}
	svc := questionbank.NewService(repo, lookup, nil)
	b, _ := svc.Create(context.Background(), questionbank.CreateInput{TenantID: tenantA, OwnerGCID: ownerA, Name: "x"})

	_, err := svc.AddQuestion(context.Background(), questionbank.AddQuestionInput{
		TenantID:       tenantA,
		QuestionBankID: b.QuestionBankID,
		OwnerGCID:      "01970000-0000-7000-9000-000000000999",
		QuestionID:     qid,
	})
	if !errors.Is(err, questionbank.ErrForbidden) {
		t.Errorf("err = %v; want ErrForbidden", err)
	}
	// Owner gate fires BEFORE the lookup — no wasted resolve.
	if lookup.calls != 0 {
		t.Errorf("lookup calls = %d; want 0 (owner gate precedes lookup)", lookup.calls)
	}
}

// -----------------------------------------------------------------------------
// RemoveQuestion / Delete
// -----------------------------------------------------------------------------

func TestService_RemoveQuestion_Persists(t *testing.T) {
	t.Parallel()

	const qid = "01970000-0000-7000-b000-000000000001"
	repo := newMemRepo()
	lookup := &memQuestions{existsByID: map[string]bool{qid: true}}
	svc := questionbank.NewService(repo, lookup, nil)
	b, _ := svc.Create(context.Background(), questionbank.CreateInput{TenantID: tenantA, OwnerGCID: ownerA, Name: "x"})
	_, _ = svc.AddQuestion(context.Background(), questionbank.AddQuestionInput{
		TenantID: tenantA, QuestionBankID: b.QuestionBankID, OwnerGCID: ownerA, QuestionID: qid,
	})

	got, err := svc.RemoveQuestion(context.Background(), questionbank.RemoveQuestionInput{
		TenantID: tenantA, QuestionBankID: b.QuestionBankID, OwnerGCID: ownerA, QuestionID: qid,
	})
	if err != nil {
		t.Fatalf("RemoveQuestion: %v", err)
	}
	if len(got.Items) != 0 {
		t.Errorf("Items = %+v; want empty after remove", got.Items)
	}
}

func TestService_Delete_SoftDeletes(t *testing.T) {
	t.Parallel()

	repo := newMemRepo()
	svc := questionbank.NewService(repo, &memQuestions{}, nil)
	b, _ := svc.Create(context.Background(), questionbank.CreateInput{TenantID: tenantA, OwnerGCID: ownerA, Name: "x"})

	if err := svc.Delete(context.Background(), questionbank.DeleteInput{
		TenantID: tenantA, QuestionBankID: b.QuestionBankID, OwnerGCID: ownerA,
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Subsequent Get must 404 (soft-deleted filtered).
	if _, err := svc.Get(context.Background(), tenantA, b.QuestionBankID); !errors.Is(err, questionbank.ErrNotFound) {
		t.Errorf("Get after delete err = %v; want ErrNotFound", err)
	}
}

func TestService_Delete_RejectsNonOwner(t *testing.T) {
	t.Parallel()

	repo := newMemRepo()
	svc := questionbank.NewService(repo, &memQuestions{}, nil)
	b, _ := svc.Create(context.Background(), questionbank.CreateInput{TenantID: tenantA, OwnerGCID: ownerA, Name: "x"})
	err := svc.Delete(context.Background(), questionbank.DeleteInput{
		TenantID: tenantA, QuestionBankID: b.QuestionBankID, OwnerGCID: "01970000-0000-7000-9000-000000000999",
	})
	if !errors.Is(err, questionbank.ErrForbidden) {
		t.Errorf("err = %v; want ErrForbidden", err)
	}
}

func TestService_GetVisible_SameTenantOnly(t *testing.T) {
	t.Parallel()

	repo := newMemRepo()
	svc := questionbank.NewService(repo, &memQuestions{}, nil)
	b, _ := svc.Create(context.Background(), questionbank.CreateInput{
		TenantID: tenantA, OwnerGCID: ownerA, Name: "x", Visibility: questionbank.VisibilityTenantInternal,
	})
	// Same tenant resolves.
	if _, err := svc.GetVisible(context.Background(), tenantA, b.QuestionBankID); err != nil {
		t.Fatalf("GetVisible same-tenant: %v", err)
	}
	// Cross-tenant never resolves (no PUBLIC path).
	if _, err := svc.GetVisible(context.Background(), tenantB, b.QuestionBankID); !errors.Is(err, questionbank.ErrNotFound) {
		t.Errorf("GetVisible cross-tenant err = %v; want ErrNotFound", err)
	}
}

func TestService_Create_PropagatesSaveError(t *testing.T) {
	t.Parallel()

	repo := newMemRepo()
	repo.saveErr = errors.New("boom")
	svc := questionbank.NewService(repo, &memQuestions{}, nil)
	if _, err := svc.Create(context.Background(), questionbank.CreateInput{
		TenantID: tenantA, OwnerGCID: ownerA, Name: "x",
	}); err == nil {
		t.Fatalf("expected save error to propagate")
	}
}

func TestService_UpdateMetadata_NotFoundPropagates(t *testing.T) {
	t.Parallel()

	svc := questionbank.NewService(newMemRepo(), &memQuestions{}, nil)
	name := "x"
	_, err := svc.UpdateMetadata(context.Background(), questionbank.UpdateMetadataInput{
		TenantID: tenantA, QuestionBankID: "missing", OwnerGCID: ownerA,
		Params: questionbank.UpdateParams{Name: &name},
	})
	if !errors.Is(err, questionbank.ErrNotFound) {
		t.Errorf("err = %v; want ErrNotFound", err)
	}
}

func TestService_AddQuestion_NotFoundPropagates(t *testing.T) {
	t.Parallel()

	svc := questionbank.NewService(newMemRepo(), &memQuestions{}, nil)
	_, err := svc.AddQuestion(context.Background(), questionbank.AddQuestionInput{
		TenantID: tenantA, QuestionBankID: "missing", OwnerGCID: ownerA,
		QuestionID: "01970000-0000-7000-b000-000000000001",
	})
	if !errors.Is(err, questionbank.ErrNotFound) {
		t.Errorf("err = %v; want ErrNotFound", err)
	}
}

func TestService_AddQuestion_PropagatesLookupError(t *testing.T) {
	t.Parallel()

	repo := newMemRepo()
	lookup := &memQuestions{err: errors.New("db down")}
	svc := questionbank.NewService(repo, lookup, nil)
	b, _ := svc.Create(context.Background(), questionbank.CreateInput{TenantID: tenantA, OwnerGCID: ownerA, Name: "x"})
	_, err := svc.AddQuestion(context.Background(), questionbank.AddQuestionInput{
		TenantID: tenantA, QuestionBankID: b.QuestionBankID, OwnerGCID: ownerA,
		QuestionID: "01970000-0000-7000-b000-000000000001",
	})
	if err == nil {
		t.Fatalf("expected lookup error to propagate")
	}
}

func TestService_AddQuestion_NilLookupFailsLoud(t *testing.T) {
	t.Parallel()

	repo := newMemRepo()
	svc := questionbank.NewService(repo, nil, nil) // no QuestionLookup wired
	b, _ := svc.Create(context.Background(), questionbank.CreateInput{TenantID: tenantA, OwnerGCID: ownerA, Name: "x"})
	_, err := svc.AddQuestion(context.Background(), questionbank.AddQuestionInput{
		TenantID: tenantA, QuestionBankID: b.QuestionBankID, OwnerGCID: ownerA,
		QuestionID: "01970000-0000-7000-b000-000000000001",
	})
	if err == nil {
		t.Fatalf("expected fail-loud error when QuestionLookup is not wired")
	}
}

// -----------------------------------------------------------------------------
// ListItemsEnriched — workbench READ-model: each membership row carries the
// question ref PLUS the resolved host atom_id + question_type, so the A+
// workbench can address /api/atoms/{atom_id} for its row-level conveniences
// (preview / edit / tags / clone). The bank stores ONLY question_id; atom_id is
// resolved at read time via the same tenant-scoped QuestionLookup.Details call
// AssembleTestSet uses. (Inverse of the add-to-bank atom_id↔question_id fix.)
// -----------------------------------------------------------------------------

func TestService_ListItemsEnriched_ResolvesHostAtomAndType(t *testing.T) {
	t.Parallel()

	lookup := &memQuestions{
		existsByID: map[string]bool{qb1: true, qb2: true},
		detailsByID: map[string]qDetail{
			qb1: {atomID: "01970000-0000-7000-a000-000000000001", qType: "mcq", prompt: "What is 2+2?"},
			qb2: {atomID: "01970000-0000-7000-a000-000000000002", qType: "oe", prompt: "Explain photosynthesis."},
		},
	}
	svc := questionbank.NewService(newMemRepo(), lookup, nil)
	b := seedBankWithQuestions(t, svc, qb1, qb2)

	items, err := svc.ListItemsEnriched(context.Background(), tenantA, b.QuestionBankID)
	if err != nil {
		t.Fatalf("ListItemsEnriched: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d; want 2", len(items))
	}
	// Each row = membership question_id + resolved host atom_id + question_type +
	// the question prompt (human-readable workbench-row label).
	if items[0].QuestionID != qb1 ||
		items[0].AtomID != "01970000-0000-7000-a000-000000000001" ||
		items[0].QuestionType != "mcq" || items[0].Prompt != "What is 2+2?" {
		t.Errorf("item0 = %+v; want qb1 / atom-1 / mcq / 'What is 2+2?'", items[0])
	}
	if items[1].QuestionID != qb2 ||
		items[1].AtomID != "01970000-0000-7000-a000-000000000002" ||
		items[1].QuestionType != "oe" || items[1].Prompt != "Explain photosynthesis." {
		t.Errorf("item1 = %+v; want qb2 / atom-2 / oe / 'Explain photosynthesis.'", items[1])
	}
	// Resolution MUST be tenant-scoped (RLS + exam-security), same as assemble.
	if lookup.detailsTenant != tenantA {
		t.Errorf("details tenant = %q; want %q (tenant-scoped)", lookup.detailsTenant, tenantA)
	}
	if lookup.detailsCalls != 2 {
		t.Errorf("details calls = %d; want 2 (one per item)", lookup.detailsCalls)
	}
}

func TestService_ListItemsEnriched_EmptyBankNeedsNoLookup(t *testing.T) {
	t.Parallel()

	// An empty bank has nothing to resolve, so a nil lookup is fine — it must
	// list cleanly (never fail loud on a misconfiguration that cannot bite).
	svc := questionbank.NewService(newMemRepo(), nil, nil)
	b, err := svc.Create(context.Background(),
		questionbank.CreateInput{TenantID: tenantA, OwnerGCID: ownerA, Name: "Empty"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	items, err := svc.ListItemsEnriched(context.Background(), tenantA, b.QuestionBankID)
	if err != nil {
		t.Fatalf("ListItemsEnriched empty: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("items = %d; want 0", len(items))
	}
}

func TestService_ListItemsEnriched_NilLookupFailsLoudForPopulatedBank(t *testing.T) {
	t.Parallel()

	// Seed a populated bank with a wired service, then read it back through a
	// service whose QuestionLookup is unwired: a populated bank cannot resolve
	// atom ids without the port, so it MUST fail loud (never return bare rows
	// the workbench would mis-address as atom ids).
	repo := newMemRepo()
	seedSvc := questionbank.NewService(repo, &memQuestions{
		existsByID:  map[string]bool{qb1: true},
		detailsByID: map[string]qDetail{qb1: {atomID: "01970000-0000-7000-a000-000000000001", qType: "mcq"}},
	}, nil)
	b := seedBankWithQuestions(t, seedSvc, qb1)

	svc := questionbank.NewService(repo, nil, nil) // no QuestionLookup wired
	if _, err := svc.ListItemsEnriched(context.Background(), tenantA, b.QuestionBankID); err == nil {
		t.Fatalf("expected fail-loud error when QuestionLookup is unwired for a populated bank")
	}
}

// fakePager stubs questionbank.ItemPager for ListItemsEnrichedPage delegation.
type fakePager struct {
	items     []questionbank.EnrichedQuestionBankItem
	total     int
	gotBankID string
	gotFilter questionbank.ItemPageFilter
}

func (p *fakePager) ListItemsPage(_ context.Context, _, bankID string, f questionbank.ItemPageFilter) ([]questionbank.EnrichedQuestionBankItem, int, error) {
	p.gotBankID = bankID
	p.gotFilter = f
	return p.items, p.total, nil
}

func TestService_ListItemsEnrichedPage_DelegatesToPager(t *testing.T) {
	t.Parallel()

	pager := &fakePager{
		items: []questionbank.EnrichedQuestionBankItem{{QuestionID: qb1, AtomID: "a1", Prompt: "p1"}},
		total: 42, // total is the full match count (huge-bank-safe), not the page length
	}
	svc := questionbank.NewService(newMemRepo(), &memQuestions{}, nil).SetItemPager(pager)
	f := questionbank.ItemPageFilter{Query: "scrum", Types: []string{"mcq"}, SortKey: "prompt", SortDesc: true, Limit: 5, Offset: 10}

	items, total, err := svc.ListItemsEnrichedPage(context.Background(), tenantA, "bank-9", f)
	if err != nil {
		t.Fatalf("ListItemsEnrichedPage: %v", err)
	}
	if total != 42 || len(items) != 1 || items[0].QuestionID != qb1 {
		t.Errorf("got total=%d items=%+v; want 42 / [qb1]", total, items)
	}
	// The service passes the bank id + filter through verbatim to the pager.
	if pager.gotBankID != "bank-9" || pager.gotFilter.Query != "scrum" ||
		pager.gotFilter.SortKey != "prompt" || !pager.gotFilter.SortDesc ||
		pager.gotFilter.Limit != 5 || pager.gotFilter.Offset != 10 ||
		len(pager.gotFilter.Types) != 1 || pager.gotFilter.Types[0] != "mcq" {
		t.Errorf("pager got bank=%q filter=%+v; want bank-9 + the passed filter", pager.gotBankID, pager.gotFilter)
	}
}

func TestService_ListItemsEnrichedPage_NilPagerFailsLoud(t *testing.T) {
	t.Parallel()

	svc := questionbank.NewService(newMemRepo(), &memQuestions{}, nil) // no pager wired
	if _, _, err := svc.ListItemsEnrichedPage(context.Background(), tenantA, "bank-1", questionbank.ItemPageFilter{}); err == nil {
		t.Fatalf("expected fail-loud error when itemPager not wired")
	}
}

func TestService_RemoveQuestion_NotFoundPropagates(t *testing.T) {
	t.Parallel()

	svc := questionbank.NewService(newMemRepo(), &memQuestions{}, nil)
	_, err := svc.RemoveQuestion(context.Background(), questionbank.RemoveQuestionInput{
		TenantID: tenantA, QuestionBankID: "missing", OwnerGCID: ownerA,
		QuestionID: "01970000-0000-7000-b000-000000000001",
	})
	if !errors.Is(err, questionbank.ErrNotFound) {
		t.Errorf("err = %v; want ErrNotFound", err)
	}
}

func TestService_Delete_NotFoundPropagates(t *testing.T) {
	t.Parallel()

	svc := questionbank.NewService(newMemRepo(), &memQuestions{}, nil)
	err := svc.Delete(context.Background(), questionbank.DeleteInput{
		TenantID: tenantA, QuestionBankID: "missing", OwnerGCID: ownerA,
	})
	if !errors.Is(err, questionbank.ErrNotFound) {
		t.Errorf("err = %v; want ErrNotFound", err)
	}
}

func TestService_PublisherWired_TrueWhenSet(t *testing.T) {
	t.Parallel()

	svc := questionbank.NewService(newMemRepo(), &memQuestions{}, recordingPublisher{})
	if !svc.PublisherWired() {
		t.Errorf("PublisherWired() = false; want true when a publisher is supplied")
	}
}

// recordingPublisher is a no-op EventPublisher used only to assert the
// PublisherWired() seam accessor. B.1 never calls Publish.
type recordingPublisher struct{}

func (recordingPublisher) Publish(_ context.Context, _ questionbank.Event) error { return nil }

// -----------------------------------------------------------------------------
// AssembleTestSet (W3.B.2 — assemble a TestSet from a QuestionBank by reusing
// chora.creation.question_batch.accepted.v1 via the TestSetAssemblyPublisher)
// -----------------------------------------------------------------------------

const (
	qb1 = "01970000-0000-7000-b000-000000000001"
	qb2 = "01970000-0000-7000-b000-000000000002"
)

// seedBankWithQuestions creates a bank owned by ownerA in tenantA and adds the
// supplied question_ids (all marked existing in the lookup).
func seedBankWithQuestions(t *testing.T, svc *questionbank.Service, qids ...string) *questionbank.QuestionBank {
	t.Helper()
	b, err := svc.Create(context.Background(), questionbank.CreateInput{TenantID: tenantA, OwnerGCID: ownerA, Name: "Pool"})
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}
	for _, qid := range qids {
		if _, err := svc.AddQuestion(context.Background(), questionbank.AddQuestionInput{
			TenantID: tenantA, QuestionBankID: b.QuestionBankID, OwnerGCID: ownerA, QuestionID: qid,
		}); err != nil {
			t.Fatalf("seed add %s: %v", qid, err)
		}
	}
	return b
}

func TestService_AssembleTestSet_BuildsEventFromBankQuestions(t *testing.T) {
	t.Parallel()

	lookup := &memQuestions{
		existsByID: map[string]bool{qb1: true, qb2: true},
		detailsByID: map[string]qDetail{
			qb1: {atomID: "01970000-0000-7000-a000-000000000001", qType: "mcq"},
			qb2: {atomID: "01970000-0000-7000-a000-000000000002", qType: "oe"},
		},
	}
	asm := &fakeTestSetAssembler{}
	svc := questionbank.NewService(newMemRepo(), lookup, nil).SetTestSetAssembler(asm)
	b := seedBankWithQuestions(t, svc, qb1, qb2)

	res, err := svc.AssembleTestSet(context.Background(), questionbank.AssembleTestSetInput{
		TenantID:       tenantA,
		QuestionBankID: b.QuestionBankID,
		OwnerGCID:      ownerA,
		Title:          "Midterm",
		Description:    "chapters 1-3",
		Traceparent:    "00-trace-span-01",
	})
	if err != nil {
		t.Fatalf("AssembleTestSet: %v", err)
	}
	if res.JobID == "" {
		t.Errorf("result JobID empty")
	}
	if res.Status != "assembling" {
		t.Errorf("result Status = %q; want assembling", res.Status)
	}
	if asm.calls != 1 {
		t.Fatalf("assembler calls = %d; want 1", asm.calls)
	}
	got := asm.last
	if got.JobID != res.JobID {
		t.Errorf("event JobID = %q; want %q (matches result)", got.JobID, res.JobID)
	}
	// host_atom_id decision: a bank-assembled set has no single host atom, so
	// the bank id is carried as provenance (the delivery subscriber never reads
	// host_atom_id for assembly — it is opaque provenance only).
	if got.HostAtomID != b.QuestionBankID {
		t.Errorf("event HostAtomID = %q; want question_bank_id %q (provenance)", got.HostAtomID, b.QuestionBankID)
	}
	if got.TenantID != tenantA || got.AuthorGCID != ownerA {
		t.Errorf("event tenant/author = %q/%q; want %q/%q", got.TenantID, got.AuthorGCID, tenantA, ownerA)
	}
	if got.Title != "Midterm" || got.Description != "chapters 1-3" {
		t.Errorf("event title/desc = %q/%q; want Midterm/chapters 1-3", got.Title, got.Description)
	}
	if got.Traceparent != "00-trace-span-01" {
		t.Errorf("event Traceparent = %q; want propagated", got.Traceparent)
	}
	if got.AcceptedAt.IsZero() {
		t.Errorf("event AcceptedAt is zero; want now")
	}
	if len(got.Items) != 2 {
		t.Fatalf("event items = %d; want 2", len(got.Items))
	}
	// Item 0 → qb1 (mcq, atom-1, default points 10, display_order 1).
	if got.Items[0].QuestionID != qb1 || got.Items[0].QuestionAtomID != "01970000-0000-7000-a000-000000000001" ||
		got.Items[0].QuestionType != "mcq" || got.Items[0].Points != 10 || got.Items[0].DisplayOrder != 1 {
		t.Errorf("item0 = %+v; want qb1/atom-1/mcq/points10/order1", got.Items[0])
	}
	// Item 1 → qb2 (oe, atom-2, default points 10, display_order 2).
	if got.Items[1].QuestionID != qb2 || got.Items[1].QuestionAtomID != "01970000-0000-7000-a000-000000000002" ||
		got.Items[1].QuestionType != "oe" || got.Items[1].Points != 10 || got.Items[1].DisplayOrder != 2 {
		t.Errorf("item1 = %+v; want qb2/atom-2/oe/points10/order2", got.Items[1])
	}
	// Question details MUST be resolved tenant-scoped (RLS + exam-security).
	if lookup.detailsTenant != tenantA {
		t.Errorf("details tenant = %q; want %q (tenant-scoped)", lookup.detailsTenant, tenantA)
	}
	if lookup.detailsCalls != 2 {
		t.Errorf("details calls = %d; want 2 (one per item)", lookup.detailsCalls)
	}
}

func TestService_AssembleTestSet_RejectsEmptyBank(t *testing.T) {
	t.Parallel()

	asm := &fakeTestSetAssembler{}
	svc := questionbank.NewService(newMemRepo(), &memQuestions{}, nil).SetTestSetAssembler(asm)
	b := seedBankWithQuestions(t, svc) // no questions

	_, err := svc.AssembleTestSet(context.Background(), questionbank.AssembleTestSetInput{
		TenantID: tenantA, QuestionBankID: b.QuestionBankID, OwnerGCID: ownerA, Title: "X",
	})
	if !errors.Is(err, questionbank.ErrEmptyBank) {
		t.Errorf("err = %v; want ErrEmptyBank", err)
	}
	if asm.calls != 0 {
		t.Errorf("assembler calls = %d; want 0 (empty bank never publishes)", asm.calls)
	}
}

func TestService_AssembleTestSet_404MissingBank(t *testing.T) {
	t.Parallel()

	svc := questionbank.NewService(newMemRepo(), &memQuestions{}, nil).SetTestSetAssembler(&fakeTestSetAssembler{})
	_, err := svc.AssembleTestSet(context.Background(), questionbank.AssembleTestSetInput{
		TenantID: tenantA, QuestionBankID: "missing", OwnerGCID: ownerA, Title: "X",
	})
	if !errors.Is(err, questionbank.ErrNotFound) {
		t.Errorf("err = %v; want ErrNotFound", err)
	}
}

func TestService_AssembleTestSet_403NonOwner(t *testing.T) {
	t.Parallel()

	asm := &fakeTestSetAssembler{}
	lookup := &memQuestions{existsByID: map[string]bool{qb1: true}, detailsByID: map[string]qDetail{qb1: {atomID: "a1", qType: "mcq"}}}
	svc := questionbank.NewService(newMemRepo(), lookup, nil).SetTestSetAssembler(asm)
	b := seedBankWithQuestions(t, svc, qb1)

	_, err := svc.AssembleTestSet(context.Background(), questionbank.AssembleTestSetInput{
		TenantID: tenantA, QuestionBankID: b.QuestionBankID, OwnerGCID: "01970000-0000-7000-9000-000000000999", Title: "X",
	})
	if !errors.Is(err, questionbank.ErrForbidden) {
		t.Errorf("err = %v; want ErrForbidden", err)
	}
	if asm.calls != 0 {
		t.Errorf("assembler calls = %d; want 0 (non-owner never publishes)", asm.calls)
	}
}

func TestService_AssembleTestSet_RejectsBlankTitle(t *testing.T) {
	t.Parallel()

	lookup := &memQuestions{existsByID: map[string]bool{qb1: true}, detailsByID: map[string]qDetail{qb1: {atomID: "a1", qType: "mcq"}}}
	svc := questionbank.NewService(newMemRepo(), lookup, nil).SetTestSetAssembler(&fakeTestSetAssembler{})
	b := seedBankWithQuestions(t, svc, qb1)

	_, err := svc.AssembleTestSet(context.Background(), questionbank.AssembleTestSetInput{
		TenantID: tenantA, QuestionBankID: b.QuestionBankID, OwnerGCID: ownerA, Title: "   ",
	})
	if !errors.Is(err, questionbank.ErrTitleRequired) {
		t.Errorf("err = %v; want ErrTitleRequired", err)
	}
}

func TestService_AssembleTestSet_503WhenAssemblerNotWired(t *testing.T) {
	t.Parallel()

	lookup := &memQuestions{existsByID: map[string]bool{qb1: true}, detailsByID: map[string]qDetail{qb1: {atomID: "a1", qType: "mcq"}}}
	svc := questionbank.NewService(newMemRepo(), lookup, nil) // assembler UNWIRED
	b := seedBankWithQuestions(t, svc, qb1)

	_, err := svc.AssembleTestSet(context.Background(), questionbank.AssembleTestSetInput{
		TenantID: tenantA, QuestionBankID: b.QuestionBankID, OwnerGCID: ownerA, Title: "X",
	})
	if !errors.Is(err, questionbank.ErrAssemblerNotWired) {
		t.Errorf("err = %v; want ErrAssemblerNotWired (fail-loud 503)", err)
	}
}

func TestService_AssembleTestSet_PropagatesDetailsError(t *testing.T) {
	t.Parallel()

	lookup := &memQuestions{
		existsByID:  map[string]bool{qb1: true},
		detailsByID: map[string]qDetail{qb1: {atomID: "a1", qType: "mcq"}},
		detailsErr:  errors.New("db down"),
	}
	svc := questionbank.NewService(newMemRepo(), lookup, nil).SetTestSetAssembler(&fakeTestSetAssembler{})
	b := seedBankWithQuestions(t, svc, qb1)

	_, err := svc.AssembleTestSet(context.Background(), questionbank.AssembleTestSetInput{
		TenantID: tenantA, QuestionBankID: b.QuestionBankID, OwnerGCID: ownerA, Title: "X",
	})
	if err == nil {
		t.Fatalf("expected Details error to propagate (fail-loud)")
	}
}

func TestService_AssembleTestSet_PropagatesPublishError(t *testing.T) {
	t.Parallel()

	lookup := &memQuestions{existsByID: map[string]bool{qb1: true}, detailsByID: map[string]qDetail{qb1: {atomID: "a1", qType: "mcq"}}}
	asm := &fakeTestSetAssembler{err: errors.New("outbox insert failed")}
	svc := questionbank.NewService(newMemRepo(), lookup, nil).SetTestSetAssembler(asm)
	b := seedBankWithQuestions(t, svc, qb1)

	_, err := svc.AssembleTestSet(context.Background(), questionbank.AssembleTestSetInput{
		TenantID: tenantA, QuestionBankID: b.QuestionBankID, OwnerGCID: ownerA, Title: "X",
	})
	if err == nil {
		t.Fatalf("expected publish error to propagate (fail-loud)")
	}
}
