package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/adapter/pg"
	"github.com/apollo-chora/chora-creation/internal/domain/topic"
)

// -----------------------------------------------------------------------------
// Scriptable tenant-tx stub: queues QueryRow / Query / Exec results in order so
// the multi-step Move / SoftDelete / AttachAtom flows can be unit-tested without
// a live DB. Reuses stubRow / stubRows / assign from atom_repository_test.go
// (same pg_test package).
// -----------------------------------------------------------------------------

type rowsOrErr struct {
	rows pg.Rows
	err  error
}

type scriptTx struct {
	rowResults  []pg.Row // consumed in order by QueryRow
	rowsResults []rowsOrErr
	execErrs    []error

	querySQLs []string
	execSQLs  []string
	execArgs  [][]any

	rowIdx, rowsIdx, execIdx int
}

func (t *scriptTx) QueryRow(_ context.Context, sql string, _ ...any) pg.Row {
	t.querySQLs = append(t.querySQLs, sql)
	if t.rowIdx < len(t.rowResults) {
		r := t.rowResults[t.rowIdx]
		t.rowIdx++
		return r
	}
	return &stubRow{err: pg.ErrNoRows}
}

func (t *scriptTx) Query(_ context.Context, sql string, _ ...any) (pg.Rows, error) {
	t.querySQLs = append(t.querySQLs, sql)
	if t.rowsIdx < len(t.rowsResults) {
		r := t.rowsResults[t.rowsIdx]
		t.rowsIdx++
		return r.rows, r.err
	}
	return &stubRows{}, nil
}

func (t *scriptTx) Exec(_ context.Context, sql string, args ...any) error {
	t.execSQLs = append(t.execSQLs, sql)
	t.execArgs = append(t.execArgs, args)
	if t.execIdx < len(t.execErrs) {
		e := t.execErrs[t.execIdx]
		t.execIdx++
		return e
	}
	return nil
}

type scriptTQ struct {
	tx      *scriptTx
	tenants []string
}

func (q *scriptTQ) RunInTenantTx(ctx context.Context, tenantID string, fn func(context.Context, pg.Tx) error) error {
	q.tenants = append(q.tenants, tenantID)
	return fn(ctx, q.tx)
}

const (
	tRepoTenant = "22222222-2222-7222-8222-222222222222"
	tRepoNodeID = "00000000-0000-7000-8000-0000000000a1"
)

func nodeRow(id, tenant, name, parent string) *stubRow {
	return &stubRow{cols: []any{
		id, tenant, name, parent, 0,
		time.Now().UTC(), time.Now().UTC(), (*time.Time)(nil),
	}}
}

func TestTopicRepository_ProductionConstructors(t *testing.T) {
	q := pg.NewPgxPoolQuerier(nil)
	if pg.NewTopicRepository(q) == nil {
		t.Error("NewTopicRepository returned nil")
	}
	if pg.NewAtomTagSource(q) == nil {
		t.Error("NewAtomTagSource returned nil")
	}
}

func TestTopicRepository_NilNodeGuards(t *testing.T) {
	r := pg.NewTopicRepositoryFromTxQuerier(&scriptTQ{tx: &scriptTx{}})
	if err := r.Create(context.Background(), nil); err == nil {
		t.Error("Create(nil) must error")
	}
	if err := r.Update(context.Background(), nil); err == nil {
		t.Error("Update(nil) must error")
	}
}

// -----------------------------------------------------------------------------
// Create / Update
// -----------------------------------------------------------------------------

func TestTopicRepository_Create_WrapsInTenantTx(t *testing.T) {
	tx := &scriptTx{}
	tq := &scriptTQ{tx: tx}
	r := pg.NewTopicRepositoryFromTxQuerier(tq)

	n, err := topic.NewTopicNode(tRepoTenant, "Fractions", nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Create(context.Background(), n); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(tq.tenants) != 1 || tq.tenants[0] != tRepoTenant {
		t.Errorf("RunInTenantTx tenants = %v, want [%s]", tq.tenants, tRepoTenant)
	}
	if len(tx.execSQLs) != 1 || !strings.Contains(tx.execSQLs[0], "INSERT INTO topic_nodes") {
		t.Errorf("expected one INSERT INTO topic_nodes; got %v", tx.execSQLs)
	}
	// parent arg (4th) is "" for a root node (NULLIF → NULL).
	if got := tx.execArgs[0][3]; got != "" {
		t.Errorf("parent arg = %v, want empty string for root", got)
	}
}

func TestTopicRepository_Update_EmitsUpdate(t *testing.T) {
	tx := &scriptTx{}
	r := pg.NewTopicRepositoryFromTxQuerier(&scriptTQ{tx: tx})
	n, _ := topic.NewTopicNode(tRepoTenant, "T", nil, 0)
	if err := r.Update(context.Background(), n); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(tx.execSQLs) != 1 || !strings.Contains(tx.execSQLs[0], "UPDATE topic_nodes") {
		t.Errorf("expected UPDATE topic_nodes; got %v", tx.execSQLs)
	}
}

// -----------------------------------------------------------------------------
// GetByID / GetTree
// -----------------------------------------------------------------------------

func TestTopicRepository_GetByID_Found(t *testing.T) {
	tx := &scriptTx{rowResults: []pg.Row{nodeRow(tRepoNodeID, tRepoTenant, "Algebra", "")}}
	r := pg.NewTopicRepositoryFromTxQuerier(&scriptTQ{tx: tx})

	n, err := r.GetByID(context.Background(), tRepoTenant, tRepoNodeID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if n.Name != "Algebra" || n.TopicID != tRepoNodeID {
		t.Errorf("node = %+v", n)
	}
	if n.ParentID != nil {
		t.Errorf("parent = %v, want nil", *n.ParentID)
	}
	if !strings.Contains(tx.querySQLs[0], "FROM topic_nodes") {
		t.Errorf("expected SELECT FROM topic_nodes; got %q", tx.querySQLs[0])
	}
}

func TestTopicRepository_GetByID_NotFound(t *testing.T) {
	tx := &scriptTx{} // no scripted row → default ErrNoRows
	r := pg.NewTopicRepositoryFromTxQuerier(&scriptTQ{tx: tx})
	_, err := r.GetByID(context.Background(), tRepoTenant, "missing")
	if !errors.Is(err, topic.ErrNotFound) {
		t.Errorf("err = %v, want topic.ErrNotFound", err)
	}
}

func TestTopicRepository_GetTree_AllNodes(t *testing.T) {
	rows := &stubRows{rows: [][]any{
		{"id1", tRepoTenant, "A", "", 0, time.Now(), time.Now(), (*time.Time)(nil)},
		{"id2", tRepoTenant, "B", "id1", 1, time.Now(), time.Now(), (*time.Time)(nil)},
	}}
	tx := &scriptTx{rowsResults: []rowsOrErr{{rows: rows}}}
	r := pg.NewTopicRepositoryFromTxQuerier(&scriptTQ{tx: tx})

	got, err := r.GetTree(context.Background(), tRepoTenant, nil)
	if err != nil {
		t.Fatalf("GetTree: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d nodes, want 2", len(got))
	}
	if got[1].ParentID == nil || *got[1].ParentID != "id1" {
		t.Errorf("node B parent = %v, want id1", got[1].ParentID)
	}
	if strings.Contains(tx.querySQLs[0], "parent_id = $2") {
		t.Errorf("unfiltered GetTree must not filter parent; got %q", tx.querySQLs[0])
	}
}

func TestTopicRepository_GetTree_ChildrenFilter(t *testing.T) {
	tx := &scriptTx{rowsResults: []rowsOrErr{{rows: &stubRows{}}}}
	r := pg.NewTopicRepositoryFromTxQuerier(&scriptTQ{tx: tx})
	parent := "id1"
	if _, err := r.GetTree(context.Background(), tRepoTenant, &parent); err != nil {
		t.Fatalf("GetTree: %v", err)
	}
	if !strings.Contains(tx.querySQLs[0], "parent_id = $2::uuid") {
		t.Errorf("child-filtered GetTree must filter parent; got %q", tx.querySQLs[0])
	}
}

// -----------------------------------------------------------------------------
// Move — the cycle guard, exercised through the adapter
// -----------------------------------------------------------------------------

func TestTopicRepository_Move_Valid(t *testing.T) {
	newParent := "00000000-0000-7000-8000-0000000000bb"
	tx := &scriptTx{
		rowResults:  []pg.Row{nodeRow(tRepoNodeID, tRepoTenant, "N", "")},               // load node
		rowsResults: []rowsOrErr{{rows: &stubRows{rows: [][]any{{newParent}, {"gp"}}}}}, // chain
	}
	r := pg.NewTopicRepositoryFromTxQuerier(&scriptTQ{tx: tx})
	if err := r.Move(context.Background(), tRepoTenant, tRepoNodeID, &newParent); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if len(tx.execSQLs) != 1 || !strings.Contains(tx.execSQLs[0], "UPDATE topic_nodes SET parent_id") {
		t.Errorf("expected reparent UPDATE; got %v", tx.execSQLs)
	}
}

func TestTopicRepository_Move_Cycle(t *testing.T) {
	// The new parent's ancestry chain contains the node itself → cycle.
	tx := &scriptTx{
		rowResults:  []pg.Row{nodeRow(tRepoNodeID, tRepoTenant, "N", "")},
		rowsResults: []rowsOrErr{{rows: &stubRows{rows: [][]any{{"target"}, {tRepoNodeID}}}}},
	}
	r := pg.NewTopicRepositoryFromTxQuerier(&scriptTQ{tx: tx})
	target := "target"
	err := r.Move(context.Background(), tRepoTenant, tRepoNodeID, &target)
	if !errors.Is(err, topic.ErrCycle) {
		t.Errorf("err = %v, want topic.ErrCycle", err)
	}
	if len(tx.execSQLs) != 0 {
		t.Errorf("a cyclic move must not persist; got %v", tx.execSQLs)
	}
}

func TestTopicRepository_Move_NodeNotFound(t *testing.T) {
	tx := &scriptTx{} // load node → default ErrNoRows
	r := pg.NewTopicRepositoryFromTxQuerier(&scriptTQ{tx: tx})
	p := "p"
	if err := r.Move(context.Background(), tRepoTenant, "missing", &p); !errors.Is(err, topic.ErrNotFound) {
		t.Errorf("err = %v, want topic.ErrNotFound", err)
	}
}

func TestTopicRepository_Move_ParentNotFound(t *testing.T) {
	tx := &scriptTx{
		rowResults:  []pg.Row{nodeRow(tRepoNodeID, tRepoTenant, "N", "")},
		rowsResults: []rowsOrErr{{rows: &stubRows{}}}, // empty chain = parent missing
	}
	r := pg.NewTopicRepositoryFromTxQuerier(&scriptTQ{tx: tx})
	p := "ghost"
	if err := r.Move(context.Background(), tRepoTenant, tRepoNodeID, &p); !errors.Is(err, topic.ErrNotFound) {
		t.Errorf("err = %v, want topic.ErrNotFound", err)
	}
}

func TestTopicRepository_Move_ToRoot(t *testing.T) {
	tx := &scriptTx{rowResults: []pg.Row{nodeRow(tRepoNodeID, tRepoTenant, "N", "oldparent")}}
	r := pg.NewTopicRepositoryFromTxQuerier(&scriptTQ{tx: tx})
	if err := r.Move(context.Background(), tRepoTenant, tRepoNodeID, nil); err != nil {
		t.Fatalf("Move to root: %v", err)
	}
	// No chain query issued (only the load QueryRow), and one UPDATE.
	if len(tx.execSQLs) != 1 {
		t.Errorf("expected one UPDATE; got %v", tx.execSQLs)
	}
}

// -----------------------------------------------------------------------------
// SoftDelete — the non-empty guard, exercised through the adapter
// -----------------------------------------------------------------------------

func TestTopicRepository_SoftDelete_Empty(t *testing.T) {
	tx := &scriptTx{rowResults: []pg.Row{
		nodeRow(tRepoNodeID, tRepoTenant, "Leaf", ""), // load node
		&stubRow{cols: []any{0}},                      // count children = 0
	}}
	r := pg.NewTopicRepositoryFromTxQuerier(&scriptTQ{tx: tx})
	if err := r.SoftDelete(context.Background(), tRepoTenant, tRepoNodeID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	// One UPDATE for the node + one for its atom links.
	if len(tx.execSQLs) != 2 {
		t.Errorf("expected 2 UPDATEs (node + links); got %v", tx.execSQLs)
	}
}

func TestTopicRepository_SoftDelete_HasChildren(t *testing.T) {
	tx := &scriptTx{rowResults: []pg.Row{
		nodeRow(tRepoNodeID, tRepoTenant, "Parent", ""),
		&stubRow{cols: []any{1}}, // count children = 1
	}}
	r := pg.NewTopicRepositoryFromTxQuerier(&scriptTQ{tx: tx})
	err := r.SoftDelete(context.Background(), tRepoTenant, tRepoNodeID)
	if !errors.Is(err, topic.ErrHasChildren) {
		t.Errorf("err = %v, want topic.ErrHasChildren", err)
	}
	if len(tx.execSQLs) != 0 {
		t.Errorf("a refused delete must not persist; got %v", tx.execSQLs)
	}
}

func TestTopicRepository_SoftDelete_NotFound(t *testing.T) {
	tx := &scriptTx{} // load node → ErrNoRows
	r := pg.NewTopicRepositoryFromTxQuerier(&scriptTQ{tx: tx})
	if err := r.SoftDelete(context.Background(), tRepoTenant, "missing"); !errors.Is(err, topic.ErrNotFound) {
		t.Errorf("err = %v, want topic.ErrNotFound", err)
	}
}

// -----------------------------------------------------------------------------
// AttachAtom
// -----------------------------------------------------------------------------

func TestTopicRepository_AttachAtom_Valid(t *testing.T) {
	tx := &scriptTx{rowResults: []pg.Row{&stubRow{cols: []any{1}}}} // verify topic exists
	r := pg.NewTopicRepositoryFromTxQuerier(&scriptTQ{tx: tx})
	atomID := "00000000-0000-7000-8000-0000000000cc"
	if err := r.AttachAtom(context.Background(), tRepoTenant, tRepoNodeID, atomID); err != nil {
		t.Fatalf("AttachAtom: %v", err)
	}
	if len(tx.execSQLs) != 1 || !strings.Contains(tx.execSQLs[0], "INSERT INTO topic_node_atoms") {
		t.Errorf("expected INSERT INTO topic_node_atoms; got %v", tx.execSQLs)
	}
	if !strings.Contains(tx.execSQLs[0], "ON CONFLICT") {
		t.Errorf("attach must be idempotent (ON CONFLICT); got %q", tx.execSQLs[0])
	}
}

func TestTopicRepository_AttachAtom_TopicMissing(t *testing.T) {
	tx := &scriptTx{} // verify → ErrNoRows
	r := pg.NewTopicRepositoryFromTxQuerier(&scriptTQ{tx: tx})
	atomID := "00000000-0000-7000-8000-0000000000cc"
	if err := r.AttachAtom(context.Background(), tRepoTenant, "missing", atomID); !errors.Is(err, topic.ErrNotFound) {
		t.Errorf("err = %v, want topic.ErrNotFound", err)
	}
}

func TestTopicRepository_AttachAtom_InvalidAtom(t *testing.T) {
	tx := &scriptTx{}
	tq := &scriptTQ{tx: tx}
	r := pg.NewTopicRepositoryFromTxQuerier(tq)
	if err := r.AttachAtom(context.Background(), tRepoTenant, tRepoNodeID, "  "); !errors.Is(err, topic.ErrInvalidAtomID) {
		t.Errorf("err = %v, want topic.ErrInvalidAtomID", err)
	}
	if len(tq.tenants) != 0 {
		t.Errorf("invalid atom must fail before opening a tx; got %v", tq.tenants)
	}
}

// -----------------------------------------------------------------------------
// ExistingRootNames (SeedSink) / DistinctAtomTags (AtomTagSource)
// -----------------------------------------------------------------------------

func TestTopicRepository_ExistingRootNames(t *testing.T) {
	tx := &scriptTx{rowsResults: []rowsOrErr{{rows: &stubRows{rows: [][]any{{"fractions"}, {"algebra"}}}}}}
	r := pg.NewTopicRepositoryFromTxQuerier(&scriptTQ{tx: tx})
	got, err := r.ExistingRootNames(context.Background(), tRepoTenant)
	if err != nil {
		t.Fatalf("ExistingRootNames: %v", err)
	}
	if _, ok := got["fractions"]; !ok {
		t.Errorf("missing fractions in %v", got)
	}
	if _, ok := got["algebra"]; !ok {
		t.Errorf("missing algebra in %v", got)
	}
	if !strings.Contains(tx.querySQLs[0], "parent_id IS NULL") {
		t.Errorf("ExistingRootNames must scope to ROOT nodes; got %q", tx.querySQLs[0])
	}
}

func TestAtomTagSource_DistinctAtomTags(t *testing.T) {
	tx := &scriptTx{rowsResults: []rowsOrErr{{rows: &stubRows{rows: [][]any{{"fractions"}, {"geometry"}}}}}}
	s := pg.NewAtomTagSourceFromTxQuerier(&scriptTQ{tx: tx})
	got, err := s.DistinctAtomTags(context.Background(), tRepoTenant)
	if err != nil {
		t.Fatalf("DistinctAtomTags: %v", err)
	}
	if len(got) != 2 || got[0] != "fractions" || got[1] != "geometry" {
		t.Errorf("tags = %v, want [fractions geometry]", got)
	}
	if !strings.Contains(tx.querySQLs[0], "jsonb_array_elements_text") {
		t.Errorf("DistinctAtomTags must expand the tags JSONB array; got %q", tx.querySQLs[0])
	}
}
