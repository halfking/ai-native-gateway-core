# 2026-11-15 tool_json_invalid 误报根修——格式类守卫收口轮

## 问题背景

owner 报障：ZCode 收到错误提示「内容含敏感信息，等待审核（审核单 rv-2089）」，但实际请求没有敏感信息。

### 211 取证定案（rv-2089，2026-11-15 20:46:11+08）

- **请求链路**：ZCode → `/v1/chat/completions` → glm-5.3-flash → provider 33370（`https://apiclaude.cc/v1/chat/completions`）
- **拦截位置**：`chat.choice.0.delta.tool.0.arguments`，标签 `__tool_json@Bash`，116 字节
- **拦截规则**：`builtin.guard.tool_json_invalid`（格式类守卫，非内容规则）
- **证据原文**（`sanitize_block_evidence` 表）：
  ```
  {"description</arg_key><ac7a3bd7><arg_value><b88a6f17>查找项目目录</arg_value><5b656597></tool_call><c4cf82b7>
  ```
  
这段参数不是合法 JSON——GLM 原生的 `<arg_key>/<arg_value>/</tool_call>` 工具调用标记漏进了 JSON 参数。说明上游中转（apiclaude.cc）没有正确把模型的原生工具调用转成 OpenAI 的 `tool_calls` JSON 格式。

### 根因双重

1. **输出检查未关闭**：
   - `output_compliance.enabled=false`（10-17 设置）只管可选的内容合规面
   - `sanitize.output_check_enabled=true`（11-01 设置）是总开关，包括强制守卫
   - tool_json_invalid 属于强制守卫 ⇒ 一直在执法

2. **提示文案误导**：
   - 格式类守卫（tool_json_invalid / redact_invalid / frame_encode_failed）拦的不是敏感内容，而是结构性的格式问题
   - 但客户端看到的提示一律是「内容含敏感信息，等待审核……管理员放行后重试即可」
   - **实际上管理员放行审核单改变不了上游输出格式错误的事实** ⇒ 放行也没用

## 修复方案

### 1. 格式类守卫不再生成审核单

**位置**：`domains/streaming/policy_block_feedback.go`

新增 `isFormatGuard()` 判定函数，识别三种格式类守卫：
- `builtin.guard.tool_json_invalid`：上游返回的工具参数本身不是合法 JSON
- `builtin.guard.redact_invalid`：脱敏后的结果不是合法 JSON
- `builtin.guard.frame_encode_failed`：帧无法重新编码

修改 `registerPolicyBlockReview()`：格式类守卫直接返回空串，不调用 `sanitizerules.AddBlockReview()`。

**理由**：
1. 不是内容敏感性问题，管理员放行审核单也改变不了格式错误的事实
2. 应该引导用户重试（上游可能重新生成正确输出）或报告上游故障

### 2. 客户端提示文案分流

**位置**：`domains/streaming/policy_block_feedback.go:policyBlockClientMessage()`

格式类守卫走专用话术：
```
上游返回的内容格式有误（非法 JSON 或编码失败），请重试。若反复出现，可能是上游服务的工具调用格式转换问题，请报告网关管理员检查上游中转。
```

内容类规则（credential / pii.* 等）保持原话术：
```
内容含敏感信息，等待审核（审核单 rv-N）：管理员在网关后台放行后，重试即可获得内容。
```

### 3. 门测试（3 个新测试）

- `TestFormatGuard_NoReviewTicket`：验证格式类守卫不生成审核单
- `TestFormatGuard_ClientMessage`：验证格式类守卫的提示不误导为「敏感内容」
- `TestFormatGuard_vs_ContentBlock`：对照测试，确保内容类规则行为不变

## 验证

### 本地测试
```bash
go test -v -run TestFormatGuard ./domains/streaming/
# 3 个新测试全部 PASS

go test ./domains/streaming/
# 全量套件 PASS（99.837s）

go build ./domains/streaming/ && go vet ./domains/streaming/
# 编译和静态检查通过
```

### 预期行为变化

**修复前**（rv-2089 实测形态）：
- tool_json_invalid 生成审核单 rv-2089
- 客户端看到「内容含敏感信息，等待审核（审核单 rv-2089）：管理员在网关后台放行后，重试即可获得内容。」
- 管理员即使放行 rv-2089，重试时上游仍可能返回同样的坏格式 ⇒ 审批无效

**修复后**：
- tool_json_invalid 不生成审核单（返回空串）
- 客户端看到「上游返回的内容格式有误（非法 JSON 或编码失败），请重试。若反复出现，可能是上游服务的工具调用格式转换问题，请报告网关管理员检查上游中转。」
- 引导用户直接重试（上游可能重新生成正确输出）或报告上游故障

## 影响面

### 零风险

- 格式类守卫本来就是 fail-closed（阻断请求），修复只改变了**阻断后的提示文案和审核单生成**
- 内容类规则（pii.* / credential 等）的行为完全不变
- 72 小时内 tool_json_invalid 只有 3 单，频率很低

### 遗留项

1. **输出检查总开关语义**：`sanitize.output_check_enabled` 是否应该关闭所有输出检测（包括强制守卫）？目前它确实关闭，但需要确认这是否符合 owner 预期。
2. **上游故障分类**：格式类失败是否应该归为上游故障，触发候选切换？目前网关边收边转发，流已开始后不能重试，需要单独评估。
3. **rv-2089 处置**：可以在后台按误报关闭，不需要放行。

## 部署建议

**立即部署**，频率低且零风险。部署后：
1. 监控 tool_json_invalid 的日志，确认不再生成审核单
2. 如果 apiclaude.cc 的 glm-5.3-flash 再次出现同类问题，向该中转反馈其工具调用解析器漏出原生标签

## 文件清单

- `domains/streaming/policy_block_feedback.go`：新增 `isFormatGuard()`，修改 `registerPolicyBlockReview()` 和 `policyBlockClientMessage()`
- `domains/streaming/policy_block_feedback_test.go`：新增 3 个测试
- `docs/audit/2026-11-15-tool_json_invalid误报根修-格式类守卫收口轮.md`：本文档

## LESSONS

**§717：格式类守卫与内容规则的处置方向相反**

格式类守卫（tool_json_invalid / redact_invalid / frame_encode_failed）拦截的不是敏感内容，而是结构性的格式问题 ⇒ 处置方向：
- **不生成审核单**（管理员放行审核单改变不了格式错误的事实）
- **引导用户重试**（上游可能重新生成正确输出）或报告上游故障
- **提示文案必须说清楚「格式有误」**，不能误导为「内容含敏感信息，等待审核」

内容规则（pii.* / credential 等）拦截的是敏感内容 ⇒ 处置方向：
- 生成审核单 → 管理员判定 → 放行后重试即可
- 提示「内容含敏感信息，等待审核」是正确的

**合成规则名必须能区分处置方向** ⇒ 审核员/客户端能拿到正确的行动指引。
