# 存储优化 v2 —— S4 前置（GAP-2 重放器）— 后续会话提示词

## 上下文

前序会话（2026-09-15）完成：S1b 遗留闭环（旁路证伪，过渡窗产物；sys final_full 构造性预期）、S2→S4 观察台账与每日轮次脚本落地（`docs/03-design/04-data-design/storage-observation-ledger.md`、`scripts/audit/storage_observation_round.sh`，均已推 main）、S3 波1 admin/logs.go 原生 turns 读样板（分支 `feat/s3-wave1-admin-logs-native-turns`，灰度开关 `storage.admin_logs_native_turns_read` 默认关）。观察期 Round 1b 全局扫描实测：**sessionv2mirror shadow write 24h 终态行丢失 258/6956=3.71%**（best-effort 写 + in-process backlog 重启即丢 + 无重放器，spec §12 GAP 2），credits 随行丢失使 D7 计费等值不可达，**S4 停写 gate 冻结**。详见 `docs/handoff/20260915-storage-s1b-close-observation-s3-sample/HANDOFF.md`。

环境：本机 llm-gateway-pg（psql 用 127.0.0.1）、网关容器 8782；01:15 起运行二进制为外部部署的 2118（sha 不在本仓库），对账前先核对 /healthz 构建身份。`session_turns.cost_usd` 为 numeric(14,6)（v1 为 14,8），成本精度漂移修复候选迁移 711（本机双账本已核 711 空闲，252 侧届时重查）。

## 任务：落地 GAP-2 重放器，解除 S4 前置阻塞

1. **方案选型**（先给对比再动手）：a) backlog 持久化到 DB 表 + 后台重放器；b) turn 写入改 outbox 模式（复用 `session_aggregate_outbox` + reaper 的既有模式，telemetry worker 只 enqueue）。注意 hook 跑在 telemetry worker goroutine 上（`internal/sessionv2mirror/hook.go:114-152`），2000ms 预算与 8 槽信号量语义要保留给前台路径。
2. **回补现缺口**：用 v1 终态行反查回填 258+ 历史缺失 turns（参考 dual_read_validator 的 v1↔v2 列映射与 `cmd/tools/backfill_sessions_v2*` 工具链），回补后 `GLOBAL_G2` 归零。
3. **顺带 711**：`session_turns.cost_usd/cost_display` numeric(14,6)→(14,8) + 双写期窗口回填（编号 711 已核本机空闲；迁移编号 711 起查双账本；行为测试覆盖）。
4. **观察期**：重放器上线后每日 `bash scripts/audit/storage_observation_round.sh`，结果追加台账；**从 GLOBAL_G2 连续归零起重新计 7 天**，达标后按 plan §4 启动 S4 停写 gate 分支化。
5. **S3 波1 收尾**（可并行）：分支 rebase 最新 main、开启灰度开关实测 /api/logs 列表+详情响应与视图形态一致性（A/B 对比）、按 §4-S3 波1 清单扩展（getLog 正文链路改读 turns/final_full 仍是波1后续项）。

**约束**：生产 252 应用 706-710 前复核 default 分区分布；admin API 登录用 POST /api/auth/token（username/password），密码取部署根 bin/current/env；查询一律带 hot∪parent 双侧（promote 8h 热窗）。
