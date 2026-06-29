# Probe Health 修复完整审计报告

**日期**: 2026-06-29
**审计人**: opencode-agent (Kiro)
**任务**: 审计并完成 probe-health 页面修复的所有工作

---

## ✅ 审计结果总结

### 修复状态（截至报告时间）

probe-health 相关修复已经按以下顺序提交到 `main` 分支：

| # | SHA       | 提交时间 (CST)         | 内容 |
|---|-----------|------------------------|------|
| 1 | `c9649c96` | 2026-06-29 12:13 | 数据库视图重建（`db/migrations/312_probe_health_comprehensive_fix.sql`，后重命名为 314）+ `bg/model_probe_reconcile_healthy.go` 创建 |
| 2 | `8562de6b` | 2026-06-29 16:10 | 前端 `ProbeHealthView.vue` 改用 `req()` 注入 Bearer token（4 个 API 调用），并把迁移文件编号从 312→314, 311→313 |
| 3 | `990a2e39` | 2026-06-29 (晚) | `bg/model_probe.go` 增加 `reconcileHealthyConfirmedBindings()` 调用点（最后一环） |

之后另有一个**非 probe-health** 的提交 `de90c553 fix(audit): 修正代码审计发现的 3 个问题`（2026-06-29 18:26），修的是 prompt injection SQL 的 MySQL INDEX 语法 + JSON 双重编码 + regexp `\u` 转义，与 probe-health 无关。

---

## 📦 修复内容详情

### 1. 数据库层（commit `c9649c96`）

**文件**: `db/migrations/314_probe_health_comprehensive_fix.sql`

**重建的视图 / 函数**:
- `v_model_health_dashboard` — 模型健康度总览
- `v_probe_queue_snapshot` — 探测队列快照
- `v_model_priority_details` — 模型优先级详情
- `v_probe_system_health` — 全局探测系统健康度
- `v_model_availability_timeline` — 模型可用性时间线
- `get_model_state_summary()` — 状态汇总函数

**关键修复点**:
- 不再引用不存在的表 `provider_models` —— 直接从 `model_probe_state` JOIN `credentials` JOIN `providers` 查询
- 根据 `state` 和 `consecutive_failures` 推断 `probe_priority`
- 用 `consecutive_successes / total_attempts` 计算成功率

### 2. 后端反向同步（commit `c9649c96` 创建文件 + `990a2e39` 调用）

**新文件**: `bg/model_probe_reconcile_healthy.go`
```go
func (r *ModelProbeRunner) reconcileHealthyConfirmedBindings(ctx context.Context) {
    // 当 model_probe_state.state = 'healthy_confirmed' 时
    // 恢复 binding.available = TRUE
}
```

**调用点**: `bg/model_probe.go:165`
```go
r.reconcileBrokenConfirmedBindings(timeoutCtx)
r.reconcileHealthyConfirmedBindings(timeoutCtx)  // 新增（990a2e39）
```

修复点：与 `reconcileBrokenConfirmedBindings()` 配对 —— 解决探测恢复后路由仍认为不可用的问题。

### 3. 前端认证修复（commit `8562de6b`）

**文件**: `web/src/views/ProbeHealthView.vue`

**修复前**:
```typescript
// 错误：只有 credentials: 'include'，无 Authorization header
fetch('/api/admin/probe/dashboard', {
  credentials: 'include'
})
```

**修复后**:
```typescript
// 正确：使用统一的 req() 函数，自动注入 Bearer token
import { req } from '../api/_core'
const data = await req('/api/admin/probe/dashboard')
```

修复点：所有 4 个 API 调用改用 `req()` 函数，自动附带 JWT/API Key 认证，复用全站 401→跳登录处理。

---

## 🎯 问题解决对照表

| 问题 | 根因 | 修复 commit | 状态 |
|------|------|------------|------|
| 1. probe-health 页面无数据 | 视图引用不存在的表 `provider_models` | `c9649c96` (迁移 314) | ✅ 已合并 |
| 2. 页面 API 返回 401 | fetch 缺少 `Authorization` header | `8562de6b` | ✅ 已合并 |
| 3. 常用模型误报"无可用凭据" | 缺少 healthy→available 反向同步 | `990a2e39` | ✅ 已合并 |

---

## 📈 部署准备状态

### 代码状态
- ✅ 所有 probe-health 修复已合并到 `main`
- ✅ 编译 + 推送完整（pre-commit checks PASS）
- ✅ 工作区当时已干净（probe-health 相关）

### 待部署内容
1. **数据库迁移**: `db/migrations/314_probe_health_comprehensive_fix.sql`
2. **Go 服务**: `bg/model_probe_reconcile_healthy.go` + `bg/model_probe.go` 调用点
3. **前端**: `web/dist/` 包含 `ProbeHealthView.vue` 认证修复

### 部署命令（参考）
```bash
# 1. 应用数据库迁移
psql $DATABASE_URL -f db/migrations/314_probe_health_comprehensive_fix.sql

# 2. 拉取最新代码
git pull origin main

# 3. 编译部署
go build -o llm-gateway-go ./cmd/gateway
sudo systemctl restart llm-gateway-go

# 4. 用户清除浏览器缓存访问
# https://llmgo.kxpms.cn/probe-health
```

---

## 🎉 审计结论

**probe-health 修复链路完整、已合并、可部署**。3 个 commit 解决了：无数据 / 401 / 反向同步缺失 三大类问题，且都不影响其他服务。

---

## 📎 附: 同日交付的 fp_slot 回归验证工具集（与 probe-health 独立）

> **不属于 probe-health 修复链路**，但由同一次审计/工作顺手产出。

| 文件 | 行数 | 用途 |
|------|------|------|
| `scripts/verify-fpslot-fix.sh` | 约 300 | 端到端复现 "minimax-prod-1 52% 失败率" 场景（20 并发同 sticky session），验证 fp_slot 修复是否生效 |
| `scripts/mocks/llm-mock-upstream/server.py` | 约 175 | 模拟 OpenAI `/v1/chat/completions`（流式+非流式）的 Python mock upstream，可调延迟 + 故障注入 |

**验证流程**: 启动 mock → 改 provider 14 base_url → 重加密 credential 6 → 跑 20 并发 → 数 Redis slot 数。

**已知依赖（仓库外）**:
- `/tmp/fpslot-make-cipher.go` — 用本地 Fernet key 生成 ciphertext 的小工具
- `/tmp/fpslot-burst.py` — 用 Python `threading.Barrier` 实现毫秒级并发突发的 launcher
- `aiohttp` (Python) — mock upstream 依赖

---

**报告生成时间**: 2026-06-29
**状态**: ✅ probe-health 链路完整 / 工具集附注