# 统计数据标准化框架 · Statistics Standardization Framework

> **目标**: 全局统一所有统计术语、数据源、计算口径与更新策略
>
> **Owner**: 2026-11-20 Goal Session
>
> **Status**: ✅ 基于 24 小时审计工作建立（2026-11-17 ~ 2026-11-20）

---

## 一、核心原则（Four Principles）

1. **单一真源（Single Source of Truth）**: 每个指标只有一个权威数据源
2. **口径一致性（Caliber Consistency）**: 同一术语在所有页面计算口径相同（条件可变）
3. **单位纯净性（Unit Purity）**: 禁止跨单位求和（USD + credits ⇒ 分开展示）
4. **更新策略显式化（Explicit Update Strategy）**: 每个数据点的更新方式必须文档化

---

## 二、数据源架构（Data Source Architecture）

### 2.1 主要事实表（Primary Fact Tables）

| 表名 | 粒度 | 数据延迟 | 用途 | 保留期 |
|------|------|----------|------|--------|
| **`usage_facts`** | 请求级 | 实时写入 | **权威真源**，所有统计的基础 | 按策略轮转 |
| **`session_turns`** | 轮次级 | 实时写入 | 会话分析、对话流 | 按策略轮转 |
| **`request_logs`** | 请求级 | 实时写入（遗留） | 旧日志面，逐步退役 | 已停写 |
| **`daily_grain`** | 日粒度 + 维度 | T+1 聚合 | 对账模块读面 | 长期保留 |
| **`report_snapshots`** | 可配置粒度 | 快照时刻 | 历史快照、趋势分析 | 长期保留 |

### 2.2 物化视图（Materialized Views）

| 视图名 | 刷新策略 | 数据源 | 用途 | 陈旧阈值 |
|--------|----------|--------|------|----------|
| **`routing_mv_*`** | 定时刷新（分钟级） | `usage_facts` | 会话分析系统 | 90 分钟 |
| **`board_metrics_cache`** | 定时 + 写触发 | `usage_facts` | Dashboard 看板 | 15 分钟 |
| **`credential_monitor_rollup`** | 滑动窗口 | `usage_facts` | 凭据监控 | 实时 |

### 2.3 缓存层（Cache Layers）

| 缓存 | TTL | 数据源 | 失效策略 | 用途 |
|------|-----|--------|----------|------|
| Redis Analytics Cache | 5 min | 物化视图 | TTL 过期 | 会话分析前端 |
| Board Cache | 15 min | `board_metrics_cache` | 写触发 + TTL | Dashboard 顶部卡片 |
| Dimension Options | 1 hour | `daily_grain` | TTL | 对账模块筛选下拉 |

---

## 三、标准术语表（Standard Terminology）

### 3.1 核心指标（Core Metrics）

| 术语 | 英文 | 计算公式 | 数据源 | 单位 | 说明 |
|------|------|----------|--------|------|------|
| **请求数** | request_count | COUNT(*) | `usage_facts` | 次 | 所有请求，含成功和失败 |
| **成功数** | success_count | COUNT(*) WHERE success=true | `usage_facts` | 次 | HTTP 200 且模型返回完整 |
| **失败数** | error_count | request_count - success_count | 派生 | 次 | 所有非成功请求 |
| **成功率** | success_rate | success_count / request_count | 派生 | % | 0~100% |
| **输入 Token** | input_tokens | SUM(input_token_count) | `usage_facts` | 个 | 用户输入 + 系统提示 |
| **输出 Token** | output_tokens | SUM(output_token_count) | `usage_facts` | 个 | 模型生成内容 |
| **缓存读 Token** | cache_read_tokens | SUM(cache_read_token_count) | `usage_facts` | 个 | 命中缓存的 Token |
| **缓存写 Token** | cache_write_tokens | SUM(cache_creation_token_count) | `usage_facts` | 个 | 写入缓存的 Token |
| **总 Token** | total_tokens | input + output + cache_read + cache_write | 派生 | 个 | 四类 Token 总和 |

### 3.2 成本指标（Cost Metrics）

| 术语 | 英文 | 计算公式 | 单位 | **关键问题** |
|------|------|----------|------|--------------|
| **内部成本** | internal_cost | SUM(internal_cost_cents) / 100 | **USD** | ✅ 单位纯净 |
| **外部成本** | external_cost | SUM(external_cost_credits) | **积分/credits** | ✅ 单位纯净 |
| ~~总成本~~ | ~~total_cost~~ | ~~internal + external~~ | ❌ **跨单位** | ⚠️ **禁止展示**（见 §3.2.1） |

#### 3.2.1 ⚠️ **跨单位求和问题**（Critical Issue）

**问题**: `total_cost = internal_cost(美元) + external_cost(积分)` 在多处代码中存在

**影响范围**:
- 对账模块: `daily_grain.cost_amount` 列存的是混合值
- 会话分析排行页: `total_cost` 列展示混合值
- Dashboard 成本卡: 显示总计但单位混乱

**解决方案**:
1. ✅ **展示层**: 总览页分成两卡「内部成本(USD)」「外部成本(积分)」
2. ✅ **排行表**: 保留 `total_cost` 列（排序键），页面顶部注明「只可相对比较，不代表实际金额」
3. ⚠️ **遗留**: `daily_grain.cost_amount` 列仍存混合值（需 schema 变更，已登记 L1）

**已修复**:
- Session Analytics Overview 页: 2026-11-20 实施，分两卡展示
- Session Analytics Ranking 页: 保留列 + 说明文案

**待修复**:
- Reconciliation 模块: `cost_amount` 列需拆分（见 docs/audit/2026-11-17-对账模块统计口径复审-主档.md § F4）

### 3.3 时间窗口（Time Windows）

| 术语 | 英文 | 定义 | 数据源 | **标准** |
|------|------|------|--------|----------|
| **UTC 日** | UTC day | 00:00~23:59 UTC | `usage_facts.created_at` | ✅ **权威口径** |
| **本地日** | Local day | 用户时区的日 | 前端聚合 | ⚠️ 仅前端趋势图 |
| **今日** | Today | 当前 UTC 日 | `usage_facts WHERE DATE(created_at AT TIME ZONE 'UTC') = CURRENT_DATE` | ✅ 标准 |
| **近 5 分钟** | Last 5 minutes | 滑动窗口 | `WHERE created_at >= NOW() - INTERVAL '5 minutes'` | 实时窗口 |
| **近 7 天** | Last 7 days | 最近 7 个完整 UTC 日 | `WHERE created_at >= CURRENT_DATE - 6` | ✅ 标准 |
| **近 30 天** | Last 30 days | 最近 30 个完整 UTC 日 | `WHERE created_at >= CURRENT_DATE - 29` | ✅ 标准 |

**关键决策**: 所有后端统计**统一使用 UTC 日**，前端可选择性聚合成本地日（仅趋势图）

**已对齐**: Dashboard Board、对账模块、会话分析均使用 UTC 日

### 3.4 维度字段（Dimensions）

| 维度 | 英文 | 列名 | NULL 处理 | **未落定桶** |
|------|------|------|-----------|--------------|
| **供应商** | Provider | `provider_id` | COALESCE(id::text, '-1') | ✅ `-1` = 「未落定」 |
| **模型** | Model | `model_name` | COALESCE(name, 'unspecified') | ✅ `'unspecified'` |
| **凭据** | Credential | `credential_id` | COALESCE(id::text, '-1') | ✅ `-1` = 「未落定」 |
| **API Key** | API Key | `api_key_id` | COALESCE(id::text, '-1') | ✅ `-1` = 「未落定」 |
| **租户** | Tenant | `tenant_id` | COALESCE(id, 'default') | ✅ `'default'` |
| **用户** | User | `end_user_id` / `person` | COALESCE(id, '') | ⚠️ 空串 |

**关键规则**:
1. **ID 维度统一 `-1` 哨兵**: provider_id, credential_id, api_key_id 三者共用 `-1` 表示未落定
2. **过滤面对应 `IS NULL`**: 前端筛选 `-1` 时，后端 SQL 必须落成 `WHERE column IS NULL`
3. **归属维度不折叠**: 分组集必须包含归属维度（如 `(tenant_id, person)` 而非 `(person)`）

**已修复**: 对账模块 2026-11-17 修复三个 ID 维度的键空间统一（见 docs/audit/2026-11-17-对账模块统计口径复审-主档.md § F1/F3）

### 3.5 状态术语（Status Terms）

| 术语 | 英文 | 定义 | 来源字段 | 说明 |
|------|------|------|----------|------|
| **在途** | In-flight | 请求已发送但未完成 | `session_turns.completed_at IS NULL` | 实时流 |
| **已完成** | Completed | 请求已结束（成功或失败） | `completed_at IS NOT NULL` | 包含成功和失败 |
| **成功** | Success | HTTP 200 且有完整响应 | `success = true` | 业务成功 |
| **失败** | Failed/Error | 非成功状态 | `success = false` | 含超时、限流、错误 |
| **未落定** | Unassigned | 维度字段为 NULL | `column IS NULL` | 未匹配到具体值 |

### 3.6 探测口径（Probe Scope Terms）——2026-11-21 统计口径对齐轮新增

跨页对齐的**第一根轴**：同一个「总请求数」在不同页面上是两个不同的集合，
术语必须把「数的是哪个宇宙」说清楚，而不是让用户猜。

| 标准术语 | 英文 | 行集定义 | 对齐面 | 标准参数名 |
|------|------|----------|--------|------------|
| **不含探测（业务口径）** | business lane / exclude probes | `usage_facts.traffic_class='business'` == 会话族业务 lane（stage 探针排除） | 内部对账（internal_grain）；board 英雄区缺省；request-logs 缺省；live2 主车道 | `include_probes` 缺省 |
| **包含探测（全量口径）** | include probes | `usage_facts` 全量 == 业务两臂 ∪ 探测臂（session_turns_hot ∪ session_turns ∪ session_turns_probe_hot） | 供应商对账（daily_grain）；board `include_probes=1`；request-logs `include_probe=1` | `include_probes=1` |
| **探测臂** | probe arm | `session_turns_probe_hot`（迁移 928 物理隔离的窄表）按 118 列 canonical 投影展开，费用经 usage_facts 按 request_id 聚合挂回 | request-logs 包含探测模式 | — |

**规则**：
1. **对账级真源只有一份**：要与对账逐字相等，就读对账快照的同源表（usage_facts），
   不要在与对账不同的表上「再算一遍同样的数」（两套谓词边缘行必分叉）。
2. **探测口径必须显式披露**：响应回传 `probe_scope`（board/session-overview）、
   `traffic_lane`（request-logs），前端徽标跟随响应值而非本地开关。
3. **会话级宇宙**：`/dashboard` 会话统计与 `/admin/session-analytics` 共用
   981/994 会话宇宙（sessions×session_dim join + owner 双源键非空 + is_probe +
   session_analytics_is_probe 三维判定）——谓词片段取自
   `domains/analytics.SessionUniverseJoin/SessionUniverseWhere`（迁移侧契约门对拍），
   禁止手抄第二份。
4. **探测行的费用边界**：探测臂行费用取自 usage_facts（与供应商对账同源）；
   未落账行费用为 NULL（COUNT 不受影响，SUM 自然跳过）。

---

## 四、页面级统计审计（Page-Level Statistics Audit）

### 4.1 Dashboard（仪表盘）

**路由**: `/dashboard`

**数据源**: `board_metrics_cache` (15 分钟 TTL)

**关键指标**:

| 显示名称 | 后端字段 | 计算口径 | 数据源 | 更新策略 |
|----------|----------|----------|--------|----------|
| 今日请求 | `today_requests` | COUNT(*) WHERE DATE(created_at)=CURRENT_DATE | `usage_facts` | 实时聚合 |
| 今日成功率 | `today_success_rate` | success_count / request_count | 派生 | 实时 |
| 活跃模型 | `active_models` | COUNT(DISTINCT model_name) WHERE 窗口内有流量 | `usage_facts` | ⚠️ **口径问题**（见 §4.1.1） |
| 活跃密钥 | `active_api_keys` | COUNT(*) WHERE enabled=true | `api_keys` | ⚠️ **口径问题**（见 §4.1.1） |

#### 4.1.1 ⚠️ **Dashboard 文案与口径错位**

**问题**: 
- `active_models` 字段实际是「窗口内有流量的模型数」，但界面显示「可用模型」
- `active_api_keys` 字段实际是「enabled 数量」，但界面显示「活跃密钥」

**修复**: 2026-11-18 简易模式总览页已修正，专家模式待对齐（已登记 F5）

**标准口径**:
- **活跃模型** = 窗口内有流量的模型（需查 `usage_facts`）
- **可用模型** = catalog 中 enabled 且有健康凭据的模型（需查 `models` + `credentials`）

### 4.2 Session Analytics（会话分析系统）

**路由**: `/admin/session-analytics/*`

**数据源**: `routing_mv_*` 物化视图 (定时刷新，陈旧阈值 90 分钟)

**子页面**:

| 页面 | 路由 | 主要指标 | 数据源 | 已实施 |
|------|------|----------|--------|--------|
| 总览页 | `/overview` | 总会话数、成功率、内部/外部成本趋势、Top 模型/用户/客户端 | 物化视图 | ✅ 2026-11-20 |
| 用户排行 | `/users` | 按用户分组的会话数、Token、成本 | 物化视图 | ✅ 2026-11-20 |
| 用户详情 | `/users/:id` | 单用户会话列表、任务列表 | 物化视图 + session_turns | ✅ 2026-11-20 |
| 模型排行 | `/models` | 按模型分组的会话数、Token、成本 | 物化视图 | ✅ 2026-11-20 |
| 客户端排行 | `/clients` | 按客户端分组的会话数、Token | 物化视图 | ✅ 2026-11-20 |

**关键口径统一**:
- **总会话数**: `COUNT(DISTINCT gw_session_id)` 从物化视图
- **成本展示**: 分两卡「内部成本(USD)」「外部成本(积分)」，禁止求和
- **平均上下文**: 后端 `avg_context_size` 恒为 8192（TODO 未接真值，已登记 L2）

**陈旧态处理**: 物化视图刷新超时 90 分钟时，返回 503 + `code=analytics_view_stale`，前端显示「聚合数据正在刷新」横幅

### 4.3 Reconciliation（对账模块）

**路由**: `/admin/reconciliation`

**数据源**: `daily_grain` + `report_snapshots` (旧口径回落)

**关键指标**:

| 显示名称 | 后端字段 | 计算口径 | 一致性验证 | 已修复 |
|----------|----------|----------|-----------|--------|
| 请求数 | `request_count` | SUM(request_count) | ✅ 与 `usage_facts` 逐日零误差 | ✅ |
| 成功数 | `success_count` | SUM(success_count) | ✅ 同上 | ✅ |
| 失败数 | `error_count` | request_count - success_count | ✅ 与 error_breakdown 合计相等 | ✅ |
| Token 数 | 四类 Token | SUM(各 token 列) | ✅ 逐日零误差 | ✅ |
| 成本 | `cost_amount` | SUM(cost_amount) | ⚠️ 混合单位，6 天差 4 分（取整误差） | ❌ F4 |

**已修复问题**（2026-11-17）:
- ✅ **F1**: 三个 ID 维度未落定桶「看得见、筛不出」⇒ 候选面 + 过滤面 + 前端三处统一 `-1` 键
- ✅ **F2**: 旧口径整天总量被算进「未落定」桶 ⇒ 候选源查询标记 `legacy_row`，旧口径支不参与 ID 维度过滤
- ✅ **F3**: 分组集漏归属维度 ⇒ 补齐 `(tenant_id, person)` / `(provider_id, credential_id)` / `(tenant_id, api_key_id)`

**待修复**:
- ❌ **F4**: 成本按组取整再求和（6 天差 4 分），需 schema 变更或写面分配尾差
- ⚠️ **F5**: 用户身份是 JSON 串（6,787 笔 4.7%），非对账模块责任，需上游修正

### 4.4 Live Topology（实时拓扑）

**路由**: `/live`

**数据源**: SSE 实时流 + `usage_facts` 滑动窗口（5 分钟）

**关键指标**:

| 显示名称 | 数据源 | 计算口径 | 更新策略 |
|----------|--------|----------|----------|
| 近 5 分钟请求 | 内存窗口 | COUNT(*) 最近 5 分钟 | SSE 推送 |
| 成功率 | 内存窗口 | success / total | 实时 |
| 在途请求 | `session_turns` | WHERE completed_at IS NULL | 实时查询 |
| 阶段带 | SSE 事件流 | 每个请求的阶段序列 | SSE 流式 |

**关键特性**:
- **有界滚动窗口**: 实时流不是「近 5 分钟全量」，而是**最多保留一定数量**的请求（生产实测会被挤出）
- **心跳过滤**: `idle_marker` 心跳事件不渲染成请求行
- **动态字段**: SSE 扁平化后字段在 `action.detail` 嵌套层

### 4.5 PI Analysis（PI 智能体分析）

**路由**: `/admin/pi-analysis`

**数据源**: `pi_analysis_results` + `pi_analysis_queue`

**关键指标**:

| 显示名称 | 后端字段 | 计算口径 | 一致性 | 已修复 |
|----------|----------|----------|--------|--------|
| 待处理 | `pending_count` | COUNT(*) WHERE status='pending' | ✅ | ✅ |
| 分析中 | `processing_count` | COUNT(*) WHERE status='processing' | ✅ | ✅ |
| 已完成 | `completed_count` | COUNT(*) WHERE status='completed' | ✅ | ✅ |
| 总规则 | `total_rules` | COUNT(*) FROM prompt_injection_rules | ✅ | ✅ |

**已修复**（2026-11-18/19）:
- ✅ 详情页 64 字段补 json tag（原版输出 Go 导出名）
- ✅ `sql.NullString` 泄漏 `{"String":...}` 对象 ⇒ PiNullString 包装
- ✅ PG 时间文本形 Invalid Date ⇒ 空格换 T
- ✅ CreatedAt 与 QCreatedAt 同名 tag 冲突 ⇒ 改 `queued_at`

---

## 五、数据更新策略（Update Strategies）

### 5.1 实时查询（Real-time Query）

**适用场景**: 数据量小、查询简单、低延迟要求

| 页面/端点 | 查询表 | 典型查询 | 响应时间 |
|-----------|--------|----------|----------|
| Dashboard 今日请求 | `usage_facts` | WHERE DATE(created_at)=CURRENT_DATE | <100ms |
| Live 在途请求 | `session_turns` | WHERE completed_at IS NULL | <50ms |
| 凭据监控 | `usage_facts` | 滑动窗口 + 按凭据分组 | <200ms |

**优点**: 数据最新、无陈旧问题
**缺点**: 查询压力直接打到主表、复杂聚合慢

### 5.2 物化视图（Materialized Views）

**适用场景**: 复杂聚合、多维分析、可容忍分钟级延迟

| 视图 | 刷新频率 | 数据源 | 查询响应 | 陈旧处理 |
|------|----------|--------|----------|----------|
| `routing_mv_overview` | 5 分钟 | `usage_facts` | <100ms | 503 + banner |
| `routing_mv_user_ranking` | 5 分钟 | `usage_facts` | <100ms | 503 + banner |
| `routing_mv_model_ranking` | 5 分钟 | `usage_facts` | <100ms | 503 + banner |

**刷新策略**: 定时 cron + 写触发（可选）

**陈旧判定**: `requireFreshViews` 预算 90 分钟，超时返回 503

**优点**: 查询快、支持复杂聚合
**缺点**: 有延迟、刷新开销、可能陈旧

### 5.3 预聚合缓存（Pre-aggregated Cache）

**适用场景**: 高频访问、计算昂贵、可容忍短期陈旧

| 缓存 | 存储 | TTL | 失效触发 | 回源 |
|------|------|-----|----------|------|
| Board metrics | Redis | 15 min | 写触发 | `board_metrics_cache` 表 |
| Analytics summary | Redis | 5 min | TTL | 物化视图 |
| Dimension options | Redis | 1 hour | TTL | `daily_grain` |

**写触发**: 写入 `usage_facts` 后异步刷新缓存（可选，当前未实施）

**优点**: 极快、减轻 DB 压力
**缺点**: 陈旧、缓存一致性、内存开销

### 5.4 批量聚合（Batch Aggregation）

**适用场景**: T+1 报表、历史分析、不要求实时

| 任务 | 频率 | 数据源 | 目标表 | 幂等性 |
|------|------|--------|--------|--------|
| Daily grain 聚合 | 每日 02:00 UTC | `usage_facts` | `daily_grain` | ✅ ON CONFLICT |
| Report snapshot | 按需/定时 | `usage_facts` | `report_snapshots` | ✅ |
| Credential rollup | 每小时 | `usage_facts` | `credential_monitor_rollup` | ✅ |

**优点**: 准确、支持复杂逻辑、不影响在线查询
**缺点**: 延迟（T+1）、需调度器

### 5.5 混合策略（Hybrid Strategy）

**典型**: Dashboard Board（对账页同理）

```
查询流程:
1. 尝试从 Redis 缓存读（TTL 15 min）
2. 缓存未命中 ⇒ 查 board_metrics_cache 表
3. 表为空或陈旧 ⇒ 实时聚合 usage_facts
4. 结果写回 Redis + board_metrics_cache
```

**优点**: 兼顾速度与准确性
**缺点**: 逻辑复杂、多个回源路径

---

## 六、术语一致性审计结果（Terminology Consistency Audit）

### 6.1 跨页面术语对照

| 术语 | Dashboard | Session Analytics | Reconciliation | Live Topology | **一致性** |
|------|-----------|-------------------|----------------|---------------|-----------|
| 请求数 | ✅ request_count | ✅ total_sessions | ✅ request_count | ✅ 近 5 分钟请求 | ✅ 口径一致 |
| 成功率 | ✅ success_rate | ✅ success_rate | ✅ 派生 | ✅ 实时计算 | ✅ 口径一致 |
| 成本 | ⚠️ 混合单位 | ✅ 分两卡（USD/积分） | ⚠️ 混合单位 | N/A | ❌ **不一致** |
| 活跃模型 | ❌ 文案错位 | ✅ 实际有流量 | N/A | N/A | ❌ **文案问题** |
| 时间窗口 | ✅ UTC 日 | ✅ UTC 日 | ✅ UTC 日 | ⚠️ 本地日（趋势图） | ⚠️ **部分不一致** |

### 6.2 已修复的不一致（2026-11-17 ~ 2026-11-20）

| 问题 | 影响范围 | 修复时间 | 修复方式 | 证据 |
|------|----------|----------|----------|------|
| **ID 维度键空间不统一** | Reconciliation 三个维度 | 2026-11-17 | 候选面/过滤面/前端统一 `-1` 键 | docs/audit/2026-11-17-对账模块统计口径复审-主档.md |
| **分组集漏归属维度** | Reconciliation 人员/凭据/apikey | 2026-11-17 | 分组集补齐归属维度 | 同上 |
| **Session Analytics 成本跨单位** | 总览页、排行页 | 2026-11-20 | 分两卡展示 + 排行列注明 | docs/audit/2026-11-20-会话分析系统前端实施报告.md |
| **Dashboard 文案与口径错位** | 简易模式总览 | 2026-11-18 | 文案与后端 SQL 对齐 | docs/audit/2026-11-18-简易模式总览页看板化优化-主档.md |
| **PI 详情页字段名错位** | PI Analysis 详情页 | 2026-11-18 | 补 json tag + null 包装 | docs/audit/2026-11-18-PI分析管理面优化-主档.md |

### 6.3 待修复的不一致

| 问题 | 影响范围 | 优先级 | 修复方案 | 登记位置 |
|------|----------|--------|----------|----------|
| **成本列混合单位** | Reconciliation `daily_grain.cost_amount` | P2 | Schema 变更拆分列 OR 写面尾差分配 | docs/audit/2026-11-17-对账模块统计口径复审-主档.md § F4 |
| **Dashboard 专家模式文案** | 看板顶部卡片 | P3 | 与简易模式修法对齐 | 待立项 |
| **平均上下文恒 8192** | Session Analytics 详情页 | P2 | 后端补真值计算 | docs/audit/2026-11-20-会话分析系统前端实施报告.md § L2 |
| **旧口径整天成本差 4 分** | Reconciliation 6 天 | P3 | 单独立项（成本低） | docs/audit/2026-11-17-对账模块统计口径复审-主档.md § F4 |

---

## 七、可复用统计处理模块（Reusable Statistical Modules）

### 7.0 已落地的模块（2026-11-21 对齐轮起为**实现**而非提案）

| 模块 | 位置 | 内容 | 消费方 |
|------|------|------|--------|
| 会话宇宙片段 | `domains/analytics/session_probe_scope.go` | `SessionUniverseJoin` / `SessionUniverseWhere`（与 994 物化视图逐字同构，契约门 `session_probe_scope_contract_test.go` 对拍迁移 SQL）；`ShanghaiCalendarRangeUtc`（与物化视图日桶同尺的 +08 日历窗） | `/api/admin/dashboard/session-overview` |
| 对齐指标单一真源 | `domains/analytics/aligned_source.go` | `QueryOverviewSummary` / `QueryModelStats`（从 admin/session_analytics 逐字迁来的物化视图查询） | `/admin/session-analytics` 总览 + `/api/admin/dashboard/session-overview` 对齐指标行 ⇒ **两面构造相等** |
| 请求级探测臂 | `db/session_probe_arm_source.go` | `SessionFamilyProbeArmSourceSQL` / `SessionFamilyTurnsSourceWindowedAllSQL`（三臂 UNION；契约门 `session_probe_arm_source_contract_test.go`） | `/api/logs` 包含探测模式 |
| 成本计算器 | `domains/analytics/cost.go` | CostCalculator（单位纪律守卫） | （见完成报告） |
| 派生指标计算器 | `domains/analytics/derived.go` | DerivedMetrics（成功率/分位/增长率等纯函数） | （见完成报告） |

### 7.1 时间窗口聚合（Time Window Aggregator）

**位置**: `domains/analytics/time_window.go`（建议新建）

**功能**:
- UTC 日聚合: `AggregateByUTCDay(facts []UsageFact) map[string]DailyAggregate`
- 本地日聚合: `AggregateByLocalDay(facts []UsageFact, tz string) map[string]DailyAggregate`
- 滑动窗口: `SlidingWindow(facts []UsageFact, duration time.Duration) []UsageFact`
- 补零逻辑: `FillMissingDays(data map[string]Aggregate, start, end time.Time) map[string]Aggregate`

**当前状态**: 散落在各模块，建议抽取

### 7.2 维度分组器（Dimension Grouper）

**位置**: `domains/analytics/grouping.go`（建议新建）

**功能**:
- 通用分组: `GroupBy(facts []UsageFact, dimensions []string) map[GroupKey]Aggregate`
- NULL 处理: `CoalesceNullKey(value *string, nullKey string) string`（统一 `-1` / `'unspecified'` 逻辑）
- 归属补全: `EnrichGroupKeyWithParent(key GroupKey, parentDim string) GroupKey`

**当前状态**: 对账模块有 `grainreport.go` 的 `classify()`，可作为蓝本

### 7.3 成本计算器（Cost Calculator）

**位置**: `domains/analytics/cost.go`（建议新建）

**功能**:
- 内部成本: `CalculateInternalCost(facts []UsageFact) float64`（返回 USD）
- 外部成本: `CalculateExternalCost(facts []UsageFact) int64`（返回 credits）
- **禁止**: `CalculateTotalCost` —— 不提供跨单位求和方法
- 成本分拆: `SplitCostByGroup(total float64, groups map[string]int) map[string]float64`（尾差分配）

**当前状态**: 成本计算散落各处，需统一

### 7.4 派生指标计算器（Derived Metrics Calculator）

**位置**: `domains/analytics/derived.go`（建议新建）

**功能**:
- 成功率: `SuccessRate(total, success int) float64`（防除零）
- 失败数: `ErrorCount(total, success int) int`
- 平均值: `Average(sum, count int) float64`（防除零）
- 百分位: `Percentile(values []float64, p float64) float64`

**当前状态**: 部分在 `dashboardapi/` 下，可抽取

### 7.5 缓存管理器（Cache Manager）

**位置**: `domains/analytics/cache.go`（建议新建）

**功能**:
- 读缓存: `Get(key string, dest interface{}) (bool, error)`
- 写缓存: `Set(key string, value interface{}, ttl time.Duration) error`
- 失效: `Invalidate(pattern string) error`
- 预热: `Warmup(keys []string, loader func(string) (interface{}, error)) error`

**当前状态**: Redis 操作散落各处，建议封装

---

## 八、实施路线图（Implementation Roadmap）

### 阶段 1: 术语标准化文档（本文档） ✅ 已完成

- ✅ 创建统一术语表
- ✅ 审计所有页面数据源
- ✅ 记录已知不一致性

### 阶段 2: 关键不一致修复（优先级 P0/P1）

| 任务 | 优先级 | 预估工作量 | 状态 |
|------|--------|-----------|------|
| 跨单位成本求和问题全面清理 | P0 | 2 天 | 进行中 |
| Dashboard 专家模式文案对齐 | P1 | 0.5 天 | 待开始 |
| Session Analytics 平均上下文真值 | P1 | 1 天 | 待开始 |

### 阶段 3: 可复用模块抽取

| 模块 | 预估工作量 | 依赖 | 状态 |
|------|-----------|------|------|
| 时间窗口聚合器 | 1 天 | 无 | 待开始 |
| 维度分组器 | 1 天 | 无 | 待开始 |
| 成本计算器 | 0.5 天 | 无 | 待开始 |
| 派生指标计算器 | 0.5 天 | 无 | 待开始 |
| 缓存管理器 | 1 天 | 无 | 待开始 |

### 阶段 4: 全面测试与部署

| 任务 | 描述 | 状态 |
|------|------|------|
| 单元测试 | 所有新模块 100% 覆盖 | 待开始 |
| 集成测试 | 跨模块数据流验证 | 待开始 |
| E2E 测试 | Maestro 全页面验证 | 待开始 |
| 211 部署 | 蓝绿部署 + 验证 | 待开始 |

---

## 九、质量门禁（Quality Gates）

### 9.1 代码级门禁

- [ ] 所有新增统计代码必须有单元测试
- [ ] 禁止跨单位求和（静态扫描规则）
- [ ] 时间窗口必须显式声明 UTC/Local
- [ ] NULL 处理必须显式声明哨兵值
- [ ] 维度分组必须包含归属维度

### 9.2 数据级门禁

- [ ] 新增聚合表必须与 `usage_facts` 对账验证
- [ ] 物化视图必须有陈旧判定逻辑
- [ ] 缓存必须有失效策略
- [ ] 批量任务必须幂等

### 9.3 文档级门禁

- [ ] 新增端点必须更新本文档
- [ ] 新增指标必须定义计算公式
- [ ] 口径变更必须记录变更日志

---

## 十、附录（Appendix）

### 10.1 相关审计报告

1. **2026-11-17 对账模块统计口径复审**: `docs/audit/2026-11-17-对账模块统计口径复审-主档.md`
2. **2026-11-18 Dashboard 会话统计审计**: `docs/audit/2026-11-18-dashboard-stats-tab会话统计审计与优化方案-主档.md`
3. **2026-11-19 PI 分析统计一致性审计**: `docs/audit/2026-11-19-PI分析统计一致性批判审计-报告.html`
4. **2026-11-20 会话分析系统前端实施**: `docs/audit/2026-11-20-会话分析系统前端实施报告.md`

### 10.2 数据字典

完整字段级数据字典见: `docs/data-dictionary.md`（待创建）

### 10.3 变更日志

| 日期 | 变更 | 影响 | 责任人 |
|------|------|------|--------|
| 2026-11-20 | 创建统计标准化框架 | 全局 | Goal Session |
| 2026-11-20 | Session Analytics 成本分拆 | 总览页、排行页 | - |
| 2026-11-17 | Reconciliation ID 维度键统一 | 对账模块三维度 | - |

---

**文档所有者**: 统计标准化委员会（虚拟）
**最后更新**: 2026-11-20
**下次复审**: 每季度或重大变更时
