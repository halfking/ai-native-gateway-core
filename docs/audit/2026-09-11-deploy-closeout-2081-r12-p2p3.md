# 2081 部署收尾 — R12 P2/P3 修复上线验证（245 + 154）

日期：2026-09-11 深夜 · 部署基线：`014b36cdd`（main）· 构建：`2.5.4-014b36cd-20260911-2081`（seq 2081）· 通道：deploy-seamless 蓝绿（两节点均 active_port=8781）

## 部署范围

本次 2081 相对上一运行版本 `2081-ab2a3c8e` 新增包含：

- `6490fd981` R12 P2/P3 收尾：legacy runOne missing-binding 分支补写 node_probe_runs 审计行（insertNodeProbeRun，direct 同时作 gw 轮、success=false、nextSec=0，attempt 由 helper 内部钳位 [1..7]）；154 nginx with-backup-upstream 变体补 `/api/v1/collect/` 路由
- `014b36cdd` docs：强启误降级修复生产只读验证结论

> 注：两节点上一运行版本已是 2081-ab2a3c8e（并行会话 2026-09-11 早间部署），非交接文档所称 2080-69420603。本次为同 seq 不同 sha 的递进（bump 判定 git_sha 变化前 seq 已被 ab2a3c8e 占用，本次按"同代码重跑"逻辑沿用 seq 2081，以 sha 区分）。

## 部署结果

| 节点 | 结果 | 门禁 |
|---|---|---|
| 245（预发） | ✅ 101s 内完成（23:46–23:48） | healthz/readyz/version 全绿；DB 就绪 2s；admin 密码同步通过；凭据解密冒烟 providers=18,1,14 creds=12 failed=0 |
| 154（生产） | ✅ 101s 内完成（23:53–23:55） | 同上全绿；DB 就绪 0s；解密冒烟 failed=0；nginx http2 deprecation warn 为既有噪音非本次引入 |

## 专项验证（P2/P3 修复生效证据）

1. **attempt 23514 消失（P2 clamp）**：两节点 journald 自切换时刻起 `grep 23514` 均为 0 条。PG `node_probe_runs` 近 30min attempt 分布：1:305 / 2:230 / 3:225 / 4:217 / 5:171 / 6:1 / 7:4——全部落在 [1..7]，0 与 >7 均消失，钳位收敛于 insertNodeProbeRun 单点生效。
2. **collector 路由修复（P3 nginx）**：154 上 `POST /api/v1/collect/runtime`（Host: llm.kxpms.cn）经本机 nginx 与公网域名实测均返回 **401**（未带 token 的预期响应），修复前为 404。with-backup-upstream 变体的 tracked 漂移已随 6490fd981 消除。
3. **bg worker 接线（两分支一致）**：245（legacy 分支）与 154（URSM authoritative 分支）journald 均出现 `CHECKPOINT: credRecovery started` + `balance_quota_probe started`（120s 间隔）；245 探测提交 count=6。无 `postgres disabled`、无 `probeSubmitter not wired`。

## cred 41/42 状态（部署时点快照）

- cred 42 `hzx-2`：`active/ready/ok`——恢复闭环维持。
- cred 41 `hzx`：`active/suspended/permanently_exhausted`，`balance_last_checked_at=23:51:01`（2min 探针持续命中）。上游仍 402 余额为零（2026-09-11 上午定性：充值未进入该 key 对应 MiniMax 账户/分组）。**网关侧无进一步动作；用户侧控制台核对充值归属后，下一轮探针自动翻牌，严禁 admin force_enable。**

## 遗留 / 转 R13

- **P4**：154 journald `ursm.v2 persist collect failed`（redis key expected hash got string）——request_dedup 命名空间双写方类型契约排查，本次未动。
- cred 41 等待上游充值归属确认（外部依赖）。
- 154 nginx 既有 http2 deprecation warn（kxpms-cn-auth/www.conf），与本次无关，可择期清理。
