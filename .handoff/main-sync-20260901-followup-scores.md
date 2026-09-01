# Handoff 后续闭环 — `data/model-quality/scores/` 决策（2026-09-01）

## 结论

**保留现状：`data/model-quality/scores/` 继续由 `.gitignore` 屏蔽，不入仓。** 不执行 main-sync-20260901 第 6 项 "从 stash@{1}^3 重新拉回仓库"。

## 评估依据

| 维度 | 事实 |
| --- | --- |
| 当前文件 | 12 个 `{provider}_{model}.jsonl`，每个 5 行 / ~1.6 KB（总 ~19 KB） |
| 文件内容 | 运行时探针记录：`probe_kind=gateway, benchmark_type=mmlu_lite, trigger_kind=scheduled`，5 个时间戳均为 2026-09-01，每个 `benchmark_id` 唯一 |
| `accuracy / latency_p95 / stability` | 全部为 0，`overall_score=10, grade=F` — 真实运行结果（非模板，模板通常不携带真实 benchmark_id） |
| `.gitignore` 现状 | 行 249-250：注释明文 "commit the schema under data/model-quality/scores/ at most once, then ignore." + 屏蔽规则 `data/model-quality/scores/` |
| 提交历史 | 任何分支、任何 stash（包括 `stash@{1}^3`）均无 `data/model-quality/` 提交记录。`.gitignore` 那条注释是"前瞻式声明"，从未实际执行过 schema commit |
| 设计文档语义 | `docs/03-design/02-feature-design/model-quality/README.md:222` 将 `scores/` 标注为 "评分历史"（runtime data），与 `reports/` 同级 |
| 真实数据节奏 | `cmd/gateway/main.go:3967-4058` 的 `model_quality_worker` 每 24h 自动跑一次 12 个模型，本地会产生持续追加的 JSONL 行 |
| 与 `data/model-quality/reports/` 对齐 | `reports/` 也未入仓，仅运行时落盘 — 与 `scores/` 同性质 |

## 为什么不该入仓

1. **运行时输出而非 schema**：`scores/` 的内容是每次探针的运行结果（`accuracy / latency_p95 / overall_score / benchmark_id / timestamp`），不是字段模板。每次新增运行都应独立 git log，不应冻结在仓库里。
2. **会持续膨胀**：`model_quality_worker` 默认 `interval_hours=24` × 12 个模型 = 每天至少 12 行追加，长期看会从 19 KB 涨到 MB 级，与 `reports/` 一样属于"环境运行时产物"。
3. **跨环境失效**：本地探针的 `accuracy=0 / grade=F` 是因为本地没有真实上游；和 154 / 245 / staging 上产出的真实评分不可比。混在仓库里会误导读者。
4. **`.gitignore` 注释承诺的 "commit once" 与现实不符**：`scores/` 从未进入 git 历史，且 schema 已经在 `cmd/calc_quality/` 的 Go struct + 设计文档里双重固定，无需 JSONL 来证明 schema。
5. **风险与收益不匹配**：恢复 → 长期跟踪成本；保留忽略 → 零成本。

## 用户决策遗留问题的最终答复

> main-sync-20260901 item #6："评估是否将 `data/model-quality/scores/` 整体从 stash@{1}^3 重新拉回仓库（用户决策遗留问题）"

**评估完毕：不需要。** 该项关闭，不在仓库跟踪。如未来确有需要（例如 schema 兼容性回归测试），可在 `cmd/calc_quality/` 加 Go test fixture（`testdata/scores/{provider}_{model}.jsonl`），那是 Go 测试体系的正确位置，而不是运行时数据目录。

## 安全门禁自检

- [x] 未对仓库做任何修改（仅读取 + 写本 handoff 文档）
- [x] 不引入未提交改动（`git status --porcelain=v1` 仍为 clean）
- [x] 不动 `.gitignore`（保留 line 250 屏蔽规则不变）

## 关联文件

- 源 handoff：`.handoff/main-sync-20260901.md`
- 父提交：`ea6a5ef50`（docs(handoff): record main-sync 2026-09-01 closure）
- `.gitignore` 行 247-250
- 设计文档：`docs/03-design/02-feature-design/model-quality/README.md:214-225`
- Worker 入口：`cmd/gateway/main.go:3965-4058`