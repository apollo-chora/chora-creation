// rehome_test.go — TDD for the W8 image re-home service (OT#4). The
// service decides, given a stored image reference, whether to copy the
// bytes into durable storage and what durable ref to persist. Both the
// storage-URL grammar (ClassifyReference) and the copy itself are port
// operations (ports.MediaReHomer) faked here.
//
// The fake mirrors the production adapter: durable refs are s3:// (the S3
// adapter's scheme), the transient source is a Garage/S3-style path-style
// presigned http URL, and classification failures are errors — so a
// regression in the decision layer cannot hide behind a fake that answers
// gs:// for everything.
package mediarehome_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/mediarehome"
	"github.com/apollo-chora/chora-creation/internal/ports"
)

const (
	durableBucket   = "chora-atom-media-dev"
	transientBucket = "chora-ai-assist-images-dev"
)

type fakeCopier struct {
	got    ports.CopyToDurableParams
	called int
	retURI string
	retErr error

	// classifyErr, when set, is returned by ClassifyReference — used to
	// prove the service fails loud BEFORE attempting any copy.
	classifyErr error

	// classifyRet, when set, is returned verbatim by ClassifyReference — lets
	// a test hand the service a specific classified ref (e.g. one whose key
	// belongs to another tenant) without contorting the URL grammar.
	classifyRet *ports.MediaReference
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

// ClassifyReference mirrors the production adapter's grammar: s3:// and
// gs:// on the durable bucket are durable; a path-style http(s) URL on the
// transient bucket is transient; everything else is an error.
func (f *fakeCopier) ClassifyReference(_ context.Context, raw string) (ports.MediaReference, error) {
	if f.classifyErr != nil {
		return ports.MediaReference{}, f.classifyErr
	}
	if f.classifyRet != nil {
		return *f.classifyRet, nil
	}
	ref := strings.TrimSpace(raw)
	for _, scheme := range []string{"s3://", "gs://"} {
		if strings.HasPrefix(ref, scheme) {
			rest := ref[len(scheme):]
			i := strings.IndexByte(rest, '/')
			if i <= 0 || i >= len(rest)-1 {
				return ports.MediaReference{}, fmt.Errorf("malformed %s URI: %q", scheme, ref)
			}
			bucket, key := rest[:i], rest[i+1:]
			if bucket != durableBucket {
				return ports.MediaReference{}, fmt.Errorf("foreign %s bucket %q", scheme, bucket)
			}
			return ports.MediaReference{Kind: ports.MediaReferenceDurable, Bucket: bucket, Key: key}, nil
		}
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		u, err := url.Parse(ref)
		if err != nil {
			return ports.MediaReference{}, fmt.Errorf("parse %q: %w", ref, err)
		}
		path := strings.TrimPrefix(u.Path, "/") // query (the signature) excluded
		i := strings.IndexByte(path, '/')
		if i <= 0 || i >= len(path)-1 {
			return ports.MediaReference{}, fmt.Errorf("malformed path-style URL: %q", ref)
		}
		bucket, key := path[:i], path[i+1:]
		if bucket != transientBucket {
			return ports.MediaReference{}, fmt.Errorf("foreign http bucket %q", bucket)
		}
		return ports.MediaReference{Kind: ports.MediaReferenceTransient, Bucket: bucket, Key: key}, nil
	}
	return ports.MediaReference{}, fmt.Errorf("unrecognized reference %q", ref)
}

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

// Already-durable s3:// ref (what the S3 adapter persists) → idempotent
// passthrough, copier NOT called. This is the case that 500'd in
// production: the old service only recognised gs://.
func TestReHome_AlreadyDurableS3_Idempotent(t *testing.T) {
	t.Parallel()
	f := &fakeCopier{}
	svc := mediarehome.New(f)
	ref := "s3://" + durableBucket + "/tenants/t1/atoms/a1/obj.png"
	got, changed, err := svc.ReHome(context.Background(), "t1", "a1", ref)
	if err != nil {
		t.Fatalf("ReHome(durable s3://) err = %v", err)
	}
	if changed {
		t.Error("ReHome(durable s3://) changed=true; want false (idempotent)")
	}
	if got != ref {
		t.Errorf("ReHome(durable s3://) = %q; want unchanged %q", got, ref)
	}
	if f.called != 0 {
		t.Errorf("copier called %d times for already-durable ref; want 0", f.called)
	}
}

// Already-durable gs:// ref → idempotent passthrough, copier NOT called
// (kept so a GCS deployment still works).
func TestReHome_AlreadyDurableGS_Idempotent(t *testing.T) {
	t.Parallel()
	f := &fakeCopier{}
	svc := mediarehome.New(f)
	ref := "gs://" + durableBucket + "/tenants/t1/atoms/a1/obj.png"
	got, changed, err := svc.ReHome(context.Background(), "t1", "a1", ref)
	if err != nil {
		t.Fatalf("ReHome(durable gs://) err = %v", err)
	}
	if changed {
		t.Error("ReHome(durable gs://) changed=true; want false (idempotent)")
	}
	if got != ref {
		t.Errorf("ReHome(durable gs://) = %q; want unchanged %q", got, ref)
	}
	if f.called != 0 {
		t.Errorf("copier called %d times for already-durable ref; want 0", f.called)
	}
}

// Transient Garage/S3 path-style signed URL → copied; durable s3:// returned.
func TestReHome_TransientPathStyle_Copies(t *testing.T) {
	t.Parallel()
	want := "s3://" + durableBucket + "/tenants/t1/atoms/a1/new.png"
	f := &fakeCopier{retURI: want}
	svc := mediarehome.New(f)

	src := "http://146.235.20.25:9000/" + transientBucket +
		"/tenants/t1/jobs/j9/abc123.png?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Expires=604800"
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
	if f.got.SrcBucket != transientBucket {
		t.Errorf("SrcBucket = %q; want %s", f.got.SrcBucket, transientBucket)
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

// The service copies the coordinates + extension the adapter classified,
// whatever bucket/key shape it reports.
func TestReHome_TransientCoordinates_ForwardedVerbatim(t *testing.T) {
	t.Parallel()
	f := &fakeCopier{retURI: "s3://" + durableBucket + "/tenants/t/atoms/a/x.svg"}
	svc := mediarehome.New(f)

	src := "https://storage.googleapis.com/" + transientBucket + "/tenants/t/jobs/j/d.svg?X-Goog-Expires=604800"
	_, changed, err := svc.ReHome(context.Background(), "t", "a", src)
	if err != nil || !changed {
		t.Fatalf("ReHome(transient) = (changed=%v, err=%v); want (true,nil)", changed, err)
	}
	if f.got.SrcBucket != transientBucket {
		t.Errorf("SrcBucket = %q; want %s", f.got.SrcBucket, transientBucket)
	}
	if f.got.SrcKey != "tenants/t/jobs/j/d.svg" {
		t.Errorf("SrcKey = %q; want tenants/t/jobs/j/d.svg", f.got.SrcKey)
	}
	if f.got.Ext != "svg" {
		t.Errorf("Ext = %q; want svg", f.got.Ext)
	}
}

// Foreign durable bucket → fail loud (we never re-home an unexpected bucket).
func TestReHome_ForeignDurableBucket_Errors(t *testing.T) {
	t.Parallel()
	f := &fakeCopier{}
	svc := mediarehome.New(f)
	for _, bad := range []string{
		"gs://some-other-bucket/k.png",
		"s3://some-other-bucket/k.png",
	} {
		_, _, err := svc.ReHome(context.Background(), "t", "a", bad)
		if err == nil {
			t.Errorf("ReHome(foreign %q) should error; got nil", bad)
		}
	}
	if f.called != 0 {
		t.Errorf("copier called for foreign bucket; want 0")
	}
}

// Unclassifiable refs → fail loud.
func TestReHome_UnclassifiableRef_Errors(t *testing.T) {
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

// A classification failure aborts BEFORE any copy — the fail-loud-before-
// persist guarantee (an unclassifiable ref must never be persisted as-is,
// nor blindly copied).
func TestReHome_ClassifyError_NoCopyAttempted(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("foreign bucket")
	f := &fakeCopier{classifyErr: sentinel}
	svc := mediarehome.New(f)
	got, changed, err := svc.ReHome(context.Background(), "t", "a",
		"http://146.235.20.25:9000/some-other-bucket/tenants/t/jobs/j/x.png?X-Amz-Signature=deadbeef")
	if err == nil {
		t.Fatal("ReHome should propagate the classification error; got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error %v should wrap the classifier sentinel", err)
	}
	if got != "" || changed {
		t.Errorf("ReHome(classify error) = (%q,%v); want (\"\",false)", got, changed)
	}
	if f.called != 0 {
		t.Errorf("copier called %d times after a classification error; want 0", f.called)
	}
}

// Copier failure is propagated (fail-loud, not swallowed).
func TestReHome_CopierError_Propagates(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("s3 boom")
	f := &fakeCopier{retErr: sentinel}
	svc := mediarehome.New(f)
	_, _, err := svc.ReHome(context.Background(), "t", "a",
		"https://storage.googleapis.com/"+transientBucket+"/tenants/t/jobs/j/x.png?sig=1")
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
	f := &fakeCopier{retURI: "s3://" + durableBucket + "/tenants/t/atoms/a/x.png"}
	svc := mediarehome.New(f)
	_, _, err := svc.ReHome(context.Background(), "t", "a",
		"https://storage.googleapis.com/"+transientBucket+"/tenants/t/jobs/j/noext?sig=1")
	if err != nil {
		t.Fatalf("ReHome err = %v", err)
	}
	if f.got.Ext != "png" {
		t.Errorf("Ext = %q; want png (default)", f.got.Ext)
	}
}

// OWNERSHIP — a transient ref whose key belongs to ANOTHER tenant is refused
// before the copy. Defense in depth behind the adapter's own guard: the accept
// path prefers a client-supplied image_url, and the copy runs with the
// adapter's credentials, so a trusted origin + bucket does not prove ownership.
func TestReHome_ForeignTenantSource_NoCopy(t *testing.T) {
	t.Parallel()
	f := &fakeCopier{classifyRet: &ports.MediaReference{
		Kind:   ports.MediaReferenceTransient,
		Bucket: transientBucket,
		Key:    "tenants/someone-else/jobs/j9/secret.png",
	}}
	svc := mediarehome.New(f)

	got, changed, err := svc.ReHome(context.Background(), "t1", "a1",
		"https://storage.googleapis.com/"+transientBucket+"/tenants/someone-else/jobs/j9/secret.png?sig=1")
	if err == nil {
		t.Fatal("ReHome(foreign-tenant source) should error; got nil")
	}
	if got != "" || changed {
		t.Errorf("ReHome(foreign-tenant source) = (%q,%v); want (\"\",false)", got, changed)
	}
	if f.called != 0 {
		t.Errorf("copier called %d times for a foreign-tenant source; want 0 (no copy attempted)", f.called)
	}
	if !strings.Contains(err.Error(), "t1") {
		t.Errorf("error %q should name the caller's tenant", err.Error())
	}
	if !strings.Contains(err.Error(), "someone-else") {
		t.Errorf("error %q should name the offending key", err.Error())
	}
	if !strings.Contains(err.Error(), "mediarehome") {
		t.Errorf("error %q should stay namespaced", err.Error())
	}
}

// The prefix boundary is the tenant segment: tenant "t1" must not match the
// "tenants/t11/" namespace.
func TestReHome_TenantPrefixBoundary_NoCopy(t *testing.T) {
	t.Parallel()
	f := &fakeCopier{classifyRet: &ports.MediaReference{
		Kind:   ports.MediaReferenceTransient,
		Bucket: transientBucket,
		Key:    "tenants/t11/jobs/j9/x.png",
	}}
	svc := mediarehome.New(f)
	if _, _, err := svc.ReHome(context.Background(), "t1", "a1", "https://x/y"); err == nil {
		t.Error("tenant t1 must not match the t11 namespace; want an error")
	}
	if f.called != 0 {
		t.Errorf("copier called %d times; want 0", f.called)
	}
}

// BLOCKER REGRESSION — the ownership guard must cover DURABLE references too.
// A foreign-tenant ref in the configured durable bucket classifies as Durable
// (well-formed, right bucket), so an early return before the check would
// persist it verbatim; the read signer scopes on bucket only and would then
// sign and serve another tenant's object. It must be refused exactly like a
// foreign transient source.
func TestReHome_ForeignTenantDurableRef_Rejected(t *testing.T) {
	t.Parallel()
	for _, kind := range []ports.MediaReferenceKind{
		ports.MediaReferenceDurable,
		ports.MediaReferenceTransient,
	} {
		f := &fakeCopier{classifyRet: &ports.MediaReference{
			Kind:   kind,
			Bucket: durableBucket,
			Key:    "tenants/someone-else/atoms/a1/secret.png",
		}}
		svc := mediarehome.New(f)

		got, changed, err := svc.ReHome(context.Background(), "t1", "a1",
			"s3://"+durableBucket+"/tenants/someone-else/atoms/a1/secret.png")
		if err == nil {
			t.Fatalf("kind=%v: ReHome(foreign-tenant ref) should error; got (%q,%v,nil)", kind, got, changed)
		}
		if got != "" || changed {
			t.Errorf("kind=%v: ReHome(foreign-tenant ref) = (%q,%v); want (\"\",false)", kind, got, changed)
		}
		if f.called != 0 {
			t.Errorf("kind=%v: copier called %d times; want 0", kind, f.called)
		}
		if !strings.Contains(err.Error(), "t1") || !strings.Contains(err.Error(), "someone-else") {
			t.Errorf("kind=%v: error %q should name the tenant and the offending key", kind, err.Error())
		}
	}
}

// A same-tenant DURABLE ref is still returned unchanged with changed=false and
// no copy — the ownership check must not break idempotency.
func TestReHome_SameTenantDurableRef_Idempotent(t *testing.T) {
	t.Parallel()
	for _, scheme := range []string{"s3://", "gs://"} {
		f := &fakeCopier{}
		svc := mediarehome.New(f)
		ref := scheme + durableBucket + "/tenants/t1/atoms/a1/obj.png"

		got, changed, err := svc.ReHome(context.Background(), "t1", "a1", ref)
		if err != nil {
			t.Fatalf("ReHome(%q) err = %v; want nil", ref, err)
		}
		if changed {
			t.Errorf("ReHome(%q) changed=true; want false (idempotent)", ref)
		}
		if got != ref {
			t.Errorf("ReHome(%q) = %q; want unchanged", ref, got)
		}
		if f.called != 0 {
			t.Errorf("ReHome(%q) called the copier %d times; want 0", ref, f.called)
		}
	}
}

// Only the TENANT is namespaced, not the job: a same-tenant key under any job
// (or none) is a legitimate re-home source, so the guard must not
// over-restrict.
func TestReHome_SameTenantOtherJob_Copies(t *testing.T) {
	t.Parallel()
	for _, key := range []string{
		"tenants/t1/jobs/a-completely-different-job/x.png",
		"tenants/t1/jobs/j9/nested/deeper/y.png",
		"tenants/t1/anything-else/z.png",
	} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			want := "s3://" + durableBucket + "/tenants/t1/atoms/a1/new.png"
			f := &fakeCopier{
				classifyRet: &ports.MediaReference{
					Kind:   ports.MediaReferenceTransient,
					Bucket: transientBucket,
					Key:    key,
				},
				retURI: want,
			}
			svc := mediarehome.New(f)
			got, changed, err := svc.ReHome(context.Background(), "t1", "a1", "https://x/y")
			if err != nil {
				t.Fatalf("ReHome(same-tenant key %q) err = %v; want nil", key, err)
			}
			if !changed || got != want {
				t.Errorf("ReHome(%q) = (%q,%v); want (%q,true)", key, got, changed, want)
			}
			if f.called != 1 {
				t.Errorf("copier called %d times; want 1", f.called)
			}
			if f.got.SrcKey != key {
				t.Errorf("SrcKey = %q; want %q", f.got.SrcKey, key)
			}
		})
	}
}

func TestReHome_ErrorsAreNamespaced(t *testing.T) {
	t.Parallel()
	svc := mediarehome.New(&fakeCopier{})
	for _, bad := range []string{
		"gs://other/k.png",
		"https://evil.example.com/x.png",
	} {
		_, _, err := svc.ReHome(context.Background(), "t", "a", bad)
		if err == nil || !strings.Contains(err.Error(), "mediarehome") {
			t.Errorf("error %v should be namespaced with 'mediarehome'", err)
		}
	}
}
