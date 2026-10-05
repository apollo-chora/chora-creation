// Package questionbank is the QuestionBank aggregate of the Content Creation domain
// (W3.B.1 — reusable question-atom pool, 2026-06-28).
//
// A QuestionBank is a CURATION aggregate: it tracks ordered question_id
// references to Questions (the Question sub-entity of LearningAtom) but DOES
// NOT own question content. LearningAtom/Question remain the primary
// aggregate roots per .claude/rules/ddd-enforcement.md (Aggregate Invariant
// #1 / #3) and CLAUDE.md §1 — "collections in other domains query atoms;
// they never own them". The QuestionBank is the proven Collection template
// (internal/domain/collection) re-applied to question references, enriched
// with tags + scoped sharing for test-set assembly (W3.B.2 builds the
// TestSet-assembly seam ON this aggregate).
//
// Cross-aggregate references (QuestionBankItem.QuestionID → Question.QuestionID)
// are UUIDs without FK constraint per ddd-enforcement Aggregate Invariant #3.
// The Service (see service.go) validates existence against the
// chora_creation.questions table via the QuestionLookup port (TENANT-SCOPED)
// before the repository persists the membership row.
//
// Soft-delete only (deleted_at) per Aggregate Invariant #4/#5. Cascade
// soft-delete QuestionBankItem children with the parent QuestionBank per Invariant
// #5 — handled at the repository boundary.
//
// HARD INVARIANTS enforced by this package:
//  1. Name required (1..200 chars after trim).
//  2. Description ≤2000 chars.
//  3. Visibility ∈ {PRIVATE, TENANT_INTERNAL}; default PRIVATE. There is NO
//     cross-tenant PUBLIC value — question pools never cross tenants in v1
//     (exam-security). This is the deliberate divergence from Collection.
//  4. Tags trimmed + deduped + capped: at most MaxTags entries, each ≤
//     MaxTagLength chars.
//  5. At most MaxItemsPerBank questions per bank.
//  6. No mutation of soft-deleted banks (returns ErrQuestionBankDeleted).
//  7. Append-only positions: a freshly added question takes the next free
//     slot; removal compacts positions.
//
// HEXAGONAL: this package is dependency-free w.r.t. infrastructure. Only
// stdlib + google/uuid are imported. The repository / lookup / event-publisher
// ports live alongside in repository.go + events.go.
package questionbank

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// -----------------------------------------------------------------------------
// Enums + constants
// -----------------------------------------------------------------------------

// Visibility scope of a QuestionBank. Aligned with the question_bank_visibility
// Postgres enum (chora_creation schema, migration 0026). Unlike Collection there
// is intentionally NO PUBLIC value — a question pool is never readable across
// tenants in v1 (exam-security).
type Visibility string

const (
	// VisibilityPrivate — only the owning author/instructor can read.
	VisibilityPrivate Visibility = "PRIVATE"
	// VisibilityTenantInternal — any GCID in the same tenant can read.
	VisibilityTenantInternal Visibility = "TENANT_INTERNAL"
)

// Valid returns true iff v is one of the recognised visibility values. PUBLIC
// (and any other value) is rejected.
func (v Visibility) Valid() bool {
	switch v {
	case VisibilityPrivate, VisibilityTenantInternal:
		return true
	}
	return false
}

const (
	// MaxItemsPerBank is the hard cap on the number of questions a single
	// QuestionBank may contain.
	MaxItemsPerBank = 1000
	// MaxNameLength is the cap on QuestionBank.Name.
	MaxNameLength = 200
	// MaxDescriptionLength is the cap on QuestionBank.Description.
	MaxDescriptionLength = 2000
	// MaxTags is the cap on the number of tags per bank.
	MaxTags = 20
	// MaxTagLength is the cap on a single tag's length (after trim).
	MaxTagLength = 50
)

// -----------------------------------------------------------------------------
// Sentinel errors
// -----------------------------------------------------------------------------

// -----------------------------------------------------------------------------
// Whose fault is it? (CHO-2175 — the same three sentinels the collection
// aggregate carries, for the same reason.)
//
// writeQuestionBankError had a single default arm that answered 400
// CREATION_QUESTION_BANK_INVALID — "your request was malformed" — for a dead
// connection pool and an RLS-blocked write alike, with the raw internal error
// pasted into the message. A 4xx is a promise the caller can fix it by sending
// something different. We do not make that promise on their behalf: 4xx is
// client error, so nothing alerts on it, which is exactly how CHO-2173 (AddAtom
// never worked, ever) hid in production for months.
// -----------------------------------------------------------------------------

// ErrInvalid — a genuinely-malformed request. The ONLY class permitted to reach
// the caller as a 4xx on their own input.
var ErrInvalid = errors.New("invalid question bank request")

// ErrRepository — a LOCAL persistence failure (chora_creation's own datastore).
// Maps to HTTP 500 CREATION_QUESTION_BANK_REPO_ERROR. Not 502: that is reserved
// for a failing upstream SERVICE, and conflating the two sends an operator to
// debug the wrong thing.
var ErrRepository = errors.New("question bank repository failure")

// ErrGateNotWired — a required port is absent: a DEPLOYMENT defect, not a
// request defect. Defensive here rather than live, because the router already
// refuses to MOUNT the question-bank routes unless the repository and the
// question lookup are both wired — a stronger guard than the collection lane
// had. Kept and wrapped so the defence cannot rot into a 400 if a future port
// escapes the mount check.
var ErrGateNotWired = errors.New("a required question-bank port is not wired")

// ErrNotFound is the canonical sentinel for a missing-or-soft-deleted
// QuestionBank at the repository boundary.
var ErrNotFound = errors.New("question bank not found")

// ErrQuestionBankDeleted is returned by mutators when the aggregate is in a
// soft-deleted state.
var ErrQuestionBankDeleted = errors.New("question bank is soft-deleted; cannot mutate")

// ErrDuplicateQuestion is returned by AddItem when the supplied question_id is
// already an active member of this bank.
var ErrDuplicateQuestion = errors.New("question already in question bank")

// ErrItemCapExceeded is returned by AddItem when MaxItemsPerBank has already
// been reached.
var ErrItemCapExceeded = errors.New("question bank has reached its question cap")

// ErrQuestionNotInBank is returned by RemoveItem when the supplied question_id
// is not a member of this bank.
var ErrQuestionNotInBank = errors.New("question not in question bank")

// ErrQuestionDoesNotExist is returned by the Service when the supplied
// question_id does not resolve to any active questions row in the bank's
// tenant. The handler maps it to HTTP 404.
var ErrQuestionDoesNotExist = errors.New("referenced question does not exist")

// ErrForbidden is returned when the caller is not the question-bank owner.
// Mapped to HTTP 403 at the handler boundary.
var ErrForbidden = errors.New("caller is not the question bank owner")

// ErrReorderMismatch is returned by ReorderItems when the supplied ordering is
// not an exact permutation of the bank's current member questions (wrong
// count, an unknown id, or a duplicate). Mapped to HTTP 400.
var ErrReorderMismatch = errors.New("reorder list must be a permutation of the bank's current questions")

// -----------------------------------------------------------------------------
// Aggregate root + child entity
// -----------------------------------------------------------------------------

// QuestionBank is the curation aggregate root. Mutations go through methods on
// this type; never expose mutator helpers from sibling packages.
type QuestionBank struct {
	QuestionBankID string     `json:"question_bank_id"`
	TenantID       string     `json:"tenant_id"`
	OwnerGCID      string     `json:"owner_gcid"`
	Name           string     `json:"name"`
	Description    string     `json:"description,omitempty"`
	Visibility     Visibility `json:"visibility"`
	Tags           []string   `json:"tags"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`

	// Items is the ordered membership list (QuestionBankItem child entities).
	// Accessed ONLY through QuestionBank methods (AddItem/RemoveItem) per
	// ddd-enforcement Aggregate Invariant #2. The in-memory slice holds only
	// ACTIVE items (DeletedAt nil); tombstoning lives at the repository.
	Items []*QuestionBankItem `json:"items"`
}

// QuestionBankItem is a child entity: a single question membership row. The
// QuestionID is a cross-aggregate reference to Question; the membership row
// does NOT own question content.
type QuestionBankItem struct {
	QuestionBankID string     `json:"question_bank_id"`
	QuestionID     string     `json:"question_id"`
	TenantID       string     `json:"tenant_id"`
	Position       int        `json:"position"`
	AddedAt        time.Time  `json:"added_at"`
	DeletedAt      *time.Time `json:"deleted_at,omitempty"`
}

// -----------------------------------------------------------------------------
// Constructor
// -----------------------------------------------------------------------------

// NewParams is the constructor input for New.
type NewParams struct {
	TenantID    string
	OwnerGCID   string
	Name        string
	Description string
	// Visibility is optional; empty defaults to PRIVATE.
	Visibility Visibility
	// Tags are normalised (trim, dedupe, cap) by New.
	Tags []string
}

// New constructs a fresh QuestionBank. Returns an error if inputs violate
// aggregate invariants.
func New(p NewParams) (*QuestionBank, error) {
	if strings.TrimSpace(p.TenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id is required", ErrInvalid)
	}
	if strings.TrimSpace(p.OwnerGCID) == "" {
		return nil, fmt.Errorf("%w: owner_gcid is required", ErrInvalid)
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if len(name) > MaxNameLength {
		return nil, fmt.Errorf("%w: name too long: %d > %d", ErrInvalid, len(name), MaxNameLength)
	}
	description := strings.TrimSpace(p.Description)
	if len(description) > MaxDescriptionLength {
		return nil, fmt.Errorf("%w: description too long: %d > %d", ErrInvalid, len(description), MaxDescriptionLength)
	}
	visibility := p.Visibility
	if visibility == "" {
		visibility = VisibilityPrivate
	}
	if !visibility.Valid() {
		return nil, fmt.Errorf("%w: invalid visibility: %q (allowed: PRIVATE, TENANT_INTERNAL)", ErrInvalid, string(visibility))
	}
	tags, err := normaliseTags(p.Tags)
	if err != nil {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuidv7: %w", err)
	}
	now := time.Now().UTC()
	return &QuestionBank{
		QuestionBankID: id.String(),
		TenantID:       p.TenantID,
		OwnerGCID:      p.OwnerGCID,
		Name:           name,
		Description:    description,
		Visibility:     visibility,
		Tags:           tags,
		CreatedAt:      now,
		UpdatedAt:      now,
		Items:          nil,
	}, nil
}

// normaliseTags trims each tag, drops empties, de-duplicates (preserving first
// occurrence order), and enforces the per-tag length + count caps. Fail-loud:
// an over-long tag or an over-cap count returns an error rather than silently
// truncating.
func normaliseTags(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}
		if len(t) > MaxTagLength {
			return nil, fmt.Errorf("%w: tag too long: %q (%d > %d)", ErrInvalid, t, len(t), MaxTagLength)
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) > MaxTags {
		return nil, fmt.Errorf("%w: too many tags: %d > %d", ErrInvalid, len(out), MaxTags)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// Mutators
// -----------------------------------------------------------------------------

// UpdateParams is the partial-update payload (PATCH semantics). A nil field is
// left unchanged; a non-nil field is applied.
type UpdateParams struct {
	Name        *string
	Description *string
	Visibility  *Visibility
	// Tags, when non-nil, REPLACES the tag set (normalised on apply).
	Tags *[]string
}

// UpdateMetadata mutates the bank per UpdateParams + bumps UpdatedAt. Refuses
// to touch a soft-deleted bank.
func (b *QuestionBank) UpdateMetadata(p UpdateParams) error {
	if b.DeletedAt != nil {
		return ErrQuestionBankDeleted
	}
	if p.Name != nil {
		name := strings.TrimSpace(*p.Name)
		if name == "" {
			return fmt.Errorf("%w: name cannot be empty", ErrInvalid)
		}
		if len(name) > MaxNameLength {
			return fmt.Errorf("%w: name too long: %d > %d", ErrInvalid, len(name), MaxNameLength)
		}
		b.Name = name
	}
	if p.Description != nil {
		desc := strings.TrimSpace(*p.Description)
		if len(desc) > MaxDescriptionLength {
			return fmt.Errorf("%w: description too long: %d > %d", ErrInvalid, len(desc), MaxDescriptionLength)
		}
		b.Description = desc
	}
	if p.Visibility != nil {
		if !p.Visibility.Valid() {
			return fmt.Errorf("invalid visibility: %q (allowed: PRIVATE, TENANT_INTERNAL)", string(*p.Visibility))
		}
		b.Visibility = *p.Visibility
	}
	if p.Tags != nil {
		tags, err := normaliseTags(*p.Tags)
		if err != nil {
			return err
		}
		b.Tags = tags
	}
	b.UpdatedAt = time.Now().UTC().Add(time.Nanosecond) // strict monotonicity for tests
	return nil
}

// AddItem appends a question to the bank. Enforces the cap + duplicate
// invariants. Position is the next free slot (len(b.Items)).
//
// There is deliberately NO cross-tenant gate here (unlike Collection.AddAtom):
// QuestionBank has no PUBLIC visibility, and the Service resolves the question via
// a TENANT-SCOPED QuestionLookup, so a cross-tenant question can never reach
// this method (it 404s at the service as ErrQuestionDoesNotExist).
func (b *QuestionBank) AddItem(questionID string) error {
	if b.DeletedAt != nil {
		return ErrQuestionBankDeleted
	}
	questionID = strings.TrimSpace(questionID)
	if questionID == "" {
		return errors.New("question_id is required")
	}
	if len(b.Items) >= MaxItemsPerBank {
		return fmt.Errorf("%w (cap=%d)", ErrItemCapExceeded, MaxItemsPerBank)
	}
	for _, it := range b.Items {
		if it.QuestionID == questionID {
			return fmt.Errorf("%w: %s", ErrDuplicateQuestion, questionID)
		}
	}
	now := time.Now().UTC()
	b.Items = append(b.Items, &QuestionBankItem{
		QuestionBankID: b.QuestionBankID,
		QuestionID:     questionID,
		TenantID:       b.TenantID,
		Position:       len(b.Items),
		AddedAt:        now,
	})
	b.UpdatedAt = now
	return nil
}

// RemoveItem drops a question from the bank + compacts positions. Returns
// ErrQuestionNotInBank if the question is not a member.
func (b *QuestionBank) RemoveItem(questionID string) error {
	if b.DeletedAt != nil {
		return ErrQuestionBankDeleted
	}
	questionID = strings.TrimSpace(questionID)
	if questionID == "" {
		return errors.New("question_id is required")
	}
	idx := -1
	for i, it := range b.Items {
		if it.QuestionID == questionID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("%w: %s", ErrQuestionNotInBank, questionID)
	}
	b.Items = append(b.Items[:idx], b.Items[idx+1:]...)
	for i, it := range b.Items {
		it.Position = i
	}
	b.UpdatedAt = time.Now().UTC()
	return nil
}

// ReorderItems reorders the bank's active items to match orderedQuestionIDs and
// reassigns positions 0..n-1 in that order. orderedQuestionIDs MUST be an exact
// permutation of the current member question_ids — same set, no additions,
// removals, or duplicates — else ErrReorderMismatch (this endpoint reorders
// only; use AddItem/RemoveItem to change membership). No-op safe on an empty
// bank (empty slice).
func (b *QuestionBank) ReorderItems(orderedQuestionIDs []string) error {
	if b.DeletedAt != nil {
		return ErrQuestionBankDeleted
	}
	if len(orderedQuestionIDs) != len(b.Items) {
		return fmt.Errorf("%w: got %d ids, bank has %d items",
			ErrReorderMismatch, len(orderedQuestionIDs), len(b.Items))
	}
	byID := make(map[string]*QuestionBankItem, len(b.Items))
	for _, it := range b.Items {
		byID[it.QuestionID] = it
	}
	seen := make(map[string]struct{}, len(orderedQuestionIDs))
	reordered := make([]*QuestionBankItem, 0, len(orderedQuestionIDs))
	for _, qid := range orderedQuestionIDs {
		qid = strings.TrimSpace(qid)
		it, ok := byID[qid]
		if !ok {
			return fmt.Errorf("%w: %q is not a member of this bank", ErrReorderMismatch, qid)
		}
		if _, dup := seen[qid]; dup {
			return fmt.Errorf("%w: duplicate id %q", ErrReorderMismatch, qid)
		}
		seen[qid] = struct{}{}
		reordered = append(reordered, it)
	}
	for i, it := range reordered {
		it.Position = i
	}
	b.Items = reordered
	b.UpdatedAt = time.Now().UTC()
	return nil
}

// Delete soft-deletes the bank (sets DeletedAt). Idempotent: a no-op on an
// already-deleted bank. Per ddd-enforcement Aggregate Invariant #4 this is a
// SOFT delete — hard delete is reserved for crypto-shred / cold archive.
//
// Child QuestionBankItem rows are cascade-soft-deleted at the repository boundary
// per Aggregate Invariant #5; the aggregate itself does NOT mutate child rows
// here (preserves in-memory state for a future event publisher to read).
func (b *QuestionBank) Delete() error {
	if b.DeletedAt != nil {
		return nil
	}
	now := time.Now().UTC()
	b.DeletedAt = &now
	b.UpdatedAt = now
	return nil
}

// IsActive returns true iff the bank is not soft-deleted.
func (b *QuestionBank) IsActive() bool { return b.DeletedAt == nil }

// -----------------------------------------------------------------------------
// Read filters
// -----------------------------------------------------------------------------

// ListFilter is the query filter for repository List operations.
type ListFilter struct {
	// OwnerGCID filters to banks owned by this GCID. Required by the
	// /api/v1/me/question-banks endpoint.
	OwnerGCID string
	// Visibility narrows by visibility scope when non-empty.
	Visibility Visibility
	// Q is an optional case-insensitive substring needle matched against the
	// bank Name. Empty = match all.
	Q string
	// Tags filters to banks whose tags overlap ANY of these (OR within the
	// family). Empty = match all. Case-sensitive (mirrors the TEXT[] `&&`
	// array-overlap operator the pg adapter uses).
	Tags []string
	// Sorts is the ordered sort spec (whitelisted field+direction). Empty =
	// the default created_at:desc.
	Sorts []Sort
	// Limit caps result size; 0 means default (handled by the adapter).
	Limit int
	// Offset is the pagination offset.
	Offset int
}

// MatchesQ reports whether the bank name matches the free-text needle
// (case-insensitive substring). Empty Q = match all. Mirrors the pg adapter's
// `name ILIKE`.
func (f ListFilter) MatchesQ(name string) bool {
	needle := strings.ToLower(strings.TrimSpace(f.Q))
	if needle == "" {
		return true
	}
	return strings.Contains(strings.ToLower(name), needle)
}

// MatchesTags reports whether the bank tags overlap ANY filter tag (OR within
// the family). Empty Tags = match all. CASE-SENSITIVE to mirror the TEXT[]
// `&&` operator.
func (f ListFilter) MatchesTags(bankTags []string) bool {
	if len(f.Tags) == 0 {
		return true
	}
	have := make(map[string]struct{}, len(bankTags))
	for _, t := range bankTags {
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
// Sort — whitelisted sort key for listMyQuestionBanks (CHO-1899).
//
// The column whitelist lives in the domain so the pg adapter renders ORDER BY
// from MAPPED CONSTANTS only — the raw user-supplied field string never reaches
// the SQL, eliminating the injection surface.
// -----------------------------------------------------------------------------

// Sort field + direction wire values.
const (
	SortFieldName      = "name"
	SortFieldCreatedAt = "created_at"
	SortFieldUpdatedAt = "updated_at"

	SortDirAsc  = "asc"
	SortDirDesc = "desc"
)

// Sort is one validated sort key. Field is the contract sort field (NOT a SQL
// column); Direction is asc|desc.
type Sort struct {
	Field     string
	Direction string
}

// sortFieldColumns maps each whitelisted sort field to its physical
// question_banks column. This map IS the SQL-injection guard: the pg adapter
// only ever emits values found here.
var sortFieldColumns = map[string]string{
	SortFieldName:      "name",
	SortFieldCreatedAt: "created_at",
	SortFieldUpdatedAt: "updated_at",
}

// Column returns the whitelisted physical column for the sort field; ok=false
// when the field is not whitelisted.
func (s Sort) Column() (string, bool) {
	col, ok := sortFieldColumns[s.Field]
	return col, ok
}

// SQLDirection returns the rendered SQL direction. Anything other than an
// explicit `asc` renders DESC (the default + a safe fallback).
func (s Sort) SQLDirection() string {
	if s.Direction == SortDirAsc {
		return "ASC"
	}
	return "DESC"
}

// Validate reports whether the sort key's field + direction are whitelisted.
func (s Sort) Validate() error {
	if _, ok := sortFieldColumns[s.Field]; !ok {
		return fmt.Errorf("questionbank.Sort: unsupported sort field %q (allowed: name, created_at, updated_at)", s.Field)
	}
	if s.Direction != SortDirAsc && s.Direction != SortDirDesc {
		return fmt.Errorf("questionbank.Sort: unsupported sort direction %q (allowed: asc, desc)", s.Direction)
	}
	return nil
}

// ParseSorts parses the `{field}:{dir}[,{field}:{dir}…]` grammar into an ordered
// []Sort. Empty input yields (nil, nil) so the caller applies the default
// created_at:desc. Returns an error on any malformed/unknown key (the HTTP
// boundary maps it to a 400).
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
			return nil, fmt.Errorf("questionbank.ParseSorts: malformed sort key %q (want field:direction)", p)
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

// SortBanks orders banks in place per the sort keys, applying a stable
// question_bank_id DESC tiebreak. Empty sorts default to created_at DESC. The
// in-memory adapter calls this to mirror the pg ORDER BY exactly.
func SortBanks(banks []*QuestionBank, sorts []Sort) {
	eff := sorts
	if len(eff) == 0 {
		eff = []Sort{{Field: SortFieldCreatedAt, Direction: SortDirDesc}}
	}
	sort.SliceStable(banks, func(i, j int) bool {
		a, b := banks[i], banks[j]
		for _, s := range eff {
			c := compareBankField(a, b, s.Field)
			if c == 0 {
				continue
			}
			if s.Direction == SortDirAsc {
				return c < 0
			}
			return c > 0
		}
		// Stable tiebreak: question_bank_id DESC (mirror the pg ORDER BY).
		return a.QuestionBankID > b.QuestionBankID
	})
}

// compareBankField returns -1/0/1 comparing two banks on a whitelisted field.
func compareBankField(a, b *QuestionBank, field string) int {
	switch field {
	case SortFieldName:
		return strings.Compare(a.Name, b.Name)
	case SortFieldUpdatedAt:
		return a.UpdatedAt.Compare(b.UpdatedAt)
	case SortFieldCreatedAt:
		return a.CreatedAt.Compare(b.CreatedAt)
	default:
		return a.CreatedAt.Compare(b.CreatedAt)
	}
}
