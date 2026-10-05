// question_lookup_adapter.go — pgx-backed implementation of
// questionbank.QuestionLookup (W3.B.1, 2026-06-28).
//
// It REUSES the existing QuestionRepository (ports.QuestionRepository) rather
// than issuing its own SQL: Resolve delegates to GetByID and maps
// question.ErrNotFound → exists=false. GetByID already runs inside a
// tenant-scoped RLS transaction (SET LOCAL chora.tenant_id), so this adapter is
// RLS-correct by construction.
//
// TENANT-SCOPED BY DESIGN: the questions table carries a tenant_isolation RLS
// policy and chora_creation_app_rw is NOBYPASSRLS — a tenant-less lookup would
// silently return zero rows. Scoping to the bank's tenant is the only fail-loud
// wiring AND enforces exam-security (a pool references only same-tenant
// questions; a cross-tenant question_id resolves as "does not exist").
//
// This is an intra-DB cross-aggregate lookup (question_banks → questions, both in
// chora_creation) — NOT a cross-DB query (which remains FORBIDDEN).
package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/domain/questionbank"
)

// questionByIDResolver is the narrow slice of the question repository this
// adapter needs. *QuestionRepository (and ports.QuestionRepository) satisfy it;
// tests inject a minimal fake.
type questionByIDResolver interface {
	GetByID(ctx context.Context, tenantID, questionID string) (*question.Question, *question.QuestionRevision, error)
}

// QuestionLookupAdapter is the pgx-backed implementation of
// questionbank.QuestionLookup.
type QuestionLookupAdapter struct {
	repo questionByIDResolver
}

// NewQuestionLookupAdapter wraps a question-by-id resolver (production passes
// the wired *QuestionRepository / ports.QuestionRepository).
func NewQuestionLookupAdapter(repo questionByIDResolver) *QuestionLookupAdapter {
	return &QuestionLookupAdapter{repo: repo}
}

// Resolve returns (true, nil) when the question exists + is active within
// tenantID; (false, nil) when it does not exist / is soft-deleted / belongs to
// another tenant; (false, err) on infrastructure failure.
func (a *QuestionLookupAdapter) Resolve(ctx context.Context, tenantID, questionID string) (bool, error) {
	if a.repo == nil {
		return false, errors.New("pg.QuestionLookupAdapter.Resolve: question repo not wired")
	}
	_, _, err := a.repo.GetByID(ctx, tenantID, questionID)
	if err != nil {
		if errors.Is(err, question.ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("pg.QuestionLookupAdapter.Resolve(%q): %w", questionID, err)
	}
	return true, nil
}

// Details resolves the question's owning atom id + canonical question_type +
// prompt within tenantID. Unlike Resolve, a missing question is a fail-loud
// ERROR: a bank item that no longer resolves at assemble-time must surface, not
// silently drop (W3.B.2). Reuses GetByID so the read runs inside the same
// tenant-scoped RLS transaction (and the prompt comes free off the same load).
func (a *QuestionLookupAdapter) Details(ctx context.Context, tenantID, questionID string) (string, string, string, error) {
	if a.repo == nil {
		return "", "", "", errors.New("pg.QuestionLookupAdapter.Details: question repo not wired")
	}
	q, _, err := a.repo.GetByID(ctx, tenantID, questionID)
	if err != nil {
		return "", "", "", fmt.Errorf("pg.QuestionLookupAdapter.Details(%q): %w", questionID, err)
	}
	return q.AtomID, string(q.Type), q.Prompt, nil
}

// Compile-time check.
var _ questionbank.QuestionLookup = (*QuestionLookupAdapter)(nil)
