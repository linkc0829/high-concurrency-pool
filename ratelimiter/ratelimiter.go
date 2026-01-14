package ratelimiter

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// Lua Script logic:
// 1. Get current token count and last refill time
// 2. Calculate how many tokens should be added (refill rate)
// 3. Check if token is enough
// 4. Update Redis
var tokenBucketScript = `
local key_tokens = KEYS[1]
local key_last_refill = KEYS[2]

local refill_rate = tonumber(ARGV[1]) -- refill rate (tokens/sec)
local capacity = tonumber(ARGV[2])    -- bucket capacity (max burst)
local now = tonumber(ARGV[3])         -- current time (unixtime)
local requested = tonumber(ARGV[4])   -- request consume number (usually 1)
local ttl = 60 						  -- Key expiration time, avoid residual garbage data

-- Get current token count, if not exist, default to full bucket (capacity)
local tokens = tonumber(redis.call("get", key_tokens))
if tokens == nil then
    tokens = capacity
end

-- Get last refill time
local last_refill = tonumber(redis.call("get", key_last_refill))
if last_refill == nil then
    last_refill = 0
end

-- Calculate how many tokens should be added (refill rate)
local delta = math.max(0, now - last_refill)
local filled_tokens = math.min(capacity, tokens + (delta * refill_rate))

-- Check if token is enough
local allowed = 0
if filled_tokens >= requested then
    allowed = 1
    filled_tokens = filled_tokens - requested
    
    -- Update Redis
    redis.call("setex", key_tokens, ttl, filled_tokens)
    redis.call("setex", key_last_refill, ttl, now)
end

return allowed
`

type Limiter struct {
	rdb        *redis.Client
	luaSha     string
	capacity   int
	refillRate int
	keyPrefix  string
}

func NewLimiter(rdb *redis.Client, rate int, capacity int, keyPrefix string) (*Limiter, error) {
	sha, err := rdb.ScriptLoad(context.Background(), tokenBucketScript).Result()
	if err != nil {
		return nil, err
	}
	return &Limiter{
		rdb:        rdb,
		luaSha:     sha,
		capacity:   capacity,
		refillRate: rate,
		keyPrefix:  keyPrefix,
	}, nil
}

// Allow checks if the request is allowed (Distributed)
func (l *Limiter) Allow(ctx context.Context) (bool, error) {
	keys := []string{l.keyPrefix + ":tokens", l.keyPrefix + ":ts"}
	now := time.Now().Unix() // Use second timestamp

	allowed, err := l.rdb.EvalSha(
		ctx,
		l.luaSha,
		keys,         // key_tokens, key_last_refill
		l.refillRate, // refill rate (tokens/sec)
		l.capacity,   // bucket capacity (max burst)
		now,          // current time(unixtime)
		1,            // request consume number
	).Int()
	if err != nil {
		return false, err
	}
	return allowed == 1, nil
}
