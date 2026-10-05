// http_pure_helpers_test.go — direct unit tests (package httpadapter) for the
// pure, unexported request-parsing / translation / error-mapping helpers spread
// across the http adapter. These are pure functions with no IO, so they are
// unit-tested directly rather than through a full router round-trip.
package httpadapter

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-common/auth/servicemesh"
	"github.com/apollo-chora/chora-creation/internal/domain/collection"
	"github.com/apollo-chora/chora-creation/internal/domain/questionbank"
	"github.com/apollo-chora/chora-creation/internal/domain/topic"
)

func TestTranslateQuestionTypesToAtomTypes(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty input is nil", nil, nil},
		{"whitespace-only input is nil", []string{"  "}, nil},
		{"mixed mapped + unknown drops unknown", []string{"MCQ", "reserved_zzz", "video"}, []string{"mcq", "video"}},
		{"lowercases and trims", []string{" MCQ ", "oe"}, []string{"mcq", "essay"}},
		{"all unknown yields nil", []string{"bogus"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := translateQuestionTypesToAtomTypes(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("translateQuestionTypesToAtomTypes(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestTranslateStatesToAtomStatus(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty is nil", nil, nil},
		{"known mapped", []string{"DRAFT", "published", "archived"}, []string{"draft", "published", "archived"}},
		{"unknown dropped", []string{"draft", "frozen"}, []string{"draft"}},
		{"all unknown nil", []string{"void"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := translateStatesToAtomStatus(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("translateStatesToAtomStatus(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseBoolTrue(t *testing.T) {
	yes := []string{"1", "true", "TRUE", "yes", "on", "  on  "}
	for _, s := range yes {
		if !parseBoolTrue(s) {
			t.Errorf("parseBoolTrue(%q) = false, want true", s)
		}
	}
	no := []string{"", "false", "0", "no", "off", "garbage"}
	for _, s := range no {
		if parseBoolTrue(s) {
			t.Errorf("parseBoolTrue(%q) = true, want false", s)
		}
	}
}

func TestParseCommaSeparated(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , b ,c", []string{"a", "b", "c"}},
		{",,a,", []string{"a"}},
	}
	for _, tc := range cases {
		if got := parseCommaSeparated(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseCommaSeparated(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParsePositiveInt(t *testing.T) {
	cases := []struct {
		in       string
		fallback int
		want     int
	}{
		{"", 5, 5},
		{"  ", 7, 7},
		{"42", 5, 42},
		{"0", 5, 0},
		{"-3", 5, 5},       // negative invalid
		{"1.5", 5, 5},      // non-digit
		{"abc", 5, 5},      // non-digit
		{"99999999", 5, 5}, // overflow cap
		{"10", 5, 10},
	}
	for _, tc := range cases {
		if got := parsePositiveInt(tc.in, tc.fallback); got != tc.want {
			t.Errorf("parsePositiveInt(%q,%d) = %d, want %d", tc.in, tc.fallback, got, tc.want)
		}
	}
}

func TestHasAuthorRole(t *testing.T) {
	cases := []struct {
		name  string
		roles string
		want  bool
	}{
		{"absent header is fail-closed", "", false},
		{"learner no", "learner", false},
		{"author yes", "author", true},
		{"instructor yes", "instructor", true},
		{"mixed includes author", "learner,author,observer", true},
		{"case-insensitive", "AUTHOR", true},
		{"spaces trimmed", " learner , author ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/atoms/x", nil)
			if tc.roles != "" {
				r.Header.Set(servicemesh.HeaderUserRoles, tc.roles)
			}
			if got := hasAuthorRole(r); got != tc.want {
				t.Errorf("hasAuthorRole(roles=%q) = %v, want %v", tc.roles, got, tc.want)
			}
		})
	}
}

func TestParseItemSort(t *testing.T) {
	cases := []struct {
		in       string
		wantKey  string
		wantDesc bool
	}{
		{"", "", false},
		{"position", "position", false},
		{"added_at:desc", "added_at", true},
		{"added_at:asc", "added_at", false},
		{"prompt:DESC", "prompt", true},
		{"bogus:desc", "", false},
		{"bogus", "", false},
		{"position:sideways", "position", false},
	}
	for _, tc := range cases {
		key, desc := parseItemSort(tc.in)
		if key != tc.wantKey || desc != tc.wantDesc {
			t.Errorf("parseItemSort(%q) = (%q,%v), want (%q,%v)", tc.in, key, desc, tc.wantKey, tc.wantDesc)
		}
	}
}

func TestQbAtoiDefault(t *testing.T) {
	if got := qbAtoiDefault("42", 1); got != 42 {
		t.Errorf("qbAtoiDefault valid = %d, want 42", got)
	}
	if got := qbAtoiDefault("abc", 7); got != 7 {
		t.Errorf("qbAtoiDefault invalid = %d, want 7", got)
	}
	if got := qbAtoiDefault("", 9); got != 9 {
		t.Errorf("qbAtoiDefault empty = %d, want 9", got)
	}
}

func TestParseItemPageFilter(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v1/question-banks/x/questions?q=hello&page=2&page_size=5&question_type=mcq&question_type=oe&sort=added_at:desc", nil)
	f, page, pageSize := parseItemPageFilter(r)
	if f.Query != "hello" {
		t.Errorf("Query = %q, want hello", f.Query)
	}
	if len(f.Types) != 2 || f.Types[0] != "mcq" || f.Types[1] != "oe" {
		t.Errorf("Types = %v, want [mcq oe]", f.Types)
	}
	if f.SortKey != "added_at" || !f.SortDesc {
		t.Errorf("Sort = %q/%v, want added_at/true", f.SortKey, f.SortDesc)
	}
	if f.Limit != 5 || f.Offset != 5 {
		t.Errorf("Limit/Offset = %d/%d, want 5/5", f.Limit, f.Offset)
	}
	if page != 2 || pageSize != 5 {
		t.Errorf("page/pageSize = %d/%d, want 2/5", page, pageSize)
	}

	// Defaults + clamping.
	r2 := httptest.NewRequest("GET", "/api/v1/question-banks/x/questions?page=0&page_size=9999", nil)
	f2, page2, pageSize2 := parseItemPageFilter(r2)
	if page2 != 1 {
		t.Errorf("page default = %d, want 1", page2)
	}
	if pageSize2 != maxQuestionsPageSize {
		t.Errorf("page_size clamp = %d, want %d", pageSize2, maxQuestionsPageSize)
	}
	if f2.Limit != maxQuestionsPageSize || f2.Offset != 0 {
		t.Errorf("Limit/Offset default = %d/%d", f2.Limit, f2.Offset)
	}
}

func TestCallerIsTopicAdmin(t *testing.T) {
	cases := []struct {
		roles string
		want  bool
	}{
		{"", false},
		{"learner", false},
		{"admin", true},
		{"owner", true},
		{"learner,owner", true},
		{"ADMIN", true},
		{"adminish", false}, // anchored token, not substring
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/api/v1/topics", nil)
		if c.roles != "" {
			r.Header.Set(servicemesh.HeaderUserRoles, c.roles)
		}
		if got := callerIsTopicAdmin(r); got != c.want {
			t.Errorf("callerIsTopicAdmin(%q) = %v, want %v", c.roles, got, c.want)
		}
	}
}

// TestWriteQuestionBankError_Mapping pins every domain sentinel → status+code
// translation so the error surface stays stable.
func TestWriteQuestionBankError_Mapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{questionbank.ErrNotFound, 404, "CREATION_QUESTION_BANK_NOT_FOUND"},
		{questionbank.ErrForbidden, 403, "CREATION_QUESTION_BANK_FORBIDDEN"},
		{questionbank.ErrQuestionDoesNotExist, 404, "CREATION_QUESTION_NOT_FOUND"},
		{questionbank.ErrDuplicateQuestion, 409, "CREATION_QUESTION_BANK_DUPLICATE_QUESTION"},
		{questionbank.ErrItemCapExceeded, 422, "CREATION_QUESTION_BANK_CAP_EXCEEDED"},
		{questionbank.ErrQuestionNotInBank, 404, "CREATION_QUESTION_BANK_QUESTION_NOT_FOUND"},
		{questionbank.ErrReorderMismatch, 400, "CREATION_QUESTION_BANK_REORDER_MISMATCH"},
		{questionbank.ErrQuestionBankDeleted, 410, "CREATION_QUESTION_BANK_DELETED"},
		{questionbank.ErrTitleRequired, 400, "CREATION_QUESTION_BANK_INVALID"},
		{questionbank.ErrEmptyBank, 422, "CREATION_QUESTION_BANK_EMPTY"},
		{questionbank.ErrAssemblerNotWired, 503, "CREATION_TESTSET_PUBLISH_NOT_WIRED"},
		{questionbank.ErrInvalid, 400, "CREATION_QUESTION_BANK_INVALID"},
		{questionbank.ErrGateNotWired, 500, "CREATION_QUESTION_BANK_NOT_WIRED"},
		{questionbank.ErrRepository, 500, "CREATION_QUESTION_BANK_REPO_ERROR"},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		writeQuestionBankError(w, tc.err, "test")
		if w.Code != tc.status {
			t.Errorf("err=%v status=%d, want %d", tc.err, w.Code, tc.status)
		}
		if body := w.Body.String(); !strings.Contains(body, tc.code) {
			t.Errorf("err=%v body missing code %q: %s", tc.err, tc.code, body)
		}
	}
	// Unclassified → 500 CREATION_INTERNAL.
	w := httptest.NewRecorder()
	writeQuestionBankError(w, &unkEr{}, "test")
	if w.Code != 500 {
		t.Errorf("unclassified status=%d, want 500", w.Code)
	}
}

type unkEr struct{}

func (*unkEr) Error() string { return "mystery" }

// TestWriteCollectionError_Mapping pins every collection domain sentinel →
// status+code translation.
func TestWriteCollectionError_Mapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{collection.ErrNotFound, 404, "CREATION_COLLECTION_NOT_FOUND"},
		{collection.ErrForbidden, 403, "CREATION_COLLECTION_FORBIDDEN"},
		{collection.ErrAtomDoesNotExist, 404, "CREATION_ATOM_NOT_FOUND"},
		{collection.ErrCrossTenantAtomNotPermitted, 403, "CREATION_COLLECTION_CROSS_TENANT_FORBIDDEN"},
		{collection.ErrDuplicateAtom, 409, "CREATION_COLLECTION_DUPLICATE_ATOM"},
		{collection.ErrAtomCapExceeded, 422, "CREATION_COLLECTION_ATOM_CAP_EXCEEDED"},
		{collection.ErrAtomNotInCollection, 404, "CREATION_COLLECTION_ATOM_NOT_FOUND"},
		{collection.ErrCollectionDeleted, 410, "CREATION_COLLECTION_DELETED"},
		{collection.ErrNoEntitledAtoms, 409, "CREATION_COLLECTION_NO_ENTITLED_ATOMS"},
		{collection.ErrAtomNotReusable, 403, "CREATION_ATOM_NOT_REUSABLE"},
		{collection.ErrAtomNotShareable, 409, "CREATION_ATOM_NOT_SHAREABLE"},
		{collection.ErrAtomReuseDenied, 403, "CREATION_ATOM_REUSE_DENIED"},
		{collection.ErrSharingUnavailable, 502, "CREATION_SHARING_UNAVAILABLE"},
		{collection.ErrSharingRejectedRequest, 502, "CREATION_SHARING_GATE_ERROR"},
		{collection.ErrInvalid, 400, "CREATION_COLLECTION_INVALID"},
		{collection.ErrGateNotWired, 500, "CREATION_GATE_NOT_WIRED"},
		{collection.ErrRepository, 500, "CREATION_COLLECTION_REPO_ERROR"},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		writeCollectionError(w, tc.err, "test")
		if w.Code != tc.status {
			t.Errorf("collection err=%v status=%d, want %d", tc.err, w.Code, tc.status)
		}
		if !strings.Contains(w.Body.String(), tc.code) {
			t.Errorf("collection err=%v body missing %q", tc.err, tc.code)
		}
	}
	// Unclassified → 500 CREATION_INTERNAL.
	w := httptest.NewRecorder()
	writeCollectionError(w, &unkEr{}, "test")
	if w.Code != 500 {
		t.Errorf("collection unclassified status=%d, want 500", w.Code)
	}
}

// TestWriteTopicError_Mapping pins every topic domain sentinel → status+code.
func TestWriteTopicError_Mapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{topic.ErrNotFound, 404, "CREATION_TOPIC_NOT_FOUND"},
		{topic.ErrCycle, 409, "CREATION_TOPIC_CYCLE"},
		{topic.ErrHasChildren, 409, "CREATION_TOPIC_HAS_CHILDREN"},
		{topic.ErrNameRequired, 400, "CREATION_TOPIC_INVALID"},
		{topic.ErrNameTooLong, 400, "CREATION_TOPIC_INVALID"},
		{topic.ErrInvalidParent, 400, "CREATION_TOPIC_INVALID"},
		{topic.ErrInvalidSortOrder, 400, "CREATION_TOPIC_INVALID"},
		{topic.ErrInvalidAtomID, 400, "CREATION_TOPIC_INVALID"},
		{topic.ErrTenantRequired, 400, "CREATION_TOPIC_INVALID"},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		writeTopicError(w, tc.err)
		if w.Code != tc.status {
			t.Errorf("topic err=%v status=%d, want %d", tc.err, w.Code, tc.status)
		}
		if !strings.Contains(w.Body.String(), tc.code) {
			t.Errorf("topic err=%v body missing %q", tc.err, tc.code)
		}
	}
	// Unclassified → 500 CREATION_TOPIC_ERROR.
	w := httptest.NewRecorder()
	writeTopicError(w, &unkEr{})
	if w.Code != 500 {
		t.Errorf("topic unclassified status=%d, want 500", w.Code)
	}
}

// TestToTopicDTO verifies the learner-safe wire projection.
func TestToTopicDTO(t *testing.T) {
	id := "11111111-1111-7111-8111-111111111111"
	parent := "22222222-2222-7222-8222-222222222222"
	dto := toTopicDTO(&topic.TopicNode{TopicID: id, ParentID: &parent, Name: "Algebra", SortOrder: 3})
	if dto.ID != id || dto.ParentID == nil || *dto.ParentID != parent {
		t.Errorf("DTO id/parent mismatch: %+v", dto)
	}
	if dto.Name != "Algebra" || dto.SortOrder != 3 {
		t.Errorf("DTO name/order mismatch: %+v", dto)
	}
	// Nil parent → nil pointer preserved.
	dto2 := toTopicDTO(&topic.TopicNode{TopicID: id, Name: "Root"})
	if dto2.ParentID != nil {
		t.Errorf("nil parent should stay nil, got %v", dto2.ParentID)
	}
}
