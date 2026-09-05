# fix(telemetry): 245 minimax-m3 request_logs 数据保留 + 可观测性 (Step 5/6)

> 日期：2026-08-23
> 服务：`llm-gateway-go`
> 影响主机：gateway-245（minimax-m3 流量入口）
> PR 关联脚本：`scripts/diag/245-minimax-m3-requestlog-investigation.sh`

---

## 1. 背景

2026-08-23 上午收到反馈：245 主机上 `minimax-m3` 的请求到达网关后，客户端侧得到正常响应（HTTP 200 + 正常 streaming chunk），但查 `request_logs_hot` 表完全找不到对应记录。同一时段 `gpt-5.6-terra` 的请求入库一切正常，行数和 upstream 侧调用次数基本对得上。

现场最初怀疑方向有两个：

1. **DB 写入失败** — 是不是 `request_logs_hot` 这一块写入链路有故障，只影响了 minimax-m3 这条线？
2. **USRM 数据转换失败** — 用户提了一句 "是不是 ursm 那边转换挂了"，需要澄清。

后续调查确认 USRM（注意正确拼写是 `ursm`，即 Upstream Request/State Module，基于 Redis 的 per-`(tenant, credential_id, raw_model)` 节点健康度追踪，维护 `available` / `cool_until_ms` / `fail_streak` / 成功率等指标）根本 **不写 `request_logs`**：它只影响后续路由决策与统计计数器，跟 request log 落库是两套完全独立的链路，因此 USRM 一开始就可以排除嫌疑。

本次 PR 是排查后形成的两步修复（Step 5 / Step 6），目标：

- 让带脏字节的 minimax-m3 请求不再 "默默" 被 sanitize 成 NULL，而是要么 prefix-truncate 救回、要么显式丢弃并被 metrics 看见；
- 给 `EmitRequestLogUpdate` 入口加上 "RequestID 缺失" 的硬防线，避免脏数据导致孤儿行 UPSERT。

---

## 2. 根因分析

最终定位到 `domains/hooks/observability/telemetry/client.go` 里的 sanitize 链路，关键路径：

- `sanitizeRequestLogEntry`
  - `sanitizeJSONField`（解析字段）
  - `sanitizeRawJSONField`（兜底，prefix-truncate 后保留可解析前缀）

`minimax-m3` 在流式响应尾部偶发会带入非法 UTF-8 字节（典型表现：响应最后几 KB 的 SSE chunk 末尾多出 NUL 或随机控制字符）。当 sanitize 链路检测到这类字节时，Go 的 `json.Unmarshal` 直接报错，fallback 走 `sanitizeRawJSONField`：

- 如果脏字节集中在尾部、prefix 仍能解析成合法 JSON → 当前实现把 prefix 保留下来，写回 `request_body`，但事件标签叫 **`repaired`**（语义不准，更像 "rescued 救回"）。
- 如果 prefix 也救不回来（比如脏字节出现在中段） → 整个字段被替换为 `""`，DB 列 NULL。

**这条事故与 2026-06-11 的 glm-5.1 同类事故同源**，代码注释 `client.go:827-835` 区域里有那次事件的历史记录，处理模式也基本一致：当时也只是把脏字段降级为 NULL，并没有打 metric，所以再次发生时没有任何可观测信号。

另一个独立发现：`EmitRequestLogUpdate` 入队前没有校验 `RequestID` 是否为空字符串。当调用方传空 ID 时会走到一次 "全字段缺主键" 的 UPSERT，产生孤儿行（`request_id=""`），后续做关联分析时会被这些噪声污染。

---

## 3. 改动概述（按 commit 顺序）

### 3.1 `5c16cfb9f` — telemetry(observability): add sanitize_events Prometheus counter + required-field guard

在 telemetry client 里新增两个观测/防御机制：

- 新增 Prometheus counter `telemetry_sanitize_events_total{outcome, field, source, stage}`，对每一条 sanitize 决策都打点。outcome 取值：`discarded`（字段被 NULL 化）/ `rescued`（保留 truncated prefix 入库）。**合法的 passthrough 输入不打 metric**（有意为之，避免基数爆炸）。field 区分是哪个字段（`request_body` / `response_body` / `outbound_body` / `attachments` / `tool_calls` 等）；source 区分 sanitize 入口（`string_field` / `json_field` / `raw_json_field`）；stage 区分阶段（`sanitize` / `required_field_guard`）。
- 在 `EmitRequestLogUpdate` 入口加 required-field guard：检测 `RequestID == ""`，命中则 `slog.Error`、不入队、对应 counter 自增。

这一条把 "看不到" 的故障变成 "看得到" 的故障。

### 3.2 `42739c0ec` — telemetry(observability): rename sanitize outcome label repaired -> rescued

把 sanitize outcome 标签从 `repaired` 重命名为 `rescued`。原因：`repaired` 字面意思偏 "修复"，容易让人以为代码对损坏字段做了语义层面的修正；实际上只是 prefix-truncate 后保留可解析的前缀部分，更准确的描述是 "rescued"（抢救出来的部分可用片段）。

这一条纯粹是命名清理，影响 Prometheus 指标 label 的取值，需要同步更新依赖该 label 的查询（如果有 dashboard / alert 用了 `outcome="repaired"`，需要改成 `outcome="rescued"`）。

### 3.3 `a815b2544` — telemetry(observability): update sanitize test to rescued label + add Step 6 regression

测试侧配套：

- 把已有 sanitize 单元测试里期望的 outcome 标签从 `repaired` 改成 `rescued`，对齐 3.2 的命名变更。
- 新增 Step 6 回归用例：模拟 minimax-m3 实际事故场景（尾部含非法 UTF-8 字节的 SSE chunk），断言 sanitize 链路产出 `outcome=rescued` 且 `telemetry_sanitize_events_total` 计数器按预期自增，确保再发生同类事故时 CI 能拦下来。

### 3.4 `1b8911573` — fix(telemetry): align EmitRequestLogUpdate TenantID fallback with nonEmpty helper

统一 `EmitRequestLogUpdate` 里 TenantID 的 fallback 行为：改用统一的 `nonEmpty` 辅助函数判断，跟 codebase 其他地方的 "空字符串视为缺省" 语义保持一致。

之前版本里有一处 `TenantID == ""` 的判断是直接用 `== ""`，跟 `RequestID` 那条刚刚加上的 guard 风格不一致；这次顺手对齐，review 时也更容易扫读。

### 3.5 `3e30967b9` — scripts(diag): 245 minimax-m3 request_logs 调查脚本 — Step 6 版

新增 `scripts/diag/245-minimax-m3-requestlog-investigation.sh`：

- 一键在 245 主机上跑完整套排查：日志 grep（按 telemetry 关键字分组）+ PostgreSQL 对照（WAL vs logs_hot 行数差、各模型字段 NULL 占比、失败/断开错误分布、rescued 候选抽样）+ Prometheus 拉取 `telemetry_sanitize_events_total` 当前值；
- 输出当前窗口的指标分布、孤儿 `request_id=""` 行数、minimax-m3 vs gpt-5.6-terra 字段 NULL 占比比对；
- 既用于本次事故调查，也是后续同类事故的 first-responder 工具。

---

## 4. 行为对比表（before / after）

| 场景 | before | after |
|---|---|---|
| 合法 JSON 输入 | passthrough | passthrough（不变） |
| 含 invalid bytes 但能 truncate 救回 | 保留 prefix，metric 标签 `repaired`（语义混淆） | 保留 prefix，标签改为 `rescued`，counter `+1` |
| 含大量 invalid bytes 无法救回 | discard + 字段变 NULL，**无任何 metric** | discard + 字段变 NULL，counter `{outcome="discarded"}` `+1` |
| `EmitRequestLogUpdate` 入队时 `RequestID == ""` | 静默 UPSERT 孤儿行（`request_id=""`） | `slog.Error` 记录 + 不入队 + counter `+1`，杜绝孤儿行 |

---

## 5. 测试覆盖

升级前本地 / CI 验证结果：

- **单元测试**：6 个新 / 改测试用例全部通过，覆盖 sanitize 两个 outcome 分支（`discarded` / `rescued`）、`EmitRequestLogUpdate` required-field guard、TenantID fallback 对齐。**注：合法 passthrough 输入不打 metric，所以无对应测试名。**
  - `TestSanitizeJSONField_DiscardsAndIncrementsMetric` —— invalid UTF-8 触发 NULL，counter +1
  - `TestSanitizeRawJSONField_DiscardsAndIncrementsMetric` —— json.RawMessage 同上
  - `TestSanitizeJSONField_ValidInputDoesNotIncrement` —— 合法输入两个 outcome 都不增
  - `TestSanitizeJSONField_RescuedKeepsTruncatedPrefix` —— Step 6 关键回归：JSON prefix + 尾部 garbage + invalid bytes，断言保留 truncated prefix 且 counter `outcome="rescued"` +1
  - `TestSanitizeRawJSONField_RescuedKeepsTruncatedPrefix` —— json.RawMessage 同上
  - `TestEmitRequestLogUpdate_RejectsEmptyRequestID` —— guard 拦截空 RequestID，counter `stage="required_field_guard", field="request_id"` +1，不入队
- **诊断脚本**：`bash -n scripts/diag/245-minimax-m3-requestlog-investigation.sh` 通过；`shellcheck` 通过。
- **Prometheus rules**（如本 PR 同步改了 `deploy/prometheus/rules/telemetry-sanitize.yml`）：`promtool check rules` 通过。

---

## 6. 升级步骤

升级到 245 主机的可复制粘贴命令：

```bash
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
go build -o gateway ./cmd/gateway
scp gateway gateway-245:/usr/local/bin/
ssh gateway-245 'sudo systemctl restart llm-gateway'
```

升级后建议立刻跑一次诊断脚本确认 metric 开始上报（**注意：脚本不接受 `--window` 参数，窗口长度由环境变量 `LOOKBACK_HOURS` 控制，默认 1 小时**）：

```bash
scp scripts/diag/245-minimax-m3-requestlog-investigation.sh gateway-245:/tmp/
ssh gateway-245 'sudo LOOKBACK_HOURS=1 bash /tmp/245-minimax-m3-requestlog-investigation.sh'
```

---

## 7. 回滚方案

- **标准回滚**（推荐）：`git revert 5c16cfb9f^..3e30967b9`，生成一个 revert PR 走正常 review。
- **紧急回滚**（P0 故障场景）：在本地 main 上 `git reset --hard 5c16cfb9f^` 后强推到 origin 并触发重新构建部署。

不涉及 DB schema / migration，回滚只是代码层 revert，**不需要任何 DDL / DML 配套**。

---

## 8. 验收指标

升级完成后 30 分钟内观察以下信号（任一异常都需要回滚排查）。**重要前提：counter 的 label 集是 `{outcome, field, source, stage}`，不含 `model`**——按 model 维度的统计需要走 `request_logs_hot` SQL，Prometheus 只按 field / source / stage 暴露：

- **`telemetry_sanitize_events_total{outcome="discarded"}`** rate 应 **明显下降**（之前大量 "discarded" 的请求其实 prefix 是可解析的，升级后会落到 `rescued` 桶里）。
- **`telemetry_sanitize_events_total{outcome="rescued"}`** 出现稳定 rate（升级前为 0，因为 label 改名前是 `repaired`，新部署后才有值）。
- **`request_logs_hot` 表中 `request_body IS NULL AND model = 'minimax-m3'` 的占比下降**，与上面 metric 的位移趋势一致（按 model 分组必须走 SQL，counter 没有 model label）。
- **`telemetry_sanitize_events_total{stage="required_field_guard", outcome="discarded"}`** 计数稳定，无突增；若突增说明有上游调用方在传空 RequestID，需要单独 fix。
- **`request_logs_hot` 中 `request_id = ''` 的孤儿行数保持为 0**（升级前可能已经残留几条历史脏数据，可用 `DELETE FROM request_logs_hot WHERE request_id = '' AND ts < now() - interval '1 day'` 清掉；具体列名以实际 schema 为准）。
- 不再出现 "minimax-m3 调用次数 >> request_logs 落库行数" 的告警。

---

## 9. 相关文档

- 诊断脚本：`scripts/diag/245-minimax-m3-requestlog-investigation.sh`
- Prometheus 规则（如本 PR 同步新增 / 修改）：`deploy/prometheus/rules/telemetry-sanitize.yml`
- 旧 incident 复盘：2026-06-11 glm-5.1 同类事故，参见 `domains/hooks/observability/telemetry/client.go` 函数 `sanitizeRequestLogEntry` 附近的历史注释（搜索 `2026-06-11` 定位）。
- 设计文档（链路总览）：`docs/03-design/telemetry-observability.md`