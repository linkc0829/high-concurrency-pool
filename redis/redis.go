package redis

import (
	"context"
	"log/slog"
	"os"

	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
)

func NewClient(addr string) *redis.Client {
	client := redis.NewClient(&redis.Options{
		Addr: addr,
	})
	if err := client.Ping(context.Background()).Err(); err != nil {
		slog.Error("Redis connection failed", "error", err)
		os.Exit(1)
	}
	if err := redisotel.InstrumentTracing(client); err != nil {
		slog.Error("Redis tracing init failed", "error", err)
		os.Exit(1)
	}
	return client
}
