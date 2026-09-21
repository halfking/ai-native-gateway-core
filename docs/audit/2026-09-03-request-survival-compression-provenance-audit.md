# Request Survival / Compression Provenance 需求与审计整理

**审计日期：2026-09-03**
**目标分支：** `fix/gateway-provider-survival-20260901`
**合并目标：** `main`

## 1. 需求范围

本任务覆盖四条相互关联的能力线：

1. **Request survival**：已知模型在首轮无可用 provider 时，流式请求应由统一 retry owner 管理恢复、预算、取消和最终成功提交。
2. **Session compression**：会话缓存必须在请求间安全复用；压缩、机械裁剪和增量拼接不能使用错误 session、陈旧 body 或不一致 marker。
3. **Sanitization**：脱敏映射、offset、TTL 和 cache key 必须在 tenant/session 边界内保持一致，日志不得泄露上游原始错误或请求正文。
4. **Provenance / audit**：压缩前消息到压缩后消息的关系必须可审计，重复消息要能区分 occurrence，摘要折叠与机械丢弃不能混淆，且需兼容旧 Redis JSON。

## 2. 已实现并验证

- 前台 streaming survival 的 retry ownership、取消检查、预算与 SSE retry notice 已在主干历史中实现。
- RawCacheV2 深拷贝、sanitizer canonical tenant key/TTL、压缩 marker 校验、Anthropic summary system 语义、stale outbound lineage guard 已提交并通过专项 race/vet。
- compression runner telemetry 已进入 request-level metadata；Anthropic upstream error telemetry 已改为长度与 digest，不记录原始错误文本。
- `AlignmentMap` 已持久化到 SessionState；本次补充 occurrence、target kind 和 target space。

## 3. 本次审计发现及修正

### 3.1 Anthropic 顶层 system 摘要坐标错误

Anthropic Messages API 的 gateway summary 位于顶层 `system`，不存在 message-level summary index。旧 alignment 逻辑把这些历史消息默认标为 dropped，无法表达“折叠进 system summary”。

**修正：**

- 引入协议感知的 alignment builder；
- 检测真实 Anthropic system summary prefix；
- 将历史消息标记为 `TargetKind=summary`、`TargetSpace=top_level_system`；
- retained messages 仍使用 `TargetSpace=messages`。

### 3.2 非法 summary index

调用方提供的 summary index 可能因 body 重建或协议差异失效。若不校验，审计元数据会指向不存在的 after message。

**修正：**

- 解析 after message 数量；
- summary index 超出范围时归一化为无 message summary；
- 不生成越界 compressed index。

### 3.3 重复消息不可区分

仅使用 message hash 时，重复内容会在审计中产生歧义。

**修正：**

- 增加 `Occurrence`，记录同一 hash 在原始序列中的零基 occurrence；
- 保持 retained duplicate 按出现顺序一一映射。

## 4. AlignmentMap 字段契约

| 字段 | 语义 |
|---|---|
| `OriginalIndex` | 压缩前消息索引 |
| `CompressedIndex` | message-space 中的压缩后索引；system summary 使用 `-1` |
| `IsCompressed` | 是否未被原样保留 |
| `CompressedInto` | message-space summary target；无单一目标为 `-1` |
| `Hash` | 不含明文的消息指纹 |
| `Occurrence` | source sequence 中同 hash 的 occurrence |
| `TargetKind` | `retained` / `summary` / `dropped` |
| `TargetSpace` | `messages` / `top_level_system` / `none` |

新增字段使用 `omitempty`，旧 Redis alignment JSON 可继续反序列化，缺失字段保持零值兼容。

## 5. 明确未完成的后续项

本次审计没有把以下设计目标误标为已完成：

- durable worker count 尚未接入真正的并发 worker pool；
- durable task reclaim/restart 尚未携带 request-wide upstream budget；
- Responses durable worker 的 body/transformation/policy 恢复仍需专项审查；
- 跨 HTTP request 的 session-level single-success 尚未由数据库最终成功契约保证；
- `SessionSanitizeManager` 独立类型及完整压缩→脱敏持久化链路尚未实现；
- sanitizer 跨进程 offset 预占仍需 Redis 原子操作；
- 尚未进行生产流量、共享 Redis/数据库或真实 `glm-5.2` 无节点验证。

## 6. 发布审计结论

本次 feature 的有效差异集中在 AlignmentMap provenance 语义与测试。修正后，OpenAI message summary、Anthropic top-level system summary、mechanical drop 和 duplicate occurrence 均有明确可审计表示；主工作目录中其他人的部署、bg probe、Web、文档和 SQL WIP 不应被本次合并覆盖。
