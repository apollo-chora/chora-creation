// embedindex_test.go: Epic-1b W4, the atom embedding indexer (publish-time
// write + env-gated backfill). G1' gap 1 widens the port: every embed carries
// tenant + actor gcid so the gateway can attribute the ledger row; the actor
// for an atom is its AUTHOR (the topic-classifier precedent).
package embedindex

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type embedCall struct {
	TenantID, GCID, Text string
}

type fakeEmbedder struct {
	got []embedCall
	vec []float32
	err error
}

func (f *fakeEmbedder) Embed(_ context.Context, tenantID, gcid, text string) ([]float32, error) {
	f.got = append(f.got, embedCall{TenantID: tenantID, GCID: gcid, Text: text})
	if f.err != nil {
		return nil, f.err
	}
	return f.vec, nil
}
func (f *fakeEmbedder) ModelID() string { return "text-embedding-004" }

type upsertCall struct {
	TenantID, AtomID, ModelID string
	Dim                       int
}

type fakeStore struct {
	upserts  []upsertCall
	missing  []Atom
	upsertEr error
}

func (f *fakeStore) Upsert(_ context.Context, tenantID, atomID string, embedding []float32, modelID string) error {
	f.upserts = append(f.upserts, upsertCall{tenantID, atomID, modelID, len(embedding)})
	return f.upsertEr
}
func (f *fakeStore) ListPublishedMissingEmbedding(_ context.Context, _ string, _ int) ([]Atom, error) {
	out := f.missing
	f.missing = nil // one batch then drained
	return out, nil
}

func TestIndexAtom_EmbedsComposedTextAndUpserts(t *testing.T) {
	emb := &fakeEmbedder{vec: []float32{0.1, 0.2}}
	store := &fakeStore{}
	svc := New(emb, store)

	err := svc.IndexAtom(context.Background(), "tnt-1", "author-gcid-1", "atom-1",
		"Fractions I", "What is 1/2 + 1/4?", "Adding simple fractions.")
	if err != nil {
		t.Fatalf("IndexAtom: %v", err)
	}
	if len(emb.got) != 1 {
		t.Fatalf("embeds = %d", len(emb.got))
	}
	call := emb.got[0]
	// The gateway refuses an unattributed embed: tenant + gcid MUST reach the
	// embedder exactly as given.
	if call.TenantID != "tnt-1" {
		t.Errorf("embedder tenant = %q, want tnt-1", call.TenantID)
	}
	if call.GCID != "author-gcid-1" {
		t.Errorf("embedder gcid = %q, want author-gcid-1", call.GCID)
	}
	for _, want := range []string{"Fractions I", "1/2 + 1/4", "Adding simple"} {
		if !strings.Contains(call.Text, want) {
			t.Errorf("composed text missing %q: %q", want, call.Text)
		}
	}
	if len(store.upserts) != 1 || store.upserts[0].AtomID != "atom-1" || store.upserts[0].ModelID != "text-embedding-004" {
		t.Fatalf("upserts = %+v", store.upserts)
	}
}

func TestIndexAtom_NoTextIsNoOp(t *testing.T) {
	emb := &fakeEmbedder{vec: []float32{0.1}}
	store := &fakeStore{}
	svc := New(emb, store)
	if err := svc.IndexAtom(context.Background(), "tnt-1", "author-gcid-1", "atom-1", " ", "", ""); err != nil {
		t.Fatalf("no-text atom must no-op, got %v", err)
	}
	if len(emb.got) != 0 || len(store.upserts) != 0 {
		t.Fatal("nothing should embed for an empty atom")
	}
}

func TestIndexAtom_ErrorsSurface(t *testing.T) {
	svc := New(&fakeEmbedder{err: errors.New("boom")}, &fakeStore{})
	if err := svc.IndexAtom(context.Background(), "t", "g", "a", "Title", "", ""); err == nil {
		t.Fatal("embed error must surface (caller decides soft-fail)")
	}
	svc = New(&fakeEmbedder{vec: []float32{0.1}}, &fakeStore{upsertEr: errors.New("boom")})
	if err := svc.IndexAtom(context.Background(), "t", "g", "a", "Title", "", ""); err == nil {
		t.Fatal("upsert error must surface")
	}
}

func TestComposeAtomText_CapsLength(t *testing.T) {
	long := strings.Repeat("x", 5000)
	text := ComposeAtomText("T", "S", long)
	if len(text) > maxEmbedTextLen+8 {
		t.Fatalf("composed text len = %d, want capped ~%d", len(text), maxEmbedTextLen)
	}
}

func TestBackfill_IndexesMissingAtomsWithTheirAuthors(t *testing.T) {
	emb := &fakeEmbedder{vec: []float32{0.1}}
	store := &fakeStore{missing: []Atom{
		{AtomID: "atom-1", Title: "A", AuthorGCID: "author-1"},
		{AtomID: "atom-2", Title: "B", AuthorGCID: "author-2"},
		{AtomID: "atom-3", AuthorGCID: "author-3"}, // no text: skipped, not fatal
	}}
	svc := New(emb, store)

	n, err := svc.Backfill(context.Background(), "tnt-1", 50)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if n != 2 || len(store.upserts) != 2 {
		t.Fatalf("backfilled = %d upserts = %+v", n, store.upserts)
	}
	// Each candidate embeds under ITS OWN author, never a shared actor.
	if emb.got[0].GCID != "author-1" || emb.got[1].GCID != "author-2" {
		t.Fatalf("backfill actor attribution wrong: %+v", emb.got)
	}
}
