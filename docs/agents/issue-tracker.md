# docs/agents/issue-tracker.md — llm-gateway-go task tracking

> 本仓库使用本地 Markdown 跟踪协议。所有 ticket 状态 / 归属 / 收口记录在此维护。

## §1 跟踪位置

仓库使用 `.scratch/<feature>/<NN>-<slug>.md` 单文件 ticket 形式，
与 acc-toolkit 元仓库（`vibe-coding/docs/agents/issue-tracker.md`）
的协议对齐。`.scratch/` 不入仓（`.gitignore` 已配置）。

跨 SP 的 stream-request lifecycle state machine 进展记录在
`docs/changelogs/2026-08-19-stream-state-machine.md` 与
`docs/design/2026-08-19-stream-state-machine.md`；本文件只维护
ticket 关闭状态与当前 active 计划。

## §2 ticket 状态

### e0a849a3f29abff690818a812fa44c34 — pre-stream keepalive 缺位

- **状态**：✅ **closed**（2026-08-18）
- **根因**：154 上 minimax-m3 大 body 请求（800+ messages / ~1.5MB）经
  session compressor `Prepare` 阶段 5-40 秒，期间没有任何 keepalive
  帧发到客户端，opencode SDK 默认 idle 超时（~10s）切断 → `r.Context()` 
  canceled → 重试循环在 attempt 0 之前 abort → 返回 provider_error。
- **修复**：`c52769e4a` 把 `startPreStreamKeepalive` 上移到
  candidate resolution 之前；HTTP 200 + 第一帧 `: keep-alive\n\n` 
  立即发出。详见 `docs/changelogs/2026-08-18-minimax-m3-large-body-keepalive-fix.md`。
- **关联**：本次 stream state machine 计划 C 的设计文档
  `docs/design/2026-08-19-stream-state-machine.md` §4 把
  pre-stream ticker 的 cancel 顺序列为"第一观察者"。

### plan C — stream-request lifecycle state machine refactor

- **状态**：✅ **SP-01..05 merged**（2026-08-19）；stream-state-machine 观察记录与 probe-canary 发布门禁分开维护。当前 probe-canary 未通过全链路门禁，245/154 不应据此推广。
- **范围**：把散落在 handler / executor / compressor / streamretry 中
  的 `slog.Info` 生命周期标记合并为显式状态机（`domains/streaming/state/`）。
  5 个子工作 SP-01..05 按顺序合入 main，245 pre-prod 24h 观察门禁
  见 `docs/changelogs/2026-08-19-stream-state-machine.md` §3。
- **历史记录**：本节记录的是 stream-state-machine 观察快照，原记录中的 `d7ebf25f7` 不代表当前仓库 HEAD。当前代码 SHA 以 `git rev-parse HEAD` 和 `origin/main` 为准；probe-canary 状态见 `docs/handoff/2026-08-19-canary-evidence-and-status.md`。
- **245 deploy 状态**：1618-d7ebf25f binary running on systemd unit
  `llmgo-245.service`（MainPID 765601，ActiveState=active，Restart=always）。
  L1-L4 baseline all green at T+0（23:27 CST）。详见
  `.scratch/2026-08-19-245-observation/00-baseline.md`。
- **设计文档**：`docs/design/2026-08-19-stream-state-machine.md`
  （4 个 ASCII 状态图 + 事件表 + RequestContext 字段表 + 取消传播
  时间线 + slog→状态机映射表 + 并发危险/守卫三件套）。
- **回滚命令**：`docs/changelogs/2026-08-19-stream-state-machine.md` §4。

### LIVE-STREAM-HIDDEN — 页面不可见时静默丢帧，且连接态不说实话

- **状态**：📌 **open**（2026-10-03 开单）
- **一句话**：`document.hidden` 期间到达的 SSE 帧被**整帧丢弃**，而 UI 的连接态
  由 `onopen` 单独置位、不反映「一帧都没处理」，于是嵌入式 / headless webview
  永久空白却看起来一切正常。
- **根因（两处，都已定位到行）**：
  - `web/src/composables/liveStreamStore.ts:1329-1339`：`onmessage` 里
    `if (!visibilityState.isVisible) { missedWhileHidden = true; return }`
    —— 在 `JSON.parse` 之后、`handleEnvelope(env)` 之前，**整帧丢弃**，
    连 `initial_data` 首帧也一样。
  - `web/src/composables/liveStreamStore.ts:1325-1328`：`onopen` 只把
    `connection` 置 `'open'`。该状态与「是否真的处理过帧」无任何耦合，
    所以丢帧期间 UI 仍显示已连接。
- **自愈只在「变可见」时发生，且自身有一个漏洞**：
  `:302-330` 的 `visibilitychange` 在 `!document.hidden && wasHidden`
  时，若 `missedWhileHidden` 且 **`refCount > 0`** 才 `closeConnection()` +
  `openConnection()` 重放。两个后果：
  1. 嵌入式 / headless / 被遮挡的 webview 里 `document.hidden` 恒为 true，
     「变可见」永不发生 ⇒ 永久空白且**无任何提示**。
  2. 即便是真人用户，只要变可见的瞬间 `refCount === 0`（所有订阅方已退订），
     分支不进，但 `missedWhileHidden = false` 仍被执行（`:324` 在 if 之外）⇒
     这一次丢帧窗口被**静默抹掉**，重连永远不会发生。这条比第 1 条更隐蔽：
     它连「等一下变可见就好」这条退路都堵死了。
- **今天（2026-10-03）内嵌 Browser 的实测，与本单同型但未定位到具体分支**：
  `https://llmgateway.internal.example.com/dashboard?tab=stream`（154 / build_seq 2408）：
  `GET /api/admin/live-stream` 返回 **HTTP 200 + `text/event-stream`**，
  但客户端**一帧都没处理**、console 里**零条 `[LiveStream]` 输出**
  （含 `:1336` 那条 `console.debug` 的 page-hidden 分支日志也一条没有），
  于是 `QueuePerspectivePanel` 的 `hasReportedRawModels` 恒为 false，
  「按模型分组的可用节点」分区（`QueuePerspectivePanel.vue:1295` 的
  `v-if` gate）永不渲染。
  **同端 curl 对照证明后端无辜**：同一 JWT 打同一端点，秒出
  `event: message` / `data: {"type":"initial_data",...}` 且带真实
  `glm-5.3` 请求记录。
  ⇒ 收敛结论：**帧在浏览器侧没进到应用**，与「后端/nginx/鉴权」无关。
  ⚠️ 诚实标注：本次**没有**复现出 `:1335` 那条 page-hidden 分支的
  debug 日志，所以**不能**断言本次就是 `document.hidden` 导致的；
  已知的是同一类症状（连接态与实际处理量脱钩 + 静默）。根因待人工在
  前台标签页复现后收敛。
- **不做的事**：不改 SSE 协议、不动「隐藏时不写 state」这个**对真人用户
  合理**的省电设计（`:309-325` 的重连自愈就是为它准备的）。要修的是
  **说谎**的部分：丢帧时连接态必须反映「数据未送达」，以及上面
  `refCount > 0` 那个把恢复路径堵死的分支。
- **建议落点**（本单不含实现，仅登记）：① `missedWhileHidden` 为真时
  连接态显示为「已连接（数据未更新）」之类的**降级态**而非已连接；
  ② `:320` 的 `refCount > 0` 守卫与 `:324` 的无条件清零必须一起改，
  否则丢帧窗口仍会被抹掉。
- **关联**：`docs/changelogs/2026-10-03-stream-model-group-scope-discoverability.md`
  §遗留（该轮已记录但**未开单**）；`docs/changelogs/2026-08-19-stream-state-machine.md`。
- **阻塞的验收**：`dashboard?tab=stream` 的生产 UI 人工验收必须前台标签页
  才能做（`liveStreamStore.ts:1334`），内嵌 Browser 无法闭合。

## §3 SP-01..04 期间的 follow-up 风险

- **pre-stream keepalive 与 session_compressor 串行顺序**：
  当前 keepalive 在 candidate resolution 之后、session compressor 之前
  启动（`c52769e4a`），但若 compressor 端 `tryLLMSummaryWithFallback` 
  在 `ctx.Err()` 检查时收到已 canceled 的 ctx，会立刻跳过 LLM 
  summarizer — 这是 plan C 的预期行为，但如果客户端在 compressor
  之前就断开且 ticker 已启动，会有 1-2 帧空 keepalive 帧发出。245
  观察期内重点看这种"先 keepalive 后 cancel"的占比与延迟分布。

- **状态机 cancel 与重试退避的耦合点**：
  `internal/streamretry/wrapper.go` 的 `bindStateCancel` 派生 ctx
  在 state-machine cancel channel 关闭时立刻 cancel。极端情况下
  如果 retry 已经在 attempt 3 接近完成，可能丢失"已耗时的
  retry 进度" — 245 观察期需关注重试成功率是否回归。

- **状态机 `EventFailed` 与 `EventCancelled` 的优先级**：
  handler 在 executor 返回 error 时用
  `!errors.Is(r.Context().Err(), context.Canceled)` 守卫避免重复
  发 `EventFailed`，但 r.Context() 与 parent context 之间的
  race window 极小 — 245 观察期关注日志里
  `EventFailed` 后立即 `EventCancelled` 的双发情况。

- **compressor 的 ctx 提前退出时机**：
  `tryLLMSummaryWithFallback` 在 `tryLLMSummary` 完成后再次
  检查 `ctx.Err()`，如果 cancel 发生在 LLM summarizer 已返回
  但尚未写入缓存的窗口内，summary 被丢弃。这是预期行为，但
  若 client retry，会重新触发 compressor，重复一次 LLM 摘要
  — 245 观察期关注 compression_quality metric 的 retry 后分布。

- **handler_state_init.go 的 goroutine 泄漏**：
  `initRequestStateMachine` 启动一个 event-loop goroutine，
  依赖 `defer cancelRequestStateMachine(rt, nil)` 触发
  `rt.Cancel(...)` 把它推到 terminal。如果 handler 在某些
  极端 panic 路径下未走到 defer（已被上层 recover 截胡），
  goroutine 会泄漏到下一个 request。SP-02 的
  `TestInitRequestStateMachine_NilSafe` 覆盖了 nil 路径，
  但 panic 路径在 245 观察期需要重点看 `goleak` 类指标。

## §4 关联

- 设计文档：`docs/design/2026-08-19-stream-state-machine.md`
- 变更日志：`docs/changelogs/2026-08-19-stream-state-machine.md`
- keepalive 修复变更日志：`docs/changelogs/2026-08-18-minimax-m3-large-body-keepalive-fix.md`
- acc-toolkit 元协议：`~/workspace/ai-native-tools/vibe-coding/docs/agents/issue-tracker.md`
- 提交纪律：`rules/35-task-commit-discipline.md`（future 合并时遵守）
