# 2026-08-17 — 部署门禁只读审计 handoff（任务 A 收尾）

> 来源：会话优化 v4 交接 `/tmp/handoff-20260817-041029.md` §10 任务 A。
> 详细事实与 smoke checklist：`docs/session-logs/2026/08/2026-08-17-deploy-gate-readonly-audit.md`。

## 1. 一句话结论

dev `llm.itestu.cn` 仍 502（根因未变：252 nginx vhost `127.0.0.1:11008` 上游无监听），但 **245 实际已 200 ready（`version=2.5.0-102c885e-20260817-1573`）**、**生产域名 `llm.kxpms.cn` 也 200**（指向 154）。本会话不修复 dev 阻塞，因晋级路径不经过 dev；245/154 门禁前置已在 245 端满足。

## 2. 本会话未推送的提交已就近保留：本地分支 `rescue/ursm-v2-rollout-gate-127e5b316`（不推送）。127e5b316 的 14 文件 diff 在远端 `b97bca01c` 工作树中已落地（commit tip 不在远端但内容已在）；task B 代码工作未受影响。

## 3. 与原 2026-08-16 handoff 的事实校正

| 项 | 2026-08-16 handoff 描述 | 本会话实测 |
|---|---|---|
| dev 是否阻断 245 晋级 | "被 dev 端点门禁阻断" | 245 healthz 200 且 version 一致；dev 阻塞不波及 245 |
| `llm.kxpms.cn` DNS | 未提及 | `115.29.212.252`（同 252 IP），后端 200（vhost 指向 154） |
| 245 是否就绪 | 仅描述为晋级路径 | 当前 `version=1573`，可走 deploy-test dry-run |

## 4. 任务 B 已落地的客观证据

- `scripts/verify-ursm-v2-rollout.sh` 与 `scripts/test-verify-ursm-v2-rollout.sh` 在 `b97bca01c` 工作树中存在；
- 离线自测：`verify-ursm-v2-rollout self-test passed.`；
- `go test ./domains/ursm/v2/... ./domains/streaming/executors/... ./metrics/...` 全部 `ok`；
- 因此**任务 B 的代码工作已完成（无需重复施工）**。任务 B-收尾建议聚焦于：outcome sidecar 在真实 shadow 流量下的双写成功率验证、采样率对 canary diff 频度的影响、回退路径的端到端 smoke。

## 5. 任务 A 的下游交付物

详见 session log §6 中的三套 smoke checklist（URSM shadow/canary、Goal-Handoff 202→confirm→restore、Durable outbox HMAC/consumer/ownership）。这些是**待运维授权后才能跑**的步骤；本会话仅提供步骤与前置。

## 6. 建议

1. 245 `--env test --dry-run` 先跑一次，验证 runner + 注入链路；运维授权后再 `--apply`。
2. dev `llm.itestu.cn` 修复另开 ticket，不阻塞会话优化 v4 主线。
3. 抢救分支不必 push；若需要远端追溯，可在合适时机 cherry-pick 或新分支承载 `127e5b316` 的 commit 元数据。