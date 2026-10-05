// atom_media_handler.go — POST /api/atoms/{atom_id}/media handler.
//
// Mints a V4 signed URL the FE PUTs an atom-media image to. Implements
// `mintAtomMediaSignedUrl` in chora-contracts/openapi/creation-admin.yaml
// (contract added at commit `62392fe5`). Bucket + IAM provisioned by
// Infra at `4df66f51`; env `ATOM_MEDIA_BUCKET` configured on the
// chora-creation deployment.
//
// ATOM Phase 1 — ADR-156 (user locked 2026-05-17). Per the plan §2 file
// ownership table, this file is owned by B2; the corresponding router
// registration is the SINGLE line edit on handler.go.
//
// Wire contract (canonical at chora-contracts):
//
//	POST /api/atoms/{atom_id}/media
//	Request:  { mime: "image/jpeg|image/png|image/webp",
//	            size_bytes: int64, alt_text?: string ≤280 }
//	Response: { upload_url, expires_at, object_url, max_size_bytes }
//	Errors:   400 invalid body / 401 unauth / 403 not author
//	          / 404 atom not found / 413 size > tenant cap
//	          / 415 mime not in enum
//	          / 503 CREATION_ATOM_MEDIA_NOT_WIRED if signer nil
//
// Per `feedback_no_stubs_real_wiring`: when ATOM_MEDIA_BUCKET is empty at
// boot, cmd/server/main.go leaves AtomMediaSigner nil and this handler
// returns 503 with envelope `CREATION_ATOM_MEDIA_NOT_WIRED`. No stub
// fallback.
//
// Auth model (Phase 1): caller GCID MUST match atom.Gcid. Role-based
// elevation for instructor/admin is future work — see TODO below; the
// servicemesh `chora-role-summary` header carries the canonical role
// list once the BFF terminates the JWT with role claims.
package httpadapter

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/apollo-chora/chora-creation/internal/adapter/mediastore"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// mintAtomMediaSignedUrlRequest is the wire shape per OpenAPI
// MintAtomMediaSignedUrlRequest schema (chora-contracts/openapi/creation-admin.yaml).
//
// Per A20 convention in this file family — accept unknown fields so the
// FE can evolve the envelope (`DisallowUnknownFields` is inappropriate
// for a forward-compatible mint endpoint).
type mintAtomMediaSignedUrlRequest struct {
	MIME      string `json:"mime"`
	SizeBytes int64  `json:"size_bytes"`
	AltText   string `json:"alt_text,omitempty"`
}

// mintAtomMediaSignedUrlResponse mirrors OpenAPI AtomMediaSignedUrl.
// snake_case JSON keys per chora-contracts convention.
type mintAtomMediaSignedUrlResponse struct {
	UploadURL    string `json:"upload_url"`
	ExpiresAt    string `json:"expires_at"` // ISO-8601 UTC (Z-suffixed)
	ObjectURL    string `json:"object_url"`
	MaxSizeBytes int64  `json:"max_size_bytes"`
}

// mintAtomMediaSignedUrl is the POST /api/atoms/{atom_id}/media handler.
//
// Flow:
//  1. 503 if atomMediaSigner is nil (env unset at boot).
//  2. Decode body — 400 on malformed JSON / missing mime / bad size.
//  3. 415 on MIME outside the Phase 1 enum.
//  4. 413 on size_bytes > Phase 1 cap (2 MiB default).
//  5. Load the atom — 404 on miss / soft-delete.
//  6. 403 if caller GCID != atom.Gcid (Phase 1 strict author check).
//  7. Sign the URL — 500 on signer failure (e.g. live GCS hiccup).
//  8. 200 with the canonical envelope.
func (h *AtomHandler) mintAtomMediaSignedUrl(w http.ResponseWriter, r *http.Request, atomID string) {
	// (1) fail-loud when the env is unset at boot — no stub fallback.
	if h.atomMediaSigner == nil {
		writeError(w, http.StatusServiceUnavailable,
			"CREATION_ATOM_MEDIA_NOT_WIRED",
			"atom-media signer not wired (set ATOM_MEDIA_BUCKET env)")
		return
	}

	// (2) Decode + structural validation.
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", "empty body")
		return
	}
	var req mintAtomMediaSignedUrlRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", err.Error())
		return
	}
	if req.MIME == "" {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY", "mime is required")
		return
	}
	if req.SizeBytes <= 0 {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY",
			"size_bytes must be positive")
		return
	}
	if len(req.AltText) > 280 {
		writeError(w, http.StatusBadRequest, "CREATION_INVALID_BODY",
			"alt_text exceeds 280-char cap")
		return
	}

	// (3) MIME enum gate — 415 with canonical envelope.
	if _, err := mediastore.ExtensionForMIME(req.MIME); err != nil {
		writeError(w, http.StatusUnsupportedMediaType,
			"CREATION_ATOM_MEDIA_UNSUPPORTED_MIME",
			"mime must be one of image/jpeg, image/png, image/webp")
		return
	}

	// (4) Size cap — 413 BEFORE we touch the signer (saves a wasted call).
	// Phase 1 ships the default 2 MiB floor; per-tenant entitlement override
	// is future work (see ADR-156 Decision #5 + the TODO on
	// mediastore.defaultMaxSizeBytes).
	maxBytes := mediastore.DefaultMaxSizeBytes()
	if req.SizeBytes > maxBytes {
		writeError(w, http.StatusRequestEntityTooLarge,
			"CREATION_ATOM_MEDIA_TOO_LARGE",
			"size_bytes exceeds the per-tenant cap")
		return
	}

	// (5) Load the atom — 404 on miss / soft-delete.
	tenantID := tenantFromContext(r.Context())
	gcid := gcidFromContext(r.Context())
	a, err := h.repo.Get(r.Context(), tenantID, atomID)
	if err != nil {
		if errors.Is(err, atom.ErrNotFound) {
			writeError(w, http.StatusNotFound, "CREATION_ATOM_NOT_FOUND", "atom not found")
			return
		}
		log.Printf("mintAtomMediaSignedUrl: repo error: %v", err)
		writeError(w, http.StatusInternalServerError, "CREATION_REPO_ERROR", "failed to load atom")
		return
	}

	// (6) Authorize — Phase 1 strict author check.
	//
	// TODO(ATOM Phase 2): elevate based on caller's `chora-role-summary`
	// servicemesh header — instructors / admins should be permitted to
	// upload atom-media for atoms they did not author. Until then a
	// non-author 403s and the FE surfaces the canonical envelope.
	if refuseNonAuthor(w, a, gcid) {
		return
	}

	// (7) Mint the V4 signed URL.
	out, err := h.atomMediaSigner.Sign(r.Context(), atomID, tenantID, ports.SignAtomMediaInput{
		MIME:      req.MIME,
		SizeBytes: req.SizeBytes,
		AltText:   req.AltText,
	})
	if err != nil {
		// Surface as 503 when the signer self-reports "not wired" (e.g.
		// the adapter was constructed but the storage client failed at
		// boot). Otherwise 500.
		if errors.Is(err, ports.ErrAtomMediaSignerNotWired) {
			writeError(w, http.StatusServiceUnavailable,
				"CREATION_ATOM_MEDIA_NOT_WIRED",
				"atom-media signer reported not-wired at sign time")
			return
		}
		log.Printf("mintAtomMediaSignedUrl: signer error: %v", err)
		writeError(w, http.StatusInternalServerError,
			"CREATION_ATOM_MEDIA_SIGN_FAILED",
			"failed to mint atom-media signed url")
		return
	}

	// (8) 200 envelope per OpenAPI AtomMediaSignedUrl schema.
	writeJSON(w, http.StatusOK, mintAtomMediaSignedUrlResponse{
		UploadURL:    out.UploadURL,
		ExpiresAt:    out.ExpiresAt.UTC().Format(time.RFC3339Nano),
		ObjectURL:    out.ObjectURL,
		MaxSizeBytes: out.MaxSizeBytes,
	})
}

// refuseNonAuthor is the ONE atom-ownership gate for this service (CHO-2264).
//
// It writes the canonical 403 and reports true when the caller is not the atom's
// author, so every mutation door reads identically:
//
//	if refuseNonAuthor(w, a, gcid) { return }
//
// Ownership, not role, is the right primitive here: a role gate alone would
// still let author A destroy author B's work. It is also the ratified posture in
// this service — reuse_visibility_handler.go says it outright: "Any other caller
// (including tenant admins) receives 403 CREATION_NOT_AUTHOR."
//
// Call it the moment the atom is loaded and BEFORE any business rule or emit: a
// non-author must not learn whether an atom is orphan-frozen, and a refused
// mutation must leave no event behind.
//
// TODO(ATOM Phase 2): role elevation for instructor/admin (the deferred TODO
// above). When it lands it must land HERE — one helper, one decision — not
// per-handler, which is exactly how the doors drifted apart in the first place
// (the /v1/ media twin was open while this one was gated).
func refuseNonAuthor(w http.ResponseWriter, a *atom.LearningAtom, gcid string) bool {
	if a == nil || a.Gcid != gcid {
		writeError(w, http.StatusForbidden, "CREATION_ATOM_NOT_AUTHOR",
			"caller is not the atom author")
		return true
	}
	return false
}
