// Package http_expert_tools — Expert Tools HTTP routes (CHO-30).
//
// Eight endpoints under /v1/expert-tools/* implementing the instructor
// power-user tooling per docs/design/ux_expert_tooling.md:
//
//	POST  /v1/expert-tools/peer-review/queue                    — enqueue review
//	POST  /v1/expert-tools/peer-review/{review_id}/vote         — record vote
//	GET   /v1/expert-tools/peer-review/queue                    — list queue
//	POST  /v1/expert-tools/golden-set/anchors                   — create anchor
//	POST  /v1/expert-tools/golden-set/compare                   — compare to anchor
//	POST  /v1/expert-tools/quality-score/compute                — compute score
//	GET   /v1/expert-tools/quality-score/{atom_id}              — fetch latest
//	POST  /v1/expert-tools/version-diff/compute                 — diff revisions
//
// All mutating endpoints publish a Pub/Sub event with the chora.common.v1
// envelope mandatory fields populated. Topic prefixes:
//   - chora.creation.peer_review.{submitted|vote_recorded|quorum_reached}.v1
//   - chora.creation.golden_set.{anchor_created|compared}.v1
//   - chora.creation.quality_score.computed.v1
//
// Event publishing failures are logged but non-fatal: persistent state
// commits before publish; downstream eventual consistency is acceptable
// per the data-consistency skill.
//
// This package lives in its own directory (rather than alongside the
// existing httpadapter package) to keep its tests independent of in-flight
// handler work in that directory and to honour the CHO-30 constraint of
// "NEW files only — DON'T touch atom CRUD/AI Assist/knowledge graph/atom
// variants files".
package http_expert_tools

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	expertinmem "github.com/apollo-chora/chora-creation/internal/adapter/inmem/expert_tools"
	gs "github.com/apollo-chora/chora-creation/internal/domain/expert_tools/golden_set"
	pr "github.com/apollo-chora/chora-creation/internal/domain/expert_tools/peer_review"
	qs "github.com/apollo-chora/chora-creation/internal/domain/expert_tools/quality_score"
	vd "github.com/apollo-chora/chora-creation/internal/domain/expert_tools/version_diff"
	"github.com/google/uuid"
)

// Publisher is the (minimal) port used by mutating handlers to emit Pub/Sub
// events. Adapters: expertinmem.MemRecorder (tests), pubsub.Publisher
// (production, M12).
type Publisher interface {
	Publish(ctx context.Context, topic string, env expertinmem.MemEnvelope, payload any) error
}

// Deps wires the handler with its repositories + publisher.
type Deps struct {
	PeerReviewRepo   pr.Repository
	AnchorRepo       gs.Repository
	QualityScoreRepo qs.Repository
	Publisher        Publisher
}

// handler holds resolved dependencies + an injectable clock.
type handler struct {
	deps  Deps
	clock func() time.Time
}

// NewRouter wires the expert-tools mux. Returns an http.Handler ready for
// embedding behind the chora-creation top-level mux or for direct
// ListenAndServe in dev.
func NewRouter(deps Deps) http.Handler {
	h := &handler{deps: deps, clock: func() time.Time { return time.Now().UTC() }}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz)
	mux.HandleFunc("/healthz/", healthz)
	mux.HandleFunc("/health", healthz)

	// Peer review
	mux.HandleFunc("/v1/expert-tools/peer-review/queue", h.peerReviewQueueDispatch)
	mux.HandleFunc("/v1/expert-tools/peer-review/", h.peerReviewItemDispatch)

	// Golden set
	mux.HandleFunc("/v1/expert-tools/golden-set/anchors", h.goldenSetAnchorsDispatch)
	mux.HandleFunc("/v1/expert-tools/golden-set/compare", h.goldenSetCompareDispatch)

	// Quality score
	mux.HandleFunc("/v1/expert-tools/quality-score/compute", h.qualityScoreComputeDispatch)
	mux.HandleFunc("/v1/expert-tools/quality-score/", h.qualityScoreItemDispatch)

	// Version diff
	mux.HandleFunc("/v1/expert-tools/version-diff/compute", h.versionDiffComputeDispatch)

	return logging(tenantContext(mux))
}

// -----------------------------------------------------------------------------
// 1. POST /v1/expert-tools/peer-review/queue       (submit)
//    GET  /v1/expert-tools/peer-review/queue       (list)
// -----------------------------------------------------------------------------

func (h *handler) peerReviewQueueDispatch(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.submitPeerReview(w, r)
	case http.MethodGet:
		h.listPeerReviewQueue(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only POST and GET are supported on /v1/expert-tools/peer-review/queue")
	}
}

type peerReviewSubmitRequest struct {
	AtomID     string `json:"atom_id"`
	RevisionID string `json:"revision_id"`
	QuorumSize int    `json:"quorum_size"`
}

func (h *handler) submitPeerReview(w http.ResponseWriter, r *http.Request) {
	var req peerReviewSubmitRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
		return
	}
	tenantID := tenantFromContext(r.Context())
	gcid := gcidFromContext(r.Context())
	sub, err := pr.Submit(pr.SubmitParams{
		TenantID: tenantID, AtomID: req.AtomID, RevisionID: req.RevisionID,
		AuthoredBy: gcid, QuorumSize: req.QuorumSize,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_REVIEW", err.Error())
		return
	}
	if err := h.deps.PeerReviewRepo.Save(r.Context(), sub); err != nil {
		log.Printf("peer_review save: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to persist review")
		return
	}
	if err := h.publishPeerReviewSubmitted(r.Context(), sub); err != nil {
		log.Printf("peer_review publish error: %v", err) // non-fatal
	}
	writeJSON(w, http.StatusCreated, sub)
}

func (h *handler) listPeerReviewQueue(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantFromContext(r.Context())
	q := r.URL.Query()
	statusStr := strings.TrimSpace(q.Get("status"))
	var statusFilter pr.Status
	if statusStr != "" {
		s := pr.Status(statusStr)
		switch s {
		case pr.StatusPending, pr.StatusApproved, pr.StatusRejected, pr.StatusCancelled:
			statusFilter = s
		default:
			writeError(w, http.StatusBadRequest, "CREATION_INVALID_STATUS",
				"status must be one of pending|approved|rejected|cancelled")
			return
		}
	}
	items, err := h.deps.PeerReviewRepo.List(r.Context(), tenantID, pr.ListFilter{Status: statusFilter})
	if err != nil {
		log.Printf("peer_review list: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to list reviews")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

// -----------------------------------------------------------------------------
// 2. POST /v1/expert-tools/peer-review/{review_id}/vote
// -----------------------------------------------------------------------------

func (h *handler) peerReviewItemDispatch(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/expert-tools/peer-review/")
	rest = strings.TrimSuffix(rest, "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "vote" {
		writeError(w, http.StatusNotFound, "CREATION_NOT_FOUND", "unknown sub-resource")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only POST is supported on /v1/expert-tools/peer-review/{id}/vote")
		return
	}
	h.recordPeerReviewVote(w, r, parts[0])
}

type peerReviewVoteRequest struct {
	Decision string `json:"decision"`
	Comment  string `json:"comment"`
}

func (h *handler) recordPeerReviewVote(w http.ResponseWriter, r *http.Request, reviewID string) {
	var req peerReviewVoteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
		return
	}
	tenantID := tenantFromContext(r.Context())
	gcid := gcidFromContext(r.Context())

	sub, err := h.deps.PeerReviewRepo.Get(r.Context(), tenantID, reviewID)
	if err != nil {
		if errors.Is(err, pr.ErrNotFound) {
			writeError(w, http.StatusNotFound, "CREATION_REVIEW_NOT_FOUND", "review not found")
			return
		}
		log.Printf("peer_review get: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "internal error")
		return
	}
	if err := sub.RecordVote(pr.VoteParams{
		ReviewerGcid: gcid, Decision: pr.Decision(req.Decision), Comment: req.Comment,
	}); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_VOTE", err.Error())
		return
	}
	if err := h.deps.PeerReviewRepo.Save(r.Context(), sub); err != nil {
		log.Printf("peer_review save: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to save")
		return
	}
	if err := h.publishPeerReviewVoteRecorded(r.Context(), sub); err != nil {
		log.Printf("peer_review vote publish: %v", err)
	}
	if sub.Status == pr.StatusApproved || sub.Status == pr.StatusRejected {
		if err := h.publishPeerReviewQuorumReached(r.Context(), sub); err != nil {
			log.Printf("peer_review quorum publish: %v", err)
		}
	}
	writeJSON(w, http.StatusAccepted, sub)
}

// -----------------------------------------------------------------------------
// 3. POST /v1/expert-tools/golden-set/anchors  (create anchor)
// -----------------------------------------------------------------------------

func (h *handler) goldenSetAnchorsDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only POST is supported on /v1/expert-tools/golden-set/anchors")
		return
	}
	h.createGoldenAnchor(w, r)
}

type goldenAnchorRequest struct {
	AtomID       string   `json:"atom_id"`
	Title        string   `json:"title"`
	Body         string   `json:"body"`
	Tags         []string `json:"tags"`
	QualityScore float64  `json:"quality_score"`
}

func (h *handler) createGoldenAnchor(w http.ResponseWriter, r *http.Request) {
	var req goldenAnchorRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
		return
	}
	tenantID := tenantFromContext(r.Context())
	gcid := gcidFromContext(r.Context())

	a, err := gs.NewAnchor(gs.NewAnchorParams{
		TenantID: tenantID, CreatedBy: gcid,
		AtomID: req.AtomID, Title: req.Title, Body: req.Body,
		Tags: req.Tags, QualityScore: req.QualityScore,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_ANCHOR", err.Error())
		return
	}
	if err := h.deps.AnchorRepo.Save(r.Context(), a); err != nil {
		log.Printf("anchor save: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to persist anchor")
		return
	}
	if err := h.publishAnchorCreated(r.Context(), a); err != nil {
		log.Printf("anchor publish: %v", err)
	}
	writeJSON(w, http.StatusCreated, a)
}

// -----------------------------------------------------------------------------
// 4. POST /v1/expert-tools/golden-set/compare
// -----------------------------------------------------------------------------

func (h *handler) goldenSetCompareDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only POST is supported on /v1/expert-tools/golden-set/compare")
		return
	}
	h.compareToGolden(w, r)
}

type goldenCompareRequest struct {
	AnchorID           string   `json:"anchor_id"`
	ComparedAtomID     string   `json:"compared_atom_id"`
	ComparedRevisionID string   `json:"compared_revision_id"`
	ComparedBody       string   `json:"compared_body"`
	ComparedTags       []string `json:"compared_tags"`
}

func (h *handler) compareToGolden(w http.ResponseWriter, r *http.Request) {
	var req goldenCompareRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
		return
	}
	tenantID := tenantFromContext(r.Context())

	anchor, err := h.deps.AnchorRepo.Get(r.Context(), tenantID, req.AnchorID)
	if err != nil {
		if errors.Is(err, gs.ErrNotFound) {
			writeError(w, http.StatusNotFound, "CREATION_ANCHOR_NOT_FOUND", "anchor not found")
			return
		}
		log.Printf("anchor get: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "internal error")
		return
	}
	cmp, err := gs.Compare(gs.CompareParams{
		TenantID:           tenantID,
		ComparedAtomID:     req.ComparedAtomID,
		ComparedRevisionID: req.ComparedRevisionID,
		ComparedBody:       req.ComparedBody,
		ComparedTags:       req.ComparedTags,
		Anchor:             anchor,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_COMPARE", err.Error())
		return
	}
	if err := h.publishCompared(r.Context(), tenantID, cmp); err != nil {
		log.Printf("compare publish: %v", err)
	}
	writeJSON(w, http.StatusOK, cmp)
}

// -----------------------------------------------------------------------------
// 5. POST /v1/expert-tools/quality-score/compute
// -----------------------------------------------------------------------------

func (h *handler) qualityScoreComputeDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only POST is supported on /v1/expert-tools/quality-score/compute")
		return
	}
	h.computeQualityScore(w, r)
}

type qualityScoreRequest struct {
	AtomID               string  `json:"atom_id"`
	RevisionID           string  `json:"revision_id"`
	Clarity              float64 `json:"clarity"`
	PedagogicalSoundness float64 `json:"pedagogical_soundness"`
	Fairness             float64 `json:"fairness"`
	Accessibility        float64 `json:"accessibility"`
}

func (h *handler) computeQualityScore(w http.ResponseWriter, r *http.Request) {
	var req qualityScoreRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
		return
	}
	tenantID := tenantFromContext(r.Context())
	gcid := gcidFromContext(r.Context())

	score, err := qs.New(qs.NewParams{
		TenantID: tenantID, AtomID: req.AtomID, RevisionID: req.RevisionID,
		ComputedBy: gcid,
		Clarity:    req.Clarity, PedagogicalSoundness: req.PedagogicalSoundness,
		Fairness: req.Fairness, Accessibility: req.Accessibility,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_SCORE", err.Error())
		return
	}
	if err := h.deps.QualityScoreRepo.Append(r.Context(), score); err != nil {
		log.Printf("quality_score append: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to persist score")
		return
	}
	if err := h.publishQualityScoreComputed(r.Context(), score); err != nil {
		log.Printf("quality_score publish: %v", err)
	}
	writeJSON(w, http.StatusCreated, score)
}

// -----------------------------------------------------------------------------
// 6. GET /v1/expert-tools/quality-score/{atom_id}   (latest)
// -----------------------------------------------------------------------------

func (h *handler) qualityScoreItemDispatch(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/expert-tools/quality-score/")
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" || strings.Contains(rest, "/") {
		writeError(w, http.StatusNotFound, "CREATION_NOT_FOUND", "unknown sub-resource")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only GET is supported on /v1/expert-tools/quality-score/{atom_id}")
		return
	}
	tenantID := tenantFromContext(r.Context())
	score, err := h.deps.QualityScoreRepo.Latest(r.Context(), tenantID, rest)
	if err != nil {
		if errors.Is(err, qs.ErrNotFound) {
			writeError(w, http.StatusNotFound, "CREATION_SCORE_NOT_FOUND", "no score found for atom")
			return
		}
		log.Printf("quality_score latest: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, score)
}

// -----------------------------------------------------------------------------
// 7. POST /v1/expert-tools/version-diff/compute
// -----------------------------------------------------------------------------

func (h *handler) versionDiffComputeDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only POST is supported on /v1/expert-tools/version-diff/compute")
		return
	}
	h.computeVersionDiff(w, r)
}

type versionDiffRequest struct {
	AtomID         string `json:"atom_id"`
	BaseRevisionID string `json:"base_revision_id"`
	BaseBody       string `json:"base_body"`
	HeadRevisionID string `json:"head_revision_id"`
	HeadBody       string `json:"head_body"`
}

func (h *handler) computeVersionDiff(w http.ResponseWriter, r *http.Request) {
	var req versionDiffRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
		return
	}
	tenantID := tenantFromContext(r.Context())
	d, err := vd.Compute(vd.ComputeParams{
		TenantID: tenantID, AtomID: req.AtomID,
		BaseRevisionID: req.BaseRevisionID, BaseBody: req.BaseBody,
		HeadRevisionID: req.HeadRevisionID, HeadBody: req.HeadBody,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_DIFF", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// -----------------------------------------------------------------------------
// Event publishing helpers
// -----------------------------------------------------------------------------

func (h *handler) publishPeerReviewSubmitted(ctx context.Context, sub *pr.ReviewSubmission) error {
	if h.deps.Publisher == nil {
		return nil
	}
	env := h.envelopeFor(sub.TenantID, sub.AuthoredBy, sub.CreatedAt, sub.ReviewID)
	return h.deps.Publisher.Publish(ctx, string(pr.EventTypeSubmitted), env, pr.NewSubmittedPayload(sub))
}

func (h *handler) publishPeerReviewVoteRecorded(ctx context.Context, sub *pr.ReviewSubmission) error {
	if h.deps.Publisher == nil || len(sub.Votes) == 0 {
		return nil
	}
	v := sub.Votes[len(sub.Votes)-1]
	env := h.envelopeFor(sub.TenantID, v.ReviewerGcid, v.CreatedAt, v.VoteID)
	return h.deps.Publisher.Publish(ctx, string(pr.EventTypeVoteRecorded), env, pr.NewVoteRecordedPayload(sub, v))
}

func (h *handler) publishPeerReviewQuorumReached(ctx context.Context, sub *pr.ReviewSubmission) error {
	if h.deps.Publisher == nil {
		return nil
	}
	occ := sub.UpdatedAt
	if sub.ResolvedAt != nil {
		occ = *sub.ResolvedAt
	}
	env := h.envelopeFor(sub.TenantID, sub.AuthoredBy, occ, sub.ReviewID+":quorum")
	return h.deps.Publisher.Publish(ctx, string(pr.EventTypeQuorumReached), env, pr.NewQuorumReachedPayload(sub))
}

func (h *handler) publishAnchorCreated(ctx context.Context, a *gs.Anchor) error {
	if h.deps.Publisher == nil {
		return nil
	}
	env := h.envelopeFor(a.TenantID, a.CreatedBy, a.CreatedAt, a.AnchorID)
	return h.deps.Publisher.Publish(ctx, string(gs.EventTypeAnchorCreated), env, gs.NewAnchorCreatedPayload(a))
}

func (h *handler) publishCompared(ctx context.Context, tenantID string, c gs.Comparison) error {
	if h.deps.Publisher == nil {
		return nil
	}
	env := h.envelopeFor(tenantID, "", c.OccurredAt, c.AnchorID+":"+c.ComparedAtomID)
	return h.deps.Publisher.Publish(ctx, string(gs.EventTypeCompared), env, gs.NewComparedPayload(tenantID, c))
}

func (h *handler) publishQualityScoreComputed(ctx context.Context, q *qs.QualityScore) error {
	if h.deps.Publisher == nil {
		return nil
	}
	env := h.envelopeFor(q.TenantID, q.ComputedBy, q.ComputedAt, q.ScoreID)
	return h.deps.Publisher.Publish(ctx, string(qs.EventTypeComputed), env, qs.NewComputedPayload(q))
}

// envelopeFor builds a chora.common.v1.EventEnvelope mandatory-fields stand-in.
// idempotencyAnchor is the dedup key for at-least-once Pub/Sub semantics
// (per pub-sub-topology skill); we use the aggregate id when there is one.
func (h *handler) envelopeFor(tenantID, gcid string, occurredAt time.Time, idempotencyAnchor string) expertinmem.MemEnvelope {
	now := h.clock()
	id, _ := uuid.NewV7()
	eventID := id.String()
	idem := idempotencyAnchor
	if idem == "" {
		idem = eventID
	}
	return expertinmem.MemEnvelope{
		EventID:        eventID,
		IdempotencyKey: idem,
		TenantID:       tenantID,
		Gcid:           gcid,
		OccurredAt:     occurredAt,
		PublishedAt:    now,
		Traceparent:    "",
		SourceProject:  "chora-content",
		SourceService:  "chora-creation",
		SchemaVersion:  1,
	}
}

// -----------------------------------------------------------------------------
// Local helpers — kept self-contained to avoid coupling to the in-flight
// httpadapter package.
// -----------------------------------------------------------------------------

type ctxKey string

const (
	ctxKeyTenantID ctxKey = "tenant_id"
	ctxKeyGcid     ctxKey = "gcid"
)

// tenantContext extracts X-Tenant-Id + gcid headers (or X-Chora-GCID
// alias) and stores them on the request context. Public paths (/healthz)
// are bypassed.
func tenantContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
		gcid := strings.TrimSpace(r.Header.Get("gcid"))
		if gcid == "" {
			gcid = strings.TrimSpace(r.Header.Get("X-Chora-GCID"))
		}
		if tenantID == "" {
			writeError(w, http.StatusBadRequest, "CREATION_TENANT_REQUIRED",
				"X-Tenant-Id header is required")
			return
		}
		if gcid == "" {
			writeError(w, http.StatusBadRequest, "CREATION_GCID_REQUIRED",
				"gcid header is required")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyTenantID, tenantID)
		ctx = context.WithValue(ctx, ctxKeyGcid, gcid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// logging logs each request once it has dispatched.
func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gcid := r.Header.Get("gcid")
		if gcid == "" {
			gcid = r.Header.Get("X-Chora-GCID")
		}
		if gcid != "" {
			log.Printf("expert_tools method=%s path=%s gcid=%s", r.Method, r.URL.Path, gcid)
		} else {
			log.Printf("expert_tools method=%s path=%s", r.Method, r.URL.Path)
		}
		next.ServeHTTP(w, r)
	})
}

func isPublicPath(p string) bool {
	switch p {
	case "/", "/healthz", "/healthz/", "/health":
		return true
	}
	return false
}

func tenantFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyTenantID).(string)
	return v
}

func gcidFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyGcid).(string)
	return v
}

// healthz returns 200 OK with a JSON payload.
func healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// decodeJSON parses r.Body into v with strict unknown-field rejection.
func decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return errors.New("empty body")
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

// writeJSON writes status + JSON-encoded body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// errEnvelope mirrors components.schemas.Error.
type errEnvelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError writes an error response.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errEnvelope{Code: code, Message: msg})
}
