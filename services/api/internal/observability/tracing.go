package observability

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// traceIDFromCtx extracts the W3C trace ID from the OpenTelemetry context.
// Returns "" when tracing is a no-op (OTLP endpoint unset, NeverSample), so
// WithTrace emits no trace_id attribute locally. This is expected: trace IDs
// only appear once an OTLP collector is configured.
func traceIDFromCtx(ctx context.Context) string {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return ""
	}
	return span.SpanContext().TraceID().String()
}

var (
	Tracer     trace.Tracer
	tracerProv *sdktrace.TracerProvider
)

// InitTracing initializes the OpenTelemetry SDK with an OTLP HTTP exporter.
// When OTEL_EXPORTER_OTLP_ENDPOINT is unset, tracing is a no-op (passthrough).
func InitTracing(serviceName string) error {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		tracerProv = sdktrace.NewTracerProvider(
			sdktrace.WithSampler(sdktrace.NeverSample()),
			sdktrace.WithResource(resource.NewSchemaless(
				attribute.String("service.name", serviceName),
			)),
		)
		otel.SetTracerProvider(tracerProv)
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		))
		Tracer = tracerProv.Tracer(serviceName)
		return nil
	}

	exp, err := otlptracehttp.New(context.Background(),
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return err
	}

	tracerProv = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewSchemaless(
			attribute.String("service.name", serviceName),
			attribute.String("service.namespace", "endpoint-lighthouse"),
		)),
	)
	otel.SetTracerProvider(tracerProv)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	Tracer = tracerProv.Tracer(serviceName)
	return nil
}

// Shutdown shuts down the tracer provider, flushing any remaining spans.
func Shutdown(ctx context.Context) error {
	if tracerProv == nil {
		return nil
	}
	return tracerProv.Shutdown(ctx)
}
