// backfill.go — the seed-from-atom-tags backfill (CHO-2275 Sub-phase A, hybrid
// data source part 1). Creates one flat ROOT TopicNode per distinct
// learning_atoms.tags slug, per tenant. Idempotent: a re-run creates nothing new
// because every name already resolves to an existing root.
//
// Pure domain logic over two narrow ports (AtomTagSource + SeedSink) so the
// diff-and-create rule is unit-tested with fakes; the pg adapter supplies the
// real reads/writes (learning_atoms.tags for the source; topic_nodes for the
// sink), each inside its own SET LOCAL chora.tenant_id transaction.
package topic

import (
	"context"
	"fmt"
	"strings"
)

// AtomTagSource reads the distinct topic-tag slugs a tenant's atoms carry
// (learning_atoms.tags JSONB — written by the CHO-2142 topic classifier).
type AtomTagSource interface {
	DistinctAtomTags(ctx context.Context, tenantID string) ([]string, error)
}

// SeedSink is the write side: what root names already exist (to skip), and how
// to create a new node.
type SeedSink interface {
	// ExistingRootNames returns the lowercased names of the tenant's active ROOT
	// topic nodes, so the backfill never re-creates one.
	ExistingRootNames(ctx context.Context, tenantID string) (map[string]struct{}, error)
	// Create persists a new node.
	Create(ctx context.Context, n *TopicNode) error
}

// SeedReport is the operator-facing outcome of one backfill run.
type SeedReport struct {
	TenantID     string   `json:"tenant_id"`
	Scanned      int      `json:"scanned"` // distinct non-blank tags considered
	Created      int      `json:"created"`
	Skipped      int      `json:"skipped"` // tags whose name already existed
	CreatedNames []string `json:"created_names"`
}

// SeedBackfill seeds root topic nodes from atom tags.
type SeedBackfill struct {
	src  AtomTagSource
	sink SeedSink
}

// NewSeedBackfill wires the backfill service.
func NewSeedBackfill(src AtomTagSource, sink SeedSink) *SeedBackfill {
	return &SeedBackfill{src: src, sink: sink}
}

// Seed runs the backfill for one tenant and returns the report. Idempotent.
func (s *SeedBackfill) Seed(ctx context.Context, tenantID string) (SeedReport, error) {
	if strings.TrimSpace(tenantID) == "" {
		return SeedReport{}, ErrTenantRequired
	}
	rep := SeedReport{TenantID: tenantID, CreatedNames: []string{}}

	tags, err := s.src.DistinctAtomTags(ctx, tenantID)
	if err != nil {
		return SeedReport{}, fmt.Errorf("topic.Seed: read atom tags: %w", err)
	}
	existing, err := s.sink.ExistingRootNames(ctx, tenantID)
	if err != nil {
		return SeedReport{}, fmt.Errorf("topic.Seed: read existing root names: %w", err)
	}

	// runSeen dedupes within THIS source list so a repeated tag is considered
	// once (Scanned counts distinct non-blank names). Separate from `existing`,
	// which drives the skip-because-already-present count.
	runSeen := make(map[string]struct{}, len(tags))

	for _, raw := range tags {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue // blanks are not tags — drop, do not count
		}
		key := strings.ToLower(name)
		if _, dup := runSeen[key]; dup {
			continue // same tag seen twice this run — count once
		}
		runSeen[key] = struct{}{}
		rep.Scanned++

		if _, present := existing[key]; present {
			rep.Skipped++
			continue
		}

		n, err := NewTopicNode(tenantID, name, nil, rep.Created)
		if err != nil {
			// A malformed tag (e.g. over-long) is skipped rather than failing the
			// whole run — one bad slug must not starve the rest.
			rep.Skipped++
			continue
		}
		if err := s.sink.Create(ctx, n); err != nil {
			return SeedReport{}, fmt.Errorf("topic.Seed: create %q: %w", name, err)
		}
		rep.Created++
		rep.CreatedNames = append(rep.CreatedNames, n.Name)
	}
	return rep, nil
}
