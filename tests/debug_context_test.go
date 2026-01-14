package tests

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func TestGinContextPropagation(t *testing.T) {
	// Setup Trace Provider
	tp := trace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	r := gin.New()
	r.Use(otelgin.Middleware("test-server"))

	r.GET("/test", func(c *gin.Context) {
		// Get span from c.Request.Context()
		span := oteltrace.SpanFromContext(c.Request.Context())

		// Start a child span using c (gin.Context)
		tr := otel.Tracer("test")

		ctxC := c
		_, sC := tr.Start(ctxC, "child_c")

		ctxReq := c.Request.Context()
		_, sReq := tr.Start(ctxReq, "child_req")

		t.Logf("Root TraceID: %s", span.SpanContext().TraceID())
		t.Logf("Child(c) TraceID: %s", sC.SpanContext().TraceID())
		t.Logf("Child(req) TraceID: %s", sReq.SpanContext().TraceID())

		if sC.SpanContext().TraceID() != span.SpanContext().TraceID() {
			t.Error("Child span created from *gin.Context has different TraceID (broken link)")
		} else {
			t.Log("Child span created from *gin.Context has correct TraceID")
		}

		if sReq.SpanContext().TraceID() != span.SpanContext().TraceID() {
			t.Error("Child span created from c.Request.Context() has different TraceID")
		}

		c.Status(200)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)
}
