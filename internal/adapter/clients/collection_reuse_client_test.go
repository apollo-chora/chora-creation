// collection_reuse_client_test.go — ADR-233 sharing-client slice (WS-4),
// RED-first.
//
// Two additions to SharingReuseClient:
//
//   - AuthorizeCollectionUse → Sharing.AuthorizeAtomUse with scope
//     GRANT_SCOPE_COLLECTION. ADR-233 D10a makes convert-to-study-list the THIRD
//     grant-write site, and activates a GrantScope enum value that has been dead
//     in the contract until now.
//   - ConsentContext → adapts GetReuseContext into the domain's
//     reuseconsent.Context (with ActorGCID set), so the collection gates and the
//     picker/snapshot gates share one predicate and can never diverge.
//
// FAIL-LOUD is the contract: every error propagates, INCLUDING Unavailable.
package clients_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	sharingv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/sharing/v1"

	"github.com/apollo-chora/chora-creation/internal/adapter/clients"
	"github.com/apollo-chora/chora-creation/internal/domain/collection"
)

func TestAuthorizeCollectionUse_WireShape(t *testing.T) {
	t.Parallel()

	rpc := &fakeSharingReuseRPC{authResp: &sharingv1.AuthorizeAtomUseResponse{GrantId: "g-1"}}
	c := clients.NewSharingReuseClient(clients.SharingReuseClientConfig{GRPCClient: rpc})

	if err := c.AuthorizeCollectionUse(context.Background(), "tenant-1", "grantee-1", "atom-1"); err != nil {
		t.Fatalf("AuthorizeCollectionUse: %v", err)
	}
	req := rpc.authReq
	if req == nil {
		t.Fatal("no AuthorizeAtomUse request issued")
	}
	if req.GetScope() != sharingv1.GrantScope_GRANT_SCOPE_COLLECTION {
		t.Errorf("scope = %v; want GRANT_SCOPE_COLLECTION (the previously-dead enum value)", req.GetScope())
	}
	if req.GetTenantId() != "tenant-1" || req.GetGranteeGcid() != "grantee-1" || req.GetAtomId() != "atom-1" {
		t.Errorf("wire req = %v; want tenant/grantee/atom populated", req)
	}
	if got, want := req.GetIdempotencyKey(), "adr233-collection:tenant-1:grantee-1:atom-1"; got != want {
		t.Errorf("idempotency key = %q; want %q", got, want)
	}
}

// The key must be deterministic on (tenant, grantee, atom) so a re-convert
// reuses the grant rather than minting a second audit record for one reuse.
func TestAuthorizeCollectionUse_IdempotencyKeyIsDeterministic(t *testing.T) {
	t.Parallel()

	rpc := &fakeSharingReuseRPC{authResp: &sharingv1.AuthorizeAtomUseResponse{GrantId: "g-1"}}
	c := clients.NewSharingReuseClient(clients.SharingReuseClientConfig{GRPCClient: rpc})

	if err := c.AuthorizeCollectionUse(context.Background(), "t", "g", "a"); err != nil {
		t.Fatalf("AuthorizeCollectionUse: %v", err)
	}
	first := rpc.authReq.GetIdempotencyKey()
	if err := c.AuthorizeCollectionUse(context.Background(), "t", "g", "a"); err != nil {
		t.Fatalf("AuthorizeCollectionUse (2nd): %v", err)
	}
	if second := rpc.authReq.GetIdempotencyKey(); second != first {
		t.Errorf("idempotency key drifted: %q → %q", first, second)
	}
}

// The COLLECTION scope must not collide with the TEST_SET scope for the same
// (tenant, grantee, atom) — they are distinct reuses and distinct audit records.
func TestAuthorizeCollectionUse_KeyDoesNotCollideWithTestSet(t *testing.T) {
	t.Parallel()

	rpc := &fakeSharingReuseRPC{authResp: &sharingv1.AuthorizeAtomUseResponse{GrantId: "g-1"}}
	c := clients.NewSharingReuseClient(clients.SharingReuseClientConfig{GRPCClient: rpc})

	if err := c.AuthorizeTestSetUse(context.Background(), "t", "g", "a"); err != nil {
		t.Fatalf("AuthorizeTestSetUse: %v", err)
	}
	testSetKey := rpc.authReq.GetIdempotencyKey()

	if err := c.AuthorizeCollectionUse(context.Background(), "t", "g", "a"); err != nil {
		t.Fatalf("AuthorizeCollectionUse: %v", err)
	}
	collectionKey := rpc.authReq.GetIdempotencyKey()

	if testSetKey == collectionKey {
		t.Errorf("test-set and collection grants share idempotency key %q; they are distinct reuses", testSetKey)
	}
}

func TestAuthorizeCollectionUse_ErrorPropagates(t *testing.T) {
	t.Parallel()

	rpc := &fakeSharingReuseRPC{authErr: status.Error(codes.Unavailable, "sharing down")}
	c := clients.NewSharingReuseClient(clients.SharingReuseClientConfig{GRPCClient: rpc})

	if err := c.AuthorizeCollectionUse(context.Background(), "t", "g", "a"); err == nil {
		t.Fatal("want error on Unavailable — the D2 audit record is not optional")
	}
}

// -----------------------------------------------------------------------------
// ConsentContext
// -----------------------------------------------------------------------------

func TestConsentContext_AdaptsGetReuseContext(t *testing.T) {
	t.Parallel()

	rpc := &fakeSharingReuseRPC{reuseResp: &sharingv1.GetReuseContextResponse{
		FriendGcids:    []string{"friend-1"},
		GrantedAtomIds: []string{"atom-1", "atom-2"},
	}}
	c := clients.NewSharingReuseClient(clients.SharingReuseClientConfig{GRPCClient: rpc})

	cc, err := c.ConsentContext(context.Background(), "caller-1", "tenant-1")
	if err != nil {
		t.Fatalf("ConsentContext: %v", err)
	}
	if rpc.reuseReq.GetGcid() != "caller-1" || rpc.reuseReq.GetTenantId() != "tenant-1" {
		t.Errorf("wire req = %v; want gcid=caller-1 tenant=tenant-1", rpc.reuseReq)
	}
	// ActorGCID is what the disjunct's `own` leg compares against — if the
	// adapter forgot to set it, every atom would look like someone else's.
	if cc.ActorGCID != "caller-1" {
		t.Errorf("ActorGCID = %q; want caller-1", cc.ActorGCID)
	}
	if len(cc.FriendGCIDs) != 1 || cc.FriendGCIDs[0] != "friend-1" {
		t.Errorf("FriendGCIDs = %v; want [friend-1]", cc.FriendGCIDs)
	}
	if len(cc.GrantedAtomIDs) != 2 || cc.GrantedAtomIDs[0] != "atom-1" {
		t.Errorf("GrantedAtomIDs = %v; want [atom-1 atom-2]", cc.GrantedAtomIDs)
	}
}

func TestConsentContext_UnavailableIsAnError(t *testing.T) {
	t.Parallel()

	rpc := &fakeSharingReuseRPC{reuseErr: status.Error(codes.Unavailable, "sharing down")}
	c := clients.NewSharingReuseClient(clients.SharingReuseClientConfig{GRPCClient: rpc})

	if _, err := c.ConsentContext(context.Background(), "caller-1", "tenant-1"); err == nil {
		t.Fatal("want error on Unavailable — an empty context would silently NARROW the gate")
	}
}

// The client must satisfy the DOMAIN-owned ports (the domain owns its ports).
func TestSharingReuseClient_ImplementsCollectionGatePorts(t *testing.T) {
	t.Parallel()
	var _ collection.ConsentContextFetcher = (*clients.SharingReuseClient)(nil)
	var _ collection.CollectionUseAuthorizer = (*clients.SharingReuseClient)(nil)
}

// =============================================================================
// CHO-2174 — gRPC status → DOMAIN SENTINEL, at the adapter.
//
// The adapter is the only layer that may know gRPC exists. Live 2026-07-13, it
// did not translate, and a consent refusal reached A+ as:
//
//	400 CREATION_COLLECTION_INVALID
//	"collection.Service.ConvertToStudyList: authorize collection use of atom
//	 00000000-…-a0a4: reuse client: AuthorizeAtomUse (collection): rpc error:
//	 code = FailedPrecondition desc = atom not published or withdrawn (412)"
//
// — wrong status (a refusal is not a malformed request), internals leaked, and
// an error code the convert dialog could do nothing with.
//
// These tests pin the translation table. The load-bearing one is
// UnavailableMapsToSharingUnavailable: an outage must NEVER be mistaken for a
// terminal 4xx verdict, because the caller (the domain) refuses on ANY error and
// the HTTP layer must report that refusal as ours, not the learner's.
// =============================================================================

// The production refusal from the live walk: chora-sharing has no
// atom_projections row for the atom → FailedPrecondition "atom not published or
// withdrawn (412)". That is a shareability verdict, not a bad request.
func TestAuthorizeCollectionUse_FailedPreconditionMapsToNotShareable(t *testing.T) {
	t.Parallel()

	rpc := &fakeSharingReuseRPC{authErr: status.Error(codes.FailedPrecondition,
		"atom not published or withdrawn (412)")}
	c := clients.NewSharingReuseClient(clients.SharingReuseClientConfig{GRPCClient: rpc})

	err := c.AuthorizeCollectionUse(context.Background(), "t", "g", "a")
	if !errors.Is(err, collection.ErrAtomNotShareable) {
		t.Fatalf("err = %v; want ErrAtomNotShareable (the atom has no reusable projection)", err)
	}
	// It must not be mistaken for an outage — retrying will never help.
	if errors.Is(err, collection.ErrSharingUnavailable) {
		t.Errorf("a shareability verdict must not masquerade as an outage: %v", err)
	}
}

// PermissionDenied is the upstream authority REFUSING this actor — a terminal
// 403, never a 502 and never the local disjunct's ErrAtomNotReusable.
func TestAuthorizeCollectionUse_PermissionDeniedMapsToReuseDenied(t *testing.T) {
	t.Parallel()

	rpc := &fakeSharingReuseRPC{authErr: status.Error(codes.PermissionDenied, "not entitled")}
	c := clients.NewSharingReuseClient(clients.SharingReuseClientConfig{GRPCClient: rpc})

	err := c.AuthorizeCollectionUse(context.Background(), "t", "g", "a")
	if !errors.Is(err, collection.ErrAtomReuseDenied) {
		t.Fatalf("err = %v; want ErrAtomReuseDenied", err)
	}
	if errors.Is(err, collection.ErrSharingUnavailable) {
		t.Errorf("a consent refusal must not masquerade as an outage: %v", err)
	}
}

// 🔴 THE PROPERTY THAT MATTERS MOST. chora-sharing being down must resolve to
// ErrSharingUnavailable — a LOUD, refusing, upstream-fault sentinel — and must
// NEVER be reclassified as a terminal 4xx verdict. A gate that reports an outage
// as a verdict is a gate that has quietly stopped gating.
func TestAuthorizeCollectionUse_UnavailableMapsToSharingUnavailable(t *testing.T) {
	t.Parallel()

	rpc := &fakeSharingReuseRPC{authErr: status.Error(codes.Unavailable, "connection refused")}
	c := clients.NewSharingReuseClient(clients.SharingReuseClientConfig{GRPCClient: rpc})

	err := c.AuthorizeCollectionUse(context.Background(), "t", "g", "a")
	if !errors.Is(err, collection.ErrSharingUnavailable) {
		t.Fatalf("err = %v; want ErrSharingUnavailable (fail loud — the grant was never minted)", err)
	}
	for _, terminal := range []error{
		collection.ErrAtomNotShareable,
		collection.ErrAtomReuseDenied,
		collection.ErrAtomNotReusable,
		collection.ErrSharingRejectedRequest,
	} {
		if errors.Is(err, terminal) {
			t.Errorf("an outage was reclassified as the terminal verdict %v — the gate degraded", terminal)
		}
	}
}

// codes.Internal (sharing's own 500s: "projection lookup: …", "authorize grant:
// …") is an upstream fault, not a learner fault → the loud bucket.
func TestAuthorizeCollectionUse_InternalMapsToSharingUnavailable(t *testing.T) {
	t.Parallel()

	rpc := &fakeSharingReuseRPC{authErr: status.Error(codes.Internal, "authorize grant: pq: deadlock")}
	c := clients.NewSharingReuseClient(clients.SharingReuseClientConfig{GRPCClient: rpc})

	if err := c.AuthorizeCollectionUse(context.Background(), "t", "g", "a"); !errors.Is(err, collection.ErrSharingUnavailable) {
		t.Fatalf("err = %v; want ErrSharingUnavailable", err)
	}
}

// sharing's notWired() fails loud with codes.Unimplemented — an unwired upstream
// dependency is an outage, not a verdict.
func TestAuthorizeCollectionUse_UnimplementedMapsToSharingUnavailable(t *testing.T) {
	t.Parallel()

	rpc := &fakeSharingReuseRPC{authErr: status.Error(codes.Unimplemented,
		`sharing: dependency "Grants" not wired (fail-loud per §1.1)`)}
	c := clients.NewSharingReuseClient(clients.SharingReuseClientConfig{GRPCClient: rpc})

	if err := c.AuthorizeCollectionUse(context.Background(), "t", "g", "a"); !errors.Is(err, collection.ErrSharingUnavailable) {
		t.Fatalf("err = %v; want ErrSharingUnavailable", err)
	}
}

// InvalidArgument means sharing rejected the request WE built (unspecified scope,
// blank idempotency key). That is a creation-side defect: it must never surface
// as a 4xx blaming the learner, and must not be mistaken for a retryable outage.
func TestAuthorizeCollectionUse_InvalidArgumentMapsToRejectedRequest(t *testing.T) {
	t.Parallel()

	rpc := &fakeSharingReuseRPC{authErr: status.Error(codes.InvalidArgument, "idempotency_key required")}
	c := clients.NewSharingReuseClient(clients.SharingReuseClientConfig{GRPCClient: rpc})

	err := c.AuthorizeCollectionUse(context.Background(), "t", "g", "a")
	if !errors.Is(err, collection.ErrSharingRejectedRequest) {
		t.Fatalf("err = %v; want ErrSharingRejectedRequest (our bug, not the learner's)", err)
	}
	if errors.Is(err, collection.ErrAtomNotShareable) || errors.Is(err, collection.ErrAtomReuseDenied) {
		t.Errorf("our own malformed request must not be reported as a verdict about the atom: %v", err)
	}
}

// A non-status transport error (dial failure, context cancellation) carries no
// gRPC code at all — it must still land in the loud bucket, never leak through
// untranslated into the domain.
func TestAuthorizeCollectionUse_NonStatusTransportErrorMapsToSharingUnavailable(t *testing.T) {
	t.Parallel()

	rpc := &fakeSharingReuseRPC{authErr: errors.New("dial tcp 10.0.0.1:9090: i/o timeout")}
	c := clients.NewSharingReuseClient(clients.SharingReuseClientConfig{GRPCClient: rpc})

	if err := c.AuthorizeCollectionUse(context.Background(), "t", "g", "a"); !errors.Is(err, collection.ErrSharingUnavailable) {
		t.Fatalf("err = %v; want ErrSharingUnavailable", err)
	}
}

// -----------------------------------------------------------------------------
// ConsentContext — the gate's OTHER gRPC leg (Sharing.GetReuseContext). It runs
// on AddAtom, GetVisible and Convert, so an outage here must be just as loud.
// -----------------------------------------------------------------------------

func TestConsentContext_UnavailableMapsToSharingUnavailable(t *testing.T) {
	t.Parallel()

	rpc := &fakeSharingReuseRPC{reuseErr: status.Error(codes.Unavailable, "sharing down")}
	c := clients.NewSharingReuseClient(clients.SharingReuseClientConfig{GRPCClient: rpc})

	_, err := c.ConsentContext(context.Background(), "caller-1", "tenant-1")
	if !errors.Is(err, collection.ErrSharingUnavailable) {
		t.Fatalf("err = %v; want ErrSharingUnavailable (an empty context would silently NARROW the gate)", err)
	}
}

func TestConsentContext_InternalMapsToSharingUnavailable(t *testing.T) {
	t.Parallel()

	rpc := &fakeSharingReuseRPC{reuseErr: status.Error(codes.Internal, "reuse context friends: pq: timeout")}
	c := clients.NewSharingReuseClient(clients.SharingReuseClientConfig{GRPCClient: rpc})

	if _, err := c.ConsentContext(context.Background(), "caller-1", "tenant-1"); !errors.Is(err, collection.ErrSharingUnavailable) {
		t.Fatalf("err = %v; want ErrSharingUnavailable", err)
	}
}

// The domain must NEVER see an `rpc error:` string — that text is the whole
// reason CHO-2174 exists. It stays available to the server-side log via the %w
// chain, but the sentinel is what the domain and the handler switch on.
func TestCollectionGateErrors_NeverSurfaceRawGRPCTextAsTheSentinel(t *testing.T) {
	t.Parallel()

	rpc := &fakeSharingReuseRPC{authErr: status.Error(codes.FailedPrecondition,
		"atom not published or withdrawn (412)")}
	c := clients.NewSharingReuseClient(clients.SharingReuseClientConfig{GRPCClient: rpc})

	err := c.AuthorizeCollectionUse(context.Background(), "t", "g", "a")
	if !errors.Is(err, collection.ErrAtomNotShareable) {
		t.Fatalf("err = %v; want ErrAtomNotShareable", err)
	}
	// The SENTINEL's own message — the one the HTTP layer is allowed to build a
	// human sentence from — must be free of transport vocabulary.
	if s := collection.ErrAtomNotShareable.Error(); strings.Contains(s, "rpc error") || strings.Contains(s, "code =") {
		t.Errorf("domain sentinel carries gRPC vocabulary: %q", s)
	}
}
