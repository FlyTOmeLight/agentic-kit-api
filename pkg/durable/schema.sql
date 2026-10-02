CREATE TABLE IF NOT EXISTS durable_run (
 id text PRIMARY KEY,
 owner_id text NOT NULL,
 session_id text NOT NULL,
 request_key text NOT NULL,
 request_hash text NOT NULL,
 spec jsonb NOT NULL,
 state jsonb NOT NULL DEFAULT '{}',
 status text NOT NULL CHECK (status IN ('Queued','Running','WaitingClarification','NeedsReview','Completed','Failed','Canceled')),
 worker_id text NOT NULL DEFAULT '',
 epoch bigint NOT NULL DEFAULT 0,
 lease_until timestamptz,
 available_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 deadline timestamptz NOT NULL,
 attempt integer NOT NULL DEFAULT 0,
 max_attempts integer NOT NULL,
 event_seq bigint NOT NULL DEFAULT 0,
 output text NOT NULL DEFAULT '',
 error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(owner_id,request_key)
);
CREATE INDEX IF NOT EXISTS durable_run_queue ON durable_run(available_at,created_at) WHERE status IN ('Queued','Running');
CREATE UNIQUE INDEX IF NOT EXISTS durable_session_active ON durable_run(owner_id,session_id) WHERE status IN ('Queued','Running','WaitingClarification','NeedsReview');
CREATE TABLE IF NOT EXISTS durable_step (
 run_id text NOT NULL REFERENCES durable_run(id),
 step_id text NOT NULL,
 input_hash text NOT NULL,
 policy text NOT NULL CHECK(policy IN ('ReadOnly','Idempotent','Unsafe')),
 status text NOT NULL CHECK(status IN ('Started','Completed')),
 result text NOT NULL DEFAULT '',
 PRIMARY KEY(run_id,step_id)
);
CREATE TABLE IF NOT EXISTS durable_event (
 run_id text NOT NULL REFERENCES durable_run(id),
 seq bigint NOT NULL,
 kind text NOT NULL,
 payload jsonb NOT NULL,
 published boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(run_id,seq)
);
CREATE INDEX IF NOT EXISTS durable_event_pending ON durable_event(created_at) WHERE NOT published;
