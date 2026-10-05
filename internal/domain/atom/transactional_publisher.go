// transactional_publisher.go - the atom write port that keeps the state
// change and the event announcing it in ONE unit of work.
//
// Why this port exists
// --------------------
// The write path used to persist the aggregate and then enqueue its outbox
// row as a second, separate commit, logging and discarding a failure on the
// second. Chaos run chaos20260807a (2026-08-07) measured the consequence: 20
// of 747 atoms committed with no event behind them (2.68%), ten of which had
// already answered HTTP 201 to their author. No downstream domain will ever
// hear of those atoms.
//
// The envelope invariant in CLAUDE.md is "atomic state-write + event-publish
// via outbox". Atomic means one transaction: either the atom row and its
// outbox row both land, or neither does and the caller is told so.
package atom

import (
	"context"
	"errors"
)

// TransactionalPublisher persists a LearningAtom and enqueues the event that
// announces it as a single unit of work.
//
// Implementations MUST NOT report success when only one of the two writes
// landed. A caller that receives nil may tell its client the write happened;
// a caller that receives an error must assume nothing was committed.
type TransactionalPublisher interface {
	SaveAndPublish(ctx context.Context, a *LearningAtom, ev Event) error
}

// SequentialPublisher is the NON-ATOMIC implementation for wiring that has no
// transaction to join: the in-memory dev repositories and unit tests. It
// saves, then publishes, and returns the publish error so the caller still
// fails loud instead of fabricating a success.
//
// It cannot roll the save back. It must never be wired on a path backed by a
// real database, where pg.AtomTxWriter is the correct implementation. main()
// logs loudly whenever this one is selected.
type SequentialPublisher struct {
	repo Repository
	pub  EventPublisher
}

// NewSequentialPublisher wraps a repository and an optional publisher.
//
// A nil publisher is a legitimate deployment shape (event emission not wired,
// e.g. the legacy NewRouter shim): the save still runs and no event is
// claimed. A nil repository is a wiring bug and fails loud on first use.
func NewSequentialPublisher(repo Repository, pub EventPublisher) *SequentialPublisher {
	return &SequentialPublisher{repo: repo, pub: pub}
}

// SaveAndPublish satisfies TransactionalPublisher without a transaction.
func (s *SequentialPublisher) SaveAndPublish(ctx context.Context, a *LearningAtom, ev Event) error {
	if s == nil || s.repo == nil {
		return errors.New("atom.SequentialPublisher: repository not wired")
	}
	if a == nil {
		return errors.New("atom.SequentialPublisher: nil atom")
	}
	if err := s.repo.Save(ctx, a); err != nil {
		return err
	}
	if s.pub == nil {
		return nil
	}
	return s.pub.Publish(ctx, ev)
}

// Compile-time port assertion.
var _ TransactionalPublisher = (*SequentialPublisher)(nil)
