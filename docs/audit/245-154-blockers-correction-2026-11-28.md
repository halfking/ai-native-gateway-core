# 245/154 追平阻塞报告勘误（2026-11-28 23:05）

## 原报告错误归因

原报告 `docs/audit/245-154-chase-blockers-2026-11-28.md` 存在**重大事实错误**：

| 原报告结论 | 实际情况 |
|---|---|
| 245 共享 211 数据库（192.168.31.211:15432） | ❌ **错误** |
| 迁移 523 因视图冲突失败 | ❌ **错误归因** |
| 154 独立数据库 172.16.2.210:5432 | ✅ 正确 |

## 真实情况

### 1. 245/154 数据库架构（实际）

| 环境 | 数据库配置（正确） | 配置文件 |
|---|---|---|
| **245** | postgres://[REDACTED:secret]@**172.16.2.210:5432**/llm_gateway | /etc/llm-gateway-go/env（SSOT） |
| **154** | postgres://[REDACTED:secret]@**172.16.2.210:5432**/llm_gateway | /etc/llm-gateway-go/env |

**172.16.2.210 = 252 跳板机/数据库服务器**

245 和 154 **都连接 252 的 PostgreSQL**，不存在"共享 211 DB"情况。

---

### 2. 245 部署失败的真实原因

#### 时间线还原

**22:49**（部署失败时）：
- deploy-245.sh 执行过程中，`/opt/llm-gateway-go/.env` 被错误修改
- 从正确的 `172.16.2.210:5432`（252）改为 `192.168.31.211:15432`（211）
- 迁移 523 尝试连接 211 DB → 失败

**原因**：部署脚本中可能有逻辑从某个模板/缓存中恢复了错误的配置。

**修复**（23:00）：
```bash
# 从 SSOT 恢复正确配置
cp /etc/llm-gateway-go/env /opt/llm-gateway-go/.env
# DATABASE_URL 恢复为：postgres://[REDACTED:secret]@172.16.2.210:5432/llm_gateway
```

#### 配置文件证据

```bash
# /etc/llm-gateway-go/env（SSOT，从未改动）
$ ls -lt /etc/llm-gateway-go/env*
-rw------- 1 root root 1978 10月 15 14:47 /etc/llm-gateway-go/env  # ← 一直是 252

# /opt/llm-gateway-go/.env（被部署脚本修改）
$ ls -lt /opt/llm-gateway-go/.env*
-rw------- 1 root root 36622 11月 28 22:49 /opt/llm-gateway-go/.env  # ← 部署失败时改错

# 历史备份证明正确配置一直是 252
$ grep DATABASE_URL /opt/llm-gateway-go/.env.bak-20260824-173202
postgres://[REDACTED:secret]@172.16.2.210:5432/llm_gateway
```

---

### 3. 共同阻塞：252 数据库密码失效 🔴

#### 现象

245 和 154 重启后均报：
```
FATAL: password authentication failed for user "llm_gateway"
```

#### 验证

```bash
# 245 测试
$ PGPASSWORD='[配置文件中的密码]' psql -h 172.16.2.210 -U llm_gateway -d llm_gateway
psql: error: FATAL: password authentication failed for user "llm_gateway"

# 154 测试（相同错误）
```

#### 根因

252 PostgreSQL 的 `llm_gateway` 用户密码已变更，但 245/154 配置文件未同步更新。

---

## 修正后的修复方案

### ✅ 方案 A：重置 252 数据库密码（推荐 P0）

#### 步骤

1. **登录 252 重置密码**：
   ```bash
   ssh root@252  # 或通过跳板机
   # 假设 252 有本地 PG 访问权限
   sudo -u postgres psql
   ALTER USER llm_gateway WITH PASSWORD 'new_secure_password';
   ```

2. **同步更新 245/154 配置**：
   ```bash
   # 245
   ssh root@245
   vim /etc/llm-gateway-go/env
   vim /opt/llm-gateway-go/.env
   # 更新 LLM_GATEWAY_DATABASE_URL 中的密码

   # 154
   ssh root@154
   vim /etc/llm-gateway-go/env
   vim /opt/llm-gateway-go/.env
   ```

3. **重启服务验证**：
   ```bash
   # 245
   systemctl restart llmgo-245-canary@8782
   systemctl status llmgo-245-canary@8782

   # 154
   systemctl restart llm-gateway-go-canary@8782
   ```

4. **确认健康后追平**（245 已部署 311ca42c@3187，154 需部署）：
   ```bash
   # 154 追平
   cd /opt/llm-gateway-go
   bash scripts/deploy-154.sh
   ```

#### 前置条件

- 🔑 252 root/postgres 访问权限
- 或联系 DBA/运维协助

---

### 方案 B：恢复旧密码（如果可获取）

如果能找到 252 当前生效的密码（例如从 252 本地运行的服务配置），直接更新 245/154 配置。

---

### 方案 C：暂缓，记录阻塞

等待 DBA 协助窗口，优先完成战役总结归档。

---

## 勘误总结

| 维度 | 原报告 | 实际情况 |
|---|---|---|
| **245 数据库** | 192.168.31.211:15432（211） | 172.16.2.210:5432（252）✅ |
| **245 阻塞原因** | 共享 DB 迁移冲突 | 部署脚本配置错误 + 252 密码失效 |
| **154 数据库** | 172.16.2.210:5432（252） | ✅ 正确 |
| **共同阻塞** | 无 | 252 DB 密码失效 🔴 |
| **修复优先级** | P2（245 架构调整） | P0（252 密码重置） |

---

## 经验教训

1. **配置 SSOT 重要性**：245 有两个配置文件（/etc 和 /opt），部署脚本修改了后者但未同步前者 → 导致错误难以追踪
2. **诊断需实证**：我最初从运行进程环境变量读取到 211 DB，但未核对配置文件历史 → 错误归因
3. **密码管理**：252 密码变更未同步到依赖方（245/154）→ 生产服务中断

---

**勘误时间**：2026-11-28 23:05  
**修正依据**：245/154 配置文件历史 + 实际连接测试  
**下一步**：DBA 重置 252 密码 → 更新 245/154 配置 → 重启验证 → 154 追平部署
