// topic_classifier_gateway_client.go — the REAL atom topic classifier
// (CHO-2142). Implements the atom.TopicClassifier port over chora-model-gateway's
// Invoke RPC.
//
// WHY NOT THE 6-AGENT GATE'S "Classifier". That Classifier
// (chora-agent-executor/internal/domain/classifier) is DEAD: ADR-145 retired the
// executor, ADR-146 retired the chora-model-broker-classifier it fanned out to,
// its only implementation in the tree is a canned MockClassifier, and nothing by
// that name is deployed (no Cloud Run services exist; no such GKE Deployment in
// ns ai-kernel). Reviving it would resurrect two retired services AND bypass the
// chokepoint. This client keeps the ROLE and moves it onto the mandated path.
//
// CHOKEPOINT. chora-model-gateway (gRPC :9090) is the single un-bypassable LLM
// chokepoint (ADR-163/177): Cloud Model Armor screens input+output and mana meters
// CENTRALLY there, so this adapter carries no guardrail of its own. The gateway's
// policy loader returns a sane default for ANY agent_id — Vertex Gemini + the
// BALANCED Model Armor template — so this new agent_id is Armor-screened by
// construction with no gateway policy change.
//
// METERING. No action_code is sent. This is an OPERATOR remediation pass, not a
// learner action: there is no learner in the loop, and debiting the atom's author
// mana for a backfill they did not request would be wrong (mana is a learner
// quota — feedback_mana_is_quota_not_model_selector). An unmapped agent_id with no
// action_code is an un-metered passthrough at the gateway, which logs it as such.
//
// DIAL. consumption→gateway is the precedent (grounded_search_gateway_client.go):
// the app dials PLAINTEXT :9090 and the Cloud Service Mesh sidecar injects mTLS.
// A new gRPC method needs a mesh AuthorizationPolicy allow-list entry for the
// CREATION principal plus a caller restart before it will pass.
//
// Per feedback_no_inline_config the target + model come from env at the wiring
// site (CHORA_MODEL_GATEWAY_GRPC_URL); an empty value fails LOUD rather than
// degrading to a no-op classifier that would silently "succeed" with no tags.
package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	mgv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/model_gateway/v1"

	"github.com/apollo-chora/chora-creation/internal/domain/atom"
)

// ErrTopicClassifierEmptyTarget is returned when the gRPC target is empty. Per
// feedback_no_inline_config, missing config MUST fail loudly.
var ErrTopicClassifierEmptyTarget = errors.New(
	"topic_classifier_gateway_client: target empty (set CHORA_MODEL_GATEWAY_GRPC_URL)")

const (
	// topicClassifyDefaultTimeout bounds one classification round-trip. A single
	// short JSON completion — no grounding, no tool loop.
	topicClassifyDefaultTimeout = 20 * time.Second

	// topicClassifierAgentID / topicClassifierCrewKind — the calling identity
	// stamped on the invocation for gateway policy resolution, the Armor tier,
	// the token ledger, and the audit trail.
	topicClassifierAgentID  = "atom_topic_classifier"
	topicClassifierCrewKind = "content_creation"

	// topicClassifierSystemPrompt constrains the output to a bare JSON array of
	// short topic slugs. The domain re-validates and normalises whatever comes
	// back (atom.NormalizeTopicTags) — this prompt is a cooperation aid, never
	// the trust boundary.
	topicClassifierSystemPrompt = `You classify a learning question into its subject topics.

Reply with ONLY a JSON array of 2-4 short lowercase topic slugs, most specific first.
Use kebab-case. No prose, no explanation, no markdown fence.

Slugs name the SUBJECT MATTER (e.g. "fractions", "number-sense", "photosynthesis",
"linear-equations"), never the question format, difficulty, or Bloom level.

Example reply: ["fractions","number-sense"]`
)

// topicClassifierGRPC is the slice of mgv1.ModelGatewayServiceClient this adapter
// calls. The generated client satisfies it; tests inject a fake.
type topicClassifierGRPC interface {
	Invoke(ctx context.Context, in *mgv1.InvokeRequest, opts ...grpc.CallOption) (*mgv1.InvokeResponse, error)
}

// TopicClassifierGatewayClient implements atom.TopicClassifier over
// chora-model-gateway.
type TopicClassifierGatewayClient struct {
	client         topicClassifierGRPC
	logicalModelID string
	timeout        time.Duration
}

// compile-time port assertion.
var _ atom.TopicClassifier = (*TopicClassifierGatewayClient)(nil)

// NewTopicClassifierGatewayClient dials the gateway at the mesh target. An empty
// target or model id fails loud. grpc.NewClient is lazy — a healthy return does
// not prove reachability; the first RPC surfaces Unavailable, which the backfill
// records as a loud per-atom failure.
func NewTopicClassifierGatewayClient(target, logicalModelID string, timeout time.Duration) (*TopicClassifierGatewayClient, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, ErrTopicClassifierEmptyTarget
	}
	if strings.TrimSpace(logicalModelID) == "" {
		return nil, errors.New("topic_classifier_gateway_client: logical_model_id is required")
	}

	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("topic_classifier_gateway_client: dial %s: %w", target, err)
	}
	return newTopicClassifier(mgv1.NewModelGatewayServiceClient(conn), logicalModelID, timeout)
}

// NewTopicClassifierGatewayClientWithStub injects a gateway stub (tests).
func NewTopicClassifierGatewayClientWithStub(stub topicClassifierGRPC, logicalModelID string, timeout time.Duration) (*TopicClassifierGatewayClient, error) {
	if stub == nil {
		return nil, errors.New("topic_classifier_gateway_client: stub is nil")
	}
	return newTopicClassifier(stub, logicalModelID, timeout)
}

func newTopicClassifier(c topicClassifierGRPC, logicalModelID string, timeout time.Duration) (*TopicClassifierGatewayClient, error) {
	if strings.TrimSpace(logicalModelID) == "" {
		return nil, errors.New("topic_classifier_gateway_client: logical_model_id is required")
	}
	if timeout <= 0 {
		timeout = topicClassifyDefaultTimeout
	}
	return &TopicClassifierGatewayClient{client: c, logicalModelID: logicalModelID, timeout: timeout}, nil
}

// ClassifyTopics asks the gateway to classify one atom into topic slugs.
//
// Every failure mode is LOUD: a gateway error, a blank completion, an
// unparseable completion, and an empty tag array all return an error. None of
// them may degrade into "no tags", which would write the CHO-2142 defect straight
// back to source as though it were a result.
func (c *TopicClassifierGatewayClient) ClassifyTopics(ctx context.Context, req atom.ClassifyTopicsRequest) ([]string, error) {
	if c == nil || c.client == nil {
		return nil, ErrTopicClassifierEmptyTarget
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// A fresh UUIDv7 per call — the gateway's ledger dedup + audit key.
	invocationID := uuid.Must(uuid.NewV7()).String()

	resp, err := c.client.Invoke(ctx, &mgv1.InvokeRequest{
		InvocationId: invocationID,
		TenantId:     req.TenantID,
		Gcid:         req.Gcid,
		AgentId:      topicClassifierAgentID,
		CrewKind:     topicClassifierCrewKind,
		// ADR-254 D7 / ADR-252 Q1: the gateway refuses an ABSENT surface loudly
		// (FAILED_PRECONDITION surface_unstamped) before any debit. A service
		// caller stamps its owning surface; content_creation is not a companion
		// surface, so the containment read does not apply and the call passes.
		Surface:        topicClassifierCrewKind,
		LogicalModelId: c.logicalModelID,
		SystemPrompt:   topicClassifierSystemPrompt,
		Prompt:         buildTopicPrompt(req),
		Traceparent:    req.TraceParent,
		Tracestate:     req.TraceState,
	})
	if err != nil {
		return nil, fmt.Errorf("topic classify (atom=%s): gateway invoke: %w", req.AtomID, err)
	}

	completion := strings.TrimSpace(resp.GetCompletion())
	if completion == "" {
		return nil, fmt.Errorf("topic classify (atom=%s): gateway returned an empty completion", req.AtomID)
	}

	tags, err := parseTopicTags(completion)
	if err != nil {
		return nil, fmt.Errorf("topic classify (atom=%s): %w", req.AtomID, err)
	}
	return tags, nil
}

// buildTopicPrompt renders the atom's content for classification. Title + stem
// carry the topic signal; body is included when present but truncated — a long
// body adds tokens without sharpening the topic.
func buildTopicPrompt(req atom.ClassifyTopicsRequest) string {
	var b strings.Builder
	b.WriteString("Title: ")
	b.WriteString(req.Title)
	if s := strings.TrimSpace(req.Stem); s != "" {
		b.WriteString("\nQuestion: ")
		b.WriteString(s)
	}
	if body := strings.TrimSpace(req.Body); body != "" {
		b.WriteString("\nContent: ")
		b.WriteString(truncate(body, 1500))
	}
	b.WriteString("\n\nTopics:")
	return b.String()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// parseTopicTags extracts the JSON array of tags from a completion. Models
// routinely wrap JSON in a markdown fence or a sentence of prose; refusing those
// would fail atoms for a cosmetic reason, so the first bracketed array in the
// text is taken. A completion with no array — or an empty one — is a LOUD error.
func parseTopicTags(completion string) ([]string, error) {
	raw := extractJSONArray(completion)
	if raw == "" {
		return nil, fmt.Errorf("no JSON array in completion: %q", truncate(completion, 200))
	}

	var tags []string
	if err := json.Unmarshal([]byte(raw), &tags); err != nil {
		return nil, fmt.Errorf("completion is not a JSON string array (%q): %w", truncate(raw, 200), err)
	}
	if len(tags) == 0 {
		return nil, errors.New("classifier returned an empty tag array")
	}
	return tags, nil
}

// extractJSONArray returns the first top-level [...] span in s, or "".
func extractJSONArray(s string) string {
	start := strings.Index(s, "[")
	if start < 0 {
		return ""
	}
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}
