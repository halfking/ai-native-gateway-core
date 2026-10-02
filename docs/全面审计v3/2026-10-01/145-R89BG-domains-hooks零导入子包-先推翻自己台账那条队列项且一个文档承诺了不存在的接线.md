# 145 号｜R89-BG：`domains/hooks` 零导入子包 —— **先推翻我自己台账里的那条队列项**（11 个全错），真零导入是另外 3 个，其中一个**文档承诺了一条不存在的接线**

- 日期：2026-10-01
- 轮次：R89-BG
- 起因：台账下轮队列记着「`domains/hooks` 目录其余零导入子包（goal/handoff/cache/security/response/
  audit/outputcompliance/session-inspector/sessionanalysis/toolexecution/tools）」，
  对应 objective 第 33 项「代码结构梳理 / 冗余标注与清理」（覆盖总览里仍标 **部分**）。
- **结论先行**：
  1. **⚠️ 台账那条队列项作废 —— 列的 11 个包没有一个是零导入，全部都有生产引用。**
     根因不是判断错，是**检索形态坏了**：我（及此前登记）用的 import 前缀漏了 `github.com/`，
     导致形态 1 对**全部 18 个包**一律返回 0。⇒ **一个检索形态对所有目标都返回 0 时，
     几乎总是形态坏了，不是「都不存在」。**
  2. **真正的零导入是另外 3 个**：`intentanalysis`（436 行）、`memoraauto`（1,593 行）、
     `promptoptimization`（1,374 行）—— **合计 3,403 行生产代码 + 各自的测试**，
     且经二次确认**不可达**（`HookRegistry.Register` 在生产代码里 0 调用点，且三包互不引用）。
  3. **⚠️ 本轮最有价值的发现（P2）：`docs/hooks/prompt-optimization.md` 明确告诉运维
     「设 `PROMPT_OPTIMIZATION_ENABLED=1` 启用」，而该环境变量只被包内 3 处读取、
     仓内无任何生产代码 import 这个包 ⇒ 照文档操作不会有任何效果。**
     这是**面向使用者的文档承诺了一条不存在的接线**，属 §13「注释/文档承诺的接线从未落地」的第 5 例。

---

## 一、⚠️ 先说更正：台账那条队列项是错的

### 1.1 台账怎么写的

`00-审计覆盖台账.md` §4.2 下轮队列：
> 「`domains/hooks` 目录其余零导入子包（goal/handoff/cache/security/response/audit/
> outputcompliance/session-inspector/sessionanalysis/toolexecution/tools）」

### 1.2 实测（**正确前缀** `github.com/kaixuan/llm-gateway-go/...`）

| 子包 | 非测试生产引用**文件数** | 台账说零导入？ |
|---|---|---|
| `audit` | **23**（`cmd/gateway/main.go`、`main_pipeline.go`、`main_v2_pipeline.go`…） | ❌ 错 |
| `response` | **16**（`domains/streaming/handler.go`、`responses.go`…） | ❌ 错 |
| `goal` | **7** | ❌ 错 |
| `cache` / `outputcompliance` / `security` / `session-inspector` / `tools` / `handoff` / `sessionaudit` | 3~4 | ❌ 错 |
| `sessionanalysis` / `toolexecution` | 1 | ❌ 错 |

**⇒ 列出的 11 个，全部有生产引用。这条队列项应当作废。**

### 1.3 根因：**形态 1 对所有 18 个包都返回 0**

```bash
# 我用的（错）：漏了 github.com/ 前缀
pkg="kaixuan/llm-gateway-go/domains/hooks/$p"
# ⇒ audit=0 cache=0 compression=0 goal=0 … **18 个全是 0**
```
`go.mod` 第一行：`module github.com/kaixuan/llm-gateway-go`
⇒ 正确的 import 路径必须带 `github.com/`。

⚠️ **本会话自己的纪律里早就写着「0 命中是危险信号」「判定『不存在』前至少两种检索方式复核」，
而形态 2（短路径 `hooks/$p"`）当时给出的恰恰是「有命中」**——
**两条形态互相矛盾时，出错的是那条「更权威」的**（全路径看起来更严谨，实际写错了）。
**若当时采信了形态 1，就会把 11 个在产线上跑着的包报成死代码。**

### 1.4 这也解释了形态 2 为什么「有值」

形态 2 用的是 `hooks/$p"`（不锚定 module 前缀），**它匹配到了正确的 import 行**
（因为行尾就是 `hooks/audit"`）⇒ **形态 2 是对的，形态 1 是错的。**

## 二、真正的零导入：3 个包，3,403 行

用正确前缀重扫 18 个子包：

| 子包 | 非测试 .go | 行数 | 生产引用 | 定性 |
|---|---|---|---|---|
| **`promptoptimization`** | 5 | **1,374** | **0** | ⚠️ 见 §三 |
| **`memoraauto`** | 5 | **1,593** | **0** | ⚠️ 见 §四 |
| **`intentanalysis`** | 1 | **436** | **0** | 见 §五 |

其余 15 个均有生产引用。

### 2.1 不可达性二次确认（不能只靠「无 import」）

- `domains/hooks/registry.go` 的 `HookRegistry.Register(hook Hook)` 存在，
  但 **`hooks.Register(` / `MustRegister` 在非测试 Go 代码里 0 命中**
  ⇒ **没有任何生产代码把任何 hook 放进这个 registry**；
- 三个包**互不引用**（扫描时已排除自身目录，同级引用也为 0）；
  ⇒ **要构造它们必须有人 import 它们 ⇒ 无人 import ⇒ 不可达。**
  **符合「不可达的缺陷不是缺陷」的前提：它们不是坏了，是从未接线。**

## 三、⚠️ P2（新）：`docs/hooks/prompt-optimization.md` 承诺了一条不存在的接线

`docs/hooks/` 下**只有这一个文件**，写得相当完整：

> `docs/hooks/prompt-optimization.md:9`
> 「> `prompt_optimization`。设 `PROMPT_OPTIMIZATION_ENABLED=1`」
> `:17`「**默认关闭**：`PROMPT_OPTIMIZATION_ENABLED=1` 显式开启，对存量流量零影响」
> `:45` 环境变量表：「`PROMPT_OPTIMIZATION_ENABLED` | `false` | 总开关（默认关闭）」

**实测**：
```bash
grep -rn "PROMPT_OPTIMIZATION_ENABLED" --include=*.go .
# config.go:5  （注释）
# config.go:23 （常量定义 EnvEnabled）
# hook.go:28   （注释）
# ⇒ **全部在本包内。包外 0 处读取。**
```

⇒ **运维照这份文档设 `PROMPT_OPTIMIZATION_ENABLED=1`，不会有任何效果** ——
因为**没有任何生产代码会构造这个 hook，轮到自己读这个变量**。
文档还写了触发条件（`:58`「Hook 未启用（`PROMPT_OPTIMIZATION_ENABLED != 1`）」），
读起来完全像一份可操作手册。

⚠️ **这是 §13「注释/文档承诺的接线默认不成立」的第 5 例，且是唯一一例
**面向使用者（运维/部署者）而不是面向开发者** 的** —— 影响面比前 4 例都大。
**建议（待裁决 60，不擅自动手）**：
① 最小：**在文档顶部加显著警示**「本 Hook 尚未接线，设该变量无效」；
② 或：**补接线**（`HookRegistry` 目前无生产调用方，接线是另一件事，需先确认 registry 本身是否在用）；
③ 无论选哪个，**都不应让文档继续以「照此设置即可启用」的口吻存在**。

## 四、`memoraauto`：连环境开关都没有

- 客户端 `NewKxmemoryClient(baseURL string, timeout time.Duration)` ——
  **`baseURL` 由构造函数传入，包内不读任何环境变量**；
- 全仓 `KXMEMORY_*` 在**包外 0 引用**；
- 包内有 `config.example.yaml` 与 `README.md`，但 README 里**没有写怎么接线**（搜「注册/接线/在 main」0 命中）。
⇒ 与 §三同族但更彻底：**它连「一个假的开关」都没留**，从外部完全看不出该怎么用。

## 五、`intentanalysis`：能力与 objective 沾边，但同样未接线

- 包注释自陈：「多轮意图分析（基于会话历史）」「**意图漂移检测（KL 散度）**」
- 构造函数 `NewIntentAnalysisHook(analyzer *intentconfig.Analyzer, logger *slog.Logger)`
  —— 依赖 `intentconfig.Analyzer`，而该类型不在本包内 ⇒ **接线路径还多一跳**；
- 无文档（`docs/hooks/` 下只有 prompt-optimization 那一份）。
⚠️ **诚实定性**：意图漂移检测**在概念上命中 objective 的「会话摘要/信息抽取」方向**，
但**本轮未找到任何代码消费它的输出** ⇒ 登记为「被遗忘的能力」，**不报缺陷**。

## 六、playbook §39 新增

> **「一个检索形态对所有目标都返回 0」时，先怀疑形态，不要先下结论。**
>
> **证据（145 号）**：用漏了 `github.com/` 的 import 前缀去扫 18 个子包，
> **18 个全部返回 0**；而形态 2（不锚定 module 前缀）给出了真实分布。
> **若采信了形态 1，就会把 11 个在 `cmd/gateway/main.go` 与
> `domains/streaming/handler.go` 里跑着的包报成死代码。**
>
> **落地**：
> 1. **0 命中必须配一条「反向对照」**（本会话已多次执行，但本轮**恰恰是形态之间互相矛盾时
>    采信错了那一条**）⇒ **当两条形态给出相反答案时，不要默认「更严格的那条更可信」——
>    要去核对 module 名、路径拼写、是否 vendor、是否被 build tag 排除。**
> 2. **凡是产出「N 个包全部零导入」这类整齐结论的扫描，先把 module 路径打印出来核对一遍。**
> 3. **本条是 §36/§37 的姊妹条**：§36 是「过滤条件悄悄吃掉数据」，
>    §37 是「存在性核对维度不全」，**本条是「检索形态本身坏了却输出了整齐结论」**。
>    **共同点：三者都产出干净、自洽、可信的数字，而它们都是错的。**

**同族**：§13 / §14 / §31 / §32 / §35 / §36 / §37 / §38。

---

## 七、诚实边界

- **未改动任何生产代码、前端代码或数据库**（全部只读）。
- **本轮只核了 `domains/hooks` 一个目录**。⚠️ **既然「零导入」的口径在别处可能也用过同样的错前缀，
  则本会话此前基于「零导入」得出的其它结论（114–120 号的 B/C/D 类划分）需要抽查复核。**
  ⇒ **如实登记为待抽查项，本轮不擅自推翻前序结论**（按约定：不回改历史报告，只登记）。
- `intentanalysis` 的「无消费者」是**检索结论**；未检查是否有动态加载形态。
- `memoraauto` README「没有接线说明」是基于关键词检索，**未逐行读完 1,593 行**。
