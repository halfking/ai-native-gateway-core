# ADR-0003：Step 7 — streaming 层 raw bytes 回退通道可行性分析

- **日期**：2026-08-23
- **作者**：llm-gateway-go backend architect
- **状态**：Analysis（可行性分析，未决策）
- **前置**：[Step 5 sanitize 保留 truncated prefix]、[Step 6 outcome=rescued + Prometheus counter + Required-field guard]

---

## 1. 背景与动机

### 1.1 Step 5 / 6 已落地的事实

我们已经在 `domains/hooks/observability/telemetry/client.go` 的 `sanitizeUTF8JSON` 路径上把"无法存成 JSONB"的两类语义分开了：

- `outcome=discarded` — `sanitizeUTF8JSON` 返回 ""，字段被 NULL 掉。`/request-logs` UI 看 NULL。
- `outcome=rescued` — 字段是一个截断的 JSON prefix（来自 `sanitizeUTF8JSON` 的修复路径），依然写入 JSONB，但尾部被丢掉。

Prometheus counter `telemetry_sanitize_events_total{outcome, field, source, stage}` 已经上线，可以观测到上游坏字节的命中率与字段分布。`EmitRequestLogUpdate` 还加了 `RequestID == ""` 必填守卫，避免 best-effort 写入空 request_id。

### 1.2 Step 5/6 仍未解决的问题

`rescued` 仍然有数据丢失，且**不是所有丢的部分都可忽略**：

1. **尾部 usage block**：OpenAI-style SSE 的 usage chunk 出现在 stream 末尾，truncate 在 JSON 最末一段合法对象之后，usage 行会被丢掉，账单/审计失去依据。
2. **tool_calls 数组最后一个 tool**：长 reasoning 模型在最后一个 chunk 闭合 `]` 的概率低，rescued prefix 通常 `}` 不闭合，数组被截掉，PostgreSQL 解析 JSONB 时即使保留 prefix 但尾部冗余 JSON 会失败，被迫降到 `discarded`。
3. **multipart 边界 / closing `}]` of attachments array**：附件 chunked 编码位于请求末尾时同样概率丢失。

也就是：**我们能观测坏字节频率**，但**修不回语义完整性**。

### 1.3 Step 7 命题

当 `telemetry_sanitize_events_total{outcome="rescued"}` 长时间高频（rate > 1/s）时，说明上游在持续发坏字节。`rescued` prefix 虽可用，但**关键尾部数据可能丢失**。Step 7 的提议：

- streaming 完成时把**完整原始字节**（包括 sanitize 会丢的部分）保留 N KB（默认 64KB）
- 编码后保存到 request_logs（base64 TEXT 或 file-based path）
- `/request-logs` UI 在 JSONB 字段 NULL 或异常时显示"完整原始字节已保存"并允许查看

本文评估该方案的可行性、代价、最小实施路径，以及是否值得在阈值触发前进入开发。

---

## 2. 候选方案

### 方案 A：raw bytes TEXT 列（base64 编码）

**改动面**：

- `request_logs_hot` ADD COLUMN `raw_request_body_b64 TEXT` / `raw_response_body_b64 TEXT`
- `request_logs_archive` 同结构（columnar 表，ADD COLUMN 代价中等）
- `request_logs_bodies`：若已存在分区，按相同模式增加分区列
- `domains/streaming/handler.go:emitTelemetry` 路径：在 `EmitRequestLogUpdate` 之前对 `OriginalRequestBody` / `OriginalResponseBody` 做 `base64.StdEncoding.EncodeToString` + `[:MaxBytes]`
- 新增配置 `TELEMETRY_RAW_BODY_MAX_BYTES`（默认 65536）
- 新增 Prometheus counter `telemetry_raw_body_truncated_total{field, source}` 用于观测截断率

**优点**：

- DB 不引入新类型，没有 schema 演化风险
- 查询简单：`SELECT raw_request_body_b64 FROM request_logs_hot WHERE request_id=$1`
- 与现有 JSONB 列紧耦合，永远同一个事务写入
- 不需要新基础设施

**缺点**：

- 存储膨胀：64 KB base64 ≈ 87 KB/字段，每行多 174 KB。10 万行/天 ≈ 17 GB/天 → 数据库 60 天 ≈ 1 TB
- `request_logs_hot` 是 hot table，heatcache / pg_class 会感知；bloat 风险
- 每行多 174 KB 让 `SELECT * FROM request_logs_hot` 退化为扫列式 IO，对 `/request-logs` 列表的 p99 退化
- base64 编解码在每个 streaming request 上加 CPU（虽然廉价，但 batched 高 QPS 下可观测）
- PII 留存：完整原始 prompt / response 副本绕过 JSONB sanitize，意味着 sanitize 路径不覆盖它

### 方案 B：file-based 附件系统复用（走 attachments StorageManager）

**改动面**：

- 把 streaming emit 阶段拿到的 `OriginalRequestBody` / `OriginalResponseBody` 直接通过 `domains/attachments.StorageManager.SaveAttachment(ctx, "<request_id>__req.bin", bodyBytes)` 写入对象存储
- `request_logs_hot` 仅新增 `raw_request_path TEXT` / `raw_response_path TEXT`（存储 key，不超过 256 字节）
- 新增端点 `/v1/admin/raw-body/:request_id?field=request`（鉴权）从 StorageManager 反向读取
- `/request-logs` UI 在 JSONB 异常时给一个 "查看原始字节" 链接

**优点**：

- DB 不爆，每行只多一段 256 字节 path
- 复用 `attachments.StorageBackend` 抽象，可平滑切换 local / OSS / Cloudreve backend（**注意：目前 backend 类型是 local / OSS / Cloudreve，没有 S3 backend**；如要 S3 需额外实现 `storage_backend_s3.go`）
- 复用 `CleanupOrphanedAttachments`（基于时间阈值），TTL 与 orphaned 清理一致
- 不重复实现文件 IO
- 与 attachments 语义对称：附件与 raw body 都是"对象存储 + DB 元数据"模型

**缺点**：

- 需要保证 attachments StorageBackend 在所有环境都已部署（local backend 必须能开）。已有 `storage_backend_local.go` 实现，问题不大
- 跨主机复制：local backend 部署为多副本时，单台写入需要走 S3 / OSS，或需引入共享 NFS，需要在 StorageManager 顶部用一个层级（OSS primary, local cache or fail back）
- 序列化：streaming 完成时再 SAVE 比 inline TEXT 更慢，多一次网络往返
- `request_logs_bodies` 不参与附件系统，监控面板需要另写查询

### 方案 C：sidecar 数据库（ClickHouse / S3 archival）

**改动面**：

- 新建 ClickHouse `telemetry.raw_body_local` 表，schema `(request_id String, ts DateTime, field LowCardinality(String), body String CODEC(ZSTD(3)))`
- streaming 完成时通过 Kafka / NATS 异步发到 ClickHouse consumer
- `request_logs_hot` 不变，UI 通过 federated query 拉

**优点**：

- 可扩展性最好，存储与 OLTP 分离
- zstd 压缩后 64KB 通常压缩到 8-15KB
- 列存擅长按 model 维度查询（事故复盘常用 grouping）

**缺点**：

- 多一套基础设施：ClickHouse 集群、Kafka topic、consumer 进程、schema registry
- 运维负担：备份、扩缩容、版本升级
- 跨服务一致性：ClickHouse 数据落后 Kafka 几秒，UI 上"已保存"和"可查"有时差
- 数据库 / 对象存储跨可用区的网络成本
- 与当前架构距离最远——`domains/attachments/` 已经有了等价的可插拔存储抽象

---

## 3. 优缺点对比表

| 维度 | 方案 A TEXT/Base64 | 方案 B 附件复用 | 方案 C ClickHouse/S3 |
| --- | --- | --- | --- |
| 实现复杂度 | 低，3-5 文件改动 | 中，5-8 文件 + StorageManager wiring | 高，多服务多进程 |
| 存储成本 | 高（每行 +174KB） | 低（每行 +256B path） | 中（压缩后 +10-20KB） |
| 查询性能 | 退化列表查询 p99 | 不影响 OLTP | 跨服务网络 |
| 运维负担 | 低 | 低（若 backend 已有） | 高 |
| 跨主机一致性 | 天然一致 | 取决于 backend 选择 | 异步最终一致 |
| 与 attachments 对称 | 无关但 schema 演化 | 完全对称 | 完全无关 |
| PII 留存 | 与 JSONB 同表，已在合规作用域 | 对象存储通常有独立 retention | 独立集群，需独立制定策略 |
| 回滚代价 | ALTER COLUMN DROP（不锁表耗时不可控） | 删除列 + 清理 attachments 上对应 key | 清整库 |
| 进入门槛 | 200 LOC | 600 LOC | 3000+ LOC，新服务 |

---

## 4. 推荐方案

**推荐方案 B（附件系统复用）+ 退化方案 A 的 fallback 路径**。理由：

1. **代码资产复用最大化**：`domains/attachments/StorageBackend` 接口已经能切换 local / OSS / Cloudreve（**目前没有 S3 backend**，如需 S3 需额外实现 `storage_backend_s3.go`），content-addressing + 去重对 raw body 同样适用——同一份 64KB 字节坏字节可能在多个 request 中复现，去重能极大降存储。
2. **存储成本可控**：`raw_request_path TEXT` 在 `request_logs_hot` 上是无主键小字段，对 p99 列表查询影响最小；实际字节在对象存储可独立 TTL。
3. **与 GDPR / PII 工作流打通**：attachments 的 retention / tombstone / cross-region replication 都已经成文，raw body 走同一条合规链路不需要重新讨论。
4. **回滚干净**：一旦 `raw_*_path` 列不需要，ALTER TABLE DROP COLUMN + 批量对象 Delete，几乎不留尾。
5. **触发式启用**：对象存储 backend 在 attachments 已经是 production 状态，启用 raw body 仅意味着 streaming 多一次 PUT，对延迟敏感路径（`handler.go:5732` 之前 fire-and-forget）影响低。

但 B 不是银弹，**两个前提**：

- 在 dev/test 环境默认以 local backend 跑通，且能容忍单实例存储（演示场景够用）
- production 环境的 attachments backend 必须是 OSS（或 Cloudreve），不依赖单台 pod 的 ephemeral disk

如果 production 仍以 local backend 为主，或 attachments backend 尚未稳定，再退化到方案 A。

---

## 5. 实施清单（推荐路径 B）

### 5.1 SQL migration

```sql
-- 084_raw_body_paths.up.sql
BEGIN;
ALTER TABLE public.request_logs_hot
    ADD COLUMN IF NOT EXISTS raw_request_path TEXT,
    ADD COLUMN IF NOT EXISTS raw_response_path TEXT;
ALTER TABLE public.request_logs_archive
    ADD COLUMN IF NOT EXISTS raw_request_path TEXT,
    ADD COLUMN IF NOT EXISTS raw_response_path TEXT;
-- request_logs_bodies 是按 request_id shard 的旁路表，本身不含 body 列；如需
-- 在 raw body 查回路径，亦加同样列；如不需，留 N/A。
COMMENT ON COLUMN public.request_logs_hot.raw_request_path IS
    'attachments StorageBackend key for the original request body bytes; populated
    only when the request body was truncated by sanitizeUTF8JSON (outcome=rescued).
    NULL means: no raw fallback captured. Decoded via GET /v1/admin/raw-body/{request_id}.';
COMMIT;
```

### 5.2 Go 代码

- `domains/streaming/handler.go`（emitTelemetry 段，约 line 5732 附近）：在调 `EmitRequestLogUpdate` 之前：
  - 若 `len(originalRequestBytes) > TELEMETRY_RAW_BODY_MAX_BYTES` 则 `incRawBodyTruncated("request", source)` 并截断
  - `path, _ := h.attachmentsManager.SaveAttachment(ctx, reqLog.RequestID + "__req", truncatedBytes)`
  - `reqLog.RawRequestPath = &path` 仅在 sanitize 路径 `rescued` 时设置
- 新增 `domains/telemetry/raw_body_emitter.go`：
  - `TelemetryRawBodyEmitter` struct：挂 `*attachments.StorageManager`、配置 `MaxBytes`、`SourceLabeler`
  - 方法 `AttachRequestBody(reqLog, raw []byte) error`、`AttachResponseBody(reqLog, raw []byte) error`
- 新增 `domains/hooks/observability/telemetry/raw_body_prometheus.go`：
  - `telemetry_raw_body_truncated_total{field, source}` counter
- 配置 `config.example.yaml`：`streaming.raw_body_max_bytes: 65536`、`streaming.raw_body_enabled: false`

### 5.3 前端 / API

- `/request-logs` 后端：当 `request_body IS NULL AND raw_request_path IS NOT NULL` 时附加字段 `_raw_body_hint: { available: true, bytes: <size>, sha256: <hash> }`
- 新端点 `/v1/admin/raw-body/:request_id?field=request|response`：
  - 鉴权：复用 admin token middleware
  - 校验：`reqLog.TenantID` 与调用者 tenant 一致，否则 403
  - 返回：`Content-Type: application/octet-stream`，附 `X-Raw-Body-Truncated: true/false`
  - 落地位置：`domains/admin/raw_body_handler.go`

### 5.4 测试

- 单测：`raw_body_emitter_test.go` 覆盖 max-bytes 截断、backend unavailable 退化（none 模式）、空字节短路
- 集成：hack 一个 minimax-style 错字节场景验证 path 写入 + GET 端点返回原字节
- 回归：`/request-logs` 列表查询 p99 在新列上不退化（线上 95 分位 ≥ 改前 ×1.05）

### 5.5 监控 / 仪表盘

- Grafana panel：`rate(telemetry_raw_body_truncated_total[5m])` 按 field 拆
- Alert：当 `rate(telemetry_sanitize_events_total{outcome="rescued"}[5m]) > 0.1` 持续 24h，把 raw body 启用建议写入值班日志

---

## 6. 风险评估

| 风险 | 概率 | 影响 | 缓解 |
| --- | --- | --- | --- |
| 存储爆炸（64KB × 10 万行/天） | 高（A 方案）/ 低（B 方案） | 数据库磁盘满 / 对象存储费用激增 | 默认配置 `raw_body_enabled: false`；按 model/tenant 黑名单开启 |
| base64 编解码 CPU 开销 | 中（A） / 低（B 不需要 base64） | QPS 高时偶发毛刺 | 仅在 `outcome=rescued` 时触发；<br>截断后再 base64（10KB 量级 CPU < 100µs） |
| GDPR / PII 留存期延长 | 中 | 合规审计需要附 retention 标签 | attachments 已有独立 retention，沿用同样策略 |
| 与 attachments 系统语义重叠 | 中 | "附件"和"raw body"在 UI 混淆 | 命名空间隔离 `__req` / `__resp` 后缀；UI 单独 tab |
| 跨主机一致性（local backend） | 中（B 方案风险点） | pod 重启后 raw body 失联 | 仅在 backend=OSS（或 Cloudreve）启用；local 模式仅 dev/test |
| 延迟增加（PUT OSS 多一次 IO） | 低（B） | streaming p99 涨 5-20ms | 走 fire-and-forget goroutine；不阻塞 EmitRequestLogUpdate |
| 误用：raw body 暴露给授权弱端点 | 中 | 数据泄漏 | `/v1/admin/raw-body/:id` 必须强制 tenant + admin 双因素，**不能挂在 `/request-logs` 主路径** |
| 列存储迁移（columnar 不允许 ALTER DROP COLUMN 短期高代价） | 低（A） | hot 退热延迟 | 优先走方案 B，不写列 |

---

## 7. 进入条件 / 触发标准

**Step 7 不应预防性开发**，应基于以下任一指标阈值观察：

### 7.1 Prometheus 阈值

- **`rate(telemetry_sanitize_events_total{outcome="rescued"}[5m]) > 0.1`** 持续 24h
  - 含义：5m 平均速率超过 0.1 events/s（即每 5 分钟约 30 次、每小时约 360 次），统计意义显著
  - 触发：开启 raw body 启用开关，灰度 model-level rollout

### 7.2 数据缺失阈值

- **`request_logs_hot WHERE client_model IN (top5_models) AND request_body IS NULL` 行数 / 该 model 总行数 > 1%**
  - 含义：JSONB 字段空值不可忽略，UI 实质退化
  - 触发：同 7.1

### 7.3 业务侧触发

- 支持或产品报告"用户对话中间段丢失"工单 ≥ 3 张 / 周
- 账单对账出现 `usage_tokens IS NULL` 但 `status_code=200` 的尾部比例显著上升

**任一触发，再进入 Step 7 development 流程**，且**默认方案 B（附件复用）**，**关闭默认开关**，**按 model + tenant 灰度**。

---

## 8. 不推荐的结论

**只有在触发条件出现时再做 Step 7**。在没有任何 rescued 高频信号、UI 未出现 NULL 退化、用户未报告数据丢失的当前状态下：

- 方案 A 的存储代价（17 GB/天）对当前 emergency 修复（Step 5/6）来说过度
- 方案 B 是合理的"未来选项"，但需要 attachements backend production 化才能落地
- 方案 C 在没有第二存储层基础设施的情况下性价比极低

**建议的更轻量替代（不进入 Step 7）**：

1. **临时手动介入**：当 Grafana 看到 rescued 高频告警时，临时拉一份 Redis / raw byte debug endpoint 拿 5-10 个样本分析即可，无须落库。Step 5/6 已经把 log/slog.Warn 改成 Prometheus counter，单次事件可定位到 (model, field, source, stage)，root cause 足以用抽样覆盖。
2. **增强现有 tool**：把 `/request-logs` UI 中 NULL 字段的 hint 提示从"原始字节已保存"改为"原始字节未保存"，让运维主动判断；同时为 `outcome=rescued` 的字段提供 JSONL parse 错误详情（显示 sanitize 阶段报错的偏移量），供审计员判断尾部到底是什么。
3. **Plan B 触发但延迟开发**：在 runbook 加一段：当 7.1/7.2 触发时，临时打开一段 `domains/streaming/handler_raw_body_dump.go` 旁路 dump 到 stdlib `io.Discard` 之外的 debug 目录，**只 dump `outcome=rescued` 的请求**，30 天后自动清理。

---

## 附录 A：参考文件路径

- 实施参考：`/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/domains/streaming/handler.go`（约 5700-5750 行，emitTelemetry 段）
- 复用入口：`/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/domains/attachments/storage_manager.go`
- 复用接口：`/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/domains/attachments/storage_backend.go`
- 复用实现：`/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/domains/attachments/storage_backend_local.go`
- Prometheus 已落地：`/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/domains/hooks/observability/telemetry/sanitize_prometheus.go`
- sanitize 实施：`/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/domains/hooks/observability/telemetry/client.go`（约 2600-2670 行）
- hot table 基线 schema：`/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/sql/quick-init-request-logs.sql`

---

## 附录 B：决策日志

- **2026-08-23**：完成可行性分析，状态 = Analysis。
- **下一步**：等待 7.1/7.2 指标在线上稳定后重审本 ADR，进入 Decision 状态选择方案 B 作为基线。
