# chora-creation

Standalone Go service for the **Content Creation** domain (one of 5 core CHORA
domains). Owns the `LearningAtom` aggregate root. Cloud-neutral: PostgreSQL +
NATS JetStream + S3-compatible object storage. No Google Cloud dependencies.

| Item | Value |
|---|---|
| Module | `github.com/apollo-chora/chora-creation` |
| Surface (admin) | A+ Creator mode |
| Aggregate root | `LearningAtom` (collections query atoms; never own them) |
| Topic prefix | `chora.creation.*` |
| Database | `chora_creation` |
| Event bus | NATS JetStream (`chora.creation.*` subjects) |
| Object storage | S3-compatible (MinIO / S3) |

## Architecture

Hexagonal layout:

```
cmd/server/main.go              entrypoint, wires server, OTLP, signal handling
internal/domain/atom/           pure aggregate (no infra deps)
internal/adapter/inmem/         in-memory Repository implementation
internal/adapter/pg/            PostgreSQL repositories (pgx)
internal/adapter/http/          REST handlers + middleware
internal/adapter/grpc/          Creation gRPC service
internal/adapter/events/        event publishers + closure subscriber
internal/adapter/pubsub/        event subscribers (ai-assist, orphan, grading)
internal/adapter/outbox/        transactional outbox + dispatcher
internal/adapter/mediastore/    S3 presigned-URL atom-media signer
internal/adapter/storage/       S3 blob store (batch uploads)
internal/observability/         OTLP exporter
```

**Invariants**:
- `LearningAtom` is the primary aggregate root.
- All entities use **soft delete** (`deleted_at`) — hard delete forbidden.
- `AtomRevision` is **append-only**.
- Cross-DB queries forbidden — events only.

## Configuration

All configuration is environment-driven (see `.env.example`):

| Variable | Purpose |
|---|---|
| `PORT` | HTTP port (default 8080) |
| `CHORA_GRPC_PORT` | gRPC port (default 9090) |
| `CHORA_DB_DSN` | PostgreSQL DSN (app_rw role) |
| `CHORA_OUTBOX_DSN` | Outbox PostgreSQL DSN (defaults to same DB) |
| `NATS_URL` | NATS JetStream broker URL |
| `CHORA_SOURCE_PROJECT` | Envelope source project (default `chora-local`) |
| `S3_ENDPOINT` / `S3_ACCESS_KEY_ID` / `S3_SECRET_ACCESS_KEY` | S3-compatible storage |
| `ATOM_MEDIA_BUCKET` | Bucket for atom-media uploads |
| `GCS_BUCKET_BATCH_UPLOADS` | Bucket for batch source-material uploads |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP trace endpoint |

## Tests

```sh
go test ./...
```

Integration tests (build-tagged) run against a real PostgreSQL:

```sh
export CHORA_TEST_DSN='postgres://chora:chora@localhost:5432/chora_creation?sslmode=disable'
go test -tags integration ./internal/adapter/pg/...
```

## Run locally

```sh
go run ./cmd/server
# in another shell
curl -s http://localhost:8080/readyz
```

## Docker

```sh
docker build -t chora-creation .
docker run -p 8080:8080 -p 9090:9090 \
  -e CHORA_DB_DSN=postgres://chora:chora@host:5432/chora_creation?sslmode=disable \
  -e NATS_URL=nats://host:4222 \
  chora-creation
```
