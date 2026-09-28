# Handoff — stream 按模型分组的可用节点：模型归属错乱（2026-09-29）

状态：**已修复并推送**（commit `d65cc6f80` 首轮 + 本轮硬化；详见「提交」一节）
影响面：`web` 前端，`dashboard?tab=stream` →「按模型分组的可用节点」分区
接手前必读：先 `git fetch origin main && git log --oneline -5`，本仓有并发会话。

---

## 1. 结论 / 根因

**用户报的现象**：筛选 `glm-5.3`、且确有 `glm-5.3` 请求（72h 内 915 次，最热模型），
但「按模型分组的可用节点」里没有 `glm-5.3` 分组，却出现筛选里没选的其它模型分组。

**两层根因**：

1. **上游**：`/api/routing/resolve` 的 `raw_models` 是
   `modelname.NormalizeRouteKeyAliases` 的**词法变体矩阵**，`removableWrapperTokens`
   会剥掉包装词 —— `glm-5.3-flash` 因此把 `glm-5.3` 也报成自己的 raw，candidates 里
   混进 `glm-5.3` 的凭据。**这是匹配逃生口，不是模型等价关系。**
2. **前端**：`QueuePerspectivePanel.loadModelScope` 在每个 resolve 完成后就地认领别名，
   冲突时 `aliases.delete(key)`；resolve 是 8 并发跑的，**归属取决于完成顺序**。
   真模型 `glm-5.3` 的作用域被词法冒名者吞掉 → 它的 raw 消失 → 分组整个不生成；
   筛选 `glm-5.3` 只能命中冒名者，于是显示成别的模型名。

## 2. 改动文件与关键行为

| 文件 | 性质 | 行为 |
|---|---|---|
| `web/src/utils/modelScopeOwnership.ts` | 新增 | 纯函数归属裁决。合并同 canonical 身份作用域 → 绑定级 canonical 证据优先 → 有证据却无主则**不归属** → 后缀名字亲和兜底 → 稳定排序。**任何分支都不再删除 raw。** |
| `web/src/components/QueuePerspectivePanel.vue` | 改 | `loadModelScope` 改为先收集每个作用域的解析结果，全部完成后交给 `resolveModelScopeOwnership` 一次性裁决；被合并作用域的特色/热门标记并入代表后从 scope 移除 |
| `web/src/utils/modelScopeOwnership.test.ts` | 新增 | 10 例纯函数单测 |
| `web/src/components/QueuePerspectivePanel.test.ts` | 改 | +4 条组件级回归测试（载荷照抄线上真实响应） |
| `docs/changelogs/2026-09-29-stream-model-group-alias-ownership.md` | 新增 | 完整根因 / 判据 / 验证记录 |

**关键行为（真实数据 before → after）**

| 筛选 | 修复前 | 修复后 |
|---|---|---|
| `glm-5.3` | `glm-5.3-flash`(9 节点) | **`glm-5.3`(9 节点)** |
| `glm-4.7` | `glm-4.7-flash`(6 节点) | **无（不冒名）** |
| `glm-5.2` | `glm-5.2`(14 节点) | `glm-5.2`(14 节点) |
| 分组总数 | 53 | 55（+`glm-5.3` +`doubao-seed-2-0-code`；无丢失、无同名重复） |

## 3. 测试命令与结果

```bash
cd web
npx vitest run src/utils/modelScopeOwnership.test.ts src/components/QueuePerspectivePanel.test.ts
# → 10 + 35 全绿
npx vue-tsc --noEmit
# → exit 0
npx vitest run
# → 全量通过（137 files / 991 tests，硬化后 995）
```

**红门取证**（两轮都做了，否则测试锁不住行为）：

- `git stash push -- web/src/components/QueuePerspectivePanel.vue`（回首轮之前）
  → 2 条 glm-5.3 回归测试转红，报错即 `expected 'glm-5.3-flash' to be 'glm-5.3'`。
- `git stash push -- web/src/utils/modelScopeOwnership.ts`（回上一轮已提交版本）
  → 恰好 2 条硬化测试转红（`leaves a raw unowned…`、`shows nothing for a filtered model…`），
  其余 42 条仍绿。

**真实数据复核**：`npx tsx` 直 import 源文件（非 JS 移植版，避免自证）+ 生产 API
+ 真实 `node_update` 快照。`/tmp/audit-real.mjs`、`/tmp/verify-fix.mjs`（一次性脚本，未入库）。

## 4. 遗留风险（未修，明确不是"已完成"）

1. **上游 `resolve` 的候选污染仍在**。`resolve('glm-5.3-flash')` 今天仍返回 `glm-5.3`
   的凭据。影响：routing-v2 resolve 诊断页看到"外来候选"；`persist_probe=1` 按这批候选
   写探测。改它要动 `removableWrapperTokens` 匹配矩阵，会影响真实选路，属独立议题。
2. **面板模型宇宙仍是 featured + 72h 热门**。集合外的模型（如 `glm-4.7`）现在**不显示**
   —— 这比冒名正确，但用户筛选这类模型会看到"没有可用节点"的提示。是否要把它们也纳入
   宇宙（需额外 resolve 调用）是产品决策，未做。
3. **后缀名字亲和是启发式**。`doubao-seed-2-0-code-preview` 只有 `-260215` 形式的绑定，
   靠"`-` 边界前缀"兜底。若将来出现裸名与后缀名分属两个 canonical 的数据，会退化为不归属。
4. **未在真实浏览器里看过**。全部结论来自 API 复算 + 组件测试；生产仍是旧构建，
   需要部署后人工确认一次。
5. **无属性过滤语义**。`passesUpperFilters` 仍是"命中任一 alias 即通过"，未改成
   "全部请求都属该模型"。本轮未触碰。

## 5. 下一轮提示词

```
接着处理 llm-gateway-go 的 dashboard?tab=stream 模型归属问题。上一轮（2026-09-29）已修好
「按模型分组的可用节点」的别名归属错乱：抽出 web/src/utils/modelScopeOwnership.ts 纯函数，
按 canonical 身份确定性裁决，修掉了"筛选 glm-5.3 却显示 glm-5.3-flash 分组"。详见
docs/changelogs/2026-09-29-stream-model-group-alias-ownership.md。

本轮请做这三件事，按顺序：

1)【部署后人工验收】生产 kxpms 仍是旧构建。部署 web 后打开
   https://llmgateway.internal.example.com/dashboard?tab=stream，按模型分组=处理队列视图，模型筛选选
   glm-5.3，确认出现的是标题为 glm-5.3 的分组（不是 glm-5.3-flash），节点数与
   「在用」状态过滤一致；再确认 glm-5.2 组仍正常。**这一步没有截图/人工确认就不算完成。**

2)【上游候选污染，独立议题】/api/routing/resolve?model=glm-5.3-flash 至今仍返回
   glm-5.3 的凭据，根因是 modelname/variants.go 的 removableWrapperTokens 把包装词
   （flash/turbo/air/highspeed/preview/thinking/reasoning/vision/audio）从首尾剥掉，
   词法变体把 base 模型也拉进来了。请先量化影响面：哪些模型的 resolve 会返回"外来凭据"，
   routing-v2 诊断页与 persist_probe=1 各受多少影响。**不要直接改 removableWrapperTokens**——
   它同时服务 GenerateAliasVariants 与真实选路匹配；先出方案 + 回归口径，再决定改不改。
   判据：改完之后，「resolve('X') 的 candidates 是否全部属于 X」这条不变式要能作为门。

3)【模型宇宙的产品决策】集合（featured + 72h 热门）外的模型现在不显示（不冒名）。
   找出「用户能筛选但看不到分组」的模型清单，判断是否要扩宇宙；扩宇宙需要额外的
   resolve 调用，注意并发与首屏耗时。

动手前先 `git fetch origin main && git log --oneline -5`：本仓有并发会话在推 main，
提交一律用 `git commit -- <paths>` 限定 pathspec。
```
