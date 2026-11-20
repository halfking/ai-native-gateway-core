# 子审计 D — 网关核心 C 方案 live-topology 分组瓦片 + 前端杂合面

> 审计窗口 `c4b5d00cb..c6bf29aea`（增量 61，提交簇 4/8）；HEAD=c6bf29aea；只读取证，未改任何代码。
> 三态判定：✅ 通过 / ⚠️ 有条件通过（P2/P3 缺陷登记） / ❌ 不通过（P1）。
> 「生产侧」结论均标注**待主控活体复核**。所有路径相对仓根；`web/`=PC 前端，`web-mobile/`=移动端。

## 0. 判定总表

| # | 审计对象 | 判定 | 核心结论 |
|---|---|---|---|
| 1 | 窄帽判据四连提交终态 | ⚠️ | 帽值已单点化（仅 liveTopologyLayout.ts）；但 is-narrow 的 900 在组件内是**死字面量**，`GATEWAY_NARROW_W` 导出零消费者，「单一真源」只兑现了一半（D-1，P2） |
| 2 | pageHelp 8 语种 | ⚠️ | 实测 88 篇 ×7 语种=616 文件；**ar-SA 0 篇**（回退 zh-CN 中文正文）；「8 语种齐全」头注与事实不符；全覆盖门禁**头注承诺、实现缺失**（D-2/D-3） |
| 3 | mobile-nav 89 席手风琴 | ✅/⚠️ | 89/6 守门数字 + seatEntries 锚订正到位；跨端一致性钉测是**手抄快照**非真源对拍（D-4，P3） |
| 4 | 设置页搜索 + 分组面板 | ✅ | 索引面=GET /api/admin/settings 全量 spec 注册表，新键自动入搜索；P3：搜索面被分类选中截断 |
| 5 | 运营看板详情分析前端 | ⚠️ | ECharts（package.json 已声明）；三态齐全；两处 P2：「完整列表」实为 pies **LIMIT 25 截断面**、非法 dimension 渲染抛错；ResizeObserver 重初始化路径漏 disconnect |
| 6 | admin-tools 211 认证配置 | ❌ | 工具代码无硬编码密钥；但 **211 生产新账户凭据写入仓内**：demoadmin(super_admin)/`Demo@admin123`、demo(tenant_admin)/`Demo@user123` + admin 密码再落两份 flow + 提交信息（D-8，P1） |
| 7 | vue-tsc/构建质量 | ✅/⚠️ | 主控已跑绿不重跑；新代码 10 处 `any`/`as any`（D-9，P3） |
| 8 | 门禁覆盖不到的质量问题 | ⚠️ | 见 D-5（图表硬编码中文/浏览器时区）、D-6（魔法数字）、D-7（a11y 小项） |

---

## 1. 题1：窄帽判据三轮返工后的终态

**演进链核实**（四提交 diff 全读）：
- `18f5e3c65`（C 方案落地）：帽 320→204，窄帽 240；判据当时用**画布宽** `width < GATEWAY_NARROW_W`（该提交 diff 第 121 行 `const maxH = width < ...`）——这就是后续错位区 bug 的种子。
- `e2dec83c0`：240→264（211 真数据最重组折 3 行 ≈257px），伴随窄版瓦片标签 `max-width:108px`。
- `42321b4ce`：帽判据与组件 is-narrow 统一按 **box.w**；补边界钉测（900/950/999/1000）。
- `0a0812927`：结构确定性重排（插件独立行 ≤2 行 + 组行 nowrap 恒单行），帽 264→288。
- `6187239cb`：`.gw__groups/.gw__grp/.gw__grp-cells` 补 `min-height:0`（flex 收缩，3 行 CSS，无测试可钉，属视觉层收尾）。

**终态盘点**：
- ✅ 帽值单点化成立：`GATEWAY_BOX_MAX_H=204`、`GATEWAY_BOX_MAX_H_NARROW=288` 仅存在于 `web/src/lib/liveTopologyLayout.ts:146,151`；组件 CSS 无帽值散落（grep 204/264/288 于 GatewayCoreBox.vue 仅命中第 6 行注释）。
- ✅ 布局侧判据正确：`liveTopologyLayout.ts:191` 用 `w < GATEWAY_NARROW_W`（w=box.w）；边界钉测 `liveTopologyLayout.test.ts:528-536`（900/950/999/1000）钉住「同量同值」的布局半边。
- ❌ **D-1（P2）组件侧 900 是死字面量，「单一真源」未闭环**：`web/src/components/live-topology/GatewayCoreBox.vue:187` `const isNarrow = computed(() => props.box.w < 900)` **未导入** `GATEWAY_NARROW_W`（`liveTopologyLayout.ts:153` 导出后全仓 grep 零外部消费者）。`liveTopologyLayout.ts:152` 注释自述「两边不许各写一个数」、`liveTopologyLayout.test.ts:520` 注释自述「GATEWAY_NARROW_W 单一真源」，但两处都只是**声明**：改 layout 常量为 860 后，布局给 box.w∈[810,900) 取宽帽 204 而组件仍切窄版（内容 ≈276px）⇒ 42321b4ce 同型 17px 级溢出**原样复发且无门禁拦截**（现有钉测只测布局函数，不读组件源码）。仓内已有「钉源码」先例（`LiveTopologyView.filters.test.ts:633` readFileSync 读 GatewayCoreBox.vue）却未用于此。**第四处同族漂移点＝组件这个 900 字面量本身**。
- P3 伴生：`GatewayCoreBox.vue:1143` `max-width:108px`（e2dec83c0 伴随物，0a0812927 重排后仍存活）是窄版布局的魔法数字，无注释指向帽依据。

## 2. 题2：页面级帮助系统 8 语种

**文件数实测**（`ls web/content/page-help/`）：zh-CN 88、zh-TW 88、en-US 88、ja-JP 88、de-DE 88、es-ES 88、fr-FR 88、**ar-SA 0**，共 **616 = 88×7**。提交链自洽：`e69baa87e` zh-CN 88 → `f2dcff6f8` zh-TW 88 → `216a3f881` 前 6 篇×5 语种 30 → `3a330d4b7` 余 82×5=410（提交自称「410 文件」与 diff 实测 411 行（410 md+1 test）一致）。

- ⚠️ **D-2（P2）ar-SA 语种正文整族缺席**：应用 8 语种含 ar-SA（`web/src/i18n.ts:9`），但帮助正文 0 篇；回退链 `PageHelpDrawer.vue:60` `contentByKey.get(id.locale) ?? contentByKey.get(id.zh-CN)` ⇒ 阿语用户抽屉 chrome 是阿语（RTL）、正文整篇中文，且**无「当前为中文回退」提示**。`PageHelpDrawer.vue:9` 与 `pageHelpRegistry.ts:28` 头注「8 语种齐全」与事实不符（`3a330d4b7` 提交信息自称「8 语种正文全齐」实际列出的恰是 7 个）。是否豁免 ar-SA（LLM 翻译质量/RTL 排版）未见登记——豁免清单（pageHelpRegistry.ts:15-22）只豁免路由，不豁免语种。
- ✅ 键覆盖门存在且扎实：`pageHelpRegistry.test.ts` 注册表↔zh-CN 内容文件**双向对拍**（孤儿文件也拦）、match↔router.ts 真实路由、titleKey 落词典、resolvePageHelp 行为（含 /live 不吞 /live2 的前缀不越权）。
- ✅ 回退链有正向钉测：`PageHelpDrawer.test.ts`（`df708bdcb` 重钉后）en-US 正向命中 + 「未登记路由+fr-FR ⇒ 本语种空态不崩」。
- ⚠️ **D-3（P2）「全覆盖」门禁头注承诺、实现缺失**：`pageHelpRegistry.test.ts:8-10` 头注第 3 条承诺「requiresAuth/requiresSuper/requiresPlatformOps 页面路由都应有帮助登记」，但该文件全文 129 行**无此用例**（仅内容完整性 3 + 路由真实性 4 + 行为 5）。后果已发生：`822c9cc78` 新增 `/admin/board-operational/:dimension`、`/admin/analytics/:dimension/:id` 两条 requiresSuper 路由（router.ts:372-374），`pathMatches('/admin/board-operational','/admin/board-operational/models')` 不命中（registry 的 match 无参数段）⇒ 两页**无 ? 图标**，且无任何门禁变红。注：主控已修的「孤儿路由」若指路由表↔导航对拍，与本条（帮助登记缺口+承诺缺失）不同面。
- ✅ helpManual 接线闭环：`/help` 路由本身登记为 id `help-manual`（pageHelpRegistry.ts:146）⇒ 帮助手册页自己也有 ? 图标；PageHelpDrawer 复用 HelpManualView 的 parseMarkdown/ManualInline 管线，无 raw HTML 注入面；AppTopbar 挂载点 + 简易模式虚拟键改写（AppTopbar.vue:86-92）闭环。
- P3：无「每 id × 每已声明语种」完整性门（只拍 zh-CN）——未来新 id 只写中文不会红（回退是设计内行为，但「7 语种齐全」的现状会静默退化）。

## 3. 题3：mobile-nav 89 席手风琴（0ce6a7005）

- ✅ 89/6 守门数字在位：`web-mobile/src/components/shell/AppDrawer.spec.ts:790-793`（DRAWER_NAV.length===89、DRAWER_GROUPS.length===6，注释明说「不许重构轮悄悄改」）；扁平===各组拼接、席 to 全局唯一、组过滤并集===扁平过滤（组不改权限口径）三条不变量齐。
- ✅ seatEntries 锚订正真实有效：`RequestAnomaliesGuard.spec.ts` 锚改为「`DRAWER_GROUPS`/`DRAWER_NAV` 两个 export 先出现者」（提交信息 §6-1 记录了旧锚把席位区切掉静默报 0 的假绿形态，与「探针锚区漂移⇒静默 0」家族一致）。
- ✅ 计数锁关系对上：PC 侧 `web/src/config/navCounts.test.ts:343` groupItems:64 等四计数是 PC 自己的锁；移动端 89 席是移动 appNav 自己的锁，两层不混算（桌面与移动菜单面本就不同构，主档 §3 有逐席映射表）。
- ⚠️ **D-4（P3）跨端一致性钉测是手抄快照**：`AppDrawer.spec.ts:783-795`「组 id 与 PC 端 NAV_GROUPS 同名同序」断言的是**硬编码数组** `['primary','operations',...]`，未 import PC `web/src/config/appNav.ts` 的 NAV_GROUPS 对拍。PC 侧组序若调整（或新增静态组），移动端测试仍绿。PC 现有静态组 5 个 + 合成组 plugins（appNav.ts:469-472，运行时注入）+ opsplatform/guide/chat 三个移动端无席组——手抄清单只声明了 5 个静态组的对位，plugins 组未在豁免注释里出现（它是合成组，属合理豁免但未写明）。
- 窗口内新增菜单项核查：`822c9cc78` 只加路由**未加** PC 菜单项（appNav.ts 零改动）、`24e211464` 仅 i18n 标签 ⇒ 无「新增菜单项未进手风琴」事件。两条 analytics 新路由在移动端无对应席（深链型页面，移动端不可达）——与 PC 同样「仅从看板 Top5 深链进入」，属一致性设计而非漂移。

## 4. 题4：设置页搜索 + 分组面板（2ec26cb44）

- ✅ **索引面=spec 注册中心，非硬编码清单**：搜索过滤的是 `loadList()` 结果（`SettingsView.vue:249-253` → `listSettings()` → `GET /api/admin/settings`），后端 `admin/settings.go:162` 遍历 `settings.Global.AllSpecs()` 全量注册面 ⇒ spec 新增键（含本轮 `system_config.go` 三键）**自动进搜索**，不存在「漏出搜索」问题。
- ✅ 搜索维度：键名/描述/分类标签三项 contains（SettingsView.vue:57-68）；空态与计数（filteredCount）有展示。
- ✅ 基础面板保存走既有 PUT 通道（校验+审计+热应用不复制）；redis/数据库只读实况口令不回传（infraValue 仅 addr/db/host）。
- ⚠️ D-5（P3）：**搜索面被分类选中静默截断**——`switchCategory` 设置 `filterCategory` 后 `loadList()` 服务端按类过滤，`filteredItems` 只在其上再搜（SettingsView.vue:50,57,445-451）。默认全量可全局搜，但用户点过分类后搜索框实际只搜该类，UI 无「当前分类内搜索」提示。待主控复核：是否属有意 scoped search。

## 5. 题5：运营看板详情分析前端（822c9cc78）

**数据流**：列表页 `BoardOperationalListView.vue` → `fetchDashboardBoard({days})`（复用看板 pies）→ 前端 switch 按 dimension 取 `pies.models/providers/clients/tenants` + 本地排序；详情页 `AnalyticsDetailView.vue` → `fetchAnalyticsDetail(dimension,id,{days})` → `GET /api/admin/analytics/{dimension}/{identifier}`（board.ts:284-307），payload 含 degraded/degraded_reason/degraded_trends/degraded_cross 降级标记。

- ✅ 三态处理齐全：loading（148）/error（146，surfaceError）/降级警告（154-157、168-170、CrossTable 125-127 逐维度 badge）/空态（TrendChart 239-241、CrossTable 114-116、130-132、ListView 179）；`degraded` 与「加载失败」两种语义分开渲染（符合 AGENTS.md ⑥「陈旧态与错误态互斥」纪律）。
- ✅ 图表库=ECharts 6.1.0，**package.json:142 已声明**（非幽灵依赖）；`AnalyticsTrendChart.vue` unmount 时 disconnect ResizeObserver + dispose（219-228），`BoardOperationalView.vue` dispose（83/310）。
- ❌ **D-6（P2）「完整列表页」实为 pies 的 LIMIT 25 截断面**：`resolveBoardPies`→`queryDimPie` SQL `ORDER BY SUM(requests) DESC LIMIT 25`（`admin/dashboard_board_queries.go:567`）⇒ 每维度最多 25 行。列表页自称「完整列表」（router.ts:371 注释、提交信息「完整列表页」），从看板 Top5「更多」进入后**无任何「已截断/共 N 项」提示**，>25 个 model/tenant 的部署上第 26 名起静默不可见——正是本仓纪律点名的「假完整」。**生产侧推定：211 维度基数是否 >25 待主控活体复核**。
- ❌ **D-7（P2）非法 dimension 渲染抛错**：`AnalyticsDetailView.vue:126` `$t(ANALYTICS_REGISTRY[dimension].titleKey)` 无可选链/守卫——手滑或外链构造 `/admin/analytics/foo/bar` 时 `ANALYTICS_REGISTRY['foo']` 为 undefined ⇒ 模板渲染期 TypeError（比 load() 的 422 更早发生），页面崩而非优雅 404/空态。列表页同族较轻（switch 落空→空态，但标题行同样裸取 registry，待复核其 title 行是否也有 `?.`）。
- ⚠️ **D-8（P2）ResizeObserver 重初始化路径漏 disconnect**：`AnalyticsTrendChart.vue:29-31` 数据变化 watch→`initChart()` 只 `dispose()` 旧实例；旧 observer 在 201-207 每次 init 新建并 observe **同一个** DOM 节点，仅在 unmount（219-228）disconnect。N 次 deep-watch 重渲染累积 N 个 observer，回调闭包引用模块级 `chartInstance` ⇒ 旧 observer 会驱动**新**实例重复 resize（有界泄漏+重复回调）。修法一行：re-init 分支先 disconnect 旧 observer。
- ⚠️ **D-9（P2）图表层时区/语种双硬编码**（门禁扫不到——不是 i18n 键、不是查询参数）：`AnalyticsTrendChart.vue:41` `toLocaleString('zh-CN',...)` 轴标签**语种硬编码**（8 语种切换后图表仍中文）；`new Date(d.bucket)`（bucket 为 ISO 8601，board.ts:220）用**浏览器本地时区**渲染，与页面其余「日切 UTC」口径（board-operational 帮助正文自述「日切 UTC」）在非 UTC 浏览器上轴标签与卡片数字错位一天——与 AGENTS.md F2/L-TZ1 同族漏点族的第 5 处。legend/series 名 `['请求数','成功率','花费','延迟']`（:92）与 tooltip 分支 `name === '成功率'`（:75-83）、yAxis name（:114-143）全部中文硬编码。**生产侧待主控活体复核**（非 UTC 时区浏览器实测轴标签）。
- ✅ SummaryCards/CrossTable 数值格式化 null-safe（'—' 兜底）、成本卡只显示 cost_usd 单独卡+credits 单独卡（L1 跨单位教训已吸收）、CrossTable slice(0,10) 见 D-10。

## 6. 题6：admin-tools 211 用户认证配置（3ff3b8312）

- ✅ 工具代码本身无硬编码密钥：`cmd/hash-password/main.go` 密码走 argv；`cmd/user-admin-tool/main.go:22-24` DSN 走 `DATABASE_URL` 环境变量，bcrypt cost=10 与种子逻辑一致。
- ❌ **D-11（P1）211 生产账户凭据写入公开仓**：
  - `maestro/flows/13-user-auth-verification-simple.yaml:28,68,107` 明文 `Veritrans&9527`（admin）、`Demo@admin123`（demoadmin/super_admin）、`Demo@user123`（demo/tenant_admin）；`13-user-auth-verification.yaml:4,36,94,157` 同;
  - `docs/211-user-auth-{setup,completion,final-verification}-report.md`（f33f5a16e）三份报告重复打印三组账号密码（completion-report.md:12-13,39,50 等）；
  - **提交信息本身**含「admin 密码改 Veritrans&9527」（3ff3b8312 message）。
  - admin 密码属**仓内既有实践**（`maestro/flows/_shared/login.yaml` 及 ~20 份历史 docs 早已有之），非本轮首创；但 **demoadmin(super_admin)/demo(tenant_admin) 两个生产账户是本轮新造**（211 生产库创建，提交自述「经 API 验证返回有效 JWT」），凭据随仓公开。211 面向公网（llm.itestu.cn）⇒ 任何人可据此以 super_admin 登录生产。**生产侧推定：账户是否仍在役、是否可公网登录，待主控活体复核**；建议收口：改密+改用 env 注入 flow（仓内 Maestro 凭据纪律先例：`.env.local` 读口令）。
- P3 附注：密码经 argv 传入工具会进 shell history/进程列表（本机运维工具可接受，登记即可）。

## 7. 题7：vue-tsc/构建质量（主控已跑绿，不重跑）

- ✅ echarts 为声明依赖（package.json:142-146）。
- ⚠️ D-12（P3）`any` 使用 10 处集中于新 analytics 面：`AnalyticsTrendChart.vue` 4 处（`(chartInstance as any).__resizeObserver` 存 observer、tooltip `params: any`）、`AnalyticsCrossTable.vue` 3 处（`items: any[]`、`'error' as any`/`'status' as any` 维度越界枚举）、SettingsView `currentValue = ref<any>` 等。均为局部便利而非结构失型，但 `items: any[]` 使 CrossTable 字段拼写无编译期保护（`item.key/requests/...` 拼错不红）。

## 8. 题8：门禁覆盖不到的质量问题（vitest 相关红主控已收口，不重复）

- **D-10（P3）魔法数字**：`AnalyticsCrossTable.vue:148` `slice(0, 10)` 截断无「共 N 项」提示（交叉表也是 Top-N 面，与 D-6 同族但为有意摘要，建议至少加计数行）；`AnalyticsTrendChart.vue` 容器 400/450px 与媒体查询 350px 三处独立数字。
- **D-13（P3）a11y 小项**：`AnalyticsCrossTable.vue:138-143` `<th>` 无 `scope` 属性（同轮 PageHelpDrawer 表格有 `scope="col"`，同仓不同标准）；`AnalyticsTrendChart.vue` 图表容器无 `role="img"`/`aria-label`，趋势对读屏用户不可达（empty 态有文案）。
- **D-14（P3）pageHelp 全覆盖门禁缺口**（已在 D-3 记录，此处归口）：头注承诺的「全覆盖」钉测从 e69baa87e 首日起即未实现，属「门禁自述与门禁实现分叉」——与 AGENTS.md「工具自称通过≠量到」同型。

---

## 9. 复核要点清单（给主控的活体/裁决清单）

1. **D-11（P1）**：211 上 demoadmin/demo 账户是否仍在役、能否公网登录；若在 ⇒ 改密 + flow 改 env 注入 + 评估 admin 密码既有公开实践的收口路径。
2. **D-6（P2）**：211 实测 `pies.models/tenants` 是否触 25 上限（`SELECT dim_type, count(*) FROM request_stats_dim_minute ... GROUP BY dim_key` 对拍）⇒ 若触顶，「完整列表页」需换真分页端点或明示截断。
3. **D-7（P2）**：构造 `/admin/analytics/foo/bar` 访问 211，确认渲染崩溃面（白屏/局部）；修法 = registry 查失败早退 + 空态。
4. **D-1（P2）**：裁决 is-narrow 单点化路径——组件 import `GATEWAY_NARROW_W`（首选）或钉源码测试锁 900 字面量；二选一，别停在注释声明。
5. **D-8（P2）**：AnalyticsTrendChart re-init 分支补 disconnect（一行）。
6. **D-9（P2）**：非 UTC 浏览器打开 211 详情页对拍轴标签 vs 卡片数字；语种硬编码裁决（图表文案是否入 i18n 8 语种）。
7. **D-2/D-3（P2/P3）**：ar-SA 豁免是否拍板（豁免要登记）；「全覆盖」钉测补实现或删头注承诺。
8. **D-5（P3）**：设置搜索 scoped 语义裁决；**D-4（P3）**：跨端组序钉测改真源对拍或登记手抄快照口径。

> 审计方法自述：全部结论来自 git show diff 逐行阅读 + 现仓 grep/readFileSync 实测 + SQL 上限核实（dashboard_board_queries.go:567 LIMIT 25）；未运行构建/测试（主控已跑绿的项不重跑）；生产侧结论均为静态推定，已逐条标注待活体复核。
