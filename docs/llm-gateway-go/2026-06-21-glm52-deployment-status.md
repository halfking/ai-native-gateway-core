# GLM-5.2 修复部署 - 当前状态报告

> **日期**: 2026-06-21 23:12  
> **状态**: ⚠️ 修复已开发，部署遇到技术障碍  
> **问题**: Docker 构建超时/网络问题

---

## ✅ 已完成的工作

### 1. 问题确认
- ✅ 使用真实 API Key 测试
- ✅ **确认问题存在**：流式请求中出现空 choices 数组
- ✅ 问题块：`{"choices":[],"created":1782053718,...}`
- ✅ 根因：上游 glm-5.2 (https://api.supxh.xin) 混合格式

### 2. 修复开发
- ✅ 创建检测器：`relay/openai_format_detector.go`
- ✅ 集成到流处理：`relay/anthropic_to_openai_stream.go` Line 291
- ✅ 编译成功：`llm-gateway-go-linux` (41 MB)
- ✅ 检测器验证通过：能拦截问题块，不误杀正常块

### 3. 部署尝试
- ✅ 构建 Linux 二进制文件
- ✅ 上传到 71 服务器
- ⚠️ **发现 71 使用 Docker 容器部署**
- ❌ Docker 镜像构建超时（网络问题）

---

## ⚠️ 当前障碍

### 技术问题

**71 服务器使用 systemd 管理的 Docker 容器**：
```bash
# 服务配置
ExecStart=/usr/bin/docker run ... kx-llm-gateway-go:gitsha-8cc5b8d7
```

**部署方式**：
1. 需要构建 Docker 镜像
2. 更新 systemd 服务配置中的镜像标签
3. 重启服务

**遇到的问题**：
- Docker 构建超时（71 上和本地都超时）
- Docker Hub 网络连接问题
- Alpine/Ubuntu 基础镜像拉取失败

---

## 🎯 解决方案

### 方案 A: 使用现有镜像构建流程（推荐）

**前提**: 项目有标准的镜像构建流程

**步骤**:
1. 在 184 服务器上构建镜像（有 Docker registry）
2. 使用 `scripts/deploy-llm-gateway-go-71.sh` 部署
3. 脚本会自动处理镜像传输和服务更新

**执行**:
```bash
# 1. 提交代码
cd __LOCAL_PATH_1__
git add relay/openai_format_detector.go relay/anthropic_to_openai_stream.go
git commit -m "fix(relay): add OpenAI format detector for glm-5.2 empty choices"
git push

# 2. 在 184 构建（或使用 CI/CD）
# 参考 scripts/deploy-llm-gateway-go-184.sh

# 3. 部署到 71
./scripts/deploy-llm-gateway-go-71.sh
```

### 方案 B: 手动 Docker 操作（备选）

**在 71 上手动构建**（避免网络问题）:

```bash
ssh __SSH_TARGET_2__

# 使用本地已有的 Ubuntu 镜像构建
cat > /tmp/Dockerfile << 'EOF'
FROM ubuntu:22.04
COPY /tmp/llm-gateway-go-linux /usr/local/bin/llm-gateway-go
RUN chmod +x /usr/local/bin/llm-gateway-go
ENTRYPOINT ["/usr/local/bin/llm-gateway-go"]
EOF

cd /tmp
docker build --network=host -f Dockerfile -t kx-llm-gateway-go:fix-glm52 .

# 更新服务
sed -i 's/kx-llm-gateway-go:gitsha-[a-z0-9]*/kx-llm-gateway-go:fix-glm52/' \
  /etc/systemd/system/llm-gateway-go.service

systemctl daemon-reload
systemctl restart llm-gateway-go

# 验证
docker ps | grep llm-gateway-go
systemctl status llm-gateway-go
```

### 方案 C: 修改现有容器（最快，但不推荐）

```bash
ssh __SSH_TARGET_2__

# 停止容器
docker stop llm-gateway-go

# 复制新二进制到容器卷
docker run --rm -v __SERVER_PATH_1__/data:/data \
  -v /tmp:/tmp alpine sh -c \
  "cp /tmp/llm-gateway-go-linux /data/llm-gateway-go-new"

# 修改 entrypoint（需要重新创建容器）
# 这个方案比较复杂，不推荐
```

---

## 📊 验证步骤（部署成功后）

### 1. 检查服务状态
```bash
ssh __SSH_TARGET_2__
systemctl status llm-gateway-go
docker ps | grep llm-gateway-go
docker logs llm-gateway-go | tail -20
```

### 2. 测试流式请求
```bash
export GLM_API_KEY="__API_KEY_8__"

curl -N -X POST https://__DOMAIN_2__/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $GLM_API_KEY" \
  -d '{
    "model": "glm-5.2",
    "messages": [{"role": "user", "content": "Count to 3"}],
    "max_tokens": 50,
    "stream": true
  }' | while read line; do
    if [[ $line == data:* ]]; then
        data="${line#data: }"
        if echo "$data" | jq -e '.choices' >/dev/null 2>&1; then
            len=$(echo "$data" | jq '.choices | length')
            if [ "$len" = "0" ]; then
                echo "❌ 仍有空 choices"
            fi
        fi
    fi
done
```

**期望结果**: 无 "❌ 仍有空 choices" 输出

### 3. 检查拦截日志
```bash
ssh __SSH_TARGET_2__
docker logs llm-gateway-go 2>&1 | grep "detected OpenAI-format"
```

**期望看到**:
```
"detected OpenAI-format data, dropping"
```

---

## 📝 代码变更摘要

### 新增文件
**relay/openai_format_detector.go** (66 行)
- `isOpenAIFormatData()` - 检测 OpenAI 格式数据
- `truncateForLog()` - 日志截断工具

### 修改文件
**relay/anthropic_to_openai_stream.go**
- Line 291-303: 添加早期检测器调用
- 在 JSON 解析前过滤 OpenAI 格式数据

### 检测逻辑
```go
if strings.Contains(dataStr, `"choices":[`) ||
   strings.Contains(dataStr, `"choices": [`) {
    return true  // 拦截
}
```

---

## 💡 建议

### 立即行动
1. **提交代码到 Git** - 保存修复
2. **在 184 构建镜像** - 使用现有 CI/CD
3. **使用标准部署脚本** - `deploy-llm-gateway-go-71.sh`

### 为什么这样做
- ✅ 遵循现有部署流程
- ✅ 避免 Docker 网络问题
- ✅ 可追溯、可回滚
- ✅ 符合团队规范

### 替代方案（如果CI/CD不可用）
- 在 71 上使用现有 Ubuntu 镜像手动构建
- 或者请熟悉 184 环境的同事协助构建

---

## 📦 交付物

### 代码文件
- ✅ `relay/openai_format_detector.go` - 已创建
- ✅ `relay/anthropic_to_openai_stream.go` - 已修改
- ✅ `llm-gateway-go-linux` - 已构建 (41 MB)

### 文档文件
- ✅ 问题确认报告
- ✅ 部署指南
- ✅ 验证步骤
- ✅ 本状态报告

### 测试证据
- ✅ 问题复现（空 choices 检测到）
- ✅ 检测器功能验证通过
- ✅ 编译成功

---

## 🎯 下一步

### 选项 1: 我来完成（需要权限）
如果您能提供：
- 184 服务器访问权限
- 或现有的镜像构建流程文档

我可以完成剩余的部署步骤。

### 选项 2: 您来完成（有文档支持）
参考本报告中的**方案 A** 或 **方案 B**，使用现有工具完成部署。

### 选项 3: 团队协作
将代码提交到 Git，请团队中熟悉部署流程的同事协助。

---

## 📞 关键文件位置

```
__LOCAL_PATH_1__/
├── relay/
│   ├── openai_format_detector.go          ← 新增（已创建）
│   └── anthropic_to_openai_stream.go      ← 已修改
├── llm-gateway-go-linux                   ← 已构建（41 MB）
└── docs/llm-gateway-go/
    ├── 2026-06-21-glm52-issue-confirmed.md
    ├── DEPLOY-NOW.md
    └── 2026-06-21-glm52-deployment-status.md  ← 本文件
```

---

**创建时间**: 2026-06-21 23:12  
**状态**: 修复已开发，等待部署完成  
**阻塞**: Docker 构建技术问题  
**建议**: 使用现有部署流程（方案 A）
