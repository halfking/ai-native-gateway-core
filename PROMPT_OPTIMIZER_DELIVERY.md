# 提示词优化模块 —— 交付说明

**状态**：✅ 实现完成，已通过单元测试 / 变异验证 / Docker 端到端测试 / **真实链路 E2E / 端到端效果实验**
**验证日期**：2026-11-11（首轮）· 2026-11-12（真实 Memora + 内部小模型 + 效果实验）
**验证记录**：`docs/PROMPT_OPTIMIZER_TEST_VALIDATION.md` · `docs/PROMPT_OPTIMIZER_REAL_INTEGRATION.md`

---

## 1. 交付内容

| 类别 | 文件 | 说明 |
|------|------|------|
| 核心 | `internal/prompt-optimizer/optimizer.go` | 优化器 + 意图分析 + 策略调度 |
| 核心 | `internal/prompt-optimizer/interfaces.go` | ContextManager / HarnessInjector / Evaluator 接口 |
| 策略 | `internal/prompt-optimizer/strategies/structural.go` | 结构化增强 |
| 策略 | `internal/prompt-optimizer/strategies/fewshot.go` | Few-shot 示例优化 |
| 策略 | `internal/prompt-optimizer/strategies/iterative.go` | 迭代优化（OPRO 风格）|
| 策略 | `internal/prompt-optimizer/strategies/llm_rewrite.go` | **LLM 驱动改写**（内部小模型 + memora 历史，温度固定 0）|
| 上下文 | `internal/prompt-optimizer/context/manager.go` | memora 接入（`domains/memory.Reader`）|
| 上下文 | `internal/prompt-optimizer/context/window.go` | token 预算窗口 |
| 记忆 | `internal/prompt-optimizer/memora/client.go` | **真实 Memora v2 客户端**（service JWT + project scope，写/检索/会话分析）|
| Harness | `internal/prompt-optimizer/harness/injector.go` | 工具/安全/错误处理/可观测性注入 |
| 评估 | `internal/prompt-optimizer/evaluation/evaluator.go` | 评估器 + 指标 |
| 评估 | `internal/prompt-optimizer/evaluation/prompt_quality.go` | 结构质量评估（无需 LLM）|
| LLM | `internal/prompt-optimizer/llm/openai.go` | OpenAI 兼容客户端（内部小模型通道）|
| 示例 | `internal/prompt-optimizer/examples/basic.go` | 三种用法示例 |
| 服务 | `cmd/prompt-optimizer/main.go` | 独立 HTTP 服务 |
| 服务 | `cmd/prompt-optimizer/hookapi.go` | **网关 Hook 契约端点** `POST /api/v1/prompts/optimize` + SHA256 缓存 |
| 实验 | `internal/prompt-optimizer/experiment/tasks.go` | 6 个**带客观判据**的真实任务集 |
| 实验 | `internal/prompt-optimizer/experiment/runner.go` | 多臂 × 多重复实验运行器 + 统计 + 报告 |
| 实验 | `internal/prompt-optimizer/experiment/executor.go` | 执行模型通道（OpenAI 兼容）|
| 实验 | `cmd/prompt-optimizer-experiment/main.go` | 效果实验 CLI（4 臂对照 + Memora 回写）|
| Mock | `cmd/prompt-optimizer-mockllm/main.go` | 本地测试用 mock LLM |
| 部署 | `Dockerfile.prompt-optimizer`(+`.dockerignore`) | 多阶段构建 |
| 部署 | `Dockerfile.prompt-optimizer-mockllm`(+`.dockerignore`) | mock LLM 镜像 |
| 部署 | `docker-compose.prompt-optimizer.yml` | 本地测试环境 |
| 配置 | `configs/prompt-optimizer.yaml` | 配置参考 |
| 测试 | `scripts/test-prompt-optimizer.sh` | 端到端测试（31 项断言，Docker + memora 桩）|
| 测试 | `scripts/test-prompt-optimizer-real.sh` | **真实链路 E2E**（真实 Memora + 真实内部小模型 + Hook 契约）|
| 测试 | `scripts/audit/prompt-optimizer-mutation-verify.sh` | 变异验证（14 臂）|
| 测试 | `internal/prompt-optimizer/**/*_test.go` | 7 个包的单元测试 |
| 文档 | `internal/prompt-optimizer/README.md` | 模块文档 |
| 文档 | `docs/PROMPT_OPTIMIZER_INTEGRATION.md` | 集成指南 |
| 文档 | `docs/PROMPT_OPTIMIZER_TEST_VALIDATION.md` | 测试验证记录 |
| 文档 | `docs/PROMPT_OPTIMIZER_REAL_INTEGRATION.md` | **真实 Memora / 内部小模型 / 网关 Hook 接线**（实测契约）|
| 文档 | `docs/PROMPT_OPTIMIZER_REAL_EXPERIMENT.md` | **端到端效果实验报告**（真实任务 · 规则判据 · 多臂对照）|

---

## 2. 验证证据

```bash
# 单元测试 —— 7 个包全绿
go test ./internal/prompt-optimizer/...

# 静态检查 —— 无输出即通过
go vet ./internal/prompt-optimizer/... ./cmd/prompt-optimizer/...

# 变异验证 —— 14 条臂全部被测试抓住
./scripts/audit/prompt-optimizer-mutation-verify.sh

# Docker 端到端 —— 31 项断言全通过
./scripts/test-prompt-optimizer.sh all

# 真实链路 E2E —— 真实 Memora + 真实内部小模型 + 网关 Hook 契约
./scripts/test-prompt-optimizer-real.sh

# 端到端效果实验 —— 4 臂 × 6 任务 × N 次重复，规则判据
export MEMORA_JWT_SECRET="$(docker inspect memora-server --format \
  '{{range .Config.Env}}{{println .}}{{end}}' | grep '^JWT_SECRET=' | cut -d= -f2-)"
go run ./cmd/prompt-optimizer-experiment --repetitions 5 --write-back \
  --out docs/audit/prompt-optimizer-experiment-run2
```

实测数据（Docker 桩环境）：50 次优化请求总耗时 663ms，平均 **13ms/次**。

> ⚠️ 这个 13ms 是**桩环境**的数字（memora 是桩、LLM 是 mock），
> 只说明服务自身的开销很低，**不代表**接了真实模型/真实 Memora 后的耗时。
> 真实链路的耗时见效果实验报告。

---

## 3. 快速开始

```bash
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go

# 一条命令跑完整流程（构建 + 启动 + 全量测试）
./scripts/test-prompt-optimizer.sh all

# 清理
./scripts/test-prompt-optimizer.sh stop
```

手工调用：

```bash
curl -s localhost:8090/api/v1/optimize \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"帮我翻译这段文字","task_type":"translation","inject_harness":true}' | jq .
```

---

## 4. 效果数字：初版是假的，这一版是实测的

初版交付文档写了「代码生成 +43.5% / 文本翻译 +19.2% / 平均提升 29%」等数字。
**那些数字不成立** —— 它们来自论文的其它任务（BBH / GSM8K 等），
不是本仓提示词上的实测值。

**2026-11-12 补做了真实端到端效果实验**（真实 Memora + 本地内部小模型
`qwen2.5:7b` + 6 个带客观判据的真实任务 + 4 个对照臂 + 每组 5 次重复
= 120 次模型调用，23 分钟）。完整报告见
[`docs/PROMPT_OPTIMIZER_REAL_EXPERIMENT.md`](docs/PROMPT_OPTIMIZER_REAL_EXPERIMENT.md)，
原始数据（含每条模型输出）见 `docs/audit/prompt-optimizer-experiment-run2/`。

### 实测结果

| 臂 | 正确率 | 相对 baseline | 平均延迟 | 提示词长度 |
|---|---|---|---|---|
| `baseline`（原始提示词）| 53.3% | — | 8583ms | 32 字符 |
| `rule`（规则结构化）| **66.7%** | **+13.4pt（相对 +25%）** | 8554ms | 360 字符 |
| `rule_harness`（+harness 注入）| 63.3% | +10.0pt | 11774ms | 900 字符 |
| `llm_harness`（LLM 驱动改写）| 36.7% | **−16.6pt** | 11876ms | 639 字符 |

### 三个能说的结论

1. **规则结构化优化有效**：在这 6 个任务上，53.3% → 66.7%。
2. **harness 注入当前是净成本**：正确率不升，延迟 +38%，提示词长度 ×2.5。
3. **用小模型做提示词改写当前是负收益**：36.7%，低于 baseline 16.6pt。
   主因是改写把原文**具体内容抽象成占位符**（"这段话"/"given text"）⇒ 原文事实丢失。

### 三个不能说的（口径边界）

- 不能说"提示词优化整体提升 X%" —— 只测了 6 个任务 / 1 个模型 / 1 类任务。
- 不能把结构质量分当效果 —— 本轮就有反例：`llm_harness` 的提示词**更详细**，
  正确率却**最低**。结构 ≠ 效果。
- 不能外推到生产流量。

### ★ 一个量具层发现（比任何效果结论都重要）

**执行侧在 `temperature=0` 下仍然非确定**：24 个「任务 × 臂」组合，
每组 5 次输出，**24 组全是 5/5 互不相同**。

后果很实在：n=1 的第一轮跑，baseline 正确率 33.3%；n=5 的第二轮，53.3%。
**同一基线差 20 个百分点** —— n=1 的对比会被完全读成噪声。

⇒ 因此实验运行器把「同组合不同输出数」做成了报告里的常驻读数，
判据口径变更时还能用 `--rescore` 离线重算（不必重跑模型）。

### 度量口径（不变）

| 口径 | 衡量什么 | 不能证明什么 |
|------|----------|--------------|
| `prompt_quality` | 提示词**要素齐不齐** | 不能证明效果更好 |
| `output_similarity` | 输出**像不像**期望答案 | 不能证明答案正确 |
| `task_correctness`（本轮新增）| 输出**是否满足原始任务要求**（规则判定）| 只覆盖本轮 6 个任务 |

第三种口径是效果实验专用：判据只检查**原始提示词里已经隐含**的要求，
不发明新需求。

---

## 5. 已修复的问题清单

初版代码从未编译过。测试验证轮修掉的问题（详见验证记录文档）：

**编译期**：import 路径全错、引入未 vendored 的 gin、`len*0.5` 截断、
examples 缺导入。

**逻辑期**：完整性指标恒真（"缺覆盖的全绿"）、评估器 nil panic、
提升幅度除零产出 NaN/Inf 导致序列化失败、`MetricScores` 空实现、
**迭代优化策略是死代码**（`getTestCases` 硬编码返回空切片）、
意图分析是空 TODO。

**契约期**：JSON 字段名不是文档承诺的 snake_case、耗时输出纳秒整数不可读、
memora 桩与 client 契约不匹配。

**部署期**：compose 引用 3 个不存在的文件 + 1 个不存在的镜像、
Dockerfile Go 版本过旧、`go mod download` 与 vendor 模式冲突、
compose `command` 覆盖 Dockerfile CMD。

**测试自身**：2 条断言写得太松（变异验证抓到）、1 条断言全文扫描误命中
（`s"` 命中 `"iterations"`）、1 条 bash 全角标点导致变量名被吞。

### 2026-11-12（真实接线 + 效果实验轮）新修掉的问题

**接线期**：
- memora 走 legacy 路由会被 service token 拒（`service_token_wrong_route`）
  ⇒ 改走 v2 fail-closed 组（service JWT + project scope）；
- v2 的三个端点各缺一个头就是 422，且错误信息不指向原因
  （`Idempotency-Key` / `X-Correlation-ID` / `X-Project-ID`）；
- `session-analysis` 的幂等键必须是 `<dispatch_id>-<analysis_kind>`；
- `session-analysis` 的 `tags` 是结构体数组，传字符串数组会被判 `invalid_json`；
- memora 的授权主体是 JWT 的 **`actor_id`**，不是 `sub`；
  project 必须存在且 `owner == actor_id`，否则 403 `project_not_found`。

**契约期**：
- 网关 Hook 调的是 `/api/v1/prompts/optimize`，本服务只有 `/api/v1/optimize`
  ⇒ Hook 接线后必然一直 404 回退（**已补齐契约端点 + SHA256 缓存**）。

**生产安全**：
- `MockToolRegistry` 是占位工具（search / calculate），注入真实请求会让模型
  以为自己有这些工具 ⇒ 新增 `ProductionInjectorConfig()`（不注入工具章节）。
  实验证据：注入占位工具那版配置里 `rule_harness` 的 `json_array_3` 从通过变不通过。

**并发/生命周期**：
- memora 写回挂在 `r.Context()` 上 ⇒ HTTP 响应一返回就被取消
  （实测 `context canceled`，E2E 第 7 条断言因此失败）
  ⇒ 改用 `context.WithoutCancel` + 独立超时。

**判据期**：
- `notice_facts` 的判据内部不自洽：同一组事实里"周六/星期六""1 小时/一小时"
  都接受多形态，唯独时间只接受"2 点"写法 ⇒ 模型写"凌晨2:00"被误判"事实缺失"。
  修正后用 `--rescore` 离线重算，并**同时披露修正前后两套数字**。

**量具期（最重要）**：
- 执行侧在 `temperature=0` 下**仍然非确定**：24 组「任务 × 臂」每组 5 次输出，
  **24 组全是 5/5 互不相同** ⇒ n=1 的效果对比无效
  （实测同一 baseline：n=1 时 33.3%，n=5 时 53.3%）。

---

## 6. 已知边界

1. ~~未接真实 memora 实例~~ —— **已接**（v2 fail-closed 组，service JWT + project scope，
   写/检索/会话分析三条链路都实测通过；见 `docs/PROMPT_OPTIMIZER_REAL_INTEGRATION.md`）。
2. ~~未接真实内部小模型~~ —— **已接**（OpenAI 兼容端点，本地 ollama `qwen2.5:1.5b`/`7b` 实测）。
3. ~~未做端到端效果实验~~ —— **已做**（4 臂 × 6 任务 × 5 次重复，见 §4）。
4. **效果结论的适用范围窄**：6 个任务 / 1 个执行模型 / 1 类任务。任务集需要扩容
   （`date_iso` 无区分度，是模型的日期算术问题；`three_bullets` 与 `json_array_3`
   结论相反）。
5. **规则优化会稀释数量约束**：`structural` 策略的固定输出模板
   （`[主要内容]/[补充说明]`）会让模型在"给我 3 条"这类任务上多输出要点
   （实测 5/5 → 1/5）。**这是当前最该修的缺陷。**
6. **LLM 改写有占位符泄漏**：小模型会把原文具体内容抽象成"这段话/给定文本"，
   导致事实丢失（实测 `no_fabrication` 1/5）。当前靠"改写失败回退原提示词"
   兜底，但**没有**检测占位符泄漏 ⇒ 需要补检测。
7. **harness 注入是净成本**（正确率不升、延迟 +38%）：注入内容 900 字符，
   其中大部分与任务无关。
8. 初版 README 承诺的 `ensemble` / `dynamic` 策略**未实现**，已在 README 标注。
9. **未接进网关主链路**：Hook 的契约端点已补齐（`POST /api/v1/prompts/optimize`），
   但**刻意不接线** —— 效果实验结论尚未支持把它放进生产请求路径。
   6 行接线方案见 `docs/hooks/prompt-optimization.md`。
10. 意图分析是关键词启发式，不调用 LLM；显式传 `TaskType`/`Domain` 更准。
11. **`token` 数是估算**（CJK 1 字 = 1 token，其余 4 字符 = 1 token）：
    本仓 vendor 模式不引入分词器依赖；该数字只用于网关审计元数据，
    **不参与任何质量判定**。
12. 权威服务仓 `ai-native-tools/tools/prompt-optimizer-service` **不在 git 版本控制内**
    （`ai-native-tools/.gitignore` 第 9 行 `/*` 把它连同整个 `tools/` 排除），
    且其 `internal/optimizer/llm_client_test.go` 原本是 0 字节空文件，
    导致整个模块 `go build ./...` 失败 —— 本轮已补上真实测试并修好构建。
