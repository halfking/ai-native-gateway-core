# LLM Gateway 全方面测试 — 总览

> 入口索引。详细拆解见 §X-XX 子文档 + `tools/` + `scenarios/` 代码。

## 整体目录结构

```
docs/全方面测试/
├── README.md                       ← 本文件（入口 / 索引）
├── 00-总览.md                       ← 新增：执行入口 + 状态图
├── 01-测试架构设计.md              ← 架构图、数据流
├── 02-测试环境部署.md              ← 启动步骤（已用 tools/ 替换原步骤）
├── 03-测试场景定义.md              ← S1-S16 详细
├── 04-数据准备方案.md              ← DB schema + fixture
├── 05-执行流程.md                  ← 新增：单场景 + run_all.sh
├── 06-验收标准.md                  ← 通过标准（与 03 一致）
├── 07-故障类型矩阵.md              ← 新增：fault matrix × scenarios
├── 08-kill-switch.md               ← 新增：三模块旁路验证
├── 执行提示词.md                   ← 原文件
│
├── tools/                           ← 可执行测试台（核心）
│   ├── mock_supplier.py            ← 60 个 mock LLM 进程（A-L × 5）
│   ├── mock_orchestrator.py        ← group 状态编排（reset-all/set-group）
│   ├── start_suppliers.sh          ← 启/停 60 supplier
│   ├── loadtest.py                 ← N client × RPS × 持续
│   ├── validation_report.py        ← 结果聚合 → Markdown
│   └── legacy/                     ← 旧版 helloworld mock（备份）
│
├── data/
│   └── seed.sql                    ← 60 providers + 120 creds + 15 models
│
└── scenarios/                      ← 16 个可执行场景
    ├── _lib.sh
    ├── run_all.sh                  ← 一键全跑 16 场景 + 出报告
    ├── S01_baseline.sh             ← 基准性能
    ├── S02_cost_route.sh           ← 成本优化路由
    ├── S03_concurrency_diff.sh     ← 并发差异化
    ├── S04_quota_failover.sh       ← 配额耗尽
    ├── S05_quality_penalty.sh      ← 降权
    ├── S06_mixed_fault.sh          ← 混合故障
    ├── S07_peak_dispatch.sh        ← 高峰调度
    ├── S08_sticky.sh               ← Session 粘性
    ├── S09_streaming.sh            ← 流式 + broken_stream
    ├── S10_long_prompt.sh          ← 长 Prompt + context_too_long
    ├── S11_quota_recovery.sh       ← 周期配额窗口
    ├── S12_comprehensive.sh        ← 15 model × 150 client 全场景
    ├── S13_no_candidate.sh         ← 全部故障
    ├── S14_model_not_found.sh      ← 不存在的模型
    ├── S15_cross_group_failover.sh ← 跨组迁移
    └── S16_quick_recovery.sh       ← 配额快速恢复
```

## 三步开始

```bash
# 1. 准备（一次性）
env-injector inject aliyun-edge-252
PGPASSWORD=<pw> psql -h localhost -p 5432 -U xutaohuang -d llm_gateway -f docs/全方面测试/data/seed.sql

# 2. 启动 mock cluster（60 个进程）
docs/全方面测试/tools/start_suppliers.sh

# 3. 跑测试（90-120 min）
docs/全方面测试/scenarios/run_all.sh --gateway http://localhost:8781
```

## 故障类型矩阵（摘要）

| 故障 | Chaos 注入 | 覆盖场景 |
|---|---|---|
| 500 server_error | set-group X server_error | S06, S12, S13, S15 |
| 503 unavailable | set-group X server_error | S06, S12 |
| 429 quota | set-group-quota X 0 60 | S04, S11, S16 |
| 429 rate_limit | set-group K rate_limited | S06, S12 |
| 401/403 auth | set-group X auth | S13 |
| slow (2-4s) | set-group G slow | S05, S06, S07, S12 |
| flaky 30% | set-group J flaky | S05, S06, S12 |
| broken_stream | set-group G broken_stream | S09, S12 |
| context_too_long | set-group E context_too_long | S10, S12 |
| dropped (cut conn) | set-group X dropped | S13 |
| model_not_found | 请求不存在的 model | S14 |

详见 `07-故障类型矩阵.md`。

## 验收标准（摘要）

- 16 个场景中 S01-S12 / S15-S16：成功率 ≥ 99%，P99 ≤ 1500-2500ms（按场景）
- S13 / S14：预期失败（100% 错误率）
- Gateway 不 panic、不死循环、无 goroutine 泄漏
- 跑完总时间：约 90-120 分钟（默认）；`--fast` 模式可减半
- 总请求量：~13,000+

详见 `06-验收标准.md` 与 `tools/validation_report.py`。

## 模块旁路（新增 — 2026-07-12 incident response）

在 154 网关上通过环境变量 `KILL_*=1` 重启即可旁路这 3 个模块：

| 开关 | 影响模块 | 文件 |
|---|---|---|
| `KILL_SESSION_COMPRESSION=1` | 上下文压缩、LCS diff | `domains/hooks/compression/hook.go` |
| `KILL_SESSION_CACHE=1` | session_cache (L1+L2+L3) | `domains/hooks/compression/session_cache.go` |
| `KILL_CIRCUIT_DEGRADATION=1` | circuit breaker degrade | `domains/streaming/executors/executor.go` |
| `KILL_FP_SLOT=1` | fingerprint prefilter | 同上 |
| `KILL_RATE_LIMITER=1` | RPM/TPM token bucket | ratelimit middleware |

详见 `08-kill-switch.md`。

## 最近一次跑通的结果

参见 `results/REPORT.md`（`run_all.sh` 自动生成）。
