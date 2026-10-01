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

> **本节结论已被下一节推翻并订正。** 下面两句当时是错的：
> 「继续定位需要部署侧日志……超出本仓范围」「待部署侧取证项」。
> 252 本机 SSH 可达，日志取回后根因立刻明确，且**与当时「PG 不可达」的
> 推测不同**。留此痕迹以免重演同一个错误判断。

附带发现一处**独立的健壮性缺口**（本轮未修，仅登记）：
`main_livestream.go:591` 的 `if len(keys) == 0 { return nil }` 位于
`modelsByCred` 已经查得之后、`RawModels` 赋值之前。当
`credential_model_bindings ⋈ provider_models` 返回空（即无任何绑定行）时，
该早退会让**已经查到的绑定信息一并被丢弃**。
当前它对本次现象不构成解释（线上 resolve 能返回 17/21 条 candidate，
说明绑定表非空），但一旦绑定表真的为空，
`RawModels` 的缺失将无法与「查询失败」区分 —— 与本仓既有的
「错误不得渲染成看起来合法的零」是同一类形状，建议后续单独处理。

## 2026-10-01 根因定位与修复（6f629ad8b）

### 拓扑与取证入口

`llm.kxpms.cn` 在 252 上是 **SNI 流代理**转给 `itestu_nginx_backend`，
后端即 `llmgo-252-dev.service`（不是 154/245）。确认路径：
`/etc/nginx/stream.d/sni-proxy.conf` 的 `llm.kxpms.cn → itestu_nginx_backend`。
注意本地 `dig llm.kxpms.cn` 被代理劫持成 `198.18.0.4`，**不能用 DNS 判源站**。

### 真实根因（不是「PG 不可达」）

当时推测是「数据库不可达」。**该推测错误** —— `psql` 手工连库完全正常，
PG 本身健康。日志实况：

```
04:26:10 WARN postgres disabled error="timeout: context deadline exceeded" retry_budget=10m0s
04:26:10 WARN telemetry DISABLED — no request logs will be persisted; live stream will be empty
04:26:10 WARN live stream hub created WITHOUT database — initial replay empty, node status disabled
04:34:19 WARN postgres unreachable at boot, retrying attempt=1
         error="ensure candidate_failure_logs heap partitions: timeout: context deadline exceeded"
04:38:14 WARN postgres disabled error="ensure candidate_failure_logs heap partitions: …" retry_budget=10m0s
04:38:14 WARN live stream hub created WITHOUT database …
```

真正的因果链是**启动期迁移抢连接超时**，不是数据库故障：

```
689 self-heal 超时
  → 整条 ensure 链返回 err → postgres disabled
  → telemetry DISABLED → live stream hub 无 DB（node status disabled）
  → SSE 推不出 nodes 快照 → hasReportedRawModels 恒 false
  → 「按模型分组的可用节点」整块永久隐藏
```

### 结构性成因

`migCtx` 只有 **3 分钟**总预算（`db/db.go:218` `context.WithTimeout(ctx, 3*time.Minute)`），
而 **78 条 `ensure*` 共享它**；`ensureCandidateFailureLogsHeapPartitions` 排在
倒数第二位。共享库持续写流下，前面任一 ensure 的 ACCESS EXCLUSIVE 锁等待
吃掉预算后，轮到本步时剩余时间不够 → `context deadline exceeded` → 冒泡
令整条链失败。

**SQL 内的 `SET LOCAL statement_timeout='10min'`（`db.go:1405`）救不了**：
context 到期由客户端直接掐断连接，服务端超时设置根本没机会生效。
**外层 context 才是真正的杀手** —— 这是本案最容易看错的一层。

### 修法

只给这一步独立 5 分钟预算（`candidateFailureLogsHeapPartitionsBudget`），
**不动 78 条链共用的 3 分钟约定**（改全局影响面过大，且 3 分钟对绝大多数
幂等 no-op ensure 是合适的）。用 `context.WithoutCancel(migCtx)` 脱离共享
取消链；本步 SQL 自身幂等，且仍在 `migrationsPinned` 抬升
statement_timeout 的窗口内执行，失败可安全重试。

### 顺带修掉一个「锚错形状」的静态门

`TestMigration694SelfHealPinsShanghaiTimezone` 原本断言字面量
`ensureCandidateFailureLogsHeapPartitions(migCtx)` —— 锚的是**变量名**而非
意图，所以本轮修复必然让它转红。改为锚意图：调用存在、仍在
`if err := …; err != nil { return err }` 守卫内、传入带超时的 `…Ctx` 变量。
694 的时区收敛断言与 heap 存储契约断言原样保留，未削弱。

变异验证：删掉调用 → 红；改成 `_ = db.ensure…` 吞错误 → 红；
仅换 context 变量 / 是否 `WithoutCancel` → 绿（设计如此：门锚意图不锚实现）。

**自曝两处自己写错的判据**（都是门当场报红纠正，非静默通过）：
① 用 `strings.Index(callLine, "\n")` 截行，命中了转义序列里的反斜杠 n，把行腰斩；
② 从匹配点而非行首取片段，把 `if err := ` 前缀切掉。

### 尚未复验

修复已推 origin/main（`6f629ad8b`），但**生产复验未做**：252 需重新部署
+ 重启才能加载新二进制，重启后 `postgres` 需在 boot 期成功连上，
`hasReportedRawModels` 才可能为真、模型分区才会渲染。
**在此之前，第 2 项（glm-5.3 / glm-5.2 目视验收）仍不得声明通过。**

## 2026-10-01 拓扑定案：验收目标环境从未部署过被验收的修复

本节纠正前面两节连续两个错误归因，并给出可复核的定案结论。

### 三个主机，三个角色（实测）

| 主机 | unit | 入口 | 当时的 build_seq |
| --- | --- | --- | --- |
| **154**（生产） | `llm-gateway-go-canary@8782` | **`llm.kxpms.cn` 实际落点** | **2356 / `2d750fb4`** |
| 245（预发布闸门） | `llmgo-245-canary@8781\|8782` | `llmgo.kxpms.cn` 等 | 2372 / `d2af305a` |
| 252 | `llmgo-252-dev` + PG17 | 仅 dev | —— |

定位手法：`curl -fsS -m 10 https://llm.kxpms.cn/healthz` 读回 `build_seq`，
再在 154 上 `curl 127.0.0.1:8782/healthz` 逐字对上。**连续 8 次采样一致**，
不是抖动。

### 两个被推翻的错误

1. 「llm.kxpms.cn 的后端在 252」——**错**。252 只有 dev 与 PG17；
   252 的 `stream.d/sni-proxy.conf` 把 `llm.kxpms.cn` 转给
   `itestu_nginx_backend`，而终点是 **154 生产**。
2. 「252 dev 处于 postgres 降级态、所以模型分组不渲染」——**因果错**。
   252 dev 确实三次 boot 降级（`ensure candidate_failure_logs` 超时，见上节），
   但**它不是 `llm.kxpms.cn` 的服务对象**，那组日志解释不了线上现象。
   本节之前把两件事连成一条因果链，是错的。

### 决定性事实

```
9cb842d3e (canonical 精确形优先 + 不变式真门)  in  2d750fb4 (154 现网)?  NO
6f629ad8b (689 self-heal 独立预算)             in  2d750fb4?             NO
317f28556 (悬空 rows.Err 事故源)               in  2d750fb4?             NO

154 现网落后 main：175 个提交
```

**本文件开头「已交付」那一节的修复，从来没有到过验收所用的那个环境。**
在 `llm.kxpms.cn` 上筛选 glm-5.3 看不到正确的 glm-5.3 分组，是
**必然结果**，不是回归、也不是环境问题 —— 那个实例上根本不存在被验收的逻辑。

推论：第 2 项目视验收此前全部无效，无论 UI 表现如何都不能作为
9cb842d3e 的验收证据。要真正验收，必须先把修复推进到 154；
而按 deploy-245/deploy-154 的闸门约定，顺序是 **245 先 → 验证 → 154**。

### 附带：245 一次失败部署的副作用（如实登记）

向 245 部署 `build_seq 2373` **失败并自动回滚**到 2372。触发点是
`[9.5/9] 验证目标机自身 nginx→gateway (127.0.0.1:443/healthz)` 30s 超时，
按 skill 约定立即回滚（回滚本身成功：healthz + running release + PG 均 OK）。

但 nginx 事后复查是 `active` 且 443 返回 200 —— 即该次超时疑为**误报**，
发生在 handoff 耗时 `351591ms`（约 5.9 分钟）之后的窗口内。

更值得注意的是 adopt 检测的误判链：

```
⚠ 8781/8782 均无监听 —— 按 fresh 主机处理，active 取契约端口 8781
⚠ active unit 无法从监听进程归属解析，按端口推导为 llmgo-245.service
```

> **⚠ 2026-10-01 05:52 订正本节**（下节「245 蓝绿契约只补了一半 vhost」给出实测）：
> 上面这段把 `llmgo-245.service` 描述成「被误启用的弃用 unit」、把
> `canary@8782` 描述成「无监听的僵尸 unit」，**两处都与后续实测不符**：
> `canary@8782` 现已**正常监听 8782**；而 `llmgo-245.service` **不是可随手停掉的
> 残留进程**——它占的 8781 是 `download.kxpms.cn` 的唯一 upstream，
> 停它会直接下线该域名。脚本侧的归属解析缺陷已由 `3d10c8b15` 修复（不再回落到
> 弃用 `$SERVICE_NAME`），但**现网 nginx 配置的缺口仍在**，两者不是同一件事。
> 保留原文是为了记录当时的误判链，**不要照原文行事**。

`llmgo-245.service` 正是 skill 明令「**弃用遗留 unit，勿使用**」的那个。
回滚后 245 实际状态（实测 `ss -ltnp` + `systemctl show`）：

- 监听 8781 的是 `llmgo-245.service`（`ExecMainStart=05:07:07`）
- `llmgo-245-canary@8781.service`：`failed`（`Result: timeout`，04:54:45
  因 `State 'stop-sigterm' timed out` 被 SIGKILL）
- `llmgo-245-canary@8782.service`：`active/running`
- `run/active-port` 记录 8782

即：站点对外仍在正常服务（无对外故障），但 245 的蓝绿 unit 拓扑与
`deploy-seamless` 的契约不符，下次部署会被同样的归属解析继续误导。
**脚本侧缺陷已在 `3d10c8b15` 修复；现网 nginx 侧缺口见下节，本轮不动。**

## 2026-10-01 154 生产侧取证：第三条独立故障

在 154（`llm.kxpms.cn` 真实后端）上继续取证，发现**第三条、与前两条都不同的**故障。

### 154 的功能与缺陷都在

模型分组功能**存在于 154 现网版本** 2d750fb4：

```
b2e6e1dd2 (qp-layer--model-groups 引入)  in 2d750fb4?  YES
e295bdeb3 (hasReportedRawModels 门引入)  in 2d750fb4?  YES
```

故上一节「验的环境里没有被验的东西」需**精确化**：不是"功能不存在"，
而是"**功能在、缺陷也在**"——`9cb842d3e`（canonical 精确形优先 +
不变式真门）不在 154 上，这正是本任务要修的那个 bug。

### 154 的 node status 反复超时

```
09-30 21:47:20  postgres connected
09-30 21:47:33  live stream hub: DB-backed replay and node status enabled
10-01 04:50:51 / 05:02:53 / 05:05:57 / 05:08:15
  WARN live stream: node status refresh failed  error="timeout: context deadline exceeded"
10-01 05:14:03  ERROR router: URSM v2 FilterAndScore failed …
  error="ursm.v2: redis unavailable: … redis HGETALL pipeline failed: context deadline exceeded"
```

`main_livestream.go:498` 给 provider 的预算是 **1.5 秒**。而
`decorateFPNodeState` 里的 Redis 批量取节点态吃的就是这个预算。252 的
Redis 明显在争用（URSM 的 HGETALL 管线同样超时），二者指向同一压力源。

### 已排除的两个猜测（都不是原因）

| 猜测 | 实测 | 判定 |
| --- | --- | --- |
| 绑定 JOIN 查询慢，吃掉 1.5s | `credential_model_bindings ⋈ provider_models` 实测 **5.476 ms** | 排除，SQL 不是瓶颈 |
| `main_livestream.go:591` 的 `len(keys)==0` 早退 | 73 凭据中 **65 个有绑定、1889 行**，`keys` 必非空 | 排除，该早退不是本次原因 |

（该早退本身仍是独立的健壮性缺口，见上文登记，此处仅澄清它**不是**本次成因。）

### 仍未闭合的一环（如实标注，不硬下结论）

告警按 30s 限频，25 分钟内只出现 4 次，说明**多数刷新其实是成功的** ——
按 `liveNodeStatusCache.refreshWith`（`main_livestream.go:727-745`）的语义，
成功即写入快照、失败仅保留上次快照，那么 154 的 `nodes` 快照**本应非空**，
前端 `hasReportedRawModels` 也**本应为真**。

但前端节点矩阵实际报「暂无节点数据」。**这一矛盾本轮未闭合**：
可能是 SSE 推送路径未把节点快照送到前端，也可能是 1.5s 预算下的
间歇失败叠加首次快照为空造成长时间空窗。

**因此第 2 项维持未通过，且本轮不把「Redis 争用」写成已证实的根因** ——
它是一个有证据支撑的**嫌疑方向**，不是已闭环的结论。下一步需要在 154 上
直接取一次 SSE 首帧的 `nodes` 载荷来判定，这需要登录态 API 取证。

## 2026-10-01 浏览器验收轮：环境故障定性 + 生产数据验收 + 一次结论订正

### 订正：「不推进 154 就无法验收」——这条不成立

上一节把第 2 项的阻断归给「154 落后 main 175 个提交」。本轮实测**推翻**了它：

```
git diff --stat 2d750fb4 HEAD -- web/src/utils/modelScopeOwnership.ts   → 空
git log --oneline 2d750fb4..HEAD -- web/src/utils/modelScopeOwnership.ts → 空
git log --oneline 2d750fb4..HEAD -- web/src/components/QueuePerspectivePanel.vue → 空
```

被验收的判据由 `modelScopeOwnership.ts`（归属裁决）+ `QueuePerspectivePanel.vue`
（渲染门）决定，这三个文件在**线上版本与 HEAD 之间逐字节一致**。
即：**这条验收判据在 154 和在 main 上跑的是同一份代码**，175 个提交的差距
对它没有影响。推进 154 不是第 2 项的前置条件（其它判据另算）。

### SSE 服务端是健康的（两种身份对照）

在 154 上用 `LLM_GATEWAY_SECRET_KEY` 现铸短命 HS256 JWT
（`iss=llm-gateway`、`aud=["llm-gateway-api"]`、`tenant_id` 必须是**字符串**，
铸成数字会被 `admin.JWTClaims` 解析拒绝并返回 401），经 nginx 打
`https://llm.kxpms.cn/api/admin/live-stream`：

| 身份 | 40s 字节 | 帧数 | node_update | initial_data |
| --- | --- | --- | --- | --- |
| `super_admin` | 19,578,651 | 33 | 9 | 1 |
| `tenant_admin`（tenant=default） | 23,965,546 | 17 | 3 | 1 |

nginx 侧 SSE location 配置正确（`proxy_http_version 1.1` + `proxy_buffering off`
+ `proxy_read_timeout 3600s`）。**服务端与代理链路都不是本次的成因。**

### 真实成因：Electron 内置浏览器环境，客户端秒断

- 浏览器那条连接在 nginx access log 里是 `200 0`（0 字节），随后 `499`；
- 同一秒后端日志：`live stream initial replay failed  err="context canceled"`
  —— **客户端在 handler 开始 replay 之前就已断开**；
- `/api/admin/live-stream/stats` 长时间 `active_clients: 0`：浏览器从未成为
  已注册客户端（我的 curl 同期正常注册）；
- 页面停滞在「连接中」，最终整个 renderer 白屏；`inspect` / `query(text)` /
  `query(console)` 反复 275s 超时。

**结论：第 2 项的浏览器人工验收在当前 Electron 内置浏览器环境下不可完成**，
这是环境故障，不是被验收功能在生产上的表现。不以局部证据冒充通过。

### 生产数据验收（数据层判据已通过，截图仍未取得）

用生产 SSE 实抓的 `node_update`（9 帧、939 个去重 `raw_models`）取 glm 族
**49 个作用域**，逐个打生产 `/api/routing/resolve`，再喂给线上逐字节相同的
`resolveModelScopeOwnership`（一次性驱动，跑完即删，不入库）：

```
aliasOwner[glm-5.3]        = glm-5.3
aliasOwner[z-ai/glm-5.3]   = glm-5.3
aliasOwner[glm-5.3-flash]  = glm-5.3-flash
aliasOwner[glm-5.2]        = glm-5.2
aliasOwner[z-ai/glm-5.2]   = glm-5.2
representatives: canonical:2422803 → glm-5.3   canonical:2716170 → glm-5.3-flash
                   canonical:173264  → glm-5.2
```

即：`glm-5.3` 与 `z-ai/glm-5.3` 归 `glm-5.3` 分组；`glm-5.3-flash` 是独立
canonical（2716170）的独立分组；`glm-5.2` 组正常。**这正是第 2 项要看的判据**，
但它是数据层等价验证，**不等于**面板渲染成功的截图。

### 疑点已判定：gate 真实存在，但**不是**本次成因（2026-10-01 10:22 补记）

`admin/live_stream_sse.go:2320` 里，连接时唯一携带 `Nodes` 的 `initial_data`
帧被整个包在 `else if len(items) > 0` 里：replay 不到任何请求条目时，
handler **一个字节都不写**，直接阻塞到客户端断开。线上 `2d750fb4` 同一位置逐行同形。

本轮用一次性取证测试把它判定掉了（两问各一份独立证据，跑完即删，不入库）：

| 问题 | 实验 | 结果 |
| --- | --- | --- |
| gate 真实存在吗？ | hub 无 DB/无 store（`replay` 返回 `nil, nil`）+ 注入有数据的 node provider，只跑 handler | 状态码 200、**响应 0 字节** ⇒ gate 成立 |
| 会不会让节点矩阵**永久**饿死？ | **反向对照**：同一份代码，只多跑一个 `hub.Run()` | 2 秒内收到 `node_update`（182 字节）⇒ **不会饿死** |

变异验证（证明第一问的门不是恒真）：把 `len(items) > 0` 改成 `len(items) >= 0`，
测试转红并打出 746 字节，其中明确含
`"nodes":[{"credential_id":7,...,"raw_models":["glm-5.3"]}]`。还原后 `go build` / `go vet` /
`go test ./admin/ -run 'TestLiveNodeStatus|TestLiveStream'` 全绿。

**结论**：该 gate 只会造成**连接建立后 ≤2s 的空窗**，随 `nodeTicker`
（`live_stream_sse.go:777/901`，2 秒一次 `fanOutNodeUpdate`）自动补齐。
生产侧独立佐证：40 秒窗口内超管连接收到 **9-10 个 `node_update`**。

⇒ **它无法解释浏览器那条 13 秒以上仍为 0 字节的连接。** 浏览器侧后端日志
`live stream initial replay failed err="context canceled"` 才是直接证据 ——
**客户端在 replay 开始前就断开了**。本条从「未证实的疑点」降级为
「已判定的非成因」，gate 本身作为健壮性小账登记（空 replay 时首个 tick 前
面板无数据），不再挂在本条主线上。

## 2026-10-01 245 蓝绿契约缺口：不是残留进程，是 nginx 只补了一半 vhost

**用户决定：本轮不动 245，仅登记缺口、另行排期。** 本节只记录实测，未执行任何变更。

### 差点造成事故的错误修法（先记下来）

最初打算「停掉弃用的 `llmgo-245.service`，按契约把 `canary@8781` 拉起来」。
**这个方案会造成下线** —— 查 nginx upstream 时才发现 8781 是在服务的。

### 实测：245 上两个 gateway 进程同时在跑，且都有真实流量

| vhost | upstream | 承载进程 | 二进制 | `build_seq` | 今日请求 |
| --- | --- | --- | --- | --- | --- |
| `llmgo.kxpms.cn` | `include run/active-upstream.conf` → **8782** | `llmgo-245-canary@8782.service`（契约内，`active`） | `releases/2374-b0925682` | 2374 | 687 |
| `download.kxpms.cn` | 硬编码 `127.0.0.1:8781` | **`llmgo-245.service`**（遗留 unit，`active`） | `releases/2372-d2af305a` | 2372 | **204**（`/download` `/healthz` `/` 等） |
| `llm.kxpms.cn` | 硬编码 `127.0.0.1:8781` | 同上 | 同上 | 2372 | 0（该域名实际落 154） |
| `acc.kxpms.cn` | 硬编码 `127.0.0.1:8781` | 同上 | 同上 | 2372 | 0 |

`llmgo-245-canary@8781.service` 仍为 `failed`（04:54:45 SIGKILL，见上节订正）。
`run/active-port` = 8782、`run/active-upstream.conf` = `server 127.0.0.1:8782`，二者自洽。

### 缺口的准确描述

蓝绿契约（`deploy-lib/targets.sh` 的 245 契约 + `deploy-seamless.sh` 的
`active unit` 推导 + `run/active-upstream.conf` 原子片段）**只接到了
`llmgo.kxpms.cn` 一个 vhost**；另外三个 vhost 仍是硬编码 `127.0.0.1:8781`。

所以它不是「部署搞乱了拓扑、留了个残留进程」，而是：

- 遗留 unit 实际上**仍在承担 `download.kxpms.cn` 的生产流量**，且跑的是
  比活跃侧**旧一个 release**（2372 vs 2374）的二进制；
- 契约的「当前只有一侧在服务」在这台机器上**从未成立**；
- 每次蓝绿切换只影响 `llmgo.kxpms.cn`，`download` / `acc` / `llm`
  三个域名的行为与 `run/active-port` **脱钩**。

### 与已修脚本缺陷的关系（两者不是同一件事）

`3d10c8b15` 修的是**脚本侧**：`active unit` 推导在 fresh 主机兜底时会回落到
弃用的 `$SERVICE_NAME`，导致部署日志出现
`⚠ active unit 无法从监听进程归属解析，按端口推导为 llmgo-245.service`。
该缺陷已修，并有变异验证。

**但脚本修好不等于现网配置补齐。** 本节记录的是 nginx 侧仍然存在的覆盖面缺口，
需把 `download` / `acc` / `llm` 三个 vhost 也改走 `active-upstream.conf`、
排空 8781 之后，该缺口才闭合。这属于改 245 生产流量路由的操作，
须单独立项、单独排期并准备回滚步骤，**不在本轮范围内**。

### 2026-10-01 10:30 补记：服务端/代理侧假设已穷举排除，只剩客户端

对「浏览器那条连接 0 字节」逐条排除，**每条都带独立实测**：

| 假设 | 实测 | 判定 |
| --- | --- | --- |
| 空 replay 导致首帧被 gate 掉 | 一次性测试：0 字节成立，但 `nodeTicker` 2 秒补 `node_update` | 真实但自愈，**非成因** |
| nginx 对 SSE 开了 gzip、响应压进缓冲区 | 154 的 `gzip_types` 不含 `text/event-stream`；带浏览器完整头（含 `Accept-Encoding: gzip, deflate, br, zstd` + `Origin`）实测**无 `Content-Encoding`**，**首字节 0.060s**，25 秒 17,094,239 字节 / 26 帧 | **证伪** |
| HTTP/2 单域名 6 条并发流被旧标签页占满 | 154 侧 `/live-stream/stats` 的 `active_clients: 0`，浏览器出口 IP 只有 4 条 ESTABLISHED TCP | **证伪** |
| 服务端对该身份不下发节点 | 超管 19.6 MB / 租户 24.0 MB，均含 `initial_data` + `node_update` | **证伪** |

**结论**：服务端、nginx 配置、HTTP/2 配额、身份/scope 四条路径全部排除后，
剩下的唯一解释是 **Electron 内置浏览器客户端自己断开了 EventSource** ——
与后端日志 `live stream initial replay failed err="context canceled"`（客户端在
replay 开始前就断了）以及 renderer 最终白屏完全一致。

**这是环境故障，不是在被验收功能的生产表现。** 第 2 项的截图验收改由用户在
普通 Chrome 中完成（判据已用生产数据验过，见上节）。

### 2026-10-01 10:29 补记：candidates 不变式的真实目录门实跑通过（此前一直是 SKIP）

`TestResolveCandidatesInvariant_Live` 此前每轮都输出
`TEST_RESOLVE_INVARIANT_DB_URL not set — SKIPPING … A skip is NOT evidence`。
本轮把 DSN 接上，**对着真实目录实跑通过**：

```
routing_resolve_invariant_test.go:247: invariant verified for 957/957 catalog models
--- PASS: TestResolveCandidatesInvariant_Live (12.26s)
```

**扫描量从 958 变成 957** —— 09-29 那次的 958/958 证据在目录规模上已经过期，
这正是「必须重跑而不是引用旧数字」的又一个例子。门是只读的
（只 SELECT `models_canonical` 并复现 resolve 查询），耗时 12 秒。

#### 顺带定案的拓扑事实：三台环境共用同一个 PG

| 环境 | 主机 | `LLM_GATEWAY_DATABASE_URL` 指向 |
| --- | --- | --- |
| 154 生产 | `47.97.111.154` | `172.16.2.210` |
| 245 预发布 | `8.136.114.245` | `172.16.2.210` |
| 252 dev | `115.29.212.252` | `172.16.2.210`（此前记录的 PG17） |

即**生产、预发布、dev 共用同一个 PG 集群**。推论：任何"在预发布验证数据库相关改动"
的说法都要重新掂量——三者本就在同一个库上。

#### 这条门怎么跑（记下来，下轮别再 SKIP）

本机到 `172.16.2.210:5432` **TCP 可达**（`nc -z` 成功），但 PG 握手直接
`unexpected EOF`，加 `sslmode=disable` 也一样 ⇒ 内网入口对来源有拦截，**不能从 Mac 直连**。

可行做法（DSN 不经过本机，密码不出内网）：

```bash
# 1) 本地交叉编译测试二进制
GOOS=linux GOARCH=amd64 go test -c -o /tmp/admin_inv.test ./admin/
# 2) 送到 245
scp -P 25022 /tmp/admin_inv.test root@8.136.114.245:/tmp/
# 3) 在 245 上用它自己的 env 跑
ssh -p 25022 root@8.136.114.245 'chmod +x /tmp/admin_inv.test && \
  TEST_RESOLVE_INVARIANT_DB_URL=$(grep -E "^LLM_GATEWAY_DATABASE_URL=" /etc/llm-gateway-go/env | head -1 | sed -E "s/^[^=]*=//; s/^\"//; s/\"$//") \
  /tmp/admin_inv.test -test.run "TestResolveCandidatesInvariant_Live" -test.v'
```

#### 另一条钉住门的复跑（当前树）

- `go build ./...` / `go vet ./admin/` 通过
- `go test ./admin/` **全包 ok（77.47s）** —— 在合并了并发会话的 R44
  全仓实跑、autoupdate testschema 护栏等改动之后的当前树上
- `TestResolveRawModelsStillLeak`：`PASS`，输出
  `[glm-5.3-flash glm-5-3-flash glm-5.3 glm-5-3]`，与生产实测逐字一致
- 生产未变：`build_seq 2356` / `git_sha 2d750fb4`

### 2026-10-01 10:35 补记：把「数据层判据」推到「面板标题」的最后一环也补上了

前面的 `aliasOwner` 验证只覆盖到**归属裁决函数**。面板最终渲染的标题要经过
`QueuePerspectivePanel.vue` 的这条链，中间还有两个硬前提，本轮一并对生产核实：

```
node.raw_models                                        (SSE node_update)
  → modelKey(raw)                                      :447
  → aliasOwner.get(rawKey)  ← 就是 modelScopeAliasIndex :448
  → modelScopeMeta.get(scopeKey)，缺失则整组丢弃        :457-458
  → groups.push({ model: scopeKey, displayName })        :497-499
  → 模板 {{ group.displayName }}                          :1296
```

#### 环节一：分组键 = aliasOwner（本轮已验）

生产真实数据下 `aliasOwner['glm-5.3'] = 'glm-5.3'`、`aliasOwner['z-ai/glm-5.3'] = 'glm-5.3'`，
故上报 `glm-5.3` / `z-ai/glm-5.3` 的节点都落进 `glm-5.3` 这一组。

#### 环节二：`modelScopeMeta` 必须含该作用域，否则 `:458` 直接 `continue`

`modelScopeMeta` 由 **featured + top-models(72h, limit=50)** 两个来源构建
（`loadModelScope` → `getFeatured()` / `getRequestLogTopModels()`）。对生产实测：

`GET /api/routing/featured` —— 共 **28** 条（与 journal `model_tier: featured set refreshed static:28` 吻合），
含 glm 的为 **`["glm-5.3","glm-5.3-flash"]`** ⇒ **两者都在**。

`GET /api/logs/top-models?from=-72h&limit=50` —— 共 50 条，glm 相关：

| canonical_name | canonical_id | request_count |
| --- | --- | --- |
| `glm-5.3` | 2422803 | 224 |
| `z-ai/glm-5.3-flash` | *(null)* | 72 |
| `glm-5.3-flash` | 2716170 | 59 |
| `glm-5.2` | 173264 | 13 |

#### 结论（每个前提都对着生产核过）

- 筛 `glm-5.3` ⇒ 存在作用域 `glm-5.3`（featured + hot 都有）⇒ 归属裁决把它判给自己
  ⇒ 渲染出标题为 **`glm-5.3`** 的分组，**不会**被 `glm-5.3-flash` 冒名（后者是
  独立 canonical 2716170，代表作用域也是它自己，合并后仍是两个组）。
- `glm-5.2` **不在 featured**，但**在 hot**（canonical 173264，13 次）⇒ 同样会出组，
  标题 **`glm-5.2`**，正常。
- 附带确认 `z-ai/glm-5.3-flash`（canonical_id 为 null、72 次，是 glm-5.3-flash 的 1.2 倍）
  会按名字归入 `canonical:2716170` 身份，合并到代表作用域 `glm-5.3-flash`，
  不会多出一个重名组。

**仍未取得的只有「浏览器里真的渲染出这两个标题」这一条目视证据**，
仍需用户在普通 Chrome 确认。本节把除目视之外的全部前提都补成了生产实测。

## 2026-10-01 10:45 新发现：model_aliases 是不完整索引，其重建服务从未接线

在核实热门榜数据时撞到一处矛盾，顺藤摸瓜挖出一条**独立于本轮主线的真实归属缺口**。

### 矛盾起点

72h 热门榜里 `z-ai/glm-5.3-flash` 的 `canonical_id` 是 **null**、请求 72 次，
但 `resolve?model=z-ai/glm-5.3-flash` 明确返回 `canonical_id = 2716170`。
同一模型，两处口径不一致。

### 机制

`admin/logs.go:1243-1259`（`listTopModels`）的兜底链只有两级：

```sql
COALESCE(mc.id, mc2.id)                                   -- ① request_logs_hot.canonical_id
LEFT JOIN models_canonical mc  ON mc.id = rl.canonical_id
LEFT JOIN LATERAL (SELECT canonical_id FROM model_aliases
                   WHERE raw_name = lower(rl.client_model) AND status='active') ma ON TRUE
LEFT JOIN models_canonical mc2 ON mc2.id = ma.canonical_id  -- ② 只查 model_aliases
-- 都落空 → canonical_id 退化为 NULL，canonical_name 退化为 rl.client_model 原串
```

而 `resolve` 在进入 `model_aliases` 阶段**之前**还有一个精确形阶段
（`resolveInputCanonicalID`，按 `provider_models.raw_model_name` 精确优先），
`z-ai/glm-5.3-flash` 正是被它命中的。**`top-models` 没有这一层兜底。**

### 库里核实（只读，245 侧执行）

```
model_aliases:  raw_name = 'z-ai/glm-5.3-flash'  →  0 行
provider_models: raw_model_name = 'z-ai/glm-5.3-flash' → canonical_id 2716170

models_canonical  = 957      provider_models = 1329
model_aliases     = 2684 行（status='active' 2100）
provider_models 中带 canonical 的不同 raw 名 = 919
  其中在 model_aliases(active) 里找不到的   = 543   （59%）
```

⇒ **`model_aliases` 只覆盖了 `provider_models` 919 个原始名的 41%。**

### 影响面（已量化）

72h 热门榜 50 条、覆盖 4194 次请求：**18 条（36%）`canonical_id` 为 null，
合计 520 次 = 全部热流量的 12.4%**，全部退化成原始 `client_model` 字符串
（`meta/muse-glimmer-30b` 76、`z-ai/glm-5.3-flash` 72、
`google/diffusiongemma-...` 58、`nvidia/*` 若干、`minimaxai/minimax-m3` 20 …）。

除 `top-models` 外，只依赖 `model_aliases` 的消费方同样受影响
（`provider/client.go` 多处、`internal/reasoncap/pgsource.go:66`、
`admin/probe_history.go:527`、`admin/models.go`）。

### 根因：重建服务是死代码

`discovery/alias_sync.go` 的 `AliasSyncService` 负责清理孤儿别名 + 重建别名索引，
但：

```
grep -rn "AliasSyncService" --include=*.go .
→ 只有 discovery/alias_sync.go（定义本身）
  与 discovery/alias_sync_live_test.go:91（测试里 &AliasSyncService{db: txDB{...}}）
NewAliasSyncService 在非测试代码中零调用；无 .RunOnce() 调用点。
生产 154 自 2026-09-30 21:47:20 启动至今，journal 里 "alias sync" 命中 0 次。
```

⇒ **该服务从未被接线**。`model_aliases` 目前只靠 `discovery/discovery.go`
的增量 upsert 维持，所以相对 `provider_models` 持续漂移。

### 对本条主线的影响（说清边界，不要夸大）

**模型分组面板不受影响**：`resolve` 的精确形阶段 + `resolveModelScopeOwnership`
的按 canonical 合并，已经把 `z-ai/glm-5.3-flash` 这类别名作用域正确并入
`glm-5.3-flash`（前一轮实测 `representatives: canonical:2716170 → glm-5.3-flash` 印证）。
**本条不改变第 2 项的结论。**

受影响的恰恰是那些**只查 `model_aliases`、又没有前置精确形阶段**的路径，
`top-models` 是可量化的那个。

### 修法方向（未执行，需先决策）

两个方向，影响面差别很大：
1. **补数据**：把 543 个 `provider_models` raw 名回填进 `model_aliases`，
   纯数据变更，影响所有消费方；
2. **修查询**：给 `listTopModels` 之类只有 `model_aliases` 兜底的路径
   补上 `provider_models` 精确形兜底，与 `resolve` 对齐。

无论哪条都应先在 245 验证，且**写库动作在生产与预发布共用的同一个 PG 上**
（见上文拓扑一节），必须先排期、不能顺手做。本轮**只登记，不动数据**。
