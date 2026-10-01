# 82-R88-s：HTTP 响应体句柄泄漏审计与 `bodyclose` 选型

- 轮次：R88（收口项）
- HEAD 基线：`6cf770af8`
- 触发：objective 明确点名「内存或句柄泄漏」
- 结论：**0 泄漏（已双重独立取证）；`bodyclose` 不予启用（已登记待裁决第 34 条）；配置内已写入原因注释**
- 改动：仅 `.golangci.yml` 注释（**零生产代码、零门、零行为变化**）

---

## 1. 一句话结论

本仓**不存在 HTTP 响应体句柄泄漏**。更重要的产出是：**成熟工具 `bodyclose` 在本仓是负收益**——它在本仓报出的 3 条经逐条读包围函数后 **3/3 为误报**，而本仓真正的主出口路径**结构性地落在它的盲区之外**。已在 `.golangci.yml` 写入原因注释，避免下一位执行者重跑一遍本轮的全部取证。

---

## 2. 取证方法：两条互相独立的路径

**为什么必须两条**：单一方法的漏检面就是它的取样面（见 §6.2，我自己的方法一就有漏检）。

### 2.1 路径 A —— 成熟工具的 SSA/dataflow 分析

```
golangci-lint run --no-config --default=none --enable=bodyclose ./...
→ exit 1，3 issues，EXIT 已取 ${PIPESTATUS[0]}
```

`--no-config` 是刻意剥离仓内 `exclude-rules`，避免存量排除项把结果洗白。工具版 `v2.13.2`。

全仓 3 条，**无遗漏地全部取回**（`max-issues-per-linter: 0` 等价语义已在 `--no-config` 下由默认值给出，输出尾部 `3 issues` 与逐条一致）。

### 2.2 路径 B —— 手工面枚举（用于证伪 + 补工具盲区）

| 面 | 生产代码计数（排除 `_test.go` / `vendor`） |
|---|---|
| `X.Do(` | 339（含 pgx `conn.Do` 等非 HTTP，**不作为泄漏证据**） |
| `http.Get(` | 2 |
| `http.Post(` | 1 |
| `http.ReadResponse(` | 1 |
| 关闭面 `ReadPrefixAndDrain(` | 12 |
| 关闭面 `DrainAndClose(` | 2 |
| 关闭面 `.Body.Close()` | 198 |

---

## 3. 三条 bodyclose 命中的逐条定性

**判定纪律**：不采信工具结论，逐条打开包围函数读（conventions §10）。

### 3.1 `domains/analysis/openai_client.go:192` —— 误报，且是最关键的一条

```go
resp, err := c.httpClient.Do(req)          // :192  bodyclose 在此报错
if err != nil { return err }
responseBody, bodyErr := httputil.ReadPrefixAndDrain(resp.Body, maxAnalysisResponseBodyBytes)
```

`pkg/httputil/body.go:15-30` 的 `ReadPrefixAndDrain` 第 28 行是**无条件** `closeErr := body.Close()`，且第 16 行先挡 `body == nil`，第 23 行 `io.ReadAll(io.LimitReader(...))` 读前缀、第 27 行 `io.Copy(io.Discard, body)` 排空尾部，三个错误 `errors.Join` 一起返回。

⇒ **body 完全正确关闭**，只是 bodyclose 看不见自定义 helper 里的 `Close()`。

### 3.2 `internal/agent/wsclient/client.go:219` —— 误报，且是工具盲区的反证

```go
resp, err := http.ReadResponse(br, req)     // 裸 TCP 上的 WebSocket 握手
```

- 成功路径（`101 Switching Protocols`）：Go 把 `resp.Body` 设为**透传** bufio+conn 的 `readWriteCloserBody`——**关闭它等于关闭 socket**。代码**刻意不关**，而是把 `br` 与 `conn` 交给 `readLoop`/`writeLoop` 长期持有。**关掉才是缺陷。**
- 失败路径（`ReadResponse` 出错 / 状态码非 101 / `Sec-WebSocket-Accept` 不匹配）：三处均显式 `conn.Close()`，底层 socket 随之关闭。`resp.Body` 未单独关闭**不产生句柄泄漏**（无 drain 复用问题，连接本就即将废弃）。

⇒ 0 泄漏。**这是本仓自定义取响应面的典型形态：bodyclose 能报出来（因为 `http.ReadResponse` 在它的已知 API 表里），但报出的原因是错的。**

### 3.3 `bg/scan_scheduler_budget_test.go:33` —— 误报

```go
_, err := client.Get(srv.URL)   // 测试用例断言 err != nil（150ms 超时）
```

Go 标准库契约：`http.Client.Do` 返回错误时 `Response` 为 nil（`CheckRedirect` 失败时虽返回非 nil `Response`，其 `Body` **也已关闭**）。此处断言 `err != nil` 后立即 `t.Fatal`，**不存在可泄漏的 body**。

---

## 4. 跨函数盲区：`bodyclose` 看不见本仓主出口

`bodyclose` **不跨函数追踪数据流**。而本仓的主出口链路是**三层自定义包装**：

```
doCompactionHTTP(req)          (*http.Response, error)   context_summarize.go:576
  └─ doCompactionUpstream(...) (*http.Response, error)   context_summarize.go:456
       └─ invokeOpenAISummarize / invokeAnthropicSummarize  ← 终端消费者
```

逐层落笔核实（不靠工具、不靠上一轮结论）：

- `doCompactionHTTP:576-588`：`e.Upstream.Do(req)` 成功即 `return resp, nil`；否则回退 `http.DefaultClient.Do(req)`。
- `doCompactionUpstream:456-542`：两层都**只透传不关闭**；其间夹着熔断记账（5xx/429 `RecordFailure`、<400 `RecordSuccess`、4xx `ReleaseProbe`）。
- 终端消费者 `:388-390`（openai）与 `:427-429`（anthropic）**结构完全同构**：

```go
resp, err := e.doCompactionUpstream(ctx, params, cand, payload, false)
if err != nil { return "", err }
//nolint:errcheck // best-effort close
defer resp.Body.Close()
body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
```

⇒ **正确关闭**。这条链是「两层都不关、终端才关」，正是 bodyclose 看不见的形态；它在本仓报 0 条，**不是因为干净，是因为看不见**。

> 顺带排除一个我差点提的伪缺陷：两处 `Close()` 前**没有 drain**。是否影响连接复用？`maxBodySize = 128 << 20`（`executor.go:60`），而 summarize 响应是摘要级体量，`LimitReader` 实际读到 EOF ⇒ 连接正常归还连接池。**触发条件不可达 ⇒ 不作为发现登记**（与 R88-r 证「封顶分支从未命中」同一纪律：不可达的缺陷不是缺陷）。

---

## 5. 选型结论：**不启用 `bodyclose`**

原设想是「仓内已有 `golangci-lint` 且 CI 已在跑（`sessionforensics-ci.yml:201`），`bodyclose` 是成熟工具，启用只需一行」。三项证据推翻：

| 判据 | 实测 |
|---|---|
| **信号量** | 全仓 3 条，**3/3 误报**。真信号 0。 |
| **误报面** | 命中的第 1 条**正是本仓标准写法**（`ReadPrefixAndDrain`，生产代码 12 处）。启用后凡按标准写法新增的出口都会假报。 |
| **触达面** | 主出口三层包装链完全在其盲区。**"主路径没覆盖、helper 写法却报"**。 |

### 5.1 无法豁免：bodyclose 无 skip 能力，且配置错误是静默的

我先尝试用 `settings.bodyclose.skip` 豁免 helper：

```yaml
settings: { bodyclose: { skip: ["ReadPrefixAndDrain"] } }
→ 无配置报错，但 **告警照旧出现**（1 issues）
```

反向对照（判定「无报错」不是「配置被接受」的证据）：

```yaml
settings: { bodyclose: { totally_bogus_key_xyz: 1 } }
→ 同样无任何配置报错
```

⇒ **`golangci-lint` v2.13.2 静默接受 `settings.<linter>` 下的未知键**：写了 `skip` 不报错、也不生效。**「配置没报错」在本工具上不是配置正确的证据**——必须用「告警是否消失」来判定。这条已写入 conventions §11.1。

**没有 `exclude-rules` 逃生口**：`issues.exclude-rules` 的 `path`/`text`/`source` 匹配的是**告警所在行**（此处是 `resp, err := c.httpClient.Do(req)`），**不含**下一行的 helper 名，匹配不到。

⇒ 唯一「解法」是给每个 `Do` 行加 `//nolint:bodyclose`。**这是比不开更坏的门**：注释一旦写下就与该行长期共存，将来有人把 helper 调用换成直接读取、`nolint` 仍在，**门静默失效且无任何信号**。按 conventions §9（判据自身不腐化 + 变异验证），拒绝这种门。

### 5.2 已落地的动作（行为中性）

`.golangci.yml` 的 `linters.enable` 上方写入 10 行注释，记录上述实测结论与「若将来确要启用，须先让 bodyclose 认识 `ReadPrefixAndDrain`」的前提。**纯注释，不改 linter 集合**。已实测验证：

- 用仓内配置跑 `./domains/analysis/...` → YAML 正常解析，退出码语义与改动前一致；
- 用仓内配置跑 `./domains/streaming/executors/...` → `bodyclose` 命中数 **0**（确认未进入清单）；
- 该包那 2 条 `staticcheck` 是既存 legacy，CI ratchet（`--new-from-rev`）模式下不判红，与本改动无关。

---

## 6. 本轮的三次自伤（如实登记）

### 6.1 「假 0 命中」第 11 次 —— 新变种：正则语法错误伪装成"不存在"

第一次数取响应面时全部返回 0：

```bash
for pat in '\.Do(' 'http\.Get(' ...; do grep -E "$pat" ...
```

`-E` 下 `(` 是**分组起始符**，模式以未闭合的 `(` 结尾 ⇒ **grep 直接报错退出**，管道里得到空串，`wc -l` 得 0。**"0" 其实是"命令失败了"。**

⇒ 与 §10.2 记录的「假 0 命中」前 10 次同族，但根因又不同（前 10 次是搜错范围/模式写错/截断，**这次是模式本身非法**）。

**教训**：`grep` 的 0 命中必须区分「模式非法」「路径不存在」「真的没有」三种。**用 `[()]` 而非裸 `()`**，或先 `echo "$pat" | grep -qE "$pat"` 验模式本身合法。

### 6.2 路径 A 自己的取样面

我上一轮口述的「139 个 `client.Do()` 取响应点，三种关闭路径全部命中，0 泄漏」——**该取样面漏了 `http.ReadResponse`**（本仓 `wsclient` 就在用），也没覆盖跨函数包装链。本轮用 `bodyclose` 独立复算才把 `http.ReadResponse` 这条面补进来（§3.2）。⇒ 上一轮结论**方向正确但取证面不完整**，不能仅凭它下"0 泄漏"的定论。

### 6.3 差点提一个不可达的伪缺陷

见 §4 末尾（`Close()` 不 drain + 128MB 上限）。已按「触发条件不可达 ⇒ 不登记」处理。

---

## 7. 变更清单

| 文件 | 改动 | 性质 |
|---|---|---|
| `.golangci.yml` | `linters.enable` 上方新增 10 行原因注释 | **纯注释**，linter 集合未变 |
| `docs/全面审计v3/README.md` | 追加本条 | 索引 |
| `docs/audit/playbook/conventions.md` | 新增 §11.1（配置类工具的"没报错"不等于"生效"）、§11.2（选型要量触达面与误报面，而非覆盖类别） | playbook |

**零生产代码改动。零新门。零 CI 行为变化。**

---

## 8. 待裁决

### 第 34 条（新增）：是否用其他手段覆盖 HTTP 响应体闭合

**本轮不建议现在做**。理由：当前 0 泄漏、且三条关闭路径（`ReadPrefixAndDrain` / `DrainAndClose` / 直接 `Body.Close()`）都已有覆盖。若后续仍想要回归保护，可选：

- (a) 维持现状 + 本注释（本轮推荐）；
- (b) 写一个**理解本仓 helper** 的自定义守卫——但按 §9 必须先证明它不会在 helper 换名/换实现时腐化，收益/成本比需重新评估；
- (c) 等上游 bodyclose 支持豁免函数后再启用。

**明确未做**：未审 `http/2` 与 `websocket` 之外的自定义帧解析器是否持有未释放的 conn；未在真库/生产日志层面统计连接池 `IdleConn` 与 `ActiveConn` 的长期走势（需运行时数据，非本轮手段）。

---

## 9. 交叉引用

- conventions §10.2（先定位函数体再决定搜什么名字）——本轮 §6.1 是该家族新变种
- conventions §9（守卫准入四组）——本轮拒绝 `//nolint` 方案的依据是「判据自身不腐化」
- 82 号 §5 对照 [[门全绿≠门覆盖我]]：bodyclose 绿，但没覆盖主路径
