# 2026-09-25 AUTO 路由 v2 闭环 P1（修正→调参提案闭环）落地与验证轮

- 依据：`docs/planning/AUTO_ROUTING_CLOSED_LOOP_V2_PLAN.md` §六 P1 行、§4.4 回路 C、§4.5、§七.1 防自激阈值。
- 定位：回路 C 的"自动生成提案 + 量化回放"半环补全——既有资产（`bg/feedback_analyzer` 质量驱动提案、`tuning_proposals` 表、approve/reject 热调参链路、`cmd/tuning-backtest` CLI）之上，新增**人工修正驱动**的提案来源，并打通"提案→内联回放量化→admin 页展示→人工批准"的完整链路。
- 结论：P1 代码闭环全部落地且回归全绿（两处 HEAD 既有失败与 P0 轮相同，无新增）。

## 一、交付物清单

| 组件 | 位置 | 说明 |
|------|------|------|
| 修正驱动分析器 | `taskprofile/analyzer.go`（新） | corrections×658 特征聚合 → 过闸 → 提案草稿（threshold_change/keyword_add）→ 内联回放 → 待审去重 → INSERT。纯函数（aggregate/qualify/draft）与 DB 层（collect/backtest/insert）分离 |
| 纯函数单测 | `taskprofile/analyzer_test.go`（新） | pair 聚合排序、类型级闸门、阈值候选选择（原始置信度覆盖率）、关键词闸门（样本数/通道白名单/占比/泛词/现词去重）、上限截断 |
| generate 端点 | `admin/auto_route_tuning.go` | `POST /api/admin/auto-route/tuning/proposals/generate?days=N`（7-90 钳制，默认 30）：运行分析器，返回生成草稿；`evidence.operator` + `slog tuning.audit` 双审计留痕 |
| analyze 端点 | 同上 | `POST /api/admin/auto-route/tuning/analyze`：按需运行既有信号分析器（履行前端 `triggerTuningAnalyze` 占位契约，该 TODO 就此销账） |
| threshold_change apply | 同上 | `applyProposalInTx` 新增 threshold 分支：key 白名单（llm_confidence/long_context_tokens）+ 与 `autoroute/tuning_store.go` 同口径的范围校验（(0,1] / 正整数）+ SELECT FOR UPDATE 行锁更新 `tuning_params`（source='feedback'） |
| CLI 回放 | `cmd/tuning-backtest/main.go` | `-proposal-id` 支持 threshold_change：[new,old) 置信带的 would-touch/would-fix-proxy 计数（与 taskprofile.BacktestThresholdBand 同 SQL，注释互指属主） |
| 前端 API | `web/src/api/tuning.ts` | `generateCorrectionProposals(days)`；`ProposalEvidence` 扩展 corrections/backtest 字段；`triggerTuningAnalyze` 注释销账 |
| 前端视图 | `web/src/views/AutoTuningView.vue` | 页头"从人工修正生成提案（窗口 7/30/90 天）"与"运行信号分析"按钮；evidenceSummary 渲染修正对/hint 占比/回放计数（band/fix≈、matched/fixed≈） |
| i18n | 8 语言 `autoTuning.ts` | action 块新增 10 键（windowDays/generate/generating/generateConfirm/generateDone/generateNone/analyze/analyzing/analyzeConfirm/analyzeDone） |

## 二、设计要点备案

1. **闸门（规划 §七.1）**：类型级修正率 ≥30% 且修正数 ≥5 才入候选；全局阈值提案另需带内修正样本 ≥10（`minGlobalCorrected`，全局改动影响面大，闸门高一档）。
2. **阈值草稿只降不升**：修正驱动语义 = 分类器置信度过高。候选 {0.85…0.50} 步进 0.05，取"覆盖率 ≥80% 的最保守（最大）候选"；地板 0.50（低于则启发式形同虚设）；覆盖率**基于原始置信度值**——带粒度（0.15/0.20）粗于候选步进，不能用于判定（单测钉桩），conf_bands 仅作 evidence 展示。
3. **关键词证据隐私口径**：token 来自 `domain_hint`（写入侧已归一化的领域特征，658 迁移列），不是 prompt 摘录；通道白名单 {reasoning, code, creative}（human 任务类型不在表内即跳过）；现词命中/泛词（unknown/other/…）/长度 <2 均跳过。
4. **回放量化口径**：threshold = [new,old) 置信带内 heuristic 请求的 would-touch（兜底成本代理）与其中被人工改判的 would-fix-proxy；keyword = 该 task_type 带 hint 的 matched 与其中被改判数。全部只读、只出计数（无行离开数据库）。
5. **apply 与 hydrate 同契约**：提案写入值必须通过 `tuning_store` hydrate 时的同款校验，"会被热路径拒绝的提案不可批准"；生效仍走 `bg/tuning_store_refresher`（≤5 分钟）。
6. **去重**：pending 同 (category, task_type, key[, token LIKE]) 即跳过（镜像 feedback_analyzer 语义，advisory）；历史已 applied 的阈值提案天然不再触发（new<old 不成立）；已应用的关键词在现词表中跳过。
7. **上限**：单次生成 ≤5 条（阈值草稿优先，其余按修正数降序）。
8. **审计链**：提案行（ts/proposal/evidence/status/reviewed_by/reviewed_at/review_note/applied_at）+ `evidence.operator`（触发者）+ `slog tuning.audit` 触发事件 + 审批走既有端点（approve 即热调参、reject 带 note）。

## 三、验证证据（本机 Windows，go1.27.1）

```
go test ./taskprofile/                    → ok（新增 6 个分析器单测 + 既有 wiring_guard 全绿）
go test ./cmd/auto-testbench/             → ok
go run ./cmd/auto-testbench -gate-default → 240 例 accuracy/macro_f1=1.0，GRRQ=100.00，GATE: PASS
go test -overlay … ./admin/               → 与 P0 轮完全相同的 3 个 Windows 固有失败
                                            （POSIX 路径断言×2 + SSE 时钟精度×1），无新增
go vet ./taskprofile/ + -overlay ./admin/ → clean；gofmt（改动文件）clean
go build ./taskprofile ./bg ./cmd/gateway ./cmd/tuning-backtest（bg/gateway 带 overlay）→ 全部成功
web: npm run typecheck → 0 错误；vitest src/i18n → 16/17（唯一失败=P0 轮已归档的
     keys_referenced 注释扫描既有问题）；npm run build → 29.82s 成功
```

## 四、遗留与顺延（非阻塞）

1. **端到端演示（规划 P1 验收项）顺延部署窗口**：真实修正 → 提案 → 回放 → 批准生效 → 套件复跑不回退，需真实 corrections 数据（252 只读库 + 标注表），随部署验证执行；本地已用单测覆盖每个纯函数环节，DB 环节 SQL 与 P0 采样同法钉桩。
2. **`tuning-backtest` 与 `taskprofile.BacktestThresholdBand` 的带查询 SQL 为同源双份**（CLI 不可被包导入的既有约束），已在两处注释互指属主；后续若 SQL 演进需同步。
3. **admin 全包 3 个 Windows 固有失败 + keys_referenced 1 例**：见 P0 审计文档 §四，本轮零增量。
4. **P2/P3（TierSelector 接线 / V3 影子）不在本轮**：按规划属独立裁决（影子期 ≥7 天），P0/P1 交付的 GRRQ 门禁与提案链路是其前置输入。

## 五、对照规划验收门禁

| 规划验收门禁 | 状态 |
|---|---|
| 端到端演示：真实修正 → 提案 → 回放量化 → 批准生效 → 套件复跑不回退 | ⏳ 代码链路就绪，真数据演示随部署窗口（§四.1） |
| 提案全程可审计 | ✅ §二.8（提案行 + operator + slog 事件 + 审批列） |
| 提案不自动生效（门禁 + 人工批准） | ✅ generate 只产 pending；生效仅经 approve（同事务写 tuning_params）+ refresher 拾取 |
