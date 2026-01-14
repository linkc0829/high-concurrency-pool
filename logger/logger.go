package logger

import (
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel/trace"
)

// InitLogger initializes the global logger with JSON handler.
// It also extracts TraceID from context if available.
func InitLogger() {
	handler := &ContextHandler{
		Handler: slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		}),
	}
	logger := slog.New(handler)
	slog.SetDefault(logger)
}

type ContextHandler struct {
	slog.Handler
}

// Handle adds TraceID and SpanID from context to the record
func (h *ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	spanContext := trace.SpanContextFromContext(ctx)
	if spanContext.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", spanContext.TraceID().String()),
			slog.String("span_id", spanContext.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, r)
}
