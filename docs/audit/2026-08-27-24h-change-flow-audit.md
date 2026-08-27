# 2026-08-27 近 24 小时变更与闭环审计

## 范围与基线

- 审计窗口：截至 2026-08-27 的最近 24 小时。
- 基线：`origin/main` 的 `a71ab4ba9`。
- 方法：按非 merge 提交和模块归类，追踪请求入口、转换、状态写入、异步消费、外部网络和最终释放点；对近期大规模合并后的关键 wiring 做存在性核验。
- 结论：当前主线关键实现与测试/迁移契约仍在，没有发现被 merge 覆盖或删除的生产代码。仓库内的不可达 Git 对象来自历史 reflog/stash；未执行任何清理或删除操作。

## 近 24 小时变更摘要

### 调度、队列与持久化

`dispatch` 在本窗口加入 planner、attempt journal、request class/dueAt 语义及 local/Redis 双后端准入。主流程为：请求进入 `Pipeline.Submit`，通过总队列及 cluster admission，进入 model/credential lane；限流或容量不足时进入 due parked 状态；转发后无论成功、失败、取消或超时，均由 complete/release 路径归还本地与 Redis admission。Redis 计数和 heartbeat 使用 Lua 原子操作，故障时退化到本地上限，自愈逻辑清理 stale 实例计数。

复核确认 `SetQueueBackend`、`releaseAllClusterAdmissions`、Redis heartbeat/due/self-heal 以及 request class/attempt journal wiring 仍在主线。对本轮外部工作树中未提交的 dispatch queue-depth WIP 未做修改，也未纳入本次提交。

### 流式、会话与网络

SSE 与 streaming 链路近期补齐了客户端 write deadline、取消路径、connection entry identity 检查、partial replay 边界和 upstream disconnect 的 interrupted 语义。连接池使用 transport 级 Dial/TLS/header timeout、keep-alive、连接数上限和 request context 取消来维持 TCP 会话可靠性。

复核确认 composition root 仍向 chat/admin 共享同一 `ConnectionRegistry`，避免实时状态和控制操作分裂。

### 恢复、配额与凭据探针

周期探测、配额探测和恢复组件将凭据状态写回数据库/cache，并通过 fast reprobe 处理短期不可达、认证失败和配额恢复。`Start`/`Stop` 与队列 consumer 的生命周期已有幂等保护。

本轮修复了 `cycleAll` 直接写 `fastReprobeQueue` 的旁路：它现在统一调用 `SubmitFastProbe`，复用每凭据 pending 去重、队列满处理、Stop 清理和延迟 worker 生命周期。这样同一凭据不会因为周期扫描、quota probe 和 recovery 并发产生重复延迟任务。

### 输入、协议转换与安全边界

近期引入 IR Responses/Gemini/Anthropic 协议识别、conversion metadata 和 malformed input 拒绝；dispatch/webhook 已有 body cap、签名验证和请求上下文超时。复核确认 `internal/jsonbody.ReadRequired`、webhook 认证、request detail/archive 和 migration request-class 链路存在。

### 数据库与 schema

近期 dbx/manifest pilot、tenant policy manifest 及 migration 608/609/610 对齐了 schema 与请求类字段。主生产 repository 仍是既有手写路径；本轮未改变 migration，也未发现当前代码引用缺失的 migration/manifest 符号。

## 本轮修复

1. `bg/credential_probe_v2.go`
   - 周期扫描失败后的 fast reprobe 改为走统一去重入口，防止重复排队和 worker 资源竞争。

2. `domains/approval/llm_client.go`
   - 默认 HTTP client 增加 30 秒总超时。
   - 非成功响应错误体限制为 8 KiB 并处理读取失败。
   - 成功 JSON 响应解码限制为 1 MiB，防止异常上游无限响应占用内存。

3. `admin/free_pool_extra.go`
   - mail.tm 与 OpenAI-compatible probe 的响应读取改为受限读取：模型响应 1 MiB、错误响应 8 KiB。
   - 处理响应读取错误，避免忽略 `io.ReadAll` 错误。
   - 每个重定向目标重新执行 public URL 校验，阻断初始 URL 合法、重定向到私网/本地地址的 SSRF 绕过。

4. `pool/pool.go`
   - `StartHealthCheck` 通过原子启动标志保证同一 pool 只创建一个 health loop，避免重复 ticker、重复探测和 Close 等待多余 goroutine。

## 验证

以下均在隔离 worktree 的审计分支执行并通过：

```text
go test ./bg ./domains/approval ./admin ./pool ./domains/dispatch ./domains/streaming/...
go build ./...
go vet ./...
go test -race ./bg ./domains/approval ./admin ./pool ./domains/dispatch ./domains/streaming/executors ./internal/logging ./domains/session/v2
git diff --check
```

race 命令仅输出 vendored `github.com/shoenig/go-m1cpu` 的 Clang variable-length-array 扩展告警；没有 Go race 报告，也没有测试失败。

## 风险结论与后续关注

- Redis fail-open 是有意可用性取舍：Redis 故障期间只能保证本实例本地边界，无法保证跨实例全局容量；指标告警和故障恢复验证仍是运营闭环的一部分。
- Approval client 成功响应的 1 MiB 上限覆盖摘要使用场景；若未来将其复用为大文档处理客户端，应单独定义更大的契约，不应静默放宽。
- 未合并远端分支和当前原工作树 stash/WIP 均已保留，未删除；它们需要独立的 merge/archive 决策，不能作为本次“代码丢失”结论的一部分。
