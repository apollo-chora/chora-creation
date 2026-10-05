package atomic

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestLearningAtom_IsValid(t *testing.T) {
	tests := []struct {
		name     string
		atomType AtomType
		diff     int
		want     bool
	}{
		{"valid_multiple_choice", AtomTypeMultipleChoice, 3, true},
		{"valid_code", AtomTypeCode, 1, true},
		{"valid_difficulty_min", AtomTypeTrueFalse, 1, true},
		{"valid_difficulty_max", AtomTypeTrueFalse, 5, true},
		{"invalid_atom_type", AtomType("unknown"), 3, false},
		{"difficulty_too_low", AtomTypeMultipleChoice, 0, false},
		{"difficulty_too_high", AtomTypeMultipleChoice, 6, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &LearningAtom{AtomType: tt.atomType, Difficulty: tt.diff}
			assert.Equal(t, tt.want, a.IsValid())
		})
	}
}

func TestLearningAtom_IsDeleted(t *testing.T) {
	t.Run("not_deleted", func(t *testing.T) {
		a := &LearningAtom{}
		assert.False(t, a.IsDeleted())
	})
	t.Run("deleted", func(t *testing.T) {
		now := time.Now()
		a := &LearningAtom{DeletedAt: &now}
		assert.True(t, a.IsDeleted())
	})
}

func TestTopicNode_IsRoot(t *testing.T) {
	t.Run("root_node", func(t *testing.T) {
		n := &TopicNode{}
		assert.True(t, n.IsRoot())
	})
	t.Run("child_node", func(t *testing.T) {
		parentID := uuid.Must(uuid.NewV7())
		n := &TopicNode{ParentID: &parentID}
		assert.False(t, n.IsRoot())
	})
}

func TestTopicNode_IsDeleted(t *testing.T) {
	t.Run("not_deleted", func(t *testing.T) {
		n := &TopicNode{}
		assert.False(t, n.IsDeleted())
	})
	t.Run("deleted", func(t *testing.T) {
		now := time.Now()
		n := &TopicNode{DeletedAt: &now}
		assert.True(t, n.IsDeleted())
	})
}
