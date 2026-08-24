# 2026-08-24 管理面板 401 轮询风暴修复 + 401 可观测性审计修正

## 背景

上一修复（`2026-08-24-dataplane-skkey-static-gate-fix.md`）的遗留项：
1. 252 nginx 单日 96,686 次 `/api/*` 401——会话过期的管理面板（Cursor 内嵌浏览器，referer `/?login=1`）持续轮询 `background-tasks`（每 30s）与 `credentials/sliding-window`（batch + N-way 回退，每对 credential×model），401 被吞掉永不停歇。
2. "401 不落 gateway.log / request_logs" 的监控盲区结论待核实与修复。

## 根因（前端）

- `web/src/api/_core.ts` 的 401 处理只覆盖 `isAdminProtectedPath`（admin/users/keys/routing/auth 系列）；`/api/system/background-tasks`、`/api/credentials/sliding-window` 等受保护端点不在名单内，401 只 throw 不清理登录态、不跳转 → 组件永远轮询。
- `SystemStatusIndicator`（全局挂载，含公共页 /?login=1）每 30s 无条件调 background-tasks（admin-cookie 端点）。
- `QueuePerspectivePanel.refreshWindowStats` 在 batch 请求失败（含 401）时回退到 legacy N-way GET —— 把一次认证失败放大为每 credential×model 一发。

## 根因（后端，审计修正）

"401 盲区"结论**部分错误**，实测修正：
- gateway.log：静态门拒绝有 `auth: invalid API key` WARN（此前误查了旧日志路径 `/opt/llm-gateway-go/logs/`，实际在 `LLM_GATEWAY_LOG_FILE=/var/log/llm-gateway-go/gateway.log`）；handler 级 401 有 `http_request` WARN。无盲区。
- request_logs：chat 路径 401 **已落表**（`EmitFailure` → `updateRequestLog` 0 行回退 `insertRequestLog`；245 实测插入 `error_kind=invalid_key` 行）。事故期间未落表是因为旧静态门在 handler 之前拒绝。剩余小缺口：`/v1/models`（GET）401 不落表（价值低，access log 已覆盖）；静态门（非 sk-）401 不落 request_logs（内部调用方，gateway.log 已有）。

## 修复

| 文件 | 内容 |
|------|------|
| `web/src/api/_core.ts` | 中央会话失效处理：非 admin 名单的 `/api/*` 401 且 SPA 仍认为已登录 → `clearAll()` + 整页跳 `/?login=1&redirect=...`（对齐 router 守卫语义；排除 `/api/auth/me` hydration 探测；`/?login=1` 页 loop guard） |
| `web/src/components/SystemStatusIndicator.vue` | 未登录时跳过 full healthz 与 background-tasks，只用公共 basic healthz |
| `web/src/components/QueuePerspectivePanel.vue` | `refreshWindowStats` 匿名跳过（skip 不 stop，重登录自动恢复）；batch 401 不回退 N-way |
| `middleware/auth_mw.go` | 静态门拒绝日志补 `key_prefix`（前 8 字符 + `****`，对齐 api_keys.key_prefix 惯例）——下次副本 key 漂移可直接从 gateway.log 追踪 |
| `web/src/components/QueuePerspectivePanel.test.ts` | 新增 2 测试：匿名不轮询、batch 401 不放大 |

## 部署阻塞处理（重要，LP owner 必读）

未跟踪的 `sql/migrations/startup/573_drop_request_logs_body_columns.sql`（LP body-storage 优化，从未提交、从未在任何环境应用）**阻塞了所有部署**：

```
psql:573: ERROR: cannot drop columns from view
```

根因：迁移用 `CREATE OR REPLACE VIEW` 重建 `request_logs_with_current_month` 并**移除** body 列投影——PG 的 CREATE OR REPLACE VIEW 只能追加尾部列，不能移除/重排已有列（rule 49 §9.1 view freeze）。需改为 `DROP VIEW + CREATE VIEW`（同事务内原子）。已重命名为 `.sql.skip`（部署脚本自带的正规延期机制）并在文件尾附修复指引。**修好后改回 `.sql` 即可随下次部署应用。**

## 验证

- `go test ./middleware/` ✅；`vue-tsc --noEmit` ✅；`vitest QueuePerspectivePanel + _core` 27/27 ✅；pre-commit 6/6 ✅
- 245（seq 1727）：dist 含 `login=1&redirect`；静态门拒绝日志带 `key_prefix`；chat 401 落 `request_logs_hot`
- 154（seq 1728）：同上
- 401 速率收敛需部署后观察 nginx（预期 background-tasks/sliding-window 401 归零或降至每次会话过期一跳）

## 事故记录（流程）

23:15 另一会话提交 78160af51 时清空了本会话未提交的工作树改动（auth_mw/_core/两个组件/测试全灭），23:20 的部署因此不带修复。已全部重做并**先提交（43899e219）再部署**。多会话并行务必走 worktree 隔离（rule 26）。

## 追加（00:00-00:30）：全局 key 统一 + 第二次覆盖事故

1. **第二次覆盖**：另一会话 00:00:57 将 245 部署回其分支 496f3cf42（分叉早于 85d9393a4，静态门修复在 245 短暂复活，实测 sk-gwops 新 key 被静态门拒）。origin/main 此后已正确合并双方（aface2d64），本地快进后重部署 245（seq 1726）恢复；154 随后同版部署（seq 1727）消除偏斜。
2. **全局 key 统一（原遗留项①完成）**：新专用 ops key `sk-gwops-*` 注册进 api_keys（id=113，owner=gateway-ops，is_system=true）；替换四处 envs SSOT 旧值（common/llm-providers 原为第三把 key、project/154 原为用户 key89、server-245 原为用户 key105 —— 三值漂移正是间歇 401 的配置根源）+ sops enc 重加密（recipient 不变）+ envs 仓库提交 3ad22ee；154 `/etc/llm-gateway-go/env` 与 245 `/opt/llm-gateway-go/.env`（注意两台 unit 的 EnvironmentFile 路径不同：llm-gateway-go.service vs llmgo-245.service）均备份后替换并重启。验证：双进程 key sha 一致（eb7e96002b82）；ops key 经 llm.kxpms.cn / llmgo.kxpms.cn / 两台直连均 200；用户 key89 无回归（仍是有效 DB key）；未来部署经 inject_gateway_api_key 从 SSOT 注入同值。
3. 遗留：两台服务器各保留 `env.bak-gwops-*` 备份（含旧用户 key 值，用户 key 本就有效，风险低）；common 的 `LLM_GATEWAY_API_KEY_LOCAL`（dev 变体）未动。
