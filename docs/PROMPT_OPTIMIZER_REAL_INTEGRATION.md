# 提示词优化 —— 真实 Memora / 内部小模型 / 网关 Hook 接线

> 本文记录的是**实测过的**接线契约，不是设计设想。
> 契约来源：`ai-platform/memora` 源码 + 真实实例实测（2026-11-12）。
> 效果实验的结论见 [`PROMPT_OPTIMIZER_REAL_EXPERIMENT.md`](PROMPT_OPTIMIZER_REAL_EXPERIMENT.md)。

---

## 一、真实 Memora 接线

### 1.1 为什么不能用网关既有的 Memora 客户端

仓库里已有 `domains/memory/client`，但它走的是 **legacy 路由**（`/api/add`、`/api/search`），
而 legacy 路由挂在 `V1Auth` 下，明确拒绝 service token：

```
POST /api/v2/memories  →  403
{"code":"service_token_wrong_route","error":"service identity not permitted on legacy user routes"}
```

提示词优化服务是**后台服务**，没有用户会话 ⇒ 只能用 service JWT ⇒ 必须走 memora 的
**v2 fail-closed 组**。因此新增了 `internal/prompt-optimizer/memora` 包。

### 1.2 端点与鉴权（实测）

| 用途 | 端点 | 鉴权 | 必需头 |
|---|---|---|---|
| 建 project | `POST /api/projects` | **user JWT**（legacy） | — |
| 写记忆 | `POST /api/v2/memories/ingest` | service JWT | `X-Project-ID`、`Idempotency-Key`、`X-Correlation-ID` |
| 检索记忆 | `POST /api/v2/memories/search` | service JWT | `X-Project-ID` |
| 会话分析投递 | `POST /api/v2/session-analysis` | service JWT | `X-Project-ID`、`Idempotency-Key`、��X-Correlation-ID` |

> **踩坑记录**：这三个头**缺一个就是 422**，且错误信息不指向原因（先报
> `missing_header`，补齐后才报 `Idempotency-Key header must be <dispatch_id>-<analysis_kind>`）。
> `session-analysis` 的幂等键还有**后缀格式要求**：必须是 `<dispatch_id>-<analysis_kind>`。
> 另一个坑：`session-analysis` 的 `tags` 是结构体数组（`{key,value,source,confidence}`），
> 传字符串数组会得到 `invalid_json`。

### 1.3 service JWT 的形状

```
header = {"alg":"HS256","typ":"JWT"}
payload = {
  "iss": "memora",                  // 必须等于 MEMORA_V2_JWT_ISSUER
  "aud": "memora-api",              // 必须等于 MEMORA_V2_JWT_AUDIENCE
  "sub":  "service:local-auth:<actor_id>",
  "tenant_id": "default",
  "actor_id": "<actor_id>",         // ★ 授权主体：project ACL 判的是它，不是 sub
  "actor_type": "service",
  "scope": "memory.read memory.write knowledge.read knowledge.write",
  "iat": ..., "exp": ...
}
```

与 memora 源码 `internal/localauth/mint.go` 的 `MintServiceJWT` 同构。

### 1.4 project scope 与 ACL

memora 的 project scope 是 **fail-closed** 的：project 不存在 ⇒ 403
`{"code":"project_forbidden","reason":"project_not_found"}`。

授权判定 SQL（`internal/auth/project_acl_query.go` 的 `projectAuthorizationQuery`）：

```sql
WHEN p.owner = $3 THEN 'owner'
WHEN EXISTS(SELECT 1 FROM project_acl a
            WHERE a.tenant_id=$2 AND a.project_id=$1 AND a.principal_id=$3 ...) THEN 'acl'
```

⇒ 最小可行配置是：**`projects.owner` 等于 JWT 的 `actor_id`**。
`memora_principal_eligible()` 对"没有目录绑定"的主体直接返回 TRUE，
所以不需要额外建用户。

实测（本机 memora 实例）：

```
POST /api/projects          → 200  {"project":{"project_id":"prompt-optimizer","owner":"local-ops",...}}
POST /api/v2/memories/search → 200  {"data":{"items":[...]}}
POST /api/v2/memories/search（换一个不存在的 project）→ 403 project_not_found
```

### 1.5 本仓的接入点

`internal/prompt-optimizer/memora/client.go`：

- `Client.Search` / `SmartSearch` **实现 `domains/memory.Reader`** ⇒ 可直接喂给
  `context.Manager`，历史检索走真实 memora。
- `Client.Ingest` ⇒ 写记忆。
- `Client.PostSessionAnalysis` ⇒ 投递会话分析（memora 会产出 fact / experience / context manifest）。
- JWT 签发用标准库 `crypto/hmac` + `crypto/sha256`，**不引入新依赖**（本仓 vendor 模式）。

服务侧的环境变量：

```
PROMPT_OPTIMIZER_MEMORA_BASE_URL     # 默认 http://127.0.0.1:12113
PROMPT_OPTIMIZER_MEMORA_JWT_SECRET   # HS256 密钥（也可用 MEMORA_JWT_SECRET）
PROMPT_OPTIMIZER_MEMORA_PROJECT_ID   # 默认 prompt-optimizer
PROMPT_OPTIMIZER_MEMORA_ACTOR_ID     # 默认 local-ops
PROMPT_OPTIMIZER_MEMORA_TENANT_ID    # 默认 default
```

缺任一项 ⇒ 服务以"无历史"模式运行并记录原因，**不会**因为记忆后端没接上就起不来。

### 1.6 历史闭环

优化服务每次完成优化后，会把「原提示词 / 优化后提示词 / 用了哪些策略」
best-effort 写回 memora（`service.recordOptimization`）。下一轮
`context.Manager.GetRelevantHistory` 就能把它检索回来 ⇒
**优化效果随使用次数累积**，而不是每次从零开始。

真实链路验证见 `scripts/test-prompt-optimizer-real.sh` 的第 7 条断言
（写入后用 memora 检索，能检索回带本次标记的记录）。

---

## 二、内部小模型接入

### 2.1 通道

走 **OpenAI Chat Completions 协议**，不绑定任何厂商 SDK：

```
POST {base}/v1/chat/completions
{"model":"...","messages":[{"role":"user","content":"..."}],"temperature":0,"max_tokens":1200}
```

因此"内部小模型"可以是：

- 本地 ollama（`http://127.0.0.1:11434/v1`）—— 本轮实验用的就是这个；
- 本仓网关自身（网关是 OpenAI 兼容的，`LLM_BASE_URL` 指向网关即可路由到内部模型）；
- 任何 OpenAI 兼容的内部推理服务。

### 2.2 两个角色

| 角色 | 配置 | 用途 |
|---|---|---|
| 优化模型 | `OPTIMIZER_BASE_URL` / `OPTIMIZER_MODEL` | 改写提示词（`llm_rewrite` 策略） |
| 执行模型 | `EXECUTOR_BASE_URL` / `EXECUTOR_MODEL` | 被测任务的执行体 |

### 2.3 LLM 重写策略（`internal/prompt-optimizer/strategies/llm_rewrite.go`）

把「原始提示词 + 任务类型 + 领域 + 领域约束 + **memora 历史片段** + 输出风格参考」
打包成一条 meta-prompt，交给模型改写。

三条契约：

1. 改写结果必须是**任务提示词本身**，不是对提示词的描述、也不是对任务的回答；
2. 改写失败（模型不可用 / 输出为空 / 输出像元描述）⇒ **回退到原提示词**，
   绝不返回一段不可用的文本；
3. 温度固定 0 —— 实验要求"提示词是唯一自变量"，优化器本身不能引入随机性。

元描述检测（输出以"你是一个…"/"你的任务是…"开头 ⇒ 判为改写失败）是必要的：
小模型很容易把 meta-prompt 的要求当成改写结果吐回来。

---

## 三、网关 Hook 契约端点

### 3.1 原先的断点

网关内置 Hook 在 `domains/hooks/promptoptimization/`，包与测试都完整，
但**文档自标"已实现、未接线"**，且它调用的契约是：

```
POST {PROMPT_OPTIMIZER_URL}/api/v1/prompts/optimize
```

而本服务原先只暴露 `POST /api/v1/optimize`（单条 prompt、字段完全不同）
⇒ **Hook 一旦接线就会一直 404 回退**，"实现了但永远不生效"。

### 3.2 已补齐的契约

`cmd/prompt-optimizer/hookapi.go` 新增 `POST /api/v1/prompts/optimize`：

```jsonc
// 请求
{"tenant_id":"t1","model":"gpt-4o",
 "prompts":[{"role":"system","content":"..."}],
 "options":{"use_memora":true,"inject_harness":true}}

// 响应
{"optimization_id":"opt_ab12cd34","cache_hit":false,
 "original_tokens":1200,"optimized_tokens":950,"latency_ms":320,
 "prompts":[{"role":"system","content":"优化后","changed":true}]}
```

对齐点（对着 `domains/hooks/promptoptimization/optimizer.go` 核过）：

- 返回的 `prompts` **数量必须与请求一致** —— Hook 侧会判 `prompt count mismatch` 并回退；
- Hook **只写回 `changed=true`** 的 content，所以 `changed` 必须真实反映内容是否变化；
- 非 2xx ⇒ Hook 走 fallback（原始 prompt 原样通过），因此错误必须如实返回状态码；
- `options` 里可传 `use_memora` / `inject_harness`，缺省都是 `true`。

附带能力：

- **SHA256 结果缓存**（`PROMPT_OPTIMIZER_CACHE_TTL_SECONDS`，默认 3600）⇒ 第二次相同请求 `cache_hit=true`；
- `original_tokens` / `optimized_tokens` 是**估算**（CJK 1 字 = 1 token，其余 4 字符 = 1 token）——
  本仓不引入分词器依赖，只用于网关审计元数据，**不参与任何质量判定**。

---

## 四、本轮修掉的一个真缺陷：占位工具不得注入生产提示词

`harness.MockToolRegistry` 里是 `search` / `calculate` 两个**占位工具**。
把它写进真实网关请求的 system prompt，等于告诉模型"你可以使用以下工具"，
而上游根本不存在这些工具 ⇒ 模型会编造工具调用或虚构工具结果。

处置：新增 `harness.ProductionInjectorConfig()`（**只注入安全约束与错误处理，不注入工具章节**），
服务与实验都改用它。`MockToolRegistry` 保留给单元测试，并加了显式警示注释。

实验证据：注入占位工具的那一版配置里，`rule_harness` 臂的
`json_array_3` 任务从"通过"变成"不通过"（输出被工具章节带偏，JSON 数组被前言污染）。

---

## 五、复现

```bash
cd ~/workspace/ai-native-tools/llm-gateway/llm-gateway-go

# 单元测试（9 个包）
go test ./internal/prompt-optimizer/... ./cmd/prompt-optimizer/...

# 真实链路 E2E（需要 memora 实例 + ollama）
./scripts/test-prompt-optimizer-real.sh

# 效果实验（4 臂 × 6 任务 × N 次重复）
export MEMORA_JWT_SECRET="$(docker inspect memora-server --format \
  '{{range .Config.Env}}{{println .}}{{end}}' | grep '^JWT_SECRET=' | cut -d= -f2-)"
go run ./cmd/prompt-optimizer-experiment --repetitions 5 --write-back \
  --out docs/audit/prompt-optimizer-experiment-run2
```
