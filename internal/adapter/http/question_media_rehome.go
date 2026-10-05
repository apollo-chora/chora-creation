// question_media_rehome.go — ADR-210: shared durable image re-home for question
// revisions. Lifts the W8/OT#4 re-home (formerly private on AtomHandler,
// publish-only) into a reusable service wired at all three SAVE seams —
// publish, save-revision (PATCH), and accept-candidate — so a
// saved-but-unpublished question image is copied into chora-atom-media and
// survives past the 7-day transient-bucket TTL (fully fixes CHO-1974). It
// leans entirely on the idempotent, fail-loud mediarehome.Service (which
// already turns a transient signed URL into a durable gs:// ref). Nil-safe
// construction: a nil re-home service or question repo yields a nil re-homer,
// and each seam then no-ops (images stay transient).
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
// gs:// refs (append-only — never mutates the prior revision). Returns the new
// revision + changed=true on a rewrite, or (nil,false,nil) when there are no
// images or they are already durable. Fail-loud: a copy / foreign-bucket error
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

	newImg, ch1, err := rh.reHomeOne(ctx, tenantID, atomID, img)
	if err != nil {
		return nil, false, err
	}
	newAns, ch2, err := rh.reHomeOne(ctx, tenantID, atomID, ansImg)
	if err != nil {
		return nil, false, err
	}
	if !ch1 && !ch2 {
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
// Used by accept-candidate: the candidate is being created, so durabilising the
// refs up-front persists a single durable revision (no extra revision) and a
// copy failure aborts BEFORE any persistence (clean retry — no orphaned draft
// atom that would force a duplicate on the author's retry). Fail-loud. mcq / oe
// may be nil (the non-matching discriminant); blank refs pass through.
func (rh *questionImageReHomer) ReHomePayloadImages(ctx context.Context, tenantID, atomID string, qType question.QuestionType, mcq *question.MCQPayload, oe *question.OEPayload) error {
	switch qType {
	case question.TypeMCQ:
		if mcq == nil {
			return nil
		}
		img, _, err := rh.reHomeOne(ctx, tenantID, atomID, mcq.ImageURL)
		if err != nil {
			return err
		}
		ans, _, err := rh.reHomeOne(ctx, tenantID, atomID, mcq.AnswerImageURL)
		if err != nil {
			return err
		}
		mcq.ImageURL, mcq.AnswerImageURL = img, ans
	case question.TypeOpenEnded:
		if oe == nil {
			return nil
		}
		img, _, err := rh.reHomeOne(ctx, tenantID, atomID, oe.ImageURL)
		if err != nil {
			return err
		}
		ans, _, err := rh.reHomeOne(ctx, tenantID, atomID, oe.AnswerImageURL)
		if err != nil {
			return err
		}
		oe.ImageURL, oe.AnswerImageURL = img, ans
	}
	return nil
}

// reHomeOne re-homes a single optional image ref. Returns the (possibly new)
// durable ref pointer + whether it changed. Blank/nil refs pass through.
func (rh *questionImageReHomer) reHomeOne(ctx context.Context, tenantID, atomID string, ref *string) (*string, bool, error) {
	if isBlank(ref) {
		return ref, false, nil
	}
	durable, changed, err := rh.svc.ReHome(ctx, tenantID, atomID, *ref)
	if err != nil {
		return nil, false, err
	}
	if !changed {
		return ref, false, nil
	}
	return &durable, true, nil
}

// isBlank reports whether an optional string ref is nil or whitespace-only.
func isBlank(s *string) bool { return s == nil || strings.TrimSpace(*s) == "" }
