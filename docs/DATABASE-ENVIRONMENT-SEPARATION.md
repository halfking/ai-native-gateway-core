# 数据库环境分离规范

**版本**: 1.0  
**日期**: 2026-06-30  
**状态**: 强制执行

---

## 一、环境概述

### 1.1 服务器拓扑

```
┌─────────────────────────────────────────────────────────────────┐
│                        生产环境 (71)                              │
│  服务器: __HOST_71_IP__ (公网IP，请勿公开)                        │
│  SSH端口: 25022                                                  │
│  认证: ~/.ssh/71_id_rsa                                          │
│  数据库: llm-gateway-pg-71-replica (Docker容器)                   │
│  用途: 生产数据，严禁未授权访问和修改                              │
│  标识: ENV=production, DB_ENV=prod                               │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│                        测试环境 (184)                             │
│  服务器: __INTERNAL_PUBLIC_IP__ (公网IP，请勿公开)                │
│  SSH端口: 25022                                                  │
│  认证: ~/.ssh/id_ed25519                                         │
│  数据库: llm-gateway-pg (K3s Pod, namespace: pms-test)            │
│  用途: 测试、开发、验证，可以进行数据同步和迁移测试                 │
│  标识: ENV=test, DB_ENV=test                                     │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│                        本地开发环境                               │
│  容器: r112_postgres (Docker容器)                                │
│  端口: 5432                                                      │
│  用途: 本地开发和测试，可从184同步数据                             │
│  标识: ENV=local, DB_ENV=local                                   │
└─────────────────────────────────────────────────────────────────┘
```

### 1.2 数据库差异说明

| 特征 | 71生产 | 184测试 | 本地开发 |
|------|--------|---------|----------|
| **数据来源** | 真实生产流量 | 测试流量 + 可能从71同步的历史数据 | 从184同步或本地生成 |
| **Schema版本** | 可能落后于184 | 最新开发版本 | 与184同步 |
| **数据一致性** | 权威数据源 | 可能与71有差异 | 仅用于开发 |
| **修改权限** | 仅运维+DBA | 开发+测试 | 完全权限 |
| **备份策略** | 每日全量+增量 | 每周全量 | 无备份要求 |

---

## 二、强制规则

### 2.1 生产环境保护 (71)

#### ❌ 禁止事项

1. **禁止未经授权直接部署到71**
   ```bash
   # ❌ 错误示例
   ./deploy.sh --target 71
   ssh root@__HOST_71_IP__ "docker restart llm-gateway-go"
   ```

2. **禁止未经审批修改71数据库结构**
   ```bash
   # ❌ 错误示例
   ssh root@__HOST_71_IP__ "docker exec llm-gateway-pg-71-replica psql ... -c 'ALTER TABLE ...'"
   ```

3. **禁止从71同步数据到其他环境**（数据泄露风险）
   ```bash
   # ❌ 错误示例
   ./scripts/sync-db-from-71.sh full
   ```

4. **禁止在脚本中硬编码71的IP地址或密码**
   ```bash
   # ❌ 错误示例
   SERVER="14.103.174.71"
   DB_PASSWORD="plaintext_password"
   ```

#### ✅ 允许的操作

1. **只读查询（需授权）**
   ```bash
   # ✅ 正确示例
   ssh root@__HOST_71_IP__ "docker exec llm-gateway-pg-71-replica psql -U llm_gateway -d llm_gateway -c 'SELECT count(*) FROM request_logs'"
   ```

2. **监控和诊断（非侵入式）**
   ```bash
   # ✅ 正确示例
   ./scripts/diagnose-routing.sh --env prod --read-only
   ```

3. **经过审批的Schema迁移**
   ```bash
   # ✅ 正确示例（需要提供审批记录）
   # 审批单号: CHG-2026-001
   # 审批人: [DBA Name]
   # 迁移脚本: migrations/036_xxx.sql
   ./scripts/apply-migration.sh --env prod --migration 036 --dry-run
   # 人工确认后再执行实际迁移
   ./scripts/apply-migration.sh --env prod --migration 036
   ```

### 2.2 测试环境使用规范 (184)

#### ✅ 允许的操作

1. **自由测试和开发**
   ```bash
   # ✅ 允许
   ./scripts/deploy-verify-from-184.sh full
   ./scripts/sync-db-from-184.sh schema-only
   ```

2. **数据库迁移测试**
   ```bash
   # ✅ 允许
   ssh root@__INTERNAL_PUBLIC_IP__ "kubectl -n pms-test exec deployment/llm-gateway-pg -- psql ..."
   ```

3. **从184同步到本地**
   ```bash
   # ✅ 允许
   ./scripts/sync-db-from-184.sh full
   ```

#### ⚠️ 注意事项

- 184数据可能包含敏感测试数据，不应公开分享
- 184的Schema可能领先于71，部署到71前需要验证兼容性

### 2.3 脚本编写规范

#### 强制使用环境变量

所有脚本必须使用占位符，禁止硬编码IP地址：

```bash
# ✅ 正确示例
SERVER_71="${SERVER_71:-__HOST_71_IP__}"
SERVER_184="${SERVER_184:-__INTERNAL_PUBLIC_IP__}"
DB_PASSWORD="${DB_PASSWORD:-__REDACTED_DB_PASSWORD__}"

# ❌ 错误示例
SERVER_71="14.103.174.71"
SERVER_184="14.103.112.184"
DB_PASSWORD="actual_password_here"
```

#### 必须包含环境确认

```bash
# ✅ 正确示例
if [ "$TARGET_ENV" == "prod" ] || [ "$TARGET_ENV" == "71" ]; then
    echo "⚠️  目标环境: 生产环境 (71)"
    echo "此操作需要运维审批。请提供审批单号："
    read -r APPROVAL_ID
    if [ -z "$APPROVAL_ID" ]; then
        echo "❌ 未提供审批单号，操作取消"
        exit 1
    fi
    echo "审批单号: $APPROVAL_ID"
    echo "继续操作前请再次确认 (yes/no):"
    read -r CONFIRM
    if [ "$CONFIRM" != "yes" ]; then
        echo "❌ 操作取消"
        exit 1
    fi
fi
```

---

## 三、数据同步策略

### 3.1 允许的同步方向

```
┌──────────┐
│ 71 生产   │ ❌ 禁止同步出
└──────────┘
     ↓ (禁止)
┌──────────┐
│ 184 测试  │ ✅ 可以同步到本地
└──────────┘
     ↓ (允许)
┌──────────┐
│ 本地开发  │
└──────────┘
```

### 3.2 同步脚本清单

| 脚本 | 源 | 目标 | 状态 | 说明 |
|------|-----|------|------|------|
| `sync-db-from-184.sh` | 184测试 | 本地 | ✅ 允许 | 用于本地开发 |
| `sync-db-from-71.sh` | 71生产 | 本地 | ❌ 已禁用 | 已重命名为 `sync-db-from-71.sh.DISABLED` |
| `deploy-verify-from-184.sh` | 184测试 | 本地 | ✅ 允许 | 一键验证脚本 |

### 3.3 禁用71同步脚本

```bash
# 为防止误操作，71同步脚本已被重命名
mv scripts/sync-db-from-71.sh scripts/sync-db-from-71.sh.DISABLED
```

如需从71同步（紧急情况，需DBA授权）：
```bash
# 1. 获取授权
# 2. 临时恢复脚本
cp scripts/sync-db-from-71.sh.DISABLED scripts/sync-db-from-71.sh.temp
# 3. 执行同步（添加审计日志）
APPROVAL_ID="CHG-2026-XXX" ./scripts/sync-db-from-71.sh.temp schema-only 2>&1 | tee sync-from-71-$(date +%Y%m%d-%H%M%S).log
# 4. 清理
rm scripts/sync-db-from-71.sh.temp
```

---

## 四、部署流程

### 4.1 到184测试环境

```bash
# 1. 本地测试通过
docker compose -f docker-compose.local-r112.yml up -d
./scripts/local-r112-smoke.sh

# 2. 构建镜像（在184服务器上）
ssh root@__INTERNAL_PUBLIC_IP__ "cd /opt/official-deploy/services/llm-gateway-go && \
    docker build -t kx-llm-gateway:test-$(date +%Y%m%d) ."

# 3. 部署到184
ssh root@__INTERNAL_PUBLIC_IP__ "kubectl -n pms-test set image deployment/llm-gateway app=kx-llm-gateway:test-$(date +%Y%m%d)"

# 4. 验证
curl -s https://llmgateway-test.kxpms.cn/healthz | jq .
```

### 4.2 到71生产环境（需审批）

```bash
# ⚠️  此流程需要变更管理审批

# 1. 提交变更申请
# - 变更内容: [描述]
# - 影响范围: [评估]
# - 回滚方案: [说明]
# - 测试验证: [184测试报告]

# 2. 获得审批后，执行部署
APPROVAL_ID="CHG-2026-XXX" ./scripts/deploy-to-prod.sh --env 71 --version v1.2.3

# 3. 验证
./scripts/verify-prod-deployment.sh --env 71

# 4. 观察
# 观察15分钟，监控错误率、延迟等指标

# 5. 如有问题，立即回滚
APPROVAL_ID="CHG-2026-XXX" ./scripts/rollback-prod.sh --env 71
```

---

## 五、环境变量配置

### 5.1 .env.example 更新

已更新 `.env.example` 文件，包含以下占位符：

```bash
# ── 服务器 IP ──────────────────────────────────────────────────
# 71 服务器公网 IP（生产环境）
HOST_71_IP=__HOST_71_IP__
# 184 服务器公网 IP（测试环境）
INTERNAL_PUBLIC_IP=__INTERNAL_PUBLIC_IP__

# ── SSH 连接 ───────────────────────────────────────────────────
SSH_PORT_71=25022
SSH_PORT_184=25022
SSH_KEY_71_PATH=$HOME/.ssh/71_id_rsa
SSH_KEY_184_PATH=$HOME/.ssh/id_ed25519
SSHPASS_71=__REDACTED_SSH_PASSWORD__

# ── 数据库 ─────────────────────────────────────────────────────
DB_USER=__DB_USER__
DB_ADMIN_PASSWORD=__REDACTED_DB_PASSWORD__
```

### 5.2 真实值存储位置

真实的IP地址、密码等敏感信息存储在：
- **仓库外**: `~/Documents/llm-gateway-env.md` (本地加密文件)
- **密码管理器**: 1Password / LastPass (推荐)
- **服务器**: `/etc/llm-gateway-go/env` (chmod 600)

---

## 六、技能文件修正清单

### 6.1 需要修正的技能

1. **deploy-acc** (`~/.agents/skills/deploy-acc/SKILL.md`)
   - 替换所有 `14.103.112.184` 为 `__INTERNAL_PUBLIC_IP__`
   - 添加环境确认提示

2. **kx-image-build** (`~/.agents/skills/kx-image-build/SKILL.md`)
   - 替换所有硬编码IP
   - 更新服务器拓扑说明

3. **disk-usage** 和 **disk-cleaner**
   - 替换184 IP为占位符

4. **pms-system-validation** (`~/.agents/skills/pms-system-validation/SKILL.md`)
   - 替换所有硬编码IP
   - 添加71生产环境警告

5. **trendaradar-deployment-validation**
   - 更新部署目标配置

### 6.2 修正原则

- 使用环境变量占位符替代硬编码IP
- 添加生产环境操作警告
- 提供环境配置说明链接
- 更新文档中的示例命令

---

## 七、监控和审计

### 7.1 操作审计

所有生产环境操作必须记录：

```bash
# 审计日志格式
{
  "timestamp": "2026-06-30T10:00:00Z",
  "operator": "user@example.com",
  "operation": "schema_migration",
  "target_env": "prod_71",
  "approval_id": "CHG-2026-XXX",
  "result": "success",
  "details": "Applied migration 036_add_index"
}
```

### 7.2 访问日志

所有SSH访问71服务器的操作自动记录到：
- `/var/log/auth.log` (服务器端)
- 本地日志: `~/.ssh/access-log-71.log`

---

## 八、应急响应

### 8.1 数据泄露响应

如发现71生产数据泄露：
1. 立即断开相关脚本和连接
2. 通知DBA和安全团队
3. 审计所有近期访问日志
4. 评估影响范围
5. 按照应急预案处理

### 8.2 误操作恢复

如误操作71数据库：
1. 立即停止操作
2. 通知DBA
3. 从备份恢复（如需要）
4. 填写事故报告
5. 更新操作规范

---

## 九、FAQ

**Q: 为什么禁止从71同步数据？**  
A: 71是生产环境，数据包含真实用户信息，同步到开发环境存在数据泄露风险。测试应使用184的测试数据。

**Q: 如何在本地开发时使用类生产数据？**  
A: 从184测试环境同步，184会定期从71同步部分脱敏数据（由DBA执行）。

**Q: 紧急情况下如何快速部署到71？**  
A: 联系运维团队，提供紧急变更单号，使用快速审批流程。

**Q: Schema迁移如何在71和184之间同步？**  
A: 先在184测试，验证通过后由DBA review，再部署到71。使用版本号管理迁移脚本。

**Q: 如何确认当前连接的是哪个环境？**  
A: 查看 `$DB_ENV` 环境变量，或执行：
```sql
SELECT current_database(), inet_server_addr();
```

---

## 十、相关文档

- [部署检查清单](./partition/MONTHLY_CHECKLIST.md)
- [数据库迁移规范](./docs/architecture/migration-guide.md)
- [安全审计报告](./SECURITY-AUDIT-2026-06-28.md)
- [环境变量配置说明](./.env.example)

---

## 附录A: 占位符对照表

| 占位符 | 说明 | 真实值位置 |
|--------|------|-----------|
| `__HOST_71_IP__` | 71生产服务器公网IP | ~/Documents/llm-gateway-env.md |
| `__INTERNAL_PUBLIC_IP__` | 184测试服务器公网IP | ~/Documents/llm-gateway-env.md |
| `__INTERNAL_K8S_HOST__` | 184内网K8s节点IP | ~/Documents/llm-gateway-env.md |
| `__INTERNAL_DOCKER_HOST__` | 184内网Docker主机IP | ~/Documents/llm-gateway-env.md |
| `__REDACTED_DB_PASSWORD__` | 数据库密码 | 密码管理器 |
| `__REDACTED_SSH_PASSWORD__` | SSH密码 | 密码管理器 |
| `__DB_USER__` | 数据库用户名 | ~/Documents/llm-gateway-env.md |

---

**最后更新**: 2026-06-30  
**维护者**: DevOps Team  
**审批**: DBA Team
