# 211 再追平审计报告（2026-11-28）

## 现状

| 项 | 值 |
|---|---|
| 211 现役 | `2.6.2-7b938ced-20261127-211` build_seq=3321（2026-11-27 晚追平产物） |
| main 当前 tip | `89c0465f4` (2026-11-28 03:10) |
| 落后笔数 | **145 笔** |
| 落后时长 | ~5 小时（2026-11-27 22:03 → 2026-11-28 03:10） |
| 变更规模 | 323 files changed, 21407 insertions(+), 14158 deletions(-) |
| 现役健康度 | ✅ 近 30 分钟零 ERROR 日志，readyz 200 |

---

## 增量迁移审计（22 个文件）

### 1. 高影响迁移（2 组）

#### 1.1 `1005_user_roles_array.sql` — 用户角色多值化（L140 配额制 P1-b）

**变更**：
```sql
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS roles TEXT[] NOT NULL DEFAULT '{}';
```

**幂等性**：✅ `IF NOT EXISTS` 保护，重放安全  
**性能**：空库 <0.05s（元数据级操作）  
**向后兼容**：✅ roles 空数组时有效角色集回落 `[role]` 单值（存量行为不变）  
**风险等级**：🟡 **中等**（新列默认空数组，需应用层同步写入逻辑；但有回落机制）

#### 1.2 `983j-r` 系列（10 个迁移）— request_metrics 读侧加速视图

**983j**: `request_metrics_973_repair` — 973 投影修复  
**983k**: `request_metrics_daily_summary` — 日汇总视图  
**983l**: `request_metrics_daily_board` — 日看板视图  
**983m**: `request_metrics_daily_history` — 日历史视图  
**983n**: `request_metrics_daily_tenant_history` — 租户日历史  
**983o**: `request_metrics_daily_history_details` — 历史明细  
**983p**: `request_metrics_history_pages` — 历史分页  
**983q**: `request_metrics_board_hours` — 看板小时粒度  
**983r**: `request_metrics_board_hour_distributions` — 小时分布

**配套 Go 层**：
- `11dc125c4` feat(stats): integrate V2 projection for the 211 release candidate
- `8928e745e` fix(metrics): reuse the released 973 projection for V2
- `48c28fb20` merge: integrate real-PG projection verification and savepoint fix

**幂等性**：✅ `CREATE OR REPLACE VIEW`，重放安全  
**性能影响**：只读视图，不影响写入路径  
**风险等级**：🟢 **低**（增量功能，现役不调用 → 无报错证明；追平后启用）

---

### 2. 低影响迁移（其他）

- `1000_billing_status_and_credit_limit.sql` 变更（diff）：已在 211 现役（1000 是 211 追平时已应用的迁移）
- `migration_745_test.go` 变更：测试文件，不影响运行时

---

## 核心功能变更

| 提交 | 功能 | 影响面 |
|---|---|---|
| `9f87ce96c` | feat(quota): L140 配额制 P1 半边交付 — /tenant/usage 重做 + users.role 多角色化 | users 表（1005 迁移）+ API 路由 |
| `be954b318` | feat(web): 非管理员菜单重构收口 | 前端 47 文件 |
| `085f88b4f` | feat(plugins): resource-limiter v1.3 资源限制监控 | plugins 子系统 |
| `bcb216dad` ~ `a98f4c56c` | feat(metrics): 983j-r 读侧加速视图族 | request_metrics 读路径（新增，不替换现有） |

---

## 风险评估

### 🟢 低风险项（可安全追平）

1. **983j-r 读侧视图族**：
   - 只读增量功能，现役不调用（零报错证明）
   - 有配套 Go 层验证通过
   - 幂等范式（CREATE OR REPLACE VIEW）

2. **plugins 灰度系列**：47 文件变更属 plugins 子系统隔离域，主链路不受影响

3. **前端变更**：菜单重构等 UI 层改动，后端 API 兼容

### 🟡 中等风险项（需验证）

1. **1005 users.roles 角色化**：
   - 新列有向后兼容设计（空数组回落 role 单值）
   - 但需确认应用层是否已部署同步写入逻辑：
     - 若 7b938ced@3321 的代码**未**包含 roles 数组写入 → 追平后新建用户的 roles 恒为空数组 → 回落机制生效 ✅
     - 若 89c0465f4 的代码**已**包含 roles 数组写入 → 需同步部署代码 ⚠️

**验证方法**：
```bash
git log 7b938ced5..89c0465f4 --all-match --grep="roles" -- internal/
```

---

## 追平建议

### 方案 A：立即追平（推荐 ⚠️ 需先验证 roles 写入逻辑）

**前提条件**：
1. ✅ 确认 89c0465f4 的 internal/ 层是否有 `users.roles` 数组写入逻辑
2. ✅ 若有，需部署代码与迁移同步进行（蓝绿部署保证）

**执行步骤**：
1. 只读取证（连接 211，读取现役状态 + 迁移面）
2. 本地隔离树验证（checkout 89c0465f4，test-gate 全绿）
3. 预核 1005/983j-r 在 211 库的幂等性（`\d users`，查 roles 列是否已存在）
4. 蓝绿部署 + 三判据验收
5. 交付登记

**预估时长**：1.5 ~ 2 小时（含验证窗口）

### 方案 B：等待下一窗口（保守）

**适用场景**：
- 若 roles 写入逻辑验证不通过（代码未就绪）
- 或用户偏好低峰部署（深夜 02:00-04:00）

**下一窗口**：2026-11-28 深夜或 2026-11-29 凌晨，届时 main 可能再前进 20-50 笔

---

## 验证清单（方案 A 执行前）

- [ ] 验证 89c0465f4 是否包含 `users.roles` 数组写入逻辑
- [ ] 本地 test-gate 全绿（尤其 deploygate + dangling 门家族）
- [ ] 211 凭据文件就绪（/root/.llm-gateway-211.env）
- [ ] 部署库 SSOT 就绪（/root/workspace/ai-native-tools/deploy/deploy-lib）
- [ ] DD_TEST_GO_IMAGE=golang:1.27.4-alpine-bash 镜像就绪
- [ ] 并行车无活跃部署锁（/var/lib/llm-gateway-go/deploy.lock）

---

## 附录：迁移文件清单

```
sql/migrations/startup/1000_billing_status_and_credit_limit.sql (变更)
sql/migrations/startup/1005_user_roles_array.down.sql (新增)
sql/migrations/startup/1005_user_roles_array.sql (新增)
sql/migrations/startup/983j_request_metrics_973_repair.down.sql (新增)
sql/migrations/startup/983j_request_metrics_973_repair.sql (新增)
sql/migrations/startup/983k_request_metrics_daily_summary.down.sql (新增)
sql/migrations/startup/983k_request_metrics_daily_summary.sql (新增)
sql/migrations/startup/983l_request_metrics_daily_board.down.sql (新增)
sql/migrations/startup/983l_request_metrics_daily_board.sql (新增)
sql/migrations/startup/983m_request_metrics_daily_history.down.sql (新增)
sql/migrations/startup/983m_request_metrics_daily_history.sql (新增)
sql/migrations/startup/983n_request_metrics_daily_tenant_history.down.sql (新增)
sql/migrations/startup/983n_request_metrics_daily_tenant_history.sql (新增)
sql/migrations/startup/983o_request_metrics_daily_history_details.down.sql (新增)
sql/migrations/startup/983o_request_metrics_daily_history_details.sql (新增)
sql/migrations/startup/983p_request_metrics_history_pages.down.sql (新增)
sql/migrations/startup/983p_request_metrics_history_pages.sql (新增)
sql/migrations/startup/983q_request_metrics_board_hours.down.sql (新增)
sql/migrations/startup/983q_request_metrics_board_hours.sql (新增)
sql/migrations/startup/983r_request_metrics_board_hour_distributions.down.sql (新增)
sql/migrations/startup/983r_request_metrics_board_hour_distributions.sql (新增)
sql/migrations/startup/migration_745_test.go (变更)
```

---

**生成时间**：2026-11-28 03:30  
**审计对象**：7b938ced5 (211 现役) → 89c0465f4 (main tip)  
**审计结论**：🟡 **中等风险，建议先验证 roles 写入逻辑后再决定追平时机**
