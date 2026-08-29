# Handoff: §4 hardening follow-through — 2026-08-29

**交接时间:** 2026-08-29 08:30 +0800
**项目根:** `/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-3`
**当前分支:** `main`
**最新 commit:** `1ac028aec`（已 push 到 origin/main）

---

## §0 任务来源

承接 `.handoff/2026-08-29-audit-native-responses-stream.md` 第四节的"已知未解决问题"
清单，并继续完成 §4 #1 的 panic recover hardening。本轮同时复测全量测试集，
发现并解决 origin/main 上其他人修复的两个 P0 缺陷，以及完成 §4 #1 hardening。

---

## §1 本轮交付

### 1.1 §4 #1 hardening（本次交付）

**修复 commit:** `1ac028aec fix(streaming): guard native Responses SSE reader goroutine against panic`

`domains/streaming/native_responses_stream.go:57` 的 goroutine 调用 `r.readEvent(readCtx)`
但没有 panic recover。**风险：** 如果 readEvent panic（攻击者控制的 SSE 数据 → `json.Unmarshal`
或未来的 `finishNativeResponsesEvent` 重构），goroutine 静默死亡，channel 永远不会被接收，
主 goroutine 被 `readCtx.Done()` 阻塞到 chunk timeout —— 把一个可恢复的 panic 转化为对客户端
的整个 stream timeout。

**修复：** 在 goroutine 内用匿名函数 + defer recover() 包装 `r.readEvent`，panic 变成 error
通过原 channel 流出，ReadEvent 走原有错误路径返回。

**测试：** `TestNativeResponsesEventReaderPanicBecomesError` 用 `panickingReader`（panic 来自 Read）
断言 (a) ReadEvent 返回的 error 包含 "panic" 字样，(b) recover 在 500ms chunk timeout 内生效，
不会等到 ctx deadline 才返回。

### 1.2 复测发现的两个 origin/main 已修 P0（不在本次 commit 内）

执行 `go test ./domains/streaming/... -count=1` 时暴露两个 origin/main 上**本会话开始前
已存在但未被审计报告捕获**的 P0 缺陷。git fetch 后确认 origin/main 已通过独立 commit 修复：

| 缺陷 | 修复 commit | 修复位置 |
|---|---|---|
| `AttemptCommitGate.Discard()` 锁序倒置导致 ABBA 死锁（生产路径） | `0b7579482 fix(streaming): AttemptCommitGate.Discard() must take g.mu only, not writeMu` | `domains/streaming/attempt_commit_gate.go:825` |
| `StreamAnthropicSSEToOpenAI` empty-response 检测漏掉 `pc != nil` 短路 | `b4e4449d3 fix(streaming): thread pc != nil into StreamAnthropicSSEToOpenAI empty-response check` | `domains/streaming/anthropic_bridge.go:1205` |

详见 `.handoff/2026-08-29-followup-streaming-recheck.md` §3（remote 团队的复测与修复文档）。

### 1.3 流程协作经验

1. **stash 重叠工作：** 本会话开始时本地 main 落后 origin/main 8 个 commit（`8778b3d6c`...
   `b741f4ece`），`git pull --ff-only` 因本地 Commit 修复未提交而冲突。**正确流程：**
   `git stash` → `git pull --ff` → `git checkout stash@{0} -- <files>` 选择性恢复 → 丢弃 stash。
2. **避免与 remote 修复重复：** origin/main `0b7579482` 的修复方向是"删除 Discard 的 writeMu"
   （符合 §10 lock-scope 注释的原始设计），比我本地"Commit 端释放双锁"更精准、更小 diff。
   本会话最终只恢复 panic recover 那两个文件，丢弃 Commit 修复，避免重复 commit。
3. **rebase 而非 merge：** push 时 origin/main 已新增 `7b652bbf3`，用 `git rebase origin/main`
   把 panic recover commit 提到最新 origin/main 之上，避免 fast-forward 阻断。

---

## §2 全量验证（panic recover + 上述 origin/main 修复同时落地后）

| 命令 | 结果 |
|---|---|
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `go test ./domains/streaming/... -count=1 -timeout 120s` | ok（66s，**所有测试通过**——含之前 §13 报告的 `TestStreamAnthropicSSEToOpenAI_DisconnectsKeepsCapturer`） |
| `go test ./domains/streaming/executors/... ./domains/streaming/integrity/... ./domains/streaming/state/... ./domains/transformation/... ./provider/... ./cmd/gateway/... -count=1 -timeout 180s` | ok（10 个目标包全绿） |
| `git status` | clean working tree |
| `git log origin/main -1` | `1ac028aec`（本次修复） |

§0.1 的目标测试集**全绿**——`streaming` 主包本次也通过，原因是 origin/main 修复了之前
hanging 的 `TestAttemptCommitGateCommitReturnsDiscardedWhenDiscardWinsDuringHook` 和失败的
`TestStreamAnthropicSSEToOpenAI_DisconnectsKeepsCapturer`。

---

## §3 本轮提交历史

```
1ac028aec fix(streaming): guard native Responses SSE reader goroutine against panic  ← 本次
7b652bbf3 feat(proxy): Stage 3 — full management UI + Prometheus metrics + tests
cd84808cb merge: integrate history-aware recovery action policy into main
231dae884 Merge branch 'main' of https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go
ee22662b4 docs(handoff): 2026-08-29 接手 main-integration 后续 — 复测与死锁修复       ← remote 团队
b4e4449d3 fix(streaming): thread pc != nil into StreamAnthropicSSEToOpenAI empty-response check  ← remote 团队
0b7579482 fix(streaming): AttemptCommitGate.Discard() must take g.mu only, not writeMu  ← remote 团队
a7f5a1c13 fix(migrations): 在创建 credential_model_capabilities 前添加主键
a659f451d merge: integrate origin/main (2026-08-29) — migrations renumber 611→612, 612→613
6ef7e8455 fix(migrations): 重编号 611→612, 612→613 避免迁移编号冲突
9dd65c7ae docs(handoff): capture 2026-08-29 audit findings + dispatch gate fix            ← 上次审计
8778b3d6c fix(dispatch): accept stream-only native Responses capability at gate          ← 上次审计
```

---

## §4 §4 剩余未解决问题

仅剩 §4 #2 和 #3——两者都是**验证覆盖问题**，**没有生产路径风险**：

1. **官方 OpenAI Responses SDK live interop 仍是 UNKNOWN**（e55f71305 注释确认）
   - SDK 不在 go.mod / 无 provider keys / 无网络
   - 当前测试覆盖 gateway 的兼容 event serializer 表面

2. **Real provider/TCP/Redis/PG verification 仍是 UNKNOWN**（e55f71305 注释确认）

§4 #1 已在本次会话关闭。

---

## §5 经验教训（未来 harden 警示）

1. **`AttemptCommitGate` 锁路径不一致是历史负担**：
   - `Discard` 历史上不拿 writeMu（lock-scope 注释明确禁止）
   - `ee438de9c` (2026-08-28) 反向引入 writeMu，造成 ABBA 死锁
   - `0b7579482` 又删回。本次复测**再次**暴露了锁路径的脆弱性——任何写路径
     （WriteFrame / FinishAttempt / FlushHoldback / Commit）的 hook 窗口都
     会被 Discard 反向死锁。
   - 建议：**未来重构 `AttemptCommitGate` 时考虑用 channel + state machine
     替代双 mutex**，避免 hook/wire 路径与 reset 路径的交叉竞争。

2. **Goroutine panic recover 是 SSE/IO 路径的通用 hardening**：
   - 本次 §4 #1 是 5 处 SSE/IO goroutine 中**唯一**没有 recover 的
   - 建议：grep `go func()` 在 streaming 包内做一次系统审计，确认所有 reader goroutine
     都有 panic recover 或可证明的安全论证

3. **流式测试的 hang 容易被误判为进程卡死**：
   - `go test ./domains/streaming/` 不带 `-timeout` 会等到 10 分钟默认超时
   - 本次复测发现：**所有 streaming 包测试必须在 CI 上带 `--timeout 60s` 才能暴露 hang**
   - 建议：在 `make test-streaming` 或 CI 配置里把 streaming 包显式 `-timeout 30s`

---

## §6 引用

- 本次修复：commit `1ac028aec` (panic recover)
- remote 团队的并发工作：`.handoff/2026-08-29-followup-streaming-recheck.md` §3
- 上次审计：`.handoff/2026-08-29-audit-native-responses-stream.md` §4
- 历史背景：`.handoff/2026-08-28-followup-audit-closeout.md` §10–§13

## §7 当前状态（Current State）

- 工作目录：`/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-3`
- 分支：`main`，HEAD `1ac028aec`，与 `origin/main` 一致
- 工作区：clean
- 未推送改动：无
