# Memory 复用 artifact 内容版本，保留本地生效与执行快照

状态：修订提案，2026-10-17；待实施，不改变 ADR-0005 已确认的首版功能范围。

artifact-go 已提供不可变 JSON 版本、发布幂等及保护能力。Memory 委托它保存内容历史，本地只管理条目身份、权限、生效引用、完整召回投影和维护回执；一条记忆对应一个集合，artifact 的 ready/latest 不代表 Memory 生效。维护采用持久意图、事务外发布、本地 CAS 应用，避免两库事务及两套生效指针；未应用的版本不参与召回。操作回执由请求命名空间与请求键的数据库唯一约束保护；主题唯一键包含subjectKey。用户版本历史以成功内容操作为索引，仅展示生效过的版本。

本地投影与生效引用同事务更新，接受 Run 时从该投影冻结完整内容。它为本地事务与恢复保留了必要的数据副本，不再自建全量历史版本库。artifact 故障不阻塞已有投影的召回或已接受 Run 的恢复；删除源版本不等于撤销记忆，停用由 Memory 明确处理。首版版本受保护且不启用 retention。停用保留Entry身份；通过显式restore的完整更新发布新版本并以CAS重新启用，不增加恢复工作流。

接入须先完成artifact三元数据隔离与集合幂等创建：tenant_id、精确project_id、user_id同时参与访问、名称唯一和幂等约束。复用现有created_by保存固定用户归属，Memory适配传入已核验的ArtifactBinding；共享条目由Memory授权，后续编辑者变化不改变远端绑定。账号和凭据治理不属于本次设计。当前tenant/project过滤尚不等于已实现用户隔离，SDK正式发布及依赖兼容仍须验证。

待补能力见[Memory设计§2.4](../memory-design.md#24-artifact-go-待补的通用能力)及[artifact独立设计](../../../artifact-go/docs/service-consumer-design-20261016.md)。Memory历史只展示生效版本，artifact保留完整存档历史；artifact直接管理操作不自动生效或撤销Memory。本提案不迁移Agent资产版本或执行checkpoint。
