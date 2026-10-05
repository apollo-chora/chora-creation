// ManaLedger — port to chora-identity's ManaService.
//
// The mana economy is owned by chora-identity (per ADR-142 — per-user
// Familiar mana, NOT TenantManaPool for AI authoring). chora-creation is a
// CALLER not an owner — it debits via gRPC pre-LLM-call, and refunds on
// failure via idempotent CreditMana (mirrors the chora-model-broker-gateway
// refund pattern; design §3.4).
//
// The adapter wraps the chora-identity gRPC `ManaService` declared in
// `chora-contracts/proto/services/identity/v1/mana_service.proto`.
package ports

import (
	"context"
)

// ManaLedger is the hexagonal port for per-user mana economy operations.
type ManaLedger interface {
	// Deduct atomically debits the user's mana ledger. Returns the resulting
	// balance + the ledger entry id. Returns question.ErrInsufficientMana
	// (wrapped through the adapter) when the deduction would underflow —
	// the handler then writes the canonical InsufficientManaUpsell envelope.
	Deduct(ctx context.Context, req DeductManaReq) (DeductManaResp, error)

	// Refund credits mana back to the user — used when the AI call fails
	// after a successful debit (Vertex AI 5xx, guardrail refusal at output,
	// etc.). Idempotent by IdempotencyKey so retries are safe.
	Refund(ctx context.Context, req RefundManaReq) (RefundManaResp, error)

	// GetBalance peeks at the user's current mana balance without deducting.
	// Used at 402-construction time to compute the recommended top-up amount.
	GetBalance(ctx context.Context, gcid string) (BalanceResp, error)
}

// DeductManaReq is the input to ManaLedger.Deduct.
type DeductManaReq struct {
	GCID           string
	TenantID       string
	ActionCode     string // e.g. "question_authoring_ai_draft"
	Units          int    // mana cost; 0 ⇒ server resolves via the price-plan layer
	IdempotencyKey string // unique per logical operation — retry-safe
	RequestID      string // for trace correlation
	TraceParent    string
	TraceState     string
	// Context is the ADR-178 price-plan resolution context (FU-4(b)). The only
	// load-bearing key is "item_count" — for a per_item action (batch per-item)
	// the resolved per-unit cost is multiplied by it server-side.
	Context map[string]string
}

// DeductManaResp is the response from ManaLedger.Deduct.
type DeductManaResp struct {
	Success             bool
	LedgerEntryID       string
	NewBalanceUnits     int64
	RequiredUnits       int64 // populated on insufficient-balance failure
	CurrentBalanceUnits int64 // populated on insufficient-balance failure
	// ChargedUnits is the amount actually debited — the server-resolved price
	// (ADR-178 price-plan layer) when the request used Units==0, including the
	// per_item × item_count multiply. The caller stamps it on the job + event
	// and refunds exactly this much on failure (it no longer hard-codes the
	// price). Echoes the request Units on an explicit Units>0 debit.
	ChargedUnits int64
}

// RefundManaReq is the input to ManaLedger.Refund.
type RefundManaReq struct {
	GCID           string
	TenantID       string
	ActionCode     string // mirrors the original Deduct's action_code
	Units          int    // mana to credit back (typically = original debit)
	IdempotencyKey string // "{request_id}-refund" suffix per design §3.4
	RequestID      string
	TraceParent    string
	TraceState     string
	Reason         string // free-text for the audit trail
}

// RefundManaResp is the response from ManaLedger.Refund.
type RefundManaResp struct {
	Success         bool
	LedgerEntryID   string
	NewBalanceUnits int64
}

// BalanceResp is the response from ManaLedger.GetBalance.
type BalanceResp struct {
	GCID                string
	CurrentBalanceUnits int64
}
