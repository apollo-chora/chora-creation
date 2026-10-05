// Domain-level event payload builders for the golden-set pub-sub topics:
//
//	chora.creation.golden_set.anchor_created.v1
//	chora.creation.golden_set.compared.v1
//
// Functions return populated payload structs (no proto import); the adapter
// wraps them in the chora.common.v1.EventEnvelope.
package golden_set

import "time"

const (
	EnvSourceProject = "chora-content"
	EnvSourceService = "chora-creation"
	EnvSchemaVersion = int32(1)
)

// EventType is the canonical Pub/Sub topic suffix.
type EventType string

const (
	EventTypeAnchorCreated EventType = "chora.creation.golden_set.anchor_created.v1"
	EventTypeCompared      EventType = "chora.creation.golden_set.compared.v1"
)

// AnchorCreatedPayload is the chora.creation.golden_set.anchor_created.v1 body.
type AnchorCreatedPayload struct {
	AnchorID     string  `json:"anchor_id"`
	TenantID     string  `json:"tenant_id"`
	AtomID       string  `json:"atom_id"`
	CreatedBy    string  `json:"created_by_gcid"`
	QualityScore float64 `json:"quality_score"`
	OccurredAt   string  `json:"occurred_at"`
}

// ComparedPayload is the chora.creation.golden_set.compared.v1 body.
type ComparedPayload struct {
	AnchorID           string  `json:"anchor_id"`
	TenantID           string  `json:"tenant_id"`
	ComparedAtomID     string  `json:"compared_atom_id"`
	ComparedRevisionID string  `json:"compared_revision_id"`
	SimilarityScore    float64 `json:"similarity_score"`
	BodySimilarity     float64 `json:"body_similarity"`
	TagSimilarity      float64 `json:"tag_similarity"`
	Verdict            string  `json:"verdict"`
	OccurredAt         string  `json:"occurred_at"`
}

// NewAnchorCreatedPayload builds an AnchorCreatedPayload.
func NewAnchorCreatedPayload(a *Anchor) AnchorCreatedPayload {
	return AnchorCreatedPayload{
		AnchorID:     a.AnchorID,
		TenantID:     a.TenantID,
		AtomID:       a.AtomID,
		CreatedBy:    a.CreatedBy,
		QualityScore: a.QualityScore,
		OccurredAt:   a.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// NewComparedPayload builds a ComparedPayload from a Comparison.
func NewComparedPayload(tenantID string, c Comparison) ComparedPayload {
	return ComparedPayload{
		AnchorID:           c.AnchorID,
		TenantID:           tenantID,
		ComparedAtomID:     c.ComparedAtomID,
		ComparedRevisionID: c.ComparedRevisionID,
		SimilarityScore:    c.SimilarityScore,
		BodySimilarity:     c.BodySimilarity,
		TagSimilarity:      c.TagSimilarity,
		Verdict:            string(c.Verdict),
		OccurredAt:         c.OccurredAt.UTC().Format(time.RFC3339Nano),
	}
}
