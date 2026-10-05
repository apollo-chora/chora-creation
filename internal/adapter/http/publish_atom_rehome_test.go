// publish_atom_rehome_test.go — OT#4 TDD: publishing an atom whose question
// carries transient W8 image URLs re-homes them into durable storage and
// rewrites the latest revision's payload to a gs:// ref. Uses the REAL
// mediarehome.Service over a fake MediaReHomer (server-side GCS copy).
package httpadapter_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpadapter "github.com/apollo-chora/chora-creation/internal/adapter/http"
	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
	"github.com/apollo-chora/chora-creation/internal/domain/question"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

const reHomeDurableBucket = "chora-atom-media-dev"

// fakeReHomer is a ports.MediaReHomer that records copies + returns a
// deterministic durable gs:// URI per call.
type fakeReHomer struct {
	n    int
	srcs []string
	err  error
}

func (f *fakeReHomer) DurableBucket() string { return reHomeDurableBucket }

func (f *fakeReHomer) CopyToDurable(_ context.Context, p ports.CopyToDurableParams) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.n++
	f.srcs = append(f.srcs, p.SrcBucket+"/"+p.SrcKey)
	return fmt.Sprintf("gs://%s/tenants/%s/atoms/%s/rehomed-%d.%s",
		reHomeDurableBucket, p.TenantID, p.AtomID, f.n, p.Ext), nil
}

func seedMCQWithImages(t *testing.T, qRepo ports.QuestionRepository, atomID, img, ans string) {
	t.Helper()
	mcq := &question.MCQPayload{
		Options: []question.MCQOption{
			{OptionID: "opt_1", Label: "A", IsCorrect: true, Explainer: "correct"},
			{OptionID: "opt_2", Label: "B", IsCorrect: false, Explainer: "nope"},
		},
	}
	if img != "" {
		mcq.ImageURL = &img
	}
	if ans != "" {
		mcq.AnswerImageURL = &ans
	}
	q, err := question.New(question.NewParams{
		TenantID: tenantA, AtomID: atomID, AuthorGcid: gcidA,
		Type: question.TypeMCQ, Prompt: "Order the planets", SourceType: atom.SourceAIAssist, MCQ: mcq,
	})
	if err != nil {
		t.Fatalf("seed question.New: %v", err)
	}
	rev, err := question.NewRevision(q, "Order the planets", mcq, nil, gcidA, atom.SourceAIAssist)
	if err != nil {
		t.Fatalf("seed NewRevision: %v", err)
	}
	if err := qRepo.Save(context.Background(), q, rev); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
}

func publishAtomWithRehome(t *testing.T, reHomer ports.MediaReHomer, img, ans string) (*httptest.ResponseRecorder, ports.QuestionRepository, string) {
	t.Helper()
	atomRepo := inmem.NewAtomRepository()
	qRepo := newFakeQRepo()
	a, _ := atom.New(atom.NewParams{
		TenantID: tenantA, Gcid: gcidA, Title: "planets mcq", Body: "body", Mode: atom.ModeStraightUp,
	})
	a.QuestionType = atom.AtomType("mcq")
	_ = atomRepo.Save(context.Background(), a)
	seedMCQWithImages(t, qRepo, a.AtomID, img, ans)

	srv := httpadapter.NewRouterWithDeps(httpadapter.RouterDeps{
		Repo:               atomRepo,
		QuestionRepository: qRepo,
		MediaReHomer:       reHomer,
	})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms/"+a.AtomID+"/publish", nil))
	return w, qRepo, a.AtomID
}

func latestMCQ(t *testing.T, qRepo ports.QuestionRepository, atomID string) *question.MCQPayload {
	t.Helper()
	_, rev, err := qRepo.GetByAtomID(context.Background(), tenantA, atomID)
	if err != nil {
		t.Fatalf("GetByAtomID: %v", err)
	}
	if rev.MCQPayload == nil {
		t.Fatal("latest revision has nil MCQPayload")
	}
	return rev.MCQPayload
}

// Publishing re-homes BOTH transient image refs to durable gs:// refs.
func TestPublishAtom_ReHomesTransientImages(t *testing.T) {
	t.Parallel()
	img := "https://storage.googleapis.com/chora-ai-assist-images-dev/tenants/" + tenantA + "/jobs/j1/stem.png?X-Goog-Expires=604800"
	ans := "https://storage.googleapis.com/chora-ai-assist-images-dev/tenants/" + tenantA + "/jobs/j1/answer.png?X-Goog-Expires=604800"
	reHomer := &fakeReHomer{}

	w, qRepo, atomID := publishAtomWithRehome(t, reHomer, img, ans)
	if w.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", w.Code, w.Body.String())
	}
	if reHomer.n != 2 {
		t.Errorf("copier called %d times; want 2 (stem + answer)", reHomer.n)
	}
	mcq := latestMCQ(t, qRepo, atomID)
	if mcq.ImageURL == nil || !strings.HasPrefix(*mcq.ImageURL, "gs://"+reHomeDurableBucket+"/") {
		t.Errorf("ImageURL = %v; want durable gs:// ref", deref(mcq.ImageURL))
	}
	if mcq.AnswerImageURL == nil || !strings.HasPrefix(*mcq.AnswerImageURL, "gs://"+reHomeDurableBucket+"/") {
		t.Errorf("AnswerImageURL = %v; want durable gs:// ref", deref(mcq.AnswerImageURL))
	}
}

// Already-durable refs → no copy, no new revision, publish still succeeds.
func TestPublishAtom_AlreadyDurable_NoReHome(t *testing.T) {
	t.Parallel()
	durable := "gs://" + reHomeDurableBucket + "/tenants/" + tenantA + "/atoms/x/img.png"
	reHomer := &fakeReHomer{}

	w, qRepo, atomID := publishAtomWithRehome(t, reHomer, durable, "")
	if w.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", w.Code, w.Body.String())
	}
	if reHomer.n != 0 {
		t.Errorf("copier called %d times for already-durable ref; want 0", reHomer.n)
	}
	mcq := latestMCQ(t, qRepo, atomID)
	if mcq.ImageURL == nil || *mcq.ImageURL != durable {
		t.Errorf("ImageURL = %v; want unchanged %q", deref(mcq.ImageURL), durable)
	}
}

// A copy failure aborts publish (fail-loud) with the discriminated 500.
func TestPublishAtom_ReHomeFailure_500(t *testing.T) {
	t.Parallel()
	img := "https://storage.googleapis.com/chora-ai-assist-images-dev/tenants/" + tenantA + "/jobs/j/x.png?sig=1"
	reHomer := &fakeReHomer{err: errors.New("gcs copy denied")}

	w, _, _ := publishAtomWithRehome(t, reHomer, img, "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d; want 500; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "CREATION_ATOM_MEDIA_REHOME_FAILED") {
		t.Errorf("body %s should carry CREATION_ATOM_MEDIA_REHOME_FAILED", w.Body.String())
	}
}

// No images → publish unaffected, copier never touched.
func TestPublishAtom_NoImages_NoReHome(t *testing.T) {
	t.Parallel()
	reHomer := &fakeReHomer{}
	w, _, _ := publishAtomWithRehome(t, reHomer, "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", w.Code, w.Body.String())
	}
	if reHomer.n != 0 {
		t.Errorf("copier called %d times with no images; want 0", reHomer.n)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
