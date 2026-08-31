# 网关错误修复 Phase 2 执行报告 - 2026-09-01（凌晨会话）

> **接续**: `gateway-error-fix-progress-20260901.md`（Task 4/5 阶段）
> **执行者**: ZCode 会话（02:55 handoff 之后）

---

## TL;DR

Task 4/5 的"部署+验证"已完成，但验证过程**推翻了原修复计划的核心假设**：
holdback window（5s/20 chunks）对 48h 内全部 12 个 `survival_resume_blocked` 事件
的覆盖率为 **0%**——所有事件都发生在 27~5568 chunks、35~178 秒之后，远超窗口。
holdback 已在 154/245 启用（无回归风险，OPT-IN 默认禁用），但**不要期待它降低
resume_blocked 发生率**；需要新的方案（见"下一步"）。

---

## 一、环境事实（与 handoff 文档的差异修正）

| 项 | handoff 文档说法 | 实际情况 |
|---|---|---|
| 154 survival | 未提及 | **之前未启用**（无 REQUEST_SURVIVAL env），本会话 05:15 已启用（mirror 245 配置） |
| 245 survival | 未提及 | **已启用**（`.env` 988-991 行），04:16 重启已加载 |
| 245 holdback | 待配置 | `.env` 998-999 行已有（并行会话 03:13 配置），04:16 生效 |
| 154 holdback | 待配置 | 本会话 04:15 写入 env-file，05:15 生效 |
| 错误现场 | "154 和 245 频繁出现" | **只在 245**：154 的 DB（252 PG）近 7 天无任何 survival/empty 错误记录 |
| resume_blocked 计数 | "发生率" 待统计 | 48h 内 12 个请求（每个 1 次），24h 内 36 次日志行 |

### 关键事件时间线（245）

- 03:13 并行会话在 `.env` 配置 holdback（998-999 行）
- 04:16:07 进程重启加载 survival+holdback env（PID 565208，release 1865-3896f727）
- **04:21、04:38、04:46、04:55 仍出现 resume_blocked**（holdback 生效后！）——见 §三
- 04:55 样本：minimax-m3，`stream_chunks:59`、`latency_ms:162740`（162 秒）后网络中断

### 154 环境变更（本会话）

1. `/etc/llm-gateway-go/env` 追加（有备份 `env.bak.survival-2026090105*`）：
   - `LLM_GATEWAY_REQUEST_SURVIVAL_ENABLED=true`（mirror 245）
   - `LLM_GATEWAY_REQUEST_SURVIVAL_INTERACTIVE_DETEADLINE_SECONDS=86400`
   - `LLM_GATEWAY_REQUEST_SURVIVAL_RETRY_MAX_SECONDS=120`
   - `LLM_GATEWAY_REQUEST_SURVIVAL_MAX_ATTEMPTS=100`
   - `LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW_MS=5000`
   - `LLM_GATEWAY_RECOVERY_HOLDBACK_MAX_CHUNKS=20`
2. 服务重启验证：
   - 05:15:28 `request_survival_armed` 日志出现 ✓
   - 05:16:13 真实 minimax-m3 流式请求 → `survival_attempt_start` 日志：
     `"holdback_window_ms":5000,"holdback_max_chunks":20` ✓（Task 4 验证标准达成）
   - healthz OK，37 chunks 正常响应
3. 期间撞上并行部署（05:06 部署 1868-e2de193a 切到 8781，清了 8782 slot），
   我的 restart 8782 失败（二进制已删）→ 已停 8782 unit，改 restart 8781 成功。

### 并行会话部署记录（154）

- 04:08 build_seq 1866（49fa7858）→ 04:16 1867（53556f44）→ 05:06 1868（e2de193a）
- 本地未提交的 VERSION/version.json/web/public/* bump 产物即 1866 那次

---

## 二、Task 4 验证结果

| 验证项 | 结果 |
|---|---      |
| `survival_attempt_start` 出现且含 holdback 参数 | ✅ 05:16:13，5000/20 |
| `gate_state_advanced` / `early_empty_delta_detected` | ⚠️ Debug 级，`LLM_GATEWAY_LOG_LEVEL=info` 不采集（如需观察临时调 debug） |
| minimax-m3 流式请求 200 | ✅ 37 chunks |
| 服务健康 | ✅ healthz/version 正常 |

`gate_state_advanced` 是 Debug 级是**设计选择**（避免生产日志洪水），如需在 154
观察可在 env 加 `LLM_GATEWAY_LOG_LEVEL=debug` 后重启（注意日志量）。

---

## 三、核心发现：holdback 窗口对失败人群覆盖率 0%

方法论：取 48h journal 中全部 `survival_resume_blocked` 的 request_id（12 个），
关联其 `audit: request completed` 行的 `stream_chunks` / `latency_ms`：

| # | 模型 | latency_ms | stream_chunks | 在窗口内？ |
|---|---|---|---|---|
| 1 | glm-5.2 | 100,649 | 5,568 | 否 |
| 1 | glm-5.2 | 38,577 | 1,191 | 否 |
| 1 | minimax-m3 | 36,323 | 139 | 否 |
| 1 | glm-5.2 | 35,083 | 231 | 否 |
| 1 | deepseek-v4-flash | 59,485 | 99 | 否 |
| 1 | minimax-m3 | 160,614 | 27 | 否（最接近，仍超） |
| 1 | minimax-03 | 43,272 | 66 | 否 |
| 1 | minimax-m3 | 177,706 | 42 | 否 |
| 1 | minimash-m3 | 40,015 | 199 | 否 |
| 1 | glm-5.2 | 64,062 | 1,910 | 否 |
| 1 | minimax-m3 | 162,740 | 59 | 否 |
| 1 | minimax-m3 | 165,584 | 31 | 否 |

（表格第 1 列统一为 attempt 1；按 request_id 去重共 12 请求）

**结论**：

1. 最小样本 27 chunks / 36 秒——所有失败都在窗口（20 chunks / 5s）**关闭之后**。
2. 04:16 holdback 生效后的 4 次 resume_blocked（04:21/04:38/04:46/04:55）证明
   窗口确实管不住这类失败：这些是**长流中段网络中断**（othersapi.com 中继，
   network_error），不是"前几个 chunk 就掉"。
3. 原分析文档断言"Minimax-m3 和 GLM-5.2 经常在前几个chunk就出问题"与实测数据
   **不符**——对 resume_blocked 人群不成立。
4. Holdback 依然值得保留（防御未来早期失败，OPT-IN 无害），且 5s 窗口对
   minimax-m3（~2-6 秒/chunk）意味着首 token 可能被扣 5 秒——对交互延迟敏感
   的场景要评估。

### 0-chunk 空响应人群（与 resume_blocked 不同的人群）

- 全部是 **gpt-5.6-terra**（~1s 失败，`stream_chunks:0`），37 次/48h
- **error_kind 为空**（可观测性缺口，见 §五）
- 分析文档归因于 glm-5.2/minimax-m3 的 empty_model_response 在 48h 窗口内
  **没有 DB 记录**（error_kind LIKE '%empty%' = 0 行）

---

## 四、Task 3 核对结果（错误分类与节点状态）

`errorsx/classify.go:578-600` `overloadKindForStatus`：

- 500/502 + overload body → `KindUpstreamOverloaded`：不触发 tuner 降并发
  （credentialhealth/tuner.go 只认 KindConcurrent），保留 failover 权
  （isTransientFailoverKind / transient continue-list 都包含它）
- 429/503/529 → `KindConcurrent`：触发并发自动调优

节点状态：`route_node_recorder.go:73` `isTransientRouteNodeFailure` 包含
`KindUpstreamOverloaded` → 标记为瞬时失败而非节点下线，不会长期拉黑。

结论：**分类逻辑健全**，分析文档 §4 的疑虑不成立（代码注释已明确设计意图）。
另有 `failover_completeness_test.go` 结构化测试钉住不变量。

---

## 五、遗留问题（新发现）

1. **resume_blocked 无有效修复**：holdback 覆盖率 0%，长流中断需要别的方案：
   - 方向 A：L2 前缀对齐续传（StreamRecoveryConfig 已有
     AlignmentThresholdBP/CommittedPrefix 骨架，`ContinuationEnabled` 默认关）
   - 方向 B：客户端可见重启（AllowVisibleRestart / stream-undo 能力声明）
   - 方向 C：收紧 minimax-m3/glm-5.2 的 credential 级 stream timeout /
     TTFB 超时，让失败尽量早发生（挪进窗口内）
   - 方向 D：中继层（othersapi.com）网络质量治理或绕开
2. **error_kind 为空的失败**（gpt-5.6-terra 37 次/48h）：audit 行 success=false
   但 error_kind 空——上游分类没落库，需查 audit pipeline
3. **sessionv2mirror shadow write 失败**（245 journal 频繁 WARN）：
   `insert bodies: ERROR: there is no unique or exclusion constraint`——252 PG
   缺唯一约束，shadow 写入持续失败（不影响主路径但污染日志且 V2 迁移欠账）
4. **154 → 245 的部署缺口**：1866/1867/1868（含新日志代码）尚未部署 245，
   245 还在 1865-3896f727（v2.5.0）。245 是 resume_blocked 现场，**新日志
   不上 245 就观察不到真实失败人群的决策路径**
5. **PEM 多行值解析警告**：`/etc/llm-gateway-go/env` 的
   `LLM_GATEWAY_LICENSE_PUBLIC_KEY` 多行 PEM 导致 systemd 每次启动报
   "Ignoring invalid environment assignment"（既有问题，非本会话引入）。
   154 基础 systemd 版本太老，建议把 PEM 压成单行 base64。

---

## 六、成功标准修正（供 24h 观察用）

原进度报告的指标已不适用，修正为：

1. **resume_blocked 发生数**：holdback 上线前后应**持平**（预期不变，0% 覆盖）
   - `journalctl -u llmgo-245-canary@<port> --since "24 hours ago" | grep -c survival_resume_blocked`
2. **survival 重试成功率**：观察 `request_survival_finished` 中 `succeed:true` 比例
3. **首 token 延迟**：p95 增幅应 ≤ 5s（minimax-m3 慢流速下可能明显）
4. **新日志覆盖**（需 245 部署 ≥1866）：每个 streaming 请求应有
   `survival_attempt_start`；失败应有 `survival_attempt_outcome` +
   `request_survival_finished` 三件套
5. **gpt-5.6-terra 空响应**：单独跟踪（error_kind 空，靠 journal
   `empty_stream_no_content` / `early_empty_detection` 关键字）

---

## 七、本会话操作清单（审计用）

| 时间 | 服务器 | 操作 | 可回滚 |
|---|---|---|---|
| 04:15 | 154 | env 追加 holdback（备份 env.bak.holdback-20260901041534） | 删两行+restart |
| 05:12 | 154 | env 追加 survival 4 行（备份 env.bak.survival-*） | 删四行+restart |
| 05:14 | 154 | stop 失效的 8782 unit（并行部署遗留） | n/a |
| 05:15 | 154 | restart 8781 → survival armed | restart |
| 05:16 | 154 | 测试请求 ×1（minimax-m3） | n/a |

未动 245 任何配置（245 的 holdback 是并行会话 03:13 配的）。
未动任何代码（1868 是并行会话部署的）。

---

**文档版本**: 1.0
**创建时间**: 2026-09-01 05:30 前后
**状态**: Task 4/5 验证完成；修复方向需重估（见 §五.1）
