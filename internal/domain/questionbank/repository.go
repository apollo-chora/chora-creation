// Repository + QuestionLookup — the QuestionBank persistence + cross-aggregate
// validation ports (W3.B.1).
//
// These are hexagonal ports: the domain owns the interfaces; adapters
// (in-memory, Cloud SQL via pgx, etc.) implement them. The domain MUST NOT
// import any adapter package.
package questionbank

import "context"

// Repository is the QuestionBank persistence port. Production wires a pgx-backed
// implementation against chora_creation; tests use an in-memory implementation.
//
// All methods operate inside a per-tenant RLS transaction at the adapter layer
// (SET LOCAL chora.tenant_id) — see internal/adapter/pg/runtime.go for the
// canonical pattern. The repository receives an explicit tenantID argument so
// the adapter can apply RLS before the user query.
type Repository interface {
	// Save persists the aggregate (UPSERT on question_bank_id). Child
	// QuestionBankItem rows are reconciled against the in-memory Items slice:
	// existing rows are soft-deleted then the active subset is UPSERT-
	// resurrected. Cascade-soft-delete of children when the parent is
	// soft-deleted lives here (per ddd-enforcement Aggregate Invariant #5).
	Save(ctx context.Context, b *QuestionBank) error

	// Get returns the bank for (tenantID, questionBankID) iff it exists, belongs
	// to tenantID, and is not soft-deleted. Returns ErrNotFound otherwise.
	Get(ctx context.Context, tenantID, questionBankID string) (*QuestionBank, error)

	// GetVisible returns the bank iff it belongs to readerTenantID and is not
	// soft-deleted. Because QuestionBank has NO PUBLIC visibility (exam-security),
	// there is no cross-tenant read path — GetVisible is same-tenant only and
	// adapters may delegate to Get. The seam is kept for API symmetry with
	// the Collection aggregate and for a future, ADR-gated sharing model.
	GetVisible(ctx context.Context, readerTenantID, questionBankID string) (*QuestionBank, error)

	// List returns active (non-deleted) banks owned by the supplied tenant +
	// filter (visibility, owner_gcid, pagination).
	List(ctx context.Context, tenantID string, filter ListFilter) ([]*QuestionBank, error)
}

// QuestionLookup is the port the Service uses to verify question existence
// before adding a question to a bank. Production wires this against the
// chora_creation.questions table (reusing the existing QuestionRepository);
// tests inject an in-memory stub.
//
// Per .claude/rules/ddd-enforcement.md Aggregate Invariant #3, cross-aggregate
// references (QuestionBankItem.QuestionID → Question.QuestionID) are UUIDs without
// FK constraint and validated via this domain service. Because questions live
// in the SAME chora_creation database, this is an intra-DB cross-aggregate
// lookup, NOT a cross-DB query (cross-DB queries remain FORBIDDEN).
//
// TENANT-SCOPED BY DESIGN: Resolve takes tenantID. The questions table carries
// a tenant_isolation RLS policy and chora_creation_app_rw is NOBYPASSRLS, so a
// tenant-less lookup cannot work; tenant-scoping is the only correct (fail-loud)
// wiring. It also enforces exam-security — a pool can reference ONLY same-tenant
// questions (a cross-tenant question_id resolves to "does not exist" → 404).
//
// This used to note a divergence from collection.AtomLookup, which returned an
// atom's owning tenant to support PUBLIC cross-tenant collections. That port is
// DELETED (ADR-233 D7) and this paragraph was right about why: its untenanted
// `SELECT tenant_id FROM learning_atoms WHERE atom_id = $1` did not merely offend
// RLS, it threw — `(current_setting('chora.tenant_id', true))::uuid` is
// `”::uuid` (22P02) on a connection whose GUC has reset — so every
// POST /collections/{id}/atoms 500'd and collection_atoms never held a row.
// Collections now resolve atom existence through a tenant-scoped read too, and
// PUBLIC/cross-tenant collections no longer exist. QuestionBank's shape was the
// right one all along; there is no divergence left to describe.
type QuestionLookup interface {
	// Resolve returns (true, nil) when the question exists + is active within
	// tenantID; (false, nil) when it does not exist / is soft-deleted /
	// belongs to another tenant. Errors are returned only for infrastructure
	// failures.
	Resolve(ctx context.Context, tenantID, questionID string) (exists bool, err error)

	// Details resolves the question's owning LearningAtom id + canonical
	// question_type + prompt (TENANT-SCOPED, same RLS + exam-security reasoning
	// as Resolve). Used by Service.AssembleTestSet (W3.B.2) to build the per-item
	// event shape, and by Service.ListItemsEnriched to label workbench rows with
	// the question prompt. Unlike Resolve, a missing / soft-deleted / cross-tenant
	// question is a fail-loud ERROR here (not an empty-string success): a bank
	// item that no longer resolves must surface, never silently drop.
	Details(ctx context.Context, tenantID, questionID string) (atomID, questionType, prompt string, err error)
}
