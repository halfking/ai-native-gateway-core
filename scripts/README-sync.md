# 数据库结构同步脚本使用指南

本目录包含用于本地和252服务器之间数据库结构双向同步的脚本。

---

## 📁 脚本文件

| 脚本 | 方向 | 用途 |
|------|------|------|
| `sync-db-to-252.sh` | 本地 → 252 | 推送本地新表到252（主要使用） |
| `sync-db-from-252.sh` | 252 → 本地 | 拉取252新表到本地 |

---

## 🚀 快速开始

### 推送本地新表到252（常用）

```bash
# 增量同步（推荐）
./scripts/sync-db-to-252.sh

# 先预览不执行
./scripts/sync-db-to-252.sh --dry-run

# 全量重新生成
./scripts/sync-db-to-252.sh --full

# 仅同步指定表
./scripts/sync-db-to-252.sh --tables "table1 table2 table3"
```

### 从252拉取新表到本地

```bash
# 增量同步
./scripts/sync-db-from-252.sh

# 先预览不执行
./scripts/sync-db-from-252.sh --dry-run

# 仅同步指定表
./scripts/sync-db-from-252.sh --tables "training_human_annotations"
```

---

## 📖 使用场景

### 场景1：开发新功能后推送到测试环境

```bash
# 1. 本地开发完成，确认迁移已运行
docker exec llm-gateway-pg psql -U postgres -d llm_gateway -c "\dt+ new_feature_*"

# 2. 预览要同步的表
./scripts/sync-db-to-252.sh --dry-run

# 3. 执行同步
./scripts/sync-db-to-252.sh

# 4. 验证252
ssh root@<env:HOST_252_IP> -p 25022
docker exec pg-252-pg17 psql -U postgres -d llm_gateway -c "\dt+ new_feature_*"
```

### 场景2：252有新表，本地需要拉取

```bash
# 1. 检查252有哪些新表
./scripts/sync-db-from-252.sh --dry-run

# 2. 拉取到本地
./scripts/sync-db-from-252.sh

# 3. 验证本地
docker exec llm-gateway-pg psql -U postgres -d llm_gateway -c "\dt+ 表名"
```

### 场景3：部署前完整对齐

```bash
# 1. 推送本地所有新表到252
./scripts/sync-db-to-252.sh --full

# 2. 拉取252的新表到本地
./scripts/sync-db-from-252.sh --full
```

---

## ⚙️ 配置要求

### 1. 环境变量

脚本会自动加载以下配置：

```bash
# 从 ../envs/loader.sh 加载项目配置
# 从 configs/env-252.sh 加载252服务器配置

# 必需的环境变量：
COMMON_PG_SUPERUSER_PASS   # 252数据库密码
REMOTE_PG_IP               # 252内网IP (默认: 10.88.0.79)
REMOTE_SSH_HOST            # 252外网IP (默认: <env:HOST_252_IP>)
REMOTE_SSH_PORT            # SSH端口 (默认: 25022)
LOCAL_TUNNEL_PORT          # 本地隧道端口 (默认: 15432)
```

### 2. SSH隧道

脚本会自动检查并建立SSH隧道：

```bash
# 自动建立的隧道
ssh -f -N -L 15432:10.88.0.79:5432 -p 25022 root@<env:HOST_252_IP>
```

---

## 🛡️ 安全特性

### 幂等性

所有脚本使用 `CREATE TABLE IF NOT EXISTS` 语法：
- ✅ 可重复执行，不会报错
- ✅ 不会覆盖已存在的表
- ✅ 不会删除任何数据

### 无事务模式

使用 `ON_ERROR_STOP=0`：
- ✅ 单个表失败不影响其他表
- ✅ 适合大批量同步
- ⚠️ 部分成功时需要重新运行

### 自动跳过

- 跳过 `auth_*` 系列表（redclaw外部服务）
- 跳过分区表的父表依赖问题
- 记录警告但继续执行

---

## 📊 输出说明

### 成功输出示例

```
[INFO] 数据库同步：本地 → 252
========================================
[INFO] 加载环境配置...
[SUCCESS] SSH隧道已存在
[INFO] 发现 15 个需要同步的表
[INFO]   导出表: knowledge_base
[SUCCESS]   CREATE TABLE
[SUCCESS]   CREATE INDEX
...
========================================
[INFO] 同步完成
========================================
[SUCCESS] 本地表数量: 479
[SUCCESS] 252表数量:  477
⚠️  差异: 2 个表（可能是分区表或配置差异）
```

---

## 🔗 相关文档

- [数据库双向对齐报告](../docs/audit/2026-09-06-db-bidirectional-sync-report.md)
- [252服务器环境配置](../configs/env-252.sh)

---

**维护**: ZCode Agent  
**最后更新**: 2026-09-06
