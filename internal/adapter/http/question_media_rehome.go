// question_media_rehome.go — ADR-210: shared durable image re-home for question
// revisions, and the tenant MEDIA OWNERSHIP policy every seam that persists or
// serves a question image ref must enforce.
//
// # The ownership invariant
//
// An image reference may be persisted or served only when its OBJECT KEY sits
// under the caller's own per-tenant namespace — ports.TenantMediaPrefix(tenantID)
// (`tenants/{tenant_id}/`). A trusted origin + bucket is NOT sufficient: the
// durable bucket is shared by every tenant, and SignDownloadURL scopes on bucket
// only, so a foreign ref that reached storage would be signed and served, and a
// durable one never expires. The boundary is TENANT-level, deliberately — see
// the policy note on ports.TenantMediaPrefix for why job/author-level scoping is
// a separate product question.
//
// The invariant is enforced on every seam below WHENEVER A STORAGE ADAPTER IS
// WIRED. With NO storage adapter wired the service runs in EXTERNAL-IMAGE MODE,
// which does NOT enforce it — see that section below for its exact policy.
//
// # Every seam, and what each enforces
//
// WRITE (persists an image ref):
//
//   - createQuestion, questions_handler.go (POST /api/atoms/{id}/questions) —
//     guardMediaPayload BEFORE the question row exists. A refusal persists
//     nothing.
//   - patchQuestion, questions_handler.go (PATCH .../questions/{qid}) —
//     guardMediaPayload BEFORE AppendRevision, over the EFFECTIVE payload
//     including a payload-omitting edit's carried-forward refs. A refusal
//     appends no revision.
//   - cloneAtom, atom_clone_handler.go (POST /api/atoms/{id}/clone) —
//     guardMediaPayload over COPIES of the source payloads BEFORE the cloned
//     atom is saved. A source lookup being tenant-scoped says nothing about the
//     historical refs the stored row carries.
//   - acceptCandidate, question_jobs_handler.go (POST .../question-jobs/{jid}/accept)
//     — guardMediaPayload BEFORE the question row exists.
//   - publishAtom, publish_atom_handler.go — ReHomeRevision. Introduces no new
//     refs (it re-homes the ALREADY-STORED revision), so it cannot introduce a
//     foreign one; with no copier wired it persists nothing at all.
//   - cloneQuestionOnto, pubsub/orphan_required_subscriber.go — OWNERSHIP-ONLY
//     validation over the latest revision's refs BEFORE the orphan question is
//     saved (no copier: this path has no caller-supplied input — see the note
//     there — and an already-durable ref needs no copy). A legacy revision
//     already carrying a foreign-tenant ref is refused, not re-persisted. With
//     no storage adapter wired (nil signer) it is the external-image mode.
//   - the qgen draft layer (pubsub/ai_assist_terminal_subscriber.go,
//     pubsub/candidate_normalizer.go) writes transient refs into the ComposeJob
//     candidate JSON, never into a question payload; the accept seam guards them
//     on the way out.
//
// SERVE (returns a signed URL):
//
//   - MintAtomMediaDownloadURL, grpc/creation_server.go — TenantOwnsRef per URI
//     before signing (chora-delivery's learner mint).
//   - mintMediaRef, questions_handler.go — the author re-open projection.
//   - signQuestionImage, handler.go — the learner projection.
//
// The two HTTP serve paths and the gRPC one each gate on TenantOwnsRef for
// EVERY non-empty ref — durable AND transient http(s) — because SignDownloadURL
// itself only scopes the bucket: a legacy row written before these write guards
// existed, or a payload carrying another tenant's transient object on the
// trusted origin, would otherwise still be signed and served.
//
// # External-image mode (no storage adapter wired)
//
// When NEITHER the re-homer NOR the download signer is wired, guardMediaPayload
// is a deliberate no-op: there is no grammar with which to classify a reference
// and no signed URL can ever be minted. The service runs in EXTERNAL-IMAGE
// MODE. Its policy, explicitly:
//
//   - refs are persisted verbatim, WITHOUT any ownership classification;
//   - the tenant-isolation invariant above is NOT enforced on the write seam
//     in this mode — this is a documented deployment shape (the alternative,
//     refusing every image ref, would break authoring on such a deployment for
//     no security gain), NOT a silent fail-open;
//   - what the mode DOES guarantee: a durable ref can never be SERVED — with no
//     signer, nothing can mint it, and both read projections drop it outright —
//     and an http(s) ref renders only while it remains fetchable. The same
//     policy governs the read projections' no-signer branch and the orphan
//     path's nil-signer branch, so every seam agrees.
//
// # The copier is not the gate
//
// guardMediaPayload enforces OWNERSHIP whenever a storage adapter is reachable —
// through the re-homer when it is wired, else through the download signer — and
// only skips the COPY when no copier exists.
//
// # Durability
//
// It leans entirely on the idempotent, fail-loud mediarehome.Service, which
// turns a transient signed URL into a durable object ref. Nil-safe construction:
// a nil re-home service or question repo yields a nil re-homer.
package httpadapter

import (
	"context"
	"fmt"
	"strings"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/mediarehome"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// questionImageReHomer durably re-homes a question's W8 transient image refs
// into chora-atom-media. Shared by publish, save-revision, and accept.
type questionImageReHomer struct {
	svc          *mediarehome.Service
	questionRepo ports.QuestionRepository
}

// newQuestionImageReHomer returns nil when either dependency is absent so the
// caller can treat a nil re-homer as "re-home disabled" (each seam no-ops).
func newQuestionImageReHomer(svc *mediarehome.Service, questionRepo ports.QuestionRepository) *questionImageReHomer {
	if svc == nil || questionRepo == nil {
		return nil
	}
	return &questionImageReHomer{svc: svc, questionRepo: questionRepo}
}

// ReHomeRevision re-homes rev's stem + answer image refs into durable storage
// and, if anything changed, appends a NEW QuestionRevision carrying the durable
// refs (append-only — never mutates the prior revision). Returns the new
// revision + changed=true on a rewrite, or (nil,false,nil) when there are no
// images or they are already durable. Fail-loud: a copy / foreign-tenant error
// aborts and is returned. Used by publish + save-revision (PATCH), which act on
// an ALREADY-PERSISTED revision (so the rewrite must be a new append).
func (rh *questionImageReHomer) ReHomeRevision(ctx context.Context, tenantID, atomID string, q *question.Question, rev *question.QuestionRevision) (*question.QuestionRevision, bool, error) {
	var img, ansImg *string
	switch q.Type {
	case question.TypeMCQ:
		if rev.MCQPayload != nil {
			img, ansImg = rev.MCQPayload.ImageURL, rev.MCQPayload.AnswerImageURL
		}
	case question.TypeOpenEnded:
		if rev.OEPayload != nil {
			img, ansImg = rev.OEPayload.ImageURL, rev.OEPayload.AnswerImageURL
		}
	}
	if isBlank(img) && isBlank(ansImg) {
		return nil, false, nil // nothing to re-home
	}

	newImg, newAns, changed, err := rh.applyPair(ctx, tenantID, atomID, img, ansImg)
	if err != nil {
		return nil, false, err
	}
	if !changed {
		return nil, false, nil // already durable — no revision bump
	}

	// Build the rewritten payload (shallow copy + swap the image refs only)
	// and append it as a new revision via the standard ApplyUpdate path so
	// provenance + monotonic revision numbering are preserved.
	params := question.UpdateParams{
		AuthorGcid: q.AuthorGcid,
		SourceType: rev.SourceType,
	}
	switch q.Type {
	case question.TypeMCQ:
		cp := *rev.MCQPayload
		cp.ImageURL, cp.AnswerImageURL = newImg, newAns
		params.MCQ = &cp
	case question.TypeOpenEnded:
		cp := *rev.OEPayload
		cp.ImageURL, cp.AnswerImageURL = newImg, newAns
		params.OE = &cp
	}

	_, newRev, err := q.ApplyUpdate(params)
	if err != nil {
		return nil, false, fmt.Errorf("re-home apply-update: %w", err)
	}
	if err := rh.questionRepo.AppendRevision(ctx, newRev); err != nil {
		return nil, false, fmt.Errorf("re-home append-revision: %w", err)
	}
	return newRev, true, nil
}

// ReHomePayloadImages re-homes a to-be-persisted payload's stem + answer image
// refs into durable storage IN PLACE, before the first revision is written.
// Used by accept-candidate, question-create, save-revision and clone: the
// payload is not yet persisted, so durabilising the refs up-front persists a
// single durable revision (no extra revision) and a failure aborts BEFORE any
// persistence (clean retry — no orphaned draft atom that would force a
// duplicate on the author's retry). Fail-loud. mcq / oe may be nil (the
// non-matching discriminant); blank refs pass through.
func (rh *questionImageReHomer) ReHomePayloadImages(ctx context.Context, tenantID, atomID string, qType question.QuestionType, mcq *question.MCQPayload, oe *question.OEPayload) error {
	switch qType {
	case question.TypeMCQ:
		if mcq == nil {
			return nil
		}
		img, ans, _, err := rh.applyPair(ctx, tenantID, atomID, mcq.ImageURL, mcq.AnswerImageURL)
		if err != nil {
			return err
		}
		mcq.ImageURL, mcq.AnswerImageURL = img, ans
	case question.TypeOpenEnded:
		if oe == nil {
			return nil
		}
		img, ans, _, err := rh.applyPair(ctx, tenantID, atomID, oe.ImageURL, oe.AnswerImageURL)
		if err != nil {
			return err
		}
		oe.ImageURL, oe.AnswerImageURL = img, ans
	}
	return nil
}

// applyPair applies the media policy to one (stem, answer) reference pair and
// returns the refs to persist, plus whether either changed.
//
// It is deliberately TWO-PHASE: every reference is classified and
// ownership-checked BEFORE any copy is performed. Copying the stem and then
// rejecting the answer would leave the stem's durable copy unreferenced — an
// orphan the caller cannot clean up and must pay for again on retry.
func (rh *questionImageReHomer) applyPair(ctx context.Context, tenantID, atomID string, img, ans *string) (newImg, newAns *string, changed bool, err error) {
	refs := []*string{img, ans}

	// Phase 1 — classify + ownership-check EVERYTHING first.
	checked := make([]*mediarehome.CheckedReference, len(refs))
	for i, ref := range refs {
		if isBlank(ref) {
			continue
		}
		c, cerr := rh.svc.Check(ctx, tenantID, *ref)
		if cerr != nil {
			return nil, nil, false, cerr
		}
		checked[i] = &c
	}

	// Phase 2 — copy only once every reference is known good.
	out := []*string{img, ans}
	for i, c := range checked {
		if c == nil || c.Kind == ports.MediaReferenceDurable {
			continue
		}
		durable, _, cerr := rh.svc.Copy(ctx, tenantID, atomID, *c)
		if cerr != nil {
			return nil, nil, false, cerr
		}
		d := durable
		out[i] = &d
		changed = true
	}
	return out[0], out[1], changed, nil
}

// guardMediaPayload applies the tenant media policy to a payload that is about
// to be persisted, and re-homes its legitimate transient refs when a copier is
// wired.
//
// OWNERSHIP does not depend on the copier. The re-homer and the download signer
// are derived from the same storage adapter in production, but the handlers must
// not be fail-open if only one is wired:
//
//   - re-homer wired            → classify + ownership-check + re-home in place.
//   - no re-homer, signer wired → ownership-check only (the COPY is skipped —
//     there is no copier). A foreign-tenant ref is still REJECTED.
//   - neither wired             → EXTERNAL-IMAGE MODE (see the package doc):
//     no storage adapter is configured at all, so there is no grammar with
//     which to classify a reference and no signed URL can ever be minted. Refs
//     are persisted verbatim, WITHOUT any ownership classification, and the
//     tenant-isolation invariant is NOT enforced on this seam in this mode.
//     That is a documented deployment shape, not a silent fail-open; the
//     mode's positive guarantees (a durable ref can never be served, an
//     http(s) ref renders only while fetchable) are spelled out in the
//     package doc and pinned by TestCreateQuestion_ExternalImageMode_* below.
//
// Fail-loud: a foreign-tenant or unclassifiable reference is returned as an
// error and the caller must abort before persisting.
func guardMediaPayload(ctx context.Context, reHomer *questionImageReHomer, signer ports.AtomMediaDownloadSigner, tenantID, atomID string, qType question.QuestionType, mcq *question.MCQPayload, oe *question.OEPayload) error {
	if reHomer != nil {
		return reHomer.ReHomePayloadImages(ctx, tenantID, atomID, qType, mcq, oe)
	}
	if signer == nil {
		return nil
	}
	return checkPayloadOwnership(ctx, signer, tenantID, qType, mcq, oe)
}

// checkPayloadOwnership verifies tenant ownership of a payload's image refs
// WITHOUT a copier, via the signer's own grammar (TenantOwnsRef).
func checkPayloadOwnership(ctx context.Context, signer ports.AtomMediaDownloadSigner, tenantID string, qType question.QuestionType, mcq *question.MCQPayload, oe *question.OEPayload) error {
	var refs []*string
	switch qType {
	case question.TypeMCQ:
		if mcq != nil {
			refs = []*string{mcq.ImageURL, mcq.AnswerImageURL}
		}
	case question.TypeOpenEnded:
		if oe != nil {
			refs = []*string{oe.ImageURL, oe.AnswerImageURL}
		}
	}
	for _, ref := range refs {
		if isBlank(ref) {
			continue
		}
		owns, err := signer.TenantOwnsRef(ctx, tenantID, *ref)
		if err != nil {
			return err
		}
		if !owns {
			return fmt.Errorf(
				"refusing image ref outside tenant %q's namespace %q (storage reference not owned by the caller)",
				tenantID, ports.TenantMediaPrefix(tenantID))
		}
	}
	return nil
}

// isBlank reports whether an optional string ref is nil or whitespace-only.
func isBlank(s *string) bool { return s == nil || strings.TrimSpace(*s) == "" }
