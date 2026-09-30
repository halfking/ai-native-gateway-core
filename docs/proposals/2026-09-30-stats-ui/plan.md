# 统计 UI 优化方案 —— 对账结算 / 租户统计 / 用户列表+详情

> 2026-09-30 方案轮 · **方案 A/B/C 已落地** · 本目录（docs/proposals/2026-09-30-stats-ui/）为交付物
>
> 效果图：[index.html](index.html) 目录 ｜ [A 对账结算](reconciliation.html) ｜ [B 租户统计](tenant-stats.html) ｜ [C 用户+详情](users.html)
> 本地预览：`cd docs/proposals/2026-09-30-stats-ui && python3 -m http.server 18792` → http://127.0.0.1:18792

---

## 0. 结论摘要

对标参考看板（暗色 slate + 蓝主色的 LLM 网关数据看板，经 OCR + 像素分析还原），对三个页面做统计信息架构升级，视觉全部走本站 `--kx-*` 令牌体系（不引入参考图配色）：

| # | 页面 | 核心改造 | 后端改动 | 分期 |
|---|------|---------|---------|------|
| A | `/admin/reconciliation` 对账结算 | KPI 主副指标卡 ×6、双趋势图（请求成本 / Token 构成+命中率）、分布表占比条 +「按 Token/按金额」切换、失败原因徽章化、筛选器接线（后端参数已存在） | **零后端改动**（纯前端） | P1 |
| B | `/tenants/:code` 统计 + 概览 | 统计 tab：KPI 8 卡 + 双趋势图 + 模型/应用分布占比条；概览 tab：KPI 卡升级带 sparkline 与环比 | `getTenantStats` 增加 `daily` 时序字段（1 条 GROUP BY） | P2 |
| C | `/users` 用户列表 | 顶部统计条、每行近 30 天请求/Token/积分列、点击行开**用户详情抽屉**（KPI/趋势/Top 模型应用/最近请求/基本信息） | 新增 2 个 admin 端点（users 用量汇总 + 单用户画像） | P3 |

关键数据面实证（本地 8782 库，2026-09-30 核对）：
- `session_owners` 视图**在仓内 schema 与本地库均不存在** → `/api/admin/session-analytics/users*` 端点本地必 500，方案 C 不依赖它（§5.2）。
- `request_logs.owner_user` 本地全空；**`api_key_owner_user` 有值**（近 30 天 4,390 行中 1,869 行），MaaS 计费明细同用此列 → 用户统计的权威关联列定为 `api_key_owner_user`。
- `report_snapshots` 的 internal "person" 维度 = `end_user_id`（终端用户），**不是**平台账号，不能直接当 /users 统计源。

---

## 1. 参考图还原（方法与结论）

视觉模型分析服务当日持续限流（429），参考图经 **tesseract OCR（chi_sim+eng，整图+三分条）+ PIL 像素色彩统计**还原。若与原图有出入，请在确认时指出，效果图按指出点修正。

### 1.1 还原出的版式（自上而下）

1. **工具栏**：时间范围（近 7 天下拉）、按小时粒度、端点/密钥/模型筛选
2. **KPI 四大卡**：`总请求数 31,178（所选范围内）` ｜ `总Token 1.32B（输入 292.47M | 输出 12.67M）` ｜ `总消费 $5,942.38（实际 $3,236.48 / 标准双口径）` ｜ `平均耗时 11.67s（每次请求）`
3. **分布表左右并排**：模型分布、分组使用分布 —— 列为 `名称｜请求数｜Token｜实际｜标准`，右上「按 Token / 按实际消费」切换（再下一排为 端点分布、推理强度）
4. **Token 使用趋势**：堆叠面积图（Input / Output / Cache Creation / Cache Read）+ Cache Hit Rate 折线（右轴），图例带色块
5. **底部**：密钥级统计小卡（如 `1,026 个 / 694`、`13,208`、`207K`）+ 最近请求表（密钥｜模型｜计费模式｜消费 $0.45527625｜首字 15.06s｜总耗时 33.10s｜时间｜USER-AGENT）

### 1.2 配色与风格（像素统计）

- 暗色主题：背景 `#0f172a` 系（slate-900 族），卡片 `#0f1d30`/`#102030`（蓝调深色），侧栏 `#111827`
- 主色 `#3b82f6`（Tailwind blue-500，约占像素 1.3%，用于图表主系列/按钮）
- 结论：**信息架构全部移植；配色不移植**，全站已有 `--kx-*` 双主题令牌（dark：bg `#0f141c` / surface `#1a222d` / primary `#5b8cff`），参考图的蓝调暗色与本站暗色主题气质一致，直接用本站令牌即可保持全站皮肤一致性（2026-09-29 巡检刚收口过）。

---

## 2. 设计原则与实施铁律

1. **信息架构对标参考图**：KPI 主副指标卡 → 趋势图 → 分布表（占比条 + 指标切换）→ 明细表。
2. **视觉零新造**：全部消费 `--kx-*` 令牌 + 语义 badge 体系；canvas 内颜色经 `getComputedStyle` 解析（`composables/useChart.ts` 既有约定）。
3. **el-\* 组件必须显式 import**（main.ts 无全局注册，2026-09-28 对账页白屏教训；`element:check` 已是 build 硬门禁）。
4. **图表统一 chart.js 主线**（useChart 封装），不在本三页引入 echarts 新面。
5. KPI 卡统一收敛到 `components/ui/StatCard.vue`（本次为其扩展 icon/sub/tone/sparkline 槽位），分布表沉淀新组件 `components/analytics/DistributionTable.vue`（名称+占比条+数值列+指标切换），趋势图复用 `analytics/TrendLineChart.vue` 扩展堆叠模式。
6. i18n 全量补键（zh-CN / en-US 必须，其余 5 语言同步占位），文案进 `locales/*/reports.ts`、`tenants.ts`、`users.ts`。

---

## 3. 方案 A —— 对账结算页 `/admin/reconciliation`（P1，纯前端）

### 3.1 现状 → 目标

现状（ReconciliationReport.vue，380 行）：工具栏 + 3 张手写 KPI 卡 + 4 张纯表格（供应商/租户、人员、模型、按天）。无图表、无筛选下钻、失败原因是纯文本 `k:v` 拼接。

目标布局（对照 [reconciliation.html](reconciliation.html)）：

| 区块 | 内容 | 数据来源（现有 API） |
|------|------|---------------------|
| 工具栏 | 视角切换（供应商/内部）＋ 日期范围 ＋ 快捷区间 chips（昨天/近7天/近30天/本月）＋ **供应商/模型筛选下拉（新增，后端参数 `provider_id`/`model` 早已支持、前端从未接线）** ＋ 刷新/导出/重跑 ＋ 快照覆盖徽章 | `RangeReport.providers/models` 本地取 options |
| KPI ×6 | 总请求数（成功/失败/错误率）｜总 Token（入/出/缓存读/写）｜供应商成本 USD·缓存命中（provider）或 内部积分·内部金额 CNY（internal）｜质量评分（加权均值，前端由 providers[].quality_score 按请求加权）｜平均耗时（P50/P95）｜日均请求 | `RangeReport.totals` + 前端计算 |
| 趋势 ×2 | ① 请求与成本趋势：按天堆叠柱（成功/失败）＋ 成本/积分折线（右轴）；② **Token 构成与缓存命中**：堆叠面积（输入/输出/缓存读/缓存写）＋ 命中率虚线（右轴）——完整对标参考图 Token 使用趋势 | `RangeReport.days[]`（`ReportDayRow.totals` 已含全部所需字段：input/output/cache_read/cache_write/cache_hit_ratio/latency） |
| 分布 ×2 | ① 按供应商（provider 视角）/按租户（internal 视角）：名称＋占比条＋请求数＋Token＋成本/积分＋失败率＋质量/占比，行点击=带该维度过滤重查（下钻）；② 按模型：＋P95＋缓存命中，失败原因改为 **badge 列表（Top2 徽章 + tooltip 全量）** | `providers/tenants/models` |
| 失败原因卡 | 区间失败原因 Top 横条图（badge + 占比条），点击联动过滤模型表 | `totals.error_breakdown` + 各行 `error_breakdown` |
| 按人员 | internal 视角独有表（占比条化） | `persons[]` |
| 按天明细 | 保留为可折叠表（默认折叠，趋势图已承载时序阅读） | `days[]` |

### 3.2 实施要点

- 组件改造集中在 `ReconciliationReport.vue` 重写模板层 + 新组件 `DistributionTable.vue`；脚本层逻辑（refresh/export/rerun）保持。
- 「按 Token/按金额」切换：纯前端切换排序与占比条基准（参考图同款交互）。
- 下钻：点击供应商行 → 设置工具栏筛选 + 重查（URL query 同步 `provider_id`，支持浏览器回退）。
- 测试：`ReconciliationReport.test.ts` 扩展（视角切换、指标切换、筛选参数拼装、快照缺失态）；`pnpm element:check`/`color:check` 必过。

### 3.3 2026-09-30 落地记录（审计后）

P1 已写进前端，未改后端。与本节原稿的偏差以代码和测试为准：

- 趋势图走 `ReconciliationCharts.vue` + `useChart`（chart.js），没有扩展 `TrendLineChart.vue`。页面测试把图表组件桩掉；`ReconciliationCharts.seriesdata.test.ts` 挂载组件并断言 chart.js 构造参数里的序列，jsdom 不画 canvas。
- 占比条分母是区间 `report.totals`（文案「占所选范围总额」）。效果图脚本里的 max 归一化没有采用。
- 下钻用 `router.push` 写 `view` / `provider_id` / `model` / `tenant_id` / `person`。日期快捷区间不进 URL，浏览器后退不会恢复日期窗口。
- `snapshot_dates: []` 时趋势不画点。`undefined` 仍保留全部天数（旧 `pickCoveredDays` 语义未改）。
- 耗时卡的值是 `latency_p50_ms`，文案是 P50；副文案只写 P95。`ReportTotals` 没有均值字段。
- 导出固定 summary，分组随供应商/租户视角。旧页的 6 维主表、列选择器和 summary/daily 切换不再作为主交互。凭证 / 租户 / 人员 / api_key 仍在「更多筛选」。
- 非平台运营视角不显示 USD，第三张卡改为缓存命中。页面测试没有登录成平台运营，USD 卡只在 `providerDist(..., showCost: true)` 单测里覆盖。
- 慢响应用 `fetchGen` 丢弃。按天明细默认不挂载表，展开后才出现。
- `element:check` 通过。`color:check --strict` 仍被无关页面（AutoTuning / TaskProfile 等）打红，对账文件不在这份违例里，没有改颜色基线。`useLiveStreamUrl.test.ts` / `useSessionSummaryJump.test.ts` 的 vitest 5 `Mock` 泛型已改；这不等于全量 `vue-tsc` 已经通过。
- 2026-09-30 晚间用 Vite 开发服务打开过页面。那次代理的是本机接口，不是镜像里的旧构建。
  - 下钻 MiniMax：地址变为 `?view=provider&provider_id=14`，请求数 12,317 → 285。后退后地址无 query，请求数回到 12,317。
  - 展开「按天」：2026-09-23 至 09-29 共 7 行。其中 09-29 请求为 0 且不在 `snapshot_dates` 里，是补零日。当时把「7 行」当成展开成功，没有标出这一行。
  - 空区间卸掉 canvas 后，卡片内可见「暂无数据」。根因是 `chart.destroy()` 会清掉 `v-show` 的 `display: none`。
- 同日后续：看板日期预设的 `last7d` 结束日是今天。对账若直接复用，首屏会请求尚未聚合的当天。`reportRange` 缺省是 UTC 昨天往前 7 天。对账预设改回这个窗口，预设里不提供「今天」。跨度上限 367（含首尾），对应 `end-start > 366 days` 才拒绝。2026-09-30 22:30 在 Vite `127.0.0.1:5781` 打开页面，触发器为「近 7 天 09/23–09/29」，预设只有昨天 / 近 7 天 / 近 30 天 / 本月 / 上月。该 Vite 的 `/api` 代理指向 `localhost:8781`，summary 返回 500 且响应体为空，所以那一次按天表和趋势图没有在浏览器里看到。这只说明当时的 Vite 代理打错了进程，不能当成 8782 的现状。
- 租户表「占比」列原先固定为请求占比。现与占比条同一分母。请求占比仍留在请求数副文案。
- 按天表对不在 `snapshot_dates` 里的补零日显示「未聚合」。有快照的真零日不标。`snapshot_dates` 为 `undefined` 时不标。
- Token 构成图的 `fill: true` 在 Chart.js 4 里等于 `origin`。改为 `fill: 'stack'`。命中率折线仍是 `fill: false`。测试只断言传给 Chart 的 `fill`，不看像素。
- 方案 B/C 已落地，偏差见 §4.3 / §5.4。这次提交还没进 8782 镜像。
- 2026-09-30 23:50 复核：本机 8782 `healthz` 为 `2.5.8-f51afc9f-20260930-2361`。已发布的 `assets/ReconciliationReport-CR-skEl7.js` 含 `367` 与预设 id `yesterday` / `last7d` / `last30d` / `thisMonth`，不含 `today` 与 `last14d`。上一轮在 build 2360 上点过「昨天」，请求数从 8462（7 天）变为 1739（1 天）。本轮没有在 2361 上重做这次点击，也没有在 2361 上重新展开按天表去看「未聚合」字样。
- 预设停在昨天只约束预设按钮。自定义开始/结束仍能选今天和未来，`reportRange` 也不拒绝 `end` 晚于昨天。本轮给对账页的 `KxDateRangePicker` 增加可选 `notAfter`（UTC 昨天）：日历格禁用，草稿晚于该日时「应用」不可用。不传该属性的看板选择器行为不变。8782 上正在跑的 2361 包还没有这个限制，要等这次源码重新部署才生效。
- UTC 当天是 1 号时，「本月」解析为昨天所在月的 1 日到昨天，按钮文案仍是「本月」。日期契约由 `snapshotRange.test.ts` 锁住，文案没有改。北京时间 0 点到 8 点，「昨天」是 UTC 昨天，比本地昨天再早一天。日期窗口仍不进 URL。预设在组件创建时取一次 `Date.now()`，挂着跨过 UTC 日界不会自己刷新。

---

## 4. 方案 B —— 租户详情 `/tenants/:code` 统计 + 概览（P2，含后端）

### 4.1 后端：`getTenantStats` 增加 `daily` 时序

`GET /api/admin/tenants/{code}/stats?days=N` 响应新增：

```go
Daily []TenantDailyStat `json:"daily"` // 按天时序，长度=days（无流量日补零行）
type TenantDailyStat struct {
    Date     string  `json:"date"`      // YYYY-MM-DD
    Requests int64   `json:"requests"`
    Success  int64   `json:"success"`
    Errors   int64   `json:"errors"`
    Tokens   int64   `json:"tokens"`
    Credits  int64   `json:"credits"`
    CostUSD  float64 `json:"cost_usd"`
}
```

- SQL：对 `request_logs_with_current_month`（近期窗口读面铁律）`GROUP BY date_trunc('day', ts)`，与既有 `by_model` 同一查询上下文复用 WHERE；
- 同步更新 `web/src/api/admin.ts` 的 `TenantStats` 类型 + 745/746 迁移头注无需动（无 DDL）；
- 测试：`admin` 包新增 stats daily SQL 测试（含跨月窗口、零流量补零、test-tenant 隔离）。

### 4.2 前端：统计 tab 重做 + 概览 tab 升级

统计 tab（对照 [tenant-stats.html](tenant-stats.html)）：

| 区块 | 内容 |
|------|------|
| 时间窗 chips | 7/30/90/365（替换现有 select，交互不变） |
| KPI ×8 | 总请求（失败副指标）｜总 Token（入/出/缓存）｜总费用（积分主值 + 成本 USD 副值，FeeCostCell 口径）｜日均请求（环比）｜独立密钥｜独立模型｜独立应用｜平均耗时 |
| 趋势 ×2 | 请求与失败趋势（堆叠柱）；Token 与积分趋势（堆叠面积 + 积分右轴线） |
| 分布 ×2 | 模型分布 Top20、应用分布 Top20 —— 占比条 +「按 Token/按积分」切换 + 成本列（平台运营视角门控 `isPlatformOpsView()`，与现状 FeeCostCell 一致） |

概览 tab：6 张 KPI 卡升级为 StatCard 扩展形态（icon + 主值 + 环比/构成副指标 + 7 天 sparkline）。数据源：`tenant.requests_7d` 等既有字段 + 概览加载时顺带请求 `stats?days=7` 取 `daily` 画 sparkline（一次轻查询）。

计费审计 / 钱包 / 账本 tab 本轮不动（已有 KPI 汇总行），仅样式令牌对齐。

### 4.3 落地偏差

- `daily` 已在 `getTenantStats` 返回，并补零。积分查询同时给出入/出/缓存读写和 `avg_latency_ms`。
- 日均请求的环比是序列里最后一天相对前一天，不是上一等长窗口。租户统计查询已经顶着 10 秒预算，不再加一条上一窗口聚合。
- Token 趋势是总量面积加积分柱，不是入/出/缓存的堆叠面积。按天序列没有这四列。
- 应用分布行点击后切到密钥 tab，并按 `application_code` 过滤。
- 概览 sparkline 用近 7 天 `daily`。副指标是失败数和日均，没有再打一版环比。

---

## 5. 方案 C —— 用户列表统计 + 用户详情（P3，含后端）

### 5.1 前端

用户列表（对照 [users.html](users.html)）：

- **顶部统计条**（mini-stats）：总用户 / 活跃（30 天内有请求）/ 管理员 / 已禁用 / 近 30 天请求 / 近 30 天积分消耗；
- **表格增强**：新增列 近 30 天请求（带占比条）/ Token / 积分；「最后活跃」列（区别于最后登录）；用户名+显示名合并为主列；
- **行点击 → 用户详情抽屉**（复用 `components/ui/AppDrawer.vue`）：
  - KPI ×4：请求数 / Token / 积分消耗（+内部金额）/ 失败率·P95；
  - 时间窗 chips（7/30/90）；
  - 请求与 Token 趋势（双线，Token 右轴）；
  - Top 模型 / Top 应用 两小表；
  - 最近请求 5 条（时间/模型/首字·总耗时/积分/状态 badge），底部「在日志中查看全部」深链到日志页带 owner 过滤；
  - 基本信息 kv + API 密钥数（链接到租户密钥 tab）；
  - 操作：启用/禁用、重置密码（现有能力收进抽屉，列表内保留快捷按钮）。
- 无用量数据的用户：列显示 `—`，抽屉统计区显示空态文案（「该账号名下密钥近 N 天无请求」）。

### 5.2 后端：两个新端点（super_admin / tenant_admin 双层权限，与 /users 列表同口径）

**① `GET /api/admin/users/usage-summary?days=30`**（列表列 + 顶部统计条数据源）

```go
// 一条 SQL 出全量：request_logs_with_current_month GROUP BY api_key_owner_user
// 返回 items: [{username, requests, tokens, credits, last_active_at}]
// 顶部统计由前端对 items + 用户列表 reduce 得出（活跃=有用量记录且命中用户名）
```

**② `GET /api/admin/users/{id}/stats?days=30`**（详情抽屉数据源）

```go
// 由 user.id → users.username → 以 api_key_owner_user = username 过滤聚合
// 返回 {kpi:{requests,tokens,credits,error_rate,p95}, daily:[...],
//        top_models:[{model,requests,tokens,credits}], top_apps:[...],
//        recent_requests:[{ts,model,first_chunk_ms,total_ms,credits,status}]}
```

数据面选型论证（本地实证）：
- ✅ `request_logs_with_current_month`（近期窗口读面）：`api_key_owner_user` 有值（本地近 30 天 1,869/4,390 行），**MaaS 计费明细 `maas/consumption_detail.go` 同用此列**，口径一致；`credits_charged/latency_ms/stream_first_chunk_ms/raw_model_name/application_code` 全齐。
- ❌ `session_owners` 视图：仓内 schema 与本地库均无定义，user_profile.go 引用即 500 —— 本轮**不修不依赖**（其修复需另立迁移建视图，留作独立遗留项）。
- ❌ `report_snapshots` persons：维度是 `end_user_id`，非平台账号。
- ⚠️ `owner_user` 列本地全空，不可用；以 `api_key_owner_user` 为准（**执行前在 245/154 生产跑一条命中率 SQL 验证**，本地库流量以探针为主不能代表生产）。

权限与租户：tenant_admin 只见本租户用户（沿用现有 `getUsers` 过滤面）；统计端点对非 super 强制 `tenant_id = GetTenantID(r)`。

### 5.3 测试

- go：新端点 handler 测试（三层权限 / 无用量用户 / tenant 隔离 / SQL 用 test-tenant fixture）；
- 前端：`UsersView.test.ts` 扩展（统计条渲染、用量列、抽屉打开与数据加载、空态）；新 `UserDetailDrawer` 组件测试。

### 5.4 落地偏差

- 读面是 `usage_facts` × `api_keys.owner_user`，不是 `request_logs_with_current_month`。后者全量 GROUP BY 在本地要几十秒。
- 抽屉「在日志中查看全部」打开 `/request-logs?owner_user=<username>&preset=d7`。日志列表只按 `request_logs.api_key_owner_user` 等值过滤，避免给 super_admin 的计数查询加上 `api_keys` 连接。
- API 密钥数链接到 `/tenants/:code?tab=keys&owner=<username>`。
- 最近请求仍返回 10 条。用户列表统计列固定 30 天；抽屉内可切 7/30/90。
- 抽屉行为由 `UsersView.test.ts` 覆盖，没有单独的 `UserDetailDrawer.test.ts`。

---

## 6. 实施顺序与工作量

| 期 | 内容 | 预估 |
|----|------|------|
| P1 | 对账页改版（纯前端 + 测试 + i18n） | 已落地，见 §3.3 |
| P2 | 租户统计（后端 daily + 前端两 tab + 测试） | 已落地，见 §4.3 |
| P3 | 用户统计（后端 2 端点 + 前端列表/抽屉 + 测试） | 已落地，见 §5.4 |
| 收尾 | 三页联测（明暗双主题截图）→ 8782 部署实测 | 未部署 |

部署链（既有约定）：go-3 提交推送 → 兄弟仓 llm-gateway-go ff 拉取 → `deploy-local.sh`（P2/P3 含后端变更必须走此链，禁直接跑二进制）。

## 7. 风险与边界

1. **快照 T+1**：对账页/internal 视角无当日数据（既有行为，KPI 副指标「数据截至」标注）；趋势图跨月区间（>当前月）需回退读裸父表分区——对账页 days 数据来自快照无此问题，租户/用户统计走 `*_with_current_month` 视图已覆盖。
2. **成本可见性**：所有 USD 成本列沿用 `isPlatformOpsView()` 门控 + FeeCostCell；internal 视角积分口径不受门控影响。
3. **并行会话**：本仓多线并行，实施时 hunk 级暂存、只碰本方案文件清单；`docs/proposals/2026-09-30-stats-ui/` 为本轮交付物不删。
4. **生产数据命中率**：方案 C 依赖 `api_key_owner_user = users.username` 的生产命中率（本地仅 admin 命中，114 行；本地流量以探针为主）——P3 动工前先在 245/154 验证，命中率低则用户列表仍交付（列显示 —），详情抽屉以「该用户密钥清单 + 密钥级用量」兜底展示。
5. 视觉模型当日限流，参考图为 OCR 还原——效果图与原图如有出入，确认时指出即改。

## 8. 待确认决策点

P1–P3 已实施。下面 1–3 按本稿推荐落地：抽屉、统计列默认 30 天、抽屉内 7/30/90。

1. 效果图整体方向与密度是否符合预期？（尤其对账页 KPI 从 3 卡扩到 6 卡、按天明细默认折叠）
2. 用户详情用**抽屉**（当前效果图）还是独立详情页路由（`/users/:id`）？推荐抽屉（浏览快、免新建路由层级），需要分享深链可加 `?user=<id>` query 支持。
3. 方案 C 的统计列默认窗口 30 天是否合适？（可选 7/30/90 全局切换）
4. 实施分期 P1→P3 顺序确认后即动工；如需并行动工请指出。
