# 六张 `public` 表设计说明：来源核查与关系边界

- **核查日期**：2026-08-31
- **范围**：`agent_discovery`、`agent_gateways`、`agent_migration_log`、`orchestration_sessions`、`industry_registry`、`gateway_run_bindings`
- **文档性质**：设计说明与审计记录，不是迁移文件，也不宣称这些表当前应由本仓库创建。
- **安全边界**：本文不记录数据库密码、连接串、密钥或任何凭证值。

## 证据等级（建议阅读时区分以下层级）

- **R 仓库事实**：仓库内当前可复现的 Go、SQL、canonical object；例如 `agents` / `agent_relationships` / `gateway_instances`。
- **H 历史审计记录**：2026-08-31 一次运行的 7 维结构摘要（`docs/audit/2026-08-31-db-structure-consistency-audit.md`）；代表当时数据库采样，不是当前实时证据。
- **S 未复核历史快照**：本文件下方逐列字段、主键、索引与约束。源自前期未跟踪草稿；目标库 `llm-gateway-pg` 在本次审计时不可访问，未做 `pg_dump` 校核，因此 **不能等同于活库证据**。
- **B 业务假设**：依据表名、字段名推测的用途与生命周期；尚未与来源代码或文档对齐。

下文若出现“无 FK / 0 行 / 与 `gateway_instances` 等同”等描述，仅指 **S 与 H 层级**，而不是对当前数据库的实时断言。

## 结论先行

这六张表从命名和字段组合看，属于三类可能相关的 feature schema：

1. **Agent 发现与网关注册**：`agent_discovery`、`agent_gateways`；
2. **Agent 网关迁移**：`agent_migration_log`；
3. **多 Agent 编排及运行绑定**：`orchestration_sessions`、`industry_registry`、`gateway_run_bindings`。

它们的关系目前只能定义为**业务语义上的候选关系**：保留的结构快照显示，六表之间没有数据库 FK，且未见六表与其他 `public` 表的 FK。因此删除、改名或迁移任一表时，数据库不会替业务引用做完整性保护。

**本次核查状态（2026-08-31）**：当前 Docker daemon 中不存在名为 `llm-gateway-pg` 的容器（`docker ps -a --filter name=llm-gateway-pg` 无结果），故不能对用户指定的本地 PostgreSQL 实例重新取证。本页逐列 DDL 是已存在草稿携带的**待复核结构快照**，而不是本次可重现的活库证据；下文所有“无 FK/约束/触发器”和行数结论均限于该快照。恢复目标容器后，必须按第 6 节只读命令重新反查，再把快照提升为已验证设计。

## 1. 来源与核查方法

### 1.1 仓库检索结果

对 Go、SQL、配置和文档做了全仓检索：

- 六张表没有 Go struct、GORM model、`AutoMigrate`、`CREATE TABLE` 迁移或运行时建表逻辑；
- `agent_discovery` 在 `cmd/gateway/main_pipeline.go`、`cmd/gateway/main_v2_pipeline.go` 和 `cmd/gateway-v2/main.go` 中出现时，是 **pipeline stage 名称**，不是数据库表访问；
- `domains/agent-ecosystem/types.go` 的 `Registry` 使用进程内 `map[string]*Agent`，`hook.go` 通过内存 registry 查找 capability，不读写 `agent_discovery`；
- `scripts/local-dev/verify-db-consistency.sh` 与 `.agents/skills/db-sync-252-local/SKILL.md` 把这些表列入 reconcile/一致性检查语境；SKILL.md 进一步声称它们“通常由运行时 AutoMigrate 创建”，但本次 checkout（含隐藏/忽略文件、所有可达 ref 及不可达 commit）没有对应 AutoMigrate、Go struct、读写 SQL 或 DDL。因此该描述仅是未证实的运维假设，不能证明当前应用实现了读写方；
- 仓库确实定义了另一套活跃 agent schema：`agents`、`agent_relationships`（如 `sql/migrations/startup/050_agents.sql`、`051_agent_relationships.sql`），但不是本页六张表。前者是带 `tenant_id`、RLS、CHECK 和 JSONB 能力索引的 `bigint` 主键模型；后者以 FK 连接前者，数据模型与本页以字符串标识为主的快照不兼容；
- `gateway_instances` 是另一个有明确运行时 DDL 与读写方的网关登记表（`db/db.go`、`center/store_pgx.go`、`sql/migrations/startup/up/377_center_ops.sql`）；它以 `instance_id` 为主键，拥有状态 CHECK 和多项运维索引。其存在不能推导 `agent_gateways` 已废弃或与其同步，只能证明二者不是同一受本仓管理的 schema。

### 1.2 Docker/PostgreSQL 核查结果

本次只执行只读检查，且没有尝试启动、创建、重建或修改任何容器、卷或数据库对象：

- 目标命令所指的 `llm-gateway-pg` 容器不存在，无法执行目标库的 `docker exec`、`psql` 或 `pg_dump --schema-only` 反查；
- Docker 本地可见的卷和镜像不能证明其中任何一个承载了本任务指定的 `llm-gateway-pg` 数据库；因此没有将它们挂载到新容器或尝试离线读取，以避免越过任务约束或改变运行状态；
- 本次不连接 252，也不使用、输出或记录密码、连接串或密钥；
- 因目标关系不可访问，当前无法取得这六表的实时列、索引、约束、入/出 FK、触发器、注释、依赖或行数证据；
- 下文标为“既有快照”的结构来自本文件原有的未跟踪草稿，不能替代目标库恢复后的重新取证。

## 2. 既有结构快照总览（待目标库复核）

| 表 | 列数 | 主键 | 其他索引 | 快照中的 FK（入/出） | 快照中的 CHECK/触发器 | 快照行数 |
|---|---:|---|---|---|---|---:|
| `agent_discovery` | 12 | `discovery_id` | `idx_agent_discovery_agent_uidx(agent_id)`，唯一 | 无 / 无 | 无 / 无 | 0 |
| `agent_gateways` | 12 | `gateway_id` | 无 | 无 / 无 | 无 / 无 | 0 |
| `agent_migration_log` | 9 | `migration_id` | 无 | 无 / 无 | 无 / 无 | 0 |
| `orchestration_sessions` | 15 | `session_id` | `idx_orch_sessions_created`、`idx_orch_sessions_root_task`（部分）、`idx_orch_sessions_state` | 无 / 无 | 无 / 无 | 0 |
| `industry_registry` | 12 | `industry_id` | 无 | 无 / 无 | 无 / 无 | 0 |
| `gateway_run_bindings` | 12 | `binding_id`（UUID） | 无 | 无 / 无 | 无 / 无 | 0 |

> “无 FK”包括无入边和无出边；“无 CHECK/触发器”仅是既有快照结论。目标库当前不可访问时，不应据此判断目标环境的实时状态。

## 3. 表设计说明

### 3.1 `agent_discovery`

**用途假设**：Agent 的发现登记/在线状态表。它看起来把一个 Agent 关联到一个网关，并保存能力、心跳、负载和扩展元数据。`agent_id` 的唯一索引暗示设计上一个 Agent 只允许一条发现记录；这与字段中存在 `gateway_id` 的多网关语义有潜在冲突，需由来源实现确认。

**字段快照**：

| 列 | 类型 | 可空 | 默认值 | 设计含义 |
|---|---|---|---|---|
| `discovery_id` | `varchar(255)` | 否，PK | — | 发现记录标识 |
| `agent_id` | `varchar(255)` | 否 | — | Agent 业务标识；有唯一索引 |
| `gateway_id` | `varchar(255)` | 否 | — | 所属/发现网关业务标识 |
| `capabilities` | `jsonb` | 是 | `'[]'` | 能力列表 |
| `last_seen` | `timestamptz` | 是 | `now()` | 最近观测时间 |
| `metadata` | `jsonb` | 是 | `'{}'` | 扩展元数据 |
| `created_at` | `timestamptz` | 是 | `now()` | 创建时间 |
| `updated_at` | `timestamptz` | 是 | `now()` | 更新时间 |
| `load` | `double precision` | 是 | `0` | 负载值 |
| `status` | `varchar(32)` | 是 | `'online'` | 在线状态 |
| `last_heartbeat` | `timestamptz` | 是 | — | 最近心跳时间 |
| `discovery_enabled` | `boolean` | 是 | `true` | 是否参与发现 |

**结构边界**：快照中的主键为 `agent_discovery_pkey(discovery_id)`，唯一索引为 `idx_agent_discovery_agent_uidx(agent_id)`；无 FK、CHECK、触发器。

**仓库证据**：pipeline 中的同名 stage 不访问本表；当前仓库没有本表读写 SQL。仓库中的 `Agent`/`Registry` 是内存模型，不能作为本表字段的实现来源。

### 3.2 `agent_gateways`

**用途假设**：网关注册、心跳和能力状态表。字段组合覆盖网关类型、主机地址、端口、在线状态、能力数量和扩展元数据。

**字段快照**：

| 列 | 类型 | 可空 | 默认值 | 设计含义 |
|---|---|---|---|---|
| `gateway_id` | `varchar(255)` | 否，PK | — | 网关业务标识 |
| `gateway_type` | `varchar(32)` | 否 | `'openclaw'` | 网关类型 |
| `host_id` | `varchar(255)` | 否 | `''` | 主机标识 |
| `host_ip` | `varchar(64)` | 否 | `''` | 主机地址 |
| `port` | `integer` | 是 | — | 网关端口 |
| `status` | `varchar(32)` | 是 | `'offline'` | 网关状态 |
| `capabilities` | `jsonb` | 是 | `'[]'` | 网关能力列表 |
| `agent_count` | `integer` | 是 | `0` | 关联/承载 Agent 数量 |
| `last_heartbeat` | `timestamptz` | 是 | — | 最近心跳时间 |
| `metadata` | `jsonb` | 是 | `'{}'` | 扩展元数据 |
| `created_at` | `timestamptz` | 是 | `now()` | 创建时间 |
| `updated_at` | `timestamptz` | 是 | `now()` | 更新时间 |

**结构边界**：快照中的主键为 `agent_gateways_pkey(gateway_id)`；除主键外无索引；无 FK、CHECK、触发器。

**关系假设**：`agent_discovery.gateway_id`、`agent_migration_log.source_gateway_id`、`agent_migration_log.target_gateway_id` 和 `gateway_run_bindings.gateway_id` 都可能业务上指向本表，但数据库未声明这种关系。

### 3.3 `agent_migration_log`

**用途假设**：记录 Agent 在网关之间迁移的过程或结果。`source_gateway_id` 与 `target_gateway_id` 表示迁移方向，`detail` 保存过程扩展信息，`completed_at` 表示完成时间。

**字段快照**：

| 列 | 类型 | 可空 | 默认值 | 设计含义 |
|---|---|---|---|---|
| `migration_id` | `varchar(255)` | 否，PK | — | 迁移记录标识 |
| `agent_id` | `varchar(255)` | 否 | — | 被迁移 Agent 标识 |
| `source_gateway_id` | `varchar(255)` | 否 | — | 源网关标识 |
| `target_gateway_id` | `varchar(255)` | 否 | — | 目标网关标识 |
| `status` | `varchar(32)` | 否 | — | 迁移状态 |
| `detail` | `jsonb` | 是 | `'{}'` | 迁移详情 |
| `created_at` | `timestamptz` | 是 | `now()` | 创建时间 |
| `completed_at` | `timestamptz` | 是 | — | 完成时间 |
| `migration_type` | `varchar(64)` | 是 | `'manual'` | 迁移类型 |

**结构边界**：快照中的主键为 `agent_migration_log_pkey(migration_id)`；无二级索引、FK、CHECK、触发器。三个 Agent/网关标识列均未声明外键。

**关系假设**：`agent_id` 可能指向 `agent_discovery.agent_id`，两个网关列可能指向 `agent_gateways.gateway_id`；这些只是业务解释，不是数据库保证。

### 3.4 `orchestration_sessions`

**用途假设**：保存一次多 Agent 编排会话的意图、任务类型、流程执行位置、状态机状态及编排账目。`ledger` 和 `staffing` 使用 JSONB，表明会话状态/人员配置的形态可能由编排器动态扩展。

**字段快照**：

| 列 | 类型 | 可空 | 默认值 | 设计含义 |
|---|---|---|---|---|
| `session_id` | `varchar(255)` | 否，PK | — | 编排会话标识 |
| `intent_text` | `text` | 否 | — | 用户/系统意图 |
| `task_type` | `varchar(64)` | 是 | — | 原始任务类型 |
| `canonical_type` | `varchar(64)` | 是 | — | 规范化任务类型 |
| `root_task_id` | `varchar(255)` | 是 | — | 根任务标识 |
| `current_flow_definition_id` | `varchar(255)` | 是 | — | 当前流程定义 |
| `current_flow_execution_id` | `varchar(255)` | 是 | — | 当前流程执行 |
| `state` | `varchar(32)` | 否 | `'planning'` | 编排状态机状态 |
| `ledger` | `jsonb` | 否 | `'[]'` | 编排账目/事件状态 |
| `staffing` | `jsonb` | 否 | `'[]'` | Agent/人员配置 |
| `stall_count` | `integer` | 否 | `0` | 停滞次数 |
| `replanned` | `boolean` | 否 | `false` | 是否发生重规划 |
| `created_by` | `varchar(255)` | 是 | — | 创建者标识 |
| `created_at` | `timestamptz` | 否 | `now()` | 创建时间 |
| `updated_at` | `timestamptz` | 否 | `now()` | 更新时间 |

**结构边界**：快照中的主键为 `orchestration_sessions_pkey(session_id)`；

- `idx_orch_sessions_created(created_at DESC)`；
- `idx_orch_sessions_root_task(root_task_id) WHERE root_task_id IS NOT NULL`（部分索引）；
- `idx_orch_sessions_state(state)`。

快照中无 FK、CHECK、触发器。表没有 `industry_id` 列，因此与 `industry_registry` 的关联不能从列级结构确认。

### 3.5 `industry_registry`

**用途假设**：按行业登记多 Agent 编排所需的领域信号、工作流、技能覆盖、角色和参与方规则，并保存共识/超时/轮次参数。它更像配置目录或注册表，而不是会话事实表。

**字段快照**：

| 列 | 类型 | 可空 | 默认值 | 设计含义 |
|---|---|---|---|---|
| `industry_id` | `text` | 否，PK | — | 行业业务标识 |
| `display_name` | `text` | 否 | — | 展示名称 |
| `domain_signals` | `jsonb` | 是 | `'{}'` | 领域识别信号 |
| `workflow_steps` | `jsonb` | 是 | `'[]'` | 工作流步骤 |
| `skill_overrides` | `jsonb` | 是 | `'{}'` | 技能覆盖配置 |
| `casdoor_roles` | `jsonb` | 是 | `'{}'` | Casdoor 角色配置 |
| `participant_rules` | `jsonb` | 是 | `'{}'` | 参与方规则 |
| `consensus_threshold` | `double precision` | 是 | `0.67` | 共识阈值 |
| `speak_timeout` | `integer` | 是 | `60000` | 发言超时，按命名推测为毫秒 |
| `max_rounds` | `integer` | 是 | `10` | 最大轮次 |
| `created_at` | `timestamptz` | 是 | `now()` | 创建时间 |
| `updated_at` | `timestamptz` | 是 | `now()` | 更新时间 |

**结构边界**：快照中的主键为 `industry_registry_pkey(industry_id)`；无二级索引、FK、CHECK、触发器。没有任何列直接关联 `orchestration_sessions`。

### 3.6 `gateway_run_bindings`

**用途假设**：把一次执行/步骤绑定到网关、员工和会话上下文。`run_key` 可能用于幂等或运行寻址，`status` 表示绑定生命周期。

**字段快照**：

| 列 | 类型 | 可空 | 默认值 | 设计含义 |
|---|---|---|---|---|
| `binding_id` | `uuid` | 否，PK | — | 绑定记录标识 |
| `execution_id` | `text` | 是 | — | 执行标识 |
| `step_id` | `text` | 是 | — | 步骤标识 |
| `workcell_id` | `text` | 是 | — | 工作单元标识 |
| `task_id` | `text` | 否 | — | 任务标识 |
| `run_key` | `text` | 是 | — | 运行/幂等键 |
| `employee_id` | `text` | 是 | — | 员工/执行者标识 |
| `gateway_id` | `text` | 是 | — | 网关业务标识 |
| `session_key` | `text` | 是 | — | 会话业务键 |
| `status` | `text` | 否 | `'pending'` | 绑定状态 |
| `created_at` | `timestamptz` | 否 | `now()` | 创建时间 |
| `updated_at` | `timestamptz` | 否 | `now()` | 更新时间 |

**结构边界**：快照中的主键为 `gateway_run_bindings_pkey(binding_id)`；无二级索引、FK、CHECK、触发器。`gateway_id` 为 `text`，与 `agent_gateways.gateway_id varchar(255)` 也不构成类型一致的数据库关系；`session_key` 不是 `orchestration_sessions.session_id`。

## 4. 关系设计与引用边界

### 4.1 数据库实际关系（按既有快照）

| 关系 | FK 状态 | 说明 |
|---|---|---|
| `agent_discovery` ↔ `agent_gateways` | 无 | 通过 `gateway_id` 形成候选业务关系 |
| `agent_migration_log` → Agent/网关 | 无 | `agent_id`、源/目标网关列均无 FK |
| `orchestration_sessions` ↔ `industry_registry` | 无 | 会话甚至没有 `industry_id` 列，最多是应用层配置选择 |
| `gateway_run_bindings` → 网关/编排会话 | 无 | `gateway_id` 类型不同；`session_key` 也不是会话 PK |
| 六表 ↔ 其他 public 表 | 无 | 既有快照未发现入边或出边 |

因此，以上箭头只表达推荐的阅读方式或命名暗示，不表达数据库约束。应用层如需保留这些关系，必须在来源实现中确认标识生成、删除策略和并发/幂等规则。

### 4.2 与仓库现存 schema 的区分

- `agents` 与 `agent_relationships` 由仓库迁移定义，前者使用 `bigint id`，并带租户、类型、状态、能力和 RLS；后者通过 FK 关联 `agents`。它们与 `agent_discovery` 的 Agent 语义重叠，但不是同一张表。
- 仓库 Go 代码实际读写的是 `gateway_instances`（如 `center/store_pgx.go`、`admin/ops_overview.go`、`center/runtime_metrics.go`）；这与 `agent_gateways` 都有网关注册/心跳语义，但没有代码证据表明二者互为替代或同步。
- `domains/agent-ecosystem` 的内存 registry 不能证明 `agents` 或 `agent_discovery` 已被运行时接入。

### 4.3 候选标识关系与实现缺口

下图故意用虚线表达**命名和字段形态推导**，不是已验证 FK 或运行时调用链：

```text
agent_discovery.agent_id ┄┄┄▶ agent_migration_log.agent_id
agent_discovery.gateway_id ┄┄┄▶ agent_gateways.gateway_id ◀┄┄┄ agent_migration_log.{source_gateway_id,target_gateway_id}
                                                     ▲
                                                     └┄┄┄ gateway_run_bindings.gateway_id (text vs varchar(255))

orchestration_sessions.session_id   [没有对应列可与 industry_registry.industry_id 直接连接]
gateway_run_bindings.session_key    [名称与 orchestration_sessions.session_id 不一致]
```

- 唯一可由快照索引形式确认的候选被引用键是 `agent_discovery.agent_id`；但它不能证明 `agent_migration_log.agent_id` 引用它，因为没有 FK；
- `agent_gateways.gateway_id` 是主键，四个同名或近似命名列可在业务层引用它；`gateway_run_bindings.gateway_id` 与其类型不同，故未来若要加 FK，须先确认值域、长度与迁移兼容性；
- `orchestration_sessions` 无 `industry_id`，`gateway_run_bindings` 无 `session_id`。不应根据名称补造关系；须先在来源代码或接口契约确认映射键、生命周期与删除语义；
- 快照未显示六表自身的触发器或其他表对其的 FK 入边。目标库复核时仍必须使用 catalog 双向查询确认，不能只查询本表 `conrelid`。

## 5. 来源假设与待确认事项

### 5.1 当前最合理的来源假设

项目审计文档与 `.agents/skills/db-sync-252-local/SKILL.md` 将这六张表描述为本地领先于 252 的 feature tables，推测它们可能由某个较新版本/特性分支的运行时建表逻辑产生，并曾通过 reconcile 回灌到另一端。这个解释与“没有仓库迁移、没有当前 Go 读写方、表之间没有 FK”相符，但**不是本仓 checkout 中可验证的来源证明**。

### 5.2 必须找到的来源证据

1. git 全分支、构建产物或部署镜像中是否存在六表的 `CREATE TABLE`/AutoMigrate；
2. 是否有实际读写这些表的服务、任务或后台 worker；
3. `agent_gateways`/`agent_discovery` 是否是 `gateway_instances`/`agents` 的新旧替代 schema；
4. 编排实现是否真正使用 `orchestration_sessions`/`industry_registry`/`gateway_run_bindings`；
5. 生产/252 与 local 是否都存在这些表，以及其 DDL 是否仍一致。

在上述证据出现前，不建议直接 DROP，也不建议仅凭字段名称新增 FK、唯一约束或索引。

## 6. 审计局限与复核清单

### 6.1 恢复 `llm-gateway-pg` 后的只读复核命令

以下命令只读取 PostgreSQL 系统目录；不含密码、不连接 252、不执行 DDL/DML。以 `PGOPTIONS='-c default_transaction_read_only=on'` 防止会话内意外写入。`target` 的唯一合法来源应是已恢复的本地 `llm-gateway-pg` 容器：

```bash
read -r -d '' tables <<'SQL' || true
'agent_discovery','agent_gateways','agent_migration_log',
'orchestration_sessions','industry_registry','gateway_run_bindings'
SQL

docker exec -i -e PGOPTIONS='-c default_transaction_read_only=on' llm-gateway-pg \
  psql -X -U llm_gateway -d llm_gateway -v ON_ERROR_STOP=1 -P pager=off <<SQL
-- 列、默认值、表类型和注释。
SELECT c.relname AS table_name, c.relkind, a.attnum, a.attname AS column_name,
       pg_catalog.format_type(a.atttypid, a.atttypmod) AS data_type,
       NOT a.attnotnull AS is_nullable, pg_get_expr(ad.adbin, ad.adrelid) AS column_default,
       col_description(a.attrelid, a.attnum) AS column_comment
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_attribute a ON a.attrelid = c.oid
LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
WHERE n.nspname = 'public' AND c.relname IN ($tables)
  AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY c.relname, a.attnum;

-- 主键、UNIQUE、CHECK 以及双向 FK；conrelid 是出边，confrelid 是入边。
SELECT con.conname, con.contype, con.conrelid::regclass AS source_table,
       con.confrelid::regclass AS referenced_table, pg_get_constraintdef(con.oid, true) AS definition
FROM pg_constraint con
WHERE con.conrelid::regclass::text IN ($tables)
   OR con.confrelid::regclass::text IN ($tables)
ORDER BY source_table::text, con.conname;

-- 索引（含主键隐含索引和部分索引谓词）。
SELECT tablename AS table_name, indexname, indexdef
FROM pg_indexes
WHERE schemaname = 'public' AND tablename IN ($tables)
ORDER BY tablename, indexname;

-- 该表自身的非内部触发器，以及触发器函数定义位置。
SELECT tg.tgrelid::regclass AS table_name, tg.tgname,
       pg_get_triggerdef(tg.oid, true) AS definition,
       p.oid::regprocedure AS function_name
FROM pg_trigger tg
JOIN pg_proc p ON p.oid = tg.tgfoid
WHERE NOT tg.tgisinternal AND tg.tgrelid::regclass::text IN ($tables)
ORDER BY table_name::text, tg.tgname;

-- 依赖 catalog：显示依赖六表的对象和六表依赖的对象（需结合对象类型人工判读）。
-- pg_describe_object 可安全描述 relation、constraint、trigger 等不同 catalog 中的对象。
SELECT pg_describe_object(d.classid, d.objid, d.objsubid) AS dependent_object,
       pg_describe_object(d.refclassid, d.refobjid, d.refobjsubid) AS referenced_object,
       d.deptype
FROM pg_depend d
WHERE d.objid IN (SELECT c.oid FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
                  WHERE n.nspname='public' AND c.relname IN ($tables))
   OR d.refobjid IN (SELECT c.oid FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
                     WHERE n.nspname='public' AND c.relname IN ($tables))
ORDER BY 1, 2, 3;
SQL
```

执行后还应保存以下无数据的表级 DDL，供与本页快照逐项比对：

```bash
for table in agent_discovery agent_gateways agent_migration_log orchestration_sessions industry_registry gateway_run_bindings; do
  docker exec -e PGOPTIONS='-c default_transaction_read_only=on' llm-gateway-pg \
    pg_dump -U llm_gateway -d llm_gateway --schema-only --no-owner --no-privileges -t "public.${table}"
done

# 若需要行数，逐表执行；精确 count(*) 对大表可能昂贵，且会跳过不存在的关系。
for table in agent_discovery agent_gateways agent_migration_log orchestration_sessions industry_registry gateway_run_bindings; do
  docker exec -e PGOPTIONS='-c default_transaction_read_only=on' llm-gateway-pg \
    psql -X -U llm_gateway -d llm_gateway -v ON_ERROR_STOP=1 -P pager=off \
    -c "SELECT '${table}' AS table_name, count(*) AS row_count FROM public.\"${table}\";"
done
```

> 上述 `pg_dump` 输出可能含对象名与权限相关元信息，应仅存入受控审计工件；不得把凭证参数写入 shell 历史、文档或提交。

### 6.2 本次审计的局限

本次审计的局限如下：

- 目标容器 `llm-gateway-pg` 不存在，无法对用户所指的本地 Docker 库做实时反查；本次没有把其他容器、卷或宿主 PostgreSQL 当作替代证据；
- 因而本页的列、默认值、索引名、约束/触发器和 0 行结论来自既有未跟踪设计草稿的历史快照，待目标库恢复后复核；
- 全仓检索覆盖普通、隐藏和忽略文件，以及所有可达 Git ref 和不可达 commit；它能证明当前仓库对象没有文本定义/访问，但不能排除未导入的分支、已部署二进制、外部服务、动态 SQL 或数据库管理脚本；
- “用途”“关系”“时间单位”（例如 `speak_timeout` 是否为毫秒）均含来源假设，不能由 PostgreSQL 类型本身证明；
- 空表只能说明快照采样时没有行，不能证明功能已废弃；缺少 FK 也不能证明没有应用层关系；
- 未连接 252 远端数据库，未比较两端当前 DDL；未执行同步、DDL、DML、容器/卷生命周期操作或代码修改。

目标库可用后，最小复核应覆盖：

1. `information_schema.columns`：列序、类型、可空性、默认值；
2. `pg_indexes`/`pg_index`：主键、唯一性、部分索引谓词；
3. `pg_constraint`：PK、UNIQUE、CHECK、FK 及动作；
4. `pg_trigger`：用户触发器；
5. `pg_depend` 与 `pg_constraint`：六表之间及与全库的双向引用；
6. `pg_class.reltuples` 或安全的精确计数：行数与表类型；
7. `obj_description`/`col_description`：表和列注释；
8. `pg_dump --schema-only`：保存脱敏后的可复核 DDL 指纹。

以上均应使用只读连接；重新取证后，只更新本设计文档的快照状态和差异，不在本任务中修改代码或迁移。
