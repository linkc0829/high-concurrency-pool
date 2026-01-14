# High Concurrency Pool (Kafka + Worker Pool + Observability Demo)

A hands-on demo project to showcase **high-concurrency backend patterns** and **distributed-system reliability** using:

- **Kafka** (producer + consumer group)
- **In-process Global Worker Pool** (bounded queue + global concurrency limit)
- **Reliability semantics**: local retry with exponential backoff + jitter, **DLQ**, and “commit only on success”
- **Observability**: Prometheus metrics + Grafana dashboards + OpenTelemetry tracing (Jaeger)

---

## What you can demo in 10–15 minutes

1. **Kafka pipeline**
   - HTTP API → Producer → `jobs_topic` → Consumer Group → local processing pipeline

2. **Reliability**
   - Success-only commit (no message loss)
   - Local retry (exp backoff + jitter)
   - DLQ on final failure
   - DLQ publish failure ⇒ **do NOT commit**, message will be replayed

3. **Backpressure / resource governance (and hot-key isolation)**
   - Global concurrency is capped (worker pool workers): protects CPU/DB/external APIs.
   - Bounded queue provides backpressure (prevents unbounded memory growth).
   - **Hot key / hot partition isolation:** a skewed key can create a *hot partition* (lag spikes on one partition),
     but the global pool prevents the system from being overwhelmed.
     In a healthy design, other partitions can still make progress while the hot partition accumulates lag
     (how much isolation you get depends on the handler/worker-pool wiring and available worker capacity).

4. **Observability**
   - Metrics: throughput / error rate / latency / in-flight / queue length / rate limit
   - Tracing: end-to-end trace across HTTP → Kafka produce → Kafka consume → handler → retry/DLQ

---

## Architecture

```
Client (/submit)
   |
   v
Gin HTTP API  --(rate limit: Redis token bucket)-->  Kafka Producer (trace injected into headers)
   |
   v
Kafka Topic: jobs_topic  (20 partitions)
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

Services:

| Component | URL |
|---|---|
| App | http://localhost:8080 |
| Prometheus | http://localhost:9090 |
| Grafana | http://localhost:3000 (admin / admin) |
| Jaeger UI | http://localhost:16686 |

> Note (Linux): `monitoring/prometheus.yml` scrapes `host.docker.internal:8080`.
> On Linux Docker Engine, you may need to enable it (see `docker-compose.yaml` comments for `extra_hosts`)
> or change the Prometheus target to your host IP / container network.

### 2) Run the service locally (recommended for the default Prometheus config)

```bash
go run .
# server listens on http://localhost:8080
```

### 3) Enqueue a job (Kafka produce)

```bash
curl -s http://localhost:8080/submit | jq
```

Example response:

```json
{
  "message": "Job Enqueued to Kafka",
  "trace_id": "...."
}
```

### 4) Load test (built-in attacker)

```bash
go run ./attacker
```

The attacker will generate concurrent requests to `/submit` and print a summary.

---

## Endpoints

- `GET /submit`  
  Enqueues a `SimpleJob` into Kafka and returns a `trace_id` for Jaeger lookup.

- `GET /metrics`  
  Prometheus metrics endpoint.

- `GET /debug/pprof/`：pprof（Gin pprof）

---

## Observability

### Metrics (Prometheus / Grafana)

Prometheus is configured in `monitoring/prometheus.yml`:

- `go_worker_pool`: `host.docker.internal:8080/metrics`
- `kafka_exporter`: `kafka-exporter:9308`

The app exports Prometheus metrics with:
- namespace: `my_system`
- subsystem: `worker_pool`

Key metrics:
- Producer
   - `my_system_worker_pool_producer_sent_total`
   - `my_system_worker_pool_producer_errors_total`
   - `my_system_worker_pool_producer_duration_seconds` (histogram)
- Consumer / Worker Pool
   - `my_system_worker_pool_consumer_processed_total`
   - `my_system_worker_pool_consumer_errors_total`
   - `my_system_worker_pool_consumer_duration_seconds` (histogram)
   - `my_system_worker_pool_consumer_queue_length` (gauge)
   - `my_system_worker_pool_consumer_inflight` (gauge)
- Rate limit
   - `my_system_worker_pool_rate_limited_total`

#### Useful PromQL snippets

Throughput (processed/sec):
```promql
sum(rate(my_system_worker_pool_consumer_processed_total[1m]))
```

Consumer error rate:
```promql
sum(rate(my_system_worker_pool_consumer_errors_total[1m]))
```

p95 / p99 processing latency:
```promql
histogram_quantile(0.95, sum by (le) (rate(my_system_worker_pool_consumer_duration_seconds_bucket[5m])))
```

Queue length / in-flight:
```promql
my_system_worker_pool_consumer_queue_length
my_system_worker_pool_consumer_inflight
```

Rate limit:
```promql
sum(rate(my_system_worker_pool_rate_limited_total[1m]))
```

Kafka consumer group lag (kafka-exporter):
```promql
sum(kafka_consumergroup_lag_sum{consumergroup="worker_group_1", topic="jobs_topic"})
```

Kafka lag by partition (how many messages are not processed yet):
```promql
kafka_consumergroup_lag{consumergroup="$group", topic="$topic"}
```

#### Grafana variables (optional)

If you want a reusable dashboard with variables:

- `group`:
```promql
label_values(kafka_consumergroup_lag_sum, consumergroup)
```

- `topic` (depends on group):
```promql
label_values(kafka_consumergroup_lag_sum{consumergroup="$group"}, topic)
```

For multi/all selections, use regex:
- `consumergroup=~"$group"`
- `topic=~"$topic"`

---

### Tracing (OpenTelemetry / Jaeger)

- The HTTP layer is instrumented by `otelgin.Middleware("workerpool")`
- Redis（token bucket， `redisotel` instrumentation）
- Kafka producer injects trace context into Kafka headers
- Kafka consumer extracts trace context and continues the trace
- Local retry schedules an event on the span: `local-retry-scheduled`
  - attributes: `next_attempt`, `sleep_ms`, `error`

**How to view in Jaeger:**

1. Call `/submit`, copy the returned `trace_id`
2. Open Jaeger UI: http://localhost:16686
3. Search by Trace ID
4. Expand spans:
   - `json_unmarshal`
   - `heavy_calculation`
   - retry events / errors
   - DLQ publish span (if triggered)

---

## Demo script (10–15 minutes)

### Demo 0 — Sanity check (1 min)
- `docker compose ps`
- App: `curl -s http://localhost:8080/metrics | head`
- Prometheus Targets: http://localhost:9090/targets
- Grafana: http://localhost:3000
- Jaeger: http://localhost:16686

### Demo 1 — Tracing (2–4 min)
1. `curl -s http://localhost:8080/submit | jq`
2. Copy `trace_id`
3. In Jaeger, search by Trace ID
4. Show:
   - end-to-end trace
   - `local-retry-scheduled` event (with next_attempt/sleep_ms/error)
   - error spans if the simulated job fails

### Demo 2 — Backpressure (3–5 min)
1. Start load: `go run ./attacker`
2. In Grafana, show:
   - `my_system_worker_pool_consumer_inflight` (should cap near worker count)
   - `my_system_worker_pool_consumer_queue_length` (grows under pressure)
3. Explain: global pool protects downstream by bounding concurrency + queue.


### Demo 2.5 — Hot Key / Hot Partition Isolation (2–4 min)
1. Run the attacker in **hot** mode (skew most requests to the same key):
   ```bash
   go run ./attacker -mode=hot -hot-key=hot -hot-ratio=0.9 -workers=20 -duration=60s -think=100ms
   ```
2. In Grafana, show:
   - `my_system_worker_pool_consumer_inflight` (should cap near worker count)
   - `my_system_worker_pool_consumer_queue_length` (grows under pressure)
   - `kafka_consumergroup_lag{consumergroup="$group", topic="$topic"}`(should see partition skew)
3. Explain: global pool protects downstream by bounding concurrency + queue.


### Demo 3 — Reliability (Retry + DLQ) (3–5 min)
The job processor randomly fails when processing time exceeds a threshold, which triggers:
- local retry (exp backoff + jitter)
- final failure → DLQ

Inspect DLQ messages (including debug headers like origin topic/partition/offset and final error):

```bash
# enter kafka container
docker exec -it kafka bash

# consume DLQ from beginning (prints values; headers are included in produce stage)
kafka-console-consumer --bootstrap-server kafka:29092 --topic jobs_topic-dlq --from-beginning
```

### Demo 4 — Lag (optional, 2–3 min)
1. Under load, show lag rising:
   - `kafka_consumergroup_lag_sum`
2. After stopping attacker, show lag draining.

---

## Configuration knobs

- Kafka partitions: `KAFKA_PARTITIONS` (default: 20)
- Worker pool:
  - `WORKERS` (global concurrency limit, default: 10)
  - `QUEUE_SIZE` (bounded queue size, default: 100)
- Rate limit:
  - `LIMIT_RATE` (tokens/sec)
  - `LIMIT_CAPACITY` (burst capacity)
- Local retry (in Kafka consumer):
  - `maxRetries` (default: 3)
  - `baseBackoff` (default: 40ms)
  - `maxBackoff` (default: 400ms)

---

## Repo structure

```
.
├── main.go               # Gin API + wiring + job processor
├── middleware.go         # Rate limit middleware (Redis token bucket)
├── attacker/             # Simple load generator
├── kafka/                # Sarama producer/consumer + topic helper + tracing propagation
├── workerpool/           # Global pool (bounded) + Kafka worker pool wrapper + Prom metrics
├── ratelimiter/          # Redis token bucket (Lua script)
├── telemetry/            # OTel tracer initialization
├── monitoring/           # Prometheus config + Grafana datasource/dashboard exports
└── docker-compose.yaml   # Kafka/ZK/Redis/Prometheus/Grafana/Jaeger
```

---

## Notes

- By design, the global pool Submit is **synchronous** (blocks until the job finishes).  
  This preserves per-partition ordering (when used in the Kafka consumer handler) while still limiting global concurrency.

- If you want to run the app in Docker instead of local:
  - Run `docker build -t workerpool-demo .` and `docker run -p 8080:8080 workerpool-demo`
  - Update Prometheus scrape target accordingly (container network or host gateway).

---

## License

MIT