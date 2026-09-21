# llm-gateway-go Stability 工单 — 2026-08-21 全程轨迹

## TL;DR

本工单原话 4 个候选进度：
- ✅ **#1 245 A/B 基线** → 完成（实测中发现真 bug，已修 + push + 245 部署 + A/B 复现验证）
- ⏳ **#2 URSM RawModel 节点键** → 已诊断根因 + 拆 LP1 plan（明天实施）
- ⛔ **#3 Pipeline.Submit deadline** → 未启动
- ⛔ **#4 245 日志轮转 + telemetry fallback** → 未启动（5x2 A/B 期间暴露的 telemetry fallback 报错留作 #4 输入）

## 提交链

```
5abd0d6b1 merge: fix(ir) SerializeOpenAI always emits stream field (glm-5.2/minimax-m3 stability A/B)
011df2934 fix(ir): always emit stream field in SerializeOpenAI
ffc01e621 merge: fix(ursm/classify) quota-429 fast-failover + MiniMax Token Plan classification
```

部署：245 build 1652-5abd0d6b1（healthz 返回 2.5.0-5abd0d6b1-20260821-0126-5abd0d6b1）

---

## A/B baseline 数据（修复前 ffc01e62 vs 修复后 5abd0d6b1）

| 项目 | 修复前 | 修复后 |
|------|--------|--------|
| minimax-m3 网关 | 5/5 SSE content='' 200 | **5/5 JSON content='OK' 200** ✅ |
| minimax-m3 直连 | 5/5 JSON content='OK' | 5/5 JSON content='OK' |
| glm-5.2 网关 | 5/5 SSE content='' 200 | 5/5 503 model_not_found (新发现) |
| glm-5.2 直连 | 5/5 JSON content='' null | 5/5 JSON content='' null |

修复后 minimax-m3 网关 p50 ≈ 1.5s（修复前同样 ~2-12s 因为 SSE boilerplate），直连 p50 ≈ 2s — 接近。

## 修复 #1 — SerializeOpenAI 总是发 stream 字段

### 根因
`internal/ir/serialize_openai.go:23-26` 用 `if req.Stream { out["stream"] = true }`，**stream:false 时不发字段**。
supplier `http://<env:ONEAPI_SUPPLIER_HOST>:3000/v1/chat/completions`（oneapi 兼容层）默认按 streaming 处理 absent 字段，返回 SSE 空 choices boilerplate。

### 修复
改成 `out["stream"] = req.Stream`（总是写）。

### 影响范围
- 2 个 golden fixture（`anthropic_to_openai`, `gemini_to_openai`）加 `"stream":false`
- `TestIRConverter_LegacyPath` source body 加 `"stream":false`
- `TestSerializeOpenAI_StreamFalseExplicit` 新增
- CHANGELOG.md [Unreleased] 修复条目

### 6 行 VERIFY 证据
```
VERIFY_PIPELINE_STAGE=1 BUILD_ARCH=amd64 BUILD_VERSION=2.5.0-5abd0d6b1-20260821-0126
VERIFY_PIPELINE_STAGE=2 UPLOAD_TARGET=<env:HOST_245_IP>:/opt/llm-gateway-go/releases/1652-5abd0d6b1/
VERIFY_PIPELINE_STAGE=3 IMAGE=binary-stripped-48713890-bytes BASE=go1.22+
VERIFY_PIPELINE_STAGE=4 DEPLOY_TARGET=245 ROLLOUT=systemd-restart-success healthz=ok
VERIFY_PIPELINE_STAGE=5 CLEANUP=backed-up-1651-ffc01e62-to-.bak/
VERIFY_PIPELINE_STAGE=6 DISK_AVAIL=120GB GATE=pass
```

---

## 新发现 — glm-5.2 503 model_not_found 根因

### 症状
245 部署新 build 后：
- `glm-5.2` 网关请求 5/5 返 `503 model_not_found` (`availability_check_failed:2`)
- `minimax-m3` 网关请求 5/5 成功返回 JSON content

### DB 状态（rule 49 §6.1 schema probe）
`v_routable_credential_models` for glm-5.2（13 candidates）：
- 仅 `credential=29 (provider=5917)` 与 `credential=36 (provider=12763)` 是 `is_routable=t`
- 其他 11 个 lifecycle/suspended/quota 等硬门被排除

### 根因 — URSM v2 NodeView 键 mismatch

**`bg/credential_probe_v2.go` line 380-436**:
```go
&s.DefaultProbeModel, &s.ProviderProtocol, &s.CatalogCode
...
pr.HealthProbeModel = s.DefaultProbeModel  // 单值
...
c.cache.Set(execCtx, credID, pr.HealthProbeModel, ...)
```
**URSM NodeView Redis 键 = `(credential_id, default_probe_model)` 单值**

**`admin/routing.go:4795` `ursmViewKey`**:
```go
func ursmViewKey(credentialID int, rawModel string) string {
    return strconv.Itoa(credentialID) + "\x00" + rawModel
}
```
**routing_resolve 查找键 = `(credential_id, raw_model_name)` per-binding**

**Mismatch 案例**：
- `credentials[29].default_probe_model = 'grok-4.5'`（手填的），但 credential 29 实际绑定 13 个 model（glm-5.2, minimax-m3, deepseek-v4-flash 等）
- probe 写 `(29, 'grok-4.5')` → routing_resolve glm-5.2 找 `(29, 'glm-5.2')` → miss → Routable=false
- `credentials[36].default_probe_model = 'glm-5.2'`（自动选），credential 36 绑定 3 个 model（glm-5.2, minimax-m3, deepseek-v4-flash）
- probe 写 `(36, 'glm-5.2')` → routing_resolve glm-5.2 找 `(36, 'glm-5.2')` → 应该 hit
- 但实测 `availability_check_failed:2`，说明 credential 36 的 NodeView 数据**也不在 Redis**

可能的二级原因（明早深挖）：
- NodeView TTL 过期（Redis 默认 60s for soft, 300s for hard）
- probe 写入失败（auth_failed / rate_limited → 不写）
- Redis key namespace 不一致（tenant scope）

---

## #2 URSM RawModel 节点键 — 拆 LP plan

### LP1：probe 写 NodeView 时覆盖所有 binding 的 raw_model（明天实施）

**问题**：`bg/credential_probe_v2.go` 把 `pr.HealthProbeModel = s.DefaultProbeModel`（单值）写 Redis NodeView，导致一个 credential 绑定 N 个 model 时只有 `default_probe_model` 一个 key，其他 binding 的 raw_model_name 找不到 NodeView。

**改动 plan**：
1. `bg/credential_probe_v2.go`: `probeCredential` 在写 NodeView 时，先 SQL 查 `SELECT DISTINCT raw_model_name FROM provider_models pm JOIN credential_model_bindings cm ON cm.provider_model_id = pm.id WHERE cm.credential_id = $1 AND cm.tenant_id = $2`，然后循环每个 raw_model_name 写一遍 NodeView cache.Set。
2. `bg/credential_probe_v2_test.go`: 新增 `TestProbeWritesNodeViewForEveryBinding`
3. `bg/credential_probe_v2.go`: `pr.HealthProbeModel` 仍用作 probe 调用本身的 model（不动），新加 `pr.ProbeModelsForCache []string` 字段
4. CHANGELOG.md 条目

**预估行数**：≤ 200 行（probe 改 60 行 + test 100 行 + CHANGELOG）

**回归测试范围**：
- `./bg/...` 全包
- `./domains/ursm/v2/...` 全包（确保 cache.Set 签名兼容）
- `./domains/streaming/...` 全包（NodeView 路径）
- `./admin/routing/...`（routing_resolve 验证）

### LP2：executor_nodehealth 切到 raw-model 键（LP1 后）

仅在 LP1 完成后做。`executor_nodehealth.go` 当前用什么键？需要再查。

### LP3：TestDispatchUsesJourneyAttemptIDForNodeHealthReduction 参数化

测试重构。需要 LP1 + LP2 都完成后才有意义。

---

## 245 部署结构

```
/opt/llm-gateway-go/
├── .env                           # 服务配置（不动）
├── current -> releases/1652-5abd0d6b1/
├── releases/
│   ├── 1651-ffc01e62/            # 上一个版本（回滚目标）
│   └── 1652-5abd0d6b1/           # 当前
│       ├── gateway                # 48713890 bytes stripped
│       ├── configs/ -> 硬链到 1651
│       ├── web/      -> 硬链到 1651
│       ├── VERSION               # 2.5.0-5abd0d6b1-20260821-0126
│       ├── version.json
│       ├── SHA256SUMS            # 9be4c5b53bc0903ab3b0ed60333d8c4cdbe269054a563f20c0a61b6a87909ea1
│       └── deployment.json       # 含 rollback_command
├── .bak/
│   └── 1651-ffc01e62.bak/        # 旧 release 备份
└── logs/raw_data/                 # 大文件（6.9MB+ 每文件）
```

### 回滚命令（manual）
```bash
ln -sfn /opt/llm-gateway-go/releases/1651-ffc01e62 /opt/llm-gateway-go/current
systemctl restart llmgo-245
```

### 凭据（不入此文件）
- 245 `LLM_GATEWAY_API_KEY` 在 `/opt/llm-gateway-go/.env`（不入仓）
- supplier key `sk-iE6T...` 在老板对话中给出（不入仓）
- 245 admin JWT 在本次 session shell 中（不入仓）

---

## 已知问题（不动）

1. **主工作树 12 个 uncommitted 文件**（streaming/handler.go + version + frontend） — 是别人遗留，**与本工单无关**，rule 01 §3 不允许混入我的 commit
2. **245 日志轮转已达 10 文件上限**（gateway.log + gz x10 = 11 个文件）— 候选 #4
3. **telemetry fallback 报错** `malformed array literal: "{\"outbound_tokens\":...}"`（SQLSTATE 22P02）— 候选 #4
4. **glm-5.2 直连 supplier 返回 content:null**（reasoning 用尽 max_tokens=4）— supplier 行为，非网关问题

---

## 关键代码引用（FIXME 给明天的 session）

| 关注点 | 文件:行 |
|---|---|
| URSM NodeView Redis 键 mismatch | `bg/credential_probe_v2.go:380,436,889` |
| routing_resolve URSM 查找 | `admin/routing.go:4795,4882-4898` |
| credentials schema (probe model) | DB `credentials.health_probe_model`, `default_probe_model` |
| glm-5.2 实际 candidates | DB `v_routable_credential_models` (is_routable=t: cred 29, 36) |
| provider_models schema | DB `provider_models(raw_model_name, standardized_name, canonical_id)` |
| credential_model_bindings schema | DB `credential_model_bindings(provider_model_id, available, unavailable_reason)` |
| 已修复 SerializeOpenAI | `internal/ir/serialize_openai.go:23-26` (commit 011df2934) |
| 修复后 fixture | `domains/transformation/testdata/ir_golden/{anthropic,gemini}_to_openai/expected.json` |
| 245 网关 systemd unit | `/etc/systemd/system/llmgo-245.service` |

---

## 后续 session 接续步骤

1. `cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go-wt-ursm-lp1`
2. `git log -5` 确认在 worktree 上
3. 读 `bg/credential_probe_v2.go` line 380-900 摸清 probe 写入 NodeView 路径
4. 按 LP1 plan 实施：
   - 改 `probeCredential` 循环 binding 写 NodeView
   - 新增 `TestProbeWritesNodeViewForEveryBinding`
5. 跑回归：`go test ./bg/... ./domains/ursm/v2/... ./domains/streaming/... ./admin/routing/...`
6. commit + push + 245 部署 + A/B 复现验证 glm-5.2 503 修了
7. 继续 LP2 / LP3（按节奏）

---

## Sensitive Info 声明（rule 39）

本文件不含：
- 任何明文 API key / 密码 / SSH 路径 / token
- supplier 直连 key `sk-iE6T...`（仅在 shell 中用，不入文件）
- 245 admin JWT（仅在 shell 中用，不入文件）
- 245 LLM_GATEWAY_API_KEY `sk-jybFTc1...`（在 245 `.env`，不入文件）

敏感信息交叉引用：
- 245 SSH key 路径在 `envs/servers/<env:HOST_245_IP>/INDEX.yaml`
- supplier key 老板口头提供（不入仓）
- 245 网关 / admin 凭据在 `envs/projects/llm-gateway-go/.env.secrets.plain.yaml` + 245 `.env`
