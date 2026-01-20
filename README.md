# High Concurrency Pool (Kafka + Worker Pool + Observability Demo)

A hands-on demo project to showcase **high-concurrency backend patterns** and **distributed-system reliability**:

- **Kafka** (producer + consumer group)
- **In-process Global Worker Pool** (bounded queue + global concurrency limit)
- **Reliability**: local retry (exp backoff + jitter), **DLQ**, and **commit only on success**
- **Observability**: Prometheus + Grafana + OpenTelemetry tracing (Jaeger)

## TL;DR
- **Bounded concurrency + bounded queue** prevents overload and unbounded memory growth.
- **Commit-on-success + retry + DLQ** provides at-least-once reliability semantics.
- **Skew/hot-key** reduces effective parallelism → lag becomes concentrated and recovery slows (observable in Grafana).

---

## What you can demo in 10–15 minutes

1) **Kafka pipeline**
- HTTP API → Producer → `jobs_topic` → Consumer Group → local processing pipeline  
**Key takeaway:** decoupled ingestion and processing via Kafka.

2) **Reliability**
- Commit only on success (no silent loss)
- Local retry (exp backoff + jitter)
- DLQ on final failure
- If DLQ publish fails ⇒ **do NOT commit**, message will be replayed  
**Key takeaway:** at-least-once processing with safe failure handling.

3) **Backpressure & resource governance**
- Global concurrency cap (`WORKERS`) protects CPU/DB/external APIs
- Bounded queue (`QUEUE_SIZE`) provides backpressure and prevents OOM  
**Key takeaway:** overload becomes **observable backlog** instead of resource meltdown.

4) **Hot-key / hot-partition skew**
- A skewed key can create a hot partition (lag concentrated on a few partitions)
- Other partitions may still progress depending on wiring and available capacity  
**Key takeaway:** skew reduces effective parallelism and slows recovery (visible via lag by partition).

5) **Observability**
- Metrics: throughput / error rate / latency / in-flight / queue length / lag
- Tracing: end-to-end trace across HTTP → Kafka produce → Kafka consume → handler → retry/DLQ  
**Key takeaway:** you can explain “why” from graphs + traces, not only “what”.

---

## Architecture

```
Client (/submit)
   |
   v
Gin HTTP API --(rate limit: Redis token bucket)--> Kafka Producer (trace injected into headers)
   |
   v
Kafka Topic: jobs_topic (20 partitions)
   |
   v
Kafka Consumer Group: worker_group_1
   |
   v
Global Worker Pool (bounded queue + global concurrency limit)
   |
   v
Job Processor (simulated work + occasional failure)
   |
   +--> local retry (exp backoff + jitter)
   +--> DLQ (<topic>-dlq) with debug headers
```

---

## Prerequisites
- Docker + Docker Compose
- Go **1.24+**

---

## Quick Start

### 1) Start infra (Kafka / Redis / Prometheus / Grafana / Jaeger)
```bash
docker compose up -d
```

| Component | URL |
|---|---|
| App | http://localhost:8080 |
| Prometheus | http://localhost:9090 |
| Grafana | http://localhost:3000 (admin / admin) |
| Jaeger UI | http://localhost:16686 |

> Note (Linux): `monitoring/prometheus.yml` scrapes `host.docker.internal:8080`.  
> On Linux Docker Engine, enable it via `extra_hosts` or change the Prometheus target to your host IP / container network.

### 2) Run the service locally (recommended for default Prometheus config)
```bash
# benchmark config used below
WORKERS=10 QUEUE_SIZE=20 go run .
# server listens on http://localhost:8080
```

### 3) Enqueue a job
```bash
curl -s http://localhost:8080/submit | jq
```

### 4) Load test (built-in attacker)
```bash
# baseline (random)
go run ./attacker -mode=random -workers=30 -duration=120s -think=200ms -print-every=5s

# hot-key (50%)
go run ./attacker -mode=hot -hot-key=hot -hot-ratio=0.5 -workers=30 -duration=120s -think=200ms -print-every=5s
```

---

## Endpoints
- `GET /submit`  
  Enqueues a `SimpleJob` into Kafka and returns a `trace_id` for Jaeger lookup.
- `GET /metrics`  
  Prometheus metrics endpoint.
- `GET /debug/pprof/`  
  pprof (Gin pprof)

---

## Observability

### Metrics (Prometheus / Grafana)

Prometheus is configured in `monitoring/prometheus.yml`:
- `go_worker_pool`: `host.docker.internal:8080/metrics`
- `kafka_exporter`: `kafka-exporter:9308`

App metrics:
- namespace: `my_system`
- subsystem: `worker_pool`

Key metrics:
- Producer
  - `my_system_worker_pool_producer_sent_total`
  - `my_system_worker_pool_producer_errors_total`
  - `my_system_worker_pool_producer_duration_seconds` (histogram)
- Consumer / Worker Pool
  - `my_system_worker_pool_consumer_processed_total` *(processed & committed on success)*
  - `my_system_worker_pool_consumer_errors_total`
  - `my_system_worker_pool_consumer_duration_seconds` (histogram) *(service time)*
  - `my_system_worker_pool_consumer_queue_length` (gauge)
  - `my_system_worker_pool_consumer_inflight` (gauge)
- Rate limit
  - `my_system_worker_pool_rate_limited_total`

#### Useful PromQL snippets

Throughput:
```promql
sum(rate(my_system_worker_pool_consumer_processed_total[1m]))
```

Errors:
```promql
sum(rate(my_system_worker_pool_consumer_errors_total[1m]))
```

p95 / p99 processing latency (service time):
```promql
histogram_quantile(0.95, sum(rate(my_system_worker_pool_consumer_duration_seconds_bucket[5m])) by (le))
```

Queue / in-flight:
```promql
my_system_worker_pool_consumer_queue_length
my_system_worker_pool_consumer_inflight
```

Rate limit:
```promql
sum(rate(my_system_worker_pool_rate_limited_total[1m]))
```

Kafka lag (sum across partitions):
```promql
sum(kafka_consumergroup_lag{consumergroup="worker_group_1", topic="jobs_topic"})
```

Lag by partition (visualize skew):
```promql
kafka_consumergroup_lag{consumergroup="$group", topic="$topic"}
```

---

### Tracing (OpenTelemetry / Jaeger)
- HTTP: `otelgin.Middleware("workerpool")`
- Redis token bucket: `redisotel` instrumentation
- Producer injects trace context into Kafka headers
- Consumer extracts trace context and continues the trace
- Local retry adds event: `local-retry-scheduled`
  - attributes: `next_attempt`, `sleep_ms`, `error`

How to view:
1. `curl /submit` and copy `trace_id`
2. Open Jaeger: http://localhost:16686
3. Search by Trace ID

---

## Demo script (10–15 minutes)

### Demo 0 — Sanity check (1 min)
- `docker compose ps`
- `curl -s http://localhost:8080/metrics | head`
- Prom targets: http://localhost:9090/targets
- Grafana: http://localhost:3000
- Jaeger: http://localhost:16686

### Demo 1 — Tracing (2–4 min)
- Call `/submit` → copy trace_id → show end-to-end spans and retry/DLQ events.

### Demo 2 — Backpressure (3–5 min)
- Run baseline attacker.
- In Grafana:
  - `consumer_inflight` should hover near `WORKERS`
  - `consumer_queue_length` grows under pressure  
Explain: bounded concurrency + queue prevents overload.

### Demo 3 — Hot-key / Hot-partition skew (2–4 min)
- Run hot-key attacker.
- In Grafana:
  - lag by partition shows skew concentration
  - recovery time increases after load stops

### Demo 4 — Reliability (Retry + DLQ) (3–5 min)
Consume DLQ from beginning:
```bash
docker exec -it kafka bash
kafka-console-consumer --bootstrap-server kafka:29092 --topic jobs_topic-dlq --from-beginning
```

### Demo 5 — Rebalance (optional, 3–6 min)
- Start a second instance:
```bash
HTTP_ADDR=:8081 WORKERS=10 QUEUE_SIZE=20 go run .
```
Observe:
- members: `kafka_consumergroup_members{consumergroup="$group"}`
- lag reshuffle by partition
- possible at-least-once replay during rebalance (expected)

---

## Configuration knobs
- Kafka partitions: `KAFKA_PARTITIONS` (default: 20)
- Worker pool:
  - `WORKERS` (default: 10)
  - `QUEUE_SIZE` (default: 20)
- Rate limit:
  - `LIMIT_RATE` (tokens/sec)
  - `LIMIT_CAPACITY` (burst capacity)
- Local retry:
  - `maxRetries` (default: 3)
  - `baseBackoff` (default: 40ms)
  - `maxBackoff` (default: 400ms)

---

## Repo structure
```
.
├── main.go
├── middleware.go
├── attacker/
├── kafka/
├── workerpool/
├── ratelimiter/
├── telemetry/
├── monitoring/
└── docker-compose.yaml
```

---

## Benchmark Snapshot

### Environment
- **Machine**: i7-13700E / 64GB / Windows 11
- **Setup**: docker-compose (Kafka, Redis, Prometheus, Grafana, Jaeger) + Go app on `:8080`
- **Benchmark config**: `WORKERS=10`, `QUEUE_SIZE=20`
- **Attacker**: `-workers=30 -duration=120s -think=200ms`

### Results (I/O-bound workload)
> Workload is I/O-bound (simulated via sleep).  
> p95/p99 reflects processing (service) time; skew mainly impacts Kafka backlog (lag) and recovery time.

| Scenario | Producer RPS (avg) | Consume RPS (avg) | p95 (ms) | p99 (ms) | Inflight avg | Queue avg | Lag peak | Lag converge (min, to lag<10) |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| random (baseline) | ~20 | ~18 | ~974 | ~994 | ~9.16 | ~8.46 | ~3114 | ~3.25 |
| hot-key (50%) | ~20 | ~5.0 | ~969 | ~993 | ~8.09 | ~4.14 | ~3185 | ~27.25 |

**One-line takeaway:** at ~20 RPS input, hot-key skew (50%) reduced throughput ~18 → ~5 RPS and increased lag recovery ~3.25 → ~27.25 min.

#### Resource Cost (reference, 5m avg)
| Scenario | CPU (cores, avg) | RSS (MiB, avg) |
|---|---:|---:|
| random (baseline) | ~0.161 | ~56 |
| hot-key (50%) | ~0.07 | ~59 |

### Reproduce
```bash
docker compose up -d
WORKERS=10 QUEUE_SIZE=20 go run .

# baseline
go run ./attacker -mode=random -workers=30 -duration=120s -think=200ms -print-every=5s

# hot-key (50%)
go run ./attacker -mode=hot -hot-key=hot -hot-ratio=0.5 -workers=30 -duration=120s -think=200ms -print-every=5s
```

### Query metrics via curl (Prometheus HTTP API)
```bash
PROM="http://localhost:9090"

# Producer RPS (5m avg)
curl -sG "$PROM/api/v1/query" --data-urlencode 'query=sum(rate(my_system_worker_pool_producer_sent_total[5m]))' | jq -r '.data.result[0].value[1]'

# Consume RPS (5m avg)
curl -sG "$PROM/api/v1/query" --data-urlencode 'query=sum(rate(my_system_worker_pool_consumer_processed_total[5m]))' | jq -r '.data.result[0].value[1]'

# p95 / p99 processing latency (ms)
curl -sG "$PROM/api/v1/query" --data-urlencode 'query=1000*histogram_quantile(0.95, sum(rate(my_system_worker_pool_consumer_duration_seconds_bucket[5m])) by (le))' | jq -r '.data.result[0].value[1]'

curl -sG "$PROM/api/v1/query" --data-urlencode 'query=1000*histogram_quantile(0.99, sum(rate(my_system_worker_pool_consumer_duration_seconds_bucket[5m])) by (le))' | jq -r '.data.result[0].value[1]'

# Inflight avg / Queue avg (5m avg)
curl -sG "$PROM/api/v1/query" --data-urlencode 'query=avg_over_time(my_system_worker_pool_consumer_inflight[5m])' | jq -r '.data.result[0].value[1]'

curl -sG "$PROM/api/v1/query" --data-urlencode 'query=avg_over_time(my_system_worker_pool_consumer_queue_length[5m])' | jq -r '.data.result[0].value[1]'

# Lag peak (5m peak)
curl -sG "$PROM/api/v1/query" --data-urlencode 'query=max_over_time((sum(kafka_consumergroup_lag{consumergroup="worker_group_1", topic="jobs_topic"}))[5m:15s])' | jq -r '.data.result[0].value[1]'
```

---

## Notes
- Global pool submit is **synchronous** (blocks until the job finishes) to preserve ordering in the consumer handler while limiting global concurrency.
- To run the app in Docker instead of local:
  - `docker build -t workerpool-demo .`
  - `docker run -p 8080:8080 workerpool-demo`
  - Update Prometheus scrape target accordingly.

---

## License
MIT