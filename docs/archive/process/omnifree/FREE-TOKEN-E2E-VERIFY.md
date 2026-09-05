# 免费 Token 汇聚 — 端到端验证手册 (P0–P3)

本手册验证四个阶段的改动如何串联成一个完整的免费资源汇聚闭环：
**可见 (P0) → 预判 (P1) → 放大 (P2) → 智能排序 (P3)**。

所有改动 fail-open、向后兼容，任一阶段缺失都不会比现状差。

---

## 0. 前置准备

### 启用 OmniFree（auto/* 虚拟路由）
```bash
export OMNIFREE_ENABLED=true
```
不启用时 P1/P3 的请求路径逻辑不生效（UI 仍可看，P0 独立）。

### 数据库 schema（自动）
四个阶段依赖的表都会在 `db.Open()` 启动时自动创建（幂等 `ensure*Schema`）：
- `free_resource_catalog` / `free_quota_tracker` / `auto_combo_templates` / `keyless_providers`（omnifree）
- `credential_keys`（多 Key 子表，076）
- `credentials.secret_ciphertext` + 扩展列

**无需手动跑迁移。**

### 构建
```bash
go build ./...                    # 后端
cd web && npm run build           # 前端
```

---

## P0：UI 实时化验证

**目标**：免费资源页实时刷新 + 显示运行时指标 + SSE 推送。

### 验证步骤

1. **打开页面** → 顶部应出现新鲜度徽章「⏱ N 秒前」，每秒跳动。
2. **自动轮询**：静置不操作，15 秒后徽章时间持续刷新（页面隐藏时暂停）。
3. **触发请求**：发一个 `auto/*` 请求 → models 表的「配额（今日）」列应出现进度条（`used/total`），随请求增长。
4. **SSE 推送**：人为让某 credential 收到 429 → 该 key 应在 **~1 秒内**变红（不等 15 秒轮询），显示重置倒计时。
5. **新鲜度颜色**：徽章绿（<60s）→ 黄（>60s）→ 红（>5min）。

### 关键文件
- 后端：`admin/routing.go` (`overlayFreePoolRuntimeHealth` + LEFT JOIN `free_quota_tracker`)
- SSE：`admin/freepool_stream_sse.go` → `/api/free-pool/stream`
- 前端：`web/src/views/FreePoolView.vue`（`onMounted` 轮询 + `openFreePoolStream`）

---

## P1：主动配额预取验证

**目标**：请求前主动调上游 usage API，让 `free_quota_tracker` 反映真实配额。

### 验证步骤

1. **配置 OpenRouter key**（支持上游 `/api/v1/key`）：
   ```
   免费资源页 → 多 Key 批量池化 → catalog_code=openrouter-free, base_url, api_key
   ```

2. **触发 `auto/*` 请求**，然后查 DB：
   ```sql
   SELECT credential_id, corrected_limit, is_exhausted, auto_reset_at, request_count
   FROM free_quota_tracker WHERE provider_code = 'openrouter-free';
   ```
   **预期**：`corrected_limit` 反映上游真实日配额（非兜底 1000）。

3. **缓存命中验证**：45 秒内重复请求 → 日志无重复上游 fetch（`quotafetcher: openrouter` 只出现一次）。
   ```bash
   # 调整日志级别观察
   grep "quotafetcher" <log>
   ```

4. **耗尽验证**：把 key 额度用尽（或 mock 401）→ 下次请求该 candidate 被丢弃，走 failover；fail-open 保证不阻塞。

5. **节流验证**：并发 10 个请求 → throttle 串行化（间隔 ~250ms），不触发上游限流。

### 覆盖的 Provider
- **OpenRouter**：`/api/v1/key` + `/api/v1/credits`（专属 fetcher）
- **DeepSeek / SiliconFlow / OpenAI**：复用 `providercap` balance endpoint（通用 fetcher）

### 关键文件
- 包：`domains/quotafetcher/`（types/cache/throttle/registry/openrouter/balance_generic/manager）
- 注入：`domains/autocombo/virtual_factory.go` (`preflightQuota` → `quotaFetcher.FetchQuota`)
- 写回：`domains/freeresource/quota_tracker.go` (`ApplyFetchedQuota`)

---

## P2：多 Key 池化验证

**目标**：同一账号 N 个 key 聚成 1 个 credential，KeyRotator 轮转放大配额。

### 前置：核心已存在
多 Key 轮转已在 commit `48875278` 实现（`credential_keys` 表 + `KeyRotator` + 执行器轮转 + A3 guard）。P2 只补齐了免费池导入路径。

### 验证步骤

1. **批量池化导入**（同一账号 5 个 key）：
   ```
   免费资源页 → 多 Key 批量池化
     catalog_code = openrouter-free
     base_url = https://openrouter.ai/api/v1
     api_keys = (5 个 sk-or-v1-...，逗号或换行分隔)
     聚合模式 = per_credential（默认）
   ```

2. **DB 验证**（应为 1 credential + 4 credential_keys 行）：
   ```sql
   SELECT id, label FROM credentials WHERE pool_group='free' AND label LIKE '%free-pool%';
   SELECT credential_id, kid_index, status FROM credential_keys WHERE credential_id = <上面 id>;
   ```
   **预期**：1 个 credential，4 行 credential_keys（kid_index 1-4，status=active）。

3. **UI 徽章**：providers 表该 credential 显示 `🔑 ×5`。

4. **轮转验证**：触发多个 `auto/*` 请求 → 日志显示 `keyrotator` 在 5 个 key 间 round-robin：
   ```bash
   grep -i "keyrotator\|resolvekey" <log>
   ```

5. **A3 guard**：模拟第 3 个 key 401 → 该 key 标 invalid，其余 4 个继续轮转，credential 整体不被熔断。

6. **对比 per_key 模式**：同样 5 key 用 `per_key` → 5 个独立 credential，无轮转。

### 关键文件
- 后端：`admin/routing.go` (`handleFreePoolBulkRegister` + `bulk_mode`)、`admin/free_pool_extra.go` (`extraKeys` 写入)
- UI：`FreePoolView.vue`（`🔑 ×N` 徽章 + 批量表单）

---

## P3：评分修复验证

**目标**：修复死的 `QuotaRemaining` 维度 + 新增 `resetWindowAffinity`。

### 背景
`engine.go:288` 评分公式**原本没有 quota 项**，但 free-tier preset 里 `QuotaRemaining` 权重是 **0.25（最高）**。这个 bug 让免费层最重要的维度对排序零影响。P3 修复了它。

### 验证步骤（单元测试）
```bash
go test ./domains/autocombo/ -run "TestEngine_SortCandidates_Quota|TestComputeResetWindow" -v
```
**预期**：
- `QuotaInfluencesOrdering`：满额(10% used) credential 排在接近耗尽(90% used)前 ✓
- `QuotaSnapNilIsNeutral`：nil 快照不扭曲延迟排序（向后兼容）✓
- `ComputeResetWindowAffinity`：已重置→1.0、未知→0.5、即将重置→高、远期→0 ✓

### 端到端验证
1. 配 2 个 free credential，A 满额（PercentUsed≈0.1）、B 接近耗尽（PercentUsed≈0.9）。
2. **改动前**（可 git stash P3）：两者排序无差异（死维度）。
3. **改动后**：A 应稳定排在 B 前面（quota 权重 0.25 生效）。
4. B 的配额重置后（ResetAt 已过）→ resetWindowAffinity 给 B 加分。

### 关键文件
- `domains/autocombo/engine.go`（`sortCandidates` + quota 项 + `computeResetWindowAffinity`）
- `domains/autocombo/virtual_factory.go`（`preflightQuota` 返回 `quotaSnap` map）
- `domains/autocombo/quota_fetcher_adapter.go`（投影 `PercentUsed`）

---

## 完整回归测试

```bash
# 后端全量
go vet ./...
go build ./...
go test ./domains/quotafetcher/ ./domains/autocombo/ ./domains/freeresource/ ./admin/ ./domains/credential/

# 前端
cd web && npx vue-tsc --noEmit && npm run build
```
全部应通过（P0-P3 新增 11 个测试 + 既有测试全绿）。

---

## 依赖与开关一览

| 能力 | 环境开关 | 关闭时行为 |
|------|----------|-----------|
| OmniFree auto/* 路由 | `OMNIFREE_ENABLED=true` | P1/P3 请求路径不生效，UI 仍可用 |
| SSE 推送 | Redis（可选） | 无 Redis 时退化为进程内广播 + 轮询兜底 |
| 主动配额预取 | 自动（注册即生效） | 无 fetcher 时走旧 DB Preflight |
| 配额预取节流 | `LLM_GATEWAY_QUOTA_FETCH_MIN_INTERVAL_MS`（默认 250） | 0=禁用 |
| 多 Key 轮转 | `credential_keys` 有行即生效 | 无 extras 时单 key 零开销路径 |
| P3 quota 评分 | `quotaSnap` 有数据即生效 | nil 时中性 0.5，不扭曲排序 |

## 失败模式（全部 fail-open）

| 场景 | 行为 |
|------|------|
| 上游 usage API 不可达 | 返回 nil quota → 走旧 DB Preflight |
| 上游 401/403 | 缓存失效 + 返回 nil → 走旧 Preflight |
| URSM v2 关闭 | autocombo 评分直接生效（P3 杠杆最大） |
| URSM v2 authoritative | URSM 接管最终排序（autocombo 改进影响减弱） |
| credential_keys 表不存在 | enrich 静默退化为单 key |

**核心保证**：任何一环失败都不会比改动前更差。
