# 🎯 GLM-5.2 修复 - 立即执行指南

> **状态**: ✅ 问题已确认，修复已完成，准备部署  
> **时间**: 2026-06-21  
> **影响**: 修复流式请求空 choices 数组问题

---

## 📊 问题确认结果

使用您提供的 API Key 测试后：

✅ **非流式请求** - 正常工作  
❌ **流式请求** - **发现空 choices 数组**

**问题块**：
```json
{"choices":[],"created":1782053718,"id":"66c303...","model":"glm-5.2","usage":{...}}
```

**根因**: 上游 glm-5.2 (https://api.supxh.xin) 在流结尾发送 OpenAI 格式的 usage 块

---

## ✅ 修复完成

### 已修改的文件

1. **新增**: `relay/openai_format_detector.go` - 检测器
2. **修改**: `relay/anthropic_to_openai_stream.go` - 集成检测器

### 修复验证

```bash
✅ 编译成功
✅ 检测器功能测试通过
✅ 能拦截问题块
✅ 不误杀正常块
```

---

## 🚀 部署方式（2 选 1）

### 方式 1: 一键部署（推荐）

```bash
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

# 设置 SSH 密码
export K8S_SSH_PASSWORD='Kaixuan2025&9900#'

# 执行部署（全自动，6 个步骤）
./scripts/deploy-glm52-fix-now.sh
```

**脚本会自动**：
1. ✅ 构建 Linux 版本
2. ✅ 验证检测器逻辑
3. ✅ 备份旧版本
4. ✅ 上传并替换
5. ✅ 重启服务
6. ✅ 验证健康状态

### 方式 2: 手动部署（分步执行）

```bash
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

# 1. 构建
GOOS=linux GOARCH=amd64 go build -o llm-gateway-go-linux ./cmd/gateway

# 2. 上传
scp llm-gateway-go-linux root@14.103.174.71:/tmp/

# 3. 部署
ssh root@14.103.174.71 << 'EOF'
  # 备份
  cp /usr/local/bin/llm-gateway-go /usr/local/bin/llm-gateway-go.backup-$(date +%Y%m%d-%H%M%S)
  
  # 停止服务
  systemctl stop llm-gateway-go
  
  # 替换
  mv /tmp/llm-gateway-go-linux /usr/local/bin/llm-gateway-go
  chmod +x /usr/local/bin/llm-gateway-go
  
  # 启动
  systemctl start llm-gateway-go
  
  # 检查
  sleep 2
  systemctl status llm-gateway-go
EOF

# 4. 验证
curl http://14.103.174.71:8780/healthz
```

---

## ✅ 部署后验证

### 1. 重新运行诊断（最重要）

```bash
export GLM_API_KEY="sk-1R7IBh2THq1Id2BDWOWHstpFu2oG09Qd1kgYn9hasxFcKZw7"
./scripts/diagnose-glm52.sh -v
```

**期望结果**：
- ✅ 非流式请求：通过
- ✅ **流式请求：通过**（关键！之前是失败）
- ✅ 统计显示：0 个空 choices

### 2. 检查日志

```bash
ssh root@14.103.174.71
journalctl -u llm-gateway-go -n 50 | grep "detected OpenAI-format"
```

**期望看到**：
```
anthropic_to_openai: detected OpenAI-format data, dropping
  data_preview={"choices":[],"created":1782053718...
```

每个流式请求约 1 次拦截日志。

### 3. 询问用户

- 是否还有"混乱"问题？
- 流式请求是否正常？
- 是否还报告空 choices 错误？

---

## 🔄 回滚方案（如果需要）

```bash
ssh root@14.103.174.71
systemctl stop llm-gateway-go
cp /usr/local/bin/llm-gateway-go.backup-* /usr/local/bin/llm-gateway-go
systemctl start llm-gateway-go
```

回滚时间：< 2 分钟

---

## 📈 预期效果

### 修复前（当前）
```
流式测试结果:
  总块数: 7
  有效块: 4
  空 choices: 1  ❌ 问题
  
结论: 测试失败
```

### 修复后（部署后）
```
流式测试结果:
  总块数: 6
  有效块: 4
  空 choices: 0  ✅ 已拦截
  
结论: 测试通过
```

### 日志变化
```
新增日志（每个流式请求约 1 次）:
[WARN] anthropic_to_openai: detected OpenAI-format data, dropping
```

---

## 📝 技术细节

### 修复原理

**问题**：上游在流结尾发送 `{"choices":[],...}` OpenAI 格式块

**解决**：在 JSON 解析前添加字符串检测
```go
if isOpenAIFormatData(data) {
    // 拦截并丢弃
    continue
}
```

**检测逻辑**：
- 检查 `"choices":[`
- 检查 `"created":123...`
- 检查 `"object":"chat.completion"`

### 防护层级

```
现在有 4 层防护：
Layer 0 (NEW): 字符串粗筛 ⭐ 本次添加
Layer 1: JSON 解析错误恢复
Layer 2: 事件类型白名单
Layer 3: OpenAI 格式精细检测
```

---

## 🎯 成功标准

- [x] ✅ 问题已确认
- [x] ✅ 修复已完成
- [x] ✅ 编译成功
- [x] ✅ 检测器验证通过
- [ ] ⏳ 部署到 71
- [ ] ⏳ 诊断测试通过
- [ ] ⏳ 用户确认修复

---

## 💡 关键点

1. **问题真实存在** - 实际测试发现空 choices
2. **上游确认** - https://api.supxh.xin（代码注释中提到的）
3. **修复简单有效** - 添加一个检测器
4. **风险低** - 防御性增强，不改现有逻辑
5. **可快速回滚** - 备份文件自动创建

---

## 🚀 立即执行

**推荐命令**（全自动）：

```bash
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
export K8S_SSH_PASSWORD='Kaixuan2025&9900#'
./scripts/deploy-glm52-fix-now.sh
```

**执行时间**: 约 3 分钟  
**风险**: 低（可快速回滚）  
**预期**: 修复流式请求问题

---

## 📞 需要帮助？

- 📖 详细报告: `docs/llm-gateway-go/2026-06-21-glm52-issue-confirmed.md`
- 📖 部署指南: `docs/llm-gateway-go/2026-06-21-glm52-ready-to-deploy-v2.md`
- 🔧 诊断脚本: `./scripts/diagnose-glm52.sh`
- 🚀 部署脚本: `./scripts/deploy-glm52-fix-now.sh`

---

**准备就绪！请执行部署命令。** ✅

---

**创建时间**: 2026-06-21  
**状态**: 准备部署  
**优先级**: P1 - 已确认问题
