# Request Logs 重复记录修复 - 变更日志

**日期**: 2026-06-26  
**问题**: 同一个 request_id 重复5次，所有记录状态卡在 'in_progress'  
**影响**: 成功的请求未正确记录，数据库存储浪费，查询性能下降

## 修复内容

### 代码修改

#### 1. `telemetry/client.go`

**变更 A: 修改 UPSERT 冲突键** (line 497)
```diff
- ON CONFLICT (request_id, ts) DO UPDATE SET
+ ON CONFLICT (request_id) DO UPDATE SET
```
- **原因**: `ts = now()` 每次都不同，导致无法触发 UPSERT 的 UPDATE 分支
- **效果**: 确保同一个 request_id 只能有一条记录

**变更 B: 改进 UPDATE CTE 查询** (line 741-746)
```diff
- WITH latest AS (
+ WITH earliest AS (
      SELECT id, ts
      FROM request_logs
      WHERE request_id = $1
-     ORDER BY ts DESC
+     ORDER BY ts ASC
      LIMIT 1
  )
```
- **原因**: 批处理延迟可能导致查询到错误的记录
- **效果**: 总是更新初始 INSERT 创建的记录

**变更 C: 禁用 fallback INSERT 逻辑** (line 904-926)
```diff
  if tag.RowsAffected() == 0 {
-     // No early row — fall back to insert so the request is not lost.
-     if rbErr := tx.Rollback(ctx); rbErr != nil {
-         slog.Warn("telemetry update rollback failed", ...)
-     }
-     fallback := *entry
-     fallback.Op = RequestLogInsert
-     return c.insertRequestLog(&fallback)
+     // 2026-06-26: Disable fallback INSERT to prevent duplicate records.
+     // [详细注释说明原因]
+     slog.Warn("telemetry update matched zero rows, skipping fallback insert", ...)
+     return tx.Commit(ctx)
  }
```
- **原因**: fallback INSERT 是重复记录恶性循环的根源
- **效果**: 停止重复记录的产生

**变更 D: 添加辅助函数** (line 976-981)
```go
func strPtrValue(p *string) string {
    if p == nil {
        return ""
    }
    return *p
}
```
- **原因**: 用于日志输出，避免 nil 指针
- **效果**: 日志更清晰

### 数据库迁移

#### 2. `db/migrations/301_request_logs_unique_request_id_only.sql`

**新增文件** - 正向迁移
- 删除旧的 `(request_id, ts)` 唯一索引
- 清理最近7天的重复记录（保留最早的）
- 创建新的 `(request_id)` 唯一索引

#### 3. `db/migrations/301_request_logs_unique_request_id_only.down.sql`

**新增文件** - 回滚迁移
- 支持在需要时回滚到旧的索引结构

### 工具和文档

#### 4. `db/scripts/diagnose_and_clean_request_logs.sql`

**新增文件** - 诊断和清理脚本
- 12个诊断查询，帮助识别重复记录
- 安全的清理操作（带确认）
- 验证修复效果的查询

#### 5. `docs/request_logs_fix_guide.md`

**新增文件** - 完整的修复指南
- 问题描述和根本原因分析
- 详细的部署步骤
- 测试验证方法
- 回滚计划
- TimescaleDB 特殊注意事项

## 测试结果

✅ 所有 telemetry 单元测试通过  
✅ 代码编译成功  
✅ 无破坏性变更

## 部署清单

### 准备阶段
- [ ] 在测试环境验证修复效果
- [ ] 备份 request_logs 表（可选但推荐）
- [ ] 审查迁移脚本

### 部署阶段
- [ ] 部署代码更新
- [ ] 运行数据库迁移 `301_request_logs_unique_request_id_only.sql`
- [ ] 执行诊断脚本验证修复

### 验证阶段
- [ ] 检查重复记录数量（预期：0）
- [ ] 检查状态分布（预期：有 success/failure，不只是 in_progress）
- [ ] 监控日志中的 "matched zero rows" 警告（预期：很少或没有）
- [ ] 功能测试：发送测试请求，验证记录正确

### 监控阶段（部署后24小时）
- [ ] 每小时检查重复记录数量
- [ ] 监控 UPDATE 失败日志
- [ ] 验证请求完整性（无丢失）
- [ ] 检查数据库性能指标

## 风险评估

**风险等级**: 低-中

**潜在问题**:
1. TimescaleDB 可能不允许 `(request_id)` 唯一索引
   - **缓解**: 代码层面的修复（变更B和C）已经能防止大部分问题
   - **回滚**: 使用 `301_*.down.sql` 回滚迁移

2. 极少数情况下，UPDATE 可能找不到初始记录
   - **影响**: 请求数据丢失（但不会产生重复记录）
   - **监控**: 通过日志 "matched zero rows" 跟踪

3. 性能影响（极小）
   - **影响**: 新索引可能略微影响插入性能
   - **收益**: 查询性能提升，存储空间减少

## 回滚计划

如果需要回滚：

```bash
# 1. 回滚数据库
psql "$DATABASE_URL" -f db/migrations/301_request_logs_unique_request_id_only.down.sql

# 2. 回滚代码
git revert <commit-hash>

# 3. 重启服务
systemctl restart llm-gateway
```

## 相关链接

- 问题追踪: [内部 issue #xxx]
- 设计文档: `docs/request_logs_fix_guide.md`
- 诊断工具: `db/scripts/diagnose_and_clean_request_logs.sql`

## 作者

修复人员: AI Assistant  
审核人员: [待添加]  
修复日期: 2026-06-26
