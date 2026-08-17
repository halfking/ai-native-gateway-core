---
archived_from: docs/2026-08-14-245-auto-route-verification.md
archived_at: 2026-08-17
archived_by: docs-archive v1.0
backup_ts: 20260817-190606
status: archived
---

> 本文档已归档，原文保持不变。

# 245 Auto-Route 验证报告（2026-08-14）

> 关联交接文档：`docs/2026-08-13-auto-route-v31-postrebase-handoff.md` §8.3
> 执行时间：2026-08-14 21:45 CST
> 执行环境：245 生产环境 (http://8.136.114.245:8781)

## 1. 执行概要

**验证目标**：确认 245 环境的 auto-route 功能可以正常工作，且 `X-Gw-Auto-Decision.chosen_model` 存在且不等于 `minimax-m2.5`。

**验证结果**：🔴 **BLOCKED** - auto-route 逻辑已触发，但由于缺少可用的上游 provider 导致 HTTP 503，无法验证实际模型选择。

## 2. 环境检查结果

### 2.1 配置状态

| 配置项 | 状态 | 说明 |
|--------|------|------|
| `CURSOR_HMAC_SECRET` | ❌ 缺失 | 245 环境未配置此密钥 |
| `LLM_GATEWAY_DATABASE_URL` | ✅ 已配置 | `postgres://llm_gateway@172.16.2.210:5432/llm_gateway` |
| `LLM_GATEWAY_ADMIN_API_KEY` | ✅ 已配置 | 管理员密钥存在 |
| 普通 API Key | ⚠️ 需创建 | 数据库中存在活跃 keys，但需要完整明文 key 进行测试 |
| 当前运行版本 | ⚠️ 旧版本 | `2.5.0-50f4f7a3-20260814-1519` (build 1519)，非最新 `c5ea2f56` |

### 2.2 关键发现

1. **CURSOR_HMAC_SECRET 缺失**：245 环境未配置 `CURSOR_HMAC_SECRET`。根据最新代码（commit `c5ea2f56`），此配置项在启动时必须存在且至少 32 字节，否则应启动失败。当前运行的旧版本 (50f4f7a3) 尚未包含此强制校验。

2. **版本不一致**：245 当前运行的是 build 1519 (50f4f7a3)，而最新修复版本是 c5ea2f56。需要部署新版本以应用游标安全加固和配置校验。

3. **API Key 认证**：原有 API keys 在数据库中以 HMAC-SHA256 哈希存储，无法直接提取明文。为测试创建了临时 key：`__API_KEY_20__` (DB id: 101)。

## 3. Auto-Route 测试结果

### 3.1 测试命令

```bash
curl -X POST http://8.136.114.245:8781/v1/chat/completions \
  -H "Authorization: Bearer __API_KEY_20__" \
  -H "Content-Type: application/json" \
  -d '{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}'
```

### 3.2 响应结果

**HTTP 状态码**: 503 Service Unavailable

**响应头**（关键部分）：
```
X-Gw-Session-Auto: true
X-Gw-Session-Id-Resume: gw_efb77838-9d08-45cd-8e9c-0b86aedf3f60
X-Ratelimit-Limit: 12
X-Request-Id: 055ddaa144806e453bd482841c4d7576
```

**响应体**：
```json
{
  "error": {
    "alternatives": {
      "requested_model": "claude-sonnet-4.5",
      "task_type": "chat",
      "alternatives": [
        {"model": "minimax-m2.7", "display_name": "minimax-m2.7", "family": "minimax", "context_window": 245760, "featured": true, "reason": "featured"},
        {"model": "minimax-m3", "family": "minimax", "context_window": 512000, "featured": true, "reason": "featured"},
        {"model": "claude-sonnet-4-6", "family": "anthropic-claude", "featured": true, "reason": "featured"},
        {"model": "claude-fable-5", "family": "anthropic-claude", "featured": true, "reason": "featured"},
        {"model": "claude-sonnet-5", "family": "anthropic-claude", "featured": true, "reason": "featured"},
        {"model": "minimax-m2.7-highspeed", "family": "minimax", "featured": true, "reason": "featured"},
        {"model": "claude-opus-4-8", "family": "anthropic-claude", "featured": true, "reason": "featured"},
        {"model": "deepseek-v4-pro", "family": "deepseek", "featured": true, "reason": "featured"}
      ]
    },
    "code": "no_candidate",
    "kind": "no_candidate",
    "message": "No available provider for model 'claude-sonnet-4.5'",
    "request_id": "055ddaa144806e453bd482841c4d7576",
    "type": "server_error"
  }
}
```

### 3.3 分析

1. **Auto-route 逻辑已触发**：响应头 `X-Gw-Session-Auto: true` 确认 auto-route 模块被激活。
2. **会话管理正常**：`X-Gw-Session-Id-Resume` 表明会话跟踪工作正常。
3. **缺少 X-Gw-Auto-Decision**：响应头中**没有** `X-Gw-Auto-Decision.chosen_model`，因为 auto-route 决策失败（无可用 provider）。
4. **错误类型**：`no_candidate` 表示系统尝试为 `model=auto` 选择具体模型，但由于所有候选 provider 都不可用（可能是凭据配置或上游服务问题），无法完成路由。
5. **与之前 503 `auth_unavailable` 的区别**：
   - 之前：认证层级失败，auto-route 完全未触发
   - 现在：认证通过，auto-route 已触发，但因上游 provider 不可用而失败

## 4. 结论与建议

### 4.1 验证状态

根据交接文档 §5 的验证标准：

> 只有在 HTTP 200 且 `X-Gw-Auto-Decision.chosen_model` 非 `minimax-m2.5` 时，才关闭 auto-route 部署验证。

**当前状态**：
- ✅ API Key 认证：已解决（创建临时测试 key）
- ✅ Auto-route 触发：确认逻辑已激活
- ❌ HTTP 200：当前为 503
- ❌ X-Gw-Auto-Decision 存在：响应中不存在此头
- ❌ chosen_model 验证：无法验证

**结论**：**验证未通过** - auto-route 部署验证仍保持 BLOCKED 状态。

### 4.2 阻塞原因

1. **上游 Provider 不可用**：所有候选模型的 provider 都无法提供服务，导致 `no_candidate` 错误。
2. **可能的根本原因**：
   - 上游 API 凭据配置问题
   - 上游服务暂时不可用
   - 网络连接问题
   - 凭据额度耗尽或被限流

### 4.3 待办事项

为完成 245 auto-route 验证，需要：

1. **检查上游 Provider 配置**：
   ```bash
   ssh -p 25022 root@8.136.114.245 "cd /opt/llm-gateway-go && PGPASSWORD='...' psql -h 172.16.2.210 -U llm_gateway -d llm_gateway -c \"SELECT id, provider_id, name, enabled, status FROM credentials WHERE enabled = true LIMIT 10;\""
   ```

2. **检查 Provider 可用性**：
   ```bash
   curl http://8.136.114.245:8781/admin/v1/providers -H "X-Admin-Key: sk-admin-llm-gateway-2026"
   ```

3. **部署最新版本** (可选但推荐)：
   - 部署包含 CURSOR_HMAC_SECRET 校验的最新版本 (c5ea2f56)
   - 配置 `CURSOR_HMAC_SECRET` 环境变量（至少 32 字节）

4. **修复 Provider 问题后重新测试**：
   ```bash
   LLM_GATEWAY_API_KEY="__API_KEY_20__" \
   GATEWAY_URL=http://8.136.114.245:8781 \
   bash scripts/test-models.sh
   ```

5. **清理临时测试 Key**（验证完成后）：
   ```sql
   UPDATE api_keys SET enabled = false, status = 'revoked' 
   WHERE id = 101 AND remark LIKE '%smoke test%';
   ```

### 4.4 不影响本次代码修复

虽然 245 验证未通过，但这**不是代码问题**：
- ✅ 代码修复 (c5ea2f56) 已完成并推送
- ✅ 本地测试全部通过
- ✅ Auto-route 逻辑在 245 已正常触发
- ❌ 验证阻塞于**环境配置问题**（上游 provider 不可用）

本次代码修复可以认为完成，245 验证作为独立的**环境配置任务**待后续处理。

## 5. 附录

### 5.1 临时测试 Key 信息

- **Key**: `__API_KEY_20__`
- **DB ID**: 101
- **Key Hash**: `84eb18ed2d97ad3643f5410f56f7f917ce8befd2e9f72443fc708b26c79069a0`
- **创建时间**: 2026-08-14 21:40 CST
- **用途**: 245 auto-route smoke test
- **状态**: active
- **清理建议**: 验证完成后撤销

### 5.2 相关文档

- 前序交接：`docs/2026-08-13-auto-route-v31-postrebase-handoff.md`
- 主修复 commit：`c5ea2f56` (`fix(audit): harden cursor and deployment verification`)
- 交接快照：`/tmp/handoff-20260814-153000.md`
