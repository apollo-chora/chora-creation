// CollectionService — the domain service that composes Collection mutators
// with Repository (persistence) + EventPublisher (Pub/Sub emission via outbox) +
// the ADR-229 reuse-consent gate (AtomReuseFactLookup + ConsentContextFetcher +
// CollectionUseAuthorizer).
//
// Per .claude/rules/ddd-enforcement.md Aggregate Invariant #3, cross-aggregate
// UUID references are validated through a domain service rather than a DB FK.
// For AddAtom that validation IS the gate: AtomReuseFactLookup.ReuseFacts is a
// tenant-scoped batch read over learning_atoms, so an atom that does not resolve
// in the caller's tenant is simply absent from the facts map and comes back as
// reuseconsent.ReasonAtomNotFound → ErrAtomDoesNotExist (HTTP 404).
//
// THERE IS NO AtomLookup PORT — and its absence is load-bearing. It used to run
//
//	SELECT tenant_id FROM learning_atoms WHERE atom_id = $1
//
// with NO tenant filter, on the bare pool (no RunInTenantTx), to answer "does
// this atom exist ANYWHERE in chora_creation, and who owns it?". Two things were
// wrong with that. It is exactly the untenanted read RLS exists to forbid — and
// it did not merely offend the rule, it BROKE: learning_atoms carries
// `tenant_id = (current_setting('chora.tenant_id', true))::uuid`, and on a pooled
// connection whose GUC has reset to the empty string, `”::uuid` throws 22P02.
// POST /api/v1/collections/{id}/atoms therefore 500'd for its entire production
// life and collection_atoms never held a single row. The other thing: what it
// returned — the atom's owning tenant — existed only to feed the BP-01
// cross-tenant rule, which ADR-233 D7 retired. Both of its jobs are gone, so it
// is gone, and the tenant-scoped gate does the work.
//
// FAIL-LOUD (ADR-229): every gate dependency is REQUIRED the moment a gate is
// needed. A sharing outage REFUSES the operation — it must never degrade the
// gate open (admitting atoms the author never consented to), nor silently narrow
// it (a 404 that is indistinguishable from "you are not a friend").
//
// Hexagonal: depends ONLY on the same package's ports + the two pure-domain
// value packages (audience, reuseconsent) — no adapter imports.
package collection

import (
	stdcontext "context"
	"errors"
	"fmt"

	"github.com/apollo-chora/chora-creation/internal/domain/audience"
	"github.com/apollo-chora/chora-creation/internal/domain/reuseconsent"
)

// Service is the domain orchestrator.
type Service struct {
	repo      Repository
	publisher EventPublisher

	// ADR-229 / ADR-233 D10 reuse-consent gate. `facts` is ALSO the atom-existence
	// oracle for AddAtom (see the package doc) — it is a tenant-scoped read, which
	// is precisely why the untenanted AtomLookup port could be deleted.
	facts   AtomReuseFactLookup
	consent ConsentContextFetcher
	authz   CollectionUseAuthorizer
}

// NewService wires the service. repo is always required; the remaining ports are
// required by the flows that use them and FAIL LOUD when a flow needs an unwired
// one (never a silent skip — see the package doc).
func NewService(
	repo Repository,
	publisher EventPublisher,
	facts AtomReuseFactLookup,
	consent ConsentContextFetcher,
	authz CollectionUseAuthorizer,
) *Service {
	return &Service{
		repo:      repo,
		publisher: publisher,
		facts:     facts,
		consent:   consent,
		authz:     authz,
	}
}

// ErrForbidden is returned when the caller is not the collection owner.
// Mapped to HTTP 403 at the handler boundary.
//
// NB: this is for WRITE / CONVERT authorization. A non-entitled READ returns
// ErrNotFound, never this — see GetVisible.
var ErrForbidden = errors.New("caller is not the collection owner")

// CreateInput is the parameter envelope for Create.
type CreateInput struct {
	TenantID    string
	OwnerGcid   string
	Title       string
	Description string
	Visibility  audience.Audience
	TraceParent string
	TraceState  string
}

// Create constructs a Collection, persists it, and publishes
// chora.creation.collection.created.v1.
func (s *Service) Create(ctx stdcontext.Context, in CreateInput) (*Collection, error) {
	c, err := New(NewParams{
		TenantID:    in.TenantID,
		OwnerGcid:   in.OwnerGcid,
		Title:       in.Title,
		Description: in.Description,
		Visibility:  in.Visibility,
	})
	if err != nil {
		return nil, err
	}
	if err := s.repo.Save(ctx, c); err != nil {
		return nil, fmt.Errorf("%w: Create save: %w", ErrRepository, err)
	}
	if s.publisher != nil {
		ev := NewCollectionCreatedEvent(c, in.TraceParent, in.TraceState)
		if err := s.publisher.Publish(ctx, ev); err != nil {
			return nil, fmt.Errorf("collection.Service.Create: publish: %w", err)
		}
	}
	return c, nil
}

// UpdateInput is the parameter envelope for Update.
type UpdateInput struct {
	TenantID     string
	CollectionID string
	OwnerGcid    string // for authorisation; must match c.OwnerGcid
	Params       UpdateParams
	TraceParent  string
	TraceState   string
}

// Update applies a PATCH to the Collection + publishes updated.v1.
// Returns ErrNotFound if the collection does not exist or belongs to a
// different tenant. Returns ErrForbidden when the caller is not the owner.
func (s *Service) Update(ctx stdcontext.Context, in UpdateInput) (*Collection, error) {
	c, err := s.repo.Get(ctx, in.TenantID, in.CollectionID)
	if err != nil {
		return nil, err
	}
	if c.OwnerGcid != in.OwnerGcid {
		return nil, ErrForbidden
	}
	if err := c.ApplyUpdate(in.Params); err != nil {
		return nil, err
	}
	if err := s.repo.Save(ctx, c); err != nil {
		return nil, fmt.Errorf("%w: Update save: %w", ErrRepository, err)
	}
	if s.publisher != nil {
		ev := NewCollectionUpdatedEvent(c, in.Params.ChangedFields(), in.TraceParent, in.TraceState)
		if err := s.publisher.Publish(ctx, ev); err != nil {
			return nil, fmt.Errorf("collection.Service.Update: publish: %w", err)
		}
	}
	return c, nil
}

// -----------------------------------------------------------------------------
// GetVisible  🔴 ADR-233 D8 — SECURITY FIX
// -----------------------------------------------------------------------------

// GetVisible returns the collection iff actorGCID may READ it:
//
//	owner ∪ tenant ∪ (friends ∧ owner ∈ actor's friend set)
//
// Before ADR-233 D8 this method took no actor at all: the handler never passed
// the caller's GCID, the service delegated to a repository GetVisible that was a
// pure passthrough to Get, and NOTHING anywhere checked owner or visibility.
// Any authenticated member of a tenant could read any collection in that tenant
// by ID — including another learner's PRIVATE collection (title, description,
// full ordered atom list). The docstring claimed the check existed, which is how
// it passed review.
//
// A non-entitled read returns ErrNotFound (404), NOT ErrForbidden (403): a 403
// would confirm the existence of a collection the caller may not see.
//
// The friend set is fetched ONLY for the `friends` audience — the one leg that
// needs it. Owner and tenant-wide reads are decided by the pure predicate, so
// the hot path pays no mesh round-trip and a chora-sharing outage cannot stop a
// learner reading their own collections. When the friend set IS load-bearing and
// cannot be fetched, the read FAILS LOUD rather than degrading into a 404 that
// is indistinguishable from an honest "not your friend".
func (s *Service) GetVisible(ctx stdcontext.Context, tenantID, actorGCID, collectionID string) (*Collection, error) {
	c, err := s.repo.Get(ctx, tenantID, collectionID)
	if err != nil {
		return nil, err
	}
	if _, err := s.viewGate(ctx, c, tenantID, actorGCID); err != nil {
		return nil, err
	}
	return c, nil
}

// viewGate is the D8 read predicate itself, extracted so the READ (GetVisible)
// and the FORK (ConvertToStudyList, ADR-233 D9 / CHO-2165) enforce the SAME rule
// from the same code rather than two copies that can drift apart. The WS-4 defect
// this predicate was written to fix was, precisely, a visibility check that
// existed only in a docstring.
//
// It returns the consent context it had to fetch, so a caller that needs the
// actor's friend set again — the fork does, for the per-atom disjunct — can reuse
// it instead of re-dialling chora-sharing. Two fetches would not just cost a
// round-trip: they could DISAGREE if a friendship were revoked between them,
// leaving the collection admitted under one friend set and its atoms judged
// against another.
//
// nil, nil means "visible, and no friend set was needed": the owner and tenant
// legs are decided by the pure predicate, so the hot path pays no mesh call and a
// chora-sharing outage cannot stop a learner reading their own collections.
func (s *Service) viewGate(
	ctx stdcontext.Context, c *Collection, tenantID, actorGCID string,
) (*reuseconsent.Context, error) {
	// Owner ∪ tenant — decidable without the friend set.
	if c.VisibleTo(actorGCID, nil) {
		return nil, nil
	}
	if c.Visibility != audience.Friends {
		// private, and the actor is not the owner.
		return nil, ErrNotFound
	}

	// friends — the friend set is the authorisation input. Fail loud.
	if s.consent == nil {
		return nil, fmt.Errorf("%w: view-gate consent fetcher; cannot resolve the friends audience (refusing rather than silently narrowing)", ErrGateNotWired)
	}
	cctx, err := s.consent.ConsentContext(ctx, actorGCID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("collection.Service.viewGate: consent context: %w", err)
	}
	if !c.VisibleTo(actorGCID, cctx.FriendGCIDs) {
		return nil, ErrNotFound
	}
	return &cctx, nil
}

// List returns active collections matching the filter. Used by GET
// /api/v1/me/collections (filter.OwnerGcid = caller's GCID).
func (s *Service) List(ctx stdcontext.Context, tenantID string, f ListFilter) ([]*Collection, error) {
	return s.repo.List(ctx, tenantID, f)
}

// -----------------------------------------------------------------------------
// AddAtom — with the ADR-229 D4.4 / ADR-233 D10b add-time gate
// -----------------------------------------------------------------------------

// AddAtomInput is the parameter envelope for AddAtom.
type AddAtomInput struct {
	TenantID     string
	CollectionID string
	OwnerGcid    string
	AtomID       string
	TraceParent  string
	TraceState   string
}

// AddAtom runs the ADR-229 add-time gate — which is BOTH the entitlement check
// and the existence check — then applies the aggregate mutator, persists, and
// publishes atom_added.v1.
//
// ONE tenant-scoped read decides both questions, because ADR-233 subsumed the two
// jobs the deleted AtomLookup port used to do (see the package doc):
//
//	existence — an atom absent from the tenant-scoped facts map does not resolve
//	            in this tenant. reuseconsent reports it as ReasonAtomNotFound,
//	            which maps to ErrAtomDoesNotExist (404). This subsumes the
//	            cross-tenant case for free: ReuseFacts is scoped to the caller's
//	            tenant, so another tenant's atom is never in the map at all.
//	entitlement — any OTHER non-entitled reason is ErrAtomNotReusable (403).
//
// Keeping those two apart matters: collapsing them would tell a learner "you may
// not reuse this" about an atom that does not exist.
//
// The gate mints NO grant and charges nothing: adding an atom to a collection is
// CURATION, not licensing (FR-030 / T068). The grant is minted at CONVERT, the
// moment the atom actually crosses into another domain.
func (s *Service) AddAtom(ctx stdcontext.Context, in AddAtomInput) (*Collection, error) {
	c, err := s.repo.Get(ctx, in.TenantID, in.CollectionID)
	if err != nil {
		return nil, err
	}
	if c.OwnerGcid != in.OwnerGcid {
		return nil, ErrForbidden
	}

	// ADR-229 add-time consent gate — and the atom-existence check.
	decision, err := s.evaluateOne(ctx, in.TenantID, in.OwnerGcid, in.AtomID)
	if err != nil {
		return nil, fmt.Errorf("collection.Service.AddAtom: reuse gate: %w", err)
	}
	if !decision.Entitled {
		if decision.Reason == reuseconsent.ReasonAtomNotFound {
			return nil, ErrAtomDoesNotExist
		}
		return nil, fmt.Errorf("%w: atom %s (%s)", ErrAtomNotReusable, in.AtomID, decision.Reason)
	}

	// The atom is in the CALLER's tenant by construction — the gate's read is
	// tenant-scoped, so anything from another tenant was already excluded above as
	// ATOM_NOT_FOUND. The aggregate's cross-tenant invariant is therefore
	// structurally unable to fire here; it is retained as defence-in-depth (it
	// still guards any other caller of Collection.AddAtom), and fed the tenant we
	// actually know rather than one we would have to make an untenanted read to
	// learn.
	if err := c.AddAtom(in.AtomID, AtomContext{TenantID: in.TenantID}); err != nil {
		return nil, err
	}
	if err := s.repo.Save(ctx, c); err != nil {
		return nil, fmt.Errorf("%w: AddAtom save: %w", ErrRepository, err)
	}
	addedAtom := c.Atoms[len(c.Atoms)-1]
	if s.publisher != nil {
		ev := NewCollectionAtomAddedEvent(c, addedAtom, in.TraceParent, in.TraceState)
		if err := s.publisher.Publish(ctx, ev); err != nil {
			return nil, fmt.Errorf("collection.Service.AddAtom: publish: %w", err)
		}
	}
	return c, nil
}

// evaluateOne runs the ADR-229 disjunct for a single atom. Both halves of the
// gate FAIL LOUD — see the package doc.
func (s *Service) evaluateOne(ctx stdcontext.Context, tenantID, actorGCID, atomID string) (reuseconsent.Decision, error) {
	// nil: AddAtom is owner-gated and runs no view gate, so nothing has resolved
	// this actor's consent context yet.
	facts, cctx, err := s.gateInputs(ctx, tenantID, actorGCID, []string{atomID}, nil)
	if err != nil {
		return reuseconsent.Decision{}, err
	}
	f, ok := facts[atomID]
	if !ok {
		// Absent from the TENANT-SCOPED read ⇒ the atom does not resolve in this
		// tenant: it never existed, was soft-deleted out from under the collection
		// (FK-less cross-aggregate refs, ddd-enforcement #3), or belongs to another
		// tenant. All three are honestly ATOM_NOT_FOUND, and the caller maps that to
		// a 404 — never an invented audience, and never a 403 that would imply the
		// atom exists.
		return reuseconsent.Decision{AtomID: atomID, Reason: reuseconsent.ReasonAtomNotFound}, nil
	}
	return reuseconsent.Evaluate(f, cctx), nil
}

// gateInputs fetches both halves of the disjunct. Missing ports and fetch errors
// are REFUSALS, never degradations.
// prefetched is the consent context the D8 view gate already resolved for THIS
// actor, or nil when nothing has resolved one yet. It exists so the fork path
// (CHO-2165) dials chora-sharing exactly ONCE: the friends leg of the view gate
// needs the actor's friend set, and so does the per-atom disjunct. Passing nil
// means "fetch it yourself" — what AddAtom, which runs no view gate, does.
func (s *Service) gateInputs(
	ctx stdcontext.Context, tenantID, actorGCID string, atomIDs []string,
	prefetched *reuseconsent.Context,
) (map[string]reuseconsent.AtomFact, reuseconsent.Context, error) {
	// An unwired port is a DEPLOYMENT defect (CHO-2175). It was already a refusal;
	// the sentinel is what stops that refusal from reaching the caller as a 400
	// telling them their request was malformed. It wasn't — our rollout was.
	if s.facts == nil {
		return nil, reuseconsent.Context{}, fmt.Errorf(
			"%w: atom reuse-fact lookup (an unwired gate must never pass)", ErrGateNotWired)
	}
	if s.consent == nil {
		return nil, reuseconsent.Context{}, fmt.Errorf(
			"%w: consent-context fetcher (an unwired gate must never pass)", ErrGateNotWired)
	}
	// A LOCAL datastore failure. This is the CHO-2175 path, and the shape CHO-2173
	// wore in prod: the raw pgx error used to reach A+ as a 400 blaming the
	// caller. It is ours, it is a 5xx, and the gate stays SHUT.
	facts, err := s.facts.ReuseFacts(ctx, tenantID, atomIDs)
	if err != nil {
		return nil, reuseconsent.Context{}, fmt.Errorf("%w: reuse facts: %w", ErrRepository, err)
	}
	if prefetched != nil {
		return facts, *prefetched, nil
	}
	// An UPSTREAM (chora-sharing) failure. The client adapter has already wrapped
	// this in one of the ADR-229 verdict sentinels (ErrSharingUnavailable et al);
	// %w keeps it reachable by errors.Is, so it keeps its own 502/403.
	cctx, err := s.consent.ConsentContext(ctx, actorGCID, tenantID)
	if err != nil {
		return nil, reuseconsent.Context{}, fmt.Errorf("consent context: %w", err)
	}
	return facts, cctx, nil
}

// -----------------------------------------------------------------------------
// RemoveAtom / Delete
// -----------------------------------------------------------------------------

// RemoveAtomInput is the parameter envelope for RemoveAtom.
type RemoveAtomInput struct {
	TenantID     string
	CollectionID string
	OwnerGcid    string
	AtomID       string
	TraceParent  string
	TraceState   string
}

// RemoveAtom drops an atom + publishes atom_removed.v1.
func (s *Service) RemoveAtom(ctx stdcontext.Context, in RemoveAtomInput) (*Collection, error) {
	c, err := s.repo.Get(ctx, in.TenantID, in.CollectionID)
	if err != nil {
		return nil, err
	}
	if c.OwnerGcid != in.OwnerGcid {
		return nil, ErrForbidden
	}
	if err := c.RemoveAtom(in.AtomID); err != nil {
		return nil, err
	}
	if err := s.repo.Save(ctx, c); err != nil {
		return nil, fmt.Errorf("%w: RemoveAtom save: %w", ErrRepository, err)
	}
	if s.publisher != nil {
		ev := NewCollectionAtomRemovedEvent(c, in.AtomID, in.TraceParent, in.TraceState)
		if err := s.publisher.Publish(ctx, ev); err != nil {
			return nil, fmt.Errorf("collection.Service.RemoveAtom: publish: %w", err)
		}
	}
	return c, nil
}

// DeleteInput is the parameter envelope for Delete.
type DeleteInput struct {
	TenantID      string
	CollectionID  string
	OwnerGcid     string // for authorisation
	DeletedByGcid string // actor (typically same as OwnerGcid; admin paths later)
	TraceParent   string
	TraceState    string
}

// Delete soft-deletes the Collection + publishes deleted.v1.
func (s *Service) Delete(ctx stdcontext.Context, in DeleteInput) error {
	c, err := s.repo.Get(ctx, in.TenantID, in.CollectionID)
	if err != nil {
		return err
	}
	if c.OwnerGcid != in.OwnerGcid {
		return ErrForbidden
	}
	if err := c.SoftDelete(); err != nil {
		return err
	}
	if err := s.repo.Save(ctx, c); err != nil {
		return fmt.Errorf("%w: Delete save: %w", ErrRepository, err)
	}
	actor := in.DeletedByGcid
	if actor == "" {
		actor = in.OwnerGcid
	}
	if s.publisher != nil {
		ev := NewCollectionDeletedEvent(c, actor, in.TraceParent, in.TraceState)
		if err := s.publisher.Publish(ctx, ev); err != nil {
			return fmt.Errorf("collection.Service.Delete: publish: %w", err)
		}
	}
	return nil
}

// -----------------------------------------------------------------------------
// ConvertToStudyList — ADR-233 D9 / D10a / D11
// -----------------------------------------------------------------------------

// ConvertToStudyListInput is the parameter envelope for ConvertToStudyList.
//
// ActorGCID is an EXPLICIT input (ADR-233 D9) rather than an implied owner —
// which is what let CHO-2165 land the policy "actor may VIEW the collection" as
// a pure policy change. Combined with VisibleTo (D8) and the per-atom disjunct
// evaluated against the ACTOR's GCID, forking a shared collection is safe BY
// CONSTRUCTION: the sharer's entitlements never travel with the list.
type ConvertToStudyListInput struct {
	TenantID     string
	CollectionID string
	ActorGCID    string
	TraceParent  string
	TraceState   string
}

// ConvertResult is what the handler renders. Excluded atoms are NAMED, with a
// reason code — ADR-233 D11 forbids dropping an atom silently.
type ConvertResult struct {
	StudyListEventID string
	AtomCount        int
	Excluded         []reuseconsent.Decision
}

// ConvertToStudyList converts a Collection into a spaced-repetition study list
// by publishing chora.creation.collection.converted_to_study_list.v1;
// chora-consumption builds the LearningPath from it (no cross-DB read — the
// atom_ids travel in the payload).
//
// Entitlement is evaluated PER ATOM against the ACTOR (never per collection), so
// a mixed collection converts partially, with the excluded atoms named and
// reasoned in the result. Zero survivors is a REFUSAL (ErrNoEntitledAtoms → 409):
// emitting an empty study list would be fabricating a success.
//
// Conversion is the moment a weak (bookmark) hold becomes a strong (licensed,
// orphan-protected) one — hence the GRANT_SCOPE_COLLECTION grant on the
// tenant-visible leg. Before it, the author can still withdraw; after it,
// consumed-continuity applies and the learner is protected by the singleton-
// orphan machinery (ADR-229 A1.1).
//
// The Collection is NOT mutated (D9) — see Collection.ConvertToStudyList.
func (s *Service) ConvertToStudyList(ctx stdcontext.Context, in ConvertToStudyListInput) (ConvertResult, error) {
	c, err := s.repo.Get(ctx, in.TenantID, in.CollectionID)
	if err != nil {
		return ConvertResult{}, err
	}

	// Convert authorization is a POLICY, not an aggregate invariant (ADR-233 D9),
	// and CHO-2165 lands the reserved half: the policy is "the actor may VIEW the
	// collection". So it IS the D8 read predicate — the same viewGate the read
	// path runs, not a restatement of it, because a fork and a read must never
	// diverge on who may see a collection.
	//
	// A collection the actor cannot see therefore refuses here exactly as it does
	// on the read path — ErrNotFound (404), never ErrForbidden: a 403 confirms the
	// existence of a collection the caller may not see.
	//
	// Authorization precedes the work. A caller who cannot see the collection
	// never reaches the reuse gate, mints no grant, and publishes nothing.
	viewCtx, err := s.viewGate(ctx, c, in.TenantID, in.ActorGCID)
	if err != nil {
		return ConvertResult{}, err
	}

	order := c.AtomIDsInOrder()
	if len(order) == 0 {
		return ConvertResult{}, ErrNoEntitledAtoms
	}

	// viewCtx is non-nil only on the friends leg, where the gate already resolved
	// this actor's consent context. Reuse it: re-dialling would cost a second
	// round-trip AND risk judging the atoms against a friend set that no longer
	// matches the one the collection was admitted under.
	facts, cctx, err := s.gateInputs(ctx, in.TenantID, in.ActorGCID, order, viewCtx)
	if err != nil {
		return ConvertResult{}, fmt.Errorf("collection.Service.ConvertToStudyList: reuse gate: %w", err)
	}

	res := reuseconsent.EvaluateAll(order, facts, cctx)
	if len(res.EntitledAtomIDs) == 0 {
		return ConvertResult{}, ErrNoEntitledAtoms
	}

	// Mint the ADR-229 D2 audit grant on every newly-reused leg, BEFORE the event
	// is emitted. A write failure REFUSES the conversion — the audit record is
	// not optional, and a study list built on unrecorded reuse is exactly the
	// one-way door D10 exists to close.
	if len(res.NeedGrantAtomIDs) > 0 {
		if s.authz == nil {
			return ConvertResult{}, fmt.Errorf("%w: ConvertToStudyList collection-use authorizer (the D2 audit record is not optional)", ErrGateNotWired)
		}
		for _, atomID := range res.NeedGrantAtomIDs {
			if err := s.authz.AuthorizeCollectionUse(ctx, in.TenantID, in.ActorGCID, atomID); err != nil {
				return ConvertResult{}, fmt.Errorf("collection.Service.ConvertToStudyList: authorize collection use of atom %s: %w", atomID, err)
			}
		}
	}

	entitled, err := c.ConvertToStudyList(res.EntitledAtomIDs)
	if err != nil {
		return ConvertResult{}, err
	}

	// in.ActorGCID, not c.OwnerGcid: the derived study list belongs to whoever
	// forked it. See NewCollectionConvertedToStudyListEvent.
	ev := NewCollectionConvertedToStudyListEvent(c, in.ActorGCID, entitled, in.TraceParent, in.TraceState)
	if s.publisher == nil {
		return ConvertResult{}, fmt.Errorf(
			"%w: ConvertToStudyList event publisher (a conversion nobody hears is not a conversion)", ErrGateNotWired)
	}
	if err := s.publisher.Publish(ctx, ev); err != nil {
		return ConvertResult{}, fmt.Errorf("collection.Service.ConvertToStudyList: publish: %w", err)
	}

	return ConvertResult{
		StudyListEventID: ev.StudyListEventID,
		AtomCount:        len(entitled),
		Excluded:         res.Excluded,
	}, nil
}
