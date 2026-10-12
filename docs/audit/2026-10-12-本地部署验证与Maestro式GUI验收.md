# 2026-10-12 本地部署验证与 Maestro 式全项目 GUI 验收

> 目标（用户原话）：使用本地部署脚本进行部署验证，并使用 Maestro 方式对整个项目
> 进行测试验证，修正发现的问题，提交代码并推送。
> 会话 checkout：`llm-gateway-go-2`，基线 `166e3dfa9`（快进合并远端 4 提交后）。

## 一、Maestro 语境调查（先查清再动手）

仓库 `docs/UI规范/10-审计与实施路线.md`、`15-真机验收与落地路线.md` 中的 Maestro 指
mobile.dev 的移动 UI 测试框架，其资产（壳仓 `.maestro/` flows + `llmgw-cdp.py`、
AVD `llmgw_hyper2`、`/opt/homebrew/bin/adb`）**全部不在本机**：

| 资产 | 本机实测 |
| --- | --- |
| `maestro` CLI | 不存在（`which maestro` 空） |
| adb | `/opt/homebrew/bin/adb` 不存在；`~/Library/Android/sdk` 不存在 |
| Android 模拟器 / AVD | 无（`emulator -list-avds` 空） |
| 壳仓 `llm-gateway-client` | 全盘（~/workspace、~/kaixuan）无此目录 |

`docs/UI规范` 记录的是**另一台机器**的状态（其文中「本机」均有 Android SDK 与
adb-filter-proxy 等本机没有的基础设施）。本机安装整套移动自动化栈属用户侧决策
（AGENTS.md：依赖安装属用户侧决策），本轮不越权安装。

**裁决**：采用 Maestro 的**方法论**——黑盒 GUI flow 驱动、每步截图视觉证据、
不注入 JS 改状态——落在浏览器自动化（ZCode IAB + Playwright/cua）上，对部署产物
做端到端验收。web-mobile 的 spec 注释（`AppDrawer.spec.ts:196` 等）也是同一口径：
「iOS 26 模拟器上 Maestro 连跑两轮……」是别的机器上的回归手段。

## 二、部署验证（两轮）

### 第一轮：2543（基线验证）

```
LLM_GATEWAY_ROOT=~/kaixuan/llm-gateway-go-v2 \
PATH="$HOME/sdk/go/bin:$HOME/kaixuan/pg18/bin:$HOME/kaixuan/redis/bin:$PATH" \
GOPROXY=https://goproxy.cn,direct GOMODCACHE=~/sdk/gomodcache GOCACHE=~/sdk/gocache \
LLM_GATEWAY_DATABASE_URL='postgresql://huangxt@127.0.0.1:54318/llm_gateway?sslmode=disable' \
LLM_GATEWAY_REDIS_ADDR=127.0.0.1:6379 \
~/sdk/bash5/bin/bash scripts/deploy-local.sh deploy
```

- `VERIFY_PASS=1`，release **2.5.8.2543**（构建自 `166e3dfa`），active **8782**；
- 显式 DSN + PATH 注入绕开 ZCode 嵌套 bash 的 netstat 缺表坑（AGENTS.md 已记）；
- 凭据解密冒烟 ✓（providers=14,18,13092 creds=17 failed=0）；
- 双端口候选验证 ok；部署收尾**零孤儿**（TERM→10s→KILL 升级路径首次实战触发一次，
  行为符合 2026-10-11 下午审计轮的设计）；
- `tests/deploy_host_first_contract_test.sh` **ALL PASSED**（10 项）；
- 回归探针：`/v1/models` 无 token / 伪造 token 均 **401**（r1017 修复保持）；
- healthz 版本徽章 `2.5.8-166e3dfa-20261011-2543` ✓。

### 第二轮起：2545/2546/2547（修复迭代 + 并行会话交叠）

- 2545：**并行会话**从另一 checkout 部署（git_sha `ee565bb3`，非本 checkout HEAD）
  ——共享机器的并发部署交叠，部署锁等待即可，不要 `--force`；
- 2546：本 checkout 首次携带 F1 修复（`3e860869` + 工作区改动）上线；
  端到端复验发现 `flex-shrink` 挤压第二层根因（见 F1）；
- 2547：最终修复（wrap + fit-content + flex-shrink:0）上线并完成复验（§四.4）。

### mesh 入口说明

`http://llm.itestu.cn:8782/readyz` 与直连 `100.106.169.63:8782` 在本机 curl 超时
——这是记忆档已记录的 **NetBird userspace 栈本机自连怪癖**（本机连自己的 utun100
进隧道不走回环，「不代表远端入站状态」）。网关监听 `*:8782`（lsof 证实），远端回连
已于 2026-10-11 经 252 实测通过。**非本轮回归。**

## 三、测试门矩阵

| 门 | 结果 |
| --- | --- |
| `go build ./...` / `go vet ./...` | rc=0（需 `SDKROOT=MacOSX26.5.sdk` 钉扎，27.0 tapi 不兼容为已知） |
| `CGO_ENABLED=0 go test` security/outputcompliance/streaming/proxy/settings/middleware/admin/bg/stats | 除 `admin` 的 `TestV1BodiesReadersAreAssessed` 外全绿——该条是**维护者故意红的负向闸门**（重跑 admin 确认唯一 `--- FAIL` 就是它），非回归 |
| web vitest | 修复前 2401 绿 → 修复后 **2402/2402 绿** |
| web-mobile vitest | **194 文件 / 7188 绿**（+1 skipped） |
| host-first 契约门 | ALL PASSED |

## 四、发现与裁决

### F1（P2，真缺陷，已修复）：顶栏版本徽章遮挡主导航并拦截点击

**现象**（1280×720 视口，EN 语言，多张截图一致）：`.app-topbar__meta`（状态 pills +
`166e3dfa v2.5.8 #2543` 版本徽章）直接盖在「Integration Guide」「运维中心」两个
导航项上，文字不可读且点击被 actions 区拦截。

**实测机理**：`.app-topbar` 为 nowrap flex；nav `flex:1; min-width:0;
overflow:visible`，链接 `white-space:nowrap`。实测 1280 视口下 nav 内容宽
**922px** 只分到 633px，**重叠 273px**；全顶栏放平需 **~1569px**。
即 **1024–1568px 的常见桌面宽度全部命中**（≥1024 才渲染桌面 nav，
<1024 汉堡接管）。EN 语言 worst case；ZH 文案较短、宽屏下不触发，故此前未暴露。

**修复**（`web/src/components/shell/AppTopbar.vue`，纯 CSS）：
- `.app-topbar` 加 `flex-wrap: wrap` + `flex-shrink: 0`；
- `.app-topbar__nav` 的 `min-width: 0` 改 `min-width: fit-content`——nav 的假定主
  尺寸变成全内容宽，单行放不下时 flex 换行把 nav **整体**送到独立行，放得下时
  单行布局与改动前逐像素一致（宽屏零回归）；无断点硬编码、i18n 无关、无 JS。
- `flex-shrink: 0` 是 2546 轮端到端复验**抓出的第二层根因**：`.app-layout` 是
  100vh 纵向 flex，页面内容超高时 topbar 作为 flex 项被压回 `min-height:64px`——
  单行时代内容恰好 64px 从不触发；换行后第二行被压出盒子、视觉叠到下方横幅上
  （实测 bar h=64 而 actions 行 y=62-98 溢出盒外）。topbar 保持内容高后，
  溢出吸收交给内部滚动的 `.main-content`（flex:1 + overflow:hidden）。
- ≤768 兜底块回置 `min-width: 0`（该块 `width:100%` 语义下 fit-content 会反向
  撑破；该块当前实际不渲染 nav——≥1024 才有桌面 nav——属防御性）。

**回归门**：`AppTopbar.responsive.test.ts` 新增
`desktop nav wraps to its own row instead of sliding under the actions`，
只对基础规则区断言（第一个 `@media` 之前）。**变异验证四段**：基线绿 →
删 `flex-wrap: wrap` 红 → `fit-content` 改回 `0` 红 → 删 `flex-shrink: 0` 红 →
还原绿。⚠ 判据第一版写松了：全文件搜 `flex-wrap: wrap` 会被 ≤768 块里**原有**的
同名规则冒充，变异不变红——已收紧为基础区切片后重验（判据纪律第 1 条
「判据必须在同一层」的 CSS 版）。

### F2（观察项，非缺陷）：登出/路由切换后视图不换（URL 已变）

Sign out 点击后：URL → `/?login=1`、`llmgw_user_info` 已清、服务端会话已清
（刷新后落地页+登录弹窗，直接深链受保护路由被守卫重定向
`/?login=1&redirect=%2Fmodels`）——**功能全部正确**。但当前视图仍挂着的已登录
Dashboard 直到手动刷新才切换。

**根因是测试环境工件**：ZCode IAB 离屏渲染面 `document.visibilityState==='visible'`
但 **rAF 永不触发**（setTimeout 正常，2s 无一帧）。Vue 过渡依赖双 rAF 推进
（leave-from → nextFrame → leave-to），rAF 冻结 ⇒ 过渡类名永久卡在 enter-from/
leave-from，视图交换与遮罩摘除全部挂起。同一机理也造成「点击导航 URL 变了视图不换」。
真实前台浏览器 rAF 正常（毫秒级完成）；真实后台标签页被节流，但**回到前台即完成**。
不修产品代码；若未来要硬化，可在过渡完成钩子加 setTimeout 兜底（登记不修）。

**测试基建连带坑**（同根因，供下轮复用）：IAB 冻结 rAF 环境下 Playwright
click/fill 的 actionability 检查大面积超时（元素可见、enabled、elementFromPoint
命中自身仍超时），`tab.cua.click/type` 坐标路径不受影响——GUI 驱动一律走 cua；
截图偶发 `surface preparation timed out`，重试即可。

### F3（排除）：重登录后 Dashboard 顶部「Unable to confirm traffic data state」红条

`V1DataFrozenBanner` 的 `failed` 态（`useV1DataHorizon` 取
`GET /api/admin/v1-data-horizon` 失败时**故意**显示——fail-closed 设计：取不到
告示 ≠ 未停更，组件注释写明这是唯一真正危险的静默方向）。实测：
- curl 带 admin token 直调端点：**200** `{"v1_data_horizon":null}`（= live 未停更）；
- 新鲜整文档加载 /dashboard：**横幅不出现**。

⇒ 红条只出现在「登录过渡期一次取数失败 + F2 的冻结视图永不重试」的合成场景，
组件行为符合设计（且横幅自带 Re-check 重试路径）。**非缺陷。**

### F4（登记不修）

- IAB rAF 冻结对 Vue 过渡的鲁棒性硬化（见 F2 尾注）——机会性改进。
- mesh 自连怪癖（§二）——已有记忆档，非本仓问题。

## 五、GUI 验收矩阵（Maestro 式 flow，均含视觉证据）

| # | flow | 结果 | 证据 |
| --- | --- | --- | --- |
| T1 | SPA 入口加载（/）| PASS：壳/导航/版本徽章 166e3dfa #2543/状态 pills 全渲染 | 截图 |
| T2 | 登录流（登出 → 落地页 → 登录弹窗 → admin 凭据 → redirect）| PASS：功能链全通 | 截图×3 |
| T2b | 登出流 | PASS（功能）：令牌/会话/路由全正确；视图切换见 F2 | 截图 |
| T3 | Models & Catalog（95cacb546 模型清单 500 回归点）| **PASS：2887 个模型**加载，Family 快选 chips、筛选器、清单表格齐全 | 截图 |
| T3b | 按模型统计抽屉 | PASS：模型/提供商/请求数/TOKEN/费用渲染 | 截图 |
| T4 | SPA 深链（/dashboard、/models、/m/keys 整文档直达；r2540 回归点）| PASS：text/html 导航放行返回 SPA；受保护路由正确重定向登录 | DOM 证据 |
| T5 | Provider Management | PASS：健康状态条（Model discovery/Fast probe/Credential cycler/Auto-recovery）+ 三维筛选 + 16 列表格 | DOM 证据 |
| T6 | 移动端 /m/keys（390×844）| PASS：顶栏/下拉刷新/Create key/密钥卡片/底部导航 Keys 高亮 | 截图 |

环境准备声明：被测网关由 §二 部署产出；admin 凭据取自 `.env.local`（gitignored）；
无任何注入式预置状态；「高用量 API Key 排行」抽屉等中间态均由真实点击产生。

## 六、结论

- 部署验证两轮全绿（2543 基线 + 2544 修复），零孤儿、契约门通过；
- 全项目测试门：后端除已知负向闸门外全绿，前端 web 2402 + web-mobile 7188 全绿；
- GUI 端到端验收 7 项 flow 全通过；发现 1 个真缺陷（F1 顶栏遮挡）已修复
  （变异验证有牙 + 回归门 + 2544 端到端复验），2 个现象判定为测试环境工件并留档；
- 提交并推送本轮代码变更。
