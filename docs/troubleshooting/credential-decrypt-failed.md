# 故障排查：凭据解密失败 (credential decrypt_failed)

## 一、症状识别

### 1.1 典型表现
- Admin API 中 provider 的凭据显示 `decrypt_failed` 状态
- 网关无法调用上游 API（所有请求失败）
- 用户报告特定 provider 不可用

### 1.2 日志特征
网关启动或运行时出现以下日志：

```
no decryption key available (keyring=false, fernet=32 bytes)
```

正常情况应该看到：
```
AES-GCM keyring initialized (source: env/CREDENTIAL_ENCRYPTION_KEY)
```

---

## 二、根因诊断

### 2.1 检查密钥配置

#### 步骤 1: 检查环境变量注入
```bash
# 对于 Docker 部署
docker inspect llm-gateway-go-active | grep -A 5 "LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY"

# 对于 systemd 部署
systemctl show llm-gateway-go --property=Environment | grep CREDENTIAL_ENCRYPTION_KEY
```

**期望结果**: 变量值应该是完整的 base64url 编码字符串（约 43 字符）  
**异常情况**: 
- 变量不存在
- 值为空字符串
- 值明显过短

#### 步骤 2: 检查 .env.local 文件
```bash
# 本地部署
grep "LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY" .env.local

# 远端部署
ssh root@<server> 'grep CREDENTIAL_ENCRYPTION_KEY /opt/llm-gateway-go/.env'
```

**常见问题**:
- 文件不存在（在 worktree 或临时目录部署）
- 变量未设置或注释掉
- 值为占位符文本（如 "请替换为固定随机串"）

#### 步骤 3: 检查网关初始化逻辑
查看网关日志开头的 keyring 初始化消息：

```bash
docker logs llm-gateway-go-active 2>&1 | head -50 | grep -i keyring
```

**正常输出**:
```
keyring initialized: source=env/CREDENTIAL_ENCRYPTION_KEY method=AES-GCM
```

**异常输出**:
```
no decryption key available (keyring=false, fernet=32 bytes)
keyring initialization skipped: no valid key source
```

### 2.2 检查密钥与数据库匹配

#### 步骤 1: 查询数据库加密凭据
```bash
psql "$LLM_GATEWAY_DATABASE_URL" -c "
SELECT provider_id, COUNT(*) as count
FROM credentials
WHERE secret_ciphertext IS NOT NULL AND secret_ciphertext != ''
GROUP BY provider_id
ORDER BY provider_id;
"
```

如果返回结果 > 0，说明数据库中存在加密凭据，**必须**配置正确的密钥。

#### 步骤 2: 使用 Admin API 验证解密
```bash
# 登录获取 token
TOKEN=$(curl -s -X POST http://localhost:8781/admin/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"your_password"}' \
  | jq -r .token)

# 获取 credentials 列表
curl -s http://localhost:8781/admin/api/credentials \
  -H "Authorization: Bearer $TOKEN" \
  | jq '[.[] | {provider_id, credential_id, status: (if .secret_plaintext then "ok" else "decrypt_failed" end)}]'
```

**期望结果**: 所有 credential 的 `status` 都是 `"ok"`  
**异常情况**: 出现 `"decrypt_failed"` 的条目

#### 步骤 3: 离线解密验证（高级）
使用部署冒烟测试脚本手动验证：

```bash
# 设置环境变量
export LLM_GATEWAY_DATABASE_URL="your_db_url"
export LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY="your_key"

# 运行验证脚本
python3 scripts/verify_credential_decrypt.py
```

---

## 三、典型故障案例

### 案例 1: 2026-09-05 Provider 587 凭据解密失败

#### 背景
- **时间**: 2026-09-05 17:48
- **版本**: 2.5.0.1950
- **影响范围**: 本地部署，所有 provider 587 的凭据显示 `decrypt_failed`

#### 根因（双重故障）
1. **部署环境问题**: 从缺少 `.env.local` 的 worktree 发起部署，导致 `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY` 为空
2. **代码缺陷**: `cmd/gateway/main.go:2692` 的 keyring 初始化守卫逻辑错误，未检查 `SECRET_KEY` 回退路径

#### 诊断步骤

**1. 检查网关日志**
```bash
docker logs llm-gateway-go-active 2>&1 | grep keyring
# 输出: no decryption key available (keyring=false, fernet=32 bytes)
```

**2. 检查容器环境变量**
```bash
docker inspect llm-gateway-go-active | grep CREDENTIAL_ENCRYPTION_KEY
# 输出: "LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY="  （空值）
```

**3. 使用 Admin API 验证**
```bash
curl http://localhost:8781/admin/api/credentials -H "Authorization: Bearer $TOKEN"
# 所有 provider 587 的 credentials 返回 secret_plaintext: null
```

**4. 检查数据库**
```sql
SELECT provider_id, credential_id, 
       secret_ciphertext IS NOT NULL as is_encrypted
FROM credentials 
WHERE provider_id = 587;
```
结果显示 7 条加密凭据（`is_encrypted = true`），确认需要密钥才能解密。

#### 修复流程

**1. 从 245 同步正确的密钥**
```bash
# 获取 245 的密钥
ssh -p 25022 root@8.136.114.245 \
  'grep CREDENTIAL_ENCRYPTION_KEY /opt/llm-gateway-go/.env'

# 写入本地 .env.local
echo "export LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=\"<从245获取的值>\"" >> .env.local
```

**2. 重新部署**
```bash
source .env.local
./scripts/deploy-local.sh deploy
```

**3. 验证修复**
```bash
# 检查容器环境变量
docker inspect llm-gateway-go-active | grep CREDENTIAL_ENCRYPTION_KEY
# 应该看到完整的密钥值

# 检查网关日志
docker logs llm-gateway-go-active 2>&1 | grep keyring
# 输出: AES-GCM keyring initialized

# 部署冒烟测试（自动执行）
# 输出: providers=587 creds=7 failed=0 ✅
```

#### 预防措施
代码层已修复（commit 0145f46d0）：
1. 修复 keyring 初始化守卫逻辑，启用 `SECRET_KEY` 回退路径
2. 部署脚本新增 `gate_credential_encryption_key()` 前置检查
3. 部署脚本新增 `smoke_credential_decrypt()` 自动冒烟测试
4. `.env.local` 缺失时显式告警

---

## 四、修复方案

### 方案 A: 恢复正确的密钥（推荐）

#### 适用场景
- 数据库中存在加密凭据
- 能够获取到原始密钥（从备份或其他环境）

#### 操作步骤
```bash
# 1. 从源环境获取密钥
ssh root@<source_server> 'grep CREDENTIAL_ENCRYPTION_KEY /path/to/.env'

# 2. 更新本地配置
vim .env.local
# 设置: export LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY="<正确的密钥>"

# 3. 重新部署
source .env.local
./scripts/deploy-local.sh deploy

# 4. 验证解密
curl http://localhost:8781/admin/api/credentials -H "Authorization: Bearer $TOKEN" \
  | jq '[.[] | select(.secret_plaintext == null)] | length'
# 输出应该是 0（没有解密失败的凭据）
```

### 方案 B: 重新加密凭据（高风险，需停机）

#### 适用场景
- 原始密钥已丢失且无法恢复
- 可以接受停机维护窗口
- 能够获取到明文凭据（从 provider 后台重新获取）

#### 操作步骤（谨慎执行）
```bash
# 1. 生成新密钥
NEW_KEY=$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=')
echo "新密钥: $NEW_KEY"

# 2. 备份数据库
pg_dump "$LLM_GATEWAY_DATABASE_URL" > backup_before_rekey.sql

# 3. 清空现有加密凭据（或手动更新明文）
psql "$LLM_GATEWAY_DATABASE_URL" -c "
UPDATE credentials 
SET secret_ciphertext = NULL, secret_plaintext = '<从provider后台获取的新值>'
WHERE secret_ciphertext IS NOT NULL;
"

# 4. 更新 .env.local
export LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY="$NEW_KEY"

# 5. 重新部署（新版本会自动加密 secret_plaintext）
./scripts/deploy-local.sh deploy

# 6. 验证加密
psql "$LLM_GATEWAY_DATABASE_URL" -c "
SELECT provider_id, COUNT(*) 
FROM credentials 
WHERE secret_ciphertext IS NOT NULL 
GROUP BY provider_id;
"
```

### 方案 C: 降级到未加密模式（不推荐，仅测试环境）

#### 适用场景
- 非生产环境
- 测试或开发目的
- 不关心凭据安全性

#### 操作步骤
```bash
# 1. 将所有凭据转为明文存储
psql "$LLM_GATEWAY_DATABASE_URL" -c "
UPDATE credentials 
SET secret_ciphertext = NULL;
"

# 2. 移除加密密钥配置
vim .env.local
# 注释掉: # export LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY="..."

# 3. 重新部署
./scripts/deploy-local.sh deploy
```

**警告**: 此方案会将 API key 以明文存储在数据库中，存在安全风险。

---

## 五、预防措施

### 5.1 部署前检查清单
- [ ] `.env.local` 文件存在且已加载（`source .env.local`）
- [ ] `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY` 已设置且非空
- [ ] 如果数据库来自远端同步，确认密钥与源环境一致
- [ ] 在非主工作目录（worktree）部署时，确认环境变量已正确传播

### 5.2 自动化保护
从版本 2.5.1+ 开始，部署脚本已内置保护机制：

1. **前置门禁 (`gate_credential_encryption_key`)**
   - 检测数据库中是否存在加密凭据
   - 如果存在但密钥为空，拒绝部署

2. **冒烟测试 (`smoke_credential_decrypt`)**
   - 部署后自动验证凭据解密
   - 失败时自动回滚到旧版本

配置冒烟测试的检查范围：
```bash
# .env.local 中设置
export LLM_GATEWAY_DECRYPT_SMOKE_PROVIDER_ID="587,18,1"  # 逗号分隔的 provider id
```

### 5.3 例行巡检
建议定期（每周或每次部署后）运行凭据解密验证：

```bash
# 本地
./scripts/verify_credential_decrypt.sh

# 远端服务器
ssh root@<server> "cd /opt/llm-gateway-go && ./scripts/verify_credential_decrypt.sh"
```

---

## 六、相关资源

- [本地部署指南](../deployment/local-deployment-guide.md) - 密钥配置详细说明
- [2026-09-05 Provider 587 事故审计报告](../../AUDIT_CREDENTIAL_DECRYPT_FIX_20260905.md) - 完整事故复盘
- [.env.local.example](../../.env.local.example) - 环境变量配置模板
- 代码修复: commit `0145f46d0` (2026-09-05)

---

## 七、故障决策树

```
凭据显示 decrypt_failed
    │
    ├─→ 检查网关日志中是否有 "keyring=false"
    │   │
    │   ├─→ 是 → 检查 CREDENTIAL_ENCRYPTION_KEY 环境变量
    │   │        │
    │   │        ├─→ 未设置/为空 → 方案A: 从源环境同步密钥
    │   │        │
    │   │        └─→ 已设置 → 检查密钥是否与数据库匹配
    │   │                     │
    │   │                     ├─→ 不匹配 → 方案A: 恢复正确密钥
    │   │                     │
    │   │                     └─→ 匹配但仍失败 → 检查代码版本（是否 < 2.5.1）
    │   │                                        │
    │   │                                        └─→ 升级到 2.5.1+ (含 keyring 修复)
    │   │
    │   └─→ 否 → 检查数据库 secret_ciphertext 字段
    │            │
    │            ├─→ 为空 → 凭据未加密，检查 secret_plaintext
    │            │
    │            └─→ 非空 → 可能是加密算法不匹配或数据损坏
    │                      → 方案B: 重新加密（需停机）
    │
    └─→ 仅部分 provider 失败 → 检查这些 provider 的凭据是否最近更新
                                 → 可能是更新时未使用正确的加密密钥
                                 → 单独重新设置这些凭据
```

---

**最后更新**: 2026-09-06  
**维护者**: DevOps Team
