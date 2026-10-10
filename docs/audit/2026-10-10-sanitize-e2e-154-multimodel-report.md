# 154 网关 · 敏感词脱敏与还原 多场景多模型验证报告

> **日期**：2026-10-10
> **目标网关**：`47.97.111.154:8782`（v2，git `ac6f2276`，build_seq 2514）
> **测试台**：`scripts/sanitize-e2e-154.sh`（可复跑）
> **模型矩阵**：minimax-m3 / gpt-6-luna / glm-5.3 / deepseek-v4-pro / mimo-v2.5-pro
>
> **另见**：[第九节 · minimax-m3「首字单行」取证](#九minimax-m3首字单行取证2026-10-10)——
> 用户报「minimax-m3 第一个请求回复单字一行」，本节末尾的取证结论是
> **网关侧未复现**，详见该节。

---

## 一、结论

**脱敏（出向不含明文、不误伤）这一半是正常的**：入向替换为占位符、出向无明文残留、流式与跨协议均无占位符泄漏、无敏感信息不被误伤。

**还原这一半无法在本轮判定为「正常」**：E2E 中各模型的 A3/C3 全部为「未还原出原文」，
但根因不是映射缺失——探针实测 Redis 映射存在且值为明文、TTL 正常（见第九之四）。
真实情况是模型没有回显占位符（上游收到的是部分打码值），没有可供还原的目标。
同时另有 96 行确实发生了完整占位符透传（第九之二节），成因尚未完全定位。

**但发现五个真实缺陷，均已修复**：

1. **密钥漏检**：带中缀连字符的 API 密钥（`sk-proj-` / `sk-ant-` / `sk-or-v1-`）此前**完全不被脱敏**，明文直发上游并落库。这是审计 244 号 F2 的遗留项。
2. **流式内部 token 泄漏**：模型把占位符改写成 `(SENSITIVE:` 并跨 SSE chunk 下发时，缓冲逻辑认不出 `(` 开头，**半截内部 token 泄漏给客户端**（约 1/3 概率复现）。
3. **裸占位符泄漏**：模型剥掉花括号写成 `SENSITIVE:phone` 时，span pattern 要求花括号故完全匹配不到；近 60 分钟生产 **26 条**。
4. **打码判据过窄**（修复 2 引出）：垃圾续包补全不了已缓冲的 marker 时，`restoreText` 返回半遮蔽文本，判据 `restored == combined` 不触发，半遮蔽 token 加续包泄漏。这一处只有把**整个 sanitize 包连依赖闭包**传到 154 用真实 Go 编译跑完整套件才暴露出来。
5. **裸形态嵌在 JSON 里仍漏**（修复 3 的边界）：span pattern 的花括号分支贪婪捕获整个 JSON，解析失败即跳过，内部的裸形态没被扫到。改为片段内重扫 + 深度上限。

另有一项**运维配置缺口**需用户决策（见第五节），不属于代码缺陷。

---

## 二、主链路验证结果

| 场景 | 断言 | 结果 |
|---|---|---|
| A1 | 单轮响应无占位符残留 | 5/5 通过 |
| A2 | 二轮响应无占位符残留 | 5/5 通过 |
| A3 | 二轮跨轮还原出原文手机号 | minimax-m3 / glm-5.3 / deepseek-v4-pro / mimo-v2.5-pro 通过；gpt-6-luna 模型侧拒绝复述 |
| B1 | 混合类型（邮箱/身份证/银行卡/密钥）无占位符残留 | 5/5 通过 |
| B2 | 模型响应未复现敏感原文 | 5/5 通过（修复后） |
| C1 | 流式响应无占位符残留 | 5/5 通过 |
| C2 | 流式响应可解析出内容 | 5/5 通过 |
| D1 | 无敏感信息正常应答 | 5/5 通过 |
| D2 | 无敏感信息未被误脱敏 | 5/5 通过 |

A3 在 gpt-6-luna 上未还原，是模型自身有防护策略、不肯复述 PII（响应里明确说"这明显是想获取敏感信息"），非网关缺陷——同会话的出向正文已确认含占位符。

### 落库与留痕取证

| 检查 | 结果 |
|---|---|
| `request_logs_bodies_hot.outbound_body` 含手机号/邮箱/身份证/银行卡明文 | **0 条** |
| `request_logs_bodies_hot.request_body` 含真值 | 0 条 |
| `request_logs_hot.request_preview` 含真值 | 0 条 |
| `session_bodies_hot.request_delta` / `outbound_body` 含真值 | 0 条 |
| 网关日志（含压缩历史）含真值 | 0 条 |
| `session_censors_hot` 占位符映射已建立 | 是（12371 行 / 90 分钟） |

出向正文实际长这样（原文已被替换）：

```json
{"model": "gpt-6-luna", "messages": [{"role": "user",
  "content": "登记信息：邮箱 {SENSITIVE:email:1}，身份证 {SENSITIVE:id_card:1}，
   银行卡 {SENSITIVE:credit_card:1}…"}]}
```

---

## 三、发现的缺陷：`sk-` 带中缀连字符的密钥系统性漏检

### 现象

用真实形态的 key 实测（`session_censors_hot` 有无映射行为权威判据）：

| 探针 | 出向占位符 | censor 映射行 | 判定 |
|---|---|---|---|
| `sk-` + 48 位纯字母数字 | ✅ | 1 | 已脱敏 |
| kaixuan 真实 key（`sk-1vH6…`） | ✅ | 1 | 已脱敏 |
| `bearer` + 40 位 | ✅ | 1 | 已脱敏 |
| **`sk-proj-Ab3dEf…`** | ❌ | **0** | **明文泄漏** |
| **`sk-probe-ABCDEFGH…`** | ❌ | **0** | **明文泄漏** |

### 根因

审计 244 号 F2 已定性：内置规则 `(sk|ak)-[a-zA-Z0-9]{16,}` 的字符类**不含 `-`**，于是键体一旦带连字符就在第一个连字符处截断。`sk-proj-` / `sk-ant-api03-` / `sk-or-v1-` 正是 OpenAI / Anthropic / OpenRouter 的实际格式，全部漏检。

生产实际加载的是 `configs/sensitive_patterns.yaml` 的 `secret.api_key`，同样有这个缺陷。该项在当前 HEAD 仍未修。

### 修复

改动三处：

1. **`security/sanitize/detector.go`** — 内置 fallback 规则改为形状匹配（允许中缀连字符），并新增 `credibleSecretCandidate` 做整串后置过滤。
2. **`security/sanitize/detector_config.go`** — 按**正则文本**（非规则名）识别 vendor key 形状，使 YAML 加载的生产检测器与内置 fallback 行为一致。
3. **`configs/sensitive_patterns.yaml`** — `secret.api_key` 同步为同一形状。

**关键设计约束**：16 位长度下限与"键体含数字"这两条都是**整串属性**，RE2 无法用前瞻断言表达（Go 的 `regexp` 直接报 `invalid or unsupported Perl syntax` 并 panic）。因此正则只负责形状，两条约束放在 Go 侧：

- 长度 ≥ 16 —— 挡掉 `sk-test` / `ak-short` / `sk-or-v1`
- 至少含一个数字 —— 挡掉 `bearer-of-responsibility` / `token-bucket-algorithm` / `api-design-patterns` 这类纯字母散文

前缀集合取两侧并集（内置原有 `sk|ak`，YAML 原有 `sk|key|token|api|bearer`），收窄任一侧都会静默丢掉另一侧已覆盖的 key。

### 验证

在 154 上用 Go 1.27.1 实跑（非本机——本机无 Go 工具链）：

- `go vet` 干净，`gofmt` 干净
- 新增 4 个回归测试全通过（含**全跨度匹配**断言：前缀命中但尾巴残留同样算泄漏）
- **原有 detector 测试无回归**——过程中确实撞到过一次我引入的回归（`KEY-ABCD` 被误过滤），已通过"按正则文本而非规则名打标记"修掉
- 生产那份 YAML 在 154 上实测 12/12 通过

---

## 四、第二个缺陷（更严重）：流式响应把占位符泄漏给客户端

### 现象

在流式场景重复请求时抓到（minimax-m3，约 1/3 概率复现）：

```
[idx0.content] ' (SENSITIVE:'
```

客户端收到了**半截内部 token**。后续续包 `phone:1}` 到达时已无缓冲可接，占位符就这样漏了出去。

### 根因

模型把 `{SENSITIVE:phone:1}` 改写成 `(SENSITIVE:` 并**跨 SSE chunk** 下发。`incompletePlaceholderStart` 只从 `{` 起算，认不出 `(` 开头的残片，于是把它当普通文本直接放行。

逐帧证据：

```
trial 0: clean
trial 1: clean
trial 2: 1 leaking frames
  [idx0.content] ' (SENSITIVE:'
```

### 影响

这是**内部 token 泄漏**——比明文敏感值泄漏性质不同：占位符本身不是用户数据，但它是脱敏体系的内部标识，一旦外泄说明 fail-closed 边界在流式路径上有缺口。

### 修复

`security/sanitize/smart_sani_guard.go`：

- 新增 `parenMarkerStart`，在 `{` 分支之后补一条 `(` 分支
- `(` 之后必须紧跟 marker 词（`SENSITIVE`）或其仍在到达的前缀，否则不缓冲——这是为了不误伤 `hello (world`、`a(b)c`、`see (1) below` 这类普通散文
- `(` 后面什么都没有时不缓冲：`strings.HasPrefix` 对空串返回 true，不显式排除会把 `普通文本 (` 误判成 marker（这个 bug 是新增回归测试当场抓出来的）
- `{` 仍是首选，canonical 拼写的优先级不变

回归测试 `security/sanitize/r244_stream_marker_split_test.go` 覆盖 14 个用例，含两条必须同时成立的边界：改写形态要缓冲、普通圆括号不缓冲。

## 四之二、第四处缺陷：修复引出的打码判据缺陷（完整包测试才发现）

前两处修复都在「隔离编译的复制品」上验证过。直到把整个 `security/sanitize` 包连同依赖闭包传到 154、用真实 Go 1.27.1 编译跑完整套件，才暴露出下面这个交互缺陷 —— 也正是我一直缺的那层验证。

首帧缓冲 `{SENSITIVE:phone:`，续包下发 `13800138000x tail`（垃圾，无法补全 marker）：

- `restoreText` 把 marker 词打码，返回 `{[REDACTED]13800138000x tail`
- 既有打码判据是 `restored == combined`（仅当还原阶段**完全没动**文本时才成立）
- 这里文本变了 → 判据不触发 → **半遮蔽 token 加续包原样到达客户端**

修复：判据改为「还原阶段是否**发生了打码**」（`strings.Contains(restored, redactedMarker)`）。用「是否打码」而非「文本是否变化」是关键 —— 后者同样会在每次正常的跨 chunk 还原上触发，那会把正常还原全打码掉（第一版就是这么写的，8 个流式测试立刻红）。

新增回归测试 `TestStreamRedactsGarbageContinuationAfterReservedMarker`。

## 四之三、第五处缺陷：裸形态嵌在 JSON 里时仍会漏（工具参数 lane）

修完上面那条后，把新规则拿去跑工具参数 JSON，发现 `{"phone":"SENSITIVE:phone"}` 里的裸形态**仍然漏**。

根因是 span pattern 的花括号分支贪婪：整个 JSON 对象被当成一个 span 捕获，`parseLoosePlaceholder` 解析失败后原代码直接 `continue`，内部的裸形态再没机会被单独扫描。

修法：`restoreLooseMarkers` 在 span 解析失败时改为**在片段内部重扫**，并把结果拼回（保住周围 JSON 结构）。两个配套约束：

- 重扫必须用**只含裸形态分支**的 pattern。直接复用完整 pattern 会再次匹配正在消费的那一段，永远不收敛 —— 第一版就是这么写的，内层返回原值、修复静默失效，是诊断打印才看出来的。
- 重扫加 `maxLooseRescanDepth` 上限。每层文本严格变短，终止性是结构性的，但上限保证请求路径上不会出现深递归。

诊断对照（修复后）：

```
outer spans=[[0 27]]
  cand="{"phone":"SENSITIVE:phone"}"  parsed=false
  inner.text="{"phone":"[REDACTED]"}"  changed=true
```

### 误伤审计：用生产真实流量验证（对应第五处修复）

新规则最大的风险是误伤散文，所以没有只靠小语料。从 154 生产库抽样后用改后的代码实跑：

| 语料 | 条数 | 解析出的 marker span | 误伤 |
|---|---|---|---|
| `response_preview` | 1920 | 22（phone/email/id_card/secret/internal_ip/account） | **0** |
| `request_preview` | 400 | 2（server_ip） | **0** |

高危形态逐一确认不误伤：`grep -n 'sensitive: 12345' file.txt`、`a.sensitive-phone`、`the sensitive data was returned`、`{"path":"/tmp/a.txt","count":3}`。

审计固化为两个回归测试：`r244_bare_marker_false_positive_test.go`（人工构造的高危形态）与 `r244_bare_marker_corpus_audit_test.go`（语料审计，样本缺失时 skip，不拖 CI）。

---

## 五、第三个缺陷：模型剥掉花括号，裸占位符泄漏（非流式）

### 现象

协议矩阵在 `chat`（非流式）上抓到 `client_placeholder_leak=True`。落库响应体里的原文：

> 用户使用了花括号格式的标签来标注敏感信息（**SENSITIVE:phone, SENSITIVE:email, SENSITIVE:id_card**）

模型不仅剥掉了花括号，还把类型成串列举、**不带索引**。

### 规模

不是偶发：近 60 分钟生产落库响应体中，裸形态 **26 条**（同窗口带花括号形态 45 条）。

### 根因

`loosePlaceholderSpanPattern` 的匹配分支强制要求花括号：

```go
`[\{｛][^{}｛｝]{0,96}[\}｝]`
```

`restoreLooseMarkers` 只处理 span pattern 命中到的片段，裸形态一个都匹配不到，于是原样放行。

### 修复（经用户裁决，见下）

`security/sanitize/placeholder_loose.go` 两处：

1. span pattern 增加裸形态分支 `\b(?i:sensitive):+[a-z_][a-z0-9_-]{0,31}:*[0-9]{0,9}\b`
2. grammar 的索引改为可选 `([0-9]*)` —— 缺失时 `Atoi("")` 落到既有的 `index = -1` 分支，语义是「是 marker 语法，但永远解析不到映射行」，mask 模式因此打码而非放行

散文不被误伤是靠「冒号 + 类型标识符」这个形状约束：`sensitive data`、`sensitive:12345`、`the sensitive field` 都不匹配。

### 踩到的两个坑

**坑一：可选索引会让类型吞掉数字。** 索引变成 `[0-9]*` 后空组总能匹配，类型组不再需要回溯，于是 `{SENSITIVEsecret1}`（既有契约要求拆成 type=secret index=1）变成了 type=`secret1` index=-1 —— 一个**本可还原的 marker 被静默降级成不可还原**。修法是让类型组必须以非数字结尾：`([a-z_](?:[a-z0-9_-]*[a-z_-])?)`。这个回归是抽既有测试跑出来的，不是想出来的。

**坑二：与既有契约冲突。** `placeholder_loose_test.go:60` 原本断言 `{SENSITIVE:secret}`「不是 marker」，理由是「无法解析成真实 token，重写会凭空捏造值」。实测这条推理掩盖了泄漏：26 条/60 分钟。

**用户裁决（2026-10-10）**：选择修复 —— 无索引形态按 `[REDACTED]` 打码、有索引照常还原。既堵住泄漏，又不违背「不凭空捏造值」：无索引形态永远解析不到映射行，只会被打码，值仍然丢失。已按此改写该断言并写明理由。

---

## 六、三个协议还原路径

网关的响应侧还原有**三份彼此独立的实现**，只测 `chat/completions` 会漏掉后两份：

| 协议 | 端点 | 实现 | 结果 |
|---|---|---|---|
| 传统 chat | `/v1/chat/completions` | `smart_sani_guard.go` choices-only restorer | 通过 |
| Anthropic Messages | `/v1/messages` | `restoreNativeMessagesBody`（`type=message`） | 通过 |
| OpenAI Responses | `/v1/responses` | `restoreNativeResponsesBody`（`object=response`） | 通过 |
| 工具入参脱敏 | `/v1/chat/completions` + tools | `sanitizeToolCalls` | 通过 |
| 工具调用响应 | 同上 | `unsafeToolValue` | 通过（`tool_calls=True`，分支确认走到） |

工具这条路径拆成两段测，因为**单段测不到**：让模型拿含 PII 的内容去调工具时，minimax-m3 一见脱敏标记就拒绝发起调用（三次尝试全部 `finish_reason=stop`）——那是模型的防护策略，不是网关缺陷。所以「入参脱敏」用带 PII 的 schema 验出向，「调用响应」另用一个不含敏感值的请求确保 `tool_calls` 分支真的被执行。

期间还观察到一次 `403 output_policy_blocked`：模型在 `tool_calls` 里生成了敏感值，输出合规层拦下不让它到客户端。这是 fail-closed 的**正确行为**，判据已按此修正（阻断时「没有 tool_calls」是期望结果）。

测试台：`scripts/sanitize-protocol-matrix.py`。

**取证时的两个陷阱**（都曾导致假阴性，值得记下来）：

1. **必须查 `_hot` 表**。`request_logs` 是分区父表，热数据只落在 `request_logs_hot`；查父表会得到"刚发的请求不存在"。
2. **并非每个请求都落 `outbound_body`**。实测近 30 分钟 502 个请求里 144 个（≈29%）按 body 保留策略不存出向正文。这是"没存"不是"没脱敏"，判据必须区分，否则会把保留策略误读成脱敏失效。测试台现在遇到这种情况会降级为客户端侧断言并显式标注取证缺口。

---

## 七、两个测试方法论上的坑（供参考）

1. **占位符要有真实映射**。构造 `{SENSITIVE:phone:1}` 让模型回显时，若会话里没有真值，映射表为空，网关按 fail-closed 打 `[REDACTED]`——那是正确行为，不是缺陷。正确做法是"先给真值、再让模型复述"。
2. **推理型模型正文在 `delta.reasoning_content`**，不在 `delta.content`；SSE 行还有 `data: ` 前缀和 `: keep-alive` 注释行。两者都要处理，否则会把正常流式误判为空响应。

---

## 八、需用户决策：censor 密钥未配置（已知状态，非本轮新发现）

`session_censors_hot` 近 90 分钟 12417 行，`original_encrypted` **全部为 NULL**。

根因：154 的 `/etc/llm-gateway-go/env` 未配置 `LLM_GATEWAY_SESSION_CENSOR_KEY`，`censorAEADKey()` 返回 nil，走的是代码注释里明确写出的降级模式（`original_encrypted=NULL`）。

**这不是本轮新问题**——审计 41-R71 与 2026-09-30 复审都已记录同一现象（`count(original_encrypted) = 0`，且全仓无解密读取方）。本轮只是再次实测确认现状未变。

影响（**本节于部署后订正两次**）：

- **不影响脱敏**——出向确实不含明文
- **不影响**同会话内的还原：Redis 映射是还原的唯一来源，实测存在且为明文
- **影响的是 DB 侧的审计可追溯性**：`original_encrypted` 全 NULL，
  占位符→原文的映射没有加密留痕（DB 仅为脱敏元数据审计，不是可逆来源）
- 因此 `session_censors` 不能按「DB 为权威」判可逆——这是 41-R71 已写明的验收口径

原文「还原走 Redis 热缓存，实测正常」与「不影响脱敏与还原」两句均已作废：
前半句把一个未验证的推断当成了事实，后半句忽略了 DB 侧无密文这一事实。

配置位置见 `envs.samples/02-gateway-features.env.sample:335`。密钥生成与分发属用户侧决策，本轮未擅自配置。

---

## 九、部署：2514-ac6f2276 → 2515-7dd0b4b67（2026-10-10 11:13）

五处修复已构建并发布到 154 生产。经用户显式确认后执行蓝绿切换。

### 发布物

- release：`/opt/llm-gateway-go/releases/2515-7dd0b4b67`
- 二进制：`CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags="-s -w"`
  （与 `deploy-seamless.sh:906` 的生产构建参数一致）
- 版本：`2.5.8-7dd0b4b67-20261010-2515`，`ready=true`
- 源码提交：`7dd0b4b67`（**本地提交，未推送 origin**）
- 回滚点：`releases/2514-ac6f2276` 原样保留

### 发布范围是刻意收窄的

切换前发现 `origin/main` 已领先本地 6 个提交，且**含真实 Go 改动**
（`admin/auth.go` 登出 cookie、`bg/project_backfill_worker.go` 性能修复、迁移 849/850）。
这些不在本次授权范围，**未纳入**。

经 diff 确认 `ac6f2276..1f1433d62` 之间 **零 `.go` 变更**（只有 web-mobile 与 docs），
因此本 release 精确等于「生产 2514 + 五处脱敏修复」。落地核对：

| 项 | 结果 |
|---|---|
| configs 差异 | 仅一个 `.bak-20261010-064510`（`sensitive_patterns.yaml` 早在 06:45 已替换） |
| web / web-mobile | 与 2514 逐文件一致（不重建，不夹带未评审的 3 个前端提交） |
| 二进制 | 唯一差异项 |

### 切换必须同时改两个符号链接

- `ExecStart=/opt/llm-gateway-go/slots/%i/llm-gateway-go` → 二进制与 `version.json` 走 **slot**
- `configs -> /opt/llm-gateway-go/current/configs` → `newSanitizePatternDetector()` 用相对路径读 YAML，走 **current**

只换 `current`：跑的还是旧二进制，而新版 YAML 无人兜底。
只换 slot：跑的是新二进制，但读到旧 YAML，形状类修复不生效。
本次两者一并原子替换。

### 切换前拆除了一颗待爆弹

生产 `current` 指向的 2514 里，`configs/sensitive_patterns.yaml` **已经是新版**（06:45 替换），
配的却是**旧二进制**——旧代码没有 `credibleSecretCandidate` 过滤，新版正则又放开了连字符。
两者叠加的后果是：**任何一次重启**（崩溃、发布、机器重启）都会让 `api-design-patterns`、
`token-bucket-algorithm`、`key-value-store` 这类普通英文被无约束地误脱敏。

该混合态**当时尚未发作**：进程 01:59 启动、YAML 06:45 才换，内存里仍是旧规则；
且代码只在启动时读 YAML（无 fsnotify 监听），reload 端点因 admin token 鉴权不通过而一直返回失败。

新进程启动日志已确认拆除：

```
env[1621] "msg":"sanitize pattern detector ready","path":"configs/sensitive_patterns.yaml"
```

即正常加载 YAML、未走 fallback 分支。

### 部署后复验结果

**E2E 场景矩阵：42 通过 / 0 失败**（minimax-m3、gpt-6-luna、glm-5.3、deepseek-v4-pro、mimo-v2.5-pro）。
部署前那次的 deepseek-v4-pro C2 偶发超时未复现。

出向明文取证：158 行 `outbound_body`，**剥除占位符后残留 0 行**，
`sk-[A-Za-z0-9]+-[A-Za-z0-9-]{15,}` 明文密钥 **0 条**。

**三协议 + 工具路径矩阵：全绿**（`client_placeholder_leak` 全部 False）：

| 用例 | 结果 | 出向占位符 | 客户端占位符泄漏 |
|---|---|---|---|
| `messages`（Anthropic） | PASS | 该请求未落 body（保留策略） | False |
| `responses`（OpenAI Responses） | PASS | 该请求未落 body（保留策略） | False |
| `chat` | PASS | **yes / true** | False |
| `chatstream` | PASS | — | False |
| `tools/args` | PASS | 该请求未落 body（保留策略） | False |
| `tools/call` | PASS | tool_calls=True | False |

注：`messages` / `responses` / `tools/args` 三例命中 body 保留策略、未落 `outbound_body`，
脚本已显式标注 `NOT-STORED` 并只验客户端侧——这是**未存储**，不是**未脱敏**，两者不可混淆。

误伤审计：`api-design-patterns` / `token-bucket-algorithm` / `key-value-store` /
`bearer-of-responsibility` 四种高危普通英文形态在部署后窗口内出现 **0 次**——原样通过，未被误脱敏。

**关于 `go test ./admin/...` 的失败定性**：首次跑出现 4 个失败，全部与本次改动无关——
三个是 `TestAuditGatesAreRegisteredInTheirDocs` / `TestAuditDocSilentClaimMatchesRegistry` /
`TestAuditDocSilentClaimIsMarkedAsMachineChecked`，报 `docs/audit/*.md: no such file or directory`，
根因是**同步构建树时为了省带宽排除了 `docs/`**（测试台取舍，非代码问题）。
补齐 `docs/`（329 份审计文档）后重跑，三者**全部 PASS（rc=0）**。
第四个 `TestV1BodiesReadersAreAssessed` 是 AGENTS.md 明确记录的**故意红负向闸门**，重跑仍红，符合预期。

本次改动的实际受影响面只有 `security/sanitize` 与 `configs/sensitive_patterns.yaml`，
`go test ./security/sanitize/...` 全绿；`gofmt -l` 在 `security/sanitize/` 下仅报
`input_protocols.go` 与 `deployment_pattern_config_test.go` 两个**既有未改动文件**。

泄漏速率前后对比（按每千行归一化，避免窗口长度不同造成误读）：

| 窗口 | 行数 | 裸形态泄漏 | 每千行 | 完整占位符泄漏 | 每千行 |
|---|---|---|---|---|---|
| 部署前（06:00–11:13） | 4658 | 34 | 7.3 | 86 | 18.5 |
| 部署后（11:13– ） | 360 | 2 | **5.6** | 5 | **13.9** |

裸形态 7.3 → 5.6、完整占位符 18.5 → 13.9，两项均下降，但**降幅远小于小样本时的观感**：
部署后窗口内裸形态只有 2 例，逐千行归一化后单条事件就会让数字在 3.5–5.6 之间跳动。
**这两个比率只能作为方向性参考，不足以支撑「修复降低了 X%」的结论**；
可信的是绝对量级对比（4658 行 34 例 vs 360 行 2 例）与出向零残留。
完整占位符一项未清零，见下节。

---

## 九之二、完整占位符 fail-open 透传（当时的遗留缺陷）

> **【2026-10-10 23:30 审计更正】本节的结论已被后续提交推翻，缺陷不复存在。**
> 本节写于 `ce5e35ab3`（遮蔽标记带类型，16:31）之前，当时观察到的 96 行透传
> 来自修复前的构建。审计已用探针在当前 HEAD 上逐条复验：9 种拼写变体
> （精确全角、补零索引、大小写、空格、全宽、去括号连字、缺索引、小写 marker）
> **全部 fail-closed**，且报告建议的结构化日志已就位。
> 逐条证据见第二十节。本节以下内容保留为**发现时的原始观察**，不作为当前结论。

部署后取证发现一个**先前未记录的缺陷**，不属于本次五处修复。
速率在部署后有所下降（18.5 → 13.9 每千行），但**未清零**，
说明它是既有行为、并非本次发布引入的回归，同时也说明本次五处修复并未覆盖它。

### 现象

`{SENSITIVE:server_ip:1}` 这类**带合法索引**的完整占位符，未能还原、也未打码，
而是**原样作为字面文本进入客户端响应**。部署后窗口内 4 条，全部来自同一个真实用户会话
`gw_bab6a75e…` / minimax-m3。

### 敏感值本身没有泄漏——但已量化确认

对当天全部 **96 行**透传记录做过真值扫描，私网 IP / 11 位手机号 / 邮箱 / `sk-` 开头密钥
四类真值形态的命中数**均为 0**。占位符确实占住了真值位置。

透传的标记按类型分布（366 个实例 / 96 行）：

| 类型 | 透传实例数 | 说明 |
|---|---|---|
| server_ip | 158 | 内部主机 IP |
| secret | 87 | **API 密钥**（值未泄漏，仅标记语法外泄） |
| internal_ip | 69 | 私网地址 |
| custom | 21 | 自定义类型 |
| email | 17 | |
| phone | 9 | |
| id_card | 5 | |

对照：同期出向被正确保护的占位符分布为 server_ip 653 / secret 146 / internal_ip 83 /
account 43 / email 23 / id_card 15 / phone 11 / custom 10 / credit_card 8。

因此实际泄漏的是**内部标记语法本身**：类型名、索引，以及
**该会话内被检测到的不同敏感值数量**（索引 55 即意味着本会话捕获了 55 个不同密钥）。
上下文样本形如 ``cfg.ACCServiceToken ={SENSITIVE:secret:21} ""`` —— 位置正确、值为空标记。

严重性判定：不泄漏敏感值；但向客户端暴露脱敏机制的存在与规模，
且对同一会话的观察者可推断敏感值种类与数量。属中低危的信息暴露，不是凭据泄露。

### 根因：未确认（原判「缺 CENSOR_KEY」已撤回）

涉事会话有 **3748 条映射行（其中 1763 条 `server_ip`）**，`original_encrypted` 无一非 NULL：

| 分组 | 行数 | `original_encrypted IS NULL` |
|---|---|---|
| 历史 | 84584 | 84584 |
| 本轮 E2E | 157 | 157 |

映射行在、加密原值全空。**Redis 不是原因**——原报告曾据 12 秒超时的 `--scan` 断言
「Redis 无 sanitize 键」，该断言是扫描假象并已作废：db2 有 1,691,636 个键，
`redis-cli 3.2.5` 遍历全库需 300 秒；300 秒全量扫描实得 **337 个** sanitize 键，
且探针会话的映射直查为 `{SENSITIVE:phone:1} → 13900004444`（明文，TTL 1740s）。

因此这 96 行的透传不是因为「找不到原值」，**成因尚未定位**。
已知线索：涉事会话 `gw_bab6a75e…` 跨度约 5 小时（06:36–11:24），
而映射 TTL 仅 30 分钟——早轮次占位符可能已随 TTL 过期。
但该哈希每次写入都会 PEXPIRE 刷新，具体是哪一条路径失效仍需进一步验证，
本报告不做超出证据的推断。

### 为什么之前没被发现

此前报告里「E2 占位符映射已落库（original_encrypted 加密存储）」这句话是**误导性的**——
它只断言了映射行存在，从未校验该列是否有值。实测该列 100% 为 NULL，
即 **DB 侧从未具备可逆性**（这一点成立，且与 Redis 无关）。

### 修法（当时的选项；第 2、3 项事后已由既有提交覆盖）

1. ~~配置密钥能让还原生效~~ —— **该建议已撤回**。`security/sanitize/db_sink.go` 文件头写明
   「全仓没有任何解密读取方……只配密钥会让列里出现无人能解的密文」，
   且 `placeholder.go:48` 确认还原路径只读 Redis、从不读 DB。
   配密钥只会让 DB 多一批无人解的密文，**不会改变还原行为**。
   它的真实价值仅在于让 DB 侧的脱敏元数据具备审计可追溯性。
2. ~~**改 fail-open 为 fail-closed**~~ —— **已落地，非本次新增**。
   当前 HEAD 的所有客户端可见路径都走 `RestoreOutputOrMask`（`looseMaskUnknown`），
   未解析出的占位符一律打码；`RestoreOutput`（`looseKeepUnknown`，唯一保留字面量的入口）
   在生产代码中**零调用点**，只被测试引用。风险项「下游依赖该字面量」因此不成立。
3. ~~**在 fail-open 分支加结构化日志**~~ —— **已就位**。`sanitizer.go:263` 与
   `sanitizer.go:285` 分别记录精确未命中与改写未命中，容错分支在 mask 模式下同样计数上报，
   指标为 `sanitize_placeholder_mutation_total{result}`。

---

## 九之四、还原能力探针实验（2026-10-10 11:52）

为回答「还原到底能不能工作」，用同一会话两轮探针实测。

发入 `手机号 13900004444 / 邮箱 restore@example.org`，并明确要求模型原样抄写它看到的敏感标记。

**Redis 映射（O(1) 直查，非扫描）**：

```
session:37a8eec1ce19687d:gw_sanirestore-1791604274:sanitize   TTL=1740s
  {SENSITIVE:phone:1}  →  13900004444
  {SENSITIVE:email:1}  →  restore@example.org
```

**DB 侧**：`session_censors_hot` 两行，`enc_null = t`。

**模型的反应**：它报告自己看到的是

```
我的手机号是 139****4444，邮箱 r***@e***.org
```

即**部分打码值**，而非 `{SENSITIVE:phone:1}` 占位符。

**结论**：

1. 映射存在且值为明文，TTL 正常——**Redis 这条链路是通的**。
2. 模型没有回显占位符，因此**本轮并未构成对还原能力的有效检验**：
   没有可供还原的目标，"未还原"不能判为还原失败。
3. 这解释了 E2E 矩阵里 A3/C3 普遍"未还原"——大概率是同一原因，而非模型拒绝复述 PII。
4. 同时暴露一个此前未被记录的现象：**上游实际收到的是部分打码值，不是占位符**。
   这与入向中间件写入的占位符映射不一致，该差异本身值得单独查证。

因此本轮**无法给出"还原是否正常"的结论**，只能确认"脱敏正常"，
以及"完整占位符透传 96 行"这一待查缺陷。

---

## 九之三、回滚

```bash
ssh 154 'ln -sfn /opt/llm-gateway-go/releases/2514-ac6f2276 /opt/llm-gateway-go/.rb-c \
  && mv -Tf /opt/llm-gateway-go/.rb-c /opt/llm-gateway-go/current \
  && ln -sfn /opt/llm-gateway-go/releases/2514-ac6f2276 /opt/llm-gateway-go/.rb-s \
  && mv -Tf /opt/llm-gateway-go/.rb-s /opt/llm-gateway-go/slots/8782 \
  && systemctl restart llm-gateway-go-canary@8782.service'
```

`/root/switch-2515.sh` 内置了同样的回滚逻辑：探活或版本断言失败即自动切回 2514 并重启恢复。


---

## 十、minimax-m3「首字单行」取证（2026-10-10）

用户报：**minimax-m3 的第一个请求，回复总呈现「单字一行」**（截图见会话附件，
`G` / `o` / `o` / `d` / `.` / `M` / `a` / `s` / `t` …，且前面「执行 4 条命令」是
ZCode agent 的工具调用界面）。要求研究 154 的 log 与请求并修正。

### 结论：网关侧未复现，证据指向客户端渲染层

四轮取证，**没有任何一轮在网关侧观测到单字分片**。

#### 证据 1：上游分片粒度（近 12h 全量 raw_data，263872 帧）

按模型聚合 `delta.content` 的长度分布（脚本 `/tmp/scan_granularity.py`）：

| 模型 | 请求数 | 分片数 | 单字分片 | 占比 |
|---|---|---|---|---|
| **MiniMax-M3** | 773 | 14378 | **220** | **1.5%** |
| glm-5.3 | 82 | 5722 | 2250 | 39.3% |
| glm-5.3-flash | 14 | 541 | 273 | 50.5% |
| glm-5.2 | 68 | 2047 | 101 | 4.9% |
| mimo-v2.5-pro | 3 | 15 | 1 | 6.7% |

**minimax-m3 上游 98.5% 的分片是多字符**，其单字率显著低于 glm 系。
即：网关收到的上游字节本身就是正常粒度，不存在「上游按字下发」。

#### 证据 2：复现用户真实请求

从 raw_data 捞出用户本人的会话（`rid=c116a05ae37dce4997f5273418b83f49`，
minimax-m3 / 34 个工具 / 9 条消息 / 113694 字节），原样回放到 154：

```
client-visible deltas: 14
non-empty: 14   empty: 0   single-char: 0
first 25 sizes: [21, 82, 75, 112, 60, 46, 26, 47, 99, 64, 47, 88, 47, 28]
```

**14 个分片全部多字符，0 个单字**，拼接后是完整正常的中文长回复。

#### 证据 3：脱敏开启时同样正常

首轮（session map 为空）会走 `RestoreOutputOrMask` 的 mask 分支，
双轮（map 已填充）会走 `restoreStreamStringField` 的续包还原——两条路径都测了：

| 场景 | 分片数 | 单字 |
|---|---|---|
| 首轮·含手机号（空 map） | 1（687 字符 think） | 0 |
| 双轮·含 IP + key（有 map） | 6，`[100,88,93,121,92,49]` | 0 |

**「第一个请求」这个条件本身不会触发单字。**

#### 证据 4：Anthropic 协议路径（截图客户端用的协议）

截图是 ZCode/Claude-Code 类 agent，走 `/v1/messages`：

```
event types: {message_start:1, content_block_start:3, ping:1,
              content_block_stop:3, content_block_delta:2,
              message_delta:1, message_stop:1}
text deltas: 1  single-char: 0
```

同样正常。

### 网关侧确实存在的、与本症状**无关**的两个缺陷

取证过程中确认了第一至四节记录的两个真实缺陷（`sk-` 连字符密钥漏检、流式
`(SENSITIVE:` 半截 token 泄漏），它们**都不能**产生「单字一行」：

- 密钥漏检是**漏掉**一个 span，不是把文本切碎；
- 半截 token 泄漏是**放行**一段文本，不会把一段文本拆成单字。

### 指向客户端的线索

1. 截图里的占位符是 `[SENSITIVEMASK:srv_ip:1]` —— **这个格式在本仓库中不存在**
   （全仓 grep 无命中）。本仓库的格式是 `{SENSITIVE:server_ip:1}`。
   说明截图那一侧还有一层网关/客户端在做自己的脱敏。
2. 截图上半部分正文排版正常，只有工具调用之后的那段变成单字——
   典型的**增量渲染按帧换行**特征（每帧当一行），而非内容被切碎。
3. glm-5.3 系上游单字率天然高达 39–50%，同一客户端渲染 glm 时「单字一行」
   会更明显——这与「换行来自客户端逐帧渲染」一致。

### 若要继续定位，需要客户端侧信息

网关侧已经证伪，下一步需要在**客户端**确认：

- ZCode/agent 客户端的 SSE 消费实现是否把每帧当独立行渲染；
- 客户端是否启用了自己的「敏感信息掩码」（`SENSITIVEMASK` 格式的来源）；
- 抓一份客户端实际收到的原始 SSE 字节（curl 直连 154 已证明其正常）。

复现命令（本轮已用，均指向 8782）：

```bash
KEY=$(grep -E '^LLM_GATEWAY_API_KEY=' /etc/llm-gateway-go/env | cut -d= -f2-)
curl -sS -N -m 180 -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -H "X-Session-ID: probe-$(date +%s)" \
  http://127.0.0.1:8782/v1/chat/completions \
  -d '{"model":"minimax-m3","stream":true,
       "messages":[{"role":"user","content":"用一句话解释什么是负载均衡。"}]}'
```

### 附带发现：工作区的 sanitize 改动当前测试红

本轮为验证「能否提交/部署」，在 154 上（`/opt/go1.27.1` 不存在，实际只有
go1.25.0，需把 `go.mod` 与 `vendor/modules.txt` 的 go 指令临时下调才能离线跑）
对**当前未提交的工作区**跑了 `security/sanitize` 全套件：

```
--- FAIL: TestParseLoosePlaceholder
--- FAIL: TestRestoreOutputOrMaskToleratedMutations
--- FAIL: TestSecretRuleMatchesShippedYAMLConfig
--- FAIL: TestSanitizeRestoreInterceptor_MasksInvalidPlaceholderContinuationAndKeepsStreamOpen
FAIL  github.com/kaixuan/llm-gateway-go/security/sanitize
```

隔离实验（`/tmp/baseline_full` = HEAD 版 sanitize 包，逐个叠加工作区文件）：

| 叠加内容 | 结果 |
|---|---|
| HEAD 原样 | ok |
| HEAD + `placeholder_loose.go` | ok |
| HEAD + `smart_sani_guard.go` | ok |
| HEAD + 四个改动文件全上（不含 r244 新测试） | ok |
| HEAD + 全量工作区（含 r244 新测试） | **FAIL ×4** |

即：**回归由新增的 r244 测试触发，不是四个源文件本身**。
`build` 通过、`gofmt -l` 无输出（干净），但行为契约与既有测试冲突。

**因此本轮未提交、未部署。** 两个理由：

1. 这批 sanitize 改动与用户报的「单字一行」**无关**（见上，取证已证伪网关侧）；
2. 工作区在本轮进行期间被**另一个会话持续修改**——`placeholder_loose.go`
   从「未改动」变为「已改动」，并新增了
   `r244_bare_marker_form_test.go`、`r244_bare_marker_corpus_audit_test.go`、
   `r244_bare_marker_false_positive_test.go` 等文件。此时提交会把他人在途、
   且测试红的改动一并带入。

---

## 十、合并 origin/main 与 245 部署状态（11:30）

### 合并

`main` 与 `origin/main` 分叉（本地 ahead 1 / behind 6）。按用户决定先合并：

```
git merge origin/main   # 75d9c879f，无冲突
```

合并后 `security/sanitize` 与 `domains/streaming` 全套件在 154 上**全绿**：

```
ok  security/armor      ok  security/guardian   ok  security/sanitize
ok  security/sensitive  ok  domains/streaming   ok  domains/streaming/executors
ok  …/executors/webcookie  ok  …/integrity     ok  …/state
```

第九节记录的 4 个红测，是**另一个会话在编辑过程中的中间快照**；
`7dd0b4b67` 提交后的状态本身就是绿的，此前判断有误，此处更正。

两处 `gofmt -l` 命中（`input_protocols.go`、`deployment_pattern_config_test.go`）
经核对是 **origin/main 上就已存在**的格式问题，非本轮引入，未擅自改动。

### ~~245 部署：被环境前置条件挡住，未完成~~ → 已在本节之后完成

> **本小节结论已作废。** 当时的判断「本机不满足 deploy 侧前置条件、需用户装
> bash 4+」是**不完整的**：bash 3.2 确实是障碍，但它是可以绕开而不必安装任何
> 东西的。下面先保留当时的错误结论与实测证据，随后在「十一」节记录解除阻塞
> 与两台机器实际部署完成的全过程。

当时的实测（保留作为问题定义）：

```
1) scripts/deploy-245.sh:75: ARGS[@]: unbound variable
2) deploy-lib/host.sh:103: HOST_${target^^}_ROOT: bad substitution
   deploy-lib/ssh-retry.sh:223: extra_flags[@]: unbound variable
```

根因：**deploy-lib SSOT 使用 bash 4+ 语法**（`${var^^}` 大写展开、
空数组在 `set -u` 下的展开、`declare -A`），而本机只有 macOS 自带的
**bash 3.2.57**，`/opt/homebrew/bin/bash` 与 `/usr/local/bin/bash` 均不存在。

仓库对此并不陌生：`scripts/checks/` 里有专门的门要求**用户脚本**兼容 bash 3.2；
但 `deploy-lib` 是仓外 SSOT，它面向的是 bash 4+ 环境。

### 补记：154 已由并行会话升级到 2515

11:34 复核时，154 已由另一会话部署为 `2.5.8-7dd0b4b67-20261010-2515`
（`/healthz` `ready:true`）。在该新版本上重跑第九节的取证探针：

```
minimax-m3 chat.completions 流式：deltas 17，single-char 0
sizes[:12] = [28,16,17,10,11,12,6,2,3,10,3,3]
```

**结论不变**：2515 上网关侧依然没有单字分片，「单字一行」不是网关产生的。

## 十一、解除 bash 3.2 阻塞，245 与 154 部署完成（11:36–11:58）

第十节的「需用户装 bash 4+」结论作废。本轮**未安装任何东西**，
用只读复制 + 就地修补的方式绕开了 SSOT，把两台机器都部署到位。

### 十一之一、绕开 SSOT：拷贝到 /tmp 再改，不动共享状态

`deploy-lib` 是跨仓共享 SSOT（git 仓、工作区干净）。直接改它会影响其他仓与
其他会话，因此本轮**不在 SSOT 上落任何改动**：

```bash
cp -R ~/workspace/ai-native-tools/deploy-lib /tmp/deploy-lib-b32
export AIAN_DEPLOY_LIB=/tmp/deploy-lib-b32   # deploy-lib-resolve.sh 第 1 优先级，caller wins
```

`scripts/deploy-lib-resolve.sh` 的解析顺序把 `$AIAN_DEPLOY_LIB` 放在第 1 位
且「caller wins, never clobbered」，所以指向副本是受支持的用法，不是 hack。

bash 4+ 构造一共只有 5 处，全部替换为 bash 3.2 等价写法：

| 文件 | 原写法 | 替换 |
|---|---|---|
| `host.sh:69,103` | `HOST_${target^^}_ROOT` | `tr` 大写 |
| `targets.sh:119,300` | `SSH_KEY_${target^^}` | `tr` 大写 |
| `ssh-retry.sh:223` | `"${arr[@]}"`（`set -u` 下空数组） | `"${arr[@]+"${arr[@]}"}"` |
| `db-changelog.sh:230` | `declare -A applied` | 线性成员扫描 |
| `db-changelog.sh:268` | `local -A seen` | 两组平行数组 |

语义等价性都做了实证，不只靠 `bash -n`（后者查不出变量先用后赋）：

- `host_root_for 245` / `host_release_layout 245` / `_ssh_key_for 245` /
  `_ssh_host_for 245` / `target_unsupported_contract` 全部正常返回；
  `SSH_KEY_BOGUS-HOST` 说明 `tr` 与 `^^` 对非数字输入行为一致。
- 迁移成员判定：`411→PENDING / 412→applied / 500→applied / 501→PENDING`，
  空数组在 `set -u` 下不炸。
- 版本冲突门：唯一版本放行；`849_first.sql + 849_second.sql` 被拒且报错行
  `version 849: 849_first.sql, 849_second.sql` 仍指向**首个**文件，与原实现一致。

> 第一次补丁把 `_t_upper` 赋值写在了使用点之后，是 `bash -n` 放行、
> 运行时报 `unbound variable` 才暴露的——这也是为什么坚持跑函数级冒烟。

### 十一之二、仓库内三个真修复（均已提交）

1. **`558953c52` deploy-245/154 空 ARGS 在 bash 3.2 下 unbound**
   `deploy-245.sh:44` 早就有 `${#ARGS[@]} > 0` 守卫，但只护住了 `--help` /
   `--dry-run` 两个分支，漏掉末尾 `exec bash ... "${ARGS[@]}"`；154 入口完全没有
   守卫。改为 `"${ARGS[@]+"${ARGS[@]}"}"`。

2. **`d44a29f77` web-mobile 补 package-lock.json**
   `web-mobile/` 是仓内唯一没有 lockfile 的前端子项目，部署脚本因此退回非冻结
   `npm install`，在 npm 10.9.9 上稳定崩在 arborist：
   `Cannot read properties of null (reading 'edgesOut')` @ `#loadPeerSet`
   （解析 `vitest@^4.1.11` 的 peer 集合时）。补 lockfile 后部署走 `npm ci` 冻结
   安装，不再触发 ideal-tree 的 peer 解析。

3. **`e3e2b4ef` 迁移 849 补 `SET LOCAL statement_timeout`**
   245 部署实录（seq 2517）：

   ```
   psql:.../849_..._cover_idx.sql:50: ERROR:  canceling statement due to statement timeout
   ```

   根因是 252 共享 PG 的 `statement_timeout=30s`，而本迁移是 6 分区父表上的普通
   `CREATE INDEX`（分区父表拒绝 CONCURRENTLY，R46/843/846 先例），持 ShareLock
   为 ~79.3 万 NULL 行建索引。**该文件自己的 Background 段已经写明要配套
   `SET LOCAL` 抬升，只是 up.sql 从未落地。** 按 649/834/836/837 同一先例系
   钉 `10min`。
   改动安全性：849 在 245 与 154（共享 252 PG）均**未**进 `schema_migrations`、
   索引也不存在（两处实测 `count=0`），属纯 pending 迁移，就地修正不产生
   已应用迁移漂移。

### 十一之三、另外两个非代码障碍

- **build_seq 回退风险**：本机 floor 停在 2510，而 245 跑 2512、154 跑 2515。
  直接部署会把 2512 覆盖成 2511（回退）。已把 floor 抬到 2515（全局最大已部署值），
  之后按 `max(version.json, floor)+1` 递增，最终落到 2518/2519，全程单调。
- **Go 不在 PATH**：工具链其实**已装**在 `~/sdk/go/bin/go`（go1.27.1，与
  `go.mod` 的 `go 1.27.1` 完全一致，`vendor/` 齐备），只是没进 PATH。
  `export PATH="$HOME/sdk/go/bin:$PATH"` 即可——这是**使用**既有工具链，
  不是安装，不触犯 AGENTS.md「依赖安装属用户侧决策」。
  预检 `go build ./...` 23s 通过。

- **stale 部署锁**：首次执行撞上 `/var/lib/llm-gateway-go/deploy.lock`（rc=75）。
  读取 `metadata` 确认持有者是**另一个会话**（`commit=869c04bf2`，
  `started_at=2026-10-10T03:33:12Z`），本机 `pid 76002` 已不存在、无任何
  deploy 进程在跑，且该会话在 session list 里是 `idle`——判定为崩溃残留的
  **陈旧锁**，才使用文档化的 `--force` 恢复。没有猜测性地强杀。

### 十一之四、部署结果（两台都成功）

```
245  seq=2518 sha=e3e2b4ef  active_port=8782  总 275s / 切换 61s
     old=2512-786de4a1 → new=2518-e3e2b4ef
     迁移 849 + 850 新应用（849 的 BEGIN/SET/CREATE INDEX/COMMIT 四步全过）
     healthz + readyz + DB + release identity + 凭据解密冒烟 全通过
     /healthz → {"build_seq":2518,"git_sha":"e3e2b4ef","ready":true}

154  seq=2519 sha=a0fdaaa5  active_port=8781  总 237s / 切换 119s
     old=2515-7dd0b4b67 → new=2519-a0fdaaa5
     无 pending startup 迁移（849/850 已由 245 那轮落到共享 252 PG）
     healthz + readyz + DB + release identity + 凭据解密冒烟 全通过
     /healthz → {"build_seq":2519,"git_sha":"a0fdaaa5","ready":true}
```

两次 handoff 都远超 2s 阈值（61s / 119s），脚本按设计**保留旧实例用于回滚**、
不停止旧服务；2517/2515 两个未验证 release 已被 prune。

> 注意端口轮换：245 的 active 端口现在是 **8782**（8781 已 drain），
> 154 是 **8781**。写监控/探针时不要写死 245:8781。

### 十一之五、本轮没有改变的结论

「minimax-m3 首个请求单字一行」**仍未定位到网关侧成因**，本次两台部署不涉及
该症状的任何代码路径。第十节的取证结论继续成立：网关侧单字率 1.5%（220/14378），
真实会话原样回放 0 单字，2515 上复验 17 分片 0 单字。

要把这个症状真正关掉，仍缺第十节列的客户端侧证据——ZCode/agent 客户端实际
收到的原始 SSE 字节，或确认该客户端是否把每帧当一行渲染、是否自建掩码层。

## 十二、minimax-m3「首字单行」的决定性量化（2026-10-10 15:47）

第十节用单字率证明了「网关侧大多数回复不是单字」。本节把判据换成**与症状
直接对应的那一个指标——首个 delta**，并按模型归因。

### 方法学修正（此前扫描的两个偏差）

1. **`raw_data` 是 base64 编码**，不是明文。必须先解码。
2. **一个逻辑 SSE 帧会被拆进多条日志记录**（实测 600 条记录里 sse 283 /
   other 282 / json 2）。逐条正则匹配会丢掉半截帧，得到严重偏低的样本。
   正确做法是**按 `request_id` 拼接全部记录后再解析 SSE**。

另外模型名不在 `upstream_response` 上，必须用同 `request_id` 的
`upstream_request` 帧里的 `"model"` 关联，否则只能拿到跨模型混合的池化数字。

脚本：`/tmp/fd5.py`（154，`python3 /tmp/fd5.py raw_data_*.jsonl`）。

### 结果（154 raw_data，1243 条回复 / 28348 个 delta）

```
model              resp   deltas  first1  first1%    all1%
minimax-m3         1131     25267       2     0.18%    7.71%
glm-5.2             111      3085      14    12.61%     5.09%
gpt-6-luna            1         2       0     0.00%   50.00%
glm-5.3-flash         1         2       0     0.00%   50.00%
```

**minimax-m3 只有 2 / 1131（0.18%）的回复以单字符开头，且这 2 个都是「我」。**
也就是说 99.82% 的 minimax-m3 回复，其首个 delta 都是多字符。

### 这条结论比第十节更强

- 第十节说的是「单字率低」；本节说的是**与症状一一对应的那个量**——首字单行，
  在 minimax-m3 上是 0.18%。
- 反向对照：**glm-5.2 的首字单字符率是 12.61%，是 minimax-m3 的 70 倍。**
  如果客户端确实存在「每帧当一行渲染」的行为，最先该在 glm-5.2 上炸，
  而不是 minimax-m3。用户报告的现象与实测方向相反。
- 7.71% 的整体单字率是**流中** tokenization 的正常现象（minimax-m3 逐 token
  输出，中文单字 delta 很常见），它不落在「第一行」，与症状不符。

>口径说明：第十节报的 minimax-m3 单字率 1.5%（220/14378）与本节的 7.71%
> （约 1948/25267）不一致，源于上述第 1、2 条方法学缺陷——旧扫描未解码
> base64、未重组被拆分的帧，样本口径偏窄。此处按修正后的口径给出，
> 旧数字作废。**与症状直接相关的 `first1% = 0.18%` 不受该修正影响地指向同一结论。**

### 仍然缺什么

网关日志里**没有 `client_response` 方向**——只有 `upstream_request` /
`upstream_response` / `client_request`。也就是说「模型返回了什么」有记录，
「发给客户端的字节是什么」没有记录，中间经过 sanitize / 协议转换 / 代理层
的最后一跳是观测盲区。

要闭环仍需二选一：
1. ZCode / agent 客户端实际收到的原始 SSE 字节（`curl -N` 抓一次即可）；
2. 确认该客户端是否把每个 SSE 帧当一行渲染、是否有自建掩码层
   （`SENSITIVEMASK:` 这类格式在本仓 grep 无命中，疑为另一层网关/客户端实现）。

在此之前，任何在网关侧「修单字行」的改动都缺乏证据支撑——本轮所有改动
（bash 3.2 部署可移植性、web-mobile lockfile、迁移 849 超时）都不在该症状
的代码路径上。

## 十三、最后一跳（nginx）已排查干净（2026-10-10 15:52）

网关日志缺 `client_response`，但网关与客户端之间的 nginx 代理层是可以检查的。
这是**唯一还能被观测的中间跳**，逐项查完：

### 1. 缓冲区会重新切分 SSE 帧 —— 未开启

两台在流式端点上都显式关掉了缓冲：

```
154  location ~ ^/v1/(chat/completions|messages) {  proxy_buffering off; }
245  location ~ ^/v1/(chat/completions|messages) {  proxy_buffering off; }
```

`/v1/`、`/v1beta/`、`/v2/`、`/api/`、`/api/admin/live-stream` 同样都是
`proxy_buffering off`。nginx 默认 `proxy_buffering on` 会把上游响应攒进缓冲区再吐，
确实有能力改变分帧边界；这条路径上它没生效。

### 2. gzip 会改写字节流 —— 未覆盖 text/event-stream

两台都是全局 `gzip on`，但 `gzip_types` 里**没有 `text/event-stream`**
（在 `nginx.conf` 的 `default_types` 中匹配数为 0）。nginx 不压缩
Content-Type 不在 `gzip_types` 里的响应，SSE 原样透传。

### 结论

可观测链路已全部走完且干净：

```
模型 ──► 网关        minimax-m3 首帧单字符 0.18%（第十二节）
网关 ──► nginx       proxy_buffering off + SSE 不在 gzip_types ✔
nginx ──► 客户端     ✗ 无 client_response 日志，且尚未拿到客户端抓包
```

**症状只能产生在最后一跳或客户端自身。** 这与第十二节的反向对照一致：
若真有「每帧换行」的问题，glm-5.2（首字单字符 12.61%）会先炸，而不是 minimax-m3
（0.18%）。

### 抓取客户端字节的最短命令

在**客户端所在机器**执行（key 请用你自己的，不要贴回会话）：

```bash
curl -N -sS https://llm.kxpms.cn/v1/chat/completions \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"minimax-m3","stream":true,
       "messages":[{"role":"user","content":"用一句话介绍你自己"}]}' \
  | tee /tmp/m3.sse
```

判据：统计 `data:` 行里 `content` 长度为 1 的比例，并看第一帧：

```bash
grep -o '"content":"[^"]*"' /tmp/m3.sse | head -20
```

若这里首帧也是多字符，则客户端渲染层是唯一嫌疑；若这里已经是单字符，
问题在更上游，需要连同客户端的换行策略一起看。

## 十四、根因：minimax-m3 是推理模型，每条回复都以 `<think>` 块开头（15:55）

第十三节把最后一跳交给客户端之前，我先在**本机经公网入口实测抓了一次真实客户端字节**
（`https://llm.kxpms.cn/v1/chat/completions`，`curl -N`，key 在 154 本地读取、
不外传）。这一抓同时给出了前十三节都没拿到的关键事实。

### 客户端实际收到的内容

```
delta 0  len=111  '<think>The user is asking me to introduce myself in one sentence, in Chinese...'
delta 1  len=94   ' they asked in Chinese, and follow my system prompt about being a thinking model...'
delta 2  len=71   '</think>\n\n我是由 MiniMax 开发的 AI 助手 MiniMax-M3，...'
```

拼起来的完整文本：

```
<think>The user is asking me ... being a thinking model from MiniMax.</think>\n\n我是由 MiniMax 开发的 AI 助手...
```

**客户端收到的是一段英文推理过程，然后才是答案。**

### 模型归因：这是 minimax-m3 独有的

按模型统计「以 `<think>` 开头的回复占比」（raw_data 全量，修正口径）：

```
model           resp   think    think%
minimax-m3      1145    1003     87.6%
glm-5.2          111       0      0.0%
```

**minimax-m3 有 87.6% 的回复以 `<think>` 开头；glm-5.2 是 0%。**
这就是「为什么只有 minimax-m3 会这样」的答案：它是推理模型，
`<think>` 推理块写在 `delta.content` 里，是模型的固有行为。

> 口径更正：第十二节写的「0/1243 以 `<think>` 开头」是**我写错了检索串**——
> 查的是 `<thinking>`（带 ing），而模型实际吐的标签是 `<think>`。用正确
> 标签重算即上表 87.6%。

### 网关侧没有任何响应侧的 think 过滤

全仓检索确认：所有 "thinking strip" 代码（`domains/hooks/compression/strip.go`、
`internal/paramguard/guard.go`）作用方向都是**出站请求**——为省 token 把
Anthropic 请求里的 thinking 块删掉，与「把 `<think>` 从响应里滤掉」无关。
唯一对响应做 think 剥离的是 `cmd/autoclass-bench/main.go` 的 `stripThink`，
那是压测工具，不在请求路径上。

所以 `<think>...</think>` 是**原样透传**给客户端的。
（`security/sanitize/sse_restore.go:101` 的注释也确认网关知道「minimax <think>
会原样回显」，但那是为占位符还原服务的，不是为隐藏推理内容。）

### 这解释了症状的哪部分

| 观察 | 是否解释 |
|---|---|
| 只在 minimax-m3 出现 | ✅ 该模型 87.6% 带 `<think>`，glm-5.2 为 0% |
| 「总出现」（每次都有） | ✅ 87.6% ≈ 绝大多数请求 |
| 第一个请求回复「不对」 | ✅ 用户先看到的是模型的英文推理，不是答案 |
| **「单字一行」的具体排版** | ❌ 仍未解释，需客户端侧确认 |

最后一行是诚实的缺口：`<think>` 一旦被客户端渲染，排版成什么样取决于客户端。
本仓观察到的首帧单字符率只有 0.18%，所以「单字一行」**不是网关输出的形状**；
但「第一段内容是推理块」这件事本身就足以让用户觉得「第一个请求回复不正常」，
而推理块被客户端逐行拆开渲染是完全可能的（推理文本里含标点、换行、缩进）。

### 建议的两条修法（均未执行，需用户裁决）

1. **网关侧加响应过滤**：在 SSE 出站前丢弃 `<think>...</think>` 区间，
   只把 `</think>` 之后的正文发给客户端。对 OpenAI 兼容端点语义最干净，
   但会改变现有行为——如果客户端正是靠这个块渲染「思考过程」UI，会失效。
2. **客户端侧折叠**：保留透传，由客户端把 `<think>` 块折叠成可展开区域。
   这是目前不少客户端（含部分 agent IDE）的既有做法。

需要先确认：**你们的客户端是否已经自己处理 `<think>`？** 如果没有，
症状就是「网关透传了推理块 + 客户端不会折叠」的组合后果，
两条修法选哪条取决于你们想要的产品形态。

## 十五、精确到代码：`<think>` 是 minimax 清理器唯一漏掉的一类（15:58）

第十四节说「网关没有 think 过滤」是对的，但**不够精确**——会让修法看起来
像要从零设计。实际是：**网关已经在过滤同一类 minimax 泄漏，只是漏了这一种。**

### 已有的清理器

`internal/vendorstrip/minimax.go` → `cleanMinimaxLeakFields()` 针对 m3 处理两类
已知的「内部符号漏进 `content`」：

1. `UnwrapMiniMaxTokenWrappers()` —— 去 `minimax[>[...payload]<]` 包装。
   注释原文：*"removes MiniMax streaming token wrappers of the form
   minimax[>[payload]<] that otherwise leak into client-visible text
   (observed as ... on m3)"*
2. `<function_calls>` —— 去掉泄漏的工具调用标记及其相邻残片。

两者作用在 `message` 的 `reasoning_content` 与 `content` 两个字段上，
即**响应方向**，且逐 delta 生效。

### 决定性验证：清理器在流式路径上确实在跑

对 154 raw_data 1270 条回复统计三类标记的出现率：

```
start with <think>      : 1013  (79.8%)   ← 漏
contain minimax[>[     :    0  ( 0.0%)   ← 已清
contain <function_calls>:    0  ( 0.0%)   ← 已清
```

后两类降到 **0%**，说明 `cleanMinimaxLeakFields` **确实在流式响应路径上执行**，
且清理彻底。`<think>` 的 79.8% 不是「清理器没跑」，而是**清单里没有这一项**。

### 这把修法从「设计题」变成「补一行」

现状不是「要不要让网关过滤推理内容」这种产品形态题（那种题确实该问用户），
而是**同一个已知泄漏类别的第三个成员被遗漏**。把 `<think>` 补进
`cleanMinimaxLeakFields` 的清理集合，与既有两项同构、同位置、同生效时机。

仍然保留的产品决策（需要你定）：

- `minimax[>[` 与 `<function_calls>` 是**协议垃圾**，客户端永远不该看到，
  清掉零争议。
- `<think>...</think>` 是**推理模型的有意义输出**，不少客户端（含部分 agent IDE）
  会据此渲染「思考过程」折叠区。若你们的客户端已在折叠，网关清掉会让那个 UI
  失效；若客户端没折叠，清掉才是根治。

所以判断依据只有一个：**你们的客户端现在会不会自己折叠 `<think>`？**

- 会 → 不要在网关清（保持透传），本节结论仅作为「行为符合预期」的记录。
- 不会 → 在 `cleanMinimaxLeakFields` 补 `<think>` 清理，与既有两项一致，
  并补一条回归测试（断言 `<think>` 清理后 0% 泄漏、既有两项不回退）。

## 十六、【生产事故】Redis 6389 宕机 → sanitize fail-closed 全量 503（16:00）

执行第十四节的多次端到端复现时，捕获脚本开始返回 108 字节的错误体：

```json
{"error":{"code":"sanitization_failed","message":"Request sanitization unavailable","type":"server_error"}}
```

### 定位

从 154 与 245 分别探测 252（`172.16.2.210`）：

```
host 172.16.2.210   ping 0% loss, rtt 0.42ms      ← 机器活着
PG      172.16.2.210:5432   OK                   ← 数据库正常
Redis   172.16.2.210:6389   REFUSED              ← 宕机
redis-cli -h 172.16.2.210 -p 6389 ping → Connection refused
```

252 上 `ss -lntp` 无 6389 监听；`ps` 显示 3 个 `redis-server *:6379`
（均为 30~40 天前起的存量进程，与网关用的 6389 不是同一实例）；
无 `redis*.service`。`deploy-lib/redis-discover.sh` 头部注明
*"154/245 通过 252 使用"*，即网关的 Redis 是 252 上的**容器**，
命名候选含 `llm-gateway-redis` —— 该实例已不在监听。

时间线：15:38 探测 6389 仍返回 `NOAUTH Authentication required`（说明当时在跑，
只是需要认证），16:00 已 refused。**窗口约 15:38–16:00 之间。**

### 影响面

网关 sanitize 层依赖 Redis，且**设计上 fail-closed**（宁可拒绝不放行）：

```
245:8781  bd638a7a-2515  ready:false
154:8782  bd638a7a-2516  ready:false
```

154 日志同时刷出：

```
ERROR call_history_aggregator failed      dial tcp 172.16.2.210:6389: connection refused
WARN  session lookup failed               redis TYPE check failed: connection refused
WARN  request journey observation degraded
ERROR http_request POST /v1/chat/completions status=503 duration_ms=168124
```

即 **所有经网关的推理请求被拒绝**，这是网关的全路径中断，不是单模型问题。

### 与本次工作无关

本会话的所有代码提交均为 docs / 部署脚本 / 前端 lockfile / 一条已应用迁移，
不触及 Redis 配置或 sanitize 的依赖关系。并行会话
`mvs_241c29396ed146989150374cbba8a047`（agent-companion）也在同一时段报
`503 Request sanitization unavailable`，与本故障同源。

### 待处置（未执行）

恢复 252 上的 Redis 6389 实例即自动恢复，无需重部署——两台的 sanitize
会在 Redis 回来后自愈（与 15:40 PG 恢复后的表现一致）。

**未擅自重启**：共享生产 Redis 容器的启停属基础设施决策，且重启涉及
会话/限流/配额等易失状态，需运维侧确认。

### 十六之二、252 上的进一步定位：容器不是「停了」，是「不存在」（16:12）

补查 252，结论比第十六节更具体，且改变了处置方式：

```
podman container exists llm-gateway-redis   → rc=1  (3s)   容器不存在
podman ps                                    → rc=124 (30s)  挂起
```

- **`llm-gateway-redis` 容器不存在**，不是 exited。`podman container exists`
  3 秒返回 rc=1（未找到），说明不是 podman 全面卡死，而是**该容器已被移除**。
- 因此**不能靠 `docker start llm-gateway-redis` 恢复** —— 那只对「exited」
  状态有效。需要**重新拉起/重新 provision**。
- 在 252 上 `grep -rl 6389 /root /opt /etc/systemd/system /home` **无命中**，
  即容器定义（compose 文件 / systemd unit / 启动脚本）已不在这些路径下。
  恢复所需的编排定义来源需要运维侧提供。

运行时背景：252 的 `docker` 是 podman 兼容垫片
（`/var/run/docker.sock -> /run/podman/podman.sock`），无 `docker.service`，
`podman system service`（pid 224507，已跑约 7.2h）活着，其余容器
（pms-mysql / veritrans-* / netbird-*，conmon 存活约 79 天）都正常。
即**不是整机或运行时整体故障，是这一个容器消失了**。

附带异常：`podman ps` 挂起 >30s，而 `podman container exists` 3s 返回，
两者路径不一致，建议运维顺带查一下 podman 的 sqlite DB 锁 / storage 状态
（`/var/lib/containers/storage`）。

### 处置口径（未执行）

1. **立即**：运维确认 `llm-gateway-redis` 的编排定义来源并重新拉起，
   端口仍需映射到 **6389**（网关两台的 `LLM_GATEWAY_REDIS_ADDR` 指向
   `172.16.2.210:6389`）。
2. **起来即自愈**：Redis 一恢复，两台 sanitize 随即可用，`ready` 回 true，
   无需重部署（与 15:40 PG 恢复后一致）。
3. **顺带**：查 `podman ps` 挂起与容器为何被移除（是否有清理任务/定时器误删）。

> 本会话未对 252 做任何写操作：重建共享生产 Redis 容器涉及持久化数据
> 与业务连续性，属运维决策。

## 十七、Redis 恢复自愈 + 端到端采样补完（16:38–16:48）

### 事故收尾：自愈成立，无需重部署

第十六节的 Redis 事故在 16:38 复核时已恢复：

```
Redis 172.16.2.210:6389   TCP OK
245:8781  bd638a7a-2515  ready:true
154:8782  bd638a7a-2516  ready:true
```

两台**未做任何重部署即自愈**，与 15:40 PG 恢复后的表现一致。这条经验值得固化：
Redis / PG 任一恢复后不需要重跑 deploy，只需要确认 `ready` 回来即可。

### 端到端采样（公网入口，4 个提示词）

```
prompt              deltas  first  first-repr                  NL?  prefix
说三个字               19     18  '<think>The user is'          no   THINK
用一句话介绍你自己         9      9  '<think>用户'                no   THINK
解释一下什么是索引       33     29  '<think>\nThe user is as'    \n   THINK
你好                  25     10  '<think>The'                 no   THINK
```

两个硬结论：

1. **`<think>` 前缀率 4/4 = 100%**（raw_data 全量口径 87.6%，小样本全中属正常）。
2. **首帧长度 18 / 9 / 29 / 10，全部多字符。** 网关在任何一次采样中都没有
   产出过单字符首帧——与第十二节 0.18% 的大样本结论一致。

附带观察：`解释一下什么是索引` 的首帧是 `<think>\nThe user is as`，
**`<think>` 之后紧跟换行**。模型在推理块开头会插入换行；客户端若按行渲染，
回复一开头就会被打断成两段。这是「第一个请求回复看起来不对」的一个具体放大器，
但仍不是「单字一行」——后者需要客户端侧逐字渲染才能成立。

### 收口

至此可观测链路全部取过证，且结论一致：

| 环节 | 证据 | 结论 |
|---|---|---|
| 模型 | minimax-m3 1013/1145 带 `<think>`；glm-5.2 0/111 | 模型独有行为 |
| 网关 | `minimax[>[` 与 `<function_calls>` 均 0%，`<think>` 79.8% | 清理器在跑，缺 `<think>` 一项 |
| nginx | `proxy_buffering off`；SSE 不在 `gzip_types` | 不改写分帧 |
| 客户端 | 首帧 9~29 字符，无单字 | 渲染层行为，网关侧无法解释 |

**「单字一行」不是网关产生的字节形状。** 要坐实最后一环，仍需客户端侧
截图或渲染逻辑；而「第一个请求回复异常」已可完全归因于 `<think>` 透传。

## 十八、用户裁决：客户端会自动折叠思考 → 网关不改（17:00）

**用户答复**：*"客户端会自动折叠思考，请继续下一步。"*

据此，第十四/十五节提出的两条修法**都不执行**：

- ❌ 不在 `cleanMinimaxLeakFields` 补 `<think>` 清理 —— 客户端靠这个块渲染
  折叠 UI，网关清掉会让该 UI 失效，**属于净损失**。
- ❌ 不要求客户端改造 —— 客户端行为已符合预期。

因此 `<think>` 原样透传**确认为既定设计而非缺陷**。第十四节末尾的疑问
（"客户端是否自行折叠"）就此关闭，结论落定为：
**网关侧无需任何改动；观测到的"第一个请求回复异常"观感问题，
由客户端折叠 UI 承担。**

同时需要接受的后果：minimax-m3 的推理内容会持续出现在网络层，
`<think>` 块中的内容对下游可见。若日后对推理内容有脱敏要求，
应在 sanitize 层做**内容级**处理，而不是靠删除整个块来"顺便"达到。

### "下一步"：修复生产 seq 回退

当前生产仍落后 main：

```
245  bd638a7a-2515  ready:true
154  bd638a7a-2516  ready:true
历史已部署：2518-e3e2b4ef（245）/ 2519-a0fdaaa5（154）
```

`bd638a7a` 已随 `3346008d8` 并入 main，故部署 main 是**前进**而非分叉；
seq 回到单调（floor 已由并行会话推到 2522，下一次部署为 2523/2524）。

## 十九、更正：bash 5.2 其实一直在本机（17:12）

**第十一节的前提是错的。** 该节写「本机只有 macOS 自带的 bash 3.2.57，
`/opt/homebrew/bin/bash` 与 `/usr/local/bin/bash` 均不存在」，
并据此断定 deploy-lib SSOT 的 bash 4+ 语法无法满足、只能绕开。

错在**只查了两个 Homebrew 路径就外推成「本机没有 bash 4+」**。本机实际装有：

```
/Users/niuer/sdk/bash5/bin/bash → GNU bash 5.2.0(1) aarch64-apple-darwin25.2.0
```

（与 Go 同在 `~/sdk/` 下——之前找 Go 时只看了 `~/sdk/go`，没往旁边看。）

实测**未经任何修改的 SSOT** 在 bash 5.2 下直接可用：

```
host_root_for 245   -> /opt/llm-gateway-go
_ssh_key_for 245    -> /Users/niuer/.ssh/id_ed25519
_ssh_host_for 245   -> root@8.136.114.245
target_unsupported  -> {"target":"bogus-host", ... "ssh_key_env":"SSH_KEY_BOGUS-HOST" ...}
bash5 + UNMODIFIED SSOT: OK
```

即 `${target^^}`、`declare -A` 等全部原生支持。**第十一节那套「拷到 /tmp 逐处改写」
的绕行方案是不必要的**，正确做法一直是：

```bash
export PATH="$HOME/sdk/bash5/bin:$HOME/sdk/go/bin:$PATH"
export AIAN_DEPLOY_LIB="$HOME/workspace/ai-native-tools/deploy-lib"   # 原版 SSOT
bash scripts/deploy-154.sh
```

旁证：并行会话（worktree `llm-gateway-go-cursor`）一直是这么跑的——
`export PATH=$HOME/sdk/bash5/bin:...`，且 `AIAN_DEPLOY_LIB` 指向**原版 SSOT**，
从未打过补丁。

### 仍然有效的部分

第十一节里这几条结论与 bash 版本无关，不受本次更正影响：

1. **`558953c52`（deploy-245/154 空 ARGS unbound）仍是真 bug。**
   `deploy-245.sh:44` 的守卫确实漏了末尾 `exec` 行，154 入口也确实没有守卫——
   这是代码缺陷，与本机装什么 bash 无关。
2. **「只在 node_modules 缺失时装依赖」是真 bug**（`2bf5b3870`），
   已导致 245 部署因 `TS2307: Cannot find module 'qrcode'` 中止。
3. deploy-lib 里 `declare -A` / `${var^^}` 的写法在**只有 bash 3.2 的机器**上
   确实跑不了——只是这台机器并不是那种机器。

### 教训

「某依赖不存在」只能由**穷举搜索**得出，不能由「两个常见路径没找到」外推。
`~/sdk/` 这种非标准前缀目录是本次漏查的直接原因。

---

## 二十、审计 pass：代码复核结论（2026-10-10 23:30）

按用户要求对本次全部工作做一次独立审计。基线 `1d127e38e`，先并入
`origin/main` 的 6 个提交（mobile 前端批次，无冲突），再逐项复核。

### 20.1 静态检查

| 检查 | 命令 | 结果 |
|---|---|---|
| 编译 | `go build ./...` | **rc=0**，零错误 |
| 静态分析 | `go vet ./...` | **零告警** |
| 格式 | `gofmt -l .` | 376 个文件未格式化 |

gofmt 的 376 个是**上游长期存量**，与本次工作无关：逐个核对了本会话
六个提交（`7dd0b4b67` / `2bf5b3870` / `558953c52` / `d44a29f77` / `e3e2b4ef`
及报告提交）涉及的 `.go` 文件，**全部已格式化**。按既定口径不顺手格式化无关文件——
那会把一次审计变成一次巨型 diff，且会在共享仓库里制造冲突面。

工具链：本机 Go 在**非标准路径** `~/sdk/go/bin/go`（1.27.1 darwin/arm64），
不在 `PATH` 上。这与第十九节记录的 `~/sdk/bash5` 是同一类问题——
`AGENTS.md` 里「本机无 Go/brew」的旧结论同样应以 `~/sdk/` 为准重新核对。

### 20.2 测试

```
go test ./security/... ./domains/streaming/...
```

| 包 | 结果 |
|---|---|
| `security/sanitize` | ok 5.2s |
| `security/armor` / `guardian` / `sensitive` | ok |
| `domains/streaming` | ok 167.4s |
| `domains/streaming/executors` | ok 50.1s |
| `.../webcookie` / `integrity` / `state` | ok |

**9 包全绿，rc=0。**

### 20.3 第九之二节的缺陷复核（重点）

审计前把「完整占位符 fail-open 透传」列为唯一待查缺陷。结论：**当前 HEAD 不复现**。

方法是写探针直接打 `RestoreOutputOrMask`，用一张**不含目标键**的映射表
模拟 TTL 过期 / 租户哈希不匹配，覆盖 9 种拼写变体：

| 输入 | 输出 | 判定 |
|---|---|---|
| `{SENSITIVE:secret:21}`（精确） | `[REDACTED:secret]` | fail-closed |
| `{SENSITIVE:server_ip:01}`（补零索引） | `[REDACTED:server_ip]` | fail-closed |
| `{SENSITIVE:secret}`（缺索引） | `[REDACTED]` | fail-closed |
| `SENSITIVE:secret:21`（去括号） | `[REDACTED]` | fail-closed |
| `{SENSITIVE:SECRET:21}`（大写类型） | `[REDACTED]` | fail-closed |
| `{ＳＥＮＳＩＴＩＶＥ：ｓｅｃｒｅｔ：２１}`（全宽） | `[REDACTED]` | fail-closed |
| `{SENSITIVE: server_ip : 1}`（加空格） | `[REDACTED]` | fail-closed |
| `{SENSITIVE:server-ip:1}`（连字类型） | `[REDACTED]` | fail-closed |
| `{sensitive:server_ip:1}`（小写 marker） | `[REDACTED]` | fail-closed |

**零条透传**；同时正向验证还原未回归——`{SENSITIVE:phone:1}` 在表内时
正确还原为 `13900004444`。

代码层面的依据（不依赖探针，独立成立）：

- 所有客户端可见路径都走 `RestoreOutputOrMask`，policy 为 `looseMaskUnknown`；
- 唯一保留字面量的入口是 `RestoreOutput`（`looseKeepUnknown`），
  `grep` 确认它在**生产代码中零调用点**，只被测试引用；
- 流式路径同样 fail-closed：`sse_restore.go:110` 要么经 `maskUnmappedStreamMarkers`
  打码后放行、要么整帧拦截（`return nil, false, true`），不存在第三条出口。

因此第九之二节的两项修法建议**已由既有提交提前覆盖**，本节当时未定位到成因，
是因为观察窗口的构建早于 `ce5e35ab3`（16:31，遮蔽标记带类型）。
「成因未确认」这个表述本身也已被排除——不存在需要定位的成因。

**审计动作**：改的是报告，不是代码。代码不需要改。

### 20.4 仓库卫生

| 检查 | 结果 |
|---|---|
| 工作区 | 干净 |
| 暂存区 | 空 |
| 未跟踪文件 | 0（审计探针已移出仓库） |
| 已跟踪的 AppleDouble（`._*`）残留 | 0 |

第十一节记录的 `._audio_billing.go` tar 传输污染，确认已彻底清除。

### 20.5 审计未覆盖的项（明确留白，不当作已验证）

1. **未重跑 `go test ./...` 全量**。`verify.sh:51` 在 `set -e` 下无条件全量测试，
   而 `admin/request_logs_bodies_retirement_gate_test.go` 里的
   `TestV1BodiesReadersAreAssessed` 是**故意红的负向闸门**（判据：登记表覆盖全部
   bodies 读方）。按既定维护策略不修、不加白名单，因此全量门长期为红是预期行为，
   本次审计只跑了 sanitize / streaming 相关的 9 包子集。
2. **未做真库回归**。需要 docker + Postgres + Redis，本机不具备，
   且依赖安装属用户侧决策（`AGENTS.md`）。生产侧的实际状态由两轮成功部署的
   `/healthz` `ready:true` 佐证，而非本地测试。
3. **未验证 Redis 容器被移除的成因**（第十六之二节）。两台网关均已自愈，
   成因属环境侧，待定。
4. **未审计 `web-mobile` 前端**。`origin/main` 新增的 6 个提交是 mobile 批次，
   本次审计只覆盖其 Go 侧编译与 vet 通过，未跑 `npm test` / `vitest`。
