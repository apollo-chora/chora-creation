// search.go — SearchFilter value object + lightweight SearchResult projection
// for the cross-atom Question picker that powers the A+ X.2 test-set editor
// (FE B-FE-X5; ADR-155 D1).
//
// Per .claude/rules/ddd-enforcement.md the questions ARE atoms in the
// chora_creation database — same domain, intra-DB. The pg adapter implements
// the search over learning_atoms (since the Phyllis seed has atoms but no rows
// in the `questions` sub-table); future iterations will compose with the
// `questions` table once authoring lands seeded payloads.
//
// Hexagonal: this file is pure-domain (NO infrastructure imports). The pg
// adapter shapes its SQL against this value object; tests RED-first verify
// Normalize / Validate / Offset / MatchesType.
package question

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// SearchFilter is the value object that captures the query-string shape of
// GET /api/v1/atoms/questions/search.
//
// All fields are optional — an empty SearchFilter is the canonical "first
// page, no filters" shape (defaults to page=1, per=20 via Normalize).
type SearchFilter struct {
	// Q is the optional free-text needle. Case-insensitive substring match
	// against the atom title + body (the prompt-equivalent for atom-as-question
	// projection). Empty Q matches all rows.
	Q string

	// TenantID is an explicit tenant filter. When empty, the adapter falls
	// back to the caller's tenant from the RLS context (chora.tenant_id
	// session GUC). RLS enforces tenant isolation as defence-in-depth.
	TenantID string

	// Types is the optional multi-value filter on question_type / atom_type.
	// Empty Types = "match all". Wire format is comma-separated; the HTTP
	// adapter splits before constructing the filter.
	Types []string

	// AuthorGCID is the optional exact-match filter on the atom's author/owner
	// (the `gcid` column on learning_atoms — there is NO `author_gcid` column).
	// Empty = "match all authors".
	AuthorGCID string

	// Tags is the optional multi-value filter on the atom's free-form `tags`
	// JSONB array. OR within the family (an atom carrying ANY of the supplied
	// tags matches — contract searchQuestions "OR within a family"). Empty =
	// "match all". Case is PRESERVED (the JSONB `?|` operator is
	// case-sensitive); do NOT lowercase.
	Tags []string

	// States is the optional multi-value filter on the atom's lifecycle
	// `status` (atom_status enum). The HTTP adapter translates the contract
	// `state` enum (DRAFT/PUBLISHED/ARCHIVED) → DB values (draft/published/
	// archived) before constructing the filter; this field holds DB-enum
	// values. Empty = "match all states" (no status predicate — preserves the
	// pre-filter behaviour where the picker showed every non-deleted atom).
	States []string

	// AtomIDs is the optional multi-value filter restricting results to a
	// specific set of atoms (UUIDv7). OR within the family. Empty = "match
	// all atoms".
	AtomIDs []string

	// Sorts is the optional ordered list of sort keys (contract `sort` grammar
	// `{field}:{dir}[,{field}:{dir}...]`). Empty = the default created_at:desc.
	// Each key is whitelist-validated (no raw column interpolation).
	Sorts []Sort

	// Page is 1-based; Per is 1..100. Normalize coerces defaults + caps.
	Page int
	Per  int

	// Source controls which atoms the search returns per
	// docs/design/ux_unified_atom_picker.md:
	//   "mine"  — atoms authored by CallerGCID.
	//   "saved" — atoms the caller bookmarked (SavedAtomIDs hydrate).
	//   "all"   — union of mine + saved (the default).
	// Empty Source defaults to "all" at Normalize time.
	Source Source

	// CallerGCID is the caller's GCID (stamped by the gateway's mesh claims).
	// Required for Source=mine and Source=all — the SQL filters on
	// gcid = CallerGCID. Empty CallerGCID with Source=mine/all returns empty.
	CallerGCID string

	// SavedAtomIDs is the hydrated list of atom IDs the caller bookmarked
	// (fetched from chora-sharing via SavedAtomIDFetcher). Populated by the
	// HTTP handler before invoking SearchQuestions. Empty means no saved
	// atoms (or the fetch failed — see PartialResult on the response).
	SavedAtomIDs []string

	// GrantedAtomIDs is the hydrated list of atom ids covered by an ACTIVE
	// AtomUsageGrant for the caller (fetched per-request from chora-sharing
	// GetReuseContext — ADR-229 D4.1, CHO-2133). Feeds the granted leg of
	// the picker consent disjunct. Orphan editions repointed by the
	// singleton-orphan machinery (A1.1) surface EXCLUSIVELY through this
	// list — they are minted reuse_visibility='private' forever, so the
	// tenant-visible leg excludes them by construction.
	GrantedAtomIDs []string
}

// Source is the picker source filter enum.
type Source string

const (
	SourceAll   Source = "all"
	SourceMine  Source = "mine"
	SourceSaved Source = "saved"
)

// -----------------------------------------------------------------------------
// Sort — a single whitelisted sort key (contract searchQuestions `sort`).
//
// The column whitelist lives here in the domain so the pg adapter renders
// ORDER BY from MAPPED CONSTANTS only — the raw user-supplied field string
// NEVER reaches the SQL text, eliminating the injection surface.
// -----------------------------------------------------------------------------

// Sort field + direction wire values (the contract `sort` grammar).
const (
	SortFieldCreatedAt    = "created_at"
	SortFieldUpdatedAt    = "updated_at"
	SortFieldTitle        = "title"         // the atom `title` column
	SortFieldQuestionType = "question_type" // the atom `question_type` column
	SortFieldPrompt       = "prompt"        // ADR-206: sorts the JOINed question prompt (display label)
)

const (
	SortDirAsc  = "asc"
	SortDirDesc = "desc"
)

// Sort is one validated sort key. Field is the contract sort field (NOT a SQL
// column); Direction is asc|desc.
type Sort struct {
	Field     string
	Direction string
}

// sortFieldColumns maps each whitelisted sort field to its physical sort
// expression. Atom columns are qualified `la.` (the search now LEFT JOINs the
// `questions q` row — ADR-206); `prompt` sorts by the display label (the live
// question prompt, falling back to stem/title for a draft atom with no question
// yet). This map IS the SQL-injection guard: the pg adapter only ever emits
// expressions found here (never the raw user field string).
var sortFieldColumns = map[string]string{
	SortFieldCreatedAt:    "la.created_at",
	SortFieldUpdatedAt:    "la.updated_at",
	SortFieldTitle:        "la.title",
	SortFieldQuestionType: "la.question_type",
	SortFieldPrompt:       "COALESCE(q.prompt, la.stem, la.title)",
}

// Column returns the whitelisted physical column for the sort field; ok=false
// when the field is not whitelisted (defence-in-depth — the parser already
// rejects unknown fields).
func (s Sort) Column() (string, bool) {
	col, ok := sortFieldColumns[s.Field]
	return col, ok
}

// SQLDirection returns the rendered SQL direction. Anything other than an
// explicit `asc` renders DESC (the contract default + a safe fallback).
func (s Sort) SQLDirection() string {
	if s.Direction == SortDirAsc {
		return "ASC"
	}
	return "DESC"
}

// Validate reports whether the sort key's field + direction are whitelisted.
func (s Sort) Validate() error {
	if _, ok := sortFieldColumns[s.Field]; !ok {
		return fmt.Errorf("question.Sort: unsupported sort field %q (allowed: created_at, updated_at, title, question_type, prompt)", s.Field)
	}
	if s.Direction != SortDirAsc && s.Direction != SortDirDesc {
		return fmt.Errorf("question.Sort: unsupported sort direction %q (allowed: asc, desc)", s.Direction)
	}
	return nil
}

// ParseSorts parses the contract `sort` grammar `{field}:{dir}[,{field}:{dir}…]`
// into an ordered []Sort. Empty input yields (nil, nil) so the caller applies
// the default created_at:desc. Returns an error on any malformed/unknown key
// (the HTTP boundary maps it to a 400).
func ParseSorts(raw string) ([]Sort, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]Sort, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		field, dir, found := strings.Cut(p, ":")
		if !found {
			return nil, fmt.Errorf("question.ParseSorts: malformed sort key %q (want field:direction)", p)
		}
		s := Sort{
			Field:     strings.ToLower(strings.TrimSpace(field)),
			Direction: strings.ToLower(strings.TrimSpace(dir)),
		}
		if err := s.Validate(); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// MaxQ is the upper bound on the free-text needle. Larger inputs are
// rejected at Validate time as a guardrail against accidental DoS via huge
// substring patterns.
const MaxQ = 4096

// MaxPer is the page-size hard cap. Higher requested values are silently
// capped to this at Normalize time (no error — UX nicety per the contract).
const MaxPer = 100

// DefaultPer is the page-size fallback when the caller omits `per`.
const DefaultPer = 20

// Validate rejects malformed filters before they reach the pg adapter. Empty
// filters validate (the canonical "no filters" shape).
func (f SearchFilter) Validate() error {
	if len(f.Q) > MaxQ {
		return fmt.Errorf("question.SearchFilter: q exceeds max length %d (got %d)", MaxQ, len(f.Q))
	}
	for _, s := range f.Sorts {
		if err := s.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Normalize returns a SearchFilter with defaults applied + values coerced
// into valid ranges. Idempotent: f.Normalize().Normalize() == f.Normalize().
func (f SearchFilter) Normalize() SearchFilter {
	out := SearchFilter{
		Q:          strings.TrimSpace(f.Q),
		TenantID:   strings.TrimSpace(f.TenantID),
		AuthorGCID: strings.ToLower(strings.TrimSpace(f.AuthorGCID)),
		Sorts:      f.Sorts,
		Page:       f.Page,
		Per:        f.Per,
	}
	if out.Page < 1 {
		out.Page = 1
	}
	if out.Per <= 0 {
		out.Per = DefaultPer
	}
	if out.Per > MaxPer {
		out.Per = MaxPer
	}
	out.Types = normalizeStrings(f.Types, true)
	// Source default + coercion.
	switch strings.TrimSpace(strings.ToLower(string(f.Source))) {
	case "mine":
		out.Source = SourceMine
	case "saved":
		out.Source = SourceSaved
	default:
		out.Source = SourceAll
	}
	out.CallerGCID = strings.TrimSpace(f.CallerGCID)
	out.AuthorGCID = strings.TrimSpace(f.AuthorGCID)
	out.SavedAtomIDs = f.SavedAtomIDs
	// Closed-vocab fields lowercase + dedupe; UUIDs lowercase for stable
	// dedupe (the DB uuid type is case-insensitive on input anyway).
	out.States = normalizeStrings(f.States, true)
	out.AtomIDs = normalizeStrings(f.AtomIDs, true)
	out.GrantedAtomIDs = normalizeStrings(f.GrantedAtomIDs, true)
	// Tags are free-form; case is PRESERVED (the JSONB `?|` match is
	// case-sensitive) — trim + dedupe only.
	out.Tags = normalizeStrings(f.Tags, false)
	return out
}

// normalizeStrings trims, drops empties, and deduplicates a wire-format string
// slice. When lower is true it also lowercases (closed-vocab fields: types,
// states, atom ids); free-form fields (tags) pass lower=false to preserve case
// for the case-sensitive JSONB `?|` match. Empty/all-empty input yields nil.
func normalizeStrings(in []string, lower bool) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimSpace(t)
		if lower {
			t = strings.ToLower(t)
		}
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Offset returns the zero-based row offset implied by Page + Per. Callers
// MUST invoke Normalize() first; calling Offset on a raw filter (Page=0)
// will produce a negative offset.
func (f SearchFilter) Offset() int {
	if f.Page <= 0 {
		return 0
	}
	return (f.Page - 1) * f.Per
}

// MatchesType reports whether the supplied question/atom type is included
// in the filter. Empty Types is treated as "match all" (the contract).
func (f SearchFilter) MatchesType(t string) bool {
	if len(f.Types) == 0 {
		return true
	}
	low := strings.ToLower(strings.TrimSpace(t))
	for _, x := range f.Types {
		if x == low {
			return true
		}
	}
	return false
}

// MatchesAuthor reports whether the supplied author gcid matches the filter.
// Empty AuthorGCID = "match all". Mirrors the pg adapter's `gcid = $N`.
func (f SearchFilter) MatchesAuthor(gcid string) bool {
	if f.AuthorGCID == "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(gcid), f.AuthorGCID)
}

// MatchesState reports whether the supplied atom status is included in the
// filter (case-insensitive). Empty States = "match all". Mirrors the pg
// adapter's `status IN (...)`.
func (f SearchFilter) MatchesState(status string) bool {
	if len(f.States) == 0 {
		return true
	}
	low := strings.ToLower(strings.TrimSpace(status))
	for _, x := range f.States {
		if x == low {
			return true
		}
	}
	return false
}

// MatchesAtomID reports whether the supplied atom id is included in the filter.
// Empty AtomIDs = "match all". Mirrors the pg adapter's `atom_id IN (...)`.
func (f SearchFilter) MatchesAtomID(id string) bool {
	if len(f.AtomIDs) == 0 {
		return true
	}
	low := strings.ToLower(strings.TrimSpace(id))
	for _, x := range f.AtomIDs {
		if x == low {
			return true
		}
	}
	return false
}

// MatchesTags reports whether the atom's tags intersect the filter tags (OR
// within the family — ANY match). Empty Tags = "match all". CASE-SENSITIVE to
// mirror the JSONB `?|` operator the pg adapter uses.
func (f SearchFilter) MatchesTags(atomTags []string) bool {
	if len(f.Tags) == 0 {
		return true
	}
	have := make(map[string]struct{}, len(atomTags))
	for _, t := range atomTags {
		have[strings.TrimSpace(t)] = struct{}{}
	}
	for _, want := range f.Tags {
		if _, ok := have[want]; ok {
			return true
		}
	}
	return false
}

// -----------------------------------------------------------------------------
// SearchResult — lightweight projection emitted by SearchQuestions
//
// Matches the FE B-FE-X5 (2026-05-16) ask schema:
//
//	{
//	  "id":            <uuid>,
//	  "title":         <atom title>,
//	  "stem":          <atom body, truncated to ~200 chars>,
//	  "question_type": "mcq|tf|sa|mc|oe|outline|flashcard|video|essay",
//	  "tenant_id":     <uuid>,
//	  "author_gcid":   <uuid>,
//	  "created_at":    rfc3339,
//	  "updated_at":    rfc3339
//	}
//
// Stem is preview-only — capped to StemPreviewLen with ellipsis suffix when
// truncated. The full body lives on GET /api/atoms/{atom_id}.
// -----------------------------------------------------------------------------

// SearchResult is the lightweight projection of a single search row. Pure
// data — no methods beyond optional truncation helpers.
type SearchResult struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Stem         string `json:"stem"`
	QuestionType string `json:"question_type"`
	// Prompt is the live question prompt (ADR-206 read-path JOIN) — the atom's
	// display identity. Empty for a draft atom with no question row yet.
	Prompt string `json:"prompt"`
	// QuestionID is the 1:1 question id (ADR-206), distinct from ID (the
	// atom_id). Empty for a question-less draft atom.
	QuestionID string `json:"question_id"`
	TenantID   string `json:"tenant_id"`
	AuthorGCID string `json:"author_gcid"`
	// Source is the picker provenance hint: "mine" = authored by caller;
	// "saved" = bookmarked by caller; "granted" = usable via an active
	// AtomUsageGrant (ADR-229 — includes repointed orphan editions);
	// "tenant" = usable via the author's tenant-visible consent. Priority
	// when several apply: mine > saved > granted > tenant. Empty when the
	// source is indeterminate (legacy callers).
	Source string `json:"source,omitempty"`
	// ReuseVisibility is the atom's ADR-229 author-consent audience label
	// (private | friends | tenant) — surfaced so the authoring FE can render
	// the consent state truthfully. Empty for legacy projections.
	ReuseVisibility string    `json:"reuse_visibility,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// StemPreviewLen is the visible stem character cap (FE picker is single-line;
// the contract preview is ≤200 chars). Truncation appends "...".
const StemPreviewLen = 200

// TruncateStem returns the input shortened to StemPreviewLen with a trailing
// ellipsis when truncation occurred. Multi-byte safe (counts runes, not
// bytes) so an emoji or CJK character isn't cut mid-codepoint.
func TruncateStem(s string) string {
	runes := []rune(s)
	if len(runes) <= StemPreviewLen {
		return s
	}
	return string(runes[:StemPreviewLen]) + "..."
}

// ErrSearchFilterInvalid is returned by SearchQuestions adapters when the
// filter fails Validate. Wraps the underlying validation error for the
// HTTP boundary to surface a 400 envelope.
var ErrSearchFilterInvalid = errors.New("question: search filter invalid")
