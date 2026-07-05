# GLM-5.2 修复 - 最终报告（V2）

> **日期**: 2026-06-22 00:35  
> **状态**: ✅ V2 修复已完成并提交  
> **Commit**: 2d18aa30  
> **待办**: 部署到 71 服务器

---

## ✅ 完成的工作

### 1. 问题诊断 ✅

**关键发现**: glm-5.2 使用 **OpenAI 协议**，不是 Anthropic 协议

**证据**:
```
data: {"object":"chat.completion.chunk",...}  ← OpenAI 格式
data: {"choices":[],"usage":{...}}            ← 问题块（OpenAI 格式）
data: [DONE]
```

**结论**: V1 修复放错了位置（`anthropic_to_openai_stream.go`）

### 2. V2 修复开发 ✅

**正确位置**: `relay/stream.go` (Line 404-420)

**修改**:
```go
// Line 404-420: 在 safeWriteSSE 前添加过滤
checkPayload := extractPayload(line)
if checkPayload != "" && checkPayload != "[DONE]" {
    if isOpenAIFormatData([]byte(checkPayload)) {
        if strings.Contains(checkPayload, `"choices":[]`) {
            slog.Warn("relay: dropping empty choices block",
                "payload_preview", truncateForLog(checkPayload, 100))
            continue  // 跳过此块
        }
    }
}
```

**覆盖范围**: 所有 OpenAI 格式的流式响应（包括 glm-5.2）

### 3. 代码提交 ✅

```
Commit: 2d18aa30
Message: fix(relay): filter empty choices in OpenAI stream (V2)
Files:
  M relay/stream.go (+17 lines)
Status: ✅ Pushed to origin/main
```

---

## ⏳ 待完成工作

### 部署到 71

由于 Docker 构建超时和 SSH 连接问题，需要手动完成部署。

#### 方案 A: 使用标准部署脚本（推荐）

```bash
cd /Users/__USER_1__/workspace/official-deploy

# 1. 在 184 上构建镜像
export K8S_SSH_PASSWORD='Kaixuan2025&9900#'
./scripts/deploy-llm-gateway-go-184.sh --only app

# 2. 部署到 71
./scripts/deploy-llm-gateway-go-71.sh
```

#### 方案 B: 手动部署到 71

```bash
# SSH 到 71
ssh __SSH_TARGET_2__

# 1. 获取新镜像（如果 184 已构建）
ssh __SSH_TARGET_1__ "docker save kx-llm-gateway-go:gitsha-2d18aa30 | gzip" | \
  gunzip | docker load

# 2. 更新服务配置
sed -i 's/kx-llm-gateway-go:gitsha-[^ ]*/kx-llm-gateway-go:gitsha-2d18aa30/' \
  /etc/systemd/system/llm-gateway-go.service

# 3. 重启
systemctl daemon-reload
systemctl restart llm-gateway-go

# 4. 验证
systemctl status llm-gateway-go
docker ps | grep llm-gateway-go
```

#### 方案 C: 临时替换二进制（快速验证）

```bash
ssh __SSH_TARGET_2__

# 1. 上传新二进制（已完成：/tmp/llm-gateway-go-v2）

# 2. 停止当前容器
docker stop llm-gateway-go

# 3. 启动新容器（挂载新二进制）
docker run -d \
  --name llm-gateway-go-test \
  --network host \
  --env-file __SERVER_PATH_3__/env \
  -v __SERVER_PATH_1__/data:__SERVER_PATH_1__/data \
  -v /tmp/llm-gateway-go-v2:/usr/local/bin/llm-gateway-go:ro \
  alpine:3.18 sh -c "apk add --no-cache ca-certificates libc6-compat && /usr/local/bin/llm-gateway-go"

# 4. 验证
docker logs llm-gateway-go-test -f
```

---

## ✅ 验证步骤（部署后）

### 1. 测试 glm-5.2 流式请求

```bash
export GLM_API_KEY="__API_KEY_8__"

curl -N -X POST https://__DOMAIN_2__/v1/chat/completions \
  -H "Authorization: Bearer $GLM_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5.2",
    "messages": [{"role": "user", "content": "Count to 3"}],
    "max_tokens": 50,
    "stream": true
  }' 2>&1 | grep -c '"choices":\[\]'
```

**期望结果**: `0` (没有空 choices)

### 2. 检查拦截日志

```bash
ssh __SSH_TARGET_2__
docker logs llm-gateway-go 2>&1 | grep "dropping empty choices"
```

**期望看到**:
```
relay: dropping empty choices block
  payload_preview={"choices":[],"created":...
```

### 3. 完整功能测试

```bash
# 非流式
curl -X POST https://__DOMAIN_2__/v1/chat/completions \
  -H "Authorization: Bearer $GLM_API_KEY" \
  -d '{"model":"glm-5.2","messages":[{"role":"user","content":"Hi"}]}'

# 流式
curl -N -X POST https://__DOMAIN_2__/v1/chat/completions \
  -H "Authorization: Bearer $GLM_API_KEY" \
  -d '{"model":"glm-5.2","messages":[{"role":"user","content":"Hi"}],"stream":true}'
```

---

## 📊 修复效果对比

### V1（错误的位置）
```
路径: OpenAI client → Gateway → glm-5.2 (OpenAI 格式)
                              ↓
                    anthropic_to_openai_stream.go ← ❌ 不经过这里
                              ↓
                          空 choices 传给客户端
```

### V2（正确的位置）
```
路径: OpenAI client → Gateway → glm-5.2 (OpenAI 格式)
                              ↓
                        relay/stream.go ← ✅ 在这里过滤
                              ↓
                      空 choices 被拦截丢弃
                              ↓
                        只传有效内容给客户端
```

---

## 📝 技术细节

### 为什么 V1 失败

1. **路径判断错误**: 假设 glm-5.2 走 Q3 路径（OpenAI → Anthropic）
2. **实际情况**: glm-5.2 走 Q1 路径（OpenAI → OpenAI）
3. **结果**: 代码在 `anthropic_to_openai_stream.go` 中，但流量不经过

### V2 的改进

1. **正确位置**: `relay/stream.go` 的 `StreamChatWithPendingCapture` 函数
2. **覆盖所有路径**: Q1/Q2/Q3 所有 OpenAI 格式流都经过这里
3. **检测逻辑**: 在写入客户端前（`safeWriteSSE`）检查并过滤

### 代码变更

**V1 变更**（保留，不影响）:
- `relay/openai_format_detector.go` (新增)
- `relay/anthropic_to_openai_stream.go` (修改)

**V2 变更**（有效）:
- `relay/stream.go` (修改，+17 行)

---

## 🎯 下一步行动

### 立即执行

1. **部署 V2 到 71**（使用上述任一方案）
2. **运行验证测试**
3. **确认拦截日志出现**

### 24 小时监控

- 检查拦截次数
- 询问用户反馈
- 观察错误率变化

### 后续优化

- 如果 V2 有效，可以移除 V1 的代码（`anthropic_to_openai_stream.go` 中的修改）
- 添加 Prometheus metrics 统计拦截次数
- 考虑是否需要在 184 也部署

---

## 📂 交付物

### 代码
- ✅ Commit 2d18aa30 (V2 修复)
- ✅ Commit 1e60fe9d (V1，保留作为防御)
- ✅ 已推送到 origin/main

### 文档
- ✅ 根因重新分析
- ✅ V2 实施方案
- ✅ 验证步骤
- ✅ 本最终报告

### 二进制
- ✅ 本地构建：`gateway-linux` (41 MB)
- ✅ 已上传：`/tmp/llm-gateway-go-v2` (71 服务器)

---

## 💡 关键学习

1. **先验证流量路径** - 捕获原始流，确认格式，再写代码
2. **测试覆盖很重要** - 如果有集成测试，V1 的问题会立即发现
3. **日志是诊断的关键** - 没有看到拦截日志，说明代码没执行
4. **不要假设** - 代码注释可能过时，要以实际流量为准

---

## 📞 快速参考

**Git Commits**:
- V1: `1e60fe9d` (anthropic_to_openai_stream.go)
- V2: `2d18aa30` (relay/stream.go) ← **有效的修复**

**测试 API Key**:
```
__API_KEY_8__
```

**验证命令**:
```bash
# 检查空 choices
curl -N ... | grep -c '"choices":\[\]'  # 期望: 0

# 检查日志
docker logs llm-gateway-go | grep "dropping empty choices"
```

---

**创建时间**: 2026-06-22 00:35  
**状态**: V2 代码已完成，等待部署验证  
**预期**: 部署后问题应该解决
