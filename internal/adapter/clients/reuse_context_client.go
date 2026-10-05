// reuse_context_client.go — chora-sharing reuse-consent gRPC client
// (ADR-229 WS-2, CHO-2133).
//
// SharingReuseClient implements two ports over the Sharing service
// (chora-contracts/proto/services/sharing/v1/sharing.proto):
//
//   - ports.ReuseContextFetcher → Sharing.GetReuseContext (friend set +
//     active-grant atom ids, once per picker search / snapshot check)
//   - ports.AtomUseAuthorizer   → Sharing.AuthorizeAtomUse (the D2 audit
//     grant written idempotently at snapshot time; scope TEST_SET, v1
//     free-license, audience-based — no source_share_entry)
//
// and, since ADR-233 WS-4, the two DOMAIN-owned collection gate ports:
//
//   - collection.ConsentContextFetcher  → Sharing.GetReuseContext, adapted into
//     reuseconsent.Context (the collection add + convert gates share ONE
//     predicate with the picker/snapshot gates, so they can never diverge)
//   - collection.CollectionUseAuthorizer → Sharing.AuthorizeAtomUse with scope
//     GRANT_SCOPE_COLLECTION (ADR-233 D10a — the third grant-write site)
//
// FAIL-LOUD contract: unlike SavedAtomClient (best-effort bookmarks,
// swallows Unavailable), every error here propagates — the consent context
// is the authorisation input, and degrading it would silently narrow or
// widen the ADR-229 gate. Both RPCs are already on the creation→sharing
// mesh allowlist (chora-infra/k8s/services/chora-sharing/
// authz-allow-creation-grpc.yaml).
package clients

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	sharingv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/sharing/v1"

	"github.com/apollo-chora/chora-creation/internal/domain/collection"
	"github.com/apollo-chora/chora-creation/internal/domain/reuseconsent"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// -----------------------------------------------------------------------------
// CHO-2174 — the hexagonal boundary for gRPC
// -----------------------------------------------------------------------------

// translateSharingGateErr converts a chora-sharing gRPC failure into a
// collection-DOMAIN sentinel. This function is the last place in the process
// that is allowed to know gRPC exists: past it, the domain and the HTTP handler
// switch on sentinels, never on codes or on `rpc error:` strings.
//
// It exists because they used to. Live 2026-07-13, a consent refusal travelled
// all the way to A+ as
//
//	400 CREATION_COLLECTION_INVALID
//	"collection.Service.ConvertToStudyList: authorize collection use of atom
//	 00000000-…-a0a4: reuse client: AuthorizeAtomUse (collection): rpc error:
//	 code = FailedPrecondition desc = atom not published or withdrawn (412)"
//
// — the wrong status, the call chain leaked, and nothing the convert dialog
// could turn into a sentence. This is the same gRPC-code → domain-sentinel →
// HTTP idiom CHO-2139 gave chora-delivery.
//
// The buckets:
//
//   - FailedPrecondition → collection.ErrAtomNotShareable (409). sharing holds no
//     reusable atom_projections row (unpublished / withdrawn), or the atom's
//     reuse_visibility is narrower than 'tenant'. A VERDICT: terminal, and
//     retrying changes nothing until the author re-publishes or re-widens.
//   - PermissionDenied   → collection.ErrAtomReuseDenied (403). The upstream
//     authority refused THIS actor.
//   - InvalidArgument    → collection.ErrSharingRejectedRequest (502). sharing
//     rejected the request WE built (unspecified scope, blank idempotency key) —
//     a creation-side defect, never the learner's fault, so never a 4xx that
//     blames them, and never an "outage" that sends ops chasing a healthy service.
//   - everything else    → collection.ErrSharingUnavailable (502). Unavailable /
//     DeadlineExceeded / Internal / Unimplemented (sharing's own notWired), plus
//     any non-status transport error. FAIL LOUD: the caller REFUSES the
//     operation. The gate must never degrade open — a conversion that proceeds
//     without minting the D2 grant is reuse nobody recorded.
//
// The upstream detail is preserved ahead of the sentinel so the server log keeps
// the full picture; the HTTP layer deliberately does NOT echo it to the caller.
func translateSharingGateErr(op string, err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		// No gRPC status at all: a dial failure, a cancelled context, a proxy
		// hanging up mid-call. The transport is down ⇒ the loud bucket. Letting
		// this fall through untranslated is exactly how an `rpc error:` string
		// reaches a learner.
		return fmt.Errorf("reuse client: %s: %v: %w", op, err, collection.ErrSharingUnavailable)
	}
	switch st.Code() {
	case codes.FailedPrecondition:
		return fmt.Errorf("reuse client: %s: %s: %w", op, st.Message(), collection.ErrAtomNotShareable)
	case codes.PermissionDenied:
		return fmt.Errorf("reuse client: %s: %s: %w", op, st.Message(), collection.ErrAtomReuseDenied)
	case codes.InvalidArgument:
		return fmt.Errorf("reuse client: %s: %s: %w", op, st.Message(), collection.ErrSharingRejectedRequest)
	default:
		return fmt.Errorf("reuse client: %s: %s (%s): %w", op, st.Message(), st.Code(), collection.ErrSharingUnavailable)
	}
}

// SharingReuseGRPCClient is the minimal slice of sharingv1.SharingClient the
// reuse gate calls. Tests inject a fake; production wires the real gRPC dial
// against SVC_SHARING_GRPC_URL (env-configured, never inline).
type SharingReuseGRPCClient interface {
	GetReuseContext(ctx context.Context, in *sharingv1.GetReuseContextRequest, opts ...grpc.CallOption) (*sharingv1.GetReuseContextResponse, error)
	AuthorizeAtomUse(ctx context.Context, in *sharingv1.AuthorizeAtomUseRequest, opts ...grpc.CallOption) (*sharingv1.AuthorizeAtomUseResponse, error)
}

// SharingReuseClientConfig carries the dial-side options.
type SharingReuseClientConfig struct {
	// GRPCClient is the pre-dialled client. When nil the adapter dials
	// GRPCAddr lazily (convenience for tests / shared conn reuse).
	GRPCClient SharingReuseGRPCClient
	// GRPCAddr is the chora-sharing gRPC address (from SVC_SHARING_GRPC_URL).
	// Used only when GRPCClient is nil.
	GRPCAddr string
	// Timeout caps each round-trip. Default 3s.
	Timeout time.Duration
}

// SharingReuseClient implements ports.ReuseContextFetcher +
// ports.AtomUseAuthorizer.
type SharingReuseClient struct {
	cfg      SharingReuseClientConfig
	fallback SharingReuseGRPCClient
}

// NewSharingReuseClient constructs the adapter. When cfg.GRPCClient is
// non-nil it is used directly; otherwise a lazy insecure dial is prepared
// (mTLS is terminated by the istio sidecar inside the mesh).
func NewSharingReuseClient(cfg SharingReuseClientConfig) *SharingReuseClient {
	c := &SharingReuseClient{cfg: cfg}
	if cfg.Timeout == 0 {
		c.cfg.Timeout = 3 * time.Second
	}
	return c
}

// client lazily dials the fallback connection on first use.
func (c *SharingReuseClient) client(_ context.Context) (SharingReuseGRPCClient, error) {
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
		return nil, fmt.Errorf("reuse client: dial %s: %w", c.cfg.GRPCAddr, err)
	}
	c.fallback = sharingv1.NewSharingClient(conn)
	return c.fallback, nil
}

// GetReuseContext fetches the caller's consent context for the PICKER /
// test-set-snapshot lanes (ports.ReuseContextFetcher). Every error propagates
// (fail loud) — including Unavailable: a sharing outage must 5xx the picker /
// refuse the snapshot, never degrade the gate.
//
// Deliberately NOT sentinel-translated: those lanes were given their own
// mappings by CHO-2133 / CHO-2139 and are not in CHO-2174's blast radius. The
// COLLECTION gates get the translation, in ConsentContext below.
func (c *SharingReuseClient) GetReuseContext(ctx context.Context, gcid, tenantID string) (ports.ReuseContext, error) {
	cl, err := c.client(ctx)
	if err != nil {
		return ports.ReuseContext{}, err
	}
	rc, err := c.getReuseContextRPC(ctx, cl, gcid, tenantID)
	if err != nil {
		return ports.ReuseContext{}, fmt.Errorf("reuse client: GetReuseContext: %w", err)
	}
	return rc, nil
}

// getReuseContextRPC issues the call under the configured timeout and returns
// the RAW upstream error — no wrap, no translation. Each public caller reports
// it in its own idiom: GetReuseContext keeps the historical string wrap;
// ConsentContext (the collection gate) translates the gRPC status into a domain
// sentinel. Keeping the error raw here is what lets ConsentContext read the
// status exactly, instead of re-parsing a wrapped string.
func (c *SharingReuseClient) getReuseContextRPC(
	ctx context.Context, cl SharingReuseGRPCClient, gcid, tenantID string,
) (ports.ReuseContext, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	resp, err := cl.GetReuseContext(ctx, &sharingv1.GetReuseContextRequest{
		Gcid:     gcid,
		TenantId: tenantID,
	})
	if err != nil {
		return ports.ReuseContext{}, err
	}
	return ports.ReuseContext{
		FriendGCIDs:    resp.GetFriendGcids(),
		GrantedAtomIDs: resp.GetGrantedAtomIds(),
	}, nil
}

// AuthorizeTestSetUse writes the ADR-229 D2 audit grant for an allowed
// non-owner snapshot: scope TEST_SET, audience-based (no source share
// entry — sharing resolves the free v1 license from the projection's
// reuse_visibility). The idempotency key is deterministic on
// (tenant, grantee, atom) so republishing the same test-set reuses the
// grant; sharing's partial-unique (grantee, atom, scope) WHERE active makes
// the write idempotent regardless.
func (c *SharingReuseClient) AuthorizeTestSetUse(ctx context.Context, tenantID, granteeGCID, atomID string) error {
	cl, err := c.client(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	_, err = cl.AuthorizeAtomUse(ctx, &sharingv1.AuthorizeAtomUseRequest{
		GranteeGcid:    granteeGCID,
		TenantId:       tenantID,
		AtomId:         atomID,
		Scope:          sharingv1.GrantScope_GRANT_SCOPE_TEST_SET,
		IdempotencyKey: fmt.Sprintf("adr229-snapshot:%s:%s:%s", tenantID, granteeGCID, atomID),
	})
	if err != nil {
		return fmt.Errorf("reuse client: AuthorizeAtomUse: %w", err)
	}
	return nil
}

// -----------------------------------------------------------------------------
// ADR-233 WS-4 — the collection gates
// -----------------------------------------------------------------------------

// ConsentContext adapts GetReuseContext into the domain's reuseconsent.Context,
// which is what the ADR-229 disjunct is actually evaluated against.
//
// ActorGCID is set from the caller — it is the value the disjunct's `own` leg
// compares each atom's author against. Forgetting it would make every atom look
// like someone else's work, and quietly turn a free reuse into a grant-minting
// one.
//
// Fails loud, exactly like GetReuseContext: an empty context would silently
// NARROW the gate, and an absent one would blow it wide open.
//
// CHO-2174: the failure is translated into a domain sentinel here (see
// translateSharingGateErr) — this is a COLLECTION-gate port, and the collection
// domain must never be handed an `rpc error:` string. This leg runs on AddAtom,
// GetVisible AND Convert, so a sharing outage on any of them now surfaces as a
// 502 refusal rather than the 400 "invalid request" it used to be.
func (c *SharingReuseClient) ConsentContext(ctx context.Context, actorGCID, tenantID string) (reuseconsent.Context, error) {
	cl, err := c.client(ctx)
	if err != nil {
		// A dial failure never reaches the RPC — still an outage, still loud.
		return reuseconsent.Context{}, translateSharingGateErr("GetReuseContext", err)
	}
	rc, err := c.getReuseContextRPC(ctx, cl, actorGCID, tenantID)
	if err != nil {
		return reuseconsent.Context{}, translateSharingGateErr("GetReuseContext", err)
	}
	return reuseconsent.Context{
		ActorGCID:      actorGCID,
		FriendGCIDs:    rc.FriendGCIDs,
		GrantedAtomIDs: rc.GrantedAtomIDs,
	}, nil
}

// AuthorizeCollectionUse writes the ADR-229 D2 audit grant for an allowed
// non-owner atom crossing into chora_consumption via convert-to-study-list:
// scope GRANT_SCOPE_COLLECTION, audience-based (no source share entry), v1
// free-license — a grant records reuse, and mints no charge.
//
// ADR-233 D10a: this is the THIRD grant-write site alongside the test-set
// snapshot and the quiz/duel arm, and it activates a GrantScope enum value that
// had been DEAD in the contract until now.
//
// The idempotency key is deterministic on (tenant, grantee, atom) and carries a
// scope-specific prefix, so re-converting the same collection reuses the grant
// while a test-set snapshot of the same atom stays a distinct audit record.
// Sharing's partial-unique (grantee, atom, scope) WHERE active makes the write
// idempotent regardless.
//
// A write failure REFUSES the conversion — the audit record is not optional.
//
// CHO-2174: the failure is translated into a domain sentinel (see
// translateSharingGateErr). This is where the live 2026-07-13 refusal came from:
// sharing has no atom_projections row for the atom, answers FailedPrecondition
// "atom not published or withdrawn (412)", and — untranslated — that whole gRPC
// string used to be handed to the learner inside a 400.
func (c *SharingReuseClient) AuthorizeCollectionUse(ctx context.Context, tenantID, granteeGCID, atomID string) error {
	cl, err := c.client(ctx)
	if err != nil {
		return translateSharingGateErr("AuthorizeAtomUse (collection)", err)
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	_, err = cl.AuthorizeAtomUse(ctx, &sharingv1.AuthorizeAtomUseRequest{
		GranteeGcid:    granteeGCID,
		TenantId:       tenantID,
		AtomId:         atomID,
		Scope:          sharingv1.GrantScope_GRANT_SCOPE_COLLECTION,
		IdempotencyKey: fmt.Sprintf("adr233-collection:%s:%s:%s", tenantID, granteeGCID, atomID),
	})
	if err != nil {
		return translateSharingGateErr("AuthorizeAtomUse (collection)", err)
	}
	return nil
}

// Compile-time interface checks.
//
// The picker/snapshot ports live in internal/ports; the collection gate ports
// are owned by the DOMAIN (internal/domain/collection) per hexagonal — the
// domain declares what it needs, adapters satisfy it.
var (
	_ ports.ReuseContextFetcher = (*SharingReuseClient)(nil)
	_ ports.AtomUseAuthorizer   = (*SharingReuseClient)(nil)

	_ collection.ConsentContextFetcher   = (*SharingReuseClient)(nil)
	_ collection.CollectionUseAuthorizer = (*SharingReuseClient)(nil)
)
