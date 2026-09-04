# 本地服务日志分析和部署建议

## 当前状态

### 本地服务信息
- **运行版本**: 2.4.7-a2526ca6-20260904-1931
- **分支**: 2.4.7（旧版本分支）
- **日志位置**: `/Users/xutaohuang/Library/Logs/openclaw/gateway.log`
- **服务状态**: 运行中（每10秒加载配置）
- **日志行数**: 1364行

### 154生产服务信息
- **运行版本**: v2.5.0-fc28e27b-20260904-1929
- **分支**: main（包含日志增强）
- **服务状态**: 运行正常
- **已包含**: 我们的日志增强代码

## 关键发现

### 1. 版本不一致
本地服务运行的是 **2.4.7.1931**，而我们的日志增强代码在 **main分支（2.5.0）** 上。

```
本地: 2.4.7.1931 (旧版本，无日志增强)
 154: 2.5.0.1929 (新版本，有日志增强)
```

### 2. 本地日志特征
查看最近的日志，只看到：
```
2026-09-04T23:46:10.158+08:00 [gateway] loading configuration…
```

这说明：
- 本地服务在正常运行
- 但没有实际的请求流量
- 没有触发任何错误或survival决策

### 3. 日志目录为空
```bash
/Users/xutaohuang/kaixuan/llm-gateway-go/logs/      # 空
/Users/xutaohuang/kaixuan/llm-gateway-go/raw-logs/  # 空
```

这说明本地服务可能配置了不同的日志路径。

## 建议方案

### 方案1：更新本地服务到main分支（推荐）

**优点**:
- 可以在本地测试新的日志功能
- 保持本地和154版本一致
- 便于本地开发和调试

**步骤**:
```bash
cd /Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4

# 1. 构建新版本
go build -o bin/gateway cmd/gateway/*.go

# 2. 停止本地服务
launchctl stop ai.openclaw.gateway

# 3. 复制新二进制文件到本地服务目录
cp bin/gateway /Users/xutaohuang/kaixuan/llm-gateway-go/bin/current/

# 4. 启动服务
launchctl start ai.openclaw.gateway

# 5. 验证版本
curl http://localhost:8080/api/system/version  # 或实际端口
```

### 方案2：保持当前版本，仅观察154服务

**优点**:
- 不影响本地开发环境
- 专注于154生产环境的真实数据

**步骤**:
1. 继续使用154服务进行观察
2. 通过SSH远程查看日志
3. 等待真实的错误场景触发

**154日志查询命令**:
```bash
# 连接到154
ssh root@47.97.111.154 -p 25022

# 查看实时日志
journalctl -u llm-gateway-go.service -f

# 查找survival决策日志
journalctl -u llm-gateway-go.service --since "1 hour ago" | \
  grep "survival_decision_aggregate"

# 查找候选节点折叠日志
journalctl -u llm-gateway-go.service --since "1 hour ago" | \
  grep "fold_candidate_outcomes"

# 查找特定错误
journalctl -u llm-gateway-go.service --since "1 hour ago" | \
  grep -E "overloaded|empty_response|committed_output"
```

### 方案3：触发本地测试请求（如果选择方案1）

更新本地服务后，可以触发一些测试请求来验证日志：

```bash
# 1. 正常请求
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }'

# 2. 查看日志
tail -f /Users/xutaohuang/Library/Logs/openclaw/gateway.log | \
  grep -E "survival|fold_candidate"
```

## 当前观察建议

鉴于本地服务版本较旧，**建议优先关注154服务的日志**：

### 立即行动
1. **SSH到154服务器**
   ```bash
   ssh root@47.97.111.154 -p 25022
   ```

2. **设置日志监控**
   ```bash
   # 在154上运行，实时监控新日志
   journalctl -u llm-gateway-go.service -f | \
     grep -E "survival_decision_aggregate|fold_candidate_outcomes" | \
     tee ~/gateway-enhanced-logs.txt
   ```

3. **等待真实流量触发错误**
   - 新的日志只在错误发生时才会产生
   - 正常的成功请求不会触发survival决策日志
   - 需要等待实际的错误场景（如上游过载、网络问题等）

### 检查现有日志

如果想查看154上是否已经产生了新日志：

```bash
ssh root@47.97.111.154 -p 25022 << 'EOF'
# 查看部署后的所有日志
journalctl -u llm-gateway-go.service --since "2026-09-04 23:48:00" | \
  grep -E "survival_decision_aggregate|fold_candidate_outcomes" | \
  head -50

# 如果有输出，说明已经有错误发生并被记录
# 如果无输出，说明还没有触发错误场景
EOF
```

## 日志产生的条件

我们的增强日志只在以下情况下产生：

### survival_decision_aggregate
**触发条件**: 
- 请求遇到可恢复的错误
- Survival Coordinator需要决定是retry/wait/fail
- 典型场景：上游过载、网络失败、empty response等

### fold_candidate_outcomes_complete
**触发条件**:
- 执行器尝试了一个或多个候选节点
- 需要将结果折叠成统一的outcome
- 典型场景：任何需要调用上游的请求失败

### fold_candidate_outcomes_no_attempts
**触发条件**:
- 执行器没有找到任何可用的候选节点
- 典型场景：配置错误、所有节点不可用

## 下一步建议

### 短期（今天-明天）
1. ✅ 优先监控154服务器的日志
2. ✅ 使用SSH远程查看实时日志
3. ⏳ 等待真实错误场景触发（可能需要几小时到几天）
4. ⏳ 收集第一批包含新日志的错误案例

### 中期（本周内）
1. 决定是否更新本地服务到main分支
2. 如果需要本地调试，执行方案1
3. 分析154上收集的日志数据
4. 根据真实数据优化配置

### 长期（下周开始）
1. 基于真实数据编写排查手册
2. 建立监控告警规则
3. 优化特定模型配置（如Minimax-m3）

## 总结

**当前最重要的是**：
- ✅ 154服务器已经部署了增强代码（v2.5.0-fc28e27b-20260904-1929）
- ⚠️ 本地服务版本较旧（2.4.7.1931），不包含增强代码
- 📊 新日志只在错误发生时产生，需要等待真实流量触发
- 🔍 建议立即开始监控154服务器的日志

**推荐的下一步操作**：
```bash
# 连接154并持续监控
ssh root@47.97.111.154 -p 25022
journalctl -u llm-gateway-go.service -f | \
  grep --line-buffered -E "survival_decision_aggregate|fold_candidate_outcomes|error"
```

这样可以实时看到任何新的错误和我们增强的日志。
