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
---

## 2026-10-01 批判式审计补记（勘误 + 补门）

本节是对上面「回归口径」一节的**勘误**。原节写「修完之后不变式要能作为门」，
但 2026-09-29 交付时**仓库里没有任何东西能断言这条不变式** —— 下面是实测。

### 勘误 1：原「回归口径」当时并没有成为门

- 当时 5 个测试（`admin/routing_resolve_filter_test.go`）**全部只测纯函数**
  `filterResolveCandidatesByCid` 与 `NormalizeRouteKeyAliasesNoStrip`，
  从不碰数据库。
- 真正做判定的 `resolveInputCanonicalID` 接收 `*pgxpool.Pool`，**零测试覆盖**。
- 唯一跑过不变式的是一个 `/tmp` 下的 python 脚本 + 11 个手挑模型：
  不在仓库、不可重复、模型集合会随 catalog 漂移。
- 换句话说：**「11/11 通过」是一次性人工观测，不是回归门**。

### 勘误 2：原实现埋了一个比污染更糟的缺陷

原 SQL：

```sql
SELECT id FROM models_canonical
WHERE lower(canonical_name) = ANY($1)
ORDER BY id LIMIT 1
```

`= ANY()` 是**集合成员判定，不带顺序**。所以 `ORDER BY id` 实际是
「在所有命中拼写里取 id 最小的那个」，把
`NormalizeRouteKeyAliasesNoStrip` 的**精确形优先**语义整个丢掉了。

若 dot 与 dash 两种拼写同时入库（`glm-5.3-flash` id=100、
`glm-5-3-flash` id=50），输入 `glm-5.3-flash` 会解析成 **50**。
于是 `filterResolveCandidatesByCid` 会**保留错误 canonical 的候选、
丢掉正确的** —— 这比它要修的污染更糟，而且 fail-open 分支**救不了**
（因为查库「成功」了，只是答案错了）。

生产实测当前**没有**这种碰撞（`models_canonical` 无重名 canonical_name，
且无 dot/dash 并存的 canonical 对），所以是**潜伏缺陷**，不是线上故障。

### 本轮修法

1. `resolveInputCanonicalID` 改为「查出全部命中行 → 交给纯函数裁决」，
   新增 `canonicalIDByVariantPriority(variants, matches)`：
   取**在变体列表中位置最靠前**的那行（即精确形），同名重复时取最小 id
   保证确定性。纯函数可单测，碰撞场景成为红-able 用例。
2. 新增 `admin/routing_resolve_invariant_test.go`：
   - **纯函数红门** 4 条：精确形压过小 id、dash 输入压过小 id、
     平局稳定取最小 id、无命中报 not-found。
   - **真库不变式门** `TestResolveCandidatesInvariant_Live`：
     遍历 `models_canonical` **全目录**（实测 958 个），
     复现 resolve 的三段 WHERE + **过滤后**候选集，断言
     「所有 candidate.canonical_id 都属于 resolve_input(X)」。
     DB 不可用时显式 `t.Skipf`，并写明「跳过不构成证据」。
   - `TestResolveRawModelsStillLeak`：把下面勘误 3 的已知缺口钉成用例，
     避免它被无声遗忘。

### 验证

```
# 纯函数门
go test ./admin/ -run 'TestCanonicalIDByVariantPriority|TestFilterResolveCandidatesByCid|TestNormalizeRouteKeyAliasesNoStrip' -count=1
→ ok（9 条全绿）

# 真库不变式门（958/958）
TEST_RESOLVE_INVARIANT_DB_URL=postgres://… go test ./admin/ \
  -run TestResolveCandidatesInvariant_Live -count=1 -v
→ invariant verified for 958/958 catalog models  PASS 3.54s

# 变异验证（证明门有守卫力）
① 抹掉变体优先级（= 旧 ORDER BY id 语义）→ 恰 2 条碰撞测试转红
   （got 50 want 100 / got 7 want 900）
② 关闭 filterResolveCandidatesByCid → 真库门报 158 个 INVARIANT BROKEN 并转红
```

**门自身的一个失误（如实记录）**：真库门第一版只查 SQL 候选集、没跑
`filterResolveCandidatesByCid`，量的是**过滤前**状态，于是报出约 30 个
假 `INVARIANT BROKEN`（claude-fable-5-thinking 193851、
deepseek-v4-*-260425 122349/122350 等）。已修正为「查候选集 + 跑过滤器」
再断言。**量错阶段的红门比没有门更糟** —— 记录在此以免重演。

### 勘误 3：`raw_models` 仍然被污染（本轮**未**修）

`resolveInputCanonicalID` 只约束了 `candidates`，响应的 `raw_models`
仍返回含包装词剥离结果的完整矩阵。生产实测：

```
GET /api/routing/resolve?model=glm-5.3-flash
  candidates   = 16 条，全部 canonical_id=2716170   ✅ 不变式成立
  raw_models   = ['glm-5.3-flash','glm-5-3-flash','glm-5.3','glm-5-3']  ❌ 仍含 base
```

即：**不变式只覆盖 candidates，不覆盖 raw_models**。
dashboard 侧由 `web/src/utils/modelScopeOwnership.ts` 的绑定级 canonical
证据做了补偿，但**任何其它 `raw_models` 消费方仍暴露**。
`TestResolveRawModelsStillLeak` 把该状态钉住：若将来上游修好，这条会
`t.Skip` 并提示「同时关掉 modelScopeOwnership.ts 的补偿」，
使两处不会单边漂移。

### 遗留

- `raw_models` 污染（上条）—— 独立议题，需动 `removableWrapperTokens`
  的消费方式或响应组装，本轮刻意未做。
- `canonicalIDByVariantPriority` 的平局分支（同名重复）生产不可达，
  属防卫性代码。
- ~~2026-09-28 记录的另一并发会话 `admin/session_panorama_handler.go`
  在本轮审计期间处于半成品状态（重构到 `loadSessionTimelineInTx` 后
  残留 `rows.Err()`，致 `admin` 包编译失败），与本轮改动无关。~~
  **已于 2026-10-01 由 d2ff2c3b1 在 origin/main 修复**（删除悬空块 +
  随之无引用的 `fmt` import；错误传播由 `loadSessionTimelineInTx` 的
  `return timeline, rows.Err()` 承担，调用方原样上抛，未削弱检查）。
  本地快进到 907d67b85 后 `go build ./...` / `go vet ./admin/` 均通过，
  `go test ./admin/` 全包 66.8s 绿。该遗留项就此关闭。

## 2026-10-01 生产复核（修复上线后）

修复（9cb842d3e）推上 origin/main 后的**线上真实响应**复核，走已登录
浏览器直接读生产 API（非 DB 直连、非 fixture）：

```
GET https://llm.kxpms.cn/api/routing/resolve?model=glm-5.3
  resolution_path = canonical                       ← 精确形优先，未回退变体矩阵
  canonical_id    = 2422803 / canonical_name = glm-5.3
  candidates      = 17 条，全部 canonical_id=2422803、
                    standardized_name 全部 glm-5.3   ✅ 不变式在生产成立
  raw_models      = ['glm-5.3','glm-5-3']           ❌ 仍含词法变体

GET https://llm.kxpms.cn/api/routing/resolve?model=glm-5.2-flash
  resolution_path = canonical
  candidates      = 21 条，全部 canonical_id=173264  ✅ 未把别的 canonical 放进候选
  raw_models      = ['glm-5.2-flash','glm-5-2-flash','glm-5.2','glm-5-2']
                                                        ❌ 剥掉包装词后混进 base
```

即勘误 3 的结论在生产**原样成立**：不变式覆盖 candidates、不覆盖
raw_models。`TestResolveRawModelsStillLeak` 本轮复跑仍绿，输出
`[glm-5.3-flash glm-5-3-flash glm-5.3 glm-5-3]`，与生产实测逐字一致 ——
钉住用例与生产事实同源，不是各自独立的断言。

`TestResolveCandidatesInvariant_Live` 本轮**未跑**（本地无
`TEST_RESOLVE_INVARIANT_DB_URL`，输出显式 `SKIPPING … A skip is NOT
evidence`）。958/958 的全目录门证据仍属 09-29 那次；本轮的生产复核
是**抽样两个真实模型**的实际响应，比 DB 全目录门覆盖面窄，
两者不可互相替代。

### dashboard 侧人工验收：未能完成（环境阻断，非回归）

原计划在 `dashboard?tab=stream` 按模型分组筛 `glm-5.3` 目视确认
出现 `glm-5.3` 分组而非 `glm-5.3-flash`。实际打开后：

- 「按处理队列」确为默认激活态（`control-btn--active`），
  「实时请求流」tab 亦激活 —— 视图入口正确。
- 但**模型分区整块不渲染**，页面只显示
  `队列数据未接入（dispatch 未启用或未 wired）`，
  节点状态矩阵同时显示「暂无节点数据」。

代码侧定位（`web/src/components/QueuePerspectivePanel.vue:1256`）：
模型分区的渲染门是

```js
v-if="hasReportedRawModels && (hasModelGroups || modelScopeLoading || modelScopeError)"
```

而 `hasReportedRawModels`（同文件 :860）= `nodes.some(n => Array.isArray(n.raw_models))`，
数据源是**实时 SSE 的 `LiveNodeStatus.raw_models`**，不是 resolve 接口。
线上此刻 SSE 节点链路无数据 ⇒ 门恒 false ⇒ 整个模型分区缺省隐藏
（该设计意图见源码注释：「实时 SSE 不提供 raw_models 时保持整个分区隐藏，
避免把未知误报为无绑定」）。

因此**本轮无法取得模型分组的目视证据**。这不是 9cb842d3e 引入的回归
（该提交只改 canonical 选取优先级与不变式门，未触及 `nodes`/SSE 投影），
而是 SSE 节点链路当前无数据。同一阻断对 `glm-5.2` 同样成立，
故「glm-5.2 组正常」这一项本轮**同样未取得证据**，不做通过声明。

### 补充：SSE 节点链路取证（同一根因的更深一层）

上一节把阻断归到「SSE 节点链路无数据」。本轮继续沿链向下定位，
结论是**断点在服务端推不出快照，而非前端渲染门或本轮改动**。

已排除的环节（逐个查证，不是推测）：

| 环节 | 位置 | 结论 |
| --- | --- | --- |
| 前端渲染门 | `QueuePerspectivePanel.vue:1256/860` | 逻辑正确，门读 SSE 的 `raw_models`，无数据即按设计隐藏 |
| SSE 端点存在 | `admin/handler.go:1014` `/api/admin/live-stream` | 存在；线上实测 `EventSource` 请求 `outcome=pending`（长连接已建立） |
| provider 注入 | `main.go:2527` `SetNodeStatusProvider(liveNodeStatusCache.get)` | 接线存在 |
| `fps` 非空 | `main.go:1329` `credentialfpslot.New` | `slot.go:199` 该构造函数**从不返回 nil**（只做默认值兜底），故 `decorateFPNodeState` 外层 `if fps != nil` 恒真 |
| 刷新循环 | `main_livestream.go:727-751` | 正常；DB 失败保留上次快照并限频告警，不会把快照洗成空 |
| 节点查询 | `main_livestream.go:500-513` | 全表扫 `credentials`，无线上不可达因素 |

线上同时可见的事实：SSE 连接状态**长期停在「连接中」**（`status--warn`），
网络面板只有那一条 `eventsource` 常驻请求、无任何数据型 XHR，
控制台无报错。即**连接建成了，但一帧快照都没推出来**，
前端因此 `nodes` 恒空 → 节点矩阵报「暂无节点数据」→ 模型分区因门控隐藏。

这三点互相印证，指向服务端 live-stream hub 未产出快照。
继续定位需要**部署侧日志**（hub 的发送条件 / 快照源是否被上游关闭），
超出本仓范围，不在本轮凭代码可判定的范围内，故记录为待部署侧取证项。

附带发现一处**独立的健壮性缺口**（本轮未修，仅登记）：
`main_livestream.go:591` 的 `if len(keys) == 0 { return nil }` 位于
`modelsByCred` 已经查得之后、`RawModels` 赋值之前。当
`credential_model_bindings ⋈ provider_models` 返回空（即无任何绑定行）时，
该早退会让**已经查到的绑定信息一并被丢弃**。
当前它对本次现象不构成解释（线上 resolve 能返回 17/21 条 candidate，
说明绑定表非空），但一旦绑定表真的为空，
`RawModels` 的缺失将无法与「查询失败」区分 —— 与本仓既有的
「错误不得渲染成看起来合法的零」是同一类形状，建议后续单独处理。
