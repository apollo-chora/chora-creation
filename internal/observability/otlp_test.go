// otlp_test.go — compile/interface + smoke coverage for the OTel delegation
// shim. Init delegates to libs/chora-go-common/otel; with OTEL_EXPORTER=stdout
// the canonical lib needs no ADC credentials, so the full delegation path
// (exporter → resource → TracerProvider → shutdown) is exercisable in-test.
package observability

import (
	"context"
	"testing"
	"time"
)

func TestInit_DelegatesAndReturnsWorkingShutdown(t *testing.T) {
	// stdout exporter ⇒ hermetic: no Cloud Trace / ADC required.
	t.Setenv("OTEL_EXPORTER", "stdout")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")

	shutdown, err := Init(context.Background())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if shutdown == nil {
		t.Fatal("Init returned nil shutdown func")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestInit_PreservesServiceIdentity(t *testing.T) {
	// The shim's contract is that call sites keep compiling with the local
	// constants — pin them so a drift against cmd/server/main.go is caught.
	if serviceName != "chora-creation" {
		t.Errorf("serviceName = %q, want chora-creation", serviceName)
	}
	if version == "" {
		t.Error("version must be non-empty")
	}
}
