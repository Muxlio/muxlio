package telemetry

import (
	"context"
	"errors"
	"fmt"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Setup enables OTLP/HTTP export using standard OTEL_* endpoint, header and
// TLS settings. Export is opt-in per signal (OTEL_*_EXPORTER=otlp); no content
// capture is installed. Shutdown flushes both signals before the process exits.
func Setup(ctx context.Context, service string) (func(context.Context) error, error) {
	shutdowns := []func(context.Context) error{}
	shutdown := func(ctx context.Context) error {
		var errs []error
		for _, fn := range shutdowns {
			errs = append(errs, fn(ctx))
		}
		return errors.Join(errs...)
	}
	if os.Getenv("OTEL_SDK_DISABLED") == "true" {
		return shutdown, nil
	}
	for _, key := range []string{"OTEL_TRACES_EXPORTER", "OTEL_METRICS_EXPORTER"} {
		value := os.Getenv(key)
		if value != "" && value != "none" && value != "otlp" {
			return shutdown, fmt.Errorf("%s must be otlp or none", key)
		}
	}
	if os.Getenv("OTEL_TRACES_EXPORTER") != "otlp" && os.Getenv("OTEL_METRICS_EXPORTER") != "otlp" {
		return shutdown, nil
	}
	res, err := resource.New(ctx, resource.WithAttributes(attribute.String("service.name", service)), resource.WithFromEnv())
	if err != nil {
		return shutdown, err
	}
	if os.Getenv("OTEL_TRACES_EXPORTER") == "otlp" {
		exporter, err := otlptracehttp.New(ctx)
		if err != nil {
			return shutdown, err
		}
		provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
		shutdowns = append(shutdowns, provider.Shutdown)
		otel.SetTracerProvider(provider)
	}
	if os.Getenv("OTEL_METRICS_EXPORTER") == "otlp" {
		exporter, err := otlpmetrichttp.New(ctx)
		if err != nil {
			_ = shutdown(ctx)
			return func(context.Context) error { return nil }, err
		}
		provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)), sdkmetric.WithResource(res))
		shutdowns = append(shutdowns, provider.Shutdown)
		otel.SetMeterProvider(provider)
	}
	return shutdown, nil
}
