// question_types_handler.go — static registry endpoint for the 16-value
// QuestionType enum (P7 — FE Phase H blocker).
//
// Path : GET /api/atoms/question-types
// Auth : ChoraSession bearer (handled by the auth interceptor upstream;
//
//	this handler has no per-tenant data).
//
// Spec : chora-contracts/openapi/creation-questions.yaml#getQuestionTypes
//
// The registry mirrors the QuestionType enum:
//   - 2 enabled (mcq, oe) with scope=phyllis
//   - 14 reserved_* (alphabetical) with enabled=false and scope=reserved
//
// Alphabetical order on the reserved_* segment is part of the contract
// (creation-questions.yaml line 642).
//
// Per `feedback_no_inline_config`, the values themselves are part of the
// API contract — they are not deployment configuration. The registry is
// therefore a compile-time literal, not env-driven.
package httpadapter

import (
	"net/http"
)

// questionTypeEntry is the wire shape from
// creation-questions.yaml#QuestionTypeOption.
//
// `label_zh` is intentionally always emitted (as `null`) per FE
// contract (atom-authoring.model.ts L179 — `readonly label_zh?: string |
// null`). Coordinator course-correction 2026-05-15 made the canonical
// shape explicit. Use a pointer + omit-NEVER so the field marshals as
// `"label_zh": null` rather than being elided.
type questionTypeEntry struct {
	Code    string  `json:"code"`
	LabelEn string  `json:"label_en"`
	LabelZh *string `json:"label_zh"` // nullable, always emitted
	Enabled bool    `json:"enabled"`
	Scope   string  `json:"scope"`
}

// questionTypeEnvelope is the GET envelope.
type questionTypeEnvelope struct {
	Items []questionTypeEntry `json:"items"`
}

// questionTypeRegistry holds the canonical 16-row registry.
//
// Order matters and matches the QuestionType enum in
// creation-questions.yaml#QuestionType (lines 643-659):
//   - mcq, oe   (the two phyllis-scope enabled types)
//   - then 14 reserved_* values in alphabetical order
//
// label_en text is locked to design §1 / FE atom-authoring.model.ts where
// available; future locales (label_zh, etc.) are intentionally absent
// from the v1 wire — the FE renders fallbacks.
// Canonical label_en strings locked by coordinator course-correction
// 2026-05-15. Match exactly; do NOT change capitalisation.
var questionTypeRegistry = []questionTypeEntry{
	{Code: "mcq", LabelEn: "Multiple Choice", LabelZh: nil, Enabled: true, Scope: "phyllis"},
	{Code: "oe", LabelEn: "Open-Ended", LabelZh: nil, Enabled: true, Scope: "phyllis"},
	{Code: "reserved_code_execution", LabelEn: "Code execution", LabelZh: nil, Enabled: false, Scope: "reserved"},
	{Code: "reserved_completion", LabelEn: "Completion", LabelZh: nil, Enabled: false, Scope: "reserved"},
	{Code: "reserved_drag_drop", LabelEn: "Drag & drop", LabelZh: nil, Enabled: false, Scope: "reserved"},
	{Code: "reserved_fill_blank", LabelEn: "Fill in the blank", LabelZh: nil, Enabled: false, Scope: "reserved"},
	{Code: "reserved_matching", LabelEn: "Matching", LabelZh: nil, Enabled: false, Scope: "reserved"},
	{Code: "reserved_multi_select", LabelEn: "Multi-select", LabelZh: nil, Enabled: false, Scope: "reserved"},
	{Code: "reserved_multimedia", LabelEn: "Multimedia", LabelZh: nil, Enabled: false, Scope: "reserved"},
	{Code: "reserved_oral", LabelEn: "Oral", LabelZh: nil, Enabled: false, Scope: "reserved"},
	{Code: "reserved_ordering", LabelEn: "Ordering", LabelZh: nil, Enabled: false, Scope: "reserved"},
	{Code: "reserved_peer_graded", LabelEn: "Peer-graded", LabelZh: nil, Enabled: false, Scope: "reserved"},
	{Code: "reserved_short_answer", LabelEn: "Short Answer", LabelZh: nil, Enabled: false, Scope: "reserved"},
	{Code: "reserved_simulation", LabelEn: "Simulation", LabelZh: nil, Enabled: false, Scope: "reserved"},
	{Code: "reserved_table_completion", LabelEn: "Table completion", LabelZh: nil, Enabled: false, Scope: "reserved"},
	{Code: "reserved_true_false", LabelEn: "True / False", LabelZh: nil, Enabled: false, Scope: "reserved"},
}

// GetQuestionTypes serves the static registry envelope.
//
// MethodNotAllowed on anything except GET. The handler does NOT touch
// any tenant-scoped data — the registry is the same for every caller —
// but it remains behind the ChoraSession bearer per the OpenAPI spec
// (handled by the auth interceptor; this handler does not inspect the
// JWT).
func (h *AtomHandler) GetQuestionTypes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			"only GET is supported on /api/atoms/question-types")
		return
	}
	writeJSON(w, http.StatusOK, questionTypeEnvelope{Items: questionTypeRegistry})
}
