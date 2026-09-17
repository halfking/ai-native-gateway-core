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

## 遗留与风险

- **live 网关未开启 survival**：默认部署 flag-off，live 只能观测 legacy 路径。要在真实部署上端到端观测 100/600/30s/5h 行为，需以 `LLM_GATEWAY_REQUEST_SURVIVAL_ENABLED=true` 重新部署（涉及共享环境，本轮未执行）。
- **glm-5.2 当前可路由**：本地目录中 glm-5.2 有健康凭据，"无节点"场景需借助瞬时故障窗口或禁用凭据才能触发；Go E2E 以 stub 固定该场景，不受环境影响。
- **live 上游状态振荡**（健康↔瞬断↔零候选）使 Python 冒烟天然非确定性：脚本对三种状态均给出诚实判定（PASS / 精确诊断 / SKIP），不假报通过。
- **`wait_recovery_window:<kind>` 格式变更**：已全仓 grep 确认无仪表盘/UI/SQL 硬编码旧值；仅测试内适配。若有外部系统按 `reason` 精确匹配 `wait_recovery_window` 需同步（混合 kind 时仍输出旧值，向后兼容）。
- **断言 ≤1 的边界**：取消落在执行器入口之后、循环顶检查之前时，允许 1 次在途调用完成；这是结构上的最小余量，非缺陷。

## 回滚

回滚本轮提交即可恢复上一轮行为（E2E 断言余量 ≤2、脚本旧解析）。核心 survival 实现与默认值本轮零改动。
