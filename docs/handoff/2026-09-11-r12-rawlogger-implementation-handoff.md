# Handoff — 2026-09-11 — R12 RawSink 实施批收尾（阶段 1-3 落地 + 审计闭环）

## Origin

- 预研：`docs/audit/2026-09-11-r12-rawlogger-prestudy.md`（R12 预研 §9 分阶段实施计划，用户授权后同日实施）。
- 本会话完成阶段 1-3 全部代码工作 + 独立审计 7 项发现闭环。阶段 4（生产灰度）、阶段 5（下线 legacy）留待后续会话，提示词见文末（已放会话内便于拷贝）。

## 本会话产物（已推送 origin/main）

| commit | 内容 |
|---|---|
| `b7e0f933e` | feat(logging): RawSink 接口 + BufferedRawSink + LLM_GATEWAY_RAW_LOG_SINK 灰度开关（阶段 1-3，11 文件 +2573） |
| `b1394d9d9` | fix(logging): 独立审计 7 项发现修复（flushMu 串行化 / stub 字段奇偶 / 有界 Sync 排空 / Close 帧索引 / 死代码 / 测试容错） |

文档记录：预研 §12（实施记录 + 5 处设计偏差）、§13（审计修正记录）。审计由独立子代理完成（并发正确性/契约奇偶性/错误处理/接线/测试质量五维），结论"可合并"，major 项（frameIndex 并发错位，-race 不可检出的逻辑竞态）已修复。

## 当前状态

- `LLM_GATEWAY_RAW_LOG_SINK` 默认 legacy（=现网 AsyncRawDataLogger），**生产行为零变化**；`buffered` 档已实现并通过全部测试（LookupFrame / close_drained 硬前置齐备）。
- 测试：本机 `./internal/logging/...` 与 `./domains/streaming/...` 通过（仅存 6 个预存 Windows 环境失败，见 R13 批）；linux/arm64 交叉编译（含 -race 测试二进制）通过。
- `-race` 运行验证：本机 windows/arm64 不支持，**须确认 Linux CI 实跑**（阶段 4 第一步）。

## 剩余任务

1. **阶段 4 生产灰度**（预研 §9.4）：测试环境 → 低流量实例 → 全量；观测 rawaudit_write_failed_total / flush 耗时 / batch 大小 / dropped / close drain 耗时 / frame correlation 成功率；完成一次回滚演练。→ Prompt A
2. **阶段 5 下线 legacy**（预研 §9.5）：前置 = Buffered 稳定一个完整发布周期 + 能力对齐 + 回滚演练通过。→ Prompt C
3. **R13 卫生批**（预存缺陷，非 R12 引入，审计中确认）：
   - `rotate()` 文件名基于时钟，粗时钟同 tick 两次 rotate 会 O_EXCL 撞名；失败后 `l.file` 残留已关闭句柄（僵尸态）。
   - `internal/logging` 6 个 Windows 环境测试失败：TempDir 句柄清理 ×5（TestInitCreatesDirAndWrites/TestRotationProducesBackup/TestReconfigure/TestListFiles/TestAsyncRawDataLogger_OverflowEmitsAnomaly）+ 路径反斜杠转义断言 ×1（TestLockFreeAnomalyReporter_StampsRawLogLocation）。
   - 预研 §10 问题 5：写失败"批次剩余条目静默丢弃"是否改逐条跳过（需用户拍板）。→ Prompt B
4. 分支卫生（本会话已做）：本地删除已合并分支 `docs/readme-multilang-enrich`、`fix/gateway-error-policy-logging`；远程无已合并分支（`agent/471-safe-backfill` 未合并，保留）；`backup/*`、`archive/*` 为有意保留的备份分支。

## 下一步会话提示词（可拷贝，与正文同款已放会话内）

见下三个 Prompt 块（A=阶段 4 灰度，B=R13 卫生批，C=阶段 5 下线 legacy）。A 与 B 可并行开会话；C 依赖 A 完成。

## 下会话必读（环境陷阱）

- 本机 windows/arm64 无原生 Go/-race/Docker：构建与测试配方见 memory `llm-gateway-windows-build-recipe`（golang-dist 便携工具链 + zig cc 交叉编译；-race 仅可交叉编译验证）。
- SMB 网络盘 git 陷阱见 memory `llm-gateway-git-workflow`：路径限定命令；push 被拒 → `git fetch origin` + `git -c rebase.autoStash=true rebase origin/main` + push（autostash 对锁定文件报 Permission denied 不丢数据）。
- 工作区常年有他人未提交脏文件（VERSION/deploy-lib/.lnk 等）：提交用显式路径 `git add -- <path>`，**严禁 `git add -A`**。
- 文档主机路径映射：预研等审计文档的 `/Users/xutaohuang/...` 主机路径对应 `Z:\workspace\ai-native-tools\llm-gateway\llm-gateway-go\...`；实施在 `Z:\workspace\ai-native-tools\syncfield\llm-gateway-go-4`（与 origin/main 同步的工作克隆）。
