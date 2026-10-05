// atom_media_handler_test.go — HTTP-layer tests for
// POST /api/atoms/{atom_id}/media (ATOM Phase 1 — ADR-156).
//
// Contract per chora-contracts/openapi/creation-admin.yaml
// `mintAtomMediaSignedUrl` operation (added at commit `62392fe5`).
// Bucket + IAM provisioned by Infra at `4df66f51`; env
// `ATOM_MEDIA_BUCKET` already set on the chora-creation deployment.
//
// Coverage matrix per the operation's error responses:
//   - 200 happy path returns upload_url + expires_at + object_url + max_size_bytes
//   - 400 missing mime / size_bytes
//   - 400 size_bytes <= 0
//   - 401 missing auth headers (covered by tenantContext middleware tests)
//   - 403 caller GCID != atom author GCID
//   - 404 atom not found
//   - 413 size_bytes > tenant cap (Phase 1 floor: 2 MiB)
//   - 415 mime not in enum
//   - 503 CREATION_ATOM_MEDIA_NOT_WIRED when ATOM_MEDIA_BUCKET unset
//
// A fake AtomMediaSigner is used to assert the wire envelope; real GCS
// V4 signing is exercised by the cluster smoke test (G3 in plan §7).
package httpadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

// -----------------------------------------------------------------------------
// fakeAtomMediaSigner — records the last Sign() call so tests can assert
// the handler forwards request fields correctly.
// -----------------------------------------------------------------------------

type fakeAtomMediaSigner struct {
	out      ports.SignAtomMediaOutput
	err      error
	lastIn   ports.SignAtomMediaInput
	lastAtom string
	lastTen  string
	calls    int
}

func (f *fakeAtomMediaSigner) Sign(_ context.Context, atomID, tenantID string, in ports.SignAtomMediaInput) (ports.SignAtomMediaOutput, error) {
	f.calls++
	f.lastIn = in
	f.lastAtom = atomID
	f.lastTen = tenantID
	if f.err != nil {
		return ports.SignAtomMediaOutput{}, f.err
	}
	return f.out, nil
}

func defaultFakeSigner() *fakeAtomMediaSigner {
	return &fakeAtomMediaSigner{
		out: ports.SignAtomMediaOutput{
			UploadURL:    "https://storage.googleapis.com/chora-atom-media-dev/tenants/t/atoms/a/o.png?X-Goog-Signature=abc",
			ExpiresAt:    time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC),
			ObjectURL:    "gs://chora-atom-media-dev/tenants/t/atoms/a/o.png",
			MaxSizeBytes: 2 * 1024 * 1024,
		},
	}
}

// newServerWithMediaSigner wires the router with both atomRepo + signer
// + seeds a single atom owned by gcidA. Returns (router, atomID, signer).
func newServerWithMediaSigner(t *testing.T, signer ports.AtomMediaSigner) (http.Handler, string) {
	t.Helper()
	atomRepo := inmem.NewAtomRepository()
	a, err := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "media test atom", Body: "body", Mode: atom.ModeStraightUp,
	})
	if err != nil {
		t.Fatalf("seed atom: %v", err)
	}
	if err := atomRepo.Save(context.Background(), a); err != nil {
		t.Fatalf("save seed atom: %v", err)
	}
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:            atomRepo,
		AtomMediaSigner: signer,
	})
	return srv, a.AtomID
}

// -----------------------------------------------------------------------------
// 200 happy path — full envelope is echoed
// -----------------------------------------------------------------------------

func TestMintAtomMediaSignedUrl_200_HappyPath(t *testing.T) {
	t.Parallel()

	signer := defaultFakeSigner()
	srv, atomID := newServerWithMediaSigner(t, signer)

	body := map[string]any{
		"mime":       "image/png",
		"size_bytes": 12345,
		"alt_text":   "a meaningful description",
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/media", body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("json: %v body=%s", err, w.Body.String())
	}
	for _, key := range []string{"upload_url", "expires_at", "object_url", "max_size_bytes"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing %q in response; body=%s", key, w.Body.String())
		}
	}
	if got["upload_url"] != signer.out.UploadURL {
		t.Errorf("upload_url = %v; want %s", got["upload_url"], signer.out.UploadURL)
	}
	if got["object_url"] != signer.out.ObjectURL {
		t.Errorf("object_url = %v; want %s", got["object_url"], signer.out.ObjectURL)
	}
	if msb, _ := got["max_size_bytes"].(float64); int64(msb) != signer.out.MaxSizeBytes {
		t.Errorf("max_size_bytes = %v; want %d", got["max_size_bytes"], signer.out.MaxSizeBytes)
	}
	// expires_at must be ISO-8601 UTC.
	if exp, _ := got["expires_at"].(string); !strings.HasSuffix(exp, "Z") {
		t.Errorf("expires_at = %q; want ISO-8601 UTC (Z-suffixed)", exp)
	}

	// Handler forwards request fields to the signer.
	if signer.calls != 1 {
		t.Fatalf("signer.Sign call count = %d; want 1", signer.calls)
	}
	if signer.lastIn.MIME != "image/png" {
		t.Errorf("signer MIME = %q; want image/png", signer.lastIn.MIME)
	}
	if signer.lastIn.SizeBytes != 12345 {
		t.Errorf("signer SizeBytes = %d; want 12345", signer.lastIn.SizeBytes)
	}
	if signer.lastIn.AltText != "a meaningful description" {
		t.Errorf("signer AltText = %q; want %q", signer.lastIn.AltText, "a meaningful description")
	}
	if signer.lastAtom != atomID {
		t.Errorf("signer atomID = %q; want %q", signer.lastAtom, atomID)
	}
	if signer.lastTen != tenantA {
		t.Errorf("signer tenantID = %q; want %q", signer.lastTen, tenantA)
	}
}

// -----------------------------------------------------------------------------
// 400 — invalid body shapes
// -----------------------------------------------------------------------------

func TestMintAtomMediaSignedUrl_400_MissingMime(t *testing.T) {
	t.Parallel()

	signer := defaultFakeSigner()
	srv, atomID := newServerWithMediaSigner(t, signer)

	body := map[string]any{
		"size_bytes": 1024,
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/media", body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400; body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["code"] != "CREATION_INVALID_BODY" {
		t.Errorf("code = %v; want CREATION_INVALID_BODY; body=%s", got["code"], w.Body.String())
	}
	if signer.calls != 0 {
		t.Errorf("signer.Sign should not be called on invalid body; calls=%d", signer.calls)
	}
}

func TestMintAtomMediaSignedUrl_400_ZeroOrNegativeSize(t *testing.T) {
	t.Parallel()

	signer := defaultFakeSigner()
	srv, atomID := newServerWithMediaSigner(t, signer)

	for _, sz := range []int64{0, -1} {
		body := map[string]any{
			"mime":       "image/png",
			"size_bytes": sz,
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/media", body))
		if w.Code != http.StatusBadRequest {
			t.Errorf("size=%d: status = %d; want 400; body=%s", sz, w.Code, w.Body.String())
		}
	}
}

func TestMintAtomMediaSignedUrl_400_MalformedJSON(t *testing.T) {
	t.Parallel()

	signer := defaultFakeSigner()
	srv, atomID := newServerWithMediaSigner(t, signer)

	r := httptest.NewRequest(http.MethodPost, "/api/atoms/"+atomID+"/media", strings.NewReader("{not json"))
	r.Header.Set("X-Tenant-Id", tenantA)
	r.Header.Set("gcid", gcidA)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400; body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 415 — mime not in enum
// -----------------------------------------------------------------------------

func TestMintAtomMediaSignedUrl_415_UnsupportedMime(t *testing.T) {
	t.Parallel()

	signer := defaultFakeSigner()
	srv, atomID := newServerWithMediaSigner(t, signer)

	for _, mime := range []string{"image/gif", "application/pdf", "video/mp4", "image/JPEG"} {
		body := map[string]any{
			"mime":       mime,
			"size_bytes": 1024,
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/media", body))
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("mime=%q: status = %d; want 415; body=%s", mime, w.Code, w.Body.String())
		}
		var got map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		if got["code"] != "CREATION_ATOM_MEDIA_UNSUPPORTED_MIME" {
			t.Errorf("mime=%q: code = %v; want CREATION_ATOM_MEDIA_UNSUPPORTED_MIME", mime, got["code"])
		}
	}
	if signer.calls != 0 {
		t.Errorf("signer.Sign should not be called on 415; calls=%d", signer.calls)
	}
}

// -----------------------------------------------------------------------------
// 413 — size_bytes > tenant cap
// -----------------------------------------------------------------------------

func TestMintAtomMediaSignedUrl_413_SizeExceedsCap(t *testing.T) {
	t.Parallel()

	signer := defaultFakeSigner()
	srv, atomID := newServerWithMediaSigner(t, signer)

	// Phase 1 cap is 2 MiB. 5 MiB is comfortably over.
	body := map[string]any{
		"mime":       "image/png",
		"size_bytes": 5 * 1024 * 1024,
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/media", body))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d; want 413; body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["code"] != "CREATION_ATOM_MEDIA_TOO_LARGE" {
		t.Errorf("code = %v; want CREATION_ATOM_MEDIA_TOO_LARGE; body=%s", got["code"], w.Body.String())
	}
	if signer.calls != 0 {
		t.Errorf("signer.Sign should not be called on 413; calls=%d", signer.calls)
	}
}

// -----------------------------------------------------------------------------
// 404 — atom not found
// -----------------------------------------------------------------------------

func TestMintAtomMediaSignedUrl_404_AtomNotFound(t *testing.T) {
	t.Parallel()

	signer := defaultFakeSigner()
	srv, _ := newServerWithMediaSigner(t, signer)

	body := map[string]any{
		"mime":       "image/png",
		"size_bytes": 1024,
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w,
		authedJSON(http.MethodPost,
			"/api/atoms/01970000-0000-7000-aaaa-bbbbbbbbbbbb/media", body))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404; body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["code"] != "CREATION_ATOM_NOT_FOUND" {
		t.Errorf("code = %v; want CREATION_ATOM_NOT_FOUND", got["code"])
	}
	if signer.calls != 0 {
		t.Errorf("signer.Sign should not be called on 404; calls=%d", signer.calls)
	}
}

// -----------------------------------------------------------------------------
// 403 — caller GCID != atom.Gcid (Phase 1 enforces strict author check;
// role-elevation for instructor/admin is future work via the servicemesh
// RoleSummary header).
// -----------------------------------------------------------------------------

func TestMintAtomMediaSignedUrl_403_CallerNotAuthor(t *testing.T) {
	t.Parallel()

	signer := defaultFakeSigner()
	atomRepo := inmem.NewAtomRepository()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	_ = atomRepo.Save(context.Background(), a)
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:            atomRepo,
		AtomMediaSigner: signer,
	})

	body := map[string]any{
		"mime":       "image/png",
		"size_bytes": 1024,
	}
	// Caller is a different gcid (gcidB).
	const gcidB = "01970000-0000-7000-9000-000000000002"
	r := httptest.NewRequest(http.MethodPost, "/api/atoms/"+a.AtomID+"/media", strings.NewReader(jsonOf(body)))
	r.Header.Set("X-Tenant-Id", tenantA)
	r.Header.Set("gcid", gcidB)
	r.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want 403; body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["code"] != "CREATION_ATOM_NOT_AUTHOR" {
		t.Errorf("code = %v; want CREATION_ATOM_NOT_AUTHOR; body=%s", got["code"], w.Body.String())
	}
	if signer.calls != 0 {
		t.Errorf("signer.Sign should not be called on 403; calls=%d", signer.calls)
	}
}

// -----------------------------------------------------------------------------
// 401 / tenant headers — covered by tenantContext middleware; verify the
// route inherits the same gate.
// -----------------------------------------------------------------------------

func TestMintAtomMediaSignedUrl_MissingTenantHeader(t *testing.T) {
	t.Parallel()

	signer := defaultFakeSigner()
	srv, atomID := newServerWithMediaSigner(t, signer)

	body := map[string]any{
		"mime":       "image/png",
		"size_bytes": 1024,
	}
	r := httptest.NewRequest(http.MethodPost, "/api/atoms/"+atomID+"/media", strings.NewReader(jsonOf(body)))
	r.Header.Set("gcid", gcidA)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (missing tenant header — gated by middleware)", w.Code)
	}
}

// -----------------------------------------------------------------------------
// 503 — signer not wired (env ATOM_MEDIA_BUCKET unset)
// -----------------------------------------------------------------------------

func TestMintAtomMediaSignedUrl_503_NotWired(t *testing.T) {
	t.Parallel()

	atomRepo := inmem.NewAtomRepository()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "x", Body: "y", Mode: atom.ModeStraightUp,
	})
	_ = atomRepo.Save(context.Background(), a)
	// Signer NOT wired — main.go path when ATOM_MEDIA_BUCKET env is empty.
	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo: atomRepo,
	})

	body := map[string]any{
		"mime":       "image/png",
		"size_bytes": 1024,
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+a.AtomID+"/media", body))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503; body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["code"] != "CREATION_ATOM_MEDIA_NOT_WIRED" {
		t.Errorf("code = %v; want CREATION_ATOM_MEDIA_NOT_WIRED; body=%s", got["code"], w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 500 — signer returns error (e.g. live GCS sign failure)
// -----------------------------------------------------------------------------

func TestMintAtomMediaSignedUrl_500_OnSignerError(t *testing.T) {
	t.Parallel()

	signer := defaultFakeSigner()
	signer.err = errors.New("simulated GCS signer failure")
	srv, atomID := newServerWithMediaSigner(t, signer)

	body := map[string]any{
		"mime":       "image/png",
		"size_bytes": 1024,
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/media", body))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d; want 500; body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["code"] != "CREATION_ATOM_MEDIA_SIGN_FAILED" {
		t.Errorf("code = %v; want CREATION_ATOM_MEDIA_SIGN_FAILED", got["code"])
	}
}

// -----------------------------------------------------------------------------
// Method gate — only POST is allowed.
// -----------------------------------------------------------------------------

func TestMintAtomMediaSignedUrl_RejectsNonPost(t *testing.T) {
	t.Parallel()

	signer := defaultFakeSigner()
	srv, atomID := newServerWithMediaSigner(t, signer)

	for _, m := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete, http.MethodPut} {
		t.Run(m, func(t *testing.T) {
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, authedReq(m, "/api/atoms/"+atomID+"/media", nil))
			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("method %s: status %d; want 405", m, w.Code)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// helper — JSON marshal in a single expression for the inline request bodies
// above that build *http.Request via httptest.NewRequest directly.
// -----------------------------------------------------------------------------

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
