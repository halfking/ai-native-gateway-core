# 2026-09-23 晚 154 流式修复轮 · 批判式复审（R 轮）

- 复审对象：当日晚间 154 部署（2234-758bf92c → 2235-b0c77269）+ 双终态帧修复（b0c77269d）+ L2 shadow 开启 + 保留期/工具通道验证。
- 方法：对本轮每一项「已验证/已达成」声明找生产或测试实证；找不到实证的补测试或降级表述；发现自引入回归立即修复。
- 关联：`docs/audit/2026-09-23-stream-error-forensics.md`（本轮修复的源头取证）、`docs/design/resume-blocked-long-stream-recovery.md`（L2 设计，本轮有勘误）。

## 一、发现与处置（按严重度）

### R1（P0，自引入回归）b0c77269d 的 unwrap 循环在 not-found 分支丢失装饰层

b0c77269d 给 `wrapAttemptWriter` 加了沿 `Unwrap()` 解包找 GateWriter 的循环，但循环把 `w` 重赋值到链上内层；链上没有 GateWriter 时（=非 survival 聊天流，本地/245 的主路径）`w` 停在裸 writer，新建 gate 包在裸 writer 上——`StreamChat…WithVendor` 在 ：649 套的 `monitoredResponseWriter`（连接监控）被静默旁路。154 因 survival 全量覆盖走了 found 分支，故当晚生产无直接症状；245/本地一旦带上 b0c77269d 部署即命中。

**修复**：循环 break 后 `w = orig` 恢复完整装饰链再建 gate；循环加 8 跳上限防病态 Unwrap 环。
**测试**：`TestWrapAttemptWriterKeepsDecorationWhenNoGateWriter`（断言新 gate 的 delegate 是最外层装饰 writer）。敏感性实证：临时删除 restore 行 → 该测试 FAIL；恢复 → PASS。未敏感性验证的测试等于没写，本轮起每个回归测试都做「回退代码必须变红」验证。

### R2（P0，上一轮只声明未实证）「单终态帧」缺 wire 级闭环

2235 上线后 24 次触发请求全部干净完成（零自然断流），双终态帧修复在**真实断流**下的 wire 证据当晚不存在——上一轮报告用「24 连净」与「自动化兜底」表述，属于声明而非实证；且「24 连净」同时混杂了 d8a7849fd（良性 EOF 对齐）的效应，不能单独归因于 b0c77269d。

**修复**：新增 field-topology 集成测试 `TestStreamChatSurvivalGateReuse_SingleTerminalOnCommittedBreak`——用 **真实的 `StreamChatWithPendingCapture`** + coordinator 式 GateWriter + 截断上游（既有 R58 e2e 的 mock executor 直写 GateWriter、从不进入真实流函数，恰好绕开了 bug 所在 seam，这是它当初没拦住的结构性原因）。断言链：①§11.6 闩必须落在 coordinator 的 gate 上（修复前为 false）；②`renderTerminal` 抑制后 wire 上恰好 1 个错误帧 + 1 个 [DONE]；③`retryable:true`；④[DONE] 后无帧。敏感性实证：回退到修复前代码 → FAIL。
**遗留**：生产自然 committed 断流的首个 wire 样本仍待 9/24 自动化以 `survival_terminal_already_rendered` 哨兵确认（机制已由本测试闭环）。

### R3（P1，上一轮结论失真）「24h 下降基线」建立在坏查询上

上一轮报告的基线（transient 110 + provider_error 84/24h）来自 `request_logs_hot` 上按 `t0_arrived_at` 过滤的查询——事后核实该表 **99.5% 的行 t0_arrived_at 为 NULL**，该查询只扫到 306 行，基数严重失真；且修复前 resume_blocked 在 DB 无独立打标，无法从 DB 重建修复前逐小时事件率。月分区 bodies 无可用 ts 索引（有界查询 40s 超时），跨天补偿查询也不可行。

**处置**：放弃数值阈值判定。9/24 自动化判定口径改为机制三件套：①洪水线=0；②每个 resume_blocked 事件满足「failure_detail_code 1:1 落库 + retryable:true + 单终态帧守卫哨兵出现」；③事件量趋势与同夜 A/B（2234 触发期 20/24 断流 vs 2235 期 24/24 干净，**声明混杂**：上游波动与 d8a7849fd 均可能贡献）方向一致。如实写明「修复前基数不可重建」。

### R4（P1，自动化计数陷阱）日志文件跨 build 追加导致时代混染

`gateway-canary-8781.log` 同时含 2211 洪水尾（19:44-19:48）与 2235 内容；8782 文件混 2211 洪水 + 2234。上一版自动化脚本用无时间过滤的 `grep -c` 统计事件，会把旧时代计数混入。journald 保留期 ~36h，9/24 复核时 9/23 晚日志已被裁掉一部分。

**处置**：自动化 v2 强制时间前缀过滤（文件）+ `journalctl --since` 交叉核对（声明保留期截断），并写明端口→build 的时代映射。

### R5（P2，表述校准）「保留期 ~7.5 天 ≥48h 达标」是深夜 1 分钟采样的外推

5.5MB/h 为 20:5x 低负载 60s 采样；白天负载可能 3-5 倍。1GB 字节预算下保留期≥48h 的真实判定线是**日均 ≤20MB/h**。9/24 自动化按此线重测；在此之前不把「达标」说死。

### R6（P2，设计勘误）L2 shadow 对目标人群打分能力受限（见设计文档勘误）

shadow 只观察不调度重放（`enforceEnabled()` 才走 `l2AlignedReplayPlan`），而 committed 断流人群在 attempt 1 即终态、无第二次 attempt，shadow 结构性采不到对齐分。P0 的原始数据源（245 48h journal 12 样本 + session_bodies response_delta）已被洪水与 stub 化写路径摧毁（response_delta 全为 4-93B 占位；月分区无索引不可扫）。已在设计文档加勘误；9/25 自动化按修正口径判定，产出「补 shadow-replay 变体 / 限时 enforce canary」二选一建议。
（shadow 开启本身的安全性与语义已由 `TestUTSRL2ModeParsingAndLegacyCompatibility` 钉死：MODE 权威、legacy true→enforce 的危险映射、invalid→off。）

## 二、当晚验证状态矩阵（如实版）

| 项 | 状态 | 实证 |
|---|---|---|
| 部署 2234/2235 | ✅ | deploy-seamless 全绿、凭据解密冒烟 12/0、公网 healthz=2235 |
| /healthz 版本 | ✅ | 本机+公网双端点一致 |
| retryable:true | ✅ | 20/20 断流捕获文件逐字节核对 |
| failure_detail_code 落库 | ✅ | 20 wire 事件 : 20 DB 行 1:1 |
| 单终态帧 | 机制✅ / 生产 wire 待首个自然事件 | 集成测试（敏感性验证过）+ 9/24 自动化哨兵 |
| survival_resume_blocked 24h 下降 | ⏳ 机制口径待 9/24 | 基数不可重建已如实声明；洪水线已归零（实测） |
| 154 保留期 ≥48h | ⏳ 白天重测 | 深夜 5.5MB/h 采样；判定线=日均 ≤20MB/h |
| 245 保留期 | ✗ 未修复 | 2215 不含洪水修复，24MB/10min 轮转；正解=部署 ≥b0c77269d，上调 backups 无效 |
| minimax 工具通道厂商修复 | 当日三路径全净（受控直连） | 非流式/流式/anthropic 原生 tool_calls，0 泄漏；启发式挂起，哨兵=minimax_tool_text_coerced |
| L2 shadow | 已开启（env+进程 environ 确认） | 打分能力结构性受限见 R6；9/25 自动化评估 |

## 三、改动清单（本复审轮）

- `domains/streaming/attempt_gate_wiring.go`：not-found 分支恢复装饰链（R1）+ 解包 8 跳上限。
- `domains/streaming/gate_writer_test.go`：+`TestWrapAttemptWriterKeepsDecorationWhenNoGateWriter`。
- `domains/streaming/stream_eof_test.go`：+`TestStreamChatSurvivalGateReuse_SingleTerminalOnCommittedBreak`（R2 wire 闭环）。
- `docs/design/resume-blocked-long-stream-recovery.md`：P2 勘误注记（R6）。
- 自动化 `automation-1c111234`（cron `52 20 24,25 9 *`）：v2 机制口径 + 时间过滤 + 保留期判定线（R3/R4/R5）。

## 四、遗留风险

1. 生产自然 committed 断流的 wire 单帧样本未采集（机制已闭环测试化）；若 9/24 哨兵未出现说明该人群当晚未复现，需延长观察而非默认通过。
2. b0c77269d 已在 154 生产（其 R1 回归在 154 被 survival 全量覆盖掩盖）；**245/本地带上 b0c77269d 部署前必须包含本复审修复**，否则非 survival 聊天流监控被旁路。
3. d8a7849fd 与 b0c77269d 对当晚观测人群的效应未解耦（同夜 A/B 有混杂）；归因需更长窗口数据。
4. `request_logs_hot.t0_arrived_at` 大面积为 NULL 属既有数据质量债，任何按时间的 DB 统计都必须走 bodies ts join——已写进自动化，但对其他报表同样适用。
5. sessionv2mirror 失败噪声（~115/min、99.6% 为 probe synthetic、advisory lock 超时）未处理，建议 mirror 跳过 synthetic 会话。
