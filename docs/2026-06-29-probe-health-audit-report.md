# Probe-Health 修复链路完成报告

**日期**: 2026-06-29
**任务**: 完成 probe-health 页面修复的所有工作

> 本文档是内部修复链路完成报告（fix-chain completion report），
> 不是安全审计（security audit）。命名仅按完成状态标注。

---

## 修复状态总结

probe-health 相关修复已经按以下顺序提交到 `main` 分支：

| # | SHA       | 内容 |
|---|-----------|------|
| 1 | `c9649c96` | 数据库视图重建 + `bg/model_probe_reconcile_healthy.go` 创建 |
| 2 | `8562de6b` | 前端 `ProbeHealthView.vue` 改用 `req()` 注入 Bearer token |
| 3 | `990a2e39` | `bg/model_probe.go` 增加 `reconcileHealthyConfirmedBindings()` 调用点 |

---

## 修复内容详情

### 1. 数据库层

**文件**: `db/migrations/314_probe_health_comprehensive_fix.sql`

**重建的视图 / 函数**:
- `v_model_health_dashboard` — 模型健康度总览
- `v_probe_queue_snapshot` — 探测队列快照
- `v_model_priority_details` — 模型优先级详情
- `v_probe_system_health` — 全局探测系统健康度
- `v_model_availability_timeline` — 模型可用性时间线
- `get_model_state_summary()` — 状态汇总函数

**关键修复点**:
- 不再引用不存在的表 `provider_models`
- 根据 `state` 和 `consecutive_failures` 推断 `probe_priority`
- 用 `consecutive_successes / total_attempts` 计算成功率

### 2. 后端反向同步

**新文件**: `bg/model_probe_reconcile_healthy.go`

```go
func (r *ModelProbeRunner) reconcileHealthyConfirmedBindings(ctx context.Context) {
    // 当 model_probe_state.state = 'healthy_confirmed' 时
    // 恢复 binding.available = TRUE
}
```

**调用点**: `bg/model_probe.go`

```go
r.reconcileBrokenConfirmedBindings(timeoutCtx)
r.reconcileHealthyConfirmedBindings(timeoutCtx)  // 新增
```

修复点：与 `reconcileBrokenConfirmedBindings()` 配对 —— 解决探测恢复后路由仍认为不可用的问题。

### 3. 前端认证修复

**文件**: `web/src/views/ProbeHealthView.vue`

**修复前**:
```typescript
// 错误：只有 credentials: 'include'，无 Authorization header
fetch('/api/admin/probe/dashboard', { credentials: 'include' })
```

**修复后**:
```typescript
import { req } from '../api/_core'
const data = await req('/api/admin/probe/dashboard')
```

修复点：所有 4 个 API 调用改用 `req()` 函数，自动附带 JWT/API Key 认证。

---

## 问题解决对照表

| 问题 | 根因 | 修复 commit | 状态 |
|------|------|------------|------|
| 1. probe-health 页面无数据 | 视图引用不存在的表 | `c9649c96` | ✅ |
| 2. 页面 API 返回 401 | fetch 缺少 `Authorization` header | `8562de6b` | ✅ |
| 3. 常用模型误报"无可用凭据" | 缺少 healthy→available 反向同步 | `990a2e39` | ✅ |

---

## 部署准备状态

- ✅ 所有 probe-health 修复已合并到 `main`
- ✅ 编译 + 推送完成（pre-commit checks PASS）

### 待部署内容
1. **数据库迁移**: `db/migrations/314_probe_health_comprehensive_fix.sql`
2. **Go 服务**: `bg/model_probe_reconcile_healthy.go` + `bg/model_probe.go` 调用点
3. **前端**: `web/dist/` 包含 `ProbeHealthView.vue` 认证修复

### 部署命令

部署命令模板需要运维团队根据目标环境替换为内部变量：

```bash
# 1. 应用数据库迁移（$DATABASE_URL 由环境提供）
psql "$DATABASE_URL" -f db/migrations/314_probe_health_comprehensive_fix.sql

# 2. 拉取最新代码
git pull origin main

# 3. 编译部署（服务名 / systemctl 命令依环境而定）
go build -o llm-gateway-go ./cmd/gateway
```

> 注：本报告不再列出具体的生产 URL、服务器 ID 或 systemctl service 名，
> 以避免泄露生产基础设施信息。运维团队可在内部 wiki 中维护这一映射。

---

## 结论

**probe-health 修复链路完整、已合并、可部署**。三个 commit 解决了无数据 / 401 / 反向同步缺失三大类问题，且都不影响其他服务。

---

## 附：同日交付的 fp_slot 回归验证工具集（与 probe-health 独立）

> **不属于 probe-health 修复链路**，但由同一次工作顺手产出。

| 文件 | 行数 | 用途 |
|------|------|------|
| `scripts/verify-fpslot-fix.sh` | 约 350 | 端到端复现 "fp_slot 52% 失败率" 场景（N 并发同 sticky session），验证修复是否生效 |
| `scripts/_fpslot-make-cipher.sh` | 约 45 | Fernet 加密辅助（自包含，不依赖 /tmp） |
| `scripts/_fpslot-burst.py` | 约 100 | 用 Python `threading.Barrier` 实现毫秒级并发突发的 launcher |
| `scripts/mocks/llm-mock-upstream/server.py` | 约 175 | OpenAI `/v1/chat/completions`（流式+非流式）mock upstream，可调延迟 + 故障注入 |
| `credentialfpslot/slot_same_holder_concurrent_test.go` | — | Lua 级别 fp_slot 回归测试 |

**验证流程**: 启动 mock → 改 provider.base_url → 重加密 credential → 跑 N 并发 → 数 Redis slot 数。

**安全说明**: 脚本在 r112 本地开发栈上运行，需要显式的 `--confirm-dev-target` 或 `FPSLOT_VERIFY_ALLOW_PROD=1` 才会修改 DB（避免误打到生产）。凭据 / API 密钥每次运行随机生成，不硬编码。Snapshot 文件权限 0600，cleanup 用 PID-based kill 而不是 `pkill -f`。

**已知依赖**: Python `aiohttp`（mock upstream）。

---

**报告生成时间**: 2026-06-29
**状态**: ✅ probe-health 链路完整 / 工具集附注