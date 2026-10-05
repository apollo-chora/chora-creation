// runtime_userroles_test.go — ADR-191 O3 layer-1 prerequisite.
//
// The chora_creation restrictive `exam_content_embargo` RLS policy matches on
// `current_setting('chora.user_roles', ...)`. That GUC is only populated if the
// tenant-tx seam emits `SET LOCAL chora.user_roles` from the caller's roles on
// ctx. These DB-free unit tests pin the SQL the seam emits (and its validation)
// so the propagation cannot silently regress to a no-op.
package pg

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-common/tracing"
)

func TestUserRolesSetLocalStmt_EmitsWhenRolesPresent(t *testing.T) {
	ctx := tracing.WithUserRoles(context.Background(), "proctor")
	stmt, emit, err := userRolesSetLocalStmt(ctx)
	if err != nil {
		t.Fatalf("err = %v; want nil", err)
	}
	if !emit {
		t.Fatalf("emit = false; want true when roles present")
	}
	if want := "SET LOCAL chora.user_roles = 'proctor'"; stmt != want {
		t.Errorf("stmt = %q; want %q", stmt, want)
	}
}

func TestUserRolesSetLocalStmt_MultiRolePrincipal(t *testing.T) {
	// The ADR-191 O3 driver: a multi-role principal like INSTRUCTOR+PROCTOR must
	// propagate the WHOLE set so the restrictive policy can see `proctor`.
	ctx := tracing.WithUserRoles(context.Background(), "instructor,proctor")
	stmt, emit, err := userRolesSetLocalStmt(ctx)
	if err != nil || !emit {
		t.Fatalf("emit=%v err=%v; want emit=true err=nil", emit, err)
	}
	if want := "SET LOCAL chora.user_roles = 'instructor,proctor'"; stmt != want {
		t.Errorf("stmt = %q; want %q", stmt, want)
	}
}

func TestUserRolesSetLocalStmt_NoRoles_NoEmit(t *testing.T) {
	stmt, emit, err := userRolesSetLocalStmt(context.Background())
	if err != nil {
		t.Fatalf("err = %v; want nil", err)
	}
	if emit || stmt != "" {
		t.Errorf("emit=%v stmt=%q; want emit=false stmt=\"\" (backward-compatible: no roles = GUC unset)", emit, stmt)
	}
}

func TestUserRolesSetLocalStmt_RejectsUnsafeRoles(t *testing.T) {
	// Defence-in-depth: SET LOCAL is parsed by Postgres (not the prepared-stmt
	// path), so an injection-shaped role string must fail loud, never reach the
	// wire.
	for _, bad := range []string{"proctor';DROP TABLE learning_atoms;--", "proctor role", "prÖctor", "a\nb"} {
		ctx := tracing.WithUserRoles(context.Background(), bad)
		if _, _, err := userRolesSetLocalStmt(ctx); err == nil {
			t.Errorf("userRolesSetLocalStmt(%q) err = nil; want rejection", bad)
		}
	}
}
