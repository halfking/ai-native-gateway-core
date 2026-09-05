# 修复说明（round2-followup · pkg5）：抽屉复审残留 P2-1 / P2-2 / P3-3

日期：2026-09-05　分支：main　HEAD：ac8396978　依据：`audit2-pkg5-drawer.md` 发现清单

## 结论

三项修复（P2-1、P2-2、P3-3）已全部落地，位于 `web/src/composables/useRequestDetailLoader.ts`
（随 `13255dd0c` "round2 followup 三包合入" 进入 HEAD；本次会话核对时工作区与 HEAD 逐字一致）。
本次会话逐项核对修法与审计建议一致，并以**回退实验**实证 4 条回归用例的判别力（回退后 4 条全失败，
恢复后全绿），全量验证通过。未改任何其他文件：`UnifiedRequestSessionDrawer.vue` 零改动、
`RequestLogDrawer.test.ts` 未动（两处缺陷均在组合式函数层，抽屉路径已被审计确认串行化，无需组件级用例）。
未执行 git add/commit。

## 一、逐项修法（file:line）

### P2-1 metaWarnings.push 无 seq 守卫 → 已加守卫（必修）

- 位置：`web/src/composables/useRequestDetailLoader.ts:296-310`
- 修法：loadMeta 两个 omitBody 端点的 `.catch` 闭包内，push 前先比对 `seq === loadSeq.value`
  （`seq` 在 `:287 const seq = bumpSeq()` 捕获，闭包可见），与外层 catch（:366）及
  ensureSessionSnap catch（:393）的既有守卫对齐：
  - `:303` `if (seq === loadSeq.value) metaWarnings.value.push(formatEndpointFailure('admin/request-detail', e))`
  - `:307` `if (seq === loadSeq.value) metaWarnings.value.push(formatEndpointFailure('/api/logs/:id', e))`
- 效果：A 在途时切 B，B 的 `resetTransientState` 已清空 warnings，A 的端点稍后失败不再把告警
  推进 B 的视图（stale push 被丢弃，A 的 Promise.all 结果仍按 stale 丢弃）。

### P2-2 同 seq 并行下 bodyless meta 覆盖完整 body 并击穿缓存 → 合并落地（必修）

- 位置：`web/src/composables/useRequestDetailLoader.ts:325-362`
- 修法：loadMeta 落地处（seq 守卫 `:324` 之后）先 `const prior = cacheGet(requestId)`（:331），
  当 per-request 缓存条目已 `bodiesLoaded` 时做**合并落地**——meta 字段用本次新值、bodies 相关字段
  保留旧值完整保留，且不降级 `bodiesLoaded`：
  - `:335-337` `landedU`：`u` 存在且 `prior.unified?.bodies` 存在 → `{ ...u, bodies: prior.unified.bodies }`
    （meta/meta 之外字段取新、bodies 取旧）；`u` 为空 → 回退 `prior.unified`；
  - `:338-348` `landedLog`：`{ ...prior.log, ...meta, request_body: meta.request_body ?? prior.log.request_body, ... }`
    （omitBody 响应的 null body 字段不得清掉旧 body，逐字段 `??` 兜底）；
  - `:356-362` `cachePut(requestId, { log: landedLog, unified: landedU, bodiesLoaded: prior?.bodiesLoaded ?? false })`
    —— `:361` 显式保留 bodiesLoaded 标记，不击穿带 body 缓存条目。
- 效果：fullscreen（RequestDetailFullscreenView 的 requestId/viewMode watcher 同 flush 并行扇出）
  下同 seq 的 ensureBodies 先落地时，随后的 loadMeta 落地不再清空视图 body、不再把缓存打回
  bodyless；抽屉路径行为不变（本就串行化）。

### P3-3 getRequestJourney 移出 Promise.all → 旁路 fire-and-forget

- 位置：`web/src/composables/useRequestDetailLoader.ts:311-323`
- 修法：journey 不再进入 `:301-310` 的 `Promise.all`（其中只剩两个 omitBody 端点），改为旁路：
  `:317-323` `void getRequestJourney(requestId).then((j) => { if (seq !== loadSeq.value) return; journey.value = j; if (j) cachePut(requestId, { journey: j }) }).catch(() => null)`
  —— seq 守卫后写入 journey 状态（:319）、失败静默（:323，维持原降级语义）、命中后并回缓存条目
  （TTL 重入经 `applyEntry` 恢复）。
- 效果：meta 首屏（抽屉 `v-if="loading"` 全面板）不再等待 journey 冷路径 RTT；journey 晚到仅补充
  `attempts` computed。

## 二、测试清单（web/src/composables/useRequestDetailLoader.test.ts）

| 用例 | 行 | 钉住的缺陷 |
|---|---|---|
| `round2 P2-1: a stale endpoint failure after switching requests does not leak into the new warnings` | :518 | A 慢失败/B 快成功 → B 视图 warnings 恒空；守卫缺失时 `:541 expect(metaWarnings).toEqual([])` 失败 |
| `round2 P2-2: same-seq parallel loadMeta + ensureBodies keeps bodies when ensureBodies lands first` | :545 | 同 id loadMeta（omitBody 挂起）与 ensureBodies 并行、ensureBodies 先完成 → 落地后视图 body 不丢（:581）、`bodiesLoaded` 仍 true（:582）、重入缓存仍恢复 body（:586） |
| `round2 P3-3: a slow journey does not delay meta first paint and lands out-of-band` | :589 | journey 挂起时 meta 首屏完成（:598 `metaLoading === false`），journey 落地后喂入 attempts（:613） |
| `round2 P3-3: a stale journey landing after a request switch is discarded` | :617 | 切换后晚到 journey 被 seq 守卫丢弃，不串入新视图 attempts（:640） |

测试总量：审计基线 798 → **802**（净增本表 4 条）。

## 三、判别力实证（回退实验，本次亲跑）

为排除"恒真用例"，本次将三处修法在 worktree 临时回退（P2-1 两处守卫删除；P2-2 合并块删除、
改回无条件 `unified.value = u; log.value = meta` + 不带 bodiesLoaded 的 cachePut；P3-3 journey 放回
Promise.all），随后仅跑 round2 用例：

```
npx vitest run src/composables/useRequestDetailLoader.test.ts -t 'round2'
→ Tests  4 failed | 17 skipped (21)
```

4 条 round2 用例在回退代码上**全部失败**（P2-1 于 ：541 失败、P2-2 于 ：581 失败、两条 P3-3 超时
失败——journey 回到 Promise.all 后 meta 首屏被挂起的 journey 阻塞），随后 `git checkout --` 恢复，
工作区与 HEAD 重新逐字一致。证明每条用例均"会失败于回归代码"。

## 四、验证输出（本次亲跑，全绿）

```
cd web && npm run test 2>&1 | tail -4
  Tests  802 passed (802)          # 0 failed
npx vue-tsc --noEmit 2>&1 | tail -2
  (无输出) exit 0
node scripts/i18n-cjk-count.mjs
  baseline: 6836 (2026-09-05) / ✅ within baseline   # 6836 = 6836，棘轮持平
```

## 五、范围确认

- 改动文件：仅 `web/src/composables/useRequestDetailLoader.ts`（已在 HEAD）+
  `web/src/composables/useRequestDetailLoader.test.ts`（已在 HEAD）；本次会话净代码改动 0
  （回退实验已完全还原）。
- `UnifiedRequestSessionDrawer.vue`：未动（0 diff）。
- 未执行 git add / commit。
