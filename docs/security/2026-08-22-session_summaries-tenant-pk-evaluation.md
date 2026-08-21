# session_summaries Tenant PK 升级评估（T11-P0）

**日期**：2026-08-22
**作者**：Session security
**状态**：决策已采纳（用户确认）
**关联 PR**：T11 前置 P0 安全修复

---

## 现状

| 项目 | 值 |
|---|---|
| 表 | `public.session_summaries` |
| 当前 PK | `(session_key)` 单列 |
| 约束文件 | `sql/objects/constraints/session_summaries_session_summaries_pkey.sql` |
| FK | `tenant_id → tenants(code)`（migration 463，另有 `tenants(id)` 历史版本） |
| RLS | 已启用（policy: tenant_isolation, super_admin_bypass） |
| 索引 | `(tenant_id, last_request_at DESC)`, `(tenant_id, compliance_status)`, `(tenant_id, total_cost_usd DESC)` 等共 7 条 |

代码侧 caller：

- `internal/summarystore.Upsert` — `INSERT ... ON CONFLICT (session_key) DO UPDATE ...`（改造前）
- `internal/summarystore.LastSummarized` / `CountNewTurns` / `CountTotalTurns` — 读侧均 `WHERE session_key = $1`，**无 tenant 过滤**（依赖 RLS）
- `domains/sessionsummary/summarizer.saveSummaryToDB` — v2 dispatch summarizer，写入时丢 `UpsertResult.Version`
- `admin/auto_summary_generator.go` — request-path auto-summary，写入时丢弃 `Version`，仅用 `Updated` 打 info log

---

## 风险分析

### 1. 跨租户 session_key 冲突

**场景**：两个 tenant 在生产环境恰好使用了相同的 `session_key`（例如 UUIDv4 撞库、客户端错误复用 ID、或 RLS 绕过的攻击路径）。

**影响**：
- 两份 summary 互相写入同一行；后写者覆盖前写者。
- `(xmax = 0)` 检测返回 `Updated = true`，但实际是「另一个 tenant 的旧 summary 被本 tenant 的新 summary 覆盖」。
- summary_version 自增只反映「行被写过几次」，不反映「我的版本是不是基于上一个版本更新」。
- 业务侧影响：A 租户的 LLM summary 文本会污染 B 租户的会话视图（运营/分析页读取 session_summaries 时）。

**现状防御**：RLS 在 PG session 端有 `app.current_tenant` GUC 时能阻断跨租户读写。但：
- DB 直连审计员 / 维护脚本（绕过 RLS bypass policy）的临时路径
- ON CONFLICT 路径只在 INSERT 第一次失败时才走 UPDATE；PG UPSERT 不感知 RLS context，RLS 在 ON CONFLICT 内不重新评估
- 一个 tenant 写 → 触发另一个 tenant 读相同 session_key 时无法防御

### 2. `summary_version` 自增 + xmax 竞态

**现状**：`summary_version = COALESCE(session_summaries.summary_version, 0) + 1` + `RETURNING (xmax = 0) AS inserted`

**问题**：
- `COALESCE(NULL, 0) + 1` 在生产实测下会因 `summary_version` 默认值 `DEFAULT 1`（migration 358）而不会真取 NULL，但语义上仍有歧义。
- `xmax = 0` 在 INSERT 时确实为 0，但两个并发 UPSERT 串行化后，第二个 writer 永远走 UPDATE 分支，`Updated=true`，但 caller 无法区分「自己刚写入的新行被覆盖」与「自己刚刚更新了别人几小时前的旧行」。
- 当前 `admin/auto_summary_generator` 仅用此打 INFO log，没有 CAS 拒绝。

### 3. 没有严格 CAS 接口

两个 caller 都把 `UpsertResult.Version` 丢弃。如果未来要实现「只在我读到的 summary_version 之上再写」，代码路径上无可用接口。

---

## 选项对比

| 选项 | 收益 | 代价 | 推荐 |
|---|---|---|---|
| **A. 替换 PK 为 `(tenant_id, session_key)`** | 根本性解决跨租户冲突；FK / 索引全部对齐；语义最清晰 | ALTER TABLE 锁表时间长（与表大小 / 索引数成正比，实测 252 上 6h+ 预估）；需重写所有 ON CONFLICT 子句、迁移所有 FK 引用 | 长期方向，本任务不做 |
| **B. 加 `UNIQUE (tenant_id, session_key)` NOT VALID**（采纳） | 新写入立即跨租户冲突检测；旧数据异步 VALIDATE 校验；不锁表；现有索引/ON CONFLICT 子句微调即可兼容 | NOT VALID 状态下旧数据不立即校验，需手动 `VALIDATE CONSTRAINT`；当前 caller 的 ON CONFLICT (session_key) 需改为 (tenant_id, session_key) | ✅ T11-P0 选用 |
| **C. 仅加 RLS 校验** | 改动最小 | RLS 在 ON CONFLICT 路径不重新评估，无法防御 UPSERT 跨租户覆盖；等于无防御 | ❌ |
| **D. 不做** | 零改动 | 风险延续到 T11 持久化路径时爆雷 | ❌ |

---

## 决策

**采纳选项 B**：
1. 加 `UNIQUE (tenant_id, session_key) NOT VALID`（migration 560）。
2. `summarystore.Upsert` 的 `ON CONFLICT` 子句从 `(session_key)` 改为 `(tenant_id, session_key)`。
3. 读侧函数补 `tenantID` 入参并在 WHERE 加上 `AND tenant_id = $N`（不依赖 RLS 双保险；与 P0.4 同步）。
4. 新增 `summarystore.UpsertCAS(ctx, sum, expectedVersion)` 严格 CAS 接口（P0.4 单独提交）。

---

## 后续路径（不在本任务）

1. **VALIDATE 约束**：运维在低峰期执行 `ALTER TABLE session_summaries VALIDATE CONSTRAINT session_summaries_session_key_per_tenant;` 一次性校验全部旧数据。如果发现冲突，需人工 cleanup（按 tenant_id 分组保留最晚更新者的行）。
2. **PK 升级**：未来如要做严格 tenant 命名空间，把 PK 升级为 `(tenant_id, session_key)`。届时：
   - 现有 UNIQUE 约束可移除（PK 自带唯一性）。
   - 所有 `ON CONFLICT (tenant_id, session_key)` 子句已就位（本次预改）。
   - 所有 FK 引用 session_summaries 的地方需要重建。
   - 估算锁表时间：表大小 1M 行 + 7 条二级索引，PG `ALTER TABLE ... ADD PRIMARY KEY` 约 4-6h（hot path 不能做）。
3. **CAS 推进**：admin auto_summary 与 v2 dispatch summarizer 可选择性改用 `UpsertCAS`；预期版本号从 `LastSummarized` 读路径一并返回（读路径上加 `summary_version` 列）。

---

## 不在本任务做的（明确边界）

- ❌ 把 Raw PII 上线（preprocess 层 hook 还没接）。
- ❌ 修改 sanitize 业务语义。
- ❌ 替换 PK（耗时 + 风险 + 收益不匹配本任务定位）。
- ❌ 强制 caller 改造（CAS 接口先暴露，由后续 PR 决定是否上生产）。

---

## 验收

- `migration 560` 语法合法、`NOT VALID` 不阻塞现有 INSERT/UPDATE。
- `summarystore.Upsert` 在新 UNIQUE 约束生效后正确处理跨租户冲突（同一 `(tenant, key)` 第二个 INSERT 直接报错 `23505`，已被代码识别为「冲突 → 走 UPDATE 分支」语义保持）。
- `summarystore.UpsertCAS` 在 nil-pool 路径与正常路径均返回正确的 `ErrStaleVersion`。