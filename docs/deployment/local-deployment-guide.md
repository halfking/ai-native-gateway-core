# LLM Gateway 本地部署指南

## 一、前置要求

### 1.1 系统依赖
- Docker 和 Docker Compose
- PostgreSQL 客户端工具（用于数据库初始化）
- Go 1.21+ （用于构建）
- Git

### 1.2 环境配置文件
**必备**: 项目根目录必须存在 `.env.local` 文件，包含所有必需的环境变量。

```bash
cp .env.local.example .env.local
```

然后编辑 `.env.local` 配置实际参数。

---

## 二、密钥配置原则（重要）

### 2.1 密钥固定性要求

以下两个密钥**必须是固定值**，一旦设置就不能变更：

1. **`LLM_GATEWAY_SECRET_KEY`**
   - 用于签发 admin 会话 token
   - 变更会导致所有已签发的会话失效

2. **`LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY`**
   - 用于加密/解密 provider 凭据（API key）
   - 变更会导致数据库中所有 `credentials.secret_ciphertext` 无法解密
   - **症状**: 所有 provider 的 API key 显示 `decrypt_failed`，网关无法调用上游

### 2.2 两种场景的密钥配置

#### 场景 A: 全新安装（空数据库）
首次安装时可以生成新密钥：

```bash
# 生成 SECRET_KEY（一次性执行，保存结果）
openssl rand -base64 32

# 生成 CREDENTIAL_ENCRYPTION_KEY（一次性执行，保存结果）
openssl rand -base64 32 | tr '+/' '-_' | tr -d '='
```

将生成的值**固定写入** `.env.local`，后续所有部署都使用相同值。

#### 场景 B: 从远端同步数据库（245/252）
如果数据库是从 245 或 252 同步过来的，**必须使用源环境的密钥**：

```bash
# 从 245 同步密钥到本地 .env.local
ssh -p 25022 root@<env:HOST_245_IP> 'grep -E "^(LLM_GATEWAY_SECRET_KEY|LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY)=" /opt/llm-gateway-go/.env' >> .env.local
```

验证同步结果：
```bash
grep -E "^LLM_GATEWAY_(SECRET|CREDENTIAL_ENCRYPTION)_KEY=" .env.local
```

### 2.3 密钥配置检查清单
- [ ] `.env.local` 文件已创建
- [ ] `LLM_GATEWAY_SECRET_KEY` 已设置为固定值
- [ ] `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY` 已设置为固定值
- [ ] 如果同步了远端数据库，确认密钥与源环境一致
- [ ] `.env.local` 已加入 `.gitignore`（避免提交敏感信息）

---

## 三、数据库准备

### 3.1 创建数据库
```bash
createdb llm_gateway_dev
```

### 3.2 执行 schema 迁移
```bash
# 设置数据库连接
export LLM_GATEWAY_DATABASE_URL="postgres://postgres:yourpassword@127.0.0.1:5432/llm_gateway_dev?sslmode=disable"

# 执行迁移
go run cmd/migrate/main.go up
```

### 3.3 （可选）从远端同步数据
如果需要使用 245 的真实数据进行本地开发：

```bash
# 参考 db-sync-252-local skill 或手动 pg_dump/pg_restore
# 注意：同步后必须确保 CREDENTIAL_ENCRYPTION_KEY 与源环境一致
```

---

## 四、部署流程

### 4.1 加载环境变量
```bash
source .env.local
```

### 4.2 执行部署
```bash
./scripts/deploy-local.sh deploy
```

部署脚本会自动执行：
1. 构建 Docker 镜像
2. 停止旧容器（如果存在）
3. 启动新容器（候选实例）
4. **凭据解密冒烟测试**（自动验证密钥配置是否正确）
5. 蓝绿切换（将候选实例提升为 active）

### 4.3 凭据解密冒烟测试
部署脚本会自动调用 admin API 验证凭据解密：

```bash
# 环境变量控制（可选）
export LLM_GATEWAY_DECRYPT_SMOKE_PROVIDER_ID="587,18,1"  # 指定要检查的 provider id
```

如果冒烟测试失败：
- 部署会自动回滚到旧版本
- 检查日志中的 `decrypt_failed` 错误
- 参考《故障排查指南》→ 凭据解密失败

---

## 五、部署后验证

### 5.1 检查容器状态
```bash
docker ps | grep llm-gateway
```

### 5.2 检查密钥注入
```bash
docker inspect llm-gateway-go-active | grep -A 5 "LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY"
```

应该看到完整的密钥值（不是空字符串）。

### 5.3 检查网关日志
```bash
docker logs llm-gateway-go-active 2>&1 | grep keyring
```

正常情况应该看到：
```
AES-GCM keyring initialized (source: env/CREDENTIAL_ENCRYPTION_KEY)
```

异常情况（说明密钥未正确配置）：
```
no decryption key available (keyring=false, fernet=32 bytes)
```

### 5.4 验证 API 可用性
```bash
# 健康检查
curl http://localhost:8781/health

# Admin API 登录
curl -X POST http://localhost:8781/admin/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"your_admin_password"}'

# 获取 provider 列表（需要 admin token）
curl http://localhost:8781/admin/api/providers \
  -H "Authorization: Bearer YOUR_TOKEN"
```

---

## 六、常见问题

### Q1: 部署时提示 "no .env.local found"
**原因**: 缺少环境配置文件  
**解决**: 按照第一章创建 `.env.local` 文件

### Q2: 部署失败，提示 "credential decryption smoke test failed"
**原因**: 密钥配置错误或与数据库不匹配  
**解决**: 参考《故障排查指南》→ 凭据解密失败

### Q3: 如何查看部署日志
```bash
# 候选实例日志
docker logs llm-gateway-go-candidate

# 活跃实例日志
docker logs llm-gateway-go-active
```

### Q4: 如何回滚到上一个版本
```bash
./scripts/deploy-local.sh rollback
```

---

## 七、安全注意事项

1. **不要提交 `.env.local`**: 包含敏感密钥，已在 `.gitignore` 中排除
2. **密钥轮换**: 如果必须更换 `CREDENTIAL_ENCRYPTION_KEY`，需要先重新加密所有凭据
3. **访问控制**: 本地部署默认监听 127.0.0.1，避免暴露到公网
4. **日志脱敏**: 生产环境部署时确保日志中不包含明文密钥

---

## 八、参考资料

- [154 生产部署 SOP](./154-production-deployment-sop.md)
- [故障排查指南 - 凭据解密失败](../troubleshooting/credential-decrypt-failed.md)
- [2026-09-05 Provider 587 事故审计报告](../audit/2026-09-05-credential-decrypt-fix-audit.md)
- [环境配置示例](.env.local.example)
