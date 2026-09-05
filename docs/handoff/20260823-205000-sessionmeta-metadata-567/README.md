# session_analysis_metadata 567 — 部署与后续

| 字段 | 值 |
| --- | --- |
| 移交 ID | `20260823-205000-sessionmeta-metadata-567` |
| 分支 | `main`（567 持久化已合入） |
| 前置 | migration 568 已在 154；567 未应用 |

## 已完成

- migration 567 + `MetadataStore.UpsertProvisional`
- `commitProvisionalArrival`：metadata 先于 title；无 title 仍写 metadata
- pgxmock + admin provisional 测试

## 下一棒 P0

1. **154 部署**（仅 154）：bump build_seq → `bash scripts/deploy-154.sh --no-frontend`
2. **确认 migration 567 applied**：`session_analysis_metadata` 表存在
3. **集成验证**：
   - 新 session 首轮 chat arrival → `status=provisional` 行存在，`payload` 含完整 Result
   - 同 session 第二轮相同 input → `updated_at` 不变（input_hash 跳过）
   - 仅有 system message → 有 metadata 行、无 title

## 下一棒 P1

- [ ] final stage LLM worker → UPSERT `status=final`（见设计 doc §7）
- [ ] `session-overview` 改用 `appendDashboardScope(..., filterOwner=true)`（产品确认）
- [ ] 154 跑 `node_health_test.go` + `cost_stats_test.go`
- [ ] 154 恢复 minimax-m2.7 至少一条 routable binding（chat E2E）

## 关键 SQL

```sql
SELECT tenant_id, scoped_session_id, status, input_hash, updated_at
  FROM public.session_analysis_metadata
 WHERE scoped_session_id = '<gw_session_id>'
 ORDER BY updated_at DESC;
```
