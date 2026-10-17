// Package mediastore adapts the AtomMediaSigner port to S3-compatible
// object storage (MinIO / S3) via presigned URLs.
//
// ATOM Phase 1 — ADR-156 atom redesign. Mints a single-use presigned URL
// the FE PUTs the image bytes to. The handler then expects the FE
// to call `PATCH /atoms/{atom_id}` with `media_assets[0].url` set to the
// canonical `s3://` ObjectURL returned alongside the upload URL
// (per ADR-156 Decision #5).
//
// Wiring: cmd/server/main.go constructs `NewAtomMediaSigner(bucket)` where
// the bucket comes from env `ATOM_MEDIA_BUCKET`. Empty env → constructor
// returns ErrAtomMediaSignerNotWired so main.go can leave the handler's
// signer reference nil and the route 503s (fail-loud per
// `feedback_no_stubs_real_wiring`).
//
// Credentials come from the environment (S3_ENDPOINT / S3_ACCESS_KEY_ID /
// S3_SECRET_ACCESS_KEY / S3_REGION / S3_FORCE_PATH_STYLE) — no cloud
// SDK credential chain.
//
// Reference classification (ports.MediaReHomer.ClassifyReference) is
// configured by ATOM_MEDIA_TRANSIENT_BUCKET, plus the
// ATOM_MEDIA_TRUSTED_PUBLIC_ORIGIN that http(s) references REQUIRE — see
// NewAtomMediaSigner.
package mediastore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"

	"github.com/apollo-chora/chora-creation/internal/ports"
)

// defaultMaxSizeBytes is the Phase 1 per-tenant size cap (2 MiB).
// The future-work hook is per-tenant entitlement at
// `TenantEntitlement.atom_media_max_bytes` — when wired, the adapter
// reads that value per-call and overrides this constant. See ADR-156
// Decision #5 + the OpenAPI MintAtomMediaSignedUrlRequest schema.
//
// TODO(ATOM Phase 2): pull TenantEntitlement.atom_media_max_bytes via
// gRPC from chora-tenancy; until then every tenant gets the 2 MiB floor.
const defaultMaxSizeBytes int64 = 2 * 1024 * 1024

// defaultSignedURLTTL is the presigned URL lifetime — 15 minutes per
// the `secrets-and-env` skill short-lived default. Tight on purpose: a
// stale signed URL leaking through logs / browser history is materially
// less dangerous than a long-lived one. The FE MUST upload within this
// window or call POST /atoms/{atom_id}/media again to re-mint.
const defaultSignedURLTTL = 15 * time.Minute

// defaultSignTimeoutValue caps the presign round-trip. Presigning is
// a local operation (no network I/O), but the timeout bounds any
// credential-provider work.
const defaultSignTimeoutValue = 10 * time.Second

// AtomMediaSignerOption configures the S3-backed AtomMediaSigner at
// construction. Functional-options pattern — keeps the constructor
// argument list short while letting callers wire only the knobs they
// need.
type AtomMediaSignerOption func(*AtomMediaSigner)

// WithSignTimeout overrides the default per-call sign deadline. Falls
// back to DefaultSignTimeout() when not supplied. Must be > 0.
func WithSignTimeout(d time.Duration) AtomMediaSignerOption {
	return func(s *AtomMediaSigner) {
		if d > 0 {
			s.signTimeout = d
		}
	}
}

// WithTransientBucket sets the TRANSIENT generated-image bucket the adapter
// accepts http(s) references for (re-home source). Defaults to env
// ATOM_MEDIA_TRANSIENT_BUCKET. Left unset, ClassifyReference still resolves
// s3:// / gs:// durable refs but fails loud on any http(s) ref rather than
// guessing a bucket.
func WithTransientBucket(bucket string) AtomMediaSignerOption {
	return func(s *AtomMediaSigner) {
		s.transientBucket = strings.TrimSpace(bucket)
	}
}

// WithTrustedPublicOrigin pins the public origin an http(s) reference must
// be served from. Defaults to env ATOM_MEDIA_TRUSTED_PUBLIC_ORIGIN. It is
// REQUIRED for http(s) references: left unset, ClassifyReference refuses
// every http(s) ref (fail closed) rather than accepting any host. Durable
// s3:// / gs:// refs are unaffected.
func WithTrustedPublicOrigin(origin string) AtomMediaSignerOption {
	return func(s *AtomMediaSigner) {
		s.trustedPublicOrigin = strings.TrimSpace(origin)
	}
}

// DefaultSignTimeout exposes the per-call sign deadline default so the
// handler/tests can assert + main.go can log the effective value.
func DefaultSignTimeout() time.Duration {
	return defaultSignTimeoutValue
}

// AtomMediaSigner is the S3-backed AtomMediaSigner.
type AtomMediaSigner struct {
	client      *s3.Client
	presign     *s3.PresignClient
	bucket      string
	signTimeout time.Duration

	// transientBucket is the short-lived generated-image bucket a
	// re-homeable http(s) ref must live in (env ATOM_MEDIA_TRANSIENT_BUCKET).
	transientBucket string

	// trustedPublicOrigin, when set, is the only host an http(s) ref may
	// be served from (env ATOM_MEDIA_TRUSTED_PUBLIC_ORIGIN).
	trustedPublicOrigin string
}

// NewAtomMediaSigner constructs the adapter from the environment
// (S3_ENDPOINT / S3_ACCESS_KEY_ID / S3_SECRET_ACCESS_KEY / S3_REGION /
// S3_FORCE_PATH_STYLE). Returns ErrAtomMediaSignerNotWired when bucket
// is empty.
//
// Two further env vars configure reference CLASSIFICATION (see
// ClassifyReference):
//
//   - ATOM_MEDIA_TRANSIENT_BUCKET — the short-lived generated-image bucket
//     the durable copy sources from. Unset ⇒ durable s3:// / gs:// refs
//     still classify, but an http(s) ref fails loud ("transient bucket is
//     not configured") instead of guessing one.
//   - ATOM_MEDIA_TRUSTED_PUBLIC_ORIGIN — the origin that issues the
//     transient presigned URLs. REQUIRED for http(s) references: unset ⇒
//     every http(s) ref fails loud ("trusted public origin is not
//     configured"), because an unauthenticated host is not a copy source we
//     can trust. Boot is unaffected; durable refs and text questions keep
//     working.
//
// Both can be overridden per-call via WithTransientBucket /
// WithTrustedPublicOrigin.
//
// S3_PUBLIC_ENDPOINT (optional) is the endpoint a BROWSER can reach. SigV4
// signs the Host header, so a presigned URL minted against the in-network
// service (e.g. garage:3900) cannot be fetched by the author's/learner's
// browser — it must name the public host. When set and non-empty, the
// PRESIGN client is derived from a second S3 client whose BaseEndpoint is
// this public endpoint, so every minted URL names the reachable host. The
// client used for actual API calls (upload/download/copy) keeps using
// S3_ENDPOINT — only the signing host changes. Unset or empty ⇒ URLs keep
// naming S3_ENDPOINT (dev / no-public-endpoint case).
func NewAtomMediaSigner(bucket string, opts ...AtomMediaSignerOption) (*AtomMediaSigner, error) {
	if strings.TrimSpace(bucket) == "" {
		return nil, fmt.Errorf("atom-media S3 bucket not set: %w", ports.ErrAtomMediaSignerNotWired)
	}

	endpoint := strings.TrimSpace(os.Getenv("S3_ENDPOINT"))
	if endpoint == "" {
		return nil, fmt.Errorf("atom-media S3 endpoint not set: %w", ports.ErrAtomMediaSignerNotWired)
	}
	accessKey := strings.TrimSpace(os.Getenv("S3_ACCESS_KEY_ID"))
	if accessKey == "" {
		accessKey = strings.TrimSpace(os.Getenv("S3_ACCESS_KEY"))
	}
	secretKey := strings.TrimSpace(os.Getenv("S3_SECRET_ACCESS_KEY"))
	if secretKey == "" {
		secretKey = strings.TrimSpace(os.Getenv("S3_SECRET_KEY"))
	}
	if accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("atom-media S3 credentials not set: %w", ports.ErrAtomMediaSignerNotWired)
	}

	region := strings.TrimSpace(os.Getenv("S3_REGION"))
	if region == "" {
		region = "us-east-1"
	}
	usePathStyle := true
	if v := strings.TrimSpace(os.Getenv("S3_FORCE_PATH_STYLE")); v != "" {
		usePathStyle = v == "true" || v == "1"
	}

	// Ensure the endpoint has a scheme.
	endpoint = ensureScheme(endpoint)

	apiOptions := s3.Options{
		Region:       region,
		BaseEndpoint: aws.String(endpoint),
		UsePathStyle: usePathStyle,
		Credentials:  credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
	}
	client := s3.New(apiOptions)

	// The client whose endpoint ends up in the presigned URL. Signing is
	// pure computation (no connection), so when a public endpoint is
	// configured we sign with THAT host — otherwise the URL names the
	// in-network service (e.g. garage:3900) and the author's/learner's
	// browser cannot fetch it. Mirrors chora-ai-kernel-orchestrator's
	// GcsImageUploadAdapter._resolve_signing_client.
	signingClient := client
	if publicEndpoint := strings.TrimSpace(os.Getenv("S3_PUBLIC_ENDPOINT")); publicEndpoint != "" {
		signingOptions := apiOptions
		signingOptions.BaseEndpoint = aws.String(ensureScheme(publicEndpoint))
		signingClient = s3.New(signingOptions)
	}

	s := &AtomMediaSigner{
		client:              client,
		presign:             s3.NewPresignClient(signingClient),
		bucket:              bucket,
		signTimeout:         defaultSignTimeoutValue,
		transientBucket:     strings.TrimSpace(os.Getenv("ATOM_MEDIA_TRANSIENT_BUCKET")),
		trustedPublicOrigin: strings.TrimSpace(os.Getenv("ATOM_MEDIA_TRUSTED_PUBLIC_ORIGIN")),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// ensureScheme prepends https:// when the endpoint carries no scheme, so
// both the API client and the signing client parse a full URL.
func ensureScheme(endpoint string) string {
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return "https://" + endpoint
	}
	return endpoint
}

// Bucket returns the configured S3 bucket name (handy for the HTTP
// handler emitting ObjectURL on the response envelope).
func (s *AtomMediaSigner) Bucket() string {
	return s.bucket
}

// Compile-time check the adapter satisfies the port.
var _ ports.AtomMediaSigner = (*AtomMediaSigner)(nil)

// Sign mints the presigned PUT URL. The object key is locked at
// `tenants/{tenant_id}/atoms/{atom_id}/{object_id}.{ext}` per ADR-156
// Phase 1 §5; object_id is a fresh UUIDv7 so a sequence of uploads to
// the same atom don't collide (the FE persists a single `media_assets[]`
// entry via PATCH after the upload completes).
//
// Phase 1 ships the simple 2 MiB tenant cap. The per-tenant
// `TenantEntitlement.atom_media_max_bytes` lookup is future work — see
// the TODO on `defaultMaxSizeBytes` above.
func (s *AtomMediaSigner) Sign(ctx context.Context, atomID, tenantID string, in ports.SignAtomMediaInput) (ports.SignAtomMediaOutput, error) {
	if strings.TrimSpace(atomID) == "" {
		return ports.SignAtomMediaOutput{}, errors.New("atom-media: atomID required")
	}
	if strings.TrimSpace(tenantID) == "" {
		return ports.SignAtomMediaOutput{}, errors.New("atom-media: tenantID required")
	}
	ext, err := ExtensionForMIME(in.MIME)
	if err != nil {
		return ports.SignAtomMediaOutput{}, fmt.Errorf("atom-media: %w", err)
	}

	objectID := uuid.Must(uuid.NewV7()).String()
	key := AtomMediaKey(tenantID, atomID, objectID, ext)
	expiresAt := time.Now().UTC().Add(defaultSignedURLTTL)

	signCtx, cancel := context.WithTimeout(ctx, s.signTimeout)
	defer cancel()

	out, err := s.presign.PresignPutObject(signCtx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		ContentType: aws.String(in.MIME),
	}, func(o *s3.PresignOptions) {
		o.Expires = defaultSignedURLTTL
	})
	if err != nil {
		return ports.SignAtomMediaOutput{}, fmt.Errorf("atom-media: presign put: %w", err)
	}

	return ports.SignAtomMediaOutput{
		UploadURL:    out.URL,
		ExpiresAt:    expiresAt,
		ObjectURL:    AtomMediaURI(s.bucket, key),
		MaxSizeBytes: defaultMaxSizeBytes,
	}, nil
}

// -----------------------------------------------------------------------------
// Pure-function helpers — exported so the HTTP handler can shape error
// envelopes and so tests can assert key formats without spinning a real
// S3 client.
// -----------------------------------------------------------------------------

// AtomMediaKey returns the canonical S3 object key for an atom-media
// upload. Locked at
// `tenants/{tenant_id}/atoms/{atom_id}/{object_id}.{ext}` per ADR-156
// Phase 1 §5. The tenant prefix scopes auditing per-tenant; the
// atom prefix groups the atom's media; the object_id (UUIDv7) avoids
// collision on re-mint; ext lets a downstream consumer probe MIME
// without re-introspection.
//
// The per-tenant prefix comes from ports.TenantMediaPrefix — the single
// definition of the ownership boundary, shared with the transient keys qgen
// writes (`tenants/{tenant}/jobs/{job}/{file}`) that a re-home sources from.
func AtomMediaKey(tenantID, atomID, objectID, ext string) string {
	return ports.TenantMediaPrefix(tenantID) + "atoms/" + atomID + "/" + objectID + "." + ext
}

// AtomMediaURI joins bucket + key into a canonical s3:// URI.
func AtomMediaURI(bucket, key string) string {
	return "s3://" + bucket + "/" + key
}

// ExtensionForMIME maps the Phase 1 MIME enum
// (image/jpeg | image/png | image/webp) to its canonical file
// extension. Returns an error for anything else so the HTTP handler can
// emit 415 with a useful message. Strict on case + whitespace — the
// OpenAPI enum is lowercase and the handler trims before validating.
func ExtensionForMIME(mime string) (string, error) {
	switch mime {
	case "image/jpeg":
		return "jpg", nil
	case "image/png":
		return "png", nil
	case "image/webp":
		return "webp", nil
	default:
		return "", fmt.Errorf("unsupported mime %q (expected image/jpeg, image/png, or image/webp)", mime)
	}
}

// DefaultMaxSizeBytes exposes the Phase 1 per-tenant size cap (2 MiB)
// so the HTTP handler can early-reject oversized requests with 413
// BEFORE touching the S3 adapter (saves a wasted Sign call).
func DefaultMaxSizeBytes() int64 {
	return defaultMaxSizeBytes
}

// DefaultSignedURLTTL exposes the Phase 1 URL lifetime so the handler /
// tests can assert expiry without redeclaring the constant.
func DefaultSignedURLTTL() time.Duration {
	return defaultSignedURLTTL
}
