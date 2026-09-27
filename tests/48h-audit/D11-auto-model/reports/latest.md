# R70 · D11 auto 模型 · auto 路由专项测试 + 三门验收

> 时间：2026-09-27 · HEAD：`1c9c753c4` · 范围：auto 路由自动任务识别与模型选择  
> 入口：`scripts/auto-testbench.sh`（v2 规划 P0①）+ 三门 + D11 48h 审计

## 结论

auto 全链路在 240 套件全 PASS + 三门（build/vet/test）全绿的强事实下，宣告**可用且正确**：

1. **分类层（heuristic:default）**：240 套件用例 accuracy=1.0000，macro_f1=1.0000，11 个任务类型 F1 全 1.0；GRRQ=100.00
2. **过配率（over-provision）**：0/30 简单类（chat / reasoning / creative 等）落入 tier-a 过配；分布 a=85 / b=105 / c=50 健康
3. **决策链端到端**：14 个 (prompt × profile) × 11 个任务类型 × 4 个 profile（含 fallback）全部 Resolve 成功、解析到的模型全部在 canonical 真实池内、无幽灵模型
4. **三门**：`go build ./...` 0 error · `go vet ./autoroute/...` 0 warning · `go test -race -short -timeout 120s ./autoroute/...` 全 PASS（含 internal/legacyflags）
5. **门禁对齐**：`gate vs cmd/auto-testbench/testdata/baseline.json` accuracy/macro_f1/grrq 三项 got ≥ min，GATE **PASS**

回归五件套全绿（详 §4），与 R55/R56 历史裁定的"auto 全链路在位"事实一致，本轮无新增 P0-P3。

## 1. 改动面 vs D11 域

48h 窗口内与 D11 域相关的提交（`git log --since=7 days -- autoroute/`）：

| SHA | 说明 | 对 auto 链路影响 |
|---|---|---|
| `c1e145193` | refactor(autoroute): helpers 抽壳收口 | withRoleFailoverHead 搬迁至独立 helpers.go（十二轮审计已核搬迁逐行一致）——无行为变更 |
| `4108f3f26` | chore(annotations): autoroute 三代 scoring 退役条件 | 注解增量，非功能改动 |
| `092ab1b61` | fix(autoroute): 补 scoreVersionRecency + scoring_simplified 恢复 HEAD 语义 | 已在本轮套件（240 例）中覆盖验证 |
| `f3f2c8a19` | fix(autoroute): 测试地基轮 —— 桩共享指针竞态根修 | 已并入单测 PASS |

结论：窗口内无 D11 P0/P1 风险动作；评分层 V3 退役标注（c 通道）保留旧 scoring.go 是历史过渡，符合 R56 灰度裁决（Wave2 维持 off、租户级 + V3 影子通道）。

## 2. 任务定位模型选型复核

D11 §2 列出的"任务定位"链路（cheap model 兜底）：

- **入口**：model="auto" → `maybeResolveAuto` → 启发式分类
- **启发式低置信度兜底**：`LLMFallbackClassifier`（`classifier_llm.go`），可调端点 + 超时由 `LLMGatewayAutoLLMTimeout` 控制（默认 3s，clamp ≤30s）；错误/无效返回 → decider 层兜底启发式 winner（`decision.go` 中 fallback 槽置信度门，`effectiveLLMThreshold()`）
- **R45 钉桩**：`TestDecide_FallbackBelowThreshold_KeepsHeuristic` / `TestDecide_FallbackError_KeepsHeuristic` 在 decider 层设门 —— classifier 层返回 error 不会被误计熔断失败（避免被误开熔断）

cheap model 选择（供"任务定位"使用）：
- 默认走 `task_default_routing` 表的 fallback 桶（每 task 一个便宜模型；种子数据由 `sql/migrations/startup/477_auto_route_v6_defaults.sql` 落库，经 `autoroute/default_routing_store.go` 读回——R71 勘误：初版把归属写成 default_routing_store.go，该文件零模型字面量）
- `intent_classification` 默认走 `minimax-m3` / `gemini-2.0-flash-exp`（极便宜/免费家族）
- `chat` 默认 fallback `qwen3-235b`；`code` 默认 `deepseek-coder`；`function_call` 默认 `deepseek-chat`

符合用户口径"使用 Minimax-m3 / gpt-5.6-luna / 本地部署的便宜模型进行任务识别"。

## 3. 验收门

### 3.1 三门

```text
$ go build ./...
（0 error）

$ go vet ./autoroute/...
（0 warning）

$ go test -race -short -timeout 120s ./autoroute/...
ok  github.com/kaixuan/llm-gateway-go/autoroute                6.099s
ok  github.com/kaixuan/llm-gateway-go/autoroute/internal/legacyflags  1.536s
```

### 3.2 auto-testbench 离线回归（240 套件）

```text
$ bash scripts/auto-testbench.sh
== offline regression (240 cases, heuristic:default) ==
auto-testbench regression
  suites: autoroute/testdata/auto_matching_suite.jsonl, autoroute/testdata/auto_matching_suite_v2.jsonl, autoroute/testdata/auto_matching_suite_v3.jsonl
  cases: 240 (generated-candidates skipped: 0, known_failure: 0 [xpass: 0])
  accuracy: 1.0000   macro_f1: 1.0000
  tier distribution: tier-a=85 tier-b=105 tier-c=50
  over-provision: 0/30 simple cases → tier-a (rate=0.0000)
  GRRQ: 100.00  (= accuracy×100 − overprovision×30)

gate vs cmd/auto-testbench/testdata/baseline.json
  accuracy       got=1.0000  min=1.0000
  macro_f1       got=1.0000  min=1.0000
  grrq           got=100.0000  min=100.0000
GATE: PASS
```

11 个任务类型 F1 全 1.0（confusion matrix 全部对角，无误判），无 known_failure / 无 xpass。

### 3.3 端到端测试矩阵

| 测试 | 范围 | 结果 |
|---|---|---|
| `TestPromptClassificationMatrix`（classifier_prompt_matrix_test.go） | 56 个真实业务提示词（含 8 个 trap_ 边界） | **56/56 PASS** |
| `TestAutoMatchingSuiteHeuristic`（auto_matching_suite_test.go） | v1+v2+v3 三轮套件 240 例 | **240/240 PASS** |
| `TestAutoRouteE2E_PromptToModel` | 10 个 task × smart + 4 个 task × cost_first | **14/14 PASS** |
| `TestAutoRouteE2E_AllTaskTypesResolvable` | 11 task × 4 profile（含 fallback） | **44/44 PASS** |
| `TestRecommendV2_*` / `TestScoreSimplified_*` / `TestValidateCachedChoice` 等 | 评分+推荐+缓存校验 24 用例 | 全 PASS |

## 4. 复用既有 D11 审计机制

| 审计点 | 复用文件 | 状态 |
|---|---|---|
| 决策链闭环（分类→推荐→re-rank→affinity→settle→feedback） | `autoroute/decision.go`, `decision_v2.go`, `decision_v2_optimizer_test.go` | 已在 R30 证实健康，本轮无回退 |
| 终态 outcome 回填（成功路径也回填） | R35 e8ddff41d 单标记半修双 P1 教训，已 propagateIsAutoRequestToEntry 全字段修复 | 本轮 unit test 覆盖 |
| 数据管道三缺陷基线（promote/chat-records/exporter） | 窗口内无触碰相关代码 | N/A |
| 回放契约 model 参数小写 `auto` | `cmd/auto-testbench` 用同一套件 | PASS |
| ML/ONNX 配对（zipmap=False、ORT 1.29.0） | 窗口内无模型/词汇表变更 | N/A |
| 多维进度评分与成本预设（RFC v2） | 窗口内无实现动作 | N/A |
| 灰度门槛 ≥ e8ddff41d | HEAD 1c9c753c4 远超 | ✓ |
| R44 兜底：fallback 槽置信度门在 decider 层统一 | `decision_test.go` TestDecide_FallbackBelowThreshold_KeepsHeuristic 钉死 | PASS |
| R45 clamp：`thresholds.llm_confidence` tuning 现已 clamp (0,1] | tuning_store_test.go 验证 | PASS |

## 5. 已知遗留（不阻断发版）

| 遗留 | 严重度 | 影响 | 处置建议 |
|---|---|---|---|
| `cmd/auto-testbench` 默认走启发式（heuristic:default）；LLM 兜底层的端到端测试需 DB + AUTO_AUDIT_API_KEY | 低 | 无法验证 LLM 分类器在生产流量的真实表现 | 下一轮 `AUTO_E2E=1` 打本地网关，验证 LLM 兜底触发比例与命中质量 |
| `docs/planning/AUTO_ROUTING_CLOSED_LOOP_V2_PLAN.md` 的 P0③ 已落测试地基，P1 列表（affinity 真实 DB 反连接、session corrections 表）仍待实施 | 低 | V2 闭环第二阶段未完全闭锁 | R71 派 worker 落地 |
| 245/154 生产环境 auto 路由 e2e 尚未跑（AUTO_E2E=1 + 真实 API key） | 中 | 本轮仅在 8782 local-test 完成静态层验证 | 245 网关预生产环境跑 60 轮（AUTO_E2E=1），与 local 对账 |
| 推荐层的 `cmd/auto-testbench -mode generate -source corrections -dsn $DATABASE_URL` 依赖 corrections 表真实流量 | 低 | 候选生成闭环待评估 | 245 跑 1 周真实流量后回灌 |

## 6. 与方案文档的对齐

- RFC：`docs/planning/AUTO_ROUTING_CLOSED_LOOP_V2_PLAN.md` §4.3（套件扩充至 ≥200 例）—— v3 套件 149 例 + v2 40 + v1 60 = 249 行超出门槛；testbench 按 key 去重（`cmd/auto-testbench/suite.go` 的 seen map）后为 240 个用例（§3.2 的 cases: 240 同源）
- 上轮 R56 留档：`tests/48h-audit/D11-auto-model/reports/latest.md`（R56 · D11 auto 模型 · 48h 审计结论）
- v2 实施状态：`autoroute/V2_IMPLEMENTATION_STATUS.md` —— P0 已完成（评分/推荐/决策/feature flags + 1 个测试文件 5 用例），P1 数据层修复（refreshIndexSQL JOIN availability）仍待办
- Wave2 灰度裁决：`docs/audit/2026-09-22-wave2-autoroute-grayscale-report.md` —— RT-1 IQ 门 / RT-3 热门加权默认 off 且 off 路径字节级钉桩，本轮未触碰

## 7. 子代理派发提示词（供 R71+）

```
你是 worker 子代理（带写权限到 tests/48h-audit/D11-auto-model/）。
知识库入口：docs/audit/playbook/domains/D11-auto-model.md
模板：tests/48h-audit/TEMPLATE-domain.md
本轮（2026-09-27 R70）已落：
  - 三门全绿（build/vet/test）
  - auto-testbench 240/240 PASS，GRRQ=100，gate vs baseline PASS
  - 11 个 task 类型 F1 全 1.0
本轮未落（留 R71+）：
  - AUTO_E2E=1 跑 8782 本地网关 + 245 预生产环境（需 AUTO_AUDIT_API_KEY）
  - V2 P1 数据层修复（refreshIndexSQL JOIN cmb/pm availability）
  - session corrections 表真实流量回灌
必填：
  - 改 plan.md §1-§7（含上面三未落项）
  - 在 business/data/stress/safety 至少各写 1 个 _test.go（当前 0 个 .go）
  - reports/latest.md 留档本轮
输出 ≤5KB。
```

---

**版本**: R70 · 2026-09-27 · auto 路由专项  
**关联**: `tests/48h-audit/D11-auto-model/plan.md` + `scripts/auto-testbench.sh` + `reports/auto-testbench.{md,json,cases.jsonl}`（本地运行产物，已被 .gitignore，fresh clone 不可复现——需重跑 testbench 再生）