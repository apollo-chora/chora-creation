# chora-creation

> The Content Creation domain: LearningAtom authoring, questions, collections,
> question banks, the topic tree, media, and AI-assisted authoring.

Go, HTTP/JSON plus gRPC. PostgreSQL for domain state, a transactional outbox to NATS
JetStream for events, and S3-compatible object storage for atom media and batch
uploads.

## What it is

This is where content is authored. The service owns six areas:

- **Atoms** — LearningAtom CRUD, variants, revisions, clone provenance, orphan and
  reuse handling.
- **Questions** — question authoring, question jobs, item-bank and snapshot
  behaviour, revision history.
- **Collections and question banks** — grouping, audience, exam embargo.
- **Topics** — the topic tree, tagging, and the backfill runner that re-tags history.
- **Media** — presigned URLs for atom media, and re-homing of generated images from
  the transient bucket into the durable one.
- **AI-assisted authoring** — the job surface behind the authoring canvas, which
  dispatches to the `qgen` crew rather than calling a model directly.

It serves HTTP on `PORT` (default `8080`) and gRPC on `CHORA_GRPC_PORT` (default
`9090`). In the stack both are `expose`d and neither is published to the host.

### The gRPC surface

The server registers `chora.services.creation.v1.Creation`,
`chora.services.creation.v1.ContentRetrieval`, and the standard gRPC health service.
Two different "not available" signals are in play, and they mean different things. Some
RPCs are explicitly marked `UNIMPLEMENTED` — `GenerateAtomsViaAI`, `AppendRevision` and
`QueryKnowledgeGraph` among them — while others are registered and return an explicit
dependency error because their repository is not wired. Read the status detail before
concluding the service is broken.

### Question routes fail loud without a database

Atoms have an in-memory fallback for local development. **The question authoring
repository does not.** With no pgx pool the question handler still mounts, but its
repository is nil and those routes answer `503 CREATION_QUESTIONS_NOT_WIRED`. That is
deliberate — there is no in-memory question store — so the 503 means "no database",
not "feature missing".

Other route families fail on their own gates rather than this one: question **jobs**
also need `SVC_IDENTITY_GRPC_URL`, question-bank and collection routes are wired
separately and can `404` when their repositories are absent, media signing `503`s
without `ATOM_MEDIA_BUCKET`, and batch uploads `503` without
`GCS_BUCKET_BATCH_UPLOADS`. One 503 does not diagnose them all.

## Prerequisites

| Route | What you need |
| --- | --- |
| In the stack | A working `chora-stack` deployment. PostgreSQL, NATS, Garage and the model gateway come from it. |
| Standalone (atoms only) | Go **1.26.1 or newer**. No database, no broker — in-memory repositories and an in-process bus. |
| Standalone (full) | Plus PostgreSQL, NATS JetStream, and an S3-compatible store for media and batch uploads. |
| Buckets | `ATOM_MEDIA_BUCKET`, `ATOM_MEDIA_TRANSIENT_BUCKET`, `GCS_BUCKET_BATCH_UPLOADS` and the content-media bucket must exist before media and upload features work. In the stack `garage-init` creates all four. |

## Credentials

This service holds **no model-provider credentials** — it asks the model gateway, and
the gateway owns the provider keys. What it needs:

| Value | Where it comes from | Variable |
| --- | --- | --- |
| PostgreSQL password | the stack's `POSTGRES_PASSWORD`, carried into the DSN as `chora_creation_app_rw` | `CHORA_DB_DSN`, `CHORA_OUTBOX_DSN` |
| Object-storage credentials | the stack's Garage key and secret | `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY` (and the `S3_ACCESS_KEY` / `S3_SECRET_KEY` aliases Compose also sets) |
| NATS endpoint | the stack's NATS service; no credential locally | `NATS_URL` |
| Embedding model id | the stack's `.env` — required by **Compose**, not read by this service | `EMBEDDING_LOGICAL_MODEL_ID` |

`EMBEDDING_LOGICAL_MODEL_ID` is a Compose interpolation requirement
(`${EMBEDDING_LOGICAL_MODEL_ID:?...}`), so the stack will not run without it — but the
name appears in this repository only inside comments that record its removal ("No
EMBEDDING_LOGICAL_MODEL_ID: the gateway resolves the embeddings"). Set it in
`chora-stack/.env`; do not expect this service to consume it.

The provider keys behind the gateway's model and embedding routes are documented once
in [`chora-stack/README.md`](../chora-stack/README.md#credentials).

**One Compose variable nothing reads.** `compose.yml` also sets
`CHORA_CREATION_DB_DSN`, but `bootstrapDBPool` reads only `CHORA_DB_DSN` and
`CHORA_DB_DSN_SECRET_ID`; the one reference to the other name is a stale comment in
`cmd/server/main.go`. Setting it to something different from `CHORA_DB_DSN` has no
effect.

## Run it in the stack

`compose.yml` does four things:

1. Builds the image from the sibling checkout at `../chora-creation`.
2. Runs `creation-migrate` first — a `postgres:18.6-alpine` one-shot applying this
   repository's migrations to `chora_creation`.
3. Starts the service once `postgres` and `nats` are healthy, `nats-init` and
   `garage-init` have completed, `garage` is healthy, the migrate one-shot completed,
   and `model-gateway` has started.
4. Publishes no host port; it `expose`s `8080` and `9090`.

```sh
cd chora-stack
docker compose up -d creation
docker compose ps creation
```

What Compose sets beyond the credentials, and why it matters:

| Variable | Stack value | Note |
| --- | --- | --- |
| `CHORA_MODEL_GATEWAY_GRPC_URL` | `model-gateway:9090` | topic classification and embeddings |
| `QUESTION_JOBS_QGEN_CREW_ENABLED` | `true` | question jobs dispatch to the qgen crew instead of the retired Vertex path |
| `ATOM_MEDIA_BUCKET` | `chora-atom-media` | the durable home for atom images |
| `ATOM_MEDIA_TRANSIENT_BUCKET` | `chora-ai-assist-images` | where generated images land first |
| `ATOM_MEDIA_TRUSTED_PUBLIC_ORIGIN` | `${CHORA_S3_PUBLIC_ENDPOINT:-}` | the only origin whose `http(s)` refs are accepted when re-homing |
| `GCS_BUCKET_BATCH_UPLOADS` | `chora-batch-uploads` | batch source-material uploads |
| `S3_PUBLIC_ENDPOINT` | `${CHORA_S3_PUBLIC_ENDPOINT:-}` | used only to **mint** presigned URLs; empty keeps the internal endpoint |
| `SVC_IDENTITY_GRPC_URL`, `SVC_SHARING_GRPC_URL` | `identity:9090`, `sharing:9090` | dependency clients |

`QUESTION_JOBS_QGEN_CREW_ENABLED` only enables the dispatching half. The receiving
crew lane also has to be on in `chora-ai-kernel-orchestrator` (`QGEN_CREW_ENABLED`),
which the stack sets — so the pair works there, and setting only one elsewhere leaves
question jobs dispatched to nobody.

Probe it from inside the network:

```sh
docker compose exec -T nats wget -qO- http://creation:8080/readyz
```

## Run it standalone

```sh
git clone https://github.com/apollo-chora/chora-creation.git
cd chora-creation
cp .env.example .env
set -a
. ./.env
set +a
go run ./cmd/server
```

Note the `set -a; . ./.env; set +a` — **this service does not load `.env` itself**; that
incantation exports the file into the process environment first. Skipping it means the
service sees no configuration and silently runs on in-memory repository fallbacks.

`.env.example` points at the Compose service names (`postgres`, `nats`, `garage`), so
change them for a host-side run. With no database and no broker the service still
starts, but only the atom surfaces work — questions return
`503 CREATION_QUESTIONS_NOT_WIRED`, and no event leaves the process.

Build, test, container:

```sh
go build ./...
go test ./...

CHORA_TEST_DSN='postgres://chora:chora@localhost:5432/chora_creation?sslmode=disable' \
  go test -tags integration ./internal/adapter/pg/...

docker build -t chora-creation .
```

The image copies `config/PII_Closure_Map.yaml` to the runtime path the closure-saga
subscriber expects. The multi-arch build the CI workflow uses:

```sh
docker buildx build --platform=linux/amd64,linux/arm64 -t chora-creation .
```

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `PORT` | `8080` | HTTP listen port |
| `CHORA_GRPC_PORT` | `9090` | gRPC listen port |
| `CHORA_DB_DSN` | unset | PostgreSQL DSN for domain repositories; unset selects in-memory for atoms |
| `CHORA_DB_DSN_SECRET_ID` | unset | secret name resolved to the DSN |
| `CHORA_DB_PROJECT` | `chora-local` | project label for secret resolution |
| `CHORA_OUTBOX_DSN` | unset | PostgreSQL DSN for the outbox dispatcher |
| `CHORA_OUTBOX_DSN_SECRET_ID` | unset | secret name for the outbox DSN |
| `CHORA_OUTBOX_WORKER_ID` | `HOSTNAME`, else `chora-creation-local` | outbox worker identity |
| `NATS_URL` | unset | NATS JetStream URL |
| `CHORA_SOURCE_PROJECT` | `chora-local` | event envelope source project |
| `EMBEDDING_LOGICAL_MODEL_ID` | **required by Compose** | not read by this service; see [Credentials](#credentials) |
| `CHORA_MODEL_GATEWAY_GRPC_URL` | unset | model gateway for topic classification and embeddings |
| `QUESTION_JOBS_QGEN_CREW_ENABLED` | `false` | dispatch question jobs to the qgen crew |
| `S3_ENDPOINT` | unset | S3-compatible endpoint |
| `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY` | unset | object-store credentials |
| `S3_ACCESS_KEY`, `S3_SECRET_KEY` | unset | aliases Compose also sets; keep them consistent with the pair above |
| `S3_REGION` | `us-east-1` | signing region |
| `S3_FORCE_PATH_STYLE` | off | required by Garage; the stack sets it |
| `S3_PUBLIC_ENDPOINT` | unset (empty = keep internal) | host origin used only to mint presigned URLs |
| `CHORA_CONTENT_MEDIA_BUCKET` | unset (empty → stub signer) | the content-media bucket `main.go` actually reads (stack: `chora-content-media`) |
| `S3_BUCKET` | unset (stack: `chora-content-media`) | set by Compose alongside the above, but **not** read by `main.go` — do not rely on it alone |
| `ATOM_MEDIA_BUCKET` | unset | durable atom-media bucket |
| `ATOM_MEDIA_TRANSIENT_BUCKET` | unset | transient generated-image bucket, re-homed from |
| `ATOM_MEDIA_TRUSTED_PUBLIC_ORIGIN` | unset | the origin whose `http(s)` refs are accepted; unset refuses them, fail-closed |
| `GCS_BUCKET_BATCH_UPLOADS` | unset | batch source-material bucket |
| `SVC_IDENTITY_GRPC_URL`, `SVC_SHARING_GRPC_URL` | unset | dependency gRPC targets |
| `SVC_MODEL_BROKER_ROUTER_URL` | unset | model-broker-router target |
| `CHORA_BACKFILL_RUN_TIMEOUT`, `CHORA_BACKFILL_STALE_CUTOFF` | domain defaults | topic-tag backfill bounds |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | unset | OTLP trace endpoint |

The two media knobs are the ones that fail in confusing ways.
`ATOM_MEDIA_TRUSTED_PUBLIC_ORIGIN` is a security boundary: an `http(s)` reference is
accepted for re-homing only when its scheme, host and port match this value, and with
it unset every `http(s)` reference is refused (fail-closed). `S3_PUBLIC_ENDPOINT` is a
different thing — the origin baked into a *minted* URL, and it must be the host the
author's browser can reach, because SigV4 signs the Host header and an in-network URL
cannot be rewritten afterwards.

## Verify it works

### It is up

```sh
docker compose exec -T nats wget -qO- http://creation:8080/healthz
docker compose exec -T nats wget -qO- http://creation:8080/readyz
```

```json
{"status":"ok","time":"..."}
{"status":"ready"}
```

`readyz` is intentionally bare — it does not enumerate dependencies, so it will not
tell you whether the database or the object store actually works. It does have one
failure mode worth knowing: when the atom repository is nil it answers
**`503 {"status":"repo-uninitialised"}`** rather than a bare 200.

### Its migrations landed

```sh
docker compose exec -T postgres psql -U chora_creation_migrate -d chora_creation \
  -c "select count(*) from schema_migrations;"
```

Expect a count at or just under the 36 forward files listed under
[Migrations](#migrations).

### Its HTTP surface answers

The root needs no context:

```sh
docker compose exec -T nats wget -qO- http://creation:8080/
```

```json
{"domain":"Content Creation","project":"chora-content","service":"chora-creation","surface":"A+ Creator mode"}
```

Every domain route does need tenant and learner context, and this service wants the
lowercase `gcid` header — `X-GCID` is rejected with a `400` here, even though some
sibling services accept both spellings:

```sh
docker compose exec -T nats wget -qO- \
  --header="X-Tenant-Id: 00000000-0000-7000-8000-000000000001" \
  --header="gcid: 1ebe8ad8-218d-5185-ab8a-f64fcf558f8d" \
  http://creation:8080/api/atoms
```

`{"items":[...]}` — possibly empty — is the pass. A `400` means the headers did not
arrive. If `/api/atoms` answers but the question routes return `503
CREATION_QUESTIONS_NOT_WIRED`, the service is running without a database pool.

### The qgen dispatch pair is complete

```sh
docker compose logs creation | grep -i qgen | tail -3
docker compose exec -T nats wget -qO- http://ai-kernel-orchestrator:8080/readyz \
  | grep -o '"qgen":{[^}]*}'
```

The kernel side must show `"started": true` for the `qgen` lane. Dispatching with the
lane off is silent: jobs are published to a subject nobody consumes.

## Troubleshooting

- **Question routes return `503 CREATION_QUESTIONS_NOT_WIRED`** — no pgx pool, so the
  question repository is nil. There is no in-memory fallback for questions by design;
  set `CHORA_DB_DSN`.
- **Atoms work but questions do not** — same cause. Atoms *do* have an in-memory
  fallback, which makes the partial state easy to miss.
- **Setting `CHORA_CREATION_DB_DSN` changes nothing** — nothing reads it. Use
  `CHORA_DB_DSN`.
- **Compose refuses to run with `required variable EMBEDDING_LOGICAL_MODEL_ID is
  missing`** — set it in `chora-stack/.env`. It gates every command in that directory,
  not just this service.
- **Presigned image URLs point at a host the browser cannot reach** — `S3_PUBLIC_ENDPOINT`
  is empty or wrong. It must be the origin the browser resolves; the in-network
  `S3_ENDPOINT` cannot stand in, because the signature covers the Host.
- **Re-homing a generated image is refused** — the source `http(s)` reference does not
  match `ATOM_MEDIA_TRUSTED_PUBLIC_ORIGIN` on scheme, host and port, or that variable
  is unset, which refuses every `http(s)` reference.
- **Media uploads or batch uploads 500** — the bucket does not exist. The stack's
  `garage-init` creates `chora-atom-media`, `chora-ai-assist-images`,
  `chora-content-media` and `chora-batch-uploads`; anywhere else you create them.
- **Question jobs are dispatched but nothing generates** — `QUESTION_JOBS_QGEN_CREW_ENABLED`
  is on here but `QGEN_CREW_ENABLED` is off in `chora-ai-kernel-orchestrator`. Both
  halves are required.
- **Events stop reaching the broker but nothing fails loudly** — `NATS_URL` is set but
  JetStream initialisation failed, so the bootstrap logs it and falls back to the
  in-memory bus. A configured-but-unreachable NATS endpoint is the quietest way to lose
  event delivery.
- **Question-job routes return `503` while atoms work** — they need
  `SVC_IDENTITY_GRPC_URL`; without it the mana client is not wired and those routes
  refuse. Separately, with `SVC_SHARING_GRPC_URL` unset, saved-atom lookup returns
  empty and consent-gated question searches fail closed.
- **The service starts with no configuration at all** — `.env` was not exported. This
  binary does not read the file; use `set -a; . ./.env; set +a`.
- **Traces never reach Tempo** — every service in this stack logs
  `invalid target address http://otel-collector:4317 ... too many colons in address`,
  so spans are dropped. That text is observed in the running stack's logs, not derived
  from this repository; the fix belongs to the shared tracing initialiser. Only tracing
  is affected.

## API surface

Two HTTP route groups on one listener, plus gRPC.

| Group | Prefix | What it serves |
| --- | --- | --- |
| Phyllis content-creation surface | `/v1/` | the newer authoring API |
| Atom, question, topic, media, AI-assist and backfill handlers | `/api/` | the bulk of the domain surface |
| Question-bank and collection handlers | `/api/v1/` | these sit under the versioned prefix, not bare `/api/` |
| Health | `/healthz`, `/health`, `/readyz` | liveness and readiness |
| Banner | `/` | service identity |

Representative endpoints:

| Method | Path |
| --- | --- |
| `GET` | `/api/atoms` |
| `GET` | `/api/atoms/{id}` |
| `GET` | `/` |
| `GET` | `/healthz`, `/health`, `/readyz` |

gRPC, on `CHORA_GRPC_PORT`:

| Service | Notes |
| --- | --- |
| `chora.services.creation.v1.Creation` | atom creation and lookup, course-scoped listing, atom-ID validation, question snapshotting |
| `chora.services.creation.v1.ContentRetrieval` | content retrieval |
| `grpc.health.v1.Health` | standard health service |

HTTP requests pass through tracing and service-mesh metadata middleware; protected
handlers take tenant and caller identity from the request context, which the BFF
gateway supplies in the normal topology.

## Architecture

```text
cmd/server/                  entrypoint and dependency wiring
internal/domain/             domain models and business rules
internal/ports/              domain-owned adapter interfaces
internal/adapter/http/       HTTP handlers and middleware
internal/adapter/grpc/       gRPC servers
internal/adapter/pg/         PostgreSQL repositories
internal/adapter/events/     domain event publishers
internal/adapter/pubsub/     event subscribers
internal/adapter/outbox/     transactional outbox and dispatcher
internal/adapter/mediastore/ presigned media URLs and durable media copy
internal/adapter/storage/    S3-compatible blob storage for batch uploads
internal/adapter/clients/    clients for dependent Chora services
internal/embedindex/         atom embedding indexing
internal/mediarehome/        durable question-image re-homing
config/                      runtime data copied into the container
migrations/                  PostgreSQL schema
```

Hexagonal, split as in every Chora Go service: `domain` holds the rules, `ports` the
interfaces, `adapter` the implementations. The interesting parts for an operator are
`mediastore` and `mediarehome` — between them they explain the bucket triad and why
the public origin has to be right.

## Events

Events go through a PostgreSQL transactional outbox and are then published to NATS
JetStream.

| Concern | Setting |
| --- | --- |
| Outbox store | `chora_outbox` DSN — the stack points it at `chora_creation` |
| Dispatcher identity | `CHORA_OUTBOX_WORKER_ID` |
| Broker | `NATS_URL`; unset falls back to the in-process bus |
| Streams | `CHORA_EVENTS`, `CHORA_DLQ` |
| Source label | `CHORA_SOURCE_PROJECT` |

The service also **consumes**: the closure-saga subscriber reads
`chora.a2a.pseudonymise.requested`-style closure events using
`config/PII_Closure_Map.yaml`, which the Dockerfile copies into the image. And the
question-job subscriber dispatches `ai_draft` work to the qgen crew when
`QUESTION_JOBS_QGEN_CREW_ENABLED` is on.

## Migrations

**36 forward migration files**, applied by the stack's `scripts/migrate.sh` in the
`creation-migrate` one-shot against `chora_creation`. The runner is not part of this
repository.

The set runs from `0001_initial.sql` and `0002_drop_knowledge_graph_edges.sql` through
`0037_embedding_dimensions_1024.up.sql` and `9999_grant_app_roles.sql`, and covers
atoms, variants, revisions, questions, question jobs, item banks, collections, topic
nodes, media, clone provenance and embedding columns.

Two carry requirements worth knowing:

- **`0036_uuidv7_pk_defaults`** switches primary-key defaults to PostgreSQL 18's
  native `uuidv7()`. The stack's `pgvector/pgvector:0.8.7-pg18` satisfies it; an older
  server fails the migrate one-shot rather than the service.
- **`0037_embedding_dimensions_1024`** widens the embedding columns to the route's
  native width. pgvector cannot cast between widths, so `ALTER` on a populated column
  discards vectors.

The `*.down.sql` siblings are destructive rollbacks and are never applied: the runner
takes every `*.sql` except `*.down.sql`, records a SHA-256 checksum per file in
`schema_migrations`, and refuses a file whose contents changed after it was applied.
Because it is not wrapped in one transaction, a failed run leaves earlier files
committed — check `schema_migrations` before re-running.
