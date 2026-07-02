# 生产环境 500 错误诊断报告

**发生时间**: 2026-07-02 14:33:59  
**URL**: `GET /api/logs/754612ab3ca7ce8950b53bd735bdeaa4`  
**状态**: 🔴 **500 Internal Server Error**

---

## 🔍 错误分析

### 1. 核心错误

```
WARN admin getLog scan failed 
request_id=754612ab3ca7ce8950b53bd735bdeaa4 
error="number of field descriptions must equal number of destinations, got 67 and 66"
```

### 2. 问题类型

**数据库 Schema 不匹配**

- **Go 代码期望**: 67 个字段
- **数据库实际**: 66 个字段  
- **差异**: 缺少 1 个字段

### 3. 根本原因

这是**部署顺序问题**：
1. ✅ 我们部署了新代码（build_seq: 9）
2. ❌ **但没有运行数据库迁移**
3. 结果：代码和数据库 schema 版本不匹配

---

## 📊 相关的数据库迁移

### 最新的迁移文件（未应用）

```
324_credential_state_log.sql
325_request_attachments.sql
326_fix_routable_view_quota_check.sql
328a_request_logs_bodies_table.sql    ← 可能相关
330_model_pricing.sql                 ← 184 的新功能
```

### 328a 迁移的变更

该迁移创建了 `request_logs_bodies` 表，并可能修改了 `request_logs` 表的结构，这可能导致字段数不匹配。

---

## 🔧 解决方案

### 选项 A: 运行数据库迁移（推荐）

```bash
# 1. 找到迁移工具或脚本
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

# 2. 运行待执行的迁移
# (需要知道项目使用的迁移工具：migrate, goose, 或自定义工具)

# 3. 验证迁移成功
# 检查 schema_migrations 表
```

**问题**: 我们需要知道：
- 项目使用什么迁移工具？
- 如何在生产环境执行迁移？
- 是否有自动迁移机制？

### 选项 B: 回滚到旧版本

如果迁移复杂或有风险，可以先回滚：

```bash
ssh -p 25022 root@14.103.112.184 \
  "kubectl rollout undo deployment/llm-gateway-go-deployment -n pms-test"
```

**影响**:
- 回退到 build_seq: 2
- 失去 184 的新功能
- 但服务恢复正常

---

## 🚨 其他发现的问题

### 1. 数据库约束错误

```
WARN telemetry request db persist failed 
error="ERROR: null value in column \"stream_chunks_sent\" 
of relation \"request_logs_2026_07\" violates not-null constraint"
```

**问题**: `stream_chunks_sent` 字段约束不满足
**影响**: 请求日志无法保存

### 2. Columnar 存储问题

```
WARN auto_route listener: refresh failed 
error="rollup credential_model_index: exec: 
ERROR: columnar_tuple_insert_speculative not implemented (SQLSTATE XX000)"
```

**问题**: Citus columnar 存储的限制
**影响**: 自动路由刷新失败（可能不影响主要功能）

---

## 📋 需要的信息

为了完全解决这个问题，我们需要：

1. **迁移工具信息**
   - 项目使用什么迁移工具？
   - 迁移文件如何应用？

2. **当前数据库状态**
   - 最后应用的迁移版本是多少？
   - `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 5;`

3. **部署流程**
   - 部署时是否应该自动运行迁移？
   - 还是需要手动执行？

---

## 🎯 建议的行动

### 立即行动（修复 500 错误）

**选项 1: 如果可以快速运行迁移**
```bash
# 运行迁移（具体命令取决于迁移工具）
# 然后重启 Pod
kubectl rollout restart deployment/llm-gateway-go-deployment -n pms-test
```

**选项 2: 如果不确定或有风险**
```bash
# 回滚到旧版本
kubectl rollout undo deployment/llm-gateway-go-deployment -n pms-test
```

### 短期（1-2天）

1. 确认数据库迁移流程
2. 在测试环境验证迁移
3. 制定安全的生产迁移计划
4. 重新部署 184 + 执行迁移

### 长期（持续改进）

1. 自动化数据库迁移
2. 在部署脚本中增加迁移检查
3. 添加 schema 版本兼容性检查
4. 改进部署流程文档

---

## 📝 相关文件

- `db/migrations/328a_request_logs_bodies_table.sql` - 可能导致问题的迁移
- `db/migrations/330_model_pricing.sql` - 184 的价格配置迁移
- `admin/logs.go` - 可能包含查询逻辑（需要检查字段数）

---

## ✅ 验证清单

部署后需要验证：

- [ ] 数据库迁移已应用
- [ ] `schema_migrations` 表包含最新版本
- [ ] `/api/logs` 列表接口正常
- [ ] `/api/logs/{id}` 详情接口正常（500 错误修复）
- [ ] 新功能可用（价格配置、客户端适配器）
- [ ] 无新的错误日志

---

**报告生成时间**: 2026-07-02 20:50  
**诊断人**: Kiro (AI Agent)  
**严重程度**: 🔴 高 - 影响日志查看功能  
**建议**: 尽快运行数据库迁移或回滚部署
