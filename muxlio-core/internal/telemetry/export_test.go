package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
)

func TestOTLPHTTPExportsAndFlushesBothSignals(t *testing.T) {
	var mu sync.Mutex
	paths := map[string]int{}
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths[r.URL.Path]++
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer collector-token" {
			t.Error("OTLP header configuration was lost")
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer collector.Close()
	t.Setenv("OTEL_SDK_DISABLED", "false")
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_METRICS_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/v1/traces")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", collector.URL+"/v1/metrics")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=Bearer%20collector-token")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "Authorization=Bearer%20collector-token")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_HEADERS", "Authorization=Bearer%20collector-token")
	oldTrace, oldMetric := otel.GetTracerProvider(), otel.GetMeterProvider()
	defer func() { otel.SetTracerProvider(oldTrace); otel.SetMeterProvider(oldMetric) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdown, err := Setup(ctx, "test-muxlio")
	if err != nil {
		t.Fatal(err)
	}
	spanCtx, end := Start(ctx, "invoke_workflow", RunAttributes("run", "workflow")...)
	Duration(spanCtx, "muxlio.queue.delay", time.Second, "generic")
	end(false)
	if err := shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if paths["/v1/traces"] != 1 || paths["/v1/metrics"] != 1 {
		t.Fatalf("shutdown did not flush both signals: %v", paths)
	}
}

func TestSetupNoExportByDefault(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "false")
	t.Setenv("OTEL_TRACES_EXPORTER", "")
	t.Setenv("OTEL_METRICS_EXPORTER", "")
	oldTrace, oldMetric := otel.GetTracerProvider(), otel.GetMeterProvider()
	shutdown, err := Setup(context.Background(), "muxlio")
	if err != nil {
		t.Fatal(err)
	}
	if oldTrace != otel.GetTracerProvider() || oldMetric != otel.GetMeterProvider() {
		t.Fatal("default setup installed an exporter")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
