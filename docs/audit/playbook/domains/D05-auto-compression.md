# D05 — 超长会话自动压缩与 context overflow 保连重试

> 领域编号: D05 ｜ 最近更新: 2026-09-17 (初版) ｜ 状态: v1

## 1. 领域边界

**管**：收到超长会话时的进入前自动压缩；请求发往供应商返回上下文超长错误时的处理——**保持客户端连接、不返回错误、自动压缩后重试**；压缩策略选择器（全局/租户配置）。
**不管**：三层 provenance 映射（D03）；队列重试编排（D04）；供应商错误分类学（D08，本域只关心 overflow 族的后续动作）。

## 2. 参考基线

设计文档：
- `docs/design/compression-strategy-selector.md` — 压缩策略选择器（GW-10 已落地）
- `docs/03-design/02-feature-design/会话优化v4/02-会话管理与压缩设计.md` — 会话管理与压缩设计
- `docs/03-design/01-architecture/architecture/ARCHITECTURE.md` §Intelligent Context Compression

代码入口：
- `domains/hooks/compression/session_compressor.go`、`quality_score.go`
- `errorsx/`（错误分类，overflow 族识别）
- 流式路径：`domains/streaming/`（保连接切换不中断的既有机制）

## 3. 检查清单

1. **进入前压缩**：会话长度超过模型窗口预估时，请求前触发压缩；预估来源（token 估算）与实际窗口配置一致（不出现"估算用旧窗口、实际用新模型"的漂移）。
2. **overflow 保连重试闭环**：供应商返回 context overflow 族错误（各厂商的错误形态：OpenAI 400 maximum context / Anthropic prompt too long / Gemini 等）都被分类到同一可重试族；命中后：客户端连接保持（流式已发头不中断）、自动压缩、再次请求；重试次数有界并在耗尽后才以 kind 化信封告知客户端。
3. **压缩收敛性**：每次重试的压缩幅度必须使 token 单调下降（不出现压缩后仍超长的死循环）；达到下限仍超长时走明确失败路径。
4. **非流式对等**：非流式会话同样具备 overflow 保连重试，不只覆盖流式。
5. **策略选择器**：全局/租户级策略覆盖关系正确（租户覆盖全局），配置变更热生效路径不产生半套策略。
6. **可观测**：每次 overflow 压缩重试有指标/日志（次数、节省 token、最终结果），供 D10 统计与凭据质量评估。

## 4. 历史回归点（轮末回注区）

- [初版] 本域健康面基线待首个 playbook 轮（R34）建立（overflow 族的分类→重试→收敛完整链路证据）

## 5. 子代理派发提示词

```text
你是 D05（超长自动压缩与 overflow 保连重试）只读审计子代理。工作目录：本仓库根。
第一步：Read docs/audit/playbook/conventions.md 和 docs/audit/playbook/domains/D05-auto-compression.md 全文。
第二步：按域文档 §3 检查清单逐条核对，审计窗口：<窗口>；改动文件清单：<该域相关子集>。
重点：errorsx 中 overflow 族分类覆盖的厂商错误形态清单；重试链路的收敛性与客户端连接保持。
只读不改。输出按 conventions.md §4 结构，每条发现带 file:line 与触发路径。
```
