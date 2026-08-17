# 33 · 会话重定位审计与 dispatch/eventbus 稳定性修复（2026-08-15）

- 日期：2026-08-15（23:00–23:45 会话）
- 基线：main @ `421683161`（本会话末态，已推送）
- 性质：跨会话状态审计 + 两个时序缺陷修复。本会话起点接到的交接文档
  （`/tmp/handoff-m1-audit-20260815.md`，基于 `1ce8c9222`）所述"待办"
  已全部过时——M1 审计（doc 20）、SC 收尾、M2（doc 21-24）、M3
  （doc 25-31）、M4 S30-S35（doc 32）均已被并行会话完成合入。

## 1. 状态重定位结论（审计事实，非自述）

| 里程碑 | 实际状态 | 证据 |
|---|---|---|
| M1 审计 | ✅ doc 20，四轨 B+，P0/P1=0 | E-P2-3/E-P2-6 已由 doc 23 闭环 |
| SC 收尾 | ✅ 已合入并清理 worktree | `fc3a2e1c5` |
| M2 | ✅ doc 21-24 | RT-2/CO-5/SR-13/MM-2 四轨收口 |
| M3 | ✅ Wave A/B（doc 26-30）+ 流式前台绑定（doc 31，`4ba5e6a5b`） | 源分支合入 `fa22a2b74` 时迁移 515→516 改号，doc 30 撞号警示已妥善处理 |
| M4 | 🚧 S30-S35 场景测试已合入（doc 32，`7a2d14705`）；部署 smoke 仍被 admin key 阻塞 | 见 §4 |

并行会话（w3c 线）在本次审计期间持续活跃推送（22:36–23:30 间 ≥15 commits），
本会话严格避开其文件域（domains/streaming、durable）。

## 2. 修复一：dispatch node_enqueued 数据竞争（`7e35ae483`，已整合 origin/main）

- **现象**：全量 `go test -race` 下 `TestModelChange` 稳定红（单包跑亦可复现）。
- **根因**：OBS-BE1（`e1c204f08`）把 node_enqueued Emit 放在
  `cf.queue <- qr` **之后**，Emit 参数求值读 `qr.ResolvedModel`——send 已把
  qr 所有权移交 forwarder goroutine，其失败重入 failover 后
  `tryModelChange`（dispatcher.go:139）并发改写同一字段，形成无
  happens-before 边的读/写对。与 2026-08-13 并发审计修 CredEnqueuedAt（T5）
  完全同 bug class（pipeline.go:427-430 注释即该次教训）。
- **回归窗口钉死**：`e1c204f08^`（`1b98a6f6c`）上同测试绿 → `e1c204f08` 红。
- **修复**：send 前快照 `reqID/model/emitCtx`，send 成功后用快照 Emit
  （保持"入队成功才发事件"语义；queue_depth 仍读 atomic）。
- **同类排查**（Explore 全量扫描 + 人工复核）：dispatch 包其余 send 点
  （dispatchIn/mq.ch/failoverCh）send 后无读或仅读不可变字段
  （`qr.Ctx` 全仓库唯一写点 pipeline.go:176 Submit 入口、`qr.ID` 零写点）；
  w3c 新代码（durable_stream/durable_wiring/survival_* /attempt_commit_gate/
  durable_recovery_worker）未发现同类模式——goroutine 交接均走
  close-channel/同步调用/锁保护惯用法。

## 3. 修复二/三：两个 flaky 测试

### 3.1 `TestPipelineEmitsFailoverActions`（dispatch，`7465e6337`）

`rec.actions(min=6)` 等到 6 个事件即返回，但断言需要完整 8 步链的最后一个
`no_route`——轮询恰在第 6/7 个事件到达后返回时断言必失败（5 轮 1 红）。
等待下限对齐 8；10 轮 -race 复跑绿。

### 3.2 `TestMemoryBus_BufferFull`（eventbus，`421683161`）

- **现象**：单包跑稳定绿，全量 200 包并行高负载偶发红。
- **根因**：测试假设"不订阅 → dispatch 不消费 buffer"。实际 dispatch
  循环（memory_bus.go:105-109）**无论有无订阅者**都持续取事件，且 handler
  在独立 goroutine 执行（:154-164，阻塞 handler 也钉不住消费循环）——
  "buffer 满"在此实现下是瞬态，原断言是调度竞态。
- **修复**：压满法——buffer=1 下 back-to-back 连发 1000 次（Publish 一次仅
  RLock+select-send，dispatch 消费一次要 recv+copy+spawn goroutine，生产
  必然领先消费），断言至少一次满拒绝 + "成功+拒绝=发布总数"钉住不丢弃
  语义。包级 ×10、-count=20、全量 -race 全绿。
- 注：审计中首个修复尝试（阻塞 handler 钉 dispatch）因 handler 异步执行
  的实现事实而错误，已废弃重写——记录在此防止重蹈。

## 4. 遗留与环境噪声

1. **M4 部署 smoke 阻塞**：`LLM_GATEWAY_ADMIN_API_KEY_252` 未注册
   （envs/projects/llm-gateway-go/ 仅有 `LLM_GATEWAY_ADMIN_API_KEY`），
   deploy-252 验证段两项持续 SKIP，按口径不得宣称 smoke 通过。
2. **环境偶发噪声（非缺陷）**：全量并行时 `plugin-runtime.
   TestMaintainCatalogClientDownloadChecksumMismatch` 偶发
   `connect: can't assign requested address`（本机临时端口耗尽，httptest
   用法正常）；复跑全量 200 包零失败确认。
3. **基线时效**：并行会话推送频繁，全量 -race 验证需以推送时刻 HEAD 为准
   （本会话经历 4 次基线失效重跑）；全量绿结论有效于 `62e734162+eventbus
   修复`，`a05fa46d1`（streaming flush 43 文件改动）仅验证 build+eventbus，
   streaming 域归 w3c 会话自验。
4. 历史遗留 worktree（/tmp queue-routing-audit prunable、distribution-b-c
   detached、.worktrees/ 三处）仍未清理，择机处理。

## 5. 验证记录

- `go build ./...` ✅（a05fa46d1 + 421683161）
- `go vet` dispatch/streaming/durable/metrics/eventbus ✅
- 全量 `go test -race ./...`：200 包零失败零竞争（62e734162 基线 +
  eventbus 修复；dispatch 修复后同结论）
- dispatch `-race` ×5、eventbus `-race` ×10、两个 flaky 测试专项复跑均绿
