-- =============================================================================
-- chora-creation : 0003_outbox.sql
--
-- Adds the transactional outbox primitive to chora_creation per the
-- Phyllis Wave-B service-wiring directive (`feedback_resilience_priority`).
--
-- Domain  : Content Creation (5 core)
-- Database: chora_creation
-- Author  : A-Service-Wiring-Content (Phyllis Wave-B)
-- Date    : 2026-05-11
--
-- Schema mirrors libs/chora-go-common/outbox/sql_fixtures so the existing
-- chora-go-common/outbox PostgresRecorder + Relay drop in without
-- modification (matches services/chora-identity/migrations/0005_outbox.sql).
--
-- HARD INVARIANT: outbox rows live in the SAME database as the domain
-- they serve (chora_creation). Cross-DB queries remain forbidden — the
-- Relay process publishes to Cloud Pub/Sub and downstream subscribers
-- (chora-consumption, chora-sharing, chora-delivery) consume via Pub/Sub.
--
-- Resilience properties (per `feedback_resilience_priority`):
--
--   - Atomic enqueue inside the domain txn (no orphan events / orphan state)
--   - Dead-pod resume: a replacement Relay claims pending rows via
--     `FOR UPDATE SKIP LOCKED` — never re-publishes already-published rows
--   - DLQ: ultimate publish failure transitions the row to
--     `status='deadlettered'` with the failure reason captured
--   - Per-(worker, topic) checkpoint table for resumeable poll cursors
--   - Idempotency keys table for multi-pod webhook + cross-domain command
--     dedupe via `chora-go-common/idempotent.PostgresStore`
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- outbox_events — atomic enqueue inside the domain transaction
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS outbox_events (
    id              TEXT        PRIMARY KEY,
    aggregate_type  TEXT        NOT NULL,
    aggregate_id    TEXT        NOT NULL,
    event_type      TEXT        NOT NULL,
    topic           TEXT        NOT NULL,
    payload         BYTEA       NOT NULL,
    envelope        JSONB       NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL,
    status          TEXT        NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending','published','failed','deadlettered')),
    retry_count     INT         NOT NULL DEFAULT 0,
    last_error      TEXT        NOT NULL DEFAULT '',
    last_attempt_at TIMESTAMPTZ,
    published_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS outbox_events_pending_idx
    ON outbox_events (occurred_at ASC)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS outbox_events_aggregate_idx
    ON outbox_events (aggregate_type, aggregate_id, occurred_at DESC);

CREATE INDEX IF NOT EXISTS outbox_events_topic_idx
    ON outbox_events (topic, status);

-- -----------------------------------------------------------------------------
-- outbox_poll_checkpoints — per-(worker, topic) resume cursor
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS outbox_poll_checkpoints (
    worker_id                  TEXT        NOT NULL,
    topic                      TEXT        NOT NULL,
    last_processed_outbox_id   TEXT        NOT NULL,
    last_processed_occurred_at TIMESTAMPTZ NOT NULL,
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (worker_id, topic)
);

CREATE INDEX IF NOT EXISTS outbox_poll_checkpoints_topic_idx
    ON outbox_poll_checkpoints (topic, updated_at DESC);

-- -----------------------------------------------------------------------------
-- outbox_dead_letters — permanent failure tracker
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS outbox_dead_letters (
    outbox_event_id   TEXT        PRIMARY KEY REFERENCES outbox_events(id),
    failure_reason    TEXT        NOT NULL,
    attempt_count     INT         NOT NULL,
    worker_id         TEXT        NOT NULL,
    deadlettered_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at       TIMESTAMPTZ,
    resolution_note   TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS outbox_dead_letters_unresolved_idx
    ON outbox_dead_letters (deadlettered_at DESC)
    WHERE resolved_at IS NULL;

-- -----------------------------------------------------------------------------
-- idempotency_keys — multi-pod webhook + cross-domain command dedupe
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS idempotency_keys (
    key           TEXT        PRIMARY KEY,
    processed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    ttl_at        TIMESTAMPTZ NOT NULL,
    result_hash   TEXT        NULL
);

CREATE INDEX IF NOT EXISTS idempotency_keys_ttl_idx
    ON idempotency_keys (ttl_at);

COMMIT;
