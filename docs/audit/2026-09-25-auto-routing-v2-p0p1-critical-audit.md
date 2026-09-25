# 2026-09-25 AUTO 路由 v2 闭环 P0+P1 批判复审轮（发现 5 问题 + 修正）

- 对象：本轮已推送的 P0（80bb3675a）与 P1（97d8870aa）交付，对照 v2 规划逐项批判审计。
- 方法：不信任"测试全绿"——对实现依赖但未核实的事实逐条溯源（表/视图拓扑、列名、
  known_failure 语义），重读全部新增代码找逻辑/契约/安全问题。
- 结论：发现 **2 个正确性问题、1 个语义不一致、2 个健壮性/展示问题**，全部修正并补
  回归单测；无安全问题；复验全绿。

## 一、发现与修正

| # | 严重度 | 问题 | 根因 | 修正 |
|---|--------|------|------|------|
| A | **高（正确性）** | P1 分析器 4 处 SQL 查基表 `auto_route_selections`，而 656/658 迁移拓扑是 **基表 + `_hot` 表 + `_all` 视图（UNION）**：近期请求落在 `_hot`，基表查询漏掉它们 | 实现时未核实 `_all` 是视图这一事实（P0 采样/generate 均正确用了 `_all`，P1 没对齐） | `taskprofile/analyzer.go` 收集 JOIN、volumes、两个 backtest 共 4 处 → `auto_route_selections_all`。影响分析：修正率分母被低估 → 闸门虚松 + 近期修正样本丢失——修复后闸门恢复规划口径 |
| B | 中（语义不一致） | testbench 把 `known_failure` 行当普通用例计入 accuracy/F1/GRRQ——包内测试（`auto_matching_suite_test.go:141-192`）的语义是**容忍其失败、xpass（意外判对）视为 stale marker 报错** | 实现时只对齐了 schema，未对齐计分语义；后果是未来套件一旦加入 triaged gap 用例，门禁立即假红 | `cmd/auto-testbench/metrics.go`：known_failure 行与 generated 候选同法排除在指标外，xpass 单独计数；summary/JSON/md 报告三处透出；新增 `TestKnownFailureExcludedFromMetrics` 钉桩 |
| C | 低（正确性） | 阈值草稿 `evidence.coverage`/rationale 打印常量 80%，非实际覆盖率 | 模板直接引用闸门常量 | 记录选择候选时的实际覆盖率（round2），rationale 同步；单测断言 13/14→0.93 |
| D | 低（健壮性） | `POST /tuning/analyze` 同步执行无时限，DB 异常时请求可悬挂 | 端点新写，未设 ctx 上界 | `context.WithTimeout` 3 分钟，超时走 writeInternalErr；后台调度不受影响 |
| E | 低（展示） | AutoTuningView `proposalSummary` 不渲染 keyword_add 的 `add` 数组与 threshold 的 old→new | 摘要函数按 signals 分析器的字段形状写，未覆盖 P1 新草稿形状 | 补 `add=[…]` 与 `old→new` 渲染 |

**核实为正确、保持不变的关键事实**：`task_type_corrections` 列名（724 迁移：
auto_task_type/human_task_type/agrees/created_at ✓）；`confidence` 列在基表与
_hot 均存在（NUMERIC(4,3) ✓）；P0 采样/generate 的 `_all` 引用 ✓；
`MessageCount: 2` 信号构造与包内测试一致 ✓；ServeMux 字面路径优先于子树模式
（generate 不进 id 解析）✓；apply 校验与 tuning_store hydrate 同口径 ✓。

## 二、过程性教训（写入操作纪律）

1. **"测试全绿"不证明 SQL 正确**——本机无 PostgreSQL，列名/表名错误在单测里
   不可见。凡新写 SQL，必须先对迁移文件核实表/视图拓扑与列名（本轮 A 即此类）。
2. **复用语义要复用行为而不止 schema**——套件 JSONL 的 `known_failure` 字段
   在三处消费者（包内测试/e2e-audit/testbench）中语义必须一致（本轮 B）。
3. **临时验证设施的生命周期**：上轮把 Windows overlay（tmp-p0-overlay）随收尾
   删除，本轮 admin 复验时被迫重建——overlay 生成配方已写入项目记忆，后续
   验证前先确认其存在再开工。

## 三、复验证据

```
go test ./taskprofile/ ./cmd/auto-testbench/        → ok（新增 2 个单测全绿）
go run ./cmd/auto-testbench -gate-default           → 240 例 GRRQ=100.00，GATE: PASS
go test -overlay=tmp-p0-overlay/overlay.json ./admin/ → 仅既有 2 个 POSIX 路径断言失败
  （SSE 时钟用例本轮随机通过），零新增
go build -overlay … ./admin/ ./bg/ ./cmd/gateway/    → 成功
go vet（overlay ./admin/、./taskprofile/）            → clean；gofmt（改动文件）clean
web: npm run typecheck → 0 错误（vitest/build 与上轮口径一致，本轮仅改展示函数）
```

## 四、遗留风险（不阻塞本轮）

1. **真数据端到端演示仍顺延部署窗口**（P1 验收项）——表拓扑修复（A）后更必要：
   首次真库运行时核对 `_all` 视图 UNION 分支的执行计划。
2. **gate 阈值恰为 1.0 的脆弱性**：known_failure 语义修复后，门禁对"新增失败
   用例"仍零容忍——这是规划"不回退"的字面语义，靠 `--refresh-baseline` 工作流
   兜底；若未来套件常态化带 triaged gap，再考虑显式 headroom。
3. `tuning-backtest` CLI 与 `taskprofile.BacktestThresholdBand` 的同源 SQL 双份
   （既有约束，注释互指属主）。
4. **overlay 目录保持不入库**（tmp- 前缀 + 混包名会破坏根 `go build ./...`），
   需要时按记忆配方重建。

## 五、对前两份审计文档的勘误

- `2026-09-24-…p0-testbench-frontends.md` §四.1 的 Windows 失败清单与 §二 指标
  口径不变；其"known_failure: 0"表述当时指事实（240 例无 known_failure 行），
  本轮修正的是**若出现该类行时的计分规则**。
- `2026-09-25-…p1-corrections-proposals.md` §二.4 的回放口径、§三 验证证据有
  效；其 SQL 所引表名以本文档修正后的 `_all` 为准。

## 六、与并行会话（R64/R65 审计轮）的会合记录

推送时发现上游 R64/R65（891542ff1/114beda22）对同一批 P1 代码做了更深的批判
复审，rebase 冲突按"取上游为底、重放本独有修复"解决：

- **上游已覆盖且更深**（以其版本为准）：`_all` 读面（并注明 hot promotion 滞后
  根因）、读面 `classifier='heuristic'` 口径过滤、domain_hint 三值枚举全拉黑、
  generate 去重+插入收敛为 advisory-lock 单事务（修竞态）、LIKE 通配符转义、
  handleAnalyze 并发 409（bg.ErrAnalyzeInProgress）。
- **本轮独有、已重放到上游基线上**：B（evidence.coverage 实际覆盖率，上游仍打
  常量 0.80）、D（handleAnalyze 3 分钟 ctx 限时，与 409 语义正交）、C（testbench
  known_failure 计分语义，上游未触及 cmd/auto-testbench）、Q（前端 add/old→new
  渲染）。
- 合并后 `analyzer_test.go` 双方用例共存（含上游新增枚举排除/LIKE 转义两测），
  全量复验见本轮提交说明。

