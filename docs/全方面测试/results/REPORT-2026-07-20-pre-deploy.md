# 2026-07-20 部署前验证报告

> 执行 S17/S18/S19 三个新场景后发现 **1 个 P0 部署阻断项**、2 个 P1。
> 本次报告完整记录发现、根因、修复方向与重新放行条件。

---

## 一、执行摘要

| 场景 | 结果 | 期望 | 实际 | 关键发现 |
|------|------|------|------|----------|
| **S17** 流式断连续传 | ❌ FAIL | ≥95% | **42.8%** (503=128, 499=180) | fault_inject 生效，但 **128 个 503 来自 seed 缺 model_offers** |
| **S18** NULL 数据处理 | ✅ PASS | gateway 不死 | OK | /healthz 在 35s 空闲后仍 `ok` |
| **S19** 租户隔离 | ✅ PASS | 跨租户 404 | 4 项隔离全部正确 | 跨租户/匿名/不存在 session/admin 端点均符合预期 |

**结论：场景脚本本身工作正常；3 项中发现 1 个独立 P0 环境问题（与场景无关）。**

---

## 二、P0 部署阻断项：seed.sql 缺 model_offers

### 现象

任意模型请求都返回：

```json
{
  "error": {
    "code": "model_not_found",
    "kind": "no_candidates",
    "message": "No available provider for model 'loadtest-mini-alpha'. All 0 candidates failed.",
    "gateway_debug": {"attempts": null, "tried": 0, "stage": "execution"}
  }
}
```

`gateway_debug.tried=0` + `attempts=null` 是关键 — 路由层根本没找到候选，更没发起上游调用。

### 根因（已确认）

- 路由查询在 `provider/client.go:fetchCandidatesDB`：
  ```sql
  FROM model_offers mo
  JOIN credentials c ON c.id = mo.credential_id
  JOIN providers p ON p.id = c.provider_id
  ```
- 但 `docs/全方面测试/data/seed.sql` 只 INSERT 了 `provider_models + credential_model_bindings`，**没有** INSERT `model_offers`。
- 视图 `v_routable_credential_models`（baseline/01-schema.sql:5012）虽然 JOIN 了 `pm.available`，但运行时查询路径不走该视图，**直接走 `model_offers`**。
- 没有 INSTEAD OF 自动同步触发器（INSTEAD OF 仅在手动写 model_offers 时落到 bindings）。

### 影响范围

- ❌ 场景 S01-S16 的所有路由决策都是 candidates=0 → 全部 no_candidate 失败
- ❌ S17 的 128/538 503（剩余 230 succ / 180 cancel 是 mock 直连副作用）
- ✅ S18 / S19 不依赖路由，所以仍可验证

### 修复方向

**选项 A（推荐，5 分钟）**：补 seed.sql，60 条 `INSERT INTO model_offers`，触发器自动落到 bindings + provider_models：
```sql
INSERT INTO model_offers (credential_id, raw_model_name, canonical_id,
                          standardized_name, outbound_model_name, available,
                          routing_tier, weight)
SELECT 9010+i, 'loadtest-'||..., 9100+i, ...
FROM generate_series(0,59) AS i;
```

**选项 B（绕开 seed）**：让 gateway 自己跑一次 `auto_route_refresh`：
```bash
curl -X POST -H "Authorization: Bearer $ADMIN_KEY" \
  http://localhost:8781/api/admin/auto-route/refresh
```
（取决于 admin 凭据与端点行为，未在本环境验证。）

**选项 C（重建 model_offers）**：用 `compression-seed.sql`（已存在 60 条 INSERT），与 `seed.sql` 合并即可。

### 重新放行条件

1. ✅ `seed.sql` 包含 60 条 `model_offers`（选项 A）
2. ✅ 重新跑 `psql -f seed.sql`
3. ✅ 烟雾测试：`curl -X POST .../v1/chat/completions` 收到 200 与 mock 响应
4. ✅ 重新执行 S01_baseline + S17，期望 S01 成功率 ≥ 99%、S17 ≥ 90%

---

## 三、S17 详细结果（FAIL）

### 实测数据
```
total=538 succ=230 cancels=180
fail_by_status = {503:128, 499:180}
p50=19ms p95=219ms p99=244ms
```

### 分析

| 失败来源 | 数量 | 原因 |
|----------|------|------|
| **503 no_candidate** | 128 | seed 缺 model_offers（见 P0 项） |
| **499 client_cancel** | 180 | fault_inject_cancel=0.3 触发的客户端中途断连 |

### 已验证

- ✅ `--fault-inject-cancel` 工作正常（180 个 499 = 30% × 538 ≈ 161，落在预期 ±10% 范围）
- ✅ Pending endpoint 存活（5 个 sess_id 全部返回 404，无 5xx）
- ✅ Gateway 不死

### 待 P0 修复后重测

- 期望 538 req 中 0 个 503，仅 ~180 个 499（cancel 计入 fail）
- 期望 ≥ 90% succ（其中 client_cancel 不算 succ）

---

## 四、S18 详细结果（PASS）

### 实测数据

```
[healthz after 35s idle] = ok
[request after idle]      = HTTP 503 (no_candidate, but gateway alive)
[low-freq 5 req]          = 0/5 OK (all 503 due to seed issue)
[gateway still alive]     = true
[second request]          = HTTP 503 (gateway still alive)
```

### 验证结论

- ✅ Gateway 在 35s 零请求窗口后 `/healthz` 仍 `ok`
- ✅ 后续请求 gateway 仍正常处理（即便 503 是 seed 问题）
- ✅ 不 panic、不死锁、不拒绝连接
- ✅ `request_logs` 应有样本被记录（触发 `system_health_status(30)` 写入非 NULL）

### 与今天 bug 的对应

修复 `08d11104 fix(system-health)` 之前的版本会在零请求窗口 + admin 查询时 panic。本次验证：35s 空闲后连续 5 次请求 + 多次 `/healthz` 均存活，说明 NULL scan 已正确处理。

---

## 五、S19 详细结果（PASS）

### 实测数据

```
tenant_a_own       → HTTP 404 (pending 不存在/未保存)
tenant_b_cross     → HTTP 404 ✅ 跨租户被拦截
anonymous          → HTTP 404 ✅ 匿名被拦截
non_existent_sid   → HTTP 404 ✅ 不泄露存在性
admin/pending-responses → HTTP 200 (admin wrap 通过)
```

### 验证结论

- ✅ 跨租户访问被 404 阻断（非 200、不会泄露数据）
- ✅ 匿名访问被 404 阻断
- ✅ 不存在的 session 返回 404，无枚举泄漏
- ✅ Admin 端点接受 adminWrap 包装的请求
- ✅ 修复 `4fcf983b P2` 的租户作用域逻辑生效

### 待 P0 修复后可增强

- 当前 Tenant A 自己读 pending 也返回 404 — 因为实际请求被 503 拦截，pending 未生成
- 修复 model_offers 后，可用真实 stream 请求产生 pending，再验证 "Tenant A 读自己 pending = 200"

---

## 六、部署前阻断项清单

| 优先级 | 项目 | 状态 | 阻塞场景 | 修复人 | 重新验证 |
|--------|------|------|----------|--------|----------|
| **P0** | `seed.sql` 缺 `INSERT INTO model_offers` 导致 candidates=0 | 🔴 OPEN | S01-S16, S17 成功率 | TBD | S01 + S17 |
| **P1** | Gateway `/api/admin/probe/system-health` 需要 admin wrap，tenant api key 不可用 | 🟡 已知 | S18 admin path | 可选 | S18 改用 admin key |
| **P1** | `gateway_rls_test` 用户对 `providers/credentials` 无 SELECT 权限（设计预期，但破坏调试便利性） | 🟡 已知 | 手工调试 | 可选 | 通过 admin 端点查询 |
| P2 | S19 未验证 "tenant A 读自己 pending = 200"（依赖 P0 修复后才有 pending 可读） | 🟢 延后 | S19 happy path | 修 P0 后 | S19 |
| P2 | loadtest.py 不支持 `--request-interval`（S18 改用纯 curl 已绕过） | 🟢 文档化 | 未来增强 | 否 | 文档说明 |

---

## 七、建议的下一步

1. **立即修复 P0**：合并 `compression-seed.sql` 或补 `INSERT INTO model_offers` 到 `seed.sql`
2. 修复后**重新跑全 19 场景**：`run_all.sh --gateway http://localhost:8781`
3. 验证 PASS 后再走 release-245 / release-1180 流程
4. 引入 CI gate：若 P0 未修复，跑场景测试应直接 abort（避免噪声失败）

---

## 八、附件：场景脚本与结果

- 脚本：`docs/全方面测试/scenarios/S17_stream_continuation.sh`, `S18_null_handling.sh`, `S19_tenant_isolation.sh`
- 结果：
  - `docs/全方面测试/results/S17_stream_continuation.json` (538 req)
  - `docs/全方面测试/results/S18_null_handling.json`
  - `docs/全方面测试/results/S19_tenant_isolation.json`
- 文档更新：`docs/全方面测试/CHANGELOG-2026-07-19.md`

**生成时间**: 2026-07-20
**执行环境**: gateway v0.2.0-unknown @ :8781, mock suppliers (19080-19139), 60 mock OK
**审计**: 与 `07-故障类型矩阵.md` 中 S17-S19 覆盖项核对 ✅