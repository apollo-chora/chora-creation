// gateway_atom_embedding_client.go: G1' gap 1 (register 6.2), the atom
// embedder riding chora-model-gateway's Embed RPC instead of dialing Vertex
// directly. Every atom embedding now produces a TokenUsageLedger row at the
// single un-bypassable chokepoint (ADR-163), attributed to the atom's AUTHOR
// (the topic-classifier precedent: learning_atoms.gcid). The calling identity
// enters the guardrail contract at `permissive` (no Armor leg on
// text-to-vector, ledger markers Bypassed, zero cost by ruling).
//
// Implements the widened embedindex.Embedder port: tenant + gcid ride as
// explicit parameters because creation never stamps tracing ctx keys. The
// dial shape mirrors topic_classifier_gateway_client.go (the live
// creation-to-gateway precedent): plaintext gRPC to the mesh Service from env
// CHORA_MODEL_GATEWAY_GRPC_URL, the sidecar does mTLS, grpc.NewClient lazy
// dial, a minimal private client seam for tests.
//
// The logical model is PINNED client-side ("text-embedding-004", the same
// space consumption mints Growth-Edge vectors in; both sides MUST share one
// space for cosine search to mean anything) and ModelID() returns the same
// constant, so the atom_embeddings.model_id label can never drift from what
// was requested. 1024-d matches the vector(1024) column, sent explicitly.
//
// Error contract: parity with the retired AtomEmbeddingClient, the same
// agentengine sentinels, so the publish path's soft-fail and the backfill's
// loud per-atom failure handling are untouched.
package clients

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	mgv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/model_gateway/v1"

	"github.com/apollo-chora/chora-common/agentengine"
	"github.com/apollo-chora/chora-common/tracing"
)

// ErrAtomEmbedEmptyTarget fails construction when the gRPC target is empty.
// Boot wiring treats an unset URL as "indexer not wired" BEFORE constructing
// this client, so reaching this error means a wiring bug, not a config choice.
var ErrAtomEmbedEmptyTarget = errors.New(
	"gateway_atom_embedding_client: target empty (set CHORA_MODEL_GATEWAY_GRPC_URL)")

const (
	// atomEmbedDefaultTimeout bounds one embedding round-trip; embeds are fast
	// single-vector calls.
	atomEmbedDefaultTimeout = 10 * time.Second

	// atomEmbedAgentID is the fixed calling-adapter identity on the ledger row
	// (AgentRole) and in agent-guardrail-mapping.yaml (permissive entry).
	atomEmbedAgentID = "creation_atom_embedder"

	// atomEmbedCrewKind mirrors topicClassifierCrewKind, the sibling
	// creation-to-gateway caller.
	atomEmbedCrewKind = "content_creation"

	// atomEmbedLogicalModelID pins the embedding model REQUESTED on the wire;
	// ModelID() returns the same constant so request and stored label cannot
	// drift apart.
	atomEmbedLogicalModelID = "text-embedding-004"

	// atomEmbedTaskTypeDocument: atoms are the DOCUMENT side of the retrieval
	// pair (consumption embeds queries on its side).
	atomEmbedTaskTypeDocument = "RETRIEVAL_DOCUMENT"

	// atomEmbedOutputDimensions matches atom_embeddings vector(1024) — the
	// LiquidAI LFM2.5 embedding route's native width (see the registry's
	// `text-embedding-004` entry). Sent explicitly, never left to a remote
	// default.
	atomEmbedOutputDimensions = 1024
)

// atomEmbedGRPC is the slice of mgv1.ModelGatewayServiceClient this adapter
// calls (Embed only). The generated client satisfies it; tests inject a fake.
type atomEmbedGRPC interface {
	Embed(ctx context.Context, in *mgv1.EmbedRequest, opts ...grpc.CallOption) (*mgv1.EmbedResponse, error)
}

// GatewayAtomEmbeddingClient embeds atom text via the gateway for the
// atom_embeddings writer (the widened embedindex.Embedder port).
type GatewayAtomEmbeddingClient struct {
	client  atomEmbedGRPC
	timeout time.Duration
}

// NewGatewayAtomEmbeddingClient dials the gateway at the mesh target. An
// empty target fails loud. grpc.NewClient is lazy: a healthy return does not
// prove reachability; the first RPC surfaces Unavailable, which the publish
// path soft-fails and the backfill records loudly.
func NewGatewayAtomEmbeddingClient(target string, timeout time.Duration) (*GatewayAtomEmbeddingClient, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, ErrAtomEmbedEmptyTarget
	}
	if timeout <= 0 {
		timeout = atomEmbedDefaultTimeout
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("gateway_atom_embedding_client: dial %q: %w", target, err)
	}
	return &GatewayAtomEmbeddingClient{client: mgv1.NewModelGatewayServiceClient(conn), timeout: timeout}, nil
}

// NewGatewayAtomEmbeddingClientFromStub injects a fake gRPC client (tests).
func NewGatewayAtomEmbeddingClientFromStub(stub atomEmbedGRPC, timeout time.Duration) *GatewayAtomEmbeddingClient {
	if timeout <= 0 {
		timeout = atomEmbedDefaultTimeout
	}
	return &GatewayAtomEmbeddingClient{client: stub, timeout: timeout}
}

// ModelID reports the pinned logical embedding model for the
// atom_embeddings.model_id column.
func (c *GatewayAtomEmbeddingClient) ModelID() string {
	return atomEmbedLogicalModelID
}

// Embed produces one 1024-d document embedding for the supplied atom text,
// attributed to the given tenant + actor (the atom's author). All three are
// REQUIRED by the gateway; a gap refuses loud here, before any RPC.
func (c *GatewayAtomEmbeddingClient) Embed(ctx context.Context, tenantID, gcid, text string) ([]float32, error) {
	if c == nil || c.client == nil {
		return nil, agentengine.ErrEngineNotConfigured
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("%w: tenant required for ledger attribution", agentengine.ErrInvalidRequest)
	}
	if strings.TrimSpace(gcid) == "" {
		return nil, fmt.Errorf("%w: gcid required for ledger attribution (pass the atom author's gcid)", agentengine.ErrInvalidRequest)
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("%w: embed text required", agentengine.ErrInvalidRequest)
	}

	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.client.Embed(callCtx, &mgv1.EmbedRequest{
		InvocationId:     uuid.Must(uuid.NewV7()).String(),
		TenantId:         tenantID,
		Gcid:             gcid,
		AgentId:          atomEmbedAgentID,
		CrewKind:         atomEmbedCrewKind,
		LogicalModelId:   atomEmbedLogicalModelID,
		Text:             text,
		TaskType:         atomEmbedTaskTypeDocument,
		OutputDimensions: atomEmbedOutputDimensions,
		Traceparent:      tracing.TraceparentFromContext(ctx),
	})
	if err != nil {
		return nil, mapAtomEmbedRPCError(err)
	}
	if resp == nil || len(resp.GetValues()) == 0 {
		return nil, fmt.Errorf("%w: empty embedding response from gateway", agentengine.ErrStreamAborted)
	}
	return resp.GetValues(), nil
}

// mapAtomEmbedRPCError translates gRPC status codes onto the canonical
// agentengine sentinels the retired direct-Vertex client used.
func mapAtomEmbedRPCError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", agentengine.ErrEngineTimeout, err)
	}
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("%w: %v", agentengine.ErrEngineUnavailable, err)
	}
	switch st.Code() {
	case codes.DeadlineExceeded:
		return fmt.Errorf("%w: %v", agentengine.ErrEngineTimeout, err)
	case codes.InvalidArgument:
		return fmt.Errorf("%w: %v", agentengine.ErrInvalidRequest, err)
	default:
		return fmt.Errorf("%w: %v", agentengine.ErrEngineUnavailable, err)
	}
}
