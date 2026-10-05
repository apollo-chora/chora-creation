// atom_updated_event.go - the metadata diff behind
// chora.creation.atom.updated.v1 (ADR-244 D5).
//
// PATCH /api/atoms/{atom_id} applies a partial update in two places: the
// ADR-156 Phase 1 fields are set directly on the aggregate by the handler, and
// the legacy four (title/body/tags/mode) go through ApplyUpdate. Neither knows
// what it moved, and ApplyUpdate bumps the revision even for a patch that
// changed nothing.
//
// changed_fields is the field chora-consumption's KG invalidation branches on,
// so "which fields did this patch actually move" has to be answered exactly:
// a field named that did not move makes a consumer redo work for nothing, and
// a field that moved but went unnamed leaves it reading stale metadata. The
// answer is a before/after comparison of the aggregate, restricted to the
// fields the request actually supplied - a field sent at its current value is
// not a change.
package httpadapter

import (
	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// atomMetadataSnapshot is the pre-patch value of every metadata slot the PATCH
// route can move. Slices are copied: the comparison must survive the handler
// replacing them on the aggregate.
type atomMetadataSnapshot struct {
	stem              string
	subject           string
	authorNote        string
	cognitiveLevel    atom.CognitiveLevel
	imdaDimensionTags []atom.ImdaDimTag
	mediaAssets       []atom.MediaAsset
	difficulty        int
	title             string
	body              string
	tags              []string
	mode              atom.Mode
}

// snapshotAtomMetadata captures the aggregate BEFORE any patch field lands.
func snapshotAtomMetadata(a *atom.LearningAtom) atomMetadataSnapshot {
	return atomMetadataSnapshot{
		stem:              a.Stem,
		subject:           a.Subject,
		authorNote:        a.AuthorNote,
		cognitiveLevel:    a.CognitiveLevel,
		imdaDimensionTags: append([]atom.ImdaDimTag(nil), a.ImdaDimensionTags...),
		mediaAssets:       append([]atom.MediaAsset(nil), a.MediaAssets...),
		difficulty:        a.Difficulty,
		title:             a.Title,
		body:              a.Body,
		tags:              append([]string(nil), a.Tags...),
		mode:              a.Mode,
	}
}

// changedMetadataFields names the fields the patch actually moved, in a fixed
// canonical order (Phase 1 fields first, then the legacy ApplyUpdate four).
// A field counts only when the request SUPPLIED it and the aggregate's value
// differs afterwards, so a same-value patch yields an empty list and the
// caller emits nothing.
//
// The comparison reads the POST-application value rather than the raw request
// value, which is what makes a whitespace-only edit (the handler trims stem /
// subject / title / body) correctly register as no change.
func changedMetadataFields(req patchAtomRequest, before atomMetadataSnapshot, a *atom.LearningAtom) []string {
	var changed []string
	add := func(supplied, differs bool, name string) {
		if supplied && differs {
			changed = append(changed, name)
		}
	}

	add(req.Stem != nil, a.Stem != before.stem, "stem")
	add(req.Subject != nil, a.Subject != before.subject, "subject")
	add(req.AuthorNote != nil, a.AuthorNote != before.authorNote, "author_note")
	add(req.CognitiveLevel != nil, a.CognitiveLevel != before.cognitiveLevel, "cognitive_level")
	add(req.ImdaDimensionTags != nil,
		!imdaTagsEqual(before.imdaDimensionTags, a.ImdaDimensionTags), "imda_dimension_tags")
	add(req.MediaAssets != nil,
		!mediaAssetsEqual(before.mediaAssets, a.MediaAssets), "media_assets")
	add(req.Difficulty != nil, a.Difficulty != before.difficulty, "difficulty")
	add(req.Title != nil, a.Title != before.title, "title")
	add(req.Body != nil, a.Body != before.body, "body")
	add(req.Tags != nil, !stringsEqual(before.tags, a.Tags), "tags")
	add(req.Mode != nil, a.Mode != before.mode, "mode")

	return changed
}

// stringsEqual reports element-wise equality; order is significant because tag
// order is author-chosen and rides the wire as given.
func stringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func imdaTagsEqual(a, b []atom.ImdaDimTag) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// mediaAssetsEqual compares by value. MediaAsset holds no pointers, so struct
// equality is a full comparison.
func mediaAssetsEqual(a, b []atom.MediaAsset) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
