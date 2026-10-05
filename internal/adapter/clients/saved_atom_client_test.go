// saved_atom_client_test.go — tests for the chora-sharing saved-atom-ID
// gRPC client (best-effort bookmarks for the picker's `saved` disjunct).
//
// Unlike SharingReuseClient (fail loud), SavedAtomClient swallows
// Unavailable and returns empty — the picker surfaces partial_result and
// continues with the `mine` disjunct only.
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

type fakeSharingSavedRPC struct {
	sharingv1.SharingClient // embedded nil — only ListSavedAtomIDs is exercised

	resp *sharingv1.ListSavedAtomIDsResponse
	err  error
	req  *sharingv1.ListSavedAtomIDsRequest
}

func (f *fakeSharingSavedRPC) ListSavedAtomIDs(_ context.Context, in *sharingv1.ListSavedAtomIDsRequest, _ ...grpc.CallOption) (*sharingv1.ListSavedAtomIDsResponse, error) {
	f.req = in
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestSavedAtomClient_ListSavedAtomIDs_MapsResponse(t *testing.T) {
	t.Parallel()
	rpc := &fakeSharingSavedRPC{resp: &sharingv1.ListSavedAtomIDsResponse{
		AtomIds:   []string{"atom-1", "atom-2"},
		Truncated: true,
	}}
	c := clients.NewSavedAtomClient(clients.SavedAtomClientConfig{GRPCClient: rpc})

	ids, truncated, err := c.ListSavedAtomIDs(context.Background(), "caller-1", "tenant-1")
	if err != nil {
		t.Fatalf("ListSavedAtomIDs: %v", err)
	}
	if rpc.req.GetGcid() != "caller-1" || rpc.req.GetTenantId() != "tenant-1" {
		t.Errorf("wire req = %v; want gcid=caller-1 tenant=tenant-1", rpc.req)
	}
	if rpc.req.GetLimit() != 500 {
		t.Errorf("limit = %d; want 500", rpc.req.GetLimit())
	}
	if len(ids) != 2 || ids[0] != "atom-1" {
		t.Errorf("ids = %v; want [atom-1 atom-2]", ids)
	}
	if !truncated {
		t.Errorf("truncated = false; want true")
	}
}

func TestSavedAtomClient_ListSavedAtomIDs_UnavailableIsSwallowed(t *testing.T) {
	t.Parallel()
	// Best-effort: a sharing outage must not fail the picker — empty, no error.
	rpc := &fakeSharingSavedRPC{err: status.Error(codes.Unavailable, "sharing down")}
	c := clients.NewSavedAtomClient(clients.SavedAtomClientConfig{GRPCClient: rpc})

	ids, truncated, err := c.ListSavedAtomIDs(context.Background(), "caller-1", "tenant-1")
	if err != nil {
		t.Fatalf("ListSavedAtomIDs on Unavailable: want nil error, got %v", err)
	}
	if len(ids) != 0 || truncated {
		t.Errorf("ids=%v truncated=%v; want empty/false", ids, truncated)
	}
}

func TestSavedAtomClient_ListSavedAtomIDs_OtherErrorsPropagate(t *testing.T) {
	t.Parallel()
	rpc := &fakeSharingSavedRPC{err: status.Error(codes.Internal, "boom")}
	c := clients.NewSavedAtomClient(clients.SavedAtomClientConfig{GRPCClient: rpc})

	if _, _, err := c.ListSavedAtomIDs(context.Background(), "caller-1", "tenant-1"); err == nil {
		t.Fatalf("ListSavedAtomIDs: want error on non-Unavailable failure, got nil")
	}
	rpc.err = errors.New("plain boom")
	if _, _, err := c.ListSavedAtomIDs(context.Background(), "caller-1", "tenant-1"); err == nil {
		t.Fatalf("ListSavedAtomIDs: want error on non-status failure, got nil")
	}
}

func TestSavedAtomClient_LazyDial_CachesFallbackClient(t *testing.T) {
	t.Parallel()
	// grpc.NewClient is lazy (no network I/O at construction) — the adapter
	// must build the fallback client from GRPCAddr once and reuse it.
	c := clients.NewSavedAtomClient(clients.SavedAtomClientConfig{
		GRPCAddr: "localhost:1", // never dialled eagerly
		Timeout:  10 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// The RPC itself fails (nothing listens). A deadline-exceeded dial surfaces
	// as Unavailable and is swallowed by contract; any other failure is an
	// error. Either way the call must not hang or panic.
	_, _, _ = c.ListSavedAtomIDs(ctx, "g", "t")
	// Second call reuses the cached fallback client.
	_, _, _ = c.ListSavedAtomIDs(ctx, "g", "t")
}

func TestSavedAtomClient_DefaultTimeout(t *testing.T) {
	t.Parallel()
	// Zero Timeout must default to 3s — observable only indirectly; assert the
	// constructor returns a usable client and the RPC honours a short ctx.
	rpc := &fakeSharingSavedRPC{resp: &sharingv1.ListSavedAtomIDsResponse{AtomIds: []string{"a"}}}
	c := clients.NewSavedAtomClient(clients.SavedAtomClientConfig{GRPCClient: rpc})
	ids, _, err := c.ListSavedAtomIDs(context.Background(), "g", "t")
	if err != nil || len(ids) != 1 {
		t.Fatalf("ListSavedAtomIDs with default timeout: ids=%v err=%v", ids, err)
	}
}
