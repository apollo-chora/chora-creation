// Package embedindex — the atom-embedding indexer (Epic-1b W4).
//
// Composes an atom's text (title + stem + body, capped), embeds it
// (text-embedding-004, 768-d — the SAME space chora-consumption mints
// Growth-Edge concept vectors in), and upserts atom_embeddings. Two drivers:
//
//   - publish-time: the publish handler fires IndexAtom asynchronously after
//     the DRAFT→PUBLISHED save (soft-fail — publishing never blocks on Vertex);
//   - backfill: an env-gated startup sweep embeds already-published atoms that
//     predate this seam (idempotent; RLS forces per-tenant scoping, so the
//     tenant list comes from ATOM_EMBEDDING_BACKFILL_TENANTS).
//
// Mirrors internal/mediarehome's shape: a small service package with locally
// declared driven ports; cmd/server adapts the pg repo + Vertex client on.
package embedindex

import (
	"context"
	"fmt"
	"strings"
)

// maxEmbedTextLen caps the composed text (Vertex input limits + cost).
const maxEmbedTextLen = 2000

// Atom is one backfill candidate. AuthorGCID is the atom's author
// (learning_atoms.gcid): the actor every embedding is attributed to on the
// gateway ledger, the exact precedent of the topic-tag backfill.
type Atom struct {
	AtomID     string
	Title      string
	Body       string
	Stem       string
	AuthorGCID string
}

// Embedder produces the 768-d document embedding. Tenant + gcid ride
// explicitly (creation never stamps tracing ctx keys): the gateway refuses an
// unattributed embed, so the port carries the attribution rather than hoping
// the ctx does.
type Embedder interface {
	Embed(ctx context.Context, tenantID, gcid, text string) ([]float32, error)
	ModelID() string
}

// Store persists embeddings + lists backfill candidates.
type Store interface {
	Upsert(ctx context.Context, tenantID, atomID string, embedding []float32, modelID string) error
	ListPublishedMissingEmbedding(ctx context.Context, tenantID string, limit int) ([]Atom, error)
}

// Service is the indexer.
type Service struct {
	embedder Embedder
	store    Store
}

// New constructs the indexer.
func New(embedder Embedder, store Store) *Service {
	return &Service{embedder: embedder, store: store}
}

// IndexAtom embeds + upserts one atom under its author's identity. A
// text-less atom is a no-op (nothing meaningful to embed); transport/storage
// errors surface so the caller can decide retry vs log-and-drop.
func (s *Service) IndexAtom(ctx context.Context, tenantID, actorGCID, atomID, title, stem, body string) error {
	text := ComposeAtomText(title, stem, body)
	if text == "" {
		return nil
	}
	vec, err := s.embedder.Embed(ctx, tenantID, actorGCID, text)
	if err != nil {
		return fmt.Errorf("embedindex: embed atom %s: %w", atomID, err)
	}
	if err := s.store.Upsert(ctx, tenantID, atomID, vec, s.embedder.ModelID()); err != nil {
		return fmt.Errorf("embedindex: upsert atom %s: %w", atomID, err)
	}
	return nil
}

// Backfill embeds one batch of published-but-unembedded atoms for a tenant.
// Returns how many were indexed. Text-less atoms are skipped silently (they
// will reappear in every scan but never block the batch).
func (s *Service) Backfill(ctx context.Context, tenantID string, batchSize int) (int, error) {
	atoms, err := s.store.ListPublishedMissingEmbedding(ctx, tenantID, batchSize)
	if err != nil {
		return 0, fmt.Errorf("embedindex: backfill scan: %w", err)
	}
	indexed := 0
	for _, a := range atoms {
		if ComposeAtomText(a.Title, a.Stem, a.Body) == "" {
			continue
		}
		if err := s.IndexAtom(ctx, tenantID, a.AuthorGCID, a.AtomID, a.Title, a.Stem, a.Body); err != nil {
			return indexed, err
		}
		indexed++
	}
	return indexed, nil
}

// ComposeAtomText joins title + stem + body (newline-separated, trimmed,
// capped at maxEmbedTextLen).
func ComposeAtomText(title, stem, body string) string {
	parts := make([]string, 0, 3)
	for _, p := range []string{title, stem, body} {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	text := strings.Join(parts, "\n")
	if len(text) > maxEmbedTextLen {
		text = text[:maxEmbedTextLen]
	}
	return text
}
