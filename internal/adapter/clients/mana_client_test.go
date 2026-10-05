// mana_client_test.go — TDD coverage for the ManaClient gRPC adapter (P4).
//
// The ManaClient wraps chora-identity ManaService (chora-contracts/proto/
// services/identity/v1/mana_service.proto) and implements
// ports.ManaLedger. On insufficient balance it returns a typed
// *InsufficientManaError so the handler can render the canonical 402
// InsufficientManaUpsell envelope (design §3.3 +
// /Users/daleleung/.claude/plans/golden-hopping-owl.md P4-A).
//
// Tests inject a fake ManaServiceClient (the gRPC interface generated in
// chora-contracts/gen/go/chora/services/identity/v1) so we avoid the
// network entirely. The same fakes also cover the Stripe checkout-session
// adjunct used to populate `stripe_checkout_url` on 402 (BE-eager mode);
// when Stripe is unset OR fails, the URL is null and the FE handles the
// fallback via POST /api/v1/me/mana/topup.
package clients_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc"

	identityv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/identity/v1"

	"github.com/apollo-chora/chora-creation/internal/adapter/clients"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// -----------------------------------------------------------------------------
// Fakes
// -----------------------------------------------------------------------------

// fakeManaGRPCClient implements identityv1.ManaServiceClient so the
// ManaClient can run without a real gRPC dial.
type fakeManaGRPCClient struct {
	gotDeduct   *identityv1.DeductManaRequest
	gotCredit   *identityv1.CreditManaRequest
	gotBalance  *identityv1.GetBalanceRequest
	deductResp  *identityv1.DeductManaResponse
	deductErr   error
	creditResp  *identityv1.CreditManaResponse
	creditErr   error
	balanceResp *identityv1.GetBalanceResponse
	balanceErr  error
}

func (f *fakeManaGRPCClient) DeductMana(_ context.Context, in *identityv1.DeductManaRequest, _ ...grpc.CallOption) (*identityv1.DeductManaResponse, error) {
	f.gotDeduct = in
	return f.deductResp, f.deductErr
}
func (f *fakeManaGRPCClient) CreditMana(_ context.Context, in *identityv1.CreditManaRequest, _ ...grpc.CallOption) (*identityv1.CreditManaResponse, error) {
	f.gotCredit = in
	return f.creditResp, f.creditErr
}
func (f *fakeManaGRPCClient) GetBalance(_ context.Context, in *identityv1.GetBalanceRequest, _ ...grpc.CallOption) (*identityv1.GetBalanceResponse, error) {
	f.gotBalance = in
	return f.balanceResp, f.balanceErr
}

// fakeStripeCheckout implements clients.StripeCheckoutCreator with a
// deterministic URL emission. The `fail` toggle simulates a Stripe 5xx —
// the ManaClient MUST swallow that + return a nil StripeCheckoutURL.
type fakeStripeCheckout struct {
	called   bool
	gotInput clients.ManaTopupCheckoutInput
	url      string
	fail     bool
}

func (f *fakeStripeCheckout) CreateManaTopupCheckout(_ context.Context, in clients.ManaTopupCheckoutInput) (string, error) {
	f.called = true
	f.gotInput = in
	if f.fail {
		return "", errors.New("stripe 5xx")
	}
	return f.url, nil
}

// -----------------------------------------------------------------------------
// Deduct — happy path
// -----------------------------------------------------------------------------

func TestManaClient_Deduct_Success_DebitsAndReturnsReceipt(t *testing.T) {
	g := &fakeManaGRPCClient{
		deductResp: &identityv1.DeductManaResponse{
			Success:           true,
			BalanceAfterUnits: 95,
			Entries: []*identityv1.LedgerEntry{
				{EntryId: "entry-1", Units: 5, BalanceAfterUnits: 95},
			},
		},
	}
	c := clients.NewManaClient(clients.ManaClientConfig{GRPCClient: g})

	resp, err := c.Deduct(context.Background(), ports.DeductManaReq{
		GCID:           "gcid-phyllis",
		TenantID:       "tenant-x",
		ActionCode:     "question_authoring_model_answer",
		Units:          5,
		IdempotencyKey: "key-1",
		RequestID:      "req-1",
	})
	if err != nil {
		t.Fatalf("Deduct: %v", err)
	}
	if !resp.Success {
		t.Error("Success = false; want true")
	}
	if resp.NewBalanceUnits != 95 {
		t.Errorf("NewBalanceUnits = %d; want 95", resp.NewBalanceUnits)
	}
	if resp.LedgerEntryID != "entry-1" {
		t.Errorf("LedgerEntryID = %q; want entry-1", resp.LedgerEntryID)
	}
	if g.gotDeduct == nil {
		t.Fatal("DeductMana not called")
	}
	if g.gotDeduct.ActionCode != "question_authoring_model_answer" {
		t.Errorf("ActionCode wire = %q", g.gotDeduct.ActionCode)
	}
	if g.gotDeduct.Units != 5 {
		t.Errorf("Units wire = %d; want 5", g.gotDeduct.Units)
	}
	if g.gotDeduct.IdempotencyKey != "key-1" {
		t.Errorf("IdempotencyKey wire = %q", g.gotDeduct.IdempotencyKey)
	}
}

// -----------------------------------------------------------------------------
// Deduct — insufficient mana — typed error + Stripe URL
// -----------------------------------------------------------------------------

func TestManaClient_Deduct_Insufficient_ReturnsTypedError_WithStripeURL(t *testing.T) {
	g := &fakeManaGRPCClient{
		deductResp: &identityv1.DeductManaResponse{
			Success:             false,
			RequiredUnits:       10,
			CurrentBalanceUnits: 3,
		},
	}
	stripeStub := &fakeStripeCheckout{url: "https://checkout.stripe.com/c/pay/cs_test_abc"}

	c := clients.NewManaClient(clients.ManaClientConfig{
		GRPCClient:     g,
		StripeCheckout: stripeStub,
	})

	_, err := c.Deduct(context.Background(), ports.DeductManaReq{
		GCID:           "gcid-phyllis",
		TenantID:       "tenant-x",
		ActionCode:     "question_authoring_ai_draft",
		Units:          10,
		IdempotencyKey: "key-1",
	})
	if err == nil {
		t.Fatal("expected *InsufficientManaError, got nil")
	}
	var iErr *clients.InsufficientManaError
	if !errors.As(err, &iErr) {
		t.Fatalf("expected *InsufficientManaError; got %T %v", err, err)
	}
	if iErr.RequiredUnits != 10 {
		t.Errorf("RequiredUnits = %d; want 10", iErr.RequiredUnits)
	}
	if iErr.CurrentBalanceUnits != 3 {
		t.Errorf("CurrentBalanceUnits = %d; want 3", iErr.CurrentBalanceUnits)
	}
	if iErr.RecommendedTopupUnits <= 0 {
		t.Errorf("RecommendedTopupUnits = %d; expected > 0", iErr.RecommendedTopupUnits)
	}
	if iErr.RecommendedPlanCode == "" {
		t.Error("RecommendedPlanCode empty; expected non-empty (e.g. MANA_TOPUP_BASIC)")
	}
	if iErr.StripeCheckoutURL == nil {
		t.Fatal("StripeCheckoutURL nil; expected populated by stripe stub")
	}
	if *iErr.StripeCheckoutURL != "https://checkout.stripe.com/c/pay/cs_test_abc" {
		t.Errorf("StripeCheckoutURL = %q; unexpected", *iErr.StripeCheckoutURL)
	}
	if !stripeStub.called {
		t.Error("Stripe checkout stub not called; expected eager creation on 402 path")
	}
	if stripeStub.gotInput.PurchaserGCID != "gcid-phyllis" {
		t.Errorf("Stripe input PurchaserGCID = %q; want gcid-phyllis", stripeStub.gotInput.PurchaserGCID)
	}
}

func TestManaClient_Deduct_Insufficient_StripeFails_StripeURLNullable(t *testing.T) {
	g := &fakeManaGRPCClient{
		deductResp: &identityv1.DeductManaResponse{
			Success:             false,
			RequiredUnits:       10,
			CurrentBalanceUnits: 0,
		},
	}
	stripeStub := &fakeStripeCheckout{fail: true}

	c := clients.NewManaClient(clients.ManaClientConfig{
		GRPCClient:     g,
		StripeCheckout: stripeStub,
	})

	_, err := c.Deduct(context.Background(), ports.DeductManaReq{
		GCID: "gcid-z", TenantID: "tenant-x", ActionCode: "question_authoring_ai_draft",
		Units: 10, IdempotencyKey: "key-2",
	})
	var iErr *clients.InsufficientManaError
	if !errors.As(err, &iErr) {
		t.Fatalf("expected *InsufficientManaError; got %v", err)
	}
	if iErr.StripeCheckoutURL != nil {
		t.Errorf("StripeCheckoutURL = %v; want nil on stripe failure (fail-soft per design §3.3)", iErr.StripeCheckoutURL)
	}
}

func TestManaClient_Deduct_Insufficient_NoStripeWired_ReturnsNullURL(t *testing.T) {
	g := &fakeManaGRPCClient{
		deductResp: &identityv1.DeductManaResponse{
			Success:             false,
			RequiredUnits:       50,
			CurrentBalanceUnits: 0,
		},
	}
	// No StripeCheckout wired.
	c := clients.NewManaClient(clients.ManaClientConfig{GRPCClient: g})

	_, err := c.Deduct(context.Background(), ports.DeductManaReq{
		GCID: "gcid", TenantID: "tenant", ActionCode: "question_authoring_batch_parse",
		Units: 50, IdempotencyKey: "k",
	})
	var iErr *clients.InsufficientManaError
	if !errors.As(err, &iErr) {
		t.Fatalf("expected *InsufficientManaError; got %v", err)
	}
	if iErr.StripeCheckoutURL != nil {
		t.Errorf("StripeCheckoutURL = %v; want nil when no stripe wired", iErr.StripeCheckoutURL)
	}
}

// -----------------------------------------------------------------------------
// Deduct — gRPC errors propagate
// -----------------------------------------------------------------------------

func TestManaClient_Deduct_GRPCErrorPropagates(t *testing.T) {
	g := &fakeManaGRPCClient{deductErr: errors.New("rpc: deadline exceeded")}
	c := clients.NewManaClient(clients.ManaClientConfig{GRPCClient: g})

	_, err := c.Deduct(context.Background(), ports.DeductManaReq{
		GCID: "g", TenantID: "t", ActionCode: "a", Units: 5, IdempotencyKey: "k",
	})
	if err == nil || errors.Is(err, &clients.InsufficientManaError{}) {
		t.Fatalf("expected raw gRPC error; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Refund — idempotent CreditMana
// -----------------------------------------------------------------------------

func TestManaClient_Refund_Idempotent(t *testing.T) {
	g := &fakeManaGRPCClient{
		creditResp: &identityv1.CreditManaResponse{
			Entry:             &identityv1.LedgerEntry{EntryId: "credit-1", Units: 10},
			BalanceAfterUnits: 110,
		},
	}
	c := clients.NewManaClient(clients.ManaClientConfig{GRPCClient: g})

	resp, err := c.Refund(context.Background(), ports.RefundManaReq{
		GCID:           "gcid",
		TenantID:       "tenant",
		ActionCode:     "question_authoring_ai_draft",
		Units:          10,
		IdempotencyKey: "req-1-refund",
		Reason:         "Vertex AI 5xx",
	})
	if err != nil {
		t.Fatalf("Refund: %v", err)
	}
	if !resp.Success {
		t.Error("Success = false; want true")
	}
	if resp.LedgerEntryID != "credit-1" {
		t.Errorf("LedgerEntryID = %q; want credit-1", resp.LedgerEntryID)
	}
	if resp.NewBalanceUnits != 110 {
		t.Errorf("NewBalanceUnits = %d; want 110", resp.NewBalanceUnits)
	}
	if g.gotCredit == nil {
		t.Fatal("CreditMana not called")
	}
	if g.gotCredit.Source != identityv1.ManaSource_MANA_SOURCE_REFUND {
		t.Errorf("Source = %v; want MANA_SOURCE_REFUND", g.gotCredit.Source)
	}
	if g.gotCredit.IdempotencyKey != "req-1-refund" {
		t.Errorf("IdempotencyKey wire = %q", g.gotCredit.IdempotencyKey)
	}
}

// -----------------------------------------------------------------------------
// GetBalance — peek without debit
// -----------------------------------------------------------------------------

func TestManaClient_GetBalance_Peek(t *testing.T) {
	g := &fakeManaGRPCClient{
		balanceResp: &identityv1.GetBalanceResponse{BalanceUnits: 42},
	}
	c := clients.NewManaClient(clients.ManaClientConfig{GRPCClient: g})

	resp, err := c.GetBalance(context.Background(), "gcid-phyllis")
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if resp.CurrentBalanceUnits != 42 {
		t.Errorf("CurrentBalanceUnits = %d; want 42", resp.CurrentBalanceUnits)
	}
	if g.gotBalance.Gcid != "gcid-phyllis" {
		t.Errorf("Gcid wire = %q", g.gotBalance.Gcid)
	}
}

// -----------------------------------------------------------------------------
// Validation guards
// -----------------------------------------------------------------------------

func TestManaClient_NewManaClient_RejectsEmptyConfig(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("NewManaClient should not panic on empty config; got %v", r)
		}
	}()
	c := clients.NewManaClient(clients.ManaClientConfig{})
	_, err := c.Deduct(context.Background(), ports.DeductManaReq{
		GCID: "g", TenantID: "t", ActionCode: "a", Units: 1, IdempotencyKey: "k",
	})
	if err == nil {
		t.Error("expected error when gRPCClient is nil")
	}
}

// -----------------------------------------------------------------------------
// Refund / GetBalance — error branches + typed-error message
// -----------------------------------------------------------------------------

func TestManaClient_Refund_NotConfigured(t *testing.T) {
	c := clients.NewManaClient(clients.ManaClientConfig{})
	_, err := c.Refund(context.Background(), ports.RefundManaReq{
		GCID: "g", TenantID: "t", ActionCode: "a", Units: 1, IdempotencyKey: "k",
	})
	if err == nil {
		t.Error("expected error when gRPCClient is nil")
	}
}

func TestManaClient_Refund_GRPCErrorPropagates(t *testing.T) {
	g := &fakeManaGRPCClient{creditErr: errors.New("identity down")}
	c := clients.NewManaClient(clients.ManaClientConfig{GRPCClient: g})

	_, err := c.Refund(context.Background(), ports.RefundManaReq{
		GCID: "g", TenantID: "t", ActionCode: "a", Units: 1, IdempotencyKey: "k",
	})
	if err == nil {
		t.Error("expected error on CreditMana RPC failure")
	}
}

func TestManaClient_Refund_NilLedgerEntry(t *testing.T) {
	g := &fakeManaGRPCClient{creditResp: &identityv1.CreditManaResponse{BalanceAfterUnits: 55}}
	c := clients.NewManaClient(clients.ManaClientConfig{GRPCClient: g})

	resp, err := c.Refund(context.Background(), ports.RefundManaReq{
		GCID: "g", TenantID: "t", ActionCode: "a", Units: 5, IdempotencyKey: "k",
	})
	if err != nil {
		t.Fatalf("Refund: %v", err)
	}
	if resp.LedgerEntryID != "" {
		t.Errorf("LedgerEntryID = %q; want empty when entry is nil", resp.LedgerEntryID)
	}
	if resp.NewBalanceUnits != 55 {
		t.Errorf("NewBalanceUnits = %d; want 55", resp.NewBalanceUnits)
	}
}

func TestManaClient_GetBalance_NotConfigured(t *testing.T) {
	c := clients.NewManaClient(clients.ManaClientConfig{})
	if _, err := c.GetBalance(context.Background(), "g"); err == nil {
		t.Error("expected error when gRPCClient is nil")
	}
}

func TestManaClient_GetBalance_GRPCErrorPropagates(t *testing.T) {
	g := &fakeManaGRPCClient{balanceErr: errors.New("identity down")}
	c := clients.NewManaClient(clients.ManaClientConfig{GRPCClient: g})

	if _, err := c.GetBalance(context.Background(), "g"); err == nil {
		t.Error("expected error on GetBalance RPC failure")
	}
}

func TestInsufficientManaError_Error(t *testing.T) {
	e := &clients.InsufficientManaError{RequiredUnits: 10, CurrentBalanceUnits: 3}
	got := e.Error()
	if got == "" {
		t.Fatal("Error() must not be empty")
	}
	for _, want := range []string{"10", "3"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q; want it to contain %q", got, want)
		}
	}
}
