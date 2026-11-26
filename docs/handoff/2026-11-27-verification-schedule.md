# P0计费修复 - 后期验证与部署计划

**创建时间**: 2026-11-27  
**基准时间**: 211 dev环境部署完成（2026-11-27 00:20）

---

## 📅 时间表

| 阶段 | 时间 | 环境 | 状态 |
|------|------|------|------|
| Phase 1 | 2026-11-27 | 211 Dev | ✅ 已完成 |
| Phase 2 | 2026-11-30 | Staging | ⏰ 已设置定时任务 |
| Phase 3 | 2026-12-07 | Production | ⏳ 待设置（需新会话） |

---

## ✅ Phase 1: Dev环境部署（已完成）

**执行时间**: 2026-11-26 22:33 - 2026-11-27 00:20

**完成项目**:
- ✅ 迁移1000/1001/1002应用到211/acc-pg17
- ✅ 代码版本验证（4fbf408f包含P0修复）
- ✅ P0-4数据验证（负成本=0条）
- ✅ API健康检查通过

**参考文档**:
- 执行报告: `docs/handoff/2026-11-27-final-execution-report.md`
- 迁移验证: 2+5+1项全部成功

---

## ⏰ Phase 2: Staging环境部署（2026-11-30）

**定时任务已创建**:
- 任务ID: `automation-0e1f70c0-84c0-4011-8258-1040b98b4dad`
- 执行时间: 2026-11-30 09:00
- 标题: 3天后staging环境部署提醒

### 执行检查清单

#### 前置验证（09:00-09:30）

**1. 211 Dev环境稳定性检查**
```bash
# 连接211服务器
ssh 211

# 检查容器状态
docker ps --filter 'name=llm-gateway' --format '{{.Names}}\t{{.Status}}'

# 检查数据库迁移状态
docker exec acc-pg17 psql -U postgres -d llm_gateway -c "
SELECT 
    COUNT(*) FILTER (WHERE cost_usd < 0) as negative_cost_count
FROM session_turns 
WHERE ts >= '2026-11-27' AND ts < '2026-11-30';
"

# 检查API健康状态
curl -s http://127.0.0.1:8781/readyz | python3 -m json.tool
```

**验收标准**:
- ✓ 容器健康运行3天无重启
- ✓ 负成本记录 = 0
- ✓ API readyz返回200
- ✓ 无异常503/500错误

#### 数据库迁移（09:30-10:00）

**连接staging数据库**:
```bash
# 假设staging数据库配置
# 需要提前确认staging数据库连接信息
psql -h staging-db-host -U postgres -d llm_gateway_staging

# 应用迁移1000
\i /path/to/1000_billing_status_and_credit_limit.sql

# 应用迁移1001
\i /path/to/1001_bill_payment_tracking.sql

# 应用迁移1002
\i /path/to/1002_billing_retry_queue.sql
```

**验证查询**:
```sql
-- 验证迁移1000
SELECT column_name FROM information_schema.columns 
WHERE table_name = 'tenant_credit_wallets' 
  AND column_name IN ('credit_limit_credits', 'credit_policy');

-- 验证迁移1001
SELECT column_name FROM information_schema.columns 
WHERE table_name = 'maas_monthly_bills' 
  AND column_name IN ('payment_date', 'payment_method', 'payment_voucher', 'payment_notes');

-- 验证迁移1002
SELECT table_name FROM information_schema.tables 
WHERE table_name = 'billing_retry_queue';
```

**验收标准**:
- ✓ 迁移1000: 2个字段成功添加
- ✓ 迁移1001: 5个字段成功添加
- ✓ 迁移1002: 1个表成功创建

#### 代码部署（10:00-10:30）

**部署到staging服务器**:
```bash
# 连接staging服务器
ssh staging-server

# 拉取包含P0修复的代码
cd /path/to/llm-gateway-go
git fetch origin
git checkout <包含P0修复的SHA>

# 执行部署脚本
bash scripts/deploy-staging.sh
```

**验收标准**:
- ✓ 代码版本包含baf621a6b（P0-1）和91e1e5b02（P0-2）
- ✓ 编译成功
- ✓ 健康检查通过

#### P0-4数据验证（10:30-11:00）

**执行验证查询**:
```sql
-- 验证1: 负成本检查
SELECT 
    DATE(ts) AS date,
    COUNT(*) FILTER (WHERE cost_usd < 0) AS negative_cost_count,
    COUNT(*) AS total_count
FROM session_turns
WHERE ts >= '2026-11-01' AND ts < '2026-12-01'
  AND cost_usd IS NOT NULL
GROUP BY DATE(ts)
HAVING COUNT(*) FILTER (WHERE cost_usd < 0) > 0
ORDER BY date DESC;

-- 验证2: Anthropic缓存场景
SELECT 
    DATE(ts) AS date,
    COUNT(*) AS cache_gt_prompt_count,
    AVG(cost_usd) AS avg_cost_usd
FROM session_turns
WHERE ts >= '2026-11-01'
  AND cache_read_tokens > prompt_tokens
  AND provider LIKE '%anthropic%'
GROUP BY DATE(ts)
ORDER BY date DESC;
```

**验收标准**:
- ✓ 负成本记录 = 0
- ✓ Anthropic缓存场景cost > 0（如有数据）

#### 生成报告（11:00-11:30）

创建文档: `docs/handoff/2026-11-30-staging-deployment-report.md`

包含内容:
- 211环境稳定性数据
- Staging迁移结果
- Staging部署验证
- P0-4验证结果
- 下一步建议

---

## ⏳ Phase 3: Production环境部署（2026-12-07）

**状态**: 需要在新会话中创建定时任务

### 创建定时任务的命令

用户需要在新会话中执行以下操作：

```
创建定时任务：
- 时间: 2026-12-07 09:00
- cron: 0 9 7 12 *
- 标题: 7天后production环境部署提醒（2026-12-07）
```

### 执行检查清单

#### 前置验证（09:00-09:30）

**1. 确认staging环境稳定**
```bash
# 连接staging服务器
ssh staging-server

# 检查7天运行数据
# 检查负成本、503错误、API响应时间等指标
```

**验收标准**:
- ✓ Staging运行7天无重大问题
- ✓ P0-4验证结果正常
- ✓ 业务指标正常

**2. 确认211 dev环境稳定**
```bash
# 211应该已运行10天（27号+7天=12月7日）
# 检查长期稳定性数据
```

#### 数据库迁移（09:30-10:30）

**245/154生产环境数据库**:

根据最终报告，245和154共用数据库（172.16.2.210）：

```bash
# 连接生产数据库
ssh 154  # 或245

# 应用迁移（与staging相同步骤）
docker exec <postgres-container> psql -U postgres -d llm_gateway << 'SQL'
-- 迁移1000
\i /path/to/1000_billing_status_and_credit_limit.sql
-- 迁移1001
\i /path/to/1001_bill_payment_tracking.sql
-- 迁移1002
\i /path/to/1002_billing_retry_queue.sql
SQL
```

**验证步骤**（与staging相同）

#### 代码部署（10:30-11:30）

**部署到245和154**:

```bash
# 245部署
ssh 245
cd /path/to/llm-gateway-go
git fetch origin
git checkout <verified-SHA>
bash scripts/deploy-245.sh

# 154部署
ssh 154
cd /path/to/llm-gateway-go
git fetch origin
git checkout <verified-SHA>
bash scripts/deploy-154.sh
```

**验收标准**:
- ✓ 两台服务器代码版本一致
- ✓ 包含所有P0修复
- ✓ 健康检查通过
- ✓ 凭据解密测试通过

#### P0-4生产数据验证（11:30-12:00）

**在生产数据库执行验证查询**（与staging相同SQL）

**额外生产验证**:
- 检查真实业务流量的cost计算
- 检查Anthropic缓存场景（生产应该有真实数据）
- 监控503/500错误率

#### 最终报告（12:00-12:30）

创建文档: `docs/handoff/2026-12-07-production-deployment-report.md`

包含内容:
- 完整部署历史（Dev → Staging → Production）
- 三个环境的验证结果对比
- 生产环境特有指标
- P0修复效果总结
- 遗留问题和建议

---

## 📊 验证指标汇总

### 关键指标

| 指标 | Dev (211) | Staging | Production |
|------|-----------|---------|------------|
| 负成本记录 | 0 | TBD | TBD |
| 迁移成功率 | 100% | TBD | TBD |
| API可用性 | 100% | TBD | TBD |
| 部署时长 | - | TBD | TBD |

### 数据验证结果

**211 Dev环境基准** (2026-11-27):
```
总记录数: 487,935
负成本记录: 0 ✓
零成本记录: 340,283 (69.75%)
正成本记录: 147,599 (30.25%)
Anthropic cache>prompt: 0 (测试环境无数据)
```

---

## 🚨 风险和应对

### 已知风险

1. **Staging/Production数据库连接信息未确认**
   - 应对: 提前与DBA确认连接信息
   - 文档: 更新到交接文档

2. **245/154 SSH连接超时问题**
   - 应对: 使用内网或跳板机
   - 文档: docs/handoff/2026-11-27-final-execution-report.md

3. **Anthropic缓存场景在测试环境无数据**
   - 应对: 在生产环境补充验证
   - 预期: 生产应该有真实数据

### 回滚计划

如果生产部署出现问题：

```bash
# 数据库回滚（使用.down.sql）
psql -d llm_gateway -f 1002_billing_retry_queue.down.sql
psql -d llm_gateway -f 1001_bill_payment_tracking.down.sql
psql -d llm_gateway -f 1000_billing_status_and_credit_limit.down.sql

# 代码回滚（使用蓝绿部署保留的旧版本）
# 245/154都保留了旧实例用于回滚
docker start <old-container-name>
# 切换nginx路由到旧端口
```

---

## 📄 相关文档

### 已完成文档
- ✅ `docs/handoff/2026-11-26-p0-tasks-handoff.md` - 主交接文档
- ✅ `docs/handoff/2026-11-26-status-update.md` - 状态更新
- ✅ `docs/handoff/2026-11-27-final-execution-report.md` - 最终执行报告
- ✅ `docs/handoff/COMPLETION-CONFIRMATION.md` - 完成确认

### 待创建文档
- ⏳ `docs/handoff/2026-11-30-staging-deployment-report.md`
- ⏳ `docs/handoff/2026-12-07-production-deployment-report.md`

### 技术文档
- `docs/fixes/2026-11-26-p0-1-handler-integration-guide.md` - P0-1集成指南
- `docs/fixes/2026-11-26-p0-2-completion-summary.md` - P0-2完成总结
- `docs/fixes/2026-11-26-p0-3-deployment-checklist.md` - P0-3部署清单
- `docs/fixes/2026-11-26-p0-4-cache-token-convention-verification.md` - P0-4验证

---

## ✅ 行动项

### 立即执行
- [x] 创建staging部署定时任务（2026-11-30 09:00）
- [ ] 在新会话中创建production部署定时任务（2026-12-07 09:00）
- [ ] 确认staging/production数据库连接信息
- [ ] 确认245/154服务器访问方式

### 2026-11-30执行
- [ ] 执行staging环境完整部署流程
- [ ] 生成staging部署报告

### 2026-12-07执行
- [ ] 执行production环境完整部署流程
- [ ] 生成最终production部署报告
- [ ] 总结P0修复项目

---

**创建人**: AI Assistant  
**最后更新**: 2026-11-27 00:30  
**状态**: Phase 1完成，Phase 2已设置定时任务，Phase 3待设置
