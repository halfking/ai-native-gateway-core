# 会话压缩算法审计（2026-08-27）

> 任务来源：用户报告下游 LLM 收到压缩摘要后声称"首条用户消息保留在下方"，却把 `<system-reminder>` skills 清单当成用户输入。要求检查会话压缩算法 + 检查 245 日志。
> 审计仓库：本仓（`ai-native-tools/llm-gateway/llm-gateway-go-5`，main @ f95f82483，v2.5.0 build_seq 1770）。
> 注意：`official-deploy/services/llm-gateway-go-5` 目录已被清空（仅剩 `.zcode`，且被父仓 `.gitignore:143` 忽略），本仓是 canonical。

## 1. 245 日志证据（只读采集）

日志入口：`/var/log/llm-gateway-go/gateway.{stdout,stderr}.log`（llmgo-245.service，append 模式）。

| 证据 | 数量 | 含义 |
|---|---|---|
| `session_compressor: LLM summary failed/no-op, falling back to mechanical trim` | 157× | LLM 摘要路径频繁失败，回退机械裁剪 |
| `session_compressor: v2 cache failed, falling back to v1` | 6× | v2 缓存降级，量少 |
| `compression_error` / `compaction_error` / panic | 0× | 无崩溃级错误 |

**观测缺口（P1，运维）**：245 unit 是 `append:` 输出模式，压缩错误写 stderr 文件；而 `scripts/deploy-lib/post-deploy-verify.sh:100-107` 只扫 `journalctl`，会漏掉文件里的 `postgres disabled` / 压缩回退信号。建议 post-deploy-verify 增补对两个日志文件的 grep。

## 2. 六项审计结论

| # | 问题 | 状态 | 证据（canonical 仓 `domains/hooks/compression/`） |
|---|---|---|---|
| 1 | system-reminder 被误钉为 FirstUser | ✅ 已修 | `retain.go:183-184` 用正确字面量 `<system-reminder>`/`</system-reminder>`；`extractOpenAI`（retain.go:86）/`extractAnthropic`（retain.go:126）命中即 continue；`splitSystemAndTail`（rebuilder_openai.go:186）保留 pre-intent reminders |
| 2 | Anthropic 尾部 tool_result 孤儿 | ✅ 已修 | `rebuilder_anthropic.go:170` 重建后接 `TrimAnthropicTail`；`rebuilder_anthropic_test.go:211` 有回归测试 |
| 3 | StripToolInfo 缺协议参数 | ✅ 已修 | `strip.go:70` 签名含 `protocol string` |
| 4 | stripThinkingBlocks 重建丢字段 | ⚠️ 遗留 P2 | `strip.go:646-655` 重建只保留 `role`+`content`；若同一消息同时含 thinking 块与 `tool_calls`/`tool_call_id`/`name` 等 OpenAI 字段，重建后丢失 → 工具链断裂。触发面窄（消息需同时满足两条件），故 P2 |
| 5 | contentFingerprint 忽略非 text 块 | ⚠️ 遗留 P2 | `diff.go:278-285` 数组 parts 只拼 `p.Type == "text"`；`input_text`/`output_text` 块的 text 被忽略 → 指纹退化为 role-only，同角色不同内容碰撞 → diff 对齐可能错位。注意 retain.go:174 的 isSystemReminderMessage 已识别这些类型，fingerprint 未同步 |
| 6 | 重复压缩 marker 幂等 | 🟡 待验证 | `diff.go:302-326` isSummaryMarkerMsg 只检测首个 text part 前缀；多轮压缩后 marker 嵌套/重复场景未在本轮补测，留新会话验证 |

审计中排除的疑点：早前会话快照里 retain.go 出现 `&lt;system-reminder>` 是转录转义假象；canonical 仓字节正确。

## 3. 测试结果（2026-08-27 实测）

```
go test ./domains/hooks/compression/... -count=1
ok  compression 0.893s | caveman 1.472s | lite 1.055s | summary 1.862s
```

## 4. 根因定性（对应用户原始报错）

下游模型看到"first user message preserved verbatim below"却找不到原文，是因为该文案由 `CompressionSummaryPrefix`（rebuilder_openai.go:39-41）无条件承诺，而当 LLM 摘要失败回退 mechanical trim（245 上 157 次）时，重建路径与摘要路径的 B-track 钉住行为不完全一致；叠加历史上 system-reminder 误钉问题（已修，#1），下游把 reminder 当用户输入。#1 修复后主要剩余风险是 #4/#5 的低频错位，以及 157 次/日的摘要失败本身（应查 summary_client 对上游错误的分类）。

## 5. 新会话遗留任务

1. **P2 修复 #4**：stripThinkingBlocks 重建时透传 `tool_calls`/`tool_call_id`/`name`（改为删除 thinking 后原样保留其余 JSON 字段）
2. **P2 修复 #5**：contentFingerprint 把 `input_text`/`output_text`（含 Anthropic tool_use/tool_result 的关键字段）纳入指纹
3. **P1 运维**：post-deploy-verify 增扫 `/var/log/llm-gateway-go/gateway.stderr.log`
4. **验证 #6**：构造双重压缩 fixture 验证 marker 幂等
5. **深挖**：157 次 "LLM summary failed" 的上游错误分类（summary_client.go），确认是超时/鉴权/模型不可用哪类为主
