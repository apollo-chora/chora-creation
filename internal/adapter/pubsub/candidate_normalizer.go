// candidate_normalizer.go — shared mapper from the qgen 2-agent crew's
// verbatim `candidate_payload_json` wire shape onto the canonical
// candidateDraft persisted in question_generation_jobs.candidate_questions_jsonb.
//
// Why this exists (risk-register #1, HIGHEST)
// -------------------------------------------
// The orchestrator (services/chora-ai-kernel-orchestrator) publishes the
// terminal candidate VERBATIM — `CandidatePayload.payload_json` is whatever
// the qgen_question reasoning engine emitted, after the executor's
// `_map_response` partially-unwraps the `{scored:{candidate:...}}` envelope.
// Across deployed engine versions + the below-threshold placeholder path the
// candidate JSON arrives in one of several shapes:
//
//  1. flat              {stem, options[...], question_type:"mcq"}
//  2. {candidate:{...}} (executor did NOT unwrap)
//  3. {scored:{candidate:{...}, factuality, clarity, composite}}
//     (raw evaluator envelope — composer_question.go StepEvaluation3)
//  4. options nested:   candidate.mcq_payload.options  (OpenAPI-aligned shape)
//  5. OE flat:          {stem, model_answer, question_type:"oe"}
//  6. OE nested:        candidate.oe_payload.model_answer (new OE contract)
//
// A naive store-as-is would pass a Pub/Sub smoke but BREAK the FE accept flow:
// question_jobs_handler.acceptQuestionJob reads candidateDraft.Type +
// .MCQPayload / .OEPayload, then feeds them to question.New(...) which REQUIRES
// exactly-one-correct MCQ options each with a non-empty explainer (or a
// non-empty OE model_answer). This normalizer maps every wire shape onto the
// validated domain payload + fills review-placeholder explainers so the
// author's accept doesn't bounce on a blank-explainer invariant.
//
// Mapping intent ported from the (deleted) clients/qgen_question_client.go
// `mcqFromEvalCandidate` (commit 63835cdb) — option_id minting + explainer
// placeholder fill preserved verbatim.
//
// Hexagonal: depends only on the question domain (no infra imports beyond
// stdlib JSON + uuid).
package pubsub

import (
	"encoding/json"
	"fmt"
	mathrand "math/rand/v2"
	"strings"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

// explainerReviewPlaceholder fills a blank LLM explainer so the FE accept's
// question.New invariant (per-option explainer mandatory) doesn't bounce an
// otherwise-valid AI draft. Mirrors the deleted mcqFromEvalCandidate fallback.
const explainerReviewPlaceholder = "AI-generated explainer; review before publishing"

// wireCandidate is the loose decode target. Every shape variant funnels into
// this struct; presence of the nested wrappers is resolved in
// normalizeCandidatePayload before reading the leaf fields.
type wireCandidate struct {
	// DraftID — Lane 1c cross-agent contract: the W2 orchestrator stamps a
	// DETERMINISTIC draft_id (uuid5 of "{assist_id}:{i}") on every batch
	// candidate and keys proposed_test_set.order[]/points{} by it. The
	// normalizer MUST preserve it verbatim (mint only when absent) or the
	// proposal's keys orphan and the FE pre-fill breaks.
	DraftID      string          `json:"draft_id"`
	Stem         string          `json:"stem"`
	QuestionType string          `json:"question_type"`
	Intent       string          `json:"intent"`
	Options      []wireMCQOption `json:"options"`
	MCQPayload   *struct {
		Options []wireMCQOption `json:"options"`
	} `json:"mcq_payload"`
	ModelAnswer string `json:"model_answer"`
	// Citations — Lane 1c (D9/D15): model-reported {source_file, page,
	// excerpt} grounding refs; the verification pass stamps verified +
	// chunk_id creation-side before persisting. Preserved verbatim here.
	Citations []questionCitation `json:"citations"`
	// ImageSpecs — CHO-1819 P2/P3: per-candidate image generation specs the crew
	// emitted. Decoded as raw bytes + carried through to candidateDraft verbatim
	// so the review-stage regenerate path can read them; the normalizer never
	// inspects or mutates them.
	ImageSpecs json.RawMessage `json:"image_specs"`
	// ImageURL / AnswerImageURL — CHO-1825: the orchestrator's render_image_set
	// node sets these TOP-LEVEL on the candidate (stem image → image_url; model-
	// answer image → answer_image_url) after a successful kroki/gateway render +
	// GCS upload. They are the canonical draft-candidate image location (matching
	// the single-candidate AiAssistCandidate + the FE's top-level read). Dropping
	// them here silently discards a rendered+stored image — the bug CHO-1825
	// surfaced once forced images reliably render. Absent ⇒ no image (optional).
	ImageURL       string `json:"image_url"`
	AnswerImageURL string `json:"answer_image_url"`
	// ImageGcsURI / AnswerImageGcsURI — ADR-210 B2: the orchestrator ALSO stamps
	// the canonical durable gs:// OBJECT path alongside the signed image_url so
	// the regenerate producer reads the object directly (never parses the signed
	// URL back into a gs://). Absent ⇒ pre-B2 producer (regen → text-to-image).
	ImageGcsURI       string `json:"image_gcs_uri"`
	AnswerImageGcsURI string `json:"answer_image_gcs_uri"`
	// wireOEPayload mirrors the qgen `outputNewOE` shape (composer_question.go)
	// + OpenAPI AiAssistCandidate.oe_payload: model_answer + a weighted rubric
	// (each {criterion_id, title, description, weight∈[0,1]}) + grader_tier +
	// optional min/max response-char guidance. Decoding ONLY model_answer (the
	// pre-§4.2 shape) silently dropped the rubric/grader_tier on the batch path.
	OEPayload *wireOEPayload `json:"oe_payload"`
}

// wireOEPayload is the nested OE answer envelope carried under candidate.
// oe_payload. Field names + the float-weight convention are IDENTICAL to the
// proven AI-Assist save path (questions_handler.go createOEPayload /
// createRubricCriterion) so the batch path produces the same domain payload.
type wireOEPayload struct {
	ModelAnswer      string                `json:"model_answer"`
	Rubric           []wireRubricCriterion `json:"rubric"`
	GraderTier       *string               `json:"grader_tier"`
	MinResponseChars *int                  `json:"min_response_chars"`
	MaxResponseChars *int                  `json:"max_response_chars"`
}

// wireRubricCriterion mirrors OpenAPI RubricCriterion: {criterion_id, title
// (required), description (optional), weight (0..1 float)}. Domain stores
// Description (collapsed from title) + integer WeightPercent — see
// oeRubricFromWire.
type wireRubricCriterion struct {
	CriterionID string  `json:"criterion_id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Weight      float64 `json:"weight"`
}

type wireMCQOption struct {
	OptionID  string `json:"option_id"`
	Label     string `json:"label"`
	IsCorrect bool   `json:"is_correct"`
	Explainer string `json:"explainer"`
}

// questionCitation mirrors OpenAPI QuestionCitation (Lane 1c). The crew
// reports {source_file, page, excerpt}; chora-creation stamps {verified,
// chunk_id} (D15 — verification runs where the chunks live; the
// orchestrator cannot read chora_creation). source_file is the full gs://
// blob_uri (the orchestrator resolves bare filenames before emitting).
type questionCitation struct {
	SourceFile string  `json:"source_file"`
	Page       *int    `json:"page,omitempty"`
	Excerpt    string  `json:"excerpt"`
	ChunkID    *string `json:"chunk_id,omitempty"`
	Verified   *bool   `json:"verified,omitempty"`
}

// scoreEnvelope captures the sibling score axes when the candidate arrives
// in the raw {scored:{candidate, composite, ...}} shape so the qgen_score can
// be surfaced on the draft for the FE review UI.
type scoreEnvelope struct {
	Composite float64 `json:"composite"`
}

// normalizeCandidatePayload converts the verbatim candidate_payload_json wire
// bytes into a candidateDraft ready for question_generation_jobs.candidate_
// questions_jsonb. hintType is the job's question_type (mcq/oe) when known;
// pass "" to discriminate purely from the payload shape (options ⇒ mcq,
// model_answer ⇒ oe).
//
// Returns an error (fail-loud) when:
//   - bytes are empty / malformed JSON
//   - the resolved candidate has no usable content (no MCQ options AND no OE
//     model_answer) — e.g. a below-threshold refused placeholder. The caller
//     routes that to a failed job rather than a broken success.
func normalizeCandidatePayload(raw []byte, hintType question.QuestionType) (candidateDraft, error) {
	if len(raw) == 0 {
		return candidateDraft{}, fmt.Errorf("candidate normalize: empty payload bytes")
	}

	// Decode generically first so we can peel the wrapper layers regardless
	// of which variant the engine emitted.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return candidateDraft{}, fmt.Errorf("candidate normalize: unmarshal envelope: %w", err)
	}

	candBytes, score := unwrapCandidate(raw, top)

	var wc wireCandidate
	if err := json.Unmarshal(candBytes, &wc); err != nil {
		return candidateDraft{}, fmt.Errorf("candidate normalize: unmarshal candidate: %w", err)
	}

	// Resolve options placement (candidate.options OR candidate.mcq_payload.options).
	opts := wc.Options
	if len(opts) == 0 && wc.MCQPayload != nil {
		opts = wc.MCQPayload.Options
	}
	// Resolve model_answer placement (flat OR oe_payload.model_answer).
	modelAnswer := strings.TrimSpace(wc.ModelAnswer)
	if modelAnswer == "" && wc.OEPayload != nil {
		modelAnswer = strings.TrimSpace(wc.OEPayload.ModelAnswer)
	}

	qType := resolveQuestionType(hintType, wc.QuestionType, len(opts) > 0, modelAnswer != "")

	// Lane 1c cross-agent contract: preserve the orchestrator's
	// deterministic draft_id when present (the composer proposal's
	// order[]/points{} key on it); mint only when absent (legacy emitters).
	draftID := strings.TrimSpace(wc.DraftID)
	if draftID == "" {
		draftID = uuid.Must(uuid.NewV7()).String()
	}

	draft := candidateDraft{
		DraftID:    draftID,
		Type:       string(qType),
		Prompt:     wc.Stem,
		QGenScore:  score,
		Citations:  wc.Citations,
		ImageSpecs: wc.ImageSpecs,
	}
	// CHO-1825 — carry the orchestrator's top-level rendered image URL(s) onto
	// the draft (canonical location; the FE + accept read them top-level). Set
	// only when present so an imageless candidate keeps the omitempty absence.
	if s := strings.TrimSpace(wc.ImageURL); s != "" {
		draft.ImageURL = &s
	}
	if s := strings.TrimSpace(wc.AnswerImageURL); s != "" {
		draft.AnswerImageURL = &s
	}
	// ADR-210 — carry the durable gs:// object path through to the stored draft
	// so the regenerate producer can source the image-to-image original.
	if s := strings.TrimSpace(wc.ImageGcsURI); s != "" {
		draft.ImageGcsURI = &s
	}
	if s := strings.TrimSpace(wc.AnswerImageGcsURI); s != "" {
		draft.AnswerImageGcsURI = &s
	}

	switch qType {
	case question.TypeMCQ:
		if len(opts) == 0 {
			return candidateDraft{}, fmt.Errorf("candidate normalize: mcq candidate has no options (refused/empty placeholder)")
		}
		draft.MCQPayload = mcqPayloadFromWire(opts)
	case question.TypeOpenEnded:
		if modelAnswer == "" {
			return candidateDraft{}, fmt.Errorf("candidate normalize: oe candidate has no model_answer (refused/empty placeholder)")
		}
		oe := &question.OEPayload{ModelAnswer: modelAnswer}
		if wc.OEPayload != nil {
			oe.GraderTier = wc.OEPayload.GraderTier
			oe.MinResponseChars = wc.OEPayload.MinResponseChars
			oe.MaxResponseChars = wc.OEPayload.MaxResponseChars
			oe.WeightedRubric = oeRubricFromWire(wc.OEPayload.Rubric)
		}
		draft.OEPayload = oe
	default:
		return candidateDraft{}, fmt.Errorf("candidate normalize: cannot resolve question_type (hint=%q wire=%q options=%d model_answer=%t)",
			hintType, wc.QuestionType, len(opts), modelAnswer != "")
	}

	return draft, nil
}

// unwrapCandidate peels the {scored:{candidate}} and {candidate} wrappers,
// returning the raw bytes of the leaf candidate object + the composite score
// (0 when not present). A flat candidate (no wrapper) returns the input bytes.
func unwrapCandidate(raw []byte, top map[string]json.RawMessage) ([]byte, float64) {
	// Shape 3: {scored:{candidate:{...}, composite, ...}}
	if scoredRaw, ok := top["scored"]; ok && len(scoredRaw) > 0 && string(scoredRaw) != "null" {
		var scoredMap map[string]json.RawMessage
		if err := json.Unmarshal(scoredRaw, &scoredMap); err == nil {
			var score float64
			var env scoreEnvelope
			if json.Unmarshal(scoredRaw, &env) == nil {
				score = env.Composite
			}
			if candRaw, ok := scoredMap["candidate"]; ok && len(candRaw) > 0 {
				return candRaw, score
			}
		}
	}
	// Shape 2: {candidate:{...}}
	if candRaw, ok := top["candidate"]; ok && len(candRaw) > 0 {
		return candRaw, 0
	}
	// Shape 1: flat candidate. The executor's _map_response may have stashed
	// the score axes under "_scored" — surface the composite if present.
	var score float64
	if sRaw, ok := top["_scored"]; ok && len(sRaw) > 0 {
		var env scoreEnvelope
		if json.Unmarshal(sRaw, &env) == nil {
			score = env.Composite
		}
	}
	return raw, score
}

// resolveQuestionType picks mcq/oe from (in priority order): the job hint, the
// candidate's own question_type field, then structural discrimination (options
// ⇒ mcq, model_answer ⇒ oe).
func resolveQuestionType(hint question.QuestionType, wire string, hasOptions, hasModelAnswer bool) question.QuestionType {
	if hint == question.TypeMCQ || hint == question.TypeOpenEnded {
		return hint
	}
	switch question.QuestionType(strings.ToLower(strings.TrimSpace(wire))) {
	case question.TypeMCQ:
		return question.TypeMCQ
	case question.TypeOpenEnded:
		return question.TypeOpenEnded
	}
	// Structural discrimination — options win over model_answer when both
	// somehow present (an MCQ shape is the stricter, more-specific match).
	if hasOptions {
		return question.TypeMCQ
	}
	if hasModelAnswer {
		return question.TypeOpenEnded
	}
	return ""
}

// mcqPayloadFromWire maps wire options onto question.MCQOption, minting a
// UUIDv7 for any blank option_id + filling a review placeholder for any blank
// explainer (FE accept's question.New requires a per-option explainer on
// correct AND distractor options). Ported from the deleted
// mcqFromEvalCandidate (commit 63835cdb).
// oeRubricFromWire maps the wire rubric onto question.Rubric, mirroring the
// proven AI-Assist save path (questions_handler.go buildPayloadFromRequest):
//   - blank criterion_id is minted (UUIDv7)
//   - Description collapses title + description with TITLE taking priority
//     (domain has no separate Title field yet — ATOM-1 Phase 1)
//   - wire `weight` (0..1 float) → integer WeightPercent (0..100), round-to-nearest
//
// Returns nil for an empty/absent rubric so the byte-stable omitempty absence is
// preserved in the oe_payload JSONB (an OE question may legitimately have no
// rubric).
func oeRubricFromWire(in []wireRubricCriterion) *question.Rubric {
	if len(in) == 0 {
		return nil
	}
	weights := make([]float64, len(in))
	for i, c := range in {
		weights[i] = c.Weight
	}
	// Largest-remainder so the integer percents sum to EXACTLY 100 (the domain
	// Validate invariant) regardless of float rounding — shared with the proven
	// AI-Assist save path.
	pcts := question.NormalizeWeightPercents(weights)

	crits := make([]question.RubricCriterion, len(in))
	for i, c := range in {
		cid := strings.TrimSpace(c.CriterionID)
		if cid == "" {
			cid = uuid.Must(uuid.NewV7()).String()
		}
		desc := strings.TrimSpace(c.Title)
		if desc == "" {
			desc = c.Description
		}
		crits[i] = question.RubricCriterion{
			CriterionID:   cid,
			Description:   desc,
			WeightPercent: pcts[i],
		}
	}
	return &question.Rubric{Criteria: crits}
}

func mcqPayloadFromWire(in []wireMCQOption) *question.MCQPayload {
	out := make([]question.MCQOption, 0, len(in))
	for _, o := range in {
		explainer := o.Explainer
		if strings.TrimSpace(explainer) == "" {
			explainer = explainerReviewPlaceholder
		}
		out = append(out, question.MCQOption{
			// OptionID is deliberately NOT carried over from the wire — see
			// MintOptionIDs below.
			Label:     o.Label,
			IsCorrect: o.IsCorrect,
			Explainer: explainer,
		})
	}
	payload := &question.MCQPayload{Options: out}
	// CHO-2255 (SECURITY) — mint every option id, ALWAYS. This path previously
	// honoured the crew's own option_id and minted only when it was blank; the
	// crew names the key (`opt_correct` + `opt_distractor_1..3` are live in this
	// database, 7 of 187 MCQs) and the learner-safe projection ships the id
	// verbatim. Shuffling below fixes POSITION and cannot fix a NAME.
	payload.MintOptionIDs(uuid.NewString)
	// Randomise option order once at creation so the correct answer is not
	// always stored first (the composer emits it first). Grading is
	// option_id-based, so this is safe. (Measured 2026-07-17: post-shuffle
	// ai_assist rows sit at 31.6% correct-first vs 25% chance — this works.)
	payload.ShuffleOptions(mathrand.Shuffle)
	return payload
}

// normalizeBatchCandidates maps a batch completed.v1 candidate_payload_json
// into N candidateDrafts + the optional composer proposal (Lane 1c D4).
//
// Two wire shapes per the OpenAPI BatchCandidatePayload back-compat rule
// (first non-space char discriminates):
//
//	`[` — legacy bare ARRAY of AiAssistCandidate (no proposal; the live
//	      shape until the orchestrator ships QGEN_TESTSET_COMPOSE_ENABLED).
//	`{` — BatchCandidatePayload object {candidates:[...],
//	      proposed_test_set:{...}} — candidates normalize identically; the
//	      raw proposal JSON is returned for proposed_test_set_jsonb.
//
// A single bare candidate object (no `candidates` key) is tolerated as a
// 1-element batch so a non-batch orchestrator emission still yields one
// atom. Any un-normalizable element fails the WHOLE batch (the
// no-partial-success rule) so the job refunds rather than persisting a
// broken candidate alongside good ones.
func normalizeBatchCandidates(raw []byte) ([]candidateDraft, json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, nil, fmt.Errorf("batch candidate normalize: empty payload bytes")
	}

	var (
		arr      []json.RawMessage
		proposal json.RawMessage
	)
	switch trimmed[0] {
	case '[':
		if err := json.Unmarshal([]byte(trimmed), &arr); err != nil {
			return nil, nil, fmt.Errorf("batch candidate normalize: unmarshal array: %w", err)
		}
	case '{':
		var envelope struct {
			Candidates      []json.RawMessage `json:"candidates"`
			ProposedTestSet json.RawMessage   `json:"proposed_test_set"`
		}
		if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
			return nil, nil, fmt.Errorf("batch candidate normalize: unmarshal object: %w", err)
		}
		if envelope.Candidates != nil {
			arr = envelope.Candidates
			if len(envelope.ProposedTestSet) > 0 && string(envelope.ProposedTestSet) != "null" {
				proposal = envelope.ProposedTestSet
			}
		} else {
			// No `candidates` key — tolerate a single bare candidate object
			// as a 1-element batch (pre-1c orchestrator emission).
			arr = []json.RawMessage{json.RawMessage(trimmed)}
		}
	default:
		return nil, nil, fmt.Errorf("batch candidate normalize: payload is neither array nor object")
	}

	if len(arr) == 0 {
		return nil, nil, fmt.Errorf("batch candidate normalize: empty candidate array")
	}
	drafts := make([]candidateDraft, 0, len(arr))
	for i, el := range arr {
		d, err := normalizeCandidatePayload([]byte(el), "")
		if err != nil {
			return nil, nil, fmt.Errorf("batch candidate normalize: candidate %d: %w", i, err)
		}
		drafts = append(drafts, d)
	}
	return drafts, proposal, nil
}

// looksLikeBatch reports whether the verbatim candidate_payload_json is a
// multi-candidate SET shape — a bare JSON array `[...]` OR a `{candidates:[...]}`
// envelope — versus a single candidate object.
//
// ADR-195 WS8 BE-1: a count>1 ai_draft is dispatched through the set lane
// (job_kind=batch + type_plan), yet the job row stays type=ai_draft, so the
// orchestrator returns a batch shape that MUST route to normalizeBatchCandidates
// even though the job's Input is not source files. Without this the single
// normalizer hits the `{candidates:...}` envelope's missing candidate fields and
// fails "cannot resolve question_type" (the live count=5 → 0-candidate bug).
//
// A single candidate object (count=1 ai_draft, including the `{candidate:{...}}`
// and `{scored:{candidate:...}}` wrappers — note the SINGULAR key) returns false
// and stays on the single-candidate path, byte-identical to pre-fix behaviour.
func looksLikeBatch(candidatePayloadJSON string) bool {
	trimmed := strings.TrimSpace(candidatePayloadJSON)
	if trimmed == "" {
		return false
	}
	switch trimmed[0] {
	case '[':
		return true
	case '{':
		var probe struct {
			Candidates json.RawMessage `json:"candidates"`
		}
		if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
			return false
		}
		return len(probe.Candidates) > 0 && string(probe.Candidates) != "null"
	default:
		return false
	}
}
