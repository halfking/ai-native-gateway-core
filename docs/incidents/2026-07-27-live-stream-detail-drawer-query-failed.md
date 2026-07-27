# 实时请求流 → 原始请求详情 抽屉 `query failed` 诊断报告

**诊断时间**: 2026-07-27 13:55
**影响范围**: llm-gateway-go 154 / 245 全环境
**症状**: 仪表盘「实时请求流」点击任意记录 → 弹出"原始请求详情"抽屉时显示错误
**严重度**: P1（核心 dashboard 功能不可用）

---

## 根本原因（Root Cause）

**Postgres 视图冻结列清单（frozen column list），而 ADD COLUMN 不会自动传播，导致 SELECT 命中 `column does not exist` (SQLSTATE 42703)。**

### 完整调用链

```
1. UI 点击泳道条目 → <LiveRequestStreamV2 @open-detail="openRequestDetail">
   ↓
2. <RequestLogDrawer :request-id="activeRequestId"> watch → getRequestLogDetail(id)
   ↓
3. api.getRequestLogDetail  → GET /api/logs/{request_id}
   ↓
4. admin.Handler.handleLogs → admin.Handler.getLog
   ↓
5. db.QueryRow(SELECT rl.canonical_model, rl.agent_name, rl.agent_type, rl.client_protocol,
                ... FROM request_logs_with_current_month rl ...)
   ↓ ❌ pgx 抛 SQLSTATE 42703 "column rl.canonical_model does not exist"
   ↓
6. handler 捕获 err != nil && !sql.ErrNoRows → writeError "query failed"
   ↓
7. 前端 catch → drawer.error.value = "query failed" → 抽屉红字
```

### 为什么视图缺列

| 时间 | Migration | 动作 | 视图是否同步？ |
|---|---|---|---|
| 2026-07-14 | `443_observability_fields.sql` | ADD COLUMN `agent_name`/`agent_type`/`client_protocol` 到 `request_logs` + `request_logs_hot` | ❌ 否 |
| 2026-07-20 | `448_request_logs_view_routing_attempts.sql` | 重建视图补齐 `routing_attempts`/`routing_summary` | ✅ 是，但只补了这两个 |
| 2026-07-27 | `458_request_logs_canonical_model.sql` | ADD COLUMN `canonical_model` 到两侧 | ❌ 否 |

Postgres 规则：**`CREATE OR REPLACE VIEW` 不会读取底层表当前列清单**，而是使用提交时的字面量列清单。`ADD COLUMN` 在 PG 11+ 会自动传播到子表（包括 partitioned child），但**不会**传播到引用这些表的视图。

因此视图最后一次重建（448）后新增的所有列，都对 `SELECT rl.<new_col>` 不可见 — 默默变成运行时 42703。

### 触发时间线

1. 2026-07-26 部署 `2.4.8-9ffe6ce3`（带 migration 458，但代码还没 SELECT `rl.canonical_model`）— 视图缺列，但没人访问
2. 2026-07-27 14:00 部署 `2.4.9-73e4cff8`（commit `7edaf19a9`，同时引入 `bg.PeriodicQuotaProbe`）— `admin/logs.go:requestLogsListCols` 新增 `rl.canonical_model, rl.agent_name, rl.agent_type, rl.client_protocol` 四列 SELECT
3. 2026-07-27 14:06 部署完成（healthz OK，未做"详情点击"端到端验证）— **触发**
4. 用户实测 → "原始请求详情"红字 `query failed`

---

## 修复

### 1. 视图重建（migration 459）

`sql/migrations/startup/459_request_logs_view_client_perception.sql`：
- 沿用 migration 448 的「读旧视图列清单 + 追加 + DROP+CREATE」幂等模式
- ADD COLUMN IF NOT EXISTS 兜底（443/458 未跑环境）
- 末尾 `information_schema.columns` 检查 + smoke `PERFORM agent_name, ... LIMIT 1`
- 防护历史 hot/parent 类型漂移（不直接 SELECT *，避免 customer_id text vs bigint 类列不同步问题）

### 2. 静态回归测试（`admin/logs_view_test.go`）

两条不依赖 DB 的单元测试：
- `TestRequestLogRowColumnAlignment`：解析 `requestLogsListCols` 的列名（含 AS alias / table 前缀 / `::cast`），断言每个列都对应 `requestLogRow` 的 JSON tag。SELECT 与 Scan 任何一边漂移立刻失败。
- `TestListColsReferenceClientPerceptionColumns`：断言 `canonical_model / agent_name / agent_type / client_protocol` 4 列在 SELECT 列表里。这是本次事故的精确回归护栏。

### 3. 验证

| 验证项 | 结果 |
|---|---|
| `go vet ./admin/...` | ✅ |
| `go build ./...` | ✅ |
| `go test ./admin/ -count=1 -short` | ✅ 2.845s |
| PG schema 验证（information_schema.columns） | ✅ 6/6 列存在（4 个 client-perception + 2 个 routing） |
| `/api/logs/{id}` HTTP 响应 | ✅ 200，68 字段，4 个新列均在 JSON |
| browser-use 实测抽屉 | ✅ 无 drawer-error，含 `请求ID/时间/模型/出站/状态/延迟/Token/Session/Task` |
| 245 部署 | ✅ build_seq 1408，迁移 459 + 460 应用 |
| 154 部署 | ✅ build_seq 1409，healthz 200，admin cookie 鉴权通过 |

---

## 经验教训

### 1. Postgres 视图是冻结的（最关键）

> **规则：每次 ADD COLUMN 后，如果该表被视图引用，必须在同 migration 或下一个 migration 重建视图。**

pgx 不会在连接时校验列存在；`Scan` 在第一次 row 后才报错。**列错位是 silent bug 的温床**。

**未来防御**：
- 所有 ADD COLUMN 的 migration 必须配对一条「view 重建」步骤（除非确认无视图引用）
- 自动化：CI 加 `psql -c "\d+ <view>"` 校验步骤
- LLM Agent 写 SQL 时，schema probe（rule 49）必须包含视图列检查

### 2. 部署前必须做「业务真实」验证（rule 03 §6 L4）

> 154 端 healthz 200，metrics 200，未授权 /v1/chat/completions 401 — **所有 L1/L2/L3 检查都过了，但 L4 失败**。

**未来防御**：
- 部署后强制跑一个 smoke 流程覆盖关键 UI 交互（live stream click → detail drawer）
- 或在 e2e 测试里加 `GET /api/logs/<known_id>` 断言 200

### 3. SELECT-vs-Scan 顺序必须保持对齐

`admin/logs.go:requestLogRow` 有 70+ 字段 + `requestLogsListCols` 是巨型常量。任何一边少加列，另一边都失配 — **pgx 不会报"列数不匹配"，只会按位置错位**。

**未来防御**：
- 已加 `TestRequestLogRowColumnAlignment` — 静态校验每次 build 时跑
- 长远：考虑用 `sqlx` 或 generated wrapper 自动派生 Scan 顺序

### 4. 同类事故历史

| 时间 | 事故 | 同模式 |
|---|---|---|
| 2026-07-20 | migration 448 修复 `routing_attempts/routing_summary` 视图缺列 | ✅ 是 |
| 2026-07-16 | migration 341 + 后续 list 接口 `jsonb_array_length(JSON null)` 抛 22023 | 近似：JSONB literal null vs SQL NULL |
| 2026-07-27 | **本次**：migration 443 + 458 后视图缺 4 列 | ✅ 完全同模式 |

**结论**：视图列管理是高发事故源，建议在 `rule 38`（SQL 脚本管理）追加一段：「所有 ADD COLUMN migration 必须附 view-rebuild 步骤」。

---

## 后续动作

1. ✅ migration 459 已部署到 245 / 154（commit `a00a224a1`）
2. ✅ 截图证据 `docs/ui-verification/ui-verify-request-detail-drawer-20260727.png`
3. ✅ CHANGELOG.md Unreleased 段补全
4. ⏳ 154 license-restricted mode 修复（独立 issue，与本次无关）
5. ⏳ rule 38 / rule 49 补强「ADD COLUMN ↔ view rebuild」强制条款
6. ⏳ `llm-gateway-deploy-test/test.sh` 在 macOS zsh 下因 `set -u` + `env-injector.sh INVENTORY_OVERRIDE` 卡住 — 走 `scripts/deploy-245.sh` / `deploy-154.sh` 绕过即可