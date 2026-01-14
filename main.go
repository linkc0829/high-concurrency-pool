package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/linkc0829/high-concurrency-pool/logger"
	"github.com/linkc0829/high-concurrency-pool/ratelimiter"
	redisClient "github.com/linkc0829/high-concurrency-pool/redis"
	"github.com/linkc0829/high-concurrency-pool/telemetry"
	"github.com/linkc0829/high-concurrency-pool/workerpool"

	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var cfg *Config
var jobProcessTimeLimit int

// SimpleJob simulate a time consuming job
type SimpleJob struct {
	ID             string `json:"id"`
	ProcessingTime int    `json:"processing_time"`
}

func ProcessSimpleJob(ctx context.Context, data []byte) error {
	tracer := otel.Tracer("process_simple_job")
	job := SimpleJob{}

	_, spanParse := tracer.Start(ctx, "json_unmarshal")
	if err := json.Unmarshal(data, &job); err != nil {
		spanParse.RecordError(err)
		spanParse.SetStatus(codes.Error, "json_unmarshal_failed")
		spanParse.End()
		return err
	}
	spanParse.End()

	_, spanProcess := tracer.Start(ctx, "heavy_calculation")
	defer spanProcess.End()

	// processing time randomly from cfg.JobProcessTimeMin(default 100) to cfg.JobProcessTimeMin + cfg.JobProcessTimeRange(default 200)
	// those job whose processing time is longer than jobProcessTimeLimit(default 190) will simulate a timeout
	// so that jobs are randomly failed to demostrate the retry mechanism of worker pool
	if job.ProcessingTime > jobProcessTimeLimit {
		select {
		// simulate a timeout
		case <-time.After(time.Duration(jobProcessTimeLimit) * time.Millisecond):
			return fmt.Errorf("job process time too long: %v", job.ProcessingTime)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	// simulate processing time
	case <-time.After(time.Duration(job.ProcessingTime) * time.Millisecond):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func main() {
	// init logger
	logger.InitLogger()

	// load config
	var err error
	cfg, err = LoadConfig()
	if err != nil {
		slog.Error("Load config failed", "error", err)
		os.Exit(1)
	}
	jobProcessTimeLimit = cfg.JobProcessTimeMin + int(float64(cfg.JobProcessTimeRange)*(1-cfg.JobProcessFailRate))

	// init tracer
	tracerShutdown, err := telemetry.InitTracer("worker-service", "localhost:4317")
	if err != nil {
		slog.Error("Init tracer failed", "error", err)
		os.Exit(1)
	}
	defer tracerShutdown(context.Background())

	// start worker pool
	pool, err := workerpool.NewKafkaJobProcessor(
		cfg.KafkaBrokers,
		cfg.KafkaTopic,
		cfg.KafkaGroupID,
		cfg.KafkaPartitions,
		cfg.Workers,
		cfg.QueueSize,
		cfg.KafkaProducerRetryMax,
		cfg.KafkaProducerTimeout,
		cfg.KafkaConsumerMaxRetries,
		cfg.KafkaConsumerBaseBackoff,
		cfg.KafkaConsumerMaxBackoff,
		ProcessSimpleJob,
	)
	if err != nil {
		slog.Error("Failed to create worker pool", "error", err)
		os.Exit(1)
	}
	pool.Start()
	defer pool.Stop()

	// start redis
	rdb := redisClient.NewClient(cfg.RedisAddr)

	// init rate limiter
	limiter, err := ratelimiter.NewLimiter(rdb, cfg.RateLimit, cfg.RateCapacity, "rate_limit")
	if err != nil {
		slog.Error("Failed to create rate limiter", "error", err)
		os.Exit(1)
	}

	r := gin.Default()
	pprof.Register(r)
	// non-tracing api
	{
		r.GET("/metrics", gin.WrapH(promhttp.Handler()))
	}
	// tracing api
	r.Use(otelgin.Middleware("workerpool"))
	r.Use(RateLimitMiddleware(pool, limiter))
	{
		r.GET("/submit", func(c *gin.Context) {
			key := c.Query("key")
			if key == "" {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": "key is required",
				})
				return
			}
			span := trace.SpanFromContext(c.Request.Context())
			span.SetAttributes(attribute.String("app.key", key))
			job := SimpleJob{
				ID:             uuid.New().String(),
				ProcessingTime: rand.Intn(cfg.JobProcessTimeRange) + cfg.JobProcessTimeMin,
			}

			if err := pool.Submit(c.Request.Context(), job, key); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": err.Error(),
				})
				return
			}
			c.JSON(http.StatusOK, gin.H{
				"message":  "Job Enqueued to Kafka",
				"trace_id": getTraceID(c.Request.Context()),
			})
		})
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}

	fmt.Println("Server listening on ", cfg.HTTPAddr)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("listen error", "error", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("Server forced to shutdown", "error", err)
		os.Exit(1)
	}

	slog.Info("Server exiting")
}

func getTraceID(ctx context.Context) string {
	span := trace.SpanFromContext(ctx)
	if span.SpanContext().IsValid() {
		return span.SpanContext().TraceID().String()
	}
	return ""
}
