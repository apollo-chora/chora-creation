package topic

import (
	"context"
	"errors"
	"sort"
	"testing"
)

// fakeTagSource returns a fixed tag list per tenant.
type fakeTagSource struct {
	tags map[string][]string
	err  error
}

func (f *fakeTagSource) DistinctAtomTags(_ context.Context, tenantID string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.tags[tenantID], nil
}

// fakeSeedSink records created root nodes per tenant (by lowercased name).
type fakeSeedSink struct {
	roots     map[string]map[string]struct{} // tenant -> set of lowercased root names
	created   []*TopicNode
	existErr  error
	createErr error
}

func newFakeSink() *fakeSeedSink {
	return &fakeSeedSink{roots: map[string]map[string]struct{}{}}
}

func (f *fakeSeedSink) ExistingRootNames(_ context.Context, tenantID string) (map[string]struct{}, error) {
	if f.existErr != nil {
		return nil, f.existErr
	}
	out := map[string]struct{}{}
	for k := range f.roots[tenantID] {
		out[k] = struct{}{}
	}
	return out, nil
}

func (f *fakeSeedSink) Create(_ context.Context, n *TopicNode) error {
	if f.createErr != nil {
		return f.createErr
	}
	if f.roots[n.TenantID] == nil {
		f.roots[n.TenantID] = map[string]struct{}{}
	}
	f.roots[n.TenantID][lower(n.Name)] = struct{}{}
	f.created = append(f.created, n)
	return nil
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 32
		}
	}
	return string(b)
}

const seedTenant = "00000000-0000-7000-8000-000000000001"

func TestSeedBackfill_CreatesOnePerDistinctTag(t *testing.T) {
	src := &fakeTagSource{tags: map[string][]string{
		seedTenant: {"fractions", "algebra", "fractions", "   ", "geometry"},
	}}
	sink := newFakeSink()
	bf := NewSeedBackfill(src, sink)

	rep, err := bf.Seed(context.Background(), seedTenant)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Scanned != 3 {
		t.Errorf("Scanned = %d, want 3 (distinct non-blank)", rep.Scanned)
	}
	if rep.Created != 3 {
		t.Errorf("Created = %d, want 3", rep.Created)
	}
	if rep.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0", rep.Skipped)
	}
	if len(sink.created) != 3 {
		t.Fatalf("sink created %d nodes, want 3", len(sink.created))
	}
	// Every seeded node is a ROOT (parent nil) and tenant-scoped.
	for _, n := range sink.created {
		if n.ParentID != nil {
			t.Errorf("seeded node %q has parent %v, want root", n.Name, *n.ParentID)
		}
		if n.TenantID != seedTenant {
			t.Errorf("seeded node tenant = %q, want %q", n.TenantID, seedTenant)
		}
	}
	got := []string{}
	for _, n := range sink.created {
		got = append(got, n.Name)
	}
	sort.Strings(got)
	want := []string{"algebra", "fractions", "geometry"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("created names = %v, want %v", got, want)
			break
		}
	}
}

func TestSeedBackfill_Idempotent(t *testing.T) {
	src := &fakeTagSource{tags: map[string][]string{
		seedTenant: {"fractions", "algebra", "geometry"},
	}}
	sink := newFakeSink()
	bf := NewSeedBackfill(src, sink)

	if _, err := bf.Seed(context.Background(), seedTenant); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	// Second run: everything already exists → zero new nodes.
	rep2, err := bf.Seed(context.Background(), seedTenant)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if rep2.Created != 0 {
		t.Errorf("re-run Created = %d, want 0 (idempotent)", rep2.Created)
	}
	if rep2.Skipped != 3 {
		t.Errorf("re-run Skipped = %d, want 3", rep2.Skipped)
	}
	if len(sink.created) != 3 {
		t.Errorf("total created across two runs = %d, want 3 (no duplicates)", len(sink.created))
	}
}

func TestSeedBackfill_SkipsExisting(t *testing.T) {
	src := &fakeTagSource{tags: map[string][]string{
		seedTenant: {"fractions", "algebra", "geometry"},
	}}
	sink := newFakeSink()
	// Pre-seed "fractions" (case-insensitive match against "Fractions").
	sink.roots[seedTenant] = map[string]struct{}{"fractions": {}}
	bf := NewSeedBackfill(src, sink)

	rep, err := bf.Seed(context.Background(), seedTenant)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Created != 2 {
		t.Errorf("Created = %d, want 2 (algebra, geometry)", rep.Created)
	}
	if rep.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 (fractions existed)", rep.Skipped)
	}
}

func TestSeedBackfill_PropagatesErrors(t *testing.T) {
	sentinel := errors.New("boom")

	// source error
	bf := NewSeedBackfill(&fakeTagSource{err: sentinel}, newFakeSink())
	if _, err := bf.Seed(context.Background(), seedTenant); !errors.Is(err, sentinel) {
		t.Errorf("source err = %v, want wrap of %v", err, sentinel)
	}

	// existing-names error
	sink := newFakeSink()
	sink.existErr = sentinel
	bf2 := NewSeedBackfill(&fakeTagSource{tags: map[string][]string{seedTenant: {"x"}}}, sink)
	if _, err := bf2.Seed(context.Background(), seedTenant); !errors.Is(err, sentinel) {
		t.Errorf("existing err = %v, want wrap of %v", err, sentinel)
	}

	// create error
	sink3 := newFakeSink()
	sink3.createErr = sentinel
	bf3 := NewSeedBackfill(&fakeTagSource{tags: map[string][]string{seedTenant: {"x"}}}, sink3)
	if _, err := bf3.Seed(context.Background(), seedTenant); !errors.Is(err, sentinel) {
		t.Errorf("create err = %v, want wrap of %v", err, sentinel)
	}
}

func TestSeedBackfill_EmptyTenant(t *testing.T) {
	bf := NewSeedBackfill(&fakeTagSource{}, newFakeSink())
	if _, err := bf.Seed(context.Background(), ""); err != ErrTenantRequired {
		t.Errorf("err = %v, want %v", err, ErrTenantRequired)
	}
}
