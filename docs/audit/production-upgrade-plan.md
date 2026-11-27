# 生产环境版本追平计划

> 生成时间：2026-11-27 19:40  
> 目标：追平211/245/154环境到最新main  
> 策略：分优先级、分批次执行

---

## 📊 当前状态概览

### 环境落后情况

| 环境 | 落后笔数 | 相对SHA | 优先级 | 建议时间线 |
|------|----------|---------|--------|-----------|
| **211** | 6笔 | origin/main@7894727bcb | 🔴 高 | 本月内 |
| **245** | 86笔 | 同上 | 🟢 低 | 1-2个月 |
| **154** | 88笔 | 同上 | 🟢 低 | 1-2个月 |

> **注意**：落后数是快照量，会随main前进而增大。建议尽快启动。

### 新增功能概览（近期main）

- ✅ 计费系统1000-1002迁移（五点已同步）
- ✅ Telemetry服务路由结算修复
- ✅ Casdoor E2E测试基础设施
- ✅ 492渠道商车道迁移（W4.3需要）
- ✅ 多项门禁收口和P0/P1修复

---

## 🎯 211环境追平计划（优先级：高）

### 基本信息

**环境**: 211生产库 (acc-pg17/llm_gateway)  
**落后**: 6笔提交  
**预估工时**: 2-3小时  
**建议窗口**: 本月内低峰时段

### 追平内容

#### 待应用提交列表

需要从main获取准确的6笔提交列表：

```bash
# 获取211当前版本
ssh 211-host "cd /path/to/deploy && git rev-parse HEAD"

# 获取待追平提交
git log <211-current-sha>..origin/main --oneline | head -6
```

#### 关键变更预览

基于已知信息，可能包含：

1. **492迁移**（新增）
   - 4张表：channels/customers/channel_users/channels_customers
   - 影响：W4.3功能解锁
   - DDL时间：~5s（lock_timeout）

2. **计费相关修复**（如未应用）
   - 1000-1002迁移
   - billing_status/bill_payment_tracking/billing_retry_queue

3. **Telemetry修复**
   - 代码级别，无DDL
   - 影响：用量账本准确性

4. **文档更新**
   - 零影响

5. **测试基础设施**
   - localtest/casdoor-e2e
   - 零运行时影响

6. **门禁收口**
   - 代码级别修复

### 执行步骤

#### 阶段1：准备（30分钟）

```bash
# 1. 确认211当前版本
ssh 211-host "cd /app/llm-gateway && git rev-parse HEAD > /tmp/211-pre-upgrade.sha"

# 2. 获取待追平提交详情
git log $(cat /tmp/211-pre-upgrade.sha)..origin/main --stat > 211-upgrade-plan.txt

# 3. 检查迁移内容
grep -E "sql/migrations" 211-upgrade-plan.txt

# 4. 生成迁移清单
ls sql/migrations/startup/492*.sql
ls sql/migrations/startup/1000*.sql  # 如未应用

# 5. 备份计划
# 211应该有自动备份，确认最近备份时间
ssh 211-host "pg_dump ... > /backup/pre-upgrade-$(date +%Y%m%d).sql"
```

#### 阶段2：部署（1-2小时）

```bash
# 1. 拉取最新代码
ssh 211-host "cd /app/llm-gateway && git fetch origin"

# 2. 切换到目标版本
ssh 211-host "cd /app/llm-gateway && git checkout origin/main"

# 3. 应用迁移（如使用apply脚本）
ssh 211-host "cd /app/llm-gateway && bash scripts/apply-db-revision-sequence.sh"

# 4. 重启服务
ssh 211-host "systemctl restart llm-gateway"  # 或实际重启命令

# 5. 健康检查
curl https://211-host/health
curl https://211-host/api/admin/stats
```

#### 阶段3：验证（30分钟）

```bash
# 1. 验证迁移已应用
psql -h 211-db -d llm_gateway -c "SELECT * FROM public.schema_migrations WHERE version = '492';"
psql -h 211-db -d llm_gateway -c "\\dt channels"  # 验证表存在

# 2. 验证API可用
curl -H "Authorization: Bearer $TOKEN" https://211-host/api/channels/list

# 3. 验证日志无错误
ssh 211-host "tail -100 /var/log/llm-gateway/error.log | grep -i error"

# 4. 验证关键指标
# - 请求成功率
# - 响应时间
# - 错误率

# 5. 记录最终版本
ssh 211-host "cd /app/llm-gateway && git rev-parse HEAD > /tmp/211-post-upgrade.sha"
```

### 验证清单

- [ ] 代码已更新到最新main
- [ ] 迁移已应用（schema_migrations验证）
- [ ] 新表已创建（channels等）
- [ ] 服务健康检查通过
- [ ] API端点正常响应
- [ ] 日志无ERROR
- [ ] 关键指标正常
- [ ] 版本SHA已记录

### 回滚计划

**如果出现问题**：

```bash
# 1. 立即回滚代码
ssh 211-host "cd /app/llm-gateway && git checkout <previous-sha>"
ssh 211-host "systemctl restart llm-gateway"

# 2. 回滚迁移（如需要）
# 492有down迁移
psql -h 211-db -d llm_gateway -f sql/migrations/startup/492_channels_customers.down.sql

# 3. 恢复备份（最后手段）
# pg_restore ...

# 4. 验证服务恢复
curl https://211-host/health
```

### 风险评估

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|----------|
| DDL锁超时 | 低 | 中 | 492有5s lock_timeout，低峰执行 |
| 新表缺数据 | 低 | 低 | channels表初始为空，正常 |
| 服务重启中断 | 中 | 低 | 滚动重启或低峰执行 |
| 迁移失败 | 低 | 高 | 有down迁移，可回滚 |

---

## 🟢 245/154环境追平计划（优先级：低）

### 基本信息

**环境**: 245/154生产库  
**落后**: 86/88笔提交  
**预估工时**: 分批执行，每批2-3小时  
**建议窗口**: 1-2个月内完成

### 执行策略

**分批追平**，每批10-20笔：

#### Batch 1: 近期关键修复（10笔）

**时间**: 第1周  
**内容**: 包含492、telemetry、关键bug修复

```bash
# 获取最近10笔
git log origin/main~10..origin/main --oneline
```

**执行**: 同211流程

#### Batch 2: 功能更新（20笔）

**时间**: 第2-3周  
**内容**: 新功能、优化

#### Batch 3-N: 历史积压（剩余笔数）

**时间**: 第4-8周  
**内容**: 按10-20笔分批

### 验证策略

每批执行后：

1. 完整功能测试
2. 性能监控7天
3. 用户反馈收集
4. 下批前确认无回归

### 时间表

| 周次 | 环境 | 批次 | 提交数 | 状态 |
|------|------|------|--------|------|
| W1 | 245 | Batch 1 | 10 | 计划中 |
| W2 | 154 | Batch 1 | 10 | 计划中 |
| W3 | 245 | Batch 2 | 20 | 计划中 |
| W4 | 154 | Batch 2 | 20 | 计划中 |
| W5-8 | 245/154 | Batch 3-N | 剩余 | 计划中 |

---

## 📋 通用执行清单

### 部署前

- [ ] 确认当前版本SHA
- [ ] 生成待追平提交列表
- [ ] 检查迁移内容
- [ ] 确认备份可用
- [ ] 预留回滚窗口
- [ ] 通知相关人员

### 部署中

- [ ] 拉取最新代码
- [ ] 应用迁移
- [ ] 重启服务
- [ ] 健康检查

### 部署后

- [ ] 验证迁移已应用
- [ ] API功能测试
- [ ] 日志检查
- [ ] 指标监控
- [ ] 记录最终版本
- [ ] 更新追平记录

### 如遇问题

- [ ] 立即通知
- [ ] 评估影响
- [ ] 决策回滚/修复
- [ ] 执行回滚（如需）
- [ ] 记录问题
- [ ] 更新方案

---

## 🔍 迁移内容速查

### 492渠道商车道

**文件**: `sql/migrations/startup/492_channels_customers.sql`

**DDL内容**:
- CREATE TABLE channels
- CREATE TABLE customers
- CREATE TABLE channel_users
- CREATE TABLE channels_customers
- 相关索引

**down迁移**: `492_channels_customers.down.sql`（DROP TABLE）

**风险**: 低（新表，无依赖）

### 1000-1002计费系统

**文件**: 
- `1000_billing_status_and_credit_limit.sql`
- `1001_bill_payment_tracking.sql`
- `1002_billing_retry_queue.sql`

**DDL内容**:
- billing相关列和表

**风险**: 中（计费核心逻辑）

---

## 📊 追平进度跟踪

### 记录模板

```markdown
# 生产环境追平记录

## 211环境

- **执行日期**: YYYY-MM-DD
- **执行人**: xxx
- **前版本**: <sha>
- **后版本**: <sha>
- **应用迁移**: 492, 1000, 1001, 1002
- **验证结果**: ✅ / ❌
- **问题记录**: 无 / [详情]
- **回滚**: 否 / 是（原因）

## 245环境

[同上]

## 154环境

[同上]
```

### 追平看板

| 环境 | 目标版本 | 当前版本 | 进度 | 最后更新 |
|------|---------|----------|------|----------|
| 211 | main@latest | xxxx | 0/6 | 待启动 |
| 245 | main@latest | xxxx | 0/86 | 待启动 |
| 154 | main@latest | xxxx | 0/88 | 待启动 |

---

## 🎯 成功标准

### 211环境（本月内）

- [x] 落后笔数 < 10
- [ ] 492迁移已应用
- [ ] channels端点可用
- [ ] 零生产事故

### 245/154环境（1-2月）

- [ ] 落后笔数 < 20
- [ ] 关键迁移已应用
- [ ] 功能正常
- [ ] 性能稳定

---

## 📚 相关文档

- 492投递执行报告: `docs/audit/2026-11-27-492-delivery-execution-report.html`
- 两周审计总结: `docs/audit/2026-11-27-two-week-audit-summary.html`
- 遗留工作报告: `docs/audit/2026-11-27-legacy-work-report.html`

---

**文档维护者**: 项目审计团队  
**最后更新**: 2026-11-27  
**下次更新**: 211执行后更新进度
