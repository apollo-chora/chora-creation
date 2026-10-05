// reuse_context_client_test.go — RED-first tests for the chora-sharing
// reuse-consent gRPC client (ADR-229 WS-2, CHO-2133).
//
// The client wraps two RPCs on the Sharing service:
//
//   - GetReuseContext   → ports.ReuseContextFetcher (picker + snapshot gate)
//   - AuthorizeAtomUse  → ports.AtomUseAuthorizer  (D2 audit grant at
//     snapshot time, scope TEST_SET, free-license v1)
//
// Unlike SavedAtomClient (best-effort, swallows Unavailable), this client
// FAILS LOUD on every error — the consent context is the authorisation
// input; degrading it would silently narrow or widen the gate.
package clients_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	sharingv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/sharing/v1"

	"github.com/apollo-chora/chora-creation/internal/adapter/clients"
)

type fakeSharingReuseRPC struct {
	reuseResp *sharingv1.GetReuseContextResponse
	reuseErr  error
	reuseReq  *sharingv1.GetReuseContextRequest

	authResp *sharingv1.AuthorizeAtomUseResponse
	authErr  error
	authReq  *sharingv1.AuthorizeAtomUseRequest
}

func (f *fakeSharingReuseRPC) GetReuseContext(_ context.Context, in *sharingv1.GetReuseContextRequest, _ ...grpc.CallOption) (*sharingv1.GetReuseContextResponse, error) {
	f.reuseReq = in
	if f.reuseErr != nil {
		return nil, f.reuseErr
	}
	return f.reuseResp, nil
}

func (f *fakeSharingReuseRPC) AuthorizeAtomUse(_ context.Context, in *sharingv1.AuthorizeAtomUseRequest, _ ...grpc.CallOption) (*sharingv1.AuthorizeAtomUseResponse, error) {
	f.authReq = in
	if f.authErr != nil {
		return nil, f.authErr
	}
	return f.authResp, nil
}

func TestReuseContextClient_GetReuseContext_MapsResponse(t *testing.T) {
	t.Parallel()
	rpc := &fakeSharingReuseRPC{reuseResp: &sharingv1.GetReuseContextResponse{
		FriendGcids:    []string{"friend-1"},
		GrantedAtomIds: []string{"atom-1", "atom-2"},
	}}
	c := clients.NewSharingReuseClient(clients.SharingReuseClientConfig{GRPCClient: rpc})

	rc, err := c.GetReuseContext(context.Background(), "caller-1", "tenant-1")
	if err != nil {
		t.Fatalf("GetReuseContext: %v", err)
	}
	if rpc.reuseReq.GetGcid() != "caller-1" || rpc.reuseReq.GetTenantId() != "tenant-1" {
		t.Errorf("wire req = %v; want gcid=caller-1 tenant=tenant-1", rpc.reuseReq)
	}
	if len(rc.GrantedAtomIDs) != 2 || rc.GrantedAtomIDs[0] != "atom-1" {
		t.Errorf("GrantedAtomIDs = %v; want [atom-1 atom-2]", rc.GrantedAtomIDs)
	}
	if len(rc.FriendGCIDs) != 1 || rc.FriendGCIDs[0] != "friend-1" {
		t.Errorf("FriendGCIDs = %v; want [friend-1]", rc.FriendGCIDs)
	}
}

func TestReuseContextClient_GetReuseContext_UnavailableIsAnError(t *testing.T) {
	t.Parallel()
	// The SavedAtomClient swallows Unavailable (best-effort bookmarks). The
	// consent context must NOT — a sharing outage fails the picker loud.
	rpc := &fakeSharingReuseRPC{reuseErr: status.Error(codes.Unavailable, "sharing down")}
	c := clients.NewSharingReuseClient(clients.SharingReuseClientConfig{GRPCClient: rpc})

	_, err := c.GetReuseContext(context.Background(), "caller-1", "tenant-1")
	if err == nil {
		t.Fatalf("GetReuseContext: want error on Unavailable (fail loud), got nil")
	}
}

func TestReuseContextClient_AuthorizeTestSetUse_WireShape(t *testing.T) {
	t.Parallel()
	rpc := &fakeSharingReuseRPC{authResp: &sharingv1.AuthorizeAtomUseResponse{GrantId: "g-1"}}
	c := clients.NewSharingReuseClient(clients.SharingReuseClientConfig{GRPCClient: rpc})

	if err := c.AuthorizeTestSetUse(context.Background(), "tenant-1", "grantee-1", "atom-1"); err != nil {
		t.Fatalf("AuthorizeTestSetUse: %v", err)
	}
	req := rpc.authReq
	if req.GetGranteeGcid() != "grantee-1" || req.GetTenantId() != "tenant-1" || req.GetAtomId() != "atom-1" {
		t.Errorf("wire req = %v; want grantee-1/tenant-1/atom-1", req)
	}
	if req.GetScope() != sharingv1.GrantScope_GRANT_SCOPE_TEST_SET {
		t.Errorf("scope = %v; want GRANT_SCOPE_TEST_SET", req.GetScope())
	}
	if req.GetIdempotencyKey() == "" {
		t.Errorf("idempotency_key must be set (deterministic snapshot-time audit)")
	}
	if req.GetSourceShareEntry() != "" {
		t.Errorf("source_share_entry must stay empty (audience-based D2 grant)")
	}
}

func TestReuseContextClient_AuthorizeTestSetUse_IdempotencyKeyDeterministic(t *testing.T) {
	t.Parallel()
	rpc := &fakeSharingReuseRPC{authResp: &sharingv1.AuthorizeAtomUseResponse{GrantId: "g-1"}}
	c := clients.NewSharingReuseClient(clients.SharingReuseClientConfig{GRPCClient: rpc})

	if err := c.AuthorizeTestSetUse(context.Background(), "tenant-1", "grantee-1", "atom-1"); err != nil {
		t.Fatalf("AuthorizeTestSetUse: %v", err)
	}
	first := rpc.authReq.GetIdempotencyKey()
	if err := c.AuthorizeTestSetUse(context.Background(), "tenant-1", "grantee-1", "atom-1"); err != nil {
		t.Fatalf("AuthorizeTestSetUse (2nd): %v", err)
	}
	if rpc.authReq.GetIdempotencyKey() != first {
		t.Errorf("idempotency key not deterministic: %q then %q", first, rpc.authReq.GetIdempotencyKey())
	}
}

func TestReuseContextClient_AuthorizeTestSetUse_ErrorPropagates(t *testing.T) {
	t.Parallel()
	rpc := &fakeSharingReuseRPC{authErr: errors.New("boom")}
	c := clients.NewSharingReuseClient(clients.SharingReuseClientConfig{GRPCClient: rpc})

	if err := c.AuthorizeTestSetUse(context.Background(), "tenant-1", "grantee-1", "atom-1"); err == nil {
		t.Fatalf("AuthorizeTestSetUse: want error, got nil (grant write is the D2 audit — never swallow)")
	}
}

func TestReuseContextClient_LazyDial_CachesFallbackClient(t *testing.T) {
	t.Parallel()
	// grpc.NewClient is lazy (no network I/O at construction) — the adapter
	// must build the fallback client from GRPCAddr once and reuse it.
	c := clients.NewSharingReuseClient(clients.SharingReuseClientConfig{
		GRPCAddr: "localhost:1", // never dialled eagerly
		Timeout:  10 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// The RPC itself fails (nothing listens) — the point is the dial path
	// constructs + caches a client and the failure is LOUD, not swallowed.
	if _, err := c.GetReuseContext(ctx, "g", "t"); err == nil {
		t.Fatalf("GetReuseContext against a dead address: want error, got nil")
	}
	if err := c.AuthorizeTestSetUse(ctx, "t", "g", "a"); err == nil {
		t.Fatalf("AuthorizeTestSetUse against a dead address: want error, got nil")
	}
}
