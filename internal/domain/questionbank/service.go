// Service — the domain service that composes QuestionBank mutators with
// QuestionLookup (question-existence validation) + Repository (persistence) +
// EventPublisher (forward seam; UNWIRED in B.1).
//
// Per .claude/rules/ddd-enforcement.md Aggregate Invariant #3, cross-aggregate
// UUID references are validated through a domain service rather than a DB FK.
// AddQuestion calls QuestionLookup.Resolve (TENANT-SCOPED) before delegating to
// the aggregate's AddItem; a missing question returns ErrQuestionDoesNotExist
// (mapped to HTTP 404 at the handler boundary).
//
// Mutating operations (UpdateMetadata / AddQuestion / RemoveQuestion / Delete)
// are owner-gated: the caller's GCID must equal QuestionBank.OwnerGCID or the
// service returns ErrForbidden (HTTP 403). This mirrors the Collection
// aggregate's authorization model — there is no separate role middleware in the
// chora-creation HTTP layer.
//
// Hexagonal: depends ONLY on the same package's ports — no adapter imports.
package questionbank

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Service is the domain orchestrator.
type Service struct {
	repo      Repository
	questions QuestionLookup
	// publisher is a forward seam for Pub/Sub emission of the generic
	// chora.creation.question_bank.* events. B.1 ships it UNWIRED (nil) and
	// never calls it — see events.go. Those contracts do not exist yet, so it
	// stays nil; it is SEPARATE from the test-set assembler below.
	publisher EventPublisher
	// assembler publishes a bank→test-set assembly by REUSING the existing
	// chora.creation.question_batch.accepted.v1 contract (W3.B.2). Attached via
	// SetTestSetAssembler; nil ⇒ AssembleTestSet fails loud (ErrAssemblerNotWired).
	assembler TestSetAssemblyPublisher
	// pager resolves a filtered/sorted/paginated page of a bank's questions in
	// SQL (huge-bank-safe — never loads the whole bank). Attached via
	// SetItemPager; nil ⇒ ListItemsEnrichedPage fails loud.
	pager ItemPager
}

// NewService wires the service. repo + questions are required for the
// happy-path flows; publisher is nil-tolerant (and nil in B.1).
func NewService(repo Repository, questions QuestionLookup, publisher EventPublisher) *Service {
	return &Service{repo: repo, questions: questions, publisher: publisher}
}

// PublisherWired reports whether an EventPublisher is attached. B.1 ships it
// unwired (nil); main.go logs this at boot and a future sub-phase flips it.
func (s *Service) PublisherWired() bool { return s.publisher != nil }

// SetTestSetAssembler attaches the W3.B.2 assemble-test-set publisher (the thin
// adapter over the EXISTING QuestionBatchAcceptedPublisher). Returns the
// receiver for fluent wiring. Nil-tolerant: AssembleTestSet fails loud
// (ErrAssemblerNotWired → 503) when unset, never a silent no-op.
func (s *Service) SetTestSetAssembler(a TestSetAssemblyPublisher) *Service {
	s.assembler = a
	return s
}

// CreateInput is the parameter envelope for Create.
type CreateInput struct {
	TenantID    string
	OwnerGCID   string
	Name        string
	Description string
	Visibility  Visibility
	Tags        []string
}

// Create constructs a QuestionBank and persists it.
func (s *Service) Create(ctx context.Context, in CreateInput) (*QuestionBank, error) {
	b, err := New(NewParams{
		TenantID:    in.TenantID,
		OwnerGCID:   in.OwnerGCID,
		Name:        in.Name,
		Description: in.Description,
		Visibility:  in.Visibility,
		Tags:        in.Tags,
	})
	if err != nil {
		return nil, err
	}
	if err := s.repo.Save(ctx, b); err != nil {
		return nil, fmt.Errorf("%w: Create save: %w", ErrRepository, err)
	}
	return b, nil
}

// Get returns the tenant-owned bank. Returns ErrNotFound when absent /
// soft-deleted / owned by a different tenant.
func (s *Service) Get(ctx context.Context, tenantID, questionBankID string) (*QuestionBank, error) {
	return s.repo.Get(ctx, tenantID, questionBankID)
}

// GetVisible returns the bank iff it belongs to readerTenantID (QuestionBank has
// no cross-tenant PUBLIC path). Delegates to the repository.
func (s *Service) GetVisible(ctx context.Context, readerTenantID, questionBankID string) (*QuestionBank, error) {
	return s.repo.GetVisible(ctx, readerTenantID, questionBankID)
}

// List returns active banks matching the filter. Used by GET
// /api/v1/me/question-banks (filter.OwnerGCID = caller's GCID).
func (s *Service) List(ctx context.Context, tenantID string, f ListFilter) ([]*QuestionBank, error) {
	return s.repo.List(ctx, tenantID, f)
}

// EnrichedQuestionBankItem is the workbench READ-model for one membership row:
// the cross-aggregate question reference (question_id / position / added_at)
// PLUS the host atom_id and canonical question_type, resolved at READ time. The
// QuestionBank aggregate persists ONLY question_id (a UUID reference with no FK
// per ddd-enforcement #3); atom_id is deliberately NOT stored on the bank. The
// A+ workbench needs it because its row-level conveniences (preview / edit /
// tags / clone) address the atom at /api/atoms/{atom_id}, NOT the question
// sub-resource — so a bare question_id row (what the bank stores) would
// mis-address those calls (the inverse of the add-to-bank atom_id↔question_id
// fix). question_type is resolved for free in the same lookup (lets the row
// render its MCQ/OE badge without a second round-trip).
type EnrichedQuestionBankItem struct {
	QuestionID   string `json:"question_id"`
	AtomID       string `json:"atom_id"`
	QuestionType string `json:"question_type"`
	// Prompt is the question stem, resolved for free off the same lookup — it
	// labels the workbench row in human-readable form (the row would otherwise
	// show only the opaque question_id).
	Prompt   string    `json:"prompt"`
	Position int       `json:"position"`
	AddedAt  time.Time `json:"added_at"`
}

// ListItemsEnriched returns the bank's membership rows, each enriched with its
// host atom_id + canonical question_type, resolved TENANT-SCOPED via the same
// QuestionLookup.Details call AssembleTestSet uses (RLS + exam-security: a pool
// references only same-tenant questions). Powers GET
// /api/v1/question-banks/{id}/questions for the A+ workbench.
//
// An empty bank needs no lookup (nothing to resolve). A populated bank fails
// loud if the lookup is unwired or a row no longer resolves — a row whose atom
// cannot be resolved must NEVER be returned bare, since the workbench would
// mis-address /api/atoms/{atom_id} with the question_id (the very bug this
// method exists to prevent).
func (s *Service) ListItemsEnriched(ctx context.Context, readerTenantID, questionBankID string) ([]EnrichedQuestionBankItem, error) {
	b, err := s.repo.GetVisible(ctx, readerTenantID, questionBankID)
	if err != nil {
		return nil, err
	}
	if len(b.Items) == 0 {
		return []EnrichedQuestionBankItem{}, nil
	}
	if s.questions == nil {
		return nil, fmt.Errorf("%w: ListItemsEnriched questionLookup", ErrGateNotWired)
	}
	out := make([]EnrichedQuestionBankItem, 0, len(b.Items))
	for _, it := range b.Items {
		atomID, qType, prompt, derr := s.questions.Details(ctx, b.TenantID, it.QuestionID)
		if derr != nil {
			return nil, fmt.Errorf("%w: ListItemsEnriched resolve question %s: %w", ErrRepository, it.QuestionID, derr)
		}
		out = append(out, EnrichedQuestionBankItem{
			QuestionID:   it.QuestionID,
			AtomID:       atomID,
			QuestionType: qType,
			Prompt:       prompt,
			Position:     it.Position,
			AddedAt:      it.AddedAt,
		})
	}
	return out, nil
}

// ItemSortKeys is the whitelist of sortable columns for a bank's question list
// (keyed by the API sort token; the raw user value NEVER reaches SQL — the pg
// adapter maps the token to a fixed column, so there is no injection surface).
var ItemSortKeys = map[string]struct{}{
	"position": {},
	"added_at": {},
	"prompt":   {},
}

// ItemPageFilter is the server-side filter/sort/paginate for a bank's question
// list: a keyword (case-insensitive substring on the prompt), a question-type
// filter, a whitelisted sort, and limit/offset. Banks can be HUGE, so the page
// is resolved in SQL (an intra-DB JOIN), never by loading every member.
type ItemPageFilter struct {
	Query    string   // prompt substring; "" = no keyword filter
	Types    []string // question_type filter (mcq/oe); empty = all types
	SortKey  string   // one of ItemSortKeys; unknown/"" ⇒ position
	SortDesc bool     // false = ASC
	Limit    int      // page size (caller clamps to a sane max)
	Offset   int      // (page-1)*limit
}

// ItemPager resolves a filtered/sorted/paginated page of a bank's questions —
// enriched with atom_id + question_type + prompt — plus the total match count,
// via an intra-DB JOIN (question_bank_items ⋈ questions, both in chora_creation;
// NOT a cross-DB query). Narrow read-model port (the pg repo implements it) so
// the workbench list never loads the whole bank.
type ItemPager interface {
	ListItemsPage(ctx context.Context, tenantID, questionBankID string, f ItemPageFilter) (items []EnrichedQuestionBankItem, total int, err error)
}

// SetItemPager attaches the server-side pager (production passes the pg repo).
func (s *Service) SetItemPager(p ItemPager) *Service {
	s.pager = p
	return s
}

// ListItemsEnrichedPage returns one filtered/sorted page of the bank's questions
// + the total match count, resolved server-side (huge-bank-safe). Tenant-scoped
// (RLS) — a non-existent / cross-tenant bank yields an empty page. Fails loud if
// the pager is not wired.
func (s *Service) ListItemsEnrichedPage(ctx context.Context, readerTenantID, questionBankID string, f ItemPageFilter) ([]EnrichedQuestionBankItem, int, error) {
	if s.pager == nil {
		return nil, 0, fmt.Errorf("%w: ListItemsEnrichedPage itemPager", ErrGateNotWired)
	}
	return s.pager.ListItemsPage(ctx, readerTenantID, questionBankID, f)
}

// UpdateMetadataInput is the parameter envelope for UpdateMetadata.
type UpdateMetadataInput struct {
	TenantID       string
	QuestionBankID string
	OwnerGCID      string // for authorisation; must match b.OwnerGCID
	Params         UpdateParams
}

// UpdateMetadata applies a PATCH to the bank's metadata. Returns ErrNotFound
// if the bank does not exist / belongs to another tenant; ErrForbidden when
// the caller is not the owner.
func (s *Service) UpdateMetadata(ctx context.Context, in UpdateMetadataInput) (*QuestionBank, error) {
	b, err := s.repo.Get(ctx, in.TenantID, in.QuestionBankID)
	if err != nil {
		return nil, err
	}
	if b.OwnerGCID != in.OwnerGCID {
		return nil, ErrForbidden
	}
	if err := b.UpdateMetadata(in.Params); err != nil {
		return nil, err
	}
	if err := s.repo.Save(ctx, b); err != nil {
		return nil, fmt.Errorf("%w: UpdateMetadata save: %w", ErrRepository, err)
	}
	return b, nil
}

// ReorderItemsInput is the parameter envelope for ReorderItems.
type ReorderItemsInput struct {
	TenantID       string
	QuestionBankID string
	OwnerGCID      string // for authorisation; must match b.OwnerGCID
	QuestionIDs    []string
}

// ReorderItems reorders the bank's questions to match in.QuestionIDs. Returns
// ErrNotFound (missing/other-tenant), ErrForbidden (non-owner), or
// ErrReorderMismatch (not a permutation). The owner gate fires BEFORE the
// aggregate mutation.
func (s *Service) ReorderItems(ctx context.Context, in ReorderItemsInput) (*QuestionBank, error) {
	b, err := s.repo.Get(ctx, in.TenantID, in.QuestionBankID)
	if err != nil {
		return nil, err
	}
	if b.OwnerGCID != in.OwnerGCID {
		return nil, ErrForbidden
	}
	if err := b.ReorderItems(in.QuestionIDs); err != nil {
		return nil, err
	}
	if err := s.repo.Save(ctx, b); err != nil {
		return nil, fmt.Errorf("%w: ReorderItems save: %w", ErrRepository, err)
	}
	return b, nil
}

// AddQuestionInput is the parameter envelope for AddQuestion.
type AddQuestionInput struct {
	TenantID       string
	QuestionBankID string
	OwnerGCID      string
	QuestionID     string
}

// AddQuestion validates question existence (TENANT-SCOPED), applies the
// aggregate mutator, and persists. A missing question returns
// ErrQuestionDoesNotExist (HTTP 404). The owner gate fires BEFORE the lookup
// so a non-owner request never touches the questions table.
func (s *Service) AddQuestion(ctx context.Context, in AddQuestionInput) (*QuestionBank, error) {
	b, err := s.repo.Get(ctx, in.TenantID, in.QuestionBankID)
	if err != nil {
		return nil, err
	}
	if b.OwnerGCID != in.OwnerGCID {
		return nil, ErrForbidden
	}
	if s.questions == nil {
		return nil, fmt.Errorf("%w: AddQuestion questionLookup", ErrGateNotWired)
	}
	exists, lookupErr := s.questions.Resolve(ctx, b.TenantID, in.QuestionID)
	if lookupErr != nil {
		return nil, fmt.Errorf("%w: AddQuestion question lookup: %w", ErrRepository, lookupErr)
	}
	if !exists {
		return nil, ErrQuestionDoesNotExist
	}
	if err := b.AddItem(in.QuestionID); err != nil {
		return nil, err
	}
	if err := s.repo.Save(ctx, b); err != nil {
		return nil, fmt.Errorf("%w: AddQuestion save: %w", ErrRepository, err)
	}
	return b, nil
}

// RemoveQuestionInput is the parameter envelope for RemoveQuestion.
type RemoveQuestionInput struct {
	TenantID       string
	QuestionBankID string
	OwnerGCID      string
	QuestionID     string
}

// RemoveQuestion drops a question from the bank.
func (s *Service) RemoveQuestion(ctx context.Context, in RemoveQuestionInput) (*QuestionBank, error) {
	b, err := s.repo.Get(ctx, in.TenantID, in.QuestionBankID)
	if err != nil {
		return nil, err
	}
	if b.OwnerGCID != in.OwnerGCID {
		return nil, ErrForbidden
	}
	if err := b.RemoveItem(in.QuestionID); err != nil {
		return nil, err
	}
	if err := s.repo.Save(ctx, b); err != nil {
		return nil, fmt.Errorf("%w: RemoveQuestion save: %w", ErrRepository, err)
	}
	return b, nil
}

// DeleteInput is the parameter envelope for Delete.
type DeleteInput struct {
	TenantID       string
	QuestionBankID string
	OwnerGCID      string // for authorisation
}

// Delete soft-deletes the bank (cascade-soft-deletes children at the repo).
func (s *Service) Delete(ctx context.Context, in DeleteInput) error {
	b, err := s.repo.Get(ctx, in.TenantID, in.QuestionBankID)
	if err != nil {
		return err
	}
	if b.OwnerGCID != in.OwnerGCID {
		return ErrForbidden
	}
	if err := b.Delete(); err != nil {
		return err
	}
	if err := s.repo.Save(ctx, b); err != nil {
		return fmt.Errorf("%w: Delete save: %w", ErrRepository, err)
	}
	return nil
}

// AssembleTestSet assembles a DRAFT TestSet from the bank's active questions by
// REUSING the chora.creation.question_batch.accepted.v1 event (no new contract):
// the chora-delivery batch_testset_inbox subscriber (origin-agnostic) assembles
// the DRAFT from it. v1 takes ALL active questions in the bank with
// DefaultTestSetPoints each and sequential 1-based display_order (bank order);
// the author refines the resulting DRAFT in chora-delivery later.
//
// Owner-gated (ErrForbidden → 403). Returns ErrNotFound (404) for a missing
// bank, ErrTitleRequired (400) for a blank title, ErrEmptyBank (422) for a bank
// with no active questions, and ErrAssemblerNotWired (503) when the publisher is
// unwired (fail-loud — never a silent no-op).
func (s *Service) AssembleTestSet(ctx context.Context, in AssembleTestSetInput) (*AssembleTestSetResult, error) {
	b, err := s.repo.Get(ctx, in.TenantID, in.QuestionBankID)
	if err != nil {
		return nil, err // ErrNotFound → 404
	}
	if b.OwnerGCID != in.OwnerGCID {
		return nil, ErrForbidden
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, ErrTitleRequired
	}
	if len(title) > MaxTestSetTitleLength {
		return nil, fmt.Errorf("title too long: %d > %d", len(title), MaxTestSetTitleLength)
	}
	description := strings.TrimSpace(in.Description)
	if len(description) > MaxTestSetDescriptionLength {
		return nil, fmt.Errorf("description too long: %d > %d", len(description), MaxTestSetDescriptionLength)
	}
	if len(b.Items) == 0 {
		return nil, ErrEmptyBank
	}
	// Fail loud BEFORE the resolve work if the endpoint can't actually publish.
	if s.assembler == nil {
		return nil, ErrAssemblerNotWired
	}
	if s.questions == nil {
		return nil, fmt.Errorf("%w: AssembleTestSet questionLookup", ErrGateNotWired)
	}

	items := make([]AssembledTestSetItem, 0, len(b.Items))
	for i, it := range b.Items {
		atomID, qType, _, derr := s.questions.Details(ctx, b.TenantID, it.QuestionID)
		if derr != nil {
			return nil, fmt.Errorf("questionbank.Service.AssembleTestSet: resolve question %s: %w", it.QuestionID, derr)
		}
		items = append(items, AssembledTestSetItem{
			QuestionAtomID: atomID,
			QuestionID:     it.QuestionID,
			QuestionType:   qType,
			Points:         DefaultTestSetPoints,
			DisplayOrder:   i + 1, // contiguous 1-based, bank order
		})
	}

	jobID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("questionbank.Service.AssembleTestSet: uuidv7: %w", err)
	}

	evt := AssembledTestSet{
		JobID: jobID.String(),
		// host_atom_id decision: a bank-assembled set has NO single host atom
		// (it curates references to many independent question atoms). The
		// delivery subscriber treats host_atom_id as OPAQUE provenance (it never
		// reads it for assembly), so the question_bank_id is the honest,
		// traceable provenance value — borrowing an arbitrary item's atom_id
		// would falsely imply a single host atom.
		HostAtomID:  b.QuestionBankID,
		TenantID:    b.TenantID,
		AuthorGCID:  b.OwnerGCID,
		Title:       title,
		Description: description,
		Items:       items,
		AcceptedAt:  time.Now().UTC(),
		Traceparent: in.Traceparent,
	}
	if err := s.assembler.PublishAssembledTestSet(ctx, evt); err != nil {
		return nil, fmt.Errorf("questionbank.Service.AssembleTestSet: publish: %w", err)
	}
	return &AssembleTestSetResult{JobID: jobID.String(), Status: TestSetStatusAssembling}, nil
}
