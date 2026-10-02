# 2026-10-03 — stream 按模型分组：范围外模型的可发现性 + 一次批判式审计

## TL;DR

对 `dashboard?tab=stream`「按模型分组的可用节点」做了一轮批判式审计。
结论是**模型归属链路本身没有缺陷**（`glm-5.3` / `glm-5.3-flash` / `glm-5.3-glb`
三组独立、节点数正确，且已由单元测试钉住）。真正的问题只有一个，且不是「算错」
而是**看不见**：筛了一个**有节点但不在可见范围内**的模型，面板只给一个空区块 +
零提示，运维会读成「这个模型没有节点 / 已下线」。

本轮只补了一条解释性提示，**不改变任何过滤行为**。

## 审计结论

### 判定为「不是缺陷」，明确记录以免下次重复排查

| 现象 | 判定 | 依据 |
|---|---|---|
| 页面 `document.hidden` 时丢弃全部 SSE 消息、却仍显示「已连接」 | **有意设计**，非 bug | `liveStreamStore.ts:1334-1339`；`:309-325` 的 `visibilitychange` 会对真人用户自愈（重连重放）。仅嵌入式/headless/被遮挡 webview 无兜底 |
| `top-models` 只返回前 50 个模型 | **有意裁剪**，服务端有注释 | `admin/logs.go:1217-1241`（为避开列存分区全扫而改查 `request_logs_hot`） |
| 范围外的模型不显示、而不是挂到别的模型名下 | **有意行为，且有专测** | `QueuePerspectivePanel.test.ts`「shows nothing for a filtered model that is outside the featured/hot universe」 |
| 两个来源返回 200 但结果为空 ⇒ 面板显示「0 个模型」 | **不修** | 若无 featured 模型且 72h 无流量，scope 本就该空；此时报错误报是制造假警报 |

### 判定为「缺陷」：不可发现

`QueuePerspectivePanel.vue` 的 `modelGroups` computed 对 scope 外的 `raw_models`
走 `if (!scopeKey) continue` —— 行为正确，但**无计数、无提示**。

线上实证（`llmgateway.internal.example.com`，2026-10-03）：`glm-5.2` 未出现在面板的 63 个模型里。
用接口同款口径在生产库复算才查清原因：

| 模型 | 72h 排名 | 请求数 | 面板 |
|---|---|---|---|
| `glm-5.3` | 3 | 544 | 显示（热门 544） |
| `glm-5.3-flash` | 17 | 75 | 显示 |
| `glm-5.2` | **75** | **18** | **不显示** |

3 天窗口内共 329 个模型，第 50 名门槛 30 次；`glm-5.2` 仅 18 次落在门槛外，
且不在 featured 列表 ⇒ 两者都不满足 ⇒ 被那条 `continue` 排除。
**不是归属缺陷，是显示范围。** 但面板没有任何提示，运维只能靠查库才能确认。

交叉验证：面板自报的 `glm-5.3` 热门 **544** 与按 `admin/logs.go:1243-1270` 同款 SQL
独立复算的 **544** 完全一致 —— UI 渲染的归属结果与生产库真相吻合。

## 改动

`web/src/components/QueuePerspectivePanel.vue`

- 新增 computed `outOfScopeFilterModels`：在 `modelFilter` 非空时，找出
  **既出现在某个节点 `raw_models` 里、又不在 scope 索引里**的筛选值。
- 分区块 `v-if` 纳入该列表，模板新增 `.qp-scope-miss` 提示行。
- **过滤行为零改动**：范围外模型依旧不出分组、依旧不冒名。

守卫是刻意收紧的：`!withNodes.has(key)` 排除了「压根没有节点」的模型 ——
那是另一种空，提示它会把排查方向带偏。

## 测试

```
cd web && npx vitest run src/components/QueuePerspectivePanel.test.ts
# 37 passed (新增 2 条)

cd web && npx vitest run
# Test Files 156 passed | Tests 1122 passed

cd web && npx vue-tsc --noEmit -p tsconfig.json
# exit 0
```

变异验证（两道新测试都承重，不是摆设）：

| 变异 | 预期红 | 实测 |
|---|---|---|
| 撤掉提示元素（`v-if="false && …"`） | 正向用例红 | ✅ 只有该条红，36 条仍绿 |
| 去掉 `withNodes` 守卫（过宽的修复） | 反向对照用例红 | ✅ 只有该条红，36 条仍绿 |

第二条尤其重要：只钉正向不钉反向的话，一个「什么情况都提示」的粗糙修复也能变绿。

## 遗留

- ~~**生产 UI 验收仍未闭合**~~ → **已闭合（2026-10-03 06:19 CST）**。
  154 已部署 `build_seq 2408 / git_sha 1a213f4c`（含 `809ce78cf`），用
  headless Chromium（Python Playwright 1.63）驱动生产页面实测，承重判据全中：

  | 判据 | 实测 |
  |---|---|
  | 恰好 1 个 glm-5.3 分组 | ✅ 层计数 `1 个模型 / 64`，`分组数 = 1` |
  | 标题不含 flash 系 | ✅ 唯一分组名 `glm-5.3`（`glm-5.3-flash` 未混入） |
  | `.qp-scope-miss` 提示行 | ✅ 实渲染：`glm-4.5 有节点，但不在「特色 / 近 3 天热门」范围内（本面板只展示范围内模型），故未列出分组。` |
  | SSE 连通 | ✅ `● Connected`，无 pageerror |

  - **「必须人工前台标签页」的前提被证伪**：headless Chromium 里
    `document.hidden === false`，面板 **9 秒**即渲染。内嵌 FilePanel 标签
    渲染不出来是**该标签 renderer 被冻结**的验收工具限制，与本改动无关
    （若走 `liveStreamStore.ts:1335` 的 page-hidden 分支，必会打出
    `:1336` 的 `console.debug`，实测一条没有）。
  - **`.qp-scope-miss` 的真实触发对象不是 `glm-5.2`**：模型选择器
    （`mp-dialog`，含全部 15 个「更多…(N)」展开区共 388 项候选）里
    **没有任何 `glm-5.2` 条目**（glm 系只有 4 / 4.5 系 / 5.1 / 5.3 /
    5.3-flash）。所以本轮用 `glm-4.5`（可选中、4 个节点、不在 scope）
    作为触发对象。**若仍按「筛 glm-5.2 看提示行」验收，会因为选不中而误判
    成「提示行没上线」。**
- ~~**尚未部署到 154**~~ → **已部署**（`2408-1a213f4c`，
  线上 `web/assets/` 三处命中 `qp-scope-miss`：JS 类名绑定、中文文案、CSS 规则）。
- ~~`document.hidden` 无兜底那条**未开单**~~ → **已开单**：
  `docs/agents/issue-tracker.md` §2 `LIVE-STREAM-HIDDEN`（open）。
  该单同时订正了「内嵌 Browser 失败即本缺陷证据」的误判。
- **新发现（未开单，待裁决）**：迁移 `818_ursm_snapshot_typed_columns.sql`
  在 1965 万行热表上做 DDL，而 252 PG 的 `statement_timeout=30s` 是硬限、
  `lock_timeout=0` 使**等锁时间计入语句超时**。首次 deploy-154 即被它挡住
  （`ERROR: canceling statement due to statement timeout`），同形态探针
  复测秒过 ⇒ 瞬时锁争用而非工时不足，重跑即过。245 部署会撞同一堵墙。
  本仓既有解法是 `SET LOCAL statement_timeout = '10min'`
  （`649` / `689` / `813` 同款），本轮未改动迁移文件。
