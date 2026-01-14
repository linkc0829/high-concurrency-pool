package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	HTTPAddr                 string        `mapstructure:"HTTP_ADDR"`
	Workers                  int           `mapstructure:"WORKERS"`
	QueueSize                int           `mapstructure:"QUEUE_SIZE"`
	RedisAddr                string        `mapstructure:"REDIS_URL"`
	RateLimit                int           `mapstructure:"RATE_LIMIT"`
	RateCapacity             int           `mapstructure:"RATE_CAPACITY"`
	KafkaBrokers             []string      `mapstructure:"KAFKA_BROKERS"`
	KafkaTopic               string        `mapstructure:"KAFKA_TOPIC"`
	KafkaGroupID             string        `mapstructure:"KAFKA_GROUP_ID"`
	KafkaPartitions          int           `mapstructure:"KAFKA_PARTITIONS"`
	KafkaProducerRetryMax    int           `mapstructure:"KAFKA_PRODUCER_RETRY_MAX"`
	KafkaProducerTimeout     time.Duration `mapstructure:"KAFKA_PRODUCER_TIMEOUT"`
	KafkaConsumerMaxRetries  int           `mapstructure:"KAFKA_CONSUMER_MAX_RETRIES"`
	KafkaConsumerBaseBackoff time.Duration `mapstructure:"KAFKA_CONSUMER_BASE_BACKOFF"`
	KafkaConsumerMaxBackoff  time.Duration `mapstructure:"KAFKA_CONSUMER_MAX_BACKOFF"`
	JobProcessTimeMin        int           `mapstructure:"JOB_PROCESS_TIME_MIN"`
	JobProcessTimeRange      int           `mapstructure:"JOB_PROCESS_TIME_RANGE"`
	JobProcessFailRate       float64       `mapstructure:"JOB_PROCESS_FAIL_RATE"`
}

func LoadConfig() (*Config, error) {
	// 1. Set default values
	viper.SetDefault("HTTP_ADDR", "localhost:8080")
	viper.SetDefault("WORKERS", 10)
	viper.SetDefault("QUEUE_SIZE", 100)
	viper.SetDefault("REDIS_URL", "localhost:6379")
	viper.SetDefault("RATE_LIMIT", 50)
	viper.SetDefault("RATE_CAPACITY", 120)
	viper.SetDefault("KAFKA_BROKERS", []string{"localhost:9092"})
	viper.SetDefault("KAFKA_TOPIC", "jobs_topic")
	viper.SetDefault("KAFKA_GROUP_ID", "worker_group_1")
	viper.SetDefault("KAFKA_PARTITIONS", 20)
	viper.SetDefault("KAFKA_PRODUCER_RETRY_MAX", 3)
	viper.SetDefault("KAFKA_PRODUCER_TIMEOUT", 5*time.Second)
	viper.SetDefault("KAFKA_CONSUMER_MAX_RETRIES", 3)
	viper.SetDefault("KAFKA_CONSUMER_BASE_BACKOFF", 40*time.Millisecond)
	viper.SetDefault("KAFKA_CONSUMER_MAX_BACKOFF", 400*time.Millisecond)
	viper.SetDefault("JOB_PROCESS_TIME_MIN", 100)
	viper.SetDefault("JOB_PROCESS_TIME_RANGE", 100)
	viper.SetDefault("JOB_PROCESS_FAIL_RATE", 0.1)

	// 2. Load from .env file
	viper.SetConfigFile(".env")
	viper.SetConfigType("env")

	// 3. Read environment variables (to overwrite .env / defaults)
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("Could not read config file: %v", err)
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("Unable to decode into struct: %v", err)
	}

	// Viper unmarshal doesn't automatically split comma-separated strings for slices
	// when reading from env/string. We might need to handle it if KAFKA_BROKERS is passed as string.
	// However, for simplicity, if it's single value in .env, it might be read as string and fail to unmarshal to []string
	// if mapstructure weak decode isn't enough.
	// Let's manually fix brokers if needed or assume simple case for now.
	// A robust way often involves a decode hook, but let's stick to simple viper first.
	// If Unmarshal fails for []string from env string, we can do a manual fix:
	if len(cfg.KafkaBrokers) == 0 {
		brokersStr := viper.GetString("KAFKA_BROKERS")
		if brokersStr != "" {
			cfg.KafkaBrokers = strings.Split(brokersStr, ",")
		}
	}

	return &cfg, nil
}
