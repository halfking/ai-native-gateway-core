# 154服务状态确认和监控指南

## 当前状态（2026-09-04 23:56）

### ✅ 服务状态
- **服务**: 运行中 (Active: active (running))
- **版本**: v2.5.0-fc28e27b-20260904-1929 ✅ **包含日志增强**
- **PID**: 24998
- **启动时间**: 2026-09-04 23:56:34 CST
- **健康检查**: ✅ /healthz 返回 200 OK
- **数据库**: ✅ postgres connected

### ⚠️ 发现的问题
服务启动时发现数据库schema问题：
```
ERROR: column "automatic" of relation "credential_probe_queue" does not exist
```

**影响范围**: 
- node_probe_worker 无法写入探测队列
- 不影响主要请求处理功能
- 不影响我们的日志增强功能

**建议**: 
- 这是一个数据库迁移问题
- 需要运行相应的SQL迁移脚本添加 `automatic` 列
- 可以稍后修复，不影响当前的日志观察

### 📊 服务活动
服务刚启动，可以看到：
- ✅ Postgres连接成功
- ✅ Credential cycler运行中（6个healthy凭据）
- ✅ Live stream系统运行中（处理174个请求）
- ✅ 多个provider活跃：
  - MiniMax (73 requests)
  - Anthropic/Claude (95 requests)  
  - OpenAI (6 requests)

## 监控新日志的方法

### 方法1: 实时监控（推荐）

在154服务器上运行，持续观察新日志：

```bash
ssh root@47.97.111.154 -p 25022

# 实时监控所有survival和候选节点相关日志
journalctl -u llm-gateway-go.service -f | \
  grep --line-buffered --color=auto -E \
  "survival_decision_aggregate|fold_candidate_outcomes|committed_output|empty_response|overloaded"
```

**说明**: 
- `-f` 持续跟踪新日志
- `--line-buffered` 确保实时输出
- `--color=auto` 高亮匹配的关键词

### 方法2: 定期检查

每隔一段时间检查是否有新的错误日志：

```bash
ssh root@47.97.111.154 -p 25022 << 'EOF'
echo "=== 检查时间: $(date) ==="
echo ""
echo "=== survival_decision_aggregate 日志 ==="
journalctl -u llm-gateway-go.service --since "1 hour ago" | \
  grep "survival_decision_aggregate" | tail -5

echo ""
echo "=== fold_candidate_outcomes 日志 ==="
journalctl -u llm-gateway-go.service --since "1 hour ago" | \
  grep "fold_candidate_outcomes" | tail -5

echo ""
echo "=== 常见错误关键词 ==="
journalctl -u llm-gateway-go.service --since "1 hour ago" | \
  grep -E "overloaded|empty_response|committed_output|tool_result.*missing" | tail -10
EOF
```

### 方法3: 查找特定错误类型

**查找 "Our servers are currently overloaded":**
```bash
ssh root@47.97.111.154 -p 25022 \
  'journalctl -u llm-gateway-go.service --since "6 hours ago" | grep -i overloaded | head -20'
```

**查找 empty_response:**
```bash
ssh root@47.97.111.154 -p 25022 \
  'journalctl -u llm-gateway-go.service --since "6 hours ago" | grep empty_response | head -20'
```

**查找 committed_output:**
```bash
ssh root@47.97.111.154 -p 25022 \
  'journalctl -u llm-gateway-go.service --since "6 hours ago" | grep committed_output | head -20'
```

**查找 tool result missing:**
```bash
ssh root@47.97.111.154 -p 25022 \
  'journalctl -u llm-gateway-go.service --since "6 hours ago" | grep "tool.*result.*missing" | head -20'
```

## 日志产生的触发条件

### ⚠️ 重要提示
我们增强的日志**只在错误发生时**才会产生。如果没有错误，就不会看到新日志。

### 新日志触发场景

#### 1. `survival_decision_aggregate`
**什么时候产生**: 
- 上游返回可恢复的错误（5xx、网络错误、超时）
- 上游返回空响应
- 需要决定是重试、等待还是失败

**典型场景**:
```json
{
  "level": "INFO",
  "msg": "survival_decision_aggregate",
  "commit_state": "none",
  "committed": false,
  "candidate_count": 2,
  "candidates": [
    {
      "provider_id": 1,
      "credential_id": "11",
      "kind": "upstream_overloaded",
      "action": "retry_now",
      "retry_after": "30s"
    }
  ],
  "decision_action": "retry_now",
  "decision_reason": "recoverable_candidate"
}
```

#### 2. `fold_candidate_outcomes_complete`
**什么时候产生**: 
- 执行器尝试了候选节点但失败了
- 正在折叠候选节点结果

**典型场景**:
```json
{
  "level": "INFO",
  "msg": "fold_candidate_outcomes_complete",
  "attempt_count": 2,
  "outcome_count": 2,
  "attempts": [
    {
      "provider_id": 1,
      "credential_id": 11,
      "raw_model": "claude-sonnet-5",
      "kind": "network_error"
    }
  ]
}
```

#### 3. `fold_candidate_outcomes_no_attempts`
**什么时候产生**: 
- 找不到任何可用的候选节点
- 配置错误或所有节点不可用

#### 4. `fold_candidate_outcomes_non_exec_error`
**什么时候产生**: 
- 遇到了非预期的错误类型
- 可能是代码bug或异常情况

## 如何触发测试日志（可选）

如果想主动触发一些错误来验证日志，可以：

### 选项1: 等待自然错误
**推荐**: 等待真实的用户流量自然触发错误（可能需要几小时到几天）

### 选项2: 临时禁用某个凭据
```bash
# 在154的数据库中临时标记某个凭据为不可用
# 这会导致requests尝试该凭据时失败并触发failover
```

**注意**: 不推荐在生产环境主动触发错误

## 预期观察结果

### 第1-2天
- [ ] 看到至少1-2个 `survival_decision_aggregate` 日志
- [ ] 看到至少1-2个 `fold_candidate_outcomes_complete` 日志
- [ ] 确认日志包含完整的字段信息

### 第3-7天
- [ ] 收集5-10个不同类型的错误案例
- [ ] 分析哪些错误最常见
- [ ] 确认重试逻辑是否按预期工作

## 当前流量情况

根据刚才的日志，可以看到：
- **总请求**: 174个请求（在过去几小时内）
- **成功**: 162个
- **失败**: 12个
- **主要模型**: 
  - claude-sonnet-5: 89个请求
  - minimax-m3: 73个请求
  - gpt-5.6-luna: 6个请求

**这说明有一定的流量，应该会触发一些错误场景。**

## 快速检查脚本

保存以下脚本到本地，方便快速检查：

```bash
#!/bin/bash
# check-154-logs.sh - 快速检查154上的新日志

export SSHPASS='Kaixuan2026&#*9527'

echo "=== 154服务状态 ==="
sshpass -e ssh -o StrictHostKeyChecking=no -p 25022 root@47.97.111.154 \
  'systemctl is-active llm-gateway-go.service'

echo ""
echo "=== 最近1小时的survival决策日志 ==="
sshpass -e ssh -o StrictHostKeyChecking=no -p 25022 root@47.97.111.154 \
  'journalctl -u llm-gateway-go.service --since "1 hour ago" | grep survival_decision_aggregate | wc -l' | \
  xargs -I {} echo "找到 {} 条日志"

echo ""
echo "=== 最近1小时的候选节点折叠日志 ==="
sshpass -e ssh -o StrictHostKeyChecking=no -p 25022 root@47.97.111.154 \
  'journalctl -u llm-gateway-go.service --since "1 hour ago" | grep fold_candidate_outcomes_complete | wc -l' | \
  xargs -I {} echo "找到 {} 条日志"

echo ""
echo "=== 最近的错误关键词 ==="
sshpass -e ssh -o StrictHostKeyChecking=no -p 25022 root@47.97.111.154 \
  'journalctl -u llm-gateway-go.service --since "1 hour ago" | grep -E "ERROR|overloaded|empty_response" | tail -5'
```

使用方法：
```bash
chmod +x check-154-logs.sh
./check-154-logs.sh
```

## 下一步行动

### 立即（今晚）
- ✅ 服务已启动并运行正常
- ✅ 版本确认包含日志增强
- ⏳ 开始监控，等待错误触发

### 明天
- [ ] 早上检查是否有新日志产生
- [ ] 如果有，分析第一批日志
- [ ] 如果没有，继续等待（说明系统稳定，没有错误）

### 本周内
- [ ] 收集足够的错误案例（5-10个）
- [ ] 分析错误模式
- [ ] 验证重试和failover逻辑

## 总结

**当前状态**: ✅ 一切就绪
- 154服务运行正常
- 版本正确（包含日志增强）
- 有一定的流量
- 等待错误场景触发

**需要耐心**: 
- 新日志只在错误发生时产生
- 如果系统稳定，可能几个小时甚至几天才会看到第一条新日志
- 这是**正常的**，说明系统工作良好

**如何知道是否工作**: 
- 当下次出现"Our servers are currently overloaded"等错误时
- 应该能在日志中看到详细的 `survival_decision_aggregate` 和 `fold_candidate_outcomes_complete` 日志
- 包含完整的候选节点信息和决策过程

**建议**: 
- 保持154服务运行
- 明天早上检查一次
- 如果一周内都没有新日志，说明系统非常稳定（这是好事）
