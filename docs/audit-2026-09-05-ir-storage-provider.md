# 2026-09-05 4小时修正行审计与主分支同步报告

## 结论摘要

- 已从 `origin/main` 刷新并快进本地 `main`：`f27e412b9 -> cbf6de062`，无合并冲突。
- 保留了同步前工作区已有的版本文件和清理脚本，未覆盖、未纳入本轮提交。
- 本轮修复并验证了 4 项局部缺陷：
  1. IR 响应传输为 Gemini 客户端补齐原生 `gemini-generate` 序列化分支；
  2. 流式 XML tool-call 片段超过 64 KiB 时清理下游内容并输出低基数脱敏告警，避免静默丢弃；
  3. `AsyncFileWriter` 改用同目录唯一临时文件，消除并发写同一路径时固定 `.tmp` 互相覆盖；
  4. `MemoryStateStore.Close` 等待 janitor 协程退出，避免关闭后后台协程残留。
- 定向测试全部通过；全仓测试结果以本报告末尾实际命令输出为准。

## 同步与工作区保护

远端刷新：`git fetch origin --prune`。远端主分支在同步时前进 2 个提交：
- `d0da61b9c docs+test: 二轮审计修正（A8）——字段计数守卫固化、文档勘误、注释补全`
- `cbf6de062 fix(deploy-local): 容器模式挂载 attachments/logs/raw-logs/backups 状态目录`

同步方式：先保存含未跟踪文件的 stash，再执行 `git merge --ff-only origin/main`，然后恢复 stash。恢复后无冲突标记，原有工作区文件仍在。

## IR、流程闭环与协议适配

### 已确认闭环

- `internal/ir` 负责统一请求/响应 IR；`domains/transformation/ir_transport.go` 承担 client protocol → IR → upstream protocol 及响应反向转换。
- 已覆盖 OpenAI Chat/Responses、Anthropic Messages、Gemini generateContent 的主要解析/序列化路径；streaming 另有 SSE 适配层。
- 请求与响应转换会记录 request id、协议、body size 和原始上游响应（由 raw logger 控制）；敏感头部采用 allowlist。
- 会话/轮次、路由候选、工具调用、思考/压缩字段在多个结构中分层传递，但并非所有字段在每个协议都能无损表达，需依靠扩展字段或降级规则。

### 本轮修复

`domains/transformation/ir_transport.go` 在响应序列化中补充 `gemini-generate`/`gemini` 分支，调用已有 `ir.SerializeGeminiResponse`。新增 `TestIRTransport_ConvertResponse_GeminiClient` 验证 Gemini 原生 `candidates`/text 形状不再落入 unsupported protocol。

### 未闭环项

- 需要建立 client×upstream×stream/non-stream×text/tool/thinking/multimodal/unknown-extension 的完整 golden 矩阵。
- `durable_recovery_worker.go` 仍调用无历史输入的 `AggregateTaskOutcome`。直接替换为空 `DecisionHistory` 会制造“伪迁移”，因为 worker 当前没有从 durable snapshot/task 读取决策历史；应先扩展持久化契约，再迁移到 `AggregateTaskOutcomeWithHistory`。
- 结构化消息的标题/摘要投影和原始轮次“去格式可读文本”仍需统一字段契约及跨存储往返测试。

## 存储、附件、媒体与版本管理

### 已确认闭环

- full 模式使用 PostgreSQL/Redis 与 hot/分区路径；lite 模式使用 SQLite、本地文件和内存状态。
- session turn/body 与 request log 有 hot 写入及后台 promote/partition 设计；附件/原始 body 使用文件目录并有部署目录挂载修复。
- 更新/删除历史 columnar 数据的约束已在文档和部分实现中体现，生产级 PG/Redis 运行态仍需集成验证。

### 本轮修复

- `storage/file/async_writer.go` 的 atomic write 改用 `os.CreateTemp` 创建同目录唯一临时文件，写入、chmod、close、rename 均检查错误并在失败时清理；并发写同一路径不会因固定 `<path>.tmp` 互相覆盖。
- `storage/memory/state_store.go` 增加 `doneCh`，janitor 退出时关闭，`Close` 在发出停止信号后等待退出，形成生命周期闭环。

### 未闭环项

- lite 模式 request body 与 SQLite reference、session metadata/body 的跨资源原子性和重启恢复仍缺少完整集成测试/outbox 补偿。
- hot 仅保留 8 小时、批量迁移、历史分区不可变、repair 工具不得修改 columnar 的约束需在真实 PostgreSQL 环境执行验证；本轮未凭猜测修改迁移器。
- 附件远程后端初始化失败的 fail-closed、temp 文件 healthcheck 的 close/delete 错误仍需补强。

## 供应商错误、fallback 与可观测性

### 已确认能力

- streaming 已有候选错误分类、commit gate 和 outage fallback 边界；已避免 committed output 后透明重试。
- fallback 对连接拒绝/reset、不可达、EOF、Redis unavailable 等可降级错误与认证/ACL/context/protocol 错误的 fail-closed 方向基本一致。

### 本轮修复

- `domains/streaming/tool_call_xml.go`：超过 64 KiB 的疑似 XML tool-call 片段现在记录不含正文的 `stream_xml_tool_call_fragment_overflow` warning，清空 fragment 并删除 delta content，避免客户收到不完整工具调用或静默丢失。
- 新增边界测试验证 fragment 被清理、正文不泄漏、下游得到有界空 delta。

### 未闭环项

- `docs/prompts/task3-error-display-optimization.md` 设计的 `supplier_errors_hot`/stats 事实源在当前 Go 生产写入链路中未被证明存在；dashboard 实际查询 `session_module_executions_hot` 等聚合，供应商/凭据/模型/HTTP code/retryable 维度不完整。不能把方案文档当作实现证据。
- 候选错误详情缺少统一的 provider name、credential label、status/code/retryable/latency/stage；前端 routing tab 仍偏 raw JSON 展示。
- Anthropic SSE reader 的底层 `Close` 不响应时仍需可取消 transport 与 goroutine leak 测试。
- 真实 TCP reset/refused/DNS/TLS/EOF/slow-read、密钥撤销传播、PG/Redis 重启和备用供应商 think/非中断流缺少运行态故障注入证据。

## 安全、并发与可靠性

- 头部日志采用安全 allowlist；现有审计显示日志/快照避免落密钥，但仍应增加自动扫描覆盖错误消息、raw tab、URL query token。
- 本轮修复减少了文件写入竞态和内存 janitor 生命周期泄漏风险。
- 不应把全局锁或固定重试用于遮掩跨介质一致性问题；需要按 request/session/credential 维度保留幂等键、lease/fencing 和补偿状态。
- 调度、队列、TCP 会话和异步 worker 仍需 race、取消、panic、shutdown drain 的混合压力验证。

## 可观测性与前端一致性

- routing source、shadow queue、survival gate 已有部分 metrics/logs；错误路径 request_id 仍非全量强制。
- 菜单配置和控件复用结构较好，但远程菜单固定 `tenant_id=default` 与 schema/权限裁剪风险需后续修复。
- 凭据详情应统一显示安全 label，缺失时使用非敏感 fallback，禁止前端 raw error 回显 Authorization、query token 或 secret。

## 冗余与清理标记

- `AggregateTaskOutcome` 是兼容入口，待 durable history 持久化契约落地后删除/迁移。
- supplier error dashboard 与方案中的 supplier_errors 表定义存在事实源分叉，需先统一命名和 schema 再清理旧聚合。
- `RequestLogDrawer` 是兼容壳，routing/error 展示应抽出可复用的结构化 panel，而非继续扩展 raw JSON。

## 验证

已通过：

```text
git diff --check
冲突标记扫描
go test ./domains/transformation ./domains/streaming ./storage/file ./storage/memory
go test -race ./domains/transformation ./domains/streaming ./storage/file ./storage/memory
go build ./...
```

`go test ./...` 未完全通过：唯一失败包为未修改的 `domains/nodestatecache`。`TestSelectP99Under1ms` 在本机三次复现 p99 分别为 `2.22975ms`、`3.675875ms`、`3.39675ms`，超过其 `1ms` 性能门禁；其余全仓包已完成。该性能问题不在本轮改动路径，本轮未放宽阈值或掩盖失败。

真实 PG/Redis、外部供应商故障注入、部署后远程日志采样未在本地环境执行，不能宣称已完成。

## 回滚点

- 同步前快照：`/var/folders/q9/_5p60_p90ts99ybv605s8h9r0000gn/T//llm-gateway-pre-sync-20260905-063058`
- 同步基线：`cbf6de062`
- 本轮代码可按最终提交 SHA 使用 `git revert` 回滚；不使用硬重置。
