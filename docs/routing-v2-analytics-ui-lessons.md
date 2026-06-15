# routing-v2 热力图 + 路由流向（Sankey）UI 经验教训

> 范围：`RoutingDashboardView` 数据分析 Tab 的双栏图表（热力图 + Sankey）  
> 时间：2026-06-16  
> 相关提交：`99d112d7` → `6f2ee211` → `49ea1f1c` → `59481b4d` → `aff6653f`（已部署 `gitsha-aff6653f`）

---

## 背景

路由全景 v2 的「数据分析」Tab 并排展示：

- **左**：任务/工作类型 × 模型热力图（`HeatmapMatrix.vue`）
- **右**：任务 → 标准模型 → 供应商 Sankey（`RouteFlowSankey.vue`）

高度、布局、标签与后端数据语义需协同；任一环节错误会导致裁切、假死或视觉失真。

---

## 问题与根因

### 1. 路由流向图被裁切（高度不足）

| 项 | 说明 |
|---|---|
| **现象** | Sankey 底部节点/连线被卡片裁切，图例与 SVG 挤在一起 |
| **根因** | 卡片 `minHeight` 只按 SVG viewBox 估算，未计入 **DOM 图例**（`sankey-legend`）和 **section 标题**；SVG 使用固定 `height: Npx` 且 flex 子项被压缩 |
| **修复** | 拆分高度链路：`computeSankeyCardHeight` = `SANKEY_SECTION_HEAD_H` + `SANKEY_DOM_LEGEND_H` + `computeSankeyHeight`；SVG 改为 `height: auto` + `preserveAspectRatio="xMidYMin meet"`；父级 `flex: 1; min-height: 0` |

**关键常量**（`sankeyLayout.ts`）：

```
SANKEY_SECTION_HEAD_H = 36    // 「路由流向」标题区
SANKEY_DOM_LEGEND_H   = 56    // 图例（可能换行）
SANKEY_V_PAD          = 60    // viewBox 内上下留白（layer 标题 + 底边）
```

### 2. 页面假死（无限循环）

| 项 | 说明 |
|---|---|
| **现象** | 打开含大量节点的流向数据后 Tab 卡死，CPU 100% |
| **根因** | `requiredColHeight` 二分查找用 `Math.ceil((lo+hi)/2)`，当 `lo = hi-1` 时 `mid === hi`，`lo` 永不前进 → **死循环** |
| **修复** | 改为 `Math.floor((lo+hi)/2)`（commit `99d112d7`） |

```typescript
// sankeyLayout.ts — requiredColHeight 收敛条件
while (lo < hi) {
  const mid = Math.floor((lo + hi) / 2)  // 勿用 Math.ceil
  if (sumAt(mid) <= mid) hi = mid
  else lo = mid + 1
}
```

### 3. 节点异常偏高（双重拉伸）

| 项 | 说明 |
|---|---|
| **现象** | 少量节点时每个条块很高，整体像被纵向拉满 |
| **根因** | ① `SANKEY_MIN_H=400` 在**有数据**时仍抬高 viewBox；② 布局用 `H - 60` 整列高度分配，未按列内节点数与权重反推 |
| **修复** | `computeSankeyHeight` 仅用 `requiredColHeight` 驱动；`SANKEY_MIN_H` **仅空态**；`SANKEY_NODE_H` 32→18；三列统一 `unifiedColHeight = max(requiredColHeight(col))`（commit `6f2ee211`） |

### 4. 中间列显示 outbound 名而非标准模型名

| 项 | 说明 |
|---|---|
| **现象** | Sankey 第二列出现原始 `outbound_model`，与热力图列名不一致 |
| **根因** | `handleFlow` 直接聚合 SQL 的 `outbound_model`，未走别名索引 |
| **修复** | `admin/analytics.go`：`loadModelAliasIndex` + `canonicalFor(raw)`，与矩阵 `handleMatrix` 列归一化一致（commit `49ea1f1c`） |

### 5. 高流量节点过度主导高度

| 项 | 说明 |
|---|---|
| **现象** | 单一热门任务/模型占据绝大部分列高，其它节点被压成细条 |
| **根因** | 节点高度与 `total` 近似线性正比 |
| **修复** | 次线性缩放 + 列内 cap（commit `59481b4d` 及后续微调） |

**缩放公式**（`scaleNodeTotal`）：

| 层 | 含义 | 公式 |
|---|---|---|
| layer 0 | 任务类型 | `log1p(total)` — 最强阻尼 |
| layer 1 | 标准模型 | `pow(total, 0.42)` |
| layer 2 | 供应商 | `pow(total, 0.50)` |

列内分配：

1. `weights[i] = scaleNodeTotal(node.total, layerIndex)`
2. `height[i] = max(SANKEY_NODE_H, (w_i / Σw) * available)`
3. 若 `max(h) > min(h) * SANKEY_MAX_HEIGHT_RATIO`（3.5），截顶后不再重分配（接受少量空白）

`requiredColHeight` 对每列二分求最小 `available`，使 `Σ nodeHeights + gaps ≤ available`。

### 6. 左右卡片高度不一致

| 项 | 说明 |
|---|---|
| **现象** | 热力图与 Sankey 卡片一高一矮，网格行不齐 |
| **根因** | 各自独立算 `minHeight`，未取对齐值 |
| **修复** | `chartHeight = max(heatmapCardHeight, sankeyCardHeight)`；grid `align-items: stretch`；子组件分别扣减 chrome 后填充（commit `aff6653f`） |

**RoutingDashboardView 高度链路**：

```typescript
heatmapContentHeight = max((rows+1)*22 + 40, 400)
heatmapCardHeight    = heatmapContentHeight + HEATMAP_CARD_CHROME  // 42
sankeyCardHeight     = max(computeSankeyCardHeight(flowData), 400)
chartHeight          = max(heatmapCardHeight, sankeyCardHeight)

heatmapBodyMinHeight = chartHeight - HEATMAP_CARD_CHROME
sankeySvgMinHeight   = chartHeight - SANKEY_SECTION_HEAD_H - SANKEY_DOM_LEGEND_H
```

---

## 文件职责

| 文件 | 职责 |
|---|---|
| `sankeyLayout.ts` | 纯函数：层构建、次线性缩放、列高二分、viewBox/卡片高度 |
| `RouteFlowSankey.vue` | SVG 渲染、图例、布局坐标、连线样式 |
| `HeatmapMatrix.vue` | 矩阵着色、`minHeight` 撑满卡片 body |
| `RoutingDashboardView.vue` | 双卡等高编排、API 拉取、筛选器 |
| `admin/analytics.go` | `handleMatrix` / `handleFlow` 列名归一化与 Sankey 三层聚合 |

---

## 部署注意事项

1. **前端热修路径**：仅改 `web/` 时可在 **184** 上只替换静态资源或走 `deploy-llm-gateway-go-184.sh --only app`，无需完整 Go 镜像重建。
2. **完整构建**：本机构建若 `kx-base:go` 拉取 **403**（daocloud/GFW），应在 **184 构建机**上 build，或使用已烤 mirror 的 buildx builder（`setup-buildx-builder.sh`）。
3. **后端标签修复**（`49ea1f1c`）需 **Go 二进制** 部署；仅前端 patch 无法修正中间列 canonical 名。
4. 验证入口：`https://llmgo.kxpms.cn` → 路由全景 → 数据分析 Tab；确认双卡等高、Sankey 不裁切、中间列为标准模型名。

---

## Do / Don't（后续改动清单）

### Do

- 改 Sankey 高度时同时检查：**section 头 + DOM 图例 + viewBox + 卡片 chrome** 四层。
- 列高相关逻辑集中在 `sankeyLayout.ts`，保持 **可单测的纯函数**。
- 二分查找统一用 `Math.floor((lo+hi)/2)`，并为 `hi` 设上界（当前 12000）防失控。
- 热力图行高变更时同步更新 `HEATMAP_CARD_CHROME` 与 `(rows+1)*22+40` 估算。
- 矩阵与 Sankey 的模型列名都走 `loadModelAliasIndex().canonicalFor()`。
- 双卡布局始终通过 `chartHeight = max(...)` 对齐。

### Don't

- 不要在有数据时对 viewBox 强行套用 `SANKEY_MIN_H`（会纵向拉伸节点）。
- 不要用 `Math.ceil` 做 `requiredColHeight` 二分中点。
- 不要让 Sankey 列各自用不同 `colHeight`（会导致三层连线错位）。
- 不要给 SVG 写死 `height: Npx` 而不留 `height: auto` 回退。
- 不要在本地直连 docker.io / 未 mirror 的 `kx-base:go` 上硬构建后抱怨 403。

---

## 回归检查项

- [ ] 空数据：两卡均 ≥400px，提示文案完整可见
- [ ] 多节点（>15/列）：页面不卡死，滚动/缩放正常
- [ ] 极端流量差：最高/最低节点高度比 ≤ 3.5
- [ ] 中间列标签与热力图列名一致（canonical）
- [ ] 缩窄窗口至 <900px：双卡纵向堆叠仍等高
- [ ] 修改 `analyticsRowDim` / `window` 后高度随数据更新

---

## 相关提交速查

| SHA | 摘要 |
|---|---|
| `99d112d7` | 修复 `requiredColHeight` 二分死循环 |
| `6f2ee211` | 去除有数据时 MIN_H 拉伸；统一列高；节点最小高度 18 |
| `49ea1f1c` | Sankey 中间列 canonical 标签 + 次线性高度初版 |
| `59481b4d` | 任务类型列 `log1p` 强化阻尼 |
| `aff6653f` | 热力图与 Sankey 卡片等高（`chartHeight`） |
