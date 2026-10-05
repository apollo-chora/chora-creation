// display_name_client_test.go — tests for the chora-identity display-name
// resolver (bufconn-backed fake IdentityServer; no live identity service).
//
// ResolveDisplayName is best-effort: any RPC failure degrades to "" with a
// wrapped error, and an empty GCID short-circuits without an RPC.
package clients_test

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	identityv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/identity/v1"

	"github.com/apollo-chora/chora-creation/internal/adapter/clients"
)

type fakeIdentityServer struct {
	identityv1.UnimplementedIdentityServer

	resp *identityv1.GetMeResponse
	err  error
	req  *identityv1.GetMeRequest
}

func (f *fakeIdentityServer) GetMe(_ context.Context, in *identityv1.GetMeRequest) (*identityv1.GetMeResponse, error) {
	f.req = in
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// dialFakeIdentity wires a bufconn gRPC server hosting the fake identity
// service and returns a client conn plus the fake for assertions.
func dialFakeIdentity(t *testing.T, srv identityv1.IdentityServer) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	s := grpc.NewServer()
	identityv1.RegisterIdentityServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(func() {
		s.Stop()
		_ = lis.Close()
	})
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestDisplayNameClient_ResolveDisplayName_HappyPath(t *testing.T) {
	t.Parallel()
	fake := &fakeIdentityServer{resp: &identityv1.GetMeResponse{
		Me: &identityv1.Me{DisplayName: "Ada Lovelace"},
	}}
	c := clients.NewIdentityDisplayNameClient(dialFakeIdentity(t, fake))

	name, err := c.ResolveDisplayName(context.Background(), "gcid-1")
	if err != nil {
		t.Fatalf("ResolveDisplayName: %v", err)
	}
	if name != "Ada Lovelace" {
		t.Errorf("name = %q; want %q", name, "Ada Lovelace")
	}
	if fake.req.GetGcid() != "gcid-1" {
		t.Errorf("wire gcid = %q; want gcid-1", fake.req.GetGcid())
	}
}

func TestDisplayNameClient_ResolveDisplayName_EmptyGCIDShortCircuits(t *testing.T) {
	t.Parallel()
	fake := &fakeIdentityServer{}
	c := clients.NewIdentityDisplayNameClient(dialFakeIdentity(t, fake))

	name, err := c.ResolveDisplayName(context.Background(), "")
	if err != nil || name != "" {
		t.Errorf("ResolveDisplayName(\"\") = %q, %v; want \"\", nil", name, err)
	}
	if fake.req != nil {
		t.Errorf("empty GCID must not issue an RPC, got req = %v", fake.req)
	}
}

func TestDisplayNameClient_ResolveDisplayName_NilMe(t *testing.T) {
	t.Parallel()
	fake := &fakeIdentityServer{resp: &identityv1.GetMeResponse{}}
	c := clients.NewIdentityDisplayNameClient(dialFakeIdentity(t, fake))

	name, err := c.ResolveDisplayName(context.Background(), "gcid-1")
	if err != nil || name != "" {
		t.Errorf("ResolveDisplayName with nil Me = %q, %v; want \"\", nil", name, err)
	}
}

func TestDisplayNameClient_ResolveDisplayName_RPCErrorDegrades(t *testing.T) {
	t.Parallel()
	fake := &fakeIdentityServer{err: context.DeadlineExceeded}
	c := clients.NewIdentityDisplayNameClient(dialFakeIdentity(t, fake))

	name, err := c.ResolveDisplayName(context.Background(), "gcid-1")
	if err == nil {
		t.Fatalf("ResolveDisplayName: want wrapped error on RPC failure, got nil")
	}
	if name != "" {
		t.Errorf("name = %q; want \"\" on error", name)
	}
}
