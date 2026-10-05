// Tests for peer-review event payload builders.
package peer_review_test

import (
	"strings"
	"testing"

	pr "github.com/apollo-chora/chora-creation/internal/domain/expert_tools/peer_review"
)

func TestNewSubmittedPayload_PopulatesFields(t *testing.T) {
	t.Parallel()
	r, _ := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA,
		AuthoredBy: authorA, QuorumSize: 2,
	})
	p := pr.NewSubmittedPayload(r)
	if p.ReviewID != r.ReviewID || p.TenantID != tenantA || p.AtomID != atomA {
		t.Errorf("payload mismatch: %+v", p)
	}
	if !strings.HasPrefix(p.OccurredAt, "20") {
		t.Errorf("occurred_at not RFC3339: %q", p.OccurredAt)
	}
}

func TestNewVoteRecordedPayload_PopulatesFields(t *testing.T) {
	t.Parallel()
	r, _ := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA,
		AuthoredBy: authorA, QuorumSize: 2,
	})
	_ = r.RecordVote(pr.VoteParams{ReviewerGcid: revwrA, Decision: pr.DecisionApprove})
	p := pr.NewVoteRecordedPayload(r, r.Votes[0])
	if p.VoteID != r.Votes[0].VoteID || p.Decision != string(pr.DecisionApprove) {
		t.Errorf("payload mismatch: %+v", p)
	}
}

func TestNewQuorumReachedPayload_PopulatesFields(t *testing.T) {
	t.Parallel()
	r, _ := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA,
		AuthoredBy: authorA, QuorumSize: 2,
	})
	_ = r.RecordVote(pr.VoteParams{ReviewerGcid: revwrA, Decision: pr.DecisionApprove})
	_ = r.RecordVote(pr.VoteParams{ReviewerGcid: revwrB, Decision: pr.DecisionApprove})
	p := pr.NewQuorumReachedPayload(r)
	if p.FinalStatus != string(pr.StatusApproved) {
		t.Errorf("final_status = %q; want approved", p.FinalStatus)
	}
	if p.ApproveCnt != 2 {
		t.Errorf("approve_count = %d; want 2", p.ApproveCnt)
	}
}

func TestIsActive_TrueWhilePending(t *testing.T) {
	t.Parallel()
	r, _ := pr.Submit(pr.SubmitParams{
		TenantID: tenantA, AtomID: atomA, RevisionID: revisionA,
		AuthoredBy: authorA, QuorumSize: 2,
	})
	if !r.IsActive() {
		t.Errorf("IsActive() = false on pending; want true")
	}
}
