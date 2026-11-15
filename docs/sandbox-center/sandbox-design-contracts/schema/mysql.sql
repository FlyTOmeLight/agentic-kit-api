-- Sandbox control-plane storage contract. MySQL 8.0.16+, InnoDB, utf8mb4.
-- Design only: this file creates tables, not services, procedures or a migration runner.
-- Execute once in a selected EMPTY database. No IF NOT EXISTS: drift must fail visibly.
-- Official syntax: https://dev.mysql.com/doc/refman/8.0/en/create-table-check-constraints.html
-- Time precision: https://dev.mysql.com/doc/refman/8.0/en/fractional-seconds.html
--
-- STORAGE / APPLICATION BOUNDARY
-- The center owns generic environments, executions, files and terminal access.
-- agentic-kit owns CLI Runner versions/arguments/events, logical CLI sessions,
-- resume decisions and their execution mappings; none are persisted here.
-- Session reuse scope IDs are caller-opaque keys, not managed CLI sessions.
-- Every connection must use UTC, strict SQL mode, and DATETIME parsing in UTC.
-- DATETIME(6) stores UTC by convention, not a zone: normalize/truncate input to
-- microseconds before hashing/writing; expiry comparisons use UTC_TIMESTAMP(6).
-- No automatic timestamp defaults hide transaction time; callers supply every
-- required column. NULL means absent/unknown, never zero/empty/success.
-- IDs, hashes, request keys, enum values and opaque refs use ascii_bin. ASCII
-- collations can pad trailing spaces: reject empty and whitespace-containing
-- IDs/keys at admission (also CHECKed below); do not normalize case. Hashes are
-- 64 lowercase hex SHA256; Git/image/vendor digests are separately typed ASCII.
-- Text/path columns use utf8mb4_bin. Paths are canonicalized and hashed for keys.
-- All integer counters use SIGNED BIGINT / Go int64. Positive counters start at
-- 1; amounts/offsets may be 0. API/JSON encoders and mutation admission enforce
-- sandboxv1.MaxCounter = 9007199254740991, including increments BEFORE commit.
-- SQL deliberately retains full signed int64 range. Exhaustion never wraps.
--
-- JSON CONTRACT
-- Every non-NULL JSON column uses {"schemaVersion":1,"data":<typed value>}.
-- SQL checks the envelope; column comments below name the data DTO. The exact
-- storage DTO catalog is internal/contract/storage_json.go, maintained separately.
-- Public and internal DTO data uses explicit lowerCamel JSON tags including
-- Id/Url. Decode the concrete catalog type; never infer names from Go fields.
-- Required lists inside data use [], not null. SQL NULL maps to nil RawMessage;
-- JSON literal null is not SQL NULL and is not a valid envelope. Rows are pure
-- storage models; decoding, validation and driver scanning adapters live outside.
-- Do not write secrets/tokens; credentials are broker references only.
-- spec_hash hashes canonical effective BUSINESS input and content digests,
-- excluding token, trace, HTTP wait and storage envelope version. It includes
-- tenant/project, target, permissions/policy versions, expected file version,
-- mode, workspace generation and action-specific parameters. Server-generated
-- execution/operation IDs are not client request identity. Hash collisions or
-- conflicting stored identity fail closed; hashes are not authorization.
--
-- IDEMPOTENCY / TOMBSTONES
-- Acquire: (tenant_id, caller_id, request_key). Caller identifies the stable
-- application across worker attempts. holder_run_epoch is semantic hash input.
-- New Run epochs query/cancel/drain the old allocation; they cannot inherit or
-- renew its write lease. Run authorization is revalidated before dispatch,
-- while admission/drain barriers settle effects already in flight.
-- Execution start: (lease_id, request_key), separate from all action namespaces.
-- Actions: (tenant_id, caller_id, action, target_kind, target_id, target_revision, request_key).
-- target_revision is 0 ONLY for unversioned targets; ProfileRevision uses a positive
-- revision and target_id=profile_id. DisableProfile revisions never collide or use
-- concatenated/truncated IDs. Lookup uses the same complete identity tuple.
-- Target is a canonical EXISTING resource/parent, not a newly generated result ID:
-- renew/release -> Lease; cancel -> Acquisition/Execution;
-- create worktree/checkpoint/restore -> Workspace (generation enters spec_hash);
-- remove worktree -> Worktree; create access -> Sandbox; connect/input/close ->
-- AccessSession; file write -> Sandbox (canonical path enters spec_hash);
-- admin -> ProfileRevision/Pool/Tenant.
-- Identical keys across actions, callers or target resources are independent.
-- Same action/target/key with different semantic parameters is conflict. Lease
-- proof remains part of spec_hash and admission checks, not the dedup namespace.
-- Resolve ownership/auth BEFORE returning a cached result. Same key/hash returns
-- original identity/result, not another effect. Unknown/Starting/Running is not
-- a retry permission. Worker claim expiry allows observation, NEVER blind replay.
-- Database-only transactions can be retried by the SAME key after reading commit
-- outcome. External retries require verified deduplication and the SAME external
-- operation ID; otherwise query authoritative evidence or remain Unknown.
-- Results may be compacted only after terminal resource cleanup; preserve key,
-- hash, status, original IDs, completion and tombstoned_at. Replays then return
-- RequestExpired. result_expires_at is a GC candidate, not authorization to delete.
-- Unknown evidence cannot be compacted while unresolved. replay_closed_at records
-- independently verified caller/task lifecycle closure; purge_after is a lower
-- bound after that closure and the maximum retry window. Time alone cannot prove
-- old requests will be rejected. No hard-delete while a live reference needs it.
--
-- REFERENCES AND LOCK ORDER (all cross-row rules are transaction obligations)
-- No foreign keys: current instance/workspace generations advance, while evidence
-- and idempotency rows must retain old identities. All references are validated
-- with tenant/project and expected generation under the locks below. Only the
-- owning store may write rows. CHECKs cannot enforce external evidence or CAS.
-- Use short READ COMMITTED transactions; no network I/O while holding row locks.
-- Pre-read the idempotency key for a fast replay, then lock involved rows in order:
--   1 quota_account sorted by id; quota_reservation sorted by id;
--   2 workspace_binding sorted by id;
--   3 reuse_slot sorted by (tenant_id,reuse_key);
--   4 warm_pool sorted by id;
--   5 instance sorted by id; acquire_request sorted by id;
--   6 lease sorted by id;
--   7 execution, action, worktree, checkpoint, access_session, access_ticket
--     (this table order, then primary-key order);
--   8 event_stream sorted by id, then event/output_segment append.
-- Skip absent/unneeded ranks. New rows and unique key inserts occur at their rank;
-- recheck idempotency inside the transaction. A duplicate returns existing state
-- after hash verification; never overwrite input using an upsert. Resolve candidate
-- IDs without locks, then revalidate after ordered locks; restart if owners changed.
-- SKIP LOCKED is for pool/work queues only, never workspace/reuse/lease ownership.
-- A worker claim-only transaction may lock a queue row and commit immediately;
-- do not carry that lock into an operation needing earlier ranks. worker_id,
-- claim_token, claim_until identify processing rights, not resource preparation.
-- All writebacks CAS version+claim_token (+generation/UID/boot where relevant),
-- increment version and append event in the same transaction. Require affected=1.
-- Deadlock/commit ambiguity never authorizes repeating external side effects.
--
-- ALLOCATION AND SINGLE WRITER
-- Reserve commits acquire intent, logical quota reservations, workspace preparation
-- claim, reuse slot, and a Claimed warm instance or cold Provisioning intent. Even
-- Queued holds its logical quota and workspace claim; its deadline is finite.
-- Claimed means acquisition-owned preparation, NOT a user lease. claim_expires_at
-- and preparation_expires_at survive worker turnover. Timeout first cancels/drains;
-- it cannot clear a possibly active preparer/physical writer merely by clock expiry.
-- Activate locks the same set, verifies Pod UID/boot/spec/readiness/credential and
-- workspace evidence, consumes workspace.next_fencing_token, creates Active lease,
-- transfers BOTH workspace and instance acquisition claims to lease, updates reuse
-- slot, freezes AcquisitionResult and commits events. No valid lease before Ready.
-- Worktree rows share this root lease, task and trust domain, not another workspace.
-- InPlace restore takes the workspace write barrier, fences prior writers, advances
-- workspace generation, updates current bound instance/lease coherently, and records
-- old generation in action/checkpoint/events. Already admitted execution snapshots
-- never change. A quarantined storage identity is not assigned to a new workspace.
--
-- RELEASE / DISPATCH BARRIER
-- Admit and BeginDispatch lock instance/workspace as needed and ALWAYS lease before
-- execution; validate Active, expires_at > UTC_TIMESTAMP(6), generation, UID, boot,
-- fence, admission_epoch and workspace write barrier. Persist Starting ticket first.
-- BeginRelease uses the SAME lease row, changes Active -> Releasing, closes admission,
-- increments epoch and records ReleaseLease action atomically. No later dispatch or
-- input is admitted. Enumerate Pending/Starting/Running/Unknown after the barrier;
-- Pending can be cancelled without dispatch. Terminal/browser/preview and file mutation
-- dispatch obey the same barrier; check outstanding action rows as well as execution.
-- Retain requires all in-flight dispatches resolved and all old processes/sessions/
-- credentials stopped/revoked. Runtime epoch revocation must reject delayed starts;
-- without that guarantee, wait for every dispatch to finish. Unknown/possible delayed
-- starts block reuse; isolate/stop writers and retain workspace ownership until fenced.
-- Do not delete the sole protected copy merely to stop an unknown user process.
-- API Pod disappearance alone is not process/storage fencing. FinishRelease requires
-- DrainEvidence; failure stays Releasing/Quarantined and retains ownership. Destroy
-- never releases physical capacity until ConfirmDeleted proves gone AND writes stopped.
-- Required save operation IDs are frozen server-side with BeginRelease and stored in
-- lease.drain_evidence (contract.DrainEvidence), including partial drain evidence.
-- SaveProtection freezes ALL requirements and original inputs, including unadmitted
-- gaps; the original operation-ID list is history, never an empty-list bypass.
-- Resolve each requirement to its latest admitted attempt: a successful replacement
-- satisfies it while the original Failed action remains unchanged.
-- Missing/failed/pending/unknown required saves block normal deletion. Instance remains
-- Draining with phase SaveBlocked; quota and workspace ownership remain held. Expiry
-- stops user execution but is not data-discard consent. Check action/checkpoint content
-- versions and coverage; a prior successful checkpoint does not discharge a later save.
-- Recoverable I/O errors remain retryable work on an admitted operation. Terminal Failed
-- actions are not resurrected. RetryRequiredSave admits a new authorized save-only
-- attempt linked to the frozen requirement and prior failure; Unknown reconciles first.
-- DiscardBlockedSave requires trusted owner-confirmation/delegation evidence; a scoped
-- cross-owner admin uses the same target checks. Body identity/booleans or worker tokens
-- are not authority. SaveRecoveryAuthorization and exact target are stored in existing
-- action.input; evidence_ref points to the verified decision and result holds a receipt.
-- Retry, enrolled-save completion and discard share protection revision CAS/locks.
-- Pre-release saves use ordinary Complete; Complete and BeginRelease check enrollment
-- under the same lock order. Enrollment-first routes completion to CompleteRequiredSave.
-- Discard closes publication admission/commit, but never means save/deletion success.
-- Before Provider.Delete, verify process isolation AND external in-flight publication
-- fencing/reconciliation for originals and retries. A DB epoch alone is insufficient.
-- Keep published checkpoints/unrelated artifacts, ownership and charges until cleanup.
-- Retain frozen inputs/authorization/fence evidence while protection or cleanup is active.
-- User lease expiry never grants new user writes; save recovery does not reopen execution.
--
-- QUOTA ACCOUNTING
-- For each resource, reserved_amount=sum Reserved and charged_amount=sum Charged;
-- check/update accounts and reservation rows in the SAME transaction. New reservations
-- require charged_amount <= limit_amount, reserved_amount <= limit_amount -
-- charged_amount, and delta <= limit_amount - charged_amount - reserved_amount.
-- Check integer overflow before addition; reduced limits never discard existing usage. Admission quota lives from Queued through release;
-- pending-acquisition and active-lease meters transition atomically on activation.
-- ActiveLeases capacity is RESERVED during queuing, CHARGED on activation and freed
-- only after confirmed release. PendingAcquisitions is a separate queue meter.
-- Warm creation reserves/charges PlatformInventory plus ClusterCapacity. Claim moves
-- inventory physical reservation to tenant physical account (same row+ledger_key).
-- charge_class changes InventoryPhysical -> TenantPhysical; both map to the same
-- PhysicalOwner ledger namespace, so a second owner charge cannot be inserted.
-- Debit source and credit destination atomically; cluster charge does not move.
-- Cold creation charges tenant physical plus cluster; its logical reservation is
-- not another CPU/memory charge. IdleBound stays tenant-charged; Lost/Deleting also
-- stay charged. Separate logical/physical/cluster meters are not additive billing.
-- Transfers preserve transferred_from_account_id and an audit event; release each
-- reservation only once. Freed admission quota does not free a still-live Pod.
-- Execution/access/storage/archive budgets use the same per-resource ledger; Unknown
-- executions retain concurrency reservations until stop/fence evidence is confirmed.
--
-- STATES
-- Public status spellings match sandboxv1, including Cancelled, Cancelling,
-- TimedOut and Lease.Quarantined. Internal extensions are called out per table.
-- Runtime's Canceled/Lost spelling is normalized to SDK Cancelled/Unknown as justified
-- by evidence; a lost process without terminal evidence does not become Failed.

SET NAMES utf8mb4 COLLATE utf8mb4_bin;
SET SESSION time_zone = '+00:00';
SET SESSION sql_mode = 'STRICT_ALL_TABLES,NO_ZERO_DATE,NO_ZERO_IN_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION';

-- Published template, digest and capabilities are immutable. Only administrative availability, conditions and version change. Platform profiles use a reserved platform tenant/project, never NULL.
-- resolved_template ProfileSpec fixes environment/data/execution policies and
-- timeout bounds. Validate cross-policy constraints before publishing; JSON
-- envelope CHECKs do not validate those business rules. Requests cannot override
-- the bound profile revision's scope, workspace mode, save policy or release mode.
CREATE TABLE sandbox_profile_revision (
  tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  profile_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  revision BIGINT NOT NULL,
  spec_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  image_digest VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  enabled TINYINT NOT NULL,
  disabled_at DATETIME(6) NULL,
  disable_reason TEXT NULL,
  resolved_template JSON NOT NULL COMMENT 'Envelope data: sandboxv1.ProfileSpec',
  capabilities JSON NOT NULL COMMENT 'Envelope data: []sandboxv1.Capability',
  conditions JSON NOT NULL COMMENT 'Envelope data: []sandboxv1.Condition',
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (tenant_id, profile_id, revision),
  KEY ix_profile_revision_1 (tenant_id, project_id, enabled, profile_id, revision),
  CONSTRAINT ck_profile_revision_1 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_profile_revision_2 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_profile_revision_3 CHECK (CHAR_LENGTH(profile_id) > 0 AND profile_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_profile_revision_4 CHECK (revision > 0),
  CONSTRAINT ck_profile_revision_5 CHECK (spec_hash REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_profile_revision_6 CHECK (enabled IN (0, 1)),
  CONSTRAINT ck_profile_revision_7 CHECK (JSON_TYPE(resolved_template) = 'OBJECT' AND JSON_CONTAINS_PATH(resolved_template, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(resolved_template, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(resolved_template, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(resolved_template, '$.data')) <> 'NULL'),
  CONSTRAINT ck_profile_revision_8 CHECK (JSON_TYPE(capabilities) = 'OBJECT' AND JSON_CONTAINS_PATH(capabilities, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(capabilities, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(capabilities, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(capabilities, '$.data')) <> 'NULL'),
  CONSTRAINT ck_profile_revision_9 CHECK (JSON_TYPE(conditions) = 'OBJECT' AND JSON_CONTAINS_PATH(conditions, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(conditions, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(conditions, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(conditions, '$.data')) <> 'NULL'),
  CONSTRAINT ck_profile_revision_10 CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;

-- One row per budget scope and resource unit. Zero limit prohibits use. Limits may be lowered below current usage; retain usage and refuse new positive reservations until capacity is available. Tenant scope_id=tenant_id; platform inventory scope_id=pool_key; physical cluster capacity scope_id=cluster_id.
CREATE TABLE sandbox_quota_account (
  id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  account_kind VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  scope_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  resource VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  limit_amount BIGINT NOT NULL,
  reserved_amount BIGINT NOT NULL,
  charged_amount BIGINT NOT NULL,
  observed_at DATETIME(6) NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_quota_account_1 (account_kind, scope_id, resource),
  KEY ix_quota_account_1 (tenant_id, project_id, account_kind, resource),
  CONSTRAINT ck_quota_account_1 CHECK (account_kind IN ('Tenant', 'PlatformInventory', 'ClusterCapacity')),
  CONSTRAINT ck_quota_account_2 CHECK (resource IN ('ActiveLeases', 'PendingAcquisitions', 'ConcurrentExecutions', 'AccessSessions', 'CPUMillis', 'MemoryBytes', 'EphemeralDiskBytes', 'WorkspaceBytes', 'ArchivedOutputBytes', 'Instances')),
  CONSTRAINT ck_quota_account_3 CHECK (reserved_amount <= 9223372036854775807 - charged_amount),
  CONSTRAINT ck_quota_account_4 CHECK (CHAR_LENGTH(id) > 0 AND id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_quota_account_5 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_quota_account_6 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_quota_account_7 CHECK (CHAR_LENGTH(scope_id) > 0 AND scope_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_quota_account_8 CHECK (limit_amount >= 0),
  CONSTRAINT ck_quota_account_9 CHECK (reserved_amount >= 0),
  CONSTRAINT ck_quota_account_10 CHECK (charged_amount >= 0),
  CONSTRAINT ck_quota_account_11 CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;

-- ledger_key=SHA256(charge_namespace, resource, subject_kind, subject_id, subject_generation); charge_namespace is PhysicalOwner for InventoryPhysical/TenantPhysical, otherwise charge_class. account_id is excluded so inventory ownership transfer updates this SAME row. Resource is read from account_id and may not change on transfer. Historical Released rows remain unique.
CREATE TABLE sandbox_quota_reservation (
  id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  account_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  ledger_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  subject_kind VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  subject_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  subject_generation BIGINT NOT NULL,
  charge_class VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  amount BIGINT NOT NULL,
  status VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  acquisition_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  instance_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  instance_generation BIGINT NULL,
  lease_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  transferred_from_account_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  reserved_at DATETIME(6) NOT NULL,
  charged_at DATETIME(6) NULL,
  released_at DATETIME(6) NULL,
  release_evidence_ref VARCHAR(1024) CHARACTER SET ascii COLLATE ascii_bin NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_quota_reservation_1 (ledger_key),
  KEY ix_quota_reservation_1 (account_id, status, id),
  KEY ix_quota_reservation_2 (subject_kind, subject_id, subject_generation, charge_class),
  KEY ix_quota_reservation_3 (tenant_id, acquisition_id, status),
  KEY ix_quota_reservation_4 (instance_id, instance_generation, status),
  CONSTRAINT ck_quota_reservation_1 CHECK (status IN ('Reserved', 'Charged', 'Released')),
  CONSTRAINT ck_quota_reservation_2 CHECK (charge_class IN ('Admission', 'TenantPhysical', 'InventoryPhysical', 'ClusterPhysical', 'Execution', 'Access', 'Storage', 'Archive')),
  CONSTRAINT ck_quota_reservation_3 CHECK (status <> 'Released' OR (released_at IS NOT NULL AND release_evidence_ref IS NOT NULL)),
  CONSTRAINT ck_quota_reservation_4 CHECK ((instance_id IS NULL) = (instance_generation IS NULL)),
  CONSTRAINT ck_quota_reservation_5 CHECK (CHAR_LENGTH(id) > 0 AND id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_quota_reservation_6 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_quota_reservation_7 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_quota_reservation_8 CHECK (CHAR_LENGTH(account_id) > 0 AND account_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_quota_reservation_9 CHECK (ledger_key REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_quota_reservation_10 CHECK (CHAR_LENGTH(subject_id) > 0 AND subject_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_quota_reservation_11 CHECK (subject_generation > 0),
  CONSTRAINT ck_quota_reservation_12 CHECK (amount > 0),
  CONSTRAINT ck_quota_reservation_13 CHECK (CHAR_LENGTH(acquisition_id) > 0 AND acquisition_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_quota_reservation_14 CHECK (CHAR_LENGTH(instance_id) > 0 AND instance_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_quota_reservation_15 CHECK (instance_generation > 0),
  CONSTRAINT ck_quota_reservation_16 CHECK (CHAR_LENGTH(lease_id) > 0 AND lease_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_quota_reservation_17 CHECK (CHAR_LENGTH(transferred_from_account_id) > 0 AND transferred_from_account_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_quota_reservation_18 CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;

-- Only clean inventory. Counts are observed projections, never the quota authority. Lock pool row and reserve budgets before recording each provisioning intent. Disable does not reclaim claimed instances.
CREATE TABLE sandbox_warm_pool (
  id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  pool_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  profile_tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  profile_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  profile_revision BIGINT NOT NULL,
  cluster_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  namespace VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  spec_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  enabled TINYINT NOT NULL,
  min_ready BIGINT NOT NULL,
  max_ready BIGINT NOT NULL,
  max_provisioning BIGINT NOT NULL,
  max_idle_seconds BIGINT NOT NULL,
  ready_count BIGINT NOT NULL,
  provisioning_count BIGINT NOT NULL,
  deleting_count BIGINT NOT NULL,
  observed_at DATETIME(6) NULL,
  pool_spec JSON NOT NULL COMMENT 'Envelope data: sandboxv1.PoolSpec',
  resolved_spec JSON NOT NULL COMMENT 'Envelope data: contract.InstanceSpec',
  conditions JSON NOT NULL COMMENT 'Envelope data: []sandboxv1.Condition',
  next_reconcile_at DATETIME(6) NULL,
  worker_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  claim_token VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  claim_until DATETIME(6) NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_warm_pool_1 (pool_key),
  KEY ix_warm_pool_1 (enabled, next_reconcile_at, id),
  KEY ix_warm_pool_2 (cluster_id, enabled, profile_id, profile_revision),
  CONSTRAINT ck_warm_pool_1 CHECK (min_ready <= max_ready),
  CONSTRAINT ck_warm_pool_2 CHECK (CHAR_LENGTH(id) > 0 AND id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_warm_pool_3 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_warm_pool_4 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_warm_pool_5 CHECK (pool_key REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_warm_pool_6 CHECK (CHAR_LENGTH(profile_tenant_id) > 0 AND profile_tenant_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_warm_pool_7 CHECK (CHAR_LENGTH(profile_id) > 0 AND profile_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_warm_pool_8 CHECK (profile_revision > 0),
  CONSTRAINT ck_warm_pool_9 CHECK (CHAR_LENGTH(cluster_id) > 0 AND cluster_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_warm_pool_10 CHECK (spec_hash REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_warm_pool_11 CHECK (enabled IN (0, 1)),
  CONSTRAINT ck_warm_pool_12 CHECK (min_ready >= 0),
  CONSTRAINT ck_warm_pool_13 CHECK (max_ready >= 0),
  CONSTRAINT ck_warm_pool_14 CHECK (max_provisioning >= 0),
  CONSTRAINT ck_warm_pool_15 CHECK (max_idle_seconds >= 0),
  CONSTRAINT ck_warm_pool_16 CHECK (ready_count >= 0),
  CONSTRAINT ck_warm_pool_17 CHECK (provisioning_count >= 0),
  CONSTRAINT ck_warm_pool_18 CHECK (deleting_count >= 0),
  CONSTRAINT ck_warm_pool_19 CHECK (JSON_TYPE(pool_spec) = 'OBJECT' AND JSON_CONTAINS_PATH(pool_spec, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(pool_spec, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(pool_spec, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(pool_spec, '$.data')) <> 'NULL'),
  CONSTRAINT ck_warm_pool_20 CHECK (JSON_TYPE(resolved_spec) = 'OBJECT' AND JSON_CONTAINS_PATH(resolved_spec, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(resolved_spec, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(resolved_spec, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(resolved_spec, '$.data')) <> 'NULL'),
  CONSTRAINT ck_warm_pool_21 CHECK (JSON_TYPE(conditions) = 'OBJECT' AND JSON_CONTAINS_PATH(conditions, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(conditions, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(conditions, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(conditions, '$.data')) <> 'NULL'),
  CONSTRAINT ck_warm_pool_22 CHECK (CHAR_LENGTH(worker_id) > 0 AND worker_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_warm_pool_23 CHECK (CHAR_LENGTH(claim_token) > 0 AND claim_token NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_warm_pool_24 CHECK (version > 0),
  CONSTRAINT ck_warm_pool_25 CHECK ((worker_id IS NULL) = (claim_token IS NULL) AND (worker_id IS NULL) = (claim_until IS NULL))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;

-- One durable row per GLOBAL workspace ID, not one independent lock per generation. In-place restore advances generation under this row lock; old generations live in checkpoint/execution/event evidence. next_fencing_token never resets, including generation changes. storage_identity_hash identifies canonical physical writable storage across all tenants; provisioning must also reject overlapping roots, which equality alone cannot detect.
CREATE TABLE sandbox_workspace_binding (
  id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  workspace_generation BIGINT NOT NULL,
  mode VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_ref VARCHAR(1024) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  base_commit VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  data_version VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  storage_ref VARCHAR(1024) CHARACTER SET ascii COLLATE ascii_bin NULL,
  storage_identity_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  recovery_level VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  status VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  preparing_acquisition_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  preparation_expires_at DATETIME(6) NULL,
  writer_lease_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  bound_instance_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  bound_instance_generation BIGINT NULL,
  next_fencing_token BIGINT NOT NULL,
  checkpoint_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  save_policy_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  save_policy_revision BIGINT NOT NULL,
  write_barrier_operation_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  write_barrier_epoch BIGINT NOT NULL,
  physical_fence_verified_at DATETIME(6) NULL,
  physical_fence_evidence_ref VARCHAR(1024) CHARACTER SET ascii COLLATE ascii_bin NULL,
  layout JSON NOT NULL COMMENT 'Envelope data: sandboxv1.WorkspaceLayout',
  failure JSON NULL COMMENT 'Envelope data: sandboxv1.Error',
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_workspace_binding_1 (tenant_id, id, workspace_generation),
  UNIQUE KEY uq_workspace_binding_2 (storage_identity_hash),
  UNIQUE KEY uq_workspace_binding_3 (preparing_acquisition_id),
  UNIQUE KEY uq_workspace_binding_4 (writer_lease_id),
  KEY ix_workspace_binding_1 (tenant_id, project_id, status, updated_at),
  KEY ix_workspace_binding_2 (bound_instance_id, bound_instance_generation),
  KEY ix_workspace_binding_3 (status, preparation_expires_at),
  CONSTRAINT ck_workspace_binding_1 CHECK (mode IN ('EphemeralCopy', 'PersistentVolume')),
  CONSTRAINT ck_workspace_binding_2 CHECK (recovery_level IN ('Ephemeral', 'Checkpointed', 'Persistent')),
  CONSTRAINT ck_workspace_binding_3 CHECK (status IN ('Preparing', 'Ready', 'Restoring', 'Quarantined', 'Failed')),
  CONSTRAINT ck_workspace_binding_4 CHECK ((preparing_acquisition_id IS NULL OR writer_lease_id IS NULL)),
  CONSTRAINT ck_workspace_binding_5 CHECK ((preparing_acquisition_id IS NULL) = (preparation_expires_at IS NULL)),
  CONSTRAINT ck_workspace_binding_6 CHECK ((bound_instance_id IS NULL) = (bound_instance_generation IS NULL)),
  CONSTRAINT ck_workspace_binding_7 CHECK (mode <> 'PersistentVolume' OR (storage_ref IS NOT NULL AND storage_identity_hash IS NOT NULL)),
  CONSTRAINT ck_workspace_binding_8 CHECK ((physical_fence_verified_at IS NULL) = (physical_fence_evidence_ref IS NULL)),
  CONSTRAINT ck_workspace_binding_9 CHECK (CHAR_LENGTH(id) > 0 AND id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_workspace_binding_10 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_workspace_binding_11 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_workspace_binding_12 CHECK (workspace_generation > 0),
  CONSTRAINT ck_workspace_binding_13 CHECK (storage_identity_hash REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_workspace_binding_14 CHECK (CHAR_LENGTH(preparing_acquisition_id) > 0 AND preparing_acquisition_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_workspace_binding_15 CHECK (CHAR_LENGTH(writer_lease_id) > 0 AND writer_lease_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_workspace_binding_16 CHECK (CHAR_LENGTH(bound_instance_id) > 0 AND bound_instance_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_workspace_binding_17 CHECK (bound_instance_generation > 0),
  CONSTRAINT ck_workspace_binding_18 CHECK (next_fencing_token > 0),
  CONSTRAINT ck_workspace_binding_19 CHECK (CHAR_LENGTH(checkpoint_id) > 0 AND checkpoint_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_workspace_binding_20 CHECK (CHAR_LENGTH(save_policy_id) > 0 AND save_policy_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_workspace_binding_21 CHECK (save_policy_revision > 0),
  CONSTRAINT ck_workspace_binding_22 CHECK (CHAR_LENGTH(write_barrier_operation_id) > 0 AND write_barrier_operation_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_workspace_binding_23 CHECK (write_barrier_epoch > 0),
  CONSTRAINT ck_workspace_binding_24 CHECK (JSON_TYPE(layout) = 'OBJECT' AND JSON_CONTAINS_PATH(layout, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(layout, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(layout, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(layout, '$.data')) <> 'NULL'),
  CONSTRAINT ck_workspace_binding_25 CHECK (JSON_TYPE(failure) = 'OBJECT' AND JSON_CONTAINS_PATH(failure, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(failure, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(failure, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(failure, '$.data')) <> 'NULL'),
  CONSTRAINT ck_workspace_binding_26 CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;

-- Unique request identity intentionally excludes project_id: conflicting project/spec under the same tenant/caller/key is IdempotencyConflict. Queued can own a preparation claim without an instance. Ready result is frozen at activation; replay does not renew the lease.
CREATE TABLE sandbox_acquire_request (
  id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  caller_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  request_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  spec_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  mode VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  scope_type VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  scope_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  holder_run_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  holder_run_epoch BIGINT NOT NULL,
  profile_tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  profile_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  profile_revision BIGINT NOT NULL,
  workspace_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  workspace_generation BIGINT NOT NULL,
  reuse_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  pool_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  capacity_policy VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  status VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  phase VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NULL,
  queue_deadline DATETIME(6) NOT NULL,
  provision_deadline DATETIME(6) NOT NULL,
  requested_lease_seconds BIGINT NOT NULL,
  instance_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  instance_generation BIGINT NULL,
  lease_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  allocation_source VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
  attempt BIGINT NOT NULL,
  completed_at DATETIME(6) NULL,
  request_spec JSON NULL COMMENT 'Envelope data: contract.StoredAcquireRequest',
  ready_result JSON NULL COMMENT 'Envelope data: sandboxv1.AcquisitionResult',
  ready_evidence JSON NULL COMMENT 'Envelope data: contract.ReadyEvidence',
  credential_grants JSON NULL COMMENT 'Envelope data: []contract.CredentialGrant (references only)',
  failure JSON NULL COMMENT 'Envelope data: sandboxv1.Error',
  conditions JSON NOT NULL COMMENT 'Envelope data: []sandboxv1.Condition',
  next_reconcile_at DATETIME(6) NULL,
  worker_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  claim_token VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  claim_until DATETIME(6) NULL,
  tombstoned_at DATETIME(6) NULL,
  result_expires_at DATETIME(6) NULL,
  replay_closed_at DATETIME(6) NULL,
  purge_after DATETIME(6) NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_acquire_request_1 (tenant_id, caller_id, request_key),
  UNIQUE KEY uq_acquire_request_2 (lease_id),
  KEY ix_acquire_request_1 (status, next_reconcile_at, id),
  KEY ix_acquire_request_2 (tenant_id, project_id, created_at, id),
  KEY ix_acquire_request_3 (workspace_id, workspace_generation, status),
  KEY ix_acquire_request_4 (status, queue_deadline),
  KEY ix_acquire_request_5 (status, provision_deadline),
  KEY ix_acquire_request_6 (tombstoned_at, purge_after),
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
  CONSTRAINT ck_acquire_request_11 CHECK (CHAR_LENGTH(id) > 0 AND id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_acquire_request_12 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_acquire_request_13 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_acquire_request_14 CHECK (CHAR_LENGTH(caller_id) > 0 AND caller_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_acquire_request_15 CHECK (CHAR_LENGTH(request_key) > 0 AND request_key NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_acquire_request_16 CHECK (spec_hash REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_acquire_request_17 CHECK (CHAR_LENGTH(scope_id) > 0 AND scope_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_acquire_request_18 CHECK (CHAR_LENGTH(holder_run_id) > 0 AND holder_run_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_acquire_request_19 CHECK (CHAR_LENGTH(profile_tenant_id) > 0 AND profile_tenant_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_acquire_request_20 CHECK (CHAR_LENGTH(profile_id) > 0 AND profile_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_acquire_request_21 CHECK (profile_revision > 0),
  CONSTRAINT ck_acquire_request_22 CHECK (CHAR_LENGTH(workspace_id) > 0 AND workspace_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_acquire_request_23 CHECK (workspace_generation > 0),
  CONSTRAINT ck_acquire_request_24 CHECK (reuse_key REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_acquire_request_25 CHECK (pool_key REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_acquire_request_26 CHECK (requested_lease_seconds > 0),
  CONSTRAINT ck_acquire_request_27 CHECK (CHAR_LENGTH(instance_id) > 0 AND instance_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_acquire_request_28 CHECK (instance_generation > 0),
  CONSTRAINT ck_acquire_request_29 CHECK (CHAR_LENGTH(lease_id) > 0 AND lease_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_acquire_request_30 CHECK (attempt > 0),
  CONSTRAINT ck_acquire_request_31 CHECK (JSON_TYPE(request_spec) = 'OBJECT' AND JSON_CONTAINS_PATH(request_spec, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(request_spec, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(request_spec, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(request_spec, '$.data')) <> 'NULL'),
  CONSTRAINT ck_acquire_request_32 CHECK (JSON_TYPE(ready_result) = 'OBJECT' AND JSON_CONTAINS_PATH(ready_result, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(ready_result, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(ready_result, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(ready_result, '$.data')) <> 'NULL'),
  CONSTRAINT ck_acquire_request_33 CHECK (JSON_TYPE(ready_evidence) = 'OBJECT' AND JSON_CONTAINS_PATH(ready_evidence, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(ready_evidence, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(ready_evidence, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(ready_evidence, '$.data')) <> 'NULL'),
  CONSTRAINT ck_acquire_request_34 CHECK (JSON_TYPE(credential_grants) = 'OBJECT' AND JSON_CONTAINS_PATH(credential_grants, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(credential_grants, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(credential_grants, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(credential_grants, '$.data')) <> 'NULL'),
  CONSTRAINT ck_acquire_request_35 CHECK (JSON_TYPE(failure) = 'OBJECT' AND JSON_CONTAINS_PATH(failure, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(failure, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(failure, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(failure, '$.data')) <> 'NULL'),
  CONSTRAINT ck_acquire_request_36 CHECK (JSON_TYPE(conditions) = 'OBJECT' AND JSON_CONTAINS_PATH(conditions, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(conditions, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(conditions, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(conditions, '$.data')) <> 'NULL'),
  CONSTRAINT ck_acquire_request_37 CHECK (CHAR_LENGTH(worker_id) > 0 AND worker_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_acquire_request_38 CHECK (CHAR_LENGTH(claim_token) > 0 AND claim_token NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_acquire_request_39 CHECK (version > 0),
  CONSTRAINT ck_acquire_request_40 CHECK ((worker_id IS NULL) = (claim_token IS NULL) AND (worker_id IS NULL) = (claim_until IS NULL)),
  CONSTRAINT ck_acquire_request_41 CHECK (purge_after IS NULL OR (tombstoned_at IS NOT NULL AND replay_closed_at IS NOT NULL AND purge_after >= replay_closed_at)),
  CONSTRAINT ck_acquire_request_42 CHECK (holder_run_epoch BETWEEN 1 AND 9007199254740991)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;

-- One current row per instance ID. generation is physical incarnation; version is row CAS. UID/boot change marks old executions Unknown and instance Lost. Never overwrite identity as if old processes survived. A generation advance needs prior physical fencing; retain old identity snapshots in leases/executions/actions/events. Deterministic pod_name includes ID and generation. Kubernetes delete uses expected pod_uid.
CREATE TABLE sandbox_instance (
  id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  generation BIGINT NOT NULL,
  status VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  phase VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NULL,
  reason VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NULL,
  profile_tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  profile_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  profile_revision BIGINT NOT NULL,
  pool_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  reuse_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  spec_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  cluster_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  namespace VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  pod_name VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  pod_uid VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  runtime_boot_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  runtime_protocol_version VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NULL,
  physical_phase VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
  ever_claimed TINYINT NOT NULL,
  claimed_acquisition_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  claim_expires_at DATETIME(6) NULL,
  active_lease_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  workspace_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  workspace_generation BIGINT NULL,
  admission_epoch BIGINT NOT NULL,
  provision_deadline DATETIME(6) NOT NULL,
  hard_expires_at DATETIME(6) NOT NULL,
  idle_expires_at DATETIME(6) NULL,
  observed_at DATETIME(6) NULL,
  physical_gone_at DATETIME(6) NULL,
  writes_stopped_at DATETIME(6) NULL,
  deleted_at DATETIME(6) NULL,
  resolved_spec JSON NOT NULL COMMENT 'Envelope data: contract.InstanceSpec',
  physical_ref JSON NULL COMMENT 'Envelope data: contract.PhysicalRef',
  endpoint JSON NULL COMMENT 'Envelope data: contract.Endpoint (private reference DTO)',
  runtime_info JSON NULL COMMENT 'Envelope data: contract.RuntimeInfo',
  effective_capabilities JSON NOT NULL COMMENT 'Envelope data: []sandboxv1.Capability',
  fence_evidence JSON NULL COMMENT 'Envelope data: contract.FenceEvidence',
  failure JSON NULL COMMENT 'Envelope data: sandboxv1.Error',
  conditions JSON NOT NULL COMMENT 'Envelope data: []sandboxv1.Condition',
  next_reconcile_at DATETIME(6) NULL,
  worker_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  claim_token VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  claim_until DATETIME(6) NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_instance_1 (id, generation),
  UNIQUE KEY uq_instance_2 (cluster_id, namespace, pod_name),
  UNIQUE KEY uq_instance_3 (cluster_id, pod_uid),
  UNIQUE KEY uq_instance_4 (claimed_acquisition_id),
  UNIQUE KEY uq_instance_5 (active_lease_id),
  KEY ix_instance_1 (status, next_reconcile_at, id),
  KEY ix_instance_2 (pool_key, status, created_at, id),
  KEY ix_instance_3 (tenant_id, project_id, status, id),
  KEY ix_instance_4 (workspace_id, workspace_generation),
  KEY ix_instance_5 (status, idle_expires_at),
  KEY ix_instance_6 (status, hard_expires_at),
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
  CONSTRAINT ck_instance_13 CHECK (CHAR_LENGTH(id) > 0 AND id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_instance_14 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_instance_15 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_instance_16 CHECK (generation > 0),
  CONSTRAINT ck_instance_17 CHECK (CHAR_LENGTH(profile_tenant_id) > 0 AND profile_tenant_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_instance_18 CHECK (CHAR_LENGTH(profile_id) > 0 AND profile_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_instance_19 CHECK (profile_revision > 0),
  CONSTRAINT ck_instance_20 CHECK (pool_key REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_instance_21 CHECK (reuse_key REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_instance_22 CHECK (spec_hash REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_instance_23 CHECK (CHAR_LENGTH(cluster_id) > 0 AND cluster_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_instance_24 CHECK (CHAR_LENGTH(pod_uid) > 0 AND pod_uid NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_instance_25 CHECK (CHAR_LENGTH(runtime_boot_id) > 0 AND runtime_boot_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_instance_26 CHECK (ever_claimed IN (0, 1)),
  CONSTRAINT ck_instance_27 CHECK (CHAR_LENGTH(claimed_acquisition_id) > 0 AND claimed_acquisition_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_instance_28 CHECK (CHAR_LENGTH(active_lease_id) > 0 AND active_lease_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_instance_29 CHECK (CHAR_LENGTH(workspace_id) > 0 AND workspace_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_instance_30 CHECK (workspace_generation > 0),
  CONSTRAINT ck_instance_31 CHECK (admission_epoch > 0),
  CONSTRAINT ck_instance_32 CHECK (JSON_TYPE(resolved_spec) = 'OBJECT' AND JSON_CONTAINS_PATH(resolved_spec, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(resolved_spec, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(resolved_spec, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(resolved_spec, '$.data')) <> 'NULL'),
  CONSTRAINT ck_instance_33 CHECK (JSON_TYPE(physical_ref) = 'OBJECT' AND JSON_CONTAINS_PATH(physical_ref, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(physical_ref, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(physical_ref, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(physical_ref, '$.data')) <> 'NULL'),
  CONSTRAINT ck_instance_34 CHECK (JSON_TYPE(endpoint) = 'OBJECT' AND JSON_CONTAINS_PATH(endpoint, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(endpoint, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(endpoint, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(endpoint, '$.data')) <> 'NULL'),
  CONSTRAINT ck_instance_35 CHECK (JSON_TYPE(runtime_info) = 'OBJECT' AND JSON_CONTAINS_PATH(runtime_info, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(runtime_info, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(runtime_info, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(runtime_info, '$.data')) <> 'NULL'),
  CONSTRAINT ck_instance_36 CHECK (JSON_TYPE(effective_capabilities) = 'OBJECT' AND JSON_CONTAINS_PATH(effective_capabilities, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(effective_capabilities, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(effective_capabilities, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(effective_capabilities, '$.data')) <> 'NULL'),
  CONSTRAINT ck_instance_37 CHECK (JSON_TYPE(fence_evidence) = 'OBJECT' AND JSON_CONTAINS_PATH(fence_evidence, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(fence_evidence, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(fence_evidence, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(fence_evidence, '$.data')) <> 'NULL'),
  CONSTRAINT ck_instance_38 CHECK (JSON_TYPE(failure) = 'OBJECT' AND JSON_CONTAINS_PATH(failure, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(failure, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(failure, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(failure, '$.data')) <> 'NULL'),
  CONSTRAINT ck_instance_39 CHECK (JSON_TYPE(conditions) = 'OBJECT' AND JSON_CONTAINS_PATH(conditions, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(conditions, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(conditions, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(conditions, '$.data')) <> 'NULL'),
  CONSTRAINT ck_instance_40 CHECK (CHAR_LENGTH(worker_id) > 0 AND worker_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_instance_41 CHECK (CHAR_LENGTH(claim_token) > 0 AND claim_token NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_instance_42 CHECK (version > 0),
  CONSTRAINT ck_instance_43 CHECK ((worker_id IS NULL) = (claim_token IS NULL) AND (worker_id IS NULL) = (claim_until IS NULL))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;

-- Active -> Releasing closes admission and advances admission_epoch in the same transaction as ReleaseLease intent. Expired/Quarantined also close admission but retain writer ownership until proven stopped. Snapshots never follow a new instance generation. ReleaseMode is assigned by policy for expiry as well as explicit release.
CREATE TABLE sandbox_lease (
  id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  acquisition_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  instance_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  instance_generation BIGINT NOT NULL,
  pod_uid VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  runtime_boot_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  workspace_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  workspace_generation BIGINT NOT NULL,
  holder_run_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  holder_run_epoch BIGINT NOT NULL,
  caller_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  scope_type VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  scope_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  fencing_token BIGINT NOT NULL,
  admission_epoch BIGINT NOT NULL,
  status VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  expires_at DATETIME(6) NOT NULL,
  hard_expires_at DATETIME(6) NOT NULL,
  admission_closed_at DATETIME(6) NULL,
  release_mode VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
  release_operation_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  releasing_at DATETIME(6) NULL,
  released_at DATETIME(6) NULL,
  runtime_barrier_confirmed_at DATETIME(6) NULL,
  process_tree_stopped_at DATETIME(6) NULL,
  sessions_closed_at DATETIME(6) NULL,
  credentials_revoked_at DATETIME(6) NULL,
  permissions JSON NOT NULL COMMENT 'Envelope data: []string',
  credential_grants JSON NOT NULL COMMENT 'Envelope data: []contract.CredentialGrant (references only)',
  termination JSON NULL COMMENT 'Envelope data: sandboxv1.TerminationResult',
  drain_evidence JSON NULL COMMENT 'Envelope data: contract.DrainEvidence',
  failure JSON NULL COMMENT 'Envelope data: sandboxv1.Error',
  next_reconcile_at DATETIME(6) NULL,
  worker_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  claim_token VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  claim_until DATETIME(6) NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_lease_1 (acquisition_id),
  UNIQUE KEY uq_lease_2 (workspace_id, fencing_token),
  KEY ix_lease_1 (instance_id, instance_generation, status),
  KEY ix_lease_2 (tenant_id, holder_run_id, status),
  KEY ix_lease_3 (status, expires_at),
  KEY ix_lease_4 (status, hard_expires_at),
  KEY ix_lease_5 (status, next_reconcile_at, id),
  KEY ix_lease_6 (workspace_id, workspace_generation, status),
  CONSTRAINT ck_lease_1 CHECK (scope_type IN ('Task', 'Session', 'Workspace')),
  CONSTRAINT ck_lease_2 CHECK (status IN ('Active', 'Releasing', 'Expired', 'Released', 'Quarantined')),
  CONSTRAINT ck_lease_3 CHECK (release_mode IN ('Retain', 'Destroy')),
  CONSTRAINT ck_lease_4 CHECK (expires_at <= hard_expires_at),
  CONSTRAINT ck_lease_5 CHECK ((status = 'Active' AND admission_closed_at IS NULL) OR (status <> 'Active' AND admission_closed_at IS NOT NULL)),
  CONSTRAINT ck_lease_6 CHECK (status <> 'Released' OR (released_at IS NOT NULL AND process_tree_stopped_at IS NOT NULL AND sessions_closed_at IS NOT NULL AND credentials_revoked_at IS NOT NULL AND drain_evidence IS NOT NULL)),
  CONSTRAINT ck_lease_7 CHECK (CHAR_LENGTH(id) > 0 AND id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_lease_8 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_lease_9 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_lease_10 CHECK (CHAR_LENGTH(acquisition_id) > 0 AND acquisition_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_lease_11 CHECK (CHAR_LENGTH(instance_id) > 0 AND instance_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_lease_12 CHECK (instance_generation > 0),
  CONSTRAINT ck_lease_13 CHECK (CHAR_LENGTH(pod_uid) > 0 AND pod_uid NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_lease_14 CHECK (CHAR_LENGTH(runtime_boot_id) > 0 AND runtime_boot_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_lease_15 CHECK (CHAR_LENGTH(workspace_id) > 0 AND workspace_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_lease_16 CHECK (workspace_generation > 0),
  CONSTRAINT ck_lease_17 CHECK (CHAR_LENGTH(holder_run_id) > 0 AND holder_run_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_lease_18 CHECK (CHAR_LENGTH(caller_id) > 0 AND caller_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_lease_19 CHECK (CHAR_LENGTH(scope_id) > 0 AND scope_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_lease_20 CHECK (fencing_token > 0),
  CONSTRAINT ck_lease_21 CHECK (admission_epoch > 0),
  CONSTRAINT ck_lease_22 CHECK (CHAR_LENGTH(release_operation_id) > 0 AND release_operation_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_lease_23 CHECK (JSON_TYPE(permissions) = 'OBJECT' AND JSON_CONTAINS_PATH(permissions, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(permissions, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(permissions, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(permissions, '$.data')) <> 'NULL'),
  CONSTRAINT ck_lease_24 CHECK (JSON_TYPE(credential_grants) = 'OBJECT' AND JSON_CONTAINS_PATH(credential_grants, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(credential_grants, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(credential_grants, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(credential_grants, '$.data')) <> 'NULL'),
  CONSTRAINT ck_lease_25 CHECK (JSON_TYPE(termination) = 'OBJECT' AND JSON_CONTAINS_PATH(termination, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(termination, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(termination, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(termination, '$.data')) <> 'NULL'),
  CONSTRAINT ck_lease_26 CHECK (JSON_TYPE(drain_evidence) = 'OBJECT' AND JSON_CONTAINS_PATH(drain_evidence, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(drain_evidence, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(drain_evidence, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(drain_evidence, '$.data')) <> 'NULL'),
  CONSTRAINT ck_lease_27 CHECK (JSON_TYPE(failure) = 'OBJECT' AND JSON_CONTAINS_PATH(failure, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(failure, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(failure, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(failure, '$.data')) <> 'NULL'),
  CONSTRAINT ck_lease_28 CHECK (CHAR_LENGTH(worker_id) > 0 AND worker_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_lease_29 CHECK (CHAR_LENGTH(claim_token) > 0 AND claim_token NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_lease_30 CHECK (version > 0),
  CONSTRAINT ck_lease_31 CHECK ((worker_id IS NULL) = (claim_token IS NULL) AND (worker_id IS NULL) = (claim_until IS NULL)),
  CONSTRAINT ck_lease_32 CHECK (holder_run_epoch BETWEEN 1 AND 9007199254740991)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;

-- Hash preimage is design section 6.1, including all policy revisions and asset digest. Upsert the stable empty slot then lock it; never delete/recreate a busy slot or skip its ownership lock. NULL owner means vacant, not an invitation to ignore workspace ownership.
CREATE TABLE sandbox_reuse_slot (
  tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  reuse_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  scope_type VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  scope_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  profile_tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  profile_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  profile_revision BIGINT NOT NULL,
  workspace_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  workspace_generation BIGINT NOT NULL,
  isolation_policy_revision BIGINT NOT NULL,
  access_policy_revision BIGINT NOT NULL,
  readonly_asset_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  preparing_acquisition_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  instance_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  instance_generation BIGINT NULL,
  writer_lease_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (tenant_id, reuse_key),
  UNIQUE KEY uq_reuse_slot_1 (preparing_acquisition_id),
  UNIQUE KEY uq_reuse_slot_2 (writer_lease_id),
  KEY ix_reuse_slot_1 (tenant_id, project_id, scope_type, scope_id),
  KEY ix_reuse_slot_2 (instance_id, instance_generation),
  KEY ix_reuse_slot_3 (workspace_id, workspace_generation),
  CONSTRAINT ck_reuse_slot_1 CHECK (scope_type IN ('Task', 'Session', 'Workspace')),
  CONSTRAINT ck_reuse_slot_2 CHECK (preparing_acquisition_id IS NULL OR writer_lease_id IS NULL),
  CONSTRAINT ck_reuse_slot_3 CHECK ((instance_id IS NULL) = (instance_generation IS NULL)),
  CONSTRAINT ck_reuse_slot_4 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_reuse_slot_5 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_reuse_slot_6 CHECK (reuse_key REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_reuse_slot_7 CHECK (CHAR_LENGTH(scope_id) > 0 AND scope_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_reuse_slot_8 CHECK (CHAR_LENGTH(profile_tenant_id) > 0 AND profile_tenant_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_reuse_slot_9 CHECK (CHAR_LENGTH(profile_id) > 0 AND profile_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_reuse_slot_10 CHECK (profile_revision > 0),
  CONSTRAINT ck_reuse_slot_11 CHECK (CHAR_LENGTH(workspace_id) > 0 AND workspace_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_reuse_slot_12 CHECK (workspace_generation > 0),
  CONSTRAINT ck_reuse_slot_13 CHECK (isolation_policy_revision > 0),
  CONSTRAINT ck_reuse_slot_14 CHECK (access_policy_revision > 0),
  CONSTRAINT ck_reuse_slot_15 CHECK (readonly_asset_digest REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_reuse_slot_16 CHECK (CHAR_LENGTH(preparing_acquisition_id) > 0 AND preparing_acquisition_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_reuse_slot_17 CHECK (CHAR_LENGTH(instance_id) > 0 AND instance_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_reuse_slot_18 CHECK (instance_generation > 0),
  CONSTRAINT ck_reuse_slot_19 CHECK (CHAR_LENGTH(writer_lease_id) > 0 AND writer_lease_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_reuse_slot_20 CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;

-- Unified idempotent mutation ledger, not one table per verb. Actions include RenewLease, ReleaseLease, RetryRequiredSave, DiscardBlockedSave, WriteFile, WriteStdin, Create/RemoveWorktree, Create/RestoreCheckpoint, CancelAcquisition/Execution, CreateTerminal/Browser/Preview, Connect/CloseAccessSession, WriteTerminal/ResizeTerminal/SignalTerminal, Publish/DisableProfile, Set/DisablePool, SetQuota, PublishArtifact, Issue/RevokeCredential. Public asynchronous and synchronous actions use OperationKind; internal actions remain internal. StartExecution uses execution table, not a second action record. Cancelled operations map to Failed with a cancellation reason; SDK OperationStatus has no Cancelled member.
CREATE TABLE sandbox_action (
  id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  caller_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  action VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  request_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  spec_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  status VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  target_kind VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  target_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  target_revision BIGINT NOT NULL DEFAULT 0 COMMENT '0 means unversioned target; positive for ProfileRevision',
  lease_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  acquisition_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  instance_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  instance_generation BIGINT NULL,
  pod_uid VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  runtime_boot_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  fencing_token BIGINT NULL,
  admission_epoch BIGINT NULL,
  workspace_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  workspace_generation BIGINT NULL,
  dispatch_attempt_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  dispatch_started_at DATETIME(6) NULL,
  dispatch_outcome VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
  replay_policy VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  external_ref VARCHAR(1024) CHARACTER SET ascii COLLATE ascii_bin NULL,
  evidence_ref VARCHAR(1024) CHARACTER SET ascii COLLATE ascii_bin NULL,
  completed_at DATETIME(6) NULL,
  input JSON NULL COMMENT 'Envelope data: action-specific normalized request DTO',
  result JSON NULL COMMENT 'Envelope data: action-specific response DTO',
  failure JSON NULL COMMENT 'Envelope data: sandboxv1.Error',
  next_reconcile_at DATETIME(6) NULL,
  worker_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  claim_token VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  claim_until DATETIME(6) NULL,
  tombstoned_at DATETIME(6) NULL,
  result_expires_at DATETIME(6) NULL,
  replay_closed_at DATETIME(6) NULL,
  purge_after DATETIME(6) NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT ck_action_target_revision CHECK ((target_kind = 'ProfileRevision' AND target_revision > 0) OR (target_kind <> 'ProfileRevision' AND target_revision = 0)),
  UNIQUE KEY uq_action_1 (tenant_id, caller_id, action, target_kind, target_id, target_revision, request_key),
  KEY ix_action_1 (lease_id, status, id),
  KEY ix_action_2 (acquisition_id, status, id),
  KEY ix_action_3 (target_kind, target_id, target_revision, created_at),
  KEY ix_action_4 (status, next_reconcile_at, id),
  KEY ix_action_5 (tenant_id, caller_id, created_at, id),
  KEY ix_action_6 (tombstoned_at, purge_after),
  CONSTRAINT ck_action_2 CHECK (status IN ('Pending', 'Running', 'Succeeded', 'Failed', 'Unknown')),
  CONSTRAINT ck_action_3 CHECK (dispatch_outcome IN ('Accepted', 'NotAccepted', 'Unknown')),
  CONSTRAINT ck_action_4 CHECK (replay_policy IN ('DatabaseOnly', 'BackendDeduplicated', 'ReconcileOnly')),
  CONSTRAINT ck_action_5 CHECK ((instance_id IS NULL) = (instance_generation IS NULL)),
  CONSTRAINT ck_action_6 CHECK ((workspace_id IS NULL) = (workspace_generation IS NULL)),
  CONSTRAINT ck_action_10 CHECK (tombstoned_at IS NOT NULL OR input IS NOT NULL),
  CONSTRAINT ck_action_11 CHECK (tombstoned_at IS NOT NULL OR status <> 'Succeeded' OR result IS NOT NULL),
  CONSTRAINT ck_action_12 CHECK (tombstoned_at IS NOT NULL OR status <> 'Failed' OR failure IS NOT NULL),
  CONSTRAINT ck_action_13 CHECK (CHAR_LENGTH(id) > 0 AND id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_action_14 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_action_15 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_action_16 CHECK (CHAR_LENGTH(caller_id) > 0 AND caller_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_action_18 CHECK (CHAR_LENGTH(request_key) > 0 AND request_key NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_action_19 CHECK (spec_hash REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_action_20 CHECK (CHAR_LENGTH(target_id) > 0 AND target_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_action_21 CHECK (CHAR_LENGTH(lease_id) > 0 AND lease_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_action_22 CHECK (CHAR_LENGTH(acquisition_id) > 0 AND acquisition_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_action_23 CHECK (CHAR_LENGTH(instance_id) > 0 AND instance_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_action_24 CHECK (instance_generation > 0),
  CONSTRAINT ck_action_25 CHECK (CHAR_LENGTH(pod_uid) > 0 AND pod_uid NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_action_26 CHECK (CHAR_LENGTH(runtime_boot_id) > 0 AND runtime_boot_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_action_27 CHECK (fencing_token > 0),
  CONSTRAINT ck_action_28 CHECK (admission_epoch > 0),
  CONSTRAINT ck_action_29 CHECK (CHAR_LENGTH(workspace_id) > 0 AND workspace_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_action_30 CHECK (workspace_generation > 0),
  CONSTRAINT ck_action_31 CHECK (CHAR_LENGTH(dispatch_attempt_id) > 0 AND dispatch_attempt_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_action_32 CHECK (JSON_TYPE(input) = 'OBJECT' AND JSON_CONTAINS_PATH(input, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(input, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(input, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(input, '$.data')) <> 'NULL'),
  CONSTRAINT ck_action_33 CHECK (JSON_TYPE(result) = 'OBJECT' AND JSON_CONTAINS_PATH(result, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(result, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(result, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(result, '$.data')) <> 'NULL'),
  CONSTRAINT ck_action_34 CHECK (JSON_TYPE(failure) = 'OBJECT' AND JSON_CONTAINS_PATH(failure, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(failure, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(failure, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(failure, '$.data')) <> 'NULL'),
  CONSTRAINT ck_action_35 CHECK (CHAR_LENGTH(worker_id) > 0 AND worker_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_action_36 CHECK (CHAR_LENGTH(claim_token) > 0 AND claim_token NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_action_37 CHECK (version > 0),
  CONSTRAINT ck_action_38 CHECK ((worker_id IS NULL) = (claim_token IS NULL) AND (worker_id IS NULL) = (claim_until IS NULL)),
  CONSTRAINT ck_action_39 CHECK (purge_after IS NULL OR (tombstoned_at IS NOT NULL AND replay_closed_at IS NOT NULL AND purge_after >= replay_closed_at))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;

-- BeginDispatch locks lease before execution, checks Active+expiry+epoch and persists Starting/attempt before network IO. Claim expiry never resets Starting. backend_ref may be NULL in Unknown. ReconcileOnly permits exactly one network dispatch; BackendDeduplicated retries require verified adapter guarantees and the SAME execution ID. Failed need not have an exit code when authoritative rejection proves no process started.
CREATE TABLE sandbox_execution (
  id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  lease_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  request_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  spec_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  instance_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  instance_generation BIGINT NOT NULL,
  pod_uid VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  runtime_boot_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  workspace_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  workspace_generation BIGINT NOT NULL,
  holder_run_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  member_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  worktree_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  fencing_token BIGINT NOT NULL,
  admission_epoch BIGINT NOT NULL,
  status VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  cwd VARCHAR(1024) NOT NULL,
  command_mode VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  deadline DATETIME(6) NOT NULL,
  max_output_bytes BIGINT NOT NULL,
  stdin_closed TINYINT NOT NULL,
  next_input_sequence BIGINT NOT NULL,
  dispatch_attempt_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  dispatch_epoch BIGINT NULL,
  dispatch_started_at DATETIME(6) NULL,
  dispatch_outcome VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
  replay_policy VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  backend_ref VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NULL,
  started_at DATETIME(6) NULL,
  completed_at DATETIME(6) NULL,
  observed_at DATETIME(6) NULL,
  exit_code BIGINT NULL,
  exit_signal VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NULL,
  output_completeness VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  output_bytes BIGINT NOT NULL,
  source_cursor VARCHAR(1024) CHARACTER SET ascii COLLATE ascii_bin NULL,
  source_cursor_kind VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
  cancel_operation_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  request_spec JSON NULL COMMENT 'Envelope data: contract.StoredExecutionRequest',
  command_spec JSON NULL COMMENT 'Envelope data: contract.CommandSpec (secret references only)',
  termination JSON NULL COMMENT 'Envelope data: sandboxv1.TerminationResult',
  failure JSON NULL COMMENT 'Envelope data: sandboxv1.Error',
  next_reconcile_at DATETIME(6) NULL,
  worker_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  claim_token VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  claim_until DATETIME(6) NULL,
  tombstoned_at DATETIME(6) NULL,
  result_expires_at DATETIME(6) NULL,
  replay_closed_at DATETIME(6) NULL,
  purge_after DATETIME(6) NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_execution_1 (lease_id, request_key),
  UNIQUE KEY uq_execution_2 (instance_id, instance_generation, runtime_boot_id, backend_ref),
  KEY ix_execution_1 (lease_id, status, id),
  KEY ix_execution_2 (status, next_reconcile_at, id),
  KEY ix_execution_3 (instance_id, instance_generation, status),
  KEY ix_execution_4 (workspace_id, workspace_generation, created_at),
  KEY ix_execution_5 (tenant_id, project_id, created_at, id),
  KEY ix_execution_6 (status, deadline),
  KEY ix_execution_7 (tombstoned_at, purge_after),
  CONSTRAINT ck_execution_1 CHECK (status IN ('Pending', 'Starting', 'Running', 'Succeeded', 'Failed', 'TimedOut', 'Cancelled', 'Unknown')),
  CONSTRAINT ck_execution_2 CHECK (command_mode IN ('Argv', 'Shell')),
  CONSTRAINT ck_execution_3 CHECK (dispatch_outcome IN ('Accepted', 'NotAccepted', 'Unknown')),
  CONSTRAINT ck_execution_4 CHECK (replay_policy IN ('BackendDeduplicated', 'ReconcileOnly')),
  CONSTRAINT ck_execution_5 CHECK (output_completeness IN ('Pending', 'Complete', 'Truncated', 'Incomplete')),
  CONSTRAINT ck_execution_6 CHECK (source_cursor_kind IN ('Byte', 'Line', 'Event', 'Opaque')),
  CONSTRAINT ck_execution_8 CHECK (status NOT IN ('Starting', 'Running', 'Unknown') OR (dispatch_attempt_id IS NOT NULL AND dispatch_started_at IS NOT NULL)),
  CONSTRAINT ck_execution_9 CHECK (status <> 'Succeeded' OR (exit_code IS NOT NULL AND exit_code = 0 AND completed_at IS NOT NULL)),
  CONSTRAINT ck_execution_10 CHECK (tombstoned_at IS NOT NULL OR (request_spec IS NOT NULL AND command_spec IS NOT NULL)),
  CONSTRAINT ck_execution_11 CHECK (CHAR_LENGTH(id) > 0 AND id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_execution_12 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_execution_13 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_execution_14 CHECK (CHAR_LENGTH(lease_id) > 0 AND lease_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_execution_15 CHECK (CHAR_LENGTH(request_key) > 0 AND request_key NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_execution_16 CHECK (spec_hash REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_execution_17 CHECK (CHAR_LENGTH(instance_id) > 0 AND instance_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_execution_18 CHECK (instance_generation > 0),
  CONSTRAINT ck_execution_19 CHECK (CHAR_LENGTH(pod_uid) > 0 AND pod_uid NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_execution_20 CHECK (CHAR_LENGTH(runtime_boot_id) > 0 AND runtime_boot_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_execution_21 CHECK (CHAR_LENGTH(workspace_id) > 0 AND workspace_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_execution_22 CHECK (workspace_generation > 0),
  CONSTRAINT ck_execution_23 CHECK (CHAR_LENGTH(holder_run_id) > 0 AND holder_run_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_execution_24 CHECK (CHAR_LENGTH(member_id) > 0 AND member_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_execution_25 CHECK (CHAR_LENGTH(worktree_id) > 0 AND worktree_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_execution_28 CHECK (fencing_token > 0),
  CONSTRAINT ck_execution_29 CHECK (admission_epoch > 0),
  CONSTRAINT ck_execution_30 CHECK (max_output_bytes >= 0),
  CONSTRAINT ck_execution_31 CHECK (stdin_closed IN (0, 1)),
  CONSTRAINT ck_execution_32 CHECK (next_input_sequence > 0),
  CONSTRAINT ck_execution_33 CHECK (CHAR_LENGTH(dispatch_attempt_id) > 0 AND dispatch_attempt_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_execution_34 CHECK (dispatch_epoch > 0),
  CONSTRAINT ck_execution_35 CHECK (output_bytes >= 0),
  CONSTRAINT ck_execution_36 CHECK (CHAR_LENGTH(cancel_operation_id) > 0 AND cancel_operation_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_execution_37 CHECK (JSON_TYPE(request_spec) = 'OBJECT' AND JSON_CONTAINS_PATH(request_spec, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(request_spec, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(request_spec, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(request_spec, '$.data')) <> 'NULL'),
  CONSTRAINT ck_execution_38 CHECK (JSON_TYPE(command_spec) = 'OBJECT' AND JSON_CONTAINS_PATH(command_spec, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(command_spec, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(command_spec, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(command_spec, '$.data')) <> 'NULL'),
  CONSTRAINT ck_execution_39 CHECK (JSON_TYPE(termination) = 'OBJECT' AND JSON_CONTAINS_PATH(termination, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(termination, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(termination, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(termination, '$.data')) <> 'NULL'),
  CONSTRAINT ck_execution_40 CHECK (JSON_TYPE(failure) = 'OBJECT' AND JSON_CONTAINS_PATH(failure, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(failure, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(failure, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(failure, '$.data')) <> 'NULL'),
  CONSTRAINT ck_execution_41 CHECK (CHAR_LENGTH(worker_id) > 0 AND worker_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_execution_42 CHECK (CHAR_LENGTH(claim_token) > 0 AND claim_token NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_execution_43 CHECK (version > 0),
  CONSTRAINT ck_execution_44 CHECK ((worker_id IS NULL) = (claim_token IS NULL) AND (worker_id IS NULL) = (claim_until IS NULL)),
  CONSTRAINT ck_execution_45 CHECK (purge_after IS NULL OR (tombstoned_at IS NOT NULL AND replay_closed_at IS NOT NULL AND purge_after >= replay_closed_at))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;

-- Part of the SAME tenant/project/task/root lease/workspace; never a new cross-tenant workspace or security boundary. Unique active branch/path reservations include Failed until cleanup is confirmed. Removed clears active keys so history does not prevent reuse. Main integration checkout must also have a reserved worktree row. Canonical paths and branch names are validated before hashing.
CREATE TABLE sandbox_worktree (
  id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  operation_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  lease_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  request_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  spec_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  workspace_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  workspace_generation BIGINT NOT NULL,
  task_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  holder_run_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  member_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  base_commit VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  branch VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  path VARCHAR(1024) NOT NULL,
  path_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  active_branch VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NULL,
  active_path_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  head_commit VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NULL,
  status VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  removed_at DATETIME(6) NULL,
  failure JSON NULL COMMENT 'Envelope data: sandboxv1.Error',
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_worktree_1 (operation_id),
  UNIQUE KEY uq_worktree_2 (workspace_id, workspace_generation, active_branch),
  UNIQUE KEY uq_worktree_3 (workspace_id, workspace_generation, active_path_hash),
  KEY ix_worktree_1 (tenant_id, workspace_id, workspace_generation, status, id),
  KEY ix_worktree_2 (lease_id, member_id, status),
  CONSTRAINT ck_worktree_1 CHECK (status IN ('Creating', 'Ready', 'Removing', 'Removed', 'Failed')),
  CONSTRAINT ck_worktree_2 CHECK ((status = 'Removed' AND active_branch IS NULL AND active_path_hash IS NULL AND removed_at IS NOT NULL) OR (status <> 'Removed' AND active_branch IS NOT NULL AND active_branch = branch AND active_path_hash IS NOT NULL AND active_path_hash = path_hash)),
  CONSTRAINT ck_worktree_3 CHECK (CHAR_LENGTH(id) > 0 AND id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_worktree_4 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_worktree_5 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_worktree_6 CHECK (CHAR_LENGTH(operation_id) > 0 AND operation_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_worktree_7 CHECK (CHAR_LENGTH(lease_id) > 0 AND lease_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_worktree_8 CHECK (CHAR_LENGTH(request_key) > 0 AND request_key NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_worktree_9 CHECK (spec_hash REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_worktree_10 CHECK (CHAR_LENGTH(workspace_id) > 0 AND workspace_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_worktree_11 CHECK (workspace_generation > 0),
  CONSTRAINT ck_worktree_12 CHECK (CHAR_LENGTH(task_id) > 0 AND task_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_worktree_13 CHECK (CHAR_LENGTH(holder_run_id) > 0 AND holder_run_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_worktree_14 CHECK (CHAR_LENGTH(member_id) > 0 AND member_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_worktree_15 CHECK (path_hash REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_worktree_16 CHECK (active_path_hash REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_worktree_17 CHECK (JSON_TYPE(failure) = 'OBJECT' AND JSON_CONTAINS_PATH(failure, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(failure, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(failure, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(failure, '$.data')) <> 'NULL'),
  CONSTRAINT ck_worktree_18 CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;

-- Data-only manifest in existing storage service. ApplicationConsistent requires workspace write barrier and actual writer quiescence, including shell processes. InPlace restore advances workspace_generation after fencing; IndependentCopy gets a new workspace ID and independent storage. Both use RestoreCheckpoint action; neither replays commands or restores processes.
CREATE TABLE sandbox_checkpoint (
  id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  operation_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  lease_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  request_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  spec_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  workspace_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  workspace_generation BIGINT NOT NULL,
  instance_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  instance_generation BIGINT NOT NULL,
  base_commit VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  data_version VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  status VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  consistency VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  manifest_ref VARCHAR(1024) CHARACTER SET ascii COLLATE ascii_bin NULL,
  manifest_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  storage_ref VARCHAR(1024) CHARACTER SET ascii COLLATE ascii_bin NULL,
  size_bytes BIGINT NULL,
  published_at DATETIME(6) NULL,
  coverage JSON NOT NULL COMMENT 'Envelope data: sandboxv1.CheckpointCoverage',
  manifest JSON NULL COMMENT 'Envelope data: contract.CheckpointManifest',
  quiescence_evidence JSON NULL COMMENT 'Envelope data: contract.QuiescenceEvidence',
  failure JSON NULL COMMENT 'Envelope data: sandboxv1.Error',
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_checkpoint_1 (operation_id),
  KEY ix_checkpoint_1 (tenant_id, workspace_id, workspace_generation, created_at, id),
  KEY ix_checkpoint_2 (status, updated_at),
  CONSTRAINT ck_checkpoint_1 CHECK (status IN ('Creating', 'Ready', 'Failed')),
  CONSTRAINT ck_checkpoint_2 CHECK (consistency IN ('ApplicationConsistent', 'CrashConsistent')),
  CONSTRAINT ck_checkpoint_3 CHECK (status <> 'Ready' OR (manifest_ref IS NOT NULL AND manifest_sha256 IS NOT NULL AND storage_ref IS NOT NULL AND size_bytes IS NOT NULL AND published_at IS NOT NULL AND manifest IS NOT NULL)),
  CONSTRAINT ck_checkpoint_4 CHECK (status <> 'Ready' OR consistency <> 'ApplicationConsistent' OR quiescence_evidence IS NOT NULL),
  CONSTRAINT ck_checkpoint_5 CHECK (CHAR_LENGTH(id) > 0 AND id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_checkpoint_6 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_checkpoint_7 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_checkpoint_8 CHECK (CHAR_LENGTH(operation_id) > 0 AND operation_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_checkpoint_9 CHECK (CHAR_LENGTH(lease_id) > 0 AND lease_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_checkpoint_10 CHECK (CHAR_LENGTH(request_key) > 0 AND request_key NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_checkpoint_11 CHECK (spec_hash REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_checkpoint_12 CHECK (CHAR_LENGTH(workspace_id) > 0 AND workspace_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_checkpoint_13 CHECK (workspace_generation > 0),
  CONSTRAINT ck_checkpoint_14 CHECK (CHAR_LENGTH(instance_id) > 0 AND instance_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_checkpoint_15 CHECK (instance_generation > 0),
  CONSTRAINT ck_checkpoint_16 CHECK (manifest_sha256 REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_checkpoint_17 CHECK (size_bytes >= 0),
  CONSTRAINT ck_checkpoint_18 CHECK (JSON_TYPE(coverage) = 'OBJECT' AND JSON_CONTAINS_PATH(coverage, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(coverage, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(coverage, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(coverage, '$.data')) <> 'NULL'),
  CONSTRAINT ck_checkpoint_19 CHECK (JSON_TYPE(manifest) = 'OBJECT' AND JSON_CONTAINS_PATH(manifest, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(manifest, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(manifest, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(manifest, '$.data')) <> 'NULL'),
  CONSTRAINT ck_checkpoint_20 CHECK (JSON_TYPE(quiescence_evidence) = 'OBJECT' AND JSON_CONTAINS_PATH(quiescence_evidence, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(quiescence_evidence, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(quiescence_evidence, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(quiescence_evidence, '$.data')) <> 'NULL'),
  CONSTRAINT ck_checkpoint_21 CHECK (JSON_TYPE(failure) = 'OBJECT' AND JSON_CONTAINS_PATH(failure, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(failure, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(failure, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(failure, '$.data')) <> 'NULL'),
  CONSTRAINT ck_checkpoint_22 CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;

-- Terminal/browser/preview share one lifecycle. Pending/Unknown are INTERNAL creation facts; creation returns only SDK Open sessions after confirmation, or a structured OutcomeUnknown/lookup result, never a fabricated Open. Terminal writer transfers under lease+session locks, advances writer_epoch and revokes the old connection before accepting input. Input sequences are connection-local: reset only with a new writer connection ID, never replay ambiguous bytes.
CREATE TABLE sandbox_access_session (
  id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  operation_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  lease_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  request_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  spec_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  kind VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  status VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  instance_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  instance_generation BIGINT NOT NULL,
  pod_uid VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  runtime_boot_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  fencing_token BIGINT NOT NULL,
  admission_epoch BIGINT NOT NULL,
  protocol VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  gateway_route_ref VARCHAR(1024) CHARACTER SET ascii COLLATE ascii_bin NULL,
  backend_ref VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NULL,
  preview_port BIGINT NULL,
  writer_connection_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  writer_subject_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  writer_epoch BIGINT NOT NULL,
  writer_expires_at DATETIME(6) NULL,
  next_input_sequence BIGINT NOT NULL,
  terminal_columns BIGINT NULL,
  terminal_rows BIGINT NULL,
  expires_at DATETIME(6) NOT NULL,
  closed_at DATETIME(6) NULL,
  disconnected_at DATETIME(6) NULL,
  request_spec JSON NOT NULL COMMENT 'Envelope data: sandboxv1.CreateTerminalRequest | CreateBrowserRequest | CreatePreviewRequest',
  endpoint JSON NULL COMMENT 'Envelope data: contract.Endpoint (private reference DTO)',
  failure JSON NULL COMMENT 'Envelope data: sandboxv1.Error',
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_access_session_1 (operation_id),
  UNIQUE KEY uq_access_session_3 (writer_connection_id),
  KEY ix_access_session_1 (lease_id, status, id),
  KEY ix_access_session_2 (tenant_id, project_id, kind, status),
  KEY ix_access_session_3 (status, expires_at),
  KEY ix_access_session_4 (instance_id, instance_generation, status),
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
  CONSTRAINT ck_access_session_12 CHECK (CHAR_LENGTH(id) > 0 AND id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_access_session_13 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_access_session_14 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_access_session_15 CHECK (CHAR_LENGTH(operation_id) > 0 AND operation_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_access_session_16 CHECK (CHAR_LENGTH(lease_id) > 0 AND lease_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_access_session_17 CHECK (CHAR_LENGTH(request_key) > 0 AND request_key NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_access_session_18 CHECK (spec_hash REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_access_session_19 CHECK (CHAR_LENGTH(instance_id) > 0 AND instance_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_access_session_20 CHECK (instance_generation > 0),
  CONSTRAINT ck_access_session_21 CHECK (CHAR_LENGTH(pod_uid) > 0 AND pod_uid NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_access_session_22 CHECK (CHAR_LENGTH(runtime_boot_id) > 0 AND runtime_boot_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_access_session_23 CHECK (fencing_token > 0),
  CONSTRAINT ck_access_session_24 CHECK (admission_epoch > 0),
  CONSTRAINT ck_access_session_25 CHECK (CHAR_LENGTH(writer_connection_id) > 0 AND writer_connection_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_access_session_26 CHECK (CHAR_LENGTH(writer_subject_id) > 0 AND writer_subject_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_access_session_27 CHECK (writer_epoch > 0),
  CONSTRAINT ck_access_session_28 CHECK (next_input_sequence > 0),
  CONSTRAINT ck_access_session_29 CHECK (JSON_TYPE(request_spec) = 'OBJECT' AND JSON_CONTAINS_PATH(request_spec, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(request_spec, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(request_spec, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(request_spec, '$.data')) <> 'NULL'),
  CONSTRAINT ck_access_session_30 CHECK (JSON_TYPE(endpoint) = 'OBJECT' AND JSON_CONTAINS_PATH(endpoint, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(endpoint, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(endpoint, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(endpoint, '$.data')) <> 'NULL'),
  CONSTRAINT ck_access_session_31 CHECK (JSON_TYPE(failure) = 'OBJECT' AND JSON_CONTAINS_PATH(failure, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(failure, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(failure, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(failure, '$.data')) <> 'NULL'),
  CONSTRAINT ck_access_session_32 CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;

-- Optional short-lived one-use gateway tickets. Store SHA256 of a high-entropy random token, never the bearer value. Consume uses SELECT FOR UPDATE after lease/session admission checks, requiring consumed_at/revoked_at NULL and expires_at > database UTC now; bind returned connection atomically. Token claims alone do not bypass current authorization.
CREATE TABLE sandbox_access_ticket (
  id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  session_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  lease_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  token_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  subject_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  audience VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  role VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  writer_epoch BIGINT NOT NULL,
  expires_at DATETIME(6) NOT NULL,
  consumed_at DATETIME(6) NULL,
  revoked_at DATETIME(6) NULL,
  connection_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_access_ticket_1 (token_hash),
  UNIQUE KEY uq_access_ticket_2 (connection_id),
  KEY ix_access_ticket_1 (session_id, expires_at),
  KEY ix_access_ticket_2 (lease_id, revoked_at),
  KEY ix_access_ticket_3 (expires_at),
  CONSTRAINT ck_access_ticket_1 CHECK (role IN ('Writer', 'Observer')),
  CONSTRAINT ck_access_ticket_2 CHECK ((consumed_at IS NULL) = (connection_id IS NULL)),
  CONSTRAINT ck_access_ticket_3 CHECK (CHAR_LENGTH(id) > 0 AND id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_access_ticket_4 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_access_ticket_5 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_access_ticket_6 CHECK (CHAR_LENGTH(session_id) > 0 AND session_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_access_ticket_7 CHECK (CHAR_LENGTH(lease_id) > 0 AND lease_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_access_ticket_8 CHECK (token_hash REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_access_ticket_9 CHECK (CHAR_LENGTH(subject_id) > 0 AND subject_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_access_ticket_10 CHECK (writer_epoch > 0),
  CONSTRAINT ck_access_ticket_11 CHECK (CHAR_LENGTH(connection_id) > 0 AND connection_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_access_ticket_12 CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;

-- Sequence allocation row. Lock after entity rows; reserve [next_sequence,next_sequence+n) then append events and advance atomically. Sequences start at 1 and never reset after GC. first_available_sequence == next_sequence means no buffered events. Stream tombstone remains after payload retention expires.
CREATE TABLE sandbox_event_stream (
  id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  entity_kind VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  entity_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  next_sequence BIGINT NOT NULL,
  first_available_sequence BIGINT NOT NULL,
  closed_at DATETIME(6) NULL,
  archive_ref VARCHAR(1024) CHARACTER SET ascii COLLATE ascii_bin NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  version BIGINT NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_event_stream_1 (tenant_id, entity_kind, entity_id),
  KEY ix_event_stream_1 (tenant_id, project_id, entity_kind, updated_at),
  KEY ix_event_stream_2 (closed_at),
  CONSTRAINT ck_event_stream_1 CHECK (first_available_sequence <= next_sequence),
  CONSTRAINT ck_event_stream_2 CHECK (CHAR_LENGTH(id) > 0 AND id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_event_stream_3 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_event_stream_4 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_event_stream_5 CHECK (CHAR_LENGTH(entity_id) > 0 AND entity_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_event_stream_6 CHECK (next_sequence > 0),
  CONSTRAINT ck_event_stream_7 CHECK (first_available_sequence > 0),
  CONSTRAINT ck_event_stream_8 CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;

-- Lifecycle/output state and events commit together. Public type is SandboxChanged/ExecutionChanged/Output/OutputGap as applicable, payload includes the matching typed SDK Event variant. Internal streams may carry allocation, lease, operation and audit events; never silently expose unknown types through the SDK. source_key deduplicates ingestion; internal events include reason and old/new row versions.
CREATE TABLE sandbox_event (
  id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  stream_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  entity_kind VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  entity_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  sequence BIGINT NOT NULL,
  type VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  entity_version BIGINT NULL,
  previous_version BIGINT NULL,
  instance_generation BIGINT NULL,
  trace_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  source_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  occurred_at DATETIME(6) NOT NULL,
  expires_at DATETIME(6) NOT NULL,
  payload JSON NOT NULL COMMENT 'Envelope data: sandboxv1.Event for public streams; contract.AuditEvent otherwise',
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_event_1 (stream_id, sequence),
  UNIQUE KEY uq_event_2 (stream_id, source_key),
  KEY ix_event_1 (tenant_id, entity_kind, entity_id, sequence),
  KEY ix_event_2 (expires_at, id),
  KEY ix_event_3 (tenant_id, project_id, created_at, id),
  CONSTRAINT ck_event_1 CHECK (previous_version IS NULL OR (entity_version IS NOT NULL AND entity_version > previous_version)),
  CONSTRAINT ck_event_2 CHECK (CHAR_LENGTH(id) > 0 AND id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_event_3 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_event_4 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_event_5 CHECK (CHAR_LENGTH(stream_id) > 0 AND stream_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_event_6 CHECK (CHAR_LENGTH(entity_id) > 0 AND entity_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_event_7 CHECK (sequence > 0),
  CONSTRAINT ck_event_8 CHECK (entity_version > 0),
  CONSTRAINT ck_event_9 CHECK (previous_version > 0),
  CONSTRAINT ck_event_10 CHECK (instance_generation > 0),
  CONSTRAINT ck_event_11 CHECK (CHAR_LENGTH(trace_id) > 0 AND trace_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_event_12 CHECK (source_key REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_event_13 CHECK (JSON_TYPE(payload) = 'OBJECT' AND JSON_CONTAINS_PATH(payload, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(payload, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(payload, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(payload, '$.data')) <> 'NULL')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;

-- Durable archive receipt, not log bytes. Archive writes use deterministic execution/boot/source-cursor identity; verify existing content digest on duplicate. Only after object durability is confirmed, lock execution+event_stream and atomically record segment, output events and source cursor. SDK output/state events share sequence; byte offsets and backend cursors remain separate.
CREATE TABLE sandbox_output_segment (
  id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  execution_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  instance_generation BIGINT NOT NULL,
  runtime_boot_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_cursor_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_cursor VARCHAR(1024) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  next_source_cursor VARCHAR(1024) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  cursor_kind VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  first_sequence BIGINT NOT NULL,
  last_sequence BIGINT NOT NULL,
  size_bytes BIGINT NOT NULL,
  sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  storage_ref VARCHAR(1024) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  completeness VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  archived_at DATETIME(6) NOT NULL,
  expires_at DATETIME(6) NULL,
  archive JSON NOT NULL COMMENT 'Envelope data: sandboxv1.ArchiveRef',
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_output_segment_1 (execution_id, runtime_boot_id, source_cursor_hash),
  UNIQUE KEY uq_output_segment_2 (execution_id, first_sequence),
  KEY ix_output_segment_1 (execution_id, last_sequence),
  KEY ix_output_segment_2 (expires_at),
  CONSTRAINT ck_output_segment_1 CHECK (first_sequence <= last_sequence),
  CONSTRAINT ck_output_segment_2 CHECK (cursor_kind IN ('Byte', 'Line', 'Event', 'Opaque')),
  CONSTRAINT ck_output_segment_3 CHECK (completeness IN ('Complete', 'Truncated', 'Incomplete')),
  CONSTRAINT ck_output_segment_4 CHECK (CHAR_LENGTH(id) > 0 AND id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_output_segment_5 CHECK (CHAR_LENGTH(tenant_id) > 0 AND tenant_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_output_segment_6 CHECK (CHAR_LENGTH(project_id) > 0 AND project_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_output_segment_7 CHECK (CHAR_LENGTH(execution_id) > 0 AND execution_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_output_segment_8 CHECK (instance_generation > 0),
  CONSTRAINT ck_output_segment_9 CHECK (CHAR_LENGTH(runtime_boot_id) > 0 AND runtime_boot_id NOT REGEXP '[[:space:]]'),
  CONSTRAINT ck_output_segment_10 CHECK (source_cursor_hash REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_output_segment_11 CHECK (first_sequence > 0),
  CONSTRAINT ck_output_segment_12 CHECK (last_sequence > 0),
  CONSTRAINT ck_output_segment_13 CHECK (size_bytes >= 0),
  CONSTRAINT ck_output_segment_14 CHECK (sha256 REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT ck_output_segment_15 CHECK (JSON_TYPE(archive) = 'OBJECT' AND JSON_CONTAINS_PATH(archive, 'all', '$.schemaVersion', '$.data') = 1 AND JSON_TYPE(JSON_EXTRACT(archive, '$.schemaVersion')) = 'INTEGER' AND JSON_EXTRACT(archive, '$.schemaVersion') = 1 AND JSON_TYPE(JSON_EXTRACT(archive, '$.data')) <> 'NULL')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC;

