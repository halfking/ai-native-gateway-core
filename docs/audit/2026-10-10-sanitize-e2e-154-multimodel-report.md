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

**脱敏与还原的主链路是正常的**：入向替换为占位符、出向不含明文、跨轮与流式都能把原文还原回客户端、无敏感信息不被误伤。

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

影响：
- **不影响**脱敏与还原——还原走 Redis 热缓存，实测正常
- **影响**的是审计可追溯性：占位符→原文的映射没有加密留痕，事后无法从 DB 还原某个占位符当时对应什么值
- 因此 `session_censors` 不能按「DB 为权威」判可逆——这是 41-R71 已写明的验收口径

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

出向明文取证：84 行 `outbound_body`，67 个合法占位符，**剥除占位符后残留 0 行**；
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

泄漏速率前后对比（按每千行归一化，避免窗口长度不同造成误读）：

| 窗口 | 行数 | 裸形态泄漏 | 每千行 | 完整占位符泄漏 | 每千行 |
|---|---|---|---|---|---|
| 部署前（06:00–11:13） | 4658 | 34 | 7.3 | 86 | 18.5 |
| 部署后（11:13– ） | 218 | 1 | **4.6** | 4 | **18.3** |

裸形态泄漏下降，五处修复生效；完整占位符泄漏**基本持平**，见下节。

---

## 九之二、遗留缺陷：完整占位符 fail-open 透传（本次未修）

部署后取证发现一个**先前未记录的缺陷**，不属于本次五处修复，且速率前后持平
（18.5 → 18.3 每千行），**不是本次发布引入的回归**。

### 现象

`{SENSITIVE:server_ip:1}` 这类**带合法索引**的完整占位符，未能还原、也未打码，
而是**原样作为字面文本进入客户端响应**。部署后窗口内 4 条，全部来自同一个真实用户会话
`gw_bab6a75e…` / minimax-m3。

泄漏的是标记语法、类型与索引，**不泄漏敏感值本身**。

### 根因：缺 `LLM_GATEWAY_SESSION_CENSOR_KEY`

该会话有 **3748 条映射行（其中 1763 条 `server_ip`），但 `original_encrypted` 无一非 NULL**。
全表同样如此：

| 分组 | 行数 | `original_encrypted IS NULL` |
|---|---|---|
| 历史 | 84584 | 84584 |
| 本轮 E2E | 157 | 157 |

映射行在、加密原值全空。还原路径于是变成：
找到映射行 → 解密 `original_encrypted` → 是 NULL → 解析不出 → **走 fail-open 分支把占位符原样透传**。

### 为什么之前没被发现

此前报告里「E2 占位符映射已落库（original_encrypted 加密存储）」这句话是**误导性的**——
它只断言了映射行存在。该列实际 100% 为 NULL，还原这一半在生产上**从未真正生效过**。
本轮 `LLM_GATEWAY_SESSION_CENSOR_KEY` 经用户决定暂不配置，故这不是回归，是既有状态的显形。

### 两条可选修法（均未执行，需用户裁决）

1. **配置密钥**：写入 `LLM_GATEWAY_SESSION_CENSOR_KEY` 后重启，映射行才会有可解密的原值，
   还原恢复正常。属用户侧密钥决策。
2. **改 fail-open 为 fail-closed**：对「有合法索引但解析不出值」的占位符改为打码而非透传。
   这是代码改动，需另行发布。风险：会让部分当前以字面量展示的占位符变成 `[REDACTED]`，
   需评估是否有下游消费方依赖该字面量。

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
