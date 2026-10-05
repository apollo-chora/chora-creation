// fork_service_test.go — CHO-2165 / ADR-233 D9, the RESERVED half. RED-first.
//
// WS-4 shipped ConvertToStudyList with the policy `actor == owner`, and D9 named
// the other half as reserved: **"actor may VIEW the collection"**. Landing it
// turns a shared collection into a FORKABLE one.
//
// The whole claim of D9 is that this is safe BY CONSTRUCTION, not by a new check:
//
//   - WHO may fork is the D8 read predicate, reused verbatim — owner ∪ tenant ∪
//     (friends ∧ actor ∈ owner's friend set). A collection you cannot READ is one
//     you cannot FORK, and the refusal is ErrNotFound (404), never ErrForbidden:
//     a 403 confirms the existence of a collection the caller may not see.
//
//   - WHAT crosses into chora_consumption is the ADR-229 disjunct, already
//     evaluated per-atom against the ACTOR's GCID. So the sharer's entitlements
//     never travel with the list. That is the security heart of this story: if
//     they did travel, curating a collection would become a laundering channel
//     for consent — anyone could re-broadcast an author's private work to the
//     whole tenant merely by putting it in a list and sharing the list.
//
//   - The Collection is NOT mutated (D9). Conversion is per-(collection, actor)
//     state and lives on the FORKER's derived path. Stamping the source would
//     mutate someone else's aggregate the moment a non-owner forks.
//
// These tests exercise the SERVICE, because the policy is a service concern —
// D9 is explicit that convert authorization is a policy, not an aggregate
// invariant.
package collection_test

import (
	"context"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/audience"
	"github.com/apollo-chora/chora-creation/internal/domain/collection"
	"github.com/apollo-chora/chora-creation/internal/domain/reuseconsent"
)

// forkerGCID is a THIRD identity, distinct from both the collection owner
// (ownerA) and the atom author (strangerGCD). All three must differ, or the
// "entitlements do not travel" test cannot tell whose consent was consulted.
const forkerGCID = "01970000-0000-7000-9000-0000000000f3"

// =============================================================================
// WHO may fork — the D8 read predicate, reused verbatim
// =============================================================================

// The headline. This replaces TestConvert_NonOwnerIsForbidden, which asserted
// the WS-4 policy (`actor == owner`) that D9 always marked as half-built.
func TestFork_StrangerForksTenantVisibleCollection(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	shared := makeAtomID(t, 1)
	c := f.seed(t, audience.Tenant, shared)
	f.facts.facts[shared] = tenantFact(shared)

	res, err := f.svc.ConvertToStudyList(context.Background(), collection.ConvertToStudyListInput{
		TenantID: tenantA, CollectionID: c.CollectionID, ActorGCID: forkerGCID,
	})
	if err != nil {
		t.Fatalf("ConvertToStudyList by a non-owner who may VIEW the collection: %v; "+
			"want success — ADR-233 D9's reserved policy is 'actor may view'", err)
	}
	if res.AtomCount != 1 {
		t.Errorf("AtomCount = %d; want 1", res.AtomCount)
	}
	if res.StudyListEventID == "" {
		t.Error("StudyListEventID is empty; the fork published no study list")
	}
}

// A private collection must be INVISIBLE to a non-owner — and invisible means
// 404, not 403. It also means the reuse gate never runs: authorization precedes
// the work, so a caller who may not even see the collection never reaches it.
func TestFork_PrivateCollectionIsNotFoundToANonOwner(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	secret := makeAtomID(t, 1)
	c := f.seed(t, audience.Private, secret)
	f.facts.facts[secret] = tenantFact(secret)

	_, err := f.svc.ConvertToStudyList(context.Background(), collection.ConvertToStudyListInput{
		TenantID: tenantA, CollectionID: c.CollectionID, ActorGCID: forkerGCID,
	})
	if errors.Is(err, collection.ErrForbidden) {
		t.Fatalf("err = ErrForbidden (403); a 403 CONFIRMS the collection exists to a caller " +
			"who may not see it — want ErrNotFound (404)")
	}
	if !errors.Is(err, collection.ErrNotFound) {
		t.Fatalf("err = %v; want ErrNotFound", err)
	}

	// The refusal happened at the READ. Nothing downstream ran.
	if got := f.facts.count(); got != 0 {
		t.Errorf("the reuse gate read atom facts %d times for a caller who cannot see the "+
			"collection; authorization must precede the work", got)
	}
	if got := f.authz.count(); got != 0 {
		t.Errorf("grants minted = %d; want 0", got)
	}
	if len(f.pub.events) != 0 {
		t.Errorf("events published = %d; want 0", len(f.pub.events))
	}
}

func TestFork_FriendForksFriendsVisibleCollection(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	shared := makeAtomID(t, 1)
	c := f.seed(t, audience.Friends, shared)
	f.facts.facts[shared] = tenantFact(shared)
	// The FORKER's friend set contains the collection's owner. The predicate asks
	// "is the owner among the actor's friends" — never the reverse.
	f.consent.friendsByActor[forkerGCID] = []string{ownerA}

	res, err := f.svc.ConvertToStudyList(context.Background(), collection.ConvertToStudyListInput{
		TenantID: tenantA, CollectionID: c.CollectionID, ActorGCID: forkerGCID,
	})
	if err != nil {
		t.Fatalf("ConvertToStudyList by a friend of the owner: %v; want success", err)
	}
	if res.AtomCount != 1 {
		t.Errorf("AtomCount = %d; want 1", res.AtomCount)
	}
}

func TestFork_NonFriendCannotForkFriendsVisibleCollection(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	shared := makeAtomID(t, 1)
	c := f.seed(t, audience.Friends, shared)
	f.facts.facts[shared] = tenantFact(shared)
	f.consent.friendsByActor[forkerGCID] = nil // not a friend of the owner

	_, err := f.svc.ConvertToStudyList(context.Background(), collection.ConvertToStudyListInput{
		TenantID: tenantA, CollectionID: c.CollectionID, ActorGCID: forkerGCID,
	})
	if !errors.Is(err, collection.ErrNotFound) {
		t.Fatalf("err = %v; want ErrNotFound", err)
	}
	if got := f.facts.count(); got != 0 {
		t.Errorf("the reuse gate ran (%d reads) for a non-friend; the D8 predicate must refuse first", got)
	}
}

// The friends leg needs the actor's friend set TWICE — once to admit the
// COLLECTION (the D8 view gate) and once to judge its ATOMS (the ADR-229
// disjunct). It must be resolved ONCE and reused.
//
// Two fetches would not merely cost a second round-trip to chora-sharing: they
// could DISAGREE. A friendship revoked between them would admit the collection
// under one friend set and judge its atoms against another.
func TestFork_FriendsLegResolvesConsentExactlyOnce(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	shared := makeAtomID(t, 1)
	c := f.seed(t, audience.Friends, shared)
	f.facts.facts[shared] = tenantFact(shared)
	f.consent.friendsByActor[forkerGCID] = []string{ownerA}

	if _, err := f.svc.ConvertToStudyList(context.Background(), collection.ConvertToStudyListInput{
		TenantID: tenantA, CollectionID: c.CollectionID, ActorGCID: forkerGCID,
	}); err != nil {
		t.Fatalf("ConvertToStudyList: %v", err)
	}

	if got := f.consent.askedAbout(); len(got) != 1 {
		t.Errorf("resolved the consent context %d times (%v); want exactly 1 — the view gate's "+
			"fetch is the one the per-atom disjunct reuses", len(got), got)
	}
}

// Regression guard: relaxing the policy must not break the owner's own path.
func TestFork_OwnerCanStillConvertTheirOwnPrivateCollection(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	own := makeAtomID(t, 1)
	c := f.seed(t, audience.Private, own)
	f.facts.facts[own] = ownFact(own)

	res, err := f.svc.ConvertToStudyList(context.Background(), collection.ConvertToStudyListInput{
		TenantID: tenantA, CollectionID: c.CollectionID, ActorGCID: ownerA,
	})
	if err != nil {
		t.Fatalf("the owner converting their OWN private collection: %v; want success", err)
	}
	if res.AtomCount != 1 {
		t.Errorf("AtomCount = %d; want 1", res.AtomCount)
	}
	if got := f.authz.count(); got != 0 {
		t.Errorf("grants minted = %d; want 0 — you do not license your own work", got)
	}
}

// =============================================================================
// 🔴🔴 WHAT crosses — the security heart
// =============================================================================

// ownerA curated a collection containing `licensed`: a PRIVATE atom authored by
// a third party, which ownerA may reuse ONLY because ownerA holds a grant for
// it. ownerA then shares the collection tenant-wide. The forker holds no such
// grant.
//
// The forker's fork must EXCLUDE `licensed` and NAME the reason. If the sharer's
// entitlements travelled with the list, curating a collection would become a
// laundering channel for consent: anyone could re-broadcast an author's private
// work to an entire tenant just by listing it and sharing the list.
//
// This test is only meaningful because memConsent is ACTOR-SCOPED. A stub that
// answered every caller with the same grants would pass this test even if the
// service resolved the consent context for the collection's OWNER — which is the
// precise defect at stake. Hence the final assertion on askedAbout().
func TestFork_TheSharersEntitlementsDoNotTravelWithTheList(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	shared := makeAtomID(t, 1)   // tenant-visible → the forker earns this on their own
	licensed := makeAtomID(t, 2) // private, third-party author, GRANTED TO ownerA ALONE

	c := f.seed(t, audience.Tenant, shared, licensed)
	f.facts.facts[shared] = tenantFact(shared)
	f.facts.facts[licensed] = privateFact(licensed)

	f.consent.grantedByActor[ownerA] = []string{licensed} // the sharer is entitled
	f.consent.grantedByActor[forkerGCID] = nil            // the forker is NOT

	res, err := f.svc.ConvertToStudyList(context.Background(), collection.ConvertToStudyListInput{
		TenantID: tenantA, CollectionID: c.CollectionID, ActorGCID: forkerGCID,
	})
	if err != nil {
		t.Fatalf("ConvertToStudyList: %v", err)
	}

	if res.AtomCount != 1 {
		t.Errorf("AtomCount = %d; want 1 — only the tenant-visible atom may cross", res.AtomCount)
	}
	if len(res.Excluded) != 1 || res.Excluded[0].AtomID != licensed {
		t.Fatalf("Excluded = %+v; want exactly [%s], the atom the SHARER was licensed for",
			res.Excluded, licensed)
	}
	if res.Excluded[0].Reason != reuseconsent.ReasonNarrowed {
		t.Errorf("Excluded[0].Reason = %q; want %q",
			res.Excluded[0].Reason, reuseconsent.ReasonNarrowed)
	}

	// It must not ride the event into chora_consumption either.
	if len(f.pub.events) != 1 {
		t.Fatalf("events = %d; want 1", len(f.pub.events))
	}
	for _, id := range f.pub.events[0].AtomIDs {
		if id == licensed {
			t.Fatalf("the sharer's licensed atom %s rode the fork into chora_consumption", licensed)
		}
	}

	// WHOSE consent was consulted? Never the sharer's. Without this assertion an
	// implementation that passed c.OwnerGcid to ConsentContext would satisfy every
	// other test in this file.
	for _, gcid := range f.consent.askedAbout() {
		if gcid == ownerA {
			t.Fatalf("the service resolved the COLLECTION OWNER's consent context (%s); "+
				"the ADR-229 disjunct must be evaluated against the FORKER (%s)", ownerA, forkerGCID)
		}
	}
}

// 🔴🔴 The derived study list belongs to the FORKER.
//
// Found by hand-probing the deployed service, NOT by any unit test — and no unit
// test could have found it before this story. NewCollectionConvertedToStudyList-
// Event took only the Collection, so it stamped the event's gcid from
// c.OwnerGcid. Under the WS-4 policy (actor == owner) that was TAUTOLOGICALLY
// correct: there was no input that could tell the two apart. The field was never
// asserted anywhere because it could not be wrong.
//
// Relaxing the policy is exactly what makes it able to be wrong, and the blast
// radius is on both sides of the fork:
//
//   - the FORKER gets NO study list — the fork returns 201, mints the grant, and
//     silently does nothing for the person who asked for it; and
//   - the SHARER gets a LearningPath they never asked for, carrying the FORKER's
//     entitled atom set. A stranger's fork WRITES INTO SOMEONE ELSE'S ACCOUNT,
//     and AppendAtom is idempotent-but-reopening, so it can be done repeatedly.
//
// chora_consumption reads owner_gcid (falling back to the envelope gcid) and
// makes it learning_paths.owner_gcid, so BOTH fields must name the ACTOR. The
// sharer is not lost: provenance rides source_id (the collection), per D2.
func TestFork_TheDerivedStudyListBelongsToTheForkerNotTheSharer(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	shared := makeAtomID(t, 1)
	c := f.seed(t, audience.Tenant, shared)
	f.facts.facts[shared] = tenantFact(shared)

	if _, err := f.svc.ConvertToStudyList(context.Background(), collection.ConvertToStudyListInput{
		TenantID: tenantA, CollectionID: c.CollectionID, ActorGCID: forkerGCID,
	}); err != nil {
		t.Fatalf("ConvertToStudyList: %v", err)
	}

	if len(f.pub.events) != 1 {
		t.Fatalf("events = %d; want 1", len(f.pub.events))
	}
	ev := f.pub.events[0]

	// The envelope gcid — whose ACTION this was.
	if ev.Gcid != forkerGCID {
		t.Errorf("event Gcid = %q; want the FORKER %q (got the sharer? %v). chora_consumption "+
			"builds the LearningPath for this learner — naming the sharer here gives the forker "+
			"nothing and writes a path into the sharer's account.",
			ev.Gcid, forkerGCID, ev.Gcid == ownerA)
	}
	// The payload owner_gcid — whose STUDY LIST this becomes. consumption reads
	// this field directly into learning_paths.owner_gcid.
	if ev.OwnerGcid != forkerGCID {
		t.Errorf("event OwnerGcid = %q; want the FORKER %q (got the sharer? %v). This field IS "+
			"learning_paths.owner_gcid downstream — it names the owner of the DERIVED LIST, not "+
			"the owner of the source collection.",
			ev.OwnerGcid, forkerGCID, ev.OwnerGcid == ownerA)
	}
	// Provenance is not lost: the source collection still identifies the sharer's
	// aggregate, and D2 routes provenance through source_id.
	if ev.CollectionID != c.CollectionID {
		t.Errorf("event CollectionID = %q; want %q — the fork must still name its source",
			ev.CollectionID, c.CollectionID)
	}
}

// The owner's own convert is unchanged: actor and owner coincide, so the derived
// list is theirs. This is the case that made the defect above invisible.
func TestFork_OwnConvertStillNamesTheOwnerOnTheEvent(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	own := makeAtomID(t, 1)
	c := f.seed(t, audience.Private, own)
	f.facts.facts[own] = ownFact(own)

	if _, err := f.svc.ConvertToStudyList(context.Background(), collection.ConvertToStudyListInput{
		TenantID: tenantA, CollectionID: c.CollectionID, ActorGCID: ownerA,
	}); err != nil {
		t.Fatalf("ConvertToStudyList: %v", err)
	}
	ev := f.pub.events[0]
	if ev.Gcid != ownerA || ev.OwnerGcid != ownerA {
		t.Errorf("event Gcid/OwnerGcid = %q/%q; want %q for the owner's own convert",
			ev.Gcid, ev.OwnerGcid, ownerA)
	}
}

// The D2 audit grant records who is NOW reusing the atom. On a fork that is the
// forker, not the sharer — a grant minted against the owner would attribute the
// reuse to the wrong learner and leave the forker's hold unrecorded, which is
// exactly the weak (bookmark) hold that conversion exists to strengthen.
func TestFork_MintsTheGrantAgainstTheForkerNotTheSharer(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	shared := makeAtomID(t, 1)
	c := f.seed(t, audience.Tenant, shared)
	f.facts.facts[shared] = tenantFact(shared)

	if _, err := f.svc.ConvertToStudyList(context.Background(), collection.ConvertToStudyListInput{
		TenantID: tenantA, CollectionID: c.CollectionID, ActorGCID: forkerGCID,
	}); err != nil {
		t.Fatalf("ConvertToStudyList: %v", err)
	}

	want := []string{forkerGCID + "|" + shared}
	got := f.authz.grants()
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("grants = %v; want %v — the GRANT_SCOPE_COLLECTION audit record names the FORKER", got, want)
	}
}

// =============================================================================
// The source aggregate is untouched (D9)
// =============================================================================

// Conversion is per-(collection, actor) state and lives on the FORKER's derived
// path. A fork must not write to the sharer's Collection at all — no
// converted_at, no stamp, no save. This is what makes the relaxed policy a pure
// POLICY change: chora-creation needs no migration for it.
func TestFork_DoesNotMutateTheSharersCollection(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	shared := makeAtomID(t, 1)
	narrowed := makeAtomID(t, 2)
	c := f.seed(t, audience.Tenant, shared, narrowed)
	f.facts.facts[shared] = tenantFact(shared)
	f.facts.facts[narrowed] = privateFact(narrowed)

	savesBefore := f.repo.saveCount()

	if _, err := f.svc.ConvertToStudyList(context.Background(), collection.ConvertToStudyListInput{
		TenantID: tenantA, CollectionID: c.CollectionID, ActorGCID: forkerGCID,
	}); err != nil {
		t.Fatalf("ConvertToStudyList: %v", err)
	}

	if got := f.repo.saveCount(); got != savesBefore {
		t.Errorf("the repository saved %d time(s) during a fork; want 0 — a non-owner convert "+
			"must never write to someone else's aggregate (ADR-233 D9)", got-savesBefore)
	}

	after, err := f.repo.Get(context.Background(), tenantA, c.CollectionID)
	if err != nil {
		t.Fatalf("Get after fork: %v", err)
	}
	if after.OwnerGcid != ownerA {
		t.Errorf("OwnerGcid = %q; want %q — a fork must not transfer ownership", after.OwnerGcid, ownerA)
	}
	// The excluded atom stays curated, so a later fork picks it up if the author
	// re-widens. D4: a source may never retract from a derived path, and a fork
	// may never retract from its source.
	if len(after.Atoms) != 2 {
		t.Errorf("collection holds %d atoms after the fork; want 2 — the source is not mutated",
			len(after.Atoms))
	}
}
