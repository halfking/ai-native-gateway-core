# Handoff: 多厂商协议对齐深度审计后续

**日期**: 2026-08-28
**分支**: `main`
**最新提交**: `c8a44cf57`
**状态**: 本轮审计修复已推送；并发工作区改动已保留在 stash，未删除

## 1. 已完成

- 审计 `cmd/gateway`、`domains/streaming`、`domains/streaming/executors`、`internal/ir`、`domains/transformation/anthropic` 的请求/响应/流式闭环。
- 修复流式 MiniMax、Doubao、Zhipu/GLM、DeepSeek sanitizer 接线和 catalog trim/lower 规范化。
- 首帧、empty-stream gate starting/buffered frame、gate disabled 直写和主循环统一执行 vendor sanitizer。
- MiniMax `base_resp.status_code` 在 strip 前检测；空 catalog 仅按可解析顶层字段推断，避免错误信号被删除。
- 收紧非 SSE JSON 错误判定：孤立顶层 `message` 不再被误判为错误。
- Anthropic transformation 首字节读取使用可关闭 body 的 timeout helper；无 closer helper 不再等待不可中断 reader。
- MiniMax 未闭合 `<tool_call>` marker 不再截断后续合法内容。
- 增加 vendor、gate、错误 envelope、timeout、数据完整性回归测试。
- 同步更新架构文档、审计报告和本 handoff。

## 2. 验证

已通过：

```text
go test ./internal/ir ./domains/transformation ./domains/streaming ./domains/streaming/executors -count=1
go test -race ./domains/streaming ./domains/streaming/executors ./domains/transformation/anthropic -count=1
go vet ./domains/streaming ./domains/streaming/executors ./domains/transformation/anthropic
go build ./...
```

验证结论：协议回归、并发竞态、静态检查和全量构建均通过。

## 3. 代码完整性和并发工作区

- 本轮任务提交：`94963792c`、`37f990f59`。
- 远端并发提交先通过普通 merge 合并，未 force push；合并提交：`c8a44cf57`。
- 冲突文件 `admin/unified_detail.go` 保留 tenant scope 安全查询，并合入远端 session fallback 降级语义。
- 当前仍有并发工作区修改：`admin/auto_route.go` 及其他文件；它们没有进入本轮任务提交。
- 原未提交改动已保存至 `stash@{0}`，恢复时因远端已有同名文件而被 Git 安全阻止；stash 仍保留，禁止删除或覆盖。恢复前先核对文件差异。
- 未跟踪历史 handoff 文件也未纳入本轮提交。

## 4. 未验证边界

以下事项没有真实外部凭据或受控环境证据，必须标记为 `UNKNOWN`，不能宣称通过：

- 厂商 API key 实际可用性、余额和权限。
- MiniMax/Qwen/GLM/DeepSeek/Doubao/Ernie 真实 API 响应兼容性。
- 真实公网 TCP 长连接、代理、连接复用和上游断链演练。
- 真实 Redis/PostgreSQL 故障、锁竞争、连接池耗尽和 failover 演练。
- 生产环境 OOM/句柄上限和外部监控指标的实际值。

## 5. 剩余任务

1. 评估 legacy `domains/streaming/responses_stream.go` 是否下线或补齐 reasoning/tool/audio 数据。
2. 为 SSE 单行长度增加 reader 层上限，并定义超限时 fail-closed/failover 语义。
3. 统一四类 vendor strip/error 逻辑到 `internal/vendor/strip`，先做设计和迁移门禁，再分批迁移。
4. 单独规划 Doubao multimodal embedding 路由、能力注册和计费支持。
5. 在有授权和脱敏凭据的环境执行真实 provider/API/TCP/Redis/PG 演练。

## 6. 子代理提示词

### Legacy Responses 路径

```text
只读审计并随后设计 domains/streaming/responses_stream.go 的 legacy 路径处理：确认生产 wiring 是否存在；若存在，补齐 reasoning_content、tool_calls、audio delta 的 IR 转换和有界文本累积；若不存在，提出带 MIGRATION-GATE 的下线方案。不要修改当前 main，先输出证据、测试缺口和回滚策略。
```

### SSE 单行上限

```text
在 llm-gateway-go 中设计 SSE 单行最大长度保护：检查 stream reader、stripChunkFields、IR parser、pending capture 和 retry/failover 语义；保持现有 128 MiB 非流响应兼容策略，不把合法大响应静默截断。先新增集成测试和可配置阈值设计，再提出最小实现与文档更新，不读取或写入真实凭据。
```

### Vendor strip 重构

```text
在 llm-gateway-go 中设计 vendor strip/error registry 重构：先盘点 streaming 与 executors 的重复实现和所有 wiring，定义 Stripper/Detector 接口与迁移门禁；保留现有 MiniMax 错误优先检测、Zhipu/DeepSeek/Doubao 字段过滤和 Ernie 透传语义。先输出迁移计划与测试矩阵，不直接删除旧实现，不覆盖他人工作区改动。
```

## 7. 参考文档

- `docs/audit/2026-08-28-vendor-flow-deep-audit.md`
- `docs/03-design/01-architecture/architecture/ARCHITECTURE.md`
- `docs/03-design/01-architecture/architecture/runtime-request-flow.md`
- `docs/handoff/20260828-vendor-alignment/HANDOFF-FOLLOWUP.md`
