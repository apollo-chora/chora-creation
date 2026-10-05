// Package clients — Saved-atom-ID gRPC client for chora-sharing.
//
// SavedAtomClient implements ports.SavedAtomIDFetcher by wrapping
// chora-sharing's `Sharing.ListSavedAtomIDs` gRPC contract
// (chora-contracts/proto/services/sharing/v1/sharing.proto). Used by the
// question picker's `saved` disjunct per docs/design/ux_unified_atom_picker.md.
package clients

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	sharingv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/sharing/v1"

	"github.com/apollo-chora/chora-creation/internal/ports"
)

// SavedAtomClientConfig carries the dial-side options.
type SavedAtomClientConfig struct {
	// GRPCClient is the pre-dialled chora-sharing SharingClient. When nil
	// the adapter dials GRPCAddr lazily (convenience for tests).
	GRPCClient sharingv1.SharingClient
	// GRPCAddr is the chora-sharing gRPC address (e.g. "localhost:9086").
	// Used only when GRPCClient is nil.
	GRPCAddr string
	// Timeout caps the ListSavedAtomIDs round-trip. Default 3s.
	Timeout time.Duration
}

// SavedAtomClient implements ports.SavedAtomIDFetcher.
type SavedAtomClient struct {
	cfg      SavedAtomClientConfig
	fallback sharingv1.SharingClient
}

// NewSavedAtomClient constructs the adapter. When cfg.GRPCClient is non-nil
// it is used directly; otherwise a lazy insecure dial is prepared.
func NewSavedAtomClient(cfg SavedAtomClientConfig) *SavedAtomClient {
	c := &SavedAtomClient{cfg: cfg}
	if cfg.Timeout == 0 {
		c.cfg.Timeout = 3 * time.Second
	}
	return c
}

// client lazily dials the fallback connection on first use.
func (c *SavedAtomClient) client(ctx context.Context) (sharingv1.SharingClient, error) {
	if c.cfg.GRPCClient != nil {
		return c.cfg.GRPCClient, nil
	}
	if c.fallback != nil {
		return c.fallback, nil
	}
	conn, err := grpc.NewClient(c.cfg.GRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(4<<20)),
	)
	if err != nil {
		return nil, err
	}
	c.fallback = sharingv1.NewSharingClient(conn)
	return c.fallback, nil
}

// ListSavedAtomIDs fetches the caller's bookmarked atom IDs from chora-sharing.
// Returns empty (not error) on Unavailable — the picker surfaces partial_result
// and continues with the `mine` disjunct only.
func (c *SavedAtomClient) ListSavedAtomIDs(ctx context.Context, gcid, tenantID string) ([]string, bool, error) {
	cl, err := c.client(ctx)
	if err != nil {
		return nil, false, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	resp, err := cl.ListSavedAtomIDs(ctx, &sharingv1.ListSavedAtomIDsRequest{
		Gcid:     gcid,
		TenantId: tenantID,
		Limit:    500,
	})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.Unavailable {
			return nil, false, nil
		}
		return nil, false, err
	}
	return resp.GetAtomIds(), resp.GetTruncated(), nil
}

// Compile-time interface check.
var _ ports.SavedAtomIDFetcher = (*SavedAtomClient)(nil)
