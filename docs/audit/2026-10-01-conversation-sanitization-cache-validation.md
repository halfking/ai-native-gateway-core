# 会话脱敏、压缩与输出防护初轮验证记录

**2026-10-02 复审纠正：以下为初轮历史记录，不能作为最终验收结论。** 复审发现混合标记、单帧工具 JSON、结构化 data、数字凭据、clean-turn、真实 handler 回退、V2 读写及 Redis 冷读清零等遗漏。初轮测试通过并不能证明这些功能已完成；依赖审计也在复审中实际执行并发现漏洞。最终变更、失败证据、验证与风险见 [批判式审计报告](2026-10-02-conversation-sanitization-critical-audit.md)，方案已同步修订。

日期：2026-10-01。方案：[业务流程、优化与剩余边界](../design/2026-10-01-conversation-sanitization-cache-plan.md)。

结论：本次已实施稳定替换、映射代际隔离、历史与工具参数脱敏、新输出敏感信息强制 gate、摘要输出检查、SSE 后续链检查，以及 RAW/SANITIZED 来源元数据。影响范围的 14 个 Go 包测试通过；7 个核心/启动包的竞态测试通过；go vet、网关编译及 diff 格式检查通过。结果为本地代码验收，尚无生产/真实供应商端到端验收证据。

## 1. 修改前复现与修复验证

先运行新增的失败用例，确认不是仅根据代码猜测问题：

- `TestSessionReplayKeepsSanitizedCacheIdentity`：原实现第二轮编号漂移，脱敏历史不等。
- `TestReplayedAssistantAndToolSecretsDoNotLeaveGateway`：原实现 assistant/tool 历史明文被跳过。
- `TestHolderReleaseIsCheckedByEveryLaterInterceptor`：原链只检查终止帧，没有将 holder 释放的多条早先事件交给下游逐条处理。

修复后上述测试及邻近回归通过。缓存代际用例还含反向对照：直接调用旧匹配逻辑确实会保留旧 summary；由 compressor 经过 generation 检查后才丢弃它，防止“测试夹具本来不命中”的假阳性。

## 2. 操作模拟与证据

所有样本均为合成数据。未使用真实凭据、远程服务器或模型付费任务。

| 模拟操作 | 验收行为 | 自动测试 |
|---|---|---|
| 同一会话两轮相同手机号回放 | 脱敏正文一致，BuildOutboundMessages unchanged | `TestSessionReplayKeepsSanitizedCacheIdentity` |
| 同值重复、新值增加 | 相同值复用，只有新值递增 | `TestSanitizeInputMiddleware_MultiRoundNoCollision`、offset/cross-tenant 回归 |
| 删除 map/offset/generation 后重建 | 新 generation；旧压缩 summary 不可重用 | `TestRecreatedMappingCannotReuseCompressedHistory` |
| 相同脱敏正文改变 generation | memo miss；V2 恢复 marker 不可沿用 | `TestMemoDoesNotCrossSanitizeGeneration`、`TestV2RecoveryDoesNotCrossSanitizeGeneration` |
| 上游生成期间 Redis 字典重建或消失 | 本轮 Context 映射还原旧值，不使用新字典值 | `TestResponseUsesRequestMappingAfterRedisGenerationChanges` |
| 字面 token 在另一字段，以 unicode 转义编码 | 预留编号，不与新工具密码相撞 | `TestLiteralMarkerInOtherFieldCannotAliasToolCredential` |
| assistant/tool 历史携带电话、邮箱、内网 IP | 敏感值从模型可见请求消失，工具 ID 保留 | `TestReplayedAssistantAndToolSecretsDoNotLeaveGateway` |
| JSON 密码包含空格和转义引号，大整数超过 2^53 | JSON 有效；大整数不丢精度；已知凭据可还原 | `TestStructuredToolCredentialsPreserveEscapesAndRestore` |
| 工具密码值是字符串数组 | 每个凭据字符串被替换 | `TestCredentialArrayLeavesAreSanitized` |
| 帐号/短密码、公网及 IPv6、URI、私钥 | fallback 与真实 YAML 同时覆盖；原值 round-trip 相同 | `TestOperationalRulesFromDefaultsAndProductionConfig` |
| 同一输出包含已知 token 和新密码/IP/帐号 | 已知值还原，新值 `[REDACTED]` | `TestOutputSensitiveMasksNewValuesAndRestoresKnownValues` |
| mask 模式生成敏感工具参数 | 整条工具操作阻断 | `TestGeneratedSensitiveToolOperationsAreBlocked` |
| block 模式或检测器不可用 | 不释放敏感输出 | `TestOutputSensitiveBlockAndDetectionFailure` |
| 上游标准 error.message 回显密码/IP | 错误文本遮蔽，错误类型保留 | `TestProviderErrorMessageIsSensitiveOutput` |
| 密码、token 分成两段 SSE；terminal 释放 | 各 lane 合并检测；新密码遮蔽、已知 token 还原 | `TestOutputSensitiveSplitStreamMaskAndRestore` |
| 无 terminal 的 EOF | Flush 内容仍通过后续还原链 | `TestOutputSensitiveEOFStillRunsRestoration` |
| completion choices.text / reasoning_content 分片 | 新秘密遮蔽，分片 token 正常还原 | `TestLegacyCompletionAndReasoningStreamsMaskAndRestore` |
| 未知 JSON envelope 还原含引号、反斜线、换行的原文 | 编码后仍为有效 JSON | `TestUnknownEnvelopeRestorationPreservesJSON` |
| 摘要模型新生成密码，原输入无敏感信息 | 摘要拼接前遮蔽；middleware 仍传递 guard | `TestSummaryGeneratedSensitiveTextIsMaskedBeforeRetry` |
| 摘要检测失败/第一候选不安全 | 机械裁剪或下一安全候选；不返回未检查摘要 | `TestSummaryGuardFailureSelectsMechanicalFallback`、`TestSummaryClientAdapterChecksEachCandidateBeforeReturning` |
| 重复 token 分布在不同消息 | RAW/SANITIZED 指纹不同，每条消息替换次数准确 | `TestRawAndSanitizedSnapshotsKeepDistinctStageIdentity` |
| V2 元数据带嵌套 plaintext/body | 代际/指纹保留，明文属性被筛掉 | `TestApplyCompressionMeta_RestoresRecoveryMetadata`、`TestEntryToProcessedRequest_CompressionMetaWhitelist` |
| 无 Redis 启动 | 同时挂强制输出检查与还原，顺序正确 | `TestSmartSaniGuardWithoutRedisWiresOutputBeforeRestoration` |

既有回归还覆盖跨租户并发、租约过期禁止旧 worker 覆盖、未知/残缺标记、媒体字段保留、signed thinking 拒绝改写、SSE buffer 超限与 owner policy。新增 gate 使用独立 stream state；不能通过关闭可选 output compliance 或 owner 豁免禁用。

## 3. 测试执行记录

完整受影响范围（14 包）通过：

```sh
go test ./security/sanitize ./domains/hooks/compression/... \
  ./domains/hooks/response ./domains/hooks/outputcompliance \
  ./domains/session/v2 ./internal/sessionv2mirror \
  ./cmd/gateway ./domains/streaming -count=1
```

记录：`/tmp/session-complete-validation.log`。流式包用时约 90 秒。

竞态检查第一组（包含 V2/mirror）通过：

```sh
go test -race ./security/sanitize ./domains/hooks/response \
  ./domains/hooks/outputcompliance ./domains/hooks/compression \
  ./domains/session/v2 ./internal/sessionv2mirror -count=1
```

记录：`/tmp/session-complete-race.log`。最终补充长耗时映射回退后，再验证修改涉及的包与 gateway 接线：

```sh
go test -race ./security/sanitize ./domains/hooks/compression \
  ./domains/hooks/outputcompliance ./domains/hooks/response ./cmd/gateway -count=1
```

记录：`/tmp/session-final-epoch-race.log`。两组合计覆盖 7 个核心/启动包；未把此前旧测试夹具导致的失败列作通过证据。race 编译时 vendor `go-m1cpu` 有既有 C 数组扩展警告，命令退出码为 0，无 data race 报告。

静态与编译检查通过：

```sh
go vet ./security/sanitize ./domains/hooks/compression/... \
  ./domains/hooks/response ./domains/hooks/outputcompliance \
  ./domains/session/v2 ./internal/sessionv2mirror \
  ./cmd/gateway ./domains/streaming
go build -o /tmp/llm-gateway-session-validation ./cmd/gateway
git diff --check
```

记录：`/tmp/session-final-vet.log`、`/tmp/session-final-build.log`。未运行仓库全量 `go test ./...`、线上部署或真实模型请求。测试日志位于本地临时目录；本报告及方案为可保存的验收记录。

## 4. 安全审查

采用 `/Users/xutaohuang/.codex/skills/security-review/SKILL.md`。

- 扫描本任务新增/修改的 29 个非测试 Go 文件，检查真实形态的 AWS key、live payment key、私钥正文、带密码数据库 URI；未发现新增明文密钥候选。规则定义和合成测试样本单独人工检查，不能由这些字面常量推断真实密钥泄漏。
- 检查本次文件中的 shell execution/unsafe/SQL 拼接危险模式，无新增候选。人工核对租户来源、Lua 参数、generation 检查、JSON 数字/转义、输出失败阻断和后置链释放。
- `.gitignore` 已覆盖 `.env`、`.pem`、`.key` 等敏感文件；本任务没有改变依赖文件或凭据。
- **依赖漏洞审计未完成**：当前环境没有 `govulncheck`，skill 所引用的 `run-all-checks.sh` 也不存在。没有把 verification contract 的 EXPECTED 当作实际结果；go vet 不能代替依赖漏洞审计。
- Redis 原值仍明文可逆；原始日志和媒体保护边界、V1 DB 恢复保守失效、流式延迟/容量限制见方案第 7 节。不得将本次审查结论扩大为全仓库/生产环境无漏洞。

安全扫描摘要：`/tmp/session-security-scan.json`。本次不安装新审计工具、不调用外部扫描服务。

## 5. 交付与操作边界

- 方案、代码、模拟测试和本报告已完成，代码位于当前工作目录，未提交、push 或部署。
- 未修改数据库 schema；V2 使用现有 JSON 元数据通道。
- 保留工作开始前已有的 `web/public/menu-config.json` 修改，未纳入本次优化。
- 全局 KxMemory 工具在本会话不可用，没有假称已检索/写入；连续性由本地方案及验证报告提供。
