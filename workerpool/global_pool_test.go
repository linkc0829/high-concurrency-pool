package workerpool

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
)

// newTestMetrics returns a PromMetrics struct with no-op collectors
// that are NOT registered to the default registry, avoiding panic.
func newTestMetrics() *PromMetrics {
	return &PromMetrics{
		ProducerSent:      prometheus.NewCounter(prometheus.CounterOpts{}),
		ProducerErrors:    prometheus.NewCounter(prometheus.CounterOpts{}),
		ProducerDuration:  prometheus.NewHistogram(prometheus.HistogramOpts{}),
		ConsumerProcessed: prometheus.NewCounter(prometheus.CounterOpts{}),
		ConsumerErrors:    prometheus.NewCounter(prometheus.CounterOpts{}),
		ConsumerDuration:  prometheus.NewHistogram(prometheus.HistogramOpts{}),
		ConsumerQueueLen:  prometheus.NewGauge(prometheus.GaugeOpts{}),
		ConsumerInFlight:  prometheus.NewGauge(prometheus.GaugeOpts{}),
		RateLimitedTotal:  prometheus.NewCounter(prometheus.CounterOpts{}),
	}
}

func TestGlobalPool_Basic(t *testing.T) {
	metrics := newTestMetrics()
	// 5 workers, queue size 10
	pool := NewGlobalPool(5, 10, metrics)
	defer pool.Close() // In test we might use Close or Shutdown

	var counter atomic.Int32
	job := func(ctx context.Context) error {
		counter.Add(1)
		return nil
	}

	err := pool.Submit(context.Background(), job)
	assert.NoError(t, err)

	// Give some time for worker to pick up
	time.Sleep(10 * time.Millisecond)
	assert.Equal(t, int32(1), counter.Load())
}

func TestGlobalPool_Concurrency(t *testing.T) {
	metrics := newTestMetrics()
	// 2 workers
	pool := NewGlobalPool(2, 5, metrics)
	defer pool.Close()

	var activeWorkers atomic.Int32
	var maxObserved atomic.Int32

	iterations := 20
	var wg sync.WaitGroup
	wg.Add(iterations)

	job := func(ctx context.Context) error {
		defer wg.Done()
		current := activeWorkers.Add(1)

		// check max
		for {
			oldMax := maxObserved.Load()
			if current > oldMax {
				if !maxObserved.CompareAndSwap(oldMax, current) {
					continue
				}
			}
			break
		}

		time.Sleep(50 * time.Millisecond)
		activeWorkers.Add(-1)
		return nil
	}

	for i := 0; i < iterations; i++ {
		go func() {
			pool.Submit(context.Background(), job)
		}()
	}

	wg.Wait()
	// Should not exceed 2 workers
	assert.LessOrEqual(t, maxObserved.Load(), int32(2))
}

func TestGlobalPool_Shutdown(t *testing.T) {
	metrics := newTestMetrics()
	pool := NewGlobalPool(5, 10, metrics)

	var processed atomic.Int32
	job := func(ctx context.Context) error {
		time.Sleep(500 * time.Millisecond) // Simulate work
		processed.Add(1)
		return nil
	}

	// Submit 3 jobs
	for i := 0; i < 3; i++ {
		go pool.Submit(context.Background(), job)
	}

	// Wait a bit to ensure they are submitted (in queue or processing)
	time.Sleep(50 * time.Millisecond)

	// Initiate shutdown
	start := time.Now()
	pool.Shutdown() // Should wait for all 3 jobs
	duration := time.Since(start)

	assert.Equal(t, int32(3), processed.Load())
	assert.GreaterOrEqual(t, duration, 500*time.Millisecond, "Shutdown should wait for jobs")

	// Submit after shutdown should fail
	err := pool.Submit(context.Background(), job)
	assert.Error(t, err)
	assert.Equal(t, "global pool is closed", err.Error())
}

func TestGlobalPool_ContextCancel(t *testing.T) {
	metrics := newTestMetrics()
	pool := NewGlobalPool(1, 0, metrics) // 0 queue size
	defer pool.Close()

	// Make the worker busy
	go pool.Submit(context.Background(), func(ctx context.Context) error {
		time.Sleep(1 * time.Second)
		return nil
	})

	time.Sleep(50 * time.Millisecond)

	// Submit another job with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := pool.Submit(ctx, func(ctx context.Context) error {
		return nil
	})

	assert.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
}
