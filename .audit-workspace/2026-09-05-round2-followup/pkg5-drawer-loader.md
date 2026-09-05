# 工作包 5：抽屉串页修复（F2-#5）——UnifiedRequestSessionDrawer 迁移 useRequestDetailLoader

日期：2026-09-05　分支：main　会话：审计 round2 followup（前端修复子代理）

## 结论

`web/src/components/detail/UnifiedRequestSessionDrawer.vue` 已整体迁移到共享组合式函数
`web/src/composables/useRequestDetailLoader.ts`。组件内自管的 `loadSeq` /
`bodiesLoadedFor` / `snapshotAbortController` / `loadRequest` / `ensureBodies` 全部删除，
三类实证缺陷（并发覆盖、换轮不清态、bodiesLoading 粘滞）由组合式函数的
per-request 缓存 + seq 守卫 + resetTransientState 根治。对外 props/emits、模板绑定、
UI 展示零变化（模板未动一行）。

验证（全绿）：
- `npm run test`：**112 files / 798 tests passed**（基线 792 → 798，净增 6 条回归用例）
- `npx vue-tsc --noEmit`：exit 0
- CJK 棘轮门禁 `src/i18n/hardcoded_cjk.baseline.test.ts`：6836 ≤ baseline 6836 ✅（未新增任何计数行）

## 一、迁移映射表（组件内旧逻辑 → 组合式函数接口）

| 组件内旧逻辑（迁移前） | 迁移后来源 |
|---|---|
| `loading = ref(false)` + `loadRequest` 手动 set/reset | `metaLoading`（解构重命名 `loading`），`loadMeta()` 内部管理（缓存命中路径不闪 loading） |
| `error = ref('')`；两端点均空 → `t('requestDetail.drawer.notFound')` | `metaError` + `REQUEST_DETAIL_NOT_FOUND` 哨兵常量；组件内 `computed error` 将哨兵映射回 i18n 文案，其他错误原样透出 |
| `log` / `unified` / `sessionSnap` refs，watcher + loadRequest 手工清态 | 组合式函数同名 refs；`loadMeta()` 开头 `resetTransientState()` 统一清态 |
| `warnings = ref([])` + 本地 `recordEndpointFailure()` | `metaWarnings`（新增暴露）+ 组合式函数内 `formatEndpointFailure()`（同样的 `HTTP {status} {msg}` 格式与端点标签） |
| `activeRequestId = ref<string\|null>(null)`（组件自管，与 seq 脱节） | 组合式函数 `activeRequestId`（新增暴露），由 `loadMeta()` 第一行设置——组件不再维护副本 |
| 本地 `sessionId` / `requestBody` / `responseBody` / `outboundBody` computed | 组合式函数同名 computed（定义逐字一致） |
| `let loadSeq` + `if (currentSeq !== loadSeq)` 守卫 | 组合式函数 `loadSeq` + `bumpSeq()`（每次 loadMeta 必 bump，含缓存命中路径）+ 全部写点 seq 守卫 |
| `snapshotAbortController` + 手写 AbortController 生命周期 | 组合式函数 per-loader `AbortController`（`bumpSeq` 时 abort 旧请求，`ensureSessionSnap(sid, signal)` 透传） |
| `let bodiesLoadedFor` + `ensureBodies` 残留守卫 | `bodiesLoaded`（per-composable）+ per-request `CacheEntry.bodiesLoaded`（45s TTL 缓存，以 requestId 为键） |
| `watch([tab, activeRequestId])` → `ensureBodies`（与 loadRequest 并发竞态） | `watch(tab)` → `loadActiveTabBodies()`；requestId watcher / `onSelectTurn` / `openAsRequest` 一律 `await loadMeta(id)` 完成后再 `ensureBodies`（串行化，消灭并发窗口），并做 `id !== activeRequestId` 过期丢弃 |
| `watch(props.requestId)` 手工清 8 个状态 + `loadRequest` | watcher 只重置 `viewMode/tab`（UI 状态），数据态交给 `loadMeta` |
| `onUnmounted` 缺失（卸载不 abort） | `onUnmounted(() => dispose())`（与全屏页消费方一致） |

## 二、三类缺陷的修复点

1. **并发覆盖**（loadRequest bodyless `unified` 落地覆盖 ensureBodies 已写入的完整 body）：
   - 组合式函数每次 `loadMeta`（含缓存命中）都 `bumpSeq()`，旧 seq 捕获的 ensureBodies
     响应到达时 `seq !== loadSeq` → 整体丢弃（useRequestDetailLoader.ts `ensureBodies` 写前守卫）；
   - ensureBodies 成功后 `cachePut(requestId, { ..., bodiesLoaded: true })` 把完整 body
     写回 **per-request** 缓存，之后同 id 的 loadMeta 缓存命中 `applyEntry` 恢复的是
     **带 body 的条目**——bodyless 投影在任何顺序下都不再能覆盖完整 body；
   - 组件侧把「request 切换」与「body 拉取」从两条并发 watcher 改为
     `await loadMeta()` → `loadActiveTabBodies()` 串行链。
2. **换轮不 bump seq / 不清态**（A 轮正文展示在 B 轮视图）：
   - `onSelectTurn` / `openAsRequest` 改走 `loadMeta(newId)`：开头 `resetTransientState()`
     （清 `bodiesLoaded/bodiesLoading/waterfall*`，并新增清 `metaError/metaWarnings`）
     + 非缓存路径清 `log/unified/sessionSnap`、缓存路径只 apply 新 id 自己的条目；
   - 旧 `ensureBodies` 守卫 `if (unified?.bodies || log?.request_body)`（跨请求残留判定）
     不复存在：守卫改为 `bodiesLoaded`（每次切换重置）+ per-request 缓存键，
     `ensureBodies` 再校验 `requestId !== activeRequestId` 双保险。
3. **bodiesLoading 粘滞**（props.requestId 变更 bump seq 后 `finally { if (seq === loadSeq) }`
   永不重置）：`resetTransientState()` 显式 `bodiesLoading.value = false`，且
   `loadMeta` 无条件调用——切换瞬间即复位，晚到的旧 fetch 既不写状态也不改 loading。

## 三、组合式函数接口变更（全部向后兼容，现有消费方不受影响）

`web/src/composables/useRequestDetailLoader.ts`：
1. **新增导出** `REQUEST_DETAIL_NOT_FOUND = '请求详情未找到'` 哨兵常量（原硬编码字面量收编，
   CJK 计数行数不变）；`loadMeta` 双端点均空时写哨兵。消费方按常量匹配后本地化
   （抽屉映射 `t('requestDetail.drawer.notFound')`）；全屏页继续直接显示 `metaError`，行为不变。
2. **新增** `formatEndpointFailure(endpoint, e)`（内部）：抽屉原 `recordEndpointFailure`
   的格式化逻辑（`ApiError` → `HTTP {status} {message}`）上收，端点标签逐字保留
   （`admin/request-detail` / `/api/logs/:id` / `sessions/:id/snapshot`）。
3. **新增** `metaWarnings: Ref<string[]>`：
   - `resetTransientState()` 中清空（同时新增清 `metaError`，修复「上一请求的 metaError
     在下一请求成功加载后仍内联显示」的同类残留）；
   - `loadMeta` 两个 omitBody 端点 `.catch` 记录（ journey 拉取保持静默，抽屉迁移前
     本就不拉 journey，不新增告警噪音）；
   - `ensureSessionSnap` catch 由纯静默改为 seq 守卫后记录告警（快照仍为可选降级，
     `sessionSnap` 保持 null——与抽屉旧行为一致；全屏页不消费 warnings，无 UI 变化）。
4. **新增暴露** `activeRequestId`（此前为内部 ref）：抽屉模板与过期检查直接消费，
   消灭「组件副本与组合式函数内部游标脱节」这一缺陷根因。
5. return 对象追加 `metaWarnings` / `activeRequestId` 两项，其余不变；
   `RequestDetailFullscreenView.vue` 未改动、行为不受影响。

组件迁移的另一处固有差异（可接受、符合审计建议）：抽屉经共享 loader 现在会额外
fire-and-forget 一次 `getRequestJourney(requestId)`（`.catch(() => null)` 静默），
结果组件未消费；换来 per-request 45s 缓存（重开同请求不再重复打列存冷路径）。

## 四、测试清单与结果

新增 6 条（组件级 2 + 组合式函数级 4），全部针对三类缺陷：

`web/src/composables/useRequestDetailLoader.test.ts`（describe `useRequestDetailLoader` 内追加）：
1. `F2-#5 concurrent override: a later meta-only reload must not clobber fetched bodies`
   —— ensureBodies 拉全后同 id 重入 loadMeta，requestBody 仍为完整 body、bodiesLoaded=true。
2. `F2-#5 concurrency: an in-flight body fetch is discarded after the meta reload re-enters`
   —— 挂起的 body fetch 在 seq bump 后到达，不写入、bodiesLoaded 保持 false（可重拉）。
3. `F2-#5 switch residue: switching requests never shows the previous request bodies`
   —— A 拉全后切 B：requestBody 立即 null、bodiesLoaded=false，ensureBodies(B) 拉到 B 自己的 body
   （旧残留守卫会让它 early-return 并显示 A 的正文）。
4. `F2-#5 sticky loading: bodiesLoading resets when the request switches mid-fetch`
   —— 切请求瞬间 bodiesLoading=false，晚到响应不写状态、不复活 loading。

`web/src/components/RequestLogDrawer.test.ts`（新 describe，经兼容壳挂载真实抽屉、真实 SessionTurnsSyncPane emit 链路）：
5. `shows the new turn bodies after a session-pane turn switch (no previous-turn residue)`
   —— chat tab → 会话轮次 → select-request(req-b) → 单请求，对话面板 body 为 B 轮自己的正文。
6. `bodies loading indicator clears after a turn switch resolves mid-fetch (not sticky)`
   —— B 轮 body fetch 挂起期间 `.drawer-body-scroll .drawer-loading` 可见，resolve 后消失且显示 B 正文。

既有测试同步修改（1 处，直接相关契约断言）：
- `web/src/views/DashboardViewV2.test.ts`：`expect(unified).toContain('getRequestLogDetail')`
  → `toContain('useRequestDetailLoader')`（迁移后抽屉不再直接 import 端点函数；
  `not.toContain('getProviderRequestStats'/'providerStats')` 与「打开全页」断言原样保留并通过）。

结果：`npm run test` = 112 files / **798 passed**（0 failed）；`npx vue-tsc --noEmit` 0 错误；
CJK 棘轮 6836 ≤ 6836 ✅。

## 五、文件权限遵守情况

改动仅 5 个文件（`git status -- web/src` 核实）：
- `web/src/components/detail/UnifiedRequestSessionDrawer.vue`（重写 script，模板未动）
- `web/src/composables/useRequestDetailLoader.ts`（+50 行）
- `web/src/composables/useRequestDetailLoader.test.ts`（+135 行）
- `web/src/components/RequestLogDrawer.test.ts`（+135 行）
- `web/src/views/DashboardViewV2.test.ts`（1 处断言更新）

未触碰：`web/public/**`、其他 web/src 组件（无需任何 import 微调——组合式函数为纯追加 API）、
后端 Go/SQL、`cmd/gateway/main.go`、`VERSION`、`version.json`、`.env.local.example`、
`scripts/deploy-local*.sh`。未执行任何 git add/commit。
