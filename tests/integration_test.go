package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/linkc0829/high-concurrency-pool/workerpool"

	"github.com/stretchr/testify/assert"
	"github.com/testcontainers/testcontainers-go/modules/kafka"
	"github.com/testcontainers/testcontainers-go/modules/redis"
)

func TestIntegration_FullFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()

	// 1. Start Redis
	redisContainer, err := redis.Run(ctx, "redis:7")
	if err != nil {
		t.Fatalf("failed to start redis: %v", err)
	}
	defer func() {
		if err := redisContainer.Terminate(ctx); err != nil {
			t.Fatalf("failed to terminate redis: %v", err)
		}
	}()

	redisConnectionString, err := redisContainer.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("failed to get redis connection string: %v", err)
	}

	// 2. Start Kafka
	kafkaContainer, err := kafka.Run(ctx, "confluentinc/cp-kafka:7.3.0")
	if err != nil {
		t.Fatalf("failed to start kafka: %v", err)
	}
	defer func() {
		if err := kafkaContainer.Terminate(ctx); err != nil {
			t.Fatalf("failed to terminate kafka: %v", err)
		}
	}()

	brokers, err := kafkaContainer.Brokers(ctx)
	if err != nil {
		t.Fatalf("failed to get kafka brokers: %v", err)
	}

	fmt.Printf("Redis running at: %s\n", redisConnectionString)
	fmt.Printf("Kafka running at: %v\n", brokers)

	// 3. Setup WorkerPool
	topic := "integration_test_topic"
	groupID := "integration_group"
	partitions := 1 // simple
	workers := 2
	queueSize := 10

	doneCh := make(chan string, 1)

	// Custom processor to verify execution
	mockProcessor := func(ctx context.Context, data []byte) error {
		var payload map[string]interface{}
		if err := json.Unmarshal(data, &payload); err != nil {
			return err
		}
		doneCh <- fmt.Sprintf("%v", payload["msg"])
		return nil
	}

	wp, err := workerpool.NewKafkaJobProcessor(
		brokers,
		topic,
		groupID,
		partitions,
		workers,
		queueSize,
		3,                    // producer retry
		5*time.Second,        // producer timeout
		3,                    // consumer retry
		100*time.Millisecond, // consumer base backoff
		1*time.Second,        // consumer max backoff
		mockProcessor,
	)
	assert.NoError(t, err)

	wp.Start()
	defer wp.Stop()

	// 4. Submit Job
	jobData := map[string]string{"msg": "hello_integration"}
	err = wp.Submit(ctx, jobData, "key")
	assert.NoError(t, err)

	// 5. Verify Result
	select {
	case result := <-doneCh:
		assert.Equal(t, "hello_integration", result)
	case <-time.After(30 * time.Second):
		t.Fatal("timeout waiting for job processing")
	}
}
