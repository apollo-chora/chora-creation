// Package httpadapter — shared event-publisher port + small helpers used by
// the variant + AI-Assist + media handlers.
//
// Per ADR-143 the chora-creation service no longer owns the Knowledge Graph
// (it relocated per-user to chora-consumption). The EventPublisher port
// previously lived alongside the now-removed knowledge_graph_handler.go;
// this file is the canonical home so the variant + media handlers continue
// to compile.
package httpadapter

import (
	"context"

	"github.com/apollo-chora/chora-creation/internal/adapter/inmem"
	"github.com/google/uuid"
)

// EventPublisher is the (minimal) port used by mutating handlers to emit
// Pub/Sub events. Adapters: inmem.EventRecorder (tests), pubsub.Publisher
// (production, M12).
type EventPublisher interface {
	Publish(ctx context.Context, topic string, env inmem.Envelope, payload any) error
}

// mustUUIDv7 returns a UUIDv7 string or empty on failure (the OS-RNG must be
// very broken for this to fail; we tolerate it because the envelope fields are
// checked at publish time, not at construction).
func mustUUIDv7() string {
	id, err := uuid.NewV7()
	if err != nil {
		return ""
	}
	return id.String()
}

// traceparentFromContext extracts the W3C traceparent stored on context (set
// by the OTel HTTP middleware in libs/chora-go-common/observability). The
// outermost middleware in this service is the local logging+tenantContext
// pair; the canonical traceparent flows via libs once main.go opts in.
//
// Today the helper falls back to an empty string when the value is absent.
// This is acceptable because the publisher Envelope's traceparent field is
// optional in the payload but mandatory only at envelope.Validate() time —
// and the publisher in this service is in-memory until M10 wires Cloud
// Pub/Sub.
func traceparentFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyTraceparent).(string); ok {
		return v
	}
	return ""
}
