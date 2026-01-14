# Attacker (Load Generator)

A simple load generator for the **High Concurrency Pool** demo.  
It sends concurrent HTTP requests to the app `/submit` endpoint and supports:

- **random key mode**: distribute messages across partitions
- **hot key mode**: intentionally create **partition skew / hot key** to demonstrate lag, backpressure, and p99 latency changes
- basic **status counters** (200 / 429 / errors) and **latency percentiles** (p50/p95/p99)

> **Important:** This attacker assumes the server supports `GET /submit?key=<value>` and uses the query `key` as the Kafka message key.

---

## Quick Start

### Run (default)
```bash
go run ./attacker
```

### Random key (even-ish distribution)
```bash
go run ./attacker \
  -mode=random \
  -workers=50 \
  -duration=60s \
  -think=20ms
```

### Hot key (simulate skew)
90% of requests use the same key (default `"hot"`), forcing most messages into one partition.
```bash
go run ./attacker \
  -mode=hot \
  -hot-key=hot \
  -hot-ratio=0.9 \
  -workers=50 \
  -duration=60s \
  -think=20ms
```

---

## Demo Playbook (10 minutes)

### Step 1 — Baseline (random keys)
```bash
go run ./attacker -mode=random -workers=50 -duration=60s -think=20ms
```

In Grafana, observe:
- consumer throughput (processed/sec)
- queue length / in-flight
- consumer group lag (kafka-exporter)
- p95/p99 latency (app metrics)

Expected:
- lag is stable or drains after load
- no single partition dominates (if you have partition-level metrics)

### Step 2 — Hot key (skew)
```bash
go run ./attacker -mode=hot -hot-key=hot -hot-ratio=0.9 -workers=50 -duration=60s -think=20ms
```

Expected:
- lag rises faster (one partition becomes a bottleneck)
- queue length grows, in-flight caps at worker limit (backpressure)
- p99 latency increases
- tracing shows more queue wait / retry / DLQ behaviors (depending on failure rate)

---

## Flags

| Flag | Default | Description |
|---|---:|---|
| `-target` | `http://127.0.0.1:8080/submit` | Target URL |
| `-workers` | `20` | Number of concurrent goroutines |
| `-duration` | `120s` | Total run duration |
| `-think` | `100ms` | Sleep between requests per worker (set `0` for max load) |
| `-timeout` | `2s` | HTTP timeout per request |
| `-mode` | `random` | `random` or `hot` |
| `-hot-key` | `hot` | Hot key value (only for `mode=hot`) |
| `-hot-ratio` | `0.9` | Ratio of requests using hot key (`0~1`) |
| `-key-param` | `key` | Query param name for key |
| `-key-space` | `10000` | Random key space size (larger => more spread) |
| `-print-every` | `5s` | Progress print interval (`0` disables) |

---

## Output

The attacker prints a final report:

- total requests
- counts by status code:
  - `200` (OK)
  - `429` (rate limited)
  - errors / other status
- latency:
  - p50 / p95 / p99
  - max

Example:
```
📊 Attack report:
--------------------------------
Total requests:        120000
Success (200 OK):      80000
Rate limit (429):      30000
Other status:          1000
Fail (Error):          9000
--------------------------------
Latency p50:           18ms
Latency p95:           120ms
Latency p99:           300ms
Latency max:           1.8s
--------------------------------
```

---

## Notes / Troubleshooting

### 1) Connection reuse (Keep-Alive)
This attacker uses a shared `http.Client` and drains response bodies to avoid port exhaustion.

### 2) Server must support `?key=...`
If your `/submit` handler does not accept `key`, either:
- update server to read `key` from query string and set Kafka message key, or
- set `-key-param` to match your server param name.

### 3) Rate limit is expected
If you enabled Redis token bucket rate limiting, `429` is normal under high load.
Use `-think` to control pressure.

### 4) Make skew more dramatic
Increase `workers`, reduce `think`, or increase `hot-ratio`:
```bash
go run ./attacker -mode=hot -hot-ratio=0.95 -workers=200 -think=0 -duration=30s
```

---

## Recommended Grafana Panels (for demo)

- **Throughput:** `rate(app_processed_total[1m])`
- **In-flight:** `app_inflight_global`
- **Queue length:** `app_queue_length`
- **Lag:** `sum(kafka_consumergroup_lag_sum{consumergroup="$group", topic="$topic"})`
- **Latency p99:** `histogram_quantile(0.99, rate(app_duration_seconds_bucket[5m]))`

(Replace metric names with your project’s actual names.)
