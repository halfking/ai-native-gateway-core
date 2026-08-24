# 2026-08-25 — Reorder scope hash drift on disabled-provider / manual-disabled credentials

> **TL;DR**：拖动排序在管理面板上一直报 `incomplete candidate set, refetch and retry (scope=15, submitted=11)`。根因是写入侧 scope SQL（`/api/routing/candidate-bindings/reorder`）的 `LEFT JOIN` 没过滤 admin 已隐藏的 provider / credential，而 `/api/routing/resolve` 视图早就过滤了 — 两边计数对不上，乐观并发永远失败。本 PR 把写入侧 scope SQL 改成与 resolve 视图对齐的 INNER JOIN，并补迁移 574 让所有历史 scope 行的 `scope_hash` 重新收敛；不动 `scope_version`，不重置前端 drag state。

## 1. 现象

- 仪表盘 `https://llm.kxpms.cn/routing-v2?tab=resolve` 任意模型拖动一行重新排序，立刻 `PATCH 409`：

  ```json
  {
    "error": {
      "detail": "incomplete candidate set, refetch and retry (scope=15, submitted=11)"
    }
  }
  ```

- 后端日志（commit `f63dbad12` 之前）：

  ```
  level=error msg="reorder scope mismatch" method=PATCH path=/api/routing/candidate-bindings/reorder
      scope_count=15 submitted_count=11 model=claude-sonnet-4-5 user_id=...
  ```

- 同一 UI 在另一台部署（gateway 1652 build_seq）上能正常保存；只有最近升级到 1692 / 567 migration 后才稳定出现。

- 不重启 gateway、不改凭据，反复 refetch → drag 都会失败；说明不是网络层或浏览器缓存问题。

## 2. 根因分析

### 2.1 Dashboard 视图路径 vs 写入侧 scope 路径不一致

| 路径 | 数据来源 | 是否过滤 `providers.enabled` | 是否过滤 `credentials.manual_disabled` |
|------|----------|-------------------------------|----------------------------------------|
| `/api/routing/resolve` | 视图 `v_routable_credential_models` | ✅ `AND p.enabled IS TRUE` | ✅ 通过 `credentials` JOIN 隐含 |
| `/api/routing/candidate-bindings/reorder` 写库前的 scope 查询 | `reorderScopeSQL` / `reorderScopeByCanonicalSQL` 直接 `LEFT JOIN credential_model_bindings` | ❌ 无过滤 | ❌ 无过滤 |

写入侧 scope 查询返回 15 行（包含 4 个被 admin 隐藏的 provider/credential 绑定），dashboard 渲染只显示 11 行 — PATCH 携带 11 个 binding，乐观并发比较 `scope_count=15 submitted_count=11` 失败，前端收到 `incomplete candidate set` 必须 refetch。

### 2.2 触发器 hash 也没过滤（migration 541 / 568 / 571）

`sync_scope_revision_on_binding_change()` 的 hash 公式对所有绑定 `LEFT JOIN` 后直接 `array_agg(cmb.credential_id)` — 这意味着：

- 一个被 `providers.enabled = FALSE` 的 provider 仍然计入 `scope_hash`；
- 一个 `credentials.manual_disabled = TRUE` 的 credential 仍然计入。

只要任一边变更（admin toggle disable / enable），`scope_hash` 会跳变，但 resolve 视图从来不返回这些 binding —— 用户视角永远"scope 大于 visible"。

### 2.3 旧迁移 541 / 568 / 571 没补 INNER JOIN 是历史债

567 之前 migrate 团队专注 raw_model 与 canonical_id 双轨 — 没有人回头补 541/568/571 的 hash 过滤；本次在 574 一次性补齐。

## 3. 修复

### 3.1 Go 代码侧

`internal/repo/credential_models.go`（写入侧 scope SQL）：

```diff
- LEFT JOIN providers p ON p.id = pm.provider_id
- LEFT JOIN credentials c ON c.id = cmb.credential_id
+ JOIN providers p ON p.id = pm.provider_id AND p.enabled = TRUE
+ JOIN credentials c ON c.id = cmb.credential_id AND COALESCE(c.manual_disabled, FALSE) = FALSE
```

两个 `ensureScopeRevision` 种子查询同步改成 INNER JOIN（确保持久化的 `scope_hash` 与 dashboard 视角一致）。

### 3.2 SQL 迁移（migration 574）

`sql/migrations/00000000000000/0574_reorder_scope_disabled_filter.{up,down}.sql`：

- 替换 6 个 statement-level bump 函数（raw_model ×3 + canonical ×3）+ `pm_update` 的 `LEFT JOIN` 为 INNER JOIN；
- 替换 `ensure_scope_revision_seed` 系列为 INNER JOIN；
- up 末尾一次性收敛：

  ```sql
  WITH hashed AS (
      SELECT s.id,
             /* 与新函数相同的过滤 + hash 表达式 */
             compute_scope_hash(s.scope_kind, s.model_key, s.credential_label) AS new_hash
        FROM scope_revisions s
  )
  UPDATE scope_revisions sr
     SET scope_hash = h.new_hash
    FROM hashed h
   WHERE sr.id = h.id
     AND sr.scope_hash IS DISTINCT FROM h.new_hash;
  ```

  - 只动 `scope_hash`，不动 `scope_version` — 前端正在进行的 drag 不会因为版本 bump 而崩；
  - `IS DISTINCT FROM` 避免全表无意义写。

- down 文件用 LEFT JOIN 形态回滚（理论上不需要，但 audit 要求对称）。

### 3.3 测试

| 测试 | 覆盖 | 类型 |
|------|------|------|
| `TestReorderScopeSQL_ExcludesDisabledProviderBinding` | 1 disabled provider + 1 enabled，scope 只含 enabled | PG 集成（`LLM_GATEWAY_PG_URL` 未设时 skip） |
| `TestReorderScopeSQL_ExcludesManualDisabledCredential` | 1 manual_disabled credential + 1 active，scope 只含 active | PG 集成 |
| `TestEnsureScopeRevision_FilterConvergentHash` | ensure 写入的 `scope_hash` 与 resolve 视图返回的 credential_id 集合哈希一致 | PG 集成 |
| `migration_574_test.go` | 文件锁定：INNER JOIN gate ≥7 处 + recompute CTE 存在 + down 回到 LEFT JOIN | 迁移契约 |

`go test ./admin/ ./sql/migrations/startup/` 全 PASS；集成测试在 CI 上有 PG 环境才会跑。

## 4. 影响面

- ✅ `/api/routing/candidate-bindings/reorder`：P0 — 主要修复对象。
- ✅ `/api/routing/resolve`：视图本身未改，只是不再与写入侧不一致。
- ✅ 触发器 `sync_scope_revision_on_binding_change`：admin 后续 toggle `providers.enabled` / `credentials.manual_disabled` 时，`scope_hash` 不再被无效 binding 污染。
- ⚠️ **作用域**：仅 `credentials` 与 `providers` 两个 admin 可关闭的实体。其他维度（priority、tier、region）原本就不参与 hash drift，不在本次范围。
- ⚠️ **历史 scope 行**：migration 574 up 一次性收敛 hash；不会 bump `scope_version`，因此不会有"假升级"风暴。如果某条 scope 行 hash 收敛后确实在客户端可见 binding 集合发生变化，client refetch 后即一致。

## 5. 回滚

- Go 代码侧 `git revert` 即可恢复 LEFT JOIN 行为（会重新触发假性 409，幂等）。
- SQL 侧：

  ```bash
  psql "$LLM_GATEWAY_PG_URL" -f sql/migrations/00000000000000/0574_reorder_scope_disabled_filter.down.sql
  ```

  down 把函数与种子查询改回 LEFT JOIN，但**不重置已收敛的 `scope_hash`** — 如果回滚后又想全表再算一遍，需要手动 `UPDATE scope_revisions SET scope_hash = NULL` 走一遍原 bump 函数。

## 6. 验证步骤（部署到 245 后）

1. 拉任意一个 admin 已 `disabled` 的 provider（例如之前误关的 "anthropic-test"），确认 `/api/routing/resolve` 不返回它。
2. 在仪表盘 `?tab=resolve` 对该模型拖拽一行（任意方向） → PATCH 200，响应里 `binding_count == submitted_count`。
3. `psql -c "SELECT id, scope_hash, scope_version FROM scope_revisions WHERE model_key = '...'"` 确认 hash 与 resolve 视角的 credential 集合哈希一致。
4. `curl -sS /api/routing/candidate-bindings?model=...` 看返回 credential 数 == dashboard 渲染数。

## 7. 已知遗留

- 567 的 hash 包含 priority，566 canonical hash 不含 priority — 当前 priority toggle 仍走 567 的 bump；如果未来需要 priority toggle 也 bump canonical scope_version，需要后续补 566 的 3 个 bump 函数（本次未涉及，已记入 backlog）。
- 本次未改 541 的 `pm_update` 之外的副作用路径（删除 provider / 删除 credential）— 这些路径走的是 `DELETE FROM bindings` 触发的 trigger，hash 收敛后无需修改。
