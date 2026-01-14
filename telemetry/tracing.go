package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// InitTracer initialize tracer and return a function to shutdown tracer
func InitTracer(serviceName string, collectorAddr string) (func(context.Context) error, error) {
	ctx := context.Background()

	// 1. setup exporter (to Jaeger)
	conn, err := grpc.NewClient(collectorAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}

	traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		return nil, err
	}

	// 2. setup resource (mark this is which service)
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, err
	}

	// 3. setup tracer provider
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter), // batch sending, better performance
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()), // always sample in development (note: should use TraceIDRatioBased in production)
	)

	// 4. set global provider
	otel.SetTracerProvider(tp)

	// 5. set propagator (Propagator)
	// it responsible for adding TraceID into HTTP Header or Kafka Header to downstream
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, // support W3C Trace Context standard
		propagation.Baggage{},
	))

	return func(ctx context.Context) error {
		if err := conn.Close(); err != nil {
			return err
		}
		return tp.Shutdown(ctx)
	}, nil
}
