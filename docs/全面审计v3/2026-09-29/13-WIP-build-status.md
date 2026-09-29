# WIP 实时 build 状态（2026-09-29 15:50 接力会话捕获）

> **告警**：本文档记录接力会话最后几轮扫描到的 WIP build 状态异常，**非正式审计结论**。
> 仅作为下一接力会话或原作者 WIP 收口时的可追溯事实。
>
> 触发：本会话早些时候（commit `1d6a6822b` 推 F04 设计提案前后）跑 `go build ./...` 是干净的；
> 在 README 索引 commit (`cce0e640b`) 之后或任务期间，WIP 被改动为 build 失败。

## 1. 现状（实测）

`go build ./... 2>&1` 在 15:50 输出：

```
# github.com/kaixuan/llm-gateway-go/internal/ir
internal/ir/reasoning_dialect.go:22:20: undefined: reasonnorm.IsDisableEffort
# github.com/kaixuan/llm-gateway-go/domains/hooks/outputcompliance
domains/hooks/outputcompliance/stream_compliance.go:80:45: meta.CallerOwner undefined
        (type *response.StreamMeta has no field or method CallerOwner)
domains/hooks/outputcompliance/stream_compliance.go:127:32: unknown field SuppressChunk
        in struct literal of type response.ChunkResult
domains/hooks/outputcompliance/stream_compliance.go:152:31: unknown field SuppressChunk
        in struct literal of type response.ChunkResult
domains/hooks/outputcompliance/stream_compliance.go:313:15: assignment mismatch:
        1 variable but it.ownerFn returns 2 values
domains/hooks/outputcompliance/stream_compliance.go:313:42: too many arguments in call to it.ownerFn
        have (context.Context, string, string)
        want (string, string)
```

## 2. 与本会话 commit 的关系

本会话 4 个 commit（`e9c733521` / `37d5b5a71` / `1d6a6822b` / `cce0e640b`）：
- 修改文件：`domains/streaming/native_response_hook_integration_test.go`（1 行测试 setup）+ `docs/`
- **未触及** `internal/ir/reasoning_dialect.go` / `domains/hooks/outputcompliance/stream_compliance.go` / `domains/hooks/response/types.go` / `internal/reasonnorm/norm.go`

错误是 WIP 内 API drift（一个文件改了类型，另一个文件没同步）。

## 3. 修复路径（建议 WIP 作者或下一接力会话）

| 错误 | 修复路径 |
|---|---|
| `reasonnorm.IsDisableEffort undefined` | 在 `internal/reasonnorm/norm.go` 加 `IsDisableEffort(effort string) bool`，或改 `internal/ir/reasoning_dialect.go:22` 走已有的 `NormalizeEffortAlias` + `"none"` / `"disabled"` 字符串比对 |
| `meta.CallerOwner undefined` | `domains/hooks/response/types.go` 的 `StreamMeta` 加 `CallerOwner string` 字段；同结构 `InterceptRequest.CallerOwner` 已有 |
| `SuppressChunk not in ChunkResult` | `domains/hooks/response/types.go` 的 `ChunkResult` 加 `SuppressChunk bool`；或 `stream_compliance.go` 改用 `ShouldBlock`（已有等价语义） |
| `ownerFn signature mismatch` | 调齐两边：`OwnerContextFunc` 当前是 `func(ctx, sessionID, tenantID) string`，`stream_compliance.go:313` 期望 `(context.Context, string, string)` 返回 2 值。或反之：让 `OwnerContextFunc` 签名返回 2 个值 |

每条修复**先确认当前生产路径**用哪一种（OwnerAllowsSensitive / ShouldRedact 的 caller 与 data owner），不要盲目让一边迁就另一边。

## 4. 教训（可迁移）

1. **WIP build 状态会以分钟为单位变**：本会话早些时候推 `1d6b6822b` 时跑 `go build ./...` 是干净的，几分钟后再跑就 FAIL
2. **任何"全绿"断言都需要在 commit 之前**现跑现取，不能用 mtime / reflog / commit message 推断
3. **`git diff --stat` 不能证明 build 状态**——只证文件修改；WIP build 错误必须真编译才能发现
4. **本会话早先 `go test ./domains/streaming -run '^TestRestoredSensitiveFrame'` PASS 是基于更早的 WIP 状态**——同一命令现在 FAIL（build error）。结论：早先的 PASS 报告**在 commit 时尚未反映最终 WIP 状态**

## 5. 下一步建议

1. WIP 作者或下一接力会话**先 build fix**，再合流 F02/F10 的 74 项 WIP 到 main
2. 在 WIP commit 之前，必须 `go build ./... && go test ./...` 全绿
3. 修 build error 后再按 `02-审计证据与缺陷台账.md` 推进 P1 收口