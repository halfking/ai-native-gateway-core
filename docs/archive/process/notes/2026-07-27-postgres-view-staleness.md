# 2026-07-27 经验笔记：Postgres 视图列管理

## TL;DR

**Postgres 视图在 `CREATE OR REPLACE VIEW` 时冻结列清单。`ADD COLUMN` 不会传播到视图。**

每次 `ALTER TABLE ... ADD COLUMN` 后，必须配对一条「重建引用该表的所有视图」的 migration，否则 SELECT 会命中 SQLSTATE 42703。

## 触发场景

```
Migration 443 (2026-07-14): ADD COLUMN agent_name/agent_type/client_protocol
Migration 448 (2026-07-20): 重建 request_logs_with_current_month 视图，补齐 routing_attempts/routing_summary
Migration 458 (2026-07-27): ADD COLUMN canonical_model

→ 视图缺 agent_name/agent_type/client_protocol/canonical_model 4 列
→ admin/logs.go:requestLogsListCols 新增这 4 列 SELECT
→ /api/logs/{id} 报 SQLSTATE 42703 → 前端抽屉 "query failed"
```

## 修复路径

1. **视图重建 migration**（沿用 448 的幂等模式）：
   ```sql
   -- 读旧视图列清单
   SELECT string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum)
     INTO base_cols
     FROM pg_attribute a
     JOIN pg_class c ON a.attrelid = c.oid
    WHERE c.relname = '<view_name>'
      AND a.attnum > 0 AND NOT a.attisdropped
      AND a.attname <> ALL(ARRAY[<new_cols>]);

   -- DROP + CREATE OR REPLACE
   EXECUTE 'DROP VIEW IF EXISTS <view_name>';
   EXECUTE format('CREATE VIEW <view_name> AS SELECT %s, %s FROM <hot> UNION ALL SELECT %s, %s FROM <parent>',
                  base_cols, new_cols, base_cols, new_cols);

   -- 末尾 smoke
   PERFORM <new_cols> FROM <view_name> LIMIT 1;
   ```

2. **静态回归测试**（无 DB）：
   - 解析 `requestLogsListCols`，断言每列在 `requestLogRow` JSON tag 集合里
   - 断言 4 列 `canonical_model/agent_name/agent_type/client_protocol` 必现
   - 见 `admin/logs_view_test.go`

## 未来防御清单

### LLM Agent（rule 49 schema-truth-first 补强）

- ✅ 写 SELECT 之前必 probe 视图列（`information_schema.columns` / `psql \d+ <view>`）
- ✅ 跨表类比前先跨视图类比 — "A 表有 x 列" 不代表 "view 也暴露 x 列"
- ✅ 改 `requestLogRow` 字段或 `requestLogsListCols` 常量后必须跑 `go test ./admin/ -run 'TestRequestLogRowColumnAlignment'`
- ✅ `requestLogsDetailCols` 必须由 `requestLogsListCols` 派生（`const requestLogsDetailCols = requestLogsListCols + `），禁止并行手写两套

### 部署流水线（rule 03 §6.0 补强）

- L4 业务真实验证必须包含 dashboard 主交互：
  ```bash
  # 部署后 smoke：拉一条最近 request_id，curl /api/logs/{id}
  RID=$(psql ... -t -A -c "SELECT request_id FROM request_logs_with_current_month WHERE ts >= now() - interval '1 hour' ORDER BY ts DESC LIMIT 1")
  curl -sS -b admin.jar "https://<host>/api/logs/$RID" -o /dev/null -w "%{http_code}\n"  # 必须 200
  ```
- 当前部署脚本 `scripts/deploy-{245,154}.sh` 只校验 healthz/DB；缺少 API 业务校验
- 建议加 `scripts/post-deploy-smoke.sh` 在 deploy-seamless.sh 末尾自动跑

### SQL 脚本管理（rule 38 §8 补强）

> 新增「视图引用追踪」章节：
> - 所有 `ALTER TABLE ADD COLUMN` migration 必须列出受影响的视图
> - migration 文件末尾 `Dependencies:` 段落必须引用视图名 + 重建 migration 号

## 同类事故历史

| 时间 | 事故 | 文件 |
|---|---|---|
| 2026-07-20 | migration 448 修复 routing_attempts 视图缺列 | sql/migrations/startup/448_request_logs_view_routing_attempts.sql |
| 2026-07-27 | **本次**：443+458 后视图缺 4 列 | sql/migrations/startup/459_request_logs_view_client_perception.sql |
| 2026-07-16 | jsonb_array_length(JSON null) 抛 22023 | 列表接口 silent failure |

## 完整诊断报告

`docs/incidents/2026-07-27-live-stream-detail-drawer-query-failed.md`