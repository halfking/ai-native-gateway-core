# 会话脱敏与缓存复审 handoff

## 任务与用户约束

审计压缩、帐号/密码/IP 等识别替换还原、RAW/SANITIZED/COMPACTED 与多级缓存、输出脱敏及回退；结合初轮方案批判式复审、修正、更新文档、提交并合并 main 推送。用户要求实事求是，必须核查真实执行，不能仅声明接线完成。未请求部署。

## 代码与上下文

工作区：`__DEV_HOME__/workspace/ai-native-tools/llm-gateway/llm-gateway-go-3`。

原基线 `e3406f9e29eed627280905fe622747859f3d8819`；实现提交 `3483152cbfd249dd568dcbea13d2faa48caf8838`；分支 `codex/conversation-sanitization-critical-audit`。审计期间远端新增 15 个提交，已合入审计分支，无冲突、无强推；本地原有菜单配置修改未纳入提交。

## 结论/根因

初轮模块测试没有覆盖混合标记、单帧工具 JSON、结构化 data、非字符串凭据、clean-turn、内部 JSON 数字/转义、实际 handler 的 NeverWorse、V2 只写未读和 Redis HSET 省略零值。最后两类问题导致冷读将旧统计/裁剪标记绑定到新字典代际，属于功能遗漏，不是单纯文档问题。

## 已修复关键行为

- 标记只保护自身范围；嵌套 JSON 转义标记预留编号；按类型拒绝不能安全转换的凭据。
- 自定义工具/reasoning 等真实输出 lane 接入强制 gate；工具单帧/多片段在终态完整检查；保留指定字符串二进制字段，检查结构化 data。
- UseNumber/JSON 字符串转义使还原保持内部参数有效和大整数精度；非法工具 JSON 阻断。
- clean-turn 保留 generation/完整本轮字典，本轮 Stats 不使用累计字典；缓存命中通过当前 guard 复验，memo v2 淘汰旧 namespace。
- handler 使用合并后已脱敏 SourceBody 做 NeverWorse 比较与回退；临时 SourceBody 不落库。真实 provider capture 证明摘要、锚点、新消息三条实际派发；旧 guard 负对照只有两条。
- 压缩源快照贯穿 handler provenance、Redis、V2 decode/export 与严格镜像。HSET 显式保存空安全、裁剪和阶段字段；新缓存实例冷读证明旧值不再复活。
- grpc v1.83.1、x/text v0.39.0 及必要传递依赖、vendor 同步；扫描可达漏洞降到零。

## 文档与测试证据

方案：`docs/design/2026-10-01-conversation-sanitization-cache-plan.md`。

最终报告：`docs/audit/2026-10-02-conversation-sanitization-critical-audit.md`。初轮报告已标为历史记录。所有最终命令与真实结果以最终报告和本会话最终响应为准，不能引用初轮“14 包通过”代替集成验收。

集成点 `bd635bdd69a582299fa9d82a66f51c377a210dba`：`env GOFLAGS=-p=2 make test` 全仓退出 0；8 包 race、全仓 go vet、网关 build 均退出 0；govulncheck 可达 0、仍有非可达项；302 文件秘密扫描 BLOCK 0/WARN 1（人工确认合成夹具）。部分外部依赖集成测试会跳过，未当作生产验证。

重点测试：`critical_audit_test.go`、`cached_summary_dispatch_test.go`、冷 Redis reset、V2 source round-trip、threetier 同范围检查及 48h-audit 非法 JSON 阻断契约。全部使用合成输入，没有真实凭据或付费模型请求。

## 验证中发现的环境问题

全仓默认高并行运行的 plugin-runtime 进程启动超时，单独复跑通过；远端同期也修正其等待预算，本任务保留远端修改。最终降低包并行度并在集成后的稳定源码树重跑。一次运行期间新增 net/url 测试 import 造成 importcfg 编译错误，已在冻结源码后重跑，不能把那轮失败记作通过。

macOS Bash 3 不支持密钥扫描脚本的关联数组，第一次未完成报告；使用已有 Bash 5 重跑。合成 URI/无效私钥样本改为运行时构造，不增加 baseline 豁免。最终扫描无 BLOCK，仅合成密码赋值 WARN；不代表全仓没有秘密。

## 决策与遗留风险

继续 fail closed；敏感工具操作不以字符串遮蔽后交给客户端执行。真实压缩回退到完整已脱敏源，不能因字节数更大就丢缓存历史。旧 V1 DB 无 generation 时保守失效。不修改生产资源或凭据。

未覆盖无标签业务秘密、token-ID prompt、OCR/ASR、媒体 URL 和全部未知扩展；Redis 字典仍明文、原始日志另有路径；SSE 终态缓冲影响延迟且有 1 MiB 上限；依赖仍有当前扫描认为不可达的漏洞。未完成真实供应商、生产 Redis/Postgres 或多实例压测。KxMemory 不可用，未伪称检索或写入。

## 收口与下一轮

收口操作记录在最终报告追加；最终本地/远端 HEAD 要用 Git 实测，菜单文件未提交修改保持原状。下一轮优先以合成数据验证真实 Redis/Postgres、多实例并发和 SSE 首字延迟/容量边界，随后独立治理原始日志与 Redis 明文保留期。先复现和写失败用例，再修改，明确未验证项；本 handoff 不构成部署授权。

## 实际收口回执（2026-10-02）

已提交实现及验收文档、no-ff 合并到 main 并推送。成功推送的集成提交 `e5006ff3304a1fa887bc76d0e9eb88f37d5baf7e` 已经通过 ls-remote 与本地 HEAD 一致性核查；之后仅补记本回执，最终 HEAD 由最终响应/自动 handoff 再次实测。保留远端同期 20 个提交，没有强推；第一次 push 被并发更新正常拒绝后 fetch/merge 重试成功。

前 15 个上游提交已包含在全仓 312 包通过/56 包无测试的验证点；后 5 个仅改迁移夹具与文档，补跑相关包、全仓 vet 和脚本语法检查通过。带 integration 标签的 715/b​​g 门仍有上游已知遗留失败，见同期迁移审计报告；本轮未修复这些独立任务。8 包 race、编译、秘密扫描及可达依赖漏洞检查结果仍按最终报告的精确边界解释，不能改写为“所有测试/所有依赖均无问题”。
