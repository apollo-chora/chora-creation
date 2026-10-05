// embedindex_wiring.go: composition root for the Epic-1b W4 atom-embedding
// seam: the gateway Embed client + atom_embeddings pg repo behind the
// embedindex service (publish-time indexing + env-gated backfill) and the
// ContentRetrieval gRPC searcher.
//
// Env (feedback_no_inline_config):
//
//	CHORA_MODEL_GATEWAY_GRPC_URL    - the mesh gRPC target chora-creation
//	  already dials for the topic classifier; the embed indexer rides the
//	  same chokepoint (G1' gap 1, every embedding lands a ledger row).
//	  Unset: indexer NOT wired (publish skips the embed step;
//	  SearchEmbeddings still serves whatever rows exist).
//	ATOM_EMBEDDING_BACKFILL_TENANTS - CSV of tenant UUIDs to backfill at
//	  boot. RLS (NOBYPASSRLS app role) forbids a cross-tenant scan, so the
//	  operator names the tenants explicitly. Unset: no backfill.
package main

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/apollo-chora/chora-creation/internal/adapter/clients"
	creationgrpc "github.com/apollo-chora/chora-creation/internal/adapter/grpc"
	creationpg "github.com/apollo-chora/chora-creation/internal/adapter/pg"
	"github.com/apollo-chora/chora-creation/internal/embedindex"
)

// buildAtomEmbedIndexer wires the embedindex service. Nil repo (no pool) or
// missing gateway target or a dial error ⇒ nil (fail-soft, logged).
func buildAtomEmbedIndexer(_ context.Context, repo *creationpg.AtomEmbeddingRepository) *embedindex.Service {
	if repo == nil {
		log.Printf("creation: atom embed indexer NOT wired (no pgx pool)")
		return nil
	}
	target := strings.TrimSpace(os.Getenv("CHORA_MODEL_GATEWAY_GRPC_URL"))
	if target == "" {
		log.Printf("creation: atom embed indexer NOT wired (CHORA_MODEL_GATEWAY_GRPC_URL unset)")
		return nil
	}
	embedder, err := clients.NewGatewayAtomEmbeddingClient(target, 0)
	if err != nil {
		log.Printf("creation: atom embed indexer NOT wired (gateway embed client: %v)", err)
		return nil
	}
	log.Printf("creation: atom embed indexer wired via model-gateway (model=%s)", embedder.ModelID())
	return embedindex.New(embedder, embedStoreAdapter{repo: repo})
}

// startAtomEmbeddingBackfill sweeps published-but-unembedded atoms for the
// env-named tenants. Idempotent (missing-only scan); batches of 50 until dry.
func startAtomEmbeddingBackfill(ctx context.Context, svc *embedindex.Service) {
	if svc == nil {
		return
	}
	tenants := splitNonEmptyCSV(os.Getenv("ATOM_EMBEDDING_BACKFILL_TENANTS"))
	if len(tenants) == 0 {
		return
	}
	go func() {
		const batch = 50
		for _, tenantID := range tenants {
			total := 0
			for {
				runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				n, err := svc.Backfill(runCtx, tenantID, batch)
				cancel()
				total += n
				if err != nil {
					log.Printf("creation: atom embedding backfill tenant=%s stopped after %d (non-fatal): %v", tenantID, total, err)
					break
				}
				if n < batch {
					log.Printf("creation: atom embedding backfill tenant=%s complete (%d embedded)", tenantID, total)
					break
				}
			}
		}
	}()
}

// embedStoreAdapter maps the pg repo onto the embedindex.Store port.
type embedStoreAdapter struct {
	repo *creationpg.AtomEmbeddingRepository
}

func (a embedStoreAdapter) Upsert(ctx context.Context, tenantID, atomID string, embedding []float32, modelID string) error {
	return a.repo.Upsert(ctx, tenantID, atomID, embedding, modelID)
}

func (a embedStoreAdapter) ListPublishedMissingEmbedding(ctx context.Context, tenantID string, limit int) ([]embedindex.Atom, error) {
	rows, err := a.repo.ListPublishedMissingEmbedding(ctx, tenantID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]embedindex.Atom, 0, len(rows))
	for _, r := range rows {
		out = append(out, embedindex.Atom{AtomID: r.AtomID, Title: r.Title, Body: r.Body, Stem: r.Stem, AuthorGCID: r.AuthorGCID})
	}
	return out, nil
}

// grpcEmbeddingSearcher maps the pg repo onto the ContentRetrieval port.
// Nil repo stays nil at the call site (the RPC FAILED_PRECONDITIONs).
type grpcEmbeddingSearcher struct {
	repo *creationpg.AtomEmbeddingRepository
}

func (s grpcEmbeddingSearcher) Search(ctx context.Context, tenantID string, query []float32, limit int) ([]creationgrpc.EmbeddingMatch, error) {
	matches, err := s.repo.Search(ctx, tenantID, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]creationgrpc.EmbeddingMatch, 0, len(matches))
	for _, m := range matches {
		out = append(out, creationgrpc.EmbeddingMatch{
			AtomID:         m.AtomID,
			CosineDistance: m.CosineDistance,
			Title:          m.Title,
		})
	}
	return out, nil
}

// splitNonEmptyCSV splits a comma-separated env value, trimming + dropping
// empties.
func splitNonEmptyCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
