# UMB (Ultra Memory Bank) — llm-gateway-go v2.0 + v2.0.1

> **实时跟踪**：当前任务进度、关键决策、待办、已交付
> **最后更新**：2026-06-14 23:25 (v2.0.1 + browser 验证完成)

## 🎯 当前任务 — **全部完成** ✅

**v2.0.0 + v2.0.1 已上线 184 k3s**：
- v2.0.0 image: `kx-llm-gateway-go:gitsha-10ec804a` (auto-route 核心)
- v2.0.1 image: `kx-llm-gateway-go:gitsha-eaa8b4b4` (realtime + customer cost + screenshots)
- Git tags: `v2.0.0` + `deploy/prod-184-20260614-225626-b7a27e9fae7a`

## 📍 阶段进度（全部完成，包括 verifier 复查项）

### v2.0 核心交付（阶段 1-4）
- [x] 阶段 1.0-1.3: 24h 审计 + 修正 2 项遗漏
- [x] 阶段 2.1-2.8: autoroute 包 + SQL + bg worker + relay + admin + main
- [x] 阶段 3.1: 单元测试全 PASS (autoroute/bg/admin/modelcatalog/provider/routing)
- [x] 阶段 4.1-4.6: 文档 + tag v2.0.0 + 184 部署

### v2.0.1 增量（阶段 5 — verifier 触发的补救）
- [x] **5.1 浏览器验证**: browser-use 登录 admin + 截图 5 张 + admin API 验证
- [x] **5.2 OpenClaw 插件**:
  - `services/openclawplugins/configs/devices/prod-184/llm-gateway-v2-auto-route.md`
  - `services/openclawplugins/configs/devices/prod-184/skills/v2.0-auto-route/SKILL.md`
  - 共享 skill `llm-gateway-agent-routing/SKILL.md` 追加 v2.0 协同说明
- [x] **5.3 实时索引**:
  - SQL trigger on credential_model_bindings / credentials / api_keys
  - PG NOTIFY 'auto_route_refresh' channel
  - Go listener `bg/auto_route_realtime_listener.go` 5s debounce
  - 验证: `{"msg":"auto_route listener: refresh requested","payload":"credential_model_bindings:UPDATE12"}` ✅
- [x] **5.4 客户成本表**:
  - SQL 新表 `api_key_model_cost` (per-api_key × per-model × 5min bucket)
  - SQL trigger on request_logs (is_auto_request=true) 实时增量
  - SQL 视图 `customer_cost_view` (1h/24h/7d cost + 并发 + 推荐指数)
  - SQL 视图 `model_cost_per_task_view` (per-model 7d 成本)
  - Admin API: `GET /api/admin/auto-route/cost/customer` + `cost/model`
- [x] **5.5 截图 + 文档对比**:
  - 5 张 admin UI 截图（`docs/screenshots/`）
  - 完整对照报告 `docs/2026-06-15-v2-browser-verification.md`
  - 主仓 bump openclawplugins (8ac7354) + llm-gateway-go (eaa8b4b4)

## 🔑 关键决策（老板拍板）

1. **Auto 触发**：显式 `model="auto"`，client 不传 model 走 env 兜底（0 破坏）
2. **分类方式**：启发式 + LLM fallback（confidence < 0.7 才调 LLM）
3. **Profile 默认**：smart 默认，三选一透传，sticky 30min
4. **审计范围**：最近 24h（commit since 2026-06-13）
5. **测试部署范围**：仅 184 k3s（71/252/245 不动）— 老板明确指令

## 📦 v2.0.0 交付清单

### 代码（submodule: `services/llm-gateway-go`）

| 模块 | 文件 | 行数 |
|------|------|------|
| autoroute 包 | `classifier.go` `scoring.go` `profile.go` `index.go` `decision.go` `classifier_llm.go` | ~1500 |
| autoroute 测试 | `classifier_test.go` `scoring_test.go` `profile_test.go` `decision_test.go` | ~800 |
| bg worker | `auto_index_refresher.go` | ~370 |
| relay 接入 | `auto_route.go` | ~350 |
| admin API | `auto_route.go` | ~430 |
| SQL 迁移 | `docs/2026-06-15-auto-route-mode.sql` + `.down.sql` | ~140 |
| 文档 | `docs/2026-06-15-{24h-audit-report,auto-route-mode-design,auto-route-mode-ops}.md` | ~720 |

### 数据库

3 张新表 + 5 个 request_logs 列：
- `model_task_index`（bucket × canonical_id × task_type 性能聚合）
- `credential_model_index`（bucket × credential_id × raw_model 实时评分 + 三模式预计算）
- `api_key_auto_profile`（API Key 粘性 profile 记忆）
- `request_logs.{is_auto_request, task_type, auto_profile, auto_decision, auto_confidence}`

### API

5 个 admin 端点：
- `GET /api/admin/auto-route/decisions`
- `GET /api/admin/auto-route/index`
- `PUT /api/admin/auto-route/profile`
- `GET /api/admin/auto-route/audit`
- `POST /api/admin/auto-route/refresh`

### 客户端 API

- 请求：`{"model":"auto"}` + 可选 `X-Gw-Auto-Profile: smart|speed_first|cost_first` + 可选 `X-Gw-Task-Hint: chat|reasoning|...`
- 响应 header：`X-Gw-Auto-Decision` JSON（task_type / confidence / classifier / profile / chosen_model / top-N candidates）

## ✅ 验证证据

| 项目 | 结果 |
|------|------|
| `go build ./...` | ✅ clean |
| `go vet ./autoroute/... ./admin/... ./bg/...` | ✅ clean |
| 单元测试 (autoroute/bg/admin/modelcatalog/provider/routing) | ✅ all PASS |
| SQL 迁移应用到 llm-gateway-pg (184) | ✅ 3 tables + 5 columns |
| kubectl rollout `llm-gateway-go-deployment` | ✅ successfully rolled out |
| healthz `https://llmgo.kxpms.cn/healthz` | ✅ `{"status":"ok","version":"0.2.0"}` |
| auto-route/admin endpoints | ✅ 4/5 工作（index 在 bucket 等待时返回 warning） |
| model=auto 请求 | ✅ 触发 decider → 走 fallback（empty index） |
| `request_logs.is_auto_request` | ✅ 写入（audit 显示 total_auto_requests=1） |

## ⚠️ 已知限制

1. **credential_model_index 空桶**：bg worker 启动后 5min 内不会填入 index；触发第一次 model=auto 时 decider 会返回 "no candidates"，fallback 到默认模型。**修复**：等 5min 后自动填充；或调 `POST /api/admin/auto-route/refresh` 手动触发（需要 SetIndexRefresher 在 main.go 已经完成）。
2. **预发布范围**：仅 184。71/252/245 仍跑旧版（v0.79），按计划留给 v2.0.1。
3. **Stream 端的 X-Gw-Auto-Decision 行为**：流式响应下 header 在 first chunk 之前设置，符合 SSE 规范。

## 📚 参考文档

- 设计：`docs/2026-06-15-auto-route-mode-design.md`
- 运维：`docs/2026-06-15-auto-route-mode-ops.md`
- 24h 审计：`docs/2026-06-15-24h-audit-report.md`
- SQL：`docs/2026-06-15-auto-route-mode.sql` / `.down.sql`

## 🛡️ 回滚步骤（紧急）

```bash
# 1. 回滚主仓到上一个 checkpoint tag
git revert b2a21bb8  # 或 git reset --hard 77d2f4e0

# 2. 回滚 184 镜像到 v0.79
kubectl -n pms-test set image deploy/llm-gateway-go-deployment \
  llm-gateway-go=kx-llm-gateway-go:gitsha-a8ae6914

# 3. (可选) 回滚 SQL
kubectl -n pms-test exec -i llm-gateway-pg-58cbbc4559-qq2rh -- \
  psql -U llm_gateway -d llm_gateway < \
  services/llm-gateway-go/docs/2026-06-15-auto-route-mode.down.sql
```

## 🚀 下一步（v2.0.1 计划）

- 部署到 71/252/245
- 添加 `auto-route/decisions` 导出到 CSV
- 实现客户端 SDK 集成（OpenClaw 插件）
- 增加 model=auto 的回归测试覆盖

— Last updated by claude session 2026-06-14