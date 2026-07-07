# LLM Gateway 本地压力测试环境 - 项目总结

## 📋 任务完成清单

✅ **代码拉取** - 已更新到最新版本  
✅ **Mock供应商服务** - 4个mock已运行 (19080-19083)  
✅ **数据库配置** - Credentials已成功注入  
✅ **压力测试脚本** - 完整的测试工具链已就绪  
✅ **文档** - 详细的使用指南已创建  

---

## 🏗️ 已创建的文件

### 1. 核心测试脚本

| 文件 | 用途 |
|------|------|
| `scripts/comprehensive-loadtest.sh` | **完整压力测试套件** - 启动12个mock，运行多场景测试 |
| `scripts/quick-start-loadtest.sh` | **快速启动脚本** - 日常开发测试，4个mock快速验证 |
| `scripts/verify-loadtest-env.sh` | **环境验证脚本** - 检查依赖和环境配置 |
| `sql/scripts/04-loadtest-mock-credentials-fixed.sql` | **修复版SQL** - 适配当前数据库schema |

### 2. 文档

| 文件 | 内容 |
|------|------|
| `docs/LOADTEST_GUIDE.md` | **完整使用指南** (7000+字) - 包含测试架构、场景说明、故障排查 |

### 3. 已有工具（项目原有）

| 文件 | 用途 |
|------|------|
| `scripts/mocks/llm-mock-upstream/server-v2.py` | Mock供应商服务 (10种故障模式) |
| `scripts/loadtest-stress.py` | 压力测试客户端 (支持20+并发) |
| `scripts/mock-state-orchestrator.sh` | Mock状态控制工具 |
| `scripts/loadtest-multiround.sh` | 多轮测试运行器 |

---

## 🚀 快速开始

### 方式1: 直连Mock测试（不经过Gateway）

```bash
# 验证环境
./scripts/verify-loadtest-env.sh

# 直接测试4个mock（验证mock工作正常）
python3 scripts/loadtest-stress.py \
    --clients 10 \
    --rounds 20 \
    --mode direct \
    --mocks "http://localhost:19080,http://localhost:19081,http://localhost:19082,http://localhost:19083"
```

**预期结果**: 
- 成功率 > 99%
- 延迟 P50 < 500ms
- Mock分布均匀（每个mock约25%）

### 方式2: 通过Gateway完整测试

```bash
# 1. 启动Gateway（需要配置.env文件）
./gateway

# 2. 运行快速测试
./scripts/quick-start-loadtest.sh

# 或运行完整测试套件
./scripts/comprehensive-loadtest.sh all
```

---

## 🎯 测试架构

```
┌──────────────────────┐
│  压力测试客户端       │  (25 clients × 100 rounds = 2,500 requests)
│  loadtest-stress.py  │
└──────────┬───────────┘
           │
           ↓ HTTP requests with session ID
           │
┌──────────┴───────────┐
│   LLM Gateway        │  (localhost:8080)
│   - Routing          │
│   - Sticky Session   │
│   - Failover         │
│   - Circuit Breaker  │
└──────────┬───────────┘
           │
           ↓ Route to mock providers
           │
    ┌──────┴──────┬──────────┬──────────┐
    ↓             ↓          ↓          ↓
┌─────────┐  ┌─────────┐  ┌─────────┐  ┌─────────┐
│ Mock-A  │  │ Mock-B  │  │ Mock-C  │  │ Mock-D  │
│ :19080  │  │ :19081  │  │ :19082  │  │ :19083  │
│ gpt-4o  │  │ gpt-4o  │  │ gpt-4o  │  │ gpt-4o  │
└─────────┘  └─────────┘  └─────────┘  └─────────┘
```

---

## 📊 测试场景

### 场景1: 基准性能测试
- **配置**: 所有mock健康
- **目标**: 测试理想状态下的性能上限
- **预期**: 成功率>99%, Sticky>95%, P95<1000ms

### 场景2: Slow故障测试  
- **配置**: 30% mock slow (5-10s延迟)
- **目标**: 验证超时和故障转移
- **预期**: 成功率>95%, Gateway路由到健康mock

### 场景3: Rate Limit测试
- **配置**: 20% mock rate_limited (429错误)
- **目标**: 验证429错误处理和重试
- **预期**: 成功率>90%, 自动重试到其他mock

### 场景4: 混合故障测试
- **配置**: 30% flaky + 20% server_error + 50% healthy
- **目标**: 模拟生产复杂故障
- **预期**: 成功率>85%, 智能路由

### 场景5: 动态故障注入
- **配置**: 运行中每10轮随机注入故障
- **目标**: 测试突发故障应对
- **预期**: 成功率>80%, 动态调整路由

---

## 🛠️ 常用命令

### Mock控制

```bash
# 查看所有mock健康状态
./scripts/mock-state-orchestrator.sh health-all

# 设置单个mock为slow模式
./scripts/mock-state-orchestrator.sh set http://localhost:19080 slow 30

# 设置所有mock为健康状态
./scripts/mock-state-orchestrator.sh set-all healthy

# 重置所有mock
./scripts/mock-state-orchestrator.sh reset-all

# 查看mock指标
curl http://localhost:19080/admin/metrics | jq
```

### 压力测试

```bash
# 基础测试 (10客户端 × 20轮)
python3 scripts/loadtest-stress.py \
    --clients 10 \
    --rounds 20 \
    --mode direct \
    --mocks "http://localhost:19080,http://localhost:19081,http://localhost:19082,http://localhost:19083"

# 通过Gateway测试 (需要API key)
python3 scripts/loadtest-stress.py \
    --clients 20 \
    --rounds 50 \
    --mode gateway \
    --gateway "http://localhost:8080" \
    --api-key "your-api-key" \
    --model "gpt-4o"

# 带动态故障注入
python3 scripts/loadtest-stress.py \
    --clients 20 \
    --rounds 50 \
    --mode gateway \
    --gateway "http://localhost:8080" \
    --model "gpt-4o" \
    --fault-injection
```

### 数据库操作

```bash
# 注入测试credentials
psql -d llm_gateway -f sql/scripts/04-loadtest-mock-credentials-fixed.sql

# 查看已配置的mock providers
psql -d llm_gateway -c "SELECT p.code, p.base_url, pm.raw_model_name, c.label 
FROM providers p 
JOIN provider_models pm ON pm.provider_id = p.id 
JOIN credentials c ON c.provider_id = p.id 
WHERE p.id BETWEEN 9010 AND 9013;"

# 清理测试数据
psql -d llm_gateway -c "
DELETE FROM credential_model_bindings WHERE credential_id BETWEEN 9010 AND 9013;
DELETE FROM provider_models WHERE provider_id BETWEEN 9010 AND 9013;
DELETE FROM credentials WHERE id BETWEEN 9010 AND 9013;
DELETE FROM providers WHERE id BETWEEN 9010 AND 9013;
"
```

---

## 📈 性能指标说明

### 成功率 (Success Rate)
- **> 99%**: 优秀 (理想状态)
- **95-99%**: 良好 (轻度故障)
- **85-95%**: 可接受 (中度故障)
- **< 85%**: 需要调查

### Sticky准确率 (Sticky Accuracy)
- **> 95%**: 优秀 (路由一致性高)
- **70-95%**: 良好 (有故障转移)
- **< 70%**: 需要调查

### 延迟 (Latency)
基准值 (healthy状态):
- **P50**: < 500ms
- **P95**: < 1000ms  
- **P99**: < 1500ms

---

## 🔍 当前环境状态

### Mock服务
```
✓ mock-00 (19080) - 运行中
✓ mock-01 (19081) - 运行中
✓ mock-02 (19082) - 运行中
✓ mock-03 (19083) - 运行中
```

### 数据库
```
✓ llm_gateway 数据库存在
✓ 4个loadtest credentials已配置 (ID: 9010-9013)
✓ 4个mock providers已配置
✓ 4个provider_models已配置 (gpt-4o)
```

### Gateway
```
○ 未启动 - 需要配置.env文件
```

---

## ⚠️ 重要说明

### Gateway启动前提条件

1. **配置.env文件**
   ```bash
   cp .env.example .env
   # 编辑 .env，配置数据库连接等
   ```

2. **数据库schema**
   - 确保已执行 `sql/01-schema.sql`
   - 确保已执行 `sql/02-seed.sql`

3. **启动Gateway**
   ```bash
   ./gateway
   # 或后台运行
   ./gateway > /tmp/gateway.log 2>&1 &
   ```

### Mock状态说明

Mock支持10种状态模式：
- `healthy` - 正常响应 (200-500ms)
- `slow` - 慢响应 (5-30s)
- `rate_limited` - 返回429错误
- `quota_exceeded` - 返回429配额错误
- `auth_error` - 返回401错误
- `server_error` - 50%概率返回500
- `timeout` - 永久挂起 (模拟超时)
- `connection_refused` - 拒绝连接
- `broken_stream` - 流式响应中断
- `flaky` - 30%概率失败

---

## 📚 详细文档

完整的使用指南、故障排查和高级用法，请参考：

```bash
cat docs/LOADTEST_GUIDE.md
```

或在线查看：`docs/LOADTEST_GUIDE.md`

---

## 🎉 总结

本项目提供了一套完整的本地压力测试和场景验证方案：

✅ **完整的Mock基础设施** - 支持10+供应商，10种故障模式  
✅ **灵活的压力测试工具** - 支持20+并发客户端，多轮会话  
✅ **动态故障注入** - 模拟生产环境随机故障  
✅ **详细的性能指标** - P50/P95/P99延迟，成功率，Sticky准确率  
✅ **自动化测试流程** - 一键运行完整测试套件  
✅ **完善的文档** - 使用指南，故障排查，高级用法  

现在你可以轻松地在本地进行全面的压力测试和场景验证！

---

**最后更新**: 2026-07-07  
**维护者**: Kiro AI Assistant
