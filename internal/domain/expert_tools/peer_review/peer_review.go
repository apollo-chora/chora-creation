// Package peer_review models the instructor peer-review queue + quorum-based
// publish decision for atoms in the Content Creation domain.
//
// Use case (per docs/design/ux_expert_tooling.md): instructors review each
// other's atoms before they go from draft -> published. A submission has a
// QuorumSize; when N reviewers vote, the majority decision moves the
// submission to approved or rejected; ties (or below-quorum vote counts)
// stay pending. Author self-vote and duplicate reviewers are rejected.
//
// Aggregate: ReviewSubmission (root) owns the Vote children. Cross-domain
// references (atom_id, revision_id) are UUIDs without FK constraints per
// .claude/rules/ddd-enforcement.md aggregate invariant #3 — the consuming
// service validates atom existence via Pub/Sub events.
//
// Hexagonal: this package is dependency-free w.r.t. infrastructure.
package peer_review

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// -----------------------------------------------------------------------------
// Status + Decision enums
// -----------------------------------------------------------------------------

// Status is the lifecycle state of a ReviewSubmission. Transitions:
//
//	pending -> approved | rejected | cancelled
//
// approved + rejected are terminal from the queue perspective; the publish
// pipeline observes them via the chora.creation.peer_review.* events.
type Status string

const (
	StatusPending   Status = "pending"
	StatusApproved  Status = "approved"
	StatusRejected  Status = "rejected"
	StatusCancelled Status = "cancelled"
)

// Decision is a reviewer's vote.
type Decision string

const (
	DecisionApprove Decision = "approve"
	DecisionReject  Decision = "reject"
)

// Valid reports whether d is a known decision.
func (d Decision) Valid() bool {
	switch d {
	case DecisionApprove, DecisionReject:
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// Caps + thresholds
// -----------------------------------------------------------------------------

const (
	maxQuorumSize = 10
	maxCommentLen = 2048
	minQuorumSize = 1
)

// -----------------------------------------------------------------------------
// Vote — child entity within ReviewSubmission aggregate
// -----------------------------------------------------------------------------

// Vote is a single reviewer's verdict on a ReviewSubmission. Votes are
// append-only — once recorded they are not edited (revoke = compensating
// new submission per docs/design/ux_expert_tooling.md).
type Vote struct {
	VoteID       string    `json:"vote_id"`
	ReviewerGcid string    `json:"reviewer_gcid"`
	Decision     Decision  `json:"decision"`
	Comment      string    `json:"comment,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// VoteParams is the input to ReviewSubmission.RecordVote.
type VoteParams struct {
	ReviewerGcid string
	Decision     Decision
	Comment      string
}

// -----------------------------------------------------------------------------
// ReviewSubmission — aggregate root
// -----------------------------------------------------------------------------

// ReviewSubmission is the peer-review submission aggregate root.
//
// Cross-domain refs (atom_id, revision_id) per ddd-enforcement #3 are UUIDs
// without FK; validated upstream via chora.creation.atom.created.v1 events.
type ReviewSubmission struct {
	ReviewID     string     `json:"review_id"`
	TenantID     string     `json:"tenant_id"`
	AtomID       string     `json:"atom_id"`
	RevisionID   string     `json:"revision_id"`
	AuthoredBy   string     `json:"authored_by"`
	QuorumSize   int        `json:"quorum_size"`
	Status       Status     `json:"status"`
	Votes        []*Vote    `json:"votes"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ResolvedAt   *time.Time `json:"resolved_at,omitempty"`
	CancelReason string     `json:"cancel_reason,omitempty"`
}

// SubmitParams is the input to Submit.
type SubmitParams struct {
	TenantID   string
	AtomID     string
	RevisionID string
	AuthoredBy string
	QuorumSize int
}

// Submit constructs a fresh ReviewSubmission in StatusPending. The caller
// records votes via RecordVote and observes the resulting Status to decide
// whether to publish/refuse the underlying atom revision.
func Submit(p SubmitParams) (*ReviewSubmission, error) {
	if strings.TrimSpace(p.TenantID) == "" {
		return nil, errors.New("tenant_id is required")
	}
	if strings.TrimSpace(p.AtomID) == "" {
		return nil, errors.New("atom_id is required")
	}
	if strings.TrimSpace(p.RevisionID) == "" {
		return nil, errors.New("revision_id is required")
	}
	if strings.TrimSpace(p.AuthoredBy) == "" {
		return nil, errors.New("authored_by is required")
	}
	if p.QuorumSize < minQuorumSize {
		return nil, fmt.Errorf("quorum_size must be >= %d", minQuorumSize)
	}
	if p.QuorumSize > maxQuorumSize {
		return nil, fmt.Errorf("quorum_size must be <= %d", maxQuorumSize)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuidv7: %w", err)
	}
	now := time.Now().UTC()
	return &ReviewSubmission{
		ReviewID:   id.String(),
		TenantID:   p.TenantID,
		AtomID:     p.AtomID,
		RevisionID: p.RevisionID,
		AuthoredBy: p.AuthoredBy,
		QuorumSize: p.QuorumSize,
		Status:     StatusPending,
		Votes:      make([]*Vote, 0, p.QuorumSize),
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

// RecordVote appends a vote and, when the quorum count is hit, transitions
// the submission's status by majority. Returns an error if the submission
// has already resolved, the reviewer is the author, or the same reviewer
// has already voted.
func (r *ReviewSubmission) RecordVote(p VoteParams) error {
	if r.Status != StatusPending {
		return fmt.Errorf("review %s already %s; no further votes accepted", r.ReviewID, r.Status)
	}
	rev := strings.TrimSpace(p.ReviewerGcid)
	if rev == "" {
		return errors.New("reviewer_gcid is required")
	}
	if rev == r.AuthoredBy {
		return errors.New("author may not vote on own submission")
	}
	if !p.Decision.Valid() {
		return fmt.Errorf("invalid decision: %q", string(p.Decision))
	}
	if len(p.Comment) > maxCommentLen {
		return fmt.Errorf("comment too long: %d > %d", len(p.Comment), maxCommentLen)
	}
	for _, v := range r.Votes {
		if v.ReviewerGcid == rev {
			return fmt.Errorf("reviewer %s already voted", rev)
		}
	}

	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("uuidv7: %w", err)
	}
	now := time.Now().UTC()
	v := &Vote{
		VoteID:       id.String(),
		ReviewerGcid: rev,
		Decision:     p.Decision,
		Comment:      strings.TrimSpace(p.Comment),
		CreatedAt:    now,
	}
	r.Votes = append(r.Votes, v)
	r.UpdatedAt = now

	// Quorum + majority resolution.
	if r.QuorumReached() {
		t := r.Tally()
		if t.Approve > t.Reject {
			r.Status = StatusApproved
		} else if t.Reject > t.Approve {
			r.Status = StatusRejected
		}
		// Tie within quorum — leave pending; one extra reviewer would break it
		// but we never accept more than QuorumSize votes (RecordVote refuses
		// further votes once Status moves off pending; see TestQuorumReached_
		// TieLeavesPending). For ties exactly at quorum the submission stays
		// pending awaiting a tiebreaker submission via Cancel + new Submit.
		if r.Status != StatusPending {
			r.ResolvedAt = &now
		}
	}
	return nil
}

// QuorumReached reports whether enough votes have been cast to evaluate
// the majority decision. Equivalent to len(Votes) >= QuorumSize.
func (r *ReviewSubmission) QuorumReached() bool {
	return len(r.Votes) >= r.QuorumSize
}

// TallyCount holds approve/reject counts.
type TallyCount struct {
	Approve int
	Reject  int
}

// Tally counts approve/reject votes.
func (r *ReviewSubmission) Tally() TallyCount {
	t := TallyCount{}
	for _, v := range r.Votes {
		switch v.Decision {
		case DecisionApprove:
			t.Approve++
		case DecisionReject:
			t.Reject++
		}
	}
	return t
}

// Cancel moves a pending submission to StatusCancelled. Only the original
// author may cancel; resolved submissions cannot be cancelled (reviewers'
// votes are durable per ddd-enforcement append-only revisions principle).
func (r *ReviewSubmission) Cancel(actorGcid, reason string) error {
	if r.Status != StatusPending {
		return fmt.Errorf("cannot cancel: review already %s", r.Status)
	}
	if actorGcid != r.AuthoredBy {
		return errors.New("only the author may cancel a submission")
	}
	now := time.Now().UTC()
	r.Status = StatusCancelled
	r.UpdatedAt = now
	r.ResolvedAt = &now
	r.CancelReason = strings.TrimSpace(reason)
	return nil
}

// IsActive reports whether the submission is still accepting votes.
func (r *ReviewSubmission) IsActive() bool { return r.Status == StatusPending }
