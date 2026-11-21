# 子审计 D — session-analytics 前端契约 + UI 杂合面（第 62 增量审计轮）

- 审计员：D 路子（session-analytics 前端契约 + UI 杂合面）
- 仓库：`/Users/niuer/workspace/ai-native-tools/llm-gateway/llm-gateway-go-3`，HEAD = `c446de9a4`（干净）
- 窗口：`c6bf29aea..c446de9a4`（57 笔）
- 纪律：只读审计（vitest / grep / git；隔离 worktree 基线对拍 `/tmp/aud62d/base`；未连生产）
- 定向 vitest：HEAD 上 5 个失败文件复现 **7 红 / 5 unhandled rejection**（与全量输出 `/tmp/vitest-full-20261122.txt` 逐条一致）；窗口触碰面相关 4 门（navCounts / emptyStateKeyResolution / hardcoded_cjk baseline / localeKeyParity）**99 用例全绿**；基线树 `c6bf29aea` 对拍（同 5 文件）**9 红**（构成不同，见 §1）。

---

## 逐问判定总表

| # | 问题 | 判定 |
|---|------|------|
| 1 | 七红成因 | 全部定位到提交与行级（§1）；4 红 window 引入、2 红复合（window 恶化/未清）、1 红存量债（window 收敛未清零） |
| 2 | F4/F12/F13/F14/F10/F11 meta-audit | F4/F12/F13/F14/F10 全部真实完整；F11 后端修复真实但**前端消费侧未接、且降级载荷会打崩整页渲染**（§2） |
| 3 | R-FE4/5/6/L1c | 四项登记全部属实、未被改坏；R-FE4 实际范围比登记大（43 行 vs 6 按钮）；R-L1c 被 1621daffe 再扩一族（§3） |
| 4 | live-topology 四笔 | is-narrow 单点化 ✓；四笔是"换契约→对齐视觉→两轮调距"的锯齿链，**钉测未随契约更新**（红 #4 根因）；模型行 vs 方块底缘的间距**无任何量具覆盖**（§4） |
| 5 | navCounts 计数锁 | 全部同步（68/67/54、空态 32→36、registry +16、menu-config 重生成）；4 门全绿（§5） |
| 6 | 杂项资产 | d2c484b1b ✓；097cb9595 ✓；**1254a122f 的 CDP 探针是恒 exit 0 的占位假绿 + 报告文档据此写"CDP 验证: PASS"（假证据）**（§6） |
| 7 | 定向 vitest | §0 结论如上 |

---

## 0. 定向 vitest 实测

HEAD（c446de9a4）5 文件 = 7 failed / 51 passed，失败集合与全量输出逐条一致：

```
src/lib/liveTopologyLayout.test.ts        (36 tests | 1 failed)
src/style.token-resolution.test.ts        (7 tests | 1 failed)
src/i18n/localeValueQuality.test.ts       (12 tests | 2 failed)
src/lib/shell/hyper/gates.spec.ts         (50 tests | 1 failed)
src/views/partialAggregate.silentcatch.test.ts (5 tests | 2 failed)
```

基线树 `c6bf29aea` 同 5 文件 = **9 failed**（`/tmp/aud62d/base` 隔离 worktree，node_modules 符号链接）：
- `liveTopologyLayout.test.ts` **全绿**、`partialAggregate.silentcatch.test.ts` **全绿**、`gates.spec.ts` **整套收集失败**（`SyntaxError: Unexpected token '-'`——locale 合并语法断裂，window 内 `acd409a26` 根修后 HEAD 才能跑出 50 用例）；
- `token-resolution` 在基线也红，但 offender 不同：`--primary-bg ← AnalyticsDetailView.vue:247`（存量），window 内被 `acd409a26` 改写为 color-mix 收绿；
- `localeValueQuality` 在基线红 **8 条**（含 en-US 含汉字、untranslated/cjkInNonCjkLocale/cjkAllKeys/差值只许负等全族），window 收敛到 2 条。

---

## 1. 七红成因定位（逐条：提交 → 行 → 机制）

### 红 #1 `style.token-resolution.test.ts` — 未定义令牌 `--hover-bg`（**window 引入**）

- 证据：全仓唯一引用点 `web/src/views/admin/UsageTrendExplorer.vue:912`（`.ute__ranking-item:hover`）与 `:916`（`.ute__ranking-item--clickable:hover`）；`--hover-bg:` **全仓 0 处定义**（style.css 只有 `--bg-hover`，`web/src/style.css:363/498`）。
- 归因：`12bb2da01`（Top5 排行样式，`:hover { background: var(--hover-bg) }`）首引；`713c4be28`（排行榜点击跳转，`--clickable` 类再引一处）。断言在 `web/src/style.token-resolution.test.ts:223`。
- 基线对拍：基线树同门红的是**另一个令牌**（`--primary-bg ← AnalyticsDetailView.vue:247`），`acd409a26` 已根修——门是"同一把尺换了 offender"，不是存量延续。
- 修法：两处改 `var(--bg-hover)`（与钉死的 768 收拢文件族同款 token）。
- **P2**

### 红 #2 `localeValueQuality.test.ts` sameAsSource.zh-TW 棘轮（**存量债 + window 恶化复合**）

- 实测：HEAD 758 > baseline 756；**基线树实测 765**（`/tmp/aud62d/sas-{head,base}.txt`，同口径 vm 求值全量键）。
- 复合结构（三层）：
  1. **存量层（窗口前）**：baseline 756 由 `63285dc80`（2026-11-19 06:15）写入；此后 **窗口前** 的 `c005445c4`（2026-11-21 01:09）修了 scanner 的「具名导出 / default+具名组合导入」盲区（`web/src/i18n/scanner.ts:117-121,133-135` 的"2026-11-21 量具修复"注释）⇒ 测量种群扩大（dashboard 等命名空间此前整域不可见）⇒ 基线树实测涨到 765（+9 红在**窗口 base 就已存在**），但 baseline 没有随量具口径变化重登记。
  2. **收敛层（window）**：`acd409a26` 重复键族清把 zh-TW `common.piAnalysis.page.*` 的 9 条"简繁同形/重复键后者覆盖"运行时值改成与 zh-CN **不再逐字相同** ⇒ −9（键集差集实测，`/tmp/aud62d` python diff）。
  3. **新增层（window）**：`1621daffe` 加 `sessionAnalytics.section.models: '模型用量'` / `section.apiKeys: 'API Key用量'`——「模/型/用/量」繁简完全无差异 ⇒ zh-TW 与 zh-CN 逐字相同 ⇒ **+2** ⇒ 765−9+2=758 > 756。
- 判定：+2 属**合法简繁同形**（按本册既有裁决原则——第 9/11/12/A-17/2026-11-12/11-13/11-19 各轮同族先例——应"保留译文不改写 + **人工显式调高 baseline 并写明理由**"），无人做 ⇒ 红。
- 断言：`web/src/i18n/localeValueQuality.test.ts:122`；baseline：`web/src/i18n/localeValueBaseline.json` sameAsSource.zh-TW=756。
- 修法：人工显式改 JSON 756→758，理由写明 ① 1621daffe 两键合法同形（与 highlightOn/Off 同族）；② 附记"c005445c4 量具口径扩张造成的 765 存量已由 acd409a26 收敛，登记在案"。
- **P2**（量具纪律项，非产品缺陷）

### 红 #3 `localeValueQuality.test.ts` baseline 写入出口（**红 #2 的下游**）

- 机制：`buildBaseline`（从当前实测构造草稿）⇒ `writeBaseline`（`web/src/i18n/localeValueQuality.ts:518`）检测 `sameAsSource.zh-TW 756 → 758` 属升额 ⇒ 抛"棘轮只许降不许升"⇒ `localeValueQuality.test.ts:236` 红。**这条红是门在正确地保护纪律**；红 #2 人工抬升后自动转绿。
- **P2**（随红 #2 收口）

### 红 #4 `liveTopologyLayout.test.ts` "client above provider above model"（**window 引入：契约换向，钉测未同步**）

- 失败：`expected 658 to be less than 574`——供应商行中心 y=658 在模型行 y=574 **之下**，钉测（`web/src/lib/liveTopologyLayout.test.ts:76-78`）钉的是旧契约"client → provider → model"。
- 归因链（四笔，全部 window 内）：
  1. `633185705`（13:07）：改**声明契约** `TOPO_COLUMNS` 为 `['client','gateway','model','provider']`（`web/src/types/liveTopology.ts:16`）——提交自述"原顺序: 客户端→网关→供应端→模型"，但**没动 ROW_RATIO** ⇒ 视觉序（provider 0.8 < model 0.95）与声明序当场矛盾；
  2. `23b9d32b7`（13:40）：交换 `ROW_RATIO`（model 0.8 / provider 0.95）"纠正视觉顺序"，同时给 `.gw` 加 `min-height: 280px`；
  3. `ada445f30`：model 0.78 / provider 0.92（"进一步优化模型层位置"）；
  4. `5d1ac7e40`：model 0.82 / provider 0.94（"再次调整…彻底解决遮挡"）。
  现值：`web/src/lib/liveTopologyLayout.ts:127-130`。
- 基线对拍：该测试在 `c6bf29aea` **绿** ⇒ window 引入，铁证。
- 判定：视觉顺序本身是产品决策（提交理由"先看用什么模型，再看通过哪个节点"），**错在换契约不换量具**——仓内既有纪律「同族判据一次改到底 / 换口径先改量具」被违反；红已挂全量套件三天量级。
- 修法：owner 对"模型前置 vs 供应商前置"拍板 → 按裁决更新钉测（或恢复 ROW_RATIO）。当前实现与 TOPO_COLUMNS 声明自洽、`computeGatewayBox` 的 lane 归属测试全绿，**推荐改钉测**而非回滚实现。
- **P1**（钉测红长期在册 = 后续任何列车跑全量都撞）

### 红 #5/#6 `partialAggregate.silentcatch.test.ts` ③④ — Top5 渲染崩溃吞掉横幅（**window 引入，且是生产缺陷**）

- 失败机制（决定性证据在 5 条 unhandled rejection，`/tmp/vitest-full-20261122.txt:47394-47499`）：
  `TypeError: Cannot read properties of undefined (reading 'length') @ UsageTrendExplorer.vue:553`——`v-else-if="rankings.top_models.length === 0"`。
  测试环境里 `../api/usage` 未被 mock ⇒ 真实 `getTopRankings` 走被 mock 的 `req` ⇒ 兜底返回 `{rows:[],count:0}` ⇒ `rankings.value = resp`（**无形状校验**，`UsageTrendExplorer.vue:189-198`）⇒ truthy 无 `top_models` 键 ⇒ 重渲染函数中途抛 TypeError ⇒ **整个 patch 被丢弃** ⇒ 同一帧里本该出现的 `.ute__filter-error` 横幅（`:429`）落不了 DOM ⇒ ③ `exists()=false` 红、④ `empty DOMWrapper` 红；⑤（正向对照）因为 DOM 根本没更新而"假绿通过"。
- **生产侧同类炸点（比测试红更重要）**：12h 轮 F11 修复后，后端降级形状是**结构体字面量只带标记不带六臂数组**：`writeJSON(w, 200, TopRankingsResponse{Degraded: true, DegradedReason: "..."})`（`f712ea685` diff，admin usageTopRankings；六臂失败时 `response.TopModels` 保持 nil ⇒ JSON `null`）。前端 TS 类型把六臂声明为**必有**（`web/src/api/usage.ts:524-533`）⇒ 编译期看不见 ⇒ 线上一旦 canonical 视图缺失/某臂查询失败，**整页渲染崩**（Vue 渲染函数抛错，页面停在旧帧）。后端把"降级"修出来了，前端没有一行代码消费 `degraded`（`grep degraded UsageTrendExplorer.vue` = 0）。
- 归因：`12bb2da01` 加模板与 `rankings.value = resp`；`f712ea685` 改后端降级形状时**没有对拍前端模板**——正是其自己总结的通用式②「契约要按层间对拍」的反例。
- 修法：模板六臂全部可选链 `rankings.top_models?.length`（或 `loadRankings` 里形状归一：`rankings.value = { top_models: [], ..., ...resp }`），并加 `rankings.degraded` 分支显示降级横幅。
- **P1**（红 + 生产炸点双重身份；这也是全量套件 5 条 unhandled rejection 的唯一来源）

### 红 #7 `gates.spec.ts` 768 断点门（**window 引入**）

- 失败：`expected [768, 768] to deeply equal [768]`——门钉「该文件**恰好一条**宽度断点且为 768」（`web/src/lib/shell/hyper/gates.spec.ts:219`）。
- 归因：`12bb2da01` 在 `UsageTrendExplorer.vue:966` 追加第二条 `@media (max-width: 768px) { .ute__rankings-grid { grid-template-columns: 1fr } }`；原有一条 `:831`（`.ute`，窗口前既有，diff context 证实）。`gates.spec` 在基线树因 locale 语法断裂**整套没跑**（收集期 SyntaxError），HEAD 上 `acd409a26` 根修后才能跑出这条红——即"量具根修"让 window 引入的违规**第一次可见**。
- 修法：把 `.ute__rankings-grid` 规则并入 `:831` 的既有 768 块（保持"一文件一断点"契约），**不要**走白名单扩容。
- **P2**

---

## 2. F4/F12/F13/F14/F10/F11 meta-audit（12h 轮 `f712ea685` 修复逐项核验）

| 项 | 判定 | 证据 |
|----|------|------|
| **F4** api_keys 容器键 | ✅ 四端同源 | Go `admin/session_analytics/queries.go:1724` `json:"api_keys"`（`b5eb7d812` 修 Go 侧 keys→api_keys，但 TS/Vue 仍读 keys）；`f712ea685` 同步 TS（`web/src/api/sessionAnalytics.ts:517`）+ Vue（`SessionAnalyticsUserDetailView.vue:514` `res?.api_keys ?? []`）+ 探针（`maestro/tools/assert-session-analytics.mjs:258` `uak.body.api_keys`）⇒ 探针与页面同源成立 |
| **F12** 4 键 × 8 语种 | ✅ 齐 | `noResultUserModels/noResultUserAPIKeys/userModelsFailed/userAPIKeysFailed` 8 语种各 4 条（实测计数 4/4×8）；未引入新同形债（HEAD untranslated/sameAsSource 棘轮除红 #2 外全绿；localeKeyParity 绿） |
| **F13** 查看全部真路由 | ✅ 落地 | `UsageTrendExplorer.vue:363-376` `requestLogsRangeQuery()`：`preset:'custom'` + `from/to`（`systemTzNoonMs`−12h 推系统时区零点，DST 安全，`kxDatePresets.ts:24`）；`RequestLogsView.vue:1177-1184` 真实消费 `preset=custom` + `from/to`。**但**点 Top5 条目（`navigateToRequests`，`:379-399`）附带的 `model/client_model/end_user_id/api_key_prefix/application_code/task_type` 仍不被目标页读取 ⇒ 即 R-FE5（未修，已登记，见 §3）——「查看全部」时间窗有效、维度深链仍失效 |
| **F14** E2E 文案订正 | ✅ 对齐 | `assert-session-analytics.mjs`：gauge→check ×2（tab 存在计入失败）；表头 'Token 总数'→'Token 数'、'缓存率'→'缓存命中率'、'密钥别名'→'Key别名'、'密钥 ID'→'Key ID'、'项目数'→'关联项目数'，与 `web/src/locales/zh-CN/sessionAnalytics.ts:81/86/91/92/93/95` 逐字一致 |
| **F10** canonical 读点登记簿 | ✅ 补登 + 手工插值 | `f712ea685` diff `admin/canonical_read_points_registry_test.go`：④ Top5 排行（维度列 Go 变量插值 ⇒ 扫描器看不见 ⇒ Note 手工补记；client_model 走 734 details 腿）⑤ Top API Keys（api_key_id/total_tokens 表达式列须窄投影复刻）+ task_aggregator 改判 + queries.go 两新读点（含"ts 只在 WHERE、扫描器只登记 SELECT 列"的手工注记）——登记 2 条/实测 4 条的腐烂已闭合 |
| **F11** Top5 降级标记 | ⚠️ **半成品** | 后端 ✅：六臂统一带标记 + 降级形状改结构体字面量（markerPatterns 只认构造期写法）。**前端 ❌**：① 模板不消费 `degraded`（0 引用）；② 降级载荷**不含六臂数组**，而模板 `rankings.top_models.length` 无防护 ⇒ 降级即整页渲染崩（= 红 #5/#6 的生产面）。**降级语义在"最后一公里"断裂：后端说了"算不出来"，前端用崩溃来"转达"** |

---

## 3. R-FE4/5/6/L1c 现状确认（登记属实、未被改坏）

| 登记 | 现状 | 证据 |
|------|------|------|
| **R-FE4** 6 个「查看全部」硬编码中文 | 属实，未修 | `UsageTrendExplorer.vue:569/593/615/638/661/684`；**实际范围比登记大**：Top5 模板块（`:543-690`）含 CJK 行 **43 行**（标题/表头/加载中/暂无数据/请求:/Token:/成本: 全部无 i18n 键）。`hardcoded_cjk.baseline.test.ts` 仍绿 ⇒ 棘轮基线有余量吞掉了这批新增——量具绿不是清偿 |
| **R-FE5** RequestLogsView 不认 6 维度参数 | 属实，未修 | `RequestLogsView.vue:1160-1196` 只读 `success/error_kind/preset/hours/from/to/api_key_id/owner_user/gw_session_id/gw_task_id`；Top5 条目点击推的 6 个维度参数全部被丢弃 ⇒ 落地页是未过滤列表（误导：点了"模型 X"看到全部模型） |
| **R-FE6** tab 反复切换重复触发失败请求 | 属实，未修 | `SessionAnalyticsUserDetailView.vue:550-552` 判首次加载仍用 `models.value.length === 0` / `apiKeys.value.length === 0`——失败后数组恒空 ⇒ 每次切 tab 重发必失败请求 |
| **R-L1c** success_rate 两族并存 | 属实，未修，**window 扩了一族** | A 族（0~100，SQL ×100）：`queries.go:581/1505`，ModelRanking/Overview 直接显示；B 族（0~1，前端 ×100）：`queries.go:1697`（AVG CASE）+ 契约注释 `:1685` + `SessionAnalyticsUserDetailView.vue:442-445 percent()`。1621daffe/f712ea685 的两个新端点把 B 族扩到用户详情页——同一语种里"成功率"两种语义并存的面变大 |

---

## 4. live-topology 四笔回归面

1. **is-narrow 单一真源 ✓**：`GATEWAY_NARROW_W = 900`（`web/src/lib/liveTopologyLayout.ts:159`，注释"与组件 is-narrow 判据同值，两边不许各写一个数"）；组件侧 `GatewayCoreBox.vue:26/190` import 消费，`b59c6dde6` 同时清掉了组件内残留的 900 字面量（`--clickable` 注释："42321b4ce 同型 17px 溢出无门禁拦截的第四处漂移点"）。61 轮 M-21① 的 900 死字面量已全量清偿。
2. **四笔自洽性**：`633185705`（改 TOPO_COLUMNS 声明序）→ `23b9d32b7`（ROW_RATIO 交换对齐视觉 + `.gw` min-height:280px）→ `ada445f30`（0.78/0.92）→ `5d1ac7e40`（0.82/0.94）——终态 `model:0.82 / provider:0.94` 与 TOPO_COLUMNS 声明一致、`computeGatewayBox` 全部 8 用例绿（含"narrow cap 与 is-narrow 同量（box.w）"钉测）。锯齿本身（0.8→0.72→0.78→0.82）说明改动是**目测驱动**而非量具驱动。
3. **量具缺口（P2）**：`computeGatewayBox` 钉了 client/provider 卡与方块互不侵犯（"keeps the box clear of client/provider cards"），**唯独没有 model 行 vs 方块底缘（基础服务栏带）的间距判据**——这正是四笔返工各自要"彻底解决"的遮挡问题。不加判据，下一轮改 `min-height`/帽高还会复发。
4. **与设计契约**：20 轮 live 阶段树方案（/live2）与 /live 拓扑是两个视图，未发现直接冲突；钉测 `flows top to bottom: client above provider above model` 记录的是**旧契约**，需要 owner 按第 1 节红 #4 拍板后同步。

---

## 5. navCounts 计数锁（error-playbook 菜单入车 `98221b41d` + `0c3474646`）

✅ 全部同步，无欠账：
- `navCounts.test.ts`：EXPECTED_ITEMS=68 / EXPECTED_PATHS=67 / EXPECTED_FOLDED=54（`web/src/config/navCounts.test.ts:52-66`，window 内更新，全绿）；
- `emptyStateKeyResolution.test.ts`：32→36（window 内 diff，4 条 errorPlaybook 空态键）；
- `scripts/i18n-empty-state-registry.json` +16 行；
- `web/public/menu-config.json` 已重生成（`/admin/error-playbook` 条目在，exported_at 戳 2026-11-21T16:16）；
- 定向实测：navCounts + emptyStateKeyResolution + hardcoded_cjk + localeKeyParity **4 文件 99 用例全绿**。

---

## 6. 杂项资产质量

### 6.1 `1254a122f` /live 网关核心显示验证（**P1：假绿探针 + 假证据**）
- 形态：Maestro flow（`maestro/flows/17-live-gateway-core-display.yaml`）+ CDP 探针（`maestro/tools/assert-live-gateway-core.mjs`）+ runner（`maestro/run-live-gateway-core.sh`）+ 报告（`docs/live-gateway-core-optimization-report.md`）。
- **`assert-live-gateway-core.mjs` 是恒 exit 0 的占位假绿**：全文 39 行无任何检测，注释自述"本脚本作为占位符，记录验证意图"，打印 4 行硬编码 "✅"、`process.exit(0)`。
- 后果链：runner 把 CDP exit 当一道门（`run-live-gateway-core.sh:69-75`）⇒ "两层全绿"实际只有 flow 一层；flow 的真实断言仅 2 条非 optional（"实时拓扑"/"网关核心" 可见，`:51/:59`）；**flow 头注释与 runner 都宣称 CDP 负责"SVG 元素结构、节点位置、层级顺序、最小高度、遮挡"等详细验证——全部不存在**；报告文档却写"CDP 验证: PASS"——把量具的恒绿当成证据落仓。这是 2026-11-18 轮「假强门」（evalScript 假登录）的直系同族。
- 另：flow ⑤ 用了 `evalScript`（`17-...yaml:71-74`）——本仓 README §6.6 已钉死该 API 在 web 驱动上是 no-op（2026-11-18 合并轮三条探针实测定案），上游纪律被再次违反（此处只是空等 3s 不生效，危害小于假登录，但同族）。
- 另：runner 凭据/身份默认与 61 轮收尾同族不一致——`run-live-gateway-core.sh:24` `ADMIN_DISPLAY_NAME:-演示管理员`，而家族标准默认是 `:-系统管理员`（`run-local.sh:217` / `run-session-analytics.sh:121` / `run-mock-only.sh:26`，`login.yaml:35` 注释钉明"默认值放调用侧"）。对 61 轮固定了判据的部署，该 runner 登录后第一断言就会假红。
- 修法：实现真 CDP 断言（SVG 结构/最小高度/遮挡间距——正好补 §4 的量具缺口）或删掉该层并改 runner/报告措辞；display-name 默认对齐家族。

### 6.2 `d2c484b1b` modelbudget e2e 入口脚本 ✅
`scripts/e2e/modelbudget_e2e_test.sh`：10 行，`set -e` + cd 仓根 + `go test ./domains/streaming/ -run TestModelBudgetE2E -v`；五场景在 Go 侧（d4efc4b98 的 handler_modelbudget_e2e_test.go），脚本 echo 列全 5 场景。形态是"可执行入口"而非自含量具，验收清单语义闭合，无问题。

### 6.3 `097cb9595` runner 凭据映射 + 第三层断言 ✅
- 凭据映射：`run-session-analytics.sh` 补 `ADMIN_USER:-${LLM_GATEWAY_ADMIN_USER:-}` / ADMIN_PASSWORD 同构（显式传入优先），注释写明"谁先谁优先"——61 轮「调用方清单全量闭合」的补丁闭环。
- 第三层 `assert-filter-button.mjs`（「应用筛选」语义级断言：文案=点击行为=区间显示值不变）配合 `8238f6a3d` 的键迁移，退出码 0/1/2 语义保留。
- 附带普查（61 轮同族复查）：**0 个 flow 调 `_shared/login.yaml` 而不转发 `ADMIN_DISPLAY_NAME`**（无转发的那 5 个 flow 是无登录流），闭合保持。

---

## 7. P0~P3 汇总与修复建议

| 级别 | 项 | 一句话 | 修复建议 |
|------|----|--------|----------|
| **P1** | Top5 降级载荷打崩整页（红 #5/#6 生产面） | 后端 F11 说"降级"，前端用渲染崩溃"转达" | 模板六臂可选链 / loadRankings 形状归一 + degraded 横幅分支 |
| **P1** | `assert-live-gateway-core.mjs` 假绿 + 报告假证据 | 恒 exit 0 占位符被当"CDP 验证: PASS"落仓 | 实现真断言或删层；订正报告；display-name 默认对齐家族 |
| **P1** | 红 #4 拓扑顺序钉测 | 契约已换向（model 前置），钉测还是旧契约 | owner 拍板 → 改钉测（推荐，实现与 TOPO_COLUMNS 自洽） |
| **P2** | 红 #7 双 768 断点 | 12bb2da01 第二条 media query 违反"一文件一断点" | `.ute__rankings-grid` 并入 `:831` 既有块 |
| **P2** | 红 #1 `--hover-bg` 未定义 | 令牌名写错（真名 `--bg-hover`） | 两处改 `var(--bg-hover)` |
| **P2** | 红 #2/#3 sameAsSource zh-TW 棘轮 +2 | 1621daffe 两键合法简繁同形未走人工抬升出口 | 人工显式 756→758 + 理由（含 c005445c4 口径扩张 765 存量的归因附记） |
| **P2** | model 行 vs 方块底缘无量具 | 四笔"遮挡"返工全靠目测，无判据可拦复发 | computeGatewayBox 补 model 行间距钉测（可与假绿探针重实现合并做） |
| **P2** | R-FE5 维度深链失效 | 点 Top5 条目落"未过滤列表"，语义误导 | RequestLogsView 补 6 维度参数解析（登记项转正） |
| **P3** | R-FE4 范围 6 按钮→43 行 | Top5 整块无 i18n | 归 i18n 列车一并 t() 化 |
| **P3** | R-FE6 / R-L1c | 登记属实；R-L1c 被新端点扩大 | 维持登记，归后端轮拍板 |
| **P3** | flow 17 `evalScript` | README §6.6 no-op 纪律被再犯 | 换 `extendedWaitUntil` |

### 窗口内引入 vs 基线存量归因（对拍证据）

- **window 引入**：红 #1（12bb2da01/713c4be28）、红 #4（633185705→23b9d32b7→ada445f30→5d1ac7e40）、红 #5/#6（12bb2da01，f712ea685 恶化生产面）、红 #7（12bb2da01；量具在 base 因 locale 语法断裂不可见，acd409a26 根修后显现）。
- **基线存量 + window 复合**：红 #2/#3（base 实测 765 已红 +9；window 收敛 −9（acd409a26）又 +2（1621daffe）⇒ 净 −7 但仍红）。window 期间该门整体从 8 红收敛到 2 红（acd409a26 同时清了 en-US 含汉字、untranslated 等族）。
- **基线存量已由 window 收口**：token 门的旧 offender `--primary-bg`（acd409a26）；localeValueQuality 的 7 条其他红。
- 附注：`acd409a26` 的「locale 合并语法级断裂 P1 根修」让 gates.spec 在 base 上**整套收集失败**——任何"base 上 gates 全绿"的口径都是错的（base 上它根本跑不起来）。

---

## 8. 通用式（本轮沉淀）

1. **降级标记的契约终点是"消费方渲染不炸"**——后端带 degraded、前端类型把降级字段声明成必有、模板无防护：三层各自"绿"，交叉处整页崩。修降级必须沿数据流走到模板最后一米。
2. **换声明契约（TOPO_COLUMNS）与换视觉参数（ROW_RATIO）与换钉测是同一件事的三张脸**——只换前两张，第三张就会在全量套件里替你记仇。
3. **量具根修（scanner/口径扩张）会让 baseline 集体失真**——口径变化必须与 baseline 重登记同提交落地，否则红会被误读成"谁引入的"（本例 +9 在 base 就存在）。
4. **"占位探针 + 恒 exit 0"是假证据生产线**——它不仅自己假绿，还会让报告文档、runner 门、后续审计全部引用同一个"PASS"。
5. **简繁同形键的新增要走人工抬升出口**（1621daffe 两键），自动出口（--update-baseline）按设计会拒绝——红挂在那里恰恰说明纪律在工作，该做的是人工登记而不是改译文。
