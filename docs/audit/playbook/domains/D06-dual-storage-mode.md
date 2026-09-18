# D06 — 双重存储架构（全量 vs 本地简化）

> 领域编号: D06 ｜ 最近更新: 2026-09-17 (初版) ｜ 状态: v1

## 1. 领域边界

**管**：全量模式（pg+redis+memory+files）与本地简化模式（sqlite+memory+files）的开关、行为对齐、能力降级边界；lite 模式下被禁用组件的收口。
**不管**：pg 内部分区结构（D07）；缓存 provenance（D03）；统计口径（D10）。

## 2. 参考基线

设计文档：
- `docs/design/lite-mode-storage-design.md` — 双模式存储架构设计（核心）
- `docs/design/lite-mode-index.md`（系列索引）、`lite-mode-architecture-decisions.md`、`lite-mode-deployment-comparison.md`
- `docs/storage/README.md`（双模式总览）、`deployment-guide.md`、`troubleshooting.md`

代码入口：
- `storage/`、`internal/fsstore/`、`internal/redis/`、`internal/dbx/`
- 模式开关：配置装载路径（`config/`、`configs/`）中 lite 相关 env

## 3. 检查清单

1. **开关收口**：full/lite 由统一开关决定；lite 六 env 收口禁 redis（清单以 lite-mode 系列文档为准），窗口内新增功能若依赖 redis，必须在 lite 下有降级路径或显式禁用。
2. **行为对齐**：同一业务操作在两模式下语义一致（成功/失败/幂等）；新增存储调用点若用了 pg 专属能力（partition/RLS/advisory lock/UPSERT 冲突子句），sqlite 侧有等价实现或编译/启动期阻断。
3. **文件存储布局**：lite 目录布局符合 docs/storage/README；文件缓存与附件存储路径不因模式切换而错位。
4. **分区管理器**：lite 下不启动（既有不变量），窗口内不引入 lite 下会误启动的调用。
5. **能力边界诚实**：lite 不支持的功能在 API 层显式返回"不支持"，而非静默空实现。
6. **部署文档同步**：两模式的部署/排障文档与代码开关现状一致（文档说禁的 env 代码真的禁）。

## 4. 历史回归点（轮末回注区）
- [R35 09-17] GLOBAL_G2 不变式与 claim/镜像双门一致性：镜像排除门（isInternalAutoEntry）与 is_final_success claim 谓词必须共享同一判定（现 telemetry.IsInternalAutoEntry 单一事实源）；任何让成功终态携带 IsAutoRequest 的改动都会同时翻转两门输入——只改一侧必破 G2（gt_/gs_ 内部回环行 claim 但不镜像=G2 恒>0，R35-P1b）。post-F1 二进制部署后首个每日观察是验证点

- [R30] full/lite 开关明确、lite 六 env 收口禁 redis、lite 缓存 L1+L1.5 文件级、分区管理器 lite 不启动 —— 健康面基准

## 5. 子代理派发提示词

```text
你是 D06（双重存储架构）只读审计子代理。工作目录：本仓库根。
第一步：Read docs/audit/playbook/conventions.md 和 docs/audit/playbook/domains/D06-dual-storage-mode.md 全文。
第二步：按域文档 §3 检查清单逐条核对，审计窗口：<窗口>；改动文件清单：<该域相关子集>。
重点：窗口内新增的存储调用点是否在 lite 模式下有对等实现或显式禁用。
只读不改。输出按 conventions.md §4 结构，每条发现带 file:line 与触发路径。
```

### R39 回注（2026-09-17，087 ensure 跳过批）
- **"能力不存在而跳过"的裁决必须进程级传导**：ensure 局部 skip ≠ 装配层知道——provider_templates 缺席曾导致路由 500 裸 42P01 + scheduler 每 sweep 告警；R39 落地 db.ProviderTemplatesProvisioned() 信号 + main 装配门（不接线→503 复用既有语义）。
- boot 连接重试必须区分 SQLSTATE：42P01/42703/42883/42809/0A000 = schema mismatch，fast-fail + 可行动日志（db.IsSchemaMismatchError），否则烧光预算后同样落到 disabled。
- 存在性检查用 pg_class.relkind IN ('r','p') 而非 to_regclass（后者对视图/序列也真）。
- ensure 内嵌 DDL 引用特性表前，先想"这张表在哪些部署形态不存在"（installer 全新安装 embed 只有 00/01/02+478 起）。

### R40 回注（2026-09-18，durable 族 RLS GUC + schema 收敛）
- **RLS Phase1#2 durable 族 GUC 补齐**：durable/rls.go 统一通道——前台写（CreateAndClaim）`SET LOCAL app.current_tenant`，worker 17 条路径（claim/terminal/reaper/settlement/pending/决策历史/指标读）`super_admin + bypass_rls` 双 GUC；**autocommit 单语句读写必须包显式事务**——is_local GUC 在 autocommit 下语句结束即回收，等于没设，降权后表现为假性 ErrLeaseLost/ErrNoRows（pgxmock 用例 ×32 同步补 Begin/GUC/Commit 期望）。
- **516 中间形态漂移（新发现，722 收敛）**：本机真库取证——2026-08-15 19:14 以从未入 git 的脏工作区 516 建表（缺 durable_llm_task_events/durable_pending_outbox/checkpoint_payload），516 后以最终形态提交；存量库三无（marker 锁死不可重放、ensure 无 durable 条目、代码直引缺失表/列）→ 首次 durable 事件写入即运行时失败。722 以 516 最终形态幂等体重放收敛（IF NOT EXISTS/DROP POLICY IF EXISTS/ADD COLUMN IF NOT EXISTS），全量注册双通道，本机真库实跑落地。
- 教训：**"迁移文件后补编辑"对已应用库是静默腐蚀**——凡是 ensure 链不覆盖的 schema 面，交付矩阵必须回答"已应用旧版 X 的库怎么拿到新版 X 的对象"；只有文件不可变 + 序号通道新迁移一条路。

### R42 回注（2026-09-18，durable 基表交付矩阵补全）
- **"ensure 链不覆盖的 schema 面"第三条通道**：R40 教训覆盖了 revision-sequence 通道，R42 补上 installer 通道——516/520 已补进五点同步（TestDurableFamilyPrerequisitesRegistered 钉桩），durable 族全新装交付矩阵闭合。
- **URSM v2 共享 Redis 属"observed-state surface"**：与 credential_model_bindings 同级，gateway-side 探测失败写它 = 集群级毒化（见 D09 R42 回注）。
- pending/pg_source.go Get/GetLatest 是 durable 包外唯一无 GUC 读点（Phase 2 前置待办，登记 R42 §五#1）。

### R43 回注（2026-09-18，表 DDL 双所有者裁决 + 五点同步三犯）
- **同表两份 DDL 先查真库定所有者**：task_type_tier_config 在 repo 有 202609_02（TEXT 型，表级表达式 UNIQUE 语法错误、从未可执行）与 deploy/sql/migrations/V370（BIGINT+COALESCE(tenant_id,0)，实表来源）两份分叉——"哪份是真的"以 information_schema 实表为准，不以文件为准。R43 裁决 V370 唯一所有者、202609_02 桩化，缺口由 `db.ensureTaskTypeTierConfig`（V370 形态+补 min_confidence 列）启动自愈。
- **迁移五点同步三犯**（720/721/726）：726 只落文件副本，installer runner/main/embed 与 sequence 通道四点全缺、双契约测试红——作者只跑了 `./sql/migrations/startup/` 包，该包不含 installer/sequence 门禁。**门禁覆盖面声明比门禁本身重要**：交付清单必须逐通道打勾（canonical/embeddata/runner/main embed+map/parity/sequence/deploy V 系列 grep）。
- deploy/ 目录纳入改动面 grep 范围：V 系列迁移不进 revision-sequence，是最易漏的第三通道。
