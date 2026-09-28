# 2026-09-29 — stream 按模型分组的可用节点：模型归属确定性裁决

## TL;DR

`dashboard?tab=stream` →「按模型分组的可用节点」里，筛选 `glm-5.3` 却看不到 `glm-5.3` 分组、
反而出现筛选里没选的其它模型分组。根因是**上游 resolve 的 `raw_models` 是词法变体矩阵而不是模型等价关系**，
叠加前端「并发 resolve 谁后到谁赢、冲突就删索引」的归属裁决。修复把归属裁决抽成纯函数
`web/src/utils/modelScopeOwnership.ts`，按 canonical 身份一次性、确定性地决定。

## 现象（线上实测）

`llmgateway.internal.example.com/dashboard?tab=stream`，模型筛选 `glm-5.3`（该模型 72h 内 915 次请求，是最热模型）：

| | 修复前 | 修复后 |
|---|---|---|
| 筛选 `glm-5.3` | `glm-5.3-flash`（9 节点） | **`glm-5.3`（9 节点）** |
| 筛选 `glm-4.7` | `glm-4.7-flash`（6 节点） | **无（不冒名）** |
| 筛选 `glm-5.2` | `glm-5.2`（14 节点） | `glm-5.2`（14 节点） |
| 分组总数 | 53 | 55（新增 `glm-5.3` + `doubao-seed-2-0-code`；无模型丢失、无同名重复组） |

## 根因（两层叠加）

### 1. 上游：`/api/routing/resolve` 的 `raw_models` 是词法变体矩阵

`admin/routing.go:handleRoutingResolve` 的 `rawModels` 来自
`modelname.NormalizeRouteKeyAliases(model)`，其中 `removableWrapperTokens`
（`modelname/variants.go`）会剥掉首尾包装词：`flash / turbo / air / highspeed / preview /
thinking / reasoning / vision / audio`。实测：

```
GET /api/routing/resolve?model=glm-5.3
  canonical_id=2422803  raw_models=[glm-5.3, glm-5-3, z-ai/glm-5.3]      candidates: 17

GET /api/routing/resolve?model=glm-5.3-flash
  canonical_id=null     raw_models=[glm-5.3-flash, glm-5-3-flash, glm-5.3, glm-5-3]
  candidates 的 model_name 集合 = [free/glm-5.3-flash, glm-5-3-flash-260828, glm-5.3,
                                   glm-5.3-flash, glm/5.3-flash, z-ai/glm-5.3, z-ai/glm-5.3-flash]
```

`glm-5.3-flash` 与 `glm-5.3` 是两个不同的商品，但解析层把前者当成后者的词法变体，
把后者的凭据也列进了前者的 candidates。这是**匹配逃生口**，不是模型等价关系；
把它当作「别名归属」的权威来源就会串味。

### 2. 前端：归属裁决依赖并发完成顺序，且冲突会删索引

`QueuePerspectivePanel.loadModelScope` 在每个作用域 resolve 完成后就地
`assignAlias(raw)`，冲突时 `aliases.delete(key)`；而 resolve 是 8 并发跑的
（`RESOLVE_CONCURRENCY = 8`），**归属取决于哪个请求先返回**。于是真模型 `glm-5.3`
的作用域被词法冒名者吞掉，它的 raw 从索引里消失 → `glm-5.3` 分组整个不生成；
筛选 `glm-5.3` 命中的只能是冒名者，显示成别的模型名。

## 修复

`web/src/utils/modelScopeOwnership.ts`（新增，纯函数、可单测）+ `QueuePerspectivePanel.vue` 接线。
全部 resolve 完成后**一次性**裁决，与到达顺序无关：

1. **合并同身份作用域** —— `canonical_id` 相同，或都没有 id 且 `canonical_name` 相同 → 合成一个
   代表作用域（代表取"名字等于 canonical_name"者 → 最短 → 字典序）。避免
   `glm-5.3-flash` 与 `z-ai/glm-5.3-flash` 各出一份同名分组。被合并者的特色/热门标记并入代表。
2. **canonical 证据优先** —— 每个 raw 归「能用 canonical 证据验证」的作用域。证据有两级：
   - 响应级 `resolve.canonical_id`；
   - **绑定级**：该作用域**自己名字对应**的候选携带的 `canonical_id`。
     这一级是关键：响应级 id 只要候选跨 canonical 就为 null（实测
     `doubao-seed-2-0-code-preview` 响应 null / 自身 canonical 353857；`glm-5.3-flash`
     响应 null / 自身 canonical 2716170），只信它会把真实模型误判成"无证据"。
3. **有证据却谁都对不上 → 不归属** —— 该 raw 属于集合（featured + 72h 热门）之外的模型
   （实测 `glm-4.7` 的节点会出现在只有 `glm-4.7-flash` 的集合里）。此时宁可该模型不显示，
   也不能把节点挂到别的模型名下——那正是本缺陷本身。
4. **后缀亲和例外** —— 只在第 3 步时启用：某模型只以"日期/版本后缀"形式注册了绑定、
   没有裸名绑定（实测 `doubao-seed-2-0-code-preview` 唯一绑定是 `-260215` 形式），
   用与后端 dash 桥同源的 `-` 边界前缀判据兜底。**无这一步第 3 步会把这个真实模型整个弄丢**
   （实测验证过：加上第 3 步而没有第 4 步，分组数从 55 掉到 54，该模型消失）。
5. **无 canonical 证据可比对时** → 退回"有 canonical 的作用域"，再退回「作用域名就是该 raw」
   → 长度 + 字典序稳定兜底。**任何分支都不再从索引里删除 raw**。

## 验证

```bash
cd web
npx vitest run src/utils/modelScopeOwnership.test.ts src/components/QueuePerspectivePanel.test.ts
npx vue-tsc --noEmit
npx vitest run            # 全量
```

- 新增纯函数单测 10 例（含 4 种并发完成顺序的顺序无关性断言、绑定级 canonical 证据、
  后缀亲和、有证据不归属）。
- 新增组件级回归测试 4 条，载荷逐字段照抄线上真实响应。
- **红门取证**：`git stash push -- web/src/utils/modelScopeOwnership.ts` 回到上一版后，
  恰好 2 条新测试转红（`leaves a raw unowned…` 与 `shows nothing for a filtered model…`），
  其余 42 条仍绿 —— 证明第二轮硬化确实是这两条在守，不是把既有行为重新包装。
- **真实数据端到端**：`npx tsx` 直 import 源文件（非 JS 移植版，避免自证）跑生产 API
  + 真实 `node_update` 快照，分组差异与上表一致。
- `resolve` 失败的兜底单独有测试：`glm-5.3` 的 resolve 抛错时，精确名 fallback 仍让它
  留在自己名下，不会被 `glm-5.3-flash` 吞掉。

## 已知边界（不在本次范围）

- 上游 `removableWrapperTokens` 让 `resolve('glm-5.3-flash')` 返回 `glm-5.3` 的凭据这件事
  本身没改。routing-v2 的 resolve 诊断页仍会看到这批"外来候选"；`persist_probe=1` 也会按
  这批候选写探测。改它要动匹配矩阵（影响真实选路），属于独立议题。
- 面板的模型宇宙仍是 featured + 72h 热门（模板注释里的产品约定）。集合外的模型
  （如 `glm-4.7`）现在**不显示**，而不是像以前那样冒名显示在 flash 分组下。
- `doubao-seed-2-0-code-preview` 这类"只有日期后缀绑定"的模型靠第 4 步的名字亲和兜底，
  属启发式；若将来出现裸名与后缀名分属两个 canonical 的数据，这里会退化为不归属。
