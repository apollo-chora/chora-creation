// Package storage adapts the media.SignedURLSigner port for both the in-memory
// dev stub (returns a deterministic stub URL) and the production Cloud Storage
// V4 signer. The production adapter lands at M12; this MVP file ships only
// the in-memory stub used by tests + dev binaries.
//
// Per CLAUDE.md §6 ("no inline config") all production wiring values come
// from env (GCS bucket name, signing key path, signer service-account
// email). The in-memory stub here takes no config and is safe by default.
package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/apollo-chora/chora-creation/internal/domain/media"
)

// InMemorySigner is the dev/test SignedURLSigner — returns a deterministic
// stub URL whose shape is recognisable to the e2e test (`stub://...`).
type InMemorySigner struct {
	BucketName string // optional — empty falls back to "chora-content-media-stub"
}

// NewInMemorySigner constructs a stub signer. Pass empty bucket for the
// default test bucket name.
func NewInMemorySigner(bucket string) *InMemorySigner {
	if strings.TrimSpace(bucket) == "" {
		bucket = "chora-content-media-stub"
	}
	return &InMemorySigner{BucketName: bucket}
}

// UploadURL returns a stub URL of the form
// `stub://{bucket}/{tenant}/{atom}/{asset_id}/{filename}?ttl={ttl}`.
// Real GCS V4 signing happens in the M12 Cloud-Storage adapter.
func (s *InMemorySigner) UploadURL(_ context.Context, m *media.MediaAsset, ttlSeconds int) (string, error) {
	if m == nil {
		return "", errors.New("storage: asset is nil")
	}
	if ttlSeconds <= 0 {
		ttlSeconds = 600 // 10 min default
	}
	return fmt.Sprintf("stub://%s/%s/%s/%s/%s?ttl=%d",
		s.BucketName, m.TenantID, m.AtomID, m.AssetID, m.Filename, ttlSeconds), nil
}

// Compile-time check the stub satisfies the port.
var _ media.SignedURLSigner = (*InMemorySigner)(nil)
