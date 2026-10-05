// Package peer_review_test exercises the peer-review queue + quorum domain.
//
// TDD RED phase — these tests assume the implementation does NOT yet exist.
// Each test names an invariant from docs/design/ux_expert_tooling.md
// (instructor-instructor review of atoms before publish) and the Content
// Creation domain rules in .claude/rules/ddd-enforcement.md (atom-centric,
// soft-delete, append-only revisions, UUIDv7 ids).
package peer_review_test

import (
	"testing"
	"time"

	pr "github.com/apollo-chora/chora-creation/internal/domain/expert_tools/peer_review"
)

const (
	tenantA   = "01970000-0000-7000-8000-000000000001"
	tenantB   = "01970000-0000-7000-8000-000000000002"
	authorA   = "01970000-0000-7000-9000-000000000001"
	revwrA    = "01970000-0000-7000-9000-000000000002"
	revwrB    = "01970000-0000-7000-9000-000000000003"
	revwrC    = "01970000-0000-7000-9000-000000000004"
	atomA     = "01970000-0000-7000-aaaa-000000000001"
	atomB     = "01970000-0000-7000-aaaa-000000000002"
	revisionA = "01970000-0000-7000-bbbb-000000000001"
)

// -----------------------------------------------------------------------------
// Submission constructor invariants
// -----------------------------------------------------------------------------

func TestSubmit_AssignsUUIDv7ReviewID(t *testing.T) {
	t.Parallel()
	sub, err := pr.Submit(pr.SubmitParams{
		TenantID:   tenantA,
		AtomID:     atomA,
		RevisionID: revisionA,
		AuthoredBy: authorA,
		QuorumSize: 2,
	})
	if err != nil {
		t.Fatalf("Submit unexpected error: %v", err)
	}
	if sub.ReviewID == "" {
		t.Fatalf("ReviewID is empty")
	}
	if len(sub.ReviewID) != 36 {
		t.Errorf("ReviewID length = %d; want 36", len(sub.ReviewID))
	}
	if sub.ReviewID[14] != '7' {
		t.Errorf("UUIDv7 version char = %q; want '7'", string(sub.ReviewID[14]))
	}
}

func TestSubmit_StartsInPendingStatus(t *testing.T) {
	t.Parallel()
	sub, _ := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA,
		AuthoredBy: authorA, QuorumSize: 2,
	})
	if sub.Status != pr.StatusPending {
		t.Errorf("Status = %q; want %q", sub.Status, pr.StatusPending)
	}
}

func TestSubmit_RejectsMissingTenantID(t *testing.T) {
	t.Parallel()
	_, err := pr.Submit(pr.SubmitParams{
		TenantID: "", AtomID: atomA, RevisionID: revisionA, AuthoredBy: authorA, QuorumSize: 2,
	})
	if err == nil {
		t.Errorf("expected error for missing tenant_id; got nil")
	}
}

func TestSubmit_RejectsMissingAtomID(t *testing.T) {
	t.Parallel()
	_, err := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: "", RevisionID: revisionA, AuthoredBy: authorA, QuorumSize: 2,
	})
	if err == nil {
		t.Errorf("expected error for missing atom_id; got nil")
	}
}

func TestSubmit_RejectsMissingRevisionID(t *testing.T) {
	t.Parallel()
	_, err := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: "", AuthoredBy: authorA, QuorumSize: 2,
	})
	if err == nil {
		t.Errorf("expected error for missing revision_id; got nil")
	}
}

func TestSubmit_RejectsQuorumZero(t *testing.T) {
	t.Parallel()
	_, err := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, AuthoredBy: authorA, QuorumSize: 0,
	})
	if err == nil {
		t.Errorf("expected error for quorum=0; got nil")
	}
}

func TestSubmit_RejectsQuorumExcess(t *testing.T) {
	t.Parallel()
	_, err := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, AuthoredBy: authorA, QuorumSize: 50,
	})
	if err == nil {
		t.Errorf("expected error for quorum>10; got nil")
	}
}

func TestSubmit_AcceptsQuorumThree(t *testing.T) {
	t.Parallel()
	sub, err := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, AuthoredBy: authorA, QuorumSize: 3,
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if sub.QuorumSize != 3 {
		t.Errorf("QuorumSize = %d; want 3", sub.QuorumSize)
	}
}

// -----------------------------------------------------------------------------
// Vote — happy paths
// -----------------------------------------------------------------------------

func TestVote_AppendsToVoteList(t *testing.T) {
	t.Parallel()
	sub, _ := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, AuthoredBy: authorA, QuorumSize: 2,
	})
	if err := sub.RecordVote(pr.VoteParams{
		ReviewerGcid: revwrA, Decision: pr.DecisionApprove, Comment: "looks good",
	}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(sub.Votes) != 1 {
		t.Errorf("votes = %d; want 1", len(sub.Votes))
	}
	if sub.Votes[0].ReviewerGcid != revwrA {
		t.Errorf("vote reviewer = %q; want %q", sub.Votes[0].ReviewerGcid, revwrA)
	}
}

func TestVote_AssignsVoteIDAndTimestamp(t *testing.T) {
	t.Parallel()
	sub, _ := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, AuthoredBy: authorA, QuorumSize: 2,
	})
	before := time.Now().UTC()
	_ = sub.RecordVote(pr.VoteParams{
		ReviewerGcid: revwrA, Decision: pr.DecisionApprove,
	})
	after := time.Now().UTC()
	v := sub.Votes[0]
	if v.VoteID == "" {
		t.Errorf("vote_id empty")
	}
	if v.CreatedAt.Before(before) || v.CreatedAt.After(after) {
		t.Errorf("CreatedAt %v not in [%v, %v]", v.CreatedAt, before, after)
	}
}

// -----------------------------------------------------------------------------
// Vote — invariants
// -----------------------------------------------------------------------------

func TestVote_RejectsAuthorSelfVote(t *testing.T) {
	t.Parallel()
	sub, _ := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, AuthoredBy: authorA, QuorumSize: 2,
	})
	err := sub.RecordVote(pr.VoteParams{ReviewerGcid: authorA, Decision: pr.DecisionApprove})
	if err == nil {
		t.Errorf("expected error for author self-vote; got nil")
	}
}

func TestVote_RejectsDuplicateReviewer(t *testing.T) {
	t.Parallel()
	sub, _ := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, AuthoredBy: authorA, QuorumSize: 3,
	})
	_ = sub.RecordVote(pr.VoteParams{ReviewerGcid: revwrA, Decision: pr.DecisionApprove})
	err := sub.RecordVote(pr.VoteParams{ReviewerGcid: revwrA, Decision: pr.DecisionReject})
	if err == nil {
		t.Errorf("expected error for duplicate reviewer; got nil")
	}
}

func TestVote_RejectsInvalidDecision(t *testing.T) {
	t.Parallel()
	sub, _ := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, AuthoredBy: authorA, QuorumSize: 2,
	})
	err := sub.RecordVote(pr.VoteParams{ReviewerGcid: revwrA, Decision: "maybe"})
	if err == nil {
		t.Errorf("expected error for invalid decision; got nil")
	}
}

func TestVote_RejectsMissingReviewer(t *testing.T) {
	t.Parallel()
	sub, _ := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, AuthoredBy: authorA, QuorumSize: 2,
	})
	err := sub.RecordVote(pr.VoteParams{ReviewerGcid: "", Decision: pr.DecisionApprove})
	if err == nil {
		t.Errorf("expected error for missing reviewer; got nil")
	}
}

func TestVote_RejectsCommentTooLong(t *testing.T) {
	t.Parallel()
	sub, _ := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, AuthoredBy: authorA, QuorumSize: 2,
	})
	long := make([]byte, 2049)
	for i := range long {
		long[i] = 'x'
	}
	err := sub.RecordVote(pr.VoteParams{
		ReviewerGcid: revwrA, Decision: pr.DecisionApprove, Comment: string(long),
	})
	if err == nil {
		t.Errorf("expected error for comment > 2048 chars; got nil")
	}
}

// -----------------------------------------------------------------------------
// Quorum + status transitions
// -----------------------------------------------------------------------------

func TestQuorumReached_AllApproveTransitionsToApproved(t *testing.T) {
	t.Parallel()
	sub, _ := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, AuthoredBy: authorA, QuorumSize: 2,
	})
	_ = sub.RecordVote(pr.VoteParams{ReviewerGcid: revwrA, Decision: pr.DecisionApprove})
	if sub.Status != pr.StatusPending {
		t.Errorf("status after 1st vote = %q; want pending", sub.Status)
	}
	_ = sub.RecordVote(pr.VoteParams{ReviewerGcid: revwrB, Decision: pr.DecisionApprove})
	if sub.Status != pr.StatusApproved {
		t.Errorf("status after quorum approve = %q; want approved", sub.Status)
	}
	if !sub.QuorumReached() {
		t.Errorf("QuorumReached() = false; want true after quorum hit")
	}
}

func TestQuorumReached_RejectMajorityTransitionsToRejected(t *testing.T) {
	t.Parallel()
	sub, _ := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, AuthoredBy: authorA, QuorumSize: 3,
	})
	_ = sub.RecordVote(pr.VoteParams{ReviewerGcid: revwrA, Decision: pr.DecisionReject})
	_ = sub.RecordVote(pr.VoteParams{ReviewerGcid: revwrB, Decision: pr.DecisionReject})
	_ = sub.RecordVote(pr.VoteParams{ReviewerGcid: revwrC, Decision: pr.DecisionApprove})
	if sub.Status != pr.StatusRejected {
		t.Errorf("status = %q; want rejected (2 reject, 1 approve, quorum=3)", sub.Status)
	}
}

func TestQuorumReached_TieLeavesPending(t *testing.T) {
	t.Parallel()
	sub, _ := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, AuthoredBy: authorA, QuorumSize: 4,
	})
	_ = sub.RecordVote(pr.VoteParams{ReviewerGcid: revwrA, Decision: pr.DecisionApprove})
	_ = sub.RecordVote(pr.VoteParams{ReviewerGcid: revwrB, Decision: pr.DecisionReject})
	// quorum not yet hit
	if sub.QuorumReached() {
		t.Errorf("QuorumReached() = true; want false at 2/4 votes")
	}
	if sub.Status != pr.StatusPending {
		t.Errorf("status = %q; want pending", sub.Status)
	}
}

func TestRecordVote_RejectsAfterQuorum(t *testing.T) {
	t.Parallel()
	sub, _ := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, AuthoredBy: authorA, QuorumSize: 2,
	})
	_ = sub.RecordVote(pr.VoteParams{ReviewerGcid: revwrA, Decision: pr.DecisionApprove})
	_ = sub.RecordVote(pr.VoteParams{ReviewerGcid: revwrB, Decision: pr.DecisionApprove})
	err := sub.RecordVote(pr.VoteParams{ReviewerGcid: revwrC, Decision: pr.DecisionApprove})
	if err == nil {
		t.Errorf("expected error voting after quorum; got nil")
	}
}

// -----------------------------------------------------------------------------
// Tally helpers
// -----------------------------------------------------------------------------

func TestTally_CountsApproveAndReject(t *testing.T) {
	t.Parallel()
	sub, _ := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, AuthoredBy: authorA, QuorumSize: 5,
	})
	_ = sub.RecordVote(pr.VoteParams{ReviewerGcid: revwrA, Decision: pr.DecisionApprove})
	_ = sub.RecordVote(pr.VoteParams{ReviewerGcid: revwrB, Decision: pr.DecisionReject})
	_ = sub.RecordVote(pr.VoteParams{ReviewerGcid: revwrC, Decision: pr.DecisionApprove})
	tally := sub.Tally()
	if tally.Approve != 2 {
		t.Errorf("Approve = %d; want 2", tally.Approve)
	}
	if tally.Reject != 1 {
		t.Errorf("Reject = %d; want 1", tally.Reject)
	}
}

// -----------------------------------------------------------------------------
// Cancel
// -----------------------------------------------------------------------------

func TestCancel_TransitionsToCancelled(t *testing.T) {
	t.Parallel()
	sub, _ := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, AuthoredBy: authorA, QuorumSize: 2,
	})
	if err := sub.Cancel(authorA, "wrong revision"); err != nil {
		t.Fatalf("Cancel unexpected error: %v", err)
	}
	if sub.Status != pr.StatusCancelled {
		t.Errorf("status = %q; want cancelled", sub.Status)
	}
}

func TestCancel_OnlyAuthorMayCancel(t *testing.T) {
	t.Parallel()
	sub, _ := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, AuthoredBy: authorA, QuorumSize: 2,
	})
	if err := sub.Cancel(revwrA, "stop"); err == nil {
		t.Errorf("expected error when non-author cancels; got nil")
	}
}

func TestCancel_RejectsAfterQuorum(t *testing.T) {
	t.Parallel()
	sub, _ := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA, AuthoredBy: authorA, QuorumSize: 2,
	})
	_ = sub.RecordVote(pr.VoteParams{ReviewerGcid: revwrA, Decision: pr.DecisionApprove})
	_ = sub.RecordVote(pr.VoteParams{ReviewerGcid: revwrB, Decision: pr.DecisionApprove})
	if err := sub.Cancel(authorA, "too late"); err == nil {
		t.Errorf("expected error cancelling after quorum; got nil")
	}
}
