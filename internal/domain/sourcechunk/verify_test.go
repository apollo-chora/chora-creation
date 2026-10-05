// verify_test.go — RED→GREEN coverage for the Lane 1c D15 citation
// verification matcher: normalised substring match with ≥0.8 token-overlap
// fuzzy fallback, file/page preference tiers, image/empty-corpus → nil
// ("AI-reported"). NEVER drops a citation — the result only stamps state.
package sourcechunk_test

import (
	"testing"
	"time"

	"github.com/apollo-chora/chora-creation/internal/domain/sourcechunk"
)

func mkChunk(id, fileURI string, pageNo *int, idx int, text string) sourcechunk.Chunk {
	return sourcechunk.Chunk{
		ChunkID:    id,
		JobID:      job,
		TenantID:   tnt,
		FileURI:    fileURI,
		FileRole:   sourcechunk.RoleSource,
		ChunkIndex: idx,
		PageNo:     pageNo,
		Text:       text,
		CreatedAt:  time.Now().UTC(),
	}
}

const (
	fileA = "gs://bucket/tenants/t/jobs/j/source-1"
	fileB = "gs://bucket/tenants/t/jobs/j/source-2"
)

func corpus() []sourcechunk.Chunk {
	return []sourcechunk.Chunk{
		mkChunk("c-a1", fileA, intp(1), 0, "Photosynthesis converts light energy into chemical energy stored in glucose."),
		mkChunk("c-a2", fileA, intp(2), 1, "Chlorophyll absorbs red and blue light while reflecting green light."),
		mkChunk("c-b1", fileB, intp(1), 0, "Mitochondria are the powerhouse of the cell, producing ATP."),
	}
}

func TestVerify_SubstringMatch_SameFileAndPage(t *testing.T) {
	v := sourcechunk.Verify(sourcechunk.Citation{
		SourceFile: fileA,
		Page:       intp(2),
		Excerpt:    "Chlorophyll absorbs RED and blue light",
	}, corpus(), false)

	if v.Verified == nil || !*v.Verified {
		t.Fatalf("Verified = %v; want true", v.Verified)
	}
	if v.ChunkID == nil || *v.ChunkID != "c-a2" {
		t.Errorf("ChunkID = %v; want c-a2", v.ChunkID)
	}
}

func TestVerify_PrefersCitedPageOverOtherPages(t *testing.T) {
	// The same sentence exists on both pages of fileA; the cited page wins.
	cs := []sourcechunk.Chunk{
		mkChunk("p1", fileA, intp(1), 0, "shared duplicated sentence appears here"),
		mkChunk("p2", fileA, intp(2), 1, "shared duplicated sentence appears here"),
	}
	v := sourcechunk.Verify(sourcechunk.Citation{
		SourceFile: fileA, Page: intp(2), Excerpt: "duplicated sentence appears",
	}, cs, false)
	if v.ChunkID == nil || *v.ChunkID != "p2" {
		t.Errorf("ChunkID = %v; want p2 (cited-page tier wins)", v.ChunkID)
	}
}

func TestVerify_FallsBackToOtherFiles(t *testing.T) {
	// Citation names fileA but the excerpt only exists in fileB — the
	// all-chunks fallback still verifies (never silently drop a real match).
	v := sourcechunk.Verify(sourcechunk.Citation{
		SourceFile: fileA, Page: intp(1), Excerpt: "mitochondria are the powerhouse of the cell",
	}, corpus(), false)
	if v.Verified == nil || !*v.Verified {
		t.Fatalf("Verified = %v; want true via all-chunks fallback", v.Verified)
	}
	if v.ChunkID == nil || *v.ChunkID != "c-b1" {
		t.Errorf("ChunkID = %v; want c-b1", v.ChunkID)
	}
}

func TestVerify_FileMatchByBasename(t *testing.T) {
	// The crew often reports the display filename, not the gs:// URI.
	v := sourcechunk.Verify(sourcechunk.Citation{
		SourceFile: "source-2", Page: intp(1), Excerpt: "producing ATP",
	}, corpus(), false)
	if v.Verified == nil || !*v.Verified {
		t.Fatalf("Verified = %v; want true (basename file match)", v.Verified)
	}
	if v.ChunkID == nil || *v.ChunkID != "c-b1" {
		t.Errorf("ChunkID = %v; want c-b1", v.ChunkID)
	}
}

func TestVerify_FuzzyTokenOverlap(t *testing.T) {
	// Paraphrase-adjacent excerpt: ≥0.8 of its tokens appear in the chunk
	// (word order differs; one token changed). 9 tokens, 8 present = 0.889.
	v := sourcechunk.Verify(sourcechunk.Citation{
		SourceFile: fileA, Page: intp(1), Excerpt: "photosynthesis converts light energy into chemical energy in sunlight",
	}, corpus(), false)
	if v.Verified == nil || !*v.Verified {
		t.Fatalf("Verified = %v; want true (fuzzy ≥0.8 token overlap)", v.Verified)
	}
	if v.ChunkID == nil || *v.ChunkID != "c-a1" {
		t.Errorf("ChunkID = %v; want c-a1", v.ChunkID)
	}
}

func TestVerify_NoMatchIsFalse_NeverDropped(t *testing.T) {
	v := sourcechunk.Verify(sourcechunk.Citation{
		SourceFile: fileA, Page: intp(1), Excerpt: "quantum entanglement of qubits in superconducting circuits",
	}, corpus(), false)
	if v.Verified == nil || *v.Verified {
		t.Fatalf("Verified = %v; want false (hallucination suspect, surfaced not dropped)", v.Verified)
	}
	if v.ChunkID != nil {
		t.Errorf("ChunkID = %v; want nil on unverified", v.ChunkID)
	}
}

func TestVerify_ImageSource_NilAIReported(t *testing.T) {
	v := sourcechunk.Verify(sourcechunk.Citation{
		SourceFile: "gs://bucket/t/j/source-3", Page: nil, Excerpt: "text read off an image",
	}, corpus(), true)
	if v.Verified != nil {
		t.Fatalf("Verified = %v; want nil for image source (AI-reported)", *v.Verified)
	}
	if v.ChunkID != nil {
		t.Errorf("ChunkID = %v; want nil", v.ChunkID)
	}
}

func TestVerify_EmptyCorpus_NilAIReported(t *testing.T) {
	v := sourcechunk.Verify(sourcechunk.Citation{
		SourceFile: fileA, Page: intp(1), Excerpt: "anything at all",
	}, nil, false)
	if v.Verified != nil {
		t.Fatalf("Verified = %v; want nil when no chunks exist (no verification possible)", *v.Verified)
	}
}

func TestVerify_EmptyExcerpt_False(t *testing.T) {
	v := sourcechunk.Verify(sourcechunk.Citation{
		SourceFile: fileA, Page: intp(1), Excerpt: "   ",
	}, corpus(), false)
	if v.Verified == nil || *v.Verified {
		t.Fatalf("Verified = %v; want false for blank excerpt", v.Verified)
	}
}

func TestTokenOverlap_Boundary(t *testing.T) {
	// 4 of 5 tokens present = 0.8 → passes the ≥0.8 gate.
	score := sourcechunk.TokenOverlap("alpha beta gamma delta epsilon", "alpha beta gamma delta UNRELATED filler")
	if score < 0.79 || score > 0.81 {
		t.Errorf("TokenOverlap = %v; want 0.8", score)
	}
	if sourcechunk.TokenOverlap("", "anything") != 0 {
		t.Error("TokenOverlap with empty excerpt must be 0")
	}
}
