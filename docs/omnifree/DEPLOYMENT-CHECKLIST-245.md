# OmniFree 245 环境部署检查清单

**环境**: 245 测试环境  
**执行日期**: 待执行  
**执行人**: _____________

---

## 部署前检查

### 1. 环境信息确认

- [ ] 数据库地址: `172.16.2.245:5432` （或其他）
- [ ] 数据库名称: `llm_gateway` （或专用测试库）
- [ ] 数据库用户: `kxuser` （或专用测试用户）
- [ ] 数据库密码: 已准备（不要写在此处）
- [ ] 是否为测试环境: 是 / 否

### 2. 权限确认

- [ ] 用户有 CREATE TABLE 权限
- [ ] 用户有 CREATE INDEX 权限
- [ ] 用户有 ALTER TABLE 权限
- [ ] 用户有 CREATE POLICY 权限
- [ ] 用户有 CREATE TRIGGER 权限

### 3. 依赖检查

- [ ] psql 已安装: `psql --version`
- [ ] Go 已安装: `go version`
- [ ] 项目代码已更新: `git pull`
- [ ] 在项目根目录: `ls go.mod`

### 4. 安全确认

- [ ] 已轮换泄露的凭据（如果是首次）
- [ ] 不在生产数据库执行
- [ ] 密码不会写入脚本或日志
- [ ] 有数据库备份（如果是重要环境）

---

## 部署执行

### 步骤 1: 设置环境变量

```bash
# 替换 YOUR_PASSWORD 为实际密码
export OMNIFREE_DB_URL='postgres://kxuser:YOUR_PASSWORD@172.16.2.245:5432/llm_gateway?sslmode=disable'
```

**执行时间**: __________  
**结果**: ☐ 成功 ☐ 失败  
**备注**: _________________

### 步骤 2: 测试数据库连接

```bash
psql "$OMNIFREE_DB_URL" -c "SELECT version();"
```

**执行时间**: __________  
**结果**: ☐ 成功 ☐ 失败  
**PostgreSQL 版本**: _________________

### 步骤 3: 执行部署脚本

```bash
./scripts/omnifree/deploy-245-test.sh
```

**执行时间**: __________  
**结果**: ☐ 成功 ☐ 失败  
**输出摘要**:
```
表创建: ☐ 4/4
资源数: ☐ 15
模板数: ☐ 6
Keyless: ☐ 3
```

---

## 部署后验证

### 1. 数据完整性

```bash
psql "$OMNIFREE_DB_URL" -c "
SELECT 
  (SELECT COUNT(*) FROM free_resource_catalog) AS resources,
  (SELECT COUNT(*) FROM auto_combo_templates) AS templates,
  (SELECT COUNT(*) FROM keyless_providers) AS keyless;
"
```

**期望**: 15, 6, 3  
**实际**: _____, _____, _____  
**结果**: ☐ 通过 ☐ 失败

### 2. 配额总量

```bash
psql "$OMNIFREE_DB_URL" -c "
SELECT ROUND(SUM(monthly_tokens)::numeric / 1000000000, 2) AS monthly_gb
FROM free_resource_catalog 
WHERE enabled = TRUE;
"
```

**期望**: ~1.18 GB  
**实际**: _____ GB  
**结果**: ☐ 通过 ☐ 失败

### 3. RLS 策略

```bash
psql "$OMNIFREE_DB_URL" -c "
SELECT tablename, policyname 
FROM pg_policies 
WHERE tablename LIKE '%free%' OR tablename LIKE '%combo%' OR tablename LIKE '%keyless%';
"
```

**期望**: 至少 4 个 policy  
**实际**: _____ 个  
**结果**: ☐ 通过 ☐ 失败

### 4. 查询性能

```bash
time psql "$OMNIFREE_DB_URL" -c "
SELECT * FROM free_resource_catalog 
WHERE enabled = TRUE AND tenant_id = 'default'
LIMIT 10;
"
```

**期望**: < 100ms  
**实际**: _____ ms  
**结果**: ☐ 通过 ☐ 失败

### 5. 租户隔离测试

```bash
psql "$OMNIFREE_DB_URL" -c "
SET app.current_tenant = 'tenant-a';
SELECT COUNT(*) FROM free_resource_catalog;
"
```

**期望**: 0 或 tenant-a 的数据  
**实际**: _____  
**结果**: ☐ 通过 ☐ 失败

---

## 问题记录

### 遇到的问题

1. **问题描述**: _____________________________
   - **错误信息**: _____________________________
   - **解决方案**: _____________________________
   - **状态**: ☐ 已解决 ☐ 待解决

2. **问题描述**: _____________________________
   - **错误信息**: _____________________________
   - **解决方案**: _____________________________
   - **状态**: ☐ 已解决 ☐ 待解决

---

## 回滚计划（如需要）

### 回滚脚本

```bash
# 执行回滚
psql "$OMNIFREE_DB_URL" -v ON_ERROR_STOP=1 \
  -f sql/migrations/075-omnifree-schema.down.sql
```

**回滚时间**: __________  
**结果**: ☐ 成功 ☐ 失败

---

## 部署结论

### 总体评估

- [ ] ✅ 部署成功，所有验证通过
- [ ] ⚠️ 部署成功，但有警告
- [ ] ❌ 部署失败，需要回滚

### 数据层评分

- 迁移执行: ___/10
- seed 导入: ___/10
- RLS 隔离: ___/10
- 查询性能: ___/10
- **总分**: ___/10

### 下一步行动

- [ ] 数据层验证完成，可开始应用层集成
- [ ] 发现问题，需要修复后重新部署
- [ ] 需要进一步测试和验证

### 备注

_____________________________________________________
_____________________________________________________
_____________________________________________________

---

**检查清单完成时间**: __________  
**签名**: __________
