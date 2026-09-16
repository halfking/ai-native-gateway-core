# Handoff: 存储优化 v2 —— S1b 闭环、观察期起算与 S4 前置阻塞确立

**会话**: 2026-09-15（本机，llm-gateway-go-4 workspace）
**日期**: 2026-09-15
**状态**: ✅ S1b 闭环（无代码缺口）+ ②观察期 Round 1/1b 完成 + ③S3 波1 样板分支就绪；⚠️ 新立 S4 前置阻塞项（shadow-write 丢失 3.71%/24h）
**运行环境**: 本机容器 llm-gateway-local-8782（观察期对账时 2.5.4.2113=6622249e；01:15 起被外部并行线重指 2.5.4.2118=c6605196，该 sha 不在本仓库历史）

---

## 结论 / 根因

1. **S1b"outbound_body 旁路"证伪，无代码缺口**（plan §4.2-10 已改写闭环）：13 行带 outbound_body 的 turn_delta 全部落在 2026-09-14 16:30:09–16:32:15，而 settings_audit 钉死双开关 PUT 时刻为 **16:32:24.58/24.60**，首条 final_full 行 16:32:28.45——即"新二进制 16:30:11 cutover 先行、开关 PUT 滞后 2 分钟"的过渡窗旧路径产物，属设计语义。旁路嫌疑逐一排除（backfill 工具不赋 OutboundBody、repair 不触碰、promote 只搬行、DualWriter 未接线且汇入同一门控 writer）。**PUT 之后父表+hot 违规均 0，§6 的 1.8GB/月收益自 09-14 16:32:24 起可计入**。
2. **sys final_full 覆盖 0% 属构造性预期**：探针/回环链路不经会话压缩器，telemetry 条目本就无 OutboundBody（源头验证：54 个无覆盖回环会话的 request_logs_bodies_hot.outbound_body 100% NULL），writer `len(OutboundBody)>0` 守卫正确跳过；单轮会话无差集基线需求。sys 会话 turn_delta 全历史 outbound 命中=0。
3. **观察期 Round 1 PASS、Round 1b FAIL → S4 前置阻塞项确立**：会话抽样 10/10 PASS（字段漂移 0），但全局 G2 扫描发现 **24h 内 258/6956（3.71%）终态 v1 行缺 turns**。根因：sessionv2mirror shadow write 为 best-effort（hook.go:129-151 超时/槽满入 in-process backlog；backlog.go:16-25 不落盘、重启即丢、无重放器=spec §12 GAP 2）；14-15h 激增（75+31 条）对应首次 cutover 失败窗口。credits 随行丢失 → D7 计费等值不可能达标 → **S4 停写冻结，GAP-2 重放器（backlog 持久化 + 后台重放，或 turn 写入 outbox 化）为新立前置工作项**。
4. **E5：cost 精度漂移**（首个真实 G1 样本）：`session_turns.cost_usd` 为 numeric(14,6)，写入时对 v1 的 numeric(14,8) 舍入（0.00001870→0.000019）。修复候选 **711**（本机双账本已核 711 空闲）：turns cost 列 14,6→14,8 + 双写期回填；修复前 G1-cost 按 ≤1e-6 绝对容差判等。
5. **重部署实战教训**：dual-read 端点在旧二进制（2110=16:28 构建）上报占位零值伪影漂移（NULL↔0 归一化 16:48 才提交）——**对账/验收前必须核对运行二进制 build_seq ≥ 相关修复 commit**；观察台账每轮须记录 build 身份（/healthz 的 git_sha/build_seq，勿信镜像 tag）。

## 改动文件与关键行为

**main（已推送，821ef3e0e）**：
| 文件 | 内容 |
|---|---|
| docs/03-design/04-data-design/storage-observation-ledger.md | 新建：gate 定义（G1/G2/G3 硬指标 + E1-E5 例外类 + 全局 GLOBAL_G2）、Round 1（抽样 PASS）、Round 1b（全局 FAIL，S4 前置阻塞确立） |
| scripts/audit/storage_observation_round.sh | 新建：每日一轮（分层抽样→dual-read 端点→G2/G3 SQL 分类→全局 G2 扫描→PASS/FAIL 行）。用法 `LLM_GATEWAY_DSN=... GW_ADMIN_PASSWORD=... bash scripts/audit/storage_observation_round.sh` |
| docs/03-design/04-data-design/storage-optimization-plan.md | §4.2-10 S1b 闭环改写；§4-S4 行补前置阻塞与数据 |

**分支 feat/s3-wave1-admin-logs-native-turns（已推送，未合并）**：
| 提交/文件 | 内容 |
|---|---|
| 6114b9bed | 部署产物同步（2113=6622249e） |
| c7ece5fe1→被后续含入 | 文档同 main |
| b1aa45338 | S3 波1 样板：`db.SessionFamilyTurnsSourceSQL()`（导出 710 session 分支 113 列投影为可复用 FROM 源，单一契约源）+ `admin/logs_turns_source.go`（logsSourceFromSQL 切换）+ listLogs/getLog FROM 换源（COUNT/聚合/分组/分页共用同行集）+ settings `storage.admin_logs_native_turns_read`（默认关、热加载）+ 形态守卫单测两则 |

## 测试

- 单测：`go test ./admin/ -run TestLogsSourceFromSQL` 2/2 PASS；`go build ./...` 通过。
- live A/B（分支样板）：近 3h 窗口 113 列全形状 EXCEPT 双向——native_only=0；view_only=29 全为已知类（28 非终态 + 1 shadow-write 丢失行）。
- 停写复测：开关 PUT 后父表+hot turn_delta 带 outbound_body 违规 = 0（至 09-15 00:49+08）。
- 观察轮次脚本：`ROUND_RESULT|sessions=10|fail=0|global_g2=258|verdict=FAIL`（global_g2>0 为预期现状，正是新立前置项的度量）。

## 风险 / 未决

1. **S4 前置（最高优先）**：GAP-2 重放器未落地前，7 天零漂移天数**不开始累计**（Round 1 记录仅作基线）；丢行业务轮的 credits 在 v1 有账、turns 缺失，若上游已有按 turns 的计费对账会差 3.71%。
2. **E5 711 迁移**：turns cost 精度 14,6→14,8 需迁移+回填，711 本机空闲、252 侧届时重查双账本。
3. **生产 252 应用 706-710 前**：仍须复核 default 分区分布（plan §4 P0 行既有约束，本轮未动）。
4. **环境并行线**：01:15 有外部部署 2118（sha 不在本仓库），Round 2+ 起的观察轮须先核对 /healthz 构建身份与 main 的对应关系；分支样板合入前需 rebase 最新 main。
5. mirror hook 内部回环排除类（isInternalAutoEntry）与 gap 行（request_type='main'）已区分；gap 行 100% 为会话级整体缺席（258/258 单轮会话）。

## 观察期速查

```bash
export LLM_GATEWAY_DSN='postgresql://llm_gateway:***@127.0.0.1:5432/llm_gateway'
export GW_ADMIN_PASSWORD='<bin/current/env 的 LLM_GATEWAY_ADMIN_PASSWORD>'
bash scripts/audit/storage_observation_round.sh
# 输出追加到 docs/03-design/04-data-design/storage-observation-ledger.md，
# 并先记录 /healthz 的 build 身份
```
