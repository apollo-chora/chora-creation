// media_durable.go — durable atom-media additions to the S3 adapter
// (W8 image re-home, OT#4):
//
//   - ClassifyReference: validates a stored media reference against the
//     configured durable + transient buckets and reports whether it names
//     a transient object (copy it) or an already-durable one (leave it) —
//     satisfies the reference-kind half of ports.MediaReHomer.
//   - CopyToDurable: server-side copy of a transient image object into the
//     durable bucket (satisfies ports.MediaReHomer).
//   - SignDownloadURL: mints a fresh short-lived presigned GET URL over a
//     durable s3:// object (satisfies ports.AtomMediaDownloadSigner).
//
// All reuse the AtomMediaSigner's existing wiring (S3 client, bucket,
// sign timeout) so there is one credential set for atom media.
package mediastore

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"

	"github.com/apollo-chora/chora-creation/internal/ports"
)

// Compile-time checks the adapter satisfies the durable-media ports.
var (
	_ ports.AtomMediaDownloadSigner = (*AtomMediaSigner)(nil)
	_ ports.MediaReHomer            = (*AtomMediaSigner)(nil)
)

// DurableBucket returns the configured durable atom-media bucket — the
// destination of CopyToDurable and the bucket ClassifyReference authorises
// durable refs against.
func (s *AtomMediaSigner) DurableBucket() string { return s.bucket }

// ClassifyReference validates raw against the buckets + origins this
// adapter is configured with and reports its reference kind:
//
//   - s3://<bucket>/<key>          → Durable when bucket is the durable
//     bucket; a foreign bucket is an error.
//   - gs://<bucket>/<key>          → Durable when bucket is the durable
//     bucket; a foreign bucket is an error (kept so a GCS deployment still
//     classifies its durable refs).
//   - http(s)://<host>/<bucket>/<key> → Transient when bucket is the
//     configured TRANSIENT bucket (env ATOM_MEDIA_TRANSIENT_BUCKET);
//     anything else is an error. The query string (the presign signature)
//     is stripped; an empty bucket or key is an error.
//   - anything else                → error.
//
// When ATOM_MEDIA_TRUSTED_PUBLIC_ORIGIN is set an http(s) ref from any
// other host is rejected; unset, any host is accepted (the transient-bucket
// check still authorises the ref). The durable bucket is always authorised
// for s3:// / gs://, whether or not the transient bucket is configured.
func (s *AtomMediaSigner) ClassifyReference(_ context.Context, raw string) (ports.MediaReference, error) {
	ref := strings.TrimSpace(raw)
	if ref == "" {
		return ports.MediaReference{}, errors.New("atom-media: empty media reference")
	}

	switch {
	case strings.HasPrefix(ref, "s3://"):
		bucket, key, err := parseObjectURI(ref, "s3://")
		if err != nil {
			return ports.MediaReference{}, fmt.Errorf("atom-media: %w", err)
		}
		if bucket != s.bucket {
			return ports.MediaReference{}, fmt.Errorf(
				"atom-media: refusing foreign s3:// bucket %q (durable bucket is %q)", bucket, s.bucket)
		}
		return ports.MediaReference{Kind: ports.MediaReferenceDurable, Bucket: bucket, Key: key}, nil

	case strings.HasPrefix(ref, "gs://"):
		bucket, key, err := parseObjectURI(ref, "gs://")
		if err != nil {
			return ports.MediaReference{}, fmt.Errorf("atom-media: %w", err)
		}
		if bucket != s.bucket {
			return ports.MediaReference{}, fmt.Errorf(
				"atom-media: refusing foreign gs:// bucket %q (durable bucket is %q)", bucket, s.bucket)
		}
		return ports.MediaReference{Kind: ports.MediaReferenceDurable, Bucket: bucket, Key: key}, nil

	case strings.HasPrefix(ref, "http://"), strings.HasPrefix(ref, "https://"):
		return s.classifyHTTPReference(ref)

	default:
		return ports.MediaReference{}, fmt.Errorf("atom-media: unrecognized media reference %q", ref)
	}
}

// classifyHTTPReference parses a path-style http(s) object URL
// (`http(s)://<host>/<bucket>/<key>?<signature>`) — the shape both the GCS
// V4 path-style URL and the S3/Garage presigned URL take — and authorises
// it against the configured transient bucket + trusted origin.
func (s *AtomMediaSigner) classifyHTTPReference(ref string) (ports.MediaReference, error) {
	u, err := url.Parse(ref)
	if err != nil {
		return ports.MediaReference{}, fmt.Errorf("atom-media: parse media URL: %w", err)
	}
	if s.trustedPublicOrigin != "" && !s.hostIsTrusted(u.Host) {
		return ports.MediaReference{}, fmt.Errorf(
			"atom-media: refusing media URL from untrusted host %q (trusted origin is %q)", u.Host, s.trustedPublicOrigin)
	}

	// u.Path excludes the query string, so the presign signature is gone.
	path := strings.TrimPrefix(u.Path, "/")
	i := strings.IndexByte(path, '/')
	if i <= 0 || i >= len(path)-1 {
		return ports.MediaReference{}, fmt.Errorf(
			"atom-media: malformed path-style media URL (want http(s)://<host>/<bucket>/<key>): %q", ref)
	}
	bucket, key := path[:i], path[i+1:]

	if s.transientBucket == "" {
		return ports.MediaReference{}, fmt.Errorf(
			"atom-media: cannot classify http(s) reference %q: transient bucket is not configured (set ATOM_MEDIA_TRANSIENT_BUCKET)", ref)
	}
	if bucket != s.transientBucket {
		return ports.MediaReference{}, fmt.Errorf(
			"atom-media: refusing foreign bucket %q in media URL (transient bucket is %q)", bucket, s.transientBucket)
	}
	return ports.MediaReference{Kind: ports.MediaReferenceTransient, Bucket: bucket, Key: key}, nil
}

// hostIsTrusted reports whether host matches the configured trusted public
// origin, which may be written either as a bare host[:port] or as a full
// URL.
func (s *AtomMediaSigner) hostIsTrusted(host string) bool {
	want := s.trustedPublicOrigin
	if u, err := url.Parse(want); err == nil && u.Host != "" {
		want = u.Host
	}
	return strings.EqualFold(host, want)
}

// CopyToDurable performs a server-side S3 copy of
// SrcBucket/SrcKey into the durable bucket under a freshly-minted per-atom
// key, returning the durable s3:// URI. No bytes transit the pod (the S3
// copy API streams object→object). Fail-loud on any storage error.
func (s *AtomMediaSigner) CopyToDurable(ctx context.Context, p ports.CopyToDurableParams) (string, error) {
	if strings.TrimSpace(p.SrcBucket) == "" || strings.TrimSpace(p.SrcKey) == "" {
		return "", errors.New("atom-media: CopyToDurable requires SrcBucket + SrcKey")
	}
	if strings.TrimSpace(p.TenantID) == "" || strings.TrimSpace(p.AtomID) == "" {
		return "", errors.New("atom-media: CopyToDurable requires TenantID + AtomID")
	}
	ext := strings.TrimSpace(p.Ext)
	if ext == "" {
		ext = "png"
	}

	objectID := uuid.Must(uuid.NewV7()).String()
	dstKey := AtomMediaKey(p.TenantID, p.AtomID, objectID, ext)

	_, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(s.bucket),
		CopySource: aws.String(p.SrcBucket + "/" + p.SrcKey),
		Key:        aws.String(dstKey),
	})
	if err != nil {
		return "", fmt.Errorf("atom-media: copy s3://%s/%s -> s3://%s/%s: %w",
			p.SrcBucket, p.SrcKey, s.bucket, dstKey, err)
	}
	return AtomMediaURI(s.bucket, dstKey), nil
}

// SignDownloadURL mints a fresh short-lived presigned GET URL over the
// durable object named by s3URI. Refuses any bucket other than the
// configured durable bucket.
func (s *AtomMediaSigner) SignDownloadURL(ctx context.Context, s3URI string) (ports.SignedDownloadURL, error) {
	bucket, key, err := parseS3URI(s3URI)
	if err != nil {
		return ports.SignedDownloadURL{}, fmt.Errorf("atom-media: %w", err)
	}
	if bucket != s.bucket {
		return ports.SignedDownloadURL{}, fmt.Errorf(
			"atom-media: refusing to sign foreign bucket %q (download signer is scoped to %q)", bucket, s.bucket)
	}

	expiresAt := time.Now().UTC().Add(defaultSignedURLTTL)
	signCtx, cancel := context.WithTimeout(ctx, s.signTimeout)
	defer cancel()

	out, err := s.presign.PresignGetObject(signCtx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, func(o *s3.PresignOptions) {
		o.Expires = defaultSignedURLTTL
	})
	if err != nil {
		return ports.SignedDownloadURL{}, fmt.Errorf("atom-media: presign get: %w", err)
	}
	return ports.SignedDownloadURL{URL: out.URL, ExpiresAt: expiresAt}, nil
}

// parseS3URI splits s3://{bucket}/{key} into bucket + key. Returns an error
// for a non-s3:// URI, a bucket-only URI, or an empty key.
func parseS3URI(s3URI string) (bucket, key string, err error) {
	return parseObjectURI(s3URI, "s3://")
}

// parseObjectURI splits {scheme}{bucket}/{key} into bucket + key. Returns an
// error for a URI with any other scheme, a bucket-only URI, or an empty key.
func parseObjectURI(uri, scheme string) (bucket, key string, err error) {
	if !strings.HasPrefix(uri, scheme) {
		return "", "", fmt.Errorf("not a %s URI: %q", scheme, uri)
	}
	rest := uri[len(scheme):]
	i := strings.IndexByte(rest, '/')
	if i <= 0 || i >= len(rest)-1 {
		return "", "", fmt.Errorf("malformed %s URI (want %sbucket/key): %q", scheme, scheme, uri)
	}
	return rest[:i], rest[i+1:], nil
}
