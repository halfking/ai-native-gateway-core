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

1. **api_key 档长窗性能**：月度分区无 api_key_id 索引（分区索引是逐月建的，新分区也不会自动带），7d+ 窗在数百万行分区上可能数十秒直至 45s 超时。根治方向二选一：给 request_logs 分区补 `(api_key_id, ts)` 索引（迁移+部署清单三处同步）；或 rollup 加 `api_key×model` 复合维（新 dim_type，写入方+回填一轮）。**在根治前，前端超时提示只有通用 loadFailed 文案，未引导用户缩窗。**
2. **未部署**：245/154 与本地 8782 均未上本功能（截至本文落笔 main=2814bde51+本轮审计提交）。llm.kxpms.cn 要看到效果需走部署清单。
3. **浏览器级 UI 未验证**：本轮门禁止于 vue-tsc/vite build/vitest 与真实库 SQL；实际渲染（canvas 尺寸、radio 溢出、深链往返）需部署后人工或浏览器实测确认。
4. 深链仅初始化读一次 query，浏览器前进/后退不会重放过滤器（低频场景，登记即可）。

## 门禁记录

- `go build ./...` ✓；`go test ./admin/` ✓（含 S4 读面守卫、折叠/透视/分档单测）
- `pnpm build`（menu-config 导出 + element-import-audit + vue-tsc + vite）✓；`vitest run` 154 文件 / 1112 测试 ✓
- 真实库 11 用例（临时门控测试，已删）：全过，数据见上表
