package workerpool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/linkc0829/high-concurrency-pool/kafka"
)

type JobProcessor func(ctx context.Context, data []byte) error

type KafkaJobProcessor struct {
	Metrics *PromMetrics

	producer    *kafka.Producer
	consumer    *kafka.Consumer
	partitions  int
	poolWorkers int
	queueSize   int
	workerPool  *GlobalPool

	processor JobProcessor

	ctx    context.Context
	cancel context.CancelFunc
	consumerWg     sync.WaitGroup
}

func NewKafkaJobProcessor(
	brokers []string,
	topic string,
	groupID string,
	partitions int,
	poolWorkers int,
	queueSize int,
	producerRetryMax int,
	producerTimeout time.Duration,
	consumerMaxRetries int,
	consumerBaseBackoff time.Duration,
	consumerMaxBackoff time.Duration,
	processor JobProcessor,
) (*KafkaJobProcessor, error) {
	// 0. initialize Topic
	mainTopic := topic
	dlqTopic := topic + "-dlq"
	_ = kafka.EnsureTopic(brokers, mainTopic, int32(partitions))
	_ = kafka.EnsureTopic(brokers, dlqTopic, 1)

	// 1. initialize Kafka Producer
	producer, err := kafka.NewProducer(brokers, topic, producerRetryMax, producerTimeout)
	if err != nil {
		return nil, fmt.Errorf("producer init failed: %w", err)
	}

	// 2. initialize Kafka Consumer
	consumer, err := kafka.NewConsumer(brokers, groupID, topic, producer, consumerMaxRetries, consumerBaseBackoff, consumerMaxBackoff)
	if err != nil {
		return nil, fmt.Errorf("consumer init failed: %w", err)
	}

	// 3. initialize Global Pool
	ctx, cancel := context.WithCancel(context.Background())
	metrics := NewPromMetrics("my_system", "worker_pool")
	return &KafkaJobProcessor{
		producer:    producer,
		consumer:    consumer,
		partitions:  partitions,
		poolWorkers: poolWorkers,
		queueSize:   queueSize,
		workerPool:  NewGlobalPool(poolWorkers, queueSize, metrics),
		processor:   processor,
		Metrics:     metrics,
		ctx:         ctx,
		cancel:      cancel,
	}, nil
}

func (wp *KafkaJobProcessor) Submit(ctx context.Context, data interface{}, key string) error {
	bytes, err := json.Marshal(data)
	if err != nil {
		return err
	}

	start := time.Now()
	err = wp.producer.Publish(ctx, bytes, key)
	if err != nil {
		wp.Metrics.ProducerErrors.Inc()
	} else {
		wp.Metrics.ProducerSent.Inc()
		wp.Metrics.ProducerDuration.Observe(time.Since(start).Seconds())
	}
	return err
}

func (wp *KafkaJobProcessor) Start() {
	wrappedHandler := func(ctx context.Context, msg []byte) error {
		start := time.Now()
		err := wp.workerPool.Submit(ctx, func(jobCtx context.Context) error {
			return wp.processor(jobCtx, msg)
		})
		if err != nil {
			wp.Metrics.ConsumerErrors.Inc()
		} else {
			wp.Metrics.ConsumerProcessed.Inc()
			wp.Metrics.ConsumerDuration.Observe(time.Since(start).Seconds())
		}
		return err
	}

	slog.Info("Kafka Pool started", "partitions", wp.partitions, "pool_workers", wp.poolWorkers, "queue_size", wp.queueSize)

	wp.consumerWg.Add(1)
	go func() {
		defer wp.consumerWg.Done()
		wp.consumer.Start(wp.ctx, wrappedHandler)
		slog.Info("Kafka consumer stopped safely.")
	}()
}

// stop gracefully
func (wp *KafkaJobProcessor) Stop() {
	slog.Info("WorkerPool stopping...")

	// 1. Stop consuming (close consumer connection).
	// This will cause the consumer loop (Start) to exit.
	wp.consumer.Close()

	// 2. Wait for consumer loop to finish claiming messages.
	wp.consumerWg.Wait()

	// 3. Drain the internal worker pool (wait for in-flight jobs).
	// This ensures that all message handlers that have already called Submit() can finish.
	wp.workerPool.Shutdown()

	// 4. Cancel the context.
	// This cleans up any lingering resources associated with wp.ctx.
	wp.cancel()

	// 5. Close producer
	wp.producer.Close()

	slog.Info("WorkerPool stopped completely.")
}
