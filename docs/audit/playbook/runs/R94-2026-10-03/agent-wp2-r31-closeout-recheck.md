# WP-2 R31收口修复复审 子代理报告（窗口 a0da9066d..HEAD）

复审对象：commit 38497afed（2026-10-02 23:41，8 文件 +433/-14）。全部结论基于当前 HEAD（57f65f7c6）只读核查。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | **P2 候选** | 「已知边界」只声明了 OpenAI 带前导 system 一种 fail-open 形态，同一结构性 fail-open 类更宽：**Anthropic 任意形态**（含打标成功的形态）在压缩后下一轮 `findDeltaAnchor` 也必失配。`hasGatewaySummary` 仅由 `isSummaryMarkerMsg` 扫 `messages[]` 各消息 content 得出，而 Anthropic 摘要在**顶层 system 字段**，永远不在 messages[] ⇒ 恒走「无摘要→严格前缀匹配」分支，要求 `clientMsgs[:len(tail)] == tail`；但压缩产物保留的 tail 是客户端重发历史的**后缀**而非前缀 ⇒ 每轮 fail-open 全量重发+重压缩。另外 **Anthropic system 为非空字符串形态时 `injectSummaryMarker` 静默 no-op**。影响面：Anthropic 车道全部压缩会话每轮重付摘要/裁剪（成本问题，无数据丢失）；本轮修复的 delta 增益在 Anthropic 车道结构性拿不到 | domains/hooks/compression/diff.go:195-227/367-391、session_compressor.go:1234-1239、rebuilder_anthropic.go:104-108、diff_test.go:298-313（仅 block 形态有打标钉）、diff_test.go:140-154（夹具无丢弃段） | 升级为登记项并入 diff 引擎域专项（已回注 D03 域文档） |
| 2 | P3 | 38497afed 提交信息把 sessionv2mirror/terminal_failure_gate_test.go、validate_sessions_v2/dual_write_value_parity_integration_test.go 列为本提交「钉测」，但两文件不在本提交改动集内且主题无关 | git show 38497afed --stat | 订正记录即可 |
| 3 | P3 | Responses 出向对称 loss 矩阵两潜伏漏臂（当前经在役桥不可达）：① Reasoning.Type 指令词汇（disabled 语义反转）无 loss 事件；② MaxReasoningTokens 全仓无 parser 赋值 | internal/ir/serialize_responses.go:562-586/803-817、parse_gemini.go:593-597 | 登记，不本轮堵（补臂只增噪声）——已回注 D02 域文档 |
| 4 | P3 | `reportOllamaReasoningLoss` 五臂中 max_reasoning_tokens 臂无任何钉测，腐化时全部测试仍绿 | internal/ir/serialize_reasoning_loss_pin_test.go:57,64；serialize_ollama.go:219-223 | 补第五臂（本轮已落地） |
| 5 | Info/P3 | Recover persist 中 prefix.Stabilize 失败时 CompressedPrefixHash 留空，state 继承 prev 旧 body 的 hash（CommitFinal 同场景先清空再算，Recover 未对齐）；Stabilize 极少失败且下一轮 CommitFinal 纠正 | domains/hooks/compression/recovery_coordinator.go:284-286；session_compressor.go:1035-1037/1004-1007 | 低优先登记 |

## 二、核实为健康的面

- **D03-① Recover persist 走 buildSessionState（属实）**：recovery_coordinator.go:273-291 五字段+两时间戳全同步；marker 口径自洽（injectSummaryMarker 返回值派生 BuildSummaryMarker(content)，体内 presence 恒真）；机械分支确无打标路径；注入失败降级安全；摘要回读闭环（cachedSummaryText 剥两层前缀）与注入格式互逆。
- **D03-② 线协议契约（OpenAI 车道属实）**：Recover 与主动路径同一函数、同一前缀常量；OpenAI 摘要消息形态满足 isSummaryMarkerMsg；findDeltaAnchor 识别链被端到端钉测锁死。
- **D01 typed-nil PSOR（属实，消费侧全兼容）**：导出条件唯一；写入侧无空非 nil 形态；四消费方（recovery/compressor/mirror/cache_v2 摄入）逐一核对 presence-lookup 兼容。
- **D02 loss 上报**：Ollama 五臂齐全且 SerializeOllama 确不消费 req.Thinking/Reasoning；Responses budget_tokens 臂带负例对照。
- **测试 4+14+1 全绿；三包全量 ok**。钉测质量：期望值独立重算无自证、双策略表驱动、负向 Contains 唯一串、typed-nil 双向、隔离去重态。

## 三、未覆盖项与原因

1. 变异复验未重跑（只读约束）；依据钉测断言与变异面的逻辑对应 + commit 自述。
2. 发现 1 的 Anthropic fail-open 未做可执行复现（现有测试无一覆盖「anthropic + 有丢弃段」delta 形态）——代码推演，置信度高，留给 diff 引擎专项钉测落实。
3. sessionv2mirror、validate_sessions_v2 两包未跑（本提交未触碰）。
4. DeserializeResponsesRequest 反向桥接端到端行为未深审（属 vapeur 域）。
5. 语义缓存对 CompressedPrefixHash 的消费链未深追。
