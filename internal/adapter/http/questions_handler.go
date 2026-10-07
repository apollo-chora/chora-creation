// questions_handler.go — HTTP handlers for the Question authoring endpoints
// mounted under /api/atoms/{atom_id}/questions[...].
//
// Per P3-B in /Users/daleleung/.claude/plans/golden-hopping-owl.md:
//
//   - POST   /api/atoms/{atom_id}/questions                    manual create (201)
//   - PATCH  /api/atoms/{atom_id}/questions/{question_id}      edit -> AppendRevision (200)
//   - GET    /api/atoms/{atom_id}/questions/{question_id}      admin AUTHOR projection (200)
//
// Hexagonal contract: depends on ports.QuestionRepository + atom.Repository
// only. The pgx adapter is wired in cmd/server/main.go. No infrastructure
// imports here.
//
// Error envelope codes (match the gateway pattern):
//
//	CREATION_QUESTION_INVALID_TYPE      — type mismatch with parent atom
//	CREATION_QUESTION_PAYLOAD_INVALID   — payload validation failure (missing options, etc.)
//	CREATION_QUESTION_DUPLICATE         — D2 violation (1 atom = 1 non-deleted question)
//	CREATION_QUESTION_NOT_FOUND         — question or atom does not exist
//	CREATION_QUESTION_TYPE_NOT_ENABLED  — reserved_* type — 501 NOT_IMPLEMENTED
package httpadapter

import (
	"context"
	"errors"
	"log"
	mathrand "math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-common/auth/servicemesh"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// QuestionsHandler exposes the /api/atoms/{atom_id}/questions[...] routes
// plus the cross-atom picker leaf at /api/atoms/questions/search (FE B-FE-X5;
// ADR-155 D1).
type QuestionsHandler struct {
	repo     ports.QuestionRepository
	atomRepo atom.Repository
	// publisher emits the chora.creation.atom.created.v1 upsert after a
	// question create/edit so atom_index learns the MCQ grading ground-truth
	// (correct_option_id + answer_count) — the same CHO-1627 contract the
	// qgen accept path honours. Nil ⇒ no emission (unit-test servers without
	// eventing); production ALWAYS wires deps.JobEventPublisher.
	publisher JobEventPublisher
	// mediaDownloadSigner mints fresh short-lived signed GET URLs for durable
	// gs:// atom-media refs on the author re-open projection (CHO-1638). May be
	// nil in tests / when the signer is not wired — minting is then a no-op.
	mediaDownloadSigner ports.AtomMediaDownloadSigner
	// savedAtomFetcher fetches the caller bookmarked atom IDs from
	// chora-sharing for the picker saved disjunct (ux_unified_atom_picker.md).
	// Nil => saved source returns empty (tests / dev without sharing wired).
	savedAtomFetcher ports.SavedAtomIDFetcher
	// reuseContext reads the caller's ADR-229 consent context (active-grant
	// atom ids + friend set) once per picker search (WS-2 chokepoint 1,
	// CHO-2133). Required for source=all/saved: nil fails those searches
	// loud (500 CREATION_REUSE_CONTEXT_NOT_WIRED); a fetch error fails loud
	// (502 CREATION_REUSE_CONTEXT_UNAVAILABLE). NEVER best-effort — the
	// context is the authorisation input for the consent disjunct.
	reuseContext ports.ReuseContextFetcher
	// reHomer durably re-homes a saved revision's transient W8 images into
	// chora-atom-media on save-revision (ADR-210), so a saved-but-unpublished
	// image survives the 7-day transient TTL. Shared with the atom + jobs
	// handlers; nil ⇒ re-home is skipped (images stay transient).
	reHomer *questionImageReHomer
}

// NewQuestionsHandler constructs the handler over the two repository ports plus
// the (optional) atom-media download signer used to mint durable gs:// image
// refs on the author projection and the sharing-backed consent fetchers for
// the picker's saved + granted disjuncts (ADR-229 WS-2).
func NewQuestionsHandler(repo ports.QuestionRepository, atomRepo atom.Repository, signer ports.AtomMediaDownloadSigner, publisher JobEventPublisher, savedFetcher ports.SavedAtomIDFetcher, reuseFetcher ports.ReuseContextFetcher) *QuestionsHandler {
	return &QuestionsHandler{repo: repo, atomRepo: atomRepo, mediaDownloadSigner: signer, publisher: publisher, savedAtomFetcher: savedFetcher, reuseContext: reuseFetcher}
}

// emitAtomUpsert publishes the atom.created.v1 upsert that backfills
// atom_index with the parent atom's CURRENT metadata + (for MCQ) the grading
// ground-truth. Mirrors the qgen accept path's payload keys (CHO-1627) and
// ALWAYS carries course_id when bound — omitting it would wipe the course
// binding consumption's projection relies on for path bootstrap. Returns the
// publish error so callers fail loud (a dropped key silently mis-grades every
// subsequent take).
func (h *QuestionsHandler) emitAtomUpsert(ctx context.Context, a *atom.LearningAtom, qType question.QuestionType, mcq *question.MCQPayload) error {
	if h.publisher == nil {
		return nil
	}
	payload := map[string]any{
		"atom_id":     a.AtomID,
		"tenant_id":   a.TenantID,
		"author_gcid": a.Gcid,
		"title":       a.Title,
		"tags":        a.Tags,
		"difficulty":  a.Difficulty,
		"atom_type":   string(a.QuestionType),
		"status":      string(a.Status),
		"created_at":  a.CreatedAt.Format(time.RFC3339Nano),
	}
	if a.CourseID != "" {
		payload["course_id"] = a.CourseID
	}
	if qType == question.TypeMCQ && mcq != nil {
		// CHO-2272 — ship the DERIVED served id (what the learner is graded on),
		// not the stored id. Fail loud: an underivable payload ships no key
		// (non-gradable) rather than a wrong one that mis-grades every take.
		if cid, cerr := mcq.ServedCorrectOptionID(); cerr != nil {
			log.Printf("emitAtomUpsert: cannot derive served correct id for atom=%s (non-gradable): %v", a.AtomID, cerr)
		} else if cid != "" {
			payload["correct_option_id"] = cid
		}
		payload["answer_count"] = int32(mcq.AnswerCount())
	}
	return h.publisher.PublishJobEvent(ctx, topicAtomCreated, payload)
}

// backfillAtomIndex re-emits the chora.creation.atom.created.v1 UPSERT for every
// currently-published, non-orphan atom of a tenant — the event that CREATES the
// chora_consumption.atom_index row (INSERT ON CONFLICT), carrying the DERIVED
// grading key. It exists because atom.published.v1 (backfillPublished) is
// UPDATE-only and cannot seed a MISSING row: a census on 2026-07-18 found 60
// published, gradeable, non-orphan MCQ atoms (all manual-authored) with NO
// atom_index row at all, so they were startable but 422-not-gradeable. This
// reuses emitAtomUpsert byte-for-byte (same proven wire shape — no new event),
// so a re-run is idempotent (an existing published row keeps its protected key;
// a missing row is inserted).
//
// Cluster-INTERNAL (POST, tenant from ?tenant_id= / JSON body per
// middleware.isPublicPath). Orphan editions are skipped — ADR-229 A1: they must
// never gain a projection row. An atom with no resolvable question is skipped
// (nothing to project). Per-atom emit failures are logged + counted as skipped;
// the run does not abort. The publisher is REQUIRED (fail-loud 500 when unwired —
// a backfill that cannot emit is meaningless).
func (h *QuestionsHandler) backfillAtomIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only POST is supported on /api/internal/atoms/backfill-atom-index")
		return
	}
	tenantID := backfillTenantID(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "CREATION_TENANT_REQUIRED",
			"tenant_id is required (query param ?tenant_id= or JSON body {\"tenant_id\":..})")
		return
	}
	if h.publisher == nil {
		writeError(w, http.StatusInternalServerError, "CREATION_PUBLISHER_NOT_WIRED",
			"atom.created upsert publisher is not wired; backfill requires it")
		return
	}
	if h.atomRepo == nil || h.repo == nil {
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_NOT_WIRED",
			"atom/question repositories are not wired")
		return
	}

	ctx := r.Context()
	atoms, err := h.atomRepo.List(ctx, tenantID, atom.ListFilter{Status: atom.StatusPublished})
	if err != nil {
		log.Printf("backfillAtomIndex: list published atoms (tenant=%s): %v", tenantID, err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to list published atoms")
		return
	}

	emitted, skipped := 0, 0
	for _, a := range atoms {
		// Defence beyond the List filter: only PUBLISHED, non-orphan atoms get a
		// learner projection row (ADR-229 A1 — orphans never do).
		if a.Status != atom.StatusPublished || a.IsOrphan() {
			skipped++
			continue
		}
		q, _, qerr := h.repo.GetByAtomID(ctx, tenantID, a.AtomID)
		if qerr != nil || q == nil {
			// No resolvable question (or a lookup error) — nothing to project.
			skipped++
			continue
		}
		if eerr := h.emitAtomUpsert(ctx, a, q.Type, q.MCQ); eerr != nil {
			log.Printf("backfillAtomIndex: emit atom.created failed (atom=%s, not queued): %v", a.AtomID, eerr)
			skipped++
			continue
		}
		emitted++
	}

	log.Printf("backfillAtomIndex: tenant=%s published=%d emitted=%d skipped=%d", tenantID, len(atoms), emitted, skipped)
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant_id": tenantID,
		"emitted":   emitted,
		"skipped":   skipped,
	})
}

// HandleSearchQuestions serves GET /api/atoms/questions/search — the
// cross-atom Question picker for the A+ X.2 test-set editor.
//
// Wire shape per chora-contracts/openapi/creation-questions.yaml#searchQuestions
// + FE ask B-FE-X5 (2026-05-16) — query params:
//
//	q             — optional substring needle (case-insensitive; matches title OR body)
//	tenant_id     — optional explicit tenant; falls back to header X-Tenant-Id
//	question_type — OpenAPI canonical multi-value filter; explode=true repeats
//	                the key (?question_type=mcq&question_type=oe). Preferred FE
//	                shape per ADR-155 D1.
//	types         — back-compat alias: comma-separated atom_type list (mcq,oe,...).
//	                Predates the OpenAPI spec; kept so existing internal callers
//	                don't break during FE rollover. Both shapes union into
//	                filter.Types; SearchFilter.Normalize() dedupes.
//	author_gcid   — exact match on the atom author/owner (`gcid` column; UUID).
//	tag           — repeated free-form tag (OR within family; JSONB `?|` GIN).
//	state         — repeated lifecycle state DRAFT|PUBLISHED|ARCHIVED → DB
//	                atom_status enum (status IN). When absent, no status
//	                predicate (all non-deleted atoms — the pre-CHO-1899 default;
//	                the contract's "default PUBLISHED-only" is deliberately NOT
//	                applied to avoid hiding drafts the live picker relies on).
//	atom_id       — repeated atom UUID (restrict to these atoms; OR in family).
//	sort          — `{field}:{dir}` (created_at|updated_at|prompt × asc|desc),
//	                comma-separated multi-key; default created_at:desc.
//	page          — 1-based page index (default 1)
//	per           — page size (default 20, max 100)
//
// DEFERRED — fail loud (400), never silent-ignore:
//
//	topic_node_id — no persisted KG-placement column on learning_atoms.
//	usable_by_gcid / include_usable=true — needs atom-sharing Phase-2
//	                (chora-sharing ListEntitledAtoms is notImplementedYet).
//
// Envelope:
//
//	{ "items": [...], "page": int, "per": int, "total": int }
//
// Authz: handler trusts the upstream mesh-trust pattern (Istio AuthorizationPolicy
// allow-from-gateway), the gateway header injection (gcid/X-Tenant-Id), and
// RLS at the DB layer. Cross-tenant `tenant_id` query param is rejected as
// 400 here as a fail-loud defence-in-depth (RLS would otherwise silently
// return 0 rows).
func (h *QuestionsHandler) HandleSearchQuestions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only GET is supported on /api/atoms/questions/search")
		return
	}

	ctxTenant := tenantFromContext(r.Context())
	q := r.URL.Query()

	tenantID := strings.TrimSpace(q.Get("tenant_id"))
	if tenantID == "" {
		tenantID = ctxTenant
	}
	if tenantID != ctxTenant {
		writeError(w, http.StatusBadRequest, "CREATION_TENANT_MISMATCH",
			"tenant_id query parameter must match caller tenant (defence-in-depth; RLS enforces at DB)")
		return
	}

	// Fail loud on the contract params that have no persisted column. A
	// silent no-op would make the picker LOOK like it filtered when it
	// didn't (CHO-1899; never silently ignore).
	if len(q["topic_node_id"]) > 0 {
		writeError(w, http.StatusBadRequest, "CREATION_SEARCH_FILTER_UNSUPPORTED",
			"topic_node_id filter not yet supported (no persisted KG placement; deferred to a follow-on slice)")
		return
	}
	// ADR-229 WS-2: the reuse-consent gate is ALWAYS on server-side —
	// `include_usable=true` is truthfully satisfied (every returned row is
	// usable by the caller), so it is accepted as a no-op. Filtering by an
	// ARBITRARY user's usability stays unsupported: the consent context is
	// caller-scoped (GetReuseContext(gcid)), so `usable_by_gcid` still
	// fails loud rather than silently impersonating.
	if strings.TrimSpace(q.Get("usable_by_gcid")) != "" {
		writeError(w, http.StatusBadRequest, "CREATION_SEARCH_FILTER_UNSUPPORTED",
			"usable_by_gcid is unsupported — usability is caller-scoped (ADR-229 D4.1); results are already narrowed to atoms the caller may reuse")
		return
	}

	// E2E-BE-6 — union the OpenAPI-canonical `question_type` repeated param
	// with the legacy `types` CSV alias, then translate the QuestionType
	// wire-enum values (mcq/oe/reserved_*) to the chora_creation.atom_type
	// DB-enum values (mcq/flashcard/video/essay/outline). Wire `oe` ↔ DB
	// `essay` per protomarshal.go#L781 + questions_handler.go#L322 — the SQL
	// `WHERE atom_type IN ('oe',...)` predicate would otherwise trip
	// SQLSTATE 22P02 (invalid input value for enum atom_type: "oe").
	// SearchFilter.Normalize() lowercases + dedupes after translation.
	rawTypes := append(parseCommaSeparated(q.Get("types")), q["question_type"]...)
	types := translateQuestionTypesToAtomTypes(rawTypes)
	page := parsePositiveInt(q.Get("page"), 0)
	per := parsePositiveInt(q.Get("per"), 0)

	callerGCID := gcidFromContext(r.Context())
	source := question.Source(q.Get("source"))

	// CHO-1899 extended filters. author_gcid + atom_id[] are UUID columns —
	// validate at the boundary so a malformed value is a clean 400 (the
	// `::uuid` cast would otherwise 22P02 → 500).
	authorGCID := strings.TrimSpace(q.Get("author_gcid"))
	if authorGCID != "" {
		if _, err := uuid.Parse(authorGCID); err != nil {
			writeError(w, http.StatusBadRequest, "CREATION_SEARCH_INVALID_FILTER",
				"author_gcid must be a valid UUID")
			return
		}
	}
	atomIDs := q["atom_id"]
	for _, id := range atomIDs {
		if _, err := uuid.Parse(strings.TrimSpace(id)); err != nil {
			writeError(w, http.StatusBadRequest, "CREATION_SEARCH_INVALID_FILTER",
				"atom_id must be a valid UUID")
			return
		}
	}

	// state[] — translate contract enum (DRAFT/PUBLISHED/ARCHIVED) → DB
	// atom_status enum (draft/published/archived); unknowns dropped (mirror
	// the question_type translation; the `$N::atom_status` cast would 22P02).
	states := translateStatesToAtomStatus(q["state"])

	// sort — parse the contract `{field}:{dir}` grammar; 400 on malformed or
	// non-whitelisted field/direction (no raw column reaches the SQL).
	sorts, serr := question.ParseSorts(q.Get("sort"))
	if serr != nil {
		writeError(w, http.StatusBadRequest, "CREATION_SEARCH_INVALID_SORT", serr.Error())
		return
	}

	filter := question.SearchFilter{
		Q:          q.Get("q"),
		TenantID:   tenantID,
		Types:      types,
		Page:       page,
		Per:        per,
		Source:     source,
		CallerGCID: callerGCID,
		AuthorGCID: authorGCID,
		Tags:       q["tag"], // free-form; Normalize trims + dedupes (case preserved)
		States:     states,
		AtomIDs:    atomIDs,
		Sorts:      sorts,
	}.Normalize()

	// ADR-229 WS-2 chokepoint 1 (CHO-2133) — hydrate the consent disjunct.
	// source=all/saved evaluate the entitled set E = mine ∪ tenant-visible ∪
	// granted, whose granted leg needs the caller's active-grant atom ids
	// from chora-sharing GetReuseContext (once per search, the
	// ListSavedAtomIDs pattern). FAIL LOUD on any gap: an unreadable consent
	// context must never degrade into a silently-narrowed (mine-only) or
	// wide-open picker. source=mine needs no context (own atoms only).
	//
	// Amendment A1.4 seam: the response also carries FriendGCIDs — they are
	// deliberately NOT threaded into the filter until ADR-230 B-lite.2
	// un-hides the `friends` audience (the SQL leg is documented in
	// question_search.go).
	if filter.Source != question.SourceMine && callerGCID != "" {
		if h.reuseContext == nil {
			log.Printf("questions: search: reuse-context fetcher NOT WIRED (SVC_SHARING_GRPC_URL unset?) — refusing consent-gated search (ADR-229)")
			writeError(w, http.StatusInternalServerError, "CREATION_REUSE_CONTEXT_NOT_WIRED",
				"reuse-consent context dependency is not wired — the picker cannot evaluate the ADR-229 gate")
			return
		}
		rc, err := h.reuseContext.GetReuseContext(r.Context(), callerGCID, tenantID)
		if err != nil {
			log.Printf("questions: search: reuse-context fetch FAILED (refusing, never degrading): %v", err)
			writeError(w, http.StatusBadGateway, "CREATION_REUSE_CONTEXT_UNAVAILABLE",
				"failed to read the reuse-consent context from chora-sharing — retry")
			return
		}
		filter.GrantedAtomIDs = rc.GrantedAtomIDs
	}

	// Fetch the caller's saved-atom IDs from chora-sharing when the source
	// includes `saved`. Best-effort — on failure the saved disjunct is empty
	// and partial_result=true is surfaced so the FE can warn. (The consent
	// context above already failed loud if sharing is down outright.)
	partialResult := false
	savedTruncated := false
	if filter.Source == question.SourceSaved || filter.Source == question.SourceAll {
		if h.savedAtomFetcher != nil && callerGCID != "" {
			ids, truncated, err := h.savedAtomFetcher.ListSavedAtomIDs(r.Context(), callerGCID, tenantID)
			if err != nil {
				log.Printf("questions: search: saved-atom fetch failed (saved badge/leg empty; partial_result=true): %v", err)
				partialResult = true
			} else {
				filter.SavedAtomIDs = ids
				savedTruncated = truncated
			}
		}
	}

	if err := filter.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_SEARCH_INVALID_FILTER", err.Error())
		return
	}

	items, total, err := h.repo.SearchQuestions(r.Context(), filter)
	if err != nil {
		log.Printf("questions: search: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR",
			"failed to search questions")
		return
	}
	if items == nil {
		items = []question.SearchResult{}
	}
	writeJSON(w, http.StatusOK, questionSearchResponse{
		Items:          items,
		Page:           filter.Page,
		Per:            filter.Per,
		Total:          total,
		PartialResult:  partialResult,
		SavedTruncated: savedTruncated,
	})
}

// questionSearchResponse is the FE B-FE-X5 wire envelope. Distinct from the
// OpenAPI QuestionSearchResponse (cursor-paginated per ADR-155 D1) — the FE
// ask collapses to a page+per+total envelope for the V1 picker.
type questionSearchResponse struct {
	Items          []question.SearchResult `json:"items"`
	Page           int                     `json:"page"`
	Per            int                     `json:"per"`
	Total          int                     `json:"total"`
	PartialResult  bool                    `json:"partial_result"`
	SavedTruncated bool                    `json:"saved_truncated"`
}

// questionTypeToAtomType maps the OpenAPI QuestionType wire-enum
// (creation-questions.yaml#QuestionType — `mcq`, `oe`, 14× `reserved_*`) to
// the chora_creation.atom_type DB-enum
// (`mcq`,`flashcard`,`video`,`essay`,`outline`). Per the canonical mapping
// at protomarshal.go#L781 + questions_handler.go#L322:
//
//	wire `mcq`         → DB `mcq`
//	wire `oe`          → DB `essay`
//	wire `flashcard`   → DB `flashcard` (legacy types= alias path)
//	wire `video`       → DB `video`     (legacy types= alias path)
//	wire `outline`     → DB `outline`   (legacy types= alias path)
//	wire `reserved_*`  → DROPPED (no atom_type analog yet)
//	wire `<unknown>`   → DROPPED (defence-in-depth against SQLSTATE 22P02)
//
// E2E-BE-6: keeping the translation table here at the HTTP boundary
// preserves the domain's pure-DDD posture — SearchFilter stays as an opaque
// list of strings the pg adapter renders verbatim into WHERE atom_type IN.
var questionTypeToAtomType = map[string]string{
	"mcq":       "mcq",
	"oe":        "essay",
	"essay":     "essay",
	"flashcard": "flashcard",
	"video":     "video",
	"outline":   "outline",
}

// translateQuestionTypesToAtomTypes converts the inbound mixed (wire / legacy)
// type filter values into the canonical chora_creation.atom_type DB enum
// values. Unknown values (reserved_*, typos) are silently dropped — they
// have no DB-enum analog yet and the SQL would otherwise panic with
// SQLSTATE 22P02. The downstream SearchFilter.Normalize() lowercases +
// dedupes the result.
func translateQuestionTypesToAtomTypes(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, t := range in {
		key := strings.ToLower(strings.TrimSpace(t))
		if key == "" {
			continue
		}
		atomType, ok := questionTypeToAtomType[key]
		if !ok {
			continue // drop reserved_* / unknown
		}
		out = append(out, atomType)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// stateToAtomStatus maps the OpenAPI searchQuestions `state` enum
// (DRAFT/PUBLISHED/ARCHIVED) onto the chora_creation.atom_status DB-enum
// (draft/published/archived). Keys are lowercased before lookup.
var stateToAtomStatus = map[string]string{
	"draft":     "draft",
	"published": "published",
	"archived":  "archived",
}

// translateStatesToAtomStatus converts the inbound `state` filter values into
// chora_creation.atom_status DB-enum values. Unknown values are silently
// dropped (mirrors translateQuestionTypesToAtomTypes) — the SQL builder casts
// each to `$N::atom_status`, which would panic with SQLSTATE 22P02 on a bad
// value. An empty result means "no status predicate" (match all states),
// preserving the pre-CHO-1899 behaviour where the picker showed every
// non-deleted atom regardless of lifecycle state.
func translateStatesToAtomStatus(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		key := strings.ToLower(strings.TrimSpace(s))
		if key == "" {
			continue
		}
		if v, ok := stateToAtomStatus[key]; ok {
			out = append(out, v)
		}
		// unknown → dropped (no atom_status analog; would trip 22P02)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseBoolTrue reports whether the query value is an explicit truthy token.
// Absent / empty / "false" → false (so `include_usable=false`, the
// catalogue-browse default, is a no-op rather than a fail-loud rejection).
func parseBoolTrue(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// parseCommaSeparated splits "a,b,c" into ["a","b","c"]. Empty input yields nil.
func parseCommaSeparated(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parsePositiveInt parses a base-10 integer; returns fallback when empty /
// invalid / negative.
func parsePositiveInt(s string, fallback int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return fallback
		}
		n = n*10 + int(c-'0')
		if n > 1_000_000 {
			return fallback
		}
	}
	return n
}

// hasAuthorRole reports whether the caller may use the authoring surface.
//
// It mirrors the BFF's phyllis.AuthCtx.HasAuthorRole (author | instructor |
// admin | owner | tenant_admin) — deliberately the SAME predicate that already
// gates the author-mode fields (correct_option_id on question_payload) on GET
// /api/atoms/{id}. Both doors expose the same secret, so they must agree on who
// may see it; widening this set is a decision for BOTH doors, not a local
// convenience.
//
// CHO-2254 gated this subtree to author|instructor only. That left the tenant's
// own administrator unable to READ BACK what the authoring surface let them
// CREATE: POST /api/atoms/{id}/question-jobs/{job}/accept is NOT gated, so an
// `admin` could mint a question and then take 403 from
// GET .../questions/{id} — the atom editor rendered empty and the generated
// content looked lost. The tenant-administrative roles are therefore admitted:
// they are the tenant's most trusted operators, and the gate exists to keep
// LEARNERS (and auditor / support / proctor) out of the answer key, not to keep
// the tenant's own admin out of their own content. This is the "ATOM Phase 2
// role elevation for instructor/admin" the media handler's TODO defers.
//
// Roles ride the canonical mesh header the gateway populates via
// servicemesh.MarshalToHeaders (mTLS-bound; a client cannot set it).
//
// Fail-CLOSED — absent or empty header means no access. Before CHO-2254 this
// service read roles NOWHERE for authz (its only role use is SET LOCAL
// chora.user_roles for RLS, and the live `questions` table carries only
// tenant_isolation), so there is no other layer behind this one: an unbuilt gate
// fails open, and this gate IS the line.
func hasAuthorRole(r *http.Request) bool {
	roles := strings.ToLower(r.Header.Get(servicemesh.HeaderUserRoles))
	if strings.TrimSpace(roles) == "" {
		return false
	}
	for _, role := range strings.Split(roles, ",") {
		switch strings.TrimSpace(role) {
		case "author", "instructor", "admin", "owner", "tenant_admin":
			return true
		}
	}
	return false
}

// dispatch routes /api/atoms/{atom_id}/questions[/{question_id}] paths. Called
// by the parent AtomHandler when it detects the `/questions` sub-segment in
// /api/atoms/{atom_id}/...
//
// Path shapes:
//
//	/api/atoms/{atom_id}/questions                  -> collection (POST, GET)
//	/api/atoms/{atom_id}/questions/{question_id}    -> item (GET, PATCH, DELETE)
func (h *QuestionsHandler) dispatch(w http.ResponseWriter, r *http.Request, atomID, rest string) {
	// CHO-2254 (SECURITY) — the ENTIRE /questions subtree is the authoring
	// surface: it mints, edits, deletes, and returns the answer key
	// (mcq_payload.is_correct + explainer, the "AUTHOR projection (full)").
	//
	// Gate the SUBTREE, not each verb: a gate per handler is one forgotten leaf
	// away from re-opening this, and that is exactly how it was open — every
	// route here read tenant + atom-ownership and nothing else, so any learner
	// in the tenant could read the key (they are handed question_id in their own
	// atom payload) AND rewrite or delete the question.
	//
	// This does NOT touch the learner's legitimate read: that is GET
	// /api/atoms/{id} on the parent AtomHandler, which serves the projection-
	// stripped copy (LearnerProjection + learnerSafeMCQOptions).
	if !hasAuthorRole(r) {
		writeError(w, http.StatusForbidden, "CREATION_FORBIDDEN",
			"authoring a question requires the author or instructor role")
		return
	}
	if rest == "" || rest == "/" {
		// Collection.
		switch r.Method {
		case http.MethodPost:
			h.createQuestion(w, r, atomID)
		default:
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
				"only POST is supported on /api/atoms/{atom_id}/questions")
		}
		return
	}
	// Item.
	rest = strings.TrimPrefix(rest, "/")
	rest = strings.TrimSuffix(rest, "/")
	if strings.Contains(rest, "/") {
		writeError(w, http.StatusNotFound, "CREATION_NOT_FOUND", "unknown sub-resource")
		return
	}
	questionID := rest
	switch r.Method {
	case http.MethodGet:
		h.getQuestion(w, r, atomID, questionID)
	case http.MethodPatch:
		h.patchQuestion(w, r, atomID, questionID)
	case http.MethodDelete:
		h.deleteQuestion(w, r, atomID, questionID)
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only GET, PATCH, DELETE are supported on /api/atoms/{atom_id}/questions/{question_id}")
	}
}

// -----------------------------------------------------------------------------
// Wire DTOs — discriminated by `type`. MCQ payload is required when type=mcq;
// OE payload is required when type=oe. Reserved_* types return 501.
// -----------------------------------------------------------------------------

type createQuestionRequest struct {
	Type       string            `json:"type"`
	Prompt     string            `json:"prompt"`
	MCQPayload *createMCQPayload `json:"mcq_payload,omitempty"`
	OEPayload  *createOEPayload  `json:"oe_payload,omitempty"`
	// W8 image-gen persistence — optional generated illustration URLs threaded
	// from the FE (AI-assist candidate). ImageURL = QUESTION/STEM image;
	// AnswerImageURL = MODEL-ANSWER image. Persisted into the mcq_payload /
	// oe_payload JSONB column so they survive into the published atom, the
	// test-set snapshot, and the learner GraphQL read. Wire names `image_url`
	// / `answer_image_url` — IDENTICAL across FE / domain payload / OpenAPI.
	ImageURL       *string `json:"image_url"`
	AnswerImageURL *string `json:"answer_image_url"`
}

type createMCQPayload struct {
	Options      []createMCQOption `json:"options"`
	XPOnCorrect  int               `json:"xp_on_correct,omitempty"`
	TimerSeconds int               `json:"timer_seconds,omitempty"`
}

type createMCQOption struct {
	OptionID  string `json:"option_id"`
	Label     string `json:"label"`
	IsCorrect bool   `json:"is_correct"`
	Explainer string `json:"explainer"`
}

// createOEPayload mirrors the OpenAPI OEPayload schema at
// chora-contracts/openapi/creation-questions.yaml. The `rubric` field is
// a FLAT array of RubricCriterion per the spec — NOT wrapped in a
// `{criteria: [...]}` envelope. Domain canonicalises on the {Rubric,
// Criteria[]} shape (see services/chora-creation/internal/domain/question/
// oe.go); the boundary at buildPayloadFromRequest performs the
// array→Rubric{Criteria:[...]} wrap.
//
// E2E-BE-OE-RUBRIC close (smoke #2 2026-05-17).
type createOEPayload struct {
	ModelAnswer      string                  `json:"model_answer"`
	Rubric           []createRubricCriterion `json:"rubric,omitempty"`
	MinResponseChars *int                    `json:"min_response_chars,omitempty"`
	MaxResponseChars *int                    `json:"max_response_chars,omitempty"`
	GraderTier       *string                 `json:"grader_tier,omitempty"`
}

// createRubricCriterion mirrors the OpenAPI RubricCriterion schema:
// {criterion_id, title (required), description (optional), weight (0..1)}.
// Domain stores `Description` (mapped from `title` at the boundary —
// domain has no separate Title concept yet; planned for ATOM-1 Phase 1)
// and integer `WeightPercent` 0..100 (multiplied from wire `weight` at
// the boundary per the deterministic-equality invariant documented at
// services/chora-creation/internal/domain/question/oe.go:Rubric).
type createRubricCriterion struct {
	CriterionID string  `json:"criterion_id"`
	Title       string  `json:"title"`
	Description string  `json:"description,omitempty"`
	Weight      float64 `json:"weight"`
}

type patchQuestionRequest struct {
	Prompt     *string           `json:"prompt,omitempty"`
	MCQPayload *createMCQPayload `json:"mcq_payload,omitempty"`
	OEPayload  *createOEPayload  `json:"oe_payload,omitempty"`
	// W8 image-gen persistence — see createQuestionRequest. Same wire names +
	// semantics; threaded into the rebuilt MCQPayload/OEPayload on edit.
	ImageURL       *string `json:"image_url"`
	AnswerImageURL *string `json:"answer_image_url"`
}

type questionWithRevisionResponse struct {
	Question *question.Question         `json:"question"`
	Revision *question.QuestionRevision `json:"revision"`
}

// -----------------------------------------------------------------------------
// POST /api/atoms/{atom_id}/questions
// -----------------------------------------------------------------------------

func (h *QuestionsHandler) createQuestion(w http.ResponseWriter, r *http.Request, atomID string) {
	var req createQuestionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
		return
	}

	tenantID := tenantFromContext(r.Context())
	authorGCID := gcidFromContext(r.Context())

	// 1. Parent atom must exist + match the requested type.
	parent, err := h.atomRepo.Get(r.Context(), tenantID, atomID)
	if err != nil {
		if errors.Is(err, atom.ErrNotFound) {
			writeError(w, http.StatusNotFound, "CREATION_ATOM_NOT_FOUND",
				"parent atom not found")
			return
		}
		log.Printf("questions: parent atom lookup: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR",
			"failed to load parent atom")
		return
	}

	// ADR-229 A1.2 (CHO-2132) — no revision appends onto a frozen orphan
	// edition; evolving one is a fork via Clone().
	if parent.IsOrphan() {
		writeError(w, http.StatusConflict, "CREATION_ATOM_ORPHANED_FROZEN",
			"orphan editions are frozen (no new questions); fork via clone to evolve")
		return
	}

	qType := question.QuestionType(strings.TrimSpace(req.Type))
	if !qType.Valid() {
		writeError(w, http.StatusBadRequest, "CREATION_QUESTION_INVALID_TYPE",
			"unknown question type")
		return
	}
	if !qType.Enabled() {
		writeError(w, http.StatusNotImplemented, "CREATION_QUESTION_TYPE_NOT_ENABLED",
			"question type is reserved_* (V1 ships MCQ + OE only)")
		return
	}
	// MCQ ↔ atom_type=mcq; OE ↔ atom_type=essay. Reject mismatch.
	atomTypeStr := strings.ToLower(string(parent.QuestionType))
	if qType == question.TypeMCQ && atomTypeStr != "mcq" {
		writeError(w, http.StatusBadRequest, "CREATION_QUESTION_INVALID_TYPE",
			"question type mcq must match atom_type=mcq")
		return
	}
	if qType == question.TypeOpenEnded && atomTypeStr != "essay" && atomTypeStr != "oe" {
		writeError(w, http.StatusBadRequest, "CREATION_QUESTION_INVALID_TYPE",
			"question type oe must match atom_type=essay")
		return
	}

	// 2. Build the domain payloads. Thread the optional W8 image URLs so they
	// persist into the mcq_payload/oe_payload JSONB and survive the SAVE path.
	mcq, oe, perr := h.buildPayloadFromRequest(qType, req.MCQPayload, req.OEPayload, req.ImageURL, req.AnswerImageURL)
	if perr != nil {
		writeError(w, http.StatusBadRequest, "CREATION_QUESTION_PAYLOAD_INVALID", perr.Error())
		return
	}

	// 3. Construct the aggregate + first revision.
	q, err := question.New(question.NewParams{
		TenantID:   tenantID,
		AtomID:     atomID,
		AuthorGcid: authorGCID,
		Type:       qType,
		Prompt:     req.Prompt,
		SourceType: atom.SourceManual,
		MCQ:        mcq,
		OE:         oe,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_QUESTION_PAYLOAD_INVALID", err.Error())
		return
	}
	rev, err := question.NewRevision(q, q.Prompt, mcq, oe, authorGCID, atom.SourceManual)
	if err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_QUESTION_PAYLOAD_INVALID", err.Error())
		return
	}
	q.LatestRevisionID = rev.RevisionID

	// 4. Persist.
	if err := h.repo.Save(r.Context(), q, rev); err != nil {
		if errors.Is(err, question.ErrAtomHasQuestion) {
			writeError(w, http.StatusConflict, "CREATION_QUESTION_DUPLICATE",
				"atom already has a non-deleted question (D2 — 1 atom = 1 question)")
			return
		}
		log.Printf("questions: save: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR",
			"failed to persist question")
		return
	}
	// 5. Backfill atom_index with the grading ground-truth (fail loud — the
	// question persisted, but without the key every take mis-grades; the
	// author retries into the PATCH path which re-emits).
	if err := h.emitAtomUpsert(r.Context(), parent, qType, mcq); err != nil {
		log.Printf("questions: emit atom upsert: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_EVENT_EMIT_FAILED",
			"question saved but the grading-key event failed to publish — retry via edit")
		return
	}
	writeJSON(w, http.StatusCreated, questionWithRevisionResponse{
		Question: q, Revision: rev,
	})
}

// -----------------------------------------------------------------------------
// PATCH /api/atoms/{atom_id}/questions/{question_id}
// -----------------------------------------------------------------------------

func (h *QuestionsHandler) patchQuestion(w http.ResponseWriter, r *http.Request, atomID, questionID string) {
	var req patchQuestionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
		return
	}
	tenantID := tenantFromContext(r.Context())
	authorGCID := gcidFromContext(r.Context())

	// ADR-229 A1.2 (CHO-2132) — revision appends onto a frozen orphan
	// edition are refused wholesale, BEFORE any question lookup (the guard
	// keys on the parent atom, so it holds even for question ids that don't
	// exist yet).
	if parent, aerr := h.atomRepo.Get(r.Context(), tenantID, atomID); aerr == nil && parent.IsOrphan() {
		writeError(w, http.StatusConflict, "CREATION_ATOM_ORPHANED_FROZEN",
			"orphan editions are frozen (append-only history sealed); fork via clone to evolve")
		return
	}

	q, _, err := h.repo.GetByID(r.Context(), tenantID, questionID)
	if err != nil {
		if errors.Is(err, question.ErrNotFound) {
			writeError(w, http.StatusNotFound, "CREATION_QUESTION_NOT_FOUND", "question not found")
			return
		}
		log.Printf("questions: GetByID: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to load question")
		return
	}
	if q.AtomID != atomID {
		writeError(w, http.StatusNotFound, "CREATION_QUESTION_NOT_FOUND",
			"question does not belong to this atom")
		return
	}

	// PATCH semantics: payloads are OPTIONAL — if the caller omits mcq_payload
	// / oe_payload, the previous revision's payload is reused (per
	// Question.ApplyUpdate § append-only PATCH semantics). We only build a
	// fresh domain payload when the wire-layer payload is non-nil.
	mcq, oe, perr := h.buildPatchPayloadFromRequest(q.Type, req.MCQPayload, req.OEPayload, req.ImageURL, req.AnswerImageURL, q.MCQ, q.OE)
	if perr != nil {
		writeError(w, http.StatusBadRequest, "CREATION_QUESTION_PAYLOAD_INVALID", perr.Error())
		return
	}

	// Apply the update. ApplyUpdate produces a NEW *Question + a new *QuestionRevision.
	updated, newRev, err := q.ApplyUpdate(question.UpdateParams{
		Prompt:     req.Prompt,
		MCQ:        mcq,
		OE:         oe,
		AuthorGcid: authorGCID,
		SourceType: atom.SourceManual,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_QUESTION_PAYLOAD_INVALID", err.Error())
		return
	}

	if err := h.repo.AppendRevision(r.Context(), newRev); err != nil {
		log.Printf("questions: AppendRevision: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR",
			"failed to append revision")
		return
	}
	// Reflect the new revision id + number onto the returned aggregate (the
	// repository may have re-numbered it based on MAX(revision_number) +1).
	updated.LatestRevisionID = newRev.RevisionID
	updated.Revision = newRev.RevisionNumber

	// ADR-210 — durably re-home the revision's transient W8 images into
	// chora-atom-media at save, so a saved-but-unpublished image survives the
	// 7-day transient bucket TTL (completes CHO-1974). Append-only: on a
	// rewrite this appends a further revision carrying the durable gs:// refs
	// (idempotent — an already-durable ref is a no-op). Fail-loud.
	if h.reHomer != nil {
		rehomed, changed, rerr := h.reHomer.ReHomeRevision(r.Context(), tenantID, atomID, updated, newRev)
		if rerr != nil {
			log.Printf("questions: media re-home (patch): %v", rerr)
			writeError(w, http.StatusInternalServerError, "CREATION_ATOM_MEDIA_REHOME_FAILED",
				"failed to durably re-home images")
			return
		}
		if changed {
			newRev = rehomed
			updated.LatestRevisionID = rehomed.RevisionID
			updated.Revision = rehomed.RevisionNumber
		}
	}

	// Re-emit the atom_index upsert with the EFFECTIVE payload (the edit may
	// have moved the correct option; a stale key mis-grades every take).
	if parent, aerr := h.atomRepo.Get(r.Context(), tenantID, atomID); aerr == nil {
		if err := h.emitAtomUpsert(r.Context(), parent, updated.Type, newRev.MCQPayload); err != nil {
			log.Printf("questions: emit atom upsert (patch): %v", err)
			writeError(w, http.StatusInternalServerError, "CREATION_EVENT_EMIT_FAILED",
				"revision saved but the grading-key event failed to publish — retry the edit")
			return
		}
	} else {
		log.Printf("questions: emit atom upsert (patch): parent lookup: %v", aerr)
		writeError(w, http.StatusInternalServerError, "CREATION_EVENT_EMIT_FAILED",
			"revision saved but the parent atom could not be loaded for the grading-key event")
		return
	}

	writeJSON(w, http.StatusOK, questionWithRevisionResponse{
		Question: updated, Revision: newRev,
	})
}

// -----------------------------------------------------------------------------
// GET /api/atoms/{atom_id}/questions/{question_id} — AUTHOR projection (full)
// -----------------------------------------------------------------------------

func (h *QuestionsHandler) getQuestion(w http.ResponseWriter, r *http.Request, atomID, questionID string) {
	tenantID := tenantFromContext(r.Context())
	q, rev, err := h.repo.GetByID(r.Context(), tenantID, questionID)
	if err != nil {
		if errors.Is(err, question.ErrNotFound) {
			writeError(w, http.StatusNotFound, "CREATION_QUESTION_NOT_FOUND", "question not found")
			return
		}
		log.Printf("questions: GetByID: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to load question")
		return
	}
	if q.AtomID != atomID {
		writeError(w, http.StatusNotFound, "CREATION_QUESTION_NOT_FOUND",
			"question does not belong to this atom")
		return
	}
	// CHO-1638 — an <img> can't carry a bearer JWT, and a PUBLISHED atom stores
	// the canonical durable gs:// ref, so the raw value never renders on the
	// author re-open. Mint the durable refs to fresh short-lived signed GET URLs
	// (transient https refs from draft atoms pass through). Never mutate the
	// repo's struct — mint over a shallow copy.
	writeJSON(w, http.StatusOK, questionWithRevisionResponse{Question: h.mintQuestionMedia(r.Context(), q), Revision: rev})
}

// mintQuestionMedia returns a shallow copy of q whose MCQ/OE ImageURL +
// AnswerImageURL durable gs:// refs are resolved to fresh signed GET URLs for
// the authoring re-open path. Transient https refs pass through unchanged. On
// a mint failure the ref is dropped (nil) and logged — a raw gs:// is NEVER
// emitted to the wire — and the response is still served. A nil signer (tests /
// unwired) makes this a no-op.
func (h *QuestionsHandler) mintQuestionMedia(ctx context.Context, q *question.Question) *question.Question {
	if q == nil || h.mediaDownloadSigner == nil {
		return q
	}
	out := *q
	if q.MCQ != nil {
		mcq := *q.MCQ
		mcq.ImageURL = h.mintMediaRef(ctx, q.MCQ.ImageURL)
		mcq.AnswerImageURL = h.mintMediaRef(ctx, q.MCQ.AnswerImageURL)
		out.MCQ = &mcq
	}
	if q.OE != nil {
		oe := *q.OE
		oe.ImageURL = h.mintMediaRef(ctx, q.OE.ImageURL)
		oe.AnswerImageURL = h.mintMediaRef(ctx, q.OE.AnswerImageURL)
		out.OE = &oe
	}
	return &out
}

// mintMediaRef resolves a single image ref: a durable gs:// ref is minted to a
// fresh signed GET URL; a transient https ref (or empty/nil) passes through;
// a mint failure drops the ref to nil (never a raw gs:// on the wire).
func (h *QuestionsHandler) mintMediaRef(ctx context.Context, ref *string) *string {
	if ref == nil || *ref == "" || !strings.HasPrefix(*ref, "gs://") {
		return ref
	}
	signed, err := h.mediaDownloadSigner.SignDownloadURL(ctx, *ref)
	if err != nil {
		log.Printf("getQuestion: mint atom-media gs-uri=%s: %v (dropping from author projection)", *ref, err)
		return nil
	}
	url := signed.URL
	return &url
}

// -----------------------------------------------------------------------------
// DELETE /api/atoms/{atom_id}/questions/{question_id} — soft delete
// -----------------------------------------------------------------------------

func (h *QuestionsHandler) deleteQuestion(w http.ResponseWriter, r *http.Request, atomID, questionID string) {
	tenantID := tenantFromContext(r.Context())
	q, _, err := h.repo.GetByID(r.Context(), tenantID, questionID)
	if err != nil {
		if errors.Is(err, question.ErrNotFound) {
			writeError(w, http.StatusNotFound, "CREATION_QUESTION_NOT_FOUND", "question not found")
			return
		}
		log.Printf("questions: GetByID: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to load question")
		return
	}
	if q.AtomID != atomID {
		writeError(w, http.StatusNotFound, "CREATION_QUESTION_NOT_FOUND",
			"question does not belong to this atom")
		return
	}
	if err := h.repo.SoftDelete(r.Context(), tenantID, questionID); err != nil {
		log.Printf("questions: SoftDelete: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to soft delete")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// -----------------------------------------------------------------------------
// Helpers — payload conversion
// -----------------------------------------------------------------------------

// buildPayloadFromRequest converts the wire-shape payloads to the domain
// shapes. Assigns UUIDs for option_id / criterion_id when omitted by the
// caller. Returns a typed validation error when neither (or both) payloads
// are supplied against a discriminated type.
// newAuthoredMCQPayload maps an author-supplied wire MCQ onto a domain payload
// with every option id freshly minted and the option ORDER decorrelated. The
// caller layers on its own image-ref semantics (create takes the wire refs;
// patch resolves them against the previous revision).
//
// CHO-2255 (SECURITY) — this is the ONE chokepoint for author-supplied MCQ, and
// it is a single function on purpose. POST and PATCH each carried their own copy
// of this mapping; the fix landed on POST's copy only, and PATCH's copy kept
// honouring the caller's option_id and never shuffled. Both leaks therefore had
// a live re-entry door, and any backfill of the legacy rows would have decayed
// on the first author edit.
//
// Neither step may be made conditional. Minting is unconditional because the
// naming is exactly what cannot be trusted (CHO-2244 proved "neutral" ids like
// opt_1a encode POSITION), and an author has no legitimate need to choose an id.
// Shuffling is unconditional because authors type the answer first and the FE
// renders payload order with a positional A/B/C/D marker.
func newAuthoredMCQPayload(wireMCQ *createMCQPayload) *question.MCQPayload {
	opts := make([]question.MCQOption, len(wireMCQ.Options))
	for i, o := range wireMCQ.Options {
		opts[i] = question.MCQOption{
			// OptionID deliberately NOT carried over from the wire — minted below.
			Label:     o.Label,
			IsCorrect: o.IsCorrect,
			Explainer: o.Explainer,
		}
	}
	payload := &question.MCQPayload{
		Options:      opts,
		XPOnCorrect:  wireMCQ.XPOnCorrect,
		TimerSeconds: wireMCQ.TimerSeconds,
	}
	payload.MintOptionIDs(mintOptionID)
	// Grading is option_id-based end-to-end, so reordering is safe.
	payload.ShuffleOptions(mathrand.Shuffle)
	return payload
}

// mintOptionID supplies an opaque option id: UUIDv7 per the new-id convention.
func mintOptionID() string {
	return uuid.Must(uuid.NewV7()).String()
}

func (h *QuestionsHandler) buildPayloadFromRequest(qType question.QuestionType, wireMCQ *createMCQPayload, wireOE *createOEPayload, imageURL, answerImageURL *string) (*question.MCQPayload, *question.OEPayload, error) {
	switch qType {
	case question.TypeMCQ:
		if wireMCQ == nil {
			return nil, nil, errors.New("mcq_payload required for type=mcq")
		}
		if wireOE != nil {
			return nil, nil, errors.New("oe_payload must be absent for type=mcq")
		}
		payload := newAuthoredMCQPayload(wireMCQ)
		payload.ImageURL = imageURL
		payload.AnswerImageURL = answerImageURL
		return payload, nil, nil
	case question.TypeOpenEnded:
		if wireOE == nil {
			return nil, nil, errors.New("oe_payload required for type=oe")
		}
		if wireMCQ != nil {
			return nil, nil, errors.New("mcq_payload must be absent for type=oe")
		}
		out := &question.OEPayload{
			ModelAnswer:      wireOE.ModelAnswer,
			ImageURL:         imageURL,
			AnswerImageURL:   answerImageURL,
			MinResponseChars: wireOE.MinResponseChars,
			MaxResponseChars: wireOE.MaxResponseChars,
			GraderTier:       wireOE.GraderTier,
		}
		// E2E-BE-OE-RUBRIC — wire shape is a flat array per OpenAPI spec;
		// boundary wraps into domain Rubric{Criteria:[...]} for the
		// deterministic-equality invariant documented at
		// services/chora-creation/internal/domain/question/oe.go.
		// Empty array → no rubric. Empty `title` falls back to `description`
		// so callers that previously sent only `description` still validate.
		if len(wireOE.Rubric) > 0 {
			out.WeightedRubric = rubricFromWireCriteria(wireOE.Rubric)
		}
		return nil, out, nil
	}
	return nil, nil, errors.New("unsupported question type")
}

// rubricFromWireCriteria maps the OpenAPI RubricCriterion wire shape onto the
// domain Rubric: mint a UUIDv7 for any blank criterion_id, collapse title +
// description onto Description (title priority — domain has no Title field yet,
// ATOM-1 Phase 1), and convert the fractional wire `weight`s into integer
// WeightPercents that sum to EXACTLY 100 via largest-remainder (so the domain
// Validate sum==100 invariant always holds; naive per-row rounding broke it for
// repeating decimals). Shared by the POST + PATCH paths AND the batch candidate
// normalizer (question.NormalizeWeightPercents).
func rubricFromWireCriteria(in []createRubricCriterion) *question.Rubric {
	weights := make([]float64, len(in))
	for i, c := range in {
		weights[i] = c.Weight
	}
	pcts := question.NormalizeWeightPercents(weights)
	crits := make([]question.RubricCriterion, len(in))
	for i, c := range in {
		cid := c.CriterionID
		if cid == "" {
			if id, err := uuid.NewV7(); err == nil {
				cid = id.String()
			}
		}
		desc := strings.TrimSpace(c.Title)
		if desc == "" {
			desc = c.Description
		}
		crits[i] = question.RubricCriterion{
			CriterionID:   cid,
			Description:   desc,
			WeightPercent: pcts[i],
		}
	}
	return &question.Rubric{Criteria: crits}
}

// resolvePatchImageRef applies PATCH image-field semantics when a fresh payload
// is rebuilt: a nil wire ref (the field was OMITTED from the PATCH body) carries
// the previous revision's ref FORWARD; an explicit empty string ("") is the
// Remove affordance and CLEARS the image; any other value SETS it. Without this,
// a text-only save-revision (the FE editor sends mcq_payload/options but omits
// image_url/answer_image_url) silently dropped the AI-assist images (CHO-1974).
func resolvePatchImageRef(wire, prev *string) *string {
	if wire == nil {
		return prev // omitted ⇒ keep the prior image (durable gs:// ref)
	}
	if *wire == "" {
		return nil // explicit clear (Remove)
	}
	return wire // explicit set
}

// buildPatchPayloadFromRequest is the PATCH-flavour of buildPayloadFromRequest.
// PATCH allows the caller to omit the payload entirely (keeping the previous
// revision's payload) but rejects the type-mismatched payload (oe_payload on
// an mcq question or vice versa). Returns (nil, nil, nil) for the "preserve
// previous payload" path. When a wire payload IS present, omitted image refs are
// carried forward from prevMCQ/prevOE (CHO-1974) so editing text never drops the
// generated images.
func (h *QuestionsHandler) buildPatchPayloadFromRequest(qType question.QuestionType, wireMCQ *createMCQPayload, wireOE *createOEPayload, imageURL, answerImageURL *string, prevMCQ *question.MCQPayload, prevOE *question.OEPayload) (*question.MCQPayload, *question.OEPayload, error) {
	switch qType {
	case question.TypeMCQ:
		if wireOE != nil {
			return nil, nil, errors.New("oe_payload must be absent for type=mcq")
		}
		if wireMCQ == nil {
			return nil, nil, nil // preserve previous payload
		}
		var prevImg, prevAns *string
		if prevMCQ != nil {
			prevImg, prevAns = prevMCQ.ImageURL, prevMCQ.AnswerImageURL
		}
		// CHO-2255 (SECURITY) — the SAME chokepoint as the POST path. This
		// builder used to keep its own copy of the wire->domain option mapping,
		// and that copy drifted: it honoured the caller's option_id and never
		// shuffled, so every edit re-opened both leaks POST had closed (and
		// would have decayed any backfill of the legacy rows). Re-minting here
		// is safe because patchQuestion re-emits the atom_index upsert with the
		// effective payload, so the key crossing into chora_consumption follows.
		payload := newAuthoredMCQPayload(wireMCQ)
		payload.ImageURL = resolvePatchImageRef(imageURL, prevImg)
		payload.AnswerImageURL = resolvePatchImageRef(answerImageURL, prevAns)
		return payload, nil, nil
	case question.TypeOpenEnded:
		if wireMCQ != nil {
			return nil, nil, errors.New("mcq_payload must be absent for type=oe")
		}
		if wireOE == nil {
			return nil, nil, nil // preserve previous payload
		}
		var prevImg, prevAns *string
		if prevOE != nil {
			prevImg, prevAns = prevOE.ImageURL, prevOE.AnswerImageURL
		}
		out := &question.OEPayload{
			ModelAnswer:      wireOE.ModelAnswer,
			ImageURL:         resolvePatchImageRef(imageURL, prevImg),
			AnswerImageURL:   resolvePatchImageRef(answerImageURL, prevAns),
			MinResponseChars: wireOE.MinResponseChars,
			MaxResponseChars: wireOE.MaxResponseChars,
			GraderTier:       wireOE.GraderTier,
		}
		// E2E-BE-OE-RUBRIC — same wire→domain mapping as the POST path
		// (rubricFromWireCriteria): title→Description, largest-remainder
		// weight→WeightPercent summing to 100.
		if len(wireOE.Rubric) > 0 {
			out.WeightedRubric = rubricFromWireCriteria(wireOE.Rubric)
		}
		return nil, out, nil
	}
	return nil, nil, errors.New("unsupported question type")
}
