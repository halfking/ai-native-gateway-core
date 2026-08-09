# 245 测试环境部署指南

## 部署概述

本次部署包含三个关键更新：
1. **Modality 推断修复**：修正 50+ 模型的 modality 标注
2. **路径遍历漏洞修复**：CVE 级安全漏洞（P0）
3. **API Key 认证集成**：统一附件访问认证

## 部署前检查

### 1. 确认当前版本

```bash
ssh user@192.168.1.245
cd /path/to/llm-gateway-go
git log --oneline -1
```

### 2. 确认服务状态

```bash
sudo systemctl status llm-gateway
# 或
docker ps | grep llm-gateway
# 或
supervisorctl status llm-gateway
```

### 3. 备份数据

```bash
# 备份数据库（如果有）
pg_dump llm_gateway > backup_$(date +%Y%m%d).sql

# 备份配置文件
cp .env .env.backup_$(date +%Y%m%d)
```

## 部署步骤

### 方案 A：使用自动化脚本（推荐）

```bash
# 1. 运行部署脚本
./scripts/deploy-245.sh

# 2. 按照提示停止服务
sudo systemctl stop llm-gateway

# 3. (可选) 启用 API Key 认证
echo 'LLM_GATEWAY_ATTACHMENT_AUTH_MODE=apikey' >> .env

# 4. 启动服务
sudo systemctl start llm-gateway

# 5. 验证服务
sudo systemctl status llm-gateway
curl http://localhost:8080/healthz
```

### 方案 B：手动部署

```bash
# 1. 拉取最新代码
git fetch origin
git checkout main
git pull origin main

# 2. 验证关键提交
git log --oneline -10 | grep -E "fix\(multimodal\)|feat\(attachments\)"
# 应看到：
# - 4639d4ae5 fix(multimodal): tier-based modality inference + path traversal defense
# - 80d4484d1 feat(attachments): API Key authentication integration

# 3. 构建
go build -o llm-gateway ./cmd/gateway

# 4. 停止服务
sudo systemctl stop llm-gateway

# 5. 替换二进制
sudo cp llm-gateway /usr/local/bin/llm-gateway
# 或根据实际部署路径调整

# 6. (可选) 配置环境变量
sudo nano /etc/systemd/system/llm-gateway.service
# 添加：
# Environment="LLM_GATEWAY_ATTACHMENT_AUTH_MODE=apikey"

# 7. 重新加载并启动
sudo systemctl daemon-reload
sudo systemctl start llm-gateway
```

## 部署验证

### 自动化测试

```bash
# 基础验证（无需 API Key）
./scripts/verify-245.sh

# 完整验证（需要 API Key）
API_KEY=sk-your-test-key ./scripts/verify-245.sh
```

### 手动测试

#### 1. 健康检查

```bash
curl http://localhost:8080/healthz
# 预期: {"status":"ok"}

curl http://localhost:8080/version
# 预期: 包含最新 commit hash
```

#### 2. 路径遍历防护验证

```bash
# 创建测试附件
mkdir -p data/attachments
echo "test-content" > data/attachments/test.txt

# 正常访问（应返回 401 或 200，取决于认证模式）
curl http://localhost:8080/api/attachments/test.txt

# 路径遍历攻击（应返回 401 或 404）
curl http://localhost:8080/api/attachments/../../../etc/passwd
# 预期: 401 Unauthorized 或 404 Not Found（不应返回文件内容）
```

#### 3. API Key 认证验证（如果启用）

```bash
# 无认证访问（应拒绝）
curl http://localhost:8080/api/attachments/test.txt
# 预期: 401 Unauthorized

# 有效 API Key 访问（应成功）
curl -H "Authorization: Bearer sk-your-valid-key" \
     http://localhost:8080/api/attachments/test.txt
# 预期: 200 OK + 文件内容

# 无效 API Key 访问（应拒绝）
curl -H "Authorization: Bearer sk-invalid-key" \
     http://localhost:8080/api/attachments/test.txt
# 预期: 401 Unauthorized
```

#### 4. Modality 路由验证

```bash
# 测试 vision 模型（GLM-4.5v）
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-4.5v",
    "messages": [{"role": "user", "content": "test"}]
  }'
# 预期: 路由到 vision-capable provider

# 测试 text 模型（GLM-4.5）
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-4.5",
    "messages": [{"role": "user", "content": "test"}]
  }'
# 预期: 正常路由
```

### 日志检查

```bash
# 查看启动日志
sudo journalctl -u llm-gateway -n 100 -f

# 或
tail -f /var/log/llm-gateway/gateway.log

# 关键日志：
# - "attachment access config loaded" - 认证配置加载
# - "attachment API Key authentication enabled" - API Key 认证启用
# - "attachment download/list handler wired" - 附件处理器注册
```

## 配置说明

### 环境变量

在 `.env` 或 systemd service 中配置：

```bash
# 附件认证模式（默认: none）
LLM_GATEWAY_ATTACHMENT_AUTH_MODE=apikey

# 附件公开 URL（可选，用于 CDN）
LLM_GATEWAY_ATTACHMENT_PUBLIC_URL=https://cdn.example.com/attachments

# CORS 配置（可选）
LLM_GATEWAY_ATTACHMENT_CORS_ORIGINS=https://app.example.com
```

### 认证模式对照表

| 模式 | 说明 | 适用场景 |
|------|------|---------|
| `none` | 无需认证（默认） | 内网部署、测试环境 |
| `apikey` | Bearer token 认证 | 生产环境、多租户 SaaS |
| `signed` | HMAC 签名链接（待实现） | Session 附件 |
| `admin` | Session cookie（待实现） | 管理后台 |

## 回滚方案

如遇问题需要回滚：

```bash
# 1. 停止服务
sudo systemctl stop llm-gateway

# 2. 恢复旧版本
cd ../backup_YYYYMMDD_HHMMSS
sudo cp llm-gateway /usr/local/bin/llm-gateway

# 3. 恢复配置
cp .env.backup_YYYYMMDD ../.env

# 4. 启动服务
sudo systemctl start llm-gateway

# 5. 验证
curl http://localhost:8080/healthz
```

## 监控指标

部署后需要监控的关键指标：

### 1. 错误率
- 路径：`/api/attachments/*`
- 预期：401/404 错误率 < 1%（排除攻击流量）

### 2. 响应时间
- P50: < 50ms
- P95: < 200ms
- P99: < 500ms

### 3. 认证失败
- 监控审计日志中的 "authentication failed"
- 排查异常 IP 和非法请求

### 4. Modality 路由
- 检查 vision 模型（glm-4.5v, qwen2.5-vl）是否正确路由
- 检查 503 错误是否减少

## 故障排查

### 问题 1: 服务启动失败

```bash
# 检查日志
sudo journalctl -u llm-gateway -n 50

# 常见原因：
# - 端口占用：lsof -i :8080
# - 数据库连接失败：检查 PostgreSQL
# - 配置错误：检查 .env 文件语法
```

### 问题 2: 认证失败

```bash
# 检查配置
grep ATTACHMENT_AUTH_MODE .env

# 检查 KeyVerifier
# 日志应包含: "attachment API Key authentication enabled"

# 测试数据库连接
psql -U llm_gateway -c "SELECT * FROM api_keys LIMIT 1;"
```

### 问题 3: 路径遍历防护误伤

```bash
# 检查日志中的路径解析错误
grep "attachments: key.*escapes base directory" /var/log/llm-gateway/gateway.log

# 确认 ATTACHMENT_DIR 配置正确
echo $LLM_GATEWAY_ATTACHMENT_DIR
ls -la $LLM_GATEWAY_ATTACHMENT_DIR
```

## 联系支持

如遇无法解决的问题：
- 技术负责人：[联系方式]
- 紧急热线：[电话]
- Issue 提交：https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go/issues

## 附录

### A. 相关 Commit

- **4639d4ae5**: fix(multimodal): tier-based modality inference + path traversal defense
- **80d4484d1**: feat(attachments): API Key authentication integration

### B. 相关文档

- [附件认证集成文档](../docs/attachment-auth-integration.md)
- [环境变量配置](.env.example)
- [API 文档](../docs/api.md)

### C. 测试数据

测试用的模型名称：
- Vision: `glm-4.5v`, `glm-4.6v`, `qwen2.5-vl-72b`, `doubao-1.5-vision-pro`
- Text: `glm-4.5`, `qwen2.5-72b-instruct`, `doubao-pro-32k`
- Multimodal: `gpt-5`, `gemini-2.5-flash`, `minimax-m3`
