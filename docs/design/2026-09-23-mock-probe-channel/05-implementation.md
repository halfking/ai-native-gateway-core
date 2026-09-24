# Mock Probe 通道——实施记录

> 实施日期：2026-09-24
> 前置文档：`02-current-code.md`（现状审计）、`03-optimization-plan.md`（方案 v2）
> 分支：`feature/mock-probe-channel`

## 一、落地清单（按方案 §四 步骤对照）

| 方案步骤 | 文件 | 状态 |
|---|---|---|
| Step 1 数据层 | `migrations/036_mock_probe_history.sql`（按天分区 + DEFAULT 兜底 + daily_partition 函数 + 双索引，全幂等） | ✅ llm_gateway_test 实跑两轮（含幂等重跑） |
| Step 2 配置层 | `config/config.go`：`MockProbeEnabled/MockProbeHideInAdmin/MockProbeInterval/MockProbeFailureThreshold`（yaml tag + env + merge + <1s 钳制 30s；导出 `MockProbeHideInAdmin()` helper） | ✅ `config/mock_probe_config_test.go` |
| Step 3 凭证旁路 | `internal/auth/mockprobe_bypass.go`：`IsMockProbeClient` / `EnforceMockProbeScope` / `MockEndpoint`（端点守卫）/ `MockProbeBypass`（API-key 门旁路） | ✅ `mockprobe_bypass_test.go` |
| Step 4 Mock 供应商 | `internal/providers/mock/{common,fast,slow}.go`：OpenAI Chat Completions + Anthropic Messages 双引擎，fast 0ms / slow 800ms±200ms，流式 SSE 按 chunk 写出，图片附件回显字节数，4MiB 硬上限 + 严格单 JSON | ✅ `fast_test.go` / `slow_test.go` |
| Step 5 Mock 客户端 | `internal/mockprobe/client.go`：`Client.Probe(ctx, supplier, stream)`，Bearer mock-probe-client，流式校验至 `[DONE]`，error_code 分类（timeout/transport/http_N/bad_body/stream_*） | ✅ `client_test.go` |
| Step 6 指标 + 历史 | `internal/observability/metrics.go`（scope 常量 + CounterVec + HistogramVec，promauto→DefaultRegisterer→/metrics 零改动暴露）；`internal/mockprobe/runner.go` 双写（先指标再异步历史，channel 256 深度非阻塞） | ✅ `metrics_test.go` / `runner_test.go` / `history_realdb_test.go` |
| Step 7 Admin 过滤 | `admin/providers.go`：listProviders WHERE 子句 `p.code NOT LIKE 'mock-%'` + 应用层 `mockProviderHidden` 双保险 | ✅ `providers_mock_filter_test.go` |
| Step 8 优雅停机 | runner 注册 `shutdown.Manager`（`KindNonStream`，id `mock-probe-runner`）；main 信号处理"先停 runner → 关 mux" | ✅ `TestRunnerShutdownManagerIntegration` + 活体 SIGTERM |
| 端点装配 | `cmd/gateway-v2/main.go`：4 端点（`/mock/v1/chat/completions/{fast,slow}` + `/mock/v1/messages/{fast,slow}`）仅 enabled 时注册；authChain 挂旁路；`startMockProbeRunner`（可选 pgxpool，DB 不可用降级只打指标） | ✅ `mock_probe_e2e_test.go` |

## 二、与方案的偏差（及理由）

1. **Histogram Name**：方案 §3.3 写 `Name: "mock_probe_latency_seconds_bucket"`——client_golang 会自动追加 `_bucket`，照抄会产生 `_bucket_bucket`。实现用 `mock_probe_latency_seconds`，暴露序列与方案 §一 图中一致。
2. **消息协议端点**：方案头注"本轮只支持 OpenAI Chat Completions"，但 §一 架构图与 §3.4 表都含 `/mock/v1/messages/{fast,slow}`。落地为：4 端点全部注册（满足"注册 4 个 mock 端点"），**探测矩阵只用 chat/completions**（协议锁定的精神保留——协议不是探测变量）。
3. **gateway-v2 无现成 pgxpool**（demo 入口，"v2 demo 无 DB"）：`startMockProbeRunner` 按需建 2 连接小池，`DATABASE_URL` 缺失/不可达时降级"只打指标"，不阻塞启动。
4. **阈值告警**：`MockProbeFailureThreshold` 达到即 `slog.Error` 升级（无外部通知通道依赖，与现有子系统告警风格一致）。
5. **PR4 未执行**：方案中的"删 python mock"动作**本轮未执行**——`scripts/mocks/llm-mock-upstream/*.py`（server.py / server-v2.py / server-v3.py / test_server.py）、`Dockerfile` / `Dockerfile.v2`、nginx 配置（`scripts/mocks/llm-mock.conf`）、`scripts/mocks/docker-compose.yml` 均仍在库，删除动作待 owner 确认后执行（v1 方案验收 `grep scripts/mocks` = 0 **未达成**）。理由：删文件超出本轮探测通道落地的爆炸半径，且外部 mock 仍被本地联调引用，贸然删除会破坏既有工作流。

## 三、验证记录（2026-09-24，本机）

### 单测 / race
```
go test ./internal/mockprobe/ ./internal/providers/mock/ ./internal/auth/ \
         ./internal/observability/ ./cmd/gateway-v2/ ./config/ -race -count=1
# 全 ok；admin 全量套件 ok
```

### 真库回归（llm_gateway_test，PG17/citus 本机容器）
- `psql -f migrations/036_mock_probe_history.sql` 两轮：CREATE×2/FUNC/索引 + 幂等重跑零报错；
- 分区：`mock_probe_history_20260924` + `mock_probe_history_default`，父表索引向分区传播；
- `TEST_DATABASE_URL=…llm_gateway_test go test -run TestHistoryStoreRealDB`：PASS（全列回环 + NULLIF 语义）。

### 活体验收（真实 gateway-v2 进程，interval=2s，API key 门开启）
- Bearer mock-probe-client 无 X-API-Key → `/mock/v1/chat/completions/fast` **200**（旁路穿全链路）；
- 错误 Bearer → **401**；mock Bearer 打 `/healthz` → **401**（门对非 mock 路径零放行）；
- `/metrics`（带 X-API-Key）：60 行 `scope="mock_probe"` 序列（4 通道 counter + histogram bucket）；
- `mock_probe_history` 落库 55 行/14 轮：fast 通道 0ms、slow 通道 634-993ms（[600,1000] 抖动带内），`all_ok=t`、无失败 streak；
- SIGTERM：日志 `shutting down` → 进程干净退出；
- 关闭模式（无 env）：mock 端点 **404**、无 `mock probe runner` 启动日志。

## 四、运维手册（速查）

| 动作 | 方法 |
|---|---|
| 开启 | `LLM_GATEWAY_MOCK_PROBE_ENABLED=true`（+ `LLM_GATEWAY_DATABASE_URL` 可选，控制历史落库） |
| 调间隔 | `LLM_GATEWAY_MOCK_PROBE_INTERVAL_SECONDS=30`（<1s 自动回落 30s） |
| Admin 放行 mock 供应商 | `LLM_GATEWAY_MOCK_PROBE_HIDE_IN_ADMIN=false` |
| 回滚 | 置 `MockProbeEnabled=false` 即停（端点 404 + runner 停 + 指标冻结）；删表/端点不影响真实供应商 |
| 生产建表 | 维护窗口对目标库手工执行 `migrations/036_mock_probe_history.sql`（与 035 同通道，不走 startup revision-sequence） |

**硬隔离（关闭态五不）**：客户端静默（runner 不启）、handler 卸载（端点 404）、history 不写、指标不打（CounterVec 无子序列零输出）、Admin 不显（默认 hide=true）。
