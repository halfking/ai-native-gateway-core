# R66 Entry ② 批判式审计 Handoff（2026-09-25）

> 审计对象：原提交 dc5f22246（R66 entry ② ollama 流式契约）· 审计轮：R66 接力批
> 推送结果：origin/main = codeup/main = `8e8141109`（0/0 同步）· 备份分支已清理（旧树 af7b6cd16 留 reflog）

## 一、结论 / 根因

**结论**：dc5f22246 的**提交信息虚构了交付面**——声称的"三档健化"三项（expectThinkingDone/handleBareContent 桥、chunkLines→handleBuf 复用、stream.go RecycleIR 缓冲回收）及三条对应测试（think-without-close/bare-content-after-done/重入解析边界、HandleBuf_ResetAfterFinalize）经逐项 grep 核实**在代码中零存在**。

**真实交付**（本身合法且已验证）：ollama 流式 content 契约由累计（cumulative）反转为增量（incremental），含注释反转、`CumulativeContent`→`StreamDelta.Content` 代码路径翻转、测试断言翻面 + `MultiFrameIncremental` 端到端钉桩、stream.go 字段注释勘误、R66 entry ② 验证报告。验证报告内容与真实交付一致、准确。

**根因**：提交信息按"计划中/想象中的工作"撰写，未与实际 diff 对账；verify 报告与代码同commit落库但无人交叉核对 message↔diff 一致性。教训入册：**commit message 是审计对象，不是元数据豁免区**——轮末验证门必须含"git show --stat 与 message 声称逐项对账"。

**处置**：改记录、不造假代码。两提交均未推送 → 安全 amend 重写 message 为实际交付面（新 SHA `985d23ae9`），顺带修正审计中发现的一处注释矛盾（serialize_ollama.go §3 thinking "(cumulative)"）；merge 重建（996ca243d→8e8141109 两次，因并行会话两轮推进远端）；树一致性校验确认相对原树仅两处有意增量；三门全绿后推送。**未**为虚构声明补实现——向谎言对齐代码是第二次造假。

## 二、改动文件与关键行为（最终推送形态）

| 文件 | 行为 |
|---|---|
| internal/ir/parse_ollama_stream.go | CONTRACT 注释反转（cumulative→incremental，附 WebFetch 证据与可逆性论证）；content 帧走 `StreamDelta{Content, DeltaType:"text"}`；CumulativeContent 恒空（R52 RESERVED 不变量保持） |
| internal/ir/parse_ollama_stream_test.go | Delta/DoneWithContent 断言翻面；新增 `TestParseOllamaStreamChunk_MultiFrameIncremental`（4 帧拼接还原 "Hello world!"） |
| internal/ir/stream.go | CumulativeContent 字段注释勘误（移除 Ollama 累计型举例） |
| internal/ir/serialize_ollama.go | §3 注释 thinking "(cumulative)" → "per-frame delta when streaming; single complete value when not"（本次审计新增修正） |
| docs/audit/verify/R66-entry2-ollama-parser-incremental.md | entry ② 验证报告（七子声明全 TRUE）+ 后审计记（本次审计新增，记录 message 不符事实与处置） |

关键行为不变量：`ParseOllamaStreamChunk` 零生产调用方（P4 未接线）——翻转无生产影响；全仓 `CumulativeContent` 生产消费者为零（本轮 grep 复核）。

## 三、测试命令与结果（实跑）

```
go test ./internal/ir/... -run 'Ollama' -count=1 -v   → 全 PASS（1.556s，parse+serialize 全套）
go test ./internal/ir/... -count=1                    → ok 0.605s / 复核 ok 4.649s
go build ./...                                        → exit 0
go vet ./internal/ir/...                              → exit 0
go test ./internal/ir/... ./bg/... -count=1           → ir ok；bg 32.227s 全 ok（并入远端 P1 根修后复核）
pre-commit 钩子（amend 时实跑）                        → PASS=5 FAIL=0（vet/SQL/迁移×3）
```

推送：`b268efbf9..8e8141109 main -> main` 成功；origin/main 与 HEAD 0/0。

## 四、遗留风险

1. **message↔diff 对账未门禁化**：本轮靠人工批判式审计抓获；pre-commit 钩子尚无"message 声称的符号/测试必须存在于 diff"自动校验。建议 R66 正轮列为 P3 门禁候选。
2. **ollama 契约反转仍缺活体抓包**：证据为上游官方文档（WebFetch）+ 仓内 wire 文档双向一致，非真机 NDJSON 抓包；P4 selector 接线前建议补一次活体验证（本机 ollama v0.20.3 已装，daemon 未启动）。
3. **dc5f22246 旧 SHA 残留引用**：如有并行会话本地分支/日志引用旧 SHA（dc5f22246/af7b6cd16/996ca243d），需以 985d23ae9/8e8141109 为准；reflog 保留旧对象至 GC。
4. **R66 其余入口未动**：①mock-probe 生产接入拍板（cmd/gateway 71 生产入口零装配维持现状，待拍板）；③P4 selector 接线包（serialize 多模态闸 + V800 ensure，下一可用迁移号需重新核对——252 第八轮已占 747）；④usage_facts occurred_at 索引评审；⑤R61 §六 1/2/3/4/5 复核；github 镜像 baseline 策略待拍板。

## 五、下一轮（R66 正轮）提示词建议

> 你是 llm-gateway-go 仓库（/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go）的审计主代理，执行 R66 48h 审计轮。
> 1. git fetch 对齐 origin/main（HEAD 基线 8e8141109）；窗口 `git log --since="48 hours ago"`；本轮特有前置：**对窗口内每个提交跑 message↔diff 对账**（git show --stat 与 message 声称逐项 grep 核对，R66 entry ② 虚构 message 教训）。
> 2. 读 docs/audit/2026-09-25-r65-24h-audit-round.md §一登记项 + docs/handoff/2026-09-25-r66-entry2-audit-handoff.md + docs/audit/verify/R66-entry2-ollama-parser-incremental.md（含后审计记）。
> 3. 优先入口：①mock-probe 生产接入拍板（第三轮挂账；现装配 cmd/gateway-v2，生产 cmd/gateway 零装配）；②ollama 契约活体抓包补证 + P4 selector 接线（serialize_ollama 多模态闸 + V800 ensure；迁移号先 fetch 核对，252 第八轮已占 747）；③usage_facts occurred_at 索引/分区评估（scratch 真库实跑）；④R61 §六 1/2/3/4/5 与大专项候选三项复核；⑤auto P2/P3。
> 4. 关键上下文：ollama content 契约已裁决为增量（StreamDelta 路径，勿再改回）；reportrollup internal_person scope_key 编码租户（tenant\x00person，读面走 splitInternalPersonScopeKey）；i18n/testbench/sequence 门禁收紧；secret-scan strict ~52 WARN（kxpms.cn 已白名单化）。
