// candidate_normalizer_test.go — TDD coverage for normalizeCandidatePayload,
// the shared mapper that converts the qgen 2-agent crew's verbatim
// `candidate_payload_json` (3 wrapper shapes + 2 options-placement variants
// + OE flat/nested) into the canonical candidateDraft persisted in
// question_generation_jobs.candidate_questions_jsonb.
//
// This is risk-register item #1 (HIGHEST): a naive store-as-is passes smoke
// but breaks the FE accept flow (question_jobs_handler.acceptQuestionJob reads
// d.MCQPayload / d.OEPayload off candidateDraft). The matrix below exercises
// every wire shape the orchestrator can emit:
//
//   - flat MCQ     {stem, options[...], question_type:"mcq"}
//   - {candidate}  {candidate:{stem, options[...]}}
//   - {scored}     {scored:{candidate:{stem, options[...]}}}
//   - mcq_payload  {candidate:{stem, mcq_payload:{options[...]}}}
//   - flat OE      {stem, model_answer, question_type:"oe"}
//   - nested OE    {candidate:{stem, oe_payload:{model_answer}}}
//   - discriminator-by-presence (no question_type → options ⇒ mcq; model_answer ⇒ oe)
//   - empty / refused-placeholder candidate (no stem, no options)
package pubsub

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

func TestNormalizeCandidate_FlatMCQ(t *testing.T) {
	raw := []byte(`{
		"stem": "What is 2+2?",
		"options": [
			{"option_id": "o1", "label": "4", "is_correct": true, "explainer": "Correct."},
			{"option_id": "o2", "label": "5", "is_correct": false, "explainer": "Off by one."}
		],
		"intent": "new_question",
		"question_type": "mcq"
	}`)
	d, err := normalizeCandidatePayload(raw, question.TypeMCQ)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if d.Type != string(question.TypeMCQ) {
		t.Errorf("Type = %q; want mcq", d.Type)
	}
	if d.Prompt != "What is 2+2?" {
		t.Errorf("Prompt = %q", d.Prompt)
	}
	if d.MCQPayload == nil {
		t.Fatal("MCQPayload nil")
	}
	if len(d.MCQPayload.Options) != 2 {
		t.Fatalf("options = %d; want 2", len(d.MCQPayload.Options))
	}
	// mcqPayloadFromWire shuffles option order (mathrand) AND mints fresh
	// option ids (CHO-2255 — the crew's id names the key), so neither the slice
	// position nor the incoming id is a usable handle. Assert by LABEL: the only
	// stable, caller-meaningful identity left.
	byLabel := map[string]question.MCQOption{}
	for _, opt := range d.MCQPayload.Options {
		byLabel[opt.Label] = opt
	}
	if o := byLabel["4"]; !o.IsCorrect || o.Explainer != "Correct." {
		t.Errorf("option labelled 4 = %+v; want {correct explainer:Correct.}", o)
	}
	if o := byLabel["5"]; o.IsCorrect {
		t.Errorf("option labelled 5 = %+v; want a distractor", o)
	}
	// CHO-2255: the wire ids must NOT survive.
	for _, opt := range d.MCQPayload.Options {
		if opt.OptionID == "o1" || opt.OptionID == "o2" {
			t.Errorf("the crew-supplied option id %q survived normalisation", opt.OptionID)
		}
		if opt.OptionID == "" {
			t.Errorf("option labelled %q has no id", opt.Label)
		}
	}
	if d.OEPayload != nil {
		t.Error("OEPayload should be nil for MCQ")
	}
	if d.DraftID == "" {
		t.Error("DraftID should be minted")
	}
}

func TestNormalizeCandidate_CandidateWrapperMCQ(t *testing.T) {
	raw := []byte(`{
		"candidate": {
			"stem": "Pick the prime.",
			"options": [
				{"option_id": "a", "label": "7", "is_correct": true, "explainer": "7 is prime."},
				{"option_id": "b", "label": "8", "is_correct": false, "explainer": "8 = 2^3."}
			],
			"question_type": "mcq"
		}
	}`)
	d, err := normalizeCandidatePayload(raw, question.TypeMCQ)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if d.MCQPayload == nil || len(d.MCQPayload.Options) != 2 {
		t.Fatalf("MCQPayload not unwrapped: %+v", d.MCQPayload)
	}
	if d.Prompt != "Pick the prime." {
		t.Errorf("Prompt = %q", d.Prompt)
	}
}

func TestNormalizeCandidate_ScoredCandidateWrapperMCQ(t *testing.T) {
	raw := []byte(`{
		"scored": {
			"candidate": {
				"stem": "Capital of France?",
				"options": [
					{"option_id": "1", "label": "Paris", "is_correct": true, "explainer": "Yes."},
					{"option_id": "2", "label": "Lyon", "is_correct": false, "explainer": "No."}
				]
			},
			"factuality": 0.9,
			"clarity": 0.95,
			"composite": 0.92
		}
	}`)
	d, err := normalizeCandidatePayload(raw, question.TypeMCQ)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if d.MCQPayload == nil || len(d.MCQPayload.Options) != 2 {
		t.Fatalf("MCQPayload not unwrapped from scored.candidate: %+v", d.MCQPayload)
	}
	if d.Prompt != "Capital of France?" {
		t.Errorf("Prompt = %q", d.Prompt)
	}
	if d.QGenScore != 0.92 {
		t.Errorf("QGenScore = %v; want 0.92 (composite)", d.QGenScore)
	}
}

func TestNormalizeCandidate_MCQPayloadOptionsPlacement(t *testing.T) {
	// Options nested under candidate.mcq_payload.options rather than
	// candidate.options.
	raw := []byte(`{
		"candidate": {
			"stem": "Which is H2O?",
			"mcq_payload": {
				"options": [
					{"option_id": "x", "label": "Water", "is_correct": true, "explainer": "H2O."},
					{"option_id": "y", "label": "Salt", "is_correct": false, "explainer": "NaCl."}
				]
			},
			"question_type": "mcq"
		}
	}`)
	d, err := normalizeCandidatePayload(raw, question.TypeMCQ)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if d.MCQPayload == nil || len(d.MCQPayload.Options) != 2 {
		t.Fatalf("options not read from mcq_payload.options: %+v", d.MCQPayload)
	}
	// Order is shuffled AND ids are minted (CHO-2255) — assert the option SET by
	// label, the only stable handle.
	got := map[string]bool{}
	for _, opt := range d.MCQPayload.Options {
		got[opt.Label] = opt.IsCorrect
		if opt.OptionID == "x" || opt.OptionID == "y" {
			t.Errorf("the crew-supplied option id %q survived normalisation", opt.OptionID)
		}
	}
	if !got["Water"] || got["Salt"] {
		t.Errorf("options read from mcq_payload.options = %v; want Water=correct Salt=distractor", got)
	}
}

func TestNormalizeCandidate_PreservesImageSpecs(t *testing.T) {
	// image_specs ride inside the candidate JSON (P3 review-stage regenerate
	// reads them); the normalizer must NOT strip them off candidateDraft.
	raw := []byte(`{
		"stem": "What gas do plants absorb?",
		"question_type": "mcq",
		"options": [
			{"option_id":"o1","label":"CO2","is_correct":true,"explainer":"yes"},
			{"option_id":"o2","label":"O2","is_correct":false,"explainer":"no"}
		],
		"image_specs": [{"slot":"stem","prompt":"a diagram of photosynthesis","status":"generated","image_url":"gs://b/k"}]
	}`)
	d, err := normalizeCandidatePayload(raw, question.TypeMCQ)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(d.ImageSpecs) == 0 {
		t.Fatal("image_specs were dropped; want preserved verbatim for P3 regenerate")
	}
	if !bytes.Contains(d.ImageSpecs, []byte("a diagram of photosynthesis")) {
		t.Errorf("image_specs mangled: %s", d.ImageSpecs)
	}
	// Survives the json.Marshal that persists candidate_questions_jsonb.
	blob, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal draft: %v", err)
	}
	if !bytes.Contains(blob, []byte("image_specs")) {
		t.Errorf("marshalled draft lost the image_specs key: %s", blob)
	}
}

func TestNormalizeCandidate_NoImageSpecs_OmitsField(t *testing.T) {
	raw := []byte(`{"stem":"q","question_type":"oe","model_answer":"a sufficiently long model answer for the open-ended path"}`)
	d, err := normalizeCandidatePayload(raw, question.TypeOpenEnded)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(d.ImageSpecs) != 0 {
		t.Errorf("image_specs should be empty when absent, got %s", d.ImageSpecs)
	}
	blob, _ := json.Marshal(d)
	if bytes.Contains(blob, []byte("image_specs")) {
		t.Errorf("absent image_specs must be omitted (omitempty): %s", blob)
	}
}

func TestNormalizeCandidate_CarriesTopLevelImageURLs(t *testing.T) {
	// CHO-1825 — the render_image_set node sets image_url / answer_image_url
	// TOP-LEVEL on the candidate after a successful render+upload. The normalizer
	// MUST carry them onto candidateDraft (the FE + accept read them top-level);
	// dropping them silently discards a rendered+stored image.
	raw := []byte(`{
		"stem": "Order the frog life cycle stages.",
		"question_type": "mcq",
		"options": [
			{"option_id":"o1","label":"egg→tadpole→froglet→frog","is_correct":true,"explainer":"yes"},
			{"option_id":"o2","label":"frog→egg→tadpole→froglet","is_correct":false,"explainer":"no"}
		],
		"image_url": "https://signed.example/stem.png",
		"answer_image_url": "https://signed.example/answer.png",
		"image_gcs_uri": "gs://chora-atom-media-dev/tenants/t/atoms/a/stem.png",
		"answer_image_gcs_uri": "gs://chora-atom-media-dev/tenants/t/atoms/a/ans.png"
	}`)
	d, err := normalizeCandidatePayload(raw, question.TypeMCQ)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if d.ImageURL == nil || *d.ImageURL != "https://signed.example/stem.png" {
		t.Errorf("top-level image_url dropped: %v", d.ImageURL)
	}
	if d.AnswerImageURL == nil || *d.AnswerImageURL != "https://signed.example/answer.png" {
		t.Errorf("top-level answer_image_url dropped: %v", d.AnswerImageURL)
	}
	// ADR-210 B3 — the durable gs:// object path (B2 stamp) must ALSO survive to
	// the draft so the regenerate producer can source the image-to-image
	// original directly (never parsing the signed URL).
	if d.ImageGcsURI == nil || *d.ImageGcsURI != "gs://chora-atom-media-dev/tenants/t/atoms/a/stem.png" {
		t.Errorf("top-level image_gcs_uri dropped: %v", d.ImageGcsURI)
	}
	if d.AnswerImageGcsURI == nil || *d.AnswerImageGcsURI != "gs://chora-atom-media-dev/tenants/t/atoms/a/ans.png" {
		t.Errorf("top-level answer_image_gcs_uri dropped: %v", d.AnswerImageGcsURI)
	}
	// Survives the json.Marshal that persists candidate_questions_jsonb (FE reads
	// it top-level off the candidate).
	blob, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal draft: %v", err)
	}
	if !bytes.Contains(blob, []byte(`"image_url":"https://signed.example/stem.png"`)) {
		t.Errorf("marshalled draft lost top-level image_url: %s", blob)
	}
	if !bytes.Contains(blob, []byte(`"image_gcs_uri":"gs://chora-atom-media-dev/tenants/t/atoms/a/stem.png"`)) {
		t.Errorf("marshalled draft lost top-level image_gcs_uri: %s", blob)
	}
}

func TestNormalizeCandidate_NoImageURLs_OmitsFields(t *testing.T) {
	raw := []byte(`{"stem":"q","question_type":"oe","model_answer":"a sufficiently long model answer for the open-ended path"}`)
	d, err := normalizeCandidatePayload(raw, question.TypeOpenEnded)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if d.ImageURL != nil || d.AnswerImageURL != nil {
		t.Errorf("image urls should be nil when absent: %v / %v", d.ImageURL, d.AnswerImageURL)
	}
	blob, _ := json.Marshal(d)
	if bytes.Contains(blob, []byte("image_url")) {
		t.Errorf("absent image urls must be omitted (omitempty): %s", blob)
	}
}

func TestNormalizeCandidate_FlatOE(t *testing.T) {
	raw := []byte(`{
		"stem": "Explain gravity.",
		"model_answer": "Gravity is the attractive force between masses, described by general relativity as the curvature of spacetime.",
		"question_type": "oe",
		"intent": "new_question"
	}`)
	d, err := normalizeCandidatePayload(raw, question.TypeOpenEnded)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if d.Type != string(question.TypeOpenEnded) {
		t.Errorf("Type = %q; want oe", d.Type)
	}
	if d.OEPayload == nil {
		t.Fatal("OEPayload nil")
	}
	if d.OEPayload.ModelAnswer == "" {
		t.Error("ModelAnswer empty")
	}
	if d.MCQPayload != nil {
		t.Error("MCQPayload should be nil for OE")
	}
	if d.Prompt != "Explain gravity." {
		t.Errorf("Prompt = %q", d.Prompt)
	}
}

func TestNormalizeCandidate_NestedOEPayload(t *testing.T) {
	raw := []byte(`{
		"candidate": {
			"stem": "Describe photosynthesis.",
			"question_type": "oe",
			"oe_payload": {
				"model_answer": "Photosynthesis converts light energy into chemical energy stored in glucose using chlorophyll in the chloroplasts of plant cells.",
				"grader_tier": "T2"
			}
		}
	}`)
	d, err := normalizeCandidatePayload(raw, question.TypeOpenEnded)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if d.OEPayload == nil || d.OEPayload.ModelAnswer == "" {
		t.Fatalf("OEPayload model_answer not read from oe_payload: %+v", d.OEPayload)
	}
}

func TestNormalizeCandidate_DiscriminatorByPresence_MCQ(t *testing.T) {
	// No question_type field + hint unspecified → options present ⇒ mcq.
	raw := []byte(`{
		"stem": "Which?",
		"options": [
			{"option_id": "1", "label": "A", "is_correct": true, "explainer": "e"},
			{"option_id": "2", "label": "B", "is_correct": false, "explainer": "e"}
		]
	}`)
	d, err := normalizeCandidatePayload(raw, "")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if d.Type != string(question.TypeMCQ) {
		t.Errorf("Type = %q; want mcq (options present)", d.Type)
	}
	if d.MCQPayload == nil {
		t.Error("MCQPayload nil")
	}
}

func TestNormalizeCandidate_DiscriminatorByPresence_OE(t *testing.T) {
	// No question_type field + hint unspecified → model_answer present ⇒ oe.
	raw := []byte(`{
		"stem": "Discuss.",
		"model_answer": "A long-form answer demonstrating depth across at least a couple of sentences here."
	}`)
	d, err := normalizeCandidatePayload(raw, "")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if d.Type != string(question.TypeOpenEnded) {
		t.Errorf("Type = %q; want oe (model_answer present)", d.Type)
	}
	if d.OEPayload == nil {
		t.Error("OEPayload nil")
	}
}

func TestNormalizeCandidate_EmptyOptionID_Minted(t *testing.T) {
	raw := []byte(`{
		"stem": "Q",
		"question_type": "mcq",
		"options": [
			{"label": "A", "is_correct": true, "explainer": "e"},
			{"label": "B", "is_correct": false, "explainer": "e"}
		]
	}`)
	d, err := normalizeCandidatePayload(raw, question.TypeMCQ)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	for i, o := range d.MCQPayload.Options {
		if o.OptionID == "" {
			t.Errorf("option[%d].OptionID empty; should be minted", i)
		}
	}
}

func TestNormalizeCandidate_EmptyExplainer_Filled(t *testing.T) {
	// FE accept calls question.New which REQUIRES a non-empty explainer on
	// every option. A blank LLM explainer would block accept — normalizer
	// fills a review placeholder (mirrors deleted mcqFromEvalCandidate).
	raw := []byte(`{
		"stem": "Q",
		"question_type": "mcq",
		"options": [
			{"option_id": "1", "label": "A", "is_correct": true, "explainer": ""},
			{"option_id": "2", "label": "B", "is_correct": false}
		]
	}`)
	d, err := normalizeCandidatePayload(raw, question.TypeMCQ)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	for i, o := range d.MCQPayload.Options {
		if o.Explainer == "" {
			t.Errorf("option[%d].Explainer empty; should be filled with review placeholder", i)
		}
	}
}

// TestNormalizeCandidate_OE_RubricGraderTierSurvive locks the §4.2 fix: the
// batch question_generation_jobs path MUST carry the OE rubric + grader_tier +
// min/max_response_chars off the wire `oe_payload` (the qgen outputNewOE shape)
// into the persisted candidateDraft.OEPayload. Before the fix, wireCandidate.
// OEPayload decoded ONLY model_answer, so json.Unmarshal silently discarded the
// rubric — the author's accept then minted an OE question with NO rubric.
//
// Wire→domain mapping MUST mirror the proven AI-Assist save path
// (questions_handler.go buildPayloadFromRequest): title→Description (collapse,
// title priority), weight(0..1 float)*100 round → WeightPercent(int), mint a
// criterion_id when blank. Domain Rubric weights are integer percents summing
// to 100 (question/oe.go).
func TestNormalizeCandidate_OE_RubricGraderTierSurvive(t *testing.T) {
	raw := []byte(`{
		"candidate": {
			"stem": "Explain how photosynthesis converts light energy into chemical energy.",
			"question_type": "oe",
			"intent": "new_question",
			"oe_payload": {
				"model_answer": "Photosynthesis converts light energy into chemical energy stored in glucose. Chlorophyll in the thylakoid membranes absorbs photons, splitting water and generating ATP and NADPH; the Calvin cycle fixes carbon dioxide into sugar.",
				"rubric": [
					{"criterion_id": "c1", "title": "Light-dependent reactions", "description": "Identifies chlorophyll absorbing photons and water splitting.", "weight": 0.4},
					{"criterion_id": "c2", "title": "Energy carriers", "description": "Mentions ATP and NADPH outputs.", "weight": 0.3},
					{"criterion_id": "c3", "title": "Calvin cycle", "description": "Describes carbon fixation.", "weight": 0.3}
				],
				"grader_tier": "T2",
				"min_response_chars": 80,
				"max_response_chars": 600
			}
		}
	}`)
	d, err := normalizeCandidatePayload(raw, question.TypeOpenEnded)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if d.OEPayload == nil {
		t.Fatal("OEPayload nil")
	}
	// Rubric survives with 3 criteria.
	if d.OEPayload.WeightedRubric == nil {
		t.Fatal("WeightedRubric nil — rubric was dropped (the §4.2 bug)")
	}
	if got := len(d.OEPayload.WeightedRubric.Criteria); got != 3 {
		t.Fatalf("rubric criteria count = %d; want 3", got)
	}
	// title→Description (title priority), weight*100→WeightPercent, id preserved.
	c0 := d.OEPayload.WeightedRubric.Criteria[0]
	if c0.CriterionID != "c1" {
		t.Errorf("criteria[0].CriterionID = %q; want c1", c0.CriterionID)
	}
	if c0.Description != "Light-dependent reactions" {
		t.Errorf("criteria[0].Description = %q; want the title (collapse)", c0.Description)
	}
	if c0.WeightPercent != 40 {
		t.Errorf("criteria[0].WeightPercent = %d; want 40 (0.4*100)", c0.WeightPercent)
	}
	// Domain invariant: integer percents sum to 100 (so Validate passes at accept).
	sum := 0
	for _, c := range d.OEPayload.WeightedRubric.Criteria {
		sum += c.WeightPercent
	}
	if sum != 100 {
		t.Errorf("rubric WeightPercent sum = %d; want 100", sum)
	}
	// grader_tier + min/max survive.
	if d.OEPayload.GraderTier == nil || *d.OEPayload.GraderTier != "T2" {
		t.Errorf("GraderTier = %v; want T2", d.OEPayload.GraderTier)
	}
	if d.OEPayload.MinResponseChars == nil || *d.OEPayload.MinResponseChars != 80 {
		t.Errorf("MinResponseChars = %v; want 80", d.OEPayload.MinResponseChars)
	}
	if d.OEPayload.MaxResponseChars == nil || *d.OEPayload.MaxResponseChars != 600 {
		t.Errorf("MaxResponseChars = %v; want 600", d.OEPayload.MaxResponseChars)
	}
	// The full payload must pass domain validation (what question.New does at accept).
	if err := d.OEPayload.Validate(); err != nil {
		t.Errorf("normalized OE payload fails domain Validate: %v", err)
	}
}

// TestNormalizeCandidate_OE_BlankCriterionIDMinted locks that a blank
// criterion_id is minted (mirrors mcqPayloadFromWire option_id minting +
// questions_handler.go). A non-empty description fallback covers title-absent.
func TestNormalizeCandidate_OE_BlankCriterionIDMinted(t *testing.T) {
	raw := []byte(`{
		"candidate": {
			"stem": "Discuss supply and demand.",
			"question_type": "oe",
			"oe_payload": {
				"model_answer": "Supply and demand jointly set the market-clearing price where the quantity supplied equals the quantity demanded, shifting with changes in either curve.",
				"rubric": [
					{"criterion_id": "", "description": "Defines equilibrium price.", "weight": 0.5},
					{"criterion_id": "", "description": "Explains curve shifts.", "weight": 0.5}
				]
			}
		}
	}`)
	d, err := normalizeCandidatePayload(raw, question.TypeOpenEnded)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if d.OEPayload == nil || d.OEPayload.WeightedRubric == nil {
		t.Fatal("OE rubric dropped")
	}
	for i, c := range d.OEPayload.WeightedRubric.Criteria {
		if c.CriterionID == "" {
			t.Errorf("criteria[%d].CriterionID empty; should be minted", i)
		}
		// description-only fallback (no title) maps onto Description.
		if c.Description == "" {
			t.Errorf("criteria[%d].Description empty; should fall back to wire description", i)
		}
	}
}

// TestNormalizeCandidate_OE_PathologicalWeightsSumTo100 is the regression for
// the adversarial-review finding: repeating-decimal weights ({1/3,1/3,1/3})
// must still yield integer percents summing to EXACTLY 100 (else the normalized
// payload fails domain Validate at accept). Largest-remainder guarantees it.
func TestNormalizeCandidate_OE_PathologicalWeightsSumTo100(t *testing.T) {
	raw := []byte(`{
		"candidate": {
			"stem": "Discuss the causes of WWI.",
			"question_type": "oe",
			"oe_payload": {
				"model_answer": "The war's causes were militarism, alliances, imperialism, and nationalism, ignited by the assassination of Archduke Franz Ferdinand.",
				"rubric": [
					{"criterion_id": "c1", "title": "Militarism", "weight": 0.3333333},
					{"criterion_id": "c2", "title": "Alliances", "weight": 0.3333333},
					{"criterion_id": "c3", "title": "Imperialism", "weight": 0.3333334}
				]
			}
		}
	}`)
	d, err := normalizeCandidatePayload(raw, question.TypeOpenEnded)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if d.OEPayload == nil || d.OEPayload.WeightedRubric == nil {
		t.Fatal("OE rubric dropped")
	}
	sum := 0
	for _, c := range d.OEPayload.WeightedRubric.Criteria {
		sum += c.WeightPercent
	}
	if sum != 100 {
		t.Errorf("repeating-decimal rubric WeightPercent sum = %d; want 100", sum)
	}
	// And it must pass the domain validation that question.New runs at accept.
	if err := d.OEPayload.Validate(); err != nil {
		t.Errorf("normalized OE payload fails Validate: %v", err)
	}
}

func TestNormalizeCandidate_EmptyCandidate_Errors(t *testing.T) {
	// Refused-placeholder / below-threshold candidate: no stem, no options,
	// no model_answer. There is nothing to persist — fail loud so the
	// terminal subscriber routes this to a failed job, not a broken success.
	raw := []byte(`{"stem": "", "question_type": "mcq"}`)
	if _, err := normalizeCandidatePayload(raw, question.TypeMCQ); err == nil {
		t.Fatal("expected error on empty MCQ candidate (no options)")
	}
}

func TestNormalizeCandidate_EmptyBytes_Errors(t *testing.T) {
	if _, err := normalizeCandidatePayload(nil, question.TypeMCQ); err == nil {
		t.Fatal("expected error on nil candidate bytes")
	}
	if _, err := normalizeCandidatePayload([]byte(""), question.TypeMCQ); err == nil {
		t.Fatal("expected error on empty candidate bytes")
	}
}

func TestNormalizeCandidate_MalformedJSON_Errors(t *testing.T) {
	if _, err := normalizeCandidatePayload([]byte(`{not json`), question.TypeMCQ); err == nil {
		t.Fatal("expected error on malformed JSON")
	}
}

func TestNormalizeCandidate_FlatWithScoredSibling(t *testing.T) {
	// The executor's _map_response unwraps {scored:{candidate}} into a flat
	// candidate + stashes the score axes under "_scored". Surface composite.
	raw := []byte(`{
		"stem": "Flat with score?",
		"options": [
			{"option_id": "1", "label": "A", "is_correct": true, "explainer": "e"},
			{"option_id": "2", "label": "B", "is_correct": false, "explainer": "e"}
		],
		"question_type": "mcq",
		"_scored": {"factuality": 0.8, "composite": 0.77}
	}`)
	d, err := normalizeCandidatePayload(raw, question.TypeMCQ)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if d.QGenScore != 0.77 {
		t.Errorf("QGenScore = %v; want 0.77 (from _scored.composite)", d.QGenScore)
	}
}

func TestNormalizeCandidate_UnresolvableType_Errors(t *testing.T) {
	// No hint, no question_type field, no options, no model_answer → cannot
	// resolve. Fail loud.
	raw := []byte(`{"stem": "orphan", "intent": "new_question"}`)
	if _, err := normalizeCandidatePayload(raw, ""); err == nil {
		t.Fatal("expected error on unresolvable question_type")
	}
}

func TestNormalizeCandidate_ScoredNull_FallsThroughToFlat(t *testing.T) {
	// Below-threshold placeholder: {"scored": null, ...}. The candidate has no
	// usable content at the top level either → error (refused placeholder).
	raw := []byte(`{"scored": null, "reason": "below_threshold", "question_type": "mcq"}`)
	if _, err := normalizeCandidatePayload(raw, question.TypeMCQ); err == nil {
		t.Fatal("expected error on scored:null below-threshold placeholder")
	}
}

func TestNormalizeCandidate_QualityWarningCandidate_StillSucceeds(t *testing.T) {
	// A best-effort last candidate (quality_warning=true) is a real,
	// structurally-complete candidate — it must NOT be dropped.
	raw := []byte(`{
		"candidate": {
			"stem": "Borderline question?",
			"options": [
				{"option_id": "1", "label": "Maybe", "is_correct": true, "explainer": "ok"},
				{"option_id": "2", "label": "Nope", "is_correct": false, "explainer": "ok"}
			],
			"question_type": "mcq"
		}
	}`)
	d, err := normalizeCandidatePayload(raw, question.TypeMCQ)
	if err != nil {
		t.Fatalf("quality-warning candidate must still normalize: %v", err)
	}
	if d.MCQPayload == nil || len(d.MCQPayload.Options) != 2 {
		t.Fatal("quality-warning candidate dropped")
	}
}
