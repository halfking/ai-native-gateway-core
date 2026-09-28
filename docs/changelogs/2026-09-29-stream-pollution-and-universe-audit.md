# 2026-09-29 — stream 上游污染量化 + 模型宇宙审计

## TL;DR

承接 `2026-09-29-stream-model-group-alias-ownership.md` 二轮审计后的两件独立议题：
1. **上游 `/api/routing/resolve` 候选污染**：11/72 模型 (15.3%) 的 resolve 响应里
   携带多个 canonical_id 的凭据，是 `modelname/variants.go` 的
   `removableWrapperTokens` 词法变体（剥首尾 flash/turbo/air/highspeed/
   preview/thinking/reasoning/vision/audio）造成的。本轮量化影响面、给出
   修法选项与回归口径，**未修改 removableWrapperTokens**。
2. **模型宇宙的产品决策**：706 个可筛模型里 638 个在面板上"看不到分组"，但
   0 个有 72h 流量（top-50 已囊括全部热模型）；featured 33 个里有 22 个近 72h
   0 流量是宇宙冗余。

---

## 一、上游污染量化

### 复现

```bash
curl -s 'http://127.0.0.1:8782/api/routing/resolve?model=glm-5.3-flash' -H "Authorization: Bearer $TOK"
```

返回：
- `canonical_id: null`（响应级无法裁决，候选跨 canonical）
- `candidates` 40 条，含两个 canonical_id `[2422803, 2716170]`
  - `2422803` = glm-5.3（base 模型）
  - `2716170` = glm-5.3-flash（本意）
- 来自 base 模型的凭据是词法变体把 `glm-5.3-flash` 剥掉 `flash` 得到
  `glm-5.3`，再走 SQL `lower(v.raw_model_name) = ANY($1)` 把 glm-5.3
  的 raw 绑也命中了。

### 量化影响面（72 个 featured + 72h hot top-50 模型全集）

污染判据**保守版**：响应 candidates 中非空 canonical_id 集合大小 > 1
（即多个不同商品凭据被并进同一份响应）。

| 污染模型 | 响应 cid | 候选 cid 集合 | 候选数 | 外来凭据来源（样本） |
|---|---|---|---|---|
| `claude-fable-5-thinking` | None | `[193851, 2067352]` | 14 | anthropic/claude-fable-5:batch |
| `doubao-seedance-2-0-fast-260128` | None | `[122342, 353856]` | 3 | (日期后缀触发跨 canonical) |
| `gemini-3-pro-image-preview` | None | `[5279, 375090]` | 5 | (preview 包装词) |
| `gemini-3.1-flash-image-preview` | None | `[370, 375093]` | 6 | flash + preview 双重剥离 |
| `glm-4-air` | None | `[56, 58]` | 11 | air 包装词 |
| `glm-4-flash` | None | `[56, 57]` | 14 | flash 包装词 |
| `glm-4.5-air` | None | `[53, 55]` | 12 | air 包装词 |
| `glm-5.3-flash` | None | `[2422803, 2716170]` | 40 | flash 包装词 |
| `gpt-4-turbo` | None | `[3, 567164]` | 9 | turbo 包装词 |
| `minimax-m2.7-highspeed` | None | `[69, 122226]` | 16 | highspeed 包装词 |
| `o1-preview` | None | `[5, 933674]` | 5 | preview 包装词 |

合计 **11/72 模型 (15.3%)**。**全部命中都是 removableWrapperTokens 的同源剥离**：
包装词后缀/前缀模型 → 词法变体把 base 模型也拉进来。

### 影响面分类

| 消费者 | 影响 |
|---|---|
| **routing-v2 诊断页** (`GET /api/routing/resolve`) | 看到外来凭据，运维误判 "glm-5.3-flash 有 40 节点" 实际其中 ~50% 是 glm-5.3 的；reorder_revision 留空（mixed canonical_id）拖拽功能被禁；candidates 多算一次去重开销。 |
| **`persist_probe=1`** (`POST /api/routing/resolve?persist_probe=1`) | `routing_resolve_probe.go:persistResolveProbe` 把全部 candidates 写入 `routing_decision_log_hot.decision_trace.planned_candidates`。`glm-5.3-flash` 的探测审计里会混进 glm-5.3 的凭据（污染审计与失败归因路径）。 |
| **面板 dashboard?tab=stream** | 本轮 `modelScopeOwnership` 修法已把它从 UI 侧拦截——污染 candidates 在归属裁决里被判为"有证据无主 → 不归属"，**实际不会挂到错误分组下**。但 `modelCandidatesByRawModel` 仍包含污染凭据（用作 `selectNode` 的 raw 绑定回填，目前没有显式读取污染候选的代码路径）。 |
| **实时选路** | runtime 走 `is_routable` flag + URSM v2，污染 candidates 的 `Routable` 由各自 canonical 的健康状态决定，不影响选路正确性。 |

### 修法选项（**未实施**，等用户决）

#### 选项 A：在 SQL 侧分离"别名命中"与"凭据返回"

`handleRoutingResolve` 当前把 `variants` 整体喂给 `lower(...) = ANY($1)`：
```sql
WHERE lower(v.raw_model_name) = ANY($1)
   OR lower(COALESCE(mo.standardized_name, v.raw_model_name)) = ANY($1)
   OR lower(mc.canonical_name) = ANY($1)
```

修法：
- 把 `variants` 拆为 **`alias_variants`（含 wrapper 剥离）** 用于
  `mc.canonical_name = ANY($1)` 与 `v.raw_model_name` 第一轮匹配；
- 把 `exact_variants`（不含 wrapper 剥离，仅 dot↔dash + 大小写）用于
  第二轮最终凭据筛选。

变更点：
- `admin/routing.go` `handleRoutingResolve` SQL 改成两段 OR：
  ```sql
  AND (
    v.canonical_id IN (SELECT canonical_id FROM resolve_first_pass WHERE match_variant = ANY($alias))
    AND v.raw_model_name = ANY($exact)
  )
  ```
  或保留单查询、查询后内存按 `canonical_id` 二次过滤。

#### 选项 B：resolve 末端按 canonical_id 二次过滤

不改 SQL，在拿到 candidates 后按"输入模型的 resolved canonical_id"过滤：
```go
inputCid := resolveInputCanonicalID(ctx, normalizedModel)  // 走只含 exact_variants 的轻量查询
filtered := make([]resolveCandidate, 0, len(candidates))
for _, c := range candidates {
    if c.CanonicalID == nil || *c.CanonicalID == inputCid {
        filtered = append(filtered, c)
    }
}
candidates = filtered
```

- **优点**：SQL 不变，diff 最小；
- **缺点**：`persist_probe=1` 仍可能把 pollution 当成 planned_candidates 写库；
  → 选项 B 配合选项 C 一并修。

#### 选项 C：persist_probe 也按 canonical_id 过滤

`routing_resolve_probe.go:persistResolveProbe` 入口加 `expectedCid` 过滤；
污染 candidates 不入 `decision_trace.planned_candidates`。
审计价值：探测表回到"真正为 X 模型探过"的语义。

### 回归口径（修后必须满足）

1. **不变式**：`resolve('X')` 的 candidates 集合中，所有 candidate 的
   `canonical_id` 都等于 `resolve_input(X)` 解析出的 canonical_id
   （或 null legacy 绑定）。这条不变式应作为不变量测试 / 端到端契约门。
2. **不被破坏**：
   - dot↔dash 桥（`qwen2.5-72b-instruct` ↔ `qwen2-5-72b-instruct`）：
     `modelname.versionPunctuationCartesian` 路径不变
   - Provider 前缀（`z-ai/glm-5.3` ↔ `glm-5.3`）：不变
   - 日期后缀（`-260828`、`-260215`）：alias 命中仍允许，**只剔除
     canonical_id 跨商品的那部分**
3. **回归测试**（admin/routing_resolve_test.go 新增）：
   - `TestResolve_FlashSuffixNotPollutesBase`：resolve(glm-5.3-flash)
     只返回 canonical=2716170 的 candidates，不含 2422803
   - `TestResolve_BaseUnaffected`：resolve(glm-5.3) 仍返回 canonical=2422803
     的所有 candidates（数量不变）
   - `TestResolve_DashBridge`：resolve(qwen-2-5) 与 resolve(qwen2.5)
     等价，candidates 集合一致
4. **真实数据红门取证**：用 2026-09-29 实测数据（11 个污染模型）跑
   before/after 量化 diff，每个模型的 candidates 数量与 canonical_id
   集合必须收敛到"单 canonical_id"。

### 推荐

- **选项 B + 选项 C 组合**：SQL 不动、resolve 末端过滤污染 +
  persist_probe 也过滤；diff 小、不动 removableWrapperTokens、回归口径清晰。
- 不直接改 removableWrapperTokens：`GenerateAliasVariants` 与真实选路匹配
  都消费它，单独抽 `removeWrapperForCanonical` 走另一路径风险更高。

---

## 二、模型宇宙审计

### 数据来源

- Dropdown（用户能筛）：`/api/routing/available-models` →
  `popular` (33) + `families` (~120) + `unmapped` (5) → **706 唯一**
- Panel scope（看得到分组）：`featured_models` (33) + `top-models limit=50`
  → **72 唯一**（11 个同时在 featured 与 hot-50；22 个 featured 0 流量；
  39 个 hot-50 但未 featured；featured 与 hot 唯一并集 = 33+50-11=72）
- `listTopModels` 实现：`admin/logs.go:1207`，`if limit > 50 { limit = 50 }`
  服务端硬上限 50。

### 隐式集合（filterable but no group）

| 集合 | 数量 | 说明 |
|---|---|---|
| Dropdown | 706 | 用户能筛的所有 canonical_name |
| Panel scope | 72 | featured + 72h top-50 |
| Hidden（筛选得到、面板无分组） | 706 − 72 = **638** | |
| Hidden & 有 72h 流量 | **0** | top-50 已囊括全部 72h 活跃模型 |
| Featured & 无 72h 流量 | **22** | 宇宙冗余，详见下表 |

### 22 个冗余 featured（可考虑清理）

`claude-fable-5`、`claude-fable-5-thinking`、`claude-opus-4-7`、`claude-opus-4-8`、
`claude-opus-5`、`claude-opus-5-5`、`claude-sonnet-5`、`doubao-1-5-pro-32k`、
`doubao-seed-2-0-code`、`doubao-seed-2-0-code-preview`、`gemini-3-flash-preview`、
`gemini-3.5-flash`、`gpt-5.6-luna`、`gpt-5.6-sol`、`gpt-5.6-terra`、`gpt-6-astra`、
`gpt-6-luna`、`gpt-6-sol`、`gpt-image-2`、`grok-4.7`、`minimax-m2.7`、
`minimax-m2.7-highspeed`

### 39 个 hot-50 但未 featured（潜在扩宇宙候选）

按 72h 请求量倒序：`glm-4` (341)、`gpt-4-turbo` (288)、`glm-4-flash` (278)、
`claude-3-opus` (273)、`claude-3-7-sonnet-20250219` (263)、`deepseek-chat` (251)、
`gemini-3-pro-image-preview` (177)、`claude-3-sonnet` (146)、`qwen-turbo` (141)、
`recov-gpt-56` (100)、`nemotron-3-embed-1b` (93)、`deepseek-v4.1-flash` (92)、
`gpt-4` (81)、`gemini-3.1-flash-image-preview` (69)、
`doubao-seedance-2-0-fast-260128` (65)、`dreamina-seedance-2-5` (63)、
`deepseek-flash` (60)、`recov-gpt-56-mini` (57)、`wan2.6-t2i` (54)、
`glm-5.3-flash` (48)、`doubao-seedance-2-5` (33)、`dreamina-seedance-2-0-fast` (30)、
`qwen-image-2.0-pro-glb` (30)、`doubao-embedding-vision` (30)、`glm-5` (25)、
`sensenova-6.8-flash-lite` (24)、`glm-4-air` (24)、`glm-4.5-air` (21)、
`gpt-image-1-mini` (18)、`sensenova-u1.5-fast` (18)、…

### 推荐（产品决策）

1. **不动 hidden 638**：其中 0 个有 72h 流量，是历史/测试模型被
   `available-models` 收录，用户若主动搜才碰得到；目前展示为空符合
   "universe = featured ∪ 72h top-50" 的设计语义。
2. **清理 22 个 0 流量 featured**：建议 `PATCH /api/routing/featured` 收缩，
   但**要保留** `claude-fable-5-thinking` 这种被上游污染命名空间影响的对照
   模型（用于诊断修复效果）。
3. **不扩 top-50 → top-100**：39 个 hot-50 未 featured 中 6 个是带包装词
   污染的（`glm-4-flash`、`glm-4-air`、`glm-4.5-air`、`gemini-3-pro-image-preview`、
   `gemini-3.1-flash-image-preview`、`doubao-seedance-2-0-fast-260128`），
   先把上游污染修了再扩才有意义，否则扩宇宙 = 扩污染。
4. **追加成本**：每加 1 个 featured 多 1 次 resolve（已有 8 并发），
   33→50 = +17 resolve ≈ +2.1s 首屏；50→100 = +50 resolve ≈ +6.3s。
   当前首屏 ~5s，加倍可接受；若超过 15s 须再评估。

### 执行约束

- `modelScopeOwnership` 的不归属路径（第 3 步 + 第 4 步后缀亲和）使得
  "hidden 模型永远不冒名"已成立；扩宇宙不会重新引入冒名风险。
- 任何扩宇宙动作必须**先修上游污染**再执行；顺序错了会复制本次缺陷的
  更大版本。