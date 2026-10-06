# chora-creation

## About

chora-creation is the Go service for Chora's Content Creation domain. It owns LearningAtom authoring and related question, collection, question-bank, topic-tree, media, and AI-assisted authoring workflows. The service exposes HTTP/JSON and gRPC APIs, stores domain state in PostgreSQL, publishes and consumes events through NATS JetStream, and uses S3-compatible object storage for atom media and batch-upload artifacts.

## Quick start

### Prerequisites

- Go 1.26.1 or newer
- PostgreSQL
- NATS JetStream for durable event delivery
- An S3-compatible object store such as MinIO or Amazon S3
- A configured `GCS_BUCKET_BATCH_UPLOADS` and compatible object-storage credentials when using batch source-material uploads

For local development, copy the checked-in environment template and adjust service addresses for your environment:

```sh
cp .env.example .env
```

The service can start without PostgreSQL or NATS. With those settings unset, it uses in-memory repository and event-bus fallbacks intended for development and tests.

Start the service:

```sh
set -a
. ./.env
set +a
go run ./cmd/server
```

Check the HTTP health endpoints:

```sh
curl -s http://localhost:8080/healthz
curl -s http://localhost:8080/readyz
```

The default HTTP port is 8080 and the default gRPC port is 9090.

## Usage

### HTTP API

The service serves two HTTP route groups:

- `/v1/` for the Phyllis content-creation surface
- `/api/` for the atom, question, collection, question-bank, topic, media, AI-assist, and internal backfill handlers

The root endpoint returns a service banner:

```sh
curl -s http://localhost:8080/
```

Atom CRUD is available under `/api/atoms`:

```sh
curl -s http://localhost:8080/api/atoms
```

The service also exposes health endpoints at `/healthz`, `/health`, and `/readyz`.

HTTP requests are wrapped with tracing and service-mesh metadata middleware. Protected handlers use tenant and caller identity from the request context; the BFF/gateway is expected to provide the mesh metadata in the normal deployment topology.

### gRPC API

The gRPC server listens on `CHORA_GRPC_PORT` and registers:

- `chora.services.creation.v1.Creation`
- `chora.services.creation.v1.ContentRetrieval`
- the standard gRPC health service

The Creation service includes atom creation and lookup, course-scoped listing, atom-ID validation, question snapshotting, and other contract-defined RPCs. RPCs whose backing repository is not wired fail with an explicit dependency error instead of being omitted from the server.

### Configuration

Configuration is environment-driven. The main settings are:

| Variable | Purpose | Default |
|---|---|---|
| `PORT` | HTTP listen port | `8080` |
| `CHORA_GRPC_PORT` | gRPC listen port | `9090` |
| `CHORA_DB_DSN` | PostgreSQL DSN for domain repositories | unset |
| `CHORA_DB_DSN_SECRET_ID` | Secret name for the PostgreSQL DSN | unset |
| `CHORA_DB_PROJECT` | Secret-resolution project label | `chora-local` |
| `CHORA_OUTBOX_DSN` | PostgreSQL DSN for the outbox dispatcher | unset |
| `CHORA_OUTBOX_DSN_SECRET_ID` | Secret name for the outbox DSN | unset |
| `CHORA_OUTBOX_WORKER_ID` | Outbox worker identifier | `HOSTNAME` or `chora-creation-local` |
| `NATS_URL` | NATS JetStream URL | unset |
| `CHORA_SOURCE_PROJECT` | Event envelope source project | `chora-local` |
| `S3_ENDPOINT` | S3-compatible endpoint | unset |
| `S3_ACCESS_KEY_ID` | S3 access key | unset |
| `S3_SECRET_ACCESS_KEY` | S3 secret key | unset |
| `S3_REGION` | S3 region | `us-east-1` |
| `S3_FORCE_PATH_STYLE` | Force S3 path-style addressing | `false` unless set |
| `ATOM_MEDIA_BUCKET` | Durable atom-media bucket | unset |
| `GCS_BUCKET_BATCH_UPLOADS` | Batch source-material bucket | unset |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP trace endpoint | unset |
| `SVC_IDENTITY_GRPC_URL` | chora-identity gRPC target | unset |
| `SVC_SHARING_GRPC_URL` | chora-sharing gRPC target | unset |
| `SVC_MODEL_BROKER_ROUTER_URL` | model-broker-router target | unset |
| `CHORA_MODEL_GATEWAY_GRPC_URL` | model-gateway target for topic classification | unset |
| `CHORA_BACKFILL_RUN_TIMEOUT` | Topic-tag backfill run timeout | domain default when unset |
| `CHORA_BACKFILL_STALE_CUTOFF` | Stale backfill-run cutoff | domain default when unset |
| `QUESTION_JOBS_QGEN_CREW_ENABLED` | Enable qgen-crew question-job dispatch | `false` |

See `.env.example` for the complete local configuration template.

### Docker

Build the standalone service image from the repository root:

```sh
docker build -t chora-creation .
```

The image builds `./cmd/server` and listens on the same HTTP and gRPC ports as the native binary. The Dockerfile also copies `config/PII_Closure_Map.yaml` to the runtime path used by the closure-saga subscriber.

## Development

The repository is a standalone Go module:

```sh
go env GOMOD
go test ./...
```

Run the server from the repository root with:

```sh
go run ./cmd/server
```

The package layout follows the service's hexagonal structure:

```text
cmd/server/                  application entrypoint and dependency wiring
internal/domain/             domain models and business rules
internal/ports/              domain-owned adapter interfaces
internal/adapter/http/       HTTP handlers and middleware
internal/adapter/grpc/       gRPC servers
internal/adapter/pg/         PostgreSQL repositories
internal/adapter/events/     domain event publishers
internal/adapter/pubsub/     event subscribers
internal/adapter/outbox/     transactional outbox and dispatcher
internal/adapter/mediastore/ S3 presigned media URLs and durable media copy
internal/adapter/storage/    S3-compatible blob storage for batch uploads
internal/adapter/clients/    clients for dependent Chora services
internal/embedindex/         atom embedding indexing
internal/mediarehome/        durable question-image re-homing
config/                      runtime data files copied into the container
migrations/                  PostgreSQL schema migrations
```

The default test suite is:

```sh
go test ./...
```

PostgreSQL adapter integration tests are build-tagged and require a real PostgreSQL database:

```sh
export CHORA_TEST_DSN='postgres://chora:chora@localhost:5432/chora_creation?sslmode=disable'
go test -tags integration ./internal/adapter/pg/...
```

To build the production image with the same targets used by the repository's container workflow:

```sh
docker buildx build \
  --platform=linux/amd64,linux/arm64 \
  -t chora-creation .
```
