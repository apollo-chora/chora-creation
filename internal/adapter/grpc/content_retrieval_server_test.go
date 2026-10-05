// content_retrieval_server_test.go — Epic-1b W4: the ContentRetrieval gRPC
// adapter (SearchEmbeddings over atom_embeddings pgvector).
package creationgrpc

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/creation/v1"
)

type fakeEmbeddingSearcher struct {
	gotTenant string
	gotQuery  []float32
	gotLimit  int
	matches   []EmbeddingMatch
	err       error
}

func (f *fakeEmbeddingSearcher) Search(_ context.Context, tenantID string, query []float32, limit int) ([]EmbeddingMatch, error) {
	f.gotTenant = tenantID
	f.gotQuery = query
	f.gotLimit = limit
	if f.err != nil {
		return nil, f.err
	}
	return f.matches, nil
}

func TestSearchEmbeddings_HappyPath(t *testing.T) {
	searcher := &fakeEmbeddingSearcher{matches: []EmbeddingMatch{
		{AtomID: "atom-1", CosineDistance: 0.12, Title: "Fractions I"},
		{AtomID: "atom-2", CosineDistance: 0.3, Title: "Fractions II"},
	}}
	srv := NewContentRetrievalServer(ContentRetrievalDeps{Embeddings: searcher})

	resp, err := srv.SearchEmbeddings(context.Background(), &creationv1.SearchEmbeddingsRequest{
		TenantId:       "tnt-1",
		QueryEmbedding: []float32{0.5, 0.25},
		Limit:          7,
	})
	if err != nil {
		t.Fatalf("SearchEmbeddings: %v", err)
	}
	if searcher.gotTenant != "tnt-1" || searcher.gotLimit != 7 || len(searcher.gotQuery) != 2 {
		t.Fatalf("searcher got tenant=%q limit=%d query=%v", searcher.gotTenant, searcher.gotLimit, searcher.gotQuery)
	}
	if len(resp.GetMatches()) != 2 {
		t.Fatalf("matches = %d", len(resp.GetMatches()))
	}
	m := resp.GetMatches()[0]
	if m.GetAtomId() != "atom-1" || m.GetTitle() != "Fractions I" || m.GetCosineDistance() != 0.12 {
		t.Fatalf("match = %+v", m)
	}
}

func TestSearchEmbeddings_Validation(t *testing.T) {
	srv := NewContentRetrievalServer(ContentRetrievalDeps{Embeddings: &fakeEmbeddingSearcher{}})

	_, err := srv.SearchEmbeddings(context.Background(), &creationv1.SearchEmbeddingsRequest{
		QueryEmbedding: []float32{0.5},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing tenant: code = %v, want InvalidArgument", status.Code(err))
	}
	_, err = srv.SearchEmbeddings(context.Background(), &creationv1.SearchEmbeddingsRequest{
		TenantId: "tnt-1",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing embedding: code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestSearchEmbeddings_NilDepFailsLoud(t *testing.T) {
	srv := NewContentRetrievalServer(ContentRetrievalDeps{})
	_, err := srv.SearchEmbeddings(context.Background(), &creationv1.SearchEmbeddingsRequest{
		TenantId:       "tnt-1",
		QueryEmbedding: []float32{0.5},
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("nil searcher: code = %v, want FailedPrecondition", status.Code(err))
	}
}

func TestSearchEmbeddings_SearchErrorIsInternal(t *testing.T) {
	srv := NewContentRetrievalServer(ContentRetrievalDeps{
		Embeddings: &fakeEmbeddingSearcher{err: errors.New("boom")},
	})
	_, err := srv.SearchEmbeddings(context.Background(), &creationv1.SearchEmbeddingsRequest{
		TenantId:       "tnt-1",
		QueryEmbedding: []float32{0.5},
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("search error: code = %v, want Internal", status.Code(err))
	}
}
