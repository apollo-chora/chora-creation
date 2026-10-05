// media_durable.go — durable atom-media additions to the S3 adapter
// (W8 image re-home, OT#4):
//
//   - CopyToDurable: server-side copy of a transient image object into the
//     durable bucket (satisfies ports.MediaReHomer).
//   - SignDownloadURL: mints a fresh short-lived presigned GET URL over a
//     durable s3:// object (satisfies ports.AtomMediaDownloadSigner).
//
// Both reuse the AtomMediaSigner's existing wiring (S3 client, bucket,
// sign timeout) so there is one credential set for atom media.
package mediastore

import (
	"context"
	"errors"
	"fmt"
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
// re-home service uses it for the idempotency check (a ref already in this
// bucket is left untouched).
func (s *AtomMediaSigner) DurableBucket() string { return s.bucket }

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
	const scheme = "s3://"
	if !strings.HasPrefix(s3URI, scheme) {
		return "", "", fmt.Errorf("not a s3:// URI: %q", s3URI)
	}
	rest := s3URI[len(scheme):]
	i := strings.IndexByte(rest, '/')
	if i <= 0 || i >= len(rest)-1 {
		return "", "", fmt.Errorf("malformed s3:// URI (want s3://bucket/key): %q", s3URI)
	}
	return rest[:i], rest[i+1:], nil
}
