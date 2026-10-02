# 73 号报告：R88-f EmptyState 精确清单定稿 + 判定机械统一不可行

- 日期：2026-10-01
- 轮次：R88-f
- 变更面：`web/src/components/EmptyState.vue`（**仅注释**）、`docs/audit/playbook/domains/D15-observability-ux.md`
- 触发：62 号把 62 号报告里「`EmptyState` 组件根本不存在」的错更正了，但**没有回灌到 playbook D15**，且当时标注「精确数字仍未定」
- 性质：**核实 + 文档订正**。**本轮未改任何渲染行为、未改组件 props。**

---

## 0. 结论先说

**不统一。** 这不是「工作量大」，而是三条硬约束：

1. 13 处自建空态里**有 6 处语义或结构上就不该换**（3 处是 loading 态、2 处在 `<td>` 里、1 处被 `!important` 覆盖）；
2. 剩下 7 处真空态里，**需要 icon / title / desc 的那些组件当前承载不了** ⇒ 要么先扩组件，要么逐处重排；
3. 任何替换都要逐处比对 padding/color token，否则违反 objective 的「**不要影响客户端的观感**」。

已登记为**待裁决第 29 条**。

---

## 1. 先修一处文档不一致：62 号的更正没回灌到 playbook

`docs/audit/playbook/domains/D15-observability-ux.md:81` 的 R85 回注仍写着：

> **`EmptyState` 组件根本不存在而 16 处视图各写空态**

62 号已在 `62-跨项目借鉴分析与事实更正.md` 里把它更正为「组件存在」，**但只改了审计报告，没改 playbook** ⇒ 下一个人读 playbook 仍会得到错结论。**这与本会话已修的另两处同类**（幽灵告警引用、与代码矛盾的测试注释）——**文档的错误结论会跨文件传播，且从不自动收敛**。

已在 D15 就地划删除线并写明订正理由。

---

## 2. 精确清单（可复现）

`web/` 实测：

| 量 | 数值 | 数法 |
|---|---|---|
| 引用 `EmptyState` 的文件 | **8**（含组件自身 ⇒ **7 个消费者**） | `grep -rl EmptyState src --include=*.vue \| wc -l` |
| 自建 `.empty-state` **容器** | **13 处 / 8 个文件** | 逐行读模板；排除 `EmptyState.vue` 自身与 `-icon`/`-title`/`-desc` 子元素 |
| 全站 `.vue` | **263** | `find src -name '*.vue' \| wc -l` |

> 62 号实测到「115 处匹配 / 8-10 个文件引用」并标注数字未收敛；本轮把它定为 13 处容器 / 7 个消费者。

**关键定性**：13 处用的是**各 view 本地定义的 `.empty-state` 类**，而组件渲染的是 `app-empty-state`
⇒ **两套并行**，不是「没有组件」。这个区别决定了后续动作完全不同：
「造一个组件」vs「扩一个已有组件的能力再逐类收敛」。

---

## 3. 13 处逐条归类

| 类 | 位置 | 渲染内容 | 能否替换 |
|---|---|---|---|
| **B·loading 态** | `UsageCost.vue:371` | `{{ t('dataLifecycle.usageCost.loading') }}` | ✗ 语义不是空态 |
| | `UsageCost.vue:440` | 同上（`class="card empty-state"`） | ✗ 且带 `card` 复合类 |
| | `UsageCost.vue:488` | 同上（`class="card empty-state"`） | ✗ 同上 |
| | `ProbeHealthPanel.vue:487` | `{{ t('probeHealth.loading') }}` | ✗ 语义不是空态 |
| **C·`<td>`** | `ProxyView.vue:644` | `<td colspan="8">` | ✗ div 组件放不进 td |
| | `ProxyView.vue:737` | `<td colspan="11">` | ✗ 同上 |
| **D·`!important`** | `ProxyView.vue:491` | `<div>`，`padding: 2rem !important` | ✗ 覆盖会与组件行内 padding 打架 |
| **A·真空态·div** | `ProbeHealthPanel.vue:488` | `t('probeHealth.empty')` | ✅ **样式与组件默认逐项相同** |
| | `CredentialHeatmapView.vue:677` | `!loading && !filteredCredentials.length` | ✅ 需比对样式 |
| | `RoutingLogView.vue:264` | `!loading && !entries.length` | ✅ 需比对样式 |
| | `RoutingDashboardView.vue:1006` | `class="card compact-card empty-state"` | ⚠ 带 `card compact-card` |
| | `ModulesView.vue:1103` | 配 `.empty-icon`（`font-size:40px`） | ✗ **需 icon，组件没有** |
| | `ClientConfigDialog.vue:314` | 含 `-icon` / `-title` / `-desc` 三层 | ✗ **需富结构，组件没有** |
| | `ProxyView.vue:491` | 同 D 类 | ⚠ 见上 |

**样式比对（本地 `.empty-state` 定义）**：

| 位置 | padding | color | 与组件默认（`40px` / `var(--muted, var(--text-secondary))` / 13px / center） |
|---|---|---|---|
| `ProbeHealthPanel.vue:717` | `40px` | `var(--muted)` | **完全相同** ⇒ 纯重复 |
| `ProxyView.vue:941` | `2rem !important` | `var(--text-secondary)` | 不同 |
| `ModulesView.vue:1339` | 无 | `var(--text-secondary)` | 不同 |

---

## 4. 组件的能力缺口比 62 号说的更大

62 号引 OmniRoute 的 `EmptyState` 带 `actionLabel` / `onAction`，指出本项目「缺 action 能力」。
**方向对，但不够**——本项目组件的模板只有：

```vue
<div class="app-empty-state" :style="{ padding }">
  <slot>{{ text }}</slot>
</div>
```

**只有一个默认 slot，没有 icon / title / desc / action 任何一项。**
真正卡住统一的是**连富结构都承载不了**，action 只是第四项。

⇒ 待裁决第 29 条：**是否给 `EmptyState` 补 icon/title/desc/action 四槽，再按上表 A 类逐处收敛。**
在补槽之前统一，只会得到「把 7 处真空态里最简单的 1 处换掉」的收益。

---

## 5. 顺带订正组件自身的注释

`EmptyState.vue` 的头注释把
`ProbeHealthDetailView / SelfCheckPanel / ModulesView / ProxyView / PromptInjectionSettingsView`
都列成「抽取来源」。实测 **ModulesView 与 ProxyView 至今仍各自手写 `class="empty-state"`**
（`ModulesView:1103`、`ProxyView:491/644/737`）⇒ 「抽取自」对这 4 处不成立。
已改为如实陈述并写入两套体系并存的事实与能力边界。

---

## 6. 本轮又踩了一次「代理量」

分类时我先用「该行是否含 `loading` 子串」打 loading 标签，被
`v-else-if="!loading && !filteredCredentials.length"`（`CredentialHeatmapView:677`）
骗了——那是**真空态**。与 `conventions.md` §9.2「判据钉语义量」同源：
**子串命中是代理量**。最终改为**逐行读渲染内容**归类。

**教训**：打标签用的判据要和要区分的语义**一一对应**；
用一个宽泛子串去近似「这段渲染的是 loading 还是空」必然出错。

---

## 7. 验证

- `npx vitest run src/config`：**2 files / 27 tests passed**（含 `navCoverage.test.ts`）。
- `go build ./...` OK。
- **本轮改动全部是注释与文档**：`.vue` 只改注释块、playbook 只加回注、
  **未触碰模板、props、样式**，因此不存在行为变化。
