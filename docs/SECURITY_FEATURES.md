# 安全特性与威胁模型 — glowing-tiger

**最后更新**: 2026-07-12  
**版本**: v1.0

---

## 📋 目录

1. [安全特性清单](#安全特性清单)
2. [威胁模型](#威胁模型)
3. [安全配置建议](#安全配置建议)
4. [安全审计](#安全审计)
5. [应急响应](#应急响应)

---

## 🛡️ 安全特性清单

### 1. Ed25519 数字签名（License Authority）

**位置**: `cmd/license-authority/middleware/sigverify.go`

**功能**:
- 防止请求伪造和篡改
- 基于 Ed25519 椭圆曲线加密算法
- 签名消息格式: `timestamp:nonce:body`

**参数**:
- `X-Instance-ID`: 实例唯一标识
- `X-Timestamp`: Unix 时间戳（最大时钟偏移 300 秒）
- `X-Nonce`: 唯一随机值（防重放）
- `X-Signature`: Base64 编码的签名

**防护措施**:
- ✅ 时钟偏移检查（MaxClockSkew = 300s）
- ✅ Nonce 重放检测（TTL = 5 分钟）
- ✅ 签名验证失败返回 401

**测试覆盖**:
- `cmd/license-authority/middleware/sigverify.go` - 实现
- `tests/security/audit.sh` - 集成测试

---

### 2. JWT 认证（Bearer Token）

**位置**: `middleware/auth_mw.go`, `cmd/license-authority/jwt_eddsa_test.go`

**功能**:
- 基于 JWT (JSON Web Token) 的无状态认证
- 使用 Ed25519 签名算法（EdDSA）
- Token 有效期：7 天

**Claims 字段**:
```json
{
  "sub": "instance:<instance_id>",
  "iss": "llm.kxpms.cn",
  "iat": <issued_at>,
  "exp": <expires_at>,
  "license_key_hash": "<sha256_hash>"
}
```

**防护措施**:
- ✅ 签名验证（Ed25519）
- ✅ 过期检查（7 天 TTL）
- ✅ 常量时间比较（防时序攻击）

**测试覆盖**:
- `cmd/license-authority/jwt_eddsa_test.go` - 单元测试
- `tests/security/audit.sh` - 集成测试

---

### 3. Nonce 防重放攻击

**位置**: 
- 内存存储: `cmd/license-authority/middleware/sigverify.go`
- Redis 存储: `cmd/license-authority/middleware/redis_nonce.go`

**功能**:
- 分布式环境下防止请求重放
- 支持内存和 Redis 两种存储后端

**实现细节**:
- Nonce TTL: 5 分钟
- Redis Key: `licensing:nonce:<nonce>`
- 清理周期: 1 分钟（内存模式）

**防护措施**:
- ✅ Nonce 唯一性检查
- ✅ 自动过期清理
- ✅ 分布式一致性（Redis 模式）

---

### 4. SQL 注入防护

**位置**: 所有数据库操作

**实现方式**:
- 使用 `pgx` 参数化查询（占位符 `$1`, `$2` 等）
- 禁止字符串拼接 SQL

**示例**:
```go
// ✅ 安全：参数化查询
rows, err := db.Query(ctx, 
    "SELECT * FROM licenses WHERE instance_id = $1", 
    instanceID)

// ❌ 危险：字符串拼接
query := fmt.Sprintf("SELECT * FROM licenses WHERE instance_id = '%s'", instanceID)
```

**检测工具**:
- `tests/security/code_review.sh` - 自动扫描 SQL 拼接

---

### 5. 路径穿越防护

**位置**: `autoupdate/downloader.go`

**防护措施**:
- ✅ 使用 `filepath.Clean()` 清理路径
- ✅ 检查 `../` 序列
- ✅ 限制文件操作在指定目录内
- ✅ SHA256 校验和验证

**示例**:
```go
// 文件路径清理
tmpPath := filepath.Join(d.downloadDir, filename+".tmp")
finalPath := filepath.Join(d.downloadDir, filename)

// 校验和验证
if checksum != expectedChecksum {
    return fmt.Errorf("checksum mismatch")
}
```

---

### 6. 敏感信息保护

**措施**:
- ✅ `.env` 文件在 `.gitignore` 中
- ✅ 密码/Token 使用 `slog` 结构化日志（不打印值）
- ✅ API Key 使用常量时间比较（`subtle.ConstantTimeCompare`）
- ✅ 避免在日志中打印敏感字段

**日志规范**:
```go
// ✅ 安全
slog.Info("auth success", "instance_id", instanceID)

// ❌ 危险
fmt.Printf("token: %s", token)
```

---

## 🎯 威胁模型

### 1. 中间人攻击（MITM）

**威胁**: 攻击者拦截 HTTP 通信，窃取/篡改数据

**缓解措施**:
- ✅ **生产环境强制 HTTPS**（TLS 1.2+）
- ✅ Ed25519 签名防止消息篡改
- ✅ Nginx/Ingress 配置 TLS 证书

**配置要求**:
```bash
# 生产环境必须使用 HTTPS
LICENSE_AUTHORITY_URL=https://license.kxpms.cn
```

**风险等级**: 🔴 HIGH（未使用 HTTPS 时）

---

### 2. DDoS 拒绝服务攻击

**威胁**: 大量请求导致服务不可用

**缓解措施**:
- ✅ **Nginx 限流**（生产环境建议）
- ⚠️ **应用层速率限制**（可选）
- ✅ **健康检查端点**（`/healthz`）

**Nginx 配置示例**:
```nginx
limit_req_zone $binary_remote_addr zone=api_limit:10m rate=100r/s;

location /api/ {
    limit_req zone=api_limit burst=20 nodelay;
}
```

**风险等级**: 🟡 MEDIUM

---

### 3. 密钥泄露

**威胁**: Ed25519 私钥、JWT 签名密钥泄露

**缓解措施**:
- ✅ **密钥定期轮换**（建议 90 天）
- ✅ **密钥文件权限**（0600）
- ✅ **环境变量注入**（不硬编码）
- ✅ **密钥分离**（开发/生产环境）

**密钥管理**:
```bash
# 生成新密钥对
openssl genpkey -algorithm ed25519 -out private.pem
openssl pkey -in private.pem -pubout -out public.pem

# 设置文件权限
chmod 600 private.pem
chmod 644 public.pem
```

**风险等级**: 🔴 HIGH

---

### 4. SQL 注入

**威胁**: 恶意 SQL 代码注入

**缓解措施**:
- ✅ **参数化查询**（pgx 占位符）
- ✅ **代码审查**（`tests/security/code_review.sh`）
- ✅ **最小权限原则**（数据库用户）

**风险等级**: 🟢 LOW（已全面防护）

---

### 5. 重放攻击

**威胁**: 攻击者重放合法请求

**缓解措施**:
- ✅ **Nonce 机制**（5 分钟 TTL）
- ✅ **时间戳验证**（300 秒时钟偏移）
- ✅ **Redis 分布式存储**（多实例环境）

**风险等级**: 🟢 LOW（已防护）

---

### 6. 路径穿越

**威胁**: 攻击者访问/覆盖任意文件

**缓解措施**:
- ✅ **路径清理**（`filepath.Clean`）
- ✅ **目录限制**（仅在 `downloadDir` 内操作）
- ✅ **文件校验**（SHA256 checksum）

**风险等级**: 🟢 LOW（已防护）

---

## ⚙️ 安全配置建议

### 生产环境配置清单

- [ ] **TLS/HTTPS**
  - `LICENSE_AUTHORITY_URL` 必须使用 `https://`
  - 配置有效的 TLS 证书（Let's Encrypt / 商业证书）
  - 禁用 TLS 1.0/1.1，强制 TLS 1.2+

- [ ] **数据库安全**
  - PostgreSQL 启用 SSL 连接
  - 数据库用户使用最小权限（只读/读写分离）
  - 密码强度要求（16+ 字符）

- [ ] **Redis 安全**
  - 启用密码保护（`requirepass`）
  - 绑定内网 IP（不暴露公网）
  - 启用持久化（AOF/RDB）

- [ ] **密钥管理**
  - 使用环境变量或密钥管理服务（Vault/AWS Secrets Manager）
  - 定期轮换密钥（90 天）
  - 密钥文件权限 0600

- [ ] **日志审计**
  - 记录所有认证失败事件
  - 定期审查异常请求（高频、非法签名）
  - 保留日志 90 天+

- [ ] **限流配置**
  - Nginx: 100 req/s per IP
  - 应用层: 可根据业务调整

- [ ] **监控告警**
  - 认证失败率 > 5% 告警
  - Nonce 重放检测告警
  - 数据库连接池耗尽告警

---

### 开发环境配置

```bash
# .env.example
LICENSE_AUTHORITY_URL=http://localhost:8081
GATEWAY_URL=http://localhost:8080
POSTGRES_HOST=localhost
POSTGRES_PORT=5432
REDIS_HOST=localhost
REDIS_PORT=6379
```

⚠️ **注意**: 开发环境可使用 HTTP，生产环境必须 HTTPS

---

## 🔍 安全审计

### 自动化测试

**运行安全审计**:
```bash
# 运行所有安全测试
./tests/security/audit.sh

# 代码安全审查
./tests/security/code_review.sh

# 安装 gosec（可选）
go install github.com/securego/gosec/v2/cmd/gosec@latest
gosec -fmt json -out gosec-report.json ./...
```

**测试覆盖**:
- ✅ Ed25519 签名验证
- ✅ JWT 认证
- ✅ SQL 注入检测
- ✅ 路径穿越检测
- ✅ 敏感信息泄露检测
- ✅ 速率限制测试

---

### 手动审查清单

**每季度审查**:
- [ ] 审查所有新增的 `os.Open` / `exec.Command` 调用
- [ ] 审查数据库查询是否使用参数化
- [ ] 审查日志中是否包含敏感信息
- [ ] 审查密钥轮换记录
- [ ] 审查访问日志异常模式

---

## 🚨 应急响应

### 密钥泄露应急流程

1. **立即操作**（0-1 小时）
   - 吊销泄露的密钥
   - 生成新密钥对
   - 更新所有客户端配置

2. **影响评估**（1-4 小时）
   - 审查日志中的异常请求
   - 识别潜在被攻击的实例
   - 通知受影响客户

3. **长期措施**（1-7 天）
   - 强制所有客户端更新
   - 实施更严格的密钥管理策略
   - 编写事后分析报告

### 认证绕过应急流程

1. **立即操作**
   - 启用维护模式
   - 关闭受影响端点
   - 热修复或回滚

2. **根因分析**
   - 定位漏洞代码
   - 编写测试用例
   - 开发修复补丁

3. **修复部署**
   - 灰度发布修复版本
   - 验证修复有效性
   - 全量发布

---

## 📚 参考资源

- [OWASP Top 10](https://owasp.org/www-project-top-ten/)
- [Go Security Best Practices](https://github.com/OWASP/Go-SCP)
- [Ed25519 Signature Scheme](https://ed25519.cr.yp.to/)
- [JWT Best Practices](https://datatracker.ietf.org/doc/html/rfc8725)

---

## 📧 安全联系方式

**安全问题报告**: security@internal.example.com  
**响应时间**: 3 个工作日内

⚠️ **请勿在公开 Issue 中披露安全漏洞**
