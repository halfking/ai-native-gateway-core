# LP9 / LP5 Baseline Report — Request Body Storage Schema

> **状态**：Baseline frozen (pre-LP1)
> **日期**：2026-08-24
> **依赖 plan**：[2026-08-24-request-body-storage-optimization-plan.md](../../docs/04-implementation/plan/2026-08-24-request-body-storage-optimization-plan.md) §4.3 LP5
> **关联审计脚本**：[scripts/check-body-storage-schema.sh](../../scripts/check-body-storage-schema.sh)

## 1. SSOT 列定義（基於 SQL DDL grep）

### 1.1 request_logs_hot（寬熱表）

| 列 | 類型 | 預期狀態 |
|---|---|---|
| request_id, ts, tenant_id, success 等 | 各種 | ✅ 保留（基線必填） |
| **request_body** | **jsonb** | ❌ LP1 必須刪除 |
| **response_body** | **jsonb** | ❌ LP1 必須刪除 |
| **outbound_body** | **jsonb** | ❌ LP1 必須刪除 |

### 1.2 request_logs_bodies_hot（專屬熱表）

5 列：`request_id, ts, request_body, outbound_body, response_body`（SSOT 與 SSOT.yaml SSOT 一致）

### 1.3 request_logs_bodies（分區父表）

5 列：`request_id, ts, request_body, outbound_body, response_body`（與 `_hot` 對齊）

### 1.4 request_logs_bodies_with_current_month（視圖）

- FROM：`public.request_logs_bodies_hot` ∪ `public.request_logs_bodies`
- 投影：5 列（與 `_hot` base 對齊）

## 2. LP5 Audit Script 行為

- ✅ exit 0: schema 匹配 LP9 SSOT
- ❌ exit 1: `request_logs_hot` 仍有 body JSONB 列（pre-LP1 預期失敗）
- ❌ exit 2: psql live cross-check 失敗
- ❌ exit 3: 缺少必要 SQL 文件

## 3. Pre-LP1 Baseline 驗證

```
$ ./scripts/check-body-storage-schema.sh
[LP5 check-body-storage-schema] === LP9 pre-LP1 baseline audit ===
  ...
  ❌ request_logs_hot still has request_body (jsonb) column defined
  ❌ request_logs_hot still has response_body (jsonb) column defined
  ❌ request_logs_hot still has outbound_body (jsonb) column defined
  ❌ FAIL: request_logs_hot still leaks body JSONB columns — LP1 not applied yet
exit=1
```

**判讀**：LP1 尚未 apply，audit 拒絕作為「freeze」基準。

## 4. LP1 Apply 後預期輸出

```
$ ./scripts/check-body-storage-schema.sh
  ✅ request_logs_hot has no request_body column
  ✅ request_logs_hot has no response_body column
  ✅ request_logs_hot has no outbound_body column
  ✅ bodies_hot column set: request_id ts request_body outbound_body response_body
  ✅ bodies_parent column set: request_id ts request_body outbound_body response_body
  ✅ view FROM clause: request_logs_bodies request_logs_bodies_hot
  === PASS ===
exit=0
```

## 5. Live PG Cross-Check（可選）

當 `PGHOST / PGDATABASE / PGUSER / DATABASE_URL` 任一環境變數存在，腳本會跑：

```sql
SELECT table_name || '.' || column_name
  FROM information_schema.columns
 WHERE table_schema='public'
   AND table_name IN ('request_logs_hot','request_logs_bodies_hot','request_logs_bodies')
 ORDER BY table_name, ordinal_position;
```

斷言：`request_logs_hot` 不得有 `request_body / response_body / outbound_body` 任一列。

245/154 staging 必須跑此 cross-check（按 plan §5.2 step-1）。

## 6. Changelog

| 日期 | 變更 | 作者 |
|---|---|---|
| 2026-08-24 | v1.0 baseline frozen (pre-LP1) | LP9 / minimax-m3 |
