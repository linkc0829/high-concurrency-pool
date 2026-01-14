package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type config struct {
	target     string
	workers    int
	duration   time.Duration
	think      time.Duration
	timeout    time.Duration
	mode       string // random | hot
	hotKey     string
	hotRatio   float64
	keyParam   string
	keySpace   int
	printEvery time.Duration
}

func main() {
	cfg := parseFlags()

	baseURL, err := url.Parse(cfg.target)
	if err != nil {
		panic(fmt.Errorf("invalid --target: %w", err))
	}

	fmt.Printf("🔥 Start attacking %s (workers=%d, duration=%v, mode=%s)...\n",
		cfg.target, cfg.workers, cfg.duration, cfg.mode)
	if cfg.mode == "hot" {
		fmt.Printf("   hot-key=%q hot-ratio=%.2f key-space=%d\n", cfg.hotKey, cfg.hotRatio, cfg.keySpace)
	}

	// Shared client (Keep-Alive) to avoid port exhaustion.
	client := &http.Client{
		Timeout: cfg.timeout,
		Transport: &http.Transport{
			MaxIdleConns:        500,
			MaxIdleConnsPerHost: 500,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	var (
		successCount   atomic.Uint64
		failCount      atomic.Uint64
		rateLimitCount atomic.Uint64
		otherCount     atomic.Uint64
		totalCount     atomic.Uint64
	)

	latCh := make(chan time.Duration, 20000)
	var latencies []time.Duration
	var latMu sync.Mutex

	// Aggregator for latencies
	aggDone := make(chan struct{})
	go func() {
		defer close(aggDone)
		for d := range latCh {
			latMu.Lock()
			latencies = append(latencies, d)
			latMu.Unlock()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), cfg.duration)
	defer cancel()

	// Progress printer
	tickerDone := make(chan struct{})
	go func() {
		defer close(tickerDone)
		if cfg.printEvery <= 0 {
			return
		}
		t := time.NewTicker(cfg.printEvery)
		defer t.Stop()
		var last uint64
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				cur := totalCount.Load()
				delta := cur - last
				last = cur
				fmt.Printf("… progress: total=%d (%.0f req/s), 200=%d, 429=%d, fail=%d\n",
					cur,
					float64(delta)/cfg.printEvery.Seconds(),
					successCount.Load(),
					rateLimitCount.Load(),
					failCount.Load(),
				)
			}
		}
	}()

	var wg sync.WaitGroup

	// Start N attackers
	for i := 0; i < cfg.workers; i++ {
		wg.Add(1)

		// Per-worker RNG to avoid global rand lock contention.
		seed := time.Now().UnixNano() + int64(i*9973)
		rng := rand.New(rand.NewSource(seed))

		go func(workerID int, r *rand.Rand) {
			defer wg.Done()

			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				key := pickKey(cfg, r)
				u := buildURLWithKey(baseURL, cfg.keyParam, key)

				start := time.Now()
				resp, err := client.Get(u)
				lat := time.Since(start)

				totalCount.Add(1)
				select {
				case latCh <- lat:
				default:
					// If channel is full, drop latency sample to avoid blocking attacker.
				}

				if err != nil {
					failCount.Add(1)
					time.Sleep(10 * time.Millisecond)
					continue
				}

				// Read & discard body so the connection can be reused.
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()

				switch resp.StatusCode {
				case http.StatusOK:
					successCount.Add(1)
				case http.StatusTooManyRequests:
					rateLimitCount.Add(1)
				default:
					otherCount.Add(1)
				}

				if cfg.think > 0 {
					time.Sleep(cfg.think)
				}
			}
		}(i, rng)
	}

	wg.Wait()
	close(latCh)
	<-aggDone
	cancel()
	<-tickerDone

	// Compute latency percentiles
	latMu.Lock()
	sorted := make([]time.Duration, len(latencies))
	copy(sorted, latencies)
	latMu.Unlock()

	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	// p50 := percentile(sorted, 0.50)
	// p95 := percentile(sorted, 0.95)
	// p99 := percentile(sorted, 0.99)

	fmt.Println("\n\n📊 Attack report:")
	fmt.Println("--------------------------------")
	fmt.Printf("Total requests:        %d\n", totalCount.Load())
	fmt.Printf("Success (200 OK):      %d\n", successCount.Load())
	fmt.Printf("Rate limit (429):      %d\n", rateLimitCount.Load())
	fmt.Printf("Other status:          %d\n", otherCount.Load())
	fmt.Printf("Fail (Error):          %d\n", failCount.Load())
	fmt.Println("--------------------------------")
	// if len(sorted) > 0 {
	// 	fmt.Printf("Latency p50:           %v\n", p50)
	// 	fmt.Printf("Latency p95:           %v\n", p95)
	// 	fmt.Printf("Latency p99:           %v\n", p99)
	// 	fmt.Printf("Latency max:           %v\n", sorted[len(sorted)-1])
	// }
	// fmt.Println("--------------------------------")
}

func parseFlags() config {
	var cfg config

	flag.StringVar(&cfg.target, "target", "http://127.0.0.1:8080/submit", "target URL")
	flag.IntVar(&cfg.workers, "workers", 20, "number of concurrent workers")
	flag.DurationVar(&cfg.duration, "duration", 120*time.Second, "attack duration")
	flag.DurationVar(&cfg.think, "think", 100*time.Millisecond, "sleep between requests per worker (0 for max)")
	flag.DurationVar(&cfg.timeout, "timeout", 2*time.Second, "http client timeout per request")

	flag.StringVar(&cfg.mode, "mode", "random", "key mode: random | hot")
	flag.StringVar(&cfg.hotKey, "hot-key", "hot", "hot key value when mode=hot")
	flag.Float64Var(&cfg.hotRatio, "hot-ratio", 0.9, "hot key ratio (0~1) when mode=hot")

	flag.StringVar(&cfg.keyParam, "key-param", "key", "query param name for key")
	flag.IntVar(&cfg.keySpace, "key-space", 10000, "random key space size (bigger => more spread)")
	flag.DurationVar(&cfg.printEvery, "print-every", 5*time.Second, "print progress interval (0 to disable)")

	flag.Parse()

	if cfg.workers <= 0 {
		cfg.workers = 1
	}
	if cfg.hotRatio < 0 {
		cfg.hotRatio = 0
	}
	if cfg.hotRatio > 1 {
		cfg.hotRatio = 1
	}
	if cfg.keySpace <= 0 {
		cfg.keySpace = 1
	}
	if cfg.keyParam == "" {
		cfg.keyParam = "key"
	}
	if cfg.mode != "random" && cfg.mode != "hot" {
		cfg.mode = "random"
	}

	return cfg
}

func pickKey(cfg config, r *rand.Rand) string {
	switch cfg.mode {
	case "hot":
		if r.Float64() < cfg.hotRatio {
			return cfg.hotKey
		}
		// fallthrough to random
		fallthrough
	default:
		// random key within a bounded space (helps demonstrate even distribution)
		n := r.Intn(cfg.keySpace)
		return fmt.Sprintf("k-%d", n)
	}
}

func buildURLWithKey(base *url.URL, keyParam, key string) string {
	u := *base // shallow copy by value is OK here
	q := u.Query()
	q.Set(keyParam, key)
	u.RawQuery = q.Encode()
	return u.String()
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	// nearest-rank
	rank := int(float64(len(sorted)-1)*p + 0.5)
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}
