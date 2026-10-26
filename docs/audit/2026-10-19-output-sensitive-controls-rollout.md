# 输出敏感检查与 Minimax-m3 部署验收（2026-10-19）

## 结果与使用入口

- 发布代码：`250ea7f80dc51c3788e08db58c8a359e4a6809ef`。双轴审查绑定该 SHA；四个 sed 修复文件与已审 `0cf788c1e` 的 blob 完全相同。
- 245：`2740-250ea7f8`，active `8782`，PID `3090700`，运行二进制 `/opt/llm-gateway-go/releases/2740-250ea7f8/gateway`。
- 154：`2741-250ea7f8`，active `8781`，PID `17787`，运行二进制 `/opt/llm-gateway-go/releases/2741-250ea7f8/llm-gateway-go`。
- 双机 `/healthz`、`/readyz` 均 200；nginx 实际路由版本、数据库、管理员登录和 17 个供应商凭据解密均通过。首次发布构建了两套前端；sed 补丁发布沿用同一已验证前端产物。
- 系统模块 → 输出合规检测 → 配置 → **输出敏感检查总开关**，参数 `sanitize.output_check_enabled`，默认与最终值均为 `true`。
- 设置 `false` 会跳过输出敏感检测、工具外传守卫与写出脱敏；输入脱敏和还原继续。无需重启。进行中的流按首帧配置固定，新请求使用更新值。

## 根因与修复

原始运维请求在记录型工具回归中，把 Bash 命令嵌入 JSON 字符串。旧 sed 识别只覆盖普通命令；转义双引号使输入检测将 `PASSWORD=.*/PASSWORD=[REDACTED]` 的一部分替换成敏感值占位符，随后产生间歇输出误封。

现场请求 `012464b6eecca6500b33dd2d6ef0228d` 在 154 被 `secret.password.assignment` 拦截；实际出站消息已证明 sed 在输入阶段被改变。新实现仅对合法 UTF-8 的 JSON 字符串解码一层，复用相同 sed 语法判定，将已证明的遮蔽范围映回原始字节；不添加通用占位符前缀豁免。

判据直接调用 `isPlaceholderSecretValue` 和真实 `configs/sensitive_patterns.yaml` 检测器，覆盖托管/非托管、三种 scope、单双引号、普通/JSON 表示、拼接真实凭据、Unicode/UTF-16 surrogate 字节映射与非法 UTF-8。审查发现的非法 UTF-8 偏移漏洞在上线前关闭。

相关提交：`a10cd4584`（低层判据）、`3c203c082`（总开关）、`64fd3d9c0`（首次修改回滚）、`0cf788c1e`（JSON sed 修复）。

## 配置、复核、发布与回滚实测

- 首次将默认 `true` 改为 `false` 后可回滚；隔离 PostgreSQL 17 同时覆盖默认 true、环境 false、回滚/重做/后续写。原 StoreDB 变异在首次 previous-value 断言处失败。
- 双机开启时，精确作用于测试 Bash.command 的临时 secret 规则均返回 403；关闭后均 200 且工具参数原样；参数回滚到 true 后均重新返回 403。
- 第二轮规则发布 `11 → 12`，双机自动收敛实测 1.643 秒；回滚创建版本 `13`（parent=12，rollback_of=11），收敛 1.399 秒。
- 最终配置摘要 `f625b796cbb4a80ba74c7632e98a0a967af21d2491406ac78a7ce9d0c27c602e` 与基线相同，临时规则已移除；历史版本保留。
- 读取管理端当前配置不会主动 Refresh；两次收敛来自后台刷新。两台 PID 在发布、开关和回滚测试前后均未改变。
- 特殊组合触发待复核事件 `790729`，人工复核 API 标记 `false_positive`；复核不隐式发布规则。真实误封事件 `790599` 已附修复与复测说明并标记误报。
- browser-use 真实浏览器完成关闭/开启保存和双机读回；最终配置页显示该参数来源为数据库、状态“启用”。
- 共享数据库现场确认迁移 `866/867/868/869/877/878` 已登记；866 已应用，无重复破坏性重放。部署通道无 pending，checksum ledger 校验通过。

## 最终 Minimax-m3 复测

保留原始运维意图与完整命令，凭据使用测试值；Bash 仅记录参数，不执行命令。修正验收说明中“不执行工具”与“请调用工具”的矛盾。每次同时校验持久化出站消息中的 sed 完整保留。

| 网关 | 模式 | 完整原样工具命令的 Request ID | 结果 |
|---|---|---|---|
| 245 | 非流式 | `23643c0fa546da18bc3dc6c0db93248f` | 200，tool_calls，命令逐字相同 |
| 245 | 流式 | `1f754764d00c81970b54fc7c149b0685` | 200，tool_calls，命令逐字相同 |
| 154 | 非流式 | `d3bd8ed98ee3e99ca6f704953d93fde9` | 200，tool_calls，命令逐字相同 |
| 154 | 流式 | `bcd9f4b753cc099a4f546023cd51ec25` | 200，tool_calls，命令逐字相同 |

最终 6 次请求均无 `output_policy_blocked`，其中 4 次取得完整原样工具命令，另 2 次为模型响应差异，未计入完整命令通过证据。这是回归样本结果，不代表所有未来模型输出均不会被策略阻断。

## 验证与证据

- `go build ./...`、独立 installer 模块构建、相关 `go vet` 通过。
- `go test ./internal/credentialtext ./security/sanitize ./domains/hooks/outputcompliance ./cmd/gateway ./settings ./domains/session/v2` 全包通过；`domains/streaming` 完整测试通过（152 秒）。
- 迁移 866–869/877/878 相关测试及 `TestMigrationRegistryStaysContinuous`、`TestRegistryShaIsActuallyTheFile` 通过。
- 8 项代码变异均在预期断言处变红：占位符恒真/恒假、YAML检测视图断开、JSON处理断开、offset+1、hook/interceptor/write-time 三处总开关失效。加上真实 PG 首次保存回滚变异，共 9 项。
- 完整 admin 包的 5 个既有 reader/control-plane 库存判据失败已在改动前 pristine 主干独立复现；相关 Module/Setting/Sanitize 判据通过，不将完整 admin 包报告成全绿。
- 原始请求、工具响应、运行日志、变异输出、浏览器截图与备份 manifest 保存在本机权限受限目录 `/tmp/output-controls-20261019/`；仓库不收录原始凭据和请求正文。
- 自建隔离测试库 `gw_output_controls_20261019_test` 与测试二进制在验收后清理；线上业务数据、备份和发布历史保留。

## 备份与一行回滚

备份均检查 tar/pg_dump 目录内容及 SHA-256；目录 0700、归档 0600。原始部署前备份与 sed 补丁前备份均保留。

| 位置 | sed 补丁前备份 | SHA-256 |
|---|---|---|
| 245 | `/opt/llm-gateway-go/backups/output-controls-20261019T092732Z/release-config.tar.gz` | `7d7aca86d8bf36a82b6bf6d14a6059419babde2d95b723b1d3808a4bdfbaba07` |
| 154 | `/opt/llm-gateway-go/backups/output-controls-20261019T092732Z/release-config.tar.gz` | `33dba7e1d5583398776a8e3ab6ab6379b914db04e7cab69b35f63322424ce18c` |

252 数据库备份：`/data/backups/llm-gateway-sensitive-rules-20261019T083104Z/`，含 schema 与策略/参数/迁移台账选择性备份，SHA256SUMS 及 TOC 已校验。原始网关备份分别为 245 `...T082928Z`、154 `...T082932Z`。

在仓库根执行：

```sh
/opt/homebrew/bin/bash scripts/deploy-seamless.sh rollback 245
/opt/homebrew/bin/bash scripts/deploy-seamless.sh rollback 154
```

规则使用敏感规则管理“回滚版本”；参数使用参数回滚。866 为增量表，二进制回滚保留这些表，避免丢失复核与规则历史；不向业务库整库覆盖恢复旧备份。
