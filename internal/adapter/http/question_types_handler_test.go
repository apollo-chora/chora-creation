// question_types_handler_test.go — RED→GREEN coverage for the
// /api/atoms/question-types static registry endpoint (P7 — FE Phase H
// blocker).
//
// Contract: chora-contracts/openapi/creation-questions.yaml#getQuestionTypes
// Envelope shape:
//
//	{ "items": [ { "code": "...", "label_en": "...", "enabled": bool,
//	               "scope": "phyllis"|"reserved" }, ... ] }
//
// Per the OpenAPI: scope is one of {phyllis, reserved} and alphabetical
// ordering on the reserved_* segment is part of the contract.
package httpadapter_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// -----------------------------------------------------------------------------
// Wire shape (must match QuestionTypeOption schema in creation-questions.yaml).
// -----------------------------------------------------------------------------

type questionTypeEnvelope struct {
	Items []questionTypeEntry `json:"items"`
}

type questionTypeEntry struct {
	Code    string  `json:"code"`
	LabelEn string  `json:"label_en"`
	LabelZh *string `json:"label_zh"` // nullable, always emitted
	Enabled bool    `json:"enabled"`
	Scope   string  `json:"scope"`
}

// -----------------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------------

func TestQuestionTypes_Returns200_WithSixteenEntries(t *testing.T) {
	t.Parallel()

	srv := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/api/atoms/question-types", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (body=%s)", w.Code, w.Body.String())
	}

	var got questionTypeEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v (body=%s)", err, w.Body.String())
	}
	if len(got.Items) != 16 {
		t.Fatalf("len(items) = %d; want 16", len(got.Items))
	}
}

func TestQuestionTypes_FirstTwoAreEnabled_MCQAndOE(t *testing.T) {
	t.Parallel()

	srv := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/api/atoms/question-types", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	var got questionTypeEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if got.Items[0].Code != "mcq" {
		t.Errorf("items[0].code = %q; want mcq", got.Items[0].Code)
	}
	if !got.Items[0].Enabled {
		t.Errorf("items[0] (mcq) must have enabled=true")
	}
	if got.Items[0].Scope != "phyllis" {
		t.Errorf("items[0].scope = %q; want phyllis", got.Items[0].Scope)
	}
	if got.Items[0].LabelEn == "" {
		t.Errorf("items[0].label_en must not be empty")
	}

	if got.Items[1].Code != "oe" {
		t.Errorf("items[1].code = %q; want oe", got.Items[1].Code)
	}
	if !got.Items[1].Enabled {
		t.Errorf("items[1] (oe) must have enabled=true")
	}
	if got.Items[1].Scope != "phyllis" {
		t.Errorf("items[1].scope = %q; want phyllis", got.Items[1].Scope)
	}
}

func TestQuestionTypes_ReservedItemsDisabled_AndAlphabetical(t *testing.T) {
	t.Parallel()

	srv := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/api/atoms/question-types", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	var got questionTypeEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	// items[2:] must all be reserved_* + enabled=false + scope=reserved + alphabetical.
	reserved := got.Items[2:]
	if len(reserved) != 14 {
		t.Fatalf("expected 14 reserved items; got %d", len(reserved))
	}
	prev := ""
	for i, it := range reserved {
		if it.Enabled {
			t.Errorf("reserved[%d] code=%q must have enabled=false", i, it.Code)
		}
		if it.Scope != "reserved" {
			t.Errorf("reserved[%d] code=%q scope=%q; want reserved", i, it.Code, it.Scope)
		}
		if len(it.Code) < len("reserved_") || it.Code[:len("reserved_")] != "reserved_" {
			t.Errorf("reserved[%d] code=%q must start with reserved_", i, it.Code)
		}
		if it.LabelEn == "" {
			t.Errorf("reserved[%d] code=%q label_en must not be empty", i, it.Code)
		}
		// Alphabetical contract — see creation-questions.yaml QuestionType description.
		if prev != "" && it.Code < prev {
			t.Errorf("reserved[%d] code=%q out of alphabetical order (prev=%q)", i, it.Code, prev)
		}
		prev = it.Code
	}
}

func TestQuestionTypes_AllSixteenContractCodesPresent(t *testing.T) {
	t.Parallel()

	srv := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/api/atoms/question-types", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	var got questionTypeEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	// Source-of-truth: creation-questions.yaml#QuestionType enum (lines 643-659).
	want := []string{
		"mcq", "oe",
		"reserved_code_execution", "reserved_completion", "reserved_drag_drop",
		"reserved_fill_blank", "reserved_matching", "reserved_multi_select",
		"reserved_multimedia", "reserved_oral", "reserved_ordering",
		"reserved_peer_graded", "reserved_short_answer", "reserved_simulation",
		"reserved_table_completion", "reserved_true_false",
	}
	if len(got.Items) != len(want) {
		t.Fatalf("len(items)=%d want=%d", len(got.Items), len(want))
	}
	for i, code := range want {
		if got.Items[i].Code != code {
			t.Errorf("items[%d].code = %q; want %q", i, got.Items[i].Code, code)
		}
	}
}

// TestQuestionTypes_CanonicalLabels locks the coordinator-canonical
// label_en strings (course-correction 2026-05-15). Any drift = test
// failure → FE sees stale labels.
func TestQuestionTypes_CanonicalLabels(t *testing.T) {
	t.Parallel()

	srv := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/api/atoms/question-types", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	var got questionTypeEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	wantLabels := map[string]string{
		"mcq":                       "Multiple Choice",
		"oe":                        "Open-Ended",
		"reserved_code_execution":   "Code execution",
		"reserved_completion":       "Completion",
		"reserved_drag_drop":        "Drag & drop",
		"reserved_fill_blank":       "Fill in the blank",
		"reserved_matching":         "Matching",
		"reserved_multi_select":     "Multi-select",
		"reserved_multimedia":       "Multimedia",
		"reserved_oral":             "Oral",
		"reserved_ordering":         "Ordering",
		"reserved_peer_graded":      "Peer-graded",
		"reserved_short_answer":     "Short Answer",
		"reserved_simulation":       "Simulation",
		"reserved_table_completion": "Table completion",
		"reserved_true_false":       "True / False",
	}
	for _, it := range got.Items {
		want, ok := wantLabels[it.Code]
		if !ok {
			t.Errorf("unexpected code %q in items", it.Code)
			continue
		}
		if it.LabelEn != want {
			t.Errorf("code=%q label_en = %q; want %q", it.Code, it.LabelEn, want)
		}
	}
}

// TestQuestionTypes_LabelZhAlwaysEmittedAsNull verifies the wire payload
// includes `"label_zh": null` for every entry (NOT omitted). FE binds
// `readonly label_zh?: string | null` and treats the missing field
// differently from null.
func TestQuestionTypes_LabelZhAlwaysEmittedAsNull(t *testing.T) {
	t.Parallel()

	srv := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodGet, "/api/atoms/question-types", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	// Quick wire-level check — every entry must contain "label_zh":null.
	body := w.Body.String()
	const want = `"label_zh":null`
	count := 0
	for i := 0; i < len(body); i++ {
		if i+len(want) <= len(body) && body[i:i+len(want)] == want {
			count++
		}
	}
	if count != 16 {
		t.Errorf("expected 16 occurrences of %q; got %d (body=%s)", want, count, body)
	}
}

func TestQuestionTypes_PostNotAllowed(t *testing.T) {
	t.Parallel()

	srv := newServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedReq(http.MethodPost, "/api/atoms/question-types", nil))

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d; want 405 (body=%s)", w.Code, w.Body.String())
	}
}
