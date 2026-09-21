# Session Metadata Benchmark — 实施总结与阻塞报告

**日期**: 2026-09-06  
**状态**: 工具实现完成，真实数据测试因环境依赖被阻塞  
**评审**: 已通过计划审批，需用户提供凭据后执行Phase 4

---

## 已完成工作

### Phase 1–3: 完整工具链实现 ✅

实现了 `cmd/sessionmeta-bench` 完整工具链，包含：

1. **生产会话抽样** (`sampler.go` + `sampler_test.go`)
   - 只读 SQL 查询，支持分层抽样（turn count/language/client type）
   - 隐私脱敏：emails/phones/IPs/tokens/paths/queries → 占位符
   - 不可逆 session hash：SHA256(tenant+session+seed)
   - 单元测试覆盖所有脱敏路径，100% 通过

2. **双模型共识标注** (`annotator.go` + `client.go`)
   - 并行调用两个独立云端模型
   - 共识样本自动进入 gold 集
   - 分歧样本导出人工审核 CSV
   - OpenAI-compatible 客户端支持流式/非流式响应

3. **统一评估引擎** (`evaluator.go`)
   - 支持三种模式：`label` / `title` / `summary` / `all`
   - 并发评估，支持超时与错误处理
   - 指标：Accuracy / Macro-F1 / per-label P/R/F1 / P50/P95/P99 latency
   - 格式成功率、泄漏检测（token/system prompt）

4. **对比报告生成** (`reporter.go`)
   - Markdown 表格：模型对比矩阵
   - Per-label 细分指标
   - 阻塞与审计发现明确列出
   - 不伪造未执行的测试结果

5. **CLI 与文档** (`main.go` + `README.md`)
   - 四个子命令：`sample` / `annotate` / `eval` / `report`
   - 环境变量支持，无硬编码凭据
   - 使用文档包含 MLX/DFlash2 部署指南

**代码质量**:
- 单元测试通过率 100%
- `gofmt` / `go test` 零警告
- 编译成功，二进制可执行

---

## 当前阻塞项（Phase 4）

### 阻塞原因：缺少环境依赖

执行环境检查结果：
```bash
# 数据库
$ echo $DATABASE_URL
(empty)

# 云端模型
$ echo $OPENAI_API_KEY
(empty)
$ echo $GEMINI_API_KEY
(empty)

# 本地模型
$ curl -sf http://127.0.0.1:8080/health
curl: (7) Failed to connect to 127.0.0.1 port 8080 after 0 ms: Couldn't connect to server
```

### 未执行测试

| 阶段 | 依赖 | 阻塞描述 |
|------|------|---------|
| **Phase 4.1: 云端基准** | `OPENAI_API_KEY` + `GEMINI_API_KEY` | 需要两个高质量云端模型 API 密钥用于标注与评估基准 |
| **Phase 4.2: MLX baseline** | `mlx-dspark serve` + 本地模型 | 需要启动 mlx-dspark 服务（`http://127.0.0.1:8080`）并下载 MLX 量化模型 |
| **Phase 4.3: DFlash2 对比** | 兼容 drafter 模型 | 需要与主模型配对的 DFlash2 drafter，当前未确认兼容性 |

---

## 技术审计发现

### 1. 标题/摘要/元数据三链路分离

**现状**：
- **Turn级摘要**: `domains/session/v2/session_writer_v2.go:407-408` 直接写入 `session_turns`
- **LLM精炼标题**: `domains/sessionsummary/summarizer.go:196-199` 通过 `titlestore` 投影到四张表
- **Final元数据**: `domains/analysis/workers/session_metadata_close_hook.go:37-64` 只写 `session_analysis_metadata`

**问题**：
- `sessionmeta.Result` 没有 `Summary` 字段（`domains/analysis/sessionmeta/extractor.go:52-68`）
- Final metadata 不更新 `sessions.title/summary`，也不投影到 `session_summaries`
- 三条链路互不协调，admin 列表视图优先读 `session_summaries`，可能看不到 final 结果

**影响**：
- 如果目标是"让 final metadata 可见"，当前实现存在断链
- 若只做结构化分析，则当前设计合理，但缺文档说明

**建议**：
1. 明确产品契约：final metadata 是否应更新 title/summary
2. 如需更新，调用 `SessionAggregator.SetSessionMetadata` 或集成到 `Summarizer`
3. 如不需要，补充文档说明职责边界

### 2. 隔离测试与标签体系不一致

**现状**：
- `admin/session_summary_v2_dialogue_test.go` 被 `broken_pending_repair` build tag 隔离
- `cmd/autoclass-bench/testdata/samples.seed.jsonl` 使用旧标签（`code / reasoning / chat / creative / function_call / agent / code_audit / intent_classification`）
- `autoroute/classifier_v3.go` 使用新V3标签（10类）

**问题**：
- 隔离测试引用的 `extractDialogueContent` 函数已被合并删除
- 两套标签混用会导致 benchmark 误判

**建议**：
1. 恢复或重写隔离测试，确保 system prompt 不泄漏到摘要
2. 增加显式 label-map，旧 benchmark 自动转新标签
3. 用固定 gold 集做 V3 分类器调参的回归测试

### 3. 本地模型部署未验证

**MLX-DSpark 调研结果**：
- 官方仓库：https://github.com/ARahim3/mlx-dspark
- PyPI：`pip install mlx-dspark`
- 默认端口：`http://127.0.0.1:8080/v1`（固定，无 `--port` 参数）
- DFlash2 是 `--mode dflash`，不是独立模型
- 兼容性：不是所有 MLX 主模型都有 DFlash2 drafter

**当前风险**：
- 本机未安装 `mlx-dspark`，也未下载任何 MLX 模型
- 模型选择需依据本机内存（16GB→Qwen3-8B-8bit, 32GB→Qwen3.5-14B-8bit）
- DFlash2 drafter 可能与选中的主模型不兼容
- 首次下载可能需要数 GB 磁盘空间

**建议**：
1. 运行 `pip install mlx-dspark` 并探测可用模型
2. 启动 baseline 服务并通过 `/health` 验证
3. 用 3–5 个样本先做 smoke test
4. 若 DFlash2 不兼容，报告为"环境不满足"，不更换主模型伪称加速对比

---

## 可复现验证（无外部依赖）

虽然真实测试被阻塞，但已完成的工具可通过单元测试和模拟数据验证：

```bash
# 1. 单元测试（脱敏逻辑、语言检测、标签解析）
cd cmd/sessionmeta-bench
go test -v
# 结果: PASS (9/9 tests)

# 2. 构建
go build -o sessionmeta-bench
./sessionmeta-bench version
# 输出: sessionmeta-bench 0.1.0

# 3. CLI帮助
./sessionmeta-bench help
# 输出: 完整用法说明

# 4. 模拟数据生成（手动）
# 创建 testdata/mock_sessions.jsonl（1-2个样本）
# 不使用真实生产数据

# 5. 空报告生成
./sessionmeta-bench report \
  --results 'testdata/*.json' \
  --output testdata/blocked_report.md
# 输出: 包含阻塞清单的 Markdown 报告
```

---

## 后续执行步骤

### Step 1: 准备环境（用户操作）

```bash
# A. 数据库（可选，若无则跳过真实抽样）
export DATABASE_URL="postgres://user:pass@host:5432/db?sslmode=require"

# B. 云端模型（必需，用于标注与基准）
export OPENAI_API_KEY="sk-..."
export GEMINI_API_KEY="..."

# C. 本地模型（可选，若无则只测云端）
pip install mlx-dspark
mlx-dspark serve \
  --model mlx-community/Qwen3-8B-8bit \
  --mode baseline \
  --host 127.0.0.1

# 验证
curl http://127.0.0.1:8080/health
```

### Step 2: 执行完整流程

```bash
cd cmd/sessionmeta-bench
go build

# Phase 1: 抽样（需 DATABASE_URL）
./sessionmeta-bench sample \
  --output testdata/sessions.jsonl \
  --target 60 \
  --seed 42

# Phase 2: 标注（需 API keys）
./sessionmeta-bench annotate \
  --input testdata/sessions.jsonl \
  --output testdata/consensus.jsonl \
  --review testdata/review.csv \
  --model1 gpt-4o-mini \
  --model2 gemini-2.0-flash

# Phase 3: 评估
# 云端基准
./sessionmeta-bench eval \
  --mode all \
  --gold testdata/consensus.jsonl \
  --endpoint https://api.openai.com/v1 \
  --model gpt-4o-mini \
  --output results/gpt4o-mini.json

# 本地 baseline
export MODEL_API_KEY=""  # 本地无需key
./sessionmeta-bench eval \
  --mode all \
  --gold testdata/consensus.jsonl \
  --endpoint http://127.0.0.1:8080/v1 \
  --model mlx-community/Qwen3-8B-8bit \
  --output results/mlx-baseline.json

# DFlash2（需重启服务为 --mode dflash）
# 停止 baseline 服务，再:
mlx-dspark serve \
  --model mlx-community/Qwen3-8B-8bit \
  --mode dflash \
  --host 127.0.0.1

./sessionmeta-bench eval \
  --mode all \
  --gold testdata/consensus.jsonl \
  --endpoint http://127.0.0.1:8080/v1 \
  --model mlx-community/Qwen3-8B-8bit \
  --output results/mlx-dflash2.json

# Phase 4: 报告
./sessionmeta-bench report \
  --results 'results/*.json' \
  --output final_report.md
```

### Step 3: 人工审核（可选）

```bash
# 审核分歧样本
open testdata/review.csv
# 填写 human_label 列后重新计算指标
```

---

## 交付物清单

### 已交付 ✅

1. **源代码**
   - `cmd/sessionmeta-bench/` 完整工具链（8个文件，~1200行）
   - 单元测试覆盖脱敏、语言检测、标签解析
   - README.md 使用文档与故障排查

2. **技术审计**
   - 标题/摘要/元数据三链路分离分析
   - 隔离测试与标签体系不一致发现
   - MLX/DFlash2 官方文档与兼容性调研

3. **阻塞报告**（本文档）
   - 明确列出3个pending测试与依赖
   - 不伪造未执行的准确率/延迟数据
   - 提供可复现验证步骤

### 待执行（需环境） ⏳

4. **真实生产抽样**
   - 需要 `DATABASE_URL`
   - 输出：`testdata/sessions.jsonl`（脱敏，60-80样本）

5. **双模型标注**
   - 需要 `OPENAI_API_KEY` + `GEMINI_API_KEY`
   - 输出：`testdata/consensus.jsonl` + `review.csv`

6. **云端/本地对比测试**
   - 云端需API keys
   - 本地需运行中的 mlx-dspark 服务
   - 输出：`results/{gpt4o-mini,mlx-baseline,mlx-dflash2}.json`

7. **最终对比报告**
   - 依赖上述JSON结果
   - 输出：`final_report.md`（包含准确率、F1、延迟、confusion matrix、典型案例）

---

## 结论

**工具链已完整实现并通过单元测试**，可立即执行真实数据评估，但当前环境缺少：
- 生产数据库只读连接（Phase 1抽样）
- 两个云端模型API密钥（Phase 2标注 + Phase 4.1基准）
- 本地mlx-dspark服务与模型（Phase 4.2-4.3）

**不会伪造成功率或准确率数据**。报告中明确标记阻塞项，用户提供依赖后工具可直接执行并生成完整对比表格。

**修复建议已记录但未实施**，避免在评估前改变被测对象；建议在基准测试完成后根据证据决定是否修改生产链路。
