# 2026-08-03 主线增量整改审计

## 范围与基线

- 审计范围：2026-07-27 至 2026-08-03 的 `origin/main` 主线增量，以及当前工作树中的会话压缩 WIP。
- 基线：本地审计开始时 `HEAD=193189c7`，`origin/main=a286d95b`；主线包含 Responses API IR、全协议 E2E fixture、Session V2 事务与 aggregate lifecycle、部署模拟和完整性监控等增量。
- 部署端口核对：`docker-compose.deploy-test.yml` 内部 Gateway 到 Redis 使用 `redis:6379`，宿主映射由 `${REDIS_PORT:-16379}` 提供；不存在把宿主端口误写进容器地址的问题。

## Standards 轴

- 保持现有 `settings.Global.EffectiveValue` 热加载模式，压缩开关、模式和窗口比例不再绕过 DB 直接读环境变量。
- 保持失败开放：摘要候选失败、摘要重建失败、消息 splice 失败均回退原请求或既有 delta 路径。
- `SessionWriterV2.Stop` 与 `Write` 的 aggregate 注册由同一互斥量保护，停止后不再注册新的异步任务；调用方 context deadline 会被返回。
- 未引入凭证、外部发布或生产部署变更。

## Spec 轴

- Compression mode：补齐 `delta_only`，`off`/`auto_threshold`/`on_4xx` 不再触发 session compressor 的 v4 proactive window；`compression.enabled=false` 保留 delta append。
- Summary protocol：OpenAI marker 修改原始 message map，保留 `tool_calls`、`name`、`refusal` 等字段；Anthropic delta 保留上一轮顶层 `system` summary。
- Summary routing：候选模型必须可用且 context window 不小于 800,000 tokens，避免摘要请求自身超出承载能力。
- Responses：标准 `function_call` 与 `function_call_output` 历史输入映射到 IR tool call/tool result；`input_file` 的 `file_data`、`file_id`、`filename`、`mime_type` 位于 item 顶层。

## 验证

已通过：

- `go test ./domains/hooks/compression/...`
- `go test ./internal/ir ./domains/session/v2`
- `go test -race ./domains/session/v2 ./domains/hooks/compression/...`
- `gofmt` 已应用于本次 Go 改动。
- `git diff --check` 无输出。

全仓 `go test ./...` 已执行，但未全绿；失败集中在现有数据库测试环境：

- `autoupdate/TestPgxStore_RecordUpdateReport`：测试数据库缺少被引用的 instance 外键行。
- `domains/providerprofile`：测试连接用户对 `provider_profile_metrics` 表无权限。

其余包在该次全仓运行中通过；上述失败未涉及本次修改的代码路径。

## 残余风险

- 本次未执行真实 PostgreSQL/Redis customer-instance 部署，部署脚本与容器网络只做静态核对。
- 摘要模型链的候选可用性依赖 Provider 实现，单元测试覆盖 HTTP/解析路径，未覆盖真实上游模型响应质量。
- 全仓测试若受本地 Docker、数据库或外部服务影响，应将基础设施失败与代码失败分开记录。

## 结论

本次整改关闭了 Responses 历史工具轮次丢语义、Responses `input_file` wire shape 错误、Anthropic summary delta 丢失和 Session V2 aggregate shutdown 注册竞态。完成最终分层验证后方可提交并推送。
