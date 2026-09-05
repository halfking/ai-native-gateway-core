# Session Digest 持久化 — P0 真实环境验证报告

**时间**: 2026-09-01 07:50–08:00 (UTC+8)
**目标环境**: 本机 staging PG 容器 `llm-gateway-pg` (`kx-citus-pg17:offline-arm64`, 127.0.0.1:5432, db=`llm_gateway`)
**Git HEAD**: `ea6a5ef503` (含 `bc48559b4 fix(audit-20260901)` + `f8bf429ec fix(admin): harden batch self-check model selection`)
**操作者**: Claude (autonomous)

---

## TL;DR

| 检查项 | 期望 | 实际 | 结论 |
|---|---|---|---|
| 526 applied | ✅ | ✅ 2026-08-17 03:18:51+08 | PASS |
| 636 SQL 在 staging 执行 | PASS | 已执行（之前手工 / 部分 replay） | PASS（重放幂等） |
| `session_turns.digest` JSONB 列 | 存在 | ✅ nullable JSONB | PASS |
| `session_turns_hot.digest` JSONB 列 | 存在 | ✅ nullable JSONB | PASS |
| View `session_turns_with_current_month` 51 列 | 51 | 51 | PASS |
| View `security_invoker=true` | true | true | PASS |
| View `digest` 列 | 存在 | ✅ | PASS |
| `promote_session_turns_hot_to_partition` digest 保留 | true | true（写入 → promote → 分区均带 digest） | PASS |
| `schema_migrations.636` 登记 | 应已 | ❌ 缺失 → 已补登 | PARTIAL → FIXED |
| `llm_gateway_migration_checksums.636` 登记 | 应已 | ❌ 缺失 → 已补登 | PARTIAL → FIXED |
| ACL 损失（view grant） | 无 | 无（staging 仅 owner grant；636 DO 循环保留） | PASS |
| Digest envelope `schema_version=1` / `algorithm_version=deterministic-v1` | true | true（写入后 query 验证） | PASS |
| 是否需要 forward migration 637 | 视 ACL 而定 | 否（staging 无额外 role grant） | **NO 637 NEEDED** |

---

## 1. 重放 636 — 输出

```
BEGIN
NOTICE:  column "digest" of relation "session_turns" already exists, skipping
ALTER TABLE
NOTICE:  column "digest" of relation "session_turns_hot" already exists, skipping
ALTER TABLE
SELECT 1                  ← grant snapshot
DROP VIEW                 ← ACL 清除点
CREATE VIEW               ← security_invoker=true 重建
COMMENT
DO                        ← ACL 重放循环
CREATE FUNCTION           ← promote_session_turns_hot_to_partition 重建
COMMENT
DO                        ← post-condition 校验
COMMIT
```

**关键观察**: 636 已经是 idempotent 设计（`IF NOT EXISTS` + 重新创建 view/function + DO 循环捕获并重放 grants），所以即使过去被部分 replay，重放整文件依然安全。

## 2. ACL 对比（前后无差）

```sql
-- 636 重放前
SELECT grantee FROM information_schema.role_table_grants
WHERE table_schema='public' AND table_name='session_turns_with_current_month';
-- 结果: llm_gateway (owner)

-- 636 重放后（同一查询）
-- 结果: llm_gateway (owner)
```

**结论**: staging 上 view 仅有 owner 的隐式 grant。636 的 ACL 保留 DO 循环正确地捕获并恢复了 owner grant。**无 ACL 损失**。

如果其他环境有额外 `GRANT SELECT` 给报表/应用角色，需在该环境分别跑 636（或 637 forward migration）后再核对比对。staging 不需要 637。

## 3. Promote digest 验证

**写入 hot 行**（用合规 check 约束枚举：`injection_verdict=pass`, `output_verdict=pass`, `source_kind=live`, `quality=inferred`, `submit_mode=full`）：

```sql
INSERT INTO session_turns_hot (..., digest := {
  "schema_version": 1,
  "algorithm_version": "deterministic-v1",
  "generated_at": "...",
  "source": "session_v2_writer",
  "payload": {
    "user_input": "P0 digest test prompt",
    "assistant_output": "P0 digest test response",
    "metrics": {...},
    "tool_usage": {"tools_used": ["web_search"]}
  }
}) RETURNING id, (digest->>'schema_version'), (digest->>'algorithm_version');
-- id=99990001, sv=1, av=deterministic-v1
```

**跑 promote**（`ts < NOW() - 7 days`，`p_batch_size=5000`）：

```
NOTICE: relation "session_turns_2026_08" already exists, skipping
moved = 1
```

**结果**:

| 位置 | id | partition_date | tableoid | sv | source |
|---|---|---|---|---|---|
| `session_turns_hot`（after） | — | — | — | — | — |
| `session_turns` parent（查分区） | 99990001 | 2026-08-02 | `session_turns_2026_08` | 1 | session_v2_writer |

**Digest JSONB 完整迁移**: ✅ schema_version=1、algorithm_version=deterministic-v1、source=session_v2_writer、tool_usage 全部保留到月度分区。

**清理**: `DELETE FROM session_turns WHERE id=99990001` — staging 干净。

## 4. Migrations 表补登

```sql
INSERT INTO schema_migrations (version, description, applied_at)
  VALUES ('636', 'persist nullable turn digest JSONB in parent and hot tables', NOW())
  ON CONFLICT (version) DO NOTHING;

INSERT INTO llm_gateway_migration_checksums
  VALUES ('636', 'session_turns_digest',
          'a7e1909b0eb5fac03253c77fafb3cb029a688195c9666b41c739db24746e6af2',
          NOW());
```

**Checksum 修正**: handoff_digest_20260901.md 登记 `375d376e…`，但实际部署的是 `a7e1909b…`（来自 commit `c5618ba7e fix(session): preserve view grants and bound digest text in migration 636`）。已在 2026-09-01 后续会话将 handoff_digest_20260901.md 的 checksum 行更新为 `a7e1909b…`。

## 5. 回归测试

```
go test ./domains/sessiondigest ./domains/session/v2 ./admin -run 'Test.*(Session.*Turn|Turn.*Digest|Persisted.*Digest|Digest.*Fallback|HandleTrigger.*ProbeEnqueueError|Build.*ToolArguments|Build.*ReturnsNilWithoutUsefulContent)'

ok  	github.com/kaixuan/llm-gateway-go/domains/sessiondigest	(cached)
ok  	github.com/kaixuan/llm-gateway-go/domains/session/v2	39.291s
FAIL	github.com/kaixuan/llm-gateway-go/admin	36.307s
    --- FAIL: TestHandleTrigger_ProbeEnqueueError
    expected 503 on enqueue failure (got 200, body={...results: glm-5.2: error="db down"...})
```

**冲突**: `TestHandleTrigger_ProbeEnqueueError` 期望 503，但 handler fan-out 分支仍返回 200 + 内嵌 `results[model].error`。

详细分析见 §6。

## 6. ⚠️ TestHandleTrigger_ProbeEnqueueError 契约冲突 — 待用户决策

### 时间线

| 时间 | Commit | 内容 |
|---|---|---|
| 06:25 | `aab2f0ca7` | 我把 test 改为 200 + 内嵌错误（与当时 handler fan-out 一致） |
| 07:34 | `f8bf429ec` | team 重写 `self_check_handlers.go` 引入"单模型 enqueue 失败 → 503"契约 |
| 07:43 | `bc48559b4` | team audit 修复把 test 改回 503，并明确注释：<br/>"single-model probeEnqueue path now returns 503 immediately on enqueue failure, preserving the original single-model API contract" |

### 当前实际状态

| 维度 | 期望（test） | 实际（handler） |
|---|---|---|
| 单模型 enqueue 失败 | 503 + `{error, message}` | **200** + `{ok, enqueued, results: {model: {error, enqueued:0}}, models_failed, ...}` |
| 多模型 enqueue 部分失败 | （未明确定义） | 200 + `models_failed > 0` + 每模型内嵌 error |

### 这是个 audit 团队未完成的修复

`bc48559b4` 提交说明显式声称 handler 已改为"立即 503"，但 `self_check_handlers.go:683` 的 `writeJSON(w, http.StatusOK, ...)` 仍然返回 200。**handler 没有同步更新**。

### 决策选项

**A. 修复 handler**（完成 team 未完成的 audit 修复 — 推荐）
- `admin/self_check_handlers.go:683` 改为：若 `failedModels == len(models)` → 503；否则 200 + 内嵌 errors
- 与 `bc48559b4` 注释契约一致；保留 fan-out 部分失败语义
- 影响：UI 自检页面触发按钮在"全部失败"时显示红色（与 503 状态码一致）

**B. 改 test 期望回 200**（与 handler 实际行为一致）
- 保留 fan-out 设计哲学（前端拿到所有模型结果）
- 但**违背 `bc48559b4` 已声明的契约**，需要额外 commit 解释 revert 理由
- 风险：与 f8bf429ec 引入的"503 fast-path"语义不一致

### 推荐：选项 A（修 handler），理由

1. `bc48559b4` commit message 已明示契约；不修就是 contract drift
2. 245/154 部署后 UI 自检会看到 200 OK 但所有模型都 enqueue 失败 — 误导操作员
3. 单模型接口（spec 原本的）确实应该 503

**等待用户决策后继续 P1 修复 dev 502 与晋级**。

## 7. P0 阶段总结

✅ **digest 持久化端到端验证通过**：636 重放幂等、digest JSONB 落盘正确、promote 携带 digest、ACL 无损失。
✅ **handoff_digest_20260901.md §P0 全部 6 项 PASS**（仅 checksum 文案需小幅修正）。
❌ **test 期望冲突暴露 audit 未完成修复** — 需用户决策 §6 选 A 或 B。

## 8. P1 路线（待用户决策 + 后续会话执行）

1. **解决 §6 契约冲突**（阻塞 admin 回归测试 PASS）
2. **修复 dev llm.itestu.cn 502**（upstream 网关挂了，与代码无关；需运维介入重启 `llmgo-245` service 或检查 nginx upstream）
3. **`--promote-245 --apply`**：digest 在真实流量下的命中率验证
4. **`--promote-154 --apply`**：245 gate 通过后
5. **7 天观测**：admin digest hit rate、fallback 频次、NULL ratio、avg JSONB size
6. **可选 backfill**（仅 fallback rate > 30%）
7. **可选 GIN 索引** `idx_session_turns_digest_gin`（若观测期反馈 top-N 工具查询需求）
