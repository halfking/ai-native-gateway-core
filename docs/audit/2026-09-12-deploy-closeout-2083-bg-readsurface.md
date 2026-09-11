# 2083 部署收尾 — bg 读面修复 + R13/R12 批次上线（245 + 154）

日期：2026-09-12 凌晨 · 部署基线：`00cbdb595`（main）· 构建：`2.5.4-00cbdb59-20260911-2083`（seq 2083）· 通道：deploy-seamless 蓝绿（两节点切换后 active_port 均轮换至 8782）

## 部署范围

本次 2083 相对上一运行版本 `2081-014b36cd` 新增包含：

- `d812e1a53` bg 近期窗口 request_logs 读面切当月视图（candidate_failure_monitor 5min staleness + auto-cool、shared_pick 7d 选型、daily_probe_audit 3d 回看）+ 守卫 `TestRecentWindowReadsUseCurrentMonthSurface`
- `ef77764e9` migration 693 部署缺口闭合（revision sequence 接线 + 启动 ensure 自愈镜像）
- R13 drawer 修复系列（`89c43559f`/`1d0f3c831`/`4fb8f55cc`/`897067f3e`/`cf7256f66`：admin offer 抽屉可编辑、凭据级模型端点路由、modality 兼容探针）
- R12 raw-sink 加固与观测（`5e98bf625`/`26733e7d2`/`3501d7f7b` + Prometheus/Grafana 工件与回滚脚本）

> 注：并行会话的 `2082-cf7256f6` 仅本机 deploy-local 容器部署（8782，浏览器复验），**未上 245/154**；且不含 d812e1a53。本批次 2083 为两者合并后的首次生产上线。

## 部署过程

- 245 首次尝试在候选预热阶段安全失败：启动期 schema ensure 链遇瞬时语句超时（session_summaries archival ensure，SQLSTATE 57014，重试第 2/2 轮成功），两轮 ensure 总耗时超过 60s healthz 探测窗，8782 候选未起监听——蓝绿护栏正常，旧实例保持服务。252 PG 宿主磁盘 53%（91G 空闲）排除磁盘因素，定性为瞬时负载。ensure 幂等已落库，**重试一次即成功**（80s，切换 17s）。
- 154 一次成功（82s，切换 3s）；解密冒烟 providers=18,1,14 creds=12 failed=0；admin 密码同步通过。

## 验证记录（两节点 8782 active）

| 项 | 245 | 154 |
|---|---|---|
| healthz/readyz | ✅ 2083-00cbdb59 ready | ✅ 2083-00cbdb59 ready |
| journald 23514（自切换） | 0 | 0 |
| SQL 42703/does not exist | — | 0（d812e1a53 当月视图读面工作正常） |
| bg 接线 | credRecovery + balance_quota_probe（120s）✅ | 同左（URSM authoritative 分支）✅ |
| collector POST /api/v1/collect/runtime | — | 公网 401 ✅；本机 nginx 须带 `Host: llm.kxpms.cn` 走 https 才复现 401（不带 Host 打默认 server 得 301/404，为测法差异非回归） |
| PG node_probe_runs attempt 分布（切换后窗口） | 越界 = 0，分布全在 [1..7] | 同左（双节点合并窗口 1:11/2:7/3:2/4:1/7:6） |

## 修复生效的直接证据

154 启动后 15s 内 `candidate_failure_monitor: failure log is stale` 告警首次以**真实数据**触发：`last_request_at=01:34:18`（当月视图，8s 前）vs `last_insert_at`（candidate_failure_logs）8h 前。修复前该探针读冷父表、last_request_at 恒为一天以上陈旧，告警从不触发。**当前暴露的真实异常：candidate_failure_logs 已约 8h 无新插入**——写入侧为何停更待查（R13 候选）。

## 遗留 / 转 R14

- **P5（仍开放，本批不含修复）**：154 `partition_manager: promote failed`，`uq_request_logs_2026_09_final_success_session` 23505（比 closeout-2081 记录的 `uq_request_logs_2*` 前缀更精确的约束名）。与 d812e1a53 读面修复无关（DDL/写侧分区提升问题），09-10 起预存。d812e1a53 三个 worker 读面已切走，但其依赖的 `request_logs_hot` 提升失败若长期不愈，当月视图数据完整性仍受威胁。
- **candidate_failure_logs 写入停更**（本批新发现，见上）。
- **P4（仍开放）**：154 `ursm.v2 persist collect failed`（redis expected hash got string），本窗口复现 1 次。
- cred 41：`suspended/permanently_exhausted`，探针持续命中（01:36:42），等用户侧充值归属核对，无网关侧动作；cred 42 `ready/ok`。
- 运维注意：蓝绿切换后 active_port 轮换至 8782，直连 8781 的探测会落空；journald 单元名两节点不同（245=`llmgo-245-canary@*`，154=`llm-gateway-go-canary@*`）。

## 修订记录（2026-09-12 审计轮，2084 跟进部署）

本会话收口前按审计门禁对 2083 部署与本文档全量复核。项目未启用 session-governance 门禁（无 `.acc-session-policy`），按 skill 口径降级为轻量自审（降级原因在此记录）：git 证据链 + 双节点线上新鲜采证 + 声明逐条核对。

### 审计结论（2083 部署本身）

- 声明逐条属实：push 内容（20ea7d4f0 恰 4 文件、menu-config churn 未提交）、bump 单调（2080→2082→2083 无撞号）、d812e1a53 不在 2082-cf7256f6、两节点 2083 门禁数据、PG attempt 越界=0、cred 41/42 状态——全部与线上实测一致。并行会话 round-17 文档亦独立确认 "release 2083 confirmed live"。
- 无未知错误面：245 剩余 ERROR 经放宽口径逐一核验均为已知家族（502/503/executor canceled 流量噪音、P5 双通道、vacuum worker 57014 瞬时——与首次部署超时同源的 252 负载）。
- 退役 8781 单元 `failed`（Result=timeout，SIGKILL/9）：drain 超出 TimeoutStopSec 被 systemd 击杀，属既有蓝绿退役路径行为，非本次缺陷。

### 实质发现与错误归因更正

本文档原"修复生效的直接证据"一节把 154 的 `candidate_failure_monitor: failure log is stale` 记为"暴露 candidate_failure_logs 写入停更的真实异常"——**归因错误，已更正**：

- 实测写入方**一直健康**：`candidate_failure_logger` 写的是 **`candidate_failure_logs_hot` 热表**（max(ts) 距查询 4 分钟，10min 12 行）；父表 max(ts)=09-11 17:39 只是最后一次提升水位。
- 假警报根因：monitor 的 staleness 探针读**裸父表** `candidate_failure_logs`——与 d812e1a53 所修 request_logs **同一族冷父表盲区**，该轮漏掉了 candidate_failure_logs 家族。同盲区还波及 checkAlerts（5min 窗恒空，从不告警）、auto-cool cfl CTE（recent_failures 钉 0）、model_probe passive boost（5min 选型恒空）、daily_probe_audit 3 天 candidate 分支（少扫）。
- **修复（d249604c7，已随 2084 上线两节点）**：6 处读面切 `candidate_failure_logs_with_current_month`（视图已在库）；守卫测试扩展锁 candidate_failure_logs 面 3 文件 + opslog_trimmer 父表 DELETE 排除锁定（UNION 视图不可删，保留语义必须留在父表）。

### 2084 验证（3c1195f2，245 切换 46s / 154 切换 4s，active 轮换回 8781）

- 两节点 healthz/readyz 2084 ready；**staleness 假警报归零**（修复前每周期触发）；23514=0；非已知噪音 ERROR=0；collector 公网 401；PG attempt 越界=0；bg 接线在位。
- 部署前已将改后 SQL 在生产库只读试跑（视图列兼容、staleness 双探针全新鲜、checkAlerts 可见真实近期失败）。

### 转 R14（更新）

- **P5（仍开放）**：`uq_request_logs_2026_09_final_success_session` 23505，双节点、双通道（partition_manager promote + data-lifecycle hot cron）确认。读面已全部切走，但提升长期失败会使父表/历史分区持续缺数，仍需根治。
- ~~candidate_failure_logs 写入停更~~ → 已定性为读面假警报，2084 修复关闭。
- **P4（仍开放）**：ursm.v2 persist collect failed（redis expected hash got string）。
- 252 PG 瞬时语句超时（57014）今晚三处命中（首部署 ensure、vacuum worker、首次候选重试）——量级尚低，列入观察。

### R14 轮结果（2026-09-12 T+40min 观察，见 docs/audit/2026-09-12-r14-observation-p5p4-readonly.md）

- P5 根因已由并行会话闭环（`8f4c13970` 接线根除 + 迁移 694 demote 自愈，独立复核成立），**待下一轮部署**；父表水位修正为 09-09 19:35:29（停摆期内有 SKIP LOCKED 并发越迁，非严格每 tick 必败）。
- P4 严重度上调：根因=persist.Collect 误吞 `request_dedup` string 标记键整批 abort，`ursm_node_snapshot_min` 停摆自 09-11 21:16；修复方案已产出（同上文档 C 节）。
- 2084 观察合格：staleness 恒 0，auto-cool 对 cred 18/19/8（100% 真故障）首次真实翻牌。
- cred 41：探针持续命中，等用户侧充值归属核对；cred 42 ready/ok。

## 关联

- 前置：docs/audit/2026-09-11-deploy-closeout-2081-r12-p2p3.md（P2/P3 闭环、P4/P5 立项）
- 并行线收尾：docs/handoff/2026-09-11-r13-drawer-fixes-closure.md（2082 本机部署与浏览器复验）
