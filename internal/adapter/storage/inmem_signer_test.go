package storage_test

import (
	"context"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/adapter/storage"
	"github.com/apollo-chora/chora-creation/internal/domain/media"
)

func TestInMemorySigner_UploadURL_DefaultBucket(t *testing.T) {
	t.Parallel()

	s := storage.NewInMemorySigner("")
	if s.BucketName != "chora-content-media-stub" {
		t.Errorf("default bucket = %q", s.BucketName)
	}
}

func TestInMemorySigner_UploadURL_CustomBucket(t *testing.T) {
	t.Parallel()

	s := storage.NewInMemorySigner("my-bucket")
	a, _ := media.New(media.NewParams{
		TenantID: "t", Gcid: "g", AtomID: "a", Filename: "f.mp4",
		ContentType: "video/mp4", SizeBytes: 1024,
	})
	url, err := s.UploadURL(context.Background(), a, 600)
	if err != nil {
		t.Fatalf("UploadURL: %v", err)
	}
	if !strings.HasPrefix(url, "stub://my-bucket/") {
		t.Errorf("url = %q; want stub://my-bucket/...", url)
	}
	if !strings.Contains(url, "ttl=600") {
		t.Errorf("url missing ttl=600: %q", url)
	}
}

func TestInMemorySigner_UploadURL_DefaultTTL(t *testing.T) {
	t.Parallel()

	s := storage.NewInMemorySigner("b")
	a, _ := media.New(media.NewParams{
		TenantID: "t", Gcid: "g", AtomID: "a", Filename: "f.mp4",
		ContentType: "video/mp4", SizeBytes: 1024,
	})
	url, err := s.UploadURL(context.Background(), a, 0)
	if err != nil {
		t.Fatalf("UploadURL: %v", err)
	}
	if !strings.Contains(url, "ttl=600") {
		t.Errorf("default ttl missing: %q", url)
	}
}

func TestInMemorySigner_UploadURL_NilAsset(t *testing.T) {
	t.Parallel()

	s := storage.NewInMemorySigner("b")
	if _, err := s.UploadURL(context.Background(), nil, 60); err == nil {
		t.Errorf("nil asset: expected error, got nil")
	}
}
