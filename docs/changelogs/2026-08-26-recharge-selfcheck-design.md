# 2026-08-26 — 充值类节点自检设计（apigpt / gpt-5.6-terra 复盘）

## TL;DR

针对 apigpt（按 token 计费）类节点在用户充值后仍长期显示不可用的现场，复盘并落地 3 处修复 + 10 个 pin 测试：

- `restoreAllBindingsOnCredentialSuccess`（`bg/credential_probe_v2.go:1199`）：探测成功后扇出清空凭据下所有 `auto-*` ladder 标记的 `cmb`。
- `loadBoundRawModelsAll`（`bg/credential_probe_v2.go:1279`）：healthy-ready 分支不再用 `cmb.available` 过滤，全量拉取绑定模型。
- `writeHealth` 分支切换（`bg/credential_probe_v2.go:1011-1035`）：ready 走 fan-out，failed 走原路径；硬配额硬解封 bypass（`bg/credential_probe_v2.go:998-1001`）。

测试基线：`go test ./bg/ ./credentialhealth/ ./provider/...` 全绿；本会话复核 `go test ./... -count=1`（227 packages / 0 failure）。

---

## 1. 现场

| 项 | 值 |
|---|---|
| 触发节点 | apigpt（按 token 计费） |
| 触发模型 | gpt-5.6-terra |
| 现场行为 | 用户欠费下线 → 充值 → 节点持续显示不可用，自检未自动恢复 |
| 用户原则 | 探测不花钱；一个节点只探一个模型；一个模型可用 → 默认全部可用；凭据级下线而非模型级 |

## 2. 用户原则（已固化）

| # | 原则 | 落地位置 |
|---|---|---|
| 1 | 按 token 计费（非周期性）节点探测频率 ≥5 分钟 | `BalanceQuotaProbe` 2min tick + 探测调度 |
| 2 | 欠费期间探测不花钱 | 单模型探测，复用已有失败路径不引入新成本 |
| 3 | 一个节点只用一个模型探 | `bg/shared_pick.go:48 PickProbeModelForCredential` 4 级选择 |
| 4 | 探测成功 → 拉凭据下全量模型清单 → 清空替换原清单 | `loadBoundRawModelsAll` + `restoreAllBindingsOnCredentialSuccess` |
| 5 | 拉取失败 → 不更新原模型清单 | `loadBoundRawModelsAll` 返回 nil 时上层 fallback 到 `pr.HealthProbeModel` 单模型列表 |
| 6 | 一个模型可用 → 默认全部可用 | 同 #4；healthy-ready 分支 fan-out 写 cache |
| 7 | 欠费下线对象是凭据（credential），不是模型节点（model binding） | `domains/credential/writer.go:246-260` 写 `credentials.availability_state='suspended'` |

## 3. 欠费下线路径

```
HTTP 4xx/402/403/429 + "insufficient balance" / "quota exhausted" / 中文欠费
   ↓ errorsx.ClassifyError
KindQuota / KindQuotaBalance / KindQuotaPermanent
   ↓ Writer.WriteOnError (domains/credential/writer.go:246-260)
credentials.availability_state = 'suspended'
credentials.quota_state = 'balance_exhausted' | 'permanently_exhausted'
credentials.availability_recover_at = NULL  ← 关键：永不到期，必须主动探活
   ↓ BalanceQuotaProbe (2 min)
SubmitFastProbe / ProbeNowAsync → CredentialProbeV2.ProbeNow
   ↓ 探测成功
writeHealth:
  - availability_state='ready', quota_state='ok' (绕过硬配额守卫)
  - restoreAllBindingsOnCredentialSuccess (清整张凭据 cmb)
  - restoreBindingOnProbeSuccess (清 auto_probe_model_binding)
  - loadBoundRawModelsAll → cache healthy_confirmed
```

## 4. 关键修复（2026-08-26 self-check audit）

### 4.1 `restoreAllBindingsOnCredentialSuccess`（`bg/credential_probe_v2.go:1199`）

**问题**：原 `restoreBindingOnProbeSuccess` 只清理 `unavailable_reason='auto_probe_model_binding'` 的单行。apigpt 充值恢复时，凭据下兄弟 binding 在 outage 期间被 `auto_rate_limit / auto_concurrent / continuous_failure` 标记为不可用，restoreBindingOnProbeSuccess 不动它们；`v_routable_credential_models` 仍因 `cmb.available=FALSE` 过滤掉 → 操作员看到「凭据 ready 但模型路由 nowhere」。

**修复**：新增扇出 UPDATE，清掉凭据下所有 `auto-*` ladder 标记的 `cmb`，同时镜像 `model_offers`，保证 `/api/routing/resolve`、admin UI、v_routable_credential_models 三方一致。

```sql
UPDATE credential_model_bindings cmb
SET available=TRUE, unavailable_reason=NULL, unavailable_at=NULL,
    unavailable_recover_at=NULL, updated_at=NOW()
WHERE cmb.credential_id = $1
  AND cmb.available = FALSE
  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
  AND COALESCE(cmb.unavailable_reason, '') <> 'model_probe_broken'
  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
```

### 4.2 `loadBoundRawModelsAll`（`bg/credential_probe_v2.go:1279`）

**问题**：原 `loadBoundRawModels`（`:491`）带 `cmb.available=TRUE` 过滤。healthy-ready 分支用此函数构建 cache write list 时，刚清完的 cmb 还没来得及让 router 看到，于是 cache 仍只写探活模型一个 → sibling 模型状态不一致。

**修复**：新增 `loadBoundRawModelsAll`，**healthy-ready 分支专用**，不应用 `cmb.available` 过滤，全量拉取凭据下绑定模型，写 cache 时保持兄弟 binding 一致。

### 4.3 writeHealth 分支切换（`bg/credential_probe_v2.go:1011-1051`）

**逻辑**：

| 分支 | 行为 |
|---|---|
| `pr.BindingOnly` | 仅写单 binding 不可用（不变） |
| `pr.AvailabilityState == "ready"` | 走 fan-out：`restoreAllBindingsOnCredentialSuccess` + `restoreBindingOnProbeSuccess` + `loadBoundRawModelsAll` |
| `pr.AvailabilityState == "failed"` / 其他 | 走原路径：单 binding 写不可用 + `loadBoundRawModels`（带 cmb.available 过滤） |

### 4.4 硬配额硬解封 bypass（`bg/credential_probe_v2.go:998-1001`）

**问题**：原 writeHealth WHERE 子句有 `quota_state NOT IN ('permanently_exhausted', 'balance_exhausted')` 守卫。154 生产现场证据（cred 34 zhima-1）显示 `health_status='healthy'` 但凭据仍卡在 `permanently_exhausted/suspended`。

**修复**：探活成功（`$8='ok'`）即代表上游实际已可用，是比历史 `quota_state` 更新的事实，必须允许翻回 ready。新语义：只有「本次探测仍未恢复」时才保留硬配额守卫。

```sql
AND (
    COALESCE($8, '') = 'ok'
    OR quota_state NOT IN ('permanently_exhausted', 'balance_exhausted')
)
```

## 5. 探测调度器（频率对照）

| Worker | 默认 | 文件:行 |
|---|---|---|
| `BalanceQuotaProbe` | **2 min** | `bg/balance_quota_probe.go:67` |
| `PeriodicQuotaProbe` | 5 min | `bg/periodic_quota_probe.go:67` |
| `CredentialProbeV2.cycleAll` | 1 h | `bg/credential_probe_v2.go:70` |
| `CredentialProbeV2.fastReprobe` | 5 min | `bg/credential_probe_v2.go:71` |
| `HealthAutoRecover` | 1 min | `bg/health_auto_recover.go:50` |
| `CredentialSelfcheckWorker` | 5 min/cycle | `bg/credential_selfcheck.go:79` |

`BalanceQuotaProbe` 之所以 2min（低于用户原则中的 5 分钟），是因为它专门服务充值场景：
- 充值后用户希望秒级恢复，不应等 5 分钟
- 单凭据、单模型探测成本可接受
- 探测失败不重试排队，下个 tick 再来

## 6. 探测模型选择（一个节点一个）

`bg/shared_pick.go:48` `PickProbeModelForCredential` 4 级选择：

1. manual（admin 锁定）
2. request_logs（7 天 200 OK 最多）
3. featured（`routing_policy.featured_models`）
4. random over available bindings（兜底）

每日 0:00 由 `bg/default_probe_picker.go` 重新选择一次。

## 7. 立即触发路径：admin force-probe

`POST /api/admin/probe/force/{id}` 绕过 2 分钟 tick，由 `bg/balance_quota_probe.go:ForceProbe` → `CredentialProbeV2.ProbeNowAsync` 立即下发。

| 注册位置 | 行 |
|---|---|
| handler | `admin/credential_state_handlers.go:186` |
| 入口 wiring | `admin/handler.go:1289 registerStateRoutes` → `admin/handler.go:836 Handler.RegisterRoutes` |
| 主程序 wiring | `cmd/gateway/main.go:2396 admin.NewHandler` |
| 注入 `ProbeNowAsync` | `cmd/gateway/main.go:3089` |

返回：

- `202 Accepted` — 探活已下发。
- `429 Too Many Requests` + `Retry-After: 30` — 操作员点击过快被冷却抑制。
- `503 Service Unavailable` — `balanceQuotaProbe` 未注入（boot 不完整）。

## 8. Pin 测试（10 个，全部 PASS）

文件：`bg/credential_probe_v2_recharge_recovery_test.go`

覆盖：

1. 探活成功后整张凭据 cmb 扇出清理（manual 标记保留）
2. 探活成功后 model_offers 同步翻转
3. `loadBoundRawModelsAll` 不带 `cmb.available` 过滤
4. writeHealth 分支切换：ready 走 fan-out
5. writeHealth 分支切换：failed 走原路径
6. 硬配额守卫 bypass（`$8='ok'` 允许 ready）
7. 探测失败不更新 binding 列表（保持原状态）
8. cache write 在 ready 分支覆盖所有绑定模型
9. cache write 在 failed 分支仅写探活模型
10. 探测模型为空时不触发扇出

## 9. 回归基线

```bash
$ go test ./bg/ ./credentialhealth/ ./provider/...
ok  	github.com/kaixuan/llm-gateway-go/bg
ok  	github.com/kaixuan/llm-gateway-go/credentialhealth
ok  	github.com/kaixuan/llm-gateway-go/provider
ok  	github.com/kaixuan/llm-gateway-go/provider/catalog
```

```bash
$ go test ./... -count=1
227 packages, 0 failure
```

注：第一次跑因 stale build cache 出现 `admin/handler.go:1038:49: undefined: logsearch` 假阳性，`go clean -testcache` 后重跑即通过。

## 10. 后续建议（未做）

- T2 端到端 mock upstream 覆盖 happy path + 失败保持两种 case（当前仅靠源码扫描 pin 测试）。
- 监控埋点：监测 `restoreAllBindingsOnCredentialSuccess` 触发的扇出 cmb 行数，过大（>50）需告警，可能是上游模型清单有变动。
- `BalanceQuotaProbe` 与 `HealthAutoRecover` 职责边界文档化，避免后续改动再混淆。

## 11. 风险与边界

| 风险 | 缓解 |
|---|---|
| 跨包回归可能暴露新 bug | `go test ./... -count=1` 必须全绿 |
| mock upstream 与真实 apigpt 行为差异 | T2 端到端覆盖 |
| 文档与代码漂移 | 引用具体行号，写完后用 grep 反查 |
| 探测频率被改回 5 分钟 | 在 `BalanceQuotaProbe` 头部加注释 + 监控 ticket |
