# 全面测试（Comprehensive Testing · 2026-09-23 之后唯一入口）

> **目的**：把 `tests/48h-audit/`（业务/数据/压力/安全 4 类域测试）和 `tests/stress/`（18 + 1 场景压测）的**所有提示词、方案、脚本**统一归档到本目录，使未来所有"全面测试"工作可在一处发起。
> **关系图**：
> - 本目录 = **规划层**（提示词、方案、脚本、报告）
> - `tests/48h-audit/`、`tests/stress/` = **执行层**（Go 源码、scenarios.json、mock 网关）
> - 执行层由规划层通过路径引用驱动，**不在执行层存放重复文档**。

---

## Mock 全面验证（核心契约）

> 全面测试要求在 **mock 环境**下覆盖网关协议、缓存、错误处理、敏感信息处理、性能、可观测性等全部面。mock 网关见 `tests/stress/gateway/`，mock 上游见 `tests/stress/mocks/`；本目录额外维护敏感信息 mock 套件（见 §敏感信息 mock 套件）。

### 范围（必须覆盖的 7 个面）

| 面 | mock 入口 | 必须覆盖项 |
|---|---|---|
| 协议兼容性 | `tests/stress/gateway` `/v1/chat/completions` | OpenAI / Anthropic / Responses 三协议 + 标准 + 流式 |
| 智能路由与粘性 | `tests/stress/gateway` `/admin/stats`、`/admin/health` | 单/多 5xx、恢复、权重漂移、降级 |
| 会话缓存 | `tests/stress/gateway`（Redis 由 miniredis 内嵌） | 语义命中、PII 命中率、TTL、跨轮次占位符 |
| 错误处理 | `tests/stress/mocks` 9 种 mode | 401/429/500/503/timeout/broken_stream/quota |
| 性能压测 | `tests/stress/scripts/scenario.go` | s1-s19 全部跑通（s17 已知缺口除外） |
| 敏感信息 | `security/sanitize/*_test.go` + D14 mock 套件 | 输入脱敏/输出还原/工具调用还原/未知 mask/伪造 mask |
| 可观测性 | `tests/stress/gateway` `/admin/memstats` + Prometheus | TPM、P99、goroutine、heap |

### mock 环境跑测前置

```bash
# 0. mock 依赖：miniredis（内嵌，无需外部 Redis） + kx-citus-pg17（数据库）
#    本地 llm-gateway-pg 镜像护栏见 docs/本地开发/llm-gateway-pg.md v1.15 §5 Q6
go build -o /tmp/stress-mock     ./tests/stress/mocks
go build -o /tmp/stress-gateway  ./tests/stress/gateway
go build -o /tmp/scenario        ./tests/stress/scripts/scenario.go

# 1. 启动 mock 网关 + 上游
bash docs/全面测试/stress/scripts/runner.sh restart

# 2. 三门
go build ./...                                    # 必须 0 error
go vet ./...                                      # 必须 0 warning
go test -race -short ./... -timeout 240s          # 必须全过

# 3. 业务/数据/压力/安全 17 域（含敏感信息 D14）
bash docs/全面测试/48h-audit/scripts/run-all.sh

# 4. 压测（含 s19 200c/150K TPM）
curl -sf http://127.0.0.1:18901/admin/memstats > /tmp/mem_before.json
/tmp/scenario \
  -gateway=http://127.0.0.1:18901 \
  -scenarios=docs/全面测试/stress/scenarios.json \
  -results=tests/stress/results/report.json
curl -sf http://127.0.0.1:18901/admin/memstats > /tmp/mem_after.json
bash docs/全面测试/stress/scripts/runner.sh stop

# 5. 聚合报告
bash tests/48h-audit/scripts/aggregate-reports.sh \
  > tests/48h-audit/reports/INDEX.md
```

---

## 敏感信息处理（占位符 → 真实值）契约

> **目标**：用户在请求中携带的敏感信息（手机号/身份证/邮箱/卡号/IP/密钥/姓名）**不出网关**——上游 LLM 与日志只见 `{SENSITIVE:type:index}` 占位符；客户端拿到的是**还原后的真实值**，工具调用（`tool_calls.arguments` / `tool_use.input`）也必须还原，否则下游插件会拿到占位符文本导致逻辑错误。

### 流程（先脱敏、后还原）

```
客户端 ──(POST /v1/chat/completions, 含真实 PII)
   │
   ├─→ SanitizeInputMiddleware     # 输入侧：PII → {SENSITIVE:type:index}
   │     映射表存 Redis (key=session:<sid>:sanitize, TTL=30min)
   │     占位符索引按 type 自增，跨轮次不撞号
   │
   ├─→ chatHandler → 上游 LLM
   │     上游只看到占位符文本（手机号是 {SENSITIVE:phone:1}）
   │
   ├─→ OutputComplianceInterceptor  # 安全检查（看到的仍是占位符）
   │
   └─→ SanitizeRestoreInterceptor   # 输出侧：占位符 → 真实值
         从 Redis 读映射表，做 RestoreOutputOrMask
         覆盖 response 内容字段 + tool_calls.arguments + tool_use.input
         ↓
客户端 拿到 还原后的真实值
```

### 协议覆盖（mock 测试必须全部钉死）

| 协议 | 还原字段 | mock 测试位置 |
|---|---|---|
| OpenAI chat | `choices[].message.content` | `security/sanitize/smart_sani_guard_test.go` TestRestoreResponseBody_* |
| OpenAI chat tool | `choices[].message.tool_calls[*].function.arguments`（JSON 字符串） | `security/sanitize/smart_sani_guard_test.go` TestRestoreResponseBody_ToolCallsArgs_* |
| OpenAI Responses | `output_text.delta` / 顶层 content | TestSanitizeRestoreInterceptor_StreamChunk_ResponsesDeltaRestore |
| Anthropic | `content_block_delta.delta.text` / `delta.input` / `delta.partial_json` | TestSanitizeRestoreInterceptor_StreamChunk_Anthropic* |
| 流式 OpenAI tool delta | `choices[].delta.tool_calls[*].function.arguments` | D14 mock `business/sanitize_tool_calls_stream_test.go` |

### 未知占位符 → mask（防 LLM 伪造）

- 映射表不存在的 `{SENSITIVE:type:99}` 文本：必须替换为 `[REDACTED]`，**不得**让 raw 占位符文本泄漏到客户端（防止 LLM 在响应里伪造占位符骗还原）。
- 校验点：每条响应被还原前先扫出所有占位符；若 sm 中找不到对应值 → `metrics.SanitizePlaceholderTamperingTotal{reason="llm_generated"}` 计数 + 日志 warn。

### 跨 chunk / 跨请求

- 跨 chunk（同一 SSE 事件被拆到两次 Write）：当前 `restoreStreamChunk` 按 chunk 独立解析；已知限制（`TestSanitizeRestoreInterceptor_StreamChunk_FrameSplitAcrossChunks` 钉住）—— 占位符如果跨 chunk 边界会被原样透传一次。修复路径是 `streaming.handler` 层加 SSE 帧缓冲。
- 跨请求（多轮会话）：Redis offset hash 记录每类已用最大编号，下一轮接着递增；sm 也在 Redis 中保留 TTL=30min，刷新靠每次响应还原 Expire。

### mock 测试新增要求（针对工具调用还原）

- 必须有「OpenAI chat 完成式 + tool_calls.arguments 还原」用例
- 必须有「OpenAI chat 流式 + tool_calls delta.arguments 还原」用例
- 必须有「Anthropic 完成式 + tool_use.input 还原」用例
- 必须有「未知占位符 → mask」用例覆盖 tool_calls.arguments 路径
- 必须有「跨轮次占位符不撞号」用例覆盖 tool_calls.arguments 路径

---

## 目录结构

```
docs/全面测试/
├── README.md                          # 本文件：总入口 + 验收门 + 敏感信息契约
├── launch-prompt.md                   # 完整测试启动提示词（含 4 变体）
├── plan.md                            # 主代理 v2 提示词（48h 审计 + 4 类测试整合）
├── master-report.md                   # R56 总览：当前状态、下一步
├── template-domain.md                 # 域目录 + 测试文件 + 报告三套模板
│
├── 48h-audit/                         # 17 域测试整合中心
│   ├── README.md                      # 索引 + 与 docs/audit/playbook/ 关系图
│   ├── D01-ir-lifecycle/              # 17 个域之一
│   │   ├── plan.md                    # 审计要点 + 验收门
│   │   └── reports/latest.md          # 本轮 48h 审计结论
│   ├── D02-protocol-adaptation/
│   ├── ... D03-D17 ...
│   ├── scripts/
│   │   ├── run-all.sh                 # 一键跑指定域的全部测试
│   │   ├── aggregate-reports.sh       # 聚合各域 latest.md → reports/INDEX.md
│   │   └── new-domain.sh              # 按模板新建一个域目录
│   └── reports/
│       └── INDEX.md                   # 跨域聚合索引（自动生成）
│
└── stress/                            # 压测（含 200 并发 + 150K TPM 门禁）
    ├── scenarios.json                 # 19 个场景定义（s1–s18 原有 + s19 新增 200c/150K TPM）
    └── scripts/
        ├── runner.sh                  # harness 生命周期：start/stop/restart/status
        ├── scenario.go.txt            # 场景驱动源码（含 TPM 聚合）· .txt 后缀避免被 Go build 误编
        └── capacity_matrix.sh         # 容量矩阵（参考）
```

---

## 快速开始

```bash
# 0. 三门预检
go build ./...                                    # 必须 0 error
go vet ./...                                      # 必须 0 warning
go test -race -short ./... -timeout 240s          # 必须全过

# 1. 业务/数据/压力/安全 17 域
bash docs/全面测试/48h-audit/scripts/run-all.sh

# 2. 压测（含 s19 200c/150K TPM）
go build -o /tmp/stress-mock     ./tests/stress/mocks
go build -o /tmp/stress-gateway  ./tests/stress/gateway
# 注意：源码真实位置在 tests/stress/scripts/scenario.go
# docs/全面测试/stress/scripts/scenario.go.txt 是文档快照（.txt 后缀避免被 Go build 误编）
go build -o /tmp/scenario        ./tests/stress/scripts/scenario.go
bash docs/全面测试/stress/scripts/runner.sh restart
curl -sf http://127.0.0.1:18901/admin/memstats > /tmp/mem_before.json
/tmp/scenario \
  -gateway=http://127.0.0.1:18901 \
  -scenarios=docs/全面测试/stress/scenarios.json \
  -results=tests/stress/results/report.json
curl -sf http://127.0.0.1:18901/admin/memstats > /tmp/mem_after.json
bash docs/全面测试/stress/scripts/runner.sh stop

# 3. 聚合报告
bash tests/48h-audit/scripts/aggregate-reports.sh \
  > tests/48h-audit/reports/INDEX.md
```

---

## 验收门（PASS/FAIL 判定）

### 三门（每次跑必须全过）

| 门 | 命令 | 必须结果 |
|---|---|---|
| 编译 | `go build ./...` | 0 error |
| 静态检查 | `go vet ./...` | 0 warning（vendor 例外） |
| 单元测试 | `go test -race -short ./... -timeout 240s` | 全 PASS |

### 业务/数据/压力/安全 17 域

- 所有非占位域必须至少 1 个 PASS
- 占位域（plan.md 占位 + reports/latest.md "待留档"）允许 no .go files
- 报告路径必须在 `tests/48h-audit/reports/history/` 或 `tests/stress/results/` 下（R61 清扫勘误：原写 `docs/全面测试/48h-audit/reports/` 与 `docs/全面测试/stress/results/` 目录从未存在，执行层 run-all.sh 恒写 `tests/48h-audit`）
- **D14 安全域必须含敏感信息 4 类测试（业务/数据/压力/安全）**，并钉住占位符 → 真实值还原

### 压测（19 场景）

- s17 已知缺口（REPORT.md §3.1 已披露，本机测试网关 + 生产 cmd/gateway 行为差异）
- s18 必须 PASS（5500 @ c50 长突发稳定性）
- **s19 必须 PASS**（200 并发 + 150K TPM 门禁）
- 内存稳定性：跑前 vs 跑后 goroutine 增长 < 20（绝对 < 50）；heap 不单调

---

## 19 个压测场景概览

| ID | 名称 | 关键指标 | 备注 |
|---|---|---|---|
| s1 | baseline | 200/c20 success ≥99% | 三家健康 |
| s2 | single 5xx | 200/c20 success ≥90% | failover |
| s3 | consecutive 5xx | 100/c10 success ≥90% | 降级 |
| s4 | provider slowdown | 80/c16 ≥ 99% | 权重漂移 |
| s5 | quota exhausted | 120/c20 ≥ 90% | 429 |
| s6 | all 5xx | 40/c8 100% 503 | 全故障 |
| s7 | recovery | 140/c14 ≥ 99% | 回流 |
| s8 | burst | 2000/c50 ≥ 99% | 突发 |
| s9 | sustained | 200/c25 ≥ 99% | 短突发 |
| s10 | stream | 100/c10 success ≥99% | SSE |
| s11 | stream broken | 80/c12 success ≥99% | 截断流 |
| s12 | flaky | 200/c20 ≥ 85% | 抖动 |
| s13 | long prompt | 200/c20 ≥ 99% | ≥20k 字符 |
| s14 | 3×503+1 healthy | 80/c12 ≥ 90% | 灾备 |
| s15 | dynamic weighting | 600/c30 ≥ 99% | 三家均分 |
| s16 | timeout | 40/c8 ≥ 80% | 25s 上限 |
| s17 | committed-eof all broken | 80/c12 success 100% + envelope | **已知缺口** |
| s18 | sustained stability | 5500/c50 ≥ 99% | 大压力 |
| **s19** | **200c + 150K TPM** | **12000/c200 ≥ 99% + tpm ≥ 150000** | **T-series 门禁** |

---

## 历史轮次留档

每轮按 `T<N>-<YYYY-MM-DD>` 命名归档到：
- 域级：`docs/全面测试/48h-audit/DXX-*/reports/history/`
- 轮级：`docs/全面测试/history/`（T1 轮已建成）

---

## 跨会话接力模板

```text
承接 T<N-1>：
  - 起点 = docs/全面测试/history/T<N-1>-<日期>.md §遗留
  - 知识库入口 = docs/全面测试/README.md + docs/全面测试/launch-prompt.md
  - 主计划 = docs/全面测试/plan.md
  - 当前轮提示词 = docs/全面测试/launch-prompt.md
其余按 docs/全面测试/plan.md 流程跑。
```

---

**版本**: v2 · 2026-09-23 22:18 CST · T2 入口（新增 mock 全面验证 + 敏感信息占位符→真实值契约）
**配套**:
- `launch-prompt.md`（执行提示词）
- `plan.md`（主代理 v2 整合提示词）
- `master-report.md`（R56 总览）
- `48h-audit/`（17 域测试整合中心）
- `stress/`（19 场景压测）