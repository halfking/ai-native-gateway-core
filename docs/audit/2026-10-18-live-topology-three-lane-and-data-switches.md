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

## 五、复审轮（2026-10-18 上午，对自己刚提交的 54e1ad4e2 批判式审计）

上游并行推进了两轮（f3610fe0f 折叠修复 + 8c4f5abbb 24h 审计轮），快进合并后
对本轮改动复审，**三个真缺陷 + 一个部署偏差**，全部当场修掉：

1. **同边双协议 chip 完全重叠**：两个 chip 都锚 t=0.45 同一点。上轮夹具
   每客户端单协议，肉眼与判据都没踩到这条路径 —— 正是判据纪律第 8 条
   「规则恒真」的又一次实证（「每边只放一个 chip」在夹具上恒成立）。
   修：沿路径错开（t=0.42/0.58）；新判据用**同一客户端混两种协议**的夹具
   断言两 chip 定位不同（M9 变异红）。
2. **外部插件清单顺序不确定**：`Registry.Snapshot()` 走 map 迭代，同一状态
   两次请求顺序都不同，前端 chip 会闪跳。修：适配层按 PluginID 排序
   （M11 变异红：删排序 → 顺序判据红）。
3. **statusLabel 未知枚举回落「全部状态」**：上一修复的兜底语义错——
   未知值应显示原始字符串（诚实），不是冒充一个不相关的标签。
4. **部署偏差**：上轮部署的 2712 是**合并前**构建（不含上游 f3610fe0f
   折叠修复），推送的提交却是合并后代码 —— 「已部署验证」与「已推送」
   指向不同二进制。本轮重建 + 重部署 2713（9c43da60 基线）弥合。

另补一个判据缺口：筛选栏「一行 + 11px 一致」是 owner 明确要求，但 jsdom
getComputedStyle 恒空（判据纪律 19）—— 按 honesty.test 的 readSrc 先例补
源码契约（nowrap/overflow-x/11px 逐块点名，M10 变异红）。

**复审后验证**：全仓 2673 条仅 4 条上游遗留红（与基线逐条一致）；3 处新
变异全红；本地 2713 部署 VERIFY_PASS=1（readyz ok、plugins 端点未授权 401 /
授权返 v1 守卫、bundle 与新构建同指纹）；合并后代码浏览器复验：9 模块格、
4 协议 chip（上游折叠改动后客户端拆分更细，chip 数从 3 → 4，均无重叠）、
隐藏横幅、33 行、5 插件 chip、点击详情全过；视觉模型确认无重叠/裁切。

## 六、遗留（登记不修）

- `probeTask`/`taskDetect` 语义上是请求侧观测，本轮按维护者给定的流程顺序排在
  出站链（响应侧）——模块带归属是**呈现口径**，若 owner 要求严格按处理阶段
  归位，只需改 `GATEWAY_{IN,OUT}_MODULES` 数组（M4 判据会守住挪动）。
- 插件清单挂载时拉取一次（插件集合只在启动/安装时变化）；安装/卸载后需刷新
  页面才更新。若要实时，接 SSE 事件即可（登记）。
- 真实浏览器肉眼验收：本地部署后经 `/dev/live-topology-preview`（免凭据）复核。

## 六、客户端易读信息 + 请求详情全页轮（2026-10-18 上午第二次增量）

owner 两个新要求，均落地并部署 2.5.8.2717：

### 1. 客户端信息更易懂
- `clientDisplayName()`（lib 纯函数）：已知品牌固定映射（claude-code→Claude Code、
  zcode→ZCode…），其余 kebab/snake 键分段 Title Case，**十六进制身份前缀原样**
  （大写化哈希即失真）；`unknown` 由渲染层走 i18n「未知客户端」。
- 生效面：图上客户端卡（label 易读名 + 副行改 `类型 · 主导协议`，协议与连线
  chip 同源）、左栏摘要/明细行、详情面板。
- 判据：品牌映射/Title 化/十六进制不动/非标识符透传 4 条（M12 变异红——
  删映射后 ZCode 用例咬住，Claude Code 用例因 Title 化兜底同形不红，属预期）。

### 2. 请求详情全页（/live/request/:requestId）
- 内容抽取 `RequestDetailView`（zoom 面板与全页**共享一份实现**，两处字段
  必然一致——判据逐字段点名防抽取漏搬）；zoom 面板变薄包装并加 ↗。
- 左右面板每行加 ↗（client 面板行结构从单 button 改容器，button 嵌套不合法）；
  跳转 `router.push`，M13 变异（不接线）红。
- **全页读原始 store 而非过滤子集**：被数据开关隐藏的探测/无效节点请求
  从链接仍可打开——隐藏是展示口径不是数据删除（M14 变异红）。
- 全页自建 SSE 连接（引用计数单例）：深链/刷新时首帧 initial_data 回放
  最近窗口，请求仍在回放窗内即可真渲染；三态分明：**有数据 / 正在取
  （首帧未到）/ 已滚出窗口**（最后者明说，绝不渲染空壳冒充）。
- 鉴权：生产路由 requiresAuth（详情含 token/费用/会话）；DEV-only 免登录
  别名 `/dev/live-request-preview/:requestId`（同 waterfall-preview 先例）供
  肉眼验收，navCoverage 按孤儿登记（生产路由因被视图链接覆盖**不需**登记——
  门自己拦下了我的多余登记，白名单「不得凭空登记」判据生效的实证）。

浏览器验收（DOM+截图）：客户端卡 "Claude Code / cli · anthropic" 等四家
易读名+协议副行全部在场；左右面板各 33 个 ↗；全页详情完整渲染 15+ 字段
（Claude Code / anthropic / 842ms / 900/334/1234 / $0.0042 / 返回链接）；
已滚出窗口态诚实显示。全量 2687 条仅剩 navCoverage(6 条上游孤儿，owner
拍板项) + CJK 棘轮(上游) 两文件红；CJK 计数 5757 净贡献仍为 0。
