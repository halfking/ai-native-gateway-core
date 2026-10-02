# 探测体系成本优化方案（2026-09-25）

> 定位：可测试的优化方案。每项改动给出涉及文件、可调参数、预期收益与验收
> 门槛；§8 测试计划可直接转测试用例。数据基线 = 本地 llm_gateway
> 生产同构库 2026-09-25 取证（48h 窗口，见 §2）。
> 前置关联：probe-volume policy INV-1/INV-3（2026-09-20，node_probe 错误
> 证据准入 + 自检 3 天使用窗）、会话优化v2/32 系统监测设计、
> perf-2026-09-05-core-quality-baseline。
>
> 本文所有 SQL 均在本地库实测通过，可直接复跑作验收。
>
> **修订记录（2026-09-25 批判式复审）**：初版对 node_probe 退避机制的定性
> 有误——系统**已有**错误分类退避（`bg/probe_recovery_policy.go`），网络类
> 走 5s..60s 短梯是**有意设计**（快速发现上游恢复），auth/404 类实际会沿
> 通用梯沉至 6h。真正的缺陷是**短梯封顶后无长尾沉底**。P0-2 已按实证重写；
> §7 恢复路径的 de-escalate 钩子表述已如实收窄；§6 自检 ping 门槛按放行
> 后行为重估放宽；基线快照数字统一到同一次取证。
>
> **修订记录（2026-09-25 第二轮，并入 59c7f4233）**：同日并入 main 的
> 59c7f4233（对健康节点零探测 + 探测失败根因三分类）已实现本方案的部分
> 诉求——①direct 成功即终态结算，gateway 轮失败不再武装重试梯（本方案
> D5/D6 的重探驱动部分）；②node/protocol/gateway 根因分类，protocol 类
> （404/405/410/415/契约形 400/422）第二次起 6h 停放（P0-2 初版的
> "结构性分类"诉求）；③修掉 stale-state 调解器重排可证健康节点的 bug
> ——该 bug 也是基线 periodic 量的来源之一，初版归因不完整。§1/§2.2/
> §4/§5/§6 已同步此变化；P0-2 剩余增量 = 网络类短梯长尾化 +
> request_failure 频控，仍然成立且未被覆盖。
>
> **落地记录（2026-09-26，r0926）**：P0-1+P0-3 部署实测生效（build
> 2252：gw_rpm_exceeded 探测弹回 738/h→0，仅启动 keyInfo 缓存窗 290 条；
> node_probe_runs ~5,000/h→~1,610/h）。P0-2 已落地（commit 1082ab0b8）：
> 短梯长尾化 + request_failure 频控 + 队列代际 attempt 重置（r0925
> handoff 遗留 1，attempt = max(task.Attempt, cf+1)）。同轮发现并根修
> 59c7f4233 的 defer 断链 P1：probeDirect/probeGateway 匿名返回值使
> root-cause 分类/标注永不到达调用方——root_cause_total 的 protocol/
> gateway 占比恒 0 即其症候，§8.3 验收时以修复后分布为准。验收基线更新：
> P0-2 的边际收益以 2252 实测 ~1,610 runs/h 为对照（基线 §2 的
> 232,402/48h 中约四分之一由 P0-1/P0-3 治掉）。r0925 遗留 3 的 7 行存量
> 污染已一次性清洗（凭据 manual_disabled 冻结，"下次相遇收敛"不成立）。
> 详见 docs/handoff/20260926-probe-p02-longtail-throttle-audit.md。

---

## 1. 现状盘点：五条探测管道

| 管道 | 触发/节奏 | 探测矩阵 | 单次动作 | 落库 |
|---|---|---|---|---|
| node_probe（`bg/node_probe.go`） | tick 30s 拾取到期行；request_failure 即时触发。退避按错误分类（`bg/probe_recovery_policy.go ProbeBackoffForErrCode`）：通用梯 5s→30s→60s→5m→1h→2h→**6h 封顶永续**；**网络/超时/5xx 类走短梯 5s→15s→30s→60s 封顶永续（无长尾，见 D2）**；上游 429 以 3min 为下限正常爬通用梯；404 连续两次确认后进 6h 复核档；quota 类固定 2m/5m；broken_confirmed 30d。**59c7f4233 起**：direct 成功即终态结算（healthy 节点零重排），失败按根因三分类（node/protocol/gateway，`bg/probe_root_cause.go`）标注并驱动退避 | `node_probe_state` 638 对 (credential, model) | direct 轮（直连上游，max_tokens=10 ping）成功后才跑 gateway 轮（网关回环，带 Pin-Credential） | `node_probe_runs` 每 attempt 一行 |
| credential_selfcheck（`bg/credential_selfcheck.go`） | 每 **5 分钟**一个周期，扫"有近期失败"的凭据 | 每凭据选 1 个主模型（featured ∪ 3 天业务使用）+ **全部到期失败绑定**；成功即停、失败逐个试完 | 每模型 1 次 chat ping；成功后追加工具调用轮 | `credential_selfcheck_runs` + 审计行 |
| model_probe（`bg/model_probe.go`） | 共识循环 5min；featured 深探 30min | `model_probe_state` 797 行 | chat ping / models-list 分层 | `model_probe_state` |
| systemmonitor（`bg/systemmonitor/`） | 任务队列（direct_ping / gateway_ping / chat_minimal / chat_tool / chat_stream / http_ping） | 任务表驱动 | 按任务类型 | `system_probe_runs` |
| unified probe coordinator（`domains/routingstate/probe_coordinator.go`） | shadow 模式（仅记录准入/去重/抑制） | — | 不调度 | — |

队列底座：`credential_probe_queue`（当前 30.06 万行存量，48h 新增 3.33 万，pending 0——写入/消费吞吐均衡，但存量为审计负担）。

## 2. 48h 实测基线（取证 SQL 见 §8.3）

### 2.1 探测总量与成功率

| 指标 | 数值 | 来源 |
|---|---|---|
| node_probe_runs 总尝试 | **232,402 次 / 48h**（≈96 次/分钟） | `node_probe_runs` |
| ─ periodic 触发 | 191,236（失败 191,191，成功 **45**） | 同上 |
| ─ request_failure 触发 | 35,385（成功 413） | 同上 |
| ─ sync_request 触发 | 5,781（成功 1,003） | 同上 |
| runs 整体成功率 | **0.63%**（1,461 / 232,402） | 同上 |
| request_logs 中 node_probe 行 | 83,146（成功 3.8%） | `request_logs_hot` |
| 自检 ping（网关请求，未标记行） | 41,056（成功 **0**，估算 token 103.8 万） | 同上 |
| 探测矩阵 | 638 对（state）；失败对退避分布（2026-09-25 快照，随窗滚动）：337 对 @6h、59 对 @30d、36 对 @1h、21 @3h、20 @2h、**45 对 @60s**、8 @5min、6 @15s、6 @30s（**≤5min 梯级共 65 对**；多次抽样 37~60 对波动） | `node_probe_state` |

### 2.2 失败构成（零信号归因）

| direct 错误类别 | 48h 次数 | 性质 | 现行退避（`ProbeBackoffForErrCode`） |
|---|---|---|---|
| `probe_direct_rate_limited`（上游 429） | **36,115** | 上游对我们限流——探测自身制造 | 3min 下限 + 通用梯（会爬至 6h）；但 request_failure 触发无 pair 级频控，持续限流期间每条业务失败都引发直连探测 |
| `probe_direct_network_error` | 19,386 | 瞬时类 | **短梯 5s→15s→30s→60s 封顶永续——无长尾（D2 主因）** |
| `probe_direct_http_503` | 14,782 | 瞬时类 | 同上（短梯封顶永续） |
| `probe_direct_http_500` | 4,362 | 瞬时类 | 同上 |
| `probe_direct_http_502` | 2,155 | 瞬时类 | 同上 |
| `probe_direct_auth_failed`（401/403） | 872 | 结构性——恢复依赖换 key | 通用梯（会沉至 6h，初版"重试无意义"的表述不准确） |
| `probe_direct_http_400` / `404` / `410` | 1,570 | 结构性——模型/参数不存在 | 404 双确认后 6h 复核档（已有）；400/410 走通用梯 |
| `probe_direct_endpoint_build` | 194 | 结构性——网关侧配置坏 | 通用梯 + 网关侧 15min 固定延迟 |
| `probe_direct_timeout` | 560 | 瞬时类 | 短梯封顶永续 |

**梯级滞留实测（2026-09-25）**：60s 封顶档 45 对的 `last_err_code` 构成 =
connection_error×25、http_503×7、timeout×3、http_500×2——**全部是网络/5xx
类短梯封顶对**，坐实 periodic 19.1 万次/48h 的主体来源之一；1h 档滞留的
403/410/429 对证明非网络类确实在沿通用梯正常沉底。上表"现行退避"列描述
的是**取证时点（59c7f4233 合入前）**的行为；该提交合入后，404/410/契约形
400/422 已按 protocol 根因第二次起 6h 停放，健康节点不再被 stale-state
调解器重排（第二个 periodic 量源，初版归因遗漏，已修）。

### 2.3 风暴直接诱因（已实锤）

自检 worker 自动生成的系统密钥 `key_tier` 落到列默认值 `'default'`
（`EnsureSystemAPIKey` 的 INSERT 未设置 tier），对应 `tierDefaults["default"]`
= **12 RPM**（`domains/authentication/verifier.go:74`）；而自检实际发射
~100 请求/分钟 → **86%（35,440/41,056，实测）被自家网关 RPM 弹回**（
`gw_rpm_exceeded`），这些 429 又把模型逐个打入"到期失败"矩阵，下一周期
fallback 逐个重试全部失败模型——**自增强风暴**，且每条弹回都产生一条
request_logs 行（已由 8993ec2d5 标记为探测，但请求本身零信号）。

## 3. 成本四维拆解

 1. **上游配额/RPM 烧蚀（最大隐性成本）**：23.2 万次直连探测 / 48h。其中
   3.6 万次 429——探测在消耗业务赖以使用的上游 RPM 预算，**探测本身成为
   业务被限流的诱因之一**；成功探测每次 ~10-20 token（量小但纯开销）。
2. **网关自耗**：12.4 万网关请求 / 48h（8.3 万 node_probe gateway 轮 +
   4.1 万自检 ping），每条走完鉴权→限流→路由→日志全链，另写侧表
   `request_context_attrs`（行数翻倍）。
3. **DB 写入与存储**：`node_probe_runs` 23.2 万行/48h（14 天保留 ≈
   680 万行常驻）+ request_logs ≈12.4 万行/48h + 队列 3.3 万行/48h。
   共享 PG（多项目）上的持续 INSERT 压力与 VACUUM 负担。
4. **信噪比（准确度成本）**：0.63% 成功率——健康信号被结构性失败淹没，
   探测面板与状态机（healthy/recovering/broken）的判定证据被污染；每次
   失败又触发 request_failure 联动（3.5 万次/48h），形成二次放大。

## 4. 缺陷清单（浪费归因 → 优化映射）

| # | 缺陷 | 证据 | 优化项 |
|---|---|---|---|
| D1 | 自检系统密钥 tier 缺省 default（12 RPM），风暴源头 | §2.3 | P0-1 |
| D2 | 网络类短梯封顶后**无长尾沉底**：上游长时间宕机时每对每分钟 1 发直连轰炸、永不升级 | 60s 档滞留 45 对全部为 connection_error/503/timeout/500（§2.2 实测） | P0-2 |
| D3 | 自检 fallback 风暴：主模型被 429 后仍逐个试完所有到期失败模型；429 弹回反喂失败矩阵 | §2.3，41,056 条 0 成功 | P0-3 |
| D4 | request_failure 触发缺 pair 级最小间隔（仅 5min in_flight 去重）：持续限流的上游每条业务失败都引发一次直连探测 | §2.2 rate_limited 36,115 次 + request_failure 触发 35,385 次 | P0-2 |
| D5 | 有业务成功证据的对仍被主动深探（gateway 轮 / featured 深探不联动业务证据）。59c7f4233 已修其重探驱动面，剩余诉求见 P1-1 | 业务成功行与探测矩阵重叠 | P1-1 |
| D6 | 每次 direct 成功都追加 gateway 回环轮（8.3 万/48h），对有业务流量的对增益近零。基线量含 stale-state 调解器 bug 贡献（59c7f4233 已修），剩余为正常周期 | `node_probe.go:1641`（`res.direct.ok` 分支内追加，非无条件） | P1-1 |
| D7 | node_probe_runs 每 attempt 全量落库，重复同错不采样 | §3-3 | P2-3 |
| D8 | featured 深探 30min 固定频率，无业务证据联动 | `spec_probe.go`（默认 1800s） | P1-1 |

## 5. 优化设计

> 原则：**瞬时故障保发现速度，结构性故障停无效重试，有业务证据不重复
> 验证**。所有开关走 `settings`（热加载），默认值即目标态，回滚 = 关开关。

### P0-1 自检系统密钥 tier 修复（成本：清零零信号弹回）

- 改动：
  1. `bg/self_check_worker.go EnsureSystemAPIKey`：INSERT 显式
     `key_tier='system'`（300 RPM 档）。
  2. 启动自愈（幂等）：发现 `is_system=TRUE AND owner_user IN
     ('self-check-worker','credential-selfcheck-worker') AND
     COALESCE(key_tier,'default')='default'` 的密钥时 UPDATE 为
     `'system'` 并打 slog.Warn（存量 key 727 类问题的自动修复，防新部署
     踩同坑）。
  3. 存量数据迁移：`sql/migrations/` 新增一条 UPDATE（走 installer 六点
     同步门禁）。
- 机制依据（复审已核实）：网关 RPM 限流**按 key 分桶**——
  `checkGatewayRateLimit` 以 `AdmitRPM(ctx, keyInfo.ID, limit)` 为粒度
  （`domains/streaming/rate_limit.go:89`），limit = `EffectiveRPM()` =
  DB `rate_limit_rpm` 为 NULL 时取 tier 默认。tier→system 即把自检自己的
  桶从 12 提到 300 RPM，不影响其他 key；备选 `is_internal`（完全豁免限流，
  `rate_limit.go:66`）过于激进，不采用。
- 参数：无需新增（沿用 tier 语义）。
- 预期：`gw_rpm_exceeded` 探测弹回 35,440/48h → **≈0**；自检请求从
  "被限流撕碎"恢复为全额放行（注意：放行后单请求量可能短暂高于基线，
  总量收敛由 P0-3 的 429 熔断与 P1-1 证据联动负责）。

### P0-2 网络类短梯长尾化 + request_failure pair 级频控（成本：砍 periodic 主体）

> 复审更正：初版"引入错误分类映射（结构性 6h 起步）"建立在对现状的误判
> 上——分类机制已存在（`ProbeBackoffForErrCode`），auth/404 已会沉至 6h；
> 且 59c7f4233 已把 protocol 类（404/405/410/415/契约形 400/422）落地为
> 第二次起 6h 停放——初版的"结构性分类"诉求已被覆盖。**真缺陷是网络类
> 短梯（5s→15s→30s→60s）封顶后永续 60s、无长尾**，本项剩余增量即修它。

- 改动 1（短梯长尾化）：`bg/probe_recovery_policy.go ProbeBackoffForKind`
  的网络类分支——attempt 走完短梯 4 步（5s/15s/30s/60s）后，**切换到通用
  梯的后续档位继续爬**（5m→1h→2h→6h 封顶），而非永续 60s。前 4 步不动，
  秒级~1 分钟的恢复发现速度完整保留；持续宕机对的探测频率从 1 次/分钟
  衰减到 1 次/6h（与 auth/404 类今日行为一致）。新链形：
  `5s→15s→30s→60s→5m→1h→2h→6h`（后四步直接复用通用梯 `NodeProbeBackoffChain` 的档位，纯函数，可单测）。
- 改动 2（request_failure 频控）：request_failure 触发入队前检查
  `node_probe_state.next_retry_at`——该对的探测已在排程（next_retry_at
  未到且非 success 状态）则跳过本次触发（语义：一次业务失败已足够触发
  验证，同分钟内第 N 条失败不应引发第 N 次探测）。现仅有的 5min
  in_flight 去重不区分"探测在途"与"探测已排程"。
- 参数（`settings/spec_probe.go` 新增）：`probe.network_chain_long_tail`
  （bool，默认 true）、`probe.request_failure_min_gap_seconds`（默认 60；
  0=现行行为）。可选增量：`probe.structural_floor_seconds`（默认 0=off，
  打开后 auth/400/build 类起步即抬到该值；默认关闭因其边际收益小——
  通用梯已会沉底，且抬高会延迟"换 key 后"的自动发现，见 §7）。
- 预期：60s 封顶档 45 对（§2.2 实测，periodic 19.1 万次/48h 的主体）
  沉入 ≥5min/≥1h 槽位；periodic 触发 19.1 万/48h → **≤2 万/48h**。

### P0-3 自检 429 周期熔断（成本：终止自增强风暴）

- 改动：`bg/credential_selfcheck.go doRequest` 收到网关 429 /
  `gw_rpm_exceeded` / `key_throttled` 时，置周期级 `rateLimited` 标记，
  `runOne` 的 fallback 循环立即终止本凭据剩余候选（语义：网关层拒绝=
  本凭据本周期不可用，逐个再试只是放大）；周期间记忆（下一周期若上一
  周期熔断，只试主模型 1 次）。
- 参数：`probe.selfcheck.ratelimit_abort`（bool，默认 true）。
- 预期：429 弹回反喂失败矩阵的回路被切断。**总量门槛诚实重估**：tier
  修复（P0-1）后请求全额放行，fallback 行走反而会比基线更"跑得动"，单靠
  P0-3 只压缩 429 型行走；§6 门槛定为 ≤20,000/48h（-51%）而非初版的
  ≤2,000（该数字不可辩护），进一步收敛依赖 P1-1 证据联动。

### P1-1 业务成功证据联动（效率：不重复验证业务已验证的事实）

- 改动：featured 深探与 node_probe gateway 轮执行前查
  "(credential, model) 在 N 小时内有成功业务（非探测）流量"（复用
  `probeTrafficExclusionPredicate` + 3 天窗）；命中则本轮跳过深探
  （state 仍按业务成功续期 healthy 证据，状态机语义不变）。
- 参数：`probe.skip_on_business_evidence_hours`（默认 24；0=关闭）。
- 预期：gateway 回环轮 8.3 万/48h → 与业务活跃对数量相当（估 **-60%
  以上**）；featured 深探同步降频。

### P1-2 provider 级聚合探测（成本：降上游配额烧蚀）

- 改动：periodic 泵选行时按 `(provider_id, raw_model_name)` 分组，同组
  多凭据先探 1 个代表凭据；代表失败（transient 类）才触发同组其余凭据
  逐个下钻；代表失败为结构性（404 双确认 / auth 类，`ProbeBackoffForErrCode`
  现有分类）则不下钻——同上游同模型大概率同样失败，下钻只是重复计费。
- 参数：`probe.provider_aggregation`（bool，默认 true）。
- 预期：同上游多凭据重复探测的上游请求数 **-40%~70%**（取决于矩阵重叠
  度；本地 35 凭据 / 276 模型的矩阵重叠显著）。

### P2（观测与护栏，配合 P0/P1 落地）

- **P2-1 探测预算熔断**：全局 periodic 探测速率上限
  `probe.periodic_budget_per_min`（默认 30）；超限时泵只出队
  request_failure 触发行，periodic 挪到下窗。防未来配置错误再次自我
  DDoS。
- **P2-2 成本指标**：`probe_attempts_total`、`probe_zero_signal_ratio`
  （上游 429 + 网关弹回占比）、`probe_estimated_tokens_total`、
  `probe_matrix_active_pairs`；探测仪表盘加预算卡片。
- **P2-3 runs 采样落库**：同 (pair, err_code) 连续失败只写首末行 +
  `attempt` 计数累加（state 表本来就有全部状态）；参数
  `probe.runs_sampling`（bool，默认 true）。`node_probe_runs` 写入
  **-70% 以上**。

## 6. 预期收益与验收门槛

上线后以 48h 窗口对比基线（§2），**全部达标才算验收通过**：

| 指标 | 基线（48h） | 验收门槛 | 依据 |
|---|---|---|---|
| 网关 RPM 弹回的探测请求（零信号） | 35,440 | **≤100（-99.7%）** | P0-1 |
| 自检 ping 网关请求 | 41,056（0 成功） | **≤20,000（-51%）且成功率 ≥30%**（初版 ≤2,000 系对 P0-1 放行效应估计不足，已重估） | P0-1+P0-3 |
| `node_probe_runs` 写入行数 | 232,402 | **≤60,000（-74%）** | P0-2+P2-3 |
| periodic 探测触发 | 191,236 | **≤20,000（-90%）** | P0-2（短梯沉底）+ 59c7f4233（健康节点零重排） |
| runs 整体成功率（信噪比） | 0.63% | **≥20%** | 全部 |
| 短暂抖动（<1h）的故障/恢复发现 | ≤60s（短梯） | **不劣化（前 4 步 5s..60s 保留）** | P0-2 |
| 持续宕机（>1h）的恢复发现 | ≤60s/分钟级轰炸换来 | **放宽至 ≤6h（与 auth/404 类今日一致）——明示的权衡，非回退** | P0-2 |
| 业务请求成功率 | 基线 | **不回退**（探测不再抢上游 RPM） | 侧面收益 |

## 7. 风险与权衡

| 风险 | 缓解 |
|---|---|
| 持续宕机对的恢复发现从 ≤60s 放宽至梯级当前位置（最深 6h） | 这是本方案的核心取舍（用发现延迟换上游配额与零信号）；短暂抖动（<1h）发现不受影响；可选 `structural_floor` 默认关闭也是同一顾虑 |
| 结构性失败（auth）下"换 key 后"的自动发现 | 复审已核实：现有 `deescalateGatewaySideProbeState` 仅覆盖 decrypt/endpoint-build 网关侧错误（启动清扫 + 解密熔断两个触发点），**不覆盖换上游 key 场景**——如实依赖 ①通用梯 6h 档自然复探 ②绑定恢复冷却（`recoverExpiredBindings`）③手动探测端点；若需即时生效，须新增"凭据 key 轮转事件→拉回该凭据探测行"钩子（列为后续小改动，非本期承诺） |
| 业务成功证据免探测的窗口错配（业务成功≠该凭据所有模型健康） | 证据按 (credential, model) 粒度判定，非凭据级；窗口 24h 可调、0 即关闭 |
| provider 级聚合掩盖单凭据劣化 | 仅聚合 periodic 巡检；request_failure 触发与 sync 探测不聚合；代表失败即下钻 |
| 预算熔断误伤（巡检延迟） | 上限 30/min 远高于优化后的常态需求（P0 后估 <15/min）；request_failure 不受限 |
| 存量 30 万队列行 | 一次性清理脚本 + 保留策略并入 cleanup（另一个 PR，不在本方案门槛内） |

## 8. 测试计划（供测试直接转化）

### 8.1 单元测试（不改 DB）

| 用例 | 断言 |
|---|---|
| `TestNetworkChain_LongTailSink` | 网络类 attempt 0-3 依次 5s/15s/30s/60s（现行不变）；attempt 4+ 依次 5m/1h/2h/6h 封顶，不再永续 60s |
| `TestRequestFailure_MinGapSkip` | next_retry_at 未到且非 success 状态 → 本次 request_failure 触发跳过；间隔可配、0=现行行为 |
| `TestEnsureSystemAPIKey_SetsSystemTier` | 新建 key `key_tier='system'` |
| `TestSystemKeyTierSelfHeal` | 存量 default-tier 系统 key 被 UPDATE 为 system，幂等（二次执行 0 行） |
| `TestSelfcheckRateLimitAbort` | 主模型 429 → fallback 循环终止、不发起剩余候选请求；周期记忆生效 |
| `TestSkipOnBusinessEvidence` | 24h 内有成功业务行 → 深探跳过且 state healthy 证据续期 |
| `TestProviderAggregation_DrillDown` | 代表 transient 失败 → 同组其余凭据入队；structural 失败 → 不下钻 |
| `TestPeriodicBudgetCircuitBreak` | 超预算周期只出队 request_failure 行 |

### 8.2 集成测试（TEST_PG_URL 必须指向一次性库——铁律）

造三类 pair 跑模拟 tick（`time.Ticker` 加速 48h→分钟级）：
`structural-pair`（恒 404）/ `transient-pair`（前 10 次 503 后恢复）/
`healthy-pair`（持续业务成功行）：
- structural-pair（恒 404）48h 模拟窗探测次数 ≤ 8 次（复测**现行** 404 双确认 6h 档，作回归护栏）；
- transient-pair 前 4 次失败仍 5s..60s 发现节奏；持续失败沉入 5m/30m/2h/6h；
  第 10 次恢复后 ≤ 当前梯级档位时间内被发现（沉入 6h 后即为 ≤6h，§6 已明示该权衡）；
- healthy 全窗零主动深探。

### 8.3 验收 SQL（上线 48h 后执行，对照 §6 门槛）

```sql
-- ① 零信号弹回（门槛 ≤100）
SELECT count(*) FROM request_logs_hot
WHERE ts > now()-interval '48 hours'
  AND (api_key_owner_user LIKE 'self-check%' OR origin_stage IN ('self_check','node_probe'))
  AND failure_detail_code IN ('gw_rpm_exceeded','gw_key_throttled');

-- ② 自检 ping 量与成功率（门槛 ≤2000 且 ≥30%）
SELECT count(*) AS reqs, count(*) FILTER (WHERE success) AS ok
FROM request_logs_hot
WHERE ts > now()-interval '48 hours' AND origin_stage='self_check';

-- ③ runs 写入与成功率（门槛 ≤60000 且 ≥20%）
SELECT count(*) AS runs, count(*) FILTER (WHERE success) AS ok
FROM node_probe_runs WHERE started_at > now()-interval '48 hours';

-- ④ periodic 触发量（门槛 ≤20000）
SELECT count(*) FROM node_probe_runs
WHERE started_at > now()-interval '48 hours' AND trigger_kind='periodic';

-- ⑤ 短梯封顶档滞留（门槛：15s/30s/60s 三档合计 ≤5 对——沉底生效后
--    不应有成规模对滞留短梯；瞬态经过属正常）
SELECT next_retry_seconds, count(*) FROM node_probe_state
WHERE next_retry_seconds IN (15,30,60)
GROUP BY 1 ORDER BY 1;

-- ⑤b 60s 档滞留对的错误构成（应为空或仅瞬态经过；若再现
--    connection_error/503 群集说明长尾化未生效或回滚被触发）
SELECT last_err_code, count(*) FROM node_probe_state
WHERE next_retry_seconds = 60 GROUP BY 1 ORDER BY 2 DESC;

-- ⑥ 故障发现时间抽查（transient 类 5xx 从首次出现到 probe 恢复的时延）
--    与业务成功率对比：request_logs 业务行 success 率 48h vs 前 48h。
```

### 8.4 灰度与回滚

- 全部改动挂 `probe.*` settings 开关（§5 各参数），热加载；
- 灰度顺序：P0-1（纯收益）→ P0-3 → P0-2 → P1-1 → P1-2/P2；
- 回滚 = 对应开关置 0/false，行为回到现行代码路径（P0-1 的 tier 修复
  保留不回滚，它只影响系统密钥自身限额）。

## 9. 落地顺序

1. **P0-1 + P0-3**（一个 PR：自检 tier + 429 熔断，风暴源头止血）；
2. **P0-2**（一个 PR：短梯长尾化 + request_failure 频控 + 单测，无 DB 迁移）；
3. **P1-1**（业务证据联动）；
4. **P1-2 + P2**（聚合、预算、指标、采样）。

每步合并后观察 48h 验收 SQL，达标再进下一步；任一步回退不影响已完成
步骤的收益。
