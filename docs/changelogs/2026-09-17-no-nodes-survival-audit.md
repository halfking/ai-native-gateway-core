# 2026-09-17 无节点保活重试（request survival）审计与修正

## 背景

产品需求：客户端访问无可用节点的模型时，网关不得立即报错，而是保持连接、由后端重试，每次重试的失败结果以 `: thinking:` 形式回流给客户端；预算为白天 100 次 / 20:00 后 600 次，固定间隔约 30 秒，总时长以 5 小时为限；客户端断开时无论剩余多少次重试立即停止。

上一轮提交 `67f78247c`（feat(streaming): no-nodes survival E2E + per-retry think reason enrichment）交付了实现与测试。本轮按"逐项复核、不信任声明"原则对该交付做审计。

## 结论 / 根因

**核心实现（SurvivalCoordinator 重试保活）成立，无需修改**；四项需求与代码的对应关系经逐行复核确认：

| 需求 | 实现位置 | 钉桩测试 |
|---|---|---|
| 保持连接不报错 | `survival_coordinator.go` Run 循环内同一 `SerializedStreamWriter`；仅成功/终态/断开才退出 | 本轮 E2E（HTTP 200 保持 + 终态帧） |
| 后端重试 100/600 次 | `SurvivalOptions.MaxRetries=100`、`NightMaxRetries=600`、`retriesFor(now)` 按 Asia/Shanghai 20:00 切换 | `TestSurvivalOptionsUseNightRetryBudget`、`TestSurvivalCoordinatorNightBudgetStopsAtExactlySixHundredRetries` |
| 间隔 30 秒 | `RetryBase=RetryInterval=30s`（`withDefaults`）；`recoveryWait` 固定节奏不加抖动 | `TestSurvivalOptionsDefaultToFiveHourThirtySecondRecovery`、`TestSurvivalCoordinatorFixedRetryIntervalIgnoresJitter` |
| 5 小时为限 | `Deadline=5h`；配置默认 `18000s` | 同上 + `config/request_survival_config_test.go`（18000/30/600/20 全钉桩） |
| 断开即停 | 循环顶与可中断 sleep 内检查 `ctx.Err()` → `client_disconnected` | 本轮 E2E（断开后执行器调用增量 ≤1） |
| 每次失败以 think 回流 | `RetryNotice → OnNodeJump → preStream.writeThinking`（`: thinking:` SSE 注释帧） | 本轮 E2E（流上断言 think 内容含底层失败 kind） |

**审计发现的问题（均已在本轮修正）**：

1. **Python mock 客户端关键解析 bug**：`parse_think_chunk` 仅在解析出 `attempt` 或 `reason` 时才记录 think 事件，而 legacy 预流重试消息（"上游请求暂时失败（…），正在重试…（等待 Xs 后第 N 次重试）"）解析不出 `reason`，导致 live 观测中 think 事件被整条丢弃——上一轮 live 验证 A 场景 `thinks=0` 的直接根因。修复：以 `: thinking:` 前缀命中为记录依据，`第 N 次` 用通用正则提取，兼容两种线上消息形态。
2. **失实声明**：脚本 docstring 声称"credentials 被禁用时自动 fallback"（无实现）与 `--skip-budget`（场景 C 未实现）。已删除失实描述，明确场景 (c)(d) 的权威验证在 Go E2E，live 脚本只做冒烟；删除未实现的参数。
3. **Go E2E 测试瑕疵**：`noNodesExecutor.attempts` 死字段；`streamRead` 注释宣称返回"剩余时间"（实际只返回字符串）；断开断言 `≤2` 次余量偏松（循环顶 + sleep 内均检查 ctx，取消后至多 1 个在途调用）。已分别清理/改正/收紧为 `≤1`。
4. **live 环境事实澄清**：本地默认部署 `request_survival_enabled=false`，live 网关跑的是 legacy 预流重试路径（指数 2s→120s 退避），不是 SurvivalCoordinator。零候选时 flag-off 行为是立即回 `"All N candidates failed"` 错误帧——即需求要改变的"改前行为"。脚本已能精确识别该状态并给出"开启 `request_survival_enabled`"的操作提示，而不是笼统报错。

## 改动清单

| 文件 | 类型 | 说明 |
|---|---|---|
| `domains/streaming/survival_no_nodes_e2e_test.go` | fix | 清死字段、正注释、断言收紧 ≤1；3 用例覆盖需求 (a)(b)(c)(d) |
| `tests/mock-system-test/scenario_no_nodes_survival.py` | fix | think 判定/解析重写（双消息形态）、双场景诚实化（D 可 SKIP）、无候选终态识别、清理未用导入 |
| `domains/streaming/survival_metrics_test.go` | —（上轮已改） | 转移标签适配 `wait_recovery_window:<kind>` |
| `domains/streaming/attempt_outcome.go` | —（上轮已改） | `waitReasonCode`：单一等待 kind 时 reason 携带底层 kind |
| `docs/测试/02-gateway-error-policy/cases.md` | doc | 新增 C08 用例行（think 帧内容契约） |
| `CHANGELOG.md` | doc | 登记 67f78247c + 本轮审计修正 |

## 验证结果

- `go test ./domains/streaming/ -run 'TestSurvivalNoNodes' -count=3`：3 次全绿（时序稳定性）。
- `go test ./domains/streaming/... -count=1`：5 包全绿（streaming 71s / executors 23s / webcookie / integrity / state）。
- `go test ./config/ -run 'TestRequestSurvival'`：7 用例全绿（默认值 18000/30/600/20 + 100 钉桩）。
- `go build ./...`、`go vet ./domains/streaming/`：干净。
- Python 解析单测：survival/legacy 两种真实线上报文 + keepalive/data 帧反例，全部通过。
- live 实测（:8782）：候选存在且瞬时故障窗口内 A PASS（HTTP 200 保持 17.8s）+ B PASS（节奏 4s/9s，均值 5.5s vs 期望 6±8s）；零候选窗口内脚本精确输出 survival-OFF 诊断。
- **live 端到端实测（同日补，隔离实例）**：以 `LLM_GATEWAY_REQUEST_SURVIVAL_ENABLED=true` 的隔离 full 模式实例（一次性 citus PG + Redis db8 + :8792，勿动共享 :8782）验证 survival 真实路径，四项需求全部实测通过：

  | 场景 | 实测 | 证据 |
  |---|---|---|
  | 制造真实零候选 | admin API `POST /api/credentials/set-manual-disabled` 逐个禁用 glm-5.2 全部 19 条凭据（生产谓词候选 8→0，`routing_audit_log` 19 条审计） | SQL 复刻候选谓词计数 |
  | (a)(b) 保持连接+think 回流+30s 固定节奏 | HTTP 200 保持 60.1s；think#1 +0.1s、think#2 +30.02s、think#3 +30.02s，**均值 30.0s**（±10s 容差内、无抖动）；`reason=wait_recovery_window:no_available_channel`，`等待 30s` | `scenario_no_nodes_survival.py` 报告 `no-nodes-live-survival-20260917T022527Z.*`（A/B PASS） |
  | (d) 断开即停（服务端） | 客户端 2 次 think 后断开（10:25:25.015），网关**同秒**记录 `survival_task_ended … attempt=2 action=fail_closed reason=client_disconnected`，随后 `request_survival_finished … decision=fail_closed reason=client_disconnected`；场景 A 主动关闭同样产生一条（attempt=3） | 网关日志 WARN ×2 |
  | (c) 预算耗尽终态（等价观测） | 重启实例为 `RETRY_INTERVAL_SECONDS=2` + `MAX_ATTEMPTS=NIGHT_MAX_ATTEMPTS=3`：think #1/#2/#3 各 +2.0s，之后线上终态帧 `{"code":"gateway_survival_fail_closed","reason":"retry_limit_exceeded","retryable":false}` + `[DONE]`；日志 `survival_task_ended … reason=retry_limit_exceeded`（attempt=4=1 初始+3 重试） | 原始 SSE 抓取 + 网关日志 |

  完整 think 消息形态（线上）：`: thinking: "正在等待可用节点并重试（第 N 次，原因=wait_recovery_window:no_available_channel，等待 30s）"`。验证后已通过同一 admin API 全量恢复（`set_manual_disabled_false` 19 条，`manual_disabled=true` 计数归零）。

## 遗留与风险

- **~~live 网关未开启 survival~~（已解决）**：本轮以隔离实例（一次性 citus PG :15432 + Redis db8 + :8792，共享 :8782 零接触）实测 survival 全路径，见上节"live 端到端实测"。共享部署仍保持默认 flag-off，未做任何变更。
- **~~glm-5.2 当前可路由，无节点场景难触发~~（已解决）**：本轮用 admin API 批量禁用/恢复凭据确定性制造与解除零候选，不再依赖瞬时故障窗口。
- **5h deadline 与 100/600 全量预算不可 live 短观测**：本轮以短间隔+小预算配置等价观测了预算终态（retry_limit_exceeded）；deadline_exceeded 分支与 5h 时长仍由 `config/request_survival_config_test.go` 钉桩 + E2E 覆盖，live 不复测。
- **隔离环境搭建中发现的工具债**：`scripts/init-local-db.sh` 的失败过滤会把 NOTICE 行（"exists, skipping"）误判为幂等噪音，吞掉同文件内的真实 ERROR（本轮 526 契约校验失败被漏报）；且 SQL 快照与迁移链对 session-turns 家族的形状不一致（526 在快照上必失败），本轮以源库 `pg_dump --schema-only` 的 DDL 补齐后由 ensure 链自愈。建议挂账：init 脚本失败检测收紧 + 快照 session-family 重建。
- **live 上游状态振荡**（健康↔瞬断↔零候选）使 Python 冒烟天然非确定性：本轮通过确定性禁用凭据消除该变量；脚本对三种状态均给出诚实判定（PASS / 精确诊断 / SKIP），不假报通过。
- **`wait_recovery_window:<kind>` 格式变更**：已全仓 grep 确认无仪表盘/UI/SQL 硬编码旧值；仅测试内适配。若有外部系统按 `reason` 精确匹配 `wait_recovery_window` 需同步（混合 kind 时仍输出旧值，向后兼容）。本轮 live 观测到该新格式在线上按预期输出。
- **断言 ≤1 的边界**：取消落在执行器入口之后、循环顶检查之前时，允许 1 次在途调用完成；这是结构上的最小余量，非缺陷。本轮 live 断开当秒即止（`reason=client_disconnected` 与断开同秒落日志），实证余量设置合理。

## 回滚

回滚本轮提交即可恢复上一轮行为（E2E 断言余量 ≤2、脚本旧解析）。核心 survival 实现与默认值本轮零改动。
