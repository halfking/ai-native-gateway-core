# 会话脱敏与压缩批判式审计

日期：2026-10-02。基线：`e3406f9e29eed627280905fe622747859f3d8819`；修复分支：`codex/conversation-sanitization-critical-audit`。目标包括复审、修正、文档、提交、合并 main 与推送；不包括服务部署。

## 结论与根因

初轮验证遗漏了真实协议、最终派发及冷缓存边界。问题不只是规则数量：按整条匹配豁免标记、按字段名豁免结构数据、只检多片段、凭据键类型契约丢失、JSON 转义/数字处理错误、把当前请求当完整历史、HSET 忽略零值、V2 只写未读共同造成绕过或错误恢复。

本次先用失败用例/独立复现确认，再修复执行路径。不能以此宣称所有敏感数据、所有协议扩展或生产系统均安全。初轮报告已明确改为历史记录。

## 已修正问题及可观察行为

| 问题 | 修正与证据 |
|---|---|
| 凭据/私钥混入有效标记即可跳过整个匹配 | 仅保护标记自身字节，其余范围继续检测；`TestCriticalMixedMarkerDoesNotExemptSecret` |
| 嵌套工具 JSON 的 Unicode 标记未预留 | 递归扫描嵌套 JSON，预留编号；`TestCriticalNestedToolUnicodeMarkerIsReserved` |
| 数字、布尔、对象凭据无类型策略 | 输入 400 不派发，输出阻断；字符串数组可脱敏，null 不改型；`TestCriticalUnsupportedCredentialShapesFailClosed` |
| Responses custom_tool / reasoning_text 不在检查路径 | 补齐工具语义、事件检查及还原；`TestCriticalCustomToolOperationsBlocked`、`TestCriticalReasoningTextStreamIsCheckedAndRestored` |
| media discriminator / data 对象跳过敏感字段 | 不豁免媒体对象的兄弟字段；只豁免指定二进制字段的字符串，对象/数组继续检查；`TestCriticalMediaShapedToolDoesNotHideText` |
| 单片段 JSON 工具参数未做完整结构检查 | terminal/EOF 一律检查完整工具 JSON，包含 Unicode 凭据键；`TestCriticalSingleFrameJSONToolAndOpaqueBoundaries` |
| 二进制被序列化后重复扫描而误阻断 | 结构检查成功直接通过，保留不透明载荷；同上测试 |
| 内部工具 JSON 还原损失大整数或引号/换行 | UseNumber + JSON 字符串转义；`TestCriticalToolRestorePreservesNumberAndEscapes` |
| clean-turn 丢 generation / 完整字典，Stats 使用累计值 | 保留本轮字典快照及活跃代际，Stats 只计本轮；`TestCriticalCleanTurnKeepsMappingGeneration` |
| 相同 generation 复用旧未检查摘要 | 当前 guard 复验命中正文，停止旧 memo namespace；`TestCurrentGuardRejectsPreviouslyCachedGeneratedSecret`、`TestCriticalCachedStructuredToolIsRecheckedBeforePrepare` |
| 真正 handler 的 NeverWorse 丢缓存摘要 | 对合并后已脱敏源比较/回退，临时 SourceBody 不入库；真实 HTTP→sanitizer→compressor→executor 测试断言 provider 收到三条完整历史 |
| RAW/delta 与完整历史对齐误报 | 增加 CompressionSourceSnapshot，按同范围检查；`TestDetectMisalignmentUsesRebuiltSourceScope` |
| 源快照 V2 只写未读 | typed decode/export、严格镜像、Redis 序列化、handler provenance 同时接通；V2/Redis round-trip 与镜像白名单测试 |
| HSET 省略零值导致旧统计/代际/引用复活 | 显式保存空安全与阶段字段，冷读测试使用新缓存实例，避免 L1 掩盖问题 |
| 旧 cut marker 被新 generation 重绑 | 显式保存 hcm/cm_*、对齐与阶段零值；`TestSessionMetadataResetSurvivesRedisColdRead` 与独立 miniredis 复现 |
| 非法 JSON 工具参数仍尝试字符串还原 | 阻断不可执行的参数，不返回损坏工具正文；更新 48h-audit 的旧兼容契约测试 |

真实 handler 用例 `TestChatHandlerPreservesCachedSummaryForTruncatedCleanTurn` 有负对照：仅恢复旧 guard 的临时 overlay 后失败，provider 收到 2 条；修复代码收到摘要、锚点、新消息 3 条。本次不是只断言 Prepare 输出就认为派发完成。独立流式矩阵覆盖 terminal/EOF × 单/多片段 × Unicode 密码键、不透明 data、可信标记，共 12 场景。

## 修改文件与职责

- `security/sanitize/`：规则、范围合并、标记预留、类型契约、协议输入、输出 gate、原生/流式还原、代际/请求字典与模拟测试。
- `domains/hooks/outputcompliance/`、`domains/hooks/response/`：完整工具 JSON 检查、custom tool/reasoning/audio 文本路径、强制检查层、holder 后续链。
- `domains/hooks/compression/`：摘要与缓存复验、memo v2、匹配代际、压缩源、回退、Redis 字段清零与冷读验证。
- `domains/streaming/handler.go`、`request_log_pipeline.go`、`cached_summary_dispatch_test.go`：真正派发回退与来源元数据。
- `domains/session/v2/cache_v2.go`、`internal/sessionv2mirror/`：元数据解析/导出、隐私白名单与测试。
- `cmd/gateway/`、`configs/sensitive_patterns.yaml`：实际启动接线、默认/严格输出动作、规则配置。
- `go.mod`、`go.sum`、`vendor/`：依赖漏洞修复及一致性更新；`tests/48h-audit/D14-security/data/`：非法工具 JSON 的阻断契约。
- 方案、初轮记录、本报告及 handoff：明确已验证行为与未覆盖边界。

## 验证记录

最终集成验证点：`bd635bdd69a582299fa9d82a66f51c377a210dba`，含本任务实现及同期远端 15 个提交；代码冻结后执行下表命令，均已结束。原始失败日志保存在本机 `/tmp/session-reaudit-red.log` 与 `red2.log`；中间的编译/旧契约失败均已修复并复跑。初轮 scoped 通过没有代替本轮全仓检查。

| 命令 | 实际结果 / 记录 |
|---|---|
| `env GOFLAGS=-p=2 make test` | exit 0，312 包通过、56 包无测试；`/tmp/session-reaudit-integrated-all.log` |
| `go test -race -p=2 ./security/sanitize ./domains/hooks/compression ./domains/hooks/outputcompliance ./domains/hooks/response ./domains/session/v2 ./internal/sessionv2mirror ./cmd/gateway ./domains/streaming -count=1` | exit 0，8 包通过，无 race 报告；`/tmp/session-reaudit-integrated-race.log` |
| `go vet ./...` | exit 0；`/tmp/session-reaudit-integrated-vet.log` |
| `go build -o /tmp/llm-gateway-critical-audit ./cmd/gateway` | exit 0；`/tmp/session-reaudit-integrated-build.log` |
| `/tmp/session-audit-tools/govulncheck ./...` | exit 0，可达漏洞 0；非可达项见下文；`/tmp/session-reaudit-integrated-govuln.log` |
| `/opt/homebrew/bin/bash scripts/scan-secrets.sh --mode=normal --baseline=scripts/scan-secrets.baseline --paths security/sanitize domains/hooks/compression domains/hooks/outputcompliance domains/hooks/response domains/session/v2 internal/sessionv2mirror cmd/gateway/sanitize_output_wiring_test.go domains/streaming/cached_summary_dispatch_test.go` | exit 0，302 文件、BLOCK 0、WARN 1（合成凭据赋值，人工核查）；`/tmp/session-reaudit-secret-scan-final.log` |
| `git diff --check` / staged diff check | exit 0；合并与推送前核查菜单文件未入提交 |

这些是本地验证：部分集成测试在缺少真实 DB/供应商时按自身条件跳过，不能把 `make test` 退出 0 解释成生产端到端通过。临时日志不是永久存储，本报告保存命令、结果、发现与边界。

依赖审计首次 `govulncheck ./...` 退出 3，发现 grpc 的 GO-2026-6348、GO-2026-6061，及 x/text 的 GO-2026-5970。更新 grpc v1.83.1、x/text v0.39.0 及必要传递依赖并同步 vendor 后，扫描退出 0，代码可达漏洞 0；仍报告 1 项被导入包漏洞及 5 项 required module 漏洞，扫描认为当前未调用。不能简写为全依赖无漏洞。

全仓重跑曾遇到 plugin-runtime 两个进程启动 15 秒超时；单独复跑通过，最终降低包并行度重跑。没有修改其测试时限来制造通过。

敏感信息扫描首次由 macOS Bash 3 执行，因缺关联数组报错且未产生完整报告，不能算通过。换用本机已有 Bash 5 后扫描识别到 3 个合成测试夹具（虚构数据库 URI、无效私钥字样）；改为运行时生成合成样本，保留原测试语义，不新增 baseline 豁免。最终完整报告仅一项 CRED_ASSIGN WARN，内容是用于混合标记绕过复现的虚构值，未认作真实凭据；没有宣称全仓秘密扫描通过。

## 遗留风险

无标签任意口令/业务语义、token-ID prompt、附件 OCR/ASR、媒体 URL 及未知扩展仍未全面覆盖。Redis 可逆字典仍保存明文，日志/数据库原始正文属于独立治理范围。强制 SSE 检查保留到终态，增加首字延迟且有 1 MiB 上限。旧 V1 DB 无 generation 的恢复保守失效。没有真实供应商、生产 Redis/Postgres、多实例灰度或压测证据。非可达依赖漏洞仍需后续治理。

未改动服务部署或数据库 schema。保留既有 `web/public/menu-config.json` 修改，提交明确排除它。KxMemory 工具不可用，未声称完成读写；本地方案、报告和 handoff 承担连续性。

## 实际提交、合并与推送回执

实现提交 `3483152cbfd249dd568dcbea13d2faa48caf8838`；文档提交 `c1e5da68d`；审计分支通过 no-ff 合并到 main（`09ba4c3ed`）。保留同期远端 20 个提交：其中前 15 个在全仓集成验证点中，随后 5 个只改迁移测试隔离/文档，合并后补跑 `go test ./internal/testdb ./sql/migrations/startup -count=1`、`go vet ./...` 与集成脚本语法检查均退出 0。没有将后续 targeted 检查描述成第二次全仓测试。

第一次 push 因远端并发新增提交被拒绝，随后正常 fetch/merge/push，未强推。成功推送的集成提交为 `e5006ff3304a1fa887bc76d0e9eb88f37d5baf7e`，随后 `git ls-remote origin refs/heads/main` 实测相同。最后仅补记文档回执，不改变已验证的会话实现；最终 HEAD 在会话最终响应和自动生成 handoff 中再次实测。工作区仅遗留原有菜单时间戳变更。

全仓测试日志 SHA-256：`7e03a40582f3808c255b774834f8f5ab871b1ca8bf7ec8d3c7e9767370865803`；8 包 race 日志：`cb66364032c6f983f19fea75c0242c629c717df4df1d1a7d259ab0846c7df77a`。

**上游已知风险另列：**同期 [迁移夹具审计](2026-10-02-round44-closure-migration-fixtures.md) §4A/收口记录了带 integration 标签的 `TestMigration715FreshChainApplyMigrations` 及 bg 集成门遗留失败；本次未运行或修复那组需要真实数据库的门。默认 `make test` 通过不能证明它们已修复，本任务也没有把这些失败隐藏成生产验收成功。
