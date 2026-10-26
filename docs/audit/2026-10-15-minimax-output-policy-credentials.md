# MiniMax 输出误拦与人工凭据声明修复（r1034）

范围：修复代码、扩展场景、验证、提交并推送；本轮不部署154/245，不修改生产数据或策略。
基线：`14d9fed9c0b801a5ef2d71feae6417835fa77903`。
代码候选：`5e9846632d1e0f41cbefd3fc9ea5687daa1c28ec`。

## 结论与实现

原始故障中的1971字节Bash参数包含 `sed` 的凭据通配匹配和遮蔽替换，被赋值正则当作真实口令，随后工具守卫整流阻断。上游原厂返回正常，问题在网关识别与终态归类。原始人工输入中的帐号/密码组合还存在漏脱敏。

- 共享 `credentialtext.DetectionText` 仅在完整sed替换的匹配段、替换段均证明没有真实秘密时屏蔽检测视图，保持字节偏移与原文。真实替换值、含秘密的匹配段、混合脚本和未知形态继续检查。
- 人工输入支持中英文明确声明、帐号/密码组合、冒号/等号/全角斜杠、改成/设置为/is/set to、Markdown和多种引号；帐号和口令分别生成可逆类型标记。测试断言映射包含完整口令，覆盖内部问号、空格和斜杠。
- 自然语言扩展只用于输入。状态说明不扩张工具输出阻断；无声明路径、窄问句与未引用的变量参考保持原样。带引号口令、明确修改动作和单引号shell字面值不会被问句/变量豁免。
- 后置合规按完整工具JSON解码后检查，片段在拼接前暂存；数字精度、键与非字符串PII检出保留。键/数字需要脱敏时不静默改型，由拦截器按现有同属主策略决定阻断；策略原生block不被属主豁免。
- 工具JSON在构建map前按token检查并拒绝重复键（含unicode转义后同名及嵌套键），避免早期秘密被覆盖后漏检。
- 阻断原因仅记录类型与字段位置，证据使用遮蔽标记。输出策略终态优先于通用survival失败，只记录一个终态；没有实际执行证据时清空缓存的首候选归属。

## 验证证据

| 检查 | 结果 |
|---|---|
| 7相关包完整测试（CGO_ENABLED=0，count=1） | 通过；既有跨秒波动后完整重跑通过 |
| 相同7包完整race（count=1） | 通过 |
| 全仓 `CGO_ENABLED=0 go vet ./...`、`go build ./...` | 通过 |
| cmd/gateway Sanitize/Restored/OutputCompliance接线子集 | 通过 |
| 原始1971B参数：前置守卫与完整guard→restore→真实builtin非流式链 | 通过；客户端arguments逐字相等 |
| Chat/Responses/Messages分片工具流 × 单/双引号sed脚本 | 6组通过；人工口令精确还原 |
| 三公共请求handler真实输入中间件→假上游捕获 | 通过；完整与部分口令均未进入上游正文 |
| 帐号/口令分类型、普通路径/问句、字面弱口令与变量参考 | production YAML与fallback均通过 |
| 数字/键PII、owner相同/不同的body/event/joined、原生block | 通过 |
| 8种强修改动作×what?/which?、重复JSON键及body/event/joined载体 | 通过 |
| survival策略阻断、双候选holdback审计归属 | 通过；单终态、正确分类、无猜测归属 |
| `git diff --check`、Task Stop Audit | 通过 |

相关7包：`internal/credentialtext`、`security/sanitize`、`domains/outputcompliance`、`domains/hooks/outputcompliance`、`domains/hooks/response`、`domains/streaming`、`domains/streaming/executors`。

九个Go overlay变异均以预期行为断言失败，非编译失败：撤去sed检测视图、输入声明范围、输入/输出作用域隔离、工具JSON解码、策略终态优先分支、数字PII检查、owner形状策略、强修改动作分隔词、重复键拒绝。

本地证据：`/tmp/minimax-policy-final-{unit,race,vet,build,wiring}.log`、`/tmp/minimax-policy-final-unit-rerun.log`、`/tmp/minimax-policy-verified-gate.log`、`/tmp/minimax-policy-mutations/*.log`。这些是本地验证记录，不随代码提交。夹具仅含不带真实凭据的工具命令；生产会话正文和原始密码未提交。

三轴审查（代码、规格、Go/标准）均批准代码候选 `5e9846632d1e0f41cbefd3fc9ea5687daa1c28ec`，此前发现已闭环。

## 已知限制与门禁

`bash verify.sh` 在干净基线与修复树均退出1，停在既有cgo约定断言：脚本设置CGO_ENABLED=0后，该约定测试在模拟Linux/CI环境继承了此设置。没有到达全仓测试，不能声称统一门通过。本项目AGENTS.md明确允许sanitize/streaming子集作为这类修复的验证门；本轮没有修改CI、故意红闸门或添加排除项。

`TestBridgeWireBytesIdenticalWithImmediateGate/responses_passthrough_normal_stream` 在一次完整测试中跨秒：两份输出的created_at相差1，其余字节归一化后相等。干净基线worktree执行该用例count=2000同样复现（2.071s），确认是既有测试时钟波动；没有修改或屏蔽该判据。证据：`/tmp/minimax-policy-pristine-clock.log`。

没有执行真实供应商付费请求或生产部署；证据为历史参数回放、真实网关检查链与本地handler集成验证。ACC sessionstore缺少本机token，保留本地Task Stop Summary与审计报告，不把无法写入的外部会话归档记为完成。

回滚：按逆序revert本任务修复提交；无DDL、生产配置或数据回滚步骤。
