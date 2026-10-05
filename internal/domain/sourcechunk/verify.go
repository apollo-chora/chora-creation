// verify.go — Lane 1c D15 citation verification matcher.
//
// The qgen crew model-reports per-question citations {source_file, page,
// excerpt}; chora-creation (the chunk-store owner — the orchestrator must
// NOT read chora_creation, cross-DB rule) verifies each excerpt against the
// job's persisted chunks and stamps the tri-state result:
//
//	verified == true  → excerpt matched chunk text ("✓ verified" chip)
//	verified == false → no match ("⚠ unverified" — surfaced, NEVER dropped)
//	verified == nil   → no verification possible ("AI-reported": image
//	                    source — no text layer in v1 — or an empty corpus)
//
// Matching ladder (deterministic):
//
//	tiers   : cited file + cited page → cited file → all chunks
//	per tier: normalised substring first; then fuzzy ≥0.8 token overlap
//	tie     : lowest chunk_index (then input order) wins
package sourcechunk

import "strings"

// FuzzyMatchThreshold is the minimum token-overlap ratio (excerpt tokens
// found in chunk tokens / excerpt tokens) for a fuzzy verification hit.
const FuzzyMatchThreshold = 0.8

// Citation is the model-reported grounding reference for one candidate
// question (wire shape: OpenAPI QuestionCitation, minus the stamped
// verification outputs).
type Citation struct {
	SourceFile string // gs:// blob uri OR the crew-reported display filename
	Page       *int   // 1-based; nil for unpaged/unknown
	Excerpt    string
}

// Verification is the stamped outcome for one Citation.
type Verification struct {
	Verified *bool   // tri-state per the package doc
	ChunkID  *string // matched source_material_chunks.chunk_id when true
}

// Verify runs the D15 matching ladder for one citation against the job's
// chunk corpus. imageSource marks citations whose cited file is an image
// (no text layer in v1) — those stay AI-reported (nil) without matching.
func Verify(c Citation, corpus []Chunk, imageSource bool) Verification {
	if imageSource {
		return Verification{}
	}
	if len(corpus) == 0 {
		// Nothing to verify against (all-image batch or extraction failed
		// for every file) — verification is not possible, not "failed".
		return Verification{}
	}

	excerpt := Normalise(c.Excerpt)
	f := false
	if excerpt == "" {
		return Verification{Verified: &f}
	}

	tiers := buildTiers(c, corpus)
	// Pass 1 — exact normalised substring, tier by tier.
	for _, tier := range tiers {
		for _, ch := range tier {
			if strings.Contains(Normalise(ch.Text), excerpt) {
				t := true
				id := ch.ChunkID
				return Verification{Verified: &t, ChunkID: &id}
			}
		}
	}
	// Pass 2 — fuzzy token-overlap ≥ threshold; best score within the first
	// tier that clears the gate.
	for _, tier := range tiers {
		bestScore := 0.0
		bestID := ""
		for _, ch := range tier {
			score := TokenOverlap(c.Excerpt, ch.Text)
			if score >= FuzzyMatchThreshold && score > bestScore {
				bestScore = score
				bestID = ch.ChunkID
			}
		}
		if bestID != "" {
			t := true
			id := bestID
			return Verification{Verified: &t, ChunkID: &id}
		}
	}
	return Verification{Verified: &f}
}

// buildTiers orders the corpus into the preference tiers:
// [cited file + cited page] → [cited file] → [everything]. Tiers may be
// empty; the full corpus is always the final tier so a real match is never
// missed because the crew garbled the filename.
func buildTiers(c Citation, corpus []Chunk) [][]Chunk {
	var samePage, sameFile []Chunk
	for _, ch := range corpus {
		if !FileRefMatches(c.SourceFile, ch.FileURI) {
			continue
		}
		sameFile = append(sameFile, ch)
		if c.Page != nil && ch.PageNo != nil && *c.Page == *ch.PageNo {
			samePage = append(samePage, ch)
		}
	}
	return [][]Chunk{samePage, sameFile, corpus}
}

// FileRefMatches reports whether the crew-cited file string refers to the
// given file URI. The crew may report the full gs:// URI, the object
// basename, or the original display filename — compare normalised full
// strings then basenames (either direction). Exported for the subscriber's
// image-source detection (same matching semantics as the tiers).
func FileRefMatches(cited, fileURI string) bool {
	cn := Normalise(cited)
	un := Normalise(fileURI)
	if cn == "" || un == "" {
		return false
	}
	if cn == un {
		return true
	}
	cb := basename(cn)
	ub := basename(un)
	return cb == ub || cb == un || cn == ub
}

func basename(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// TokenOverlap returns |excerpt tokens present in text| / |excerpt tokens|
// over normalised alphanumeric tokens. 0 when the excerpt has no tokens.
// Duplicate excerpt tokens count once (set semantics).
func TokenOverlap(excerpt, text string) float64 {
	eTokens := tokenSet(excerpt)
	if len(eTokens) == 0 {
		return 0
	}
	tTokens := tokenSet(text)
	hit := 0
	for tok := range eTokens {
		if _, ok := tTokens[tok]; ok {
			hit++
		}
	}
	return float64(hit) / float64(len(eTokens))
}

func tokenSet(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, tok := range strings.FieldsFunc(Normalise(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if tok != "" {
			out[tok] = struct{}{}
		}
	}
	return out
}
