-- Sandbox control-plane storage contract. PostgreSQL 14+, UTF-8.
-- Design only: this file creates tables, not services, procedures or a migration runner.
-- Execute once in a selected EMPTY database. No IF NOT EXISTS: drift must fail visibly.
-- Time precision: TIMESTAMPTZ (microseconds precision, UTC timezone-aware).
--
-- STORAGE / APPLICATION BOUNDARY
-- The center owns generic environments, executions, files and terminal access.
-- agentic-kit owns CLI Runner versions/arguments/events, logical CLI sessions,
-- resume decisions and their execution mappings; none are persisted here.
-- Session reuse scope IDs are caller-opaque keys, not managed CLI sessions.
-- Every connection must use UTC. TIMESTAMPTZ stores timestamp with time zone in UTC;
-- comparisons use CURRENT_TIMESTAMP.
-- No automatic timestamp defaults hide transaction time; callers supply every
-- required column. NULL means absent/unknown, never zero/empty/success.
-- IDs, hashes, request keys, enum values and opaque refs use COLLATE "C" for exact
-- byte-order comparison. Text/path columns use standard UTF-8.
-- All integer counters use SIGNED BIGINT / Go int64. Positive counters start at 1.
-- API/JSON encoders and mutation admission enforce sandboxv1.MaxCounter = 9007199254740991.
--
-- JSONB CONTRACT
-- Every non-NULL JSONB column uses {"schemaVersion":1,"data":<typed value>}.
-- SQL checks the envelope via jsonb_typeof and key inspection; column comments below
-- name the data DTO. The storage DTO catalog is internal/contract/storage_json.go.
-- Required lists inside data use [], not null. SQL NULL maps to nil RawMessage;
-- JSON literal null is not SQL NULL and is not a valid envelope.
--
-- REFERENCES AND LOCK ORDER (all cross-row rules are transaction obligations)
-- No foreign keys: current instance/workspace generations advance, while evidence
-- and idempotency rows must retain old identities. All references are validated
-- with tenant/project and expected generation under the locks below.
-- Use short READ COMMITTED transactions; no network I/O while holding row locks.
-- FOR UPDATE SKIP LOCKED is supported natively in PostgreSQL for pool/work queues.

SET TIME ZONE 'UTC';


-- the bound profile revision's scope, workspace mode, save policy or release mode.
CREATE TABLE sandbox_profile_revision (
  tenant_id VARCHAR(64) COLLATE "C" NOT NULL,
  project_id VARCHAR(64) COLLATE "C" NOT NULL,
  profile_id VARCHAR(64) COLLATE "C" NOT NULL,
  revision BIGINT NOT NULL,
  spec_hash CHAR(64) COLLATE "C" NOT NULL,
  image_digest VARCHAR(255) COLLATE "C" NOT NULL,
  enabled BOOLEAN NOT NULL,
  disabled_at TIMESTAMPTZ NULL,
  disable_reason TEXT NULL,
  resolved_template JSONB NOT NULL,
  capabilities JSONB NOT NULL,
  conditions JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (tenant_id, profile_id, revision),
  CONSTRAINT ck_profile_revision_1 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id !~ '[[:space:]]'),
  CONSTRAINT ck_profile_revision_2 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id !~ '[[:space:]]'),
  CONSTRAINT ck_profile_revision_3 CHECK (CHAR_LENGTH(profile_id) > 0 AND profile_id !~ '[[:space:]]'),
  CONSTRAINT ck_profile_revision_4 CHECK (revision > 0),
  CONSTRAINT ck_profile_revision_5 CHECK (spec_hash ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_profile_revision_7 CHECK (jsonb_typeof(resolved_template) = 'object' AND resolved_template ? 'schemaVersion' AND resolved_template ? 'data' AND (resolved_template->>'schemaVersion') = '1' AND jsonb_typeof(resolved_template->'data') <> 'null'),
  CONSTRAINT ck_profile_revision_8 CHECK (jsonb_typeof(capabilities) = 'object' AND capabilities ? 'schemaVersion' AND capabilities ? 'data' AND (capabilities->>'schemaVersion') = '1' AND jsonb_typeof(capabilities->'data') <> 'null'),
  CONSTRAINT ck_profile_revision_9 CHECK (jsonb_typeof(conditions) = 'object' AND conditions ? 'schemaVersion' AND conditions ? 'data' AND (conditions->>'schemaVersion') = '1' AND jsonb_typeof(conditions->'data') <> 'null'),
  CONSTRAINT ck_profile_revision_10 CHECK (version > 0)
);

CREATE INDEX ix_profile_revision_1 ON sandbox_profile_revision (tenant_id, project_id, enabled, profile_id, revision);

COMMENT ON COLUMN sandbox_profile_revision.resolved_template IS 'Envelope data: sandboxv1.ProfileSpec';
COMMENT ON COLUMN sandbox_profile_revision.capabilities IS 'Envelope data: []sandboxv1.Capability';
COMMENT ON COLUMN sandbox_profile_revision.conditions IS 'Envelope data: []sandboxv1.Condition';

-- One row per budget scope and resource unit. Zero limit prohibits use. Limits may be lowered below current usage; retain usage and refuse new positive reservations until capacity is available. Tenant scope_id=tenant_id; platform inventory scope_id=pool_key; physical cluster capacity scope_id=cluster_id.
CREATE TABLE sandbox_quota_account (
  id VARCHAR(64) COLLATE "C" NOT NULL,
  tenant_id VARCHAR(64) COLLATE "C" NOT NULL,
  project_id VARCHAR(64) COLLATE "C" NOT NULL,
  account_kind VARCHAR(32) COLLATE "C" NOT NULL,
  scope_id VARCHAR(64) COLLATE "C" NOT NULL,
  resource VARCHAR(32) COLLATE "C" NOT NULL,
  limit_amount BIGINT NOT NULL,
  reserved_amount BIGINT NOT NULL,
  charged_amount BIGINT NOT NULL,
  observed_at TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT ck_quota_account_1 CHECK (account_kind IN ('Tenant', 'PlatformInventory', 'ClusterCapacity')),
  CONSTRAINT ck_quota_account_2 CHECK (resource IN ('ActiveLeases', 'PendingAcquisitions', 'ConcurrentExecutions', 'AccessSessions', 'CPUMillis', 'MemoryBytes', 'EphemeralDiskBytes', 'WorkspaceBytes', 'ArchivedOutputBytes', 'Instances')),
  CONSTRAINT ck_quota_account_3 CHECK (reserved_amount <= 9223372036854775807 - charged_amount),
  CONSTRAINT ck_quota_account_4 CHECK (CHAR_LENGTH(id) > 0 AND id !~ '[[:space:]]'),
  CONSTRAINT ck_quota_account_5 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id !~ '[[:space:]]'),
  CONSTRAINT ck_quota_account_6 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id !~ '[[:space:]]'),
  CONSTRAINT ck_quota_account_7 CHECK (CHAR_LENGTH(scope_id) > 0 AND scope_id !~ '[[:space:]]'),
  CONSTRAINT ck_quota_account_8 CHECK (limit_amount >= 0),
  CONSTRAINT ck_quota_account_9 CHECK (reserved_amount >= 0),
  CONSTRAINT ck_quota_account_10 CHECK (charged_amount >= 0),
  CONSTRAINT ck_quota_account_11 CHECK (version > 0)
);

CREATE UNIQUE INDEX uq_quota_account_1 ON sandbox_quota_account (account_kind, scope_id, resource);
CREATE INDEX ix_quota_account_1 ON sandbox_quota_account (tenant_id, project_id, account_kind, resource);

-- ledger_key=SHA256(charge_namespace, resource, subject_kind, subject_id, subject_generation); charge_namespace is PhysicalOwner for InventoryPhysical/TenantPhysical, otherwise charge_class. account_id is excluded so inventory ownership transfer updates this SAME row. Resource is read from account_id and may not change on transfer. Historical Released rows remain unique.
CREATE TABLE sandbox_quota_reservation (
  id VARCHAR(64) COLLATE "C" NOT NULL,
  tenant_id VARCHAR(64) COLLATE "C" NOT NULL,
  project_id VARCHAR(64) COLLATE "C" NOT NULL,
  account_id VARCHAR(64) COLLATE "C" NOT NULL,
  ledger_key CHAR(64) COLLATE "C" NOT NULL,
  subject_kind VARCHAR(32) COLLATE "C" NOT NULL,
  subject_id VARCHAR(64) COLLATE "C" NOT NULL,
  subject_generation BIGINT NOT NULL,
  charge_class VARCHAR(32) COLLATE "C" NOT NULL,
  amount BIGINT NOT NULL,
  status VARCHAR(32) COLLATE "C" NOT NULL,
  acquisition_id VARCHAR(64) COLLATE "C" NULL,
  instance_id VARCHAR(64) COLLATE "C" NULL,
  instance_generation BIGINT NULL,
  lease_id VARCHAR(64) COLLATE "C" NULL,
  transferred_from_account_id VARCHAR(64) COLLATE "C" NULL,
  reserved_at TIMESTAMPTZ NOT NULL,
  charged_at TIMESTAMPTZ NULL,
  released_at TIMESTAMPTZ NULL,
  release_evidence_ref VARCHAR(1024) COLLATE "C" NULL,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT ck_quota_reservation_1 CHECK (status IN ('Reserved', 'Charged', 'Released')),
  CONSTRAINT ck_quota_reservation_2 CHECK (charge_class IN ('Admission', 'TenantPhysical', 'InventoryPhysical', 'ClusterPhysical', 'Execution', 'Access', 'Storage', 'Archive')),
  CONSTRAINT ck_quota_reservation_3 CHECK (status <> 'Released' OR (released_at IS NOT NULL AND release_evidence_ref IS NOT NULL)),
  CONSTRAINT ck_quota_reservation_4 CHECK ((instance_id IS NULL) = (instance_generation IS NULL)),
  CONSTRAINT ck_quota_reservation_5 CHECK (CHAR_LENGTH(id) > 0 AND id !~ '[[:space:]]'),
  CONSTRAINT ck_quota_reservation_6 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id !~ '[[:space:]]'),
  CONSTRAINT ck_quota_reservation_7 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id !~ '[[:space:]]'),
  CONSTRAINT ck_quota_reservation_8 CHECK (CHAR_LENGTH(account_id) > 0 AND account_id !~ '[[:space:]]'),
  CONSTRAINT ck_quota_reservation_9 CHECK (ledger_key ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_quota_reservation_10 CHECK (CHAR_LENGTH(subject_id) > 0 AND subject_id !~ '[[:space:]]'),
  CONSTRAINT ck_quota_reservation_11 CHECK (subject_generation > 0),
  CONSTRAINT ck_quota_reservation_12 CHECK (amount > 0),
  CONSTRAINT ck_quota_reservation_13 CHECK (CHAR_LENGTH(acquisition_id) > 0 AND acquisition_id !~ '[[:space:]]'),
  CONSTRAINT ck_quota_reservation_14 CHECK (CHAR_LENGTH(instance_id) > 0 AND instance_id !~ '[[:space:]]'),
  CONSTRAINT ck_quota_reservation_15 CHECK (instance_generation > 0),
  CONSTRAINT ck_quota_reservation_16 CHECK (CHAR_LENGTH(lease_id) > 0 AND lease_id !~ '[[:space:]]'),
  CONSTRAINT ck_quota_reservation_17 CHECK (CHAR_LENGTH(transferred_from_account_id) > 0 AND transferred_from_account_id !~ '[[:space:]]'),
  CONSTRAINT ck_quota_reservation_18 CHECK (version > 0)
);

CREATE UNIQUE INDEX uq_quota_reservation_1 ON sandbox_quota_reservation (ledger_key);
CREATE INDEX ix_quota_reservation_1 ON sandbox_quota_reservation (account_id, status, id);
CREATE INDEX ix_quota_reservation_2 ON sandbox_quota_reservation (subject_kind, subject_id, subject_generation, charge_class);
CREATE INDEX ix_quota_reservation_3 ON sandbox_quota_reservation (tenant_id, acquisition_id, status);
CREATE INDEX ix_quota_reservation_4 ON sandbox_quota_reservation (instance_id, instance_generation, status);

-- Only clean inventory. Counts are observed projections, never the quota authority. Lock pool row and reserve budgets before recording each provisioning intent. Disable does not reclaim claimed instances.
CREATE TABLE sandbox_warm_pool (
  id VARCHAR(64) COLLATE "C" NOT NULL,
  tenant_id VARCHAR(64) COLLATE "C" NOT NULL,
  project_id VARCHAR(64) COLLATE "C" NOT NULL,
  pool_key CHAR(64) COLLATE "C" NOT NULL,
  profile_tenant_id VARCHAR(64) COLLATE "C" NOT NULL,
  profile_id VARCHAR(64) COLLATE "C" NOT NULL,
  profile_revision BIGINT NOT NULL,
  cluster_id VARCHAR(64) COLLATE "C" NOT NULL,
  namespace VARCHAR(255) COLLATE "C" NOT NULL,
  spec_hash CHAR(64) COLLATE "C" NOT NULL,
  enabled BOOLEAN NOT NULL,
  min_ready BIGINT NOT NULL,
  max_ready BIGINT NOT NULL,
  max_provisioning BIGINT NOT NULL,
  max_idle_seconds BIGINT NOT NULL,
  ready_count BIGINT NOT NULL,
  provisioning_count BIGINT NOT NULL,
  deleting_count BIGINT NOT NULL,
  observed_at TIMESTAMPTZ NULL,
  pool_spec JSONB NOT NULL,
  resolved_spec JSONB NOT NULL,
  conditions JSONB NOT NULL,
  next_reconcile_at TIMESTAMPTZ NULL,
  worker_id VARCHAR(64) COLLATE "C" NULL,
  claim_token VARCHAR(64) COLLATE "C" NULL,
  claim_until TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT ck_warm_pool_1 CHECK (min_ready <= max_ready),
  CONSTRAINT ck_warm_pool_2 CHECK (CHAR_LENGTH(id) > 0 AND id !~ '[[:space:]]'),
  CONSTRAINT ck_warm_pool_3 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id !~ '[[:space:]]'),
  CONSTRAINT ck_warm_pool_4 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id !~ '[[:space:]]'),
  CONSTRAINT ck_warm_pool_5 CHECK (pool_key ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_warm_pool_6 CHECK (CHAR_LENGTH(profile_tenant_id) > 0 AND profile_tenant_id !~ '[[:space:]]'),
  CONSTRAINT ck_warm_pool_7 CHECK (CHAR_LENGTH(profile_id) > 0 AND profile_id !~ '[[:space:]]'),
  CONSTRAINT ck_warm_pool_8 CHECK (profile_revision > 0),
  CONSTRAINT ck_warm_pool_9 CHECK (CHAR_LENGTH(cluster_id) > 0 AND cluster_id !~ '[[:space:]]'),
  CONSTRAINT ck_warm_pool_10 CHECK (spec_hash ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_warm_pool_12 CHECK (min_ready >= 0),
  CONSTRAINT ck_warm_pool_13 CHECK (max_ready >= 0),
  CONSTRAINT ck_warm_pool_14 CHECK (max_provisioning >= 0),
  CONSTRAINT ck_warm_pool_15 CHECK (max_idle_seconds >= 0),
  CONSTRAINT ck_warm_pool_16 CHECK (ready_count >= 0),
  CONSTRAINT ck_warm_pool_17 CHECK (provisioning_count >= 0),
  CONSTRAINT ck_warm_pool_18 CHECK (deleting_count >= 0),
  CONSTRAINT ck_warm_pool_19 CHECK (jsonb_typeof(pool_spec) = 'object' AND pool_spec ? 'schemaVersion' AND pool_spec ? 'data' AND (pool_spec->>'schemaVersion') = '1' AND jsonb_typeof(pool_spec->'data') <> 'null'),
  CONSTRAINT ck_warm_pool_20 CHECK (jsonb_typeof(resolved_spec) = 'object' AND resolved_spec ? 'schemaVersion' AND resolved_spec ? 'data' AND (resolved_spec->>'schemaVersion') = '1' AND jsonb_typeof(resolved_spec->'data') <> 'null'),
  CONSTRAINT ck_warm_pool_21 CHECK (jsonb_typeof(conditions) = 'object' AND conditions ? 'schemaVersion' AND conditions ? 'data' AND (conditions->>'schemaVersion') = '1' AND jsonb_typeof(conditions->'data') <> 'null'),
  CONSTRAINT ck_warm_pool_22 CHECK (CHAR_LENGTH(worker_id) > 0 AND worker_id !~ '[[:space:]]'),
  CONSTRAINT ck_warm_pool_23 CHECK (CHAR_LENGTH(claim_token) > 0 AND claim_token !~ '[[:space:]]'),
  CONSTRAINT ck_warm_pool_24 CHECK (version > 0),
  CONSTRAINT ck_warm_pool_25 CHECK ((worker_id IS NULL) = (claim_token IS NULL) AND (worker_id IS NULL) = (claim_until IS NULL))
);

CREATE UNIQUE INDEX uq_warm_pool_1 ON sandbox_warm_pool (pool_key);
CREATE INDEX ix_warm_pool_1 ON sandbox_warm_pool (enabled, next_reconcile_at, id);
CREATE INDEX ix_warm_pool_2 ON sandbox_warm_pool (cluster_id, enabled, profile_id, profile_revision);

COMMENT ON COLUMN sandbox_warm_pool.pool_spec IS 'Envelope data: sandboxv1.PoolSpec';
COMMENT ON COLUMN sandbox_warm_pool.resolved_spec IS 'Envelope data: contract.InstanceSpec';
COMMENT ON COLUMN sandbox_warm_pool.conditions IS 'Envelope data: []sandboxv1.Condition';

-- One durable row per GLOBAL workspace ID, not one independent lock per generation. In-place restore advances generation under this row lock; old generations live in checkpoint/execution/event evidence. next_fencing_token never resets, including generation changes. storage_identity_hash identifies canonical physical writable storage across all tenants; provisioning must also reject overlapping roots, which equality alone cannot detect.
CREATE TABLE sandbox_workspace_binding (
  id VARCHAR(64) COLLATE "C" NOT NULL,
  tenant_id VARCHAR(64) COLLATE "C" NOT NULL,
  project_id VARCHAR(64) COLLATE "C" NOT NULL,
  workspace_generation BIGINT NOT NULL,
  mode VARCHAR(32) COLLATE "C" NOT NULL,
  source_ref VARCHAR(1024) COLLATE "C" NOT NULL,
  base_commit VARCHAR(255) COLLATE "C" NOT NULL,
  data_version VARCHAR(255) COLLATE "C" NOT NULL,
  storage_ref VARCHAR(1024) COLLATE "C" NULL,
  storage_identity_hash CHAR(64) COLLATE "C" NULL,
  recovery_level VARCHAR(32) COLLATE "C" NOT NULL,
  status VARCHAR(32) COLLATE "C" NOT NULL,
  preparing_acquisition_id VARCHAR(64) COLLATE "C" NULL,
  preparation_expires_at TIMESTAMPTZ NULL,
  writer_lease_id VARCHAR(64) COLLATE "C" NULL,
  bound_instance_id VARCHAR(64) COLLATE "C" NULL,
  bound_instance_generation BIGINT NULL,
  next_fencing_token BIGINT NOT NULL,
  checkpoint_id VARCHAR(64) COLLATE "C" NULL,
  save_policy_id VARCHAR(64) COLLATE "C" NOT NULL,
  save_policy_revision BIGINT NOT NULL,
  write_barrier_operation_id VARCHAR(64) COLLATE "C" NULL,
  write_barrier_epoch BIGINT NOT NULL,
  physical_fence_verified_at TIMESTAMPTZ NULL,
  physical_fence_evidence_ref VARCHAR(1024) COLLATE "C" NULL,
  layout JSONB NOT NULL,
  failure JSONB NULL,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT ck_workspace_binding_1 CHECK (mode IN ('EphemeralCopy', 'PersistentVolume')),
  CONSTRAINT ck_workspace_binding_2 CHECK (recovery_level IN ('Ephemeral', 'Checkpointed', 'Persistent')),
  CONSTRAINT ck_workspace_binding_3 CHECK (status IN ('Preparing', 'Ready', 'Restoring', 'Quarantined', 'Failed')),
  CONSTRAINT ck_workspace_binding_4 CHECK ((preparing_acquisition_id IS NULL OR writer_lease_id IS NULL)),
  CONSTRAINT ck_workspace_binding_5 CHECK ((preparing_acquisition_id IS NULL) = (preparation_expires_at IS NULL)),
  CONSTRAINT ck_workspace_binding_6 CHECK ((bound_instance_id IS NULL) = (bound_instance_generation IS NULL)),
  CONSTRAINT ck_workspace_binding_7 CHECK (mode <> 'PersistentVolume' OR (storage_ref IS NOT NULL AND storage_identity_hash IS NOT NULL)),
  CONSTRAINT ck_workspace_binding_8 CHECK ((physical_fence_verified_at IS NULL) = (physical_fence_evidence_ref IS NULL)),
  CONSTRAINT ck_workspace_binding_9 CHECK (CHAR_LENGTH(id) > 0 AND id !~ '[[:space:]]'),
  CONSTRAINT ck_workspace_binding_10 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id !~ '[[:space:]]'),
  CONSTRAINT ck_workspace_binding_11 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id !~ '[[:space:]]'),
  CONSTRAINT ck_workspace_binding_12 CHECK (workspace_generation > 0),
  CONSTRAINT ck_workspace_binding_13 CHECK (storage_identity_hash ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_workspace_binding_14 CHECK (CHAR_LENGTH(preparing_acquisition_id) > 0 AND preparing_acquisition_id !~ '[[:space:]]'),
  CONSTRAINT ck_workspace_binding_15 CHECK (CHAR_LENGTH(writer_lease_id) > 0 AND writer_lease_id !~ '[[:space:]]'),
  CONSTRAINT ck_workspace_binding_16 CHECK (CHAR_LENGTH(bound_instance_id) > 0 AND bound_instance_id !~ '[[:space:]]'),
  CONSTRAINT ck_workspace_binding_17 CHECK (bound_instance_generation > 0),
  CONSTRAINT ck_workspace_binding_18 CHECK (next_fencing_token > 0),
  CONSTRAINT ck_workspace_binding_19 CHECK (CHAR_LENGTH(checkpoint_id) > 0 AND checkpoint_id !~ '[[:space:]]'),
  CONSTRAINT ck_workspace_binding_20 CHECK (CHAR_LENGTH(save_policy_id) > 0 AND save_policy_id !~ '[[:space:]]'),
  CONSTRAINT ck_workspace_binding_21 CHECK (save_policy_revision > 0),
  CONSTRAINT ck_workspace_binding_22 CHECK (CHAR_LENGTH(write_barrier_operation_id) > 0 AND write_barrier_operation_id !~ '[[:space:]]'),
  CONSTRAINT ck_workspace_binding_23 CHECK (write_barrier_epoch > 0),
  CONSTRAINT ck_workspace_binding_24 CHECK (jsonb_typeof(layout) = 'object' AND layout ? 'schemaVersion' AND layout ? 'data' AND (layout->>'schemaVersion') = '1' AND jsonb_typeof(layout->'data') <> 'null'),
  CONSTRAINT ck_workspace_binding_25 CHECK (jsonb_typeof(failure) = 'object' AND failure ? 'schemaVersion' AND failure ? 'data' AND (failure->>'schemaVersion') = '1' AND jsonb_typeof(failure->'data') <> 'null'),
  CONSTRAINT ck_workspace_binding_26 CHECK (version > 0)
);

CREATE UNIQUE INDEX uq_workspace_binding_1 ON sandbox_workspace_binding (tenant_id, id, workspace_generation);
CREATE UNIQUE INDEX uq_workspace_binding_2 ON sandbox_workspace_binding (storage_identity_hash);
CREATE UNIQUE INDEX uq_workspace_binding_3 ON sandbox_workspace_binding (preparing_acquisition_id);
CREATE UNIQUE INDEX uq_workspace_binding_4 ON sandbox_workspace_binding (writer_lease_id);
CREATE INDEX ix_workspace_binding_1 ON sandbox_workspace_binding (tenant_id, project_id, status, updated_at);
CREATE INDEX ix_workspace_binding_2 ON sandbox_workspace_binding (bound_instance_id, bound_instance_generation);
CREATE INDEX ix_workspace_binding_3 ON sandbox_workspace_binding (status, preparation_expires_at);

COMMENT ON COLUMN sandbox_workspace_binding.layout IS 'Envelope data: sandboxv1.WorkspaceLayout';
COMMENT ON COLUMN sandbox_workspace_binding.failure IS 'Envelope data: sandboxv1.Error';

-- Unique request identity intentionally excludes project_id: conflicting project/spec under the same tenant/caller/key is IdempotencyConflict. Queued can own a preparation claim without an instance. Ready result is frozen at activation; replay does not renew the lease.
CREATE TABLE sandbox_acquire_request (
  id VARCHAR(64) COLLATE "C" NOT NULL,
  tenant_id VARCHAR(64) COLLATE "C" NOT NULL,
  project_id VARCHAR(64) COLLATE "C" NOT NULL,
  caller_id VARCHAR(64) COLLATE "C" NOT NULL,
  request_key VARCHAR(128) COLLATE "C" NOT NULL,
  spec_hash CHAR(64) COLLATE "C" NOT NULL,
  mode VARCHAR(32) COLLATE "C" NOT NULL,
  scope_type VARCHAR(32) COLLATE "C" NOT NULL,
  scope_id VARCHAR(64) COLLATE "C" NOT NULL,
  holder_run_id VARCHAR(64) COLLATE "C" NOT NULL,
  holder_run_epoch BIGINT NOT NULL,
  profile_tenant_id VARCHAR(64) COLLATE "C" NOT NULL,
  profile_id VARCHAR(64) COLLATE "C" NOT NULL,
  profile_revision BIGINT NOT NULL,
  workspace_id VARCHAR(64) COLLATE "C" NOT NULL,
  workspace_generation BIGINT NOT NULL,
  reuse_key CHAR(64) COLLATE "C" NULL,
  pool_key CHAR(64) COLLATE "C" NOT NULL,
  capacity_policy VARCHAR(32) COLLATE "C" NOT NULL,
  status VARCHAR(32) COLLATE "C" NOT NULL,
  phase VARCHAR(255) COLLATE "C" NULL,
  queue_deadline TIMESTAMPTZ NOT NULL,
  provision_deadline TIMESTAMPTZ NOT NULL,
  requested_lease_seconds BIGINT NOT NULL,
  instance_id VARCHAR(64) COLLATE "C" NULL,
  instance_generation BIGINT NULL,
  lease_id VARCHAR(64) COLLATE "C" NULL,
  allocation_source VARCHAR(32) COLLATE "C" NULL,
  attempt BIGINT NOT NULL,
  completed_at TIMESTAMPTZ NULL,
  request_spec JSONB NULL,
  ready_result JSONB NULL,
  ready_evidence JSONB NULL,
  credential_grants JSONB NULL,
  failure JSONB NULL,
  conditions JSONB NOT NULL,
  next_reconcile_at TIMESTAMPTZ NULL,
  worker_id VARCHAR(64) COLLATE "C" NULL,
  claim_token VARCHAR(64) COLLATE "C" NULL,
  claim_until TIMESTAMPTZ NULL,
  tombstoned_at TIMESTAMPTZ NULL,
  result_expires_at TIMESTAMPTZ NULL,
  replay_closed_at TIMESTAMPTZ NULL,
  purge_after TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT ck_acquire_request_1 CHECK (mode IN ('New', 'Reuse')),
  CONSTRAINT ck_acquire_request_2 CHECK (scope_type IN ('Task', 'Session', 'Workspace')),
  CONSTRAINT ck_acquire_request_3 CHECK (capacity_policy IN ('AllowCold', 'WaitForWarm', 'FailIfNoCapacity')),
  CONSTRAINT ck_acquire_request_4 CHECK (status IN ('Queued', 'Provisioning', 'Preparing', 'Ready', 'Cancelling', 'Cancelled', 'Failed', 'TimedOut')),
  CONSTRAINT ck_acquire_request_5 CHECK (allocation_source IN ('WarmPool', 'Cold', 'Reuse')),
  CONSTRAINT ck_acquire_request_6 CHECK (queue_deadline <= provision_deadline),
  CONSTRAINT ck_acquire_request_7 CHECK ((instance_id IS NULL) = (instance_generation IS NULL)),
  CONSTRAINT ck_acquire_request_8 CHECK (mode <> 'Reuse' OR reuse_key IS NOT NULL),
  CONSTRAINT ck_acquire_request_9 CHECK (status <> 'Ready' OR (lease_id IS NOT NULL AND instance_id IS NOT NULL AND completed_at IS NOT NULL)),
  CONSTRAINT ck_acquire_request_10 CHECK (tombstoned_at IS NOT NULL OR request_spec IS NOT NULL),
  CONSTRAINT ck_acquire_request_11 CHECK (CHAR_LENGTH(id) > 0 AND id !~ '[[:space:]]'),
  CONSTRAINT ck_acquire_request_12 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id !~ '[[:space:]]'),
  CONSTRAINT ck_acquire_request_13 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id !~ '[[:space:]]'),
  CONSTRAINT ck_acquire_request_14 CHECK (CHAR_LENGTH(caller_id) > 0 AND caller_id !~ '[[:space:]]'),
  CONSTRAINT ck_acquire_request_15 CHECK (CHAR_LENGTH(request_key) > 0 AND request_key !~ '[[:space:]]'),
  CONSTRAINT ck_acquire_request_16 CHECK (spec_hash ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_acquire_request_17 CHECK (CHAR_LENGTH(scope_id) > 0 AND scope_id !~ '[[:space:]]'),
  CONSTRAINT ck_acquire_request_18 CHECK (CHAR_LENGTH(holder_run_id) > 0 AND holder_run_id !~ '[[:space:]]'),
  CONSTRAINT ck_acquire_request_19 CHECK (CHAR_LENGTH(profile_tenant_id) > 0 AND profile_tenant_id !~ '[[:space:]]'),
  CONSTRAINT ck_acquire_request_20 CHECK (CHAR_LENGTH(profile_id) > 0 AND profile_id !~ '[[:space:]]'),
  CONSTRAINT ck_acquire_request_21 CHECK (profile_revision > 0),
  CONSTRAINT ck_acquire_request_22 CHECK (CHAR_LENGTH(workspace_id) > 0 AND workspace_id !~ '[[:space:]]'),
  CONSTRAINT ck_acquire_request_23 CHECK (workspace_generation > 0),
  CONSTRAINT ck_acquire_request_24 CHECK (reuse_key ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_acquire_request_25 CHECK (pool_key ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_acquire_request_26 CHECK (requested_lease_seconds > 0),
  CONSTRAINT ck_acquire_request_27 CHECK (CHAR_LENGTH(instance_id) > 0 AND instance_id !~ '[[:space:]]'),
  CONSTRAINT ck_acquire_request_28 CHECK (instance_generation > 0),
  CONSTRAINT ck_acquire_request_29 CHECK (CHAR_LENGTH(lease_id) > 0 AND lease_id !~ '[[:space:]]'),
  CONSTRAINT ck_acquire_request_30 CHECK (attempt > 0),
  CONSTRAINT ck_acquire_request_31 CHECK (jsonb_typeof(request_spec) = 'object' AND request_spec ? 'schemaVersion' AND request_spec ? 'data' AND (request_spec->>'schemaVersion') = '1' AND jsonb_typeof(request_spec->'data') <> 'null'),
  CONSTRAINT ck_acquire_request_32 CHECK (jsonb_typeof(ready_result) = 'object' AND ready_result ? 'schemaVersion' AND ready_result ? 'data' AND (ready_result->>'schemaVersion') = '1' AND jsonb_typeof(ready_result->'data') <> 'null'),
  CONSTRAINT ck_acquire_request_33 CHECK (jsonb_typeof(ready_evidence) = 'object' AND ready_evidence ? 'schemaVersion' AND ready_evidence ? 'data' AND (ready_evidence->>'schemaVersion') = '1' AND jsonb_typeof(ready_evidence->'data') <> 'null'),
  CONSTRAINT ck_acquire_request_34 CHECK (jsonb_typeof(credential_grants) = 'object' AND credential_grants ? 'schemaVersion' AND credential_grants ? 'data' AND (credential_grants->>'schemaVersion') = '1' AND jsonb_typeof(credential_grants->'data') <> 'null'),
  CONSTRAINT ck_acquire_request_35 CHECK (jsonb_typeof(failure) = 'object' AND failure ? 'schemaVersion' AND failure ? 'data' AND (failure->>'schemaVersion') = '1' AND jsonb_typeof(failure->'data') <> 'null'),
  CONSTRAINT ck_acquire_request_36 CHECK (jsonb_typeof(conditions) = 'object' AND conditions ? 'schemaVersion' AND conditions ? 'data' AND (conditions->>'schemaVersion') = '1' AND jsonb_typeof(conditions->'data') <> 'null'),
  CONSTRAINT ck_acquire_request_37 CHECK (CHAR_LENGTH(worker_id) > 0 AND worker_id !~ '[[:space:]]'),
  CONSTRAINT ck_acquire_request_38 CHECK (CHAR_LENGTH(claim_token) > 0 AND claim_token !~ '[[:space:]]'),
  CONSTRAINT ck_acquire_request_39 CHECK (version > 0),
  CONSTRAINT ck_acquire_request_40 CHECK ((worker_id IS NULL) = (claim_token IS NULL) AND (worker_id IS NULL) = (claim_until IS NULL)),
  CONSTRAINT ck_acquire_request_41 CHECK (purge_after IS NULL OR (tombstoned_at IS NOT NULL AND replay_closed_at IS NOT NULL AND purge_after >= replay_closed_at)),
  CONSTRAINT ck_acquire_request_42 CHECK (holder_run_epoch BETWEEN 1 AND 9007199254740991)
);

CREATE UNIQUE INDEX uq_acquire_request_1 ON sandbox_acquire_request (tenant_id, caller_id, request_key);
CREATE UNIQUE INDEX uq_acquire_request_2 ON sandbox_acquire_request (lease_id);
CREATE INDEX ix_acquire_request_1 ON sandbox_acquire_request (status, next_reconcile_at, id);
CREATE INDEX ix_acquire_request_2 ON sandbox_acquire_request (tenant_id, project_id, created_at, id);
CREATE INDEX ix_acquire_request_3 ON sandbox_acquire_request (workspace_id, workspace_generation, status);
CREATE INDEX ix_acquire_request_4 ON sandbox_acquire_request (status, queue_deadline);
CREATE INDEX ix_acquire_request_5 ON sandbox_acquire_request (status, provision_deadline);
CREATE INDEX ix_acquire_request_6 ON sandbox_acquire_request (tombstoned_at, purge_after);

COMMENT ON COLUMN sandbox_acquire_request.request_spec IS 'Envelope data: contract.StoredAcquireRequest';
COMMENT ON COLUMN sandbox_acquire_request.ready_result IS 'Envelope data: sandboxv1.AcquisitionResult';
COMMENT ON COLUMN sandbox_acquire_request.ready_evidence IS 'Envelope data: contract.ReadyEvidence';
COMMENT ON COLUMN sandbox_acquire_request.credential_grants IS 'Envelope data: []contract.CredentialGrant (references only)';
COMMENT ON COLUMN sandbox_acquire_request.failure IS 'Envelope data: sandboxv1.Error';
COMMENT ON COLUMN sandbox_acquire_request.conditions IS 'Envelope data: []sandboxv1.Condition';

-- One current row per instance ID. generation is physical incarnation; version is row CAS. UID/boot change marks old executions Unknown and instance Lost. Never overwrite identity as if old processes survived. A generation advance needs prior physical fencing; retain old identity snapshots in leases/executions/actions/events. Deterministic pod_name includes ID and generation. Kubernetes delete uses expected pod_uid.
CREATE TABLE sandbox_instance (
  id VARCHAR(64) COLLATE "C" NOT NULL,
  tenant_id VARCHAR(64) COLLATE "C" NOT NULL,
  project_id VARCHAR(64) COLLATE "C" NOT NULL,
  generation BIGINT NOT NULL,
  status VARCHAR(32) COLLATE "C" NOT NULL,
  phase VARCHAR(255) COLLATE "C" NULL,
  reason VARCHAR(255) COLLATE "C" NULL,
  profile_tenant_id VARCHAR(64) COLLATE "C" NOT NULL,
  profile_id VARCHAR(64) COLLATE "C" NOT NULL,
  profile_revision BIGINT NOT NULL,
  pool_key CHAR(64) COLLATE "C" NULL,
  reuse_key CHAR(64) COLLATE "C" NULL,
  spec_hash CHAR(64) COLLATE "C" NOT NULL,
  cluster_id VARCHAR(64) COLLATE "C" NOT NULL,
  namespace VARCHAR(255) COLLATE "C" NOT NULL,
  pod_name VARCHAR(255) COLLATE "C" NOT NULL,
  pod_uid VARCHAR(64) COLLATE "C" NULL,
  runtime_boot_id VARCHAR(64) COLLATE "C" NULL,
  runtime_protocol_version VARCHAR(255) COLLATE "C" NULL,
  physical_phase VARCHAR(32) COLLATE "C" NULL,
  ever_claimed BOOLEAN NOT NULL,
  claimed_acquisition_id VARCHAR(64) COLLATE "C" NULL,
  claim_expires_at TIMESTAMPTZ NULL,
  active_lease_id VARCHAR(64) COLLATE "C" NULL,
  workspace_id VARCHAR(64) COLLATE "C" NULL,
  workspace_generation BIGINT NULL,
  admission_epoch BIGINT NOT NULL,
  provision_deadline TIMESTAMPTZ NOT NULL,
  hard_expires_at TIMESTAMPTZ NOT NULL,
  idle_expires_at TIMESTAMPTZ NULL,
  observed_at TIMESTAMPTZ NULL,
  physical_gone_at TIMESTAMPTZ NULL,
  writes_stopped_at TIMESTAMPTZ NULL,
  deleted_at TIMESTAMPTZ NULL,
  resolved_spec JSONB NOT NULL,
  physical_ref JSONB NULL,
  endpoint JSONB NULL,
  runtime_info JSONB NULL,
  effective_capabilities JSONB NOT NULL,
  fence_evidence JSONB NULL,
  failure JSONB NULL,
  conditions JSONB NOT NULL,
  next_reconcile_at TIMESTAMPTZ NULL,
  worker_id VARCHAR(64) COLLATE "C" NULL,
  claim_token VARCHAR(64) COLLATE "C" NULL,
  claim_until TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT ck_instance_1 CHECK (status IN ('Provisioning', 'WarmReady', 'Claimed', 'Preparing', 'Ready', 'IdleBound', 'Lost', 'Failed', 'Draining', 'Deleting', 'Deleted')),
  CONSTRAINT ck_instance_2 CHECK (physical_phase IN ('Pending', 'Running', 'Failed', 'Gone', 'Unknown')),
  CONSTRAINT ck_instance_3 CHECK ((workspace_id IS NULL) = (workspace_generation IS NULL)),
  CONSTRAINT ck_instance_4 CHECK ((claimed_acquisition_id IS NULL) = (claim_expires_at IS NULL)),
  CONSTRAINT ck_instance_5 CHECK (claimed_acquisition_id IS NULL OR active_lease_id IS NULL),
  CONSTRAINT ck_instance_6 CHECK (status <> 'WarmReady' OR (ever_claimed = 0 AND claimed_acquisition_id IS NULL AND active_lease_id IS NULL AND workspace_id IS NULL AND pool_key IS NOT NULL)),
  CONSTRAINT ck_instance_7 CHECK (status <> 'Claimed' OR (claimed_acquisition_id IS NOT NULL AND ever_claimed = 1)),
  CONSTRAINT ck_instance_8 CHECK (status <> 'Preparing' OR claimed_acquisition_id IS NOT NULL),
  CONSTRAINT ck_instance_9 CHECK (status <> 'Ready' OR (active_lease_id IS NOT NULL AND pod_uid IS NOT NULL AND runtime_boot_id IS NOT NULL AND workspace_id IS NOT NULL)),
  CONSTRAINT ck_instance_10 CHECK (status <> 'IdleBound' OR (ever_claimed = 1 AND active_lease_id IS NULL AND claimed_acquisition_id IS NULL AND workspace_id IS NOT NULL AND idle_expires_at IS NOT NULL)),
  CONSTRAINT ck_instance_11 CHECK (status <> 'Deleted' OR (physical_gone_at IS NOT NULL AND writes_stopped_at IS NOT NULL AND deleted_at IS NOT NULL)),
  CONSTRAINT ck_instance_12 CHECK (provision_deadline <= hard_expires_at),
  CONSTRAINT ck_instance_13 CHECK (CHAR_LENGTH(id) > 0 AND id !~ '[[:space:]]'),
  CONSTRAINT ck_instance_14 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id !~ '[[:space:]]'),
  CONSTRAINT ck_instance_15 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id !~ '[[:space:]]'),
  CONSTRAINT ck_instance_16 CHECK (generation > 0),
  CONSTRAINT ck_instance_17 CHECK (CHAR_LENGTH(profile_tenant_id) > 0 AND profile_tenant_id !~ '[[:space:]]'),
  CONSTRAINT ck_instance_18 CHECK (CHAR_LENGTH(profile_id) > 0 AND profile_id !~ '[[:space:]]'),
  CONSTRAINT ck_instance_19 CHECK (profile_revision > 0),
  CONSTRAINT ck_instance_20 CHECK (pool_key ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_instance_21 CHECK (reuse_key ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_instance_22 CHECK (spec_hash ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_instance_23 CHECK (CHAR_LENGTH(cluster_id) > 0 AND cluster_id !~ '[[:space:]]'),
  CONSTRAINT ck_instance_24 CHECK (CHAR_LENGTH(pod_uid) > 0 AND pod_uid !~ '[[:space:]]'),
  CONSTRAINT ck_instance_25 CHECK (CHAR_LENGTH(runtime_boot_id) > 0 AND runtime_boot_id !~ '[[:space:]]'),
  CONSTRAINT ck_instance_27 CHECK (CHAR_LENGTH(claimed_acquisition_id) > 0 AND claimed_acquisition_id !~ '[[:space:]]'),
  CONSTRAINT ck_instance_28 CHECK (CHAR_LENGTH(active_lease_id) > 0 AND active_lease_id !~ '[[:space:]]'),
  CONSTRAINT ck_instance_29 CHECK (CHAR_LENGTH(workspace_id) > 0 AND workspace_id !~ '[[:space:]]'),
  CONSTRAINT ck_instance_30 CHECK (workspace_generation > 0),
  CONSTRAINT ck_instance_31 CHECK (admission_epoch > 0),
  CONSTRAINT ck_instance_32 CHECK (jsonb_typeof(resolved_spec) = 'object' AND resolved_spec ? 'schemaVersion' AND resolved_spec ? 'data' AND (resolved_spec->>'schemaVersion') = '1' AND jsonb_typeof(resolved_spec->'data') <> 'null'),
  CONSTRAINT ck_instance_33 CHECK (jsonb_typeof(physical_ref) = 'object' AND physical_ref ? 'schemaVersion' AND physical_ref ? 'data' AND (physical_ref->>'schemaVersion') = '1' AND jsonb_typeof(physical_ref->'data') <> 'null'),
  CONSTRAINT ck_instance_34 CHECK (jsonb_typeof(endpoint) = 'object' AND endpoint ? 'schemaVersion' AND endpoint ? 'data' AND (endpoint->>'schemaVersion') = '1' AND jsonb_typeof(endpoint->'data') <> 'null'),
  CONSTRAINT ck_instance_35 CHECK (jsonb_typeof(runtime_info) = 'object' AND runtime_info ? 'schemaVersion' AND runtime_info ? 'data' AND (runtime_info->>'schemaVersion') = '1' AND jsonb_typeof(runtime_info->'data') <> 'null'),
  CONSTRAINT ck_instance_36 CHECK (jsonb_typeof(effective_capabilities) = 'object' AND effective_capabilities ? 'schemaVersion' AND effective_capabilities ? 'data' AND (effective_capabilities->>'schemaVersion') = '1' AND jsonb_typeof(effective_capabilities->'data') <> 'null'),
  CONSTRAINT ck_instance_37 CHECK (jsonb_typeof(fence_evidence) = 'object' AND fence_evidence ? 'schemaVersion' AND fence_evidence ? 'data' AND (fence_evidence->>'schemaVersion') = '1' AND jsonb_typeof(fence_evidence->'data') <> 'null'),
  CONSTRAINT ck_instance_38 CHECK (jsonb_typeof(failure) = 'object' AND failure ? 'schemaVersion' AND failure ? 'data' AND (failure->>'schemaVersion') = '1' AND jsonb_typeof(failure->'data') <> 'null'),
  CONSTRAINT ck_instance_39 CHECK (jsonb_typeof(conditions) = 'object' AND conditions ? 'schemaVersion' AND conditions ? 'data' AND (conditions->>'schemaVersion') = '1' AND jsonb_typeof(conditions->'data') <> 'null'),
  CONSTRAINT ck_instance_40 CHECK (CHAR_LENGTH(worker_id) > 0 AND worker_id !~ '[[:space:]]'),
  CONSTRAINT ck_instance_41 CHECK (CHAR_LENGTH(claim_token) > 0 AND claim_token !~ '[[:space:]]'),
  CONSTRAINT ck_instance_42 CHECK (version > 0),
  CONSTRAINT ck_instance_43 CHECK ((worker_id IS NULL) = (claim_token IS NULL) AND (worker_id IS NULL) = (claim_until IS NULL))
);

CREATE UNIQUE INDEX uq_instance_1 ON sandbox_instance (id, generation);
CREATE UNIQUE INDEX uq_instance_2 ON sandbox_instance (cluster_id, namespace, pod_name);
CREATE UNIQUE INDEX uq_instance_3 ON sandbox_instance (cluster_id, pod_uid);
CREATE UNIQUE INDEX uq_instance_4 ON sandbox_instance (claimed_acquisition_id);
CREATE UNIQUE INDEX uq_instance_5 ON sandbox_instance (active_lease_id);
CREATE INDEX ix_instance_1 ON sandbox_instance (status, next_reconcile_at, id);
CREATE INDEX ix_instance_2 ON sandbox_instance (pool_key, status, created_at, id);
CREATE INDEX ix_instance_3 ON sandbox_instance (tenant_id, project_id, status, id);
CREATE INDEX ix_instance_4 ON sandbox_instance (workspace_id, workspace_generation);
CREATE INDEX ix_instance_5 ON sandbox_instance (status, idle_expires_at);
CREATE INDEX ix_instance_6 ON sandbox_instance (status, hard_expires_at);

COMMENT ON COLUMN sandbox_instance.resolved_spec IS 'Envelope data: contract.InstanceSpec';
COMMENT ON COLUMN sandbox_instance.physical_ref IS 'Envelope data: contract.PhysicalRef';
COMMENT ON COLUMN sandbox_instance.endpoint IS 'Envelope data: contract.Endpoint (private reference DTO)';
COMMENT ON COLUMN sandbox_instance.runtime_info IS 'Envelope data: contract.RuntimeInfo';
COMMENT ON COLUMN sandbox_instance.effective_capabilities IS 'Envelope data: []sandboxv1.Capability';
COMMENT ON COLUMN sandbox_instance.fence_evidence IS 'Envelope data: contract.FenceEvidence';
COMMENT ON COLUMN sandbox_instance.failure IS 'Envelope data: sandboxv1.Error';
COMMENT ON COLUMN sandbox_instance.conditions IS 'Envelope data: []sandboxv1.Condition';

-- Active -> Releasing closes admission and advances admission_epoch in the same transaction as ReleaseLease intent. Expired/Quarantined also close admission but retain writer ownership until proven stopped. Snapshots never follow a new instance generation. ReleaseMode is assigned by policy for expiry as well as explicit release.
CREATE TABLE sandbox_lease (
  id VARCHAR(64) COLLATE "C" NOT NULL,
  tenant_id VARCHAR(64) COLLATE "C" NOT NULL,
  project_id VARCHAR(64) COLLATE "C" NOT NULL,
  acquisition_id VARCHAR(64) COLLATE "C" NOT NULL,
  instance_id VARCHAR(64) COLLATE "C" NOT NULL,
  instance_generation BIGINT NOT NULL,
  pod_uid VARCHAR(64) COLLATE "C" NOT NULL,
  runtime_boot_id VARCHAR(64) COLLATE "C" NOT NULL,
  workspace_id VARCHAR(64) COLLATE "C" NOT NULL,
  workspace_generation BIGINT NOT NULL,
  holder_run_id VARCHAR(64) COLLATE "C" NOT NULL,
  holder_run_epoch BIGINT NOT NULL,
  caller_id VARCHAR(64) COLLATE "C" NOT NULL,
  scope_type VARCHAR(32) COLLATE "C" NOT NULL,
  scope_id VARCHAR(64) COLLATE "C" NOT NULL,
  fencing_token BIGINT NOT NULL,
  admission_epoch BIGINT NOT NULL,
  status VARCHAR(32) COLLATE "C" NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  hard_expires_at TIMESTAMPTZ NOT NULL,
  admission_closed_at TIMESTAMPTZ NULL,
  release_mode VARCHAR(32) COLLATE "C" NULL,
  release_operation_id VARCHAR(64) COLLATE "C" NULL,
  releasing_at TIMESTAMPTZ NULL,
  released_at TIMESTAMPTZ NULL,
  runtime_barrier_confirmed_at TIMESTAMPTZ NULL,
  process_tree_stopped_at TIMESTAMPTZ NULL,
  sessions_closed_at TIMESTAMPTZ NULL,
  credentials_revoked_at TIMESTAMPTZ NULL,
  permissions JSONB NOT NULL,
  credential_grants JSONB NOT NULL,
  termination JSONB NULL,
  drain_evidence JSONB NULL,
  failure JSONB NULL,
  next_reconcile_at TIMESTAMPTZ NULL,
  worker_id VARCHAR(64) COLLATE "C" NULL,
  claim_token VARCHAR(64) COLLATE "C" NULL,
  claim_until TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT ck_lease_1 CHECK (scope_type IN ('Task', 'Session', 'Workspace')),
  CONSTRAINT ck_lease_2 CHECK (status IN ('Active', 'Releasing', 'Expired', 'Released', 'Quarantined')),
  CONSTRAINT ck_lease_3 CHECK (release_mode IN ('Retain', 'Destroy')),
  CONSTRAINT ck_lease_4 CHECK (expires_at <= hard_expires_at),
  CONSTRAINT ck_lease_5 CHECK ((status = 'Active' AND admission_closed_at IS NULL) OR (status <> 'Active' AND admission_closed_at IS NOT NULL)),
  CONSTRAINT ck_lease_6 CHECK (status <> 'Released' OR (released_at IS NOT NULL AND process_tree_stopped_at IS NOT NULL AND sessions_closed_at IS NOT NULL AND credentials_revoked_at IS NOT NULL AND drain_evidence IS NOT NULL)),
  CONSTRAINT ck_lease_7 CHECK (CHAR_LENGTH(id) > 0 AND id !~ '[[:space:]]'),
  CONSTRAINT ck_lease_8 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id !~ '[[:space:]]'),
  CONSTRAINT ck_lease_9 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id !~ '[[:space:]]'),
  CONSTRAINT ck_lease_10 CHECK (CHAR_LENGTH(acquisition_id) > 0 AND acquisition_id !~ '[[:space:]]'),
  CONSTRAINT ck_lease_11 CHECK (CHAR_LENGTH(instance_id) > 0 AND instance_id !~ '[[:space:]]'),
  CONSTRAINT ck_lease_12 CHECK (instance_generation > 0),
  CONSTRAINT ck_lease_13 CHECK (CHAR_LENGTH(pod_uid) > 0 AND pod_uid !~ '[[:space:]]'),
  CONSTRAINT ck_lease_14 CHECK (CHAR_LENGTH(runtime_boot_id) > 0 AND runtime_boot_id !~ '[[:space:]]'),
  CONSTRAINT ck_lease_15 CHECK (CHAR_LENGTH(workspace_id) > 0 AND workspace_id !~ '[[:space:]]'),
  CONSTRAINT ck_lease_16 CHECK (workspace_generation > 0),
  CONSTRAINT ck_lease_17 CHECK (CHAR_LENGTH(holder_run_id) > 0 AND holder_run_id !~ '[[:space:]]'),
  CONSTRAINT ck_lease_18 CHECK (CHAR_LENGTH(caller_id) > 0 AND caller_id !~ '[[:space:]]'),
  CONSTRAINT ck_lease_19 CHECK (CHAR_LENGTH(scope_id) > 0 AND scope_id !~ '[[:space:]]'),
  CONSTRAINT ck_lease_20 CHECK (fencing_token > 0),
  CONSTRAINT ck_lease_21 CHECK (admission_epoch > 0),
  CONSTRAINT ck_lease_22 CHECK (CHAR_LENGTH(release_operation_id) > 0 AND release_operation_id !~ '[[:space:]]'),
  CONSTRAINT ck_lease_23 CHECK (jsonb_typeof(permissions) = 'object' AND permissions ? 'schemaVersion' AND permissions ? 'data' AND (permissions->>'schemaVersion') = '1' AND jsonb_typeof(permissions->'data') <> 'null'),
  CONSTRAINT ck_lease_24 CHECK (jsonb_typeof(credential_grants) = 'object' AND credential_grants ? 'schemaVersion' AND credential_grants ? 'data' AND (credential_grants->>'schemaVersion') = '1' AND jsonb_typeof(credential_grants->'data') <> 'null'),
  CONSTRAINT ck_lease_25 CHECK (jsonb_typeof(termination) = 'object' AND termination ? 'schemaVersion' AND termination ? 'data' AND (termination->>'schemaVersion') = '1' AND jsonb_typeof(termination->'data') <> 'null'),
  CONSTRAINT ck_lease_26 CHECK (jsonb_typeof(drain_evidence) = 'object' AND drain_evidence ? 'schemaVersion' AND drain_evidence ? 'data' AND (drain_evidence->>'schemaVersion') = '1' AND jsonb_typeof(drain_evidence->'data') <> 'null'),
  CONSTRAINT ck_lease_27 CHECK (jsonb_typeof(failure) = 'object' AND failure ? 'schemaVersion' AND failure ? 'data' AND (failure->>'schemaVersion') = '1' AND jsonb_typeof(failure->'data') <> 'null'),
  CONSTRAINT ck_lease_28 CHECK (CHAR_LENGTH(worker_id) > 0 AND worker_id !~ '[[:space:]]'),
  CONSTRAINT ck_lease_29 CHECK (CHAR_LENGTH(claim_token) > 0 AND claim_token !~ '[[:space:]]'),
  CONSTRAINT ck_lease_30 CHECK (version > 0),
  CONSTRAINT ck_lease_31 CHECK ((worker_id IS NULL) = (claim_token IS NULL) AND (worker_id IS NULL) = (claim_until IS NULL)),
  CONSTRAINT ck_lease_32 CHECK (holder_run_epoch BETWEEN 1 AND 9007199254740991)
);

CREATE UNIQUE INDEX uq_lease_1 ON sandbox_lease (acquisition_id);
CREATE UNIQUE INDEX uq_lease_2 ON sandbox_lease (workspace_id, fencing_token);
CREATE INDEX ix_lease_1 ON sandbox_lease (instance_id, instance_generation, status);
CREATE INDEX ix_lease_2 ON sandbox_lease (tenant_id, holder_run_id, status);
CREATE INDEX ix_lease_3 ON sandbox_lease (status, expires_at);
CREATE INDEX ix_lease_4 ON sandbox_lease (status, hard_expires_at);
CREATE INDEX ix_lease_5 ON sandbox_lease (status, next_reconcile_at, id);
CREATE INDEX ix_lease_6 ON sandbox_lease (workspace_id, workspace_generation, status);

COMMENT ON COLUMN sandbox_lease.permissions IS 'Envelope data: []string';
COMMENT ON COLUMN sandbox_lease.credential_grants IS 'Envelope data: []contract.CredentialGrant (references only)';
COMMENT ON COLUMN sandbox_lease.termination IS 'Envelope data: sandboxv1.TerminationResult';
COMMENT ON COLUMN sandbox_lease.drain_evidence IS 'Envelope data: contract.DrainEvidence';
COMMENT ON COLUMN sandbox_lease.failure IS 'Envelope data: sandboxv1.Error';

-- Hash preimage is design section 6.1, including all policy revisions and asset digest. Upsert the stable empty slot then lock it; never delete/recreate a busy slot or skip its ownership lock. NULL owner means vacant, not an invitation to ignore workspace ownership.
CREATE TABLE sandbox_reuse_slot (
  tenant_id VARCHAR(64) COLLATE "C" NOT NULL,
  project_id VARCHAR(64) COLLATE "C" NOT NULL,
  reuse_key CHAR(64) COLLATE "C" NOT NULL,
  scope_type VARCHAR(32) COLLATE "C" NOT NULL,
  scope_id VARCHAR(64) COLLATE "C" NOT NULL,
  profile_tenant_id VARCHAR(64) COLLATE "C" NOT NULL,
  profile_id VARCHAR(64) COLLATE "C" NOT NULL,
  profile_revision BIGINT NOT NULL,
  workspace_id VARCHAR(64) COLLATE "C" NOT NULL,
  workspace_generation BIGINT NOT NULL,
  isolation_policy_revision BIGINT NOT NULL,
  access_policy_revision BIGINT NOT NULL,
  readonly_asset_digest CHAR(64) COLLATE "C" NOT NULL,
  preparing_acquisition_id VARCHAR(64) COLLATE "C" NULL,
  instance_id VARCHAR(64) COLLATE "C" NULL,
  instance_generation BIGINT NULL,
  writer_lease_id VARCHAR(64) COLLATE "C" NULL,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (tenant_id, reuse_key),
  CONSTRAINT ck_reuse_slot_1 CHECK (scope_type IN ('Task', 'Session', 'Workspace')),
  CONSTRAINT ck_reuse_slot_2 CHECK (preparing_acquisition_id IS NULL OR writer_lease_id IS NULL),
  CONSTRAINT ck_reuse_slot_3 CHECK ((instance_id IS NULL) = (instance_generation IS NULL)),
  CONSTRAINT ck_reuse_slot_4 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id !~ '[[:space:]]'),
  CONSTRAINT ck_reuse_slot_5 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id !~ '[[:space:]]'),
  CONSTRAINT ck_reuse_slot_6 CHECK (reuse_key ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_reuse_slot_7 CHECK (CHAR_LENGTH(scope_id) > 0 AND scope_id !~ '[[:space:]]'),
  CONSTRAINT ck_reuse_slot_8 CHECK (CHAR_LENGTH(profile_tenant_id) > 0 AND profile_tenant_id !~ '[[:space:]]'),
  CONSTRAINT ck_reuse_slot_9 CHECK (CHAR_LENGTH(profile_id) > 0 AND profile_id !~ '[[:space:]]'),
  CONSTRAINT ck_reuse_slot_10 CHECK (profile_revision > 0),
  CONSTRAINT ck_reuse_slot_11 CHECK (CHAR_LENGTH(workspace_id) > 0 AND workspace_id !~ '[[:space:]]'),
  CONSTRAINT ck_reuse_slot_12 CHECK (workspace_generation > 0),
  CONSTRAINT ck_reuse_slot_13 CHECK (isolation_policy_revision > 0),
  CONSTRAINT ck_reuse_slot_14 CHECK (access_policy_revision > 0),
  CONSTRAINT ck_reuse_slot_15 CHECK (readonly_asset_digest ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_reuse_slot_16 CHECK (CHAR_LENGTH(preparing_acquisition_id) > 0 AND preparing_acquisition_id !~ '[[:space:]]'),
  CONSTRAINT ck_reuse_slot_17 CHECK (CHAR_LENGTH(instance_id) > 0 AND instance_id !~ '[[:space:]]'),
  CONSTRAINT ck_reuse_slot_18 CHECK (instance_generation > 0),
  CONSTRAINT ck_reuse_slot_19 CHECK (CHAR_LENGTH(writer_lease_id) > 0 AND writer_lease_id !~ '[[:space:]]'),
  CONSTRAINT ck_reuse_slot_20 CHECK (version > 0)
);

CREATE UNIQUE INDEX uq_reuse_slot_1 ON sandbox_reuse_slot (preparing_acquisition_id);
CREATE UNIQUE INDEX uq_reuse_slot_2 ON sandbox_reuse_slot (writer_lease_id);
CREATE INDEX ix_reuse_slot_1 ON sandbox_reuse_slot (tenant_id, project_id, scope_type, scope_id);
CREATE INDEX ix_reuse_slot_2 ON sandbox_reuse_slot (instance_id, instance_generation);
CREATE INDEX ix_reuse_slot_3 ON sandbox_reuse_slot (workspace_id, workspace_generation);

-- Unified idempotent mutation ledger, not one table per verb. Actions include RenewLease, ReleaseLease, RetryRequiredSave, DiscardBlockedSave, WriteFile, WriteStdin, Create/RemoveWorktree, Create/RestoreCheckpoint, CancelAcquisition/Execution, CreateTerminal/Browser/Preview, Connect/CloseAccessSession, WriteTerminal/ResizeTerminal/SignalTerminal, Publish/DisableProfile, Set/DisablePool, SetQuota, PublishArtifact, Issue/RevokeCredential. Public asynchronous and synchronous actions use OperationKind; internal actions remain internal. StartExecution uses execution table, not a second action record. Cancelled operations map to Failed with a cancellation reason; SDK OperationStatus has no Cancelled member.
CREATE TABLE sandbox_action (
  id VARCHAR(64) COLLATE "C" NOT NULL,
  tenant_id VARCHAR(64) COLLATE "C" NOT NULL,
  project_id VARCHAR(64) COLLATE "C" NOT NULL,
  caller_id VARCHAR(64) COLLATE "C" NOT NULL,
  action VARCHAR(255) COLLATE "C" NOT NULL,
  request_key VARCHAR(128) COLLATE "C" NOT NULL,
  spec_hash CHAR(64) COLLATE "C" NOT NULL,
  status VARCHAR(32) COLLATE "C" NOT NULL,
  target_kind VARCHAR(32) COLLATE "C" NOT NULL,
  target_id VARCHAR(64) COLLATE "C" NOT NULL,
  target_revision BIGINT NOT NULL DEFAULT 0,
  lease_id VARCHAR(64) COLLATE "C" NULL,
  acquisition_id VARCHAR(64) COLLATE "C" NULL,
  instance_id VARCHAR(64) COLLATE "C" NULL,
  instance_generation BIGINT NULL,
  pod_uid VARCHAR(64) COLLATE "C" NULL,
  runtime_boot_id VARCHAR(64) COLLATE "C" NULL,
  fencing_token BIGINT NULL,
  admission_epoch BIGINT NULL,
  workspace_id VARCHAR(64) COLLATE "C" NULL,
  workspace_generation BIGINT NULL,
  dispatch_attempt_id VARCHAR(64) COLLATE "C" NULL,
  dispatch_started_at TIMESTAMPTZ NULL,
  dispatch_outcome VARCHAR(32) COLLATE "C" NULL,
  replay_policy VARCHAR(32) COLLATE "C" NOT NULL,
  external_ref VARCHAR(1024) COLLATE "C" NULL,
  evidence_ref VARCHAR(1024) COLLATE "C" NULL,
  completed_at TIMESTAMPTZ NULL,
  input JSONB NULL,
  result JSONB NULL,
  failure JSONB NULL,
  next_reconcile_at TIMESTAMPTZ NULL,
  worker_id VARCHAR(64) COLLATE "C" NULL,
  claim_token VARCHAR(64) COLLATE "C" NULL,
  claim_until TIMESTAMPTZ NULL,
  tombstoned_at TIMESTAMPTZ NULL,
  result_expires_at TIMESTAMPTZ NULL,
  replay_closed_at TIMESTAMPTZ NULL,
  purge_after TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT ck_action_target_revision CHECK ((target_kind = 'ProfileRevision' AND target_revision > 0) OR (target_kind <> 'ProfileRevision' AND target_revision = 0)),
  CONSTRAINT ck_action_2 CHECK (status IN ('Pending', 'Running', 'Succeeded', 'Failed', 'Unknown')),
  CONSTRAINT ck_action_3 CHECK (dispatch_outcome IN ('Accepted', 'NotAccepted', 'Unknown')),
  CONSTRAINT ck_action_4 CHECK (replay_policy IN ('DatabaseOnly', 'BackendDeduplicated', 'ReconcileOnly')),
  CONSTRAINT ck_action_5 CHECK ((instance_id IS NULL) = (instance_generation IS NULL)),
  CONSTRAINT ck_action_6 CHECK ((workspace_id IS NULL) = (workspace_generation IS NULL)),
  CONSTRAINT ck_action_10 CHECK (tombstoned_at IS NOT NULL OR input IS NOT NULL),
  CONSTRAINT ck_action_11 CHECK (tombstoned_at IS NOT NULL OR status <> 'Succeeded' OR result IS NOT NULL),
  CONSTRAINT ck_action_12 CHECK (tombstoned_at IS NOT NULL OR status <> 'Failed' OR failure IS NOT NULL),
  CONSTRAINT ck_action_13 CHECK (CHAR_LENGTH(id) > 0 AND id !~ '[[:space:]]'),
  CONSTRAINT ck_action_14 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id !~ '[[:space:]]'),
  CONSTRAINT ck_action_15 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id !~ '[[:space:]]'),
  CONSTRAINT ck_action_16 CHECK (CHAR_LENGTH(caller_id) > 0 AND caller_id !~ '[[:space:]]'),
  CONSTRAINT ck_action_18 CHECK (CHAR_LENGTH(request_key) > 0 AND request_key !~ '[[:space:]]'),
  CONSTRAINT ck_action_19 CHECK (spec_hash ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_action_20 CHECK (CHAR_LENGTH(target_id) > 0 AND target_id !~ '[[:space:]]'),
  CONSTRAINT ck_action_21 CHECK (CHAR_LENGTH(lease_id) > 0 AND lease_id !~ '[[:space:]]'),
  CONSTRAINT ck_action_22 CHECK (CHAR_LENGTH(acquisition_id) > 0 AND acquisition_id !~ '[[:space:]]'),
  CONSTRAINT ck_action_23 CHECK (CHAR_LENGTH(instance_id) > 0 AND instance_id !~ '[[:space:]]'),
  CONSTRAINT ck_action_24 CHECK (instance_generation > 0),
  CONSTRAINT ck_action_25 CHECK (CHAR_LENGTH(pod_uid) > 0 AND pod_uid !~ '[[:space:]]'),
  CONSTRAINT ck_action_26 CHECK (CHAR_LENGTH(runtime_boot_id) > 0 AND runtime_boot_id !~ '[[:space:]]'),
  CONSTRAINT ck_action_27 CHECK (fencing_token > 0),
  CONSTRAINT ck_action_28 CHECK (admission_epoch > 0),
  CONSTRAINT ck_action_29 CHECK (CHAR_LENGTH(workspace_id) > 0 AND workspace_id !~ '[[:space:]]'),
  CONSTRAINT ck_action_30 CHECK (workspace_generation > 0),
  CONSTRAINT ck_action_31 CHECK (CHAR_LENGTH(dispatch_attempt_id) > 0 AND dispatch_attempt_id !~ '[[:space:]]'),
  CONSTRAINT ck_action_32 CHECK (jsonb_typeof(input) = 'object' AND input ? 'schemaVersion' AND input ? 'data' AND (input->>'schemaVersion') = '1' AND jsonb_typeof(input->'data') <> 'null'),
  CONSTRAINT ck_action_33 CHECK (jsonb_typeof(result) = 'object' AND result ? 'schemaVersion' AND result ? 'data' AND (result->>'schemaVersion') = '1' AND jsonb_typeof(result->'data') <> 'null'),
  CONSTRAINT ck_action_34 CHECK (jsonb_typeof(failure) = 'object' AND failure ? 'schemaVersion' AND failure ? 'data' AND (failure->>'schemaVersion') = '1' AND jsonb_typeof(failure->'data') <> 'null'),
  CONSTRAINT ck_action_35 CHECK (CHAR_LENGTH(worker_id) > 0 AND worker_id !~ '[[:space:]]'),
  CONSTRAINT ck_action_36 CHECK (CHAR_LENGTH(claim_token) > 0 AND claim_token !~ '[[:space:]]'),
  CONSTRAINT ck_action_37 CHECK (version > 0),
  CONSTRAINT ck_action_38 CHECK ((worker_id IS NULL) = (claim_token IS NULL) AND (worker_id IS NULL) = (claim_until IS NULL)),
  CONSTRAINT ck_action_39 CHECK (purge_after IS NULL OR (tombstoned_at IS NOT NULL AND replay_closed_at IS NOT NULL AND purge_after >= replay_closed_at))
);

CREATE UNIQUE INDEX uq_action_1 ON sandbox_action (tenant_id, caller_id, action, target_kind, target_id, target_revision, request_key);
CREATE INDEX ix_action_1 ON sandbox_action (lease_id, status, id);
CREATE INDEX ix_action_2 ON sandbox_action (acquisition_id, status, id);
CREATE INDEX ix_action_3 ON sandbox_action (target_kind, target_id, target_revision, created_at);
CREATE INDEX ix_action_4 ON sandbox_action (status, next_reconcile_at, id);
CREATE INDEX ix_action_5 ON sandbox_action (tenant_id, caller_id, created_at, id);
CREATE INDEX ix_action_6 ON sandbox_action (tombstoned_at, purge_after);

COMMENT ON COLUMN sandbox_action.target_revision IS '0 means unversioned target; positive for ProfileRevision';
COMMENT ON COLUMN sandbox_action.input IS 'Envelope data: action-specific normalized request DTO';
COMMENT ON COLUMN sandbox_action.result IS 'Envelope data: action-specific response DTO';
COMMENT ON COLUMN sandbox_action.failure IS 'Envelope data: sandboxv1.Error';

-- BeginDispatch locks lease before execution, checks Active+expiry+epoch and persists Starting/attempt before network IO. Claim expiry never resets Starting. backend_ref may be NULL in Unknown. ReconcileOnly permits exactly one network dispatch; BackendDeduplicated retries require verified adapter guarantees and the SAME execution ID. Failed need not have an exit code when authoritative rejection proves no process started.
CREATE TABLE sandbox_execution (
  id VARCHAR(64) COLLATE "C" NOT NULL,
  tenant_id VARCHAR(64) COLLATE "C" NOT NULL,
  project_id VARCHAR(64) COLLATE "C" NOT NULL,
  lease_id VARCHAR(64) COLLATE "C" NOT NULL,
  request_key VARCHAR(128) COLLATE "C" NOT NULL,
  spec_hash CHAR(64) COLLATE "C" NOT NULL,
  instance_id VARCHAR(64) COLLATE "C" NOT NULL,
  instance_generation BIGINT NOT NULL,
  pod_uid VARCHAR(64) COLLATE "C" NOT NULL,
  runtime_boot_id VARCHAR(64) COLLATE "C" NOT NULL,
  workspace_id VARCHAR(64) COLLATE "C" NOT NULL,
  workspace_generation BIGINT NOT NULL,
  holder_run_id VARCHAR(64) COLLATE "C" NOT NULL,
  member_id VARCHAR(64) COLLATE "C" NULL,
  worktree_id VARCHAR(64) COLLATE "C" NULL,
  fencing_token BIGINT NOT NULL,
  admission_epoch BIGINT NOT NULL,
  status VARCHAR(32) COLLATE "C" NOT NULL,
  cwd VARCHAR(1024) NOT NULL,
  command_mode VARCHAR(32) COLLATE "C" NOT NULL,
  deadline TIMESTAMPTZ NOT NULL,
  max_output_bytes BIGINT NOT NULL,
  stdin_closed BOOLEAN NOT NULL,
  next_input_sequence BIGINT NOT NULL,
  dispatch_attempt_id VARCHAR(64) COLLATE "C" NULL,
  dispatch_epoch BIGINT NULL,
  dispatch_started_at TIMESTAMPTZ NULL,
  dispatch_outcome VARCHAR(32) COLLATE "C" NULL,
  replay_policy VARCHAR(32) COLLATE "C" NOT NULL,
  backend_ref VARCHAR(255) COLLATE "C" NULL,
  started_at TIMESTAMPTZ NULL,
  completed_at TIMESTAMPTZ NULL,
  observed_at TIMESTAMPTZ NULL,
  exit_code BIGINT NULL,
  exit_signal VARCHAR(255) COLLATE "C" NULL,
  output_completeness VARCHAR(32) COLLATE "C" NOT NULL,
  output_bytes BIGINT NOT NULL,
  source_cursor VARCHAR(1024) COLLATE "C" NULL,
  source_cursor_kind VARCHAR(32) COLLATE "C" NULL,
  cancel_operation_id VARCHAR(64) COLLATE "C" NULL,
  request_spec JSONB NULL,
  command_spec JSONB NULL,
  termination JSONB NULL,
  failure JSONB NULL,
  next_reconcile_at TIMESTAMPTZ NULL,
  worker_id VARCHAR(64) COLLATE "C" NULL,
  claim_token VARCHAR(64) COLLATE "C" NULL,
  claim_until TIMESTAMPTZ NULL,
  tombstoned_at TIMESTAMPTZ NULL,
  result_expires_at TIMESTAMPTZ NULL,
  replay_closed_at TIMESTAMPTZ NULL,
  purge_after TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT ck_execution_1 CHECK (status IN ('Pending', 'Starting', 'Running', 'Succeeded', 'Failed', 'TimedOut', 'Cancelled', 'Unknown')),
  CONSTRAINT ck_execution_2 CHECK (command_mode IN ('Argv', 'Shell')),
  CONSTRAINT ck_execution_3 CHECK (dispatch_outcome IN ('Accepted', 'NotAccepted', 'Unknown')),
  CONSTRAINT ck_execution_4 CHECK (replay_policy IN ('BackendDeduplicated', 'ReconcileOnly')),
  CONSTRAINT ck_execution_5 CHECK (output_completeness IN ('Pending', 'Complete', 'Truncated', 'Incomplete')),
  CONSTRAINT ck_execution_6 CHECK (source_cursor_kind IN ('Byte', 'Line', 'Event', 'Opaque')),
  CONSTRAINT ck_execution_8 CHECK (status NOT IN ('Starting', 'Running', 'Unknown') OR (dispatch_attempt_id IS NOT NULL AND dispatch_started_at IS NOT NULL)),
  CONSTRAINT ck_execution_9 CHECK (status <> 'Succeeded' OR (exit_code IS NOT NULL AND exit_code = 0 AND completed_at IS NOT NULL)),
  CONSTRAINT ck_execution_10 CHECK (tombstoned_at IS NOT NULL OR (request_spec IS NOT NULL AND command_spec IS NOT NULL)),
  CONSTRAINT ck_execution_11 CHECK (CHAR_LENGTH(id) > 0 AND id !~ '[[:space:]]'),
  CONSTRAINT ck_execution_12 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id !~ '[[:space:]]'),
  CONSTRAINT ck_execution_13 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id !~ '[[:space:]]'),
  CONSTRAINT ck_execution_14 CHECK (CHAR_LENGTH(lease_id) > 0 AND lease_id !~ '[[:space:]]'),
  CONSTRAINT ck_execution_15 CHECK (CHAR_LENGTH(request_key) > 0 AND request_key !~ '[[:space:]]'),
  CONSTRAINT ck_execution_16 CHECK (spec_hash ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_execution_17 CHECK (CHAR_LENGTH(instance_id) > 0 AND instance_id !~ '[[:space:]]'),
  CONSTRAINT ck_execution_18 CHECK (instance_generation > 0),
  CONSTRAINT ck_execution_19 CHECK (CHAR_LENGTH(pod_uid) > 0 AND pod_uid !~ '[[:space:]]'),
  CONSTRAINT ck_execution_20 CHECK (CHAR_LENGTH(runtime_boot_id) > 0 AND runtime_boot_id !~ '[[:space:]]'),
  CONSTRAINT ck_execution_21 CHECK (CHAR_LENGTH(workspace_id) > 0 AND workspace_id !~ '[[:space:]]'),
  CONSTRAINT ck_execution_22 CHECK (workspace_generation > 0),
  CONSTRAINT ck_execution_23 CHECK (CHAR_LENGTH(holder_run_id) > 0 AND holder_run_id !~ '[[:space:]]'),
  CONSTRAINT ck_execution_24 CHECK (CHAR_LENGTH(member_id) > 0 AND member_id !~ '[[:space:]]'),
  CONSTRAINT ck_execution_25 CHECK (CHAR_LENGTH(worktree_id) > 0 AND worktree_id !~ '[[:space:]]'),
  CONSTRAINT ck_execution_28 CHECK (fencing_token > 0),
  CONSTRAINT ck_execution_29 CHECK (admission_epoch > 0),
  CONSTRAINT ck_execution_30 CHECK (max_output_bytes >= 0),
  CONSTRAINT ck_execution_32 CHECK (next_input_sequence > 0),
  CONSTRAINT ck_execution_33 CHECK (CHAR_LENGTH(dispatch_attempt_id) > 0 AND dispatch_attempt_id !~ '[[:space:]]'),
  CONSTRAINT ck_execution_34 CHECK (dispatch_epoch > 0),
  CONSTRAINT ck_execution_35 CHECK (output_bytes >= 0),
  CONSTRAINT ck_execution_36 CHECK (CHAR_LENGTH(cancel_operation_id) > 0 AND cancel_operation_id !~ '[[:space:]]'),
  CONSTRAINT ck_execution_37 CHECK (jsonb_typeof(request_spec) = 'object' AND request_spec ? 'schemaVersion' AND request_spec ? 'data' AND (request_spec->>'schemaVersion') = '1' AND jsonb_typeof(request_spec->'data') <> 'null'),
  CONSTRAINT ck_execution_38 CHECK (jsonb_typeof(command_spec) = 'object' AND command_spec ? 'schemaVersion' AND command_spec ? 'data' AND (command_spec->>'schemaVersion') = '1' AND jsonb_typeof(command_spec->'data') <> 'null'),
  CONSTRAINT ck_execution_39 CHECK (jsonb_typeof(termination) = 'object' AND termination ? 'schemaVersion' AND termination ? 'data' AND (termination->>'schemaVersion') = '1' AND jsonb_typeof(termination->'data') <> 'null'),
  CONSTRAINT ck_execution_40 CHECK (jsonb_typeof(failure) = 'object' AND failure ? 'schemaVersion' AND failure ? 'data' AND (failure->>'schemaVersion') = '1' AND jsonb_typeof(failure->'data') <> 'null'),
  CONSTRAINT ck_execution_41 CHECK (CHAR_LENGTH(worker_id) > 0 AND worker_id !~ '[[:space:]]'),
  CONSTRAINT ck_execution_42 CHECK (CHAR_LENGTH(claim_token) > 0 AND claim_token !~ '[[:space:]]'),
  CONSTRAINT ck_execution_43 CHECK (version > 0),
  CONSTRAINT ck_execution_44 CHECK ((worker_id IS NULL) = (claim_token IS NULL) AND (worker_id IS NULL) = (claim_until IS NULL)),
  CONSTRAINT ck_execution_45 CHECK (purge_after IS NULL OR (tombstoned_at IS NOT NULL AND replay_closed_at IS NOT NULL AND purge_after >= replay_closed_at))
);

CREATE UNIQUE INDEX uq_execution_1 ON sandbox_execution (lease_id, request_key);
CREATE UNIQUE INDEX uq_execution_2 ON sandbox_execution (instance_id, instance_generation, runtime_boot_id, backend_ref);
CREATE INDEX ix_execution_1 ON sandbox_execution (lease_id, status, id);
CREATE INDEX ix_execution_2 ON sandbox_execution (status, next_reconcile_at, id);
CREATE INDEX ix_execution_3 ON sandbox_execution (instance_id, instance_generation, status);
CREATE INDEX ix_execution_4 ON sandbox_execution (workspace_id, workspace_generation, created_at);
CREATE INDEX ix_execution_5 ON sandbox_execution (tenant_id, project_id, created_at, id);
CREATE INDEX ix_execution_6 ON sandbox_execution (status, deadline);
CREATE INDEX ix_execution_7 ON sandbox_execution (tombstoned_at, purge_after);

COMMENT ON COLUMN sandbox_execution.request_spec IS 'Envelope data: contract.StoredExecutionRequest';
COMMENT ON COLUMN sandbox_execution.command_spec IS 'Envelope data: contract.CommandSpec (secret references only)';
COMMENT ON COLUMN sandbox_execution.termination IS 'Envelope data: sandboxv1.TerminationResult';
COMMENT ON COLUMN sandbox_execution.failure IS 'Envelope data: sandboxv1.Error';

-- Part of the SAME tenant/project/task/root lease/workspace; never a new cross-tenant workspace or security boundary. Unique active branch/path reservations include Failed until cleanup is confirmed. Removed clears active keys so history does not prevent reuse. Main integration checkout must also have a reserved worktree row. Canonical paths and branch names are validated before hashing.
CREATE TABLE sandbox_worktree (
  id VARCHAR(64) COLLATE "C" NOT NULL,
  tenant_id VARCHAR(64) COLLATE "C" NOT NULL,
  project_id VARCHAR(64) COLLATE "C" NOT NULL,
  operation_id VARCHAR(64) COLLATE "C" NOT NULL,
  lease_id VARCHAR(64) COLLATE "C" NOT NULL,
  request_key VARCHAR(128) COLLATE "C" NOT NULL,
  spec_hash CHAR(64) COLLATE "C" NOT NULL,
  workspace_id VARCHAR(64) COLLATE "C" NOT NULL,
  workspace_generation BIGINT NOT NULL,
  task_id VARCHAR(64) COLLATE "C" NOT NULL,
  holder_run_id VARCHAR(64) COLLATE "C" NOT NULL,
  member_id VARCHAR(64) COLLATE "C" NOT NULL,
  base_commit VARCHAR(255) COLLATE "C" NOT NULL,
  branch VARCHAR(255) COLLATE "C" NOT NULL,
  path VARCHAR(1024) NOT NULL,
  path_hash CHAR(64) COLLATE "C" NOT NULL,
  active_branch VARCHAR(255) COLLATE "C" NULL,
  active_path_hash CHAR(64) COLLATE "C" NULL,
  head_commit VARCHAR(255) COLLATE "C" NULL,
  status VARCHAR(32) COLLATE "C" NOT NULL,
  removed_at TIMESTAMPTZ NULL,
  failure JSONB NULL,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT ck_worktree_1 CHECK (status IN ('Creating', 'Ready', 'Removing', 'Removed', 'Failed')),
  CONSTRAINT ck_worktree_2 CHECK ((status = 'Removed' AND active_branch IS NULL AND active_path_hash IS NULL AND removed_at IS NOT NULL) OR (status <> 'Removed' AND active_branch IS NOT NULL AND active_branch = branch AND active_path_hash IS NOT NULL AND active_path_hash = path_hash)),
  CONSTRAINT ck_worktree_3 CHECK (CHAR_LENGTH(id) > 0 AND id !~ '[[:space:]]'),
  CONSTRAINT ck_worktree_4 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id !~ '[[:space:]]'),
  CONSTRAINT ck_worktree_5 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id !~ '[[:space:]]'),
  CONSTRAINT ck_worktree_6 CHECK (CHAR_LENGTH(operation_id) > 0 AND operation_id !~ '[[:space:]]'),
  CONSTRAINT ck_worktree_7 CHECK (CHAR_LENGTH(lease_id) > 0 AND lease_id !~ '[[:space:]]'),
  CONSTRAINT ck_worktree_8 CHECK (CHAR_LENGTH(request_key) > 0 AND request_key !~ '[[:space:]]'),
  CONSTRAINT ck_worktree_9 CHECK (spec_hash ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_worktree_10 CHECK (CHAR_LENGTH(workspace_id) > 0 AND workspace_id !~ '[[:space:]]'),
  CONSTRAINT ck_worktree_11 CHECK (workspace_generation > 0),
  CONSTRAINT ck_worktree_12 CHECK (CHAR_LENGTH(task_id) > 0 AND task_id !~ '[[:space:]]'),
  CONSTRAINT ck_worktree_13 CHECK (CHAR_LENGTH(holder_run_id) > 0 AND holder_run_id !~ '[[:space:]]'),
  CONSTRAINT ck_worktree_14 CHECK (CHAR_LENGTH(member_id) > 0 AND member_id !~ '[[:space:]]'),
  CONSTRAINT ck_worktree_15 CHECK (path_hash ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_worktree_16 CHECK (active_path_hash ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_worktree_17 CHECK (jsonb_typeof(failure) = 'object' AND failure ? 'schemaVersion' AND failure ? 'data' AND (failure->>'schemaVersion') = '1' AND jsonb_typeof(failure->'data') <> 'null'),
  CONSTRAINT ck_worktree_18 CHECK (version > 0)
);

CREATE UNIQUE INDEX uq_worktree_1 ON sandbox_worktree (operation_id);
CREATE UNIQUE INDEX uq_worktree_2 ON sandbox_worktree (workspace_id, workspace_generation, active_branch);
CREATE UNIQUE INDEX uq_worktree_3 ON sandbox_worktree (workspace_id, workspace_generation, active_path_hash);
CREATE INDEX ix_worktree_1 ON sandbox_worktree (tenant_id, workspace_id, workspace_generation, status, id);
CREATE INDEX ix_worktree_2 ON sandbox_worktree (lease_id, member_id, status);

COMMENT ON COLUMN sandbox_worktree.failure IS 'Envelope data: sandboxv1.Error';

-- Data-only manifest in existing storage service. ApplicationConsistent requires workspace write barrier and actual writer quiescence, including shell processes. InPlace restore advances workspace_generation after fencing; IndependentCopy gets a new workspace ID and independent storage. Both use RestoreCheckpoint action; neither replays commands or restores processes.
CREATE TABLE sandbox_checkpoint (
  id VARCHAR(64) COLLATE "C" NOT NULL,
  tenant_id VARCHAR(64) COLLATE "C" NOT NULL,
  project_id VARCHAR(64) COLLATE "C" NOT NULL,
  operation_id VARCHAR(64) COLLATE "C" NOT NULL,
  lease_id VARCHAR(64) COLLATE "C" NOT NULL,
  request_key VARCHAR(128) COLLATE "C" NOT NULL,
  spec_hash CHAR(64) COLLATE "C" NOT NULL,
  workspace_id VARCHAR(64) COLLATE "C" NOT NULL,
  workspace_generation BIGINT NOT NULL,
  instance_id VARCHAR(64) COLLATE "C" NOT NULL,
  instance_generation BIGINT NOT NULL,
  base_commit VARCHAR(255) COLLATE "C" NOT NULL,
  data_version VARCHAR(255) COLLATE "C" NOT NULL,
  status VARCHAR(32) COLLATE "C" NOT NULL,
  consistency VARCHAR(32) COLLATE "C" NOT NULL,
  manifest_ref VARCHAR(1024) COLLATE "C" NULL,
  manifest_sha256 CHAR(64) COLLATE "C" NULL,
  storage_ref VARCHAR(1024) COLLATE "C" NULL,
  size_bytes BIGINT NULL,
  published_at TIMESTAMPTZ NULL,
  coverage JSONB NOT NULL,
  manifest JSONB NULL,
  quiescence_evidence JSONB NULL,
  failure JSONB NULL,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT ck_checkpoint_1 CHECK (status IN ('Creating', 'Ready', 'Failed')),
  CONSTRAINT ck_checkpoint_2 CHECK (consistency IN ('ApplicationConsistent', 'CrashConsistent')),
  CONSTRAINT ck_checkpoint_3 CHECK (status <> 'Ready' OR (manifest_ref IS NOT NULL AND manifest_sha256 IS NOT NULL AND storage_ref IS NOT NULL AND size_bytes IS NOT NULL AND published_at IS NOT NULL AND manifest IS NOT NULL)),
  CONSTRAINT ck_checkpoint_4 CHECK (status <> 'Ready' OR consistency <> 'ApplicationConsistent' OR quiescence_evidence IS NOT NULL),
  CONSTRAINT ck_checkpoint_5 CHECK (CHAR_LENGTH(id) > 0 AND id !~ '[[:space:]]'),
  CONSTRAINT ck_checkpoint_6 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id !~ '[[:space:]]'),
  CONSTRAINT ck_checkpoint_7 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id !~ '[[:space:]]'),
  CONSTRAINT ck_checkpoint_8 CHECK (CHAR_LENGTH(operation_id) > 0 AND operation_id !~ '[[:space:]]'),
  CONSTRAINT ck_checkpoint_9 CHECK (CHAR_LENGTH(lease_id) > 0 AND lease_id !~ '[[:space:]]'),
  CONSTRAINT ck_checkpoint_10 CHECK (CHAR_LENGTH(request_key) > 0 AND request_key !~ '[[:space:]]'),
  CONSTRAINT ck_checkpoint_11 CHECK (spec_hash ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_checkpoint_12 CHECK (CHAR_LENGTH(workspace_id) > 0 AND workspace_id !~ '[[:space:]]'),
  CONSTRAINT ck_checkpoint_13 CHECK (workspace_generation > 0),
  CONSTRAINT ck_checkpoint_14 CHECK (CHAR_LENGTH(instance_id) > 0 AND instance_id !~ '[[:space:]]'),
  CONSTRAINT ck_checkpoint_15 CHECK (instance_generation > 0),
  CONSTRAINT ck_checkpoint_16 CHECK (manifest_sha256 ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_checkpoint_17 CHECK (size_bytes >= 0),
  CONSTRAINT ck_checkpoint_18 CHECK (jsonb_typeof(coverage) = 'object' AND coverage ? 'schemaVersion' AND coverage ? 'data' AND (coverage->>'schemaVersion') = '1' AND jsonb_typeof(coverage->'data') <> 'null'),
  CONSTRAINT ck_checkpoint_19 CHECK (jsonb_typeof(manifest) = 'object' AND manifest ? 'schemaVersion' AND manifest ? 'data' AND (manifest->>'schemaVersion') = '1' AND jsonb_typeof(manifest->'data') <> 'null'),
  CONSTRAINT ck_checkpoint_20 CHECK (jsonb_typeof(quiescence_evidence) = 'object' AND quiescence_evidence ? 'schemaVersion' AND quiescence_evidence ? 'data' AND (quiescence_evidence->>'schemaVersion') = '1' AND jsonb_typeof(quiescence_evidence->'data') <> 'null'),
  CONSTRAINT ck_checkpoint_21 CHECK (jsonb_typeof(failure) = 'object' AND failure ? 'schemaVersion' AND failure ? 'data' AND (failure->>'schemaVersion') = '1' AND jsonb_typeof(failure->'data') <> 'null'),
  CONSTRAINT ck_checkpoint_22 CHECK (version > 0)
);

CREATE UNIQUE INDEX uq_checkpoint_1 ON sandbox_checkpoint (operation_id);
CREATE INDEX ix_checkpoint_1 ON sandbox_checkpoint (tenant_id, workspace_id, workspace_generation, created_at, id);
CREATE INDEX ix_checkpoint_2 ON sandbox_checkpoint (status, updated_at);

COMMENT ON COLUMN sandbox_checkpoint.coverage IS 'Envelope data: sandboxv1.CheckpointCoverage';
COMMENT ON COLUMN sandbox_checkpoint.manifest IS 'Envelope data: contract.CheckpointManifest';
COMMENT ON COLUMN sandbox_checkpoint.quiescence_evidence IS 'Envelope data: contract.QuiescenceEvidence';
COMMENT ON COLUMN sandbox_checkpoint.failure IS 'Envelope data: sandboxv1.Error';

-- Terminal/browser/preview share one lifecycle. Pending/Unknown are INTERNAL creation facts; creation returns only SDK Open sessions after confirmation, or a structured OutcomeUnknown/lookup result, never a fabricated Open. Terminal writer transfers under lease+session locks, advances writer_epoch and revokes the old connection before accepting input. Input sequences are connection-local: reset only with a new writer connection ID, never replay ambiguous bytes.
CREATE TABLE sandbox_access_session (
  id VARCHAR(64) COLLATE "C" NOT NULL,
  tenant_id VARCHAR(64) COLLATE "C" NOT NULL,
  project_id VARCHAR(64) COLLATE "C" NOT NULL,
  operation_id VARCHAR(64) COLLATE "C" NOT NULL,
  lease_id VARCHAR(64) COLLATE "C" NOT NULL,
  request_key VARCHAR(128) COLLATE "C" NOT NULL,
  spec_hash CHAR(64) COLLATE "C" NOT NULL,
  kind VARCHAR(32) COLLATE "C" NOT NULL,
  status VARCHAR(32) COLLATE "C" NOT NULL,
  instance_id VARCHAR(64) COLLATE "C" NOT NULL,
  instance_generation BIGINT NOT NULL,
  pod_uid VARCHAR(64) COLLATE "C" NOT NULL,
  runtime_boot_id VARCHAR(64) COLLATE "C" NOT NULL,
  fencing_token BIGINT NOT NULL,
  admission_epoch BIGINT NOT NULL,
  protocol VARCHAR(32) COLLATE "C" NOT NULL,
  gateway_route_ref VARCHAR(1024) COLLATE "C" NULL,
  backend_ref VARCHAR(255) COLLATE "C" NULL,
  preview_port BIGINT NULL,
  writer_connection_id VARCHAR(64) COLLATE "C" NULL,
  writer_subject_id VARCHAR(64) COLLATE "C" NULL,
  writer_epoch BIGINT NOT NULL,
  writer_expires_at TIMESTAMPTZ NULL,
  next_input_sequence BIGINT NOT NULL,
  terminal_columns BIGINT NULL,
  terminal_rows BIGINT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  closed_at TIMESTAMPTZ NULL,
  disconnected_at TIMESTAMPTZ NULL,
  request_spec JSONB NOT NULL,
  endpoint JSONB NULL,
  failure JSONB NULL,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT ck_access_session_1 CHECK (kind IN ('Terminal', 'Browser', 'Preview')),
  CONSTRAINT ck_access_session_2 CHECK (status IN ('Pending', 'Open', 'Closing', 'Closed', 'Expired', 'Failed', 'Unknown')),
  CONSTRAINT ck_access_session_3 CHECK (protocol IN ('WebSocket', 'HTTPS')),
  CONSTRAINT ck_access_session_4 CHECK ((writer_connection_id IS NULL) = (writer_subject_id IS NULL)),
  CONSTRAINT ck_access_session_5 CHECK ((writer_connection_id IS NULL) = (writer_expires_at IS NULL)),
  CONSTRAINT ck_access_session_6 CHECK (kind <> 'Preview' OR (preview_port IS NOT NULL AND preview_port BETWEEN 1 AND 65535)),
  CONSTRAINT ck_access_session_7 CHECK (preview_port IS NULL OR preview_port BETWEEN 1 AND 65535),
  CONSTRAINT ck_access_session_8 CHECK (terminal_columns IS NULL OR terminal_columns > 0),
  CONSTRAINT ck_access_session_9 CHECK (terminal_rows IS NULL OR terminal_rows > 0),
  CONSTRAINT ck_access_session_10 CHECK (status <> 'Open' OR backend_ref IS NOT NULL),
  CONSTRAINT ck_access_session_11 CHECK (status <> 'Closed' OR closed_at IS NOT NULL),
  CONSTRAINT ck_access_session_12 CHECK (CHAR_LENGTH(id) > 0 AND id !~ '[[:space:]]'),
  CONSTRAINT ck_access_session_13 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id !~ '[[:space:]]'),
  CONSTRAINT ck_access_session_14 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id !~ '[[:space:]]'),
  CONSTRAINT ck_access_session_15 CHECK (CHAR_LENGTH(operation_id) > 0 AND operation_id !~ '[[:space:]]'),
  CONSTRAINT ck_access_session_16 CHECK (CHAR_LENGTH(lease_id) > 0 AND lease_id !~ '[[:space:]]'),
  CONSTRAINT ck_access_session_17 CHECK (CHAR_LENGTH(request_key) > 0 AND request_key !~ '[[:space:]]'),
  CONSTRAINT ck_access_session_18 CHECK (spec_hash ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_access_session_19 CHECK (CHAR_LENGTH(instance_id) > 0 AND instance_id !~ '[[:space:]]'),
  CONSTRAINT ck_access_session_20 CHECK (instance_generation > 0),
  CONSTRAINT ck_access_session_21 CHECK (CHAR_LENGTH(pod_uid) > 0 AND pod_uid !~ '[[:space:]]'),
  CONSTRAINT ck_access_session_22 CHECK (CHAR_LENGTH(runtime_boot_id) > 0 AND runtime_boot_id !~ '[[:space:]]'),
  CONSTRAINT ck_access_session_23 CHECK (fencing_token > 0),
  CONSTRAINT ck_access_session_24 CHECK (admission_epoch > 0),
  CONSTRAINT ck_access_session_25 CHECK (CHAR_LENGTH(writer_connection_id) > 0 AND writer_connection_id !~ '[[:space:]]'),
  CONSTRAINT ck_access_session_26 CHECK (CHAR_LENGTH(writer_subject_id) > 0 AND writer_subject_id !~ '[[:space:]]'),
  CONSTRAINT ck_access_session_27 CHECK (writer_epoch > 0),
  CONSTRAINT ck_access_session_28 CHECK (next_input_sequence > 0),
  CONSTRAINT ck_access_session_29 CHECK (jsonb_typeof(request_spec) = 'object' AND request_spec ? 'schemaVersion' AND request_spec ? 'data' AND (request_spec->>'schemaVersion') = '1' AND jsonb_typeof(request_spec->'data') <> 'null'),
  CONSTRAINT ck_access_session_30 CHECK (jsonb_typeof(endpoint) = 'object' AND endpoint ? 'schemaVersion' AND endpoint ? 'data' AND (endpoint->>'schemaVersion') = '1' AND jsonb_typeof(endpoint->'data') <> 'null'),
  CONSTRAINT ck_access_session_31 CHECK (jsonb_typeof(failure) = 'object' AND failure ? 'schemaVersion' AND failure ? 'data' AND (failure->>'schemaVersion') = '1' AND jsonb_typeof(failure->'data') <> 'null'),
  CONSTRAINT ck_access_session_32 CHECK (version > 0)
);

CREATE UNIQUE INDEX uq_access_session_1 ON sandbox_access_session (operation_id);
CREATE UNIQUE INDEX uq_access_session_3 ON sandbox_access_session (writer_connection_id);
CREATE INDEX ix_access_session_1 ON sandbox_access_session (lease_id, status, id);
CREATE INDEX ix_access_session_2 ON sandbox_access_session (tenant_id, project_id, kind, status);
CREATE INDEX ix_access_session_3 ON sandbox_access_session (status, expires_at);
CREATE INDEX ix_access_session_4 ON sandbox_access_session (instance_id, instance_generation, status);

COMMENT ON COLUMN sandbox_access_session.request_spec IS 'Envelope data: sandboxv1.CreateTerminalRequest | CreateBrowserRequest | CreatePreviewRequest';
COMMENT ON COLUMN sandbox_access_session.endpoint IS 'Envelope data: contract.Endpoint (private reference DTO)';
COMMENT ON COLUMN sandbox_access_session.failure IS 'Envelope data: sandboxv1.Error';

-- Optional short-lived one-use gateway tickets. Store SHA256 of a high-entropy random token, never the bearer value. Consume uses SELECT FOR UPDATE after lease/session admission checks, requiring consumed_at/revoked_at NULL and expires_at > database UTC now; bind returned connection atomically. Token claims alone do not bypass current authorization.
CREATE TABLE sandbox_access_ticket (
  id VARCHAR(64) COLLATE "C" NOT NULL,
  tenant_id VARCHAR(64) COLLATE "C" NOT NULL,
  project_id VARCHAR(64) COLLATE "C" NOT NULL,
  session_id VARCHAR(64) COLLATE "C" NOT NULL,
  lease_id VARCHAR(64) COLLATE "C" NOT NULL,
  token_hash CHAR(64) COLLATE "C" NOT NULL,
  subject_id VARCHAR(64) COLLATE "C" NOT NULL,
  audience VARCHAR(255) COLLATE "C" NOT NULL,
  role VARCHAR(32) COLLATE "C" NOT NULL,
  writer_epoch BIGINT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  consumed_at TIMESTAMPTZ NULL,
  revoked_at TIMESTAMPTZ NULL,
  connection_id VARCHAR(64) COLLATE "C" NULL,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT ck_access_ticket_1 CHECK (role IN ('Writer', 'Observer')),
  CONSTRAINT ck_access_ticket_2 CHECK ((consumed_at IS NULL) = (connection_id IS NULL)),
  CONSTRAINT ck_access_ticket_3 CHECK (CHAR_LENGTH(id) > 0 AND id !~ '[[:space:]]'),
  CONSTRAINT ck_access_ticket_4 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id !~ '[[:space:]]'),
  CONSTRAINT ck_access_ticket_5 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id !~ '[[:space:]]'),
  CONSTRAINT ck_access_ticket_6 CHECK (CHAR_LENGTH(session_id) > 0 AND session_id !~ '[[:space:]]'),
  CONSTRAINT ck_access_ticket_7 CHECK (CHAR_LENGTH(lease_id) > 0 AND lease_id !~ '[[:space:]]'),
  CONSTRAINT ck_access_ticket_8 CHECK (token_hash ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_access_ticket_9 CHECK (CHAR_LENGTH(subject_id) > 0 AND subject_id !~ '[[:space:]]'),
  CONSTRAINT ck_access_ticket_10 CHECK (writer_epoch > 0),
  CONSTRAINT ck_access_ticket_11 CHECK (CHAR_LENGTH(connection_id) > 0 AND connection_id !~ '[[:space:]]'),
  CONSTRAINT ck_access_ticket_12 CHECK (version > 0)
);

CREATE UNIQUE INDEX uq_access_ticket_1 ON sandbox_access_ticket (token_hash);
CREATE UNIQUE INDEX uq_access_ticket_2 ON sandbox_access_ticket (connection_id);
CREATE INDEX ix_access_ticket_1 ON sandbox_access_ticket (session_id, expires_at);
CREATE INDEX ix_access_ticket_2 ON sandbox_access_ticket (lease_id, revoked_at);
CREATE INDEX ix_access_ticket_3 ON sandbox_access_ticket (expires_at);

-- Sequence allocation row. Lock after entity rows; reserve [next_sequence,next_sequence+n) then append events and advance atomically. Sequences start at 1 and never reset after GC. first_available_sequence == next_sequence means no buffered events. Stream tombstone remains after payload retention expires.
CREATE TABLE sandbox_event_stream (
  id VARCHAR(64) COLLATE "C" NOT NULL,
  tenant_id VARCHAR(64) COLLATE "C" NOT NULL,
  project_id VARCHAR(64) COLLATE "C" NOT NULL,
  entity_kind VARCHAR(32) COLLATE "C" NOT NULL,
  entity_id VARCHAR(64) COLLATE "C" NOT NULL,
  next_sequence BIGINT NOT NULL,
  first_available_sequence BIGINT NOT NULL,
  closed_at TIMESTAMPTZ NULL,
  archive_ref VARCHAR(1024) COLLATE "C" NULL,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT ck_event_stream_1 CHECK (first_available_sequence <= next_sequence),
  CONSTRAINT ck_event_stream_2 CHECK (CHAR_LENGTH(id) > 0 AND id !~ '[[:space:]]'),
  CONSTRAINT ck_event_stream_3 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id !~ '[[:space:]]'),
  CONSTRAINT ck_event_stream_4 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id !~ '[[:space:]]'),
  CONSTRAINT ck_event_stream_5 CHECK (CHAR_LENGTH(entity_id) > 0 AND entity_id !~ '[[:space:]]'),
  CONSTRAINT ck_event_stream_6 CHECK (next_sequence > 0),
  CONSTRAINT ck_event_stream_7 CHECK (first_available_sequence > 0),
  CONSTRAINT ck_event_stream_8 CHECK (version > 0)
);

CREATE UNIQUE INDEX uq_event_stream_1 ON sandbox_event_stream (tenant_id, entity_kind, entity_id);
CREATE INDEX ix_event_stream_1 ON sandbox_event_stream (tenant_id, project_id, entity_kind, updated_at);
CREATE INDEX ix_event_stream_2 ON sandbox_event_stream (closed_at);

-- Lifecycle/output state and events commit together. Public type is SandboxChanged/ExecutionChanged/Output/OutputGap as applicable, payload includes the matching typed SDK Event variant. Internal streams may carry allocation, lease, operation and audit events; never silently expose unknown types through the SDK. source_key deduplicates ingestion; internal events include reason and old/new row versions.
CREATE TABLE sandbox_event (
  id VARCHAR(64) COLLATE "C" NOT NULL,
  tenant_id VARCHAR(64) COLLATE "C" NOT NULL,
  project_id VARCHAR(64) COLLATE "C" NOT NULL,
  stream_id VARCHAR(64) COLLATE "C" NOT NULL,
  entity_kind VARCHAR(32) COLLATE "C" NOT NULL,
  entity_id VARCHAR(64) COLLATE "C" NOT NULL,
  sequence BIGINT NOT NULL,
  type VARCHAR(255) COLLATE "C" NOT NULL,
  entity_version BIGINT NULL,
  previous_version BIGINT NULL,
  instance_generation BIGINT NULL,
  trace_id VARCHAR(64) COLLATE "C" NULL,
  source_key CHAR(64) COLLATE "C" NULL,
  occurred_at TIMESTAMPTZ NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  payload JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT ck_event_1 CHECK (previous_version IS NULL OR (entity_version IS NOT NULL AND entity_version > previous_version)),
  CONSTRAINT ck_event_2 CHECK (CHAR_LENGTH(id) > 0 AND id !~ '[[:space:]]'),
  CONSTRAINT ck_event_3 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id !~ '[[:space:]]'),
  CONSTRAINT ck_event_4 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id !~ '[[:space:]]'),
  CONSTRAINT ck_event_5 CHECK (CHAR_LENGTH(stream_id) > 0 AND stream_id !~ '[[:space:]]'),
  CONSTRAINT ck_event_6 CHECK (CHAR_LENGTH(entity_id) > 0 AND entity_id !~ '[[:space:]]'),
  CONSTRAINT ck_event_7 CHECK (sequence > 0),
  CONSTRAINT ck_event_8 CHECK (entity_version > 0),
  CONSTRAINT ck_event_9 CHECK (previous_version > 0),
  CONSTRAINT ck_event_10 CHECK (instance_generation > 0),
  CONSTRAINT ck_event_11 CHECK (CHAR_LENGTH(trace_id) > 0 AND trace_id !~ '[[:space:]]'),
  CONSTRAINT ck_event_12 CHECK (source_key ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_event_13 CHECK (jsonb_typeof(payload) = 'object' AND payload ? 'schemaVersion' AND payload ? 'data' AND (payload->>'schemaVersion') = '1' AND jsonb_typeof(payload->'data') <> 'null')
);

CREATE UNIQUE INDEX uq_event_1 ON sandbox_event (stream_id, sequence);
CREATE UNIQUE INDEX uq_event_2 ON sandbox_event (stream_id, source_key);
CREATE INDEX ix_event_1 ON sandbox_event (tenant_id, entity_kind, entity_id, sequence);
CREATE INDEX ix_event_2 ON sandbox_event (expires_at, id);
CREATE INDEX ix_event_3 ON sandbox_event (tenant_id, project_id, created_at, id);

COMMENT ON COLUMN sandbox_event.payload IS 'Envelope data: sandboxv1.Event for public streams; contract.AuditEvent otherwise';

-- Durable archive receipt, not log bytes. Archive writes use deterministic execution/boot/source-cursor identity; verify existing content digest on duplicate. Only after object durability is confirmed, lock execution+event_stream and atomically record segment, output events and source cursor. SDK output/state events share sequence; byte offsets and backend cursors remain separate.
CREATE TABLE sandbox_output_segment (
  id VARCHAR(64) COLLATE "C" NOT NULL,
  tenant_id VARCHAR(64) COLLATE "C" NOT NULL,
  project_id VARCHAR(64) COLLATE "C" NOT NULL,
  execution_id VARCHAR(64) COLLATE "C" NOT NULL,
  instance_generation BIGINT NOT NULL,
  runtime_boot_id VARCHAR(64) COLLATE "C" NOT NULL,
  source_cursor_hash CHAR(64) COLLATE "C" NOT NULL,
  source_cursor VARCHAR(1024) COLLATE "C" NOT NULL,
  next_source_cursor VARCHAR(1024) COLLATE "C" NOT NULL,
  cursor_kind VARCHAR(32) COLLATE "C" NOT NULL,
  first_sequence BIGINT NOT NULL,
  last_sequence BIGINT NOT NULL,
  size_bytes BIGINT NOT NULL,
  sha256 CHAR(64) COLLATE "C" NOT NULL,
  storage_ref VARCHAR(1024) COLLATE "C" NOT NULL,
  completeness VARCHAR(32) COLLATE "C" NOT NULL,
  archived_at TIMESTAMPTZ NOT NULL,
  expires_at TIMESTAMPTZ NULL,
  archive JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT ck_output_segment_1 CHECK (first_sequence <= last_sequence),
  CONSTRAINT ck_output_segment_2 CHECK (cursor_kind IN ('Byte', 'Line', 'Event', 'Opaque')),
  CONSTRAINT ck_output_segment_3 CHECK (completeness IN ('Complete', 'Truncated', 'Incomplete')),
  CONSTRAINT ck_output_segment_4 CHECK (CHAR_LENGTH(id) > 0 AND id !~ '[[:space:]]'),
  CONSTRAINT ck_output_segment_5 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id !~ '[[:space:]]'),
  CONSTRAINT ck_output_segment_6 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id !~ '[[:space:]]'),
  CONSTRAINT ck_output_segment_7 CHECK (CHAR_LENGTH(execution_id) > 0 AND execution_id !~ '[[:space:]]'),
  CONSTRAINT ck_output_segment_8 CHECK (instance_generation > 0),
  CONSTRAINT ck_output_segment_9 CHECK (CHAR_LENGTH(runtime_boot_id) > 0 AND runtime_boot_id !~ '[[:space:]]'),
  CONSTRAINT ck_output_segment_10 CHECK (source_cursor_hash ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_output_segment_11 CHECK (first_sequence > 0),
  CONSTRAINT ck_output_segment_12 CHECK (last_sequence > 0),
  CONSTRAINT ck_output_segment_13 CHECK (size_bytes >= 0),
  CONSTRAINT ck_output_segment_14 CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ck_output_segment_15 CHECK (jsonb_typeof(archive) = 'object' AND archive ? 'schemaVersion' AND archive ? 'data' AND (archive->>'schemaVersion') = '1' AND jsonb_typeof(archive->'data') <> 'null')
);

CREATE UNIQUE INDEX uq_output_segment_1 ON sandbox_output_segment (execution_id, runtime_boot_id, source_cursor_hash);
CREATE UNIQUE INDEX uq_output_segment_2 ON sandbox_output_segment (execution_id, first_sequence);
CREATE INDEX ix_output_segment_1 ON sandbox_output_segment (execution_id, last_sequence);
CREATE INDEX ix_output_segment_2 ON sandbox_output_segment (expires_at);

COMMENT ON COLUMN sandbox_output_segment.archive IS 'Envelope data: sandboxv1.ArchiveRef';
