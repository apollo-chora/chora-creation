package reuseconsent_test

import (
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/audience"
	"github.com/apollo-chora/chora-creation/internal/domain/reuseconsent"
)

// ADR-229 D4.1 / ADR-233 D10-D11 — THE reuse-consent disjunct, as ONE pure
// predicate:
//
//	own ∪ (tenant-visible ∧ published) ∪ (friends-visible ∧ published ∧
//	author ∈ my friend set) ∪ granted
//
// Shared by BOTH gates: collection-ADD (ADR-229 D4.4, local flag check) and
// collection-CONVERT (ADR-233 D10, full disjunct + grant mint). The picker
// (question_search.go) enforces the same predicate in SQL; this package is the
// single canonical statement of it so an arm and a convert can never diverge.

const (
	actor  = "019f0000-0000-7000-8000-00000000ac70"
	author = "019f0000-0000-7000-8000-0000000a1780"
	friend = "019f0000-0000-7000-8000-000000f81e4d"
	atomA  = "019f0000-0000-7000-8000-0000000a70a1"
)

func ctxWith(friends, granted []string) reuseconsent.Context {
	return reuseconsent.Context{ActorGCID: actor, FriendGCIDs: friends, GrantedAtomIDs: granted}
}

func fact(a audience.Audience, published bool, authorGCID string) reuseconsent.AtomFact {
	return reuseconsent.AtomFact{AtomID: atomA, AuthorGCID: authorGCID, Audience: a, Published: published}
}

// ---------------------------------------------------------------- own leg ---

func TestEvaluate_OwnAtom_EntitledRegardlessOfAudienceOrPublishState(t *testing.T) {
	// You may always reuse your own atom — even private, even unpublished.
	// And it needs NO grant: a grant is the audit record of reusing SOMEONE
	// ELSE's atom (ADR-229 D2).
	for _, a := range []audience.Audience{audience.Private, audience.Friends, audience.Tenant} {
		for _, published := range []bool{true, false} {
			d := reuseconsent.Evaluate(fact(a, published, actor), ctxWith(nil, nil))
			if !d.Entitled {
				t.Fatalf("own atom (audience=%q published=%v): Entitled=false, want true", a, published)
			}
			if d.NeedsGrant {
				t.Fatalf("own atom (audience=%q): NeedsGrant=true, want false — never grant against yourself", a)
			}
		}
	}
}

// ------------------------------------------------------------- tenant leg ---

func TestEvaluate_TenantVisibleAndPublished_EntitledAndNeedsGrant(t *testing.T) {
	d := reuseconsent.Evaluate(fact(audience.Tenant, true, author), ctxWith(nil, nil))
	if !d.Entitled {
		t.Fatal("tenant-visible + published: Entitled=false, want true")
	}
	// This is the leg that mints the GRANT_SCOPE_COLLECTION audit record
	// (ADR-233 D10a) — the whole reason conversion is a grant-write site.
	if !d.NeedsGrant {
		t.Fatal("tenant-visible + published: NeedsGrant=false, want true (D2 audit record)")
	}
}

func TestEvaluate_TenantVisibleButUnpublished_Excluded(t *testing.T) {
	d := reuseconsent.Evaluate(fact(audience.Tenant, false, author), ctxWith(nil, nil))
	if d.Entitled {
		t.Fatal("tenant-visible but UNPUBLISHED: Entitled=true, want false")
	}
	if d.Reason != reuseconsent.ReasonNotPublished {
		t.Fatalf("Reason = %q, want %q", d.Reason, reuseconsent.ReasonNotPublished)
	}
}

// ------------------------------------------------------------ friends leg ---

func TestEvaluate_FriendsVisible_EntitledOnlyWhenAuthorIsAFriend(t *testing.T) {
	// ADR-229 D4.1's friends leg. Inert until the A+ audience un-hides
	// (A1.4 / ADR-230 B-lite.2) because no atom carries audience=friends yet —
	// but implemented, so convert already works the day it does.
	in := reuseconsent.Evaluate(fact(audience.Friends, true, friend), ctxWith([]string{friend}, nil))
	if !in.Entitled {
		t.Fatal("friends-visible + published + author IS a friend: Entitled=false, want true")
	}
	if !in.NeedsGrant {
		t.Fatal("friends-visible pass: NeedsGrant=false, want true (non-owner reuse ⇒ audit record)")
	}

	out := reuseconsent.Evaluate(fact(audience.Friends, true, author), ctxWith([]string{friend}, nil))
	if out.Entitled {
		t.Fatal("friends-visible but author NOT in friend set: Entitled=true, want false")
	}
	if out.Reason != reuseconsent.ReasonNotInFriendSet {
		t.Fatalf("Reason = %q, want %q", out.Reason, reuseconsent.ReasonNotInFriendSet)
	}
}

func TestEvaluate_FriendsVisibleUnpublished_ExcludedAsNotPublished(t *testing.T) {
	d := reuseconsent.Evaluate(fact(audience.Friends, false, friend), ctxWith([]string{friend}, nil))
	if d.Entitled {
		t.Fatal("friends-visible but unpublished: Entitled=true, want false")
	}
	if d.Reason != reuseconsent.ReasonNotPublished {
		t.Fatalf("Reason = %q, want %q", d.Reason, reuseconsent.ReasonNotPublished)
	}
}

// ------------------------------------------------------------- granted leg ---

func TestEvaluate_PrivateButGranted_Entitled_AndDoesNotReGrant(t *testing.T) {
	// The orphan-edition path (ADR-229 A1.1): a withdrawn atom is re-minted as
	// a frozen orphan with audience=private FOREVER, reachable ONLY through a
	// repointed grant. It must convert.
	d := reuseconsent.Evaluate(fact(audience.Private, true, author), ctxWith(nil, []string{atomA}))
	if !d.Entitled {
		t.Fatal("private but GRANTED (orphan edition): Entitled=false, want true")
	}
	if d.NeedsGrant {
		t.Fatal("already granted: NeedsGrant=true, want false — must not re-mint an existing grant")
	}
}

func TestEvaluate_GrantOutranksUnpublished(t *testing.T) {
	// An active grant is dispositive. Orphan editions are never 'published'.
	d := reuseconsent.Evaluate(fact(audience.Private, false, author), ctxWith(nil, []string{atomA}))
	if !d.Entitled {
		t.Fatal("granted + unpublished (orphan): Entitled=false, want true")
	}
}

// ------------------------------------------------------------- private leg ---

func TestEvaluate_PrivateNotOwnNotGranted_Excluded(t *testing.T) {
	d := reuseconsent.Evaluate(fact(audience.Private, true, author), ctxWith(nil, nil))
	if d.Entitled {
		t.Fatal("private, not own, not granted: Entitled=true, want false")
	}
	if d.Reason != reuseconsent.ReasonNarrowed {
		t.Fatalf("Reason = %q, want %q", d.Reason, reuseconsent.ReasonNarrowed)
	}
}

// ----------------------------------------------------------------- batch ---

func TestEvaluateAll_PartitionsPreservingCollectionOrder(t *testing.T) {
	// ADR-233 D11: mixed collection ⇒ partial success. The entitled subset
	// converts IN COLLECTION ORDER (the proto pins order); the rest come back
	// named, with reasons, so A+ can tell the learner what was left out.
	own := "019f0000-0000-7000-8000-00000000own1"
	tenantOK := "019f0000-0000-7000-8000-0000000tenA"
	narrowed := "019f0000-0000-7000-8000-00000000nar1"
	unpub := "019f0000-0000-7000-8000-0000000unp1"
	missing := "019f0000-0000-7000-8000-00000000mis1"

	order := []string{own, narrowed, tenantOK, unpub, missing}
	facts := map[string]reuseconsent.AtomFact{
		own:      {AtomID: own, AuthorGCID: actor, Audience: audience.Private, Published: true},
		tenantOK: {AtomID: tenantOK, AuthorGCID: author, Audience: audience.Tenant, Published: true},
		narrowed: {AtomID: narrowed, AuthorGCID: author, Audience: audience.Private, Published: true},
		unpub:    {AtomID: unpub, AuthorGCID: author, Audience: audience.Tenant, Published: false},
		// `missing` is absent from the map — soft-deleted / never existed.
	}

	res := reuseconsent.EvaluateAll(order, facts, ctxWith(nil, nil))

	wantEntitled := []string{own, tenantOK}
	if len(res.EntitledAtomIDs) != len(wantEntitled) {
		t.Fatalf("EntitledAtomIDs = %v, want %v", res.EntitledAtomIDs, wantEntitled)
	}
	for i := range wantEntitled {
		if res.EntitledAtomIDs[i] != wantEntitled[i] {
			t.Fatalf("EntitledAtomIDs[%d] = %q, want %q (collection order must be preserved)",
				i, res.EntitledAtomIDs[i], wantEntitled[i])
		}
	}

	if len(res.Excluded) != 3 {
		t.Fatalf("len(Excluded) = %d, want 3 (narrowed, unpublished, missing)", len(res.Excluded))
	}
	byAtom := map[string]reuseconsent.Reason{}
	for _, e := range res.Excluded {
		byAtom[e.AtomID] = e.Reason
	}
	for atomID, want := range map[string]reuseconsent.Reason{
		narrowed: reuseconsent.ReasonNarrowed,
		unpub:    reuseconsent.ReasonNotPublished,
		missing:  reuseconsent.ReasonAtomNotFound,
	} {
		if byAtom[atomID] != want {
			t.Fatalf("Excluded[%s].Reason = %q, want %q", atomID, byAtom[atomID], want)
		}
	}

	// Only the tenant-visible non-own atom needs a grant minted.
	if len(res.NeedGrantAtomIDs) != 1 || res.NeedGrantAtomIDs[0] != tenantOK {
		t.Fatalf("NeedGrantAtomIDs = %v, want [%s]", res.NeedGrantAtomIDs, tenantOK)
	}
}

func TestEvaluateAll_AllExcluded_YieldsEmptyEntitled(t *testing.T) {
	// The caller (service) turns this into 409 CREATION_COLLECTION_NO_ENTITLED_ATOMS.
	// Emitting an empty study list would be fabricating a success.
	order := []string{atomA}
	facts := map[string]reuseconsent.AtomFact{
		atomA: {AtomID: atomA, AuthorGCID: author, Audience: audience.Private, Published: true},
	}
	res := reuseconsent.EvaluateAll(order, facts, ctxWith(nil, nil))
	if len(res.EntitledAtomIDs) != 0 {
		t.Fatalf("EntitledAtomIDs = %v, want empty", res.EntitledAtomIDs)
	}
	if len(res.Excluded) != 1 {
		t.Fatalf("len(Excluded) = %d, want 1", len(res.Excluded))
	}
}
