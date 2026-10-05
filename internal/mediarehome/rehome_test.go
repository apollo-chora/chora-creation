// rehome_test.go — TDD for the W8 image re-home service (OT#4). The
// service decides, given a stored image URL, whether to copy the bytes
// into durable storage and what durable gs:// ref to persist. The actual
// GCS copy is a port (ports.MediaReHomer) faked here.
package mediarehome_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/mediarehome"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

const durableBucket = "chora-atom-media-dev"

type fakeCopier struct {
	got    ports.CopyToDurableParams
	called int
	retURI string
	retErr error
}

func (f *fakeCopier) CopyToDurable(_ context.Context, p ports.CopyToDurableParams) (string, error) {
	f.called++
	f.got = p
	if f.retErr != nil {
		return "", f.retErr
	}
	return f.retURI, nil
}
func (f *fakeCopier) DurableBucket() string { return durableBucket }

// Empty URL → no-op (no image to re-home).
func TestReHome_EmptyURL_NoOp(t *testing.T) {
	t.Parallel()
	f := &fakeCopier{}
	svc := mediarehome.New(f)
	got, changed, err := svc.ReHome(context.Background(), "t1", "a1", "")
	if err != nil || changed || got != "" {
		t.Fatalf("ReHome(empty) = (%q,%v,%v); want (\"\",false,nil)", got, changed, err)
	}
	if f.called != 0 {
		t.Errorf("copier called %d times for empty URL; want 0", f.called)
	}
}

// Already-durable gs:// ref → idempotent passthrough, copier NOT called.
func TestReHome_AlreadyDurable_Idempotent(t *testing.T) {
	t.Parallel()
	f := &fakeCopier{}
	svc := mediarehome.New(f)
	ref := "gs://" + durableBucket + "/tenants/t1/atoms/a1/obj.png"
	got, changed, err := svc.ReHome(context.Background(), "t1", "a1", ref)
	if err != nil {
		t.Fatalf("ReHome(durable) err = %v", err)
	}
	if changed {
		t.Error("ReHome(durable) changed=true; want false (idempotent)")
	}
	if got != ref {
		t.Errorf("ReHome(durable) = %q; want unchanged %q", got, ref)
	}
	if f.called != 0 {
		t.Errorf("copier called %d times for already-durable ref; want 0", f.called)
	}
}

// Transient path-style signed URL → copied; durable gs:// returned.
func TestReHome_TransientPathStyle_Copies(t *testing.T) {
	t.Parallel()
	want := "gs://" + durableBucket + "/tenants/t1/atoms/a1/new.png"
	f := &fakeCopier{retURI: want}
	svc := mediarehome.New(f)

	src := "https://storage.googleapis.com/chora-ai-assist-images-dev/tenants/t1/jobs/j9/abc123.png?X-Goog-Algorithm=GOOG4-RSA-SHA256&X-Goog-Expires=604800"
	got, changed, err := svc.ReHome(context.Background(), "t1", "a1", src)
	if err != nil {
		t.Fatalf("ReHome(transient) err = %v", err)
	}
	if !changed {
		t.Error("ReHome(transient) changed=false; want true")
	}
	if got != want {
		t.Errorf("ReHome(transient) = %q; want %q", got, want)
	}
	if f.called != 1 {
		t.Fatalf("copier called %d times; want 1", f.called)
	}
	if f.got.SrcBucket != "chora-ai-assist-images-dev" {
		t.Errorf("SrcBucket = %q; want chora-ai-assist-images-dev", f.got.SrcBucket)
	}
	if f.got.SrcKey != "tenants/t1/jobs/j9/abc123.png" {
		t.Errorf("SrcKey = %q; want tenants/t1/jobs/j9/abc123.png (query stripped)", f.got.SrcKey)
	}
	if f.got.Ext != "png" {
		t.Errorf("Ext = %q; want png", f.got.Ext)
	}
	if f.got.TenantID != "t1" || f.got.AtomID != "a1" {
		t.Errorf("tenant/atom = %q/%q; want t1/a1", f.got.TenantID, f.got.AtomID)
	}
}

// Transient virtual-hosted signed URL → parsed correctly.
func TestReHome_TransientVirtualHosted_Copies(t *testing.T) {
	t.Parallel()
	f := &fakeCopier{retURI: "gs://" + durableBucket + "/tenants/t/atoms/a/x.svg"}
	svc := mediarehome.New(f)

	src := "https://chora-ai-assist-images-dev.storage.googleapis.com/tenants/t/jobs/j/d.svg?X-Goog-Expires=604800"
	_, changed, err := svc.ReHome(context.Background(), "t", "a", src)
	if err != nil || !changed {
		t.Fatalf("ReHome(virtual-hosted) = (changed=%v, err=%v); want (true,nil)", changed, err)
	}
	if f.got.SrcBucket != "chora-ai-assist-images-dev" {
		t.Errorf("SrcBucket = %q; want chora-ai-assist-images-dev", f.got.SrcBucket)
	}
	if f.got.SrcKey != "tenants/t/jobs/j/d.svg" {
		t.Errorf("SrcKey = %q; want tenants/t/jobs/j/d.svg", f.got.SrcKey)
	}
	if f.got.Ext != "svg" {
		t.Errorf("Ext = %q; want svg", f.got.Ext)
	}
}

// Foreign gs:// bucket → fail loud (we never re-home an unexpected bucket).
func TestReHome_ForeignGSBucket_Errors(t *testing.T) {
	t.Parallel()
	f := &fakeCopier{}
	svc := mediarehome.New(f)
	_, _, err := svc.ReHome(context.Background(), "t", "a", "gs://some-other-bucket/k.png")
	if err == nil {
		t.Fatal("ReHome(foreign gs://) should error; got nil")
	}
	if f.called != 0 {
		t.Errorf("copier called for foreign gs://; want 0")
	}
}

// Non-GCS URL → fail loud.
func TestReHome_NonGCSURL_Errors(t *testing.T) {
	t.Parallel()
	svc := mediarehome.New(&fakeCopier{})
	for _, bad := range []string{
		"https://evil.example.com/x.png",
		"ftp://storage.googleapis.com/b/k.png",
		"not a url at all ::::",
	} {
		if _, _, err := svc.ReHome(context.Background(), "t", "a", bad); err == nil {
			t.Errorf("ReHome(%q) should error; got nil", bad)
		}
	}
}

// Copier failure is propagated (fail-loud, not swallowed).
func TestReHome_CopierError_Propagates(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("gcs boom")
	f := &fakeCopier{retErr: sentinel}
	svc := mediarehome.New(f)
	_, _, err := svc.ReHome(context.Background(), "t", "a",
		"https://storage.googleapis.com/chora-ai-assist-images-dev/tenants/t/jobs/j/x.png?sig=1")
	if err == nil {
		t.Fatal("ReHome should propagate copier error; got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error %v should wrap copier sentinel", err)
	}
}

// Key with no extension → defaults to png (the orchestrator's default).
func TestReHome_NoExtension_DefaultsPNG(t *testing.T) {
	t.Parallel()
	f := &fakeCopier{retURI: "gs://" + durableBucket + "/tenants/t/atoms/a/x.png"}
	svc := mediarehome.New(f)
	_, _, err := svc.ReHome(context.Background(), "t", "a",
		"https://storage.googleapis.com/chora-ai-assist-images-dev/tenants/t/jobs/j/noext?sig=1")
	if err != nil {
		t.Fatalf("ReHome err = %v", err)
	}
	if f.got.Ext != "png" {
		t.Errorf("Ext = %q; want png (default)", f.got.Ext)
	}
}

func TestReHome_ErrorsAreNamespaced(t *testing.T) {
	t.Parallel()
	svc := mediarehome.New(&fakeCopier{})
	_, _, err := svc.ReHome(context.Background(), "t", "a", "gs://other/k.png")
	if err == nil || !strings.Contains(err.Error(), "mediarehome") {
		t.Errorf("error %v should be namespaced with 'mediarehome'", err)
	}
}
