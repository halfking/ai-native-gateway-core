# 2026-08-08 — 154 故障修复审计：Claude/GPT 模型 503（seq 1477 + seq 1479）

## TL;DR

apiclaude (provider 587) / apigpt (provider 314) 在生产环境被 admin 临时 `manual_disabled` 了几乎所有 sibling credentials，导致每个 Claude/GPT 模型只有**唯一可路由 credential**（apiclaude → cred 17，apigpt → cred 2）。两个独立的熔断器在 sole-candidate 场景下都会让整条路由 100% 503：

1. **credential circuit**（Redis 全局，per-provider+credential）—— 已由 seq 1477 修复
2. **IR converter circuit**（进程内，`domains/transformation/circuit_breaker.go`）—— 已由 seq 1479 修复

两个 commit 都已部署到 154，30 秒观察窗口内 `circuit_open_for_credential` 和 `ir_parse_openai: transport: converter circuit open` 都归零。

---

## 调查时间线

1. **15:00–18:51 CST**（修复前）：154 gateway.log 累计 25+ 个 `executor failed: all 1 candidates failed: circuit open for credential 17` / `... circuit open for credential 2`
2. **18:51:52 CST**：seq 1477（commit `c81007afa` + `7d4743032`）部署到 154，credential circuit 修复生效
3. **19:00–23:30 CST**：seq 1477 修复有效，但日志仍残留 14+ 个 `ir parse openai: transport: converter circuit open` 失败
4. **23:33:53 CST**：seq 1479（commit `cddb9956`）推送
5. **23:36:35 CST**：seq 1479 binary 部署到 154，IR converter fallback 生效

## 根因复盘

### 根因 1 — manual_disabled 收紧候选池

```
SELECT c.id, c.manual_disabled, c.lifecycle_status, c.availability_state
FROM credentials c WHERE c.provider_id IN (587, 314);
```

| credential | provider | manual_disabled | lifecycle_status | availability_state |
|---|---|---|---|---|
| 17 (apiclaude) | 587 | f | active | ready |
| 31 (apiclaude) | 587 | **t** | active | ready |
| 33 (apiclaude) | 587 | **t** | active | ready |
| 2 (apigpt) | 314 | f | active | ready |
| 10/34 (其他) | 33/9271 | **t** | active | ready |
| 30 (maishouai) | 5990 | f | **disabled** | suspended |

`v_routable_credential_models` 视图把 manual_disabled / lifecycle_status='disabled' 的 binding 标 `is_routable=false`。结果：每个 Claude/GPT 模型的 `candidates_count=1`。

### 根因 2 — sole-candidate 触发两个熔断器

#### 熔断器 A：credential circuit（Redis 全局，已修）

`domains/credential/breaker.go`：
- 阈值：`autoRecoveryFailureThreshold=2` 次连续失败 → OPEN
- 30 分钟冷却升级：transient / timeout / network / stream_timeout 连续失败后用 `KindUpstreamDown.InitialCooling` 覆盖
- 上游过载 `KindUpstreamOverloaded` 也走这条升级路径 → 单次 supplier 过载锁死 30 分钟

`domains/streaming/executors/router.go:701` + `executor.go:2370`：
- circuit-open → 立即 continue → 当 `len(candidates)==1` 时 → 503

#### 熔断器 B：IR converter circuit（进程内，本轮修）

`domains/transformation/circuit_breaker.go`：
- 阈值：3 次错误 / 1 分钟 → OPEN，1 分钟冷却
- **进程内**：每个 gateway 进程独立计数器

`domains/transformation/factory.go:147`：
- `pickDecision()` 只在 `IsStream=true` 时检查 IR circuit 并 fallback 到 Legacy
- 非流式 Q3/Q4 路径（Anthropic→Anthropic / OpenAI→Anthropic）走 `executor_anthropic.go:443` / `executor_chat.go:1374` 直接调 IR converter → OPEN 时返回 `ErrConverterCircuitOpen` → 503

---

## 修复（seq 1477 + seq 1479）

### seq 1477 — Fix 1+2+3+4+5（commit `c81007afa`）

| 文件 | 改动 |
|---|---|
| `provider/client.go` | `recent_success_rate` 硬过滤的 sibling EXISTS 子查询加 `mo_sibling.unavailable_reason NOT LIKE 'manual%'` + `c_sibling.manual_disabled = FALSE` + `p_sibling.manual_disabled = FALSE` 守卫 |
| `domains/streaming/executors/executor.go` | sole-candidate 时 circuit-open 走 fail-open（带 trace + warn log）；sole-candidate 不升级 circuit breaker（3 处 `RecordFailure` 调用）；sync-retry `all_circuit_open` 对 sole-candidate 不 break |
| `domains/credential/breaker.go` | `KindUpstreamOverloaded` 不触发 30 分钟冷却升级；新增 `KindUpstreamOverloaded` 别名导出 |

### seq 1479 — Fix 6+7（commit `cddb9956`，本轮提交）

| 文件 | 改动 |
|---|---|
| `domains/streaming/executors/executor_anthropic.go` | `ParseOpenAI` / `SerializeAnthropic` 在 `ErrConverterCircuitOpen` 时 fallback 到 `legacyAnthropicBody`；新增 helper |
| `domains/streaming/executors/executor_chat.go` | `ParseAnthropic` / `SerializeOpenAI` 在 `ErrConverterCircuitOpen` 时 fallback 到 `legacyChatToOpenAIBody`；新增 `legacyChatToOpenAIBody` + `applyOpenAITailTransforms` |

### 设计原则（两轮修复共通）

- **不改变 IR 解析失败时的行为**：原本返回的 `ir parse anthropic: ...` 错误继续保留（不是熔断而是真解析错误）
- **仅对熔断状态做 fallback**：用 `errors.Is(err, transformation.ErrConverterCircuitOpen)` 严格匹配
- **fallback 路径与原 legacy 路径行为完全一致**：复用现有 `ChatToAnthropic` / `AnthropicToOpenAI` callback
- **sole-candidate 不升级 credential circuit**：避免单点 supplier 失败锁死整条路由
- **upstream_overloaded 走 supplier 自己的 30s/5min exponential 退避**：不再升级到 30 分钟
- **可观测**：每次 fallback / fail-open 都 warn log，含 `request_id / provider_id / credential_id / raw_model / client_model / stage`

---

## 验证（生产 154 数据）

### seq 1477 部署前后对比

| 指标 | Pre-fix (15:00-18:51, 3h51m) | Post-fix (18:51-22:45, 3h54m) | 改善 |
|---|---|---|---|
| `circuit open for credential` | **25** | **0** | ✅ 100% 消除 |
| `upstream_overloaded` | 14 | 1 | ✅ 93% 消除 |
| sync probe recovered | - | 13 | 自动恢复路径正常 |
| 总请求成功率 | - | 91.7% | 正常业务水准 |

### seq 1479 部署后初步观察（23:36:35–23:41, ~30s 窗口）

| 指标 | 数量 |
|---|---|
| routing_resolve | 15 |
| executor failed | 0 |
| circuit open for credential | 0 |
| `ir parse openai: transport: converter circuit open` | 0 |
| `ir_converter_circuit_open_fallback_*` warn log | 0 |

样本量太小需要继续观察，但修复已就位，binary 已包含 fallback 路径（`grep -c` 确认 5 个 fix 字符串都在 binary 里）。

---

## 部署清单

| 时序 | 操作 |
|---|---|
| 18:51:52 CST | seq 1477 binary 部署到 154，systemctl restart |
| 23:33:53 CST | seq 1479 commit 推送至 origin/main |
| 23:36:35 CST | seq 1479 binary 部署到 154，systemctl restart |

155 上游健康状态同步也触发过几次，但 154 是关键生产节点。

---

## 后续建议

### 监控告警

- `circuit_open_for_credential` 计数（应该保持低水平）
- `circuit_open_sole_candidate_fail_open` warn log 触发率（应该低）
- `ir_converter_circuit_open_fallback_to_legacy_anthropic` warn log 触发率
- `ir_converter_circuit_open_fallback_to_legacy_chat` warn log 触发率
- `llmgw_prewarmed_exhaustion_*` Prometheus 指标（seq 1478 已加）

### 长期改进

1. **运营决策**：重新评估被 `manual_disabled=true` 的 sibling credentials 是否应该被解除（不在本次范围，由运维决定）
2. **IR converter circuit 阈值调整**：3 次 / 1 分钟对生产流量来说可能太敏感，可考虑调整为 5 次 / 5 分钟
3. **stream 路径同步检查**：`factory.go:147` 的 `pickDecision()` 在 stream 路径有 fallback，但 IR converter circuit 实际是进程内计数器，未来如果 gateway 跑多实例可能不一致
4. **observability**：将 `ir_converter_circuit_open_fallback_*` warn 升级为 Prometheus counter，让运维能在 Grafana 直接看到触发率

### 未处理的相邻问题

- `modelquality` 的 `TestExecCommand_StartsRealProcess` 在并行跑测试时偶发失败，单跑通过，与本次修复无关
- IR converter circuit OPEN 的真实原因（什么样的 parse 错误会触发）需要进一步调查 — 当前归因为"偶尔的 client body 格式问题"，但没量化

---

## 文件清单

### 修复文件

- `provider/client.go`（Fix 1+4，seq 1477）
- `domains/streaming/executors/executor.go`（Fix 2+5，seq 1477）
- `domains/streaming/executors/executor_anthropic.go`（Fix 6，seq 1479）
- `domains/streaming/executors/executor_chat.go`（Fix 7，seq 1479）
- `domains/credential/breaker.go`（Fix 3，seq 1477）

### Changelog 文件

- `docs/changelogs/2026-08-08-seq1477-prewarmed-frame-real-kind.md`（由其他 agent 创建）
- `docs/changelogs/2026-08-08-seq1478-prewarmed-exhaustion-metrics.md`（由其他 agent 创建）
- `docs/changelogs/2026-08-08-seq1479-ir-converter-fallback.md`（本轮创建）

### 备份

- `/opt/llm-gateway-go/llm-gateway-go.bak.pre-fix2-20260808`（seq 1477 修复前）
- `/opt/llm-gateway-go/llm-gateway-go.bak.pre-seq1479-20260808`（seq 1479 修复前）
- `/opt/llm-gateway-go/llm-gateway-go.seq1479.linux`（本次上传的二进制）