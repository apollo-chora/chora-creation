// media_durable.go — durable atom-media additions to the S3 adapter
// (W8 image re-home, OT#4):
//
//   - ClassifyReference: validates a stored media reference against the
//     configured durable + transient buckets and reports whether it names
//     a transient object (copy it) or an already-durable one (leave it) —
//     satisfies the reference-kind half of ports.MediaReHomer.
//   - CopyToDurable: server-side copy of a transient image object into the
//     durable bucket (satisfies ports.MediaReHomer).
//   - IsDurableRef: reports whether a stored ref is a durable atom-media
//     object this adapter can mint a signed GET URL for.
//   - TenantOwnsRef: reports whether a stored ref's object key lives in a
//     given tenant's own namespace — the ownership gate, independent of the
//     bucket gate SignDownloadURL applies.
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
//   - http(s)://<host>/<bucket>/<key> → Transient when host matches the
//     configured TRUSTED PUBLIC ORIGIN (env
//     ATOM_MEDIA_TRUSTED_PUBLIC_ORIGIN) and bucket is the configured
//     TRANSIENT bucket (env ATOM_MEDIA_TRANSIENT_BUCKET); anything else is
//     an error. The query string (the presign signature) is stripped; an
//     empty bucket or key is an error.
//   - anything else                → error.
//
// The http(s) path FAILS CLOSED: with ATOM_MEDIA_TRUSTED_PUBLIC_ORIGIN unset
// no http(s) reference is accepted at all (the error names the env var),
// because the origin that issued the presigned URL is the only authority for
// trusting it. Durable refs are unaffected — s3:// / gs:// on the durable
// bucket classify whether or not either env var is set.
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
// (`http(s)://<host>/<bucket>/<key>?<signature>`) — the shape both a GCS V4
// path-style URL and an S3/Garage presigned URL take — and authorises it
// against the configured trusted origin + transient bucket.
//
// It FAILS CLOSED on the origin. The origin that issued the presigned URL is
// the only authority for accepting an http(s) reference, so when
// ATOM_MEDIA_TRUSTED_PUBLIC_ORIGIN is unset every http(s) reference is
// refused with an error naming that env var. The service still boots and text
// questions plus already-durable s3:// refs are unaffected — only a transient
// http(s) ref needs the origin. A URL that names a different scheme, host or
// port than the configured origin is refused; the comparison is on parsed
// components, never a suffix match. Userinfo, a fragment and a missing host
// are refused outright.
func (s *AtomMediaSigner) classifyHTTPReference(ref string) (ports.MediaReference, error) {
	u, err := url.Parse(ref)
	if err != nil {
		return ports.MediaReference{}, fmt.Errorf("atom-media: parse media URL %s: %w", redactedRef(ref), err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ports.MediaReference{}, fmt.Errorf(
			"atom-media: unsupported media URL scheme %q (want http or https)", u.Scheme)
	}
	if u.User != nil {
		return ports.MediaReference{}, fmt.Errorf(
			"atom-media: refusing media URL carrying userinfo: %s", redactedRef(ref))
	}
	if u.Fragment != "" || u.RawFragment != "" {
		return ports.MediaReference{}, fmt.Errorf(
			"atom-media: refusing media URL carrying a fragment: %s", redactedRef(ref))
	}
	if u.Host == "" {
		return ports.MediaReference{}, fmt.Errorf(
			"atom-media: media URL has no host: %s", redactedRef(ref))
	}

	if s.trustedPublicOrigin == "" {
		return ports.MediaReference{}, errors.New(
			"atom-media: cannot classify an http(s) media reference: trusted public origin is not configured " +
				"(set ATOM_MEDIA_TRUSTED_PUBLIC_ORIGIN to the origin that issues the presigned URLs)")
	}
	if !originMatches(s.trustedPublicOrigin, u) {
		return ports.MediaReference{}, fmt.Errorf(
			"atom-media: refusing media URL from untrusted origin %s (trusted origin is %q)",
			redactedOrigin(u), s.trustedPublicOrigin)
	}

	// u.Path excludes the query string, so the presign signature is gone.
	path := strings.TrimPrefix(u.Path, "/")
	i := strings.IndexByte(path, '/')
	if i <= 0 || i >= len(path)-1 {
		return ports.MediaReference{}, fmt.Errorf(
			"atom-media: malformed path-style media URL (want http(s)://<host>/<bucket>/<key>): %s", redactedRef(ref))
	}
	bucket, key := path[:i], path[i+1:]

	if s.transientBucket == "" {
		return ports.MediaReference{}, errors.New(
			"atom-media: cannot classify an http(s) media reference: transient bucket is not configured " +
				"(set ATOM_MEDIA_TRANSIENT_BUCKET)")
	}
	if bucket != s.transientBucket {
		return ports.MediaReference{}, fmt.Errorf(
			"atom-media: refusing foreign bucket %q in media URL (transient bucket is %q)", bucket, s.transientBucket)
	}
	return ports.MediaReference{Kind: ports.MediaReferenceTransient, Bucket: bucket, Key: key}, nil
}

// originMatches reports whether u matches the configured trusted origin on
// scheme, host and port — a canonical comparison of parsed components, never
// a suffix/substring match. The origin may be configured as a full URL
// (`http://host:9000`, the preferred form) or as a bare `host[:port]`.
func originMatches(trusted string, u *url.URL) bool {
	scheme, host, port, ok := parseTrustedOrigin(trusted)
	if !ok {
		return false
	}
	if scheme != "" && !strings.EqualFold(scheme, u.Scheme) {
		return false
	}
	if !strings.EqualFold(host, u.Hostname()) {
		return false
	}
	switch {
	case port != "":
		return u.Port() == port
	case scheme != "":
		// The origin pinned a scheme but no port — compare effective ports.
		return effectivePort(u.Scheme) == effectivePort(scheme)
	default:
		// Bare host with no port: the ref must not name one either.
		return u.Port() == ""
	}
}

// parseTrustedOrigin splits the configured trusted origin into canonical
// (scheme, host, port). scheme is "" for a bare `host[:port]` origin.
func parseTrustedOrigin(trusted string) (scheme, host, port string, ok bool) {
	if u, err := url.Parse(trusted); err == nil && u.Host != "" {
		return strings.ToLower(u.Scheme), strings.ToLower(u.Hostname()), u.Port(), true
	}
	if u, err := url.Parse("//" + trusted); err == nil && u.Host != "" {
		return "", strings.ToLower(u.Hostname()), u.Port(), true
	}
	return "", "", "", false
}

// effectivePort returns a URL's explicit port, or its scheme's default.
func effectivePort(scheme string) string {
	switch strings.ToLower(scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

// redactedRef returns ref safe to put in an error or a log line: any
// userinfo, query string and fragment are stripped, so a presign signature (a
// credential) never reaches the logs.
func redactedRef(ref string) string {
	u, err := url.Parse(ref)
	if err != nil {
		return "<unparseable media reference>"
	}
	u.User = nil
	u.RawQuery, u.ForceQuery = "", false
	u.Fragment, u.RawFragment = "", ""
	return u.String()
}

// redactedOrigin renders scheme://host:port for a URL, dropping the path,
// query and userinfo — the origin alone is what failed the check.
func redactedOrigin(u *url.URL) string {
	origin := u.Host
	if u.Scheme != "" {
		origin = u.Scheme + "://" + u.Host
	}
	return origin
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

	// OWNERSHIP. The copy runs with THIS service's credentials, so a trusted
	// bucket + host does not prove the object belongs to the caller — on the
	// accept path the handler prefers a client-supplied image_url over the
	// stored draft's, so SrcKey is attacker-influenced. Refuse anything outside
	// the caller's own tenant namespace BEFORE minting a destination key or
	// touching the network. (The service checks the same boundary for every
	// classified reference; this is independent protection for the copy itself.)
	tenantPrefix := ports.TenantMediaPrefix(p.TenantID)
	if !strings.HasPrefix(p.SrcKey, tenantPrefix) {
		return "", fmt.Errorf(
			"atom-media: refusing to copy source key %s — it is outside tenant %q's namespace %q",
			redactedRef(p.SrcKey), p.TenantID, tenantPrefix)
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

// IsDurableRef reports whether ref names a durable atom-media object this
// adapter can mint a signed GET URL for: a well-formed
// `s3://<durable bucket>/<key>` URI.
//
// The accepted scheme set is deliberately exactly what SignDownloadURL can
// sign — `s3://` only, because parseS3URI accepts nothing else. Reporting a
// `gs://` durable ref as durable here would hand the signer a ref it always
// refuses, and a caller could no longer tell "this deployment cannot mint
// that" from "this needs no minting".
//
// MIGRATION: a legacy `gs://` ref written by an earlier GCS-era deployment is
// therefore NOT durable for this adapter. The read paths fail closed on it —
// they drop any non-durable ref that is not an already-fetchable http(s) URL,
// so it is never emitted raw — which means the image DISAPPEARS from the
// author/learner projection even though the stored reference is untouched.
// Recovering those images needs a data migration to s3://, not a grammar
// change here.
//
// Note the deliberate asymmetry with ClassifyReference, which DOES accept a
// `gs://` durable ref (for re-home idempotency): classifying a stored string
// is not the same as this adapter being able to sign it.
//
// False for an http(s) URL, an empty string, a foreign bucket, and a
// malformed ref. Reuses parseS3URI — one grammar, not two.
func (s *AtomMediaSigner) IsDurableRef(ref string) bool {
	bucket, _, err := parseS3URI(strings.TrimSpace(ref))
	return err == nil && bucket == s.bucket
}

// TenantOwnsRef reports whether ref names a media object inside tenantID's own
// per-tenant namespace (ports.TenantMediaPrefix).
//
// This is the OWNERSHIP gate, separate from the BUCKET gate SignDownloadURL
// applies: the durable bucket is shared by every tenant, so a caller that knows
// another tenant's object key could otherwise have it signed. It reuses
// ClassifyReference for the grammar, so a transient `http(s)` reference is
// checked exactly like a durable one.
//
// Returns (false, err) when the reference cannot be classified — ownership
// cannot be established, so the caller must fail closed.
func (s *AtomMediaSigner) TenantOwnsRef(ctx context.Context, tenantID, ref string) (bool, error) {
	if strings.TrimSpace(tenantID) == "" {
		return false, errors.New("atom-media: TenantOwnsRef requires a tenantID")
	}
	classified, err := s.ClassifyReference(ctx, ref)
	if err != nil {
		return false, err
	}
	return strings.HasPrefix(classified.Key, ports.TenantMediaPrefix(tenantID)), nil
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
