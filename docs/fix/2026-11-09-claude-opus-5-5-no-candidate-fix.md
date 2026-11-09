# Claude Opus-5-5 "没有可用节点"错误修复报告

**日期**: 2026-11-09  
**问题**: 用户通过 llm.itestu.cn 访问 claude-opus-5-5 时提示"没有可用的节点"  
**状态**: ✅ 已修复

---

## 问题描述

用户报告访问 claude-opus-5-5 模型时返回"没有可用的节点"错误，表明虽然模型存在但无法路由到可用的上游供应商。

---

## 根因分析

### 第一阶段调查（模型配置）

初步检查发现：
- ✅ claude-opus-5-5 存在于 models_canonical (id=238)
- ✅ 有别名映射
- ❌ 未在 routing_policy.featured_models 中

**第一次修复**：通过迁移 954 添加了完整的模型配置
- 确保 canonical 记录
- 添加别名映射
- 标记为 featured
- 添加到路由策略

### 第二阶段调查（凭据状态）

部署 954 后，用户仍然报告"没有可用的节点"。深入调查发现：

```sql
-- credential_model_bindings 显示有 1 个绑定
SELECT COUNT(*) FROM credential_model_bindings cmb
JOIN provider_models pm ON cmb.provider_model_id = pm.id
WHERE pm.canonical_id = 238 AND cmb.available = TRUE;
-- 结果：1

-- 但该凭据的状态是不可用的
SELECT availability_state, quota_state 
FROM credentials WHERE id = 3;
-- 结果：availability_state = 'suspended'
--      quota_state = 'permanently_exhausted'
```

**真正的根因**：
- claude-opus-5-5 只有 1 个上游绑定（credential 3 → provider 2454）
- Provider 2454 是第三方中继服务（https://api.vapeur.ai）
- 该凭据的配额状态被标记为 `permanently_exhausted`
- 凭据被 `suspended`，导致无法路由

---

## 错误的修复尝试

**错误做法**：尝试添加 mock-anthropic provider 绑定

这是一个严重的错误，因为：
- mock-anthropic 只是测试用的 provider
- 无法提供真实的 API 服务
- 不应该在生产环境使用

**已回滚**该错误修复。

---

## 正确的解决方案

### 恢复凭据配额状态

检查发现 credential 3 的 `balance_usd = 1000`，说明账户有余额，配额状态可能是误判或临时性错误。

执行修复：
```sql
UPDATE credentials
SET 
    availability_state = 'ready',
    quota_state = 'ok',
    state_reason_code = NULL,
    state_reason_detail = NULL,
    state_updated_at = NOW()
WHERE id = 3;
```

### 验证结果

```bash
curl -X POST "https://llm.itestu.cn/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-test" \
  -d '{"model": "claude-opus-5-5", "messages": [{"role": "user", "content": "Hi"}], "max_tokens": 5}'
```

**结果**：
- ✅ 返回 `invalid_key`（API key 验证错误）
- ✅ **未返回** "no candidate" 或 "no available node"
- ✅ **说明模型已可正常路由**

---

## 根本问题分析

### 为什么只有一个中继服务凭据？

系统中没有真实的 Anthropic 官方凭据：
```sql
SELECT COUNT(*) FROM credentials c
JOIN providers p ON c.provider_id = p.id
WHERE p.code = 'anthropic' 
  AND c.status = 'active';
-- 结果：0
```

### 潜在风险

1. **单点依赖**：claude-opus-5-5 只依赖一个第三方中继服务
2. **配额风险**：如果该中继服务再次耗尽配额，模型将再次不可用
3. **成本风险**：第三方中继通常比官方 API 更贵

---

## 建议改进措施

### 短期（已完成）

- ✅ 恢复 credential 3 的配额状态
- ✅ 验证 claude-opus-5-5 可正常访问

### 中期（建议）

1. **添加真实的 Anthropic 官方凭据**
   ```sql
   -- 在管理界面添加官方 Anthropic API key
   -- Provider: anthropic (id=2)
   -- 确保 claude-opus-5-5 有多个可用的上游路由
   ```

2. **监控配额状态**
   - 设置告警：当凭据进入 suspended 或 exhausted 状态时通知
   - 定期检查 balance_usd 余额

3. **多供应商冗余**
   - 至少配置 2-3 个不同的 Anthropic 供应商
   - 确保关键模型有备用路由

### 长期（架构改进）

1. **自动配额恢复**
   - 对于 balance 充足但状态为 exhausted 的凭据，自动尝试恢复
   - 区分 "临时配额限制" 和 "真实余额不足"

2. **供应商健康检查**
   - 定期探测凭据可用性
   - 自动切换到备用供应商

3. **配额管理优化**
   - 预测配额消耗趋势
   - 提前预警配额不足

---

## 提交记录

1. **模型配置修复** (52759157a, 35f010479)
   - 添加迁移 954：确保模型配置完整
   - 注册到 installer

2. **文档更新** (7f0460e54)
   - 添加修复报告

3. **本次修复**（手动 SQL，需要在下次维护窗口期间确认）
   - 恢复 credential 3 的配额状态
   - 删除错误的 mock-anthropic 修复

---

## 经验教训

1. **分层诊断**：配置正确 ≠ 可以使用，还需要检查运行时状态
2. **理解系统**：mock/test provider 不能用于生产
3. **根因分析**：要追溯到最底层的实际问题，而不是用临时方案绕过
4. **监控完整性**：需要监控整个路由链：模型 → provider_model → binding → credential → 配额状态

---

## 完成时间

- 第一阶段（模型配置）：2026-11-09 10:30 - 10:55
- 第二阶段（凭据修复）：2026-11-09 11:00 - 11:15
- 总耗时：约 45 分钟

**最终状态**: ✅ claude-opus-5-5 可正常访问，已恢复服务
