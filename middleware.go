package main

import (
	"net/http"

	"github.com/linkc0829/high-concurrency-pool/ratelimiter"
	"github.com/linkc0829/high-concurrency-pool/workerpool"

	"github.com/gin-gonic/gin"
)

func RateLimitMiddleware(pool *workerpool.KafkaJobProcessor, limiter *ratelimiter.Limiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		allowed, err := limiter.Allow(c.Request.Context())
		if err != nil {
			// fail closed when rate limiter fails
			// can also fail open to avoid service interruption
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "Service Unavailable"})
			return
		}

		if !allowed {
			pool.Metrics.RateLimitedTotal.Inc()
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "Too many requests"})
			return
		}

		c.Next()
	}
}
