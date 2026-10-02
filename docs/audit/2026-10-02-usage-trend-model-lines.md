# 2026-10-02 看板「用量趋势」按模型分线 + 全页用量趋势分析（含批判式审计轮）

## 需求

1. `/dashboard` 看板「用量趋势」卡按模型分线展示；指标（请求数 / Token / 积分 / 成本）可切换；能聚焦查看单个模型的用量变化。
2. 时间范围选择器右侧新增「更多」按钮 → 打开全页用量趋势图（`/admin/usage-trends`），支持供应商 / 租户 / API Key / 时间范围 / 模型 / 指标过滤。

交付提交：`6428851ec`（功能）+ `2814bde51`（合并并行）；审计轮修复见文末。

## 方案定形（含被否决的路径）

### 后端 `GET /api/admin/usage/trend-series|trend-models`（admin/usage_trend_series.go，经 HandleUsageAdmin 分发，admin 鉴权 + statsTenantScope 租户钳制）

三档读面按过滤条件自动选择：

| 过滤条件 | 数据源 | 模型名口径 | 本地真库实测 |
|---|---|---|---|
| 无 provider/api_key | `request_stats_dim_minute`（dim_type='model'） | dim_key = outbound→client_model→`__unknown__` | 今天 2ms / 7d 0.6s（22K 行）/ 30d 0.5s（40K 行） |
| 仅 provider | `request_stats_minute` × `provider_models`（DISTINCT ON 取名，无映射显示 `model#<canonical_id>`） | provider_models.outbound_model_name→raw_model_name | 7d 80ms / +model 过滤 23ms |
| 含 api_key | `request_logs_with_current_month_without_customer_id` | 同 dim 档 | 9/24 单日 key727 17,421 行 2.97s；**7d 长窗在无 api_key 索引的月分区上会到数十秒级**，handler 45s 超时兜底 |

**被否决的路径（有实测依据，勿走回头路）：**
- SQL 内 ranked/picked CTE 折叠：detail 档双扫实测 75s（本地 9 月分区 215 万行、分区无 api_key_id 索引），单扫+Go 折叠砍半后弃用 CTE。
- usage_ledger 当 detail 源：无 credits_charged 列，四指标不齐。
- 给 rollup 加 api_key 维：单维 rollup 无法做 api_key×model 交叉（图表需要的是该 key 下按模型分线），复合维是另一轮的工程量。

**折叠实现**：三档统一单次扫描出 (model,bucket) 行 → Go 侧 `foldUsageTrendRows` 按窗口总量取 top-N，未入选行**改写**为 `__others__`（不是丢弃——首版实现真的丢过，被单测抓住）；model 过滤已定时 WHERE 收敛单模型、跳过折叠。`__others__` 图上灰色虚线。

**降级**：缺 rollup 表/缺视图 → IsMissingRelationError → 空序列 200（全新安装不炸）。
**读面登记**：新读 request_logs 视图的文件必须进 `requestLogsReadInventory`（admin/request_logs_read_inventory_test.go，S4 守卫，计数 = `FROM request_logs*` 行数）。本文件计 2。

### 前端

- `ModelTrendChart.vue`（新，看板卡与全页共用）：单指标多模型线；`__others__` 虚线灰；y 轴紧凑刻度（12.3K/4.5M）；**legend 隐藏态按模型名记入 Set、initChart 销毁重建后回放**（useChart 的 initChart 是 destroy+recreate，看板卡 60s 节流跟随刷新会把用户手动隐藏的模型打回——审计轮修复）；画布尺寸走 CSS var + `!important`（Chart.js responsive 会改写内联 style，与 TrendLineChart 同模式）。
- `BoardUsageTrendSection.vue` 重写：指标单选（4 档 radio）+ top6 多线；自取数不进 board 缓存载荷；时间范围变化即刷、board 轮询按 60s 节流跟随。
- `BoardFilterBar.vue`：时间范围右侧「更多 ›」按钮（bfb__more）；BoardPanel 带 start/end 深链跳 `/admin/usage-trends`。
- `UsageTrendExplorer.vue`（新，`/admin/usage-trends`，meta.requiresAuth）：六维过滤全部同步 URL query（深链/分享）；模型下拉选项随其余过滤条件由 trend-models 端点联动；汇总表含当前指标占比列。清空值统一 truthy 防护（EP clear 产出 undefined/''/null 因版本而异，防 `provider=` 空参残留）。
- 菜单「模型与路由」组挂入口（super + hideForTenant）；menu-config.json 构建时再生成（顺带追平了此前 appNav 已有但导出陈旧的「会话分析中心」条目）。
- i18n：`usageTrend` 命名空间 + `dashboard.board.trendMore/trendMoreTitle/trendOthers` + `nav.item.usageTrends`，8 语言同步。

## 批判式审计轮（本文件同日第二轮）发现与处置

| # | 发现 | 严重度 | 处置 |
|---|---|---|---|
| 1 | 首轮「SQL 已在真库验证」实为手敲近似 SQL，Go 拼出的真实 SQL 从未执行（集成测试因无 llm_gateway_test 库全 SKIP）——声明与事实不符 | 高（验证债） | 已补：临时 env 门控测试（TREND_E2E_DSN）直调 queryUsageTrendRollup/Provider/Detail 及三档 models 函数于真实库，11 用例全过（上表实测数据即来自此轮），验证后删除临时文件 |
| 2 | api_key 档从未有非空结果证据（本地近期无带 key 流量，0 行结果无法区分「对了但没数据」与「过滤错了」） | 高（验证债） | 已补：9/24 窗（key727 当日 12 万行）→ 17,421 行非空；另核实该 key 流量 99.9% 在 request_status 三态内，过滤条件本身无误 |
| 3 | ModelTrendChart 画布高度用内联 style，Chart.js responsive 会改写内联尺寸（仓库已验证模式是 `!important` CSS） | 中（塌陷风险） | 已修：CSS var `--mtc-h` + `height: !important`，对齐 TrendLineChart |
| 4 | legend 隐藏态在图表重建后丢失（initChart=destroy+recreate；看板卡 60s 跟随刷新即复现），「只看某模型」核心体验被打回 | 中（UX 缺陷） | 已修：hiddenModels Set + 重建后 applyHiddenState 回放 |
| 5 | syncQuery 在清空过滤器后可能写空参（`provider=`）入 URL；EP clear 值因版本而异 | 低 | 已修：truthy 统一防护，query 全量重建 |
| 6 | provider 档 source 标识 `request_stats_minute` 未映射文案，UI 裸显英文标识 | 低 | 已修：并入 sourcePg 文案 |
| 7 | 30 天窗 dim 档返回行数未测过（盲区，最坏模型×桶组合膨胀） | 低 | 已测：30d 40K 行 0.5s，Go 折叠零压力 |
| 8 | provider 档模型名为 canonical 命名空间（provider_models），与无过滤档的 outbound 命名空间存在跨档不一致：先选模型再加供应商过滤时名字可能对不上→空图 | 中（已知限制） | 未修（行为可解释：两档本就是不同读面）；全页模型下拉随过滤联动刷新可自然规避。登记为遗留 |
| 9 | `model#<id>`（canonical 无映射）在 provider 档大量出现（本地 key 流量多落 canonical_id=0） | 中（观感） | 未修：显示是真值（该供应商下未映射 canonical 的流量），命名口径属数据治理（见 [[standard-model-name-cleanup]]），不在本轮 |

## 遗留风险与建议

1. **api_key 档长窗性能**：月度分区无 api_key_id 索引（分区索引是逐月建的，新分区也不会自动带），7d+ 窗在数百万行分区上可能数十秒直至 45s 超时。根治方向二选一：给 request_logs 分区补 `(api_key_id, ts)` 索引（迁移+部署清单三处同步）；或 rollup 加 `api_key×model` 复合维（新 dim_type，写入方+回填一轮）。~~在根治前，前端超时提示只有通用 loadFailed 文案，未引导用户缩窗。~~ **2026-10-02 下午已补前端引导**（见下「遗留项跟进」§2），索引根治评估结论为暂不实施、蓝图就绪（见下 §3）。
2. **未部署**：245/154 与本地 8782 均未上本功能（截至本文落笔 main=2814bde51+本轮审计提交）。llm.kxpms.cn 要看到效果需走部署清单。→ **2026-10-02 下午本地 8782 已部署 d3485a431**（见下 §1）；245/154 仍未上。
3. **浏览器级 UI 未验证**：本轮门禁止于 vue-tsc/vite build/vitest 与真实库 SQL；实际渲染（canvas 尺寸、radio 溢出、深链往返）需部署后人工或浏览器实测确认。→ **2026-10-02 下午已实测**，结论见下 §1。
4. 深链仅初始化读一次 query，浏览器前进/后退不会重放过滤器（低频场景，登记即可）。

## 遗留项跟进（2026-10-02 下午轮）

### 1. 本地 8782 部署与浏览器实测

**部署事实（2026-10-02 13:50–14:05）**：main d3485a431 已上本地 8782，seq 2390（`2.5.8-d3485a43-20261002-2390`）。部署走干净 git worktree（d3485a431 检出 + 修 deploy-lib 软链 + 拷 .env.local + 显式真实库 DSN 覆盖），**未从 sibling 工作树部署**——当时 sibling 有并行会话 5 分钟前的未提交 Go WIP（session_analytics 等），直接 deploy 会把半成品编进共享网关。核验全绿：/healthz 200、/version=d3485a43#2390、部署前后容器 env diff 为零（SECRET_KEY/CREDENTIAL_ENCRYPTION_KEY 哈希一致，无 09-18 式 secret 漂移）、页面引用 chunk hash 与 slot 2390 一致、deploy 自带 VERIFY_PASS=1（admin 登录+凭据解密冒烟）。注意：sibling 的 .env.local DSN 已被并行 RLS 会话改为 `llm_gateway_test`，本次照 09-18 先例以 run/*.env 的真实 DSN 显式覆盖部署。

**浏览器实测矩阵（IAB 内嵌 Chromium，admin 登录态）**：

| # | 项 | 结论 |
|---|---|---|
| 1 | 看板卡多线渲染 | ✓ legend 7 项（top6+Others 虚线）、y 轴刻度、x 轴 5min 桶、__others__ 灰虚线 |
| 2 | canvas 尺寸 !important 修复（审计 #3） | ✓ 内联 style height=284px 被压回 268px（CSS var --mtc-h 生效），Chart.js responsive 改写被 !important 压住 |
| 3 | 指标切换 | ✓ Requests→Tokens：radio checked 翻转，y 轴从 10~18 变 5K~40K 紧凑刻度（compactTickValue 生效） |
| 4 | legend 点击隐藏 / 隐藏保持回放（审计 #4） | **✗ 未通过——图表交互层不响应（新发现，见下）**；回放修复因此无法在浏览器层验证 |
| 5 | 「更多」深链 | ✓ bfb__more → `/admin/usage-trends?start=2026-10-02&end=2026-10-02`，范围还原 Today |
| 6 | 全页六维过滤 | ✓ provider 选择→URL `provider=2` 同步+表格联动；**清空→URL 无 `provider=` 空参残留（审计 #5 truthy 防护实证）**；apikey=727+自定义窗深链还原→detail 档 13s 返回 11 行真数据（claude-opus-4-5 6.7K req/187K tok/5.2%），source 切「Request detail」；model 下拉随过滤联动（provider=2 当日无流量→选项空，行为正确） |
| 7 | 空态 | ✓ 2026-01-01~01-03：`No data` 占位可见、0 行、无报错、深链还原 |

**新发现（本轮实测头条，P1）：图表交互层不响应。** 证据链：
- 点击探针（document 捕获层）证实**真实 isTrusted click 落在 CANVAS 元素**（坐标命中 legend box/文字，含 5 点垂直扫描），位图零变化、无 toggle；
- 合成 mousemove/click 同样无 tooltip、无反应；
- 活性探针：视口 1440→1100 后 canvas CSS 963 而 **buffer 冻结在旧值**（活实例 responsive 应跟随重算）；但切指标销毁重建后的**新实例出生时 buffer 立即正确**（963）——即「出生即活、随后失活」；
- 渲染面完全正常（多线/刻度/指标切换重绘），且指标切换路径（watch→initChart 销毁重建）本身工作。

定位线索（未定论）：useChart `initChart` 的 `Chart.getChart→existing.destroy()`+`destroyChart()` 双重销毁舞步、同一 flush 内 chartConfig deep watch 与数组 watch 双触发 refreshChart、Chart.js v4.5.1 DomPlatform `addEventListener` 先 removeEventListener 同类型再挂的语义，三者在同一 canvas 上交错后的最终实例疑似 listeners 未挂/被摘；也可能是 IAB 内嵌 webview 特有（RO/事件投递）。**待办：在桌面 Chrome/Safari 复测一次以二分环境因素；若复现，修 useChart 生命周期（一次 mount 一个实例，update 代替 destroy+recreate）。legend 回放修复（hiddenModels+applyHiddenState）逻辑本身有单测，等交互层修复后重验。**

### 2. detail 档超时引导文案（已交付）

后端 handler 的 45s 截止经 `writeInternalErr` 被折叠成固定 op 文案（`"usage trend-series query failed"`，真实 err 只进服务端日志），前端无法从报错里看到 timeout 字样。前端识别信号取「api_key 过滤生效（detail 档）+ 错误文案含 `query failed`」（UsageTrendExplorer `detailQueryFailed`），命中时在错误行下追加引导 `usageTrend.detailTimeoutHint`（8 语言同步：zh-CN/zh-TW/en-US/ja-JP/de-DE/es-ES/fr-FR/ar-SA），文案统一引导「缩短时间范围（如 7 天内）后重试」。看板卡无 api_key 过滤（dim 档实测 30d 0.5s），不需要该引导。

### 3. request_logs 分区 `(api_key_id, ts)` 索引迁移评估 —— 结论：暂不实施，蓝图就绪

**现状实证（本地真库，2026-10-02）**：
- request_logs 月分区 2026_07–2026_11 + default；2026_09 分区 2474 MB；每分区已挂 ~49 个索引。
- **api_key_id 在全部存量分区上零索引**（tenant_id 反而有 4 个 ts 复合索引，`tenant_id_ts_idx2` 形状可直接对标）；detail 档查询谓词 `api_key_id = $ AND ts >= $ AND ts < $`（+可选 tenant/provider/model）只能顺序扫——这就是 7d 长窗数十秒的根因。

**实施蓝图（仓内已有同表先例，勿发明新模式）**：728（`sql/migrations/startup/728_sql_audit_request_logs_credential_model_index.sql`）就是 request_logs 分区补 credential 复合索引的三段式迁移，`(api_key_id, ts DESC)` 版本近乎照抄：
1. 逐分区 `CREATE INDEX CONCURRENTLY ... ON request_logs_<part> (api_key_id, ts DESC)`（`\gexec` 从 pg_inherits 生成，新库无分区时自然空操作）——PG17 分区父表不支持 CONCURRENTLY（42809）；
2. 父表 `CREATE INDEX ... ON ONLY request_logs (api_key_id, ts DESC)` 壳（仅元数据锁）；未来月分区经 PARTITION OF 自动继承；
3. 幂等 ATTACH 子索引（DO 守卫，防同分区已有别名子索引时 55000）。
中断残留的 INVALID 索引必须先 DROP 再建（IF NOT EXISTS 会永久跳过，见 db/db.go ensureSqlAuditPartialIndexes）。三处同步：sql/migrations/startup/<n>（up+down）、installer embeddata/startup、installed_startup_migrations.tsv。

**建号三重查重（2026-10-02 实测）**：① 仓内 `sql/migrations/startup/` max=814（813/814 已合 main；并行会话正补其 embeddata/tsv 接线，sibling 未提交）；② `go test ./sql/migrations/startup/ -run Unique` ✓；③ 共享 252 账本（ssh 245 查 `llm_gateway_migration_checksums`）813/814 已登记、999 为测试号 → **815 当前三处全空闲**。

**暂不实施的理由**：
1. **建号窗口被并行会话占用**：813/814 的 embeddata/tsv 接线尚未收口（sibling 工作区有未提交改动，当日仍活跃）。startup 迁移在 deploy 路径上（不同于可随意重编号的 hotfix 迁移），此刻落 815 就是主动复刻 694 撞号事故（他项目抢占致自愈静默失效，见迁移建号 memory）。
2. **写放大**：request_logs 是最热写表、每分区已 49 个索引，+1 索引＝每条请求日志多一次 btree 维护；新索引估算 60–100MB/月分区（生产分区更大），换取的只有一个 admin 分析端点 detail 档的长窗加速。
3. **需要独立运维窗口**：CONCURRENTLY 逐分区构建在真库分钟到小时级 + WAL 放大，252 宿主机根盘 197G 易满；不宜搭车功能轮。
4. UX 缺口已由 §2 前端引导兜住，45s 超时兜底仍在。

**启用条件（下一位维护者可直接执行）**：等 813/814 接线会话收口 → 重新三重查重取号（预期 815）→ 按 728 蓝图落迁移（down＝逐分区 DROP CONCURRENTLY + ONLY 壳 DROP）→ 先在本地 8782 验证 detail 档 7d EXPLAIN 走 Index Scan → 再排产 245/154（252 磁盘预检先行）。

## 门禁记录

- `go build ./...` ✓；`go test ./admin/` ✓（含 S4 读面守卫、折叠/透视/分档单测）
- `pnpm build`（menu-config 导出 + element-import-audit + vue-tsc + vite）✓；`vitest run` 154 文件 / 1112 测试 ✓
- 真实库 11 用例（临时门控测试，已删）：全过，数据见上表
- 下午跟进轮：`pnpm build` ✓（超时引导文案 + 8 locale 后）；`go test ./sql/migrations/startup/ -run Unique` ✓（815 三重查重之一）；`vitest run` 154 文件 / 1112 测试 ✓（与首轮基线持平）；本地 8782 实机部署 d3485a431#2390 + 浏览器实测矩阵见「遗留项跟进」§1
