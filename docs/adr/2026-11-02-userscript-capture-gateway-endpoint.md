# ADR：Userscript Capture 网关对端实施设计

> 日期：2026-11-02 · 状态：PROPOSED（待评审）
> 归属：《会话治理融合优化方案-2026-11》P0-3 · 上游契约：ai-session-manager `docs/contracts/userscript-capture.md`（**CURRENT**，2026-11-02 订正——M1-M3 交付 + live E2E PASS 后契约已升 CURRENT；本 ADR 初编时为 PARTIAL）
> 证据基线：ASM `internal/userscript/{ingress,types}.go`、`internal/knowledge/worker/linker.go`、网关 `admin/body_resolver.go`、`internal/outbox/event_builder_v1.go`、`internal/outbox/wire.go`（2026-11-02 源码核实）

## 0. 问题

浏览器 userscript 捕获的对话正文没有生产入口：ASM 摄取端（`POST /v1/userscript/captures`）只收头元数据（`body_ref` 定位符），契约规定正文只发给 Gateway——但 Gateway 的用户态 capture API 为零雏形（`userscript` 在网关 Go 代码 0 命中）。后果：纯浏览器会话（不经网关 request 流）永远无法关联 canonical session（ASM current.md:70-71 已知边界），知识提取链路（linker → extraction）对该流量不可用。

## 1. 目标架构（数据流不变式）

```
userscript（浏览器，TS client 的 GatewayCaptureTransport）
  │ ① POST /v1/userscript/captures（HMAC + 身份；messages[] 带正文 content）
  ▼
Gateway capture API（本 ADR 新增）
  ├─ 校验/限流/幂等（idempotency_key）
  ├─ 存正文 → capture_bodies，签发 body_ref = internal://capture/{uuid}
  ├─ 同事务写 outbox 事件 userscript.captured.v1（含头元数据 + body_ref[]，不含正文）
  │    （复用现有 outbox_events 表 + dispatcher，ASM 投递通道同 request.completed）
  ▼
ASM /internal/v1/events（既有 HMAC batch ingress）
  └─ gatewayevents 新 case → 复用 internal/userscript repository 写
     userscript_capture_batches/messages → linker 照常盖章 canonical_session → extraction
```

不变式：**正文只落 Gateway**（ASM 仅持 body_ref）；**事件 at-least-once + event_id 幂等**（网关侧 `ON CONFLICT DO NOTHING`，ASM 侧 gateway_projection_events guard）；**capture 写入不阻塞数据面**（独立 API，不挂请求热路径）。

## 2. 决策

### D1 调用拓扑：userscript 单跳 Gateway，ASM 经 outbox 收（否决"两跳"）

契约明文："Userscript 只能调用 Gateway 的用户态 authenticated capture API……Gateway 负责……durable outbox"。两跳方案（userscript 再调 ASM 摄取端）要求浏览器持有 ASM HMAC secret，违反"ASM 摄取端服务化"边界。ASM 已实现的 `/v1/userscript/captures` HTTP ingress 保留为测试/工具入口，不再是生产主路径（D7 中 ASM 消费改为事件驱动）。

### D2 API 形状：path 对齐 ASM 参考实现，请求为其超集

- `POST /v1/userscript/captures`（与 ASM ingress path 完全一致，userscript TS client 的 `GatewayCaptureTransport` 无需改目标 path）。
- 请求 = ASM `capture-batch-v1.json` 的超集：`messages[].content`（正文，≤100000 bytes，ASM 版无此字段）**替换** `body_ref`（入站不带 ref，由本 API 签发）。其余字段（schema_version=1.0、source_system、client_session_key、correlation_id、idempotency_key、parent_message_id、sequence、content_revision、occurred_at）逐字段对齐。
- 批次上限 100 条消息、envelope ≤2 MiB（对齐 ASM `DefaultMaxBodyBytes`/`MaxBatchSize`）。
- 响应：`{ batch_id, accepted, duplicates, conflict_batch_id?, messages: [{source_message_id, body_ref}] }`；幂等重放（同 idempotency_key 同 payload）返回原 body_ref 集，不重复存储。
- 错误语义对齐契约行为要求：`409`（conflict 批次，不可重试）、`413/422`（超限/schema，不可重试）、`429`（限流，退避）、`503`（capability unavailable——脚本保留本地队列，不绕行）。
- **canonical session 响应（M3）**：`X-Gw-Session-Id` 存在或批次可归属时，响应附 `canonical_session`，闭合"纯浏览器会话无法关联"的契约已知边界。

### D3 认证：传输级 HMAC + 身份级 API key（Phase 2 换 capture token）

- 传输级：`X-Userscript-Signature/Timestamp/Nonce` 三头，HMAC-SHA256 签 `timestamp.nonce.raw_body`（与 ASM ingress 及网关 audit ingress 同款语义）；secret 为独立的 `LLM_GATEWAY_USERSCRIPT_CAPTURE_SECRET`（per-tenant 配置，不与任何现有 secret 复用——对齐 ASM `UserscriptCaptureSecretEnv` 的独立性纪律）。防篡改 + 防重放（nonce TTL 10min）。
- 身份级（用户授权，契约要求 Gateway 负责）：`Authorization: Bearer <api-key>`（网关数据面既有认证 → tenant）+ `X-End-User-Id`（既有 header 惯例 → end_user）。body_ref 的跨租户拒绝依赖 tenant 边界。
- **已知限制（显式记录）**：API key 常驻浏览器内存有泄露面。Phase 2 引入短期 capture token（HS256 JWT，scope `capture:write`，绑定 tenant+end_user，5min TTL + refresh；由 `POST /v1/capture/token` 用 API key 一次性换取）。Phase 1 受控灰度（可信用户组），Phase 2 全量。

### D4 body_ref：新命名空间 `internal://capture/{uuid}`

- 格式：`internal://capture/{capture_message_id}`（uuid v4）。与现有 `internal://body/{request_id}/{prompt|response}`（request 流正文）平行，前缀区分、互不解析混淆。
- 读取端：扩展 `admin/body_resolver.go` 的 `parseControlledBodyRef` 认 `internal://capture/` 前缀，从 `capture_bodies` 取正文；认证/租户校验沿用现有 service JWT（scope `session:read`）路径，跨租户 ref 由归属校验拒绝（ASM FetchBody 契约第 3 条不变）。
- ASM 侧零改动即可消费（它把 ref 当不透明路径参数）。

### D5 存储：新表族（迁移 931——原编 928，与远端 928_probe 撞号后重编，三表 + RLS）

```sql
-- 931_userscript_capture_family.sql（示意，落地时对齐 890 后的建表惯例：
-- tenant_id 首列 + RLS USING (tenant_id = current_setting('app.current_tenant', true)::TEXT)）
CREATE TABLE public.capture_batches (
    batch_id         UUID PRIMARY KEY,
    tenant_id        VARCHAR(255) NOT NULL,
    end_user_id      TEXT,
    source_system    TEXT NOT NULL,
    client_session_key TEXT NOT NULL,
    correlation_id   TEXT,
    schema_version   TEXT NOT NULL,
    message_count    INT NOT NULL,
    accepted_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    metadata         JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE TABLE public.capture_messages (
    capture_message_id UUID PRIMARY KEY,          -- body_ref 的 uuid 部分
    tenant_id        VARCHAR(255) NOT NULL,
    batch_id         UUID NOT NULL REFERENCES capture_batches ON DELETE CASCADE,
    source_conversation_id TEXT NOT NULL,
    source_message_id      TEXT NOT NULL,
    parent_message_id      TEXT,
    sequence_no      BIGINT NOT NULL,
    content_revision INT NOT NULL,
    role             TEXT NOT NULL,
    body_ref         TEXT NOT NULL,               -- internal://capture/{capture_message_id}
    idempotency_key  TEXT NOT NULL,
    occurred_at      TIMESTAMPTZ NOT NULL,
    captured_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    metadata         JSONB NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (tenant_id, idempotency_key),          -- 幂等重放收敛点
    UNIQUE (tenant_id, source_conversation_id, source_message_id, content_revision)
);
CREATE TABLE public.capture_bodies (
    body_ref         TEXT PRIMARY KEY,            -- internal://capture/{uuid}
    tenant_id        VARCHAR(255) NOT NULL,
    capture_message_id UUID NOT NULL REFERENCES capture_messages ON DELETE CASCADE,
    content          TEXT NOT NULL,               -- 敏感数据处理见 D5.1
    content_type     TEXT NOT NULL DEFAULT 'text/plain',
    sha256           TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

- D5.1 敏感数据处理：入库前跑网关现有脱敏/提示词注入检测规则（`domains/hooks` sanitize 族，配置开关 `capture.sanitize`，默认 on）；sha256 记录原文指纹供对账。Phase 1 不做静态加密，登记后续项。
- 保留策略：capture_bodies 挂 `BodiesCleanupWorker` 同款 1h 清理（新 retention 配置 `lifecycle.capture_bodies_ttl_days`，默认 90d，对齐 summaries 惯例）；头表长期保留（ASM 侧同款）。
- 与 `session_bodies` 的关系：**不合并**。session_bodies 是 turn 流增量正文（canonical 会话事实）；capture_bodies 是用户捕获快照（可能编辑/分支/纯浏览器），生命周期与语义不同。

### D6 outbox 事件：`userscript.captured.v1`

- 构造器 `BuildUserscriptCapturedEventV1(tenantID, batchID string, batch CaptureBatchHeader, messages []CapturedMessageHeader)`，与 `BuildSessionOpenedEventV1` 同 envelope 惯例（SchemaVersion 1、SourceSystem "gateway"、event_id = `evt-capture-` + sha256(tenant\x00batch_id)[:8] 确定性派生 → 重发幂等）。
- payload：批次头（source_system/client_session_key/correlation_id/end_user_id）+ `messages[]`（头元数据 + body_ref，**不含 content**）。ASM schema（gateway-event-schema-v1.json）加 `UserscriptCapturedPayload` + EventEnvelope type enum + oneOf——三处同步（本轮 session.closed.v1 已验证该 checklist：EventEnvelope enum×2 + payload def + oneOf 分支，漏一处即 422）。
- 写入时机：capture API 事务内（表写入 + outbox INSERT 同事务），失败整体回滚（这是用户 API，不是数据面旁路，不需要 fail-open——与 telemetry 派生链的区别）。

### D7 ASM 消费：gatewayevents 新 case，复用 userscript repository

- `applyContractEvent` 加 `case "userscript.captured.v1"` → 解 payload → 调 `internal/userscript` 的 repository 写 `userscript_capture_batches/messages`（与 HTTP ingress 同一落点，linker/extraction 链路零改动）。
- 缺表/冲突按既有 Retryable/Terminal 语义；`gateway_projection_events` guard 天然幂等。

## 3. 分期落地

| 里程碑 | 内容 | 验收 |
|---|---|---|
| M1 网关摄取 | D2 API + D3 认证 + D5 存储 + D4 ref 签发 | 单测 + realdb 测试：幂等重放/冲突/超限/跨租户拒绝/nonce 重放全负向 |
| M2 事件闭环 | D6 事件 + ASM D7 消费 | 跨仓 E2E：capture → outbox → ASM 投影 → linker 盖章 |
| M3 会话关联 | 响应携带 canonical_session + `handleControlledBody` 读 capture ref | 契约 userscript-capture.md PARTIAL→CURRENT；纯浏览器会话关联用例 |

## 4. 风险与回滚

- 表/事件为**新增**面，不动既有读写路径；回滚 = 下线路由 + 停 dispatcher 投递（outbox 天然可重放）。
- ASM 契约纪律：ASM 侧 schema/消费改动须与网关事件**同轮**上线（ASM default 分支对未知类型 Terminal → 直接发事件会 DLQ；与 session.closed.v1 同结论）。
- secret 管理：capture secret 独立配置，部署通道沿用 `deploy-seamless-docker.sh` 的 env 保留逻辑（dd_asm_event_env 同款模式）。

## 5. 验收映射（契约"验收"节 → 测试义务）

刷新/多标签页/重试/重启不重复 canonical message → D5 唯一约束 + D2 幂等响应测试；分支与编辑可还原 → parent_message_id + content_revision 落库测试；跨租户/伪造 parent/重放/超限 body/secret 扫描 → D3/D5 负向测试（secret 扫描对齐 ASM ingress 的字段黑名单惯例）。
