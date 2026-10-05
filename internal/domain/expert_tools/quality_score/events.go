// Domain-level event payload builder for the quality-score topic:
//
//	chora.creation.quality_score.computed.v1
//
// Returns a populated payload struct (no proto import); the adapter wraps
// it in the chora.common.v1.EventEnvelope.
package quality_score

import "time"

const (
	EnvSourceProject = "chora-content"
	EnvSourceService = "chora-creation"
	EnvSchemaVersion = int32(1)
)

// EventType is the canonical Pub/Sub topic suffix.
type EventType string

const (
	EventTypeComputed EventType = "chora.creation.quality_score.computed.v1"
)

// ComputedPayload is the chora.creation.quality_score.computed.v1 body.
type ComputedPayload struct {
	ScoreID              string  `json:"score_id"`
	TenantID             string  `json:"tenant_id"`
	AtomID               string  `json:"atom_id"`
	RevisionID           string  `json:"revision_id"`
	ComputedBy           string  `json:"computed_by_gcid"`
	Clarity              float64 `json:"clarity"`
	PedagogicalSoundness float64 `json:"pedagogical_soundness"`
	Fairness             float64 `json:"fairness"`
	Accessibility        float64 `json:"accessibility"`
	AggregateScore       float64 `json:"aggregate_score"`
	Verdict              string  `json:"verdict"`
	OccurredAt           string  `json:"occurred_at"`
}

// NewComputedPayload builds a ComputedPayload.
func NewComputedPayload(q *QualityScore) ComputedPayload {
	return ComputedPayload{
		ScoreID:              q.ScoreID,
		TenantID:             q.TenantID,
		AtomID:               q.AtomID,
		RevisionID:           q.RevisionID,
		ComputedBy:           q.ComputedBy,
		Clarity:              q.Clarity,
		PedagogicalSoundness: q.PedagogicalSoundness,
		Fairness:             q.Fairness,
		Accessibility:        q.Accessibility,
		AggregateScore:       q.AggregateScore,
		Verdict:              string(q.Verdict),
		OccurredAt:           q.ComputedAt.UTC().Format(time.RFC3339Nano),
	}
}
