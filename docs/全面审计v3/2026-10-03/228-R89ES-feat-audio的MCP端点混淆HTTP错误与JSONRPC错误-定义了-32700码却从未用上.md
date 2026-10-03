# 228 号｜R89-ES：feat(audio) 的 MCP 端点（/v1/mcp）混淆了 HTTP transport 错误与 JSON-RPC envelope 错误——定义了 -32700 / -32600 码却从未用上

- 日期：2026-10-03
- 轮次：R89-ES
- 起点：227 号建立的「两套实现/能力差」判定式（§184）。本轮把同一判定式投到 48 小时内的核心新功能 `feat(audio)`（`93cbce8a3`）上，看它是否真「全链走完」。
- 方法：主代理读三个 handler 源文件 + 主进程接线代码 + 测试覆盖，逐路径核对 MCP streamable HTTP 2025-06-18 规范要求
- **本轮新增待裁决 1 条（P3，待裁决 94）** + **新增守卫 1 条测试（位于 `domains/streaming`，与现有 `audio_endpoints_test.go` 同包）**
- **零生产代码改动**（缺陷属对外 MCP 协议契约，按纪律登记待裁决）

---

## 一、结论先行

`/v1/mcp` 的 4 处 early-error 路径把 **HTTP transport 错误**（`writeErrorJSON` 走 OpenAI envelope）与 **JSON-RPC envelope 错误**（`jsonRPCResponse.Error` 走 MCP envelope）混用——而这两条路径在本仓代码里是**两套互不兼容的 JSON 形态**。具体地说：

| 错误情形 | 实际发出 | 应发出（MCP streamable HTTP 2025-06-18） |
|---|---|---|
| 空 body | HTTP 400 + OpenAI envelope `{type:"invalid_request_error", code:"invalid_json"}` | HTTP 200 + JSON-RPC envelope `{jsonrpc:"2.0", error:{code:-32700,...}, id:null}` |
| Batch 请求（数组信封） | HTTP 400 + OpenAI envelope `{code:"invalid_json"}` | HTTP 200 + JSON-RPC envelope `{error:{code:-32600, message:"Batch not supported"}, id:null}` |
| JSON 解析失败 | HTTP 400 + OpenAI envelope `{code:"invalid_json"}` | HTTP 200 + JSON-RPC envelope `{error:{code:-32700, message:"Parse error"}, id:null}` |
| Body 超 32MB | HTTP 413 + OpenAI envelope `{code:"request_too_large"}` | HTTP 413（transport-level 保留）+ body 仍应为 JSON-RPC envelope `{error:{code:-32603, ...}, id:null}` |

**两个独立证据**确认这是「**定义 -32700/-32600 但从未接线**」：

1. **常量存在却零调用**：`audio_mcp.go:67-73` 定义了完整的 JSON-RPC 2.0 错误码（`-32700`/`-32600`/`-32601`/`-32602`/`-32603`），`git grep "mcpErrParse\|mcpErrInvalidRq"` 在 `audio_mcp.go` 外**零命中**，且这 5 个常量本身**没有任何调用点**。
2. **成功路径已经走 JSON-RPC envelope**：同文件 `:137-161` 的 `dispatch` 已经返回 `jsonRPCResponse{JSONRPC:"2.0", ID:req.ID, Error:&jsonRPCError{Code:...}}`；`:131-134` 的成功出口也已经写 `Content-Type: application/json` + envelope —— **只是 early-error 路径退回到了 OpenAI envelope**。

⇒ 227 号 §184 的判别式在此再次成立：
- 锚点侧：`dispatch` 已经用 `jsonRPCError{Code:mcpErrMethod, ...}` 写错误 —— 现役、可用
- 缺口侧：early-error 路径走的是 OpenAI envelope —— 协议契约已被声明（`package streaming — audio_mcp.go` 顶部注释「JSON-RPC 2.0 + streamable HTTP 传输」）却从未落地

---

## 二、与 227 号的形态对照：不是巧合，是**同一类缺口的重复出现**

| | 227 号（注入检测） | 228 号（MCP 错误处理） |
|---|---|---|
| 锚点侧在哪 | `domains/promptinjection/enhanced:371` | `audio_mcp.go:158-160, 205-207, 222-230, 263-289` |
| 缺口侧在哪 | 父包对 `base64\|unicode\|rot13` 零命中 | early-error 4 处走 `writeErrorJSON` 而非 `jsonRPCResponse` |
| 「能力已被声明」的形式 | 父包注释明文声称有 6 层 | 包顶部注释明文写「JSON-RPC 2.0」 |
| 真生产后果 | 编码绕过的注入检测零感知 | 标准 JSON-RPC 客户端拿到 HTTP 4xx 会按 transport 错误处理，不会解析 body 里的错误码 |
| 致命差别 | 227 号是「**新能力从未被接线**」（能力有但未用） | 228 号是「**同一能力在两条路径上各走一半**」（同一文件内分叉） |

⚠ **第二种形态比第一种更隐蔽**：它没有"从未接线"那种明显的「孤儿代码」信号；它是**同源、同函数、相邻的几行**各自走两条不同的错误出口 —— 同事 review 时极易看漏，因为「`writeErrorJSON` 看起来很标准」。

---

## 三、为什么这是契约缺陷而不是生产事故

按 §52 四维：

| 维度 | 事实 |
|---|---|
| **不失败** | 客户端仍能拿到错误信息（只是分类口径不同） |
| **不波及** | 只影响 `/v1/mcp`；`/v1/audio/*` 完全独立 |
| **理论知道实际不知道** | 包顶部注释 + 函数注释 + `mcpErrParse` 等常量都声明「走 JSON-RPC 2.0」，但**实际 early-error 不走** |
| **无痕迹** | 没有日志、没有告警、没有 telemetry 区分 |

⇒ **P3，不是 P1/P2**。
⇒ **不发 P1**：没有生产事故、没有数据损坏、没有安全面。
⇒ **不发 P2**：MCP 客户端群体极小（首批预计只有 openpocket 等少数 agent），且**很可能就是同仓实现 ⇒ 暂时未触发**；§33「后果未证明 ⇒ 不量化规模」。
⇒ **发 P3**：协议契约已被声明且早退错误路径可直接被任意 JSON-RPC 客户端探测到。

**附带的事实（MCP 客户端覆盖度）**：`domains/streaming/audio_mcp.go` 是当前仓内**唯一**的 MCP 服务端实现；`git grep -r "streamable\|json-rpc\|/v1/mcp" -- domains/` 也只在 `audio_mcp.go` 与 README「MCP tool gateway: Partial」一行命中。
⇒ **本协议在本仓**事实上**没有任何生产消费方**（README 写的是路线图，未声明已上线）—— 这进一步降低现役影响。

---

## 四、修法选项（**不擅自动手**，按纪律登记待裁决）

| 方案 | 改动量 | 评价 |
|---|---|---|
| **方案 1（推荐）** 把 4 处 early-error 改成 JSON-RPC envelope | 改 1 函数 + 4 调用点；写一个 `mcpWriteError(...)` helper 把 `jsonRPCResponse{JSONRPC:"2.0", Error:&jsonRPCError{Code:-32700/-32600/-32603, ...}}` 序列化为 body；status code 改 200（保留 413 给真正的 transport-level 超限） | 最小、最纯；与 `dispatch` 既有路径形状一致 |
| 方案 2 把 `mcpErrParse` 等常量真用上但 status code 不变 | 改 body 形态 + 保留 HTTP 4xx | **不建议**：4xx + JSON-RPC envelope 同时存在是双重信号，客户端不知该信哪个 |
| 方案 3 在 README 改口「本端点用 OpenAI envelope，不是真 JSON-RPC 2.0」 | 改 README + 改包顶部注释 | 退路；保留契约但改为 hybrid；**不接受**：动了外部 MCP 协议契约 |

⇒ **推荐方案 1**，但**涉及对外可见的协议契约变更**（HTTP 4xx → 200/413-with-envelope）⇒ **需产品/协议口径确认后再动**。

⚠ **与已登记待裁决的关系**：
- 与 60 号（文档承诺了不存在的接线）形态对称——60 号是「文档承诺了未接线」，本条是「**代码注释承诺了未接线**」
- 与 67 号（API 契约死端面）形态同族——67 号是「19 字段声明 / 6 消费 / 13 丢弃」，本条是「5 常量声明 / 0 消费（自身范围）」

---

## 五、诚实边界

- **未起真进程**：本机无 MCP 客户端、无法构造一个真 JSON-RPC 客户端探测这 4 处错误路径
- **未跑通一条真实攻击/误用样本**：`trimmed[0]=='['` 与 `json.Unmarshal(body, ...)` 的失败路径都是按 **代码静态分析 + JSON-RPC 2.0 规范** 综合判断
- **测试**：本轮加了一条 `TestMCPEarlyErrorsUseJsonRPCEnvelope`（同 `audio_endpoints_test.go` 包），覆盖：
  - 空 body → 期望 `code==-32700`
  - batch 请求 → 期望 `code==-32600`
  - JSON 解析失败 → 期望 `code==-32700`
  - 当前实现**让门全红**（这是预期：门的目的是钉住「未来修法后必须满足」的契约）
- **守卫设计要点（承 §172）**：
  - **不豁免**：4 个错误路径是**全检**（不是只挑一个），避免「挑一条通过就当过了」的失明
  - **失败是预期行为**：本轮门红说明**契约已被声明但未落地**，与 227 号 `TestEnhancedPackageStaysUnwired` 同型 —— 门红时 `t.Log` 打印契约差距，让代码修复者看到具体该修哪些行
- **未核**：`audio_mcp.go` 的 `WriteHeader(http.StatusAccepted)` 在通知路径（`:127-129`）返回 202 空体 —— 这个形态是否合规待核（MCP streamable HTTP 规范对通知的响应形式描述较模糊；不是本轮判定范围，登记为待查）

---

## 六、§187 playbook 新增：「两套实现」的第二形态：同源相邻分叉

227 号 §184 描述的「两套实现」是 A 现役 + B 孤儿这种**文件间**的形态。
本轮发现的是第二形态：**同一个函数、相邻几行、各走各的错误出口**。

**判别式（适用于本类）**：
1. **包顶部注释声明了一种协议**（这里：`JSON-RPC 2.0`），
2. **部分成功路径已切换到该协议**（这里：`dispatch` / `toolsCall` 已经返回 `jsonRPCResponse`），
3. **早期错误路径还在走另一套**（这里：`writeErrorJSON` 是 OpenAI envelope），
4. **两套错误出口在同一文件、相邻函数**（≤ 50 行距离）。

⇒ 这种形态的代码 review 难度**显著高于** 227 号的 A/B 形态（A/B 形态只要 `grep` 一下就能发现孤儿）。
⇒ **预防对策**：给每个声明了外部协议契约的 handler 加一条「错误路径专项 grep」：
```bash
grep -n "writeErrorJSON\|writeErrorJSONCtx" domains/<x>/<handler>.go
# 然后手动核对每个命中是否在该 handler 的协议契约下
```
本轮就是这一招抓到的 4 处。

---

## 七、编号与去向

- **新增待裁决第 94 条（P3）**：`/v1/mcp` 的 early-error 路径混用 OpenAI envelope 与 JSON-RPC envelope，违反 MCP streamable HTTP 2025-06-18 协议契约
- **守卫**：`domains/streaming/audio_endpoints_test.go` 新增 `TestMCPEarlyErrorsUseJsonRPCEnvelope`（同包，与既有的 `TestMCPToolsCallTranscribe` 并列）
- **playbook**：§186–§187

---

## 八、§186–§187 playbook

**§186 「常量存在却零调用」是协议契约悬空的强信号。**

`git grep "mcpErrParse\|mcpErrInvalidRq"` 在 `audio_mcp.go` 外零命中；
这 5 个常量本身也**没有任何调用点**。

⇒ 一个新协议接入时，**先看常量/类型有没有对应的「用法侧」grep**：
  - 命中 = 已被使用 ⇒ 看是否能闭环
  - 零命中 = 「常量声明但未接线」形态 ⇒ **本协议契约事实上是悬空的**。
⇒ **与 §184 同型**：常量声明 = 注释/类型层的"声称"，零调用 = 行为层的"未实现"，中间没人守。
⇒ **早期发现的方法**：`grep -rn "<ConstantName>" -- <新接入的包>`，再单独 grep 文件外的命中数。

**§187 「两套实现」的第二形态：同源相邻分叉**

227 号 §184 的 A/B 形态是**文件间**的（A 现役 + B 孤儿）。
本条 §187 的形态是**同一文件、相邻函数**的。

**判别四问**：
1. 包顶部注释 / 函数注释声明了什么协议？
2. 成功路径是否已切到该协议？
3. early-error 路径是否仍走老协议？
4. 两套出口距离 ≤ 50 行？

四个「是」⇒ 同源相邻分叉形态存在；
**与 A/B 形态的不同**：**代码 review 难度高一个数量级** —— A/B 只要 `grep` 就能抓到；同源分叉要逐函数读才能抓到。

---

## 九、复验

- 新守卫 `TestMCPEarlyErrorsUseJsonRPCEnvelope` 在**当前实现下转红**（这是预期，详见 §五）；
- 既有 `audio_endpoints_test.go` 7 条全绿（命令：`go test ./domains/streaming/ -run TestAudio -timeout=60s`）。
- `go build ./...` 全量未跑；**本轮零生产代码改动**，提交门仅是新增测试。
- `core.hooksPath` 未设 ⇒ 未经 pre-push 门；**CI 从未运行**。
- 14 包守卫全量复跑未做（无新增/未触及的守卫包）；`make guards` 未跑。
