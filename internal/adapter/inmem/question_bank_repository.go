// question_bank_repository.go — in-memory adapter for questionbank.Repository (W3.B.1).
//
// Used by the HTTP handler test suite so the question-bank routes can be exercised
// end-to-end without a live database. NOT safe for production — does not
// survive a restart. Mirrors inmem.CollectionRepository.
package inmem

import (
	"context"
	"sync"

	"github.com/apollo-chora/chora-creation/internal/domain/questionbank"
)

// QuestionBankRepository is a goroutine-safe, map-backed QuestionBank repository.
type QuestionBankRepository struct {
	mu    sync.RWMutex
	banks map[string]*questionbank.QuestionBank // keyed by QuestionBankID
}

// NewQuestionBankRepository constructs an initialised repository.
func NewQuestionBankRepository() *QuestionBankRepository {
	return &QuestionBankRepository{banks: make(map[string]*questionbank.QuestionBank)}
}

// Save persists the bank. Stores a clone so callers cannot mutate repo state
// via the returned reference.
func (r *QuestionBankRepository) Save(_ context.Context, b *questionbank.QuestionBank) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	clone := *b
	clone.Items = cloneItems(b.Items)
	r.banks[b.QuestionBankID] = &clone
	return nil
}

// Get returns the bank iff it belongs to tenantID + is not soft-deleted.
func (r *QuestionBankRepository) Get(_ context.Context, tenantID, id string) (*questionbank.QuestionBank, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.banks[id]
	if !ok || b.TenantID != tenantID || b.DeletedAt != nil {
		return nil, questionbank.ErrNotFound
	}
	clone := *b
	clone.Items = cloneItems(b.Items)
	return &clone, nil
}

// GetVisible returns the bank iff it belongs to readerTenantID + is not
// soft-deleted. QuestionBank has NO PUBLIC visibility, so there is no cross-tenant
// read path — same-tenant only.
func (r *QuestionBankRepository) GetVisible(_ context.Context, readerTenantID, id string) (*questionbank.QuestionBank, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.banks[id]
	if !ok || b.DeletedAt != nil || b.TenantID != readerTenantID {
		return nil, questionbank.ErrNotFound
	}
	clone := *b
	clone.Items = cloneItems(b.Items)
	return &clone, nil
}

// List returns active banks for the supplied tenant + filter.
func (r *QuestionBankRepository) List(_ context.Context, tenantID string, f questionbank.ListFilter) ([]*questionbank.QuestionBank, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []*questionbank.QuestionBank{}
	for _, b := range r.banks {
		if b.TenantID != tenantID || b.DeletedAt != nil {
			continue
		}
		if f.OwnerGCID != "" && b.OwnerGCID != f.OwnerGCID {
			continue
		}
		if f.Visibility != "" && b.Visibility != f.Visibility {
			continue
		}
		if !f.MatchesQ(b.Name) {
			continue
		}
		if !f.MatchesTags(b.Tags) {
			continue
		}
		clone := *b
		clone.Items = cloneItems(b.Items)
		out = append(out, &clone)
	}
	// Mirror the pg ORDER BY (whitelisted sort + question_bank_id DESC tiebreak;
	// default created_at DESC) BEFORE paginating so the page is deterministic.
	questionbank.SortBanks(out, f.Sorts)
	if f.Limit > 0 && len(out) > f.Limit {
		if f.Offset > len(out) {
			f.Offset = len(out)
		}
		end := f.Offset + f.Limit
		if end > len(out) {
			end = len(out)
		}
		out = out[f.Offset:end]
	}
	return out, nil
}

func cloneItems(in []*questionbank.QuestionBankItem) []*questionbank.QuestionBankItem {
	if len(in) == 0 {
		return nil
	}
	out := make([]*questionbank.QuestionBankItem, len(in))
	for i, it := range in {
		c := *it
		out[i] = &c
	}
	return out
}

// Compile-time check.
var _ questionbank.Repository = (*QuestionBankRepository)(nil)
