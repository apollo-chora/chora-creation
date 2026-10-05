// content_retrieval_server.go — gRPC adapter for the ContentRetrieval contract
// (Epic-1b W4): SearchEmbeddings answers chora-consumption's Growth-Edge
// drill-atom resolution with the nearest PUBLISHED atoms by cosine distance
// over chora_creation.atom_embeddings (pgvector). The caller supplies the
// 768-d query embedding — this server never embeds.
//
// Per `feedback_no_stubs_real_wiring`: registered unconditionally; a nil
// searcher dep returns FAILED_PRECONDITION (fail-loud, distinguishable from
// "method not registered").
package creationgrpc

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/creation/v1"
)

// EmbeddingMatch is the adapter-local match carrier (cmd/server maps the pg
// repo's matches onto it — keeps this adapter off the pg package).
type EmbeddingMatch struct {
	AtomID         string
	CosineDistance float32
	Title          string
}

// EmbeddingSearcher is the driven port SearchEmbeddings dispatches into.
type EmbeddingSearcher interface {
	Search(ctx context.Context, tenantID string, query []float32, limit int) ([]EmbeddingMatch, error)
}

// ContentRetrievalServer adapts the ContentRetrieval gRPC contract.
type ContentRetrievalServer struct {
	creationv1.UnimplementedContentRetrievalServer

	embeddings EmbeddingSearcher
}

// ContentRetrievalDeps captures the constructor arguments.
type ContentRetrievalDeps struct {
	Embeddings EmbeddingSearcher
}

// NewContentRetrievalServer constructs the adapter.
func NewContentRetrievalServer(deps ContentRetrievalDeps) *ContentRetrievalServer {
	return &ContentRetrievalServer{embeddings: deps.Embeddings}
}

// SearchEmbeddings — nearest published atoms by cosine distance.
func (s *ContentRetrievalServer) SearchEmbeddings(ctx context.Context, req *creationv1.SearchEmbeddingsRequest) (*creationv1.SearchEmbeddingsResponse, error) {
	if strings.TrimSpace(req.GetTenantId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}
	if len(req.GetQueryEmbedding()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "query_embedding required")
	}
	if s.embeddings == nil {
		return nil, status.Error(codes.FailedPrecondition,
			"content_retrieval: embedding searcher not wired (pgx pool unavailable at boot)")
	}

	matches, err := s.embeddings.Search(ctx, req.GetTenantId(), req.GetQueryEmbedding(), int(req.GetLimit()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "content_retrieval: search: %v", err)
	}
	out := &creationv1.SearchEmbeddingsResponse{
		Matches: make([]*creationv1.EmbeddingMatch, 0, len(matches)),
	}
	for _, m := range matches {
		out.Matches = append(out.Matches, &creationv1.EmbeddingMatch{
			AtomId:         m.AtomID,
			CosineDistance: m.CosineDistance,
			Title:          m.Title,
		})
	}
	return out, nil
}
