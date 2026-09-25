# 探测体系成本优化方案（2026-09-25）

> 定位：可测试的优化方案。每项改动给出涉及文件、可调参数、预期收益与验收
> 门槛；§8 测试计划可直接转测试用例。数据基线 = 本地 llm_gateway
> 生产同构库 2026-09-25 取证（48h 窗口，见 §2）。
> 前置关联：probe-volume policy INV-1/INV-3（2026-09-20，node_probe 错误
> 证据准入 + 自检 3 天使用窗）、会话优化v2/32 系统监测设计、
> perf-2026-09-05-core-quality-baseline。
>
> 本文所有 SQL 均在本地库实测通过，可直接复跑作验收。

---

## 1. 现状盘点：五条探测管道

| 管道 | 触发/节奏 | 探测矩阵 | 单次动作 | 落库 |
|---|---|---|---|---|
| node_probe（`bg/node_probe.go`） | tick 30s 拾取到期行；request_failure 即时触发；退避梯 5s→30s→60s→5m→1h→2h→6h 封顶（attempt 7+ 恒 6h），broken_confirmed 30d | `node_probe_state` 638 对 (credential, model) | direct 轮（直连上游，max_tokens=10 ping）成功后才跑 gateway 轮（网关回环，带 Pin-Credential） | `node_probe_runs` 每 attempt 一行 |
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
| 探测矩阵 | 638 对（state）；失败对退避分布：338 对 @6h、60 对 @1h、60 对 @**60s**、42 @2h、18 @5min、14 @30s、6 @15s（**≤5min 梯级共 98 对**） | `node_probe_state` |

### 2.2 失败构成（零信号归因）

| direct 错误类别 | 48h 次数 | 性质 |
|---|---|---|
| `probe_direct_rate_limited`（上游 429） | **36,115** | 上游对我们限流——探测自身制造，重试即放大 |
| `probe_direct_network_error` | 19,386 | 瞬时类，适合退避重试 |
| `probe_direct_http_503` | 14,782 | 瞬时类 |
| `probe_direct_http_500` | 4,362 | 瞬时类 |
| `probe_direct_http_502` | 2,155 | 瞬时类 |
| `probe_direct_auth_failed`（401/403） | 872 | **结构性**——key 不换，重试无意义 |
| `probe_direct_http_400` / `404` / `410` | 1,570 | **结构性**——模型/参数不存在 |
| `probe_direct_endpoint_build` | 194 | **结构性**——网关侧配置坏 |
| `probe_direct_timeout` | 560 | 瞬时类 |

### 2.3 风暴直接诱因（已实锤）

自检 worker 自动生成的系统密钥 `key_tier` 落到列默认值 `'default'`
（`EnsureSystemAPIKey` 的 INSERT 未设置 tier），对应 `tierDefaults["default"]`
= **12 RPM**（`domains/authentication/verifier.go:74`）；而自检实际发射
~100 请求/分钟 → **88% 被自家网关 RPM 弹回**（35,440 条
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
| D2 | 失败不分类：结构性失败（auth/404/400/build）照走 60s/300s 短梯级 | §2.2 872+1,570+194 次、60 对卡 60s | P0-2 |
| D3 | 自检 fallback 风暴：主模型被 429 后仍逐个试完所有到期失败模型；429 弹回反喂失败矩阵 | §2.3，41,056 条 0 成功 | P0-3 |
| D4 | 上游 429 不触发探测侧退避（36k 次反复撞） | §2.2 | P0-2 |
| D5 | 有业务成功证据的对仍被主动深探（gateway 轮 / featured 深探不联动业务证据） | 业务成功行与探测矩阵重叠 | P1-1 |
| D6 | 每次直接探测成功都追加 gateway 回环轮（8.3 万/48h），对有业务流量的对增益近零 | `node_probe.go:1641` 无条件追加 | P1-2 |
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
- 参数：无需新增（沿用 tier 语义）。
- 预期：`gw_rpm_exceeded` 探测弹回 35,440/48h → **≈0**；自检周期从
  "被限流撕碎"恢复为"5min 周期 × 每凭据少量请求"。

### P0-2 失败分类驱动的退避下限（成本+准确度：砍结构性重试）

- 改动：`bg/node_probe.go` 写 `next_retry_seconds` 处引入错误分类映射
  （新文件 `bg/probe_retry_class.go`，纯函数可单测）：

  | 错误类 | 判定（direct_err_code） | 下限 next_retry_seconds |
  |---|---|---|
  | structural | `auth_failed`、`http_400/404/410`、`endpoint_build`、`invalid_protocol` | **21,600（6h）起步**，与 attempt 无关 |
  | upstream_throttled | `rate_limited`（上游 429） | **3,600（1h）起步** + 抖动 |
  | transient | network/timeout/5xx | 现行退避梯不变（5s→6h） |

  映射只抬高下限、不改既有梯级上限；config 变更事件（已有
  de-escalate 钩子 `deescalateGatewaySideProbeState`）立即拉回重探，
  不受下限惩罚。
- 参数（`settings/spec_probe.go` 新增）：`probe.retry_floor_structural_seconds`
  （默认 21600）、`probe.retry_floor_throttled_seconds`（默认 3600）。
- 预期：卡在 ≤5min 梯级的 98 对（§2.1 实测，贡献 periodic 19.1 万次的主体）全部
  沉入 ≥1h/≥6h 槽位；periodic 触发从 19.1 万/48h → **≤2 万/48h**。

### P0-3 自检 429 周期熔断（成本：终止自增强风暴）

- 改动：`bg/credential_selfcheck.go doRequest` 收到网关 429 /
  `gw_rpm_exceeded` / `key_throttled` 时，置周期级 `rateLimited` 标记，
  `runOne` 的 fallback 循环立即终止本凭据剩余候选（语义：网关层拒绝=
  本凭据本周期不可用，逐个再试只是放大）；周期间记忆（下一周期若上一
  周期熔断，只试主模型 1 次）。
- 参数：`probe.selfcheck.ratelimit_abort`（bool，默认 true）。
- 预期：自检网关请求 41,056/48h → **≤2,000/48h**（35 凭据 × 288 周期 ×
  ~1 请求的量级），且失败矩阵不再被 429 反喂膨胀。

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
  逐个下钻。结构性失败不下钻（P0-2 分类已给出）。
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
| 自检 ping 网关请求 | 41,056（0 成功） | **≤2,000（-95%）且成功率 ≥30%** | P0-3 |
| `node_probe_runs` 写入行数 | 232,393 | **≤60,000（-74%）** | P0-2+P2-3 |
| periodic 探测触发 | 191,205 | **≤20,000（-90%）** | P0-2 |
| runs 整体成功率（信噪比） | 0.63% | **≥20%** | 全部 |
| 真实故障发现时间（瞬时类） | ≤6h（梯级封顶） | **不劣化（≤6h）** | P0-2 只动下限 |
| 业务请求成功率 | 基线 | **不回退**（探测不再抢上游 RPM） | 侧面收益 |

## 7. 风险与权衡

| 风险 | 缓解 |
|---|---|
| 结构性失败长退避后，"换 key/修配置"的恢复依赖下一梯级才发现 | 复用既有 config-change de-escalate 钩子即时复探；手动探测端点兜底；下限 6h 而非 30d |
| 业务成功证据免探测的窗口错配（业务成功≠该凭据所有模型健康） | 证据按 (credential, model) 粒度判定，非凭据级；窗口 24h 可调、0 即关闭 |
| provider 级聚合掩盖单凭据劣化 | 仅聚合 periodic 巡检；request_failure 触发与 sync 探测不聚合；代表失败即下钻 |
| 预算熔断误伤（巡检延迟） | 上限 30/min 远高于优化后的常态需求（P0 后估 <15/min）；request_failure 不受限 |
| 存量 30 万队列行 | 一次性清理脚本 + 保留策略并入 cleanup（另一个 PR，不在本方案门槛内） |

## 8. 测试计划（供测试直接转化）

### 8.1 单元测试（不改 DB）

| 用例 | 断言 |
|---|---|
| `TestProbeRetryClass_Mapping` | `auth_failed`/`http_404`/`endpoint_build` → structural；`rate_limited` → upstream_throttled；`network_error`/`http_503` → transient |
| `TestProbeRetryFloor_OnlyRaises` | structural 对 attempt=1 也得 ≥21,600s；transient attempt=1 仍为 5s（现行梯不变） |
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
- structural 48h 模拟窗探测次数 ≤ 8 次（6h 下限）；
- transient 恢复后 ≤ 30 分钟内被发现（发现速度不劣化）；
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

-- ⑤ 退避分布（结构性失败对应 ≥21600s(6h)，throttled 对应 ≥3600s(1h)）
SELECT next_retry_seconds, count(*) FROM node_probe_state
GROUP BY 1 ORDER BY 2 DESC LIMIT 10;

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
2. **P0-2**（一个 PR：分类退避 + 迁移 + 单测）；
3. **P1-1**（业务证据联动）；
4. **P1-2 + P2**（聚合、预算、指标、采样）。

每步合并后观察 48h 验收 SQL，达标再进下一步；任一步回退不影响已完成
步骤的收益。
