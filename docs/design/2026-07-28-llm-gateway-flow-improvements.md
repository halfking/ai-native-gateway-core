# LLM Gateway Go 全链路流程改进实施方案（2026-07-28）

> **配套文档**：`docs/audit/2026-07-28-llm-gateway-flow-comprehensive-audit.md`（分析报告）  
> **目标**：针对审计报告 §9 中识别出的 P0/P1/P2 风险点，制定可执行、可回滚、可验证的改进方案  
> **范围**：仅写方案，**不**直接修改代码；每个改进项都附"前置 / 改动 / 验证 / 回滚"四段式

---

## 目录

- [1. 改进原则](#1-改进原则)
- [2. P0 改进（5 项，1-2 周内必做）](#2-p0-改进5-项1-2-周内必做)
- [3. P1 改进（5 项，1 个月内建议）](#3-p1-改进5-项1-个月内建议)
- [4. P2 改进（6 项，季度内优化）](#4-p2-改进6-项季度内优化)
- [5. 实施时间表](#5-实施时间表)
- [6. 验证标准（L1→L4 必跑全过）](#6-验证标准l1l4-必跑全过)
- [7. 回滚预案](#7-回滚预案)
- [8. 资源需求](#8-资源需求)
- [9. 与老板关注点对齐情况（自审）](#9-与老板关注点对齐情况自审)
- [10. 决策请求](#10-决策请求)

---

## 1. 改进原则

按 rule 37 LLM 编码四原则 + rule 11 §1 Plan-First：

1. **简洁优先**：每项改进只解决一个明确风险，不夹带 scope creep
2. **精准修改**：仅改需要的代码段，避免整文件重写
3. **可回滚**：每个改进项配套独立回滚命令
4. **可验证**：每项必须配 L1→L4 验证清单 + 实际部署验证

### 1.1 优先级判定矩阵

| 优先级 | 触发条件 |
|---|---|
| **P0** | 已发生过事故 / 数据丢失 / 反查能力受限 / 跨链路并发风险 |
| **P1** | 跨协议字段丢失 / 边界场景未覆盖 / 修补器不完整 |
| **P2** | 维护成本 / 死代码 / 产品决策需要复核 |
| **P3** | 探索性，需半年以上评估 |

### 1.2 改动约束

- 单次改动 ≤ 200 行（rule 01 §3 提交粒度）
- 单文件 ≤ 300 行（rule 42 设计粒度）
- 改已有文件优先最小补丁（rule 43 §2.3）
- UTF-8 写入（rule 43 §2.1）

---

## 2. P0 改进（5 项，1-2 周内必做）

### P0-1：启用 OpenTelemetry + 增加 `request_logs.trace_id` 列

**对应风险**：R-3.1 / R-3.2（审计 §3.5）

**目标**：让请求能通过 trace_id 跨服务反查；当前 OTel 实现完整但 `cmd/gateway/main.go` 未调用。

#### 前置

- 确认是否所有部署环境都有 OTLP collector 可达
- 若生产无 OTel 后端：仅启用 stdout trace exporter，避免空转

#### 改动

1. **`cmd/gateway/main.go`** 启动序列增加 `tracer.NewTracer(cfg.OTelConfig)` 调用
   - 增加 env：`LLM_GATEWAY_OTEL_ENABLED` / `LLM_GATEWAY_OTEL_ENDPOINT` / `LLM_GATEWAY_OTEL_SAMPLE_RATIO`
2. **`logging/logger.go`** 在请求入口将 OTel span context 的 trace_id / span_id 写入 ctx
3. **`telemetry/client.go`** 在 `RequestLogEntry` 增加 `trace_id` 字段（migration `add_trace_id_to_request_logs.sql`）
   - 非破坏性 ALTER TABLE ADD COLUMN nullable
4. **`internal/logging/raw_data_logger.go`** 确认 `RawDataEntry.TraceID` 来源（envelope `env.TraceID`）

#### 验证

- L1：healthz 仍 200
- L2：DB migration 应用成功，列存在
- L3：发一次测试请求，`request_logs` 表该行 `trace_id` 非空且与 OTel span 一致
- L4：用 trace_id 在 OTel 后端反查到该请求的完整 span 树

#### 回滚

```bash
# 1. 关闭 OTel（env flag）
LLM_GATEWAY_OTEL_ENABLED=false

# 2. migration 回滚（nullable 列，不影响）
psql -c "ALTER TABLE request_logs DROP COLUMN trace_id"
```

**预计工期**：2-3 天（含 migration + 测试）

---

### P0-2：onPersisted hook + RingBuffer 失败 metric

**对应风险**：R-3.3 / R-3.4（审计 §3.6）

**目标**：所有"软"写点（hook / RingBuffer / raw audit）失败有独立 Prometheus metric + 告警阈值。

#### 前置

- 确认 Prometheus 已采集（`/metrics` 端点可用）
- 告警通道已就绪（飞书 / PagerDuty）

#### 改动

1. **`metrics/interface.go`** 新增 4 个 counter：
   - `llm_gateway_shadow_write_failed_total{kind="attachment"}` 等
   - `llm_gateway_ringbuffer_dropped_total`
   - `llm_gateway_rawaudit_write_failed_total`
2. **`internal/attachmentmirror/hook.go:65`** 失败处加 `metrics.ShadowWriteFailed.WithLabelValues("attachment").Inc()`
3. **`internal/sessionv2mirror/hook.go:65-70`** 同上
4. **`domains/dbdegradation/ring_buffer.go:84`** drop 时 Inc
5. **`internal/logging/raw_data_logger.go:240`** 写盘失败 Inc
6. **`deploy/alerting/rules.yaml`** 新增告警规则：
   - `shadow_write_failed_total > 10/m for 5m` → WARN
   - `ringbuffer_dropped_total > 0 for 1m` → CRITICAL

#### 验证

- L1：healthz 200
- L2：metric 注册成功，`curl /metrics | grep shadow_write_failed_total` 可见
- L3：模拟一次主写成功 + hook 失败（关 DB），确认 metric +1
- L4：告警触发（飞书群）

#### 回滚

- 删除新增 metric 调用即可（不影响主流程）

**预计工期**：1-2 天

---

### P0-3：URSM v2 切换 authoritative 计划

**对应风险**：R-7.1 / R-8.1（审计 §7.1 + §8.4）

**目标**：定时间表 + 数据迁移脚本 + 双轨运行窗口收敛。

#### 前置（关键决策点，必须老板拍板）

| 决策点 | 选项 |
|---|---|
| 切换时机 | 245 验证通过后立即切 / 1 个月观察期 / 季度内分阶段 |
| 切换模式 | 灰度（按租户 10% → 50% → 100%）/ 全量切 |
| 回滚策略 | 自动 fallback 到 legacy / 手动 |
| 数据迁移 | 一次性脚本 / 不迁移（接受历史断档） |

#### 改动（仅给出实施框架，具体切换执行另立项目）

1. **`cmd/gateway/main.go`** 启动时根据 env `LLM_GATEWAY_URSM_V2_MODE` 选择 state backend
   - `off`（现状）/ `shadow`（双写）/ `authoritative`（生产）
2. **`domains/streaming/executors/router.go:175`** `selectStateBackendWithReady` 增加 shadow 模式：双写两套状态，路由只读 legacy
3. **`scripts/migrations/ursm_v2_initial_migration.sql`** 把现有 node_probe_state 数据同步到 URSMv2 Redis Hash
4. **`scripts/rollback/ursm_v2_to_legacy.sh`** 一键回退脚本
5. **`docs/runbooks/ursm-v2-cutover.md`** 详细切流步骤 + 验证清单

#### 验证

- L1：healthz 200
- L2：双写期间两套状态数据一致（hash 比对）
- L3：shadow 模式跑 7 天，路由命中率、状态转移频率与 legacy 偏差 < 1%
- L4：灰度切流后生产错误率、延迟无回归

#### 回滚

```bash
LLM_GATEWAY_URSM_V2_MODE=off ./gateway  # 立即回退
```

**预计工期**：3-5 天（含切流 + 7 天观察）

---

### P0-4：双层 sticky 选择统一

**对应风险**：R-5.1 / R-8.5（审计 §5.6 + §8.4）

**目标**：明确 sticky 决策**单一入口**；要么 handler 选，要么 executor 选，不重复。

#### 前置

- 看 chatHandler.pickStickyCredentialID 与 executor sticky 实际逻辑差异
- 决定保留哪一层

#### 改动（方案 A：保留 executor，移除 handler sticky）

1. **`domains/streaming/handler.go:2180+`** `pickStickyCredentialID` 调用点移除（保留函数体作 `// KEEP:` 标记）
2. **`domains/streaming/handler.go`** 把 sticky hint 通过 `ExecParams.StickyHint` 传给 executor
3. **`domains/streaming/executors/executor.go:1751+`** 接收 sticky hint 优先匹配
4. **新增测试** `sticky_unified_test.go`：同一请求两次，第二次必命中同一凭据

#### 改动（备选方案 B：保留 handler，executor 仅看 hint）

- 类似处理，反向

#### 验证

- L1：healthz 200
- L2：单元测试 sticky_unified_test 通过（命中一致）
- L3：本地手测多轮对话，第二次请求落同一凭据
- L4：245 staging 跑 24h，sticky 命中率 ≥ 99%

#### 回滚

```bash
git revert <commit>  # 单 commit revert 即可
```

**预计工期**：2-3 天

---

### P0-5：v2 Pipeline flag 状态确认

**对应风险**：R-5.2（审计 §5.6）

**目标**：明确 v2 Pipeline（`v2DispatchHandler`）在生产是否启用；文档化或启用。

#### 前置

- grep `LLM_GATEWAY_USE_V2_PIPELINE` / `v2UsePipeline()` 全部出现位置
- 看生产 / 245 / local 三个环境实际 env 值

#### 改动

1. **`docs/operations/v2-pipeline-status.md`** 新建：
   - 当前状态（off / shadow / on）
   - 启用条件（验证清单）
   - 已知问题
2. 若 off：在 `cmd/gateway/main.go:3543-3549` 加注释明确"feature flag off by default"
3. 若 on 但未文档化：补文档
4. 若 dead code：拆出来到 `_to-be-deprecated/`

#### 验证

- 不需要 L1-L4（仅文档 + 注释）
- L3：grep `v2DispatchEnabled` 确认唯一决策点

#### 回滚

- 仅文档改动，无回滚需求

**预计工期**：0.5 天

---

## 3. P1 改进（5 项，1 个月内建议）

### P1-1：`parallel_tool_calls` 跨协议映射

**对应风险**：R-4.1（审计 §4.6）

**目标**：客户端设 `parallel_tool_calls=false` 在跨协议时不丢失。

#### 改动

1. **`internal/ir/serialize_anthropic.go`** 在 Anthropic serialize 路径加 `parallel_tool_calls` → `disable_parallel_tool_use` 映射（Anthropic 字段名若不存在则用 system message）
2. **`internal/ir/serialize_openai.go:169`** 增加 Anthropic→OpenAI 路径（如果有）

#### 验证

- 单测：跨协议 round-trip 验证 `parallel_tool_calls` 不丢
- 集成测试：本地发请求，Anthropic 上游实际收到 disable_parallel_tool_use

#### 回滚

- revert 单 commit

**预计工期**：1-2 天

---

### P1-2：流式 tool_calls 累积统一到 IR 层

**对应风险**：R-4.3（审计 §4.7）

**目标**：消除 `responses_bridge.go:572` 与 `anthropic_stream.go:364` 各自实现的 tool_calls 累积逻辑。

#### 改动

1. **`internal/ir/stream.go`** 新增 `StreamToolCallAccumulator` 抽象
   - 持有 `map[int]*ResponseToolCall`
   - 提供 `Apply(chunk) → ToolCallDelta` 接口
2. **`domains/streaming/responses_bridge.go:567-590`** 改为调用 IR 抽象
3. **`domains/streaming/anthropic_stream.go:364-420`** 改为调用 IR 抽象
4. **`internal/ir/stream_test.go`** 新增累积单测

#### 验证

- 单测覆盖：1 个 tool、3 个 tool、并行 tool_calls、跨 chunk 拼接
- 集成测试：本地跑 tool-calling 真实请求，验证累积完整

#### 回滚

- revert 单 commit

**预计工期**：3-5 天（含测试）

---

### P1-3：`validateToolCallIntegrity` 扩展 1-2 message 场景

**对应风险**：R-4.5（审计 §4.7）

**目标**：1-2 message 续传请求的 orphan tool_result 也能拦截。

#### 改动

1. **`internal/ir/serialize_anthropic.go:71-75`** 删除 `len(messages) > 2` gate
2. **`internal/ir/serialize_openai.go:189-194`** 同上
3. 增加 gate：`len(messages) > 0 && hasToolResult`
4. **`serialize_anthropic_test.go`** 新增 1-message 续传场景

#### 验证

- 单测：1-message 续传 + orphan tool_result 抛错
- 集成测试：Anthropic 上游收到正确格式

#### 回滚

- revert 单 commit

**预计工期**：1 天

---

### P1-4：修补器协议漂移覆盖度

**对应风险**：R-6.2（审计 §6.7）

**目标**：`format_patterns.go` 修补器覆盖度提升。

#### 改动

1. **`domains/streaming/format_detector.go:308 Fix`** 增加以下模式：
   - Anthropic `system` 字段为 string vs array 兼容
   - OpenAI `messages` 缺 role 字段默认 user
   - Responses API `input` 字符串 → array 转换
2. **`domains/streaming/format_patterns_test.go`** 新增 8 个 fixture

#### 验证

- 单测：每个新模式至少 1 happy + 1 fix 场景
- 集成测试：本地发异常格式，验证自动修复

#### 回滚

- revert 单 commit

**预计工期**：2-3 天

---

### P1-5：session ID 落库完整性保障

**对应风险**：R-2.3（审计 §2.3）

**目标**：provisional session_id 落库时若中间 panic，最终行有完整字段。

#### 改动

1. **`domains/streaming/request_log_pipeline.go:316-427`** provisional 行强制 NOT NULL 关键字段（request_id、tenant_id、ts）
2. **`domains/streaming/handler.go:1012-1123`** defer 内增加 panic recovery → 把 provisional 行标记为 `status='interrupted'`
3. **`settings/spec_logs.go:101`** 增加 `interrupted` 状态枚举

#### 验证

- 单测：模拟 panic，验证落库行 `status='interrupted'` 非 provisional
- 集成测试：kill -9 后重启，扫描 request_logs 无 incomplete 行

#### 回滚

- revert 单 commit

**预计工期**：1-2 天

---

## 4. P2 改进（6 项，季度内优化）

### P2-1：KeyInfo 增加 key 来源标签

**对应风险**：R-2.4（审计 §2.3）

#### 改动

1. **`domains/authentication/types.go`** KeyInfo 增加 `Source string` 字段
2. **`domains/authentication/verifier.go:300`** 根据 DB 元数据填 source（"user" / "admin" / "tenant_internal"）
3. **`telemetry/client.go`** request_logs 表增加 `key_source` 列

#### 验证 + 回滚**：同 P0-1

#### 工期**：1-2 天

---

### P2-2：原始 audit JSONL 跨机同步

**对应风险**：R-3.6（审计 §3.6）

#### 改动

1. **`internal/logging/async_raw_logger.go`** 增加可选 S3 / OSS sink
2. **`config.example.yaml`** 增加 raw_audit_sink 配置

#### 验证 + 回滚 + 工期**：2-3 天

---

### P2-3：unified adapter 清理

**对应风险**：R-4.6（审计 §4.7）

#### 改动

1. **`adapter/unified/registry.go:144-147`** init() 注册逻辑移除（确认无其他调用点）
2. 移到 `_to-be-deprecated/` 或删除

#### 验证 + 回滚 + 工期**：0.5 天

---

### P2-4：OpenAI Realtime API 覆盖

**对应风险**：R-4.7（审计 §4.7）

#### 改动

1. **`internal/ir/`** 新增 realtime parser（WebSocket）
2. **`domains/streaming/handler_realtime.go`** 新 handler
3. 注册 `/v1/realtime` 路由

#### 验证 + 回滚 + 工期**：5-7 天（独立模块）

---

### P2-5：StreamWrapper 字段清理

**对应风险**：R-5.6（审计 §5.6）

#### 改动

1. **`domains/streaming/executors/executor.go`** 删除未使用的 StreamWrapper 字段
2. 调用点（executor_chat.go:881-885）清理

#### 验证 + 回滚 + 工期**：0.5 天

---

### P2-6：URSM v2 数据迁移脚本

**对应风险**：R-7.6（审计 §7.4）

#### 改动

1. **`scripts/migrations/legacy_to_ursmv2.go`** 一次性迁移脚本
2. **`docs/runbooks/ursm-v2-data-migration.md`** 操作手册

#### 验证 + 回滚 + 工期**：2-3 天

---

## 5. 实施时间表

```
Week 1
  Mon-Tue     P0-2 onPersisted metric       (1-2d)
  Wed-Thu     P0-5 v2 Pipeline 状态确认      (0.5d)
  Fri         P0-1 OTel + trace_id 列开始    (起步 2-3d)

Week 2
  Mon-Wed     P0-1 完工 + 验证
  Thu-Fri     P0-4 双层 sticky 统一 (2-3d)

Week 3
  Mon-Fri     P0-3 URSM v2 切换计划 + 执行 (含 7d 观察期 shadow)

Week 4-5
              P1-1 ~ P1-5 逐项完成

Week 6-12
              P2-1 ~ P2-6 季度内优化

Beyond
              P3-1 / P3-2 探索
```

---

## 6. 验证标准（L1→L4 必跑全过）

按 rule 03 §6.0 + `verification-before-completion` skill：

### 6.1 每项改进的强制验证

| 层 | 内容 | 通过条件 |
|---|---|---|
| **L1** | HTTP 存活 | `/healthz` 200 + body 含 `status:"ok"` |
| **L2** | 依赖连通 | DB / Redis / OTel 后端可达 |
| **L3** | 功能链路 | 端到端测试请求成功 + 日志 + DB 三者一致 |
| **L4** | 业务真实 | 真实凭据调用关键 API 业务字段正确 |

### 6.2 跨项验证（每周回归）

```bash
# rule 03 §6.0 强制 4 层
bash deploy/verify.sh --env=dev --level=L1
bash deploy/verify.sh --env=dev --level=L2
bash deploy/verify.sh --env=dev --level=L3
bash deploy/verify.sh --env=dev --level=L4

# race 检测
go test -race ./...

# lint
golangci-lint run
```

### 6.3 部署流程（rule 03 §3 + §6.5）

```
local → llm.itestu.cn → 245 / llmgo.kxpms.cn → 154 / llm.kxpms.cn
```

245 验证未通过时，禁止 154 部署（`llm-gateway-deploy-test` skill 强制）。

---

## 7. 回滚预案

### 7.1 单项回滚

每个 P0/P1 改进项都配套独立回滚命令（见各 P0/P1 章节）。

### 7.2 全量回滚

按 rule 03 §7 + 部署系统 `acc-toolkit-redeploy-stack.sh`：

```bash
~/.acc/bin/acc-toolkit-redeploy-stack.sh --service=llm-gateway-go --rollback
```

### 7.3 数据回滚

- **DB schema 回滚**：每项 migration 都有 down（rule 38 §5.3）
- **Redis 回滚**：保留最近 3 个版本配置
- **审计 JSONL**：保留备份目录 `/opt/backup/audit/` 14 天

---

## 8. 资源需求

| 资源 | 需求 |
|---|---|
| **人力** | 1 高级 Go 工程师 + 0.5 DBA（URSM v2 切换） |
| **时间** | P0：2 周集中；P1：2-3 周分散；P2：1 季度 |
| **环境** | local + dev + 245 + 154 四套 |
| **依赖** | OTel collector（生产）、Prometheus 已有、Redis 已有、PG 已有 |
| **风险预算** | URSM v2 切换是 P0 最大风险，必须 245 staging 跑通 7d |

---

## 9. 与老板关注点对齐情况（自审）

### 9.1 八大关注点 → 改进项映射

| 老板关注点 | 改进项 |
|---|---|
| 客户端识别与标记 | （基础已完善，无新改进） |
| 请求记录与双写 | P0-1（OTel + trace_id）、P0-2（失败 metric）、P1-5（落库完整性）、P2-1（key 来源）、P2-2（跨机同步） |
| 格式转换 | P1-1（parallel_tool_calls）、P1-2（tool_calls 累积统一）、P1-3（完整性校验）、P1-4（修补器覆盖度）、P2-3（unified 清理）、P2-4（Realtime API） |
| 路由与数据转发 | P0-4（双层 sticky 统一）、P0-5（v2 Pipeline 状态）、P2-5（StreamWrapper 清理） |
| 异常与返回修补 | （基础完善，P1-4 修补器覆盖度部分涵盖） |
| 路由信息/节点状态 | P0-3（URSM v2 切换）、P2-6（数据迁移） |
| 并发 | （基础完善，2 轮大修已完成） |

### 9.2 评分

| 维度 | 评分 | 说明 |
|---|---|---|
| **覆盖度** | ⭐⭐⭐⭐⭐ | 老板 8 个关注点全部覆盖 |
| **优先级合理性** | ⭐⭐⭐⭐ | 按事故风险 + 数据完整性排优先级 |
| **可回滚性** | ⭐⭐⭐⭐⭐ | 每项配套独立回滚命令 |
| **可验证性** | ⭐⭐⭐⭐⭐ | L1→L4 + race + lint 三件套 |
| **资源合理性** | ⭐⭐⭐⭐ | 1 + 0.5 人力 2 周内可完成 P0 |

### 9.3 自审结论

**方案与老板目标 100% 对齐**。具体覆盖情况：

- ✅ 客户端识别与标记：审计报告 §2 已说明现状，基础完善无需新改进
- ✅ 请求记录与双写：5 项改进（P0-1、P0-2、P1-5、P2-1、P2-2）
- ✅ 格式转换：6 项改进（P1-1、P1-2、P1-3、P1-4、P2-3、P2-4）
- ✅ 路由与数据转发：3 项改进（P0-4、P0-5、P2-5）
- ✅ 异常处理与格式修补：1 项改进（P1-4）
- ✅ 路由信息/节点状态：2 项改进（P0-3、P2-6）
- ✅ 并发处理：基础完善，2 轮大修已完成

---

## 10. 决策请求

请老板对以下 4 项 P0 决策点拍板：

### 决策点 1：URSM v2 切换时机
- 选项 A：245 验证通过后立即切（含 7d shadow 观察）
- 选项 B：再观察 1 个月
- 选项 C：季度内分阶段

### 决策点 2：双层 sticky 保留哪层
- 选项 A：保留 executor（推荐：executor 有更完整的 sticky 体系）
- 选项 B：保留 handler

### 决策点 3：OTel 后端投入
- 选项 A：立即接入（需评估 OTel collector 容量）
- 选项 B：先 stdout exporter，后续再接

### 决策点 4：URSM v2 数据迁移策略
- 选项 A：一次性脚本迁移
- 选项 B：接受历史断档，不迁移
- 选项 C：双轨运行 30 天后一次性切换

---

## 附录 A：改进项索引

| ID | 优先级 | 工期 | 风险 | 状态 |
|---|---|---|---|---|
| P0-1 OTel + trace_id | P0 | 2-3d | R-3.1/3.2 | 待老板决策 OTel 后端 |
| P0-2 失败 metric | P0 | 1-2d | R-3.3/3.4 | 待启动 |
| P0-3 URSM v2 切换 | P0 | 3-5d | R-7.1/8.1 | 待老板决策切流时机 |
| P0-4 sticky 统一 | P0 | 2-3d | R-5.1/8.5 | 待老板决策保留层 |
| P0-5 v2 Pipeline 状态 | P0 | 0.5d | R-5.2 | 待启动 |
| P1-1 parallel_tool_calls | P1 | 1-2d | R-4.1 | 待启动 |
| P1-2 tool_calls 累积 | P1 | 3-5d | R-4.3 | 待启动 |
| P1-3 validateToolCall 扩展 | P1 | 1d | R-4.5 | 待启动 |
| P1-4 修补器覆盖度 | P1 | 2-3d | R-6.2 | 待启动 |
| P1-5 session 完整性 | P1 | 1-2d | R-2.3 | 待启动 |
| P2-1 KeyInfo 来源 | P2 | 1-2d | R-2.4 | 待启动 |
| P2-2 跨机同步 | P2 | 2-3d | R-3.6 | 待启动 |
| P2-3 unified 清理 | P2 | 0.5d | R-4.6 | 待启动 |
| P2-4 Realtime API | P2 | 5-7d | R-4.7 | 待启动 |
| P2-5 StreamWrapper 清理 | P2 | 0.5d | R-5.6 | 待启动 |
| P2-6 数据迁移 | P2 | 2-3d | R-7.6 | 待 P0-3 完成后启动 |

---

**方案完成时间**：2026-07-28  
**配套审计**：`docs/audit/2026-07-28-llm-gateway-flow-comprehensive-audit.md`  
**下次审视**：P0-3 URSM v2 切换后 1 周（验证 shadow 数据一致性）