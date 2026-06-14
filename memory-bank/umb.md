# UMB (Ultra Memory Bank) — llm-gateway-go v2.0

> **实时跟踪**：当前任务进度、关键决策、待办、已交付
> **最后更新**：2026-06-14 22:56 (post-deploy checkpoint)

## 🎯 当前任务 — **已完成** ✅

**目标**：llm-gateway-go v2.0.0 — Auto 路由模式 + 24h 审计修正
**状态**：✅ 已上线 184 k3s (image `kx-llm-gateway-go:gitsha-10ec804a`)
**Git tag**：`v2.0.0` (submodule) + `deploy/prod-184-20260614-225626-b7a27e9fae7a` (主仓)

## 📍 阶段进度（全部完成）

- [x] **阶段 1.0**: 启动 codegraph + memory-bank 初始化
- [x] **阶段 1.1**: 审计最近 24h commit + 修正 2 项遗漏
  - modelcatalog.ClearProviderBindings 包事务
  - upsertCredentialModelSQL LIKE 'manual%%' 改为 'manual%'
- [x] **阶段 1.2**: 写 24h 审计报告 `docs/2026-06-15-24h-audit-report.md`
- [x] **阶段 1.3**: 提交 audit fix commit + push
- [x] **阶段 2.1**: 创建 SQL 迁移 (3 张新表 + request_logs 5 列 + down)
- [x] **阶段 2.2**: 实现 `autoroute/classifier.go` + 测试 (16 测试)
- [x] **阶段 2.3**: 实现 `autoroute/scoring.go` + `profile.go` + 测试
- [x] **阶段 2.4**: 实现 `autoroute/index.go` + `decision.go` + LLM fallback + 测试
- [x] **阶段 2.5**: 实现 `bg/auto_index_refresher.go` 后台 worker
- [x] **阶段 2.6**: 修改 `relay/handler.go` + `auto_route.go` 接入 auto
- [x] **阶段 2.7**: 实现 `admin/auto_route.go` 5 个 API + `handler.go` 注册
- [x] **阶段 2.8**: `cmd/gateway/main.go` 装配 Decider
- [x] **阶段 3.1**: 单元测试全部通过 (autoroute / bg / admin / modelcatalog / provider / routing)
- [x] **阶段 4.1**: v2.0 design + ops 文档
- [x] **阶段 4.2**: 打 `v2.0.0` tag + push
- [x] **阶段 4.3**: bump 主仓 submodule 到 `194087f3` + push
- [x] **阶段 4.4**: SSH 184 应用 SQL 迁移到 `llm-gateway-pg` (postgres pod)
- [x] **阶段 4.5**: 184 k3s 部署 + curl 验证（包含一个 NULL scan fix in 10ec804a）
- [x] **阶段 4.6**: post-deploy checkpoint + memory-bank 收尾

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