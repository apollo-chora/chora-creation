package cms

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestProjectService_CreateProject(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	t.Run("creates project and publishes event", func(t *testing.T) {
		t.Parallel()

		projectRepo := new(mockProjectRepo)
		publisher := new(mockEventPublisher)

		projectRepo.On("Save", mock.Anything, mock.AnythingOfType("*cms.Project")).Return(nil)

		var capturedEvent DomainEvent
		publisher.On("Publish", mock.Anything, TopicCMSEvents, mock.AnythingOfType("cms.DomainEvent")).
			Run(func(args mock.Arguments) {
				capturedEvent = args.Get(2).(DomainEvent)
			}).Return(nil)

		svc := NewProjectService(projectRepo, new(mockProjectMemberRepo), new(mockProjectDeliverableRepo), publisher)
		project, err := svc.CreateProject(context.Background(), tenantID, "Capstone", "A capstone project", ProjectTypeCapstone, nil, gcid)

		require.NoError(t, err)
		assert.Equal(t, "Capstone", project.Name)
		assert.Equal(t, ProjectTypeCapstone, project.ProjectType)
		assert.Equal(t, tenantID, project.TenantID)

		// Verify event payload matches AsyncAPI contract
		assert.Equal(t, EventProjectCreated, capturedEvent.EventType)
		assert.Equal(t, "Project", capturedEvent.AggregateType)
		assert.Equal(t, "Capstone", capturedEvent.Payload["name"])
		assert.Equal(t, "capstone", capturedEvent.Payload["project_type"])

		projectRepo.AssertExpectations(t)
		publisher.AssertExpectations(t)
	})

	t.Run("rejects empty name", func(t *testing.T) {
		t.Parallel()

		svc := NewProjectService(new(mockProjectRepo), new(mockProjectMemberRepo), new(mockProjectDeliverableRepo), new(mockEventPublisher))
		_, err := svc.CreateProject(context.Background(), tenantID, "", "desc", ProjectTypeIndividual, nil, gcid)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrValidationFailed)
	})

	t.Run("rejects invalid project type", func(t *testing.T) {
		t.Parallel()

		svc := NewProjectService(new(mockProjectRepo), new(mockProjectMemberRepo), new(mockProjectDeliverableRepo), new(mockEventPublisher))
		_, err := svc.CreateProject(context.Background(), tenantID, "Name", "desc", ProjectType("invalid"), nil, gcid)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrValidationFailed)
	})

	t.Run("rejects description exceeding 5000 chars", func(t *testing.T) {
		t.Parallel()

		svc := NewProjectService(new(mockProjectRepo), new(mockProjectMemberRepo), new(mockProjectDeliverableRepo), new(mockEventPublisher))
		longDesc := strings.Repeat("a", 5001)
		_, err := svc.CreateProject(context.Background(), tenantID, "Name", longDesc, ProjectTypeIndividual, nil, gcid)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrValidationFailed)
	})
}

func TestProjectService_GetProject(t *testing.T) {
	t.Parallel()

	projectID := uuid.Must(uuid.NewV7())

	t.Run("returns project when found", func(t *testing.T) {
		t.Parallel()

		project := &Project{ID: projectID, Name: "Found"}
		projectRepo := new(mockProjectRepo)
		projectRepo.On("FindByID", mock.Anything, projectID).Return(project, nil)

		svc := NewProjectService(projectRepo, new(mockProjectMemberRepo), new(mockProjectDeliverableRepo), new(mockEventPublisher))
		got, err := svc.GetProject(context.Background(), projectID)

		require.NoError(t, err)
		assert.Equal(t, "Found", got.Name)
	})

	t.Run("returns not found", func(t *testing.T) {
		t.Parallel()

		projectRepo := new(mockProjectRepo)
		projectRepo.On("FindByID", mock.Anything, projectID).Return(nil, nil)

		svc := NewProjectService(projectRepo, new(mockProjectMemberRepo), new(mockProjectDeliverableRepo), new(mockEventPublisher))
		_, err := svc.GetProject(context.Background(), projectID)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrProjectNotFound)
	})
}

func TestProjectService_UpdateProject(t *testing.T) {
	t.Parallel()

	projectID := uuid.Must(uuid.NewV7())

	t.Run("updates name and due date", func(t *testing.T) {
		t.Parallel()

		project := &Project{ID: projectID, Name: "Old"}
		projectRepo := new(mockProjectRepo)
		projectRepo.On("FindByID", mock.Anything, projectID).Return(project, nil)
		projectRepo.On("Save", mock.Anything, mock.AnythingOfType("*cms.Project")).Return(nil)

		svc := NewProjectService(projectRepo, new(mockProjectMemberRepo), new(mockProjectDeliverableRepo), new(mockEventPublisher))
		newName := "New Name"
		dueDate := time.Now().Add(24 * time.Hour)
		got, err := svc.UpdateProject(context.Background(), projectID, &newName, nil, &dueDate)

		require.NoError(t, err)
		assert.Equal(t, "New Name", got.Name)
		assert.Equal(t, &dueDate, got.DueDate)
	})

	t.Run("returns not found", func(t *testing.T) {
		t.Parallel()

		projectRepo := new(mockProjectRepo)
		projectRepo.On("FindByID", mock.Anything, projectID).Return(nil, nil)

		svc := NewProjectService(projectRepo, new(mockProjectMemberRepo), new(mockProjectDeliverableRepo), new(mockEventPublisher))
		_, err := svc.UpdateProject(context.Background(), projectID, nil, nil, nil)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrProjectNotFound)
	})

	t.Run("rejects empty name", func(t *testing.T) {
		t.Parallel()

		project := &Project{ID: projectID, Name: "Existing"}
		projectRepo := new(mockProjectRepo)
		projectRepo.On("FindByID", mock.Anything, projectID).Return(project, nil)

		svc := NewProjectService(projectRepo, new(mockProjectMemberRepo), new(mockProjectDeliverableRepo), new(mockEventPublisher))
		emptyName := ""
		_, err := svc.UpdateProject(context.Background(), projectID, &emptyName, nil, nil)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrValidationFailed)
	})
}

func TestProjectService_SoftDeleteProject(t *testing.T) {
	t.Parallel()

	projectID := uuid.Must(uuid.NewV7())

	t.Run("soft deletes project", func(t *testing.T) {
		t.Parallel()

		project := &Project{ID: projectID, Name: "ToDelete"}
		projectRepo := new(mockProjectRepo)
		projectRepo.On("FindByID", mock.Anything, projectID).Return(project, nil)
		projectRepo.On("SoftDelete", mock.Anything, projectID).Return(nil)

		svc := NewProjectService(projectRepo, new(mockProjectMemberRepo), new(mockProjectDeliverableRepo), new(mockEventPublisher))
		err := svc.SoftDeleteProject(context.Background(), projectID)

		require.NoError(t, err)
		projectRepo.AssertCalled(t, "SoftDelete", mock.Anything, projectID)
	})

	t.Run("returns not found", func(t *testing.T) {
		t.Parallel()

		projectRepo := new(mockProjectRepo)
		projectRepo.On("FindByID", mock.Anything, projectID).Return(nil, nil)

		svc := NewProjectService(projectRepo, new(mockProjectMemberRepo), new(mockProjectDeliverableRepo), new(mockEventPublisher))
		err := svc.SoftDeleteProject(context.Background(), projectID)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrProjectNotFound)
	})
}

func TestProjectService_ListProjects(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())

	t.Run("returns projects for tenant", func(t *testing.T) {
		t.Parallel()

		projects := []Project{{Name: "A"}, {Name: "B"}}
		projectRepo := new(mockProjectRepo)
		projectRepo.On("ListByTenant", mock.Anything, tenantID, (*uuid.UUID)(nil), 10).Return(projects, nil)

		svc := NewProjectService(projectRepo, new(mockProjectMemberRepo), new(mockProjectDeliverableRepo), new(mockEventPublisher))
		got, err := svc.ListProjects(context.Background(), tenantID, nil, 10)

		require.NoError(t, err)
		assert.Len(t, got, 2)
	})

	t.Run("returns error on repo failure", func(t *testing.T) {
		t.Parallel()

		projectRepo := new(mockProjectRepo)
		projectRepo.On("ListByTenant", mock.Anything, tenantID, (*uuid.UUID)(nil), 10).Return(nil, assert.AnError)

		svc := NewProjectService(projectRepo, new(mockProjectMemberRepo), new(mockProjectDeliverableRepo), new(mockEventPublisher))
		_, err := svc.ListProjects(context.Background(), tenantID, nil, 10)

		require.Error(t, err)
	})
}

func TestProjectService_AddMember(t *testing.T) {
	t.Parallel()

	projectID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	t.Run("adds member successfully", func(t *testing.T) {
		t.Parallel()

		projectRepo := new(mockProjectRepo)
		projectRepo.On("FindByID", mock.Anything, projectID).Return(&Project{ID: projectID}, nil)

		memberRepo := new(mockProjectMemberRepo)
		memberRepo.On("FindByProjectAndGCID", mock.Anything, projectID, gcid).Return(nil, nil)
		memberRepo.On("Save", mock.Anything, mock.AnythingOfType("*cms.ProjectMember")).Return(nil)

		svc := NewProjectService(projectRepo, memberRepo, new(mockProjectDeliverableRepo), new(mockEventPublisher))
		member, err := svc.AddMember(context.Background(), projectID, tenantID, gcid, MemberRoleContributor)

		require.NoError(t, err)
		assert.Equal(t, MemberRoleContributor, member.MemberRole)
		assert.Equal(t, projectID, member.ProjectID)
		assert.Equal(t, gcid, member.GCID)
	})

	t.Run("rejects invalid role", func(t *testing.T) {
		t.Parallel()

		svc := NewProjectService(new(mockProjectRepo), new(mockProjectMemberRepo), new(mockProjectDeliverableRepo), new(mockEventPublisher))
		_, err := svc.AddMember(context.Background(), projectID, tenantID, gcid, MemberRole("invalid"))

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrValidationFailed)
	})

	t.Run("rejects duplicate member", func(t *testing.T) {
		t.Parallel()

		projectRepo := new(mockProjectRepo)
		projectRepo.On("FindByID", mock.Anything, projectID).Return(&Project{ID: projectID}, nil)

		existing := &ProjectMember{ProjectID: projectID, GCID: gcid}
		memberRepo := new(mockProjectMemberRepo)
		memberRepo.On("FindByProjectAndGCID", mock.Anything, projectID, gcid).Return(existing, nil)

		svc := NewProjectService(projectRepo, memberRepo, new(mockProjectDeliverableRepo), new(mockEventPublisher))
		_, err := svc.AddMember(context.Background(), projectID, tenantID, gcid, MemberRoleContributor)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrProjectMemberDuplicate)
	})

	t.Run("rejects member for non-existent project", func(t *testing.T) {
		t.Parallel()

		projectRepo := new(mockProjectRepo)
		projectRepo.On("FindByID", mock.Anything, projectID).Return(nil, nil)

		svc := NewProjectService(projectRepo, new(mockProjectMemberRepo), new(mockProjectDeliverableRepo), new(mockEventPublisher))
		_, err := svc.AddMember(context.Background(), projectID, tenantID, gcid, MemberRoleContributor)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrProjectNotFound)
	})
}

func TestProjectService_RemoveMember(t *testing.T) {
	t.Parallel()

	memberID := uuid.Must(uuid.NewV7())

	t.Run("removes member via hard delete", func(t *testing.T) {
		t.Parallel()

		memberRepo := new(mockProjectMemberRepo)
		memberRepo.On("Delete", mock.Anything, memberID).Return(nil)

		svc := NewProjectService(new(mockProjectRepo), memberRepo, new(mockProjectDeliverableRepo), new(mockEventPublisher))
		err := svc.RemoveMember(context.Background(), memberID)

		require.NoError(t, err)
		memberRepo.AssertCalled(t, "Delete", mock.Anything, memberID)
	})
}

func TestProjectService_ListMembers(t *testing.T) {
	t.Parallel()

	projectID := uuid.Must(uuid.NewV7())

	t.Run("returns members for project", func(t *testing.T) {
		t.Parallel()

		members := []ProjectMember{{MemberRole: MemberRoleOwner}, {MemberRole: MemberRoleContributor}}
		memberRepo := new(mockProjectMemberRepo)
		memberRepo.On("ListByProject", mock.Anything, projectID).Return(members, nil)

		svc := NewProjectService(new(mockProjectRepo), memberRepo, new(mockProjectDeliverableRepo), new(mockEventPublisher))
		got, err := svc.ListMembers(context.Background(), projectID)

		require.NoError(t, err)
		assert.Len(t, got, 2)
	})

	t.Run("returns error on repo failure", func(t *testing.T) {
		t.Parallel()

		memberRepo := new(mockProjectMemberRepo)
		memberRepo.On("ListByProject", mock.Anything, projectID).Return(nil, assert.AnError)

		svc := NewProjectService(new(mockProjectRepo), memberRepo, new(mockProjectDeliverableRepo), new(mockEventPublisher))
		_, err := svc.ListMembers(context.Background(), projectID)

		require.Error(t, err)
	})
}

func TestProjectService_SubmitDeliverable(t *testing.T) {
	t.Parallel()

	projectID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	t.Run("creates deliverable and publishes event", func(t *testing.T) {
		t.Parallel()

		projectRepo := new(mockProjectRepo)
		projectRepo.On("FindByID", mock.Anything, projectID).Return(&Project{ID: projectID}, nil)

		deliverableRepo := new(mockProjectDeliverableRepo)
		publisher := new(mockEventPublisher)

		deliverableRepo.On("Save", mock.Anything, mock.AnythingOfType("*cms.ProjectDeliverable")).Return(nil)

		var capturedEvent DomainEvent
		publisher.On("Publish", mock.Anything, TopicCMSEvents, mock.AnythingOfType("cms.DomainEvent")).
			Run(func(args mock.Arguments) {
				capturedEvent = args.Get(2).(DomainEvent)
			}).Return(nil)

		svc := NewProjectService(projectRepo, new(mockProjectMemberRepo), deliverableRepo, publisher)
		content := map[string]any{"text": "My submission"}
		deliverable, err := svc.SubmitDeliverable(context.Background(), projectID, tenantID, "Final Report", content, nil, gcid)

		require.NoError(t, err)
		assert.Equal(t, "Final Report", deliverable.Title)
		assert.Equal(t, DeliverableStatusSubmitted, deliverable.Status)
		assert.NotNil(t, deliverable.SubmittedAt)

		// Verify event payload matches AsyncAPI contract
		assert.Equal(t, EventDeliverableSubmitted, capturedEvent.EventType)
		assert.Equal(t, "ProjectDeliverable", capturedEvent.AggregateType)
		assert.Equal(t, projectID.String(), capturedEvent.Payload["project_id"])
		assert.Equal(t, "Final Report", capturedEvent.Payload["title"])

		deliverableRepo.AssertExpectations(t)
		publisher.AssertExpectations(t)
	})

	t.Run("rejects empty title", func(t *testing.T) {
		t.Parallel()

		svc := NewProjectService(new(mockProjectRepo), new(mockProjectMemberRepo), new(mockProjectDeliverableRepo), new(mockEventPublisher))
		_, err := svc.SubmitDeliverable(context.Background(), projectID, tenantID, "", nil, nil, gcid)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrValidationFailed)
	})

	t.Run("rejects deliverable for non-existent project", func(t *testing.T) {
		t.Parallel()

		projectRepo := new(mockProjectRepo)
		projectRepo.On("FindByID", mock.Anything, projectID).Return(nil, nil)

		svc := NewProjectService(projectRepo, new(mockProjectMemberRepo), new(mockProjectDeliverableRepo), new(mockEventPublisher))
		_, err := svc.SubmitDeliverable(context.Background(), projectID, tenantID, "Title", nil, nil, gcid)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrProjectNotFound)
	})
}

func TestProjectService_GradeDeliverable(t *testing.T) {
	t.Parallel()

	deliverableID := uuid.Must(uuid.NewV7())
	projectID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	t.Run("grades deliverable and publishes event", func(t *testing.T) {
		t.Parallel()

		deliverable := &ProjectDeliverable{
			ID:        deliverableID,
			ProjectID: projectID,
			TenantID:  tenantID,
			Title:     "Final Report",
			Status:    DeliverableStatusSubmitted,
		}
		deliverableRepo := new(mockProjectDeliverableRepo)
		publisher := new(mockEventPublisher)

		deliverableRepo.On("FindByID", mock.Anything, deliverableID).Return(deliverable, nil)
		deliverableRepo.On("Save", mock.Anything, mock.AnythingOfType("*cms.ProjectDeliverable")).Return(nil)

		var capturedEvent DomainEvent
		publisher.On("Publish", mock.Anything, TopicCMSEvents, mock.AnythingOfType("cms.DomainEvent")).
			Run(func(args mock.Arguments) {
				capturedEvent = args.Get(2).(DomainEvent)
			}).Return(nil)

		svc := NewProjectService(new(mockProjectRepo), new(mockProjectMemberRepo), deliverableRepo, publisher)
		score := 85.0
		feedback := "Well done"
		got, err := svc.GradeDeliverable(context.Background(), deliverableID, &score, &feedback, nil, gcid)

		require.NoError(t, err)
		assert.Equal(t, DeliverableStatusGraded, got.Status)
		assert.Equal(t, &score, got.Score)
		assert.Equal(t, &feedback, got.Feedback)
		assert.NotNil(t, got.GradedAt)
		assert.Equal(t, &gcid, got.GradedByGCID)

		// Verify event payload matches AsyncAPI contract
		assert.Equal(t, EventDeliverableGraded, capturedEvent.EventType)
		assert.Equal(t, "ProjectDeliverable", capturedEvent.AggregateType)
		assert.Equal(t, projectID.String(), capturedEvent.Payload["project_id"])
		assert.Equal(t, "Final Report", capturedEvent.Payload["title"])

		deliverableRepo.AssertExpectations(t)
		publisher.AssertExpectations(t)
	})

	t.Run("returns not found", func(t *testing.T) {
		t.Parallel()

		deliverableRepo := new(mockProjectDeliverableRepo)
		deliverableRepo.On("FindByID", mock.Anything, deliverableID).Return(nil, nil)

		svc := NewProjectService(new(mockProjectRepo), new(mockProjectMemberRepo), deliverableRepo, new(mockEventPublisher))
		_, err := svc.GradeDeliverable(context.Background(), deliverableID, nil, nil, nil, gcid)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrDeliverableNotFound)
	})

	t.Run("rejects already graded deliverable", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		deliverable := &ProjectDeliverable{
			ID:       deliverableID,
			Status:   DeliverableStatusGraded,
			GradedAt: &now,
		}
		deliverableRepo := new(mockProjectDeliverableRepo)
		deliverableRepo.On("FindByID", mock.Anything, deliverableID).Return(deliverable, nil)

		svc := NewProjectService(new(mockProjectRepo), new(mockProjectMemberRepo), deliverableRepo, new(mockEventPublisher))
		_, err := svc.GradeDeliverable(context.Background(), deliverableID, nil, nil, nil, gcid)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrDeliverableAlreadyGraded)
	})

	t.Run("rejects score below 0", func(t *testing.T) {
		t.Parallel()

		deliverable := &ProjectDeliverable{
			ID:        deliverableID,
			ProjectID: projectID,
			TenantID:  tenantID,
			Title:     "Report",
			Status:    DeliverableStatusSubmitted,
		}
		deliverableRepo := new(mockProjectDeliverableRepo)
		deliverableRepo.On("FindByID", mock.Anything, deliverableID).Return(deliverable, nil)

		svc := NewProjectService(new(mockProjectRepo), new(mockProjectMemberRepo), deliverableRepo, new(mockEventPublisher))
		score := -1.0
		_, err := svc.GradeDeliverable(context.Background(), deliverableID, &score, nil, nil, gcid)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrValidationFailed)
	})

	t.Run("rejects score above 100", func(t *testing.T) {
		t.Parallel()

		deliverable := &ProjectDeliverable{
			ID:        deliverableID,
			ProjectID: projectID,
			TenantID:  tenantID,
			Title:     "Report",
			Status:    DeliverableStatusSubmitted,
		}
		deliverableRepo := new(mockProjectDeliverableRepo)
		deliverableRepo.On("FindByID", mock.Anything, deliverableID).Return(deliverable, nil)

		svc := NewProjectService(new(mockProjectRepo), new(mockProjectMemberRepo), deliverableRepo, new(mockEventPublisher))
		score := 101.0
		_, err := svc.GradeDeliverable(context.Background(), deliverableID, &score, nil, nil, gcid)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrValidationFailed)
	})

	t.Run("rejects feedback exceeding 10000 chars", func(t *testing.T) {
		t.Parallel()

		deliverable := &ProjectDeliverable{
			ID:        deliverableID,
			ProjectID: projectID,
			TenantID:  tenantID,
			Title:     "Report",
			Status:    DeliverableStatusSubmitted,
		}
		deliverableRepo := new(mockProjectDeliverableRepo)
		deliverableRepo.On("FindByID", mock.Anything, deliverableID).Return(deliverable, nil)

		svc := NewProjectService(new(mockProjectRepo), new(mockProjectMemberRepo), deliverableRepo, new(mockEventPublisher))
		longFeedback := strings.Repeat("a", 10001)
		_, err := svc.GradeDeliverable(context.Background(), deliverableID, nil, &longFeedback, nil, gcid)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrValidationFailed)
	})
}

func TestProjectService_GetDeliverable(t *testing.T) {
	t.Parallel()

	deliverableID := uuid.Must(uuid.NewV7())

	t.Run("returns deliverable when found", func(t *testing.T) {
		t.Parallel()

		deliverable := &ProjectDeliverable{ID: deliverableID, Title: "Report"}
		deliverableRepo := new(mockProjectDeliverableRepo)
		deliverableRepo.On("FindByID", mock.Anything, deliverableID).Return(deliverable, nil)

		svc := NewProjectService(new(mockProjectRepo), new(mockProjectMemberRepo), deliverableRepo, new(mockEventPublisher))
		got, err := svc.GetDeliverable(context.Background(), deliverableID)

		require.NoError(t, err)
		assert.Equal(t, "Report", got.Title)
	})

	t.Run("returns not found", func(t *testing.T) {
		t.Parallel()

		deliverableRepo := new(mockProjectDeliverableRepo)
		deliverableRepo.On("FindByID", mock.Anything, deliverableID).Return(nil, nil)

		svc := NewProjectService(new(mockProjectRepo), new(mockProjectMemberRepo), deliverableRepo, new(mockEventPublisher))
		_, err := svc.GetDeliverable(context.Background(), deliverableID)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrDeliverableNotFound)
	})
}

func TestProjectService_ListDeliverables(t *testing.T) {
	t.Parallel()

	projectID := uuid.Must(uuid.NewV7())

	t.Run("returns deliverables for project", func(t *testing.T) {
		t.Parallel()

		deliverables := []ProjectDeliverable{{Title: "A"}, {Title: "B"}}
		deliverableRepo := new(mockProjectDeliverableRepo)
		deliverableRepo.On("ListByProject", mock.Anything, projectID).Return(deliverables, nil)

		svc := NewProjectService(new(mockProjectRepo), new(mockProjectMemberRepo), deliverableRepo, new(mockEventPublisher))
		got, err := svc.ListDeliverables(context.Background(), projectID)

		require.NoError(t, err)
		assert.Len(t, got, 2)
	})

	t.Run("returns error on repo failure", func(t *testing.T) {
		t.Parallel()

		deliverableRepo := new(mockProjectDeliverableRepo)
		deliverableRepo.On("ListByProject", mock.Anything, projectID).Return(nil, assert.AnError)

		svc := NewProjectService(new(mockProjectRepo), new(mockProjectMemberRepo), deliverableRepo, new(mockEventPublisher))
		_, err := svc.ListDeliverables(context.Background(), projectID)

		require.Error(t, err)
	})
}
