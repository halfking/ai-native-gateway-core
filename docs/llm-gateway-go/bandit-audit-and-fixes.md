# Bandit Integration 审计报告与修正方案

## 审计日期：2026-06-26

## 🔴 严重问题（Critical Issues）

### 问题 1：数据库表名不匹配 ⚠️

**现状：**
- Migration 文件操作的是 `credentials` 表
- Flusher 更新的是 `api_keys` 表

**位置：**
- `deploy/sql/migrations/033_bandit_scoring.sql`: `ALTER TABLE credentials ADD COLUMN...`
- `domains/credential/flusher.go:135`: `UPDATE api_keys SET...`

**影响：**
- 🔴 **Flusher 写入会失败**（字段不存在）
- 🔴 **Migration 不会生效**（表名错误）

**修复方案：**
需要确认正确的表名。检查现有代码：
```bash
grep -r "FROM api_keys\|FROM credentials" --include="*.go" | head -5
```

根据 llm-gateway-go 的实际表名统一修改。


### 问题 2：缺少从数据库加载初始状态的功能 ⚠️

**现状：**
- `BanditScorer` 只有内存状态（`scores map[string]*BanditScore`）
- Main.go 启动时创建空的 `NewBanditScorer()`
- 每次重启服务，所有历史学习数据丢失

**影响：**
- 🟡 服务重启后从零开始学习（冷启动问题）
- 🟡 无法利用历史数据优化选择

**修复方案：**
添加 `LoadFromDB(db *pgxpool.Pool) error` 方法到 BanditScorer：
```go
func (b *BanditScorer) LoadFromDB(ctx context.Context, db *pgxpool.Pool) error {
    rows, err := db.Query(ctx, `
        SELECT id, bandit_alpha, bandit_beta, 
               bandit_success_count, bandit_failure_count, bandit_429_count,
               bandit_total_latency_ms, penalty_429_accumulated, penalty_429_last_at
        FROM api_keys 
        WHERE enabled = true AND status = 'active'
    `)
    // ... populate b.scores map
}
```

在 main.go 中调用：
```go
banditScorer := credential.NewBanditScorer()
if err := banditScorer.LoadFromDB(context.Background(), dbConn.Pool()); err != nil {
    slog.Warn("bandit: failed to load state from DB", "error", err)
}
```


### 问题 3：Migration 字段与 Flusher 字段不完全匹配 ⚠️

**现状对比：**

| Migration (credentials 表) | Flusher (api_keys 表) | 状态 |
|---------------------------|----------------------|------|
| `bandit_alpha` | `bandit_alpha` | ✅ |
| `bandit_beta` | `bandit_beta` | ✅ |
| `success_requests` | `bandit_success_count` | ❌ 名称不同 |
| `failure_requests` | `bandit_failure_count` | ❌ 名称不同 |
| `rate_limit_hits` | `bandit_429_count` | ❌ 名称不同 |
| `total_latency_ms` | `bandit_total_latency_ms` | ❌ 名称不同 |
| (无) | `bandit_avg_latency_ms` | ❌ Migration 缺失 |
| `rate_limit_penalty` | `penalty_429_accumulated` | ❌ 名称不同 |
| `last_rate_limit_hit` | `penalty_429_last_at` | ❌ 名称不同 |
| `last_scored_at` | `last_scored_at` | ✅ |

**修复方案：**
统一字段命名，推荐使用 Flusher 中的命名（更明确）：
- `bandit_success_count`（而非 `success_requests`）
- `bandit_failure_count`（而非 `failure_requests`）
- `bandit_429_count`（而非 `rate_limit_hits`）
- `bandit_total_latency_ms`（保持前缀一致）
- `bandit_avg_latency_ms`（新增）
- `penalty_429_accumulated`（而非 `rate_limit_penalty`）
- `penalty_429_last_at`（而非 `last_rate_limit_hit`）


## 🟡 中等问题（Medium Issues）

### 问题 4：Main.go 中 defer Stop() 的作用域问题

**现状：**
```go
if providerClient.Enabled() {
    // ...
    banditFlusher.Start()
    defer banditFlusher.Stop()  // ← 在 if 块内
    // ...
}
```

**影响：**
- defer 在 if 块结束时执行，而非 main 函数结束时
- 可能导致 flusher 过早停止

**修复方案：**
将 flusher 提升为包级变量，在 main 函数最外层 defer：
```go
var banditFlusher *credential.BanditFlusher

func main() {
    defer func() {
        if banditFlusher != nil {
            banditFlusher.Stop()
        }
    }()
    // ... 初始化逻辑
}
```


### 问题 5：缺少 Bandit 禁用开关

**现状：**
- Bandit 在有数据库时自动启用
- 无环境变量控制

**建议：**
添加环境变量 `LLM_GATEWAY_ENABLE_BANDIT_SCORING=true/false`，默认 false（灰度发布）


## 🟢 轻微问题（Minor Issues）

### 问题 6：缺少 Observability

**建议添加：**
- Prometheus metrics: `bandit_score_distribution`, `bandit_flush_duration_seconds`
- 日志：每次 flush 记录更新的凭据数量和成功/失败率


### 问题 7：测试覆盖不足

**当前测试：**
- ✅ BanditScorer 单元测试
- ✅ Scoring 单元测试
- ✅ Flusher 单元测试（需要 DB，会 skip）

**缺失：**
- ❌ 端到端集成测试（真实请求 → 记录 → flush → 验证 DB）
- ❌ Router.banditOrder() 单元测试
- ❌ 冷启动场景测试（LoadFromDB）


## 📋 修正后的实施计划

### Phase 1.1 - 修复关键问题（高优先级）

1. **确认并统一表名**
   - [ ] 检查实际数据库表名（api_keys 还是 credentials？）
   - [ ] 修改 Migration 或 Flusher，统一表名
   - [ ] 统一字段命名（推荐 `bandit_*` 前缀）

2. **实现状态加载**
   - [ ] 添加 `BanditScorer.LoadFromDB()` 方法
   - [ ] 在 main.go 启动时调用
   - [ ] 添加加载失败的降级逻辑

3. **修复 defer 作用域**
   - [ ] 将 banditFlusher 提升为包级变量
   - [ ] 在 main() 最外层 defer Stop()

4. **添加禁用开关**
   - [ ] 添加 `LLM_GATEWAY_ENABLE_BANDIT_SCORING` 环境变量
   - [ ] 默认 false，逐步灰度

### Phase 1.2 - 完善功能（中优先级）

5. **添加 Observability**
   - [ ] Prometheus metrics
   - [ ] 结构化日志

6. **集成测试**
   - [ ] 端到端测试（本地 DB）
   - [ ] 冷启动测试

### Phase 1.3 - 部署验证（最终）

7. **部署前检查**
   - [ ] 运行 Migration（干跑模式）
   - [ ] 检查现有 api_keys 表结构
   - [ ] 备份生产数据库

8. **金丝雀部署**
   - [ ] 单个实例启用 Bandit
   - [ ] 监控 24 小时
   - [ ] 逐步扩展到全量


## 🎯 修正后的 TODO（按优先级）

### P0 - 阻塞部署（必须修复）
1. ✅ 统一表名（api_keys vs credentials）
2. ✅ 统一字段命名（Migration 与 Flusher 对齐）
3. ✅ 实现 LoadFromDB（避免冷启动）

### P1 - 生产必需
4. ✅ 修复 defer 作用域
5. ✅ 添加禁用开关（灰度发布）
6. ✅ 端到端集成测试

### P2 - 优化增强
7. ⏸️ Observability（metrics + logs）
8. ⏸️ Phase 2-5 功能（cooldown, quota, catalog, health worker）


## 🔧 下一步行动

**立即执行（按顺序）：**

1. 确认表名：
   ```bash
   # 检查现有查询使用哪个表名
   grep -r "FROM api_keys\|UPDATE api_keys" services/llm-gateway-go --include="*.go" | wc -l
   grep -r "FROM credentials\|UPDATE credentials" services/llm-gateway-go --include="*.go" | wc -l
   ```

2. 修改 Migration 或 Flusher，统一到正确表名

3. 实现 `LoadFromDB()` 方法

4. 修复 defer 作用域问题

5. 添加环境变量开关

6. 编写集成测试验证完整流程


---

**审计人：** AI Assistant  
**审计时间：** 2026-06-26  
**严重问题数：** 3 个（P0 阻塞）  
**中等问题数：** 2 个（P1 生产必需）  
**轻微问题数：** 2 个（P2 优化）  
**建议：** 暂停部署，先修复 P0 问题再继续
