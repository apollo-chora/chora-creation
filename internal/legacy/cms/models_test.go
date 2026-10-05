package cms

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidContentType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ct   ContentType
		want bool
	}{
		{"rich_text", ContentTypeRichText, true},
		{"media", ContentTypeMedia, true},
		{"code", ContentTypeCode, true},
		{"interactive", ContentTypeInteractive, true},
		{"invalid", ContentType("unknown"), false},
		{"empty", ContentType(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ValidContentType(tt.ct))
		})
	}
}

func TestValidVisibilityStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		vs   VisibilityStatus
		want bool
	}{
		{"draft", VisibilityStatusDraft, true},
		{"in_review", VisibilityStatusInReview, true},
		{"published", VisibilityStatusPublished, true},
		{"archived", VisibilityStatusArchived, true},
		{"withdrawn", VisibilityStatusWithdrawn, true},
		{"invalid", VisibilityStatus("unknown"), false},
		{"empty", VisibilityStatus(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ValidVisibilityStatus(tt.vs))
		})
	}
}

func TestValidWorkflowStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ws   WorkflowStatus
		want bool
	}{
		{"draft", WorkflowStatusDraft, true},
		{"submitted", WorkflowStatusSubmitted, true},
		{"ai_review", WorkflowStatusAIReview, true},
		{"peer_review", WorkflowStatusPeerReview, true},
		{"approved", WorkflowStatusApproved, true},
		{"rejected", WorkflowStatusRejected, true},
		{"published", WorkflowStatusPublished, true},
		{"invalid", WorkflowStatus("unknown"), false},
		{"empty", WorkflowStatus(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ValidWorkflowStatus(tt.ws))
		})
	}
}

func TestValidProjectType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pt   ProjectType
		want bool
	}{
		{"individual", ProjectTypeIndividual, true},
		{"group", ProjectTypeGroup, true},
		{"capstone", ProjectTypeCapstone, true},
		{"invalid", ProjectType("unknown"), false},
		{"empty", ProjectType(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ValidProjectType(tt.pt))
		})
	}
}

func TestValidMemberRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mr   MemberRole
		want bool
	}{
		{"owner", MemberRoleOwner, true},
		{"contributor", MemberRoleContributor, true},
		{"reviewer", MemberRoleReviewer, true},
		{"invalid", MemberRole("unknown"), false},
		{"empty", MemberRole(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ValidMemberRole(tt.mr))
		})
	}
}

func TestValidDeliverableStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ds   DeliverableStatus
		want bool
	}{
		{"draft", DeliverableStatusDraft, true},
		{"submitted", DeliverableStatusSubmitted, true},
		{"graded", DeliverableStatusGraded, true},
		{"invalid", DeliverableStatus("unknown"), false},
		{"empty", DeliverableStatus(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ValidDeliverableStatus(tt.ds))
		})
	}
}

func TestValidWorkflowTransition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		from WorkflowStatus
		to   WorkflowStatus
		want bool
	}{
		// Valid transitions
		{"draft_to_submitted", WorkflowStatusDraft, WorkflowStatusSubmitted, true},
		{"submitted_to_ai_review", WorkflowStatusSubmitted, WorkflowStatusAIReview, true},
		{"ai_review_to_peer_review", WorkflowStatusAIReview, WorkflowStatusPeerReview, true},
		{"ai_review_to_rejected", WorkflowStatusAIReview, WorkflowStatusRejected, true},
		{"peer_review_to_approved", WorkflowStatusPeerReview, WorkflowStatusApproved, true},
		{"peer_review_to_rejected", WorkflowStatusPeerReview, WorkflowStatusRejected, true},
		{"approved_to_published", WorkflowStatusApproved, WorkflowStatusPublished, true},
		{"rejected_to_draft", WorkflowStatusRejected, WorkflowStatusDraft, true},
		// Invalid transitions
		{"draft_to_published", WorkflowStatusDraft, WorkflowStatusPublished, false},
		{"draft_to_approved", WorkflowStatusDraft, WorkflowStatusApproved, false},
		{"submitted_to_approved", WorkflowStatusSubmitted, WorkflowStatusApproved, false},
		{"submitted_to_published", WorkflowStatusSubmitted, WorkflowStatusPublished, false},
		{"approved_to_draft", WorkflowStatusApproved, WorkflowStatusDraft, false},
		{"published_to_draft", WorkflowStatusPublished, WorkflowStatusDraft, false},
		{"published_to_submitted", WorkflowStatusPublished, WorkflowStatusSubmitted, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ValidWorkflowTransition(tt.from, tt.to))
		})
	}
}
