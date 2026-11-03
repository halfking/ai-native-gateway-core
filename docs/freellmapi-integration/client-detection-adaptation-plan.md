# freellmapi × llm-gateway-go：客户端识别与适配收敛方案

> **适用链路**：客户端智能体 → `llm-gateway-go` → `freellmapi`（作为上游供应商）
>
> **文档定位**：只描述现状、差异与收敛方案；不含任何代码改动。
>
> **证据约定**：本文所有事实性断言都带 `文件路径:行号`。凡是**没有实际运行验证**的推断，一律显式标注「未验证」。本文不含任何真实密钥、令牌、密码。

---

## 0. 结论摘要

1. **两边现在各识别各的，但驱动的东西完全不同。** 网关的 client-type 直接驱动**参数级行为**（原厂直通）、凭据配额、FpSlot 持有者、DLP 放行记录、会话身份、遥测标签；freellmapi 的 `classifyClientAgent` **不驱动任何请求行为**，只写日志和看板（`server/src/lib/client-classifier.ts:45` 的唯一消费者是 `server/src/lib/client-context.ts:48`，其 `agent` 字段只被 `server/src/lib/request-log.ts:128` 等 6 处日志 INSERT 读取）。**因此本方案的核心不是"让两边对账"，而是"把识别收敛成一次，并让识别结果可被下游消费"。**

2. **实测 74 条 UA 样本，两边只有 13 条得到同一规范值，46 条真分歧。** 其中：网关识别出而 freellmapi 识别不出的 20 条、freellmapi 识别出而网关识别不出的 23 条、两边都识别出但**规范值不同**的 3 条（`RooCode/*`、`roo-code/*` → `roocode` vs `roo`；`MiMo-Code/0.4.0 (opencode)` → `opencode` vs `mimo-code`）。详见 §3.6。

3. **规范值集合只有 9 个交集。** 网关 23 个具体值、freellmapi 26 个具体值，交集 9 个（`claude-code`/`codex`/`opencode`/`qwen-code`/`pi`/`zcode`/`cursor`/`zed`/`jetbrains`），并集 40 个具体值 + `unknown`。网关独有 14 个、freellmapi 独有 17 个。

4. **网关内部有 4 处按 client-type 手工维护的词表，其中 3 处已实际漂移。** ① `telemetry/request_metadata.go:504-584` 的第二张 UA 表**漏掉 `intellij`/`pycharm`/`webstorm`/`visual-studio-code`**——实测 UA `IntelliJ IDEA/2024.1` 在 `clienttype` 侧是 `jetbrains`、在 telemetry 侧是 `unknown`；② `domains/credentialquota/normalize.go:14-18` 的闭集**漏掉 10 个已登记的规范值**（`pi`/`codebuddy`/`trae`/`comate`/`qwen-code`/`kimi-cli`/`iflow`/`raccoon`/`iflycode`/`lingma`），实测这些客户端在凭据配额维度全部塌成 `unknown`；③ `domains/analysis/sessionmeta/extractor_rules.go:199-214` 的 `agentRole` 闭集**缺 `pi`**（且其中的 `cline`/`aider`/`continue`/`kiro` 是死条目，被 `Normalize` 提前塌掉）。第 4 处 `routingopt/enhancer.go:125-142` 是另一套独立的会话模式词表（`ide`/`cli`/`web`/`api`），语义不同但同样陈旧。

5. **判定顺序差异有一处会直接导致误判**：freellmapi 把 `x-session-id` 视作 Claude Code 证据（`server/src/lib/client-classifier.ts:111`），网关完全不读这个头（`internal/clienttype/clienttype.go:115-216` 无此分支）；反过来 freellmapi 的 `pi` 规则锚定在 `^pi/\d`（`server/src/lib/client-classifier.ts:86`），网关用双边界 token（`internal/clienttype/clienttype.go:181`），覆盖范围不同。

6. **"client profile"这个词在两个仓里有 3 种不同含义**，合流时极易踩坑：网关的 `client_profiles` 表（行为画像，`sql/migrations/domain/132_client_profile.sql:1`）、网关的 `X-Client-Profile` 头（指纹维度，`domains/identity/hook.go:88-93`）、freellmapi 的 `client_profiles` 表（**API key** 绑定的 system prompt，`server/src/routes/client-profiles.ts:1-12`）。三者互不相干，**不应合并**。

7. **方案方向：网关持有权威客户端目录，freellmapi 消费。** 理由见 §5.2。目录内容取两边**并集**（freellmapi 独有的 17 个客户端必须补进网关目录，否则网关对它们永远 `unknown`）。

8. **收敛机制**：网关在请求边界识别一次，把结果以**服务端注入**的出站头 `X-Gw-Detected-Client` 透传给 freellmapi，freellmapi 优先消费、本地分类器降级为 fallback。网关当前**不向任何上游透传入站头**（`upstream/client.go:513-543` 显式构造全部头），所以这是一处新增而非改造。

9. **落地拆 4 个可独立验收的阶段**，其中阶段 1、2 **完全不改运行时判定**，只建立契约与门禁（§5.5）。

10. **本次不做**：不改协议转换/路由算法、不合并三套 "client profile"、不删任一侧现有识别实现、不做跨仓 CI 编排、不承诺生产 UA 覆盖率（本次未采样生产流量）。完整清单见 §5.8。

---

## 1. 现状：两边的识别机制各自是怎么工作的

### 1.1 网关侧：5 条识别路径，共享 1 个规范值闭集

#### (1) 规范值闭集与别名表 —— `internal/clienttype/clienttype.go`

- `Normalize(value string) string`（`:55-92`）：
  - 白名单（直接返回）：`cursor`/`claude-code`/`opencode`/`zcode`/`codex`/`roocode`/`vscode`/`copilot`/`windsurf`/`zed`/`jetbrains`/`pi`/`minimax-code`/`deepseek-code`/`codebuddy`/`trae`/`comate`/`qwen-code`/`kimi-cli`/`iflow`/`raccoon`/`iflycode`/`lingma`/`unknown`（`:58-70`）。
  - 别名折叠：`zulu`→`comate`（`:76-78`）、`code-buddy`→`codebuddy`（`:79-80`）、`trae-cli`/`trae-agent`→`trae`（`:81-82`）、`kimi`→`kimi-cli`（`:83-84`）、`iflow-cli`→`iflow`（`:85-86`）、`qwen-cli`→`qwen-code`（`:87-88`）。
  - 白名单外一律 `unknown`（`:89-91`）。**归一化是"闭集收敛"，不是"大小写归一"**。
- `Unknown = "unknown"`（`:8`）。

#### (2) 匹配器 —— 双边界 token

`HasToken(haystack, token)`（`:24-41`）：命中要求 token 前后**紧邻字符不是小写字母或数字**（`isAlnumLower`，`:43-45`）。因此 `/`、`-`、空格、字符串首尾都是边界，字母数字延续不是边界。调用方负责先 `ToLower`（`:127`、`:497`）。

> 设计意图见 `:16-23` 的注释：`MiniMaxCoder/1.0` 含 `minimaxcode`、`ZCodex/2.0` 含 `zcode`（会遮蔽 codex 分支）、`visualized/2.0` 含 `zed`——裸 `strings.Contains` 会误判。

#### (3) header/UA 主链 —— `DetectFromRequest(r *http.Request) string`（`:115-216`）

判定顺序（注释在 `:106-114`）：

| 顺序 | 信号 | 位置 | 命中结果 |
|---|---|---|---|
| 1 | `X-Gw-Client-Type` 头 | `:120-122` | `Normalize(ct)`（**客户端可控**，但受白名单约束） |
| 2 | UA token（`HasToken` 双边界） | `:127-207` | 见下表 |
| 3 | `Anthropic-Beta` 含 `claude-code-` | `:212-214` | `claude-code` |
| — | 都不命中 | `:215` | 返回 **`""`（空串）**，兜底由调用方决定 |

UA token 表（按代码中 `switch` 的先后顺序）：

| 顺序 | token | 规范值 | 行号 |
|---|---|---|---|
| 1 | `cursor` | `cursor` | `:129-130` |
| 2 | `claude-code` | `claude-code` | `:131-132` |
| 3 | `claude-cli` | `claude-code` | `:139-140` |
| 4 | `opencode` | `opencode` | `:141-142` |
| 5 | `zcode` | `zcode` | `:143-144` |
| 6 | `codex` | `codex` | `:145-146` |
| 7 | `roocode` / `roo-code` | `roocode` | `:147-148` |
| 8 | `vscode` / `visual-studio-code` | `vscode` | `:149-150` |
| 9 | `github-copilot` / `copilot` | `copilot` | `:151-152` |
| 10 | `windsurf` | `windsurf` | `:153-154` |
| 11 | `zed` | `zed` | `:155-156` |
| 12 | `jetbrains` / `intellij` / `pycharm` / `webstorm` | `jetbrains` | `:157-159` |
| 13 | `minimax-code` / `minimaxcode` | `minimax-code` | `:164-165` |
| 14 | `deepseek-code` / `deepseek-ide` / `deepseek-cli` | `deepseek-code` | `:170-172` |
| 15 | `pi` | `pi` | `:181-182` |
| 16 | `codebuddy` / `code-buddy` | `codebuddy` | `:189-190` |
| 17 | `trae-cli` / `trae-agent` / `trae` | `trae` | `:191-192` |
| 18 | `zulu` / `comate` | `comate` | `:193-194` |
| 19 | `qwen-code` / `qwen-cli` | `qwen-code` | `:195-196` |
| 20 | `kimi-cli` / `kimi` | `kimi-cli` | `:197-198` |
| 21 | `iflow-cli` / `iflow` | `iflow` | `:199-200` |
| 22 | `raccoon` | `raccoon` | `:201-202` |
| 23 | `iflycode` | `iflycode` | `:203-204` |
| 24 | `lingma` | `lingma` | `:205-206` |

> **注意 `zcode` 排在 `codex` 之前**（`:143` vs `:145`），注释明确说明这是为了避免 `ZCodex/2.0` 命中 `zcode` 后遮蔽 `codex`（`:123-126`）。`HasToken` 的双边界使 `ZCodex/2.0` 两个分支都不命中（实测见 §6.2）。

#### (4) body 级识别 —— `telemetry/body_client_marker.go`

`ExtractClientTypeFromBody(body []byte, xCodeSessionID string) string`（`:35-105`），判定规则（注释 `:23-34`）：

- `metadata.zcode_version` 存在 → `zcode`（`:59-61`）
- `metadata.client_type ∈ {minimax_code, minimax-code, minimaxcode}` → `minimax-code`（`:63-68`）
- `metadata.deepseek_session_id` 存在 → `deepseek-code`（`:70-72`）
- `tools[].function.name` 以 `zcode_` 开头 → `zcode`（`:135-137`）
- `model` 以 `deepseek-` 开头 **且** tools 含 `{read_file,write_file,edit_file,search_files,execute_command}` → `deepseek-code`（`:82-95`、`:123-140`）。**两者共现才判**，注释 `:76-81` 明确说明"模型选择 ≠ 客户端身份"。
- `X-Code-Session-Id` 头存在 → `minimax-code`（`:100-102`）

#### (5) system-prompt 语义识别 —— `telemetry/request_metadata.go`

`DetectAgentFromSystemPrompt(systemPrompt string) string`（`:234-249`）：把 prompt 小写后按**注册顺序**做 `strings.Contains` 匹配，命中即返回注册名。

注册表 `defaultAgentPatterns()`（`:34-187`）的取值域（`:52-185`）：
`zcode`、`opencode`、`codex`、`claude-code`、`roocode`、`windsurf`、`zed`、`copilot`、`cline`、`aider`、`continue`、`kiro`、`cursor`、`vscode`、`minimax-code`、`deepseek-code`、`codebuddy`、`trae`、`comate`、`qwen-code`、`kimi-cli`、`iflow`、`raccoon`、`iflycode`、`lingma`、`claude`（裸兜底，`:185`）。

> **这套词表里有 5 个值不在 `clienttype.Normalize` 白名单里**：`cline`、`aider`、`continue`、`kiro`、`claude`。下游若把该结果过 `Normalize`，这 5 个会全部塌成 `unknown`（实测见 §3.7 / §6.2）。

#### (6) 遥测侧的**第二张 UA 表** —— `telemetry/request_metadata.go:482-610`

`ExtractAgentNameFromRequest(r, body)`：

1. `X-Agent-Name` 头 → `normalizeAgentName()`（`:485-488`、`:441-457`）：去空白、剥控制字符、截断 255 rune，**不过白名单**（`:439-440` 明确说明"白名单会把自定义 agent 名全部塌缩成 unknown"）。
2. UA 匹配 → 独立 `switch` 表（`:504-584`），**不是** `clienttype.DetectFromRequest`。它额外含 5 个弱名：`postman`/`insomnia`/`python`→`python-client`/`go-http-client`→`go-client`/`curl`（`:572-583`），未命中时返回 `unknown`（`:582-583`）。
3. body marker（仅当 UA 结果是弱名或缺失时）：`:591-599`，覆盖策略见 `domains/streaming/request_log_pipeline.go:658-666`、`:747-757`。
4. 都无 → 返回 UA 弱名或 `unknown`（`:601-609`）。

#### 调用链上的两份薄委托（不是独立表）

- `domains/streaming/client_fingerprint.go:22-30` → `clienttype.DetectFromRequest(r)`
- `domains/streaming/executors/executor.go:1088-1096` → `clienttype.DetectFromRequest(r)`

> ⚠️ 任务描述中「`executors/executor.go` 镜像副本」的说法**已过时**：2026-10-29 识别轮已把两份独立表合并为一份唯一实现 + 两处薄委托（`internal/clienttype/clienttype.go:94-104` 的注释自陈此事）。**但仍残留两处副本**：① `clientTokenOf` 在 `domains/streaming/executors/executor.go:1106-1111` 与 `domains/streaming/client_fingerprint.go:63-68` 各一份，注释自承"两份逻辑需同步演进"（`executor.go:1104-1105`）；② 见 §3.7 的 3 份闭集。

### 1.2 freellmapi 侧：1 条识别路径，1 张正则表

`server/src/lib/client-classifier.ts`：

- 枚举 `CLIENT_AGENTS`（`:3-31`），共 **26 个具体值 + `unknown`**；类型 `ClientAgent = (typeof CLIENT_AGENTS)[number]`（`:33`）。
- 头部取值 `header(req, name)`（`:35-38`）：取第一个值并 `toLowerCase`，缺失返回 `''`。
- 路径取值：`(req.originalUrl ?? req.url ?? '').split('?')[0].toLowerCase()`，`tokenizedPath` 去掉 `/v1/t/<token>` 前缀（`:46-47`）。

判定顺序：

| 顺序 | 信号 | 匹配方式 | 规范值 | 行号 |
|---|---|---|---|---|
| 1 | `x-claude-code-session-id` 头存在 | 存在性 | `claude-code` | `:51` |
| 2 | `x-zcode-app-version` 存在 **或** `x-title` 以 `z code` 开头 | 存在性 / 前缀 | `zcode` | `:56` |
| 3 | UA `/claude[- ]?code\|\bclaude-cli\b/` | 正则 | `claude-code` | `:62` |
| 4 | UA `/\bcodex\b/` | 正则 | `codex` | `:63` |
| 5 | UA `/qwen[- ]?code\|\bqwen-cli\b/` | 正则 | `qwen-code` | `:64` |
| 6 | UA `/gemini[- /]?cli/` | 正则 | `gemini-cli` | `:65` |
| 7 | UA `/kilo[- ]?code\|\bkilocode\b/` | 正则 | `kilo-code` | `:66` |
| 8 | UA `/\bcrush(?:\/\|\s\|$)/` | 正则 | `crush` | `:67` |
| 9 | UA `/deepseek[- ]?harness\|\bdsh\//` | 正则 | `deepseek-harness` | `:69` |
| 10 | UA `/mimo[- ]?code\|\bmimo\//` | 正则 | `mimo-code` | `:72` |
| 11 | UA `/\batomcode\b/` | 正则 | `atomcode` | `:74` |
| 12 | UA `/\bopenclaw\b/` | 正则 | `openclaw` | `:78` |
| 13 | UA `/hermes[- ]?agent\|\bhermes-cli\b/` | 正则 | `hermes-agent` | `:82` |
| 14 | UA `/\bpi-coding-agent\b\|^pi\/\d/` | 正则 | `pi` | `:86` |
| 15 | UA `/^reasonix\//` | 正则 | `reasonix` | `:88` |
| 16 | UA `/\bzcode\b/` | 正则 | `zcode` | `:90` |
| 17 | UA `/opencode/` | 正则（**无边界**） | `opencode` | `:91` |
| 18 | UA `/\bcline\b/` | 正则 | `cline` | `:92` |
| 19 | UA `/\broo[- /]\|roo ?code/` | 正则 | `roo` | `:94` |
| 20 | UA `/continue(?:\.dev)?/` | 正则（**无边界**） | `continue` | `:95` |
| 21 | UA `/\baider\b/` | 正则 | `aider` | `:96` |
| 22 | UA `/\bgoose\b/` | 正则 | `goose` | `:97` |
| 23 | UA `/\bcursor\b/` | 正则 | `cursor` | `:98` |
| 24 | UA `/\bzed\b/` | 正则 | `zed` | `:99` |
| 25 | UA `/jetbrains\|intellij\|pycharm\|webstorm/` | 正则 | `jetbrains` | `:100` |
| 26 | UA `/\bollama\b/` | 正则 | `ollama-client` | `:101` |
| 27 | UA `/anthropic\|claude-sdk/` | 正则 | `anthropic-sdk` | `:102` |
| 28 | UA `/openai\|langchain\|litellm/` | 正则 | `openai-sdk` | `:103` |
| 29 | 路径 `/v1/responses` 或 `/responses` | 路径前缀 | `codex` | `:106` |
| 30 | 路径 `/v1beta/` | 路径前缀 | `gemini-cli` | `:107` |
| 31 | 路径 `/v1/messages` | 路径前缀 + `x-session-id` 存在性 | `claude-code` / `anthropic-sdk` | `:108-112` |
| 32 | 路径 `/api/chat` `/api/generate` `/api/tags` | 路径前缀 | `ollama-client` | `:113-115` |
| 33 | 兜底 | — | `unknown` | `:116` |

**没有归一化层、没有别名表、没有闭集校验**：每条规则直接返回字面量。因此 `X-Gw-Client-Type: kimi` 这样的别名在 freellmapi 侧不会被折叠成 `kimi-cli`（它根本没有 `kimi` 规则）。

### 1.3 识别结果**实际**驱动什么（对任务前提的一处修正）

> 任务描述假定 freellmapi 的分类结果驱动「路由决策、quirks/任务类型、工具能力、system prompt 处理、客户端档案」。**逐项核对源码后，这一假定不成立**——freellmapi 的这些能力全部按**别的维度**分叉，`classifyClientAgent` 的结果只进日志。

| freellmapi 的能力 | 实际分叉维度 | 证据 | 是否消费 client-agent |
|---|---|---|---|
| 路由决策 | 模型组 / `routingStrategy`（决策模型选的 preset） | `server/src/services/router.ts:767`（只读 `getClientContext().routingStrategy`；**全文无 `agent` 引用**） | ❌ |
| 任务类型 | `x-freellm-task-type` 头 + 请求形状 | `server/src/lib/task-type.ts:13-17`、`:67`；消费点 `server/src/routes/anthropic.ts:706`、`routes/proxy.ts:2536`、`routes/responses.ts:1217` | ❌ |
| quirks | 模型 × 平台（`quirk_targets` 表） | `server/src/services/quirks.ts:25-40`（SQL JOIN，**按 `model_glob`/`platform` 过滤**） | ❌ |
| 工具能力 | 模型 × 端点（tool 拒绝计数） | `server/src/lib/tool-capability.ts:1-21`（键是 `modelStatsKey(platform, modelId, endpointScope)`） | ❌ |
| system prompt | **API key**（统一密钥 vs `sk-cp-` 客户端档案密钥） | `server/src/lib/system-prompt.ts:11`、`:56-70`、`:79-82` | ❌ |
| 客户端档案 | **API key** 记录（`sk-cp-` 前缀，绑定 system prompt） | `server/src/routes/client-profiles.ts:1-12`、`:87-103` | ❌ |
| **请求日志 / 看板** | **client-agent** | `server/src/lib/request-log.ts:126-128`（`requests.client_agent`）、`services/search.ts:494-511`、`services/decision.ts:425-427`；前端 `client/src/pages/AgentsPage.tsx:82`、`client/src/pages/AnalyticsPage.tsx:196` | ✅ **唯一消费点** |

> **推论（对方案的影响）**：freellmapi 侧的"客户端适配"目前是**按 API key** 分叉的（`sk-cp-` 档案），不是按客户端身份分叉的。所以本次收敛的**真实收益**在网关侧（消除 3 份漂移闭集 + 让网关能认识 freellmapi 独有的 17 个客户端），以及**为 freellmapi 未来按客户端分叉能力**准备好可信输入。freellmapi 侧本期只需要"能消费网关的判定结果 + 记录来源"，不必改行为。

### 1.4 术语冲突：两边各有 2~3 套"客户端身份"轴

| 轴 | 载体 | 取值域 | 谁提供 | 证据 |
|---|---|---|---|---|
| **client-type** | UA / `X-Gw-Client-Type` / body / prompt | 闭集 24 值 | 客户端**推断** | `internal/clienttype/clienttype.go:57-70` |
| **client profile（指纹维度）** | `X-Client-Profile` 头，缺省 `"default"` | 自由串 | 客户端**声明** | `domains/identity/hook.go:88-93`、`domains/identity/identity.go:158` |
| **client profile（行为画像）** | `client_profiles` 表，键 `identity_hash` | 行为统计 | 网关**聚合** | `sql/migrations/domain/132_client_profile.sql:1-35` |
| **client profile（模型可见性）** | `model_aliases.client_profiles` `text[]` | 自由串 | 运维**配置** | `provider/client.go:1382-1385`、`:1505-1507` |
| **agent name（遥测标签）** | `X-Agent-Name` 头 / UA / prompt | 自由串（截断 255） | 客户端**声明** | `telemetry/request_metadata.go:441-457`、`:485-488` |
| **client-agent（freellmapi）** | `classifyClientAgent` | 闭集 27 值 | freellmapi **推断** | `server/src/lib/client-classifier.ts:3-31` |
| **client profile（freellmapi）** | `sk-cp-` API key | 每条档案一行 | 运维**配置** | `server/src/lib/system-prompt.ts:11`、`:56-70` |

⚠️ **`X-Gw-Client-Type`、`X-Client-Profile`、`X-Agent-Name` 三个头都是客户端可控的**。网关当前对 `X-Gw-Client-Type` 走 `Normalize` 白名单（`internal/clienttype/clienttype.go:120-122`），对 `X-Agent-Name` 只做长度/字符清洗（`telemetry/request_metadata.go:441-457`），对 `X-Client-Profile` **完全不校验**（`domains/identity/hook.go:88-91` 直接返回）。后者还参与路由候选过滤（`provider/client.go:1384`）与 sticky key（`domains/streaming/handler.go:4471`）——这是既有事实，本文不改动它，但方案里透传头必须**服务端注入**。

---

## 2. 客户端清单对照表

> 「网关识别结果」列填**规范值**（`clienttype.Normalize` 的取值）；网关未命中时 `DetectFromRequest` 返回 `""`（空串），下表统一写作 `→ unknown` 并注明。
> 「freellmapi 识别结果」列填 `CLIENT_AGENTS` 枚举值；未命中即 `unknown`（是枚举内的合法值）。

| 客户端 | 网关规范值 | 网关依据 (file:line) | freellmapi 枚举值 | freellmapi 依据 (file:line) | 是否同名 | 差异与风险 |
|---|---|---|---|---|---|---|
| claude-code | `claude-code` | `internal/clienttype/clienttype.go:131-132`,`:139-140`,`:212-214` | `claude-code` | `server/src/lib/client-classifier.ts:51`,`:62` | ✅ 同名 | 判定信号不同：网关读 `Anthropic-Beta` 旗标，freellmapi 不读；freellmapi 读 `x-claude-code-session-id`，网关不读。UA `claude-cli/*` 两边一致 |
| codex | `codex` | `internal/clienttype/clienttype.go:145-146` | `codex` | `server/src/lib/client-classifier.ts:63`,`:106` | ✅ 同名 | 网关无 `/v1/responses` 路径兜底；UA `codex_cli_rs/0.1.0` 网关命中（`_` 是边界）、freellmapi **不命中**（`\b` 下 `_` 是词字符） |
| opencode | `opencode` | `internal/clienttype/clienttype.go:141-142` | `opencode` | `server/src/lib/client-classifier.ts:91` | ✅ 同名 | freellmapi 规则无边界（`/opencode/`）；网关用双边界 |
| cursor | `cursor` | `internal/clienttype/clienttype.go:129-130` | `cursor` | `server/src/lib/client-classifier.ts:98` | ✅ 同名 | 无实质差异 |
| copilot | `copilot` | `internal/clienttype/clienttype.go:151-152` | ❌ 无 | — | ❌ 仅网关 | freellmapi 侧 GitHub Copilot 全部记 `unknown` |
| windsurf | `windsurf` | `internal/clienttype/clienttype.go:153-154` | ❌ 无 | — | ❌ 仅网关 | 同上 |
| zed | `zed` | `internal/clienttype/clienttype.go:155-156` | `zed` | `server/src/lib/client-classifier.ts:99` | ✅ 同名 | 无实质差异 |
| jetbrains | `jetbrains` | `internal/clienttype/clienttype.go:157-159` | `jetbrains` | `server/src/lib/client-classifier.ts:100` | ✅ 同名 | **网关内部不一致**：telemetry 第二张表只有 `jetbrains` token，UA `IntelliJ IDEA/2024.1` 在 telemetry 侧是 `unknown`（实测） |
| gemini-cli | ❌ 无 | — | `gemini-cli` | `server/src/lib/client-classifier.ts:65`,`:107` | ❌ 仅 freellmapi | 网关侧 Gemini CLI 全程 `unknown` |
| cline | ❌ 无（UA 层） | — | `cline` | `server/src/lib/client-classifier.ts:92` | ❌ 仅 freellmapi | **网关 system-prompt 层能识别**（`telemetry/request_metadata.go:60`），但下游 `Normalize` 会把它塌成 `unknown`（实测） |
| roo | `roocode` | `internal/clienttype/clienttype.go:147-148` | `roo` | `server/src/lib/client-classifier.ts:94` | ❌ **同名不同值** | **风险 R1**：同一个客户端两个规范值，跨系统对账必错 |
| continue | ❌ 无 | — | `continue` | `server/src/lib/client-classifier.ts:95` | ❌ 仅 freellmapi | freellmapi 规则**无边界**（`/continue/`），散文型 UA 也会命中 |
| aider | ❌ 无 | — | `aider` | `server/src/lib/client-classifier.ts:96` | ❌ 仅 freellmapi | 网关 system-prompt 层有（`telemetry/request_metadata.go:61`），同样被 `Normalize` 塌掉 |
| goose | ❌ 无 | — | `goose` | `server/src/lib/client-classifier.ts:97` | ❌ 仅 freellmapi | — |
| qwen-code | `qwen-code` | `internal/clienttype/clienttype.go:195-196` | `qwen-code` | `server/src/lib/client-classifier.ts:64` | ✅ 同名 | 别名 `qwen-cli` 只在网关侧（`:87-88`、`:196`） |
| kilo-code | ❌ 无 | — | `kilo-code` | `server/src/lib/client-classifier.ts:66` | ❌ 仅 freellmapi | — |
| crush | ❌ 无 | — | `crush` | `server/src/lib/client-classifier.ts:67` | ❌ 仅 freellmapi | — |
| deepseek-code | `deepseek-code` | `internal/clienttype/clienttype.go:170-172` | ❌ 无 | — | ❌ 仅网关 | **风险 R2**：网关的 `deepseek-code` 与 freellmapi 的 `deepseek-harness` 是**不同产品**，两边各认一半，互不识别 |
| deepseek-harness | ❌ 无 | — | `deepseek-harness` | `server/src/lib/client-classifier.ts:69` | ❌ 仅 freellmapi | 同 R2 |
| mimo-code | ❌ 无 | — | `mimo-code` | `server/src/lib/client-classifier.ts:72` | ❌ 仅 freellmapi | **风险 R3**：UA `MiMo-Code/0.4.0 (opencode)` 网关判 `opencode`（误），freellmapi 判 `mimo-code`（对） |
| atomcode | ❌ 无 | — | `atomcode` | `server/src/lib/client-classifier.ts:74` | ❌ 仅 freellmapi | — |
| openclaw | ❌ 无 | — | `openclaw` | `server/src/lib/client-classifier.ts:78` | ❌ 仅 freellmapi | — |
| hermes-agent | ❌ 无 | — | `hermes-agent` | `server/src/lib/client-classifier.ts:82` | ❌ 仅 freellmapi | — |
| pi | `pi` | `internal/clienttype/clienttype.go:181-182` | `pi` | `server/src/lib/client-classifier.ts:86` | ✅ 同名 | **覆盖范围不同**：网关任意位置双边界 token；freellmapi 只认 `pi-coding-agent` 或**行首** `pi/\d`。UA `proxy pi/1.0` 网关命中、freellmapi 不命中 |
| reasonix | ❌ 无 | — | `reasonix` | `server/src/lib/client-classifier.ts:88` | ❌ 仅 freellmapi | — |
| zcode | `zcode` | `internal/clienttype/clienttype.go:143-144` | `zcode` | `server/src/lib/client-classifier.ts:56`,`:90` | ✅ 同名 | **信号差异**：freellmapi 读 `x-zcode-app-version`/`x-title`，网关只读 UA token。ZCode **桌面版** UA 为 `ai-sdk/provider-utils/…`（见 `server/src/__tests__/lib/client-classifier.test.ts:102`），网关侧不可识别 |
| minimax-code | `minimax-code` | `internal/clienttype/clienttype.go:164-165` | ❌ 无 | — | ❌ 仅网关 | 另有 body 级信号 `metadata.client_type`、`X-Code-Session-Id`（`telemetry/body_client_marker.go:63-68`,`:100-102`） |
| trae | `trae` | `internal/clienttype/clienttype.go:191-192` | ❌ 无 | — | ❌ 仅网关 | 别名 `trae-cli`/`trae-agent` 只在网关侧（`:81-82`） |
| comate | `comate` | `internal/clienttype/clienttype.go:193-194` | ❌ 无 | — | ❌ 仅网关 | 别名 `zulu` 只在网关侧（`:76-78`） |
| codebuddy | `codebuddy` | `internal/clienttype/clienttype.go:189-190` | ❌ 无 | — | ❌ 仅网关 | 别名 `code-buddy` 只在网关侧（`:79-80`） |
| kimi-cli | `kimi-cli` | `internal/clienttype/clienttype.go:197-198` | ❌ 无 | — | ❌ 仅网关 | 别名 `kimi` 只在网关侧（`:83-84`） |
| iflow | `iflow` | `internal/clienttype/clienttype.go:199-200` | ❌ 无 | — | ❌ 仅网关 | 别名 `iflow-cli` 只在网关侧（`:85-86`） |
| raccoon | `raccoon` | `internal/clienttype/clienttype.go:201-202` | ❌ 无 | — | ❌ 仅网关 | — |
| iflycode | `iflycode` | `internal/clienttype/clienttype.go:203-204` | ❌ 无 | — | ❌ 仅网关 | — |
| lingma | `lingma` | `internal/clienttype/clienttype.go:205-206` | ❌ 无 | — | ❌ 仅网关 | — |
| ollama-client | ❌ 无 | — | `ollama-client` | `server/src/lib/client-classifier.ts:101`,`:113-115` | ❌ 仅 freellmapi | 网关侧 Ollama 走 `/api/*` 面，不在 client-type 词表内 |
| openai-sdk | ❌ 无 | — | `openai-sdk` | `server/src/lib/client-classifier.ts:103` | ❌ 仅 freellmapi | **语义层级不同**：SDK 级，不是智能体。网关对应物是 telemetry 弱名 `python-client`/`go-client`/`curl`（`telemetry/request_metadata.go:576-581`） |
| anthropic-sdk | ❌ 无 | — | `anthropic-sdk` | `server/src/lib/client-classifier.ts:102` | ❌ 仅 freellmapi | 同上 |
| **兜底：unknown** | `unknown`（`DetectFromRequest` 未命中返回 `""`，经 `Normalize` 落 `unknown`） | `internal/clienttype/clienttype.go:215`、`:89-91`、`:8` | `unknown`（枚举内合法值） | `server/src/lib/client-classifier.ts:116`、`:30` | ✅ 同名 | **兜底语义不同**：网关 `""` 会继续落到 body marker / system prompt 兜底链；freellmapi 的 `unknown` 是终态 |
| （网关额外）`vscode` | `vscode` | `internal/clienttype/clienttype.go:149-150` | ❌ 无 | — | ❌ 仅网关 | 任务清单未列，但影响模型可见性维度 |
| （网关额外）`deepseek-harness` | ❌ 无（网关只有 `deepseek-code`） | — | `deepseek-harness` | `server/src/lib/client-classifier.ts:69` | ❌ 仅 freellmapi | 见 R2 |
| （网关 telemetry 弱名）`curl`/`python-client`/`go-client`/`postman`/`insomnia` | 弱名（**不在** client-type 闭集） | `telemetry/request_metadata.go:572-583` | 全部 → `unknown` | — | ❌ 仅网关 | 同一 curl 请求：网关 `agent_name="curl"`，freellmapi `client_agent="unknown"` |

### 2.1 集合关系摘要

| 集合 | 数量 | 明细 |
|---|---|---|
| 网关规范值（具体） | **23** | 由 `Normalize` 运行时探针反推（§6.2 ②），对应 `internal/clienttype/clienttype.go:58-68` |
| freellmapi 枚举（具体） | **26** | `server/src/lib/client-classifier.ts:4-29` |
| 交集 | **9** | `claude-code`、`codex`、`cursor`、`jetbrains`、`opencode`、`pi`、`qwen-code`、`zcode`、`zed` |
| 仅网关 | **14** | `codebuddy`、`comate`、`copilot`、`deepseek-code`、`iflow`、`iflycode`、`kimi-cli`、`lingma`、`minimax-code`、`raccoon`、`roocode`、`trae`、`vscode`、`windsurf` |
| 仅 freellmapi | **17** | `aider`、`anthropic-sdk`、`atomcode`、`cline`、`continue`、`crush`、`deepseek-harness`、`gemini-cli`、`goose`、`hermes-agent`、`kilo-code`、`mimo-code`、`ollama-client`、`openai-sdk`、`openclaw`、`reasonix`、`roo` |
| 并集（具体值） | **40** | + `unknown` = **41** 个唯一规范值 |

> 上表全部数字由 §6.2 ②⑨ 的**运行时探针**反推（`Normalize(x) == x` 即规范值），不是从源码手抄。
> 网关另有 **6 组共 7 条**别名映射（`zulu`→`comate`、`code-buddy`→`codebuddy`、`trae-cli`/`trae-agent`→`trae`、`kimi`→`kimi-cli`、`iflow-cli`→`iflow`、`qwen-cli`→`qwen-code`），见 `internal/clienttype/clienttype.go:76-88`。

---

## 3. 差异与风险清单

> 每条差异都有运行时证据或精确的 `file:line`。未跑运行时验证的条目在 §6.3 单独列出。

### 3.1 同名不同值（canonical 值不一致）

| 编号 | 差异 | 网关 | freellmapi | 证据 |
|---|---|---|---|---|
| **R1** | Roo Code | `roocode` | `roo` | 网关 `internal/clienttype/clienttype.go:147-148`；freellmapi `server/src/lib/client-classifier.ts:94`。实测 UA `RooCode/3.50.4`、`roo-code/3.50.4` 两边均不同名 |
| **R2** | DeepSeek 系 | `deepseek-code` | `deepseek-harness` | 网关 `internal/clienttype/clienttype.go:170-172`；freellmapi `server/src/lib/client-classifier.ts:69`。**这是两个不同产品**（DeepSeek Code 客户端 vs DeepSeek Harness），两边各认一半：UA `DeepSeek-Code/1.0` → 网关 `deepseek-code` / freellmapi `unknown`；UA `deepseek-harness/0.3.1` → 网关 `unknown` / freellmapi `deepseek-harness` |
| **R3** | MiMo Code | `opencode`（误判） | `mimo-code` | 网关**无 mimo 规则**，`MiMo-Code/0.4.0 (opencode)` 命中 `opencode`（`internal/clienttype/clienttype.go:141-142`）；freellmapi 显式把 mimo 放在 opencode 之前（`server/src/lib/client-classifier.ts:72` vs `:91`，注释 `:70-71` 说明原因）。实测两者结论不同 |

**影响**：跨系统按 `client_type` 对账（成本归集、配额统计、看板）必然对不上；R1 更严重——同一个客户端在两段链路里是**两个不同的能力等级标识**。

### 3.2 一方能识别、另一方识别不了（漏识别 → 被当 `unknown` 处理）

- **网关独有 14 项**：`roocode`、`vscode`、`copilot`、`windsurf`、`minimax-code`、`deepseek-code`、`codebuddy`、`trae`、`comate`、`kimi-cli`、`iflow`、`raccoon`、`iflycode`、`lingma`。
- **freellmapi 独有 17 项**：`cline`、`roo`、`continue`、`aider`、`goose`、`kilo-code`、`crush`、`deepseek-harness`、`mimo-code`、`atomcode`、`openclaw`、`hermes-agent`、`reasonix`、`gemini-cli`、`ollama-client`、`openai-sdk`、`anthropic-sdk`。

**后果（网关侧，漏识别 = `unknown`）**：

1. **原厂直通失效**：`internal/nativepair/nativepair.go:64-73` 只登记了 `zcode`、`minimax-code`、`deepseek-code`、`claude-code` 四个客户端。其它客户端即使是对自己厂商的模型也不走直通——这是设计如此，但一旦某客户端被误判成 `unknown`，直通必然不触发。
2. **凭据配额维度塌陷**：`domains/credentialquota/normalize.go:20-29` 把不在其闭集内的值归 `unknown`，于是这些客户端**无法配置独立的 per-client 配额**（实测见 §3.7）。
3. **会话身份错误**：`domains/analysis/sessionmeta/extractor_rules.go:37-47` 的 `extractClient` 产出 `ClientIdentity{Type, Source, Confidence}`，落库见 `domains/session/v2/family_writers.go:60`。
4. **FpSlot 持有者合并**：holder = `userKey\|clientType`（`domains/streaming/client_fingerprint.go:63-68`），`unknown` 的客户端共用同一持有者槽位，互相挤占 24h Pin。

**后果（freellmapi 侧，漏识别 = `unknown`）**：只影响日志与看板（§1.3），当前无行为后果。

### 3.3 判定顺序 / 判定信号差异

| 编号 | 差异 | 证据 | 影响 |
|---|---|---|---|
| **D1** | freellmapi 把 `x-session-id` 当 Claude Code 证据（仅 `/v1/messages` 路径）；网关**完全不读** `x-session-id` 和 `x-claude-code-session-id` | freellmapi `server/src/lib/client-classifier.ts:111`；网关 `internal/clienttype/clienttype.go:115-216` 无此分支 | 同一个 Claude Code 请求：freellmapi 判 `claude-code`，网关可能判 `unknown` |
| **D2** | freellmapi 读 `x-zcode-app-version` / `x-title: Z Code@…`（最高优先级）；网关只读 UA token `zcode` | freellmapi `server/src/lib/client-classifier.ts:56`（注释 `:52-55` 说明必须早于 `/v1/messages` 分支）；网关 `internal/clienttype/clienttype.go:143-144` | **ZCode 桌面版不可识别**：其 UA 为 `ai-sdk/provider-utils/4.0.39 runtime/node.js/24`（见 `server/src/__tests__/lib/client-classifier.test.ts:102`），网关侧无 `zcode` token → `unknown`（**未验证**：本轮未对该 UA 单独跑网关探针，但按 `:143-144` 的 token 判定可判定） |
| **D3** | `pi` 的覆盖范围 | 网关 `HasToken(ua,"pi")`（`internal/clienttype/clienttype.go:181`），任意位置、双边界；freellmapi `/\bpi-coding-agent\b\|^pi\/\d/`（`server/src/lib/client-classifier.ts:86`），锚定行首且要求数字版本 | UA `proxy pi/1.0`：网关 `pi` / freellmapi `unknown`（实测） |
| **D4** | wire-format（路径）兜底只有 freellmapi 有 | freellmapi `server/src/lib/client-classifier.ts:106-115`；网关无路径兜底（协议由路由决定，见 `domains/streaming/responses.go`、`domains/streaming/messages.go`） | 无 UA 的 `/v1/responses` 请求：freellmapi 判 `codex`，网关侧没有"客户端=codex"这个概念（该请求走 Responses 协议处理） |
| **D5** | 边界定义不同：网关双边界（字母数字延续被拒），freellmapi 用 `\b`（下划线是词字符） | 网关 `internal/clienttype/clienttype.go:34-35`、`:43-45`；freellmapi `server/src/lib/client-classifier.ts:63` | UA `codex_cli_rs/0.1.0`：网关 `codex` / freellmapi `unknown`（实测） |
| **D6** | freellmapi 有两条**无边界**裸匹配：`/opencode/`（`:91`）、`/continue/`（`:95`） | 同左 | UA 含 `continue` 的散文型 UA 会被判成编码智能体；网关无此规则 |

### 3.4 别名 / 归一化覆盖差异

| 编号 | 差异 | 证据 |
|---|---|---|
| **A1** | 网关有 alias→canonical 表（**6 组共 7 条**：`zulu`→`comate`、`code-buddy`→`codebuddy`、`trae-cli`/`trae-agent`→`trae`、`kimi`→`kimi-cli`、`iflow-cli`→`iflow`、`qwen-cli`→`qwen-code`）；**freellmapi 没有任何等价物**（每条规则直接返回字面量） | 网关 `internal/clienttype/clienttype.go:76-88`；freellmapi `server/src/lib/client-classifier.ts:45-117` |
| **A2** | 网关 UA 层也做别名收敛（`zulu`\|`comate`、`kimi-cli`\|`kimi`、`trae-cli`\|`trae-agent`\|`trae`、`iflow-cli`\|`iflow`、`qwen-code`\|`qwen-cli`、`codebuddy`\|`code-buddy`）；freellmapi 对 CN 厂商这一族**完全没有规则** | 网关 `internal/clienttype/clienttype.go:189-206`；freellmapi 无 |
| **A3** | 网关 `Normalize` 是**闭集收敛**（白名单外 → `unknown`）；freellmapi 没有闭集概念 | 网关 `internal/clienttype/clienttype.go:55-92`；freellmapi `server/src/lib/client-classifier.ts:3-31` |

**后果**：若阶段 3 让网关把识别结果透传给 freellmapi，freellmapi 必须**先按契约做归一化**再落库，否则 `X-Gw-Detected-Client: kimi` 与 `kimi-cli` 会变成两个不同的看板维度。

### 3.5 误判面（短词边界）

| 编号 | 客户端 | 网关行为 | freellmapi 行为 | 证据 |
|---|---|---|---|---|
| **E1** | `pi` | 双边界；`pip/24.0`、`api-client/2.0` 拒绝（`internal/clienttype/clienttype_test.go:99-101`）；**`raspberry-pi/1.0` 会命中**（连字符是边界）——网关自己在 `internal/clienttype/clienttype.go:96-98` 把它登记为"已知假阳性" | 只认 `pi-coding-agent` 或 `^pi/\d`，**不会**命中 `raspberry-pi` | 网关 `internal/clienttype/clienttype.go:173-182`；freellmapi `server/src/lib/client-classifier.ts:86` |
| **E2** | `zed` | `visualized/2.0` 拒绝（`internal/clienttype/clienttype_test.go:106`） | `\bzed\b` 同样拒绝 | 同左 |
| **E3** | `continue` | 无此规则 | `/continue(?:\.dev)?/` **无边界**，散文型 UA 也会命中 | `server/src/lib/client-classifier.ts:95` |
| **E4** | `crush` | 无此规则 | `/\bcrush(?:\/\|\s\|$)/` 较严 | `server/src/lib/client-classifier.ts:67` |
| **E5** | SDK 级 | 无此规则；telemetry 侧只有 5 个弱名（`curl`/`python-client`/`go-client`/`postman`/`insomnia`） | `/anthropic\|claude-sdk/`→`anthropic-sdk`、`/openai\|langchain\|litellm/`→`openai-sdk` | freellmapi `server/src/lib/client-classifier.ts:102-103`；网关 `telemetry/request_metadata.go:572-583` |

> **结论**：两边对短词的**保守程度相反**——网关更保守（双边界）但仍有已登记的假阳性面（`raspberry-pi`）；freellmapi 在 `pi`/`zed` 上很保守，却在 `opencode`/`continue` 上很激进。合并时不能简单取其一。

### 3.6 同一 UA 在两边落到不同枚举（实测全表）

> 方法：用 `go test -overlay` 注入只读探针跑 `clienttype.DetectFromRequest`；用 `node --experimental-strip-types` 跑 freellmapi 的**逐字节副本**（sha256 一致，见 §6.2）。74 条 UA 样本，两侧各跑一次后逐条比对。

**统计**：74 条可比 UA → **13 条同值** / **15 条两边都未识别出具体客户端**（网关 `""` vs freellmapi `unknown`）/ **46 条真分歧**。

46 条真分歧的构成：

- 网关识别出、freellmapi 未识别：**20 条**
- freellmapi 识别出、网关未识别：**23 条**
- 两边都识别出但规范值不同：**3 条**（`RooCode/3.50.4`、`roo-code/3.50.4`、`MiMo-Code/0.4.0 (opencode)`）

<details>
<summary>展开全部 74 条实测对照（点击展开）</summary>

| UA | 网关 | freellmapi | 类别 |
|---|---|---|---|
| `claude-cli/2.1.75 (external, cli)` | `claude-code` | `claude-code` | 同 |
| `claude-code/1.0` | `claude-code` | `claude-code` | 同 |
| `opencode/1.2.3` | `opencode` | `opencode` | 同 |
| `pi-coding-agent` | `pi` | `pi` | 同 |
| `pi/1.4.0 (linux; node/v22.23.4; x64)` | `pi` | `pi` | 同 |
| `ZCode/3.16.3 ai-sdk/provider-utils/4.0.39 runtime/node.js/24` | `zcode` | `zcode` | 同 |
| `zcode/3.16.3` | `zcode` | `zcode` | 同 |
| `QwenCode/0.5 qwen-code` | `qwen-code` | `qwen-code` | 同 |
| `qwen-code/0.5` | `qwen-code` | `qwen-code` | 同 |
| `cursor/0.42` | `cursor` | `cursor` | 同 |
| `zed/0.1.0` | `zed` | `zed` | 同 |
| `IntelliJ IDEA/2024.1` | `jetbrains` | `jetbrains` | 同 |
| `some proxy zcode/1.0` | `zcode` | `zcode` | 同 |
| `pip/24.0` | （未识别） | `unknown` | 同（都未识别） |
| `reasonix-web-fetch/1.0` | （未识别） | `unknown` | 同（都未识别） |
| `zcodex/2.0` | （未识别） | `unknown` | 同（都未识别） |
| `MiniMaxCoder/1.0` | （未识别） | `unknown` | 同（都未识别） |
| `visualized/2.0` | （未识别） | `unknown` | 同（都未识别） |
| `Mozilla/5.0` | （未识别） | `unknown` | 同（都未识别） |
| `Trajectory/1.0` | （未识别） | `unknown` | 同（都未识别） |
| `KimiK2/1.0` | （未识别） | `unknown` | 同（都未识别） |
| `my-api-client/1.0` | （未识别） | `unknown` | 同（都未识别） |
| `curl/8.14.1` | （未识别） | `unknown` | 同（都未识别）\* |
| `python-requests/2.31` | （未识别） | `unknown` | 同（都未识别）\* |
| `Go-http-client/1.1` | （未识别） | `unknown` | 同（都未识别）\* |
| `PostmanRuntime/7.36` | （未识别） | `unknown` | 同（都未识别）\* |
| `insomnia/2024.1` | （未识别） | `unknown` | 同（都未识别）\* |
| （空 UA） | （未识别） | `unknown` | 同（都未识别） |
| **`codex_cli_rs/0.1.0`** | `codex` | `unknown` | **真分歧**（D5） |
| **`OpenAI/JS 6.10.0`** | （未识别） | `openai-sdk` | **真分歧** |
| **`openai-python/1.0`** | （未识别） | `openai-sdk` | **真分歧** |
| **`RooCode/3.50.4`** | `roocode` | `roo` | **真分歧**（R1） |
| **`roo-code/3.50.4`** | `roocode` | `roo` | **真分歧**（R1） |
| **`roo/1.0`** | （未识别） | `roo` | **真分歧** |
| **`Kilo-Code/4.2`** | （未识别） | `kilo-code` | **真分歧** |
| **`crush/0.8`** | （未识别） | `crush` | **真分歧** |
| **`deepseek-harness/0.3.1 (+https://github.com/deepseek-ai/deepseek-harness)`** | （未识别） | `deepseek-harness` | **真分歧**（R2） |
| **`dsh/0.3.1`** | （未识别） | `deepseek-harness` | **真分歧**（R2） |
| **`DeepSeek-Code/1.0`** | `deepseek-code` | `unknown` | **真分歧**（R2） |
| **`deepseek-ide/1.0`** | `deepseek-code` | `unknown` | **真分歧**（R2） |
| **`mimo/0.4.0`** | （未识别） | `mimo-code` | **真分歧** |
| **`MiMo-Code/0.4.0 (opencode)`** | `opencode` | `mimo-code` | **真分歧**（R3） |
| **`atomcode/5.0.9`** | （未识别） | `atomcode` | **真分歧** |
| **`openclaw`** | （未识别） | `openclaw` | **真分歧** |
| **`openclaw/2026.9.4`** | （未识别） | `openclaw` | **真分歧** |
| **`hermes-agent`** | （未识别） | `hermes-agent` | **真分歧** |
| **`hermes-cli/0.21.3`** | （未识别） | `hermes-agent` | **真分歧** |
| **`Reasonix/2.29.0`** | （未识别） | `reasonix` | **真分歧** |
| **`MiniMaxCode/1.0`** | `minimax-code` | `unknown` | **真分歧** |
| **`minimax-code/1.0.0`** | `minimax-code` | `unknown` | **真分歧** |
| **`GeminiCLI/2.1.0`** | （未识别） | `gemini-cli` | **真分歧** |
| **`gemini-cli/0.1.0`** | （未识别） | `gemini-cli` | **真分歧** |
| **`cline/3.1.0`** | （未识别） | `cline` | **真分歧** |
| **`aider/0.80`** | （未识别） | `aider` | **真分歧** |
| **`continue/1.0.0`** | （未识别） | `continue` | **真分歧**（E3） |
| **`goose/1.0`** | （未识别） | `goose` | **真分歧** |
| **`Copilot/1.2.3`** | `copilot` | `unknown` | **真分歧** |
| **`github-copilot/1.2.3`** | `copilot` | `unknown` | **真分歧** |
| **`Windsurf/1.2.3`** | `windsurf` | `unknown` | **真分歧** |
| **`ollama/0.3.0`** | （未识别） | `ollama-client` | **真分歧** |
| **`anthropic-sdk/0.30.0`** | （未识别） | `anthropic-sdk` | **真分歧** |
| **`claude-sdk/1.0`** | （未识别） | `anthropic-sdk` | **真分歧** |
| **`trae-cli/1.0`** | `trae` | `unknown` | **真分歧** |
| **`kimi-cli/1.0`** | `kimi-cli` | `unknown` | **真分歧** |
| **`kimi/1.0`** | `kimi-cli` | `unknown` | **真分歧** |
| **`comate/1.0`** | `comate` | `unknown` | **真分歧** |
| **`zulu/1.0`** | `comate` | `unknown` | **真分歧** |
| **`iflow/1.0`** | `iflow` | `unknown` | **真分歧** |
| **`codebuddy/1.0`** | `codebuddy` | `unknown` | **真分歧** |
| **`raccoon/1.0`** | `raccoon` | `unknown` | **真分歧** |
| **`iflycode/1.0`** | `iflycode` | `unknown` | **真分歧** |
| **`lingma/1.0`** | `lingma` | `unknown` | **真分歧** |
| **`vscode/1.90`** | `vscode` | `unknown` | **真分歧** |
| **`Trae/1.0`** | `trae` | `unknown` | **真分歧** |

\* 网关的 `DetectFromRequest` 不含弱名，但 telemetry 侧另有弱名表：`curl/8.14.1`→`curl`、`python-requests/2.31`→`python-client`、`Go-http-client/1.1`→`go-client`、`insomnia/2024.1`→`insomnia`、`PostmanRuntime/7.36`→`unknown`（`PostmanRuntime` 的 `postman` 后面接字母 `R`，双边界拒绝）。见 `telemetry/request_metadata.go:572-583`。

</details>

### 3.7 重复实现与**已经发生**的漂移（网关内部）

> freellmapi 侧**没有同类副本**——`classifyClientAgent` 只有一份实现（`server/src/lib/client-classifier.ts:45`），唯一消费入口是 `server/src/lib/client-context.ts:48`。这是 freellmapi 侧的一项优势。

| 编号 | 副本位置 | 状态 | 证据 |
|---|---|---|---|
| **F1** | `telemetry/request_metadata.go:504-584`（第二张 UA 表） | **已漂移**：缺 `intellij`/`pycharm`/`webstorm`/`visual-studio-code` 四个 token。UA `IntelliJ IDEA/2024.1` → `clienttype` 侧 `jetbrains`、telemetry 侧 `unknown`（**实测**）。注释自称"one vocabulary, two call sites; both must move together"（`:550-552`）但无强制机制 | `internal/clienttype/clienttype.go:157-159` vs `telemetry/request_metadata.go:528-529` |
| **F2** | `domains/credentialquota/normalize.go:14-18`（第三份闭集） | **已漂移 10 项**：`pi`/`codebuddy`/`trae`/`comate`/`qwen-code`/`kimi-cli`/`iflow`/`raccoon`/`iflycode`/`lingma` 全部塌成 `unknown`（**实测**）。注释自承上一次漂移（`:11-13`："minimax-code/deepseek-code were added to clienttype.Normalize (ce85e767a) but not mirrored here"） | 同左 |
| **F3** | `domains/analysis/sessionmeta/extractor_rules.go:199-214`（`agentRole` 闭集） | **缺 `pi`** → `pi` 落 `other_agent`（默认分支 `:212-214`）。且列表里的 `cline`/`aider`/`continue`/`kiro` 是**死条目**：名字先经 `normalizeAgent`（`:222-229`）→ `clienttype.Normalize` 塌成 `unknown`，`agentRole` 再也匹配不上（**实测**：system prompt `"You are Cline, a coding agent."` → `agent="unknown" role="unknown"`） | 同左 |
| **F4** | `routingopt/enhancer.go:125-142`（`detectSessionMode` 闭集） | **语义不同的第 4 套词表**，且同样陈旧：闭集只有 `cursor`/`claude-code`/`windsurf`/`vscode`/`vs-code`/`jetbrains`/`intellij`/`pycharm`/`webstorm`/`goland`/`copilot`/`roocode` 算 `ide`；**`codex`/`opencode`/`zcode`/`pi`/`minimax-code`/`qwen-code` 等全部落到 `api`**（`:136-139` "未知 ClientType：按 api 处理"）。且调用点 `:83` 传入空 UA，UA 兜底路径实际是死的 | 同左 |
| **F5** | `domains/streaming/executors/executor.go:1106-1111` 的 `clientTokenOf` | 与 `domains/streaming/client_fingerprint.go:63-68` 各一份；注释自承"两份逻辑需同步演进"（`:1104-1105`） | 同左 |

> **F1~F4 共同的可执行结论**：只要 client-type 的词表在网关内有 4 处手写副本（3 处是 client-type 的镜像、1 处是语义不同的会话模式词表），任何"新增一个客户端"的动作都必须改 4 处，而其中 3 处历史上已漏改。**这正是"单一事实源"必须落地的直接证据。**

### 3.8 兜底值差异

| 编号 | 差异 | 网关 | freellmapi |
|---|---|---|---|
| **G1** | 未命中时的返回值 | `""`（`internal/clienttype/clienttype.go:215`），**可继续兜底**：body marker（`domains/streaming/executors/executor_chat.go:66-79`）→ system prompt（`domains/streaming/client_fingerprint.go:43-48`）→ `Normalize("")`→`unknown`（`internal/clienttype/clienttype.go:89-91`） | `unknown`（`server/src/lib/client-classifier.ts:116`），**终态**，无后续兜底 |
| **G2** | 弱名粒度 | 5 个弱名：`curl`/`python-client`/`go-client`/`postman`/`insomnia`（`telemetry/request_metadata.go:572-583`） | 全部 `unknown` |
| **G3** | 未识别是否可恢复 | 可（弱名可被 body marker 覆盖，`domains/streaming/request_log_pipeline.go:658-666`） | 不可 |

### 3.9 风险登记表

| 编号 | 风险 | 类别 | 影响 | 严重度 |
|---|---|---|---|---|
| R1 | Roo Code 两个规范值 | 同名不同值 | 跨系统对账必错；能力等级标识分裂 | 高 |
| R2 | DeepSeek Code / Harness 互不识别 | 一方能识别另一方不能 | 两边各漏一半 DeepSeek 系流量 | 高 |
| R3 | MiMo Code 被判为 opencode | 判定顺序 | 网关侧能力判定错误 | 中 |
| D1/D2 | `x-session-id` / `x-zcode-*` 头网关不读 | 判定顺序 | Claude Code / ZCode 桌面版在网关侧不可识别 | 高 |
| D3/D5 | `pi` 覆盖范围、`\b` vs 双边界 | 判定顺序 | 同一 UA 两边结论不同 | 中 |
| A1/A2 | 别名表只在网关侧 | 别名覆盖 | 透传后 freellmapi 侧无法归一 | 中 |
| E1/E3 | `raspberry-pi` 假阳性 / `continue` 裸匹配 | 误判面 | 短词边界风险 | 中 |
| F1~F4 | 网关 4 份手写闭集已漂移 3 处 | 重复实现 | 新增客户端必然漏改；配额/会话/会话模式维度错误 | **高** |
| G1/G2 | 兜底语义与粒度不同 | 兜底值 | 同一 SDK 请求在两边归类差异 | 中 |
| §1.4 | 三套 "client profile" 同名不同义 | 术语冲突 | 合流时的命名陷阱 | 中 |

---

## 4. 适配行为的分叉点

### 4.1 网关侧：识别值直接驱动行为

| 下游 | 机制 | 证据 | client-type 缺失/错误的代价 |
|---|---|---|---|
| **原厂直通（参数级行为）** | `nativepair.Match(clientType, catalogCode, outboundModel)`；已登记 4 对：`zcode`↔glm、`minimax-code`↔minimax、`deepseek-code`↔deepseek、`claude-code`↔claude | `internal/nativepair/nativepair.go:64-73`、`:91-114`；调用点 `domains/streaming/executors/executor_chat.go:56-79` | 判错 ⇒ 直通给了不该给的端点 ⇒ 厂商参数不被认 ⇒ 4xx 剥参重试白费往返。`internal/nativepair/nativepair.go:10-20` 记录了 nvidia 托管 glm 的真实事故（53 请求 ×600KB ×2 次往返，TTFB 29s+） |
| **凭据配额 / 并发** | 策略键 `(credential_id, client_type)`；`normalizeClientType` 先过 `clienttype.Normalize` 再过本地闭集 | `domains/credentialquota/normalize.go:20-29`、`:31-41`；`domains/credentialquota/service.go:68`、`:106`、`:149` | 值落在闭集外 ⇒ 全部塌 `unknown` ⇒ **该客户端无法配置独立配额**，且与真正的 `unknown` 流量混计 |
| **FpSlot 持有者 / Pin 复用** | holder = `userKey\|clientType`；`userKey` 空→`anon`，`clientType` 空→`unknown` | `domains/streaming/client_fingerprint.go:50-68`；`domains/streaming/executors/executor.go:2715-2727` | 同一个用户在不同客户端下的槽位隔离依赖 client-type 正确；塌成 `unknown` 后互相挤占 |
| **Prometheus 标签** | `client_type` 出现在 4 个指标的标签里 | `credentialfpslot/metrics.go:143-176`、`:357`（`clienttype.Normalize`） | 标签基数与分布失真 |
| **会话身份** | `ClientIdentity{Type, Protocol, Source, Confidence}`；落 `client_type` 列 | `domains/analysis/sessionmeta/extractor_rules.go:37-47`；`domains/session/v2/family_writers.go:60`；`domains/session/v2/session_aggregator.go:224` | 会话统计分类错误 |
| **会话角色** | `agentRole(name)` → `coding_agent`/`ide_client`/`other_agent`/`unknown` | `domains/analysis/sessionmeta/extractor_rules.go:199-214` | 缺 `pi` ⇒ `pi` 落 `other_agent`（实测 F3） |
| **DLP 放行记录的客户端维度** | `client_type` 记入放行键与审核队列 | `domains/sanitizerules/block_review_persist.go:18`、`egress_release.go:58`、`postgres_block_review.go:38` | **放行键取租户**，client_type 只用于展示与追溯 ⇒ 影响有限（`:18` 明确说明） |
| **连接注册表** | `RegistrationMetadata.ClientType`（注释称"internal/clienttype normalized client kind"） | `domains/streaming/connection_registry.go:143-146`、`:154` | 运维视图分类错 |
| **路由候选过滤（间接）** | `model_aliases.client_profiles` 数组按 `profile` 过滤候选 | `provider/client.go:1382-1385`、`:1505-1507` | **注意：这里的 `profile` 是 `X-Client-Profile` 指纹维度，不是 client-type**（`domains/streaming/handler.go:3741`、`:4049`）——两者不可混为一谈 |
| **遥测 agent_name** | `X-Agent-Name` > UA 表 > body marker；弱名可被 body marker 覆盖 | `telemetry/request_metadata.go:482-610`；`domains/streaming/request_log_pipeline.go:747-757`、`:658-666` | 统计口径与 FpSlot 的 client-type **不同源** |
| **会话模式（路由优化）** | `detectSessionMode(clientType, UA)` → `ide`/`cli`/`web`/`api` | `routingopt/enhancer.go:125-142`、`:83`、`:158-192` | 见 F4：`codex`/`opencode`/`zcode`/`pi` 等一律 `api` |

> **未按 client-type 分叉的（明确排除，避免误写）**：凭据熔断（`domains/credential/` 无 client-type 引用）、定价回退（按模型别名与 `client_profiles` 数组，不按 client-type）、协议转换（按入站协议）。

### 4.2 freellmapi 侧：识别值**不**驱动行为

见 §1.3 的逐项核对表。结论：freellmapi 的"按客户端适配"实际是**按 API key 适配**（`sk-cp-` 档案注入 system prompt，`server/src/lib/system-prompt.ts:56-70`、`:79-82`），而不是按客户端身份适配。`classifyClientAgent` 的唯一产出是日志维度。

> 这带来一个**正面结论**：合流时 freellmapi 侧**不需要**把 client-agent 接到任何行为路径上，本期只需"消费 + 记录来源"，风险面极小。

### 4.3 「同一个客户端在两段链路里被当作不同能力等级」的后果

在链路上，客户端 → 网关 → freellmapi，请求只走一次，但两侧各判一次：

1. **同一个 Roo Code 用户**：网关认为 `roocode`（进 FpSlot 的 `roocode` 桶、配额 `roocode` 桶、会话角色 `coding_agent`），freellmapi 认为是 `roo`（看板维度）。若未来 freellmapi 按 client-agent 给模型可见性，两边会给出**不同的模型集合**。
2. **同一个 Gemini CLI 用户**：网关全程 `unknown` ⇒ 走不到 `nativepair` 直通、拿不到 per-client 配额、会话角色落 `other_agent`；freellmapi 判 `gemini-cli`。网关侧的"能力等级"显著低于实际。
3. **同一个 Claude Code 用户（通过 `x-session-id` 而非 UA 暴露身份）**：freellmapi 判 `claude-code`（`server/src/lib/client-classifier.ts:111`），网关判 `unknown` ⇒ **网关不会给它 `claude-code`↔`anthropic` 原厂直通**（`internal/nativepair/nativepair.go:72`）。这是本文最具体的一条能力等级落差：**该用户拿不到首发全参数透传**，会走 paramguard + 剥参重试。
4. **同一个 ZCode 桌面版用户**：freellmapi 判 `zcode`（靠 `x-zcode-app-version`/`x-title`，`server/src/lib/client-classifier.ts:56`），网关判 `unknown` ⇒ **拿不到 zcode↔glm 原厂直通**（`internal/nativepair/nativepair.go:65`）。同样落到 paramguard 路径。
5. **同一个 Pi 用户**：网关 `pi` 会进 FpSlot 的 `pi` 桶，但 `domains/credentialquota/normalize.go:14-18` 的闭集没有 `pi` ⇒ 配额维度塌成 `unknown`（实测）。而 `domains/analysis/sessionmeta/extractor_rules.go:199-214` 也没有 `pi` ⇒ 角色落 `other_agent`。**同一个客户端在网关内部三个维度上已经是三种等级。**

---

## 5. 优化方案

### 5.1 设计原则

1. **识别只做一次**：链路上的权威判定点唯一，位置在网关的请求边界。
2. **判定结果随请求透传**：下游不重新猜，靠显式字段消费。
3. **规范值只有一个词表**：全链路（含 Prometheus 标签、DB 列、看板维度）共用一份。
4. **未知必须可恢复**：兜底不是终态，允许被更强的信号覆盖。
5. **不合并语义不同的轴**：三套 "client profile" 保持分离（§1.4）。
6. **契约先落地、行为后切换**：先建立"改一处、两边都红"的门禁，再改行为。

### 5.2 单一事实源：网关持有权威目录

**结论：权威客户端目录由网关仓（`llm-gateway-go`）持有，内容取两边并集；freellmapi 作为消费方。**

**理由（为什么不是反过来）**：

1. **权威必须落在"做决策的那一侧"。** 网关的 client-type 直接驱动参数级行为（原厂直通）、配额、限流、会话身份；freellmapi 的 client-agent 目前不驱动任何行为（§1.3）。让观测侧的分类器当权威，等于用"没被行为检验过"的判定去控制行为。
2. **工程资产在网关侧。** 网关已有：唯一实现 + 双边界匹配器（`internal/clienttype/clienttype.go:24-41`）、闭集 + 别名表（`:55-92`）、负臂测试（`internal/clienttype/clienttype_test.go:72-123`）、三层兜底链（header→UA→body→prompt，`docs/client-formats/README.md:82-102`）、审计档案（`docs/audit/CRITICAL-AUDIT-2026-10-29-identification-round.md`）。freellmapi 侧只有一张正则表，**没有归一化层、没有负臂测试、没有闭集校验**。
3. **发布形态匹配。** 契约用 JSON 单文件；网关是 Go（`go:generate` 生成 Go 代码），freellmapi 是 TS/ESM（`node --experimental-strip-types` 已验证可跑无依赖的 `.ts`，见 §6.2），两侧都能零依赖消费同一份 JSON。
4. **反例（为什么不能"谁在链路上游谁说了算"的朴素推导）**：freellmapi 独有的 17 个客户端**必须**进网关目录，否则网关对它们永远是 `unknown`（§3.2）。所以"权威在网关"≠"内容来自网关"——**权威决定编辑流程，内容取并集**。

**消费方式（建议：构建期生成 + 运行时接口，二者并存）**：

| 消费方 | 方式 | 说明 |
|---|---|---|
| 网关自身 | **构建期生成** | `go:generate` 从契约生成 `internal/clienttype/generated_catalog.go`（规范值集合、别名表、UA token 表、优先级顺序）。运行时零 IO、零解析开销 |
| freellmapi | **构建期生成**（首选）+ 运行时兜底 | 生成 `server/src/lib/generated/client-catalog.ts`；同时保留一个**运行时接口** `/api/client-catalog`（或静态 JSON 路由）供 freellmapi 在无法重新构建时拉取——本期不做，仅在契约里预留字段 |
| 两侧的**判定向量** | **同一份 golden 文件** | `contracts/client-vectors.v1.json`，两侧测试都读它（§5.6） |

> **不选"运行时接口"作为唯一方式**的原因：网关是 Go 热路径，判定在每个请求的边界执行；每次拉远程 JSON 会引入网络依赖与缓存一致性问题。**不选"共享包/子模块"**的原因：两个仓的发布节奏、依赖管理和语言栈不同（Go module vs npm workspace），子模块会引入版本锁定成本。

### 5.3 契约的形状

契约文件（建议路径 `llm-gateway-go/contracts/client-catalog.v1.json`）——**每一项都能落到一条可测试的断言**：

```jsonc
{
  "version": "client-catalog/v1",
  "fallback": "unknown",

  // 规范值闭集：全链路唯一词表（Prometheus 标签 / DB 列 / 看板维度共用）
  "canonical": [
    "claude-code", "codex", "opencode", "qwen-code", "pi", "zcode", "cursor",
    "zed", "jetbrains",                                     // 9 个交集
    "roocode", "vscode", "copilot", "windsurf", "minimax-code",
    "deepseek-code", "codebuddy", "trae", "comate", "kimi-cli",
    "iflow", "raccoon", "iflycode", "lingma",                // 14 个网关独有
    "cline", "roo", "continue", "aider", "goose", "kilo-code", "crush",
    "deepseek-harness", "mimo-code", "atomcode", "openclaw",
    "hermes-agent", "reasonix", "gemini-cli", "ollama-client",
    "openai-sdk", "anthropic-sdk",                           // 17 个 freellmapi 独有
    "unknown"
  ],

  // 别名表：alias -> canonical（合并网关 6 条 + freellmapi 的等价值）
  "aliases": {
    "zulu": "comate", "code-buddy": "codebuddy",
    "trae-cli": "trae", "trae-agent": "trae",
    "kimi": "kimi-cli", "iflow-cli": "iflow", "qwen-cli": "qwen-code",
    "roo": "roocode",                 // ← 需要裁决（见下）
    "dsh": "deepseek-harness",
    "gemini-cli": "gemini-cli"
  },

  // 判定规则：有序列表，priority 小的先匹配；每条都可单独测试
  "rules": [
    { "id": "gw-header",   "priority": 10, "signal": "header",
      "name": "x-gw-detected-client", "match": "exact_alias", "trust": "server" },
    { "id": "cc-session",  "priority": 20, "signal": "header",
      "name": "x-claude-code-session-id", "match": "presence", "value": "claude-code" },
    { "id": "zcode-headers","priority": 25, "signal": "header",
      "name": "x-zcode-app-version", "match": "presence", "value": "zcode" },
    { "id": "zcode-title",  "priority": 26, "signal": "header",
      "name": "x-title", "match": "prefix", "prefix": "z code", "value": "zcode" },
    { "id": "ua-token",     "priority": 30, "signal": "user_agent",
      "match": "dual_boundary_token", "tokens": ["cursor", "claude-code", "claude-cli", "..."] },
    { "id": "ua-path",      "priority": 40, "signal": "user_agent",
      "match": "regex", "pattern": "^reasonix/", "value": "reasonix" },
    { "id": "body-marker",  "priority": 50, "signal": "body", "match": "json_path", "...": "..." },
    { "id": "system-prompt","priority": 60, "signal": "system_prompt", "match": "substring", "...": "..." },
    { "id": "wire-format",  "priority": 70, "signal": "path", "match": "prefix", "...": "..." },
    { "id": "fallback",     "priority": 99, "signal": "none", "value": "unknown" }
  ],

  // 弱名（SDK / HTTP 库级，非智能体）：与 canonical 分层，避免和智能体混在一张标签池
  "weak": ["curl", "python-client", "go-client", "postman", "insomnia"],

  // 判定向量（golden）：两侧测试共用
  "vectors": [
    { "ua": "RooCode/3.50.4", "expect": "roocode" },
    { "ua": "MiMo-Code/0.4.0 (opencode)", "expect": "mimo-code" },
    { "ua": "codex_cli_rs/0.1.0", "expect": "codex" }
  ]
}
```

**每项的可测试规则**：

| 契约项 | 对应的可测试断言 |
|---|---|
| `canonical` | 「生成的规范值集合 == 契约集合」（集合相等 **且** 顺序相等） |
| `aliases` | 「`Normalize(alias) == canonical[alias]`」逐条；且「`Normalize` 对非白名单值返回 `fallback`」 |
| `rules` 顺序 | 「按 `priority` 升序，对每条向量，第一个命中的规则给出的值 == `expect`」 |
| `weak` 分层 | 「弱名不进 `canonical`，但不被 `unknown` 吞掉」（两层断言） |
| `fallback` | 「空 UA / 无任何信号 ⇒ 结果 == `unknown`，且 `unknown` 出现在输出里可被下游统计」 |
| 未知客户端处置 | 「不在 `canonical` 的输入 ⇒ 归一为 `unknown`，**不得**产生新的标签值」（防 Prometheus 基数爆炸） |

**需要裁决的 3 个点（本文给出建议，实施前需确认）**：

| 裁决点 | 建议 | 理由 |
|---|---|---|
| `roo` 的规范值 | 取 **`roocode`** | 网关侧 `roocode` 已被 `nativepair`、`agentRole`、`credentialquota` 引用；`roo` 是 freellmapi 的短名。改 freellmapi 的 1 处（枚举 + 看板映射 `client/src/pages/AgentsPage.tsx:44`）比改网关的 4 处闭集便宜 |
| `deepseek-code` vs `deepseek-harness` | **保留两个值** | 它们是两个不同产品（DeepSeek 编程客户端 vs DeepSeek Harness），不是别名关系 |
| `mimo-code` 是否独立 | **保留独立值** | 它是 OpenCode 的派生品，能力等级不同；网关需补规则并把 mimo 排在 opencode 之前 |

### 5.4 收敛机制：识别一次 + 随请求透传

**机制**：

```
客户端 UA/头
   │
   ▼
[网关请求边界] clienttype.DetectFromRequest(r)  ← 唯一判定点
   │  结果：canonical 值 + source(header|ua|body|prompt|path)
   │
   ├──► 网关自身所有下游（配额/限流/直通/会话/遥测）：直接用这个值，不再各自判
   │
   └──► 出站注入（upstream/client.go:513-543）
          X-Gw-Detected-Client: <canonical>
          X-Gw-Detected-Client-Source: <source>
          ▼
       [freellmapi] classifyClientAgent(req)
          1. 若存在 X-Gw-Detected-Client → 按契约归一化后直接采用，source=gateway
          2. 否则 → 走本地规则（契约生成），source=local
          3. 两者都无 → unknown
```

**为什么用「出站注入头」而不是「运行时接口查询」**：

- 请求级数据不适合走查询接口（一次请求一次调用，且要求 freellmapi 反查网关）。
- 头是天然的请求级载体，与现有 `X-Gw-*` 命名族一致。
- 网关当前**不向任何上游透传入站头**（`upstream/client.go:513-543` 显式构造 `Content-Type`/`Authorization`/`Accept`/`X-Virtual-*`），所以这是**新增**一个 `Set`，不涉及"要不要放开透传"的策略决策。

**安全边界（必须一起设计）**：

1. **出站头必须由网关无条件 `Set`**，并在**入站边界剥离**同名头——沿用现成的 `X-Virtual-*` 规则 20 做法（`upstream/client.go:532-536`：`req.Header.Del` 先删客户端残留再注入）。
2. **freellmapi 只在信任该头时才用它**。最小实现：网关与 freellmapi 之间是同一部署内的私网链路，freellmapi 侧通过配置（可信代理 CIDR / 共享密钥）决定是否信任。**默认不信任**（与 `telemetry.ExtractClientIPTrusted` 的信任模型同款，`telemetry/request_metadata.go:330-372`）。
3. **不沿用 `X-Gw-Client-Type` 这个名字**做透传：它当前的语义是"客户端可声明的显式覆盖"（`internal/clienttype/clienttype.go:120-122`，且客户端可控），与"服务端权威判定结果"语义相反，混用会引入伪造风险。新头用 `X-Gw-Detected-Client`。

**源信息（source）必须一起传**：下游需要知道"这个值是网关判的还是客户端自己说的"，才能在排障时区分误判与伪造。

### 5.5 落地方案：4 个可独立验收的阶段

#### 阶段 1：建立契约与门禁（**不改任何运行时判定**）

| 项 | 内容 |
|---|---|
| **改哪些文件** | 网关仓新增 `contracts/client-catalog.v1.json`、`contracts/client-vectors.v1.json`、`cmd/clientcatalog-gen/main.go`（生成器）、`internal/clienttype/generated_catalog.go`（生成物）；freellmapi 仓新增 `scripts/gen-client-catalog.mjs`、`server/src/lib/generated/client-catalog.ts`（生成物） |
| **契约内容** | §5.3 的 JSON：40 个具体规范值 + `unknown` + 别名表 + 有序规则表 + 弱名分层 + 判定向量 |
| **加什么门禁** | ① 生成物与契约一致的快照测试（两侧各一）；② **契约向量回归**（见 §5.6 的 G1/G2） |
| **怎么验收** | `go test ./internal/clienttype/ -run TestCatalog` 与 `npm test -- client-catalog` 全绿；`git diff --exit-code` 对生成物为空（codegen 可重复）；**线上判定结果零变化**（阶段 1 的验收标准就是"行为不变"） |
| **风险** | 无运行时风险；唯一成本是契约内容需要一次人工评审（尤其是 §5.3 的 3 个裁决点） |

#### 阶段 2：网关内部闭集收敛（消除 F1~F4 的漂移）

| 项 | 内容 |
|---|---|
| **改哪些文件** | `domains/credentialquota/normalize.go:14-18`（闭集改为生成物）；`domains/analysis/sessionmeta/extractor_rules.go:199-214`（`agentRole` 改为生成物 + 补 `pi`）；`telemetry/request_metadata.go:504-584`（第二张 UA 表改为生成物，消除 `intellij`/`pycharm`/`webstorm`/`visual-studio-code` 缺口）；`routingopt/enhancer.go:125-142`（闭集改为生成物）；`domains/streaming/executors/executor.go:1106-1111` 与 `domains/streaming/client_fingerprint.go:63-68`（`clientTokenOf` 去重，保留一处） |
| **加什么门禁** | G3：「**每个下游闭集 ⊇ 契约 `canonical` 集合**」的断言（这条直接堵住 F2 的历史漂移）；G4：「同一 UA 在 `clienttype` 与 telemetry 两张表判定相同」的交叉断言（堵住 F1） |
| **怎么验收** | F2 实测的 10 个漏项（`pi`/`codebuddy`/`trae`/`comate`/`qwen-code`/`kimi-cli`/`iflow`/`raccoon`/`iflycode`/`lingma`）在凭据配额维度不再塌 `unknown`；UA `IntelliJ IDEA/2024.1` 在两张表都判 `jetbrains`；`pi` 的会话角色落 `coding_agent`；`go test ./domains/credentialquota/ ./domains/analysis/sessionmeta/ ./telemetry/ ./routingopt/` 全绿 |
| **行为变化** | **有**（这是唯一改变现有判定的一步）。变化方向是"以前塌成 `unknown` 的现在被识别"，属于**修复**；`routingopt` 的会话模式会把 `codex`/`opencode`/`zcode`/`pi` 从 `api` 改为 `ide`/`cli`——**这一项会改变路由优化行为，必须单独灰度**（见 §5.7） |

#### 阶段 3：识别一次 + 出站透传

| 项 | 内容 |
|---|---|
| **改哪些文件** | 网关：`upstream/client.go:513-543`（新增 `Set("X-Gw-Detected-Client", …)` 与 `Set("X-Gw-Detected-Client-Source", …)`）+ 出站构造的其他入口（`domains/streaming/executors/` 下的上游请求构造点，需在实施时用 `grep -rn 'http.NewRequestWithContext' domains/streaming/` 逐一定位）；入站剥离点（`domains/streaming/handler.go` 的请求入口）。freellmapi：`server/src/lib/client-classifier.ts:45-117`（优先读新头）+ `server/src/lib/client-context.ts:39-50`（把 source 一并存进 `ClientContext`） |
| **加什么门禁** | G5：「客户端伪造 `X-Gw-Detected-Client` 时，出站头里的值等于网关自己的判定结果，而不是客户端的值」；G6：「网关未识别时，出站头值为 `unknown` 或头不存在（不得省略，否则下游无法区分"未识别"与"未透传"）」 |
| **怎么验收** | 端到端测试：带伪造 `X-Gw-Detected-Client: cursor` 的请求穿过网关到 freellmapi，freellmapi 落库的 `client_agent` 必须是网关的真实判定值；`freellmapi_client_agent_source_total{source="gateway"}` 占比符合预期 |
| **风险** | 需要确认所有出站请求构造点都被覆盖（**实施时需 grep 穷举**，本轮未穷举，见 §6.3） |

#### 阶段 4：freellmapi 侧对齐 +（可选）能力挂载

| 项 | 内容 |
|---|---|
| **改哪些文件** | freellmapi：`server/src/lib/client-classifier.ts`（枚举与 token 表改为从契约生成；本地表保留为 fallback）；`client/src/pages/AgentsPage.tsx:33-53`（`analyticsIds` 映射补齐新规范值）；`server/src/db/migrations/`（若需要新增 `client_agent_source` 列） |
| **加什么门禁** | G2：freellmapi 侧跑同一份 `contracts/client-vectors.v1.json`，逐条断言；G6（同上） |
| **怎么验收** | 两侧对同一份向量的判定**逐条相等**（G7）；freellmapi 看板的客户端维度不再出现 `roo`（统一为 `roocode`） |
| **可选能力挂载** | 把 client-agent 接到 freellmapi 真正需要按客户端分叉的能力（如模型可见性、task-type 默认值）——**本期不做**，见 §5.8 |

### 5.6 防漂移机制：门禁怎么写

**门禁清单（每条都给出"什么测试断言什么"）**：

| 编号 | 位置 | 断言 | 堵住的漂移 |
|---|---|---|---|
| **G1** | 网关 `internal/clienttype/generated_catalog_test.go` | `generated.Canonical` 与契约 `canonical` **集合相等且顺序相等**；`generated.Aliases` 逐条相等；`generated.Rules` 的 `id` 序列相等 | 契约改了但生成物没重新生成 |
| **G2** | freellmapi `server/src/__tests__/lib/client-catalog.test.ts` | 生成的 `CLIENT_AGENTS` 与契约 `canonical` 相等；别名表逐条相等 | 同上（freellmapi 侧） |
| **G3** | 网关 `internal/clienttype/closed_sets_test.go` | **每个下游闭集 ⊇ 契约 `canonical` 集合**：`credentialquota.allowedClientTypes`、`sessionmeta.agentRole` 的 coding_agent 集合、`routingopt` 的 ide/cli 集合，逐个断言 | **F2 / F3 / F4 的历史漂移模式**（新增客户端漏改某个下游闭集） |
| **G4** | 网关 `telemetry/client_detection_parity_test.go` | 对 `contracts/client-vectors.v1.json` 的每条向量，`clienttype.DetectFromRequest` 与 `telemetry.ExtractAgentNameFromRequest` 的结果**必须相同**（弱名向量除外，弱名单列一份期望表） | **F1**（两张 UA 表漂移） |
| **G5** | 网关 `domains/streaming/client_detect_header_test.go` | 客户端伪造 `X-Gw-Detected-Client: cursor` 时，出站请求该头的值 == 网关自己的判定结果 | 透传头被客户端污染 |
| **G6** | 网关 + freellmapi | 网关未识别 ⇒ 出站头值为 `unknown`（**不得省略该头**）；freellmapi 在无该头时必须走本地规则并把 `source` 记为 `local` | 下游无法区分"未识别"与"未透传" |
| **G7** | 两侧各跑一次（同一份向量文件） | 两侧对每条向量的**输出必须逐条相等** | "各自与契约一致，但彼此不等"（最隐蔽的一类漂移） |
| **G8** | CI 编排 | 契约文件变更 ⇒ 两侧 codegen 产物 `git diff --exit-code` 非空即失败 | 契约改了但只重新生成了其中一侧 |

> **G7 是最关键的一条**：G1/G2/G4 只能保证"各自与契约一致"，**只有 G7 能保证"两侧彼此一致"**。契约本身写错（比如把 `roo` 写成 canonical）时，G1~G4 全绿而 G7 红——这是我们想要的失败模式。

**防漂移的工程前提**：契约文件必须**双仓可达**。建议做法是网关仓持有权威副本 + freellmapi 仓持有一份**定期同步的镜像**，CI 里对两份做内容比对（G8 的一种实现）；或用 git submodule。**本方案不替用户选型**，但要求两侧 CI 都能读到同一份内容。

### 5.7 风险、灰度、回退与观测

#### 5.7.1 识别错了会怎样（按下游列）

| 下游 | 错误后果 | 严重度 | 缓解 |
|---|---|---|---|
| 原厂直通（`internal/nativepair/nativepair.go:91-114`） | 把直通给了不该给的端点 ⇒ 厂商参数不被认 ⇒ 4xx 剥参重试、白费往返 | 高 | 直通规则本身要求"客户端 × 模型族 × **原厂 catalog code**"三重匹配（`:110-113`），单靠 client-type 错不足以触发直通；且默认开关可关（`:75-82`） |
| 凭据配额（`domains/credentialquota/service.go:68-149`） | 配额落到错误的 client-type 桶 ⇒ 某客户端被限流或超发 | 中 | 桶错只影响**该 client-type 的策略命中**，不影响总量 |
| FpSlot / Pin（`domains/streaming/executors/executor.go:2726-2727`） | 持有者错换 ⇒ 丢失 24h Pin 复用（注释 `:2711-2713` 明确关心这一点） | 中 | 影响的是复用率，不是正确性 |
| 会话身份 / 角色（`domains/analysis/sessionmeta/extractor_rules.go:37-47`） | 统计分类错 | 低 | 纯观测 |
| DLP 放行（`domains/sanitizerules/block_review_persist.go:18`） | 放行键取租户，client_type 只用于展示追溯 | 低 | 天然低风险 |
| freellmapi 看板（`server/src/lib/request-log.ts:128`） | 维度错 | 低 | 纯观测 |

#### 5.7.2 灰度

| 层 | 开关 | 默认 | 语义 |
|---|---|---|---|
| 网关（阶段 2 的 `routingopt` 会话模式变更） | `LLM_GATEWAY_SESSION_MODE_FROM_CLIENT_TYPE=off\|shadow\|on` | `off` | `shadow` = 仍用旧逻辑，只打差异指标 |
| 网关（阶段 3 透传） | `LLM_GATEWAY_CLIENT_DETECT_HEADER=off\|shadow\|on` | `shadow` | `shadow` = 只打指标不注入头；`on` = 注入 |
| freellmapi（阶段 3/4 消费） | `FREELLMAPI_CLIENT_CATALOG_SOURCE=local\|gateway` | `local` | `gateway` = 优先用网关的判定；`local` = 保持现状 |

> 灰度顺序建议：**先 `shadow` 跑一周，用 §5.7.3 的 `mismatch` 指标确认两侧一致后再切 `on`**。

#### 5.7.3 回退

| 场景 | 回退动作 | 生效时间 |
|---|---|---|
| 网关透传头导致 freellmapi 误判 | 网关 `LLM_GATEWAY_CLIENT_DETECT_HEADER=off` | 立即（重启或热加载，需确认开关是否热加载——**未验证**） |
| freellmapi 消费网关值导致看板异常 | freellmapi `FREELLMAPI_CLIENT_CATALOG_SOURCE=local` | 立即 |
| 阶段 2 的闭集收敛引入回归 | 回滚对应 commit（生成物与手写表在同一 commit，回滚即一致） | 一次发布 |
| 契约本身写错 | 改契约 → 两侧重新 codegen → G1~G8 全绿后发布 | 一次发布 |

> **回退的关键前提**：生成物与消费方代码**必须在同一个 commit 里**，否则回滚会出现"契约新、生成物旧"的中间态。

#### 5.7.4 观测（建议的指标名）

| 指标 | 类型 | 标签 | 用途 |
|---|---|---|---|
| `gateway_client_detected_total` | Counter | `client_type`, `source` | 识别分布与来源占比（`source` ∈ `header_gw`/`ua`/`body`/`prompt`/`path`/`unknown`） |
| `gateway_client_detect_fallback_total` | Counter | `stage` | 各兜底层被触发的量（`body`/`prompt`/`path`） |
| `gateway_client_detected_upstream_total` | Counter | `client_type` | 出站注入值的分布（验证透传真的在发生） |
| `gateway_client_detect_mismatch_total` | Counter | `client_type` | 阶段 2 中「旧闭集 vs 新闭集」判定不一致的量（shadow 期用） |
| `freellmapi_client_agent_source_total` | Counter | `source` | `gateway` / `local` / `unknown` 占比 |
| `freellmapi_client_agent_mismatch_total` | Counter | `client_type` | 网关值与 freellmapi 本地值不一致的量（灰度期最关键的观测项） |

> 命名沿用现有惯例：网关侧标签用 `client_type`（与 `credentialfpslot/metrics.go:143-176` 一致），freellmapi 侧用 `client_agent`（与 `requests.client_agent` 列名一致，`:126-128`）。

### 5.8 这次**不做**的事

1. **不改网关的协议转换、URSM 路由、参数改写算法**。本方案只动"识别结果从哪来、叫什么"。
2. **不合并三套 "client profile"**（§1.4）。网关的 `client_profiles` 行为画像表、`X-Client-Profile` 指纹维度、freellmapi 的 `sk-cp-` 档案是**三个不同概念**，强行合并会把"指纹稳定性""行为聚合""凭据绑定"混成一件事，风险远大于收益。
3. **不把 client-agent 接到 freellmapi 的任何行为路径**（路由、quirks、工具能力、system prompt）。这些当前按模型/平台/API key 分叉，本期不动。freellmapi 侧只做"消费 + 记录来源"。
4. **不删除任一侧现有的识别实现**。freellmapi 的本地分类器保留为 fallback；网关的 body marker 与 system prompt 兜底链保留。
5. **不做跨仓 CI 的完整编排**（共享 runner、合并队列等）。阶段 1 只要求两侧各自有一个能读到契约文件的门禁。
6. **不改 `X-Gw-Client-Type` / `X-Client-Profile` / `X-Agent-Name` 三个入向头的现有语义**（包括 `X-Client-Profile` 不校验的事实）。本方案只**新增**一个服务端注入的出站头。
7. **不承诺生产 UA 覆盖率**。本文的所有 UA 结论来自 74 条构造样本，**没有采样生产流量**（网关侧有生产 UA 分布的既往审计档 `docs/audit/2026-10-29-客户端识别轮-原厂规范对照与线材实态.md`，但本次未取数）。真实覆盖率需在阶段 1 之后用 `gateway_client_detected_total` 的 `source` 分布来量化。
8. **不处理 freellmapi 侧的"客户端配置生成器"**（`setup-claude` / `setup-openclaw` 等，见 `docs/zh-cn/clients/01-agent-clients.md:27-61`）。它写的是客户端的 provider 配置，与识别无关。

---

## 6. 附录

### 6.1 证据索引

**网关（`/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go`）**

| 主题 | 位置 |
|---|---|
| 规范值闭集 + 别名表 | `internal/clienttype/clienttype.go:8`、`:55-92` |
| 双边界匹配器 | `internal/clienttype/clienttype.go:24-45` |
| header/UA 主链 + 优先级 | `internal/clienttype/clienttype.go:94-216` |
| UA token 全表 | `internal/clienttype/clienttype.go:127-207` |
| `claude-code` beta 旗标 | `internal/clienttype/clienttype.go:208-214` |
| streaming 侧薄委托 | `domains/streaming/client_fingerprint.go:22-30`、`:43-48`、`:63-68` |
| executors 侧薄委托 + `clientTokenOf` 副本 | `domains/streaming/executors/executor.go:1088-1096`、`:1104-1111` |
| body 级识别 | `telemetry/body_client_marker.go:23-34`、`:35-105`、`:112-143` |
| system-prompt 词表 | `telemetry/request_metadata.go:34-187`、`:234-249` |
| telemetry 第二张 UA 表 + 弱名 | `telemetry/request_metadata.go:482-610`（表 `:504-584`，弱名 `:572-583`） |
| `X-Agent-Name` 清洗 | `telemetry/request_metadata.go:436-457` |
| body marker 覆盖策略 | `domains/streaming/request_log_pipeline.go:649-666`、`:740-757` |
| 凭据配额闭集（已漂移） | `domains/credentialquota/normalize.go:9-29`、`:31-41`；`domains/credentialquota/service.go:68-160` |
| 会话身份 / 角色闭集 | `domains/analysis/sessionmeta/extractor_rules.go:16-47`、`:199-214`、`:222-229` |
| 原厂直通规则 | `internal/nativepair/nativepair.go:57-73`、`:91-114`；调用点 `domains/streaming/executors/executor_chat.go:50-79` |
| 会话模式闭集 | `routingopt/enhancer.go:83`、`:120-142`、`:158-192` |
| FpSlot 指标标签 | `credentialfpslot/metrics.go:143-176`、`:357` |
| 出站头构造（不透传入站头） | `upstream/client.go:513-543` |
| `X-Client-Profile` 指纹维度 | `domains/identity/hook.go:88-93`；`domains/identity/identity.go:43`、`:158`；`domains/identity/builder.go:24-28` |
| `client_profiles` 行为画像表 | `sql/migrations/domain/132_client_profile.sql:1-35` |
| `model_aliases.client_profiles` 过滤 | `provider/client.go:1310`、`:1382-1385`、`:1505-1507`；调用点 `domains/streaming/handler.go:3741`、`:4049` |
| DLP 放行记录的 client_type | `domains/sanitizerules/block_review_persist.go:18`、`egress_release.go:58`、`postgres_block_review.go:38` |
| 会话落库 | `domains/session/v2/family_writers.go:60`；`domains/session/v2/session_aggregator.go:224` |
| 连接注册表 | `domains/streaming/connection_registry.go:143-146`、`:154` |
| 已有测试 | `internal/clienttype/clienttype_test.go:5-29`、`:39-67`、`:72-123` |
| 识别链既有文档 | `docs/client-formats/README.md:82-102` |
| 识别轮审计档 | `docs/audit/CRITICAL-AUDIT-2026-10-29-identification-round.md` |

**freellmapi（`/Users/xutaohuang/workspace/ai/freellmapi`）**

| 主题 | 位置 |
|---|---|
| 枚举 | `server/src/lib/client-classifier.ts:3-31`、`:33` |
| 判定全链 | `server/src/lib/client-classifier.ts:35-117` |
| 唯一消费入口 | `server/src/lib/client-context.ts:39-50`（`:48`） |
| 日志落库 | `server/src/lib/request-log.ts:123-128`；`services/search.ts:491-511`；`services/decision.ts:422-427` |
| 路由只读 routingStrategy | `server/src/services/router.ts:767`（无 agent 引用） |
| 任务类型按头 + 请求形状 | `server/src/lib/task-type.ts:13-17`、`:67`；消费点 `routes/anthropic.ts:706`、`routes/proxy.ts:2536`、`routes/responses.ts:1217` |
| quirks 按模型/平台 | `server/src/services/quirks.ts:25-40` |
| 工具能力按模型/端点 | `server/src/lib/tool-capability.ts:1-21` |
| system prompt 按 API key | `server/src/lib/system-prompt.ts:11`、`:56-70`、`:79-82` |
| 客户端档案 = API key | `server/src/routes/client-profiles.ts:1-12`、`:87-103` |
| 会话亲和读 Claude Code 头 | `server/src/routes/anthropic.ts:597` |
| 内部调用标记 | `server/src/services/routing-decision.ts:27`、`:195` |
| 前端看板维度 | `client/src/pages/AgentsPage.tsx:22`、`:33-53`、`:82`；`client/src/pages/AnalyticsPage.tsx:196` |
| 既有测试 | `server/src/__tests__/lib/client-classifier.test.ts:12-155` |
| 既有文档 | `docs/zh-cn/clients/01-agent-clients.md:27-61`（智能体表）；`docs/zh-cn/clients/02-supported-agents.md`（支持清单）；`docs/clients/01-agent-clients.md`（**仅跳转页**，正文在 `docs/en/` 与 `docs/zh-cn/`） |

### 6.2 实际跑过的校验命令与输出摘要

> 所有命令均为**只读**性质；探针文件写在 `/tmp`，通过 `go test -overlay` 注入，**未向两个仓库写入任何文件**。

**① 基线：网关既有测试全绿**

```bash
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
go test ./internal/clienttype/ -v
```
→ `PASS`，`TestHasToken` 20 个子用例全 PASS（含 4 个形近词负臂）。

**② 网关 `Normalize` 全词表探针**（`go test -overlay` 注入 `/tmp/ctprobe/zz_probe_test.go`）

```
NORMALIZE "roo"            => "unknown"      # 规范值是 roocode
NORMALIZE "cline"          => "unknown"      # system-prompt 词表有 cline，Normalize 不认
NORMALIZE "aider"          => "unknown"
NORMALIZE "continue"       => "unknown"
NORMALIZE "kiro"           => "unknown"
NORMALIZE "claude"         => "unknown"
NORMALIZE "deepseek-harness" => "unknown"
NORMALIZE "gemini-cli"     => "unknown"
NORMALIZE "openai-sdk"     => "unknown"
NORMALIZE "anthropic-sdk"  => "unknown"
NORMALIZE "ollama-client"  => "unknown"
NORMALIZE "kilo-code" / "crush" / "mimo-code" / "atomcode" / "openclaw" / "hermes-agent" / "reasonix" / "goose" => "unknown"
NORMALIZE "zulu" => "comate" ; "code-buddy" => "codebuddy" ; "trae-cli"/"trae-agent" => "trae"
NORMALIZE "kimi" => "kimi-cli" ; "iflow-cli" => "iflow" ; "qwen-cli" => "qwen-code"
```

**③ 网关 `DetectFromRequest` 74 条 UA 探针**（同上）→ 见 §3.6 全表。

**④ 网关 telemetry 第二张表 + system-prompt 探针**（`/tmp/ctprobe/zz_probe2_test.go`）

```
TELEMETRY "IntelliJ IDEA/2024.1"      => "unknown"   # clienttype 侧是 "jetbrains" —— F1
TELEMETRY "PyCharm/2024.1"            => "unknown"
TELEMETRY "WebStorm/2024.1"           => "unknown"
TELEMETRY "Visual Studio Code/1.90"   => "unknown"
TELEMETRY "vscode/1.90"               => "vscode"
TELEMETRY "pi/1.4.0 (linux; node/v22)" => "pi"
TELEMETRY "curl/8.14.1"               => "curl"
TELEMETRY "python-requests/2.31"      => "python-client"
TELEMETRY "Go-http-client/1.1"        => "go-client"
TELEMETRY "PostmanRuntime/7.36"       => "unknown"    # "Postman" 后接字母 R，双边界拒绝
SYSPROMPT "You are Cline, a coding agent."   => "cline"    # 随后被 Normalize 塌掉
SYSPROMPT "You are aider, ..."                => "aider"
SYSPROMPT "You are Continue, ..."             => "continue"
SYSPROMPT "You are Kiro, an AI IDE."           => "kiro"
SYSPROMPT "You are claude."                    => "claude"
```

**⑤ 网关凭据配额闭集漂移探针**（`/tmp/ctprobe/zz_probe3_test.go`）

```
QUOTA "pi"          => "unknown"    # 应为 "pi"          —— F2
QUOTA "codebuddy"   => "unknown"    # 应为 "codebuddy"
QUOTA "trae"        => "unknown"
QUOTA "comate"      => "unknown"
QUOTA "qwen-code"   => "unknown"
QUOTA "kimi-cli"    => "unknown"
QUOTA "iflow"       => "unknown"
QUOTA "raccoon"     => "unknown"
QUOTA "iflycode"    => "unknown"
QUOTA "lingma"      => "unknown"
（其余 13 个规范值正常）
```

**⑥ 网关 sessionmeta 塌陷探针**（`/tmp/ctprobe/zz_probe4_test.go`）

```
SESSIONMETA "You are Cline, a coding agent." => agent="unknown" role="unknown" src="system_prompt" | client="unknown" src="unknown"
SESSIONMETA "You are aider, ..."             => agent="unknown" role="unknown"
SESSIONMETA "You are Continue, ..."          => agent="unknown" role="unknown"
SESSIONMETA "You are Kiro, an AI IDE."        => agent="unknown" role="unknown"
SESSIONMETA "You are Claude Code, ..."        => agent="claude-code" role="coding_agent" | client="claude-code" src="agent"
SESSIONMETA "You are Kimi CLI."               => agent="kimi-cli" role="coding_agent"
```

**⑦ freellmapi 分类器探针**（逐字节副本 + sha256 校验）

```bash
cp /Users/xutaohuang/workspace/ai/freellmapi/server/src/lib/client-classifier.ts /tmp/ftprobe/
shasum -a 256 <两份>    # 两侧同为 c11d67770e90b3f0ca634c33480965ac06c82ca581eb76bc4279d2ceec09199a
node --experimental-strip-types /tmp/ftprobe/probe.ts
```
→ 74 条 UA 结果见 §3.6 全表；枚举 dump：
`claude-code,codex,cline,roo,continue,aider,opencode,goose,qwen-code,kilo-code,crush,deepseek-harness,mimo-code,atomcode,openclaw,hermes-agent,pi,reasonix,zcode,cursor,gemini-cli,zed,jetbrains,ollama-client,openai-sdk,anthropic-sdk,unknown`（27 个值）。

**⑧ 两边结果逐条比对**（Python 脚本，读 ⑥/⑦ 的输出）

```
gateway UA probes=74  freellmapi UA probes=74  comparable=74
  A. 同一规范值              = 13
  B. 两边都未识别出具体客户端  = 15   (gw='' / fl='unknown')
  C. 真分歧                  = 46
     网关识别出、freellmapi 未识别 = 20
     freellmapi 识别出、网关未识别 = 23
     两边都识别出但规范值不同      =  3
```

**⑨ 集合差脚本**（从 ② 的运行时探针输出反推网关规范值，再与 freellmapi 枚举求差）

```bash
# 网关规范值 = { x : Normalize(x) == x }，由 ② 的探针输出得到（不手抄源码）
python3 - <<'PY'
import re, json
canon, alias = set(), {}
for line in open('/tmp/ctprobe/norm.txt'):
    m = re.match(r'NORMALIZE\t(.*?)\t=>\t(.*)$', line.rstrip('\n'))
    k = json.loads(m.group(1)); v = json.loads(m.group(2))
    if k and k == v: canon.add(k)
    elif v != 'unknown': alias[k] = v
canon.discard('unknown')
fl = set(re.findall(r"'([a-z0-9\-]+)'", open('/Users/xutaohuang/workspace/ai/freellmapi/server/src/lib/client-classifier.ts')
       .read().split('CLIENT_AGENTS = [')[1].split(']')[0]))
fl.discard('unknown')
print(len(canon), len(alias), len(fl), len(canon & fl), len(canon - fl), len(fl - canon), len(canon | fl))
PY
```
输出：
```
gateway canonical (runtime-derived) = 23
gateway alias entries               = 8   # 其中 1 条是 "  Claude-Code  " 的空白裁剪用例，非别名；真别名 7 条
freellmapi enum (specific)          = 26
intersection = 9  ['claude-code','codex','cursor','jetbrains','opencode','pi','qwen-code','zcode','zed']
gateway-only = 14 ['codebuddy','comate','copilot','deepseek-code','iflow','iflycode','kimi-cli',
                   'lingma','minimax-code','raccoon','roocode','trae','vscode','windsurf']
fl-only      = 17 ['aider','anthropic-sdk','atomcode','cline','continue','crush','deepseek-harness',
                   'gemini-cli','goose','hermes-agent','kilo-code','mimo-code','ollama-client',
                   'openai-sdk','openclaw','reasonix','roo']
union (specific) = 40   +unknown => 41
```

**⑩ 仓库清洁度自查**（见本文末尾"交付自查"）

```bash
git -C /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go status --short
git -C /Users/xutaohuang/workspace/ai/freellmapi status --short
```

### 6.3 未验证标注清单（本文明确未验证的推断）

| # | 未验证的陈述 | 所在位置 | 如何确认 |
|---|---|---|---|
| 1 | ZCode **桌面版**的 UA `ai-sdk/provider-utils/4.0.39 runtime/node.js/24` 在网关侧被判为 `unknown` | §3.3 D2 | 对该 UA 跑一次 `clienttype.DetectFromRequest` 探针（本轮未单独跑；按 `:143-144` 只有 `zcode` token 可命中，该 UA 无 `zcode` 子串，**推断为 `unknown`，但未实测**） |
| 2 | 网关侧 `codex`/`opencode`/`zcode`/`pi` 等在 `routingopt` 会话模式里从 `api` 改为 `ide`/`cli` 会**改变路由优化行为** | §5.5 阶段 2 | 需在 shadow 期用 `gateway_client_detect_mismatch_total` + 会话模式分布指标实测 |
| 3 | 灰度开关是否支持热加载（即"立即回退"是否真的立即） | §5.7.3 | 读网关的开关加载实现（`routingopt`/`cmd/gateway` 的 config 加载路径）——本轮未查 |
| 4 | 网关**全部**出站上游请求构造点已枚举 | §5.5 阶段 3 | 需 `grep -rn 'http.NewRequestWithContext' domains/streaming/ domains/hooks/` 等穷举——本轮只确认了 `upstream/client.go:513-543` 一处 |
| 5 | 两侧客户端目录在**真实生产流量**上的覆盖率 | §5.8 第 7 条 | 需采样生产 UA 分布（网关侧有既往审计档 `docs/audit/2026-10-29-客户端识别轮-原厂规范对照与线材实态.md`，本次未取数） |
| 6 | freellmapi 侧 `client_agent` 是否被任何**未搜到的**路径消费（例如前端本地逻辑或报表 SQL） | §1.3 | 需全仓（含 `client/`、`scripts/`、迁移文件）再 grep 一遍 `client_agent`；本轮 grep 覆盖了 `server/src/**` 与 `client/src/**` |
| 7 | `freellmapi` 的 `x-session-id` → `claude-code` 判定是否会与 `routes/anthropic.ts:597` 的会话亲和逻辑冲突 | §3.3 D1 | 需读 `routes/anthropic.ts` 完整会话亲和路径；本轮只看了 `:597` 一行与其注释 |
| 8 | 契约 40 个规范值的**最终取值**（尤其 `roo`→`roocode`、`mimo-code` 独立、SDK 类是否单列一层） | §5.3 裁决点 | 需人工评审拍板，本方案只给建议 |

---

## 交付自查

本次交付**只写入本文一个文件**。探针文件（`/tmp/ctprobe/*.go`、`/tmp/ftprobe/*.ts`）全部在仓库外，通过 `go test -overlay` 与 `node --experimental-strip-types` 运行，**未向任何仓库落过临时文件**（已用 `git status --short --untracked-files=all | grep -i probe` 确认为空）。

```bash
$ git -C .../llm-gateway-go status --short
 M admin/handler.go                       # ← 并行会话的 SSO/app-token 改动，非本次
 M pkg/identity/minting.go                # ← 同上
 M scripts/deploy-seamless-docker.sh      # ← 同上
?? admin/app_token.go                     # ← 同上
?? admin/app_token_test.go                # ← 同上
?? scripts/deploy-seamless-identity_env_test.sh  # ← 同上
?? docs/freellmapi-integration/           # ← 本次交付（唯一）

$ git -C .../freellmapi status --short
 M .env.example / docs/*\/deployment\/OVERVIEW.md / server/src/app.ts
 M server/src/middleware/requireAuth.ts / server/src/routes/auth.ts
 M server/src/services/auth.ts
?? server/src/routes/sso.ts / server/src/lib/identity.ts / ...   # ← 全部为并行会话的 SSO 改动
```

> **说明**：本文撰写开始时（2026-11-03 10:39）两个仓库的 `git status --short` 均为**空**。上述 `admin/app_token.go`、`server/src/routes/sso.ts` 等改动在 10:40~10:47 之间由**并行会话**写入（内容为 SSO / app-token / identity 鉴权，与客户端识别无关），**不是本次交付的一部分**，本文未做任何修改。本文档只新增了 `docs/freellmapi-integration/` 目录及其下的本文件。

本方案**未修改任何 Go / TypeScript 源码、README、CHANGELOG 或其它文档**；唯一的写入是本文。
