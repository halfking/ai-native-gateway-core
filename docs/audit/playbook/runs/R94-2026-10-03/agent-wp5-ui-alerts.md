# WP-5 UI与告警面 子代理报告（窗口 a0da9066d..HEAD）

审计范围：317351daf（四页整合）、f762e60f1/2602bd32e/5575414bf（usage-trend 三连）、e5197e0a4（会话过滤）、60b372bd7（nil-slice null）、2e68487a8（selection 产出侧告警）。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | Low | 会话列表空结果时 "sessions" 编码为 JSON null，违反 api-yaml 声明的 type: array；这正是 60b372bd7 修的同族形态，但该提交未覆盖本端点 | admin/session_analytics_handler.go:330/361、session-analytics.yaml:92-94 | **本轮已修** make(...,0) |
| 2 | Low | handleStrategies 内 summary/breakdown 两 slice 同款 nil→null 问题，未被 60b372bd7 修复；该函数是未注册路由的死代码（nolint:unused），风险潜伏 | admin/auto_route_tuning.go:838/888/932/976-977 | 若确认废弃则删除；若接线补 make(...,0)——登记 |
| 3 | Low | admin 面 nil-slice→null 同族候选清单：① candidate_failure_handlers.go:324→331 data（同文件其余三处都有 make，唯独此处没有）→**本轮已修**；② cache_metrics_handler.go:228→248 buckets；③ credential_success_rate.go:69→101；④ health_check_handlers.go:46→106 items；⑤ annotation_handler.go:218/621/1161-1163（前端现有防护，属契约层） | 见左 | ①本轮修；②-⑤按契约严格度排期 |
| 4 | Info | dashboard 看板 "trends" 可为 null 是注释明示的既定契约，前端有防护 | admin/dashboard_board.go:91-96、dashboard_board_queries.go:293-294 | 保持现状 |
| 5 | Low | 旧四路由 redirect 丢弃旧页面其余 query（页码/筛选态丢失，路由本身不死链） | web/src/router.ts:200-212 | 可选：redirect 用函数保留同名参数；现状可接受——登记 |
| 6 | Low | 会话列表日期过滤把裸 YYYY-MM-DD 解析为 UTC 日界；UTC+8 运营者筛「今天」偏移 8 小时；代码注释自述刻意，yaml 未写时区语义 | admin/session_analytics_list_filters.go:59-70 | yaml description 补「按 UTC 解释」——登记 |
| 7 | Low | api-yaml 会话列表规格漂移：DateFrom/DateTo 描述与实际缺省行为不符；列了 handler 不消费的 Model/Provider/HealthGrade/sort_by 参数 | session-analytics.yaml:633-652/57-77 | 规格订正一轮——登记 |
| 8 | Low | rule_tests 头注释停留在中间修形态（sum by (instance)/and on(instance)），最终规则与 Go 门都是 (job, instance) | rule_tests/auto-route-selection-output_test.yml:51-52 vs rules/:123-131 | **本轮已订正** |
| 9 | Low | getAvailableModelsRaw 成为死导出：5575414bf 后 web 侧零调用 | web/src/api/models.ts:52 | 交 deadcode-scan 处置或删除——登记 |
| 10 | Info | auto-ops 宿主 KPI「待审提案」徽标 = min(pending,200)（limit:200 硬取）>200 时低估；「今日待标注」用 UTC 日界（自洽但非本地日） | AutoRoutingOpsView.vue loadKpi | 可接受——登记 |
| 11 | Info | en-US/nav.ts 存在窗口外既有的 key 缺口（routingDefaults/requestTrace/tenantLicense/tenantAutoUpdate 在 ja-JP/ar-SA 有而 en-US 无）；i18n 工具 source-locale 实为 zh-CN | en-US/nav.ts vs ja-JP/nav.ts:35,46,76-77、scripts/i18n-audit.mjs | 窗口外既有项——登记 |

## 二、核实为健康的面

**317351daf 四页整合**：旧路由重定向齐全（router.ts:200-212）；全仓无残留指向旧路由的内部链接；menu-config.json 已重导出。super 门控双保险（normalizeTab 对非 super 返回 null 深链弹回 + tabs 列表仅对 super 含 profiles/tuning，面板不挂载不发请求）。i18n：autoOps 新模块 8 语种逐键一致；node scripts/i18n-audit.mjs 实跑 PASS (0 missing keys)；被删的 4 个 nav 死键无残留引用。数据加载：四面板各自 onMounted 加载；宿主 KPI 用 Promise.allSettled；watch 互写无死循环。

**usage-trend 三连**：后端多选统一 `= ANY($n)` 参数化（六条查询路径全收口；去重/剔空/上限 20）；多选时不折叠 __others__。定时器/监听器：refreshTimer 卸载清理 + 重设前先 clear；loadSeries 有 token 竞态守卫；ModelTrendChart 卸载 destroyChart。「清除全部」与 syncQuery truthy 语义对称；前端序列化契约有 4 条守卫测试；modelsSelectedCount 词条 8 语种齐全。5575414bf：死链删净、必填校验保留、无孤儿。

**e5197e0a4 会话过滤**：纯参数化（task_id 双列匹配同一参数复用）；list/count 两查询共享同一 where/args，total 不脱节；非法日期 400、from>to 400；end-of-day 用 24h-1µs 对齐 µs 精度（注释解释 round-up 陷阱）；单测 5 组；api-yaml task_id 已由 8fcb0d392 补入。

**2e68487a8 产出侧告警（实跑验证）**：`or (0 * max by (job, instance) (up{...}))` 把无序列转成带标签的 0（优于裸 or vector(0)）；`and on(job, instance) (up==1)` 防误报；阈值/时长语义清楚（2h 窗 + for:1h ⇒ 归零约 1h 后触发）。重复面排查：全 rules 目录 grep 无重叠；metric 名与 selection_metrics.go 完全一致。promtool 实跑：check rules SUCCESS + test rules 6 场景全过（A 从未产出触发/C 正常不触发/**D 有流量但归零触发**/E 下线不触发/F-G dropped 两态）；「有流量但归零」vs「无流量」两态区分正是 A/D 对。Go 门只钉机制不钉字面串。

## 三、未覆盖项与原因

1. Go 编译/单测未执行（只读约束）；i18n-audit 与 promtool 因确认纯读已实跑。
2. vitest/vue-tsc/element:check 门禁未重跑；usage-trend 多选集成测试需真库。
3. promtool 变异验证未做（需改文件）；正向 6 场景已过。
4. 8fcb0d392 只做与本窗口相关性核查；menu-config re-export 机制未深查。
5. nil-slice 家族清单为响应写入点级筛查，非编译器级穷举。
