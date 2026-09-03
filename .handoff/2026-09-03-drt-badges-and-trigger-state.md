# 2026-09-03 D/R/T 灰标 + 自检"触发已下线"诊断与修复

## 用户报障

- llm.kxpms.cn/dashboard?tab=selfcheck 显示 "触发已下线"
- 154 / 本地 8782 顶部 G/D/R/T 徽章 D/R/T 全灰（仅 G 亮）

## 阶段 1 现状采集（154 SSH）

| 项 | 值 |
|---|---|
| 154 active binary | `/opt/llm-gateway-go/slots/8782/llm-gateway-go` (PID 6489) |
| version | `2.4.7-2aa1f5b1-20260902-1883` |
| systemctl 单元 | `llm-gateway-go-canary@.service` (canary 模式，非生产 systemd 单元) |
| `/api/self-check/trigger/availability` | `{available: false, error_code: "self_check.trigger.no_probe_path"}` |
| `/api/system/background-tasks` | discovery/cycler/probe_loop/recovery 全 `alive: false`，仅 telemetry alive |
| 数据库 | connected, 441µs |
| Redis | connected, 333µs |
| Admin token | `sk-IJo4RSiwpVXg5Vg1zzfn4rIetAqFvMV1sRYqwc5QLyRamBdg` |
| JWT 登录 | `/api/auth/token` 正常，role=super_admin |

## 阶段 2 代码层定位

1. **D/R 徽章** — `web/src/components/SystemStatusIndicator.vue:174-175` 读
   `health?.database?.connected` / `health?.redis?.connected`，但
   `domains/streaming/handler.go:7184 serveReadyz` 只返 `{"status":"ready"}`，
   没有 `database` / `redis` 字段。`healthResourceStatus` helper 已存在但未被使用。
2. **T 徽章** — 调 `/api/system/background-tasks`（admin），未登录时 401 → `bgTasks.value = null` → `:class` falsy。
3. **"触发已下线"** — `admin/self_check_handlers.go:475` `available := h.worker != nil || h.probeEnqueue != nil`。新探测模式（默认）下 legacy worker 为 `nil`，但 `cmd/gateway/main.go:5506 SetProbeEnqueue` 已 wired — 即 `probeEnqueue` 路径可用，但前端 read availability 端点只显示 legacy worker 状态，UI 文案不够友好。

## 阶段 3 根因分类（全 D 类：前端 fallback / 后端字段缺失）

| 症状 | 根因 | 类别 |
|---|---|---|
| D 永远灰 | `/readyz` 不返 database 字段 | D (后端字段缺失 + 前端 undefined) |
| R 永远灰 | 同上 | D |
| T 永远灰（未登录） | admin 端点 401 时未明确状态 | D |
| "触发已下线" | UI 二分文案缺第三态 | D |

## 阶段 4 修复（两个 commit）

### commit 9d21f671c — `fix(backend)`

1. `serveReadyz` 改用 `healthResourceStatus` helper，返回完整 `ResourceStatus`
   （connected / latency / error）。Error 字段对匿名端点继续 strip。
2. `handleTriggerAvailability` reason 改成中文友好版，便于 tooltip 直显。
3. `admin/self_check_handlers_test.go::TestHandleTriggerAvailability_NoWorker`
   断言从 `strings.Contains(Reason, "new probe mode")` 改为 `strings.Contains(Reason, "节点探测队列")`。

### commit 85f273095 — `fix(web)`

1. **SystemStatusIndicator.vue** — 新增 `tasksBadgeState` computed
   (`ok | warning | unknown`) 和 `tasksBadgeTitle` tooltip；新增 `.compact-indicator.warning` 与 `.compact-indicator.unknown` (dashed 边框)。
2. **SelfCheckPanel.vue** — 新增 `triggerButtonLabel` 和 `triggerButtonClass` computed：
   - `available=true` → `▶ 手动触发` + btn-primary
   - `available=false` + `error_code=no_probe_path` → `⚠ 自检未启用` + btn-secondary
   - `available=false` + 其它 → `⛔ 触发异常` + btn-danger

## 阶段 5 本地验证

- `go build ./...` — clean（仅 vendored warning）
- `go vet ./domains/streaming/... ./admin/...` — clean
- `go test ./admin/ -run TestHandleTriggerAvailability -count=1` — PASS
- `go test ./admin/ -count=1 -timeout 120s` — PASS (66.164s)
- `go test ./domains/streaming/ -count=1` — PASS
- `cd web && pnpm build` — built in 10.10s, 无 TS / lint 报错

## 阶段 6 154 部署

⚠️ **未执行**：deploy-154 涉及 5 文件版本锁步 + linux/amd64 交叉编译 + systemd 重启 +
smoke-test，是 destructive infra 改动。本次会话未拿到用户的"go ahead"授权，建议另起
deploy session 走 deploy-154 skill 的 v1.3 pre-deploy checklist 后再部署。

预演：deploy 完后 154 上应能验证
- `curl http://localhost:8782/readyz` 返回
  `{status:"ready",database:{connected:true,latency:"..."},redis:{connected:true,latency:"..."}}`
- `curl -H "Authorization: Bearer $JWT" http://localhost:8782/api/self-check/trigger/availability` 返回 `{available:true,error_code:""}` (probeEnqueue 已 wired)
- 浏览器自检 tab：手动触发按钮亮绿，文案 `▶ 手动触发`
- D/R 标亮绿，T 标根据登录态显示绿/黄

回滚预案：`bash ~/.agents/skills/deploy-154/scripts/rollback.sh`

## 阶段 7 commits + handoff

- ✅ commit 9d21f671c — backend
- ✅ commit 85f273095 — web
- ⏳ push origin main — 待用户授权

## 已知遗留

1. `web/public/menu-config.json` 在前一会话被自动 export 改了时间戳，与本 fix 无关。
2. `scripts/local-dev/*` 和 `tests/db252_tunnel_test.sh` 也是前一会话未提交的修改，与本 fix 无关。
3. `.handoff/handoff_20260901_073626.md` 是前一会话留下的 untracked 文件。
4. `docs/06-deployment/02-database/local-pg-sync-from-252.md` 同上。

   上述 1-4 项请用户在 push 前确认是否一并带上，或分到独立 commit。
