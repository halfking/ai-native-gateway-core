# 审计报告（round2-followup · pkg5）：UnifiedRequestSessionDrawer 迁移 useRequestDetailLoader（F2-#5）

日期：2026-09-05　分支：main　审计对象：工作区未提交改动（`git diff HEAD -- web/src/`，5 个文件，与修复报告声明一致）
审计方式：只读，逐路径源码推演 + 实测（不依赖修复报告结论）。

## 实测自验（本审计亲跑）

| 项目 | 结果 |
|---|---|
| `cd web && npm run test` | **112 files / 798 tests passed（0 failed）**，与报告 792→798（+6）一致 |
| `npx vue-tsc --noEmit` | exit 0 |
| `node scripts/i18n-cjk-count.mjs` | **6836 = baseline 6836 ✅**（计数器实为"跳过纯注释行"口径，`src/i18n/hardcodedCjk.ts:65`；本包新增中文全部位于注释中；`'请求详情未找到'` 字面量在 useRequestDetailLoader.ts 同文件内由行内字面量收编为导出常量，非注释行计数中性） |
| 改动文件清单 | `git status --short web/src` = 恰好 5 个：UnifiedRequestSessionDrawer.vue / useRequestDetailLoader.ts / 两个 .test.ts / DashboardViewV2.test.ts ✅（另 web/public/menu-config.json、version.json 有改动，不在本包范围） |

---

## 一、重点审计项逐项结论

### 1. 三类缺陷是否真被修掉

#### 1a. 并发覆盖（bodyless unified 覆盖完整 body）——抽屉内 ✅ 已根治；组合式函数层存在一个"同 seq 并行"例外（见 P2-2）

抽屉内所有 `ensureBodies` 触发点全部串行化，逐路径核实：

- **props.requestId watcher**（UnifiedRequestSessionDrawer.vue:117-130）：`await loadMeta(id)` → `id !== activeRequestId.value` 过期丢弃 → `loadActiveTabBodies()`。loadMeta 在途时模板走 `v-if="loading"` 全面板 loading 分支（模板 :228），tab 行/会话面板均不可见不可点 → 期间无法触发第二个 ensureBodies。
- **tab watcher**（:133-135）：loadMeta 在途时用户点不了 tab；watcher 复位 tab 只会落到 `overview`/`flow`（`tabNeedsBodies` 均为 false）→ 复位引发的 tab-watcher 触发是安全 no-op（复位发生在 `await loadMeta` 之前，此时 activeRequestId 仍是旧 id，但因 no-op 不产生 fetch）。
- **onSelectTurn**（:139-148）/ **openAsRequest**（:150-158）：同 watcher 串行链 + 过期丢弃；会话面板同样被 loading 分支遮挡。
- **缓存命中路径**（useRequestDetailLoader.ts:275-285）：每次 loadMeta（含命中）`bumpSeq()`（:277），`applyEntry` 同步整体覆盖 log/unified/sessionSnap——ensureBodies 成功后 `cachePut(requestId, { ..., bodiesLoaded: true })`（:384）以 per-request 键写回带 body 条目，同 id 重入恢复的是带 body 条目。✅
- **abort/错误路径**：ensureBodies 单方端点失败（catch → null）不写状态、不置 bodiesLoaded（:382-385 有 `merged || fullUnified` 守卫），可重拉；stale fetch 被 `seq !== loadSeq.value || requestId !== activeRequestId.value`（:376）双保险丢弃。✅

**例外（组合式函数层）**：当 loadMeta 的非缓存 fetch 与同 id 的 ensureBodies 以**同一 seq** 并行时（ensureBodies 在 loadMeta 的 Promise.all 之后、seq 未再 bump 的窗口内启动），若 body fetch 先落地，loadMeta 落地处（:307-315）无 bodiesLoaded 检查，bodyless `unified.value = u` 会覆盖完整 body，且 `cachePut(..., bodiesLoaded: false)`（:315）击穿带 body 的缓存条目。修复报告"bodyless 投影在任何顺序下都不再能覆盖完整 body"的表述过强。**抽屉消费方不可达（如上，已串行化）；但既有消费方 RequestDetailFullscreenView.vue 可达**——详见 P2-2。属未改文件的预存在竞态，非本包回归，但与本包宣称的不变量冲突。

#### 1b. 换轮残留——✅ 基本根治；发现一处新变体残留（P2-1：stale warnings push）

对组件模板消费的全部响应式状态逐一核对 `resetTransientState`（useRequestDetailLoader.ts:233-253）覆盖：

| 状态 | 换轮后归属 | 核对结果 |
|---|---|---|
| journey / waterfall / waterfallSource / waterfallError / bodiesLoaded / bodiesLoading / waterfallLoading | resetTransientState 显式清 | ✅ |
| metaError / metaWarnings（本次新增清这两项） | resetTransientState 显式清 | ✅（但见 P2-1 时序漏洞） |
| log / unified / sessionSnap | 非缓存路径显式置 null（:288-290）；缓存路径 `applyEntry` 同步整体覆盖（:255-270），无闪窗 | ✅ |
| metaLoading | loadMeta 管理：非缓存置 true，finally `seq === loadSeq` 守卫复位（:322）；stale finally 不碰 | ✅ |
| activeRequestId | loadMeta 第一行同步设置（:273），无空窗（watcher 同 tick 内完成） | ✅ |
| requestBody/responseBody/outboundBody/sessionId/hasRouting/attempts | 均为 log/unified/journey/waterfall 的 computed，随上游清态 | ✅ |
| viewMode / tab | UI 状态，props watcher 复位（与迁移前一致）；onSelectTurn 不复位属有意设计（模板未动行） | ✅ |

未发现模板绑定的 ref/computed 缺少来源或漏清项。**唯一漏洞**：loadMeta 两个端点 `.catch` 里的 `metaWarnings.value.push(...)`（:297-304）发生在 seq 守卫（:307）**之前**——A 请求在途时切到 B，B 的 loadMeta 已清空 warnings，A 的端点稍后失败会把 `admin/request-detail: HTTP 500 …` 推进 **B 的告警列表**且 B 成功后仍显示。这正是本包要根治的"跨请求残留"在新代码中的新变体，详见 P2-1。快照路径无此问题（ensureSessionSnap catch 有 seq 守卫 :346；stale abort 由 `signal?.aborted` + bumpSeq abort 双守卫，不产生误告警）。

#### 1c. bodiesLoading 粘滞——✅ 已根治

- 切请求瞬间：`loadMeta` 无条件调用 `resetTransientState()` → `bodiesLoading.value = false`（:246），同步生效于任何 await 之前。
- 晚到响应：stale ensureBodies 的 finally `if (seq === loadSeq.value)`（:389）失配 → 既不写状态也不碰 loading；写点同样被 :376 双守卫。任何时序下晚到 fetch 无法把 loading 复位为 true 或复活（ensureBodies 只在启动时同步置 true 一次，且启动前提是通过 `requestId !== activeRequestId.value → return` 活性检查 :352）。✅

### 2. 迁移完整性

- 组件内自管加载状态全部删除：`loading/error/log/unified/sessionSnap/activeRequestId/warnings/loadSeq/snapshotAbortController/bodiesLoadedFor/loadRequest/ensureBodies/recordEndpointFailure` 在 diff 中确认清除，无旧 watcher 与新链路并行的双写点（旧 `watch([tab, activeRequestId])` 已改为 `watch(tab)` 单源）。
- 模板未动一行（diff 核实仅 script 变更）；模板绑定的每个 ref/computed 在解构/computed 中都有来源（逐一核对见上表）。
- 对外契约不变：props（requestId/mode/initialTraceOpen/stackLevel/initialViewMode）与 emits（close/filterSession/openRequest/sessionTitleChanged）声明逐字保留；`emit('openRequest')` 时序不变（异步启动后同步 emit）。父组件核实：DashboardViewV2（`openDrawerRequestFullscreen` → `openRequestDetailPage`，不动抽屉 prop）、RequestLogsView（`onDrawerOpenRequest` → `showDetail` → prop 变更 → 与 openAsRequest 内 loadMeta 并行的第二次 loadMeta——缓存命中或 seq 丢弃，无竞态危害，与迁移前双载行为一致）。`RequestLogDrawer.vue` 兼容壳未动。
- 卸载：新增 `onUnmounted(() => dispose())`（:137），abort 快照/瀑布（body fetch 不接 signal，见 P3-5）。
- `activeRequestId` 类型由 `Ref<string|null>` 变为 `Ref<string>`（初值 `''`）：模板 `v-if="activeRequestId"` 等价；`SessionTurnsSyncPane` prop 声明 `string | null`（SessionTurnsSyncPane.vue:37），`''` 走其 `if (activeId)` 假分支，行为与 null 等价 ✅。

### 3. 其他消费方零回归

`grep useRequestDetailLoader` 既有消费方全集：

1. **RequestDetailFullscreenView.vue**（未改动）：不消费 `metaWarnings`（解构清单 :32-36 无此项）→ ensureSessionSnap/端点失败新增告警对其无 UI 变化 ✅；不消费 `activeRequestId` ✅。**resetTransientState 新清 metaError 对其无影响**：其错误分支 `v-else-if="metaError && !log && !unified"`（:212）在新请求加载成功后本就被 `!log && !unified` 条件遮蔽，清早清晚不可见差异；不存在"依赖上一请求 metaError 存活"的路径 ✅。404 时直显哨兵中文串 `'请求详情未找到'` 与迁移前逐字一致 ✅。
2. **RequestDetailSectionHost.vue**：仅 `import type { DetailSection }`（:5），纯类型，零影响 ✅。
3. **RequestLogDrawer.vue** 兼容壳：纯透传，未动 ✅。

新哨兵常量 `REQUEST_DETAIL_NOT_FOUND`：新导出 + `loadMeta` 内替换同一字面量，非行为变更 ✅。

### 4. 测试质量——6 条回归均为"会失败于回归代码"，非恒真

逐条推演（对照旧实现与新实现的可能回归形态）：

| 用例 | 钉住的缺陷 | 回归时会失败的断言 |
|---|---|---|
| composable#1 concurrent override…must not clobber | loadMeta 重入覆盖完整 body | 若缓存命中恢复 bodyless 条目 → `requestBody` toEqual 失败 |
| composable#2 in-flight body fetch…discarded | seq bump 后 stale body 写入 | 若丢守卫 → `requestBody` 变 `{stale:true}`/`bodiesLoaded` true，失败 |
| composable#3 switch residue…previous bodies | 残留守卫 early-return | 若 ensureBodies 在 B 视图看到 A 残留 body 而跳过 → `requestBody` toEqual `{owner:'req-b'}` 失败 |
| composable#4 sticky loading | 切换不复位/晚到复活 | 若 reset 漏 bodiesLoading → 切换瞬间 `false` 断言失败；若 stale 写入 → 后两条失败 |
| drawer#5 turn-switch residue（经兼容壳挂真实抽屉 + 真实 SessionTurnsSyncPane emit 链） | A 轮正文串 B 轮 | 旧实现下该序列会触发残留守卫（select-request 同步改 activeRequestId → 旧 watch 立即 ensureBodies，此时 A 的 body 尚在）→ body 不等于 B_BODY，失败 |
| drawer#6 loading indicator clears…not sticky | bodiesLoading 粘滞 | 旧实现下 B 的 body fetch 根本不发生（残留守卫），挂起期 `.drawer-loading` 存在断言与最终 B_BODY 断言均失败 |

两条组件级用例走真实 `RequestLogDrawer → UnifiedRequestSessionDrawer → SessionTurnsSyncPane` emit 链（非直接调方法），链路保真度好；`clearRequestDetailCache()` + `mockReset` 的 beforeEach 隔离正确。**缺口**：无任何用例覆盖 P2-1（stale warnings push）与 P2-2（同 seq 并行覆盖）——两者恰是本次审计新发现的残留类缺陷。

### 5. 边界

- **组件卸载**：`onUnmounted(() => dispose())` abort 快照/瀑布；body fetch 不接 signal（P3-5），卸载后晚到 body 响应仍会写 refs（组件已卸载无 UI 危害，仅网络浪费）。
- **requestId 快速连续切换**：A→B→A（45s 内）路径核实：每次 loadMeta 必 bumpSeq；A 的缓存命中 applyEntry；B 的在途响应 seq 失配整体丢弃；B watcher 恢复后被 `id !== activeRequestId` 丢弃。✅ A→B→null（关闭）：null 早退不清态，但模板 `v-if="requestId"` 全遮蔽，重开时 loadMeta 清态。✅
- **404 路径**：双端点空 → 哨兵 → 抽屉映射 `t('requestDetail.drawer.notFound')`（8 语言 locale 均有 `requestDetail.drawer.notFound`，各 requestDetail.ts:53 实核）；全面板错误态、loading 已复位、不写缓存（重复打开重拉，无负缓存）。既有观感问题：双 5xx 也归化为"未找到"且 warnings 被全面板错误分支遮蔽（P3-6，迁移前即如此，非本包回归）。

### 6. CJK 棘轮

亲测 6836 = baseline 6836 ✅。本包非注释行唯一含中文的是 `REQUEST_DETAIL_NOT_FOUND = '请求详情未找到'`（useRequestDetailLoader.ts:33），该字面量在 HEAD 同文件 :315 已存在（`metaError.value = '请求详情未找到'`），收编为常量计数中性；抽屉新增中文全在 `//` 注释（计数器跳过纯注释行，hardcodedCjk.ts:65,72）。报告声明"6836 持平"属实。

---

## 二、发现清单

### P2-1 loadMeta 端点失败的 metaWarnings.push 无 seq 守卫——跨请求告警残留（本包欲根治缺陷的新变体）
- 证据：`web/src/composables/useRequestDetailLoader.ts:297-304`（两处 `metaWarnings.value.push(formatEndpointFailure(...))` 位于 catch 内，先于 :307 的 `if (seq !== loadSeq.value) return` 守卫执行）。
- 时序：loadMeta(A) 在途 → 切 B → loadMeta(B) 的 resetTransientState 已清空 warnings → A 的端点稍后失败 → 告警写入 B 的列表；A 的 Promise.all 随后 `return`（stale 丢弃）但 push 已发生，B 成功加载后仍显示 A 的失败告警。`metaError`（:320 外层 catch）与 ensureSessionSnap catch（:346）都有 seq 守卫，唯独这两处没有。
- 影响：排障误导（把 A 的端点故障记到 B 头上）；与修复目标"换轮后 warnings 不残留"直接冲突。测试未覆盖。
- 建议：catch 闭包内先比对 seq——`getUnifiedRequestDetail(...).catch((e) => { if (seq === loadSeq.value) metaWarnings.value.push(...); return null })`（`seq` 在 Promise.all 之前已捕获，闭包可见）；并补一条"切请求后 stale 端点失败不进新 warnings"的回归用例。

### P2-2 组合式函数层"同 seq 并行"下 bodyless meta 仍可覆盖完整 body 并击穿缓存——F2-#5 修复的"任何顺序"断言不成立（抽屉不可达；RequestDetailFullscreenView 可达，预存在）
- 证据：`web/src/composables/useRequestDetailLoader.ts:307-315`——loadMeta 非缓存落地仅检查 seq，不检查 `bodiesLoaded`；若同 id 的 ensureBodies（:364-385，捕获同一 seq）先落地（写完整 unified + `cachePut(bodiesLoaded:true)`），随后 loadMeta 落地 `unified.value = u`（bodyless）+ `cachePut(..., bodiesLoaded: false)`（:315）→ 视图 body 清空、45s 缓存条目被 bodyless 版本覆盖。
- 可达路径（未改文件，预存在竞态）：`RequestDetailFullscreenView.vue:85-92` openAsRequest 一次 router.replace 同时改 requestId 与 mode → :54-62 requestId watcher 同步段启动 loadMeta（fetch 在途）→ :68-72 viewMode watcher 在同一 flush 内触发 `onSectionNeed(id, section)` → ensureBodies 与 loadMeta 同 seq 并行 → 响应到达顺序决定是否覆盖。抽屉侧因全链路 `await loadMeta` 串行化 + loading 全面板遮挡而不可达（这正是迁移的成果）。
- 影响：fullscreen 下偶发"open as request 后 chat/overview 短暂无正文"（下一次 ensureBodies 会自愈，因 bodiesLoaded 被打回 false 可重拉）；属 UI 闪烁 + 双请求，非持久数据错误。
- 建议：loadMeta 落地处加保护——`if (seq !== loadSeq.value) return; if (bodiesLoaded.value && !u?.bodies && !meta?.request_body) { /* 已有完整 body：跳过 unified 覆盖或仅更新 metadata 字段 */ }`；或 ensureBodies 做 per-requestId 在途去重（见 P3-4），从根上消掉并行窗口。

### P3-3 loadMeta 的 Promise.all 现包含 getRequestJourney——抽屉首屏（loading 态）被 journey 冷路径延迟；修复文档"fire-and-forget"表述不准确
- 证据：`web/src/composables/useRequestDetailLoader.ts:296-306`——`getRequestJourney(requestId)` 与两个 omitBody 端点同入 `Promise.all`，loadMeta（含 drawer 的 `v-if="loading"` 全面板 loading）要等三者中最慢者。抽屉迁移前（HEAD UnifiedRequestSessionDrawer）只等两个端点；fullscreens 侧本就如此（非回归）。
- 影响：点开抽屉的首屏延迟新增 journey RTT（列存/冷行时更明显）；失败已静默（`.catch(() => null)`），仅延迟问题。
- 建议：journey 移出 Promise.all，改为 `getRequestJourney(id).then(j => { if (seq === loadSeq.value) journey.value = j }).catch(() => null)` 的旁路 fire-and-forget；或在文档中如实标注首屏代价。

### P3-4 ensureBodies 无在途去重——同一 requestId 的全量 body 双请求/双写；并发下 bodiesLoading 提前熄灭（观感）
- 证据：`web/src/composables/useRequestDetailLoader.ts:351-391`——无 in-flight Map；可重入路径：SessionTurnsSyncPane 对同一轮次快速双发 `select-request`（drawer onSelectTurn 两次并发）；RequestLogsView `openAsRequest`（内部 loadMeta+loadActiveTabBodies）与 prop 回流触发的 watcher 链重叠。两次并发 ensureBodies 捕获同一 seq，各自 fetch、先后写同值；先完成的 finally（:389）会把 bodiesLoading 置 false，而第二个 fetch 仍在途——loading 指示提前消失。
- 影响：重复网络请求（列存冷路径加倍）；loading 指示短暂失真。无数据正确性问题。
- 建议：composable 内 `const bodyInflight = new Map<string, Promise<void>>()` 去重复用。

### P3-5 ensureBodies 的两个 body fetch 不接 AbortSignal——dispose/切换无法取消在途 body 请求
- 证据：`web/src/composables/useRequestDetailLoader.ts:372-375`（`getRequestLogDetail(requestId).catch(() => null)` / `getUnifiedRequestDetail(requestId).catch(() => null)` 无 `{ signal }`），对比 ensureWaterfall :406 有 `signal: abort?.signal`。`onUnmounted → dispose()`（drawer :137）只能取消快照/瀑布。
- 影响：仅网络资源浪费；正确性由 seq/requestId 守卫保证。迁移前组件版同样不接 signal（非回归）。
- 建议：`ensureBodies` 透传 `abort?.signal`（fetch 层支持时）。

### P3-6（观察，非本包回归）双端点同时失败（含双双 5xx）归化为"未找到"，且此时 warnings 被全面板错误分支遮蔽
- 证据：`web/src/composables/useRequestDetailLoader.ts:311-314`（`!u && !meta` → 哨兵，不区分 404 与 5xx）+ 抽屉模板 :228-237（`v-else-if="error && !log && !unified"` 全面板分支不含 warnings 列表）。HEAD 行为逐字相同；本包新增的 warnings 采集在该路径下不可见，P2-1 修复时值得一并处理（如双失败且 warnings 非空时展示失败明细而非"未找到"）。

### P3-7（观察，非本包引入）死契约随迁移原样保留
- `sessionTitleChanged` emit 在新旧两版均声明从未 emit（HEAD :55 vs 当前 :57；RequestLogDrawer.vue:25/39 与 RequestLogsView `syncSessionTitle` 挂空）；`props.mode` 声明未消费。与本包无关，建议另立清理项。

---

## 三、结论

F2-#5 三类缺陷在**抽屉消费路径**上已真实根治（逐路径推演通过，模板/契约/父组件零感知，迁移完整无双写残留）；测试、类型、CJK 门禁全部亲测通过，6 条回归用例均具判别力。但发现两个 P2：
1. **P2-1** 是本次新代码引入的跨请求告警残留时序漏洞（stale warnings push 缺 seq 守卫）——与本包要根治的缺陷同类，建议本包内顺手修复（一行守卫 + 一条用例）；
2. **P2-2** 是组合式函数层并发覆盖不变量的遗留缺口（fullscreen 同 seq 并行可达，预存在）——修复宣称的"任何顺序不覆盖"应收窄为"抽屉串行链内不覆盖"，或按建议补 loadMeta 落地保护。

P0 = 0，P1 = 0，P2 = 2（P2-1、P2-2），P3 = 5（P3-3～P3-7）。
