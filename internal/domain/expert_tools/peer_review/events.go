// Domain-level event payload builders for the peer-review pub-sub topics:
//
//	chora.creation.peer_review.submitted.v1
//	chora.creation.peer_review.vote_recorded.v1
//	chora.creation.peer_review.quorum_reached.v1
//
// Functions take an aggregate + W3C trace context and return a populated
// payload struct ready for the EventEnvelope adapter wrap. They are
// dependency-free (no proto import; the adapter does proto-encoding).
package peer_review

import "time"

const (
	// EnvSourceProject is the GCP project the chora-creation service runs in.
	EnvSourceProject = "chora-content"
	// EnvSourceService is this service's logical name.
	EnvSourceService = "chora-creation"
	// EnvSchemaVersion is the v1 schema version of these payloads.
	EnvSchemaVersion = int32(1)
)

// EventType is the canonical Pub/Sub topic suffix.
type EventType string

const (
	EventTypeSubmitted     EventType = "chora.creation.peer_review.submitted.v1"
	EventTypeVoteRecorded  EventType = "chora.creation.peer_review.vote_recorded.v1"
	EventTypeQuorumReached EventType = "chora.creation.peer_review.quorum_reached.v1"
)

// SubmittedPayload is the chora.creation.peer_review.submitted.v1 body.
type SubmittedPayload struct {
	ReviewID   string `json:"review_id"`
	TenantID   string `json:"tenant_id"`
	AtomID     string `json:"atom_id"`
	RevisionID string `json:"revision_id"`
	AuthoredBy string `json:"authored_by"`
	QuorumSize int    `json:"quorum_size"`
	OccurredAt string `json:"occurred_at"`
}

// VoteRecordedPayload is the chora.creation.peer_review.vote_recorded.v1 body.
type VoteRecordedPayload struct {
	ReviewID     string `json:"review_id"`
	TenantID     string `json:"tenant_id"`
	AtomID       string `json:"atom_id"`
	VoteID       string `json:"vote_id"`
	ReviewerGcid string `json:"reviewer_gcid"`
	Decision     string `json:"decision"`
	OccurredAt   string `json:"occurred_at"`
}

// QuorumReachedPayload is the chora.creation.peer_review.quorum_reached.v1 body.
// Emitted only when Status transitions off Pending (approved/rejected).
type QuorumReachedPayload struct {
	ReviewID    string `json:"review_id"`
	TenantID    string `json:"tenant_id"`
	AtomID      string `json:"atom_id"`
	RevisionID  string `json:"revision_id"`
	FinalStatus string `json:"final_status"`
	ApproveCnt  int    `json:"approve_count"`
	RejectCnt   int    `json:"reject_count"`
	OccurredAt  string `json:"occurred_at"`
}

// NewSubmittedPayload builds a SubmittedPayload.
func NewSubmittedPayload(r *ReviewSubmission) SubmittedPayload {
	return SubmittedPayload{
		ReviewID:   r.ReviewID,
		TenantID:   r.TenantID,
		AtomID:     r.AtomID,
		RevisionID: r.RevisionID,
		AuthoredBy: r.AuthoredBy,
		QuorumSize: r.QuorumSize,
		OccurredAt: r.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// NewVoteRecordedPayload builds a VoteRecordedPayload from the most-recent
// vote. The caller must ensure the vote is non-nil and belongs to r.
func NewVoteRecordedPayload(r *ReviewSubmission, v *Vote) VoteRecordedPayload {
	return VoteRecordedPayload{
		ReviewID:     r.ReviewID,
		TenantID:     r.TenantID,
		AtomID:       r.AtomID,
		VoteID:       v.VoteID,
		ReviewerGcid: v.ReviewerGcid,
		Decision:     string(v.Decision),
		OccurredAt:   v.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// NewQuorumReachedPayload builds a QuorumReachedPayload. Caller should only
// invoke when r.Status is approved or rejected.
func NewQuorumReachedPayload(r *ReviewSubmission) QuorumReachedPayload {
	t := r.Tally()
	occ := r.UpdatedAt
	if r.ResolvedAt != nil {
		occ = *r.ResolvedAt
	}
	return QuorumReachedPayload{
		ReviewID:    r.ReviewID,
		TenantID:    r.TenantID,
		AtomID:      r.AtomID,
		RevisionID:  r.RevisionID,
		FinalStatus: string(r.Status),
		ApproveCnt:  t.Approve,
		RejectCnt:   t.Reject,
		OccurredAt:  occ.UTC().Format(time.RFC3339Nano),
	}
}
