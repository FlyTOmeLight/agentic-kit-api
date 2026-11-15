# 数据字典与存储载荷

必要保存失败时，`sandbox_instance.status=Draining`、`phase=SaveBlocked`；既有租约、工作区占用和实际配额保留。`sandbox_lease.drain_evidence` 的 `contract.DrainEvidence.SaveProtection` 保存服务端冻结的全部必要保存意图、未受理缺口及当前确认快照；`RequiredSaveOperationIDs` 保留已受理原操作的历史引用，不是完整要求集合。释放前按 requirement 解析最新受理的 attempt，核验发布结果和原始数据范围；成功的替代重试可满足该要求，原 Failed 历史不复活，也不继续阻塞。单个旧 checkpoint ID、空操作列表或超时都不能解除保护。这里沿用既有 JSON 列与状态字段，不新增表列，保留全部 `profile_tenant_id`。

本表由 [mysql.sql](schema/mysql.sql) 与 [rows.go](internal/table/rows.go) 逐列核对后整理。DDL 是字段、索引和 CHECK 的依据；本文提供便于评审的字段映射。所有表使用 InnoDB，时间统一 UTC，应用显式提交必填字段；生成列由数据库计算。完整业务事务规则见主方案第 22 节。

SQL 不配置外键或级联删除。跨表引用、租户和代次必须由所属事务端口同时校验；工作区世代变化不能改写旧执行的快照。唯一约束提供互斥，不能替代这些检查。

所有 JSON 列使用 `JSONValue[T]`：`{"schemaVersion":1,"data":...}`。具体类型见 [storage_json.go](internal/contract/storage_json.go)。SQL NULL 对应 nil RawMessage，和 JSON 字面量 null 不同。

当前契约共 18 张表、517 列，与 18 个 Row 结构逐列对应。沙箱中心保存通用环境、进程、文件和终端事实；CLI Runner、厂商版本参数与事件、逻辑 CLI 会话及 resume 由 agentic-kit 管理。Session 复用范围只接受调用方不透明标识，不建立 CLI 会话记录。检查点通过现有路径覆盖字段描述保存范围，不判断应用会话是否可恢复。

接管契约在申请与租约表各增加 `holder_run_epoch`，不复用控制器的 `worker_id`。新 epoch 仅可读对账及授权清理；普通写入/续租须匹配冻结 epoch 和可信当前领取。详见 [Worker 接管协议](../worker-handoff.md)。

## sandbox_profile_revision

不可变 Profile 版本与可用状态。Go 结构：`table.ProfileRevisionRow`。

`resolved_template.data` 对应 `sandboxv1.ProfileSpec`，其中 `EnvironmentPolicy` 固定作用域与释放方式，`WorkspaceDataPolicy` 固定保存策略版本与数据恢复等级，`ExecutionPolicy`、`ProvisionTimeout` 和 `LeasePolicy` 规定执行、准备、租约及终端保留边界。调用方提交的策略字段只作一致性校验。策略之间的兼容性由发布校验负责；SQL 的 JSON 封装 CHECK 不代替业务校验，不新增表或列。

| 列 | SQL 类型 | 允许 NULL | Go 字段与类型 | 说明 |
| --- | --- | --- | --- | --- |
| `tenant_id` | `VARCHAR(64)` | 否 | `TenantID string` |  |
| `project_id` | `VARCHAR(64)` | 否 | `ProjectID string` |  |
| `profile_id` | `VARCHAR(64)` | 否 | `ProfileID string` |  |
| `revision` | `BIGINT` | 否 | `Revision int64` |  |
| `spec_hash` | `CHAR(64)` | 否 | `SpecHash string` |  |
| `image_digest` | `VARCHAR(255)` | 否 | `ImageDigest string` |  |
| `enabled` | `TINYINT` | 否 | `Enabled bool` |  |
| `disabled_at` | `DATETIME(6)` | 是 | `DisabledAt sql.NullTime` |  |
| `disable_reason` | `TEXT` | 是 | `DisableReason sql.NullString` |  |
| `resolved_template` | `JSON` | 否 | `ResolvedTemplate json.RawMessage` | Envelope data: sandboxv1.ProfileSpec |
| `capabilities` | `JSON` | 否 | `Capabilities json.RawMessage` | Envelope data: []sandboxv1.Capability |
| `conditions` | `JSON` | 否 | `Conditions json.RawMessage` | Envelope data: []sandboxv1.Condition |
| `created_at` | `DATETIME(6)` | 否 | `CreatedAt time.Time` |  |
| `updated_at` | `DATETIME(6)` | 否 | `UpdatedAt time.Time` |  |
| `version` | `BIGINT` | 否 | `Version int64` |  |

索引：

```sql
PRIMARY KEY (tenant_id, profile_id, revision)
KEY ix_profile_revision_1 (tenant_id, project_id, enabled, profile_id, revision)
```

## sandbox_quota_account

租户、平台库存与集群容量账户。Go 结构：`table.QuotaAccountRow`。

| 列 | SQL 类型 | 允许 NULL | Go 字段与类型 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `VARCHAR(64)` | 否 | `ID string` |  |
| `tenant_id` | `VARCHAR(64)` | 否 | `TenantID string` |  |
| `project_id` | `VARCHAR(64)` | 否 | `ProjectID string` |  |
| `account_kind` | `VARCHAR(32)` | 否 | `AccountKind string` |  |
| `scope_id` | `VARCHAR(64)` | 否 | `ScopeID string` |  |
| `resource` | `VARCHAR(32)` | 否 | `Resource string` |  |
| `limit_amount` | `BIGINT` | 否 | `LimitAmount int64` |  |
| `reserved_amount` | `BIGINT` | 否 | `ReservedAmount int64` |  |
| `charged_amount` | `BIGINT` | 否 | `ChargedAmount int64` |  |
| `observed_at` | `DATETIME(6)` | 是 | `ObservedAt sql.NullTime` |  |
| `created_at` | `DATETIME(6)` | 否 | `CreatedAt time.Time` |  |
| `updated_at` | `DATETIME(6)` | 否 | `UpdatedAt time.Time` |  |
| `version` | `BIGINT` | 否 | `Version int64` |  |

索引：

```sql
PRIMARY KEY (id)
UNIQUE KEY uq_quota_account_1 (account_kind, scope_id, resource)
KEY ix_quota_account_1 (tenant_id, project_id, account_kind, resource)
```

## sandbox_quota_reservation

按资源单位记录预留、计入和释放。Go 结构：`table.QuotaReservationRow`。

| 列 | SQL 类型 | 允许 NULL | Go 字段与类型 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `VARCHAR(64)` | 否 | `ID string` |  |
| `tenant_id` | `VARCHAR(64)` | 否 | `TenantID string` |  |
| `project_id` | `VARCHAR(64)` | 否 | `ProjectID string` |  |
| `account_id` | `VARCHAR(64)` | 否 | `AccountID string` |  |
| `ledger_key` | `CHAR(64)` | 否 | `LedgerKey string` |  |
| `subject_kind` | `VARCHAR(32)` | 否 | `SubjectKind string` |  |
| `subject_id` | `VARCHAR(64)` | 否 | `SubjectID string` |  |
| `subject_generation` | `BIGINT` | 否 | `SubjectGeneration int64` |  |
| `charge_class` | `VARCHAR(32)` | 否 | `ChargeClass string` |  |
| `amount` | `BIGINT` | 否 | `Amount int64` |  |
| `status` | `VARCHAR(32)` | 否 | `Status string` |  |
| `acquisition_id` | `VARCHAR(64)` | 是 | `AcquisitionID sql.NullString` |  |
| `instance_id` | `VARCHAR(64)` | 是 | `InstanceID sql.NullString` |  |
| `instance_generation` | `BIGINT` | 是 | `InstanceGeneration sql.NullInt64` |  |
| `lease_id` | `VARCHAR(64)` | 是 | `LeaseID sql.NullString` |  |
| `transferred_from_account_id` | `VARCHAR(64)` | 是 | `TransferredFromAccountID sql.NullString` |  |
| `reserved_at` | `DATETIME(6)` | 否 | `ReservedAt time.Time` |  |
| `charged_at` | `DATETIME(6)` | 是 | `ChargedAt sql.NullTime` |  |
| `released_at` | `DATETIME(6)` | 是 | `ReleasedAt sql.NullTime` |  |
| `release_evidence_ref` | `VARCHAR(1024)` | 是 | `ReleaseEvidenceRef sql.NullString` |  |
| `created_at` | `DATETIME(6)` | 否 | `CreatedAt time.Time` |  |
| `updated_at` | `DATETIME(6)` | 否 | `UpdatedAt time.Time` |  |
| `version` | `BIGINT` | 否 | `Version int64` |  |

索引：

```sql
PRIMARY KEY (id)
UNIQUE KEY uq_quota_reservation_1 (ledger_key)
KEY ix_quota_reservation_1 (account_id, status, id)
KEY ix_quota_reservation_2 (subject_kind, subject_id, subject_generation, charge_class)
KEY ix_quota_reservation_3 (tenant_id, acquisition_id, status)
KEY ix_quota_reservation_4 (instance_id, instance_generation, status)
```

## sandbox_warm_pool

预热池配置、库存观察和补池处理权。Go 结构：`table.WarmPoolRow`。

| 列 | SQL 类型 | 允许 NULL | Go 字段与类型 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `VARCHAR(64)` | 否 | `ID string` |  |
| `tenant_id` | `VARCHAR(64)` | 否 | `TenantID string` |  |
| `project_id` | `VARCHAR(64)` | 否 | `ProjectID string` |  |
| `pool_key` | `CHAR(64)` | 否 | `PoolKey string` |  |
| `profile_tenant_id` | `VARCHAR(64)` | 否 | `ProfileTenantID string` |  |
| `profile_id` | `VARCHAR(64)` | 否 | `ProfileID string` |  |
| `profile_revision` | `BIGINT` | 否 | `ProfileRevision int64` |  |
| `cluster_id` | `VARCHAR(64)` | 否 | `ClusterID string` |  |
| `namespace` | `VARCHAR(255)` | 否 | `Namespace string` |  |
| `spec_hash` | `CHAR(64)` | 否 | `SpecHash string` |  |
| `enabled` | `TINYINT` | 否 | `Enabled bool` |  |
| `min_ready` | `BIGINT` | 否 | `MinReady int64` |  |
| `max_ready` | `BIGINT` | 否 | `MaxReady int64` |  |
| `max_provisioning` | `BIGINT` | 否 | `MaxProvisioning int64` |  |
| `max_idle_seconds` | `BIGINT` | 否 | `MaxIdleSeconds int64` |  |
| `ready_count` | `BIGINT` | 否 | `ReadyCount int64` |  |
| `provisioning_count` | `BIGINT` | 否 | `ProvisioningCount int64` |  |
| `deleting_count` | `BIGINT` | 否 | `DeletingCount int64` |  |
| `observed_at` | `DATETIME(6)` | 是 | `ObservedAt sql.NullTime` |  |
| `pool_spec` | `JSON` | 否 | `PoolSpec json.RawMessage` | Envelope data: sandboxv1.PoolSpec |
| `resolved_spec` | `JSON` | 否 | `ResolvedSpec json.RawMessage` | Envelope data: contract.InstanceSpec |
| `conditions` | `JSON` | 否 | `Conditions json.RawMessage` | Envelope data: []sandboxv1.Condition |
| `next_reconcile_at` | `DATETIME(6)` | 是 | `NextReconcileAt sql.NullTime` |  |
| `worker_id` | `VARCHAR(64)` | 是 | `WorkerID sql.NullString` |  |
| `claim_token` | `VARCHAR(64)` | 是 | `ClaimToken sql.NullString` |  |
| `claim_until` | `DATETIME(6)` | 是 | `ClaimUntil sql.NullTime` |  |
| `created_at` | `DATETIME(6)` | 否 | `CreatedAt time.Time` |  |
| `updated_at` | `DATETIME(6)` | 否 | `UpdatedAt time.Time` |  |
| `version` | `BIGINT` | 否 | `Version int64` |  |

索引：

```sql
PRIMARY KEY (id)
UNIQUE KEY uq_warm_pool_1 (pool_key)
KEY ix_warm_pool_1 (enabled, next_reconcile_at, id)
KEY ix_warm_pool_2 (cluster_id, enabled, profile_id, profile_revision)
```

## sandbox_workspace_binding

工作区世代、存储身份、准备权、单写租约和恢复屏障。Go 结构：`table.WorkspaceBindingRow`。

| 列 | SQL 类型 | 允许 NULL | Go 字段与类型 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `VARCHAR(64)` | 否 | `ID string` |  |
| `tenant_id` | `VARCHAR(64)` | 否 | `TenantID string` |  |
| `project_id` | `VARCHAR(64)` | 否 | `ProjectID string` |  |
| `workspace_generation` | `BIGINT` | 否 | `WorkspaceGeneration int64` |  |
| `mode` | `VARCHAR(32)` | 否 | `Mode string` |  |
| `source_ref` | `VARCHAR(1024)` | 否 | `SourceRef string` |  |
| `base_commit` | `VARCHAR(255)` | 否 | `BaseCommit string` |  |
| `data_version` | `VARCHAR(255)` | 否 | `DataVersion string` |  |
| `storage_ref` | `VARCHAR(1024)` | 是 | `StorageRef sql.NullString` |  |
| `storage_identity_hash` | `CHAR(64)` | 是 | `StorageIdentityHash sql.NullString` |  |
| `recovery_level` | `VARCHAR(32)` | 否 | `RecoveryLevel string` |  |
| `status` | `VARCHAR(32)` | 否 | `Status string` |  |
| `preparing_acquisition_id` | `VARCHAR(64)` | 是 | `PreparingAcquisitionID sql.NullString` |  |
| `preparation_expires_at` | `DATETIME(6)` | 是 | `PreparationExpiresAt sql.NullTime` |  |
| `writer_lease_id` | `VARCHAR(64)` | 是 | `WriterLeaseID sql.NullString` |  |
| `bound_instance_id` | `VARCHAR(64)` | 是 | `BoundInstanceID sql.NullString` |  |
| `bound_instance_generation` | `BIGINT` | 是 | `BoundInstanceGeneration sql.NullInt64` |  |
| `next_fencing_token` | `BIGINT` | 否 | `NextFencingToken int64` |  |
| `checkpoint_id` | `VARCHAR(64)` | 是 | `CheckpointID sql.NullString` |  |
| `save_policy_id` | `VARCHAR(64)` | 否 | `SavePolicyID string` |  |
| `save_policy_revision` | `BIGINT` | 否 | `SavePolicyRevision int64` |  |
| `write_barrier_operation_id` | `VARCHAR(64)` | 是 | `WriteBarrierOperationID sql.NullString` |  |
| `write_barrier_epoch` | `BIGINT` | 否 | `WriteBarrierEpoch int64` |  |
| `physical_fence_verified_at` | `DATETIME(6)` | 是 | `PhysicalFenceVerifiedAt sql.NullTime` |  |
| `physical_fence_evidence_ref` | `VARCHAR(1024)` | 是 | `PhysicalFenceEvidenceRef sql.NullString` |  |
| `layout` | `JSON` | 否 | `Layout json.RawMessage` | Envelope data: sandboxv1.WorkspaceLayout |
| `failure` | `JSON` | 是 | `Failure json.RawMessage` | Envelope data: sandboxv1.Error |
| `created_at` | `DATETIME(6)` | 否 | `CreatedAt time.Time` |  |
| `updated_at` | `DATETIME(6)` | 否 | `UpdatedAt time.Time` |  |
| `version` | `BIGINT` | 否 | `Version int64` |  |

索引：

```sql
PRIMARY KEY (id)
UNIQUE KEY uq_workspace_binding_1 (tenant_id, id, workspace_generation)
UNIQUE KEY uq_workspace_binding_2 (storage_identity_hash)
UNIQUE KEY uq_workspace_binding_3 (preparing_acquisition_id)
UNIQUE KEY uq_workspace_binding_4 (writer_lease_id)
KEY ix_workspace_binding_1 (tenant_id, project_id, status, updated_at)
KEY ix_workspace_binding_2 (bound_instance_id, bound_instance_generation)
KEY ix_workspace_binding_3 (status, preparation_expires_at)
```

## sandbox_acquire_request

申请幂等、准备进度与冻结的 Ready 结果。Go 结构：`table.AcquireRequestRow`。

| 列 | SQL 类型 | 允许 NULL | Go 字段与类型 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `VARCHAR(64)` | 否 | `ID string` |  |
| `tenant_id` | `VARCHAR(64)` | 否 | `TenantID string` |  |
| `project_id` | `VARCHAR(64)` | 否 | `ProjectID string` |  |
| `caller_id` | `VARCHAR(64)` | 否 | `CallerID string` |  |
| `request_key` | `VARCHAR(128)` | 否 | `RequestKey string` |  |
| `spec_hash` | `CHAR(64)` | 否 | `SpecHash string` |  |
| `mode` | `VARCHAR(32)` | 否 | `Mode string` |  |
| `scope_type` | `VARCHAR(32)` | 否 | `ScopeType string` |  |
| `scope_id` | `VARCHAR(64)` | 否 | `ScopeID string` |  |
| `holder_run_id` | `VARCHAR(64)` | 否 | `HolderRunID string` |  |
| `holder_run_epoch` | `BIGINT` | 否 | `HolderRunEpoch int64` | 冻结的上游 Run 领取代次，1..MaxCounter |
| `profile_tenant_id` | `VARCHAR(64)` | 否 | `ProfileTenantID string` |  |
| `profile_id` | `VARCHAR(64)` | 否 | `ProfileID string` |  |
| `profile_revision` | `BIGINT` | 否 | `ProfileRevision int64` |  |
| `workspace_id` | `VARCHAR(64)` | 否 | `WorkspaceID string` |  |
| `workspace_generation` | `BIGINT` | 否 | `WorkspaceGeneration int64` |  |
| `reuse_key` | `CHAR(64)` | 是 | `ReuseKey sql.NullString` |  |
| `pool_key` | `CHAR(64)` | 否 | `PoolKey string` |  |
| `capacity_policy` | `VARCHAR(32)` | 否 | `CapacityPolicy string` |  |
| `status` | `VARCHAR(32)` | 否 | `Status string` |  |
| `phase` | `VARCHAR(255)` | 是 | `Phase sql.NullString` |  |
| `queue_deadline` | `DATETIME(6)` | 否 | `QueueDeadline time.Time` |  |
| `provision_deadline` | `DATETIME(6)` | 否 | `ProvisionDeadline time.Time` |  |
| `requested_lease_seconds` | `BIGINT` | 否 | `RequestedLeaseSeconds int64` |  |
| `instance_id` | `VARCHAR(64)` | 是 | `InstanceID sql.NullString` |  |
| `instance_generation` | `BIGINT` | 是 | `InstanceGeneration sql.NullInt64` |  |
| `lease_id` | `VARCHAR(64)` | 是 | `LeaseID sql.NullString` |  |
| `allocation_source` | `VARCHAR(32)` | 是 | `AllocationSource sql.NullString` |  |
| `attempt` | `BIGINT` | 否 | `Attempt int64` |  |
| `completed_at` | `DATETIME(6)` | 是 | `CompletedAt sql.NullTime` |  |
| `request_spec` | `JSON` | 是 | `RequestSpec json.RawMessage` | Envelope data: contract.StoredAcquireRequest |
| `ready_result` | `JSON` | 是 | `ReadyResult json.RawMessage` | Envelope data: sandboxv1.AcquisitionResult |
| `ready_evidence` | `JSON` | 是 | `ReadyEvidence json.RawMessage` | Envelope data: contract.ReadyEvidence |
| `credential_grants` | `JSON` | 是 | `CredentialGrants json.RawMessage` | Envelope data: []contract.CredentialGrant (references only) |
| `failure` | `JSON` | 是 | `Failure json.RawMessage` | Envelope data: sandboxv1.Error |
| `conditions` | `JSON` | 否 | `Conditions json.RawMessage` | Envelope data: []sandboxv1.Condition |
| `next_reconcile_at` | `DATETIME(6)` | 是 | `NextReconcileAt sql.NullTime` |  |
| `worker_id` | `VARCHAR(64)` | 是 | `WorkerID sql.NullString` |  |
| `claim_token` | `VARCHAR(64)` | 是 | `ClaimToken sql.NullString` |  |
| `claim_until` | `DATETIME(6)` | 是 | `ClaimUntil sql.NullTime` |  |
| `tombstoned_at` | `DATETIME(6)` | 是 | `TombstonedAt sql.NullTime` |  |
| `result_expires_at` | `DATETIME(6)` | 是 | `ResultExpiresAt sql.NullTime` |  |
| `replay_closed_at` | `DATETIME(6)` | 是 | `ReplayClosedAt sql.NullTime` |  |
| `purge_after` | `DATETIME(6)` | 是 | `PurgeAfter sql.NullTime` |  |
| `created_at` | `DATETIME(6)` | 否 | `CreatedAt time.Time` |  |
| `updated_at` | `DATETIME(6)` | 否 | `UpdatedAt time.Time` |  |
| `version` | `BIGINT` | 否 | `Version int64` |  |

索引：

```sql
PRIMARY KEY (id)
UNIQUE KEY uq_acquire_request_1 (tenant_id, caller_id, request_key)
UNIQUE KEY uq_acquire_request_2 (lease_id)
KEY ix_acquire_request_1 (status, next_reconcile_at, id)
KEY ix_acquire_request_2 (tenant_id, project_id, created_at, id)
KEY ix_acquire_request_3 (workspace_id, workspace_generation, status)
KEY ix_acquire_request_4 (status, queue_deadline)
KEY ix_acquire_request_5 (status, provision_deadline)
KEY ix_acquire_request_6 (tombstoned_at, purge_after)
```

## sandbox_instance

实例物理身份、代次、配置与生命周期。Go 结构：`table.InstanceRow`。

| 列 | SQL 类型 | 允许 NULL | Go 字段与类型 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `VARCHAR(64)` | 否 | `ID string` |  |
| `tenant_id` | `VARCHAR(64)` | 否 | `TenantID string` |  |
| `project_id` | `VARCHAR(64)` | 否 | `ProjectID string` |  |
| `generation` | `BIGINT` | 否 | `Generation int64` |  |
| `status` | `VARCHAR(32)` | 否 | `Status string` |  |
| `phase` | `VARCHAR(255)` | 是 | `Phase sql.NullString` |  |
| `reason` | `VARCHAR(255)` | 是 | `Reason sql.NullString` |  |
| `profile_tenant_id` | `VARCHAR(64)` | 否 | `ProfileTenantID string` |  |
| `profile_id` | `VARCHAR(64)` | 否 | `ProfileID string` |  |
| `profile_revision` | `BIGINT` | 否 | `ProfileRevision int64` |  |
| `pool_key` | `CHAR(64)` | 是 | `PoolKey sql.NullString` |  |
| `reuse_key` | `CHAR(64)` | 是 | `ReuseKey sql.NullString` |  |
| `spec_hash` | `CHAR(64)` | 否 | `SpecHash string` |  |
| `cluster_id` | `VARCHAR(64)` | 否 | `ClusterID string` |  |
| `namespace` | `VARCHAR(255)` | 否 | `Namespace string` |  |
| `pod_name` | `VARCHAR(255)` | 否 | `PodName string` |  |
| `pod_uid` | `VARCHAR(64)` | 是 | `PodUID sql.NullString` |  |
| `runtime_boot_id` | `VARCHAR(64)` | 是 | `RuntimeBootID sql.NullString` |  |
| `runtime_protocol_version` | `VARCHAR(255)` | 是 | `RuntimeProtocolVersion sql.NullString` |  |
| `physical_phase` | `VARCHAR(32)` | 是 | `PhysicalPhase sql.NullString` |  |
| `ever_claimed` | `TINYINT` | 否 | `EverClaimed bool` |  |
| `claimed_acquisition_id` | `VARCHAR(64)` | 是 | `ClaimedAcquisitionID sql.NullString` |  |
| `claim_expires_at` | `DATETIME(6)` | 是 | `ClaimExpiresAt sql.NullTime` |  |
| `active_lease_id` | `VARCHAR(64)` | 是 | `ActiveLeaseID sql.NullString` |  |
| `workspace_id` | `VARCHAR(64)` | 是 | `WorkspaceID sql.NullString` |  |
| `workspace_generation` | `BIGINT` | 是 | `WorkspaceGeneration sql.NullInt64` |  |
| `admission_epoch` | `BIGINT` | 否 | `AdmissionEpoch int64` |  |
| `provision_deadline` | `DATETIME(6)` | 否 | `ProvisionDeadline time.Time` |  |
| `hard_expires_at` | `DATETIME(6)` | 否 | `HardExpiresAt time.Time` |  |
| `idle_expires_at` | `DATETIME(6)` | 是 | `IdleExpiresAt sql.NullTime` |  |
| `observed_at` | `DATETIME(6)` | 是 | `ObservedAt sql.NullTime` |  |
| `physical_gone_at` | `DATETIME(6)` | 是 | `PhysicalGoneAt sql.NullTime` |  |
| `writes_stopped_at` | `DATETIME(6)` | 是 | `WritesStoppedAt sql.NullTime` |  |
| `deleted_at` | `DATETIME(6)` | 是 | `DeletedAt sql.NullTime` |  |
| `resolved_spec` | `JSON` | 否 | `ResolvedSpec json.RawMessage` | Envelope data: contract.InstanceSpec |
| `physical_ref` | `JSON` | 是 | `PhysicalRef json.RawMessage` | Envelope data: contract.PhysicalRef |
| `endpoint` | `JSON` | 是 | `Endpoint json.RawMessage` | Envelope data: contract.Endpoint (private reference DTO) |
| `runtime_info` | `JSON` | 是 | `RuntimeInfo json.RawMessage` | Envelope data: contract.RuntimeInfo |
| `effective_capabilities` | `JSON` | 否 | `EffectiveCapabilities json.RawMessage` | Envelope data: []sandboxv1.Capability |
| `fence_evidence` | `JSON` | 是 | `FenceEvidence json.RawMessage` | Envelope data: contract.FenceEvidence |
| `failure` | `JSON` | 是 | `Failure json.RawMessage` | Envelope data: sandboxv1.Error |
| `conditions` | `JSON` | 否 | `Conditions json.RawMessage` | Envelope data: []sandboxv1.Condition |
| `next_reconcile_at` | `DATETIME(6)` | 是 | `NextReconcileAt sql.NullTime` |  |
| `worker_id` | `VARCHAR(64)` | 是 | `WorkerID sql.NullString` |  |
| `claim_token` | `VARCHAR(64)` | 是 | `ClaimToken sql.NullString` |  |
| `claim_until` | `DATETIME(6)` | 是 | `ClaimUntil sql.NullTime` |  |
| `created_at` | `DATETIME(6)` | 否 | `CreatedAt time.Time` |  |
| `updated_at` | `DATETIME(6)` | 否 | `UpdatedAt time.Time` |  |
| `version` | `BIGINT` | 否 | `Version int64` |  |

索引：

```sql
PRIMARY KEY (id)
UNIQUE KEY uq_instance_1 (id, generation)
UNIQUE KEY uq_instance_2 (cluster_id, namespace, pod_name)
UNIQUE KEY uq_instance_3 (cluster_id, pod_uid)
UNIQUE KEY uq_instance_4 (claimed_acquisition_id)
UNIQUE KEY uq_instance_5 (active_lease_id)
KEY ix_instance_1 (status, next_reconcile_at, id)
KEY ix_instance_2 (pool_key, status, created_at, id)
KEY ix_instance_3 (tenant_id, project_id, status, id)
KEY ix_instance_4 (workspace_id, workspace_generation)
KEY ix_instance_5 (status, idle_expires_at)
KEY ix_instance_6 (status, hard_expires_at)
```

## sandbox_lease

写持有者、续租时间、准入屏障和释放证据。Go 结构：`table.LeaseRow`。

| 列 | SQL 类型 | 允许 NULL | Go 字段与类型 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `VARCHAR(64)` | 否 | `ID string` |  |
| `tenant_id` | `VARCHAR(64)` | 否 | `TenantID string` |  |
| `project_id` | `VARCHAR(64)` | 否 | `ProjectID string` |  |
| `acquisition_id` | `VARCHAR(64)` | 否 | `AcquisitionID string` |  |
| `instance_id` | `VARCHAR(64)` | 否 | `InstanceID string` |  |
| `instance_generation` | `BIGINT` | 否 | `InstanceGeneration int64` |  |
| `pod_uid` | `VARCHAR(64)` | 否 | `PodUID string` |  |
| `runtime_boot_id` | `VARCHAR(64)` | 否 | `RuntimeBootID string` |  |
| `workspace_id` | `VARCHAR(64)` | 否 | `WorkspaceID string` |  |
| `workspace_generation` | `BIGINT` | 否 | `WorkspaceGeneration int64` |  |
| `holder_run_id` | `VARCHAR(64)` | 否 | `HolderRunID string` |  |
| `holder_run_epoch` | `BIGINT` | 否 | `HolderRunEpoch int64` | 冻结的上游 Run 领取代次，1..MaxCounter |
| `caller_id` | `VARCHAR(64)` | 否 | `CallerID string` |  |
| `scope_type` | `VARCHAR(32)` | 否 | `ScopeType string` |  |
| `scope_id` | `VARCHAR(64)` | 否 | `ScopeID string` |  |
| `fencing_token` | `BIGINT` | 否 | `FencingToken int64` |  |
| `admission_epoch` | `BIGINT` | 否 | `AdmissionEpoch int64` |  |
| `status` | `VARCHAR(32)` | 否 | `Status string` |  |
| `expires_at` | `DATETIME(6)` | 否 | `ExpiresAt time.Time` |  |
| `hard_expires_at` | `DATETIME(6)` | 否 | `HardExpiresAt time.Time` |  |
| `admission_closed_at` | `DATETIME(6)` | 是 | `AdmissionClosedAt sql.NullTime` |  |
| `release_mode` | `VARCHAR(32)` | 是 | `ReleaseMode sql.NullString` |  |
| `release_operation_id` | `VARCHAR(64)` | 是 | `ReleaseOperationID sql.NullString` |  |
| `releasing_at` | `DATETIME(6)` | 是 | `ReleasingAt sql.NullTime` |  |
| `released_at` | `DATETIME(6)` | 是 | `ReleasedAt sql.NullTime` |  |
| `runtime_barrier_confirmed_at` | `DATETIME(6)` | 是 | `RuntimeBarrierConfirmedAt sql.NullTime` |  |
| `process_tree_stopped_at` | `DATETIME(6)` | 是 | `ProcessTreeStoppedAt sql.NullTime` |  |
| `sessions_closed_at` | `DATETIME(6)` | 是 | `SessionsClosedAt sql.NullTime` |  |
| `credentials_revoked_at` | `DATETIME(6)` | 是 | `CredentialsRevokedAt sql.NullTime` |  |
| `permissions` | `JSON` | 否 | `Permissions json.RawMessage` | Envelope data: []string |
| `credential_grants` | `JSON` | 否 | `CredentialGrants json.RawMessage` | Envelope data: []contract.CredentialGrant (references only) |
| `termination` | `JSON` | 是 | `Termination json.RawMessage` | Envelope data: sandboxv1.TerminationResult |
| `drain_evidence` | `JSON` | 是 | `DrainEvidence json.RawMessage` | Envelope data: contract.DrainEvidence |
| `failure` | `JSON` | 是 | `Failure json.RawMessage` | Envelope data: sandboxv1.Error |
| `next_reconcile_at` | `DATETIME(6)` | 是 | `NextReconcileAt sql.NullTime` |  |
| `worker_id` | `VARCHAR(64)` | 是 | `WorkerID sql.NullString` |  |
| `claim_token` | `VARCHAR(64)` | 是 | `ClaimToken sql.NullString` |  |
| `claim_until` | `DATETIME(6)` | 是 | `ClaimUntil sql.NullTime` |  |
| `created_at` | `DATETIME(6)` | 否 | `CreatedAt time.Time` |  |
| `updated_at` | `DATETIME(6)` | 否 | `UpdatedAt time.Time` |  |
| `version` | `BIGINT` | 否 | `Version int64` |  |

索引：

```sql
PRIMARY KEY (id)
UNIQUE KEY uq_lease_1 (acquisition_id)
UNIQUE KEY uq_lease_2 (workspace_id, fencing_token)
KEY ix_lease_1 (instance_id, instance_generation, status)
KEY ix_lease_2 (tenant_id, holder_run_id, status)
KEY ix_lease_3 (status, expires_at)
KEY ix_lease_4 (status, hard_expires_at)
KEY ix_lease_5 (status, next_reconcile_at, id)
KEY ix_lease_6 (workspace_id, workspace_generation, status)
```

## sandbox_reuse_slot

复用范围对应的当前准备权/写持有者。Go 结构：`table.ReuseSlotRow`。

| 列 | SQL 类型 | 允许 NULL | Go 字段与类型 | 说明 |
| --- | --- | --- | --- | --- |
| `tenant_id` | `VARCHAR(64)` | 否 | `TenantID string` |  |
| `project_id` | `VARCHAR(64)` | 否 | `ProjectID string` |  |
| `reuse_key` | `CHAR(64)` | 否 | `ReuseKey string` |  |
| `scope_type` | `VARCHAR(32)` | 否 | `ScopeType string` |  |
| `scope_id` | `VARCHAR(64)` | 否 | `ScopeID string` |  |
| `profile_tenant_id` | `VARCHAR(64)` | 否 | `ProfileTenantID string` |  |
| `profile_id` | `VARCHAR(64)` | 否 | `ProfileID string` |  |
| `profile_revision` | `BIGINT` | 否 | `ProfileRevision int64` |  |
| `workspace_id` | `VARCHAR(64)` | 否 | `WorkspaceID string` |  |
| `workspace_generation` | `BIGINT` | 否 | `WorkspaceGeneration int64` |  |
| `isolation_policy_revision` | `BIGINT` | 否 | `IsolationPolicyRevision int64` |  |
| `access_policy_revision` | `BIGINT` | 否 | `AccessPolicyRevision int64` |  |
| `readonly_asset_digest` | `CHAR(64)` | 否 | `ReadonlyAssetDigest string` |  |
| `preparing_acquisition_id` | `VARCHAR(64)` | 是 | `PreparingAcquisitionID sql.NullString` |  |
| `instance_id` | `VARCHAR(64)` | 是 | `InstanceID sql.NullString` |  |
| `instance_generation` | `BIGINT` | 是 | `InstanceGeneration sql.NullInt64` |  |
| `writer_lease_id` | `VARCHAR(64)` | 是 | `WriterLeaseID sql.NullString` |  |
| `created_at` | `DATETIME(6)` | 否 | `CreatedAt time.Time` |  |
| `updated_at` | `DATETIME(6)` | 否 | `UpdatedAt time.Time` |  |
| `version` | `BIGINT` | 否 | `Version int64` |  |

索引：

```sql
PRIMARY KEY (tenant_id, reuse_key)
UNIQUE KEY uq_reuse_slot_1 (preparing_acquisition_id)
UNIQUE KEY uq_reuse_slot_2 (writer_lease_id)
KEY ix_reuse_slot_1 (tenant_id, project_id, scope_type, scope_id)
KEY ix_reuse_slot_2 (instance_id, instance_generation)
KEY ix_reuse_slot_3 (workspace_id, workspace_generation)
```

## sandbox_action

统一动作幂等账本与异步操作结果。Go 结构：`table.ActionRow`。

| 列 | SQL 类型 | 允许 NULL | Go 字段与类型 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `VARCHAR(64)` | 否 | `ID string` |  |
| `tenant_id` | `VARCHAR(64)` | 否 | `TenantID string` |  |
| `project_id` | `VARCHAR(64)` | 否 | `ProjectID string` |  |
| `caller_id` | `VARCHAR(64)` | 否 | `CallerID string` |  |
| `action` | `VARCHAR(255)` | 否 | `Action string` |  |
| `request_key` | `VARCHAR(128)` | 否 | `RequestKey string` |  |
| `spec_hash` | `CHAR(64)` | 否 | `SpecHash string` |  |
| `status` | `VARCHAR(32)` | 否 | `Status string` |  |
| `target_kind` | `VARCHAR(32)` | 否 | `TargetKind string` |  |
| `target_id` | `VARCHAR(64)` | 否 | `TargetID string` |  |
| `target_revision` | `BIGINT` | 否 | `TargetRevision int64` | 0 means unversioned target; positive for ProfileRevision |
| `lease_id` | `VARCHAR(64)` | 是 | `LeaseID sql.NullString` |  |
| `acquisition_id` | `VARCHAR(64)` | 是 | `AcquisitionID sql.NullString` |  |
| `instance_id` | `VARCHAR(64)` | 是 | `InstanceID sql.NullString` |  |
| `instance_generation` | `BIGINT` | 是 | `InstanceGeneration sql.NullInt64` |  |
| `pod_uid` | `VARCHAR(64)` | 是 | `PodUID sql.NullString` |  |
| `runtime_boot_id` | `VARCHAR(64)` | 是 | `RuntimeBootID sql.NullString` |  |
| `fencing_token` | `BIGINT` | 是 | `FencingToken sql.NullInt64` |  |
| `admission_epoch` | `BIGINT` | 是 | `AdmissionEpoch sql.NullInt64` |  |
| `workspace_id` | `VARCHAR(64)` | 是 | `WorkspaceID sql.NullString` |  |
| `workspace_generation` | `BIGINT` | 是 | `WorkspaceGeneration sql.NullInt64` |  |
| `dispatch_attempt_id` | `VARCHAR(64)` | 是 | `DispatchAttemptID sql.NullString` |  |
| `dispatch_started_at` | `DATETIME(6)` | 是 | `DispatchStartedAt sql.NullTime` |  |
| `dispatch_outcome` | `VARCHAR(32)` | 是 | `DispatchOutcome sql.NullString` |  |
| `replay_policy` | `VARCHAR(32)` | 否 | `ReplayPolicy string` |  |
| `external_ref` | `VARCHAR(1024)` | 是 | `ExternalRef sql.NullString` |  |
| `evidence_ref` | `VARCHAR(1024)` | 是 | `EvidenceRef sql.NullString` |  |
| `completed_at` | `DATETIME(6)` | 是 | `CompletedAt sql.NullTime` |  |
| `input` | `JSON` | 是 | `Input json.RawMessage` | Envelope data: action-specific normalized request DTO |
| `result` | `JSON` | 是 | `Result json.RawMessage` | Envelope data: action-specific response DTO |
| `failure` | `JSON` | 是 | `Failure json.RawMessage` | Envelope data: sandboxv1.Error |
| `next_reconcile_at` | `DATETIME(6)` | 是 | `NextReconcileAt sql.NullTime` |  |
| `worker_id` | `VARCHAR(64)` | 是 | `WorkerID sql.NullString` |  |
| `claim_token` | `VARCHAR(64)` | 是 | `ClaimToken sql.NullString` |  |
| `claim_until` | `DATETIME(6)` | 是 | `ClaimUntil sql.NullTime` |  |
| `tombstoned_at` | `DATETIME(6)` | 是 | `TombstonedAt sql.NullTime` |  |
| `result_expires_at` | `DATETIME(6)` | 是 | `ResultExpiresAt sql.NullTime` |  |
| `replay_closed_at` | `DATETIME(6)` | 是 | `ReplayClosedAt sql.NullTime` |  |
| `purge_after` | `DATETIME(6)` | 是 | `PurgeAfter sql.NullTime` |  |
| `created_at` | `DATETIME(6)` | 否 | `CreatedAt time.Time` |  |
| `updated_at` | `DATETIME(6)` | 否 | `UpdatedAt time.Time` |  |
| `version` | `BIGINT` | 否 | `Version int64` |  |

索引：

```sql
PRIMARY KEY (id)
UNIQUE KEY uq_action_1 (tenant_id, caller_id, action, target_kind, target_id, target_revision, request_key)
KEY ix_action_1 (lease_id, status, id)
KEY ix_action_2 (acquisition_id, status, id)
KEY ix_action_3 (target_kind, target_id, target_revision, created_at)
KEY ix_action_4 (status, next_reconcile_at, id)
KEY ix_action_5 (tenant_id, caller_id, created_at, id)
KEY ix_action_6 (tombstoned_at, purge_after)
```

## sandbox_execution

命令登记、派发证据、输出游标和终态。Go 结构：`table.ExecutionRow`。

| 列 | SQL 类型 | 允许 NULL | Go 字段与类型 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `VARCHAR(64)` | 否 | `ID string` |  |
| `tenant_id` | `VARCHAR(64)` | 否 | `TenantID string` |  |
| `project_id` | `VARCHAR(64)` | 否 | `ProjectID string` |  |
| `lease_id` | `VARCHAR(64)` | 否 | `LeaseID string` |  |
| `request_key` | `VARCHAR(128)` | 否 | `RequestKey string` |  |
| `spec_hash` | `CHAR(64)` | 否 | `SpecHash string` |  |
| `instance_id` | `VARCHAR(64)` | 否 | `InstanceID string` |  |
| `instance_generation` | `BIGINT` | 否 | `InstanceGeneration int64` |  |
| `pod_uid` | `VARCHAR(64)` | 否 | `PodUID string` |  |
| `runtime_boot_id` | `VARCHAR(64)` | 否 | `RuntimeBootID string` |  |
| `workspace_id` | `VARCHAR(64)` | 否 | `WorkspaceID string` |  |
| `workspace_generation` | `BIGINT` | 否 | `WorkspaceGeneration int64` |  |
| `holder_run_id` | `VARCHAR(64)` | 否 | `HolderRunID string` |  |
| `member_id` | `VARCHAR(64)` | 是 | `MemberID sql.NullString` |  |
| `worktree_id` | `VARCHAR(64)` | 是 | `WorktreeID sql.NullString` |  |
| `fencing_token` | `BIGINT` | 否 | `FencingToken int64` |  |
| `admission_epoch` | `BIGINT` | 否 | `AdmissionEpoch int64` |  |
| `status` | `VARCHAR(32)` | 否 | `Status string` |  |
| `cwd` | `VARCHAR(1024)` | 否 | `Cwd string` |  |
| `command_mode` | `VARCHAR(32)` | 否 | `CommandMode string` |  |
| `deadline` | `DATETIME(6)` | 否 | `Deadline time.Time` |  |
| `max_output_bytes` | `BIGINT` | 否 | `MaxOutputBytes int64` |  |
| `stdin_closed` | `TINYINT` | 否 | `StdinClosed bool` |  |
| `next_input_sequence` | `BIGINT` | 否 | `NextInputSequence int64` |  |
| `dispatch_attempt_id` | `VARCHAR(64)` | 是 | `DispatchAttemptID sql.NullString` |  |
| `dispatch_epoch` | `BIGINT` | 是 | `DispatchEpoch sql.NullInt64` |  |
| `dispatch_started_at` | `DATETIME(6)` | 是 | `DispatchStartedAt sql.NullTime` |  |
| `dispatch_outcome` | `VARCHAR(32)` | 是 | `DispatchOutcome sql.NullString` |  |
| `replay_policy` | `VARCHAR(32)` | 否 | `ReplayPolicy string` |  |
| `backend_ref` | `VARCHAR(255)` | 是 | `BackendRef sql.NullString` |  |
| `started_at` | `DATETIME(6)` | 是 | `StartedAt sql.NullTime` |  |
| `completed_at` | `DATETIME(6)` | 是 | `CompletedAt sql.NullTime` |  |
| `observed_at` | `DATETIME(6)` | 是 | `ObservedAt sql.NullTime` |  |
| `exit_code` | `BIGINT` | 是 | `ExitCode sql.NullInt64` |  |
| `exit_signal` | `VARCHAR(255)` | 是 | `ExitSignal sql.NullString` |  |
| `output_completeness` | `VARCHAR(32)` | 否 | `OutputCompleteness string` |  |
| `output_bytes` | `BIGINT` | 否 | `OutputBytes int64` |  |
| `source_cursor` | `VARCHAR(1024)` | 是 | `SourceCursor sql.NullString` |  |
| `source_cursor_kind` | `VARCHAR(32)` | 是 | `SourceCursorKind sql.NullString` |  |
| `cancel_operation_id` | `VARCHAR(64)` | 是 | `CancelOperationID sql.NullString` |  |
| `request_spec` | `JSON` | 是 | `RequestSpec json.RawMessage` | Envelope data: contract.StoredExecutionRequest |
| `command_spec` | `JSON` | 是 | `CommandSpec json.RawMessage` | Envelope data: contract.CommandSpec (secret references only) |
| `termination` | `JSON` | 是 | `Termination json.RawMessage` | Envelope data: sandboxv1.TerminationResult |
| `failure` | `JSON` | 是 | `Failure json.RawMessage` | Envelope data: sandboxv1.Error |
| `next_reconcile_at` | `DATETIME(6)` | 是 | `NextReconcileAt sql.NullTime` |  |
| `worker_id` | `VARCHAR(64)` | 是 | `WorkerID sql.NullString` |  |
| `claim_token` | `VARCHAR(64)` | 是 | `ClaimToken sql.NullString` |  |
| `claim_until` | `DATETIME(6)` | 是 | `ClaimUntil sql.NullTime` |  |
| `tombstoned_at` | `DATETIME(6)` | 是 | `TombstonedAt sql.NullTime` |  |
| `result_expires_at` | `DATETIME(6)` | 是 | `ResultExpiresAt sql.NullTime` |  |
| `replay_closed_at` | `DATETIME(6)` | 是 | `ReplayClosedAt sql.NullTime` |  |
| `purge_after` | `DATETIME(6)` | 是 | `PurgeAfter sql.NullTime` |  |
| `created_at` | `DATETIME(6)` | 否 | `CreatedAt time.Time` |  |
| `updated_at` | `DATETIME(6)` | 否 | `UpdatedAt time.Time` |  |
| `version` | `BIGINT` | 否 | `Version int64` |  |

索引：

```sql
PRIMARY KEY (id)
UNIQUE KEY uq_execution_1 (lease_id, request_key)
UNIQUE KEY uq_execution_2 (instance_id, instance_generation, runtime_boot_id, backend_ref)
KEY ix_execution_1 (lease_id, status, id)
KEY ix_execution_2 (status, next_reconcile_at, id)
KEY ix_execution_3 (instance_id, instance_generation, status)
KEY ix_execution_4 (workspace_id, workspace_generation, created_at)
KEY ix_execution_5 (tenant_id, project_id, created_at, id)
KEY ix_execution_6 (status, deadline)
KEY ix_execution_7 (tombstoned_at, purge_after)
```

## sandbox_worktree

成员工作树、分支/路径独占与删除事实。Go 结构：`table.WorktreeRow`。

| 列 | SQL 类型 | 允许 NULL | Go 字段与类型 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `VARCHAR(64)` | 否 | `ID string` |  |
| `tenant_id` | `VARCHAR(64)` | 否 | `TenantID string` |  |
| `project_id` | `VARCHAR(64)` | 否 | `ProjectID string` |  |
| `operation_id` | `VARCHAR(64)` | 否 | `OperationID string` |  |
| `lease_id` | `VARCHAR(64)` | 否 | `LeaseID string` |  |
| `request_key` | `VARCHAR(128)` | 否 | `RequestKey string` |  |
| `spec_hash` | `CHAR(64)` | 否 | `SpecHash string` |  |
| `workspace_id` | `VARCHAR(64)` | 否 | `WorkspaceID string` |  |
| `workspace_generation` | `BIGINT` | 否 | `WorkspaceGeneration int64` |  |
| `task_id` | `VARCHAR(64)` | 否 | `TaskID string` |  |
| `holder_run_id` | `VARCHAR(64)` | 否 | `HolderRunID string` |  |
| `member_id` | `VARCHAR(64)` | 否 | `MemberID string` |  |
| `base_commit` | `VARCHAR(255)` | 否 | `BaseCommit string` |  |
| `branch` | `VARCHAR(255)` | 否 | `Branch string` |  |
| `path` | `VARCHAR(1024)` | 否 | `Path string` |  |
| `path_hash` | `CHAR(64)` | 否 | `PathHash string` |  |
| `active_branch` | `VARCHAR(255)` | 是 | `ActiveBranch sql.NullString` |  |
| `active_path_hash` | `CHAR(64)` | 是 | `ActivePathHash sql.NullString` |  |
| `head_commit` | `VARCHAR(255)` | 是 | `HeadCommit sql.NullString` |  |
| `status` | `VARCHAR(32)` | 否 | `Status string` |  |
| `removed_at` | `DATETIME(6)` | 是 | `RemovedAt sql.NullTime` |  |
| `failure` | `JSON` | 是 | `Failure json.RawMessage` | Envelope data: sandboxv1.Error |
| `created_at` | `DATETIME(6)` | 否 | `CreatedAt time.Time` |  |
| `updated_at` | `DATETIME(6)` | 否 | `UpdatedAt time.Time` |  |
| `version` | `BIGINT` | 否 | `Version int64` |  |

索引：

```sql
PRIMARY KEY (id)
UNIQUE KEY uq_worktree_1 (operation_id)
UNIQUE KEY uq_worktree_2 (workspace_id, workspace_generation, active_branch)
UNIQUE KEY uq_worktree_3 (workspace_id, workspace_generation, active_path_hash)
KEY ix_worktree_1 (tenant_id, workspace_id, workspace_generation, status, id)
KEY ix_worktree_2 (lease_id, member_id, status)
```

## sandbox_checkpoint

数据检查点、覆盖范围、静止证据与发布状态。Go 结构：`table.CheckpointRow`。

| 列 | SQL 类型 | 允许 NULL | Go 字段与类型 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `VARCHAR(64)` | 否 | `ID string` |  |
| `tenant_id` | `VARCHAR(64)` | 否 | `TenantID string` |  |
| `project_id` | `VARCHAR(64)` | 否 | `ProjectID string` |  |
| `operation_id` | `VARCHAR(64)` | 否 | `OperationID string` |  |
| `lease_id` | `VARCHAR(64)` | 否 | `LeaseID string` |  |
| `request_key` | `VARCHAR(128)` | 否 | `RequestKey string` |  |
| `spec_hash` | `CHAR(64)` | 否 | `SpecHash string` |  |
| `workspace_id` | `VARCHAR(64)` | 否 | `WorkspaceID string` |  |
| `workspace_generation` | `BIGINT` | 否 | `WorkspaceGeneration int64` |  |
| `instance_id` | `VARCHAR(64)` | 否 | `InstanceID string` |  |
| `instance_generation` | `BIGINT` | 否 | `InstanceGeneration int64` |  |
| `base_commit` | `VARCHAR(255)` | 否 | `BaseCommit string` |  |
| `data_version` | `VARCHAR(255)` | 否 | `DataVersion string` |  |
| `status` | `VARCHAR(32)` | 否 | `Status string` |  |
| `consistency` | `VARCHAR(32)` | 否 | `Consistency string` |  |
| `manifest_ref` | `VARCHAR(1024)` | 是 | `ManifestRef sql.NullString` |  |
| `manifest_sha256` | `CHAR(64)` | 是 | `ManifestSHA256 sql.NullString` |  |
| `storage_ref` | `VARCHAR(1024)` | 是 | `StorageRef sql.NullString` |  |
| `size_bytes` | `BIGINT` | 是 | `SizeBytes sql.NullInt64` |  |
| `published_at` | `DATETIME(6)` | 是 | `PublishedAt sql.NullTime` |  |
| `coverage` | `JSON` | 否 | `Coverage json.RawMessage` | Envelope data: sandboxv1.CheckpointCoverage |
| `manifest` | `JSON` | 是 | `Manifest json.RawMessage` | Envelope data: contract.CheckpointManifest |
| `quiescence_evidence` | `JSON` | 是 | `QuiescenceEvidence json.RawMessage` | Envelope data: contract.QuiescenceEvidence |
| `failure` | `JSON` | 是 | `Failure json.RawMessage` | Envelope data: sandboxv1.Error |
| `created_at` | `DATETIME(6)` | 否 | `CreatedAt time.Time` |  |
| `updated_at` | `DATETIME(6)` | 否 | `UpdatedAt time.Time` |  |
| `version` | `BIGINT` | 否 | `Version int64` |  |

索引：

```sql
PRIMARY KEY (id)
UNIQUE KEY uq_checkpoint_1 (operation_id)
KEY ix_checkpoint_1 (tenant_id, workspace_id, workspace_generation, created_at, id)
KEY ix_checkpoint_2 (status, updated_at)
```

## sandbox_access_session

终端/浏览器/预览会话与单写连接。Go 结构：`table.AccessSessionRow`。

| 列 | SQL 类型 | 允许 NULL | Go 字段与类型 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `VARCHAR(64)` | 否 | `ID string` |  |
| `tenant_id` | `VARCHAR(64)` | 否 | `TenantID string` |  |
| `project_id` | `VARCHAR(64)` | 否 | `ProjectID string` |  |
| `operation_id` | `VARCHAR(64)` | 否 | `OperationID string` |  |
| `lease_id` | `VARCHAR(64)` | 否 | `LeaseID string` |  |
| `request_key` | `VARCHAR(128)` | 否 | `RequestKey string` |  |
| `spec_hash` | `CHAR(64)` | 否 | `SpecHash string` |  |
| `kind` | `VARCHAR(32)` | 否 | `Kind string` |  |
| `status` | `VARCHAR(32)` | 否 | `Status string` |  |
| `instance_id` | `VARCHAR(64)` | 否 | `InstanceID string` |  |
| `instance_generation` | `BIGINT` | 否 | `InstanceGeneration int64` |  |
| `pod_uid` | `VARCHAR(64)` | 否 | `PodUID string` |  |
| `runtime_boot_id` | `VARCHAR(64)` | 否 | `RuntimeBootID string` |  |
| `fencing_token` | `BIGINT` | 否 | `FencingToken int64` |  |
| `admission_epoch` | `BIGINT` | 否 | `AdmissionEpoch int64` |  |
| `protocol` | `VARCHAR(32)` | 否 | `Protocol string` |  |
| `gateway_route_ref` | `VARCHAR(1024)` | 是 | `GatewayRouteRef sql.NullString` |  |
| `backend_ref` | `VARCHAR(255)` | 是 | `BackendRef sql.NullString` |  |
| `preview_port` | `BIGINT` | 是 | `PreviewPort sql.NullInt64` |  |
| `writer_connection_id` | `VARCHAR(64)` | 是 | `WriterConnectionID sql.NullString` |  |
| `writer_subject_id` | `VARCHAR(64)` | 是 | `WriterSubjectID sql.NullString` |  |
| `writer_epoch` | `BIGINT` | 否 | `WriterEpoch int64` |  |
| `writer_expires_at` | `DATETIME(6)` | 是 | `WriterExpiresAt sql.NullTime` |  |
| `next_input_sequence` | `BIGINT` | 否 | `NextInputSequence int64` |  |
| `terminal_columns` | `BIGINT` | 是 | `TerminalColumns sql.NullInt64` |  |
| `terminal_rows` | `BIGINT` | 是 | `TerminalRows sql.NullInt64` |  |
| `expires_at` | `DATETIME(6)` | 否 | `ExpiresAt time.Time` |  |
| `closed_at` | `DATETIME(6)` | 是 | `ClosedAt sql.NullTime` |  |
| `disconnected_at` | `DATETIME(6)` | 是 | `DisconnectedAt sql.NullTime` |  |
| `request_spec` | `JSON` | 否 | `RequestSpec json.RawMessage` | Envelope data: sandboxv1.CreateTerminalRequest \| CreateBrowserRequest \| CreatePreviewRequest |
| `endpoint` | `JSON` | 是 | `Endpoint json.RawMessage` | Envelope data: contract.Endpoint (private reference DTO) |
| `failure` | `JSON` | 是 | `Failure json.RawMessage` | Envelope data: sandboxv1.Error |
| `created_at` | `DATETIME(6)` | 否 | `CreatedAt time.Time` |  |
| `updated_at` | `DATETIME(6)` | 否 | `UpdatedAt time.Time` |  |
| `version` | `BIGINT` | 否 | `Version int64` |  |

索引：

```sql
PRIMARY KEY (id)
UNIQUE KEY uq_access_session_1 (operation_id)
UNIQUE KEY uq_access_session_3 (writer_connection_id)
KEY ix_access_session_1 (lease_id, status, id)
KEY ix_access_session_2 (tenant_id, project_id, kind, status)
KEY ix_access_session_3 (status, expires_at)
KEY ix_access_session_4 (instance_id, instance_generation, status)
```

## sandbox_access_ticket

短期访问票据摘要、原子消费与撤销。Go 结构：`table.AccessTicketRow`。

| 列 | SQL 类型 | 允许 NULL | Go 字段与类型 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `VARCHAR(64)` | 否 | `ID string` |  |
| `tenant_id` | `VARCHAR(64)` | 否 | `TenantID string` |  |
| `project_id` | `VARCHAR(64)` | 否 | `ProjectID string` |  |
| `session_id` | `VARCHAR(64)` | 否 | `SessionID string` |  |
| `lease_id` | `VARCHAR(64)` | 否 | `LeaseID string` |  |
| `token_hash` | `CHAR(64)` | 否 | `TokenHash string` |  |
| `subject_id` | `VARCHAR(64)` | 否 | `SubjectID string` |  |
| `audience` | `VARCHAR(255)` | 否 | `Audience string` |  |
| `role` | `VARCHAR(32)` | 否 | `Role string` |  |
| `writer_epoch` | `BIGINT` | 否 | `WriterEpoch int64` |  |
| `expires_at` | `DATETIME(6)` | 否 | `ExpiresAt time.Time` |  |
| `consumed_at` | `DATETIME(6)` | 是 | `ConsumedAt sql.NullTime` |  |
| `revoked_at` | `DATETIME(6)` | 是 | `RevokedAt sql.NullTime` |  |
| `connection_id` | `VARCHAR(64)` | 是 | `ConnectionID sql.NullString` |  |
| `created_at` | `DATETIME(6)` | 否 | `CreatedAt time.Time` |  |
| `updated_at` | `DATETIME(6)` | 否 | `UpdatedAt time.Time` |  |
| `version` | `BIGINT` | 否 | `Version int64` |  |

索引：

```sql
PRIMARY KEY (id)
UNIQUE KEY uq_access_ticket_1 (token_hash)
UNIQUE KEY uq_access_ticket_2 (connection_id)
KEY ix_access_ticket_1 (session_id, expires_at)
KEY ix_access_ticket_2 (lease_id, revoked_at)
KEY ix_access_ticket_3 (expires_at)
```

## sandbox_event_stream

资源事件序列分配、保留范围与关闭状态。Go 结构：`table.EventStreamRow`。

| 列 | SQL 类型 | 允许 NULL | Go 字段与类型 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `VARCHAR(64)` | 否 | `ID string` |  |
| `tenant_id` | `VARCHAR(64)` | 否 | `TenantID string` |  |
| `project_id` | `VARCHAR(64)` | 否 | `ProjectID string` |  |
| `entity_kind` | `VARCHAR(32)` | 否 | `EntityKind string` |  |
| `entity_id` | `VARCHAR(64)` | 否 | `EntityID string` |  |
| `next_sequence` | `BIGINT` | 否 | `NextSequence int64` |  |
| `first_available_sequence` | `BIGINT` | 否 | `FirstAvailableSequence int64` |  |
| `closed_at` | `DATETIME(6)` | 是 | `ClosedAt sql.NullTime` |  |
| `archive_ref` | `VARCHAR(1024)` | 是 | `ArchiveRef sql.NullString` |  |
| `created_at` | `DATETIME(6)` | 否 | `CreatedAt time.Time` |  |
| `updated_at` | `DATETIME(6)` | 否 | `UpdatedAt time.Time` |  |
| `version` | `BIGINT` | 否 | `Version int64` |  |

索引：

```sql
PRIMARY KEY (id)
UNIQUE KEY uq_event_stream_1 (tenant_id, entity_kind, entity_id)
KEY ix_event_stream_1 (tenant_id, project_id, entity_kind, updated_at)
KEY ix_event_stream_2 (closed_at)
```

## sandbox_event

生命周期/输出事件、幂等来源键和审计。Go 结构：`table.EventRow`。

| 列 | SQL 类型 | 允许 NULL | Go 字段与类型 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `VARCHAR(64)` | 否 | `ID string` |  |
| `tenant_id` | `VARCHAR(64)` | 否 | `TenantID string` |  |
| `project_id` | `VARCHAR(64)` | 否 | `ProjectID string` |  |
| `stream_id` | `VARCHAR(64)` | 否 | `StreamID string` |  |
| `entity_kind` | `VARCHAR(32)` | 否 | `EntityKind string` |  |
| `entity_id` | `VARCHAR(64)` | 否 | `EntityID string` |  |
| `sequence` | `BIGINT` | 否 | `Sequence int64` |  |
| `type` | `VARCHAR(255)` | 否 | `Type string` |  |
| `entity_version` | `BIGINT` | 是 | `EntityVersion sql.NullInt64` |  |
| `previous_version` | `BIGINT` | 是 | `PreviousVersion sql.NullInt64` |  |
| `instance_generation` | `BIGINT` | 是 | `InstanceGeneration sql.NullInt64` |  |
| `trace_id` | `VARCHAR(64)` | 是 | `TraceID sql.NullString` |  |
| `source_key` | `CHAR(64)` | 是 | `SourceKey sql.NullString` |  |
| `occurred_at` | `DATETIME(6)` | 否 | `OccurredAt time.Time` |  |
| `expires_at` | `DATETIME(6)` | 否 | `ExpiresAt time.Time` |  |
| `payload` | `JSON` | 否 | `Payload json.RawMessage` | Envelope data: sandboxv1.Event for public streams; contract.AuditEvent otherwise |
| `created_at` | `DATETIME(6)` | 否 | `CreatedAt time.Time` |  |

索引：

```sql
PRIMARY KEY (id)
UNIQUE KEY uq_event_1 (stream_id, sequence)
UNIQUE KEY uq_event_2 (stream_id, source_key)
KEY ix_event_1 (tenant_id, entity_kind, entity_id, sequence)
KEY ix_event_2 (expires_at, id)
KEY ix_event_3 (tenant_id, project_id, created_at, id)
```

## sandbox_output_segment

持久日志片段、厂商游标映射与归档引用。Go 结构：`table.OutputSegmentRow`。

| 列 | SQL 类型 | 允许 NULL | Go 字段与类型 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `VARCHAR(64)` | 否 | `ID string` |  |
| `tenant_id` | `VARCHAR(64)` | 否 | `TenantID string` |  |
| `project_id` | `VARCHAR(64)` | 否 | `ProjectID string` |  |
| `execution_id` | `VARCHAR(64)` | 否 | `ExecutionID string` |  |
| `instance_generation` | `BIGINT` | 否 | `InstanceGeneration int64` |  |
| `runtime_boot_id` | `VARCHAR(64)` | 否 | `RuntimeBootID string` |  |
| `source_cursor_hash` | `CHAR(64)` | 否 | `SourceCursorHash string` |  |
| `source_cursor` | `VARCHAR(1024)` | 否 | `SourceCursor string` |  |
| `next_source_cursor` | `VARCHAR(1024)` | 否 | `NextSourceCursor string` |  |
| `cursor_kind` | `VARCHAR(32)` | 否 | `CursorKind string` |  |
| `first_sequence` | `BIGINT` | 否 | `FirstSequence int64` |  |
| `last_sequence` | `BIGINT` | 否 | `LastSequence int64` |  |
| `size_bytes` | `BIGINT` | 否 | `SizeBytes int64` |  |
| `sha256` | `CHAR(64)` | 否 | `SHA256 string` |  |
| `storage_ref` | `VARCHAR(1024)` | 否 | `StorageRef string` |  |
| `completeness` | `VARCHAR(32)` | 否 | `Completeness string` |  |
| `archived_at` | `DATETIME(6)` | 否 | `ArchivedAt time.Time` |  |
| `expires_at` | `DATETIME(6)` | 是 | `ExpiresAt sql.NullTime` |  |
| `archive` | `JSON` | 否 | `Archive json.RawMessage` | Envelope data: sandboxv1.ArchiveRef |
| `created_at` | `DATETIME(6)` | 否 | `CreatedAt time.Time` |  |

索引：

```sql
PRIMARY KEY (id)
UNIQUE KEY uq_output_segment_1 (execution_id, runtime_boot_id, source_cursor_hash)
UNIQUE KEY uq_output_segment_2 (execution_id, first_sequence)
KEY ix_output_segment_1 (execution_id, last_sequence)
KEY ix_output_segment_2 (expires_at)
```

## 动作账本的 JSON 类型

下表列出 `sandbox_action.input/result` 内层 data 的 Go 类型。未加前缀的类型来自 `sandboxv1`；内部类型带 `contract.`。异步操作保存最终资源结果，查询时组装 Operation，不递归保存完整 Operation。Failed 保存 Error，Unknown 不生成成功结果。

当前映射共 28 项，其中公开 OperationKind 为 25 项；其余 3 项是内部产物与凭据动作。

| action | input 类型 | 成功 result 类型 |
| --- | --- | --- |
| `RenewLease` | `RenewLeaseRequest` | `Lease` |
| `ReleaseLease` | `ReleaseLeaseRequest` | `Lease` |
| `RetryRequiredSave` | `contract.StoredSaveRetryRequest` | `RequiredSaveResult` |
| `DiscardBlockedSave` | `contract.StoredDiscardRequest` | `DiscardReceipt` |
| `CancelAcquisition` | `CancelAcquisitionRequest` | `Acquisition` |
| `CancelExecution` | `CancelExecutionRequest` | `Execution` |
| `WriteFile` | `WriteFileRequest` | `FileWriteResult` |
| `WriteStdin` | `WriteStdinRequest` | `InputReceipt` |
| `CreateWorktree` | `CreateWorktreeRequest` | `Worktree` |
| `RemoveWorktree` | `RemoveWorktreeRequest` | `Worktree` |
| `CreateCheckpoint` | `CreateCheckpointRequest` | `Checkpoint` |
| `RestoreCheckpoint` | `RestoreCheckpointRequest` | `RestoreResult` |
| `CreateTerminal` | `CreateTerminalRequest` | `AccessSession` |
| `CreateBrowser` | `CreateBrowserRequest` | `AccessSession` |
| `CreatePreview` | `CreatePreviewRequest` | `AccessSession` |
| `ConnectAccessSession` | `ConnectAccessRequest` | `AccessConnection` |
| `CloseAccessSession` | `CloseAccessSessionRequest` | `AccessSession` |
| `WriteTerminal` | `TerminalInputRequest` | `InputReceipt` |
| `ResizeTerminal` | `TerminalResizeRequest` | `InputReceipt` |
| `SignalTerminal` | `TerminalSignalRequest` | `InputReceipt` |
| `PublishProfile` | `PublishProfileRequest` | `ProfileRevision` |
| `DisableProfile` | `DisableProfileRequest` | `ProfileRevision` |
| `SetPool` | `PutPoolRequest` | `WarmPool` |
| `DisablePool` | `DisablePoolRequest` | `WarmPool` |
| `SetQuota` | `PutQuotaRequest` | `Quota` |
| `PublishArtifact` | `contract.ArtifactSpec` | `contract.ArtifactRef` |
| `IssueCredential` | `contract.CredentialRequest` | `contract.CredentialGrant` |
| `RevokeCredential` | `contract.RevokeCredentialRequest` | `contract.CredentialRevocation` |

StartExecution 的请求幂等事实由 execution 表持有，不再写第二份 action。CLI 逻辑会话、尝试与 execution 的映射由 agentic-kit 持有，不进入沙箱中心动作账本。

动作键包含 tenant、caller、action、target kind/id/revision、requestKey。ProfileRevision 目标使用正 revision，普通目标 revision=0；删除 Worktree 的目标为 worktreeID，创建 Worktree 的目标为 workspaceID。租约身份和工作区世代纳入参数摘要，并在新派发时重新校验。

terminal 输入按连接 ID 与 sequence 识别，不能仅用 sessionID 把新连接序号当成旧连接重放。连接 ID 进入规范化动作目标；SDK 中连接对象提供该身份。输入结果不明保留 Unknown，不能重复喂入字节。

公开 Event payload 使用 sandboxv1.Event；内部审计使用 contract.AuditEvent。存储读取必须按 action、session kind、stream kind 选择确定的 Go 类型；遇到未知版本或类型显式失败。

## 保存阻塞快照、授权与事务

`DrainEvidence.SaveProtection` 为 `contract.SaveProtectionState`，含 `Snapshot: sandboxv1.BlockedSave`、`Intents: []contract.FrozenRequiredSave`、`PublicationEpoch` 和可空 `DiscardOperationID`。`Snapshot.Target` 绑定租户/项目、实例及工作区代次、数据版本、释放操作 ID、正数确认修订和非空完整 required-save 集合。`Intents` 逐项保存原始检查点或产物输入，按稳定 requirement ID 与意图摘要对应；即使尚未受理也必须保留，不允许用空 operation ID 列表放行。派发重试时历史 LeaseProof 仅作原输入记录，由独立保存许可替代，不能拿过期凭据恢复执行。

`BlockedSave.Saves` 对每个 requirement 记录最新 attempt。operation/status 同为 null 表示未受理；Failed 保留错误；Unknown 必须先按原操作键对账。`BeginSaveRetry` 只对缺口或确定失败建立新 action，原记录保持 Failed，`StoredSaveRetryRequest.RetryOfOperationID` 指向前次失败（缺口为 null），通过 Target 的 release ID、requirement ID 和冻结输入保持同一业务意图。最终结果必须证明同一版本与范围已发布，不能把受理重试或放弃视为保存成功。

`Authorizer.AuthorizeSaveRecovery` 返回服务端生成的 `SaveRecoveryAuthorization`，保存在两个新动作的 input 中，并将 `EvidenceRef` 写入 action.evidence_ref。授权记录包含经过验证的 principal、owner、action、完整 Target、策略版本和有效期；Discard 还须有绑定该快照的业务确认引用和确认时间。`ConfirmationRef` 只定位既有业务确认，不能作为凭证；请求 body 的身份/布尔确认或普通 worker token 不成立。事务验证可信证明及有效性，网络授权核验在事务外完成。owner 可处置自己的结果；管理员跨用户使用相同 scope 与目标校验，不增加强制放弃路由或审批表。

两个新动作以 Sandbox 为 target kind，`target_id=Target.Sandbox.ID`、`target_revision=0`；实例/工作区代次、数据版本、required-save 集合和确认修订进入语义摘要，放弃还包括确认引用和原因。保存派生的授权时间、permit 和前次 attempt 不改变同一调用的请求摘要。`OperationLookup` 按原 caller、Kind/TargetID/RequestKey 查询；同键不同请求返回 `IdempotencyConflict`，相同请求先校验当前读取权限再返回旧操作/回执，不再次消费确认。新动作必须通过当前目标 CAS；陈旧快照返回 `RevisionConflict`，代次不符返回 `GenerationMismatch`，无阻塞返回 `InvalidState`。

`BlockedSaveStore` 是既有 OperationStore 的窄事务补充，方法为 `GetBlockedSave`、`BeginSaveRetry`、`CompleteRequiredSave`、`ConfirmDiscard`。查询先通过 Authorizer 的资源读取授权。重试受理、已纳入保护的保存完成和放弃共享行锁顺序及持久 `Snapshot.Target.Revision` CAS，原子更新 action、保存进度、修订与事件。`CompleteRequiredSave` 显式接收 `ExpectedProtectionRevision` 和 `PublicationEpoch`，只允许当前 attempt 提交符合冻结意图的结果，成功后逐 requirement 解除保护。

正常业务阶段尚未被释放事务纳入保护的检查点/产物，仍通过 `OperationStore.Complete` 完成，不要求 release ID 或 protection revision。`Complete` 与 `BeginRelease` 共享资源/action 锁顺序，并在锁内判断是否已纳入：完成先提交，释放就读取已完成事实；释放先纳入，普通完成返回冲突，调用方重读后转受保护入口。发生 CAS 冲突只重新读取并对账已有外部结果，不再次发布。

`ConfirmDiscard` 在同一事务中通过目标 CAS 标记该业务确认已用于本 action，保存 `StoredDiscardRequest`、Succeeded action、`DiscardReceipt`、disposition 引用和审计事件，并推进修订和 publication epoch，拒绝后续保存受理及成功提交。确认不会要求跨系统网络调用加入 SQL 事务。同一目标的并发新键不能重复消费；要求、数据或 attempt/发布状态有变动时旧确认失败。回执列出的 `WaivedRequirementIDs` 由服务端取尚未满足的要求，已发布 checkpoint 和无关产物不在删除范围内。

放弃回执只是解除对应保存阻塞，不是保存或删除成功。`FinishRelease` 在调用 Provider.Delete 前验证回执范围、进程隔离及 `DrainEvidence.PublicationFence: contract.SavePublicationFence`。后者覆盖原 attempt 和全部重试，证明外部存储已拒绝迟到调用，或所有调用均已确定完成且保存访问已撤销；数据库 epoch 或 worker claim 过期本身无效。Unknown 发布先对账或取得权威外部隔离，晚到结果只保留审计/隔离证据，不重开发布提交。相关证据及冻结输入在保护和清理结束前不得清除，资源实际释放前保留占用和配额。
