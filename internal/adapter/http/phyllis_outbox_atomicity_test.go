// phyllis_outbox_atomicity_test.go - the transactional-outbox atomicity
// contract for the /v1/atoms write path.
//
// Measured defect (chaos run chaos20260807a, 2026-08-07, evidence
// docs/report/evidence/2026-08-07/cli/chaos-11-FINDINGS.md): under 64
// concurrent writers the sole chora-creation pod was SIGKILLed and 20 of 747
// atoms (2.68%) committed to learning_atoms with no outbox row behind them.
// Ten of those returned HTTP 201 to the author. The victim logged 13x
// "publish error: outbox: Insert: connection reset by peer" as its
// cloudsql-proxy sidecar died, then discarded every one of them.
//
// Contract under test: the atom row and the row announcing it are ONE unit of
// work. When the event cannot be enqueued, nothing commits and the caller is
// told the truth so it can retry. A 201 for an atom the rest of the platform
// will never hear about is a fabricated success.
//
// These tests drive the REAL production composition (pg.AtomTxWriter over
// outbox.Publisher over outbox.TxStore); only the pgx transaction itself is
// faked, by a recorder that applies statements to its committed log only if
// the transaction function returns nil.
package httpadapter_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	creationoutbox "github.com/apollo-chora/chora-creation/internal/adapter/outbox"
	creationpg "github.com/apollo-chora/chora-creation/internal/adapter/pg"
	"github.com/apollo-chora/chora-creation/internal/adapter/storage"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// errOutboxSevered is the failure the victim pod actually logged: the outbox
// INSERT losing its database connection mid-request.
var errOutboxSevered = errors.New("connection reset by peer")

// -----------------------------------------------------------------------------
// A transaction recorder standing in for pgx.
// -----------------------------------------------------------------------------

// txRecorder implements pg.TxQuerier with real commit / rollback semantics:
// statements issued inside the transaction function are buffered, and are
// appended to `committed` only when that function returns nil. A non-nil
// return discards the buffer, exactly as a rollback discards the work.
type txRecorder struct {
	// failOn makes any statement containing this substring return an error,
	// standing in for the severed outbox connection.
	failOn string

	transactions []string // tenant_id per RunInTenantTx call
	committed    []string // statements that actually survived a commit
}

func (r *txRecorder) RunInTenantTx(ctx context.Context, tenantID string, fn func(context.Context, creationpg.Tx) error) error {
	r.transactions = append(r.transactions, tenantID)
	buf := &txRecorderTx{parent: r}
	if err := fn(ctx, buf); err != nil {
		return err // rollback: buf.pending is discarded
	}
	r.committed = append(r.committed, buf.pending...)
	return nil
}

// committedMatching counts committed statements containing sub.
func (r *txRecorder) committedMatching(sub string) int {
	n := 0
	for _, s := range r.committed {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

type txRecorderTx struct {
	parent  *txRecorder
	pending []string
}

func (t *txRecorderTx) Exec(_ context.Context, sql string, _ ...any) error {
	if t.parent.failOn != "" && strings.Contains(sql, t.parent.failOn) {
		return errOutboxSevered
	}
	t.pending = append(t.pending, sql)
	return nil
}

func (t *txRecorderTx) Query(_ context.Context, _ string, _ ...any) (creationpg.Rows, error) {
	return nil, errors.New("txRecorderTx: Query not used by the write path")
}

func (t *txRecorderTx) QueryRow(_ context.Context, _ string, _ ...any) creationpg.Row {
	return nil
}

// newAtomicPhyllisServer wires the Phyllis router the way production does:
// an AtomTxWriter whose publisher writes outbox rows through the very same
// transaction as the atom row.
func newAtomicPhyllisServer(t *testing.T, rec *txRecorder) (http.Handler, *inmem.AtomRepository) {
	t.Helper()
	repo := inmem.NewAtomRepository()
	writer := creationpg.NewAtomTxWriter(rec, func(tx creationpg.Tx) atom.EventPublisher {
		return creationoutbox.NewPublisher(creationoutbox.PublisherConfig{
			Store:         creationoutbox.NewTxStore(tx),
			SourceProject: "chora-local",
			SourceService: "chora-creation",
		})
	})
	h := httpadapter.NewPhyllisRouter(httpadapter.PhyllisDeps{
		Repo:        repo,
		Broker:      &stubBroker{},
		Publisher:   &recordingPub{},
		AtomWriter:  writer,
		MediaRepo:   inmem.NewMediaRepository(),
		MediaSigner: storage.NewInMemorySigner(""),
	})
	return h, repo
}

func createAtomBody(title string) map[string]any {
	return map[string]any{
		"course_id":  courseA,
		"title":      title,
		"body":       "body",
		"type":       "mcq",
		"difficulty": 3,
	}
}

// -----------------------------------------------------------------------------
// The positive control.
// -----------------------------------------------------------------------------

// TestPhyllis_PostAtoms_AtomAndEventShareOneTransaction is the control that
// makes the failure tests below mean something: on the happy path the harness
// must be able to SEE both writes, and see them inside a single transaction.
// Without this, "0 statements committed" could mean fixed or blind.
func TestPhyllis_PostAtoms_AtomAndEventShareOneTransaction(t *testing.T) {
	t.Parallel()

	rec := &txRecorder{}
	srv, _ := newAtomicPhyllisServer(t, rec)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/atoms", createAtomBody("atom whose event is durable")))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	if len(rec.transactions) != 1 {
		t.Fatalf("transactions opened = %d; want exactly 1 covering both writes", len(rec.transactions))
	}
	if rec.transactions[0] != tenantA {
		t.Errorf("transaction tenant = %q; want %q", rec.transactions[0], tenantA)
	}
	if got := rec.committedMatching("learning_atoms"); got != 1 {
		t.Errorf("committed learning_atoms statements = %d; want 1", got)
	}
	if got := rec.committedMatching("outbox_events"); got != 1 {
		t.Errorf("committed outbox_events statements = %d; want 1", got)
	}
}

// -----------------------------------------------------------------------------
// The defect.
// -----------------------------------------------------------------------------

// TestPhyllis_PostAtoms_OutboxInsertFails_RollsBackAndFails is the exact shape
// of the ten atoms that were handed a 201 with no event behind them.
func TestPhyllis_PostAtoms_OutboxInsertFails_RollsBackAndFails(t *testing.T) {
	t.Parallel()

	rec := &txRecorder{failOn: "outbox_events"}
	srv, _ := newAtomicPhyllisServer(t, rec)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/v1/atoms", createAtomBody("atom whose event never queued")))

	// Positive control: the write path must have reached the transaction at
	// all, otherwise the assertions below pass for the wrong reason.
	if len(rec.transactions) != 1 {
		t.Fatalf("transactions opened = %d; want 1. This test cannot see the defect it targets", len(rec.transactions))
	}

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d; want 500. The author must not be told an atom was created when its event was never queued. body=%s",
			w.Code, w.Body.String())
	}
	if got := rec.committedMatching("learning_atoms"); got != 0 {
		t.Errorf("committed learning_atoms statements = %d; want 0. The atom must roll back with the event that failed to queue", got)
	}
	if len(rec.committed) != 0 {
		t.Errorf("committed statements = %d; want 0 (nothing at all survives a failed enqueue): %v", len(rec.committed), rec.committed)
	}
}

// TestPhyllis_PatchAtom_PersistsRevisionWithoutOutboxRow covers the second
// call site. PATCH appends a revision but emits NO event: atom.revised.v1 has
// no contract, no binary encoder and no consumer, so the revision is
// Creation-local and the write must not depend on the outbox being reachable.
func TestPhyllis_PatchAtom_PersistsRevisionWithoutOutboxRow(t *testing.T) {
	t.Parallel()

	rec := &txRecorder{}
	srv, repo := newAtomicPhyllisServer(t, rec)

	// Seed an atom directly in the repository the PATCH handler reads from.
	seeded, err := atom.NewBound(atom.NewBoundParams{
		TenantID: tenantA, Gcid: gcidA, CourseID: courseA,
		Title: "atom to revise", Body: "original body",
		AtomType: atom.AtomType("mcq"), SourceType: atom.SourceManual,
	})
	if err != nil {
		t.Fatalf("seed atom: %v", err)
	}
	if err := repo.Save(context.Background(), seeded); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	// Sever the outbox: the append emits no event, so it must still succeed.
	rec.failOn = "outbox_events"
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPatch, "/v1/atoms/"+seeded.AtomID, map[string]any{
		"body":        "revised body with no event",
		"source_type": "manual",
	}))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d; want 200 (the revision append emits no event). body=%s",
			w.Code, w.Body.String())
	}
	if got := rec.committedMatching("outbox_events"); got != 0 {
		t.Errorf("committed outbox_events statements = %d; want 0 (the append must emit no event)", got)
	}
	got, err := repo.Get(context.Background(), tenantA, seeded.AtomID)
	if err != nil {
		t.Fatalf("repo.Get after PATCH: %v", err)
	}
	if len(got.RevisionHistory()) != 2 {
		t.Errorf("revision history len = %d; want 2 (the append must still persist)", len(got.RevisionHistory()))
	}
}
