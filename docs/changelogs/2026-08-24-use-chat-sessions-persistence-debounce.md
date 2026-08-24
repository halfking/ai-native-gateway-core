# useChatSessions 全树 localStorage 写入防抖 (LP6)

- 日期：2026-08-24
- 类型：perf（行为保持型优化）
- 计划文档：`docs/04-implementation/plan/2026-08-24-session-queue-memory-optimization-plan.md` (LP6)
- 交接文档：`/tmp/opencode/llm-gateway-memory-optimization-phase2-handoff.md`
- 阶段：llm-gateway 内存优化 第二阶段 — 前端

## 背景

`useChatSessions()` 每次会话变更（创建、切换、streaming token 累计、模型切换、
title 自动生成）都会立即调用 `saveAll(sessions.value)`，每次都做一次
`JSON.stringify` + `localStorage.setItem` 全树写入。在 LLM 流式回复期间这是热点
路径：每秒数十次 mutation，每次都是同步 JSON 序列化 + 主线程 IO 阻塞。

## 变更内容

### `web/src/composables/useChatSessions.ts`

1. **写入防抖 (300ms)**：所有内部 `persist()` 调用改为 `schedulePersist()`，
   在 300ms 窗口内合并为一次 `flushPersist()`。窗口选 200-500ms 区间的中位
   值：足够吸收 streaming token 累计的 burst，又足够短让用户主动操作
   （切会话、关 tab）感受不到延迟。
2. **生命周期 flush**：注册 `visibilitychange`（hidden）、`pagehide`、
   `beforeunload` 监听器，在隐藏/卸载时立即同步 flush，保证用户不会丢
   未落盘的状态。
3. **scope 自动清理**：通过 `onScopeDispose` 在 Vue effect scope 销毁时
   （典型场景：ChatView 卸载）执行最后一次 flush 并移除事件监听器，
   避免 listener 泄漏。
4. **错误降级**：`saveAll` 内部对 `localStorage.setItem` 和
   `localStorage.removeItem` 都包了 try/catch；私有模式 / 配额耗尽时静默
   失败，内存中的 `sessions` ref 仍为真相，下一个 flush 窗口或 lifecycle
   事件会再次重试。`flushPersist()` 返回 `boolean` 暴露成功/失败状态。
5. **snapshot short-circuit**：跟踪上次成功写入的 JSON 快照，若 burst
   收敛到相同 payload（常见于幂等的 `updateActive({ model })`）则直接
   short-circuit 跳过 setItem。
6. **公开 `flushPersist()`**：暴露在 composable 返回值上，调用方（测试
   或罕见需要立即同步落盘的代码）可主动触发。

### `web/src/composables/useChatSessions.test.ts` (新增)

8 个单测覆盖：
- 5 次连续 mutate 合并为 1 次写入
- `flushPersist()` 在 debounce 窗口前立即落盘
- `setItem` 抛错时不崩、内存状态保留
- snapshot short-circuit 跳过幂等重复写
- `beforeunload` / `pagehide` 同步 flush
- `visibilitychange`（hidden）同步 flush
- `visibilitychange`（visible）不 flush（避免无意义 IO）
- `onScopeDispose` 清理监听器 + 最终 flush

## 行为保持说明

- `saveAll` 调用的次数从「每次 mutation 一次」降到「每次 burst 一次」，
  数据内容（写入 key、TTL、JSON 格式）完全不变。
- 旧调用点的 `persist()` 函数签名保留，内部从「同步 saveAll」改为
  「schedulePersist」，所以业务代码无需改动。
- v1 → v2 迁移逻辑与 legacy key 清理逻辑保持不变，仅放入 try/catch
  容错（之前一旦 setItem 抛错会阻断后续逻辑）。

## 验证

- `pnpm vitest run src/composables/useChatSessions.test.ts` — 8/8 passed
- `pnpm vue-tsc --noEmit` — 无错误
- `pnpm build` — 成功（ChatView chunk: 33.47 kB）
- `bash scripts/pre-commit-check.sh` — PASS（vue-tsc + token compliance）
- `bash verify.sh --skip-govulncheck --web` — PASS
- browser-use 实测（`ui-verify-useChatSessions-*` 截图存档）：
  - 5 次 sync burst 在 300ms 后合并为 1 次 setItem
  - pagehide / beforeunload / visibilitychange(hidden) 同步 flush
  - 刷新页面后会话仍持久化（model='visibilitychange-test'）
  - `setItem` 抛 QuotaExceededError 时不崩，内存状态保留
  - 无 console error / 404 / 渲染异常

## 暂缓项

- `boardLiveMerge.ts` / `liveStreamStore.ts` store 重构（需先 benchmark 支撑）
- per-session key 拆分迁移（当前单 key 已能稳定工作）