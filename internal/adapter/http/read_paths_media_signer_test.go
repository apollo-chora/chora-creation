// read_paths_media_signer_test.go — the REAL S3 adapter wired behind the two
// authed read paths.
//
// The sibling unit tests use fakeDownloadSigner, which answers IsDurableRef for
// s3:// — but the defect these paths had was that they hardcoded `gs://` while
// the deployed adapter persists s3://. Only the real adapter can prove the two
// agree, so this test wires mediastore.NewAtomMediaSigner into the router and
// asserts a stored s3:// durable ref reaches the wire as a presigned GET URL
// (never raw) on BOTH the author and the learner projection.
package httpadapter_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mediastore "github.com/apollo-chora/chora-creation/internal/adapter/mediastore"
)

func TestReadPaths_RealS3Signer_MintsDurableS3Ref(t *testing.T) {
	// Presigning is local (no network I/O), so a stub endpoint is enough.
	t.Setenv("S3_ENDPOINT", "http://localhost:9000")
	t.Setenv("S3_ACCESS_KEY_ID", "test")
	t.Setenv("S3_SECRET_ACCESS_KEY", "test")
	t.Setenv("ATOM_MEDIA_TRANSIENT_BUCKET", "")
	t.Setenv("ATOM_MEDIA_TRUSTED_PUBLIC_ORIGIN", "")

	signer, err := mediastore.NewAtomMediaSigner("chora-atom-media-dev")
	if err != nil {
		t.Fatalf("NewAtomMediaSigner: %v", err)
	}
	srv, atomID := seedMCQAtomWithSigner(t, signer)

	durable := "s3://chora-atom-media-dev/tenants/" + tenantA + "/atoms/x/stem.png"
	body := map[string]any{
		"type":   "mcq",
		"prompt": "Identify the condensation stage.",
		"mcq_payload": map[string]any{
			"options": []map[string]any{
				{"option_id": "a", "label": "Condensation", "is_correct": true, "explainer": "Yes"},
				{"option_id": "b", "label": "Evaporation", "is_correct": false, "explainer": "No"},
			},
		},
		"image_url": durable,
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedJSON(http.MethodPost, "/api/atoms/"+atomID+"/questions", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed q: %d %s", w.Code, w.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	qID := created["question"].(map[string]any)["question_id"].(string)

	assertPresigned := func(where, got string) {
		t.Helper()
		if got == durable {
			t.Fatalf("%s: raw durable ref emitted to the wire: %q", where, got)
		}
		if !strings.HasPrefix(got, "http://localhost:9000/chora-atom-media-dev/tenants/"+tenantA+"/atoms/x/stem.png?") {
			t.Errorf("%s: got %q; want a presigned GET URL over the durable object", where, got)
		}
		if !strings.Contains(got, "X-Amz-Signature=") {
			t.Errorf("%s: %q is not signed", where, got)
		}
	}

	// Author projection — GET /api/atoms/{id}/questions/{qid}.
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, authedJSON(http.MethodGet, "/api/atoms/"+atomID+"/questions/"+qID, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("author get: %d %s", w2.Code, w2.Body.String())
	}
	var fetched map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &fetched)
	mcq := fetched["question"].(map[string]any)["mcq"].(map[string]any)
	authorImg, _ := mcq["image_url"].(string)
	assertPresigned("author projection", authorImg)

	// Learner projection — GET /api/atoms/{id}.
	w3 := httptest.NewRecorder()
	srv.ServeHTTP(w3, authedReq(http.MethodGet, "/api/atoms/"+atomID, nil))
	if w3.Code != http.StatusOK {
		t.Fatalf("learner get: %d %s", w3.Code, w3.Body.String())
	}
	var learner map[string]any
	_ = json.Unmarshal(w3.Body.Bytes(), &learner)
	qp, ok := learner["question_payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected question_payload; got %s", w3.Body.String())
	}
	learnerImg, _ := qp["image_url"].(string)
	assertPresigned("learner projection", learnerImg)
}
