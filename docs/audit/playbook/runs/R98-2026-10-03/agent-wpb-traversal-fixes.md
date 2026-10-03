# WP-B：遍历扫描修复批复审

- 窗口：`16381a6ec~1..94b1a836f`（复审 16 笔非 release 提交 + 4+2 笔 chore(release) 记账面）
- 审计人：WP-B 只读子代理（第三十八轮，2026-10-03）
- 验证手段：逐笔 `git show`、`go vet ./admin/`（exit 0）、`go build ./admin/... ./cmd/...`（exit 0）、
  gate 测试实跑（session-detail SQL 门 / planCostTrend 门 / JSON 契约门 / wiring 门，全绿）、
  `vitest run sortByName + nameSortWiring`（39/39）、`ui-audit.mjs --strict`（exit 0，9 条豁免全部带理由）、
  `ui-sweep.mjs --selftest`（28/28 + 17/17 + 6/6 实跑复现）
- 纪律遵守：未修改任何生产文件、未部署、未注入凭据；本文件为唯一写出物

## 逐笔判定表（commit / 主张 / 判定 / 证据）

| # | Commit | 主张（摘要） | 判定 | 证据 |
|---|--------|--------------|------|------|
| 1 | `16381a6ec` fix(web,admin) | 7 个真缺陷 + ui-sweep 运行时门 + wiring 静态门 | **基本成立，牵出 P1（鉴权）** | 8 项修复逐一对得上 diff：routing_overrides.go:401 nil→空切片、useChart.ts ref→shallowRef（环断开论证与 Chart.js options 回写一致）、auto_route.go:183 `prof` 改 `*string`、auto_route_tuning.go 三处 AVG 包 COALESCE、tenants.go 富化拆 1.5s 独立 ctx 且 slog.Warn 留痕、credential-monitor.ts 按服务端 100 上限切片并行合并、PromptInjectionSettingsView 改 listModels()、main.go:6532 接线 output-compliance。**但接线本身把一个从未过鉴权的 handler 家族暴露到公网（见深审一）**。wiring_gate_test 判据覆盖 4 种接线写法且有「判据失效」自检，实跑通过 |
| 2 | `37d4db60a` fix(web,admin) | /records 端点 + correlations 白屏 + 弹层可复跑 | **功能成立；SQL 参数化干净；鉴权缺失（P1）** | handleRecords：`where` 子句全部 `$n` 占位符，`fmt.Sprintf` 只内联编译期常量（preview 长度）与占位符序号，无注入面；parsePagination 钳制 limit>0 / offset>=0；`content_preview` 仅 `redacted=true` 时取 `redacted_output` 并截 120 字，evidence 列不回显——脱敏约束在 SQL CASE 里硬编码，不靠调用方自觉。correlations 修复：归一化收敛在写出口 `normalizeCorrelationsResponse`，配套 json_contract_test 直接钉「过归一化后零值序列化为 `[]`」且兜底扫 `:null`，判据落在被测性质上；前端再加 Array.isArray 二层防御。stats 字段补齐与 checker.go 五字面量（pii/toxic/secret/internal_ip/bias）一致，jailbreak 恒 0 是诚实值 |
| 3 | `693e0f965` fix(ui) | probe-health/detail 补「未指定模型」说明态 | 成立 | 纯 UI：`v-else-if="!modelName"` 说明块 + goBack（组件 :348 已存在）；配合 #1 的请求短路，双保险 |
| 4 | `1bf076f16` feat(web) | 弹层遍历支持 manual 项 | 成立 | manual 项照常进报告、`skipped:true` 不参与合格/不合格判定，汇总行拆「自动/待人工」两列——「没测」与「测过没问题」在报告上可区分，主张与实现一致 |
| 5 | `62d4f8033` chore(release) | build_seq → 2419 | 记账一致 | version.json/VERSION/web 三处同步 2419，sha=1bf076f1 与前序修复对应 |
| 6 | `23d87c04e` feat(ui) | Agent 台账与凭据表按名称排序 | 成立 | AgentRegistryView 接 sortByName；CredsTab 用派生 computed（不动 prop）+ `['label','name']` 键；label **刻意不进 DEFAULT_KEYS** 且有测试钉死（防四处已上线行序静默改变）——负向钉测是这批排序提交里质量最好的一个 |
| 7 | `90e6fb6c6` docs(audit) | 68 列表控件普查落档 | 成立但已过时（P3） | 普查文档落档 + 自身消耗 build_seq 2420（记账面见下）；文档第 36 行 StandardModelPricingView 的豁免理由后被 `8587e1675` 证伪但**未回改本文档** |
| 8 | `f9fb53219` docs(audit) | maintain-api 鉴权契约与本地 401 真因 | 成立（锚点一处本地不可核） | vite.config.ts:110-114 直透代理、不换签，与文档描述一致；`ai-native-maintain/internal/config/config.go:80` 锚点所在仓库不在本机，无法本地核行号（标注为不可核，非否证） |
| 9 | `11c0a862b` docs(web) | 遍历扫描运行纪律 | 成立 | ui-sweep.mjs 头部注释写明「扫描期间不改工作树/不开第二个浏览器」及 15/15 受控复现记录 |
| 10 | `79a554366` docs(audit) | usage_ledger 缺两列待决策简报 | 成立，含一处数据口径矛盾（P3） | 论证链完整：INSERT 固定列清单（telemetry/client.go）无此二列→加迁移只会得到永久 NULL；54.3s vs 0.23s 实测支撑「桥不可用」；行差 4.0% 支撑「换表不干净」。但与 `33da7e609` 的 0.42% 方向相反（见新发现问题 #3） |
| 11 | `a0b1366ed` feat(web) | 弹层遍历加 ModelPicker + 前置条件 | 成立 | 脚本头写明「网关 + maintain 都得在跑」前置条件，防把依赖服务停机误读成全站回归 |
| 12 | `c910cc58a` fix(web) | 弹层探针 class 名盲区改几何差分 | 成立 | 修法四步（几何候选→收遮罩内静态后代→点击前后差分→排除遮罩后取最外层）每步有夹具；变异验证记录在案（改回 class 名 11/14 红、去差分 13/14 红）；本轮实跑弹层自检 17/17 复现通过 |
| 13 | `8e17d92a4` fix(admin+web) | 会话详情 500 + 13 动态详情页 + 9 处排序 | **成立（三项均核实）** | 根因见深审二；DYNAMIC_ROUTES 恰 13 条且带真实 ID 解析（fetch 列表端点/链式取 ID），取不到逐条点名不静默丢——是**真覆盖机制**而非清单登记；nameSortWiring.test 钉「computed 换了但模板没换」这一接线层，v-for 换回原数组即红 |
| 14 | `d1f76bcc9` fix(web) | 契约 B「限高可滚」结构判据 | 成立 | 判据从「命名豁免」换成「限高元素自己的模板子树内是否存在 overflow 声明」，三态实测（真实代码 0 / 抽掉 overflow 1 / 移出弹层 1），第三行反向对照证明非恒绿；文档同时自我更正了上一轮「静态核对为无裁切」的过头表述 |
| 15 | `8587e1675` fix(web) | StandardModelPricingView 排序 | 成立 | useModelCatalogFilters.ts 只 filter 不 sort 不 group（读源码核实），「待决策」前提确实不存在；视图加 sortedFiltered（display_name 键，与首列主键一致）；同时更正 ModelIntegrityView 的豁免理由（结论对理由错，文档明写） |
| 16 | `33da7e609` fix(admin) | usage_enhanced 删幻影列换基表保口径 | **成立，附两处口径注记（P3）** | 见深审三；usage_ledger_sourceless_columns_test.go 七条判据含「检测器必须吃下 b9a8baba4 原始 SQL 并报红」的反向对照，实跑通过；request_logs 读清单如实登记扫描器盲区（1 vs 真实 2）而非放宽扫描抹平告警——审计诚实度高 |

记账面（4+2 笔 chore(release)）：2419(`62d4f8033`)→2421(`7123fef91`)→2422→2423→2424→2425，各笔 sha 与前一修复提交一一对应，无重复无回退。**2420 不缺**：`90e6fb6c6`（docs 提交）的 version.json 恰为 `2.5.8-b38719e1-20261003-2420`（工作树态 sha）。2418 在全部 refs 无对应提交——与本仓既有模式一致（2399→2402、2405→2408、2412→2415 均有跳号，build_seq 按构建尝试消耗、非按提交递增），判定为记账正常而非丢提交。

## 重点深审三件

**一、/records 端点鉴权（37d4db60a，兼及 16381a6ec 的接线）——发现 P1**
`OutputComplianceHandler.RegisterRoutes`（admin/output_compliance_handler.go:31-46）把 8 个端点（policy/keywords/review-queue/feedback/stats/records 及子路由）**裸挂 mux，无任何鉴权中间件**。全局链的 `middleware.NewAuthMiddleware` 对 `/api/` 前缀**显式旁路**，其源码注释（middleware/auth_mw.go:37-40）写明的安全不变式是「every registered /api/* endpoint is wrapped by wrapAdmin/superAdmin」——output-compliance 家族恰恰违反了这条自证安全声明。对照组：prompt-injection 同位置注册但每个 handler 过 `AdminMiddleware`；sessions/detail 用 `wrapAdmin`；admin.Handler 用 `h.admin`。后果：未认证请求打到该家族任意端点，`GetTenantID` 落到兜底 `"default"`，可**未认证读** default 租户的合规命中记录（session_key、命中类型、脱敏预览）与策略，且 `handlePolicy` 接受 PUT/POST——**未认证写**合规策略。暴露面由 `16381a6ec` 接线引入（接线前是 404 死代码），`37d4db60a` 的 /records 在同一无鉴权模式上扩了一个读端点。SQL 本身参数化干净，问题纯在中间件层。新增的 wiring_gate_test 只断言「构造了且有 RegisterRoutes」，不查鉴权，故两道门都拦不住这类缺陷。

**二、会话详情页 500 根因（8e17d92a4）——主张成立，覆盖为真**
根因链每环都有实证：`sessionAnalysisSelectCols()`（admin/session_meta_view.go:103）输出 `sam.status … sam.updated_at`，`querySessionDetailV2` 的 `LEFT JOIN LATERAL` 因此在作用域内同时暴露 `sessions.status/updated_at` 与 `sam.status/updated_at`，SELECT 里裸写 `updated_at` 触发 SQLSTATE 42702——这是 PostgreSQL 确定性行为，修法（sessions 列全部 `s.` 限定，LATERAL 列保持 `sam.`）语义精确等价，当前源码已核对无裸列。回归门设计合理：断言「SELECT 列表形状」而非跑真库，`TestSessionDetailSelectQualifiesSessionsColumns` 更强的形状约束能拦住「下次新增列又写裸名」。13 个动态详情页是**真覆盖**：DYNAMIC_ROUTES 每条带 `from`+`pick`（或链式 `chain`）在扫描时调真实 API 解析 ID，解析失败逐条点名写进报告；router.ts 未登记的动态路由另有告警。commit message 里两处自我纠错（items/clients 键名、ID 空间取错导致假 404）说明覆盖确实被跑过而不是纸面登记。

**三、usage_enhanced 换基表保口径（33da7e609）——等价性成立，两处注记**
「原视图」没有可与之等价的正常语义：b9a8baba4 引入的 work_type/intent 引用（`ul.work_type`、`ON ss.session_key = ul.gw_session_id`）自诞生起必然 42703——基表列清单已从 deploy/sql/schemas/baseline/01-schema.sql:18238-18258 核实（19 列 + migration 739 的 rate_multiplier = 20 列，确无 work_type/gw_session_id/compression_strategy）。所以正确的等价基准是**同端点 model/provider/api_key 维度**的 ledger 口径。换基表后的语义：cost 取 `rl.cost_usd`，commit 自述同窗口两表均 150.15 美元（无法本地复核，但回归门 + 文档一致引用该数）；分母（request_count、percentage）则落在 request_logs 总体上。对 admin 报表消费方的影响：UsageCost.vue 下拉内五个维度现在来自两张总体略有差异的表，同一页面横向切维度时分子分母人口不同——commit 遗留清单如实写明（约 0.42% 行差），属可接受但应让报表读者知晓的口径注记。`unique_sessions` 删除前核过消费方（web/src 及两个外部仓零命中），破坏性变更有据；cache-economics 把压缩计数拆成独立查询、失败只 Warn 不连坐主聚合，降级面收敛正确。IsSchemaBehindError 机制本身未动（commit 自己标为遗留）——下一个引用幻影列的人仍会拿到 200+全 0，这是机制级残留风险。

## 新发现问题

**P1（安全，需下一轮根修）**
1. `/api/admin/output-compliance/*` 全家族（含新增 `/records`）无鉴权：`RegisterRoutes` 未过 AdminMiddleware/wrapAdmin，全局 auth 中间件对 `/api/` 旁路，违反 middleware/auth_mw.go:37-40 自证的安全不变式。未认证可读 default 租户合规记录/策略，且 policy 端点可未认证 PUT/POST。修法建议：仿 prompt-injection，RegisterRoutes 内 wrap AdminMiddleware；并给 wiring 门补「接线必带鉴权」断言（该门当前只查 RegisterRoutes 存在）。

**P2**
无。

**P3**
1. `docs/audit/列表控件普查.md`（90e6fb6c6 落档）第 36 行仍保留已被 `8587e1675` 证伪的 StandardModelPricingView 豁免理由（「排序发生在 useModelCatalogFilters 内部…需单独评估」），第 79 行「已按名称排序： 否」也已过时（视图现为 sortedFiltered）。该文档自称「重新生成：整体替换」，建议重跑 `npm run ui:check` 刷新，避免下一轮照错理由捡待办。
2. `33da7e609` 遗留清单承认 IsSchemaBehindError 降级机制未动：42703 仍会被静默降级为 200+全 0。本轮消除了实例，机制性「把代码缺陷伪装成迁移延迟」的风险仍在，建议后续把「哪些 42703 可降级」收敛为显式白名单（列名级）而非按 SQLSTATE 一刀切。
3. 两份同日文档对 request_logs vs usage_ledger 行差的记录方向相反：`79a554366` 简报「近 30 天 ledger 2,082,837 vs request_logs 2,169,970（request_logs 多 4.0%）」，`33da7e609` 遗留「request_logs 比 usage_ledger 少约 0.42%」。窗口/租户口径未注明，二者不能同时成立；影响的是「换基表口径差」这一论据的精度（不影响 cost 等价主张）。建议补注测量条件或复测一次。
4. `f9fb53219` 引用的锚点 `ai-native-maintain/internal/config/config.go:80` 所在仓库不在本机，行号本轮不可核（非否证，仅登记）。

## 结论

这批提交的工程质量显著高于均值：每个修复主张与 diff 一致、根因论证带实测或源码级证据、钉测普遍落在被测性质上且自带反向对照（json_contract 门、sourceless-columns 门、nameSortWiring 门的变异记录均实跑复现），自我纠错（恒绿夹具、恒红门、假阳性 ID 空间）全部留痕。记账面干净：build_seq 2420 确由 90e6fb6c6 消耗，2418 缺号符合本仓构建尝试惯例。唯一的实质问题是审计重点三（/records 鉴权）反向命中：**把一个从未设防的 handler 家族接上了公网，并继续按同一无鉴权模式扩端点**——这是 P1，应在下一轮作为第一顺位根修，并同步收紧 wiring 门的判据（接线 ≠ 安全）。
