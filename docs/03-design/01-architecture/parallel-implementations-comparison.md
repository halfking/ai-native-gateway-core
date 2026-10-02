# 新旧并行实现对比与统一收敛方案（重点标注版）

> **版本**: v1.0 · **事实快照**: 2026-10-01 · 基线 `3efae99`（main）
> **任务来源**: 2026-10-01 全码审计轮——"对比有无同功能的新旧两个版本的实现代码，需要重点标注，并做好统一优化的提示词方案"。
> **方法**: 子代理全仓扫描（import 关系 grep + git 历史 + wiring 追踪）+ 主代理对每组并行对的关键证据逐项复核（死包零引用计数、文件头自述、装配点行号）。模块名 `github.com/kaixuan/llm-gateway-go`。
> **配套**: 逐项可执行提示词见 [`unified-optimization-prompts.md`](./unified-optimization-prompts.md)；架构影响已同步进 [`architecture/ARCHITECTURE.md`](./architecture/ARCHITECTURE.md) §8。

---

## 状态分类图例

| 标记 | 含义 | 处置基调 |
|---|---|---|
| 🔴 **A 双活** | 新旧两版都在生产热路径按开关切换 | 最优先收敛：定默认、排退役时间表 |
| 🟠 **B 新版已接管** | 旧版仅遗留（被门控/仅测试引用/死代码） | 删旧：设保留期到期日，收集零引用证据后删除 |
| 🟡 **C 新版未接管** | 旧版仍是主用，新版为演示/灰度 | 决策：推进切流或冻结新版，避免三头维护 |
| ⚪ **死代码** | 全仓零外部 import | 直接删除（保留 git 历史） |

---

## 一、总览表（重点标注）

| # | 功能 | 旧版（主用/遗留） | 新版 | 状态 | 谁在生产 |
|---|---|---|---|---|---|
| P-01 | HTTP 入口 | `cmd/gateway`（v1，100+ 文件） | `cmd/gateway-v2` + 进程内 `/v2/*` 路由组 + v1 包装桥 | 🟡 C | v1（生产镜像只建 v1：`Dockerfile:42`） |
| P-02 | 探测/自检 worker | legacy 5 worker（`bg/self_check_worker.go` 等） | `bg/credential_selfcheck.go`+`node_probe.go`+`system_health.go` | 🟠 B | 新版默认（2026-07-14 起，`internal/probemode`） |
| P-03 | autoroute 决策/分类/评分 | `decision.go`(v1)+`classifier.go`(8类)+`scoring.go` | `decision_v2.go`+`classifier_v3.go`(10类)+`scoring_v2.go`/`scoring_simplified.go` | 🔴 A | **v1 默认**（`AUTO_ENABLE_V2=false`），v2 内部还会回退 v1 |
| P-04 | 会话存储 | V1 `request_logs`（`domains/session/` 122 文件） | V2 `sessions` 表族（`domains/session/v2/`，44 importer） | 🔴 A（迁移中） | V1 主写 / V2 影子写（`dual_writer.go`） |
| P-05 | outbox 事件构造器 | `event_builder.go` 的 `BuildRequestCompletedEvent`(V1)/`V2`（仅测试引用） | `event_builder_v1.go` 的 `V3`（GW-1.2 schema） | 🟠 B | V3（`domains/hooks/observability/telemetry/client.go:1957`） |
| P-06 | URSM Redis key schema | legacy node namespace（admin 等仍读） | `domains/ursm/v2/`（110 importer）+ k2 目标 schema | 🔴 A（迁移中） | `URSM_V2_KEY_SCHEMA_MODE=dual` 等四档 |
| P-07 | routing overrides 写路径 | `admin/routing_overrides.go`（delete/extend 仍在） | `control/routing/create.go`（CQRS，仅 create 迁出） | 🔴 A（迁移中） | 两包各管一半写命令 |
| P-08 | Outbox 模式 | `durable/pending_outbox.go`+`settlement_outbox.go` | `internal/outbox/`、`internal/sessionv2mirror/outbox.go` | 🔴 A | 4 套并行，语义各异 |
| P-09 | centeragent 上报路径 | 直连 DB（`agent_direct.go`，`OPS_COLLECT_DIRECT_DB=1`） | HTTP 上报 `OPS_COLLECT_URL`（默认） | 🟠 B | HTTP 默认 |
| P-10 | 死代码孤儿包 | `internal/probe`、`internal/orchestration`、`internal/capabilityscore`、`internal/ctxpool`、`internal/agent/wsclient`、`adapter/unified`、`examples/` | — | ⚪ | 无人（外部 import 计数=0，主代理复核） |

**误报澄清（曾经疑似、实为分工，勿当重复删）**：`api/` vs `internal/handlers`（审批回调 vs goalrun/quality handler，放置分裂问题见 ADR-0002）；`provider/` vs `internal/providers/mock`（真适配 vs 假上游）；`modeliqdata` vs `internal/quality` vs `domains/modelquality`（基准数据/画像管道/聚合视图）；`durable/` vs `pending/`（任务状态机 vs 断线响应缓存）；`center/` vs `internal/centeragent`（服务端 vs agent 端）。

---

## 二、逐项详证

### P-01 HTTP 入口两代 🟡 C（重点认知风险）

- **旧/主**：`cmd/gateway`（main.go ~7800 行；内含 `main_v2_pipeline.go` 挂 `/v2/*` 平行路由组，`LLM_GATEWAY_V2_ENABLED` 默认 OFF；`main_pipeline.go:161` `LLM_GATEWAY_USE_V2_PIPELINE` 把 4 个 v1 端点用 v2 Pipeline preflight/postflight 包装，OR 语义）。
- **新**：`cmd/gateway-v2`（4 个 go 文件），文件头自述"**并行的演示入口**……不替换/不修改 cmd/gateway/main.go（避免影响 71 生产部署路径）"，基于 `domain.PipelineRequest + pipeline.RequestPipeline`。
- **部署事实**：`Dockerfile:42` 与 `Dockerfile.build.r0924:7` 只构建 `./cmd/gateway`；gateway-v2 仅 `docker-compose.dev-research.yml:205`（`image: r112-gateway-v2:disabled`、`profiles:["v2"]`）与 scripts/local-* 引用；`deploy/`、`installer/`、`.github/`、Makefile 零引用。
- **三重 v2 暴露面**：独立二进制 + 进程内 `/v2/*` 路由组 + v1 端点包装桥——三者并存导致"哪条是 v2 真路径"认知成本最高。
- **命名陷阱**：`main_v3_wiring.go`=会话压缩、`main_v32_wiring.go`=SSE provider，**不是**管线 v3。

### P-02 探测/自检 worker 族 🟠 B

- **旧**：`bg/self_check_worker.go`、`bg/credential_probe_v2.go`（注意此名中 v2 属 legacy 代）、`bg/model_probe.go`、`bg/passive_probe_listener.go`、`bg/active_probe_worker.go`。
- **新**：`bg/credential_selfcheck.go` + `bg/node_probe.go` + `bg/system_health.go`。
- **证据**：开关唯一权威 `internal/probemode/probemode.go`（默认 true）；`cmd/gateway/main_helpers.go:288-306` `useNewProbeMode()` 注明 legacy 名单与回滚方式；双门控 `legacy_selfcheck_gate_test.go:8-14`（需 `LLM_GATEWAY_USE_NEW_PROBE_MODE=false` **且** `LLM_GATEWAY_ENABLE_LEGACY_SELFCHECK=true`）。
- **残留风险**：legacy 对象仍被构造并注入 admin（main.go:4032 `NewCredentialProbeV2`、:4134 `NewModelProbeRunner`、:5489 `SetModelProbeRunner`），`.Start()` 全部被门控；R36 审计修过 reviver/guard 读旧表的路径复活问题。
- **同族非重复**：`internal/reqprobe`（请求级探测学习，executor 在用）、`internal/mockprobe`（2×2 mock）、`internal/probeutil`、`credentialfpslot`、`credentialhealth` 均活跃单一职责。

### P-03 autoroute 三代 🔴 A（最高优先收敛点）

- **决策**：`autoroute/decision.go` vs `decision_v2.go`；`decision_v2.go:38-43` 遇非 `*Index` 类型回退 `d.Decide`（v1）。
- **分类**：`classifier.go`（8 类）vs `classifier_v3.go`（10 类，flag `auto_v3_enhanced_classification`）；`task_types_ext.go` vs `task_types_v3.go`。
- **评分**：`scoring.go` vs `scoring_v2.go` vs `scoring_simplified.go`；`scoring.go:161` 已自述"三代退役条件"。
- **开关**：`autoroute/feature_flags.go:341` `DecideWithFeatureFlags`——无任何 V2 子开关走 legacy；`:189` `AUTO_ENABLE_V2` 默认 **false**。生产调用点：`domains/streaming/auto_route.go:593`、`auto_route_nonchat.go:160,208`。
- **工具分叉**：`cmd/traffic-replay`/`tuning-backtest`/`auto-testbench` 各盯一代。
- **文档**：`autoroute/V2_IMPLEMENTATION_STATUS.md`。

### P-04 Session V1/V2 双写 🔴 A（全仓最大计划内双活）

- **V1**：`domains/session/`（122 文件，request_logs 事实源）。
- **V2**：`domains/session/v2/`（44 importer；表族 public.sessions/session_turns/session_bodies/session_turn_logs，430 迁移）。
- **桥**：`domains/session/dual_writer.go:10-22`——**V1 PRIMARY（失败即请求失败）/ V2 SHADOW（fail-open）**；接线 `cmd/gateway/session_v2_init.go`（flag `sessions_v2.enabled && sessions_v2.shadow_write`，热更）。
- **门禁**：`cmd/gateway/dual_read_validator.go`（storage-plan-v2 S2 行级对账，7 天零漂移才翻主读）。
- **回填**：`internal/sessionv2mirror/`（17 文件：backlog/replay/outbox）。

### P-05 outbox 事件构造器三代 🟠 B

- **旧**：`internal/outbox/event_builder.go:24` `BuildRequestCompletedEvent`、`:100` `BuildRequestCompletedEventV2`——现仅测试引用。
- **新**：`internal/outbox/event_builder_v1.go:75` `BuildRequestCompletedEventV3`（GW-1.2 schema，兼容 legacy 字段）——生产调用 `domains/hooks/observability/telemetry/client.go:1957`。
- **命名陷阱**：V3 构造器在名为 `_v1` 的文件里。

### P-06 URSM key schema 三代 🔴 A（迁移中）

- legacy node namespace（`admin/handler.go`、`credentialfpslot/node_state.go` 等仍读）vs `domains/ursm/v2/`（116 文件，含 migration/bootstrap/rollout）vs k2 目标。
- 开关 `domains/ursm/v2/config.go:269` `URSM_V2_KEY_SCHEMA_MODE=dual|canonical|k2`。
- 工具链：`cmd/migrate-ursm-v2`（生产可用）、`cmd/ursm-k2-preflight`（只读）、`cmd/k2-migrate-ursm`（**dry-run only，M5-0/T0 BLOCKED/NO-GO**，main.go:1-6 自述）。

### P-07 admin vs control CQRS 半迁移 🔴 A（迁移中）

- 同一资源（routing overrides）的写命令分居两包：create 在 `control/routing/create.go:1-13`（Task B1 PoC），delete（`admin/routing_overrides.go:207`）/extend（`:260`）留旧包；旧包 `:25` import 新包。

### P-08 Outbox 模式 4 套 🔴 A

- `durable/pending_outbox.go` + `durable/settlement_outbox.go`（任务存储自有，20 文件/11 importer）
- `internal/outbox/`（ASM 事件投递，17 文件）
- `internal/sessionv2mirror/outbox.go`（V2 镜像回填）
- 同一分布式模式四份落库/投递代码，幂等/重放/签名语义各自为政。

### P-09 centeragent 双路径 🟠 B

- 新默认 HTTP → `OPS_COLLECT_URL`；旧直连 DB `agent_direct.go`（仅 `OPS_COLLECT_DIRECT_DB=1`）。见 `internal/centeragent/agent.go:13-16`。

### P-10 死代码孤儿包 ⚪（主代理复核：外部 import=0）

| 包 | 文件 | 备注 |
|---|---|---|
| `internal/probe/` | trace.go | `admin/provider_probe.go:17` 留言"a future pass can move them to internal/probe" |
| `internal/orchestration/` | engine/loader/scheduler | 末次提交 2026-08-20 |
| `internal/capabilityscore/` | score.go | 符号仅自身引用 |
| `internal/ctxpool/` | ctxpool.go | 0 引用 |
| `internal/agent/wsclient/` | client.go | 0 引用 |
| `adapter/unified/` | anthropic/openai/registry | 2026-08-30 头注释已声明废弃 |
| `examples/` | — | 2026-08-17 后停更 |

---

## 三、风险 Top5 与收敛方向

| 排名 | 并行对 | 为什么最高风险 | 收敛方向 |
|---|---|---|---|
| 1 | **P-03 autoroute 三代** | 同热路径按 env 实时切换且 v1 默认；v2 内部回退 v1 造成组合爆炸、行为漂移难归因；三个 cmd 工具各盯一代 | 按 `scoring.go:161` 已写明的退役条件执行删减：先定 v2/v3 默认，再删 v1 死臂与重复 scoring |
| 2 | **P-04 Session V1/V2** | 长期双活导致两侧 schema 演进不同步；mirror 回填与实时写竞争；门禁（7 天零漂移）无期限悬置 | 明确 cut-over 期限或回退预案；门禁指标纳入监控；冻结 V1 侧 schema 变更 |
| 3 | **P-01 + P-10 入口与布局双轨** | 三重 v2 暴露面 + 7 个零引用孤儿包，新人无从判断真路径；死代码被误当活跃"修复"（IR 传输层 ADR 教训） | 先删死包（零风险），再裁决 gateway-v2 去留（冻结或推进），收单一 v2 暴露面 |
| 4 | **P-08 + P-05 outbox 四套+三代构造器** | 幂等/重放/审计口径不一致；死构造器占认知成本 | 抽公共 outbox 内核（幂等键+重放+签名），先删 event_builder.go 两个死构造器 |
| 5 | **P-02 legacy 探测仍装配** | legacy 对象构造+注入 admin，refactor 时旧路径易复活；R36 已修过一次复活事故 | 设 rollback 保留期到期日（建议 2026-10-31），到期删除 legacy 5 worker 与双 env 门 |

**治理原则**（同 ARCHITECTURE §11）：收敛走"影子验证→门禁→删旧→登记"；删除以零生产调用点 grep 证据为准；每组收敛独立可回滚并先补回归测试。

---

**最后更新**: 2026-10-01 · 生成于全码审计轮（子代理扫描 + 主代理逐项实证）
