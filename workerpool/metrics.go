package workerpool

import "github.com/prometheus/client_golang/prometheus"

type PromMetrics struct {
	ProducerSent      prometheus.Counter
	ProducerErrors    prometheus.Counter
	ProducerDuration  prometheus.Histogram
	ConsumerProcessed prometheus.Counter
	ConsumerErrors    prometheus.Counter
	ConsumerDuration  prometheus.Histogram
	ConsumerQueueLen  prometheus.Gauge
	ConsumerInFlight  prometheus.Gauge
	RateLimitedTotal  prometheus.Counter
}

func NewPromMetrics(namespace, subsystem string) *PromMetrics {
	m := &PromMetrics{
		ProducerSent: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "producer_sent_total",
			Help:      "Number of messages sent by the producer.",
		}),
		ProducerErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "producer_errors_total",
			Help:      "Number of errors occurred by the producer.",
		}),
		ProducerDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "producer_duration_seconds",
			Help:      "Duration of messages sent by the producer.",
		}),
		ConsumerProcessed: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "consumer_processed_total",
			Help:      "Number of messages processed by the consumer.",
		}),
		ConsumerErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "consumer_errors_total",
			Help:      "Number of errors occurred by the consumer.",
		}),
		ConsumerDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "consumer_duration_seconds",
			Help:      "Duration of messages processed by the consumer.",
		}),
		ConsumerQueueLen: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "consumer_queue_length",
			Help:      "Number of pending jobs waiting in the in-process worker queue.",
		}),
		ConsumerInFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "consumer_inflight",
			Help:      "Number of jobs currently being processed by in-process workers.",
		}),
		RateLimitedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "rate_limited_total",
			Help:      "Number of messages rate limited by the rate limiter.",
		}),
	}
	prometheus.MustRegister(
		m.ProducerSent,
		m.ProducerErrors,
		m.ProducerDuration,
		m.ConsumerProcessed,
		m.ConsumerErrors,
		m.ConsumerDuration,
		m.ConsumerQueueLen,
		m.ConsumerInFlight,
		m.RateLimitedTotal,
	)
	return m
}
