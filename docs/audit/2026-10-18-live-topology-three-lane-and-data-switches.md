# 2026-10-18 /live 实时拓扑改版：数据开关 + 三带网关 + 连线修复 + 分页面板

> 会话目标（owner 原话要点）：默认去除探测请求与无效供应商/节点（可做成开关，
> 默认不选）；客户端对网关核心的连线没有，协议要小字标注或着色；请求与供应商
> 响应要滚动输出、分页最多一页 50 条、点击看详情；筛选条件一行、字号更小且
> 一致；网关核心模块按处理流程位置排布、显示在用插件、进/出两条链路、存储
> 转发效果。完成后本地部署验证、提交推送。

## 一、七项需求的落点

| 需求 | 落点 | 判据 |
|---|---|---|
| 默认去探测（开关默认不选） | `TopoFilters.showProbe`（默认 false）→ `matchesFilters` 在 `applyFilters` 里剔除 `is_probe`；URL `?probe=1` 打开 | filter.test「hides probe by default / shows when on」；view.test「hides probe traffic everywhere + banner」 |
| 默认去无效供应商/节点（开关默认不选） | `invalidNodeKeys()`（熔断打开/不可达/`manual_disabled` → down）→ 同一集合喂给过滤（剔流量）与 `buildTopology.hiddenNodes`（不建节点） | filter.test「hides requests via invalid nodes」；graph.test「hiddenNodes removes nodes+edges / 不连坐同供应商健康节点」 |
| 客户端→网关连线 | **根因是布局缺陷**：`computeLayout` 跳过 gateway 列，`gateway:core` 从不在 `byId`，所有以它为端点的边在 `if (!a||!b) continue` 被静默丢弃（图上只剩 provider→model）。修复：合成网关布局节点（矩形=大方块），边锚到方块顶/底边并按对端 x 散开 | layout.test「★ keeps client→gateway and gateway→provider edges」（修复钉子）+「anchors spread」 |
| 协议标注/着色 | `bumpEdge` 在 client→core 边聚合 `protocolCounts`；画布渲染 DOM 小字 chip（`协议 ×N`，固定色板 openai/anthropic/gemini/openai-responses，未知协议稳定哈希取色）；边线按主导协议着色（失败红优先） | graph.test「aggregates client_protocol on client→gateway only」；view.test「protocol chips」 |
| 分页 ≤50/页 + 点击详情 + 滚动 | `PANEL_PAGE_SIZE=50`；两面板内置翻页（‹ › + 第 x/y 页 · 共 N 条），列表本体滚动、翻页回顶；数据源不再预截断（原 `PANEL_LIST_LIMIT=60` 删除）；左右面板点行都打开 `RequestZoomPanel`（内联，不弹窗） | RequestPanels.test 分页组（50/50/20、首末页禁用、点行 emit zoom） |
| 筛选一行 + 小字号一致 | `.live-topology__filters` ≥1024 nowrap；全部控件 11px / 24px 高（el-select 走 `--el-font-size-base`，原生 select/checkbox/按钮同尺寸）；新增两个数据开关勾选框 | view.test「both toggles default to unchecked」 |
| 网关三带 + 插件 + 进出双链 + 存储转发 | `GatewayCoreBox` 重写：进站链（缓存→脱敏→**插件带**→路由→节点状态→⬇派发）／存储转发带（真实 pipeline 深度徽标 + 写入⇄文件存储 + ↕ 流动连接）／出站链（任务识别→错误记录→探测任务，旁路虚线）。模块格从绝对定位改正常文档流（三带几何 `box.lanes` 由布局函数给判据用） | GatewayCoreBox.test 三带归属/DOM 顺序/插件分带/队列徽标（null≠0） |
| 在用插件显示 | 新端点 `GET /api/admin/topology/plugins`：builtin=治理管线 `security.Registry.List()`（经 `v2DispatchDeps.SecurityRegistry` 回传**真正拦流量的那个注册中心**），external=`pluginruntime.Registry.Snapshot()`（新加，含 failed/degraded）。装配缺失=200+degraded 信封（不是空清单） | admin/topology_plugins_test 三条；plugin-runtime registry_test Snapshot 判据 |

## 二、本轮钉住的坑（判据纪律增量）

1. **「连线没有」的真因不在画布在布局**：canvas 的 drawEdge 一直会把边画出来，
   丢边发生在 `computeLayout` 的端点解析。若只在画布层「补画」客户端连线，
   就是症状层修补。判据直接钉 `layout.edges` 必须含 `client:*->gateway:core`
   与 `gateway:core->cred:*`（M3 变异复现原始缺陷即红）。
2. **隐藏语义必须收敛在过滤函数内**：第一版 `matchesFilters` 只看调用方传不传
   `hiddenNodes`，`showInvalidNodes=true` 时若调用方忘了停传集合就双重过滤。
   判据抓住后改为**函数内同时看开关与集合**（开关开=传了集合也不隐藏）。
3. **无效≠warning**：配额耗尽/降级（warning）仍在服务，藏掉它会让「半死」
   供应商从视野消失。`invalidNodeKeys` 只收 down（熔断打开/不可达/手动停用），
   `manual_disabled` 并入 down 口径，判据逐节点点名。
4. **隐藏了多少必须说出口**：`hiddenProbeCount`/`hiddenInvalidCount` 横幅与
   `droppedByWindow` 同层同款（否则「图上比预期少」会被读成丢数据）。
5. **队列深度 null≠0**：管线未上报时徽标整体不出现；depth 0 是真值（空队列）
   渲染为非忙碌态。两条都有判据。
6. **变异姿势也要验证打没打上**：M4 第一版只改 `lane` 字段没挪数组、M8 跑错
   测试文件（视图测试被 URL 同步覆盖默认值，本就不该红）——两次「全绿」
   都是变异没生效，靠应用校验（grep 变异结果）+ 换对判据文件才验出真牙。
   **变异验证脚本的每一步都要自证「变异确实在场」。**

## 三、验证记录

- 后端：`go build ./...` rc=0；`go vet ./admin ./plugin-runtime ./cmd/gateway` rc=0；
  `go test ./admin -run TestTopology` ok；`plugin-runtime` 全绿（含新 Snapshot 判据）。
- 前端：`vue-tsc --noEmit` 0 错；live-topology 相关 980 条全绿；全仓 2663 条
  仅 4 条红（parity×2 / navCoverage×1 / cjk 棘轮×1），与 `git stash` 后基线
  **逐条一致**（上游遗留，AGENTS 0.0 节在案）。
- i18n：`i18n:check` 0 缺失键（8 locale 同构）；CJK 计数 5757 与合并前
  **逐位一致**（本轮净贡献 0）。
- 变异验证 8 处全红：M1 删探测过滤 / M2 删无效过滤 / M3 删网关合成节点 /
  M4 route 挪带 / M5 删协议聚合 / M6 页大小 100 / M7 不传插件 / M8 默认开关翻转。

## 四、浏览器验收轮（免凭据路径，DOM 实读 + 视觉模型）

环境照抄 2026-10-18 凌晨轮配方：`/tmp/dev-topology-stub.mjs`（本次补了
`/api/admin/topology/plugins` 桩）+ `vite --port 5199` + `/dev/live-topology-preview`。
1600×900 视口，DOM 查询为主、截图一张过视觉模型。

**DOM 级确认**：

| 项 | 观察值 | 意义 |
|---|---|---|
| 三带 | IN（Cache0→Sanitize33→插件带→Route33→Node state2→⬇）/ Store（write0⇄file0，↕ 连接）/ OUT（⬆Task→Error→Probe+output 插件） | 三带归属与顺序正确 |
| 插件 chip | sanitize_pattern、sensitive_word_input、ai-session-manager(ready)、broken-one(**failed**) 在 IN；output_compliance 在 OUT | 按 direction 分带；failed 外部插件不被藏 |
| 协议 chip | `anthropic ×16`、`openai ×14`、`anthropic ×3` | client→gateway 边小字标注成立 |
| 隐藏横幅 | `9 probe requests hidden · 38 requests via invalid nodes hidden` | 数字与夹具对账吻合（76=33+9+38−4 重叠） |
| 无效节点 | zs-key-d（unreachable）默认不在图上；qz-key-c（warning）保留（在折叠节点内） | 无效≠warning 口径成立 |
| 开关交互 | invalid 开 → 33→50 行（分页上限恰好生效）；双开 → 76 条、横幅消失、URL=`?probe=1&invalid=1`；还原 → 33 | URL 往返 + 分页封顶 |
| 点击详情 | 左栏点行 → RequestZoomPanel 打开，protocol/status 均为译文非原始键 | （修复后）|
| 筛选栏 | 全控件 11px；整行无裁切（lastRight 1084 < barRight 1573） | （修复后）|

**验收抓到并当场修掉的三个真问题**：

1. **7 个语言包 `zoom.protocol` 插错块**：批量脚本用「第一条 `client:` 行」当
   锚，而 `column.client` 排在 `zoom.client` 之前 —— protocol 落进了 column 块，
   浏览器里详情面板显示原始键 `liveTopology.zoom.protocol`。修复脚本改为
   锚定 zoom 块且**自证落点**（找 protocol 行号必须在 zoomStart 之后、只出现一次）。
   ⇒ 批量改语言包的脚本必须自证锚点命中，否则「打上了」和「打对地方了」
   是两件事——单元测试全绿没抓到它（测试只用 zh-CN，zh-CN 是手工插对的）。
2. **存量 bug：zoom 面板 in_progress/rate_limited 状态渲染原始键**——
   `t(\`liveTopology.filter.${status}\`)` 直接拼 snake_case，文案键是 camelCase。
   补 `statusLabel()` 映射 + 逐枚举值判据（`not.toContain('liveTopology.')`）。
3. **两个视觉缺陷（视觉模型从截图抓到，DOM 判据抓不到）**：
   - 筛选栏右端被裁（与标题同行时 1600 宽只剩 ~1010px，控件需 ~1110px）→
     过滤栏独占第二行 + nowrap + 极窄横向滚动兜底；
   - 大方块纯比例取高，1723px 高画布下拉成 1000px+ 空框 → 高度夹在
     内容紧包裹上限（320px）并在客户端/供应端行之间垂直居中，判据钉
     `box.h ≤ 320`。

## 五、遗留（登记不修）

- `probeTask`/`taskDetect` 语义上是请求侧观测，本轮按维护者给定的流程顺序排在
  出站链（响应侧）——模块带归属是**呈现口径**，若 owner 要求严格按处理阶段
  归位，只需改 `GATEWAY_{IN,OUT}_MODULES` 数组（M4 判据会守住挪动）。
- 插件清单挂载时拉取一次（插件集合只在启动/安装时变化）；安装/卸载后需刷新
  页面才更新。若要实时，接 SSE 事件即可（登记）。
- 真实浏览器肉眼验收：本地部署后经 `/dev/live-topology-preview`（免凭据）复核。
