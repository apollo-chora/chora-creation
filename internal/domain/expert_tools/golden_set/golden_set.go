// Package golden_set models the curated reference set ("golden anchors")
// instructors use to benchmark new atom quality.
//
// Each anchor is a snapshot of a high-quality atom: title, body, tags, and
// a curator-assigned QualityScore in [0, 1]. New atoms are compared against
// an anchor via Compare which produces a similarity score in (0, 1] and a
// verdict (above_bar | near_bar | below_bar) the publish pipeline can
// surface in the O+ governance dashboard + R+ instructor review queue.
//
// Hexagonal: this package is dependency-free w.r.t. infrastructure.
// Cross-domain references (atom_id, revision_id) are UUIDs without FK per
// .claude/rules/ddd-enforcement.md aggregate invariant #3.
package golden_set

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// -----------------------------------------------------------------------------
// Verdict thresholds (curator-tunable; defaults align with platform-MVP)
// -----------------------------------------------------------------------------

const (
	thresholdAboveBar = 0.85
	thresholdNearBar  = 0.55
)

// Verdict is the qualitative bucket the similarity score falls into.
type Verdict string

const (
	VerdictAboveBar Verdict = "above_bar"
	VerdictNearBar  Verdict = "near_bar"
	VerdictBelowBar Verdict = "below_bar"
)

// classifyVerdict maps similarity score to Verdict.
func classifyVerdict(score float64) Verdict {
	switch {
	case score >= thresholdAboveBar:
		return VerdictAboveBar
	case score >= thresholdNearBar:
		return VerdictNearBar
	default:
		return VerdictBelowBar
	}
}

// -----------------------------------------------------------------------------
// Anchor aggregate
// -----------------------------------------------------------------------------

// Anchor is a curated reference atom acting as a quality benchmark.
//
// Aggregate root. Soft-delete only per ddd-enforcement #5; hard delete is
// reserved for crypto-shred. AtomID is a cross-domain reference (chora_creation
// LearningAtom) — no FK constraint per ddd-enforcement #3.
type Anchor struct {
	AnchorID     string     `json:"anchor_id"`
	TenantID     string     `json:"tenant_id"`
	CreatedBy    string     `json:"created_by_gcid"`
	AtomID       string     `json:"atom_id"`
	Title        string     `json:"title"`
	Body         string     `json:"body"`
	Tags         []string   `json:"tags"`
	QualityScore float64    `json:"quality_score"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	DeletedAt    *time.Time `json:"deleted_at,omitempty"`
}

// NewAnchorParams is the input to NewAnchor.
type NewAnchorParams struct {
	TenantID     string
	CreatedBy    string
	AtomID       string
	Title        string
	Body         string
	Tags         []string
	QualityScore float64
}

// NewAnchor constructs a fresh Anchor in active state.
func NewAnchor(p NewAnchorParams) (*Anchor, error) {
	if strings.TrimSpace(p.TenantID) == "" {
		return nil, errors.New("tenant_id is required")
	}
	if strings.TrimSpace(p.CreatedBy) == "" {
		return nil, errors.New("created_by is required")
	}
	if strings.TrimSpace(p.AtomID) == "" {
		return nil, errors.New("atom_id is required")
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		return nil, errors.New("title is required")
	}
	body := strings.TrimSpace(p.Body)
	if body == "" {
		return nil, errors.New("body is required")
	}
	if p.QualityScore < 0 || p.QualityScore > 1 {
		return nil, fmt.Errorf("quality_score out of range [0,1]: %f", p.QualityScore)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuidv7: %w", err)
	}
	tags := append([]string(nil), p.Tags...)
	now := time.Now().UTC()
	return &Anchor{
		AnchorID:     id.String(),
		TenantID:     p.TenantID,
		CreatedBy:    p.CreatedBy,
		AtomID:       p.AtomID,
		Title:        title,
		Body:         body,
		Tags:         tags,
		QualityScore: p.QualityScore,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// SoftDelete marks the anchor as removed; idempotent.
func (a *Anchor) SoftDelete() {
	if a.DeletedAt != nil {
		return
	}
	now := time.Now().UTC()
	a.DeletedAt = &now
	a.UpdatedAt = now
}

// IsActive reports whether the anchor is still listed in the curator set.
func (a *Anchor) IsActive() bool { return a.DeletedAt == nil }

// -----------------------------------------------------------------------------
// Compare — similarity scorer
// -----------------------------------------------------------------------------

// Comparison is the result of comparing a candidate atom revision against
// a single Anchor.
type Comparison struct {
	ComparedAtomID     string    `json:"compared_atom_id"`
	ComparedRevisionID string    `json:"compared_revision_id"`
	AnchorID           string    `json:"anchor_id"`
	SimilarityScore    float64   `json:"similarity_score"` // (0, 1]
	BodySimilarity     float64   `json:"body_similarity"`
	TagSimilarity      float64   `json:"tag_similarity"`
	Verdict            Verdict   `json:"verdict"`
	OccurredAt         time.Time `json:"occurred_at"`
}

// CompareParams is the input to Compare.
type CompareParams struct {
	TenantID           string
	ComparedAtomID     string
	ComparedRevisionID string
	ComparedBody       string
	ComparedTags       []string
	Anchor             *Anchor
}

// Compare scores a candidate atom revision against an Anchor. Returns a
// Comparison with similarity, body / tag breakdown, and verdict bucket.
//
// Algorithm (intentionally simple + deterministic — true semantic similarity
// is deferred to Vertex Embedding via the AI Kernel; this is the reference
// scoring used by tests + the M11 in-memory adapter):
//   - Body similarity: token-overlap Jaccard on lower-cased whitespace tokens.
//   - Tag similarity:  Jaccard on tag sets.
//   - Total similarity: 0.7 * body + 0.3 * tag.
func Compare(p CompareParams) (Comparison, error) {
	if p.Anchor == nil {
		return Comparison{}, errors.New("anchor is required")
	}
	if strings.TrimSpace(p.TenantID) == "" {
		return Comparison{}, errors.New("tenant_id is required")
	}
	if p.TenantID != p.Anchor.TenantID {
		return Comparison{}, fmt.Errorf("anchor belongs to tenant %s; refusing cross-tenant compare", p.Anchor.TenantID)
	}
	if strings.TrimSpace(p.ComparedAtomID) == "" {
		return Comparison{}, errors.New("compared_atom_id is required")
	}
	if strings.TrimSpace(p.ComparedRevisionID) == "" {
		return Comparison{}, errors.New("compared_revision_id is required")
	}
	body := strings.TrimSpace(p.ComparedBody)
	if body == "" {
		return Comparison{}, errors.New("compared_body is required")
	}

	bodySim := jaccardTokens(body, p.Anchor.Body)
	tagSim := jaccardSets(p.ComparedTags, p.Anchor.Tags)
	total := 0.7*bodySim + 0.3*tagSim
	if total <= 0 {
		// Floor at a tiny positive value so callers can distinguish "compared,
		// no overlap" from "compare not run". Clamp at 0.0001.
		total = 0.0001
	}

	return Comparison{
		ComparedAtomID:     p.ComparedAtomID,
		ComparedRevisionID: p.ComparedRevisionID,
		AnchorID:           p.Anchor.AnchorID,
		SimilarityScore:    total,
		BodySimilarity:     bodySim,
		TagSimilarity:      tagSim,
		Verdict:            classifyVerdict(total),
		OccurredAt:         time.Now().UTC(),
	}, nil
}

// jaccardTokens returns Jaccard similarity over lower-cased whitespace tokens.
func jaccardTokens(a, b string) float64 {
	at := tokenSet(a)
	bt := tokenSet(b)
	return jaccardMaps(at, bt)
}

// jaccardSets returns Jaccard similarity over two string slices, treating
// them as multisets (deduplicated, lower-cased, trimmed).
func jaccardSets(a, b []string) float64 {
	at := make(map[string]struct{}, len(a))
	for _, x := range a {
		x = strings.ToLower(strings.TrimSpace(x))
		if x == "" {
			continue
		}
		at[x] = struct{}{}
	}
	bt := make(map[string]struct{}, len(b))
	for _, x := range b {
		x = strings.ToLower(strings.TrimSpace(x))
		if x == "" {
			continue
		}
		bt[x] = struct{}{}
	}
	return jaccardMaps(at, bt)
}

// tokenSet lower-cases s, splits on whitespace, and returns a set of tokens
// with surrounding punctuation stripped.
func tokenSet(s string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, t := range strings.Fields(strings.ToLower(s)) {
		t = strings.Trim(t, ".,;:!?\"'()[]{}-")
		if t == "" {
			continue
		}
		out[t] = struct{}{}
	}
	return out
}

// jaccardMaps computes |A ∩ B| / |A ∪ B|. Returns 0 for two empty inputs.
func jaccardMaps(a, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	un := len(a) + len(b) - inter
	if un == 0 {
		return 0
	}
	return float64(inter) / float64(un)
}
