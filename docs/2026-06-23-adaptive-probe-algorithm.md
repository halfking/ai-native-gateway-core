# 自适应探测调度算法（2026-06-23）

## 📋 问题背景

用户报告：
> "我们需要对于瞬时尖峰这种情况有处理的方案...我们的关键问题在于峰值过后没有及时恢复。"

### minimax-m3 06-23 事件分析

- **时间窗口**: 07:56 - 08:08（5 分钟）
- **失败**: 27 次 `no_candidates`（90% 失败率）
- **根本原因**: 瞬时流量尖峰（30 req/分钟）导致 fp_slot 竞争，30 秒后实际恢复
- **延迟恢复**: 因为探测 backoff 是固定的 5 分钟，导致恢复延迟 5 分钟

### 当前算法的问题

| 缺陷 | 影响 |
|------|------|
| 固定 backoff（30s/2m/5m/15m） | 无论失败多久都用相同间隔 |
| 没有自适应频率 | 早期失败和近期失败探测频率相同 |
| 被动信号未利用 | 只有周期性探测，请求失败信号被忽略 |
| 没有尖峰快速响应 | 依赖 60s cooldown，无法快速恢复 |

## 🎯 新算法设计

### 核心思想

**"距当前时间越近的失败，测试频度越高；时间越早的失败，测试频度越低"**

- **新鲜失败**（< 5 min）：高频探测，快速确认是否真的故障
- **老失败**（> 1 hour）：低频探测，避免浪费资源
- **无失败**：由请求过程自然监测，不需要主动探测

### Backoff v2 算法

```
consecutive_failures  age_since_last_failure  next_retry_interval
────────────────────  ────────────────────────  ───────────────────
0 (healthy)            any                      2 hours (watchdog)
1                      < 5 min                  1 minute
1                      5-30 min                 3 minutes
1                      30-60 min                10 minutes
1                      > 60 min                 30 minutes
2                      < 5 min                  2 minutes
2                      5-30 min                 5 minutes
2                      30-60 min                15 minutes
2                      > 60 min                 45 minutes
3+ (broken path)       any                      60 minutes
```

### 被动失败加速（Passive Boost）

当 `candidate_failure_logs` 中 5 分钟内有：
- **3+ 失败** → `next_retry_at = NOW() + 30s`
- **2 失败** → `next_retry_at = NOW() + 1m`
- **< 2 失败** → 不调整

这是修复 minimax-m3 事件的关键：5 分钟内 3 次失败，下一次探测从 5 分钟后提前到 30 秒后。

### 优先级排序

新的 `cycle()` 查询 `v_adaptive_probe_targets` 视图，按以下优先级排序：

1. **最近尝试时间最早**（`last_attempt_at ASC`）- 等待最久的优先
2. **连续失败最多**（`consecutive_failures DESC`）- 接近 broken 的优先
3. **binding id** - 稳定排序

## 📊 数据流

```
请求失败
  ↓
candidate_failure_logs (写入)
  ↓
[每 10 分钟] model_probe_runner.cycle()
  ↓
1. reconcileBrokenConfirmedBindings() (幂等修复)
2. applyPassiveBoosts() (被动加速) ← 新增
3. SELECT FROM v_adaptive_probe_targets ORDER BY 优先级
4. 对每个目标执行 probeModel()
5. applyResult() → model_probe_state (with backoff_v2)
```

## 🔧 实施内容

### 新增文件

1. **`db/migrations/038_adaptive_probe_scheduling.sql`**
   - `model_probe_backoff_v2()` 函数
   - `model_probe_passive_boost()` 函数
   - `v_adaptive_probe_targets` 视图

2. **`tests/038_adaptive_probe_test.sql`**
   - 验证 backoff_v2 所有分支
   - 验证 passive_boost 行为
   - 清理测试数据

### 修改文件

1. **`bg/model_probe.go`**:
   - `cycle()`: 调用 `applyPassiveBoosts()`
   - `cycle()`: `ORDER BY` 改为多级优先级
   - `applyResult()`: 使用 `model_probe_backoff_v2($5, NOW())`
   - 新增 `applyPassiveBoosts()` 方法

## 🎯 关键改进点

### 1. 年龄感知（Age-Aware）

老失败用长间隔，新失败用短间隔。避免对"几乎已经恢复"的失败做无用探测。

### 2. 被动信号利用

请求失败（即使不是该凭据引起的）写入 `candidate_failure_logs`。runner 利用这些信号快速响应。

### 3. 智能优先级排序

不再固定 `ORDER BY next_retry_at`，而是：
- 等待最久的优先（避免饥饿）
- 接近 broken 的优先（避免彻底失败）
- 稳定排序（避免抖动）

### 4. 三层防护

- **Layer 1**: 周期性探测（每 10 分钟）
- **Layer 2**: 被动失败加速（5 分钟窗口）
- **Layer 3**: 60 秒 cooldown（credential_recovery.go 已存在）

## 📈 预期效果

| 场景 | 旧行为 | 新行为 |
|------|--------|--------|
| 单次瞬时失败 | 5 分钟后探测 | 1 分钟后探测（如果失败新鲜） |
| 5 分钟前失败 | 5 分钟后探测 | 30 秒后探测（被动加速） |
| 1 小时前失败 | 5 分钟后探测 | 30 分钟后探测（age-aware） |
| 健康凭据 | 不探测（除非手动） | 2 小时 watchdog |

## 🧪 验证测试

### SQL 单元测试

```bash
psql -f tests/038_adaptive_probe_test.sql
```

测试覆盖：
- ✅ 0 失败 → 2h watchdog（任何年龄）
- ✅ 1 失败 → 1m → 3m → 10m → 30m（按年龄递增）
- ✅ 2 失败 → 2m → 5m → 15m → 45m（按年龄递增）
- ✅ 3+ 失败 → 60m（still recovering）
- ✅ NULL last_attempt_at → 默认为老（>60min 路径）
- ✅ passive_boost 函数：2 次失败 → +1m, 3+ 次失败 → +30s

### Go 单元测试（待补充）

```go
// bg/model_probe_test.go
func TestApplyPassiveBoosts_RecentFailures(t *testing.T) {
    // 模拟 candidate_failure_logs 中的失败记录
    // 调用 applyPassiveBoosts()
    // 验证 next_retry_at 被正确调整
}
```

## 🚀 部署建议

### 1. 灰度上线

```bash
# 1. 先在 71 部署（用户流量较小）
./scripts/deploy-llm-gateway-go-71.sh

# 2. 观察 24 小时
# - 监控 candidate_failure_logs 中的失败模式
# - 检查 model_probe_runs 表的 probe 频率变化

# 3. 确认无问题后部署 184
./scripts/deploy-llm-gateway-go-184.sh
```

### 2. 监控指标

建议添加：
- `adaptive_probe_boost_total{trigger="3_failures_5min"}`
- `adaptive_probe_boost_total{trigger="2_failures_5min"}`
- `model_probe_run_duration_seconds{triggered_by="passive_boost"}`

### 3. 回滚方案

如果发现问题，立即回滚：

```bash
# 禁用 adaptive 算法
# ALTER FUNCTION model_probe_backoff_v2 RENAME TO model_probe_backoff_v2_disabled;

# 或者恢复使用旧 backoff
UPDATE model_probe_state
SET next_retry_at = NOW() + model_probe_backoff(consecutive_failures)
WHERE next_retry_at > NOW();

# Go 代码回滚到 commit 0d40478a
git revert HEAD --no-edit
```

## 📚 相关文档

- **完整诊断报告**: `docs/2026-06-23-minimax-m3-no-candidates-diagnostic.md`
- **SQL Migration**: `db/migrations/038_adaptive_probe_scheduling.sql`
- **测试**: `tests/038_adaptive_probe_test.sql`
- **源码**: `bg/model_probe.go`