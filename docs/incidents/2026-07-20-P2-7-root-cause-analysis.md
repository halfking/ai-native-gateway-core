# P2-#7 错误根因分析报告

## 错误是如何引入的？

### 时间线
1. **2024 年初**: credentials 表设计时使用 `status` 列（非 `enabled`）
2. **commit dc2577186** (2026-06): 在 `bg/node_probe.go` 中添加过滤条件时，**错误假设** credentials 表有 `enabled` 列
3. **生产运行**: 错误的 SQL 在生产环境运行数月未被发现
4. **2026-07-20**: 问题爆发，所有 minimax-m3 请求 503

### 引入 commit
```
commit dc2577186
Author: [开发者]
Date:   Mon Jun XX 2026

fix(audit): unify rate-limited status and probe lifecycle guards

+		  AND c.enabled = TRUE AND c.lifecycle IN ('active', 'grace')
+		  AND p.enabled = TRUE AND p.manual_disabled = FALSE
```

### 为什么会犯这个错误？

#### 1. **Schema 不一致的假设**
- `providers` 表**有** `enabled` 列 ✓
- `tool_categories` 表**有** `enabled` 列 ✓  
- `tool_registry` 表**有** `enabled` 列 ✓
- **错误假设**: credentials 表也应该有 `enabled` 列 ✗

**实际**: credentials 表使用 `status` (active/inactive/disabled)

#### 2. **缺少 Schema 验证**
- 代码修改时没有查看实际表结构
- 没有跑完整的集成测试（probe SQL 没被测试覆盖）
- Code review 时审查者也没发现 schema 不匹配

#### 3. **同样的错误出现两次**
- `c.enabled` → 应该是 `c.status`
- `c.lifecycle` → 应该是 `c.lifecycle_status`

两个列名都是"接近但不完全正确"的常见陷阱。

### 为什么这么久才发现？

#### 1. **SQL 错误被"静默"处理**
- node_probe 探测失败时记录为 `endpoint_build` 错误
- 系统**没有**alert/panic，只是标记 credential 不可用
- 错误日志淹没在大量正常日志中

#### 2. **有备用路由掩盖问题**
- minimax-m3 有多个 provider (NVIDIA NIM / 火山 / 普联)
- credential 21 (minimax direct) 失败时，routing 自动切到其他 provider
- **用户没有感知**，直到所有备用 provider 也出问题

#### 3. **指数退避放大影响**
- 第一次失败 → 5 分钟后重试
- 连续失败 → 1 小时 → 8 小时 → 1 天
- `consecutive_failures=5` 时，`next_retry_at` 已经是几天后
- 一旦进入这个状态，**自动恢复几乎不可能**

## 教训与改进建议

### 1. **强制 Schema 验证**
- 所有 SQL 查询在 commit 前必须过 schema lint
- CI 中增加"SQL vs Schema 一致性检查"
- 工具：`sqlc` / `sqlx` / custom linter

### 2. **Probe 测试覆盖**
- node_probe 的 `resolveDirectTarget` 必须有集成测试
- 测试用真实 DB schema，不是 mock
- 失败时测试应该 fail，而非静默

### 3. **更明显的错误信号**
- `endpoint_build` 类错误应触发 alert
- SQL 错误应单独统计（`sql_error_count` metric）
- 连续失败 > 3 次应通知 on-call

### 4. **文档化 Schema**
- 维护 `docs/schema/credentials.md` 列出所有列
- 或者用代码生成工具（`go-migrate docs` / `dbdocs`）
- Code review checklist 增加"SQL 列名核对"

### 5. **更快的失败恢复**
- 指数退避的上限不应超过 1 小时
- 部署新代码后自动清理 `node_probe_state`
- 或者增加"手动触发 re-probe"的 admin API

## 修复措施

### 代码修复
```diff
- AND c.enabled = TRUE AND c.lifecycle IN ('active', 'grace')
+ AND c.status IN ('active', 'cooling', 'degraded')
+ AND c.lifecycle_status = 'active'
```

### 部署
- ✅ 154 (生产): build 1252
- ✅ 245 (测试): build 1254

### 验证
- ✅ minimax-m3 恢复正常
- ✅ 6 个候选可用
- ✅ 无 SQL 错误

## 总结

这是一个典型的 **"假设 vs 现实不符"** 错误：
- 开发者**假设** credentials 表有 `enabled` 列（因为其他表都有）
- **实际上** credentials 表用 `status` 列
- 缺少自动化验证 + 测试覆盖不足 = 错误进入生产
- 备用路由 + 静默失败 = 问题被延迟发现
- 指数退避 = 影响被放大

**修复已完成，服务已恢复正常。**
