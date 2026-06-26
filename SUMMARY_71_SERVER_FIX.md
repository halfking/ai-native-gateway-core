# 71服务器路由问题修复总结报告

## 📋 问题概述

**报告时间**: 2026-06-26  
**服务器**: 71服务器 (llmgateway.internal.example.com)  
**问题级别**: P0 - 生产环境故障  

### 问题现象
1. ❌ 无法通过 `llmgateway.internal.example.com/v1` 发起任何请求
2. ❌ 路由无法正确匹配到可用凭据（虽然 minimax 下的 minimax-m3 可用）
3. ❌ 184数据库的 `request_logs` 中大量记录显示 `empty_response`

## 🔍 根因分析

### 1. 路由匹配失败的可能原因

#### A. 质量门限过滤过严
- **代码位置**: `provider/client.go:578-615`
- **机制**: 最近引入的多层质量门限策略
  - 第一轮：`success_rate >= 0.3` (严格)
  - 第二轮：`success_rate >= 0.0` (宽松回退)
- **问题**: 如果所有凭据的最近50次请求成功率都低于30%，第一轮会全部过滤，只能依赖第二轮回退

#### B. 凭据状态异常
可能的状态问题：
- `availability_state != 'ready'` (如 `suspended`, `auth_failed`, `cooling`, `rate_limited`, `unreachable`)
- `circuit_state = 'open'` (熔断器打开)
- `lifecycle_status != 'active'` (如 `expired`, `suspended`)
- `quota_state != 'ok'` (如 `balance_exhausted`, `periodic_exhausted`)

#### C. 探测状态错误
- **表**: `model_probe_state`
- **问题状态**: `state = 'broken_confirmed'`
- **影响**: SQL查询中的 `NOT EXISTS` 子句会过滤掉这些凭据
- **代码位置**: `provider/client.go:703-708`

#### D. 视图不同步
- **视图**: `v_routable_credential_models`
- **问题**: 视图返回 `is_routable = false`
- **原因**: 视图综合了多个条件（provider.manual_disabled, credentials.manual_disabled, cmb.unavailable_reason）

### 2. empty_response 问题

#### 已修复（提交 78de1295）
- **修复内容**: `detectEmptyStreamResponse` 函数增加了3个短路条件
  1. `stream_interrupted` - 避免将中断错误误判为空响应
  2. `tool_calls` - 有工具调用时不判定为空
  3. `upstream_finish_reason` - 有正常结束标志时不判定为空
- **修复位置**: `relay/handler.go:3064-3140`

#### 可能的残留问题
如果71服务器未部署最新代码，仍会出现误判：
- 工具调用响应被标记为 empty_response
- 推理模型到达 max_tokens 被标记为 empty_response
- eof_without_done 良性中断被标记为 empty_response

## 🛠️ 已提供的修复工具

### 1. 诊断脚本
**文件**: `scripts/diagnose_routing_issue.sh`

**功能**:
- 检查最近1小时请求统计
- 列出指定模型的候选凭据状态
- 显示最近50次请求成功率
- 检查 model_probe_state 探测状态
- 显示最近失败请求详情
- 分析质量门限过滤情况

**使用**:
```bash
./scripts/diagnose_routing_issue.sh minimax-m3
```

### 2. 凭据状态修复脚本
**文件**: `scripts/fix_credentials_state.sql`

**功能**:
- 诊断当前凭据状态
- 提供修复SQL（需手动取消注释）
- 重置 availability_state、circuit_state、lifecycle_status、quota_state
- 清除 broken_confirmed 探测状态

**使用**:
```bash
psql "$LLM_GATEWAY_DATABASE_URL" -f scripts/fix_credentials_state.sql
```

### 3. 紧急凭据重置脚本
**文件**: `scripts/emergency_fix_credentials.sql`

**功能**:
- 强制重置所有活跃凭据状态
- 备份当前状态到临时表
- 显示修复摘要
- 需要手动 COMMIT 确认

**使用**:
```bash
psql "$LLM_GATEWAY_DATABASE_URL" -f scripts/emergency_fix_credentials.sql
# 确认后执行 COMMIT;
```

### 4. 部署自动化脚本
**文件**: `scripts/deploy_fix_to_71.sh`

**功能**:
- 自动同步代码到71服务器
- 在服务器上编译
- 备份旧版本
- 重启服务
- 检查健康状态

**使用**:
```bash
./scripts/deploy_fix_to_71.sh root@llmgateway.internal.example.com
```

### 5. 路由测试脚本
**文件**: `scripts/test_routing_fix.sh`

**功能**:
- 测试 minimax-m3 非流式请求
- 测试流式请求
- 检查路由解析 API

**使用**:
```bash
./scripts/test_routing_fix.sh
```

## 📖 文档

### 1. 快速参考
**文件**: `README_71_SERVER_FIX.md`  
**内容**: 4步快速修复指南

### 2. 详细修复指南
**文件**: `docs/HOTFIX_71_SERVER_ROUTING.md`  
**内容**: 
- 详细根因分析
- 诊断步骤
- 5种修复方案（A-E）
- 验证方法
- 预防措施
- 回滚方案

### 3. 部署检查清单
**文件**: `DEPLOYMENT_CHECKLIST.md`  
**内容**:
- 7步部署流程
- 每步的检查项
- 常见问题场景处理
- 监控指标
- 升级路径

### 4. 快速开始
**文件**: `QUICK_START_71.md`  
**内容**: 5步快速修复流程

## 🎯 推荐执行步骤

### 立即在71服务器上执行：

```bash
# 1. SSH登录
ssh root@llmgateway.internal.example.com

# 2. 进入项目目录
cd /opt/llm-gateway-go

# 3. 拉取最新代码和工具
git fetch origin
git checkout server-71
git pull origin server-71

# 4. 运行诊断
./scripts/diagnose_routing_issue.sh minimax-m3 | tee diagnosis.txt

# 5. 根据诊断结果选择修复方案
# 方案A: 如果代码版本不是 78de1295，重新编译
git log --oneline -1
go build -o llm-gateway ./cmd/gateway
systemctl restart llm-gateway

# 方案B: 如果凭据状态异常，重置凭据
psql "$LLM_GATEWAY_DATABASE_URL" -f scripts/emergency_fix_credentials.sql
# 然后执行 COMMIT;

# 6. 验证修复
./scripts/test_routing_fix.sh

# 7. 监控恢复
journalctl -u llm-gateway -f
```

## 📊 期望结果

修复后应达到以下指标：

| 指标 | 修复前 | 目标值 |
|------|--------|--------|
| 可用凭据数量 | 0-1 | >= 3 |
| 请求成功率 | < 50% | >= 95% |
| empty_response 占比 | > 30% | < 5% |
| no_candidate 错误 | 大量 | 0 |
| 路由解析返回凭据 | 0 | >= 1 |

## ⚠️ 注意事项

1. **诊断优先**: 先运行诊断脚本了解问题，再选择合适的修复方案
2. **数据库操作谨慎**: emergency_fix_credentials.sql 会重置所有凭据，需要手动 COMMIT
3. **监控恢复**: 修复后至少监控10分钟确保稳定
4. **备份先行**: 紧急修复前建议备份数据库关键表

## 🔄 预防措施

### 1. 添加监控告警
```sql
-- 凭据可用性告警
SELECT COUNT(*) FROM credentials 
WHERE status='active' AND availability_state='ready';
-- 期望: >= 3

-- no_candidate 错误告警
SELECT client_model, COUNT(*) 
FROM request_logs 
WHERE ts > NOW() - INTERVAL '5 minutes' 
  AND error_kind = 'no_candidate'
GROUP BY client_model;
-- 期望: 0
```

### 2. 定期健康检查
建议添加 cron 任务每5分钟检查凭据状态

### 3. 代码部署流程
- 每次部署前运行测试脚本
- 部署后立即检查日志
- 监控关键指标至少10分钟

## 📞 升级和支持

如果30分钟内无法修复：

1. **回滚到上一个稳定版本**
2. **切换到备用凭据**
3. **联系支持团队**，提供：
   - 诊断脚本输出 (`diagnosis.txt`)
   - 服务日志 (`journalctl -u llm-gateway --since "1 hour ago"`)
   - Git版本信息 (`git log --oneline -10`)

## ✅ 提交记录

**Commit**: `2d204165`  
**Message**: `fix(ops): add 71 server routing diagnosis and fix tools`  
**Files Changed**: 9 files, 1548 insertions  
**Branch**: `server-71`  
**Status**: ✅ 已推送到远程仓库

## 📝 总结

已为71服务器问题提供完整的诊断和修复工具集：
- ✅ 5个诊断/修复脚本
- ✅ 4份详细文档
- ✅ 自动化部署工具
- ✅ 完整的操作清单

**下一步行动**: 请立即在71服务器上执行快速修复流程（参考 QUICK_START_71.md）

---

**报告生成时间**: 2026-06-26  
**预计修复时间**: 15-30分钟  
**紧急程度**: P0 - 立即处理
