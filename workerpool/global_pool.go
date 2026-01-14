package workerpool

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
)

// Job is a unit of work executed by the in-process worker pool.
// It MUST be safe to run concurrently.
type Job func(ctx context.Context) error

type jobReq struct {
	ctx  context.Context
	job  Job
	resC chan error
}

// GlobalPool is a bounded, in-process worker pool used to control processing concurrency
// inside a single service instance (e.g., one ECS task).
//
// It provides:
// - Backpressure via a bounded queue
// - A hard cap on concurrent in-flight jobs
// - Context-aware submit / wait
type GlobalPool struct {
	jobs chan jobReq

	closed    atomic.Bool
	workersWg sync.WaitGroup

	// metrics are optional
	metrics *PromMetrics
	// number of in-flight jobs (workers currently running user code)
	inFlight atomic.Int64
}

func NewGlobalPool(workers, queueSize int, metrics *PromMetrics) *GlobalPool {
	if workers <= 0 {
		workers = 1
	}
	if queueSize < 0 {
		queueSize = 0
	}

	p := &GlobalPool{
		jobs:    make(chan jobReq, queueSize),
		metrics: metrics,
	}

	for i := 0; i < workers; i++ {
		p.startWorker()
	}

	return p
}

func (p *GlobalPool) startWorker() {
	p.workersWg.Add(1)
	go p.worker()
}

func (p *GlobalPool) worker() {
	defer p.workersWg.Done()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("worker panic", "error", r)
			// restart worker if panic
			p.startWorker()
		}
	}()
	for req := range p.jobs {
		// If caller context is already cancelled, short-circuit.
		if err := req.ctx.Err(); err != nil {
			req.resC <- err
			close(req.resC)
			continue
		}

		p.inFlight.Add(1)
		p.metrics.ConsumerInFlight.Set(float64(p.inFlight.Load()))
		p.metrics.ConsumerQueueLen.Set(float64(len(p.jobs)))

		err := req.job(req.ctx)

		p.inFlight.Add(-1)
		p.metrics.ConsumerInFlight.Set(float64(p.inFlight.Load()))
		p.metrics.ConsumerQueueLen.Set(float64(len(p.jobs)))

		req.resC <- err
		close(req.resC)
	}
}

// Submit runs a job on the pool and blocks until it finishes (or ctx is cancelled).
// This is intentionally synchronous to preserve per-partition ordering in Kafka consumers
// while still limiting *global* concurrency across partitions.
func (p *GlobalPool) Submit(ctx context.Context, job Job) error {
	if p.closed.Load() {
		return errors.New("global pool is closed")
	}
	if job == nil {
		return errors.New("job is nil")
	}

	resC := make(chan error, 1)
	req := jobReq{
		ctx:  ctx,
		job:  job,
		resC: resC,
	}

	// Backpressure: block until the bounded queue has room, or ctx is cancelled.
	select {
	case p.jobs <- req:
		p.metrics.ConsumerQueueLen.Set(float64(len(p.jobs)))
	case <-ctx.Done():
		return ctx.Err()
	}

	// Wait for completion, but also respect ctx cancellation (rebalance/shutdown).
	select {
	case err := <-resC:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close closes the pool immediately.
func (p *GlobalPool) Close() {
	if p.closed.Swap(true) {
		return
	}
	close(p.jobs)
}

// Shutdown initiates a graceful shutdown.
// It closes the job channel (no new jobs accepted)
// and waits for all workers to finish processing current jobs.
func (p *GlobalPool) Shutdown() {
	p.Close()
	p.workersWg.Wait()
}
