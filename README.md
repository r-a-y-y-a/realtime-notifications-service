# realtime-notifications-service

A production-quality, real-time notifications service built in Go using a multi-service architecture with Kafka, Redis, and PostgreSQL.

## Architecture

```
 Client
   │
   ├─── POST /notifications ──► [ API Service :8080 ]
   │                                     │
   │                              Kafka topic
   │                           (notification_requests)
   │                                     │
   │                         [ Processor Service ]
   │                          /           \
   │                    Postgres         Redis
   │                  (persist)    (unread counts +
   │                               Pub/Sub publish)
   │                                     │
   └─── WebSocket/SSE ──────► [ Gateway Service :8081 ]
        /ws?user_id=X              subscribes to
        /sse?user_id=X          Redis Pub/Sub channel
                                notifications:{userID}
```

## Data Flow

1. A client POSTs a notification request to the **API Service**.
2. The API validates the request, enforces per-user/tenant rate limits via Redis, and publishes a `NotificationRequest` JSON message to a Kafka topic (keyed by `user_id` for ordering).
3. The **Processor Service** consumes from Kafka with exactly-once semantics:
   - Deduplicates via a Postgres transaction using the `processed_requests` table (durable, survives Redis restarts).
   - Persists the notification to Postgres within the same transaction.
   - On failure, writes the message to a Dead Letter Queue (DLQ) for manual inspection and retry.
   - Updates Redis unread counters (best-effort) and publishes the full `Notification` JSON to a Redis Pub/Sub channel.
4. The **Gateway Service** maintains long-lived WebSocket or SSE connections. Each connection subscribes to `notifications:{userID}` on Redis and streams messages to the client in real time.

## Prerequisites

- [Docker](https://docs.docker.com/get-docker/) and [Docker Compose](https://docs.docker.com/compose/)
- `curl` (for API testing)

## Quick Start

```bash
git clone https://github.com/r-a-y-y-a/realtime-notifications-service
cd realtime-notifications-service
docker compose up --build
```

All services start automatically. The API is available at `http://localhost:8080` and the Gateway at `http://localhost:8081`.

## API Documentation

### Health Check

```bash
curl http://localhost:8080/health
# {"status":"ok"}
```

### Send a Notification

```bash
curl -X POST http://localhost:8080/notifications \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "user-123",
    "tenant_id": "tenant-abc",
    "title": "New like",
    "body": "Someone liked your post",
    "channel": "push"
  }'
# {"request_id":"...","status":"queued"}
```

`channel` can be `push`, `email`, or `sms`.

Returns `429 Too Many Requests` when the rate limit (20 requests/minute per user+tenant) is exceeded.

### List Notifications for a User

```bash
curl "http://localhost:8080/users/user-123/notifications?limit=10&offset=0"
```

Query params:
- `limit` – number of results (default 20, max 100)
- `offset` – pagination offset (default 0)

### Prometheus Metrics

```bash
curl http://localhost:8080/metrics
```

## WebSocket / SSE Usage

### WebSocket

```bash
# Using websocat
websocat "ws://localhost:8081/ws?user_id=user-123"
```

Or from a browser:
```js
const ws = new WebSocket("ws://localhost:8081/ws?user_id=user-123");
ws.onmessage = (e) => console.log(JSON.parse(e.data));
```

### Server-Sent Events

```bash
curl -N "http://localhost:8081/sse?user_id=user-123"
```

Or from a browser:
```js
const es = new EventSource("http://localhost:8081/sse?user_id=user-123");
es.onmessage = (e) => console.log(JSON.parse(e.data));
```

## Configuration

All services are configured via environment variables:

| Variable              | Default                                                                 | Description                          |
|-----------------------|-------------------------------------------------------------------------|--------------------------------------|
| `KAFKA_BROKERS`       | `kafka:9092`                                                            | Comma-separated Kafka broker list    |
| `KAFKA_TOPIC`         | `notification_requests`                                                 | Kafka topic for notifications        |
| `KAFKA_DLQ_TOPIC`     | `notification_requests_dlq`                                             | Kafka topic for failed messages (DLQ) |
| `REDIS_ADDR`          | `redis:6379`                                                            | Redis address                        |
| `POSTGRES_DSN`        | `postgres://notifications:notifications@postgres:5432/notifications?sslmode=disable` | Postgres connection string |
| `API_PORT`            | `8080`                                                                  | Port for the API service             |
| `GATEWAY_PORT`        | `8081`                                                                  | Port for the Gateway service         |
| `RATE_LIMIT_PER_MINUTE` | `20`                                                                  | Max notifications per user per minute |

## Design Decisions & Trade-offs

- **Kafka for durability**: Decouples ingestion from processing. If the processor is down, messages queue up and are processed when it recovers.
- **Kafka keyed by user_id**: Guarantees per-user message ordering without a global lock.
- **Postgres idempotency (exactly-once semantics)**: Notification insertion and idempotency check happen in a single atomic transaction using the `processed_requests` table. This is durable, survives Redis restarts, and eliminates race conditions. Failed messages are not lost even if intermediate operations fail.
- **Dead Letter Queue (DLQ)**: Processing failures write to `notification_requests_dlq` for manual inspection and retry instead of silently discarding messages. Kafka offsets are still committed to prevent infinite retry loops.
- **Redis Pub/Sub for real-time delivery**: Low-latency fan-out to connected WebSocket/SSE clients without polling Postgres. Unread counts are best-effort (updates after transaction commit).
- **Both WebSocket and SSE**: WebSocket is preferred for bidirectional use cases; SSE works across proxies/CDNs that buffer WebSocket upgrades.
- **Rate limiting via Redis Lua script**: Atomic fixed-window rate limiting with a single round-trip. Each user has a per-minute quota enforced via `INCR` and `EXPIRE`.
- **Graceful shutdown**: All services listen for SIGINT/SIGTERM, drain in-flight work, and close connections cleanly.
- **Structured logging (slog)**: JSON log lines are easy to ingest into Datadog, CloudWatch, or any log aggregator.
- **No global metrics registry**: The `/metrics` endpoint exposes a minimal counter sufficient for the demo. A real deployment would integrate `prometheus/client_golang`.
