package kafka

import (
	"context"
	"log"
	"time"

	"github.com/IBM/sarama"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type Producer struct {
	producer sarama.SyncProducer
	topic    string
}

func NewProducer(brokers []string, topic string, retryMax int, timeout time.Duration) (*Producer, error) {
	config := sarama.NewConfig()
	config.Version = sarama.V2_8_0_0
	config.Producer.Return.Successes = true
	config.Producer.RequiredAcks = sarama.WaitForAll // the most strict confirmation, ensure no data loss
	config.Producer.Retry.Max = retryMax
	config.Producer.Timeout = timeout // write timeout

	p, err := sarama.NewSyncProducer(brokers, config)
	if err != nil {
		return nil, err
	}

	return &Producer{
		producer: p,
		topic:    topic,
	}, nil
}

// EnsureTopic automatically checks and creates the topic if it doesn't exist
func EnsureTopic(brokers []string, topic string, partitions int32) error {
	config := sarama.NewConfig()
	config.Version = sarama.V2_8_0_0

	// Create Admin connection
	admin, err := sarama.NewClusterAdmin(brokers, config)
	if err != nil {
		return err
	}
	defer admin.Close()

	topics, err := admin.ListTopics()
	if err != nil {
		return err
	}

	if topicDetail, exists := topics[topic]; exists {
		log.Printf("Topic %s already exists", topic)
		if topicDetail.NumPartitions < partitions {
			return admin.CreatePartitions(topic, partitions, nil, false)
		}
		return nil
	}

	// Create topic if it doesn't exist
	err = admin.CreateTopic(topic, &sarama.TopicDetail{
		NumPartitions:     partitions,
		ReplicationFactor: 1,
	}, false)

	if err != nil {
		return err
	}
	log.Printf("Created topic %s with %d partitions", topic, partitions)
	return nil
}

func (p *Producer) Publish(ctx context.Context, message []byte, key string) error {
	var keyBytes []byte
	if key != "" {
		keyBytes = []byte(key)
	}
	return p.SendToTopicWithHeaders(ctx, p.topic, message, keyBytes, nil)
}

// SendToTopicWithHeaders sends a message to an arbitrary topic while preserving trace context
// and attaching custom headers (e.g., DLQ metadata).
func (p *Producer) SendToTopicWithHeaders(
	ctx context.Context,
	topic string,
	message []byte,
	key []byte,
	headers []sarama.RecordHeader,
) error {
	tr := otel.Tracer("kafka-producer")
	ctx, span := tr.Start(ctx, "kafka_send_"+topic, trace.WithSpanKind(trace.SpanKindProducer))
	defer span.End()

	msg := &sarama.ProducerMessage{
		Topic: topic,
		Value: sarama.ByteEncoder(message),
	}
	if len(key) > 0 {
		msg.Key = sarama.ByteEncoder(key)
	}
	if len(headers) > 0 {
		msg.Headers = append(msg.Headers, headers...)
	}

	injectTracing(ctx, msg)

	_, _, err := p.producer.SendMessage(msg)
	if err != nil {
		span.RecordError(err)
	}
	return err
}

func (p *Producer) Close() error {
	return p.producer.Close()
}

func injectTracing(ctx context.Context, msg *sarama.ProducerMessage) {
	// use MapCarrier as carrier
	carrier := propagation.MapCarrier{}

	// write Context's TraceID into carrier
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	// convert carrier to Kafka Headers
	for k, v := range carrier {
		msg.Headers = append(msg.Headers, sarama.RecordHeader{
			Key:   []byte(k),
			Value: []byte(v),
		})
	}
}
