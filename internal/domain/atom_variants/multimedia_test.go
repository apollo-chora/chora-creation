package atom_variants_test

import (
	"strings"
	"testing"

	av "github.com/apollo-chora/chora-creation/internal/domain/atom_variants"
)

func TestMultimedia_ValidatesURLAndQA(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		params  av.MultimediaParams
		wantErr string
	}{
		{
			name:    "missing atom",
			params:  av.MultimediaParams{VideoURL: "https://x.test/v.mp4", Question: "Q?", CorrectAnswer: "yes"},
			wantErr: "atom",
		},
		{
			name:    "missing video url",
			params:  av.MultimediaParams{AtomID: atomID, Question: "Q?", CorrectAnswer: "yes"},
			wantErr: "video_url",
		},
		{
			name:    "non-https video url",
			params:  av.MultimediaParams{AtomID: atomID, VideoURL: "http://x.test/v.mp4", Question: "Q?", CorrectAnswer: "y"},
			wantErr: "https",
		},
		{
			name:    "garbage video url",
			params:  av.MultimediaParams{AtomID: atomID, VideoURL: "not-a-url", Question: "Q?", CorrectAnswer: "y"},
			wantErr: "video_url",
		},
		{
			name:    "missing question",
			params:  av.MultimediaParams{AtomID: atomID, VideoURL: "https://x.test/v.mp4", CorrectAnswer: "y"},
			wantErr: "question",
		},
		{
			name:    "missing correct answer",
			params:  av.MultimediaParams{AtomID: atomID, VideoURL: "https://x.test/v.mp4", Question: "Q?"},
			wantErr: "correct_answer",
		},
		{
			name:    "non-https thumbnail",
			params:  av.MultimediaParams{AtomID: atomID, VideoURL: "https://x.test/v.mp4", Question: "Q?", CorrectAnswer: "y", ThumbnailURL: "ftp://x"},
			wantErr: "thumbnail",
		},
		{
			name:    "question too long",
			params:  av.MultimediaParams{AtomID: atomID, VideoURL: "https://x.test/v.mp4", Question: strings.Repeat("q", 1025), CorrectAnswer: "y"},
			wantErr: "question",
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := av.NewMultimedia(tc.params)
			if err == nil {
				t.Fatalf("expected error containing %q; got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %q; want contains %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestMultimedia_HappyPath_WithThumbnail(t *testing.T) {
	t.Parallel()

	m, err := av.NewMultimedia(av.MultimediaParams{
		AtomID:        atomID,
		VideoURL:      "https://cdn.chora.site/v/abc.mp4",
		Question:      "What is the capital of France?",
		CorrectAnswer: "Paris",
		ThumbnailURL:  "https://cdn.chora.site/v/abc.jpg",
	})
	if err != nil {
		t.Fatalf("NewMultimedia unexpected: %v", err)
	}
	if m.Type() != av.VariantTypeMultimedia {
		t.Errorf("Type = %s; want multimedia", m.Type())
	}
	if m.ThumbnailURL == "" {
		t.Errorf("ThumbnailURL empty after roundtrip")
	}
	if m.PublishedAt.IsZero() {
		t.Errorf("PublishedAt zero; want set")
	}
}

func TestMultimedia_HappyPath_NoThumbnail(t *testing.T) {
	t.Parallel()

	m, err := av.NewMultimedia(av.MultimediaParams{
		AtomID:        atomID,
		VideoURL:      "https://cdn.chora.site/v/abc.mp4",
		Question:      "Q?",
		CorrectAnswer: "A",
	})
	if err != nil {
		t.Fatalf("NewMultimedia unexpected: %v", err)
	}
	if m.ThumbnailURL != "" {
		t.Errorf("ThumbnailURL = %q; want empty when not provided", m.ThumbnailURL)
	}
}
