# Minimax-M3 通过网关超时问题分析报告

**日期**: 2026-07-23  
**环境**: 154生产环境 (llm.kxpms.cn)  
**问题**: Minimax-M3模型通过网关频繁出现第一字节超时和流中断问题

---

## 一、问题现象

### 1.1 用户反馈
- 直连 minimax-m3 或 nvidia nim：正常（仅有快慢差异）
- 通过网关 llm.kxpms.cn 连接：频繁出错
  - **第一字节超时** (first_byte_timeout)
  - **任务执行一半自动停止** (eof_without_done)

### 1.2 日志证据

从154服务器日志中发现大量超时错误：

```json
// 第一字节超时 - NVIDIA NIM路由 (credential_id=23, provider_id=18)
{
  "time": "2026-07-23T12:55:46.815554347+08:00",
  "level": "WARN",
  "msg": "executor: stream interrupted",
  "credential_id": 23,
  "provider_id": 18,
  "reason": "first_byte_timeout",
  "chunk_count": 0,
  "resumable": true,
  "classified_as": "stream_timeout",
  "benign_eof": false
}

// 流中断 - MiniMax直连 (credential_id=21, provider_id=14)
{
  "time": "2026-07-23T12:55:49.703667+08:00",
  "level": "WARN",
  "msg": "executor: stream interrupted",
  "credential_id": 21,
  "provider_id": 14,
  "reason": "eof_without_done",
  "chunk_count": 6,
  "resumable": false,
  "classified_as": "stream_timeout",
  "benign_eof": true
}
```

**统计数据（12:50-13:03时间段）**:
- `eof_without_done` 错误: 18次
- `first_byte_timeout` 错误: 1次
- 主要影响凭据:
  - credential_id=21 (MiniMax直连): 17次
  - credential_id=23 (NVIDIA NIM): 1次

---

## 二、根因分析

### 2.1 核心问题

**问题1: 第一字节超时配置过短**
- **当前配置**: `FirstByteTimeout = 30秒` (默认值)
- **日志提示**: 
  ```
  "stream first-byte timeout"
  "hint": "if frequent, increase LLM_GATEWAY_FIRST_BYTE_TIMEOUT or admin config (default 120s)"
  ```
- **问题**: Minimax-M3模型较大，初始化响应可能需要更长时间，30秒不足

**问题2: 流中断 (eof_without_done)**
- **原因**: Minimax上游在流结束时未发送标准的 `[DONE]` 标记
- **chunk_count**: 从6到421不等，说明是在流传输中途或末尾中断
- **benign_eof=true**: 网关认为这是"良性EOF"（即正常结束），但缺少完成标记
- **分类**: 被错误分类为 `stream_timeout`，实际可能是协议不兼容

**问题3: 数据库约束错误**
```json
{
  "msg": "persist request_logs_bodies_hot failed",
  "error": "ERROR: there is no unique or exclusion constraint matching the ON CONFLICT specification (SQLSTATE 42P10)"
}
```
- 这会导致遥测数据丢失，影响后续分析

### 2.2 配置文件分析

**当前默认配置** (`config/config.go:240-250`):
```go
UpstreamTimeout:    120,  // 上游总超时
StreamTimeout:      900,  // 流总超时（15分钟）
StreamChunkTimeout: 300,  // 单个chunk超时（5分钟）
FirstByteTimeout:   30,   // 第一字节超时 ⚠️ 问题所在
KeepaliveInterval:  15,   // Keepalive间隔
```

**154环境检查**:
- `.env`文件中**未设置**超时相关环境变量
- 使用代码硬编码的默认值

### 2.3 凭据路由情况

根据日志，Minimax-M3有4个候选凭据：
1. **credential_id=21** (MiniMax直连, provider_id=14)
   - 主要路由目标
   - 频繁出现 `eof_without_done`
   - URL: `https://api.minimaxi.com/v1/chat/completions`

2. **credential_id=23** (NVIDIA NIM, provider_id=18)
   - 备用路由
   - 出现 `first_byte_timeout`
   - URL: `https://integrate.api.nvidia.com/v1/chat/completions`
   - 被熔断器打开: `"cooling_until":"2026-07-23T12:56:16+08:00"`

3. credential_id=11 (火山方舟 TokenPlan)
4. credential_id=29 (普联)

---

## 三、解决方案

### 3.1 立即修复（生产环境）

#### 方案A: 增加第一字节超时配置

在154服务器上添加环境变量：

```bash
# SSH到154服务器
ssh 154

# 编辑.env文件（如果不存在则创建）
cd /opt/llm-gateway-go
cat >> .env << 'EOF'

# Minimax-M3超时优化配置 - 2026-07-23
# 第一字节超时从30秒增加到120秒
LLM_GATEWAY_FIRST_BYTE_TIMEOUT=120

# 可选：增加单个chunk超时（默认300秒可能也不够）
LLM_GATEWAY_STREAM_CHUNK_TIMEOUT=600

# 可选：启用流前keepalive，防止客户端超时
LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE=true
EOF

# 重启网关
systemctl restart llm-gateway

# 查看日志
journalctl -u llm-gateway -f
```

#### 方案B: 通过数据库动态配置

如果网关支持从数据库读取配置，可以通过admin接口更新：

```sql
-- 假设有system_settings表
UPDATE system_settings 
SET value = '120' 
WHERE key = 'first_byte_timeout_seconds';
```

### 3.2 代码修复（长期方案）

#### 修复1: 调整默认超时值

编辑 `config/config.go`:

```go
// 针对大模型优化的超时配置
FirstByteTimeout:   120,  // 30 → 120秒
StreamChunkTimeout: 600,  // 300 → 600秒（10分钟）
```

#### 修复2: 处理Minimax的EOF协议差异

编辑 `domains/streaming/stream.go` 或相关流处理器，增加对Minimax特殊行为的处理：

```go
// 针对Minimax的benign EOF处理
if outcome.Reason == "eof_without_done" && outcome.BenignEOF && chunkCount > 0 {
    // Minimax经常不发送[DONE]，但如果已经接收到内容，视为成功
    outcome.Resumable = false  // 不需要重试
    outcome.Success = true     // 标记为成功
    slog.Info("minimax_eof_workaround", 
        "chunk_count", chunkCount,
        "credential_id", credID)
}
```

#### 修复3: 修复数据库约束错误

检查并修复 `request_logs_bodies_hot` 表的约束：

```sql
-- 检查表结构
\d request_logs_bodies_hot;

-- 添加缺失的唯一约束或主键
-- 假设request_id应该是唯一的
ALTER TABLE request_logs_bodies_hot 
ADD CONSTRAINT request_logs_bodies_hot_pkey 
PRIMARY KEY (request_id);
```

### 3.3 监控和验证

部署后监控以下指标：

```bash
# 监控第一字节超时
ssh 154 "grep 'first_byte_timeout' /opt/llm-gateway-go/logs/gateway.log | tail -20"

# 监控eof_without_done
ssh 154 "grep 'eof_without_done' /opt/llm-gateway-go/logs/gateway.log | wc -l"

# 监控Minimax成功率
ssh 154 "grep 'minimax-m3' /opt/llm-gateway-go/logs/gateway.log | grep -E 'upstream_status|stream interrupted' | tail -30"
```

---

## 四、实施步骤

### 阶段1: 紧急修复（15分钟）

1. ✅ 分析日志，确认问题（已完成）
2. ⏳ SSH到154服务器
3. ⏳ 添加超时环境变量到 `.env`
4. ⏳ 重启网关服务
5. ⏳ 验证日志中超时错误是否减少

### 阶段2: 验证和监控（24小时）

1. 持续监控日志
2. 收集用户反馈
3. 对比超时错误率

### 阶段3: 代码优化（1-2天）

1. 修改默认配置
2. 添加Minimax协议适配器
3. 修复数据库约束
4. 编写测试用例
5. 部署到测试环境验证

---

## 五、风险评估

### 5.1 修改超时配置的风险

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| 增加客户端等待时间 | 低 | 仅影响首字节，不影响后续流传输 |
| 占用更多网关资源 | 低 | 120秒仍在合理范围内 |
| 影响其他模型 | 极低 | 其他模型响应快，不受影响 |

### 5.2 回滚方案

如果修复导致新问题：

```bash
# 删除环境变量
cd /opt/llm-gateway-go
sed -i '/LLM_GATEWAY_FIRST_BYTE_TIMEOUT/d' .env
sed -i '/LLM_GATEWAY_STREAM_CHUNK_TIMEOUT/d' .env
sed -i '/LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE/d' .env

# 重启恢复默认值
systemctl restart llm-gateway
```

---

## 六、后续优化建议

1. **模型特定配置**: 支持per-model超时配置
2. **智能重试**: 对eof_without_done错误实现智能重试逻辑
3. **协议适配层**: 为不同供应商实现协议适配器
4. **监控告警**: 增加超时率告警阈值
5. **文档完善**: 在运维手册中记录大模型超时调优经验

---

## 七、参考信息

- **日志路径**: `/opt/llm-gateway-go/logs/gateway.log`
- **配置文件**: `config/config.go`
- **流处理器**: `domains/streaming/stream.go`
- **执行器**: `domains/streaming/executors/executor_chat.go`
- **相关Provider**: 
  - Provider 14: MiniMax (api.minimaxi.com)
  - Provider 18: NVIDIA NIM (integrate.api.nvidia.com)
