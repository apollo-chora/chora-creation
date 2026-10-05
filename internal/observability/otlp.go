// Package observability is a thin compatibility shim that delegates to the
// canonical libs/chora-go-common/otel package per Wave B (2026-05-14, tracker
// #146). The previous local implementation duplicated the
// otlptracegrpc + WithInsecure() pattern, which silently failed the TLS
// handshake against telemetry.googleapis.com:443 and dropped every span.
//
// The canonical lib wires a real Cloud Trace exporter (ADC auth + TLS); this
// shim preserves the local Init(ctx) signature so call sites in
// cmd/server/main.go keep compiling without edits. Spans now reach Cloud
// Trace correctly per Tier 3 D13.
package observability

import (
	"context"

	commonotel "github.com/apollo-chora/chora-common/otel"
)

// serviceName is the canonical OTLP service.name attribute for this binary.
const serviceName = "chora-creation"

// version is bumped per-release; matches the constant in cmd/server/main.go.
const version = "0.1.0"

// Init wires the OTel exporter via the canonical lib and registers a global
// TracerProvider. Returns a shutdown func the caller MUST defer to flush
// spans on exit. Public signature preserved for backward compatibility.
func Init(ctx context.Context) (shutdown func(context.Context) error, err error) {
	return commonotel.Init(ctx, serviceName, version)
}
