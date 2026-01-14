package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strconv"
	"time"

	"github.com/IBM/sarama"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// MessageHandler defines the signature of the message handler
type MessageHandler func(ctx context.Context, msg []byte) error

type Consumer struct {
	group    sarama.ConsumerGroup
	producer *Producer

	groupID  string
	topic    string
	dlqTopic string

	// local retry times (not including the first attempt)
	maxRetries int

	baseBackoff time.Duration
	maxBackoff  time.Duration
}

func NewConsumer(brokers []string, groupID string, topic string, producer *Producer, maxRetries int, baseBackoff time.Duration, maxBackoff time.Duration) (*Consumer, error) {
	config := sarama.NewConfig()
	config.Version = sarama.V2_8_0_0
	config.Consumer.Group.Rebalance.Strategy = sarama.NewBalanceStrategyRoundRobin()
	config.Consumer.Offsets.Initial = sarama.OffsetNewest

	group, err := sarama.NewConsumerGroup(brokers, groupID, config)
	if err != nil {
		return nil, err
	}

	return &Consumer{
		group:       group,
		producer:    producer,
		groupID:     groupID,
		topic:       topic,
		dlqTopic:    topic + "-dlq",
		maxRetries:  maxRetries,
		baseBackoff: baseBackoff,
		maxBackoff:  maxBackoff,
	}, nil
}

func (c *Consumer) Start(ctx context.Context, handler MessageHandler) {
	internalHandler := &groupHandler{
		consumer:    c,
		userHandler: handler,
	}

	slog.Info("Kafka Consumer started", "topic", c.topic, "group_id", c.groupID)

	for {
		if ctx.Err() != nil {
			return
		}
		if err := c.group.Consume(ctx, []string{c.topic}, internalHandler); err != nil {
			if errors.Is(err, sarama.ErrClosedConsumerGroup) {
				return
			}
			slog.ErrorContext(ctx, "Consumer consume error", "error", err)
			time.Sleep(2 * time.Second)
		}
		// after rebalance/shutdown, Consume returns, loop re-consume
	}
}

func (c *Consumer) Close() error {
	return c.group.Close()
}

// groupHandler implements sarama.ConsumerGroupHandler
type groupHandler struct {
	consumer    *Consumer
	userHandler MessageHandler
}

func (h *groupHandler) Setup(_ sarama.ConsumerGroupSession) error   { return nil }
func (h *groupHandler) Cleanup(_ sarama.ConsumerGroupSession) error { return nil }

func (h *groupHandler) ConsumeClaim(sess sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	tr := otel.Tracer("kafka-consumer")

	for msg := range claim.Messages() {
		if err := h.processOne(sess, msg, tr); err != nil {
			// not mark this message, let it at-least-once replay
			return err
		}
	}
	return nil
}

func (h *groupHandler) processOne(sess sarama.ConsumerGroupSession, msg *sarama.ConsumerMessage, tr trace.Tracer) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic in handler: %v", r)
		}
	}()

	ctx := h.consumer.extractTrace(sess.Context(), msg.Headers)
	ctx, span := tr.Start(ctx, "process_message", trace.WithSpanKind(trace.SpanKindConsumer))
	defer span.End()

	span.SetAttributes(
		attribute.String("kafka.topic", msg.Topic),
		attribute.Int("kafka.partition", int(msg.Partition)),
		attribute.Int64("kafka.offset", msg.Offset),
		attribute.String("kafka.group", h.consumer.groupID),
		attribute.Int("payload.bytes", len(msg.Value)),
	)

	maxAttempts := 1 + h.consumer.maxRetries
	attempts := 0
	start := time.Now()

	for {
		attempts++
		err = h.userHandler(ctx, msg.Value)
		if err == nil {
			break
		}
		if attempts >= maxAttempts {
			break
		}
		if sess.Context().Err() != nil {
			// rebalance/shutdown：don't mark this message, let it at-least-once replay
			span.RecordError(sess.Context().Err())
			span.SetStatus(codes.Error, "session cancelled")
			return sess.Context().Err()
		}

		// exponential backoff + jitter (local retry)
		d := h.consumer.baseBackoff * time.Duration(1<<(attempts-1))
		if d > h.consumer.maxBackoff {
			d = h.consumer.maxBackoff
		}
		d += time.Duration(rand.Int63n(int64(30 * time.Millisecond)))

		span.AddEvent("local-retry-scheduled",
			trace.WithAttributes(
				attribute.Int("next_attempt", attempts+1),
				attribute.Int64("sleep_ms", d.Milliseconds()),
				attribute.String("error", trimErr(err)),
			),
		)

		select {
		case <-time.After(d):
		case <-sess.Context().Done():
			span.RecordError(sess.Context().Err())
			span.SetStatus(codes.Error, "session cancelled")
			return sess.Context().Err()
		}
	}

	dur := time.Since(start)
	span.SetAttributes(attribute.Int("local.attempts", attempts))
	if err == nil {
		span.SetStatus(codes.Ok, "ok")
		sess.MarkMessage(msg, "")
		return nil
	}

	// failed finally, only mark message when DLQ success
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	span.SetAttributes(attribute.Int64("duration_ms", dur.Milliseconds()))

	if perr := h.consumer.produceDLQ(ctx, msg, attempts, err); perr != nil {
		span.RecordError(perr)
		// don't mark this message, let it at-least-once replay
		return perr
	}

	// DLQ success, mark this message
	sess.MarkMessage(msg, "")
	return nil
}

// produceDLQ send finally failed message to <topic>-dlq (with trace)
func (c *Consumer) produceDLQ(ctx context.Context, msg *sarama.ConsumerMessage, attempts int, cause error) error {
	headers := []sarama.RecordHeader{
		{Key: []byte("x-attempt"), Value: []byte(strconv.Itoa(attempts))},
		{Key: []byte("x-origin-topic"), Value: []byte(msg.Topic)},
		{Key: []byte("x-origin-partition"), Value: []byte(strconv.Itoa(int(msg.Partition)))},
		{Key: []byte("x-origin-offset"), Value: []byte(strconv.FormatInt(msg.Offset, 10))},
		{Key: []byte("x-final-error"), Value: []byte(trimErr(cause))},
	}
	return c.producer.SendToTopicWithHeaders(ctx, c.dlqTopic, msg.Value, msg.Key, headers)
}

// extractTrace extract trace from message headers
func (c *Consumer) extractTrace(ctx context.Context, headers []*sarama.RecordHeader) context.Context {
	carrier := propagation.MapCarrier{}
	for _, h := range headers {
		if h == nil {
			continue
		}
		carrier[string(h.Key)] = string(h.Value)
	}
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}

// trimErr trim error message to 200 characters
func trimErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 200 {
		return s[:200]
	}
	return s
}
