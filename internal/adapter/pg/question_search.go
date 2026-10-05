// question_search.go — pg adapter implementation of
// ports.QuestionRepository.SearchQuestions for the A+ X.2 test-set editor
// question picker (FE B-FE-X5; ADR-155 D1).
//
// PROJECTION SHAPE: the FE picker treats EACH ATOM as a candidate question
// (atoms ARE questions intra-domain — questions sub-entity is a strict subset
// of atoms once authored). The Phyllis seed has 15 atoms but ZERO rows in
// `questions` yet, so the picker must surface atoms regardless. This SELECT
// queries `learning_atoms` directly and returns one SearchResult per atom.
//
// RLS contract — chora_creation_app_rw is NOBYPASSRLS, so the SELECT runs
// inside RunInTenantTx so `SET LOCAL chora.tenant_id` applies in the same
// transaction as the user query (per atom_repository.go #27 pattern).
//
// Soft-delete: `deleted_at IS NULL` is the default filter (ddd-enforcement #6).
// Aligned with chora-contracts/openapi/creation-questions.yaml#searchQuestions
// (envelope simplified to FE B-FE-X5 ask: items + page + per + total).
package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

// SearchQuestions runs the cross-atom search and returns one SearchResult
// per matching atom, plus the total count across all pages.
//
// The total count is computed with a separate COUNT(*) query inside the same
// tenant-tx so the FE can render "X questions" without re-fetching.
func (r *QuestionRepository) SearchQuestions(ctx context.Context, filter question.SearchFilter) ([]question.SearchResult, int, error) {
	if r.tx == nil {
		return nil, 0, errors.New("pg.QuestionRepository.SearchQuestions: no TxQuerier wired")
	}

	f := filter.Normalize()
	if err := f.Validate(); err != nil {
		return nil, 0, fmt.Errorf("%w: %v", question.ErrSearchFilterInvalid, err)
	}

	tenantID := strings.TrimSpace(f.TenantID)
	if tenantID == "" {
		// No explicit tenant — return empty rather than error; the http
		// handler enforces the tenant header upstream. This keeps the repo
		// honest about its invariants without forcing the handler to repeat
		// the empty-tenant guard.
		return []question.SearchResult{}, 0, nil
	}

	// Build the WHERE clause + arg list. $1 is always tenant_id (RLS-friendly
	// even when the WHERE uses it directly for defence-in-depth). All atom
	// columns are qualified `la.` now that the query LEFT JOINs `questions q`
	// (ADR-206 read-path: the search returns the live question prompt + the real
	// question_type + question_id, mirroring the bank member-list JOIN).
	where := []string{
		"la.tenant_id = $1",
		"la.deleted_at IS NULL",
	}
	args := []any{tenantID}
	argIdx := 2

	// q substring on the atom title/body OR the live question prompt
	// (case-insensitive) — so the picker finds a row by what the learner is
	// actually asked, not only the (often stale) atom title (ADR-206 A2).
	if f.Q != "" {
		where = append(where, fmt.Sprintf(
			"(la.title ILIKE $%d OR la.body ILIKE $%d OR q.prompt ILIKE $%d)", argIdx, argIdx, argIdx,
		))
		args = append(args, "%"+f.Q+"%")
		argIdx++
	}

	// types — filter on the REAL question type, falling back to the atom's
	// denormalised question_type for a draft atom with no question row. ADR-206
	// A3: the atom's denormalised type drifts from the question's, so the
	// MCQ/OE filter must key off the live question type.
	//
	// The questions.question_type enum uses 'oe' while learning_atoms uses
	// 'essay' (translateQuestionTypesToAtomTypes maps oe→essay for the atom
	// column). COALESCE picks the question type first, so the filter must
	// include BOTH 'oe' and 'essay' when the user asks for open-ended —
	// otherwise rows where q.question_type='oe' get filtered out by the
	// 'essay' predicate.
	if len(f.Types) > 0 {
		expanded := make([]string, 0, len(f.Types)*2)
		for _, t := range f.Types {
			expanded = append(expanded, t)
			if t == "essay" {
				expanded = append(expanded, "oe")
			}
		}
		placeholders := make([]string, 0, len(expanded))
		for _, t := range expanded {
			placeholders = append(placeholders, fmt.Sprintf("$%d", argIdx))
			args = append(args, t)
			argIdx++
		}
		where = append(where, fmt.Sprintf(
			"COALESCE(q.question_type::text, la.question_type::text) IN (%s)", strings.Join(placeholders, ", "),
		))
	}

	// author_gcid — exact match on the atom author/owner. The author column on
	// learning_atoms is `gcid` (there is NO `author_gcid` column). The HTTP
	// boundary validates the UUID, so the `::uuid` cast cannot trip 22P02.
	if f.AuthorGCID != "" {
		where = append(where, fmt.Sprintf("la.gcid = $%d::uuid", argIdx))
		args = append(args, f.AuthorGCID)
		argIdx++
	}

	// tag[] — OR within the family: match atoms whose `tags` JSONB array
	// contains ANY of the supplied tags via the GIN-indexable `?|` operator
	// (idx_learning_atoms_tags_gin, jsonb_ops). pgx encodes []string as a
	// Postgres text[]; the `::text[]` cast pins the operator's RHS type.
	if len(f.Tags) > 0 {
		where = append(where, fmt.Sprintf("la.tags ?| $%d::text[]", argIdx))
		args = append(args, f.Tags)
		argIdx++
	}

	// state[] — status IN (...) over the atom_status enum. The HTTP boundary
	// translated DRAFT/PUBLISHED/ARCHIVED → draft/published/archived and
	// dropped unknowns, so every `::atom_status` cast is a valid enum value.
	if len(f.States) > 0 {
		placeholders := make([]string, 0, len(f.States))
		for _, s := range f.States {
			placeholders = append(placeholders, fmt.Sprintf("$%d::atom_status", argIdx))
			args = append(args, s)
			argIdx++
		}
		where = append(where, fmt.Sprintf(
			"la.status IN (%s)", strings.Join(placeholders, ", "),
		))
	}

	// atom_id[] — restrict to a specific set of atoms. UUIDs validated at the
	// HTTP boundary; `::uuid` cast for explicitness.
	if len(f.AtomIDs) > 0 {
		placeholders := make([]string, 0, len(f.AtomIDs))
		for _, id := range f.AtomIDs {
			placeholders = append(placeholders, fmt.Sprintf("$%d::uuid", argIdx))
			args = append(args, id)
			argIdx++
		}
		where = append(where, fmt.Sprintf(
			"la.atom_id IN (%s)", strings.Join(placeholders, ", "),
		))
	}

	// ── ADR-229 WS-2 (CHO-2133) — chokepoint 1: the consent disjunct ──────
	//
	// The picker never returns an atom the caller may not REUSE. The
	// entitled set per D4.1 (narrowed by Amendment A1.4) is
	//
	//   E = mine ∪ tenant-visible ∪ granted
	//
	//   mine            la.gcid = $caller (the author sees own atoms at any
	//                   lifecycle state / visibility)
	//   tenant-visible  la.reuse_visibility = 'tenant' AND la.status =
	//                   'published' — shaped for the mig-0029 partial index
	//                   (tenant_id, reuse_visibility) WHERE deleted_at IS
	//                   NULL (this WHERE already pins la.tenant_id = $1 AND
	//                   la.deleted_at IS NULL). Draft/archived atoms are not
	//                   reusable exports, so the leg requires published.
	//   granted         la.atom_id = ANY($granted) — ACTIVE AtomUsageGrant
	//                   atom ids hydrated per-request from chora-sharing
	//                   GetReuseContext. NO status predicate: a grant is
	//                   explicit consent and must keep surfacing repointed
	//                   orphan editions (A1.1 continuity) regardless of the
	//                   original's lifecycle. This SQL references NO orphan
	//                   column by cross-lane contract — orphan editions are
	//                   minted reuse_visibility='private' forever, so they
	//                   reach the caller EXCLUSIVELY through this leg.
	//
	// FRIENDS SEAM (Amendment A1.4 — deferred until ADR-230 B-lite.2
	// un-hides the A+ `friends` audience): when the audience ships, append
	//
	//   OR (la.reuse_visibility = 'friends' AND la.status = 'published'
	//       AND la.gcid = ANY($friendGCIDs))
	//
	// fed by ports.ReuseContext.FriendGCIDs (already returned by
	// GetReuseContext; deliberately unconsumed here until then).
	//
	// Source semantics (ux_unified_atom_picker.md × ADR-229):
	//   mine  → mine
	//   saved → saved ∩ E   (a bookmark alone no longer authorises reuse;
	//                        saved = la.atom_id = ANY($saved) AND published)
	//   all   → E           (saved ∩ E ⊆ E, so no separate saved leg)
	//
	// An empty CallerGCID matches NOTHING (identity-less callers must not
	// harvest tenant-visible rows — fail-closed, preserving the pre-WS-2
	// posture). All legs are la-qualified: the LEFT JOIN `questions q` also
	// carries atom_id, so a bare `atom_id = ANY(...)` is ambiguous (42702) —
	// the pre-WS-2 saved leg had this latent bug.
	entitledClause := func() string {
		if f.CallerGCID == "" {
			return "FALSE"
		}
		legs := make([]string, 0, 3)
		legs = append(legs, fmt.Sprintf("la.gcid = $%d", argIdx))
		args = append(args, f.CallerGCID)
		argIdx++
		legs = append(legs, "(la.reuse_visibility = 'tenant' AND la.status = 'published')")
		if len(f.GrantedAtomIDs) > 0 {
			legs = append(legs, fmt.Sprintf("la.atom_id = ANY($%d)", argIdx))
			args = append(args, f.GrantedAtomIDs)
			argIdx++
		}
		return "(" + strings.Join(legs, " OR ") + ")"
	}
	switch f.Source {
	case question.SourceMine:
		if f.CallerGCID == "" {
			where = append(where, "FALSE")
		} else {
			where = append(where, fmt.Sprintf("la.gcid = $%d", argIdx))
			args = append(args, f.CallerGCID)
			argIdx++
		}
	case question.SourceSaved:
		if len(f.SavedAtomIDs) == 0 || f.CallerGCID == "" {
			where = append(where, "FALSE")
		} else {
			saved := fmt.Sprintf("la.atom_id = ANY($%d) AND la.status = 'published'", argIdx)
			args = append(args, f.SavedAtomIDs)
			argIdx++
			where = append(where, fmt.Sprintf("(%s AND %s)", saved, entitledClause()))
		}
	case question.SourceAll:
		where = append(where, entitledClause())
	}

	whereClause := strings.Join(where, " AND ")

	// COUNT(*) total — separate query, same transaction so the snapshot is
	// consistent. Per the contract we always emit `total` (FE B-FE-X5 ask
	// envelope; ADR-155 D1's include_total=true on first page is implicit
	// here because the picker always shows the count).
	// FROM clause shared by the count + page queries: LEFT JOIN the 1:1
	// `questions` row (atom_id + tenant_id; non-deleted). LEFT (not INNER) so
	// question-less draft atoms still surface (ADR-206 — the picker must show
	// drafts). The (atom_id, tenant_id) WHERE deleted_at IS NULL partial-unique
	// index makes this 1:1 + indexed.
	const fromJoin = `FROM learning_atoms la
          LEFT JOIN questions q
            ON q.atom_id = la.atom_id
           AND q.tenant_id = la.tenant_id
           AND q.deleted_at IS NULL`

	// COUNT(DISTINCT la.atom_id) — defensive against any row multiplication
	// from the join. The 1:1 invariant makes it a no-op, but DISTINCT keeps the
	// total honest even if a stray duplicate question ever slipped the index.
	countSQL := fmt.Sprintf(
		"SELECT COUNT(DISTINCT la.atom_id) %s WHERE %s",
		fromJoin, whereClause,
	)

	// Page query. ADR-206 read-path: the atom's display identity is its
	// question's prompt, so the projection carries the live `q.prompt`, the REAL
	// `q.question_type` (falling back to the atom's denormalised type for a
	// draft atom), and `q.question_id`. Legacy `title`/`stem` are still emitted
	// for back-compat (the FE prefers `prompt`, Slice 5). NULL-able text is
	// COALESCEd so Scan targets stay non-nullable `string`.
	listSQL := fmt.Sprintf(`
        SELECT la.atom_id::text,
               COALESCE(la.title, ''),
               COALESCE(la.stem, la.body, ''),
               COALESCE(q.question_type::text, la.question_type::text, ''),
               la.tenant_id::text,
               la.gcid,
               la.created_at,
               la.updated_at,
               COALESCE(q.prompt, ''),
               COALESCE(q.question_id::text, ''),
               COALESCE(la.reuse_visibility, 'private')
          %s
         WHERE %s
         %s
         LIMIT $%d OFFSET $%d
    `, fromJoin, whereClause, buildOrderBy(f.Sorts), argIdx, argIdx+1)
	listArgs := append([]any{}, args...)
	listArgs = append(listArgs, f.Per, f.Offset())

	var (
		results []question.SearchResult
		total   int
	)

	txErr := r.tx.RunInTenantTx(ctx, tenantID, func(ctx context.Context, tx Tx) error {
		// Total first — small query, deterministic ordering.
		if err := tx.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
			if !errors.Is(err, ErrNoRows) {
				return fmt.Errorf("count: %w", err)
			}
			total = 0
		}

		rows, err := tx.Query(ctx, listSQL, listArgs...)
		if err != nil {
			return fmt.Errorf("list: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var (
				atomID, title, body, questionType, tenantStr, gcid string
				prompt, questionID, reuseVisibility                string
				createdAt, updatedAt                               time.Time
			)
			if err := rows.Scan(
				&atomID, &title, &body, &questionType, &tenantStr, &gcid, &createdAt, &updatedAt, &prompt, &questionID, &reuseVisibility,
			); err != nil {
				return fmt.Errorf("scan: %w", err)
			}
			results = append(results, question.SearchResult{
				ID:              atomID,
				Title:           title,
				Stem:            question.TruncateStem(body),
				QuestionType:    questionType,
				Prompt:          prompt,
				QuestionID:      questionID,
				TenantID:        tenantStr,
				AuthorGCID:      gcid,
				Source:          sourceHint(atomID, gcid, reuseVisibility, f),
				ReuseVisibility: reuseVisibility,
				CreatedAt:       createdAt.UTC(),
				UpdatedAt:       updatedAt.UTC(),
			})
		}
		return rows.Err()
	})
	if txErr != nil {
		return nil, 0, fmt.Errorf("pg.QuestionRepository.SearchQuestions: %w", txErr)
	}
	if results == nil {
		results = []question.SearchResult{}
	}
	return results, total, nil
}

// sourceHint stamps the picker provenance badge on a search result row.
// Priority when several apply: mine > saved > granted > tenant (ADR-229
// WS-2 — the FE "Usable by me" affordance says WHY a row is usable).
// Returns "" when no disjunct matched (legacy callers that omit Source).
func sourceHint(atomID, gcid, reuseVisibility string, f question.SearchFilter) string {
	if f.Source == question.SourceMine || f.Source == question.SourceAll {
		if gcid == f.CallerGCID && f.CallerGCID != "" {
			return "mine"
		}
	}
	if f.Source == question.SourceSaved || f.Source == question.SourceAll {
		for _, sid := range f.SavedAtomIDs {
			if sid == atomID {
				return "saved"
			}
		}
	}
	if f.Source == question.SourceSaved || f.Source == question.SourceAll {
		for _, gid := range f.GrantedAtomIDs {
			if gid == atomID {
				return "granted"
			}
		}
		if reuseVisibility == "tenant" {
			return "tenant"
		}
	}
	return ""
}

// buildOrderBy renders a safe ORDER BY clause from the validated sort keys.
// Column names come ONLY from the domain whitelist (question.Sort.Column) and
// the direction is constrained to ASC/DESC — the raw user-supplied field string
// never reaches the SQL text, so there is no injection surface. A stable
// `atom_id DESC` tiebreak is always appended; absent sorts default to
// `created_at DESC` (preserving the pre-filter behaviour exactly).
func buildOrderBy(sorts []question.Sort) string {
	cols := make([]string, 0, len(sorts)+1)
	for _, s := range sorts {
		col, ok := s.Column()
		if !ok {
			continue // defence-in-depth: the parser already rejected unknowns
		}
		cols = append(cols, col+" "+s.SQLDirection())
	}
	if len(cols) == 0 {
		cols = append(cols, "la.created_at DESC")
	}
	cols = append(cols, "la.atom_id DESC")
	return "ORDER BY " + strings.Join(cols, ", ")
}
