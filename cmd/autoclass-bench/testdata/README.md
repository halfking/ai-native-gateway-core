# autoclass-bench testdata

`samples.seed.jsonl` 是冒烟用的种子样本集（40 条，中英混合，覆盖 8 个文本可判定的任务类型：code、reasoning、chat、creative、function_call、agent、code_audit、intent_classification）。

`planning` 已于 2026-09-05 移除：生产 LLM fallback 分类 prompt（`autoroute/classifier_llm.go` buildClassificationPrompt）的固定标签集不含该类，保留只会让 bench 结果偏离生产行为（启发式层 `IsPlanningRequest` 仍存在，不受影响）。

用途：验证 `cmd/autoclass-bench` 工具本身和某个 provider 端点的连通性、输出格式合规率与延迟量级。**不能**用它得出的准确率作为放量依据——正式门槛请按 `docs/auto-model-optimization/10-free-llm-integration.md` 准备 500–1000 条经人工审核的样本。

说明：

- `vision`、`long_context` 未纳入：这两类靠结构信号（图片部件、token 数）判定，纯文本无法有效标注。
- 每行一个 JSON 对象：`{"text": "...", "label": "..."}`；`#` 开头的行和空行会被跳过。

用法示例：

```bash
go run ./cmd/autoclass-bench \
  -endpoint https://api.groq.com/openai/v1 \
  -api-key-env GROQ_API_KEY \
  -model llama-3.3-70b-versatile \
  -samples cmd/autoclass-bench/testdata/samples.seed.jsonl
```
