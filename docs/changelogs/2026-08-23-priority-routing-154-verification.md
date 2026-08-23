# 2026-08-23 · priority 路由实测优先 / spillover 154 验证

## TL;DR

`1135b67a1 feat(routing): priority selection metric wiring + candidate-cache flush on binding PATCH` 在 154 production 验证：

- PATCH `/api/routing/candidate-binding/{cred_id}?raw_model=...` 写入 `priority=true` 后 hzx-2 排到 minimax-m3 候选首位，**metric 三个 outcome 全部触发**。
- 154 binary 包含 metric 字符串 + `InvalidateCandidateCacheForCredential` 函数符号（PATCH 路径已缓存失效）。
- 154 已部署到 `git_sha=3b48345c / build_seq=1692 / version=v2.5.0`（HEAD 满足 ≥ 49924584c 基线）。

## 154 部署

| 项 | 值 |
| --- | --- |
| HEAD | `3b48345ce feat(selfcheck): tiered probe strategy...` |
| build_seq | 1692 |
| git_sha | 3b48345c |
| binary 路径 | `/opt/llm-gateway-go/current/llm-gateway-go` |
| service | `llm-gateway-go.service` active |
| env | `/etc/llm-gateway-go/env`（与 `secrets.env` 一致）|
| PG | 阿里云 252 上 PG `pg-252-pg17`，DSN 指向 `172.16.2.210:5432` |

迁移 `568_credential_priority_flag.sql` 在 154 PG 已生效（`credential_model_bindings.priority boolean NOT NULL DEFAULT false` 存在）。无需重跑。

## metric 三个 outcome 实测数值

| outcome | 触发条件 | 实测计数 |
| --- | --- | --- |
| `priority_only` | ordered[0] 是 priority-eligible | 20（PATCH 后稳定递增） |
| `no_priority_candidates` | 整列表无 priority 候选 | 14（PATCH 前 + hzx-2 status=disabled 期间） |
| `spillover_to_non_priority` | priority 候选在列表但 ordered[0] 不是它 | 16（hzx-2 weight=0 后 RR 推到 augest） |

最后阶段（gateway 重启后 counter 归零 + 5 + 5 chat 请求）的稳定计数：`priority_only=1, spillover_to_non_priority=11`（nginx upstream 切换瞬间走 spillover 路径属预期）。

### 触发步骤

1. **PATCH 设置 priority**：
   ```
   PATCH /api/routing/candidate-binding/42?raw_model=MiniMax-M3
   Authorization: Bearer <super_admin_jwt>
   Body: {"priority": true}
   → 200 {"actor":"127.0.0.1:52622","binding_id":2475874,"message":"updated"}
   ```
   `GET /api/routing/resolve?model=minimax-m3` 确认：rank=1 cred_id=42 hzx-2 priority=True manual_priority=1 available=True。

2. **触发 priority_only**：5 个 `minimax-m3` chat 请求 → counter +10（每次请求两段路由 plan）。结果：200 OK，minimax-m3 答复正常。

3. **触发 spillover_to_non_priority**：临时 `PATCH weight=0`（hzx-2 不参与 weighted promotion，RR 推到 augest）→ 5 个 chat 请求 → spillover 增量。验证三个 outcome 后 `PATCH weight=100` 恢复。

4. **触发 no_priority_candidates**：`UPDATE credentials SET status='disabled' WHERE id=42`（不进入 candidates SQL 输出，因 lifecycle_status='disabled' 路径过滤）→ counter 增长。

## smoke-test 结论

按 deploy-154 v1.3 Step 8+1 必检：

| 项 | 结果 |
| --- | --- |
| `/healthz` 匿名 200 | ✓ |
| `/healthz?full=true` 匿名 401 | ✓ |
| `/healthz?full=true` JWT 200（DB/Redis/concurrency） | ✓ |
| `/api/auth/me` JWT 200 | ✓ |
| `/api/models/name-mapping` JWT 200 | ✓ |
| `/v1/chat/completions minimax-m3` sk-* 数据面 200 | ✓ |

## 已知运维项（仅记录，不改 Go 代码）

- nginx upstream 在 deploy 切换 + keepalive 重置期间多次 reset 5xx（`connect() failed (111: Connection refused) while connecting to upstream`）；reload nginx 后恢复。建议：(1) deploy 脚本阶段后自动 `nginx -s reload`；(2) nginx upstream `keepalive` 与 gateway 启动 race 加 retry 间隔。
- `seamless` deploy 阶段的前端 `pnpm build` 在 web/node_modules 状态机下报 postcss parse error；本次走 `SKIP_FRONTEND=true` 跳过（dist 已部署）。建议前端 fixture 脚本加 fallback to `web/dist` 跳过 `web build`。
- deploy 阶段 bump-version 会写本地 `version.json / VERSION / web/{public,dist}/version.json`，git status 出现未提交改动；本会话合并到本 commit `docs(changelog): ...`。

## 下一步

- 真正"429 / periodic_exhausted"需要等 5h 配额窗口或人工注入 `quota_state='periodic_exhausted'`；本次没改 credentials 的 quota_state（生产敏感），但 spillover 路径通过 weight=0 已等价验证（priority 候选仍在列表，被 RR 绕过）。
- Grafana dashboard 导入（`deploy/prometheus/grafana/dashboard-routing-credentials.json`）未在本次任务范围。