# LLM Gateway 超时优化全面测试方案

## 测试目标

验证超时优化项目的稳定性、容错性和性能表现，覆盖正常场景和各种异常场景。

---

## 测试环境

### 本地测试环境

- **Gateway**: localhost:8080
- **数据库**: 172.16.2.210:5432/llm_gateway
- **模拟延迟**: 使用测试工具注入延迟
- **错误注入**: 模拟4xx/5xx错误

### 测试工具

- **curl**: HTTP请求
- **wrk**: 压力测试
- **vegeta**: 负载测试
- **psql**: 数据库验证

---

## 测试场景分类

### 场景1: 正常请求（基线）

**目标**: 建立性能基线，验证基本功能

#### 1.1 小请求 (< 10K tokens)

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [
      {"role": "user", "content": "Hello, how are you?"}
    ],
    "stream": true
  }' --no-buffer
```

**预期**:
- ✅ 响应时间: 2-5秒
- ✅ effective_timeout_seconds: 90秒
- ✅ timeout_mode: "adaptive" 或 "static"
- ✅ context_size_tokens: < 100
- ✅ success: true

#### 1.2 中等请求 (10K-20K tokens)

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [
      {"role": "user", "content": "'"$(python3 -c "print('请详细解释量子计算的原理。' * 500)")"'"}
    ],
    "stream": true
  }' --no-buffer
```

**预期**:
- ✅ 响应时间: 10-20秒
- ✅ effective_timeout_seconds: 90秒
- ✅ context_size_tokens: 10,000-20,000
- ✅ success: true

#### 1.3 大请求 (> 20K tokens)

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [
      {"role": "user", "content": "'"$(python3 -c "print('请详细解释量子计算、相对论和弦理论的关系，包括历史发展、数学基础、实验验证等方面。' * 1000)")"'"}
    ],
    "stream": true
  }' --no-buffer
```

**预期**:
- ✅ 响应时间: 30-60秒
- ✅ effective_timeout_seconds: 135秒 (90 + 45)
- ✅ context_size_tokens: > 20,000
- ✅ timeout_mode: "adaptive"
- ✅ success: true

---

### 场景2: 延迟场景

**目标**: 验证超时配置和自适应调整

#### 2.1 轻微延迟 (5-10秒)

**模拟方式**: 选择慢速模型或节点

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [
      {"role": "user", "content": "解释相对论"}
    ],
    "stream": true
  }' --no-buffer
```

**预期**:
- ✅ 响应时间: 5-10秒
- ✅ TTFB (Time To First Byte): 5-10秒
- ✅ keepalive事件: 至少1个 (如果 >15秒)
- ✅ success: true
- ✅ 历史延迟记录到request_logs

#### 2.2 中等延迟 (20-40秒)

```bash
# 发送复杂请求到慢速模型
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [
      {"role": "user", "content": "'"$(python3 -c "print('详细分析量子力学的发展历史，包括主要人物、关键实验、理论突破。' * 100)")"'"}
    ],
    "stream": true
  }' --no-buffer
```

**预期**:
- ✅ 响应时间: 20-40秒
- ✅ keepalive事件: 2-3个
- ✅ effective_timeout_seconds: 135-145秒 (考虑历史延迟)
- ✅ success: true

#### 2.3 接近超时 (80-90秒)

```bash
# 超大请求到慢速模型
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [
      {"role": "system", "content": "你是一个详细的科学讲解专家，每个问题都要详细解释。"},
      {"role": "user", "content": "'"$(python3 -c "print('请详细解释量子力学、相对论、弦理论、标准模型的关系和发展历史，包括所有重要公式和实验。' * 500)")"'"}
    ],
    "stream": true,
    "max_tokens": 4000
  }' --no-buffer
```

**预期**:
- ✅ 响应时间: 80-90秒
- ✅ keepalive事件: 5-6个
- ✅ effective_timeout_seconds: 145-180秒
- ✅ timeout_mode: "adaptive"
- ✅ success: true (不应超时)

---

### 场景3: 超时场景

**目标**: 验证超时处理和重试机制

#### 3.1 单节点超时

**模拟方式**: 请求一个已知会超时的模型/节点

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "slow-test-model",
    "messages": [
      {"role": "user", "content": "test timeout"}
    ],
    "stream": true
  }' --no-buffer -m 95
```

**预期**:
- ✅ 95秒后超时
- ✅ error_code: "timeout"
- ✅ timeout_reason: 记录到request_logs
- ✅ 触发重试（如果有备用节点）
- ✅ node_switch事件（如果重试）

#### 3.2 多节点超时重试

```bash
# 会触发多次重试的请求
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [
      {"role": "user", "content": "超大请求..."}
    ],
    "stream": true,
    "max_tokens": 4000
  }' --no-buffer
```

**预期**:
- ✅ 第一次尝试超时
- ✅ node_switch事件发出
- ✅ 切换到备用节点
- ✅ 第二次尝试成功
- ✅ attempt_num: 2
- ✅ final_credential_id != initial_credential_id

---

### 场景4: 4xx错误场景

**目标**: 验证客户端错误处理

#### 4.1 无效API Key (401)

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer invalid-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [
      {"role": "user", "content": "test"}
    ]
  }'
```

**预期**:
- ✅ HTTP 401
- ✅ error.code: "invalid_api_key"
- ✅ 不应触发重试
- ✅ latency_ms < 100

#### 4.2 模型不存在 (404)

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "non-existent-model",
    "messages": [
      {"role": "user", "content": "test"}
    ]
  }'
```

**预期**:
- ✅ HTTP 404
- ✅ error.code: "model_not_found"
- ✅ 可能触发sticky cache清理
- ✅ latency_ms < 500

#### 4.3 请求过大 (413)

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [
      {"role": "user", "content": "'"$(python3 -c "print('x' * 1000000)")"'"}
    ]
  }'
```

**预期**:
- ✅ HTTP 413 或 400
- ✅ error.code: "context_length_exceeded"
- ✅ 不应触发重试
- ✅ latency_ms < 100

#### 4.4 速率限制 (429)

```bash
# 连续快速发送请求
for i in {1..20}; do
  curl -X POST http://localhost:8080/v1/chat/completions \
    -H "Authorization: Bearer test-key" \
    -H "Content-Type: application/json" \
    -d '{
      "model": "gpt-3.5-turbo",
      "messages": [{"role": "user", "content": "test '$i'"}]
    }' &
done
wait
```

**预期**:
- ✅ 部分请求返回 HTTP 429
- ✅ error.code: "rate_limit_exceeded"
- ✅ Retry-After header
- ✅ 应触发exponential backoff重试
- ✅ 最终部分请求成功

---

### 场景5: 5xx错误场景

**目标**: 验证服务端错误处理和重试

#### 5.1 上游服务不可用 (502/503)

```bash
# 请求一个已知不可用的节点
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [
      {"role": "user", "content": "test"}
    ]
  }'
```

**预期**:
- ✅ 自动切换到备用节点
- ✅ node_switch事件
- ✅ 最终成功或返回503
- ✅ attempt_num: 2-3
- ✅ total_latency_ms包含所有尝试

#### 5.2 上游超载 (503)

```bash
# 高并发请求
seq 1 50 | xargs -P 50 -I {} curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "test {}"}]
  }'
```

**预期**:
- ✅ 部分请求可能503
- ✅ 触发load balancing
- ✅ P2C算法选择低负载节点
- ✅ 大部分请求最终成功
- ✅ 平均成功率 > 95%

#### 5.3 上游内部错误 (500)

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [
      {"role": "user", "content": "触发内部错误的特殊内容"}
    ]
  }'
```

**预期**:
- ✅ 触发重试（最多3次）
- ✅ 切换到不同节点
- ✅ 如果所有节点都失败，返回500
- ✅ 记录详细错误信息

---

### 场景6: 继续请求场景

**目标**: 验证ContinuationDetector

#### 6.1 中文继续请求

```bash
# 第一次请求
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [
      {"role": "user", "content": "解释量子计算"}
    ]
  }'

# 继续请求
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [
      {"role": "user", "content": "解释量子计算"},
      {"role": "assistant", "content": "量子计算是..."},
      {"role": "user", "content": "请继续"}
    ]
  }'
```

**预期**:
- ✅ is_continuation: true
- ✅ 检测到"请继续"关键词
- ✅ 正常处理请求

#### 6.2 英文继续请求

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [
      {"role": "user", "content": "Explain quantum computing"},
      {"role": "assistant", "content": "Quantum computing..."},
      {"role": "user", "content": "continue"}
    ]
  }'
```

**预期**:
- ✅ is_continuation: true
- ✅ 检测到"continue"关键词

---

### 场景7: Keepalive场景

**目标**: 验证KeepaliveSender

#### 7.1 长时间流式请求

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [
      {"role": "user", "content": "'"$(python3 -c "print('请详细讲解量子力学的发展历史。' * 200)")"'"}
    ],
    "stream": true,
    "max_tokens": 3000
  }' --no-buffer
```

**预期**:
- ✅ 每15秒收到`event: keepalive`
- ✅ keepalive数据格式正确
- ✅ keepalive_sent_count > 0
- ✅ 连接保持活跃
- ✅ 无代理超时

#### 7.2 节点切换通知

```bash
# 请求会触发重试的模型
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [
      {"role": "user", "content": "test"}
    ],
    "stream": true
  }' --no-buffer
```

**预期**:
- ✅ 如果切换节点，收到`event: node_switch`
- ✅ 包含from_node、to_node、attempt
- ✅ 前端可以显示切换提示

---

### 场景8: 配置热加载场景

**目标**: 验证配置热加载

#### 8.1 修改基础超时

```bash
# 1. 修改配置
psql -h 172.16.2.210 -U llm_gateway -d llm_gateway <<EOF
UPDATE system_settings 
SET value = '120'
WHERE key = 'timeout.upstream_base_seconds';
EOF

# 2. 等待30秒
sleep 30

# 3. 发送测试请求
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "test"}]
  }'
```

**预期**:
- ✅ 日志显示"timeout config reloaded"
- ✅ effective_timeout_seconds: 120
- ✅ 无需重启服务

#### 8.2 修改超时模式

```bash
# 切换到context_aware模式
psql -h 172.16.2.210 -U llm_gateway -d llm_gateway <<EOF
UPDATE system_settings 
SET value = '"context_aware"'
WHERE key = 'timeout.dynamic_mode';
EOF

sleep 30

# 测试
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [
      {"role": "user", "content": "'"$(python3 -c "print('test ' * 10000)")"'"}
    ]
  }'
```

**预期**:
- ✅ timeout_mode: "context_aware"
- ✅ 大请求超时增加

---

### 场景9: 压力测试

**目标**: 验证高并发稳定性

#### 9.1 中等并发 (100 QPS)

```bash
wrk -t4 -c100 -d30s --latency \
  -s wrk_scripts/chat_completion.lua \
  http://localhost:8080/v1/chat/completions
```

**预期**:
- ✅ P99延迟 < 10秒
- ✅ 错误率 < 1%
- ✅ 无goroutine泄漏
- ✅ 内存稳定

#### 9.2 高并发 (500 QPS)

```bash
vegeta attack -rate=500 -duration=60s \
  -targets=vegeta_targets/chat.txt \
  -output=results.bin

vegeta report results.bin
```

**预期**:
- ✅ P99延迟 < 20秒
- ✅ 成功率 > 95%
- ✅ 超时率 < 2%
- ✅ CPU < 80%

---

### 场景10: 数据验证

**目标**: 验证数据记录完整性

#### 10.1 检查新字段

```sql
-- 验证effective_timeout_seconds
SELECT 
    effective_timeout_seconds,
    COUNT(*) as count,
    AVG(latency_ms) as avg_latency
FROM request_logs
WHERE ts > NOW() - INTERVAL '1 hour'
  AND effective_timeout_seconds IS NOT NULL
GROUP BY effective_timeout_seconds
ORDER BY effective_timeout_seconds;
```

**预期**:
- ✅ 90秒、135秒、180秒等不同档位
- ✅ 大请求对应高超时
- ✅ 无NULL值（新请求）

#### 10.2 检查超时模式分布

```sql
SELECT 
    timeout_mode,
    COUNT(*) as count,
    AVG(effective_timeout_seconds) as avg_timeout,
    AVG(latency_ms) as avg_latency
FROM request_logs
WHERE ts > NOW() - INTERVAL '1 hour'
  AND timeout_mode IS NOT NULL
GROUP BY timeout_mode;
```

**预期**:
- ✅ adaptive模式占主导
- ✅ 不同模式的超时分布合理

#### 10.3 检查继续请求

```sql
SELECT 
    COUNT(*) FILTER(WHERE is_continuation) as continuation_count,
    COUNT(*) as total_count,
    ROUND(100.0 * COUNT(*) FILTER(WHERE is_continuation) / COUNT(*), 2) as continuation_rate
FROM request_logs
WHERE ts > NOW() - INTERVAL '1 hour';
```

**预期**:
- ✅ 5-10%请求为继续请求
- ✅ is_continuation准确

#### 10.4 检查keepalive

```sql
SELECT 
    AVG(keepalive_sent_count) as avg_keepalive,
    MAX(keepalive_sent_count) as max_keepalive
FROM request_logs
WHERE ts > NOW() - INTERVAL '1 hour'
  AND is_stream = true
  AND success = true;
```

**预期**:
- ✅ 平均1-3个keepalive
- ✅ 长请求更多keepalive

---

## 测试执行计划

### 阶段1: 基础功能测试 (30分钟)

1. 场景1: 正常请求 (3个测试)
2. 场景6: 继续请求 (2个测试)
3. 场景10: 数据验证 (4个测试)

### 阶段2: 异常处理测试 (45分钟)

4. 场景2: 延迟场景 (3个测试)
5. 场景3: 超时场景 (2个测试)
6. 场景4: 4xx错误 (4个测试)
7. 场景5: 5xx错误 (3个测试)

### 阶段3: 高级功能测试 (30分钟)

8. 场景7: Keepalive (2个测试)
9. 场景8: 配置热加载 (2个测试)

### 阶段4: 压力测试 (30分钟)

10. 场景9: 压力测试 (2个测试)

**总计**: 约2小时

---

## 验收标准

### 功能完整性

- [ ] 所有正常请求成功
- [ ] 超时配置生效
- [ ] Keepalive正常发送
- [ ] 继续请求检测准确
- [ ] 配置热加载生效

### 稳定性

- [ ] 错误率 < 1%
- [ ] 超时率 < 2%
- [ ] 无goroutine泄漏
- [ ] 无内存泄漏

### 性能

- [ ] P99延迟 < 20秒
- [ ] 支持 >500 QPS
- [ ] CPU < 80%
- [ ] 内存增长 < 10MB/hour

### 数据完整性

- [ ] 所有新字段有数据
- [ ] 数据分布合理
- [ ] 无脏数据

---

## 测试报告模板

```markdown
# 测试执行报告

## 执行信息
- 日期: YYYY-MM-DD
- 执行人: XXX
- 环境: local/154
- 版本: git commit hash

## 测试结果

### 场景1: 正常请求
- 1.1 小请求: ✅ PASS
- 1.2 中等请求: ✅ PASS  
- 1.3 大请求: ✅ PASS

### 场景2: 延迟场景
- ...

## 问题清单

| ID | 场景 | 问题描述 | 严重性 | 状态 |
|----|------|----------|--------|------|
| 1  | 场景X | XXX | P0/P1/P2 | Open/Fixed |

## 性能数据

| 指标 | 目标 | 实际 | 达标 |
|------|------|------|------|
| 超时率 | <2% | X% | ✅/❌ |
| P99延迟 | <20s | Xs | ✅/❌ |

## 总结

- 通过: X/Y
- 失败: X/Y
- 总体评估: PASS/FAIL
```

---

**测试准备就绪！开始执行！** 🚀
