# D01 — IR 数据结构与生命周期

> 领域编号: D01 ｜ 最近更新: 2026-09-17 (初版) ｜ 状态: v1

## 1. 领域边界

**管**：IR（中间表示）核心数据结构的定义完整性、创建、分步赋值、解析、序列化/反序列化、落各类存储（内存/队列元数据/文件缓存/DB）时的保真。
**不管**：协议 parse/serialize 的双向语义正确性（D02）；三层缓存 provenance 业务链（D03）；hot/columnar 落库分区策略（D07）。

## 2. 参考基线

设计文档：
- `docs/架构优化v6/09-ir-class-journal-decoupling.md` — IR 请求类型 + 执行轨迹队列 + 调度解耦定稿
- `docs/adr/2026-09-09-ir-transport-layer-retirement.md` — IR 传输层退役决策
- `docs/audit/agent-1-ir-structure-audit.md`、`docs/audit/agent-1-ir-final-audit-20260831.md` — IR 结构/生命周期历史审计基线

代码入口：
- `internal/ir/` — class.go（请求类型）、detect.go（协议识别）、各协议 parser、anomaly_reporter.go
- `internal/irconv/converter.go` — IR 转换层
- `internal/requestfact/` — T0–T9 调度瀑布事实与 session_turns 诊断组
- `internal/paramreg/` — 参数注册（未知字段保真）

## 3. 检查清单

1. **身份维度字段族**在 IR 请求/事实对象上齐备且语义唯一：日期时间、项目、用户、任务、轮次、多 tag、模型、总轮次、供应商、凭据。grep 结构体定义核对，不允许同义重复字段（两个"轮次"语义并存）。
2. **多轮对话与路由数据、流程跟踪、调度瀑布、压缩脱敏数据、附件与媒体引用**各有明确字段/子结构承载，且在分步赋值过程中不存在"半初始化对象逃逸到序列化"路径（构造函数→中间态→完成态有守卫或不可变分阶段）。
3. **未知字段保真**：Extensions + paramreg 路径下，跨协议往返后未知字段不丢（roundtrip 测试在位且覆盖窗口内新增字段）。
4. **JSONB 落库免疫**：所有 IR JSONB 写入继续走 `$N::text::jsonb` 参数化（22P02 免疫），窗口内新增写入点不破例。
5. **序列化器对齐**：所有响应内容块类型（text/tool_call/tool_result/thinking/refusal/多模态块）在全部序列化器中行为一致——不存在"parse 保留、serialize 丢弃"的块类型。
6. **存储分层**：IR 元数据在内存缓存、队列元数据、服务器目录文件缓存、DB 四处的字段口径一致；文件缓存的 schema 版本化（旧版本可读或显式失效）。
7. **anomaly 上报**：IR 异常（空内容、畸形块、超限）进入 anomaly_reporter 而非静默吞掉。
8. **存储优化**：大字段（原始 body）与热字段分离存储，读取路径不反序列化大字段除非必要。

## 4. 历史回归点（轮末回注区）

- [R30] refusal 块 parse 侧保留但三个响应序列化器全部丢弃 → refusal-only 响应坍缩空壳成功（契约）— 修复 692205664；钉桩 refusal→text 分支测试
- [R30] JSONB 全走 `$N::text::jsonb` 为健康面基准，新增写入点必须延续（不变量）
- [R30] Extensions+paramreg 未知字段跨协议保真为健康面基准

## 5. 子代理派发提示词

```text
你是 D01（IR 数据结构与生命周期）只读审计子代理。工作目录：本仓库根。
第一步：Read docs/audit/playbook/conventions.md 和 docs/audit/playbook/domains/D01-ir-lifecycle.md 全文。
第二步：按域文档 §3 检查清单逐条核对，审计窗口：<窗口，由主代理注入>；改动文件清单：<该域相关子集>。
只读不改。输出按 conventions.md §4 结构（发现候选表/健康面/未覆盖项），每条发现带 file:line 与触发路径。
```
