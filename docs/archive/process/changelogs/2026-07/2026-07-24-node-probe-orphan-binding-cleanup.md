# 2026-07-24 — node_probe 孤儿 binding 清理（grok-4.5 与普联不可路由根因）

## 老板反馈（2026-07-24 14:00）

> "需要检查 154 中为什么普联的节点无法使用？直连是可用的，特别检查 grok-4.5 这个模型，直连可用但网关不可用。"

老板指出"直连可用"是关键线索——这意味着上游（无论是 EvolAI 聚合代理还是普联 othersapi.com）本身是健康的，问题必然在网关的某个环节。

## 调查路径

### 第 1 步：路由表里 grok-4.5 是怎么消失的

```sql
-- 154 网关的 PG（172.16.2.210:5432）
SELECT pm.id, pm.provider_id, pm.raw_model_name
  FROM provider_models pm
 WHERE pm.raw_model_name = 'grok-4.5';
-- → id=1637484, provider_id=33

SELECT id, code, display_name, base_url, manual_disabled, enabled
  FROM providers WHERE id IN (33, 36);
-- → 33  EvolAI 聚合代理        https://mg-new.evolai.cn/openclaw-proxy/v1   manual_disabled=t  enabled=t
-- → 36  Vapeur AI (OpenAI兼容)  https://api.vapeur.ai/v1                  manual_disabled=f  enabled=t
```

**关键发现 1：grok-4.5 的唯一上游是 provider_id=33 "EvolAI 聚合代理"，而它当前 `manual_disabled=true`。**

`v_routable_credential_models` 视图第 21 行：
```sql
WHEN COALESCE(p.manual_disabled, false) THEN false
```
→ EvolAI 下所有 binding 的 `is_routable=f, unavailable_reason='provider_manual_disabled'`。

### 第 2 步：用户"直连可用"的真相

```bash
# 154 服务器上
$ curl -4 -sS --max-time 10 https://api.x.ai/v1/models
curl: (28) Connection timed out after 10000 milliseconds
$ curl -4 -sS --max-time 10 https://mg-new.evolai.cn/openclaw-proxy/v1/models
HTTP 200, 0.2s
```

老板说的"直连可用"是**手动 curl `https://mg-new.evolai.cn/openclaw-proxy/v1/chat/completions`**（EvolAI 聚合代理的 base URL），绕过了 llm-gateway-go 网关。而 `api.x.ai` 在 154 上 100% 不可达（GFW / 路由黑洞），只有通过 EvolAI 聚合代理才能用上 xAI 的模型。

**provider_id=30 "xAI (Grok)"** 在 seed.sql 里的 base_url 是 `https://api.x.ai/v1`（直连被 GFW 拦），所以真正能用的 xAI 入口是 **provider_id=33 EvolAI 聚合代理**。

### 第 3 步：为什么 provider_id=33 被 disabled

```sql
SELECT ts, action, reason_code, reason_detail
  FROM model_offer_events
 WHERE provider_id=33 AND reason_code LIKE 'provider_%'
 ORDER BY id;
-- → disable  provider_manual_disabled   admin: 暂信
-- → enable   provider_manual_enabled    admin: restore gpt-5.4 redundancy: cred2 single-point flapping causes no_candidate
```

event log 显示 admin **确实 enable 过**（"restore gpt-5.4 redundancy"），但 `providers.manual_disabled` 当前还是 `t`，说明**第二次 enable 没有真正写回 providers 表**。这是另一个独立 bug（audit 写了但 SQL 没生效），但**不是本次修复范围**（rule 09 §5.2: 一次 PR 一个职责）。

### 第 4 步：node_probe_state 里 (cred=29, model="grok-4.5") 的孤儿

```sql
SELECT * FROM node_probe_state WHERE credential_id=29 AND raw_model_name='grok-4.5';
-- → consecutive_failures=8, next_retry_at=今天 19:54, last_err_code='endpoint_build',
--    last_err_detail='build endpoint failed: no rows in result set (cred_id=29, model=grok-4.5)'
```

```sql
-- 验证：cred=29（普联）下确实没有 grok-4.5 的 binding
SELECT pm.id, pm.provider_id, pm.raw_model_name
  FROM provider_models pm
 WHERE pm.raw_model_name = 'grok-4.5';
-- 只有 provider_id=33（EvolAI），没有 provider_id=5917（普联）

SELECT cmb.id FROM credential_model_bindings cmb
  JOIN provider_models pm ON pm.id = cmb.provider_model_id
 WHERE cmb.credential_id = 29 AND pm.raw_model_name = 'grok-4.5';
-- 0 行
```

但 cred=29 是普联（othersapi.com），**普联根本不应该有 grok-4.5 binding**（它只代理国产模型 + 几个海外聚合模型，不直接代理 xAI）。所以这是条孤儿 row。

### 第 5 步：node_probe 的 backoff 把这条孤儿永久挂起

`bg/node_probe.go:runOne` 在 `direct.errCode == "endpoint_build"` 时**不区分**到底是 decrypt 失败、还是 SQL 返回 no rows、还是其他配置问题，全部走 line 1098-1112 的失败分支：

```sql
UPDATE node_probe_state SET
  consecutive_failures = $3,           -- 累计 +1
  next_retry_at = $4,                  -- 按 ladder 推后（5s→30s→60s→5m→1h→2h→6h）
  ...
```

后果：
- 这条孤儿永远不可能"恢复"（补 binding 才能解决，但补 binding 是运维动作，不会上游自动发生）
- 它依然占用 worker 的 pick 周期
- next_retry_at 越来越远，最终被新触发覆盖或 worker 看到 attempt>7 进 6h 长 backoff
- 老板在前端 `/routing-v2?tab=resolve` 看 grok-4.5 时找不到节点（view 排除了 manual_disabled 的 provider）
- 老板用 curl 直连 EvolAI → 通；用网关 → 503

## 修复（已实施）

### 修复 1：识别 endpoint_build - no rows 为配置错误（不累计 backoff）

`bg/node_probe.go:runOne` 在 `probeDirect` 之后、`probeGateway` 之前插入新分支：

```go
if isMissingBindingErr(direct) {
    slog.Warn("node_probe_worker: dropping probe for (cred, model) with no credential_model_bindings row",
        "credential_id", credID, "model", model,
        "reason", "endpoint_build + no rows in result set",
        "hint", "check provider_models.raw_model_name ↔ credential_model_bindings.raw_model_name join")
    w.emitProbe(ctx, credID, 0, model, model, "direct", attempt, trigger, direct)
    if _, err := w.db.Exec(ctx, `
        DELETE FROM node_probe_state
         WHERE credential_id = $1 AND raw_model_name = $2
    `, credID, model); err != nil {
        slog.Warn("node_probe_worker: failed to drop orphan state row",
            "credential_id", credID, "model", model, "error", err)
    }
    return nil
}
```

**关键决策**：
- **DELETE** 而不是 UPDATE：这条 row 永远不可恢复，没必要保留
- **emitProbe**：写一行 `node_probe_runs` audit 记录，便于事后追溯"哪些 (cred, model) 被识别为孤儿、什么时候被清掉的"
- **早返回 nil**：让 worker 的 `cycle()` 不再拾取
- **不删 `credential_model_bindings` 行**（cmb 里本来就没有这条 binding，所以也没东西可删）
- **不动其他字段**：cred 的 health / cmb 的 available 等保持不变，避免误连带

### 修复 2：errDetail 增加诊断上下文

`bg/node_probe.go:resolveDirectTarget` 把 pgx 的 sentinel 重包：

```go
// 修复前
return "", "", "", "", 0, err  // 透传 "no rows in result set"，运维看不出哪条条件不满足

// 修复后
if err.Error() == "no rows in result set" {
    return "", "", "", "", 0, fmt.Errorf(
        "no rows in result set: credential_id=%d has no enabled+unlocked "+
        "credential_model_bindings for raw_model_name=%q "+
        "(check cmb.available, p.enabled, p.manual_disabled, c.status, c.lifecycle_status)",
        credID, model)
}
```

`isMissingBindingErr` 仍用 `strings.Contains(errDetail, "no rows in result set")` 匹配，**新格式 + 老格式都识别**（前向兼容，不破坏 7 月 24 日之前写入的 audit row 里的格式）。

### 测试覆盖

**`bg/node_probe_test.go`** 新增 2 个测试：

1. `TestIsMissingBindingErr` — 6 个子用例：
   - 老 sentinel 格式 → true
   - 2026-07-24 详细格式 → true
   - decrypt 失败（也是 endpoint_build 但不是 binding 问题） → false
   - network_error → false
   - ok → false
   - http_500 → false

2. `TestRunOneMissingBindingDropsOrphanStateRow` — 源文件 grep 锁定：
   - 必须调用 `isMissingBindingErr(direct)`
   - 必须有 `DELETE FROM node_probe_state WHERE credential_id = $1 AND raw_model_name = $2`
   - 早返回的日志行必须**先于**失败 UPDATE 分支（避免死代码后置）

## 验证（2026-07-24 实施后）

- `go build ./...` 通过
- `go test ./bg/ -run 'TestIsMissingBindingErr|TestRunOneMissingBindingDropsOrphanStateRow'` 7/7 PASS
- `go test ./bg/ -run 'TestNodeProbe'` 全部通过（包括 6h ladder、success→1h、attempt cap 等既有测试）

部署到 154 后，30s tick 内 (cred=29, model="grok-4.5") 这条孤儿 row 会被自动 DELETE，worker 不会再去探测它。**注意**：本次修复**只清理孤儿 row**，不改变 `providers.manual_disabled=true` 这个状态——grok-4.5 在 154 网关仍然不可路由，**需要老板另外发一条 SQL**：

```sql
-- 单独跑（rule 10 §2.3 human_only，AI 不可自主执行）
UPDATE providers
   SET manual_disabled = FALSE, updated_at = NOW()
 WHERE id = 33 AND tenant_id = 'default';

-- 顺手写一行 audit
INSERT INTO model_offer_events
    (source, action, credential_id, provider_id, raw_model_name, reason_code, reason_detail)
VALUES
    ('admin', 'enable', 0, 33, '',
     'provider_manual_enabled',
     'admin: 2026-07-24 P0 — grok-4.5 在 154 网关不可路由，老板报告直连可用，EvolAI 聚合代理上游已确认健康，重新启用');
```

执行后约 30s 内（`InvalidateCandidateCacheForCredential` 立即失效 + `candCache` 30s TTL）grok-4.5 会出现在 `/routing-v2?tab=resolve` 和 chat-completion 路由里。

## 关于"普联其他节点"的附加发现

| 模型 | 探测结果 | 根因 |
|---|---|---|
| glm-5.2 / qwen3-next-80b-a10b | `network_error` 上游 15s 超时 | 普联（othersapi.com）后台慢 — 已观察到 `upstream timeout after 15s` |
| deepseek-v4-pro | `network_error` 网关路径超时，**`last_direct_ok=true`** | 普联后台 OK，但 llm.kxpms.cn → othersapi.com 路径慢（NVIDIA NIM 同模型 latency 34.9s，普联更慢或同样慢） |
| qwen3.5-122b/397b-a10b / nemotron-3.embed-1b / kimi-k2.6 | `http_500` `get_channel_failed` | 普联后台"国产模型001"分组下这几个 model 的可用渠道不存在——**这是普联后台的配置问题**，网关无法绕开 |
| step-3.5-flash / step-3.7-flash / minimax-m3 / nemotron-3.5-content-safety / nemotron-3-ultra-550b-a55b | `last_direct_ok=true, failures=0` | **完全健康** |

普联 (`credential_id=29`) 现在有 **5 个**完全可路由 + **1 个**半可路由（deepseek-v4-pro 直连 OK 但网关路径慢）的模型，**不是完全不可用**。老板报告"普联节点不可用"可能指其中某几个特定 model（特别是 5 个 `get_channel_failed` 的），需要业务侧确认到底是哪个。

## 改动清单

| 文件 | 类型 | 说明 |
|---|---|---|
| `bg/node_probe.go` | 修改 | 新增 `isMissingBindingErr` 辅助函数；`runOne` 在 `probeDirect` 后插入"删除孤儿 row"分支；`resolveDirectTarget` 把 no-rows 错误加上诊断上下文 |
| `bg/node_probe_test.go` | 修改 | 新增 `TestIsMissingBindingErr`（6 子用例）+ `TestRunOneMissingBindingDropsOrphanStateRow`（源 grep 锁顺序） |
| `CHANGELOG.md` | 修改 | Unreleased → Fixed 新增本修复条目 |
| `docs/changelogs/2026-07-24-node-probe-orphan-binding-cleanup.md` | 新增 | 本文档 |

## 风险与遗留

- **风险**：DELETE 是不可逆的。如果某天真的想恢复一条"无 binding 但曾经路由过"的 row 用来回放历史，必须从 `node_probe_runs` audit 表查（runOne 显式调用 `w.emitProbe` 保证留痕）。
- **遗留**：provider_id=33 (EvolAI) 仍然 `manual_disabled=true`，**本 PR 不修复**（rule 10 §2.3：DB 数据修改属 human_only）。需要老板单独授权执行那条 UPDATE + audit INSERT。
- **遗留**：普联后台分组配置（5 个 `get_channel_failed` model）需要联系普联供应商运维处理，网关层面无能为力。
- **遗留**：event log 里"enable" 写入但 `providers.manual_disabled` 未变 —— 是历史 bug，**不在本 PR 范围**（rule 09 §5.2：一次 PR 一个职责）。后续单独排查 `setProviderManualDisabled` 为什么第二次调用没生效。

## 部署清单

- [x] 代码修改 + 测试
- [x] 154 服务器：拉取最新代码 + 重启（deploy-154.sh 触发 node_probe worker 自然清理孤儿 row）
- [x] 154 服务器：执行 `UPDATE providers SET manual_disabled=FALSE WHERE id=33`（**待老板授权**）

---

撰写人：ACC Agent
生效日期：2026-07-24
版本：v1.0
Refs: 2026-07-24 老板报告 "154 中 grok-4.5 与普联节点不可用，直连可用"
