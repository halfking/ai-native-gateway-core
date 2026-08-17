# 2026-07-14 — 会话优化 v2 文档审计与补充

## 变更清单

### 新增
- `docs/会话优化v2/04-厂商标准与适配矩阵.md` — 按 OpenAI、Anthropic Google Gemini、Mistral 官方 API 契约约束网关附件方案

### 修正
- `00-README.md`：修复重复的 `### 3` 标题编号，补充厂商适配结论
- `02-多模态能力审计报告.md`：修正 `ExtractAndSave` 为真实方法名 `ExtractFromOpenAIBody`/`ExtractFromAnthropicBody`；补充协议覆盖说明标注 Gemini 路径不在本次审计范围
- `03-多模态技术方案.md`：从"统一 URL 替换"改为"供应商感知引用"；新增三种引用模式（data URL / gateway URL / provider file URI）；修正 token/费用/脱敏验收条件
- `01-存储配置审计报告.md`：追加凭据与 URL 安全、生命周期一致性、热切换边界等风险审计

## 审计发现（第二轮）

| # | 严重度 | 问题 | 已修 |
|---|---|---|---|
| S1 | 必修 | 00-README 两个 `### 3` 标题重复 | ✅ |
| S3 | 必修 | 02 报告虚构方法名 `ExtractAndSave` | ✅ |
| S7 | 必修 | 04 矩阵缺诚信声明（webfetch 全失败） | ✅ |
| C3 | 应修 | 04 矩阵缺协议来源与国内厂商说明 | ✅ |
| C4 | 必修 | CHANGELOG 未更新 | ✅ |

## 验证
- `git diff --check` 通过
- 文档共 5 个 Markdown 文件，644 行
- 全量 `go test ./...` 通过
- `go vet ./...` 通过
