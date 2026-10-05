// Package clients — Mana ledger gRPC client (P4-A).
//
// ManaClient implements ports.ManaLedger by wrapping chora-identity's
// `ManaService` gRPC contract (chora-contracts/proto/services/identity/v1/
// mana_service.proto). The client lives next to QGenEngineClient + AIKO
// client so the chora-creation composition root has a single import path
// for outbound dependencies.
//
// 402 envelope (design §3.3): when DeductMana returns success=false the
// adapter wraps the upstream upsell context in a typed
// *InsufficientManaError and (optionally) eagerly creates a Stripe
// Checkout session for top-up via the StripeCheckoutCreator port. Stripe
// failures fail-soft — the URL field stays nil + the FE falls back to
// POST /api/v1/me/mana/topup. The handler maps this error to 402 with
// the canonical `InsufficientManaUpsell` envelope.
//
// Refund (design §3.4) — on LLM failure after a successful debit the
// orchestrator calls Refund. Idempotent on IdempotencyKey ({request_id}-
// refund) so retries don't double-credit.
//
// Per `feedback_no_inline_config`:
//   - SVC_IDENTITY_GRPC_URL (default chora-identity:8081 in the mesh)
//   - STRIPE_MANA_TOPUP_BASIC_PRICE_CENTS / STANDARD / PREMIUM and
//     STRIPE_MANA_TOPUP_BASIC_UNITS / STANDARD / PREMIUM all sourced
//     from env when constructing the topup recommendation. Defaults
//     keep the path safe when env is empty.
package clients

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/grpc"

	identityv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/identity/v1"

	"github.com/apollo-chora/chora-creation/internal/ports"
)

// ManaServiceGRPCClient is the minimal slice of identityv1.ManaServiceClient
// the adapter calls. The generated identityv1.ManaServiceClient interface
// already satisfies this. Tests inject a fake; production wires the real
// gRPC dial against SVC_IDENTITY_GRPC_URL via grpc.NewClient.
type ManaServiceGRPCClient interface {
	DeductMana(ctx context.Context, in *identityv1.DeductManaRequest, opts ...grpc.CallOption) (*identityv1.DeductManaResponse, error)
	CreditMana(ctx context.Context, in *identityv1.CreditManaRequest, opts ...grpc.CallOption) (*identityv1.CreditManaResponse, error)
	GetBalance(ctx context.Context, in *identityv1.GetBalanceRequest, opts ...grpc.CallOption) (*identityv1.GetBalanceResponse, error)
}

// ManaTopupCheckoutInput is the payload to StripeCheckoutCreator.
type ManaTopupCheckoutInput struct {
	TenantID      string
	PurchaserGCID string
	PlanCode      string // e.g. "MANA_TOPUP_BASIC"
	AmountCents   int64
	Currency      string
	Units         int
}

// StripeCheckoutCreator is the eager-creation port for the 402 upsell.
// When wired (BE-eager mode per design §3.3 default), the ManaClient
// pre-creates a Stripe Checkout Session so the FE can deep-link the user
// straight to payment. When nil OR failing, the URL stays null + FE
// drops back to POST /api/v1/me/mana/topup.
type StripeCheckoutCreator interface {
	CreateManaTopupCheckout(ctx context.Context, in ManaTopupCheckoutInput) (string, error)
}

// ManaClientConfig bundles the deps. Tests inject a stub gRPC + Stripe
// pair; production wires the real identity dial + a Stripe client adapter.
type ManaClientConfig struct {
	GRPCClient     ManaServiceGRPCClient
	StripeCheckout StripeCheckoutCreator

	// RecommendedTopupUnits when set overrides the default plan ladder.
	RecommendedTopupUnits int
	// RecommendedPlanCode when set overrides the default basic plan.
	RecommendedPlanCode string
	// RecommendedAmountCents when set overrides the basic-plan dollar amount.
	RecommendedAmountCents int64
	// Currency overrides the default "usd".
	Currency string
}

// ManaClient is the gRPC adapter satisfying ports.ManaLedger.
type ManaClient struct {
	cfg ManaClientConfig
}

// NewManaClient constructs the client. Zero-value config is allowed; the
// Deduct path returns ErrNotConfigured (loud-and-clear failure mode) when
// GRPCClient is nil.
func NewManaClient(cfg ManaClientConfig) *ManaClient {
	if cfg.RecommendedTopupUnits == 0 {
		cfg.RecommendedTopupUnits = 100 // MANA_TOPUP_BASIC default
	}
	if cfg.RecommendedPlanCode == "" {
		cfg.RecommendedPlanCode = "MANA_TOPUP_BASIC"
	}
	if cfg.RecommendedAmountCents == 0 {
		cfg.RecommendedAmountCents = 499 // $4.99 default for BASIC tier
	}
	if cfg.Currency == "" {
		cfg.Currency = "usd"
	}
	return &ManaClient{cfg: cfg}
}

// Compile-time check.
var _ ports.ManaLedger = (*ManaClient)(nil)

// ErrManaClientNotConfigured is returned when the gRPC client is nil. The
// composition root MUST wire SVC_IDENTITY_GRPC_URL — empty env is a boot-
// time failure per `feedback_no_inline_config` + `feedback_no_stubs_real_wiring`.
var ErrManaClientNotConfigured = errors.New("mana client: gRPC client not configured")

// InsufficientManaError is the typed sentinel returned when DeductMana
// reports success=false. The handler unwraps via `errors.As` and renders
// the canonical 402 envelope.
type InsufficientManaError struct {
	RequiredUnits         int64
	CurrentBalanceUnits   int64
	RecommendedTopupUnits int
	RecommendedPlanCode   string
	StripeCheckoutURL     *string // nil when no stripe wired OR stripe failed (fail-soft)
}

// Error satisfies the error interface.
func (e *InsufficientManaError) Error() string {
	return fmt.Sprintf("insufficient mana: required=%d current=%d", e.RequiredUnits, e.CurrentBalanceUnits)
}

// -----------------------------------------------------------------------------
// Deduct — atomic debit + 402 upsell composition
// -----------------------------------------------------------------------------

// Deduct implements ports.ManaLedger.Deduct.
func (c *ManaClient) Deduct(ctx context.Context, req ports.DeductManaReq) (ports.DeductManaResp, error) {
	if c.cfg.GRPCClient == nil {
		return ports.DeductManaResp{}, ErrManaClientNotConfigured
	}
	resp, err := c.cfg.GRPCClient.DeductMana(ctx, &identityv1.DeductManaRequest{
		Gcid:           req.GCID,
		ActionCode:     req.ActionCode,
		Units:          int64(req.Units),
		IdempotencyKey: req.IdempotencyKey,
		RequestId:      req.RequestID,
		TenantId:       req.TenantID,
		Context:        req.Context,
	})
	if err != nil {
		return ports.DeductManaResp{}, fmt.Errorf("mana client: DeductMana rpc: %w", err)
	}
	if !resp.Success {
		// Build the canonical InsufficientMana envelope. The Stripe URL is
		// eagerly populated (BE-eager mode) iff a Stripe checkout creator
		// is wired AND succeeds; fail-soft.
		out := &InsufficientManaError{
			RequiredUnits:         resp.RequiredUnits,
			CurrentBalanceUnits:   resp.CurrentBalanceUnits,
			RecommendedTopupUnits: c.cfg.RecommendedTopupUnits,
			RecommendedPlanCode:   c.cfg.RecommendedPlanCode,
		}
		if c.cfg.StripeCheckout != nil {
			url, sErr := c.cfg.StripeCheckout.CreateManaTopupCheckout(ctx, ManaTopupCheckoutInput{
				TenantID:      req.TenantID,
				PurchaserGCID: req.GCID,
				PlanCode:      c.cfg.RecommendedPlanCode,
				AmountCents:   c.cfg.RecommendedAmountCents,
				Currency:      c.cfg.Currency,
				Units:         c.cfg.RecommendedTopupUnits,
			})
			if sErr == nil && url != "" {
				u := url
				out.StripeCheckoutURL = &u
			}
			// Stripe failure is intentionally swallowed — the FE can still
			// top up via POST /api/v1/me/mana/topup. Log via the caller's
			// trace context if needed.
		}
		return ports.DeductManaResp{
			Success:             false,
			RequiredUnits:       resp.RequiredUnits,
			CurrentBalanceUnits: resp.CurrentBalanceUnits,
		}, out
	}

	out := ports.DeductManaResp{
		Success:         true,
		NewBalanceUnits: resp.BalanceAfterUnits,
	}
	// ChargedUnits = the server-resolved amount actually debited (ADR-178 /
	// FU-4(b)). Sum the ledger rows (a debit may span subsidy + personal
	// slices). On a units==0 resolve this is the price the caller must stamp +
	// refund; on an explicit Units>0 debit it equals req.Units.
	for _, e := range resp.Entries {
		out.ChargedUnits += e.Units
	}
	if out.ChargedUnits == 0 {
		// Free action (resolved cost 0 → no ledger rows) or explicit debit with
		// no entries echoed: fall back to the caller's quote.
		out.ChargedUnits = int64(req.Units)
	}
	if len(resp.Entries) > 0 {
		out.LedgerEntryID = resp.Entries[0].EntryId
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// Refund — idempotent CreditMana with source=REFUND
// -----------------------------------------------------------------------------

// Refund implements ports.ManaLedger.Refund.
func (c *ManaClient) Refund(ctx context.Context, req ports.RefundManaReq) (ports.RefundManaResp, error) {
	if c.cfg.GRPCClient == nil {
		return ports.RefundManaResp{}, ErrManaClientNotConfigured
	}
	resp, err := c.cfg.GRPCClient.CreditMana(ctx, &identityv1.CreditManaRequest{
		Gcid:           req.GCID,
		Source:         identityv1.ManaSource_MANA_SOURCE_REFUND,
		Units:          int64(req.Units),
		Reason:         identityv1.ManaReasonCode_MANA_REASON_CODE_REFUND,
		IdempotencyKey: req.IdempotencyKey,
		TenantId:       req.TenantID,
		ReasonText:     req.Reason,
	})
	if err != nil {
		return ports.RefundManaResp{}, fmt.Errorf("mana client: CreditMana rpc: %w", err)
	}
	out := ports.RefundManaResp{
		Success:         true,
		NewBalanceUnits: resp.BalanceAfterUnits,
	}
	if resp.Entry != nil {
		out.LedgerEntryID = resp.Entry.EntryId
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// GetBalance — peek
// -----------------------------------------------------------------------------

// GetBalance implements ports.ManaLedger.GetBalance.
func (c *ManaClient) GetBalance(ctx context.Context, gcid string) (ports.BalanceResp, error) {
	if c.cfg.GRPCClient == nil {
		return ports.BalanceResp{}, ErrManaClientNotConfigured
	}
	resp, err := c.cfg.GRPCClient.GetBalance(ctx, &identityv1.GetBalanceRequest{Gcid: gcid})
	if err != nil {
		return ports.BalanceResp{}, fmt.Errorf("mana client: GetBalance rpc: %w", err)
	}
	return ports.BalanceResp{
		GCID:                gcid,
		CurrentBalanceUnits: resp.BalanceUnits,
	}, nil
}
