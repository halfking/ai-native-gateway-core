# 217 号 | R89-EG — 消化第三份「未消化的报告」：预算闸**覆盖面**逐面坐实，而报告对 Gemini 的判断**漏看了一层**

> **变更面**：**零代码改动**（本轮全部是核实 + 一条过期注释的认定）。
> 触发方式：215、216 号清账的第三份。上三轮共 5 个子代理，本轮消化第 3 份
> 「审计队列租约与预算 fail-open」（VERDICT: PARTIAL，**9 条证伪** + 5 条发现 + 3 条未查清）。

---

## 一、先按 §152 查基线：报告的「计数断言」已经过期

`git diff --stat 013411cd1 HEAD -- domains/streaming domains/dispatch authentication`
⇒ **36 文件、6573 insertions / 82 deletions**。

报告里最硬的一条证据是「**全仓非测试 `CheckBudget(` 调用点仅此两处**」。
⚠️ 这是一条**计数断言**，而它已经过期：

| 时点 | 生产调用点 |
|---|---|
| 报告成文时 | 2 处（`handler.go:2386` chat、`embeddings.go:399`） |
| **当前 HEAD** | **3 处** —— 多出 `audio_service.go:705` |

第三处是**录音转写网关**带来的（214 号那次入站合并引入 `audio_service.go`），
**报告成文时它还不存在**。⇒ 这不是报告的错，但它证明了一件事（见 §五 playbook）。

我**没有**用 `Select-String` 出这个结论——第一次就是这么干的：

> ⚠️ `Select-String -Path 'domains/**/*.go'` —— **PowerShell 不支持 `**` 递归通配**，
> 它只按字面路径找，**结果不可信**。改用 `grep` 工具（真递归）重做才有上面的三处。

---

## 二、核心：预算闸的**逐面覆盖面**坐实

### 先把闸门的位置找准（这一步决定后面全部结论）

`handler.go` 的函数边界是 `1895 ServeHTTP` → `1912 serveHTTPInner` → `2227 serveWithExecutor`。
预算闸在 **`handler.go:2386`**，落在 **1912~2227 区间内** ⇒ **它在 `serveHTTPInner` 里，
不在 `serveWithExecutor` 里**。

⚠️ 报告的说法是「`messages.go`/`responses.go`/`handler_gemini.go` 各自 `ServeHTTP`，
不进 `serveWithExecutor`（唯一调用点 `handler.go:2200`）⇒ 预算完全缺席」——
**判据选错了函数**，于是把三个面一锅端了。

### 逐面结论

| 入站面 | 路由 | Handler | 配额闸 |
|---|---|---|---|
| OpenAI Chat | `/v1/chat/completions`、`/v1/completions` | `chatHandler.ServeHTTP` → `serveHTTPInner` | ✅ `handler.go:2386` |
| **Anthropic Messages** | `/v1/messages` | **`MessagesHandler.serveHTTPInner`**（自己的） | ❌ **无** |
| **OpenAI Responses** | `/v1/responses` | **`ResponsesHandler.serveHTTPInner`**（自己的） | ❌ **无** |
| **Gemini 原生** | `/v1beta/models/`、`/v1/models/` | `GeminiHandler` → **`chatHandler.ServeHTTP`** | ✅ **有**（报告判断错误） |
| Embeddings | `/v1/embeddings` | 独立 handler | ✅ `embeddings.go:399` |
| 音频三面 | `/v1/audio/transcriptions`、`/v1/audio/speech`、`/v1/mcp` | 共享 `AudioService` | ✅ `audio_service.go:705` |

**Gemini 为什么有闸**（215 号已坐实，本轮复用）：
`handler_gemini.go:330 ParseGemini`（body→IR）→ `:352 SerializeOpenAI`（IR→**OpenAI body**）
→ `:369/239` 合成一个 **path = `/v1/chat/completions`** 的请求
→ `:378/388 h.chatHandler.ServeHTTP(synthReq)`
⇒ 进 `serveHTTPInner` ⇒ 过 `handler.go:2386`。
⚠️ 报告看到 `handler_gemini.go:281` 独立 `ServeHTTP` 就下了结论，**没往下看它的 Step 7 会把请求交回 chatHandler**。
这与 215 号 F4（Gemini「零脱敏」）是**同一个漏看位置**——两轮里它错了两次，方向相反。

### ⚠️ 必须区分两种「budget」，否则会把缺口读反

`messages.go:295-302` 与 `responses.go:302-310` **有** `promptBudgetExceeded`，
它是**单请求 prompt 大小上限**（防 245 号 OOM，超了 413）；
`CheckBudget` 是**租户/API key 的累计配额**（超了 402 `insufficient_quota`）。
⇒ **两者完全不同**。「这两个面什么预算都没有」是错的读法；准确说法是
「**这两个面有 prompt 大小护栏，但没有累计配额闸**」。

### 也不是「用量没记」

- 用量记录：两个面都有（`messages.go:699 recordInitialRequestLog`、`responses.go` 同构）
  ⇒ 进 `request_logs`。
- `enqueueQuotaTask` 只服务 OmniFree 免费池（唯一生产调用点在 `handler_autocombo.go:395/426`），
  **不是**通用用量记账，**不能**拿它当「通用记账缺失」的证据。

⇒ 缺口被精确收敛成一句话：**只有累计配额闸缺席**。

---

## 三、观测面核实：报告的观测论断**成立**

`metrics/budget_metrics.go:10-13` 说「CheckBudget 的错误结局此前被**两调用点**类型断言静默吞掉」。
⚠️ 「两调用点」同样已过期（现在是三处）。但**机制**比那个计数更稳：

`BudgetChecksTotal` 是在 **`KeyVerifier.CheckBudget` 内部**计数的
（`verifier.go:763` skipped_snapshot、`:769` ok、`:771` exceeded、`:775` error、`:817` degraded_no_ledger），
**不在调用点**。

⇒ **新增任何调用点都自动被观测覆盖**，不会漏计。**三个面全覆盖，无观测缺口。**
（`verifier.go:773` 那句「DB 故障放行（fail-open 既有语义）」措辞不精确——
`CheckBudget` 是**返回 err、交给调用点决定**，`budget_failclosed_test.go` 钉的是调用点的 503；
不影响结论，但该注释会误导读者以为「DB 故障会放行」。**登记为低优先级注释项，本轮不改**，
因为要改就得先决定它到底该描述「检查与执行分离」还是描述某次具体裁决。）

---

## 四、处置：**不加闸**，并入待裁决 79

给 `/v1/messages` 与 `/v1/responses` 补配额闸，会让**原本能通过的请求变成 402** ——
这是**对外行为的实质变更**，属产品口径（这两个面是否应当计费/限额、
Anthropic/Responses 客户端的 402 语义如何对齐），**审计代理不代拍**。

⇒ **并入待裁决 79（三面预算闸需产品裁决）**，并把上面的**逐面矩阵**作为裁决依据补进去：
原裁决项说「三面」，实际情况是「**六个数据面里两个没有闸**」，Gemini 不在其中。

**没有新建门**，理由是本轮已经踩过一次同族坑（211 号 F3）：
`domains/streaming` **不在** `GUARD_PACKAGES`（`Makefile:58` 只有 10 个 `./internal/*` + `./sql/schema`）
⇒ 写在那里的门**永远不会在 `make guards` / CI 上跑**，等于「只在手工执行时绿的门」。
要让这条覆盖面将来不被漏掉，正确做法是**先把 `domains/streaming` 纳入守卫登记并单独量预算**
（参照 216 号量 `sql/schema` 的做法），**再**加覆盖面门 —— 这是下一轮的事，本轮只登记。

---

## 五、报告其余发现的处置（不逐条复现）

5 条发现中：F2（DB 抖动时先等 5s 再 503）、F3（新钉的 3s 前置守卫在高负载 CI 可假红）、
F4（同一 ZSET 混两种 score 粒度）、F5（滚动升级窗口）均属**低-中危且依赖外部证据**
（生产 `LeaseTTL` 配置面、真实升级过程），本轮**没有**重新核 ⇒ 明确记为**未做**，不是「核实通过」。
9 条证伪（Z1–Z9）质量很高，本轮不逐条复核，**只采用其中一条已独立验证的**：
Z5「不中断在途请求」与我在 §二的定位一致（闸在 `serveHTTPInner` 早期、路由前）。

---

## 六、诚实边界

- **未起真进程、未执行任何 SQL、未连 PG / Redis / Docker。**
- 本轮**零代码改动**；§二 的逐面结论是**代码路径静态坐实**，**没有起真进程**对每个面
  打一次超预算请求去看是否真的 402 ⇒ 「messages/responses 无闸」在**代码层**确定，
  但「生产上确实有租户因此超支」**未取数**。
- ⚠️ `count_tokens`（`main.go:6364` `NewCountTokensHandler(chatHandler)`）**本轮未核**
  —— 它是否需要配额闸、是否走 chatHandler 路径，**未查**。清单里不列它，避免把「未核」写成「无闸」。
- 本轮**没有**重核 F2–F5（见 §五）。
- `scripts/checks/guards-sync.sh` 仍未跑过（本机**无 `bash`**）；
  `core.hooksPath` 未设置 ⇒ 推送**未经 pre-push 门**；**CI 仍未运行**。
- 215 号的 N1/F3、216 号并入 81 的那条，本轮**未动**。

---

## 七、playbook 新增

### §155 **「全仓只有 N 处」是**计数断言**，会因别人合入新代码而过期 —— 必须自己数一遍

报告用「全仓非测试 `CheckBudget(` 调用点**仅此两处**」作为 F1 的核心证据。
它成文时正确；**214 号那次入站合并引入 `audio_service.go` 后变成三处**。

⇒ 复核**任何以计数为证据的结论**时，先问「**这个 N 是什么时候数的**」——
  如果基线比 HEAD 老，**第一件事是自己重数**（而不是采信报告里的 N）。
⚠️ 特别地，「**只有 N 处**」比「**有 N 处**」更危险：
  多出来的新调用点往往**没有跟上同样的配套**（配套：fail-closed 语义、观测、测试），
  而「有 N 处」多一处的风险只是覆盖面变大。
⚠️ 本轮还因此踩了一个工具坑：`Select-String -Path 'domains/**/*.go'`
  **不支持 `**` 递归通配** ⇒ 它的结果不可信 ⇒ 换 `grep` 工具（真递归）才拿到正确的三处。
  **凡是用 glob 猜路径的搜索，先确认它真的递归了。**
  与 §152（先查是不是已被别人修了）互补：那条管「结论是否已被推翻」，
  这一条管「证据里的数字是否已被推翻」。

### §156 同一个位置可以连续两轮各错一次，方向还相反 —— 所以它是**结构性盲点**不是疏忽

`handler_gemini.go` 的「独立 `ServeHTTP`」这一处：
- **215 号**：报告据此判「Gemini 零脱敏」（实际**有**脱敏）；
- **217 号**：另一份报告据此判「Gemini 无配额闸」（实际**有**闸）。

两次都是**在同一个位置停住**，一次看错成「漏」，一次看错成「有」。
⇒ 这不是粗心，是**结构性的**：那一行的字面意思（自己实现了 `ServeHTTP`）确实支持那个读法，
  误导藏在**下一层**（它把请求交回 chatHandler）。

⇒ **判别方法**：看到「某 handler 自己也实现了 `ServeHTTP`」时，
  **下一句必须问「它最终把请求交给谁 / 转发给哪个 handler」**——
  包装型 handler（chat/messages/responses/gemini 都属此类）**几乎一定会复用底层 handler**，
  它们的独有部分通常只到「协议适配」为止。
⇒ 与 §150 互为印证：**「顺着数据流走到底」这条纪律，本轮又救回一条真实结论。**
