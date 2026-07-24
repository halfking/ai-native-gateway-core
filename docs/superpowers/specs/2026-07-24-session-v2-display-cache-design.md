# 会话优化 V2 + 展示与缓存完善 — 设计规约

> **文档状态**：DRAFT v1.0 → 等待用户审阅
> **创建日期**：2026-07-24
> **作者**：会话优化 V2 工作组
> **关联文档**：
>
> - `docs/会话优化v2/31-当前实现基线与修正决策.md`（权威基线）
> - `docs/会话优化v2/00-完整方案文档.md`（V2 存储设计 + 勘误）
> - `docs/拆分/08-会话存储与缓存优化.md`（TARGET 评估）
> - `docs/拆分/08b-会话展示与实施.md`（展示目标）
> - `docs/拆分/15-会话存储代码事实核验.md`（代码事实）
> - `docs/拆分/17-会话展示代码事实核验.md`（展示事实）
> - `docs/拆分/30-会话优化V2与S1门禁冲突分析.md`（依赖矩阵）
> - `docs/拆分/23-会话管理迁移总控规划.md`（迁移总控）
> - `docs/拆分/24-会话功能页面模块全量矩阵.md`（页面矩阵）
> - `docs/拆分/27-会话管理页面交互与视觉规范.md`（UI 规范）

---

## 1. 概要

### 1.1 目标

将 `docs/会话优化v2` 既有的 V2 存储方案（gateway 4 张分区表 + DualWriter）与 `docs/拆分/08/08b` 提出的会话展示、三级缓存重构、即时总结、附件 manifest 等设计目标**集成实施**，形成单一可行的工程方案，避免文档设计各自为政导致的实现错位。

### 1.2 不在本设计的范围

- ❌ 不实施 SessionManager 服务本体（按 docs/拆分/30 §3，无阻塞冲突，保持现状）
- ❌ 不实现 HTTP 202 + `X-LLM-Gateway-Retry-Scheduled` Retry 协议（按 31 号 ADR-GOAL-001，仍为提案）
- ❌ 不重写 Summary Worker 至 SessionManager（按 31 号 §1.2，session-manager 未交付）
- ❌ 不下线 `public.session_turn_snapshots`（按 30 号 §2.1，385 表保留 down migration 待运维手动调用）

### 1.3 核心设计取舍（已与用户对齐）

- **存储**：在现有 `gateway.*` 4 表基础上增强；不引入新表
- **缓存**：采用 docs/拆分/08 §8.4 的 L0 / L1 / L2 / L3 四层，与用户提出"原始+一级+二级"概念映射
- **总结 worker**：留在 Gateway 内（基于现有 SessionSummaryWorker 演进），async + 即时按钮双触发
- **附件**：在 Gateway 内提供 turn-level manifest + signed URL（不跨服务）
- **展示**：新建 `SessionDetailPage.vue`，双栏倒排 + 抽屉式详情，替代 `SessionTurnsPanel.vue` legacy compare

---

## 2. 现状摘要（来自 15/17 号代码事实核验）

| 能力 | 现状 | 缺口 |
| --- | --- | --- |
| `gateway.sessions/session_turns/session_bodies/session_turn_logs` DDL | ✅ Migration 430 已落地 | — |
| RLS | ✅ 4 表 tenant isolation policy | 缺 admin owner filter |
| TurnWriter / BodiesWriter / TurnLogsWriter / SessionAggregator | ✅ 已实现 + 单测通过 | V2-P2.3 wire 未做；V2-P3 回填未做 |
| DualWriter / pipeline_hook.go | ⚠️ `pipeline_hook.go` 存在，未在 main pipeline 注册 | V2-P2.3 待完成 |
| SubmitModeDetector | ✅ P0-P5 + 单元测试 | 缺生产环境观测 |
| Cache V2 (L1/L2) | ✅ 已实现 + 单测 | L0 L3 待 wire |
| L3 冷启动 | ⚠️ PARTIAL：仍读 `request_logs.outbound_body` | 改读 session_bodies / session_turns |
| SessionCompare API | ⚠️ legacy，500 条上限，无 cursor | 新增 `GET /api/admin/sessions/:id/turns` cursor API |
| `SessionTurnsPanel.vue` | ⚠️ 读 legacy compare payload | 改造或新建 `SessionDetailPage.vue` |
| 即时总结 worker | ⚠️ Gateway `SessionSummaryWorker` 订阅 `session.closed` | 升级模型选择 + 即时按钮触发 |
| 附件 | 🔴 request-level，缺 tenant/session/turn manifest + signed URL | 补 turn-level manifest + signed URL + 撤销 + 下载审计 |

---

## 3. 数据模型变更

### 3.1 不新增表，仅补列与约束

| 表 | 变更 | 变更原因 | 风险 |
| --- | --- | --- | --- |
| `gateway.sessions` | 新增 `last_full_request JSONB`、`last_full_response JSONB`、`last_full_payload_at TIMESTAMPTZ`、`title TEXT`、`summary TEXT`、`summary_model TEXT`、`summary_generated_at TIMESTAMPTZ`、`summary_quality TEXT` | 用户要求"会话快照表记录最后一轮完整请求/回复"+ 即时总结落库 | 单轮 JSON 体积 < 2MB；不存附件二进制 |
| `gateway.session_turns` | 新增 `attempt_no INT DEFAULT 0`、`tools JSONB DEFAULT '[]'::jsonb`、`title TEXT`（每轮一句话）、`summary TEXT`（每轮一句话） | 支持"分轮一句话"预览与分轮摘要；attempt_no 区分同 turn 内部 failover 重试（共享 turn_no） | 极小，仅 metadata |
| `gateway.session_bodies` | 新增 `request_attachments JSONB DEFAULT '[]'::jsonb`（对象引用清单）、`response_attachments JSONB DEFAULT '[]'::jsonb`，`outbound_body` 改为可空 | docs/拆分/08 §8.3.2 要求"附件只存引用，不存 base64" | 已是可空，无破坏 |
| `gateway.session_turn_logs` | 不变 | — | — |

#### 3.1.1 last_full_request/response 写入策略

- 会话每次写入新 `session_turns` 成功后，通过轻量级 `pg_notify` 通道异步触发 `SessionSnapshotUpdater` 更新 `gateway.sessions.last_full_*`
- 取该 turn 的 `request_delta` + `response_delta` 拼接；若需更历史上下文，可重放前 N 轮的 delta，**不持久化全景**
- 防抖：同一会话 1 秒内多次写入合并为一次更新
- 写失败仅记日志，不影响 turn 写入主路径

### 3.2 分区表与存储

- 四表均为 `*-hot` heap 格式 + 按月 `PARTITION BY RANGE (partition_date)`
- 仅新增/批量删除分区，DDL 走 `ensure_sessions_v2_partitions(target_date)`；
  调用方：bg.PartitionManager（已接管 `ensure_request_logs_partition`） + 启动期预创建本月+下月
- 严禁：点对点 DELETE/UPDATE 大于 5k 行 → 走分区级 DDL
- 新分区的 `partition_date` 必须覆盖到 query range；冷读返回分区缺失错误，调用方降级到相邻月查询

### 3.3 RLS（保持现状，补充 owner filter）

- 已有 4 张表 RLS 隔离（30 号 §3 表）
- 新增 `session_turns_owner_filter`：admin 路径需 JOIN `session_dim` 取 `owner_user`，谓词如下：
  ```
  USING (
    EXISTS (
      SELECT 1 FROM session_dim sd
      WHERE sd.session_id = gateway.session_turns.session_id
        AND sd.tenant_id = current_setting('app.current_tenant', true)
        AND (sd.owner_user = current_setting('app.current_user', true)
             OR current_setting('app.current_role', true) = 'super_admin'
             OR current_setting('app.bypass_rls', true) = 'true')
    )
  )
  ```
- 提供 down 与 up migration（session_turns_owner_filter_20260724.sql）
- 必须新增 `TestRLS_SessionsV2_Turns_OwnerFilter` 真实 PG DSN 集成测试

### 3.4 24h 环节状态表

- 沿用 `session_turn_logs`，TTL 函数 `cleanup_expired_session_turn_logs()` 已存在
- 在 bg.Worker 中加调度：每 30 分钟运行一次
- 会话关闭（由 handler 在 `SessionCache.CloseSession` 调用）触发 `AggregateSessionLogs(session_id)` 一次：
  - 把该会话 24h 内的 turn_logs 聚合为 JSONB
  - UPDATE `gateway.sessions.turn_logs_summary`
  - 之后立即删除该会话的 turn_logs（避免冗余）

---

## 4. API 契约（新增/调整）

### 4.1 Admin API（Gateway 暴露）

| 路径 | 方法 | 功能 | 数据源 | 失败语义 |
| --- | --- | --- | --- | --- |
| `/api/admin/sessions/:id/snapshot` | GET | 会话元信息 + 最后一轮全量快读 + 累计 turn/tokens/cost + title/summary | `gateway.sessions` | 200 / 404 / RLS 403 / 5xx |
| `/api/admin/sessions/:id/turns?order=desc&cursor=&limit=50` | GET | cursor 分页轮次列表，仅元信息 + 每轮一句话预览 + 附件 manifest 摘要 | `gateway.session_turns` + 部分 `session_bodies` 附件字段 | 200 + `has_more` + `next_cursor`；游标不可逆；超时/历史缺失返回 `NOT_READY` 标志 |
| `/api/admin/sessions/:id/turns/:turnNo` | GET | 单轮完整请求+回复+附件 + compression/governance 元数据 + 子 turns（attempt_no 列表） | `session_bodies` + `session_turns` | 200 / 403 / 5xx |
| `/api/admin/sessions/:id/instant-summary` | POST | 触发即时总结；async，返回 202 + `summary_job_id` | 内部 LLM worker | 202 / 503 / 429 |
| `/api/admin/sessions/:id/instant-summary/:job_id` | GET | 轮询总结状态 | worker 结果暂存 | 200 / 404 / 202 |
| `/api/admin/sessions/:id/turns/:turnNo/attachments/:attId/url` | GET | 签发下载 URL（短 TTL，绑定 tenant + session + turn + sha256） | 对象存储 | 200 + signed URL / 403 / 404 / 已撤销 |
| `/api/admin/sessions/:id/turns/:turnNo/attachments/:attId/revoke` | DELETE | 撤销单条附件授权 | — | 204 / 403 / 404 |

#### 4.1.1 cursor 设计

- 不可逆不透明 token；服务端构造为 `base64(tenantId|sessionId|turnNo|ts)` + HMAC 签名
- 升级或版本切换时强制返回 `NOT_READY`，客户端重新拉首屏
- 客户端用 `next_cursor` POST/GET 下一批

#### 4.1.2 安全契约

- 所有 admin API 必须：
  1. 强制 `Authorization: Bearer <admin_jwt>`，校验 `aud`/`scope`/`exp`/tenant context
  2. 不允许 URL 或 client header 携带 `tenant_id` 直接信任
  3. 失败附加结构化错误码（缺请求/会话/权限/限流），便于 UI 渲染

### 4.2 兼容性回退

- 老 `GET /api/admin/sessions/:id/compare`（SessionCompare）保留**只读**六个月
- 比较接口不再写新数据；展示层读 legacy 改为读 `/sessions/:id/turns` 主路径，仅当 `gateway.session_turns` 未就绪时降级到 `/compare`
- 双读对账日志（与 docs/拆分/08 §8.6 S3 一致）：前 4 周抽样对比新旧 reader 输出

---

## 5. 缓存层装配（L0 / L1 / L2 / L3）

按 docs/拆分/08 §8.4 设计实现。与现有 `cache_v2.go` 已有 L1/L2 框架一致，新增 L0 与 L3。

### 5.1 L0 — 原始缓存（本轮增量）

| 项 | 设计 |
| --- | --- |
| 内容 | 与 session_turns/session_bodies 写入同步：`{turn_no, request_delta, response_delta, submit_mode, attachments}` |
| 形态 | 内存 ringbuffer（按 tenantID+sessionID 哈希分片）；不上 redis（避免引入新热路径依赖） |
| 生命周期 | LRU 容量 1024 sessions；get_or_create 时若 DB miss，按 tenant/session 从 session_turns 重建 |
| Skip | 无；日志必需 |
| 代码位置 | `domains/session/v2/cache_v2.go` 新增 `type RawCacheV2` |

### 5.2 L1 — 压缩缓存（一级）

| 项 | 设计 |
| --- | --- |
| 内容 | `SummaryMarker, CompressedPrefixHash, TokenEstimate, RecentlyCompressedAt, ToolsHash`，不存 body |
| 形态 | 复用现有 `CompressionMetaCache`（MemoryLRU 1024） |
| Skip 条件 | `compression=OFF` 且 `session_cache=OFF` → L1 不写不读 |
| 写入时机 | SessionCompressor 决策后，仅针对请求；回复原样入 L0 |
| 代码位置 | `domains/hooks/compression/session_cache.go` 已实现，仅补 feature flag 配置读取 |

### 5.3 L2 — 治理缓存（二级）

| 项 | 设计 |
| --- | --- |
| 内容 | `injection_verdict, output_verdict`（pass/warn/block/skip） |
| 形态 | Redis Hash `session:gov:{tenantID}:{gwSessionID}:v1`，30min TTL（已存在） |
| Skip 条件 | 每个 verdict 字段独立：模块未装则 `skip` 不写不读 |
| 写入时机 | PreRouting 审计完成后写 `injection_verdict`；PostResponse 合规检查完成后写 `output_verdict` |
| 回写 V2 | 同时镜像到 `session_turns.injection_verdict / output_verdict` |

### 5.4 L3 — 冷启动（DB 重建）

| 项 | 设计 |
| --- | --- |
| 内容 | `session_turns` + `session_bodies`（按时间窗取最近 N 轮，例如 N=10）+ `session_cache` last 状态 |
| 形态 | DB query；不缓存 raw body，只缓存聚合的 SessionState |
| 读源 | `session_turns` 元数据 JOIN `session_bodies` 增量并按 turn_no 顺序拼接（不读 request_logs） |
| Skip | 无 |
| 代码位置 | 改造 `domains/hooks/compression/session_cache.go:loadFromDB`，把 `LastOutboundForSession` 替换为 `TurnReader.LoadChain(tenant, session, n)` |

### 5.5 缓存命中路径示例

```
PostRouting 阶段：
  1. 查 L0：get_raw_cache(tenant, session) → 拿到本 turn 的 request_delta 上下文
     miss → 查 L3：从 session_turns/session_bodies 重建，加载入 L0
  2. 触发 SessionCompressor → 写 L1 CompressionMeta + L0 raw_cache.delta
  3. PreRouting 审计 → 写 L2 injection_verdict（skipped 时不写）
  4. PostResponse 合规检查 → 写 L2 output_verdict
  5. SessionWriterV2 写入 → session_turns + session_bodies（落库，异步）
  6. SnapshotUpdater 通过 pg_notify 异步刷 sessions.last_full_*
```

---

## 6. UI 设计（双栏倒排 + 抽屉）

### 6.1 入口与导航

- 在 `admin/sessions` 列表（按 docs/拆分/24 矩阵中的"会话管理"）行点击进入新页面 `SessionDetailPage.vue`
- URL：`/admin/sessions/:id?turn=:turnNo&focus=1`
- 顶部 sticky bar：会话 ID、tenant、累计 turns/tokens/cost、Title（LLM 生成，可点击 "即时总结" 触发）、Summary（一段话）
- 主内容：双栏倒排轮次面板（默认每页 50，cursor 滚到顶部加载更早；每行仅请求+回复摘要，**详情放进抽屉**）

### 6.2 双栏倒排

```
┌─────────────────────────────────────────────────────────────┐
│  Session gw_abc123  | 实现用户认证模块  | [立即总结] | ⋯     │
│  Tenant kxpms · 38 turns · 43k tokens · $1.23 · 3h 12m      │
├──────────────────────────────┬──────────────────────────────┤
│  Turn 5  02:31  请求         │  Turn 5  02:31  回复          │
│  [user] 请补充 JWT …         │  [assistant] 已添加 refresh … │
│  📎 2 附件 ⤓                 │  📎 1 附件 ⤓                  │
│  ⚠ injection: pass          │  ✓ output: pass              │
│  Δ 1200 tokens · $0.012     │  Δ 800 tokens · $0.006        │
├──────────────────────────────┼──────────────────────────────┤
│  Turn 4  ...                  │  Turn 4  ...                 │
│  (倒序排列)                   │                              │
└──────────────────────────────┴──────────────────────────────┘
```

### 6.3 抽屉详情（点击轮次后展开）

- 从右侧划出（Element Plus el-drawer / 类似），宽 70vw
- 顶部 Tab：[请求] [回复] [元数据] [压缩/治理] [附件]
- 附件以列表形式（图标+名称+大小+sha256），点击触发 signed URL 下载
- 治理 verdict 标签颜色编码：pass=绿 / warn=黄 / block=红 / skip=灰
- **不要在抽屉中暴露对象存储原始 key**

### 6.4 即时总结 UI

- 触发方式：
  1. 顶部按钮 "即时总结"
  2. 若 turns > 阈值（默认 12）且最近 5 分钟未总结过 → 自动提示
- 状态机：`idle` → `pending`（202 + jobId）→ `streaming`（可选流式补全） → `done` / `failed`
- 仅在 done 时落库 `sessions.title/summary/summary_model/summary_generated_at`；失败展示重试按钮
- 不阻塞页面其他操作

### 6.5 模型选择（Summary Model Selector）

详见 §7。

### 6.6 视觉规范遵循

- 复用 docs/拆分/27 §27 已定义的色彩、字号、状态色规范
- 与 `SessionTurnsPanel.vue` 样式解耦；新组件 `SessionTurnListItem.vue`、`SessionTurnDrawer.vue`、`SessionSummaryBar.vue`
- 旧 `SessionTurnsPanel.vue` 保留为兼容组件，内部委托到 `SessionDetailPage`（通过 `<router-view>` + 老路径）

---

## 7. 即时总结模型选择（Summary Model Selector）

### 7.1 模型清单获取

- 调用 `modelcatalog` 服务：输入 `(tenant_id, capability=summary, max_tokens>=4096, supports_text=true)` 返回候选模型列表
- 若 modelcatalog 服务不可用 → fallback 到 settings 表 `summary.model` 显式配置

### 7.2 候选评分与挑选

候选评分（分数越高越优）：

```
score = w1 * availability + w2 * cost_efficiency + w3 * context_window_score + w4 * latency_p95_inv

  availability        — 0..1，来自 routing 最近 5min 健康率
  cost_efficiency     — 1 / (price_per_1k_tokens)，归一化
  context_window      — min(1, ctx / 32768)
  latency_p95_inv     — 1 / latency_p95，归一化到 0..1
```

默认权重 `w1=w2=0.35, w3=w4=0.15`，租户可覆盖。

### 7.3 调度策略

- 异步 worker 接收 `InstantSummaryJob { tenant, session, turns[], target=("title"|"summary"|"turn_one_liner"), preferred_model? }`
- 单 worker 池：默认 2 workers（可基于 settings 配）；全局 QPS 限速
- 每个 turn_one_liner 输入 ≤ 2k tokens，整体会话 ≤ 12 turns；超长截断 + `[smm_v1:...]` 摘要 marker
- 输出：`title` (≤ 64 chars) / `summary` (≤ 200 chars / 中文) / `turn_one_liner` (≤ 40 chars / 中文)
- 失败重试：成本感知，最多 1 次，换模型

### 7.4 存储与可见

- 结果落 `gateway.sessions`：title / summary / summary_model / summary_generated_at / summary_quality (`rejected|partial|verified`)
- 也同步写 `session_turns.title / summary`（逐轮一句话）
- 写入失败仅记日志，UI 展示 `pending` 不阻塞页面

### 7.5 安全与成本护栏

- 每个会话每日总结次数上限（默认 3 次，可覆盖）
- 总结任务的 token 用量独立计费键（`summary_tokens`）记入 `billing_orders`
- 总结任务触发的模型调用受 URSM 限速；超阈值不响应

---

## 8. 附件 manifest + Signed URL

### 8.1 manifest 字段

`gateway.session_bodies.request_attachments / response_attachments`：

```json
[
  {
    "att_id": "att_<uuid>",
    "name": "spec.md",
    "object_key": "tenant/kxpms/sessions/gw_abc123/turn_5/spec-<sha256>.md",
    "mime": "text/markdown",
    "size": 12345,
    "sha256": "...",
    "uploaded_at": "2026-07-24T03:00:00Z",
    "uploaded_by": "user:<id>" 
  }
]
```

### 8.2 签发 / 撤销 / 审计

- 签发：`GET /turns/:turnNo/attachments/:attId/url` 返回 signed URL（含 HMAC + 5min TTL）
- 撤销：`DELETE /turns/:turnNo/attachments/:attId/revoke` → 将 `att_id` 加入 blacklist（Redis SET，TTL 1h，配合服务端验证）
- 审计：每次签发/下载/撤销写 `attachment_access_log`（包含 tenant, session, turn, attId, action, ip, ua, ts）
- 对象存储不可暴露长期 URL；服务侧必须校验 HMAC + 过期 + tenant/session/turn 三元组

### 8.3 完整证据矩阵（在测试中覆盖）

- [x] 跨 tenant/session/turn 引用返回 403
- [x] 过期签名返回 410
- [x] 已撤销附件返回 410
- [x] 路径篡改返回 403
- [x] 大小/MIME/sha256 不匹配返回 422

---

## 9. SubmitMode 检测 + 客户端协作

按 docs/拆分/08 §8.5 已实现 P0-P5。本设计：

- 已存在 P0：`X-Gw-Submit-Mode: snapshot | delta | full` 协作头
- 已存在 P1：消息数回退
- 已存在 P2：摘要 marker 识别
- 已存在 P3：orphaned tool_result
- 已存在 P4：LCS 重叠
- 已存在 P5：首轮 full

**新增点**：

- `domains/session/v2/submit_mode_detector.go` 新增 fail-safe 行为：当 `compression=OFF AND session_cache=OFF` 时，P0/P1 判定后**直接走 `inferred_compressed` 路径**，整包视作本轮 delta（不做 LCS）。
- 行为不改变 session_bodies 写入事实源。

---

## 10. 实施阶段（映射 docs/拆分/08 §8.6）

按 docs/拆分/30 §4 的"V2 架构推进"顺序，叠加本设计的展示/总结/附件三个新增 workstream：

| 阶段 | 内容 | 通过标准 | 依赖 | 回滚点 |
| --- | --- | --- | --- | --- |
| **V2-P2.3** | 把 SessionPersistHook 接入 main pipeline；配置 `sessions_v2.enabled=false` 默认；open telemetry/log 收集 | main 注册；hot reload 可触发；smoke test 命中 | 现成 writers / dual_writer | feature flag 关掉 |
| **V2-P2.4** | sessions 表 last_full_*/title/summary 等列 migration；session_turns attempt_no / tools migration；up+down SQL | up/down dry run；RLS 不变 | V2-P2.3 | down migration |
| **V2-P2.5** | L0/L3 缓存实现 + 现有 L1/L2 对接新 L0 + pipeline 装配 | 单元/集成测试；缓存命中率埋点 | V2-P2.3 | 关闭 L0，仅 L1/L2 |
| **V2-P3** | 历史数据回填：sessions / session_turns / session_bodies from request_logs（best-effort，source_kind=backfill，quality=inferred） | 可重跑；抽样对账；不阻塞生产 | V2-P2.5 | 删除/隔离 batch |
| **V2-P3.1** | RLS owner filter + TestRLS_SessionsV2_Turns_OwnerFilter 真实 DSN 集成 | 5 个负向用例全过 | V2-P3 | 关 RLS bypass |
| **V2-P3.2** | session_turn_logs 聚合回写到 sessions.turn_logs_summary | 函数 unit test；e2e 单会话观察 | V2-P2.4 | 关聚合，仅原 24h 清理 |
| **V2-P4** | Admin API：snapshot / turns(cursor) / turn detail / attachments signed url/revoke/audit | 全部 -short + 真实 DSN 集成；旧 `/compare` 仍可读 | V2-P3.1 + P3.2 | 路由到旧 `/compare` |
| **V2-P4.1** | `SessionDetailPage.vue` + `SessionTurnListItem.vue` + `SessionTurnDrawer.vue`；`SessionTurnsPanel.vue` 委托 | UI 视觉门禁 27 章；响应式；权限实测 | V2-P4 | fallback legacy compare |
| **V2-P5** | SessionSummaryWorker 演进为模型选择策略；新增 turn_one_liner 逻辑 | 单测覆盖模型打分 + 限速；生产小流量观察 | V2-P4 | 关 worker，老 fallback 路径保留 |
| **V2-P5.1** | 即时总结 UI 按钮 + 状态机 | UI gate | V2-P5 | 默认隐藏按钮 |
| **V2-P5.2** | 附件 manifest 写入 flow + signed url 撤销 + 审计 + 负向测试 | 与 §8.3 矩阵一致 | V2-P4 | 对象存储降级为请求级 |
| **V2-P6** | 灰度切读：先 1% → 10% → 50% → 100%；观察 dual read 差异 | 命中阈值（差异 < 0.5%；磁盘 p99 不超基线） | V2-P3～P5.2 | feature flag 切回 legacy |
| **V2-P7** | shadow write + dual read 一致性观察窗（≥ 7 天） | p99/错误率/差异率全部达标 | V2-P6 | 关 V2 主读 |
| **V2-P8** | 主读写切到 V2（V1 read-only 兼容层 6 个月） | 生产无回归 | V2-P7 | 关闭 dual write，仅 V1 |

每阶段退出条件：
- 单测 + 集成测试 + Lint 通过
- ADR 决策记录（如有架构变更）
- 对账报告与 runbook 一份
- 配套 Doc + 监控指标 + 告警规则

---

## 11. 风险与回滚

| 风险 | 缓解 | 回滚动作 |
| --- | --- | --- |
| RLS owner filter 性能退化 | 在 `session_dim` 建 `(session_id, tenant_id)` 复合索引 | 关 RLS 仅保留 tenant 隔离 |
| Summary 模型打分不合理 | 指标采集 + 阈值监控 + 租户级覆盖 | 回退到 settings 固定模型 |
| V2 schema 漂移 | feature flag 默认关；回填脚本幂等；schema 监控 | down migration |
| L0 ringbuffer 抖动影响热路径 | 容量 1024，超限淘汰；埋点命中率 | 关 L0，让 L3 直接接管 |
| 附件 signed URL 滥用 | TTL 5min；aud/exp/cert 三段 HMAC；审计 | revoke 接口 + 黑名单立即生效 |
| V2-P4 admin API 写错 RLS | 5 个负向用例 + 真实 DSN 测试 | 切回 legacy compare 调用 |
| SubmitMode 误判 | P0 协作头优先；可手动 admin 标记 | 进 admin 后台修 |

---

## 12. 测试与验收

### 12.1 单元测试

每个包保持现有测试覆盖，新增：

- `cache_v2` 增加 L0/L3 行为
- `submit_mode_detector` 增加 fail-safe 行为
- `instant_summary_selector` 评分单测（11 个场景）
- `turn_attachment_sign` HMAC 单测
- `session_snapshot_updater` 异步聚合单测

### 12.2 集成测试（短码 + 真实 DB）

- `TestRLS_SessionsV2_Turns_OwnerFilter_*`：5 个负向
- `TestAdmin_Sessions_Turns_Cursor`：上/下/中间翻页、过期 cursor、版本切换
- `TestInstantSummary_Worker_EndToEnd`：单会话总结 + 落库 + 失败重试
- `TestAttachment_SignedURL_*`：跨 tenant/session/turn/过期/撤销/篡改 6 个负向

### 12.3 端到端（人工 gate）

- 给一份 10 轮多模态样例请求，页面按双栏倒排 + 抽屉正确呈现
- 即时总结触发后 30s 内落库且不影响页面其他操作
- 附件 signed URL 在 5min 内有效，过期后 410

### 12.4 验收门禁（不允许 CUTOVER-READY 缺失项）

- RLS / 越权读取 = 0
- 漏写 / 重复 / turn_gap = 0
- 双读差异率 ≤ 0.5% 且全部归因到已知根因
- p99 延迟 ≤ 切换前基线 + 20%
- 磁盘增长 ≤ 切换前基线 × 0.5（实测 / 压测）

---

## 13. 文档交付（每阶段须沉淀）

- `docs/会话优化v2/34-V2存储+展示+总结实施记录.md`：本设计的活动 log
- `docs/会话优化v2/35-RLS-Owner-Filter.md`：迁移 + 测试记录
- `docs/会话优化v2/36-Attachment-Manifest-Signed-URL.md`：附件 manifest / signed URL 设计
- `docs/会话优化v2/37-Instant-Summary-Model-Selector.md`：模型打分策略
- `docs/会话优化v2/38-Session-Detail-Page.md`：UI 实施记录
- `docs/会话优化v2/39-Dual-Read-Gate-Report.md`：双读门禁报告
- 更新 `docs/会话优化v2/31-当前实现基线与修正决策.md` 每阶段后

---

## 14. 跨文档冲突解决

- 与 docs/拆分/08 §8.6 阶段一致；新增 V2-P2.4 / P3.1 / P4.1 等子阶段
- 与 docs/拆分/30 §3.2 缺口一致：owner filter + 真实 RLS 测试
- 与 docs/拆分/30 §6 禁止事项保持：未 owner filter 前切 admin 读 / 未真实 RLS 测试前宣验证通过 / 未灰度验证前大流量开启
- 与 docs/会话优化v2/31 权威基线一致：以本文为后续 V2-P2.3+ 实施唯一参考
- 与 docs/拆分/23 迁移规划关系：本方案不直接迁移到 session-manager，但保留写入事实源；session-manager 接入时按 docs/拆分/30 §4.3 路径进行

---

## 附 A：request_logs_hot 写入失败 — 同步调研结果（systematic-debugging Phase 1）

> **状态**：仅证据收集，未提议修复。修复方案待用户审计后单独立项。

### A.1 重要更正

- `request_logs_hot` 是 **独立的 heap 热表**（不是带 `_hot` 后缀的月分区子表）。所有写入 100% 进入 `request_logs_hot`，后台 `bg/partition_manager.go:693 promoteSpecs()` 把 >7 天冷数据搬到 columnar 月分区 `request_logs_YYYY_MM`。
- `partition_date` 字段**不存在**于 `request_logs_hot`；它仅用于 sessions_v2 表。

### A.2 关键写入路径

| 路径 | file:line | 描述 |
| --- | --- | --- |
| 主 INSERT | `domains/hooks/observability/telemetry/client.go:641-717` | `insertRequestLog()` → `INSERT INTO request_logs_hot (...) ON CONFLICT (request_id) DO UPDATE`，85 列，绑定为 `$N::text::jsonb` 规避 pgx 二进制 JSONB 失败 |
| UPDATE 路径 | `domains/hooks/observability/telemetry/client.go:1057-1130` | `updateRequestLog()` → `UPDATE request_logs_hot WHERE request_id=$1`（依赖 migration 455 把 PK 从 `(request_id, ts)` 改为 `(request_id)`） |
| 批量入口 | `domains/hooks/observability/telemetry/client.go:502` | `flush()` → 全部走 `persistRequestLog()` → 失败回退到 `dbdegradation.BackupWriter.WriteRequestLog` |
| 同步路径 | `domains/hooks/observability/telemetry/client.go:400` | `EmitRequestLog()` |
| bodies upsert | `client.go:1377` | `upsertRequestLogBodies()`（同一 tx，紧跟 INSERT 之后） |
| admin 直写 | `admin/telemetry.go:317` | 直接 `INSERT INTO request_logs_hot`（HTTP `/api/telemetry/request-log`，无 bodies） |

### A.3 分区维护链路

- 入口：`bg/partition_manager.go:115 run()`，启动 + 每 `mainTicker`（默认 1h）调 `ensureNextMonthPartitions(ctx)` (`:149`) → `SELECT ensure_request_logs_partition($1)`（`:156`）
- promote 由 `promoteTicker`（默认 1h）触发：`bg/partition_manager.go:130, 141`，函数在 `:693 promoteSpecs()`
- 当前月/下月两轮（offset 0/1）；**partition_manager 自己不写 `request_logs_hot`，只确保下游 columnar 月分区存在**

### A.4 根因假设（按可能性排序）

| # | 可能性 | 描述 | 证据 |
| --- | --- | --- | --- |
| 1 | 🔴 高 | **migration 455 PK 变更 + INSERT 路径不一致** — `INSERT … ON CONFLICT (request_id)` 仅在 `request_id` 是唯一约束时才能 conflict-update；若运行环境 `schema_migrations=455` 没跑成功（PK 仍是 `(request_id, ts)`），则并发写入会抛 `23505 unique_violation` | `sql/migrations/startup/455_request_id_unique_for_hot_tables.sql` 是 2026-07-23 加的；`sql/quick-init-request-logs.sql:30` 仍用 `UNIQUE (request_id, ts)` |
| 2 | 🔴 高 | **INSERT 后紧跟的 `upsertRequestLogBodies` 在同一 tx 失败 → 整 tx rollback** — `client.go:1017` bodies 写失败时 `return err`，等于 `request_logs_hot` 这一行也被回滚 | commit `be56ec2f` 修的就是这个；comment 写明"must tolerate both historical UNIQUE (request_id, ts) and migration-455 UNIQUE (request_id) deployments" → 迁移期 hosts 不一致 |
| 3 | 🔴 高 | **bodies upsert 用 `$N::jsonb` 直接 cast 空对象以外的非法字符串 → SQLSTATE 22P02** | `client.go:1392`；`strPtrToJSON` 现在对非法 JSON 返回 `"null"` 或 `"{}"`（`be56ec2f` #2），但若上游 `RequestBody`/`ResponseBody` 截断 UTF-8 |
| 4 | 🟡 中 | JSONB 字段 `$N::text::jsonb` cast 列表遗漏（`ca95a8e8` 之前未 sanitize 的字段 `auto_decision / compression_meta / outbound_body / outbound_msg_hashes / quality_fix_actions / tool_calls / attachments / routing_attempts`）在 UPDATE 路径仍可能 22P02 | `ca95a8e8` 之前无 sanitize |
| 5 | 🟡 中 | bodies upsert 中 `request_id` 依赖 `request_logs_hot` 同事务 INSERT — `be56ec2f` 注释"previous subquery-based INSERT could return 0 rows under concurrent access" → 迁移期或并发下 bodies 会 0 行成功；主表 INSERT 已成功 → 主表保留但 bodies 缺失（不是主表失败） | `be56ec2f` 注释 |
| 6 | 🟢 低 | partition_manager 自身的 `ensure_request_logs_partition` 抛错 — `bg/partition_manager.go:159-163` 只 slog.Error 然后 `continue`；且 `request_logs_hot` 不依赖任何 partition 存在才能写入（独立 heap 表） | 可基本排除 |
| 7 | 🟢 低 | 252 → 本地 DB 同步期间父表新增 8 列 + 3 索引未在生产应用 | `docs/changelogs/2026-07-24-db-schema-sync-from-252.md` 纯 DDL 同步；若生产没跟进只会在新字段被引用时报错（42703） |

### A.5 需要的现场数据（用于 Phase 2 验证）

- 生产 Postgres 的 `schema_migrations` 表，确认 455、341、449、350、455、`2026-07-13-multimodal-token-fields-hot` 是否全部 `applied`
- 当前 `request_logs_hot` 的实际约束：`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid='request_logs_hot'::regclass AND contype IN ('p','u');`
- 失败时间窗的 `pg_log` / `log_min_duration_statement` 输出（特别 `SQLSTATE 22P02`、`23505`、`22021`）
- 应用层 `slog` 中 `persist request_logs_bodies_hot failed` / `telemetry request db persist failed` stack 切片
- `c.FailCounts()`（`client.go:462`）的 transient/permanent/fallback 计数当前比例

### A.6 后续行动

1. **现场抓取（待用户授权）**：取生产 schema_migrations、pg_constraint、近期 pg_log
2. **诊断日志注入（无修复）**：在 `client.go:1017` 之后加 `slog.Debug` 打印 `len(reqJSON)/len(respJSON)`
3. **修复立项（待 Phase 2/3 验证后）**：单独立项 request_logs_hot 修复；本设计文档不混入

### A.7 范围声明

- 本次未修改任何文件、未运行任何写操作、未写修复 patch
- 仅 grep / cat / git log / git show
- 修复不在本设计 workstream 范围
