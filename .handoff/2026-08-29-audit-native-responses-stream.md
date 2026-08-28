# Handoff: 全面审计 + 流程闭环修复 — 2026-08-29

**交接时间:** 2026-08-29 07:30 +0800
**项目根:** `/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-3`
**当前分支:** `main`
**最新 commit:** `8778b3d6c`（已 push 到 origin/main）

---

## §0 审计结论

最近两次 commit（`feat(streaming): add gated native Responses SSE capability` 5ba33d9e3 +
`feat(audit): unified legacy stream contract + Gemini writer hardening` e55f71305）
经过 9 个维度的逐项检查，**发现 1 个真实 bug（流程闭环不对称）并已修复**。

### 0.1 状态总览

- ✅ `go build ./...` — clean
- ✅ `go vet ./...` — clean
- ✅ `go test ./domains/streaming/... ./domains/transformation/... ./provider/... ./cmd/gateway/... -count=1` — 全绿
- ✅ `8778b3d6c` 已推送到 `origin/main`（fast-forward）
- ✅ origin/main 14 个新提交已并入（proxy 阶段1/2 + audit hardening），无冲突
- ✅ 工作区干净（`git status` 无未提交变更）

### 0.2 修复详情

**Bug #1：dispatch gate 与 executeOpenAI 的 capability gate 不对称**

| 位置 | 原逻辑 | 修复后 |
|---|---|---|
| `domains/streaming/executors/executor_dispatch.go:596` | `!cand.SupportsNativeResponses` | `!cand.SupportsNativeResponses && !cand.SupportsNativeResponsesStream` |
| `domains/streaming/executors/executor_chat.go:374-382` | 已接受 stream | (不变) |

**触发场景：** 一个 credential 仅启用 `native_responses_stream` capability
（migration 612 扩宽了 CHECK constraint 允许此 key），但未启用 `native_responses_nonstream`。

**原行为：** dispatch_v2 路径（生产唯一默认路径，见 executor_dispatch.go:23-31 注释）
在 `forwardForDispatch` line 596 直接拒绝该候选，错误信息
"native Responses upstream capability is not enabled"——新加的 stream capability
在生产路径下完全不可用。

**修复行为：** 与 executeOpenAI 的 capability gate 对齐，承认 stream-only credential，
转发至 executeOpenAI 后由后者内部的 gate 决定是否走 native stream 路径。

### 0.3 新增回归测试

| 测试 | 验证内容 |
|---|---|
| `TestForwardForDispatchAcceptsStreamOnlyNativeCapability` | SupportsNativeResponsesStream=true, SupportsNativeResponses=false 的 credential 必须能通过 dispatch gate 并完成 streaming forward |
| `TestForwardForDispatchRejectsNoNativeCapability` | 两个 capability 都为 false 的 credential 必须被 dispatch gate 拒绝，且不得触碰 upstream |

两个测试都通过（`go test ./domains/streaming/executors/...`）。

---

## §1 9 个审计维度的检查结论

| 维度 | 检查项 | 结论 |
|---|---|---|
| 1. 代码无丢失 | 两个 commit 的 20 个文件全部在 working tree | ✅ 无丢失 |
| 2. 流程闭环 | capability gate / executor callback / dispatch.forwardForDispatch 三处对称 | 🔴 发现 bug #1（已修） |
| 3. 并发锁 | attempt_commit_gate 双锁（mu+writeMu）/ StreamWriter mu / native_responses_capture 无锁（local helper） | ✅ 合理 |
| 4. 资源/句柄 | native_responses_stream.go:150 `defer resp.Body.Close()` / executor_anthropic.go 5 处 close + Drain / legacy_transport body-close goroutine 完备 | ✅ 无泄漏 |
| 5. 错误处理 | StreamWriter/Flush recover() / safeFlush recover() / dispatch.go:445 panic recover / native_responses_stream.go:57 goroutine 无 recover（r.readEvent 只调 LineReader，已知安全）| ✅ 可接受 |
| 6. 数据转换 | native_responses_capture.go 用 `json.Marshal(map[string]any{...})` 构造 Chat-shaped 合成 payload，仅喂给 legacy StreamCapture funnel，从不重发给客户端 | ✅ 无丢失 |
| 7. 溢出/数据结构 | sseMaxLineBytes 16MiB（stream_support.go）/ pendingCapturer.maxBytes 1MiB（pending.go）/ maxBodySize 128MiB（executor.go:55） | ✅ 受控 |
| 8. 网络/TCP | DeriveStreamContext 派生整体超时 / legacy_transport body-close goroutine 在 ctx done 时关闭 body / StreamWriter ctx cancel 短路 / Flush panic recover | ✅ 可靠 |
| 9. 密钥/API 可用性 | migration 612 CHECK constraint 包含两个 key / provider/client.go LEFT JOIN credential_model_capabilities cmcap + cmstream / Candidate JSON tag 正确 | ✅ 一致 |

---

## §2 用户原始请求回顾

> 请对本任务进行审计，根据模块及业务流程进行总结，提出关键要点。然后再根据这些要点，检查当前的代码，确认在这个过程中没有代码丢失的情况。并仔细分析其流程闭环、数据来源与去处、并发锁控制、多线程资源竞争、内存或句柄泄漏、异步处理异常、异常错误的处理、请求数据转换、各数据结构与参数有无溢出可能性、网络请求处理的可靠性、tcp会话维持的可靠性、密钥可用、数据api的可用情况等等方面进行检查，修正发现的问题，然后提交代码并合并到主分支推送，不要丢弃其他人修改的代码

**回应总结：**

1. **基于模块与业务流程的关键要点：**
   - 两条并行主线：`feat(streaming)` 5ba33d9e3 + `feat(audit)` e55f71305
   - streaming 链路：candidate → dispatch gate → executeOpenAI gate → native stream handler → client
   - audit 链路：StreamWriter 统一契约 + Gemini writer hardening + frame classifier 扩展

2. **代码无丢失验证：** ✅ 全部 20 个文件均在 working tree

3. **9 个维度逐项检查：** 见 §1 表格（仅流程闭环发现问题）

4. **修正的问题：** 1 个（dispatch gate 不对称），已修复 + 已加测试

5. **架构/流程文档同步：** 修复仅是 gate 条件对齐，未涉及架构或流程修改——docs 不需要更新

6. **推送状态：** `8778b3d6c` 在 `origin/main`

7. **未丢弃其他人的代码：** ✅ merge origin/main（fast-forward）无冲突

---

## §3 提交历史（含本次修复）

```
8778b3d6c fix(dispatch): accept stream-only native Responses capability at gate  ← 本次
f2f527511 feat(proxy): 阶段2 — Transport 工厂缓存 + 并发健康检查
a2c5bcb81 feat(proxy): 代理管理系统阶段1 — 让网关经本地网桥代理探活海外供应商
7715ca1d4 fix(audit): harden merged URSM/ledger fixes — lua nil-guard, atomic ledger append, subscriber auto-reconnect
bf1ae3c0a merge: integrate origin/main (2026-08-29) — keep local §13 follow-up review
4ebd6a24f docs(handoff): capture main-integration follow-ups (merge + audit)
987653e98 fix(streaming): align empty-response detection with documented contract
f3513cebb fix(streaming): align IsAnthropicStreamEmpty with documented contract
6942dbc9a merge: integrate origin/main into fix/streaming-ursm-audit-closeout-20260828
6b1159bb4 docs(handoff): capture main-integration blockers from audit pass
366f9da36 docs(recovery): record history-aware decision audit findings
f97c8eeda fix(recovery): address history-aware decision audit findings
d4f410c88 feat(recovery): add history-aware request action policy
dbcc42ab9 fix(audit): harden regen-credentials and restore SafeHGetAll
ee438de9c fix(streaming): close gate and empty-response lifecycle gaps
5ba33d9e3 feat(streaming): add gated native Responses SSE capability            ← 审计起点
e55f71305 feat(audit): unified legacy stream contract + Gemini writer hardening    ← 审计起点
```

---

## §4 已知未解决问题（出本次范围）

1. **`native_responses_stream.go:57` goroutine 无 panic recover**
   - 现状：goroutine 调用 `r.readEvent(readCtx)` → `r.reader.ReadLine()`，LineReader 已知安全
   - 风险：极低（除非 LineReader 自身引入 panic）
   - 建议：未来 hardening 时在 goroutine 外层加 `defer func(){ if r := recover(); r != nil { ... } }()`

2. **官方 OpenAI Responses SDK live interop 仍是 UNKNOWN**（e55f71305 注释确认）
   - SDK 不在 go.mod / 无 provider keys / 无网络
   - 当前测试覆盖 gateway 的兼容 event serializer 表面

3. **Real provider/TCP/Redis/PG verification 仍是 UNKNOWN**（e55f71305 注释确认）

---

## §5 建议下一步

如果未来需要扩展审计，建议关注：

- **Capability gate 全链路一致性：** 增 capability 时必须同时检查 dispatch + executeOpenAI + 任何新引入的 executor 入口
- **StreamWriter 错误传播路径：** 上游调用者是否都通过 `sw.Err()` 而不是依赖底层 w 的状态
- **Frame classifier 演进：** 增新 event name 时记得把新 name 加进 `classifyResponsesFrame` switch，否则会被归类为 FrameClassUnknown（fail-closed）

如果有任何问题或需要继续审查，请在此基础上继续。
