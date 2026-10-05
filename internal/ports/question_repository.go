// QuestionRepository — the persistence port for the Question aggregate.
//
// Implementations (pgx adapter, in-memory adapter for tests) live in
// internal/adapter/pg and internal/adapter/inmem. The domain owns the
// interface; adapters depend on it. NEVER reverse the dependency.
package ports

import (
	"context"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

// QuestionRepository is the hexagonal port for persistence of the Question
// aggregate + its append-only revision history.
//
// Save persists the parent Question + a new QuestionRevision in the same
// transaction (per the migration 0006 + 0007 atomicity expectation): both
// rows are inserted in a single round-trip; `questions.latest_revision_id`
// is set to the new revision's id.
//
// GetByAtomID returns the active (non-deleted) Question for an atom + its
// latest revision in one round-trip per design §2.6 (`LoadAtomWithQuestions`).
// Returns question.ErrNotFound when the atom has no live question.
//
// GetByID is the same shape as GetByAtomID but keyed on the question id —
// used by PATCH / DELETE handlers that already have the question_id.
//
// AppendRevision adds a new revision row for an existing question. Used by
// the orchestrator when the parent Question has already been persisted and
// only the revision payload needs appending (rare — most callers use Save
// for atomic create + append).
//
// SoftDelete sets deleted_at = now() on the matching question. Idempotent.
//
// SearchQuestions returns the lightweight SearchResult projection for the
// A+ X.2 test-set-editor question picker (ADR-155 D1; FE B-FE-X5). Filter
// shape (q substring + types list + tenant_id + page/per) is captured by
// question.SearchFilter. The second return value is the total row count
// across all pages (for the FE "X questions" header). The atoms ARE the
// queryable surface intra-domain; the pg adapter joins learning_atoms.
type QuestionRepository interface {
	Save(ctx context.Context, q *question.Question, rev *question.QuestionRevision) error
	GetByAtomID(ctx context.Context, tenantID, atomID string) (*question.Question, *question.QuestionRevision, error)
	GetByID(ctx context.Context, tenantID, questionID string) (*question.Question, *question.QuestionRevision, error)
	AppendRevision(ctx context.Context, rev *question.QuestionRevision) error
	SoftDelete(ctx context.Context, tenantID, questionID string) error
	SearchQuestions(ctx context.Context, filter question.SearchFilter) ([]question.SearchResult, int, error)
}
