# GLM-5.2 修复 - 准备部署

> **日期**: 2026-06-21  
> **状态**: ✅ 修复已完成，准备部署  
> **修复类型**: 防御性增强（添加第四层防护）

---

## ✅ 问题确认

### 测试发现
通过网关测试 glm-5.2（使用真实 API Key）：
- ✅ **非流式请求正常** - 响应格式正确
- ❌ **流式请求有问题** - 检测到空 choices 数组

### 问题块示例
```json
{
  "choices": [],
  "created": 1782053718,
  "id": "66c303528ab045ca877356748730576b",
  "model": "glm-5.2",
  "usage": {
    "prompt_tokens": 187,
    "completion_tokens": 15,
    "total_tokens": 202
  }
}
```

**特征**：
- OpenAI 格式（不是 Anthropic）
- 出现在流的结尾
- choices 数组为空
- 包含 usage 统计信息

---

## ✅ 已完成的修复

### 1. 创建检测器
**文件**: `relay/openai_format_detector.go`

**功能**: 快速检测 OpenAI 格式数据
- 检查 `"choices":[` 或 `"choices": [`
- 检查 `"created":` 后跟数字
- 检查 `"object":"chat.completion"`

**验证**: ✅ 能正确拦截问题块，不误杀正常块

### 2. 集成到流处理
**文件**: `relay/anthropic_to_openai_stream.go`  
**位置**: Line 291-303（在 JSON 解析前）

**代码**:
```go
// 2026-06-21 enhancement: Early coarse filter
if isOpenAIFormatData(data) {
    slog.Warn("anthropic_to_openai: detected OpenAI-format data, dropping",
        "event_type", eventType,
        "data_preview", truncateForLog(string(data), 100),
        "request_id", requestID)
    continue
}
```

### 3. 编译验证
```bash
✅ go build ./cmd/gateway - 成功
✅ 检测器功能测试 - 通过
```

---

## 🚀 部署步骤

### 方式 A: 手动部署（推荐，更可控）

#### 1. 构建
```bash
cd __LOCAL_PATH_1__
GOOS=linux GOARCH=amd64 go build -o llm-gateway-go-linux ./cmd/gateway
```

#### 2. 备份当前版本
```bash
ssh __SSH_TARGET_2__
cp /usr/local/bin/llm-gateway-go /usr/local/bin/llm-gateway-go.backup-$(date +%Y%m%d-%H%M%S)
```

#### 3. 上传新版本
```bash
scp llm-gateway-go-linux __SSH_TARGET_2__:/tmp/
```

#### 4. 部署
```bash
ssh __SSH_TARGET_2__ << 'ENDSSH'
systemctl stop llm-gateway-go
mv /tmp/llm-gateway-go-linux /usr/local/bin/llm-gateway-go
chmod +x /usr/local/bin/llm-gateway-go
systemctl start llm-gateway-go
sleep 2
systemctl status llm-gateway-go
ENDSSH
```

#### 5. 验证
```bash
# 检查健康状态
curl -i http://__PUB_IP_2__:__PORT_2__/healthz

# 重新运行诊断
export GLM_API_KEY="__API_KEY_8__"
./scripts/diagnose-glm52.sh -v
```

### 方式 B: 使用部署脚本（需要密码）

```bash
export K8S_SSH_PASSWORD='Kaixuan2025&9900#'
./scripts/deploy-glm52-enhancement.sh
```

---

## 📊 预期结果

### 修复前（当前状态）
```
流式请求:
  Chunk 1-4: ✅ 正常内容
  Chunk 5: ❌ {"choices":[]} - 传给客户端
  [DONE]

结果: 客户端可能崩溃
```

### 修复后（部署后）
```
流式请求:
  Chunk 1-4: ✅ 正常内容
  Chunk 5: 🛡️ {"choices":[]} - 被拦截丢弃
  [DONE]

结果: 客户端正常工作
```

### 日志变化
**新增警告**（每个流式请求约 1 次）:
```
[WARN] anthropic_to_openai: detected OpenAI-format data, dropping
  event_type=...
  data_preview={"choices":[],"created":1782053718...
  request_id=req-xxx
```

---

## ✅ 验证清单

部署后必须验证：

- [ ] **服务启动成功**
  ```bash
  ssh __SSH_TARGET_2__ 'systemctl status llm-gateway-go'
  ```

- [ ] **健康检查通过**
  ```bash
  curl http://__PUB_IP_2__:__PORT_2__/healthz
  # 期望: {"status":"ok","version":"0.2.0"}
  ```

- [ ] **重新运行诊断**
  ```bash
  export GLM_API_KEY="__API_KEY_8__"
  ./scripts/diagnose-glm52.sh -v
  # 期望: 流式测试 ✅ 通过
  ```

- [ ] **检查日志**
  ```bash
  ssh __SSH_TARGET_2__
  journalctl -u llm-gateway-go -n 50 | grep "detected OpenAI-format"
  # 期望: 看到拦截日志
  ```

- [ ] **用户验证**
  - 询问用户是否还有"混乱"问题
  - 流式请求是否正常
  - 是否还有空 choices 错误

---

## 🔄 回滚方案

如果出现问题，立即回滚：

```bash
ssh __SSH_TARGET_2__
systemctl stop llm-gateway-go
cp /usr/local/bin/llm-gateway-go.backup-* /usr/local/bin/llm-gateway-go
systemctl start llm-gateway-go
systemctl status llm-gateway-go
```

---

## 📈 监控建议

### 前 24 小时
- 每小时检查日志中的拦截次数
- 观察是否有新的错误
- 询问用户反馈

### 7 天监控
- 统计拦截事件总数
- 确认用户不再报告问题
- 评估是否需要调整

---

## 📝 变更文件

| 文件 | 变更类型 | 说明 |
|------|---------|------|
| `relay/openai_format_detector.go` | 新增 | 检测器实现 |
| `relay/anthropic_to_openai_stream.go` | 修改 | 集成检测器（Line 291-303） |

---

## 🎯 成功标准

- ✅ 服务正常启动
- ✅ 健康检查通过
- ✅ **流式诊断测试通过**（关键指标）
- ✅ 日志中有拦截记录
- ✅ 用户不再报告"混乱"
- ✅ 24 小时无回滚

---

## 💡 关键点

1. **问题已确认** - 真实环境测试发现空 choices
2. **修复已验证** - 检测器能正确拦截
3. **影响范围小** - 只添加防护，不改变现有逻辑
4. **风险低** - 防御性增强，最坏情况是多过滤几个事件
5. **可快速回滚** - 备份文件存在

---

## 🚀 下一步

**立即执行**：
1. 用户决定部署方式（手动 / 脚本）
2. 执行部署
3. 运行验证清单
4. 监控 24 小时

**准备就绪！等待部署指令。** ✅

---

**创建时间**: 2026-06-21  
**修复类型**: 防御性增强  
**风险等级**: 低  
**回滚时间**: < 2 分钟
