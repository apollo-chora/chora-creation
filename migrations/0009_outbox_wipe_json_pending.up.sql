-- 0009_outbox_wipe_json_pending.up.sql
--
-- Outbox payload encoding migration (codebase-wide JSON→binary-protobuf fix
-- — task #33, 2026-05-16).
--
-- Background
-- ----------
-- Pre-fix outbox rows on chora_creation.outbox_events held JSON-marshalled
-- payload bytes that GCP Pub/Sub Schema Registry (BINARY encoding) rejects
-- at publish time with "Invalid binary proto message". The dispatcher
-- retries forever (MaxAttempts=5 then deadletter) and the row never
-- publishes.
--
-- Fix
-- ---
-- internal/adapter/events/protomarshal now emits canonical binary protobuf
-- bytes for the chora-creation BINARY-encoded topics:
--
--   * chora.creation.atom.created.v1
--   * chora.creation.question.authored.v1
--   * chora.creation.question.generation_requested.v1
--   * chora.creation.question.generation_completed.v1
--
-- New rows written after the fix carry binary bytes and publish cleanly.
-- This migration drains pre-fix JSON-payload pending outbox rows so the
-- dispatcher stops retrying them; rows that NEVER successfully published
-- (still 'pending') are safe to mark 'failed' — no downstream subscriber
-- ever saw them.
--
-- Replay strategy
-- ---------------
-- We mark-failed rather than translate-and-retry: the producer-side handlers
-- (question_jobs_handler.create*, ai_assist subscriber, atom create / revise
-- flow) are idempotent on envelope.idempotency_key — re-triggering the user
-- flow will emit a fresh correctly-encoded outbox row. Translating
-- JSON-decoded fields back into the typed proto would be more error-prone
-- than re-emission, and these topics carry zero state-change semantics
-- beyond the audit + downstream notification feed.
--
-- chora.creation.atom.revised.v1 is also wiped — there is no deployed Pub/Sub
-- topic for it yet, so any pending row sat blocked forever. Re-emission will
-- pick up the topic when (and if) the schema lands; until then the bridge
-- falls back to JSON + WARN.
--
-- Idempotent: re-running is a no-op (the WHERE clause matches no rows after
-- the first pass).
UPDATE outbox_events
SET status          = 'failed',
    last_error      = 'codebase-wide outbox protobuf encoding fix #33 — pre-fix JSON-payload row drained',
    last_attempt_at = now()
WHERE status = 'pending'
  AND topic IN (
    'chora.creation.atom.created.v1',
    'chora.creation.atom.revised.v1',
    'chora.creation.question.authored.v1',
    'chora.creation.question.generation_requested.v1',
    'chora.creation.question.generation_completed.v1'
  );
