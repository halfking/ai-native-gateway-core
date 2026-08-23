# 2026-08-23 — session_analysis_metadata provisional 持久化（migration 567）

## TL;DR

arrival 路径在 provisional title 投影之外，异步 UPSERT 完整 `sessionmeta.Result` 到 `session_analysis_metadata`（`status=provisional`）。`input_hash` 不变则跳过更新；无 user title 时仍写 metadata。

## What changed

### 数据库

- `sql/migrations/startup/567_session_analysis_metadata.sql` + `.down.sql`
- `sql/objects/tables/session_analysis_metadata.sql` — SSOT

### 后端

- `domains/analysis/sessionmeta/store.go` — `MetadataStore.UpsertProvisional`
- `domains/analysis/sessionmeta/store_test.go` — pgxmock
- `admin/handler.go` — `analysisMetadataStore` 接线
- `admin/auto_title_generator.go` — `commitProvisionalArrival`（先 metadata，再 title）
- `admin/auto_title_provisional_test.go` — 扩展 metadata 路径测试

## 验证

```bash
go test ./domains/analysis/sessionmeta/... ./admin -run 'Provisional|MetadataStore' -count=1
```

## 未完成（移交下一棒）

- 154 部署 + migration 567 应用 + DB 集成验证
- final stage worker UPSERT `status=final`
- `session-overview` owner_user 过滤对齐（需产品确认）
