# 🚀 执行修复 - 三种方案

## 当前状态
✅ 问题已诊断：252数据库缺少 `request_wal_hot` 表  
✅ 修复工具已准备  
⏳ 等待执行修复

---

## 方案1: 一键修复（交互式，推荐）

```bash
cd /Users/xutaohuang/workspace/llm-gateway-go-cursor
./scripts/complete-fix-252.sh
```

**特点**：
- ✅ 交互式引导
- ✅ 自动选择最佳方案
- ✅ 包含验证步骤
- ✅ 自动重启服务

**适用场景**：首次执行，需要引导

---

## 方案2: 在252服务器上执行（最简单）

### 步骤1: 上传脚本
```bash
scp scripts/fix-252-local.sh root@192.168.0.252:/tmp/
```

### 步骤2: 在252上执行
```bash
ssh root@192.168.0.252 'chmod +x /tmp/fix-252-local.sh && /tmp/fix-252-local.sh'
```

### 步骤3: 重启154服务
```bash
ssh root@192.168.0.154 'systemctl restart llm-gateway'
```

### 步骤4: 验证
```bash
ssh root@192.168.0.252 'sudo -u postgres psql -d llm_gateway -c "SELECT COUNT(*), MAX(created_at) FROM request_wal_hot WHERE created_at > NOW() - INTERVAL '\''5 minutes'\'';"'
```

**特点**：
- ✅ 无需数据库密码
- ✅ 在服务器本地执行，更安全
- ✅ 适合有SSH访问权限的场景

---

## 方案3: 手动复制SQL执行

### 步骤1: 在252服务器上创建SQL文件

```bash
ssh root@192.168.0.252
```

然后执行：

```bash
cat > /tmp/fix-request-wal-hot.sql << 'EOF'
BEGIN;

CREATE TABLE IF NOT EXISTS request_wal_hot (
    request_id character varying(64) NOT NULL,
    tenant_id character varying(64) NOT NULL,
    gw_session_id character varying(128),
    status character varying(20) DEFAULT 'pending'::character varying NOT NULL,
    stage smallint DEFAULT 0 NOT NULL,
    client_model character varying(100),
    upstream_provider_id bigint,
    upstream_credential_id bigint,
    completion_tokens integer,
    prompt_tokens integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    upstream_request_at timestamp with time zone,
    upstream_response_at timestamp with time zone,
    error text,
    compression_strategy character varying(50),
    compression_meta jsonb,
    CONSTRAINT request_wal_hot_pkey PRIMARY KEY (request_id, created_at)
) WITH (fillfactor=90);

CREATE TABLE IF NOT EXISTS request_wal_bodies (
    request_id character varying(64) NOT NULL,
    outbound_body text,
    compression_meta jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT request_wal_bodies_pkey PRIMARY KEY (request_id)
);

DROP VIEW IF EXISTS request_wal_with_current_month;
CREATE VIEW request_wal_with_current_month AS
SELECT * FROM request_wal_hot
UNION ALL
SELECT * FROM request_wal;

COMMIT;
EOF
```

### 步骤2: 执行SQL
```bash
sudo -u postgres psql -d llm_gateway -f /tmp/fix-request-wal-hot.sql
```

### 步骤3: 验证
```bash
sudo -u postgres psql -d llm_gateway -c "SELECT COUNT(*) FROM request_wal_hot;"
```

### 步骤4: 退出并重启154服务
```bash
exit  # 退出252
ssh root@192.168.0.154 'systemctl restart llm-gateway'
```

**特点**：
- ✅ 完全手动控制
- ✅ 适合了解PostgreSQL的用户
- ✅ 可以逐步验证

---

## 快速验证命令

修复后，使用以下命令验证：

### 检查表是否存在
```bash
ssh root@192.168.0.252 'sudo -u postgres psql -d llm_gateway -c "\d request_wal_hot"'
```

### 检查最近5分钟的记录
```bash
ssh root@192.168.0.252 'sudo -u postgres psql -d llm_gateway -c "SELECT COUNT(*), MAX(created_at) FROM request_wal_hot WHERE created_at > NOW() - INTERVAL '\''5 minutes'\'';"'
```

### 查看最新记录
```bash
ssh root@192.168.0.252 'sudo -u postgres psql -d llm_gateway -c "SELECT request_id, status, client_model, created_at FROM request_wal_hot ORDER BY created_at DESC LIMIT 5;"'
```

### 检查154服务状态
```bash
ssh root@192.168.0.154 'systemctl status llm-gateway'
```

### 查看154服务日志
```bash
ssh root@192.168.0.154 'journalctl -u llm-gateway -n 50 --no-pager'
```

---

## 预期结果

### 修复成功的标志

1. **表创建成功**
   ```
   SELECT COUNT(*) FROM request_wal_hot;
   ```
   能正常返回结果（即使是0）

2. **写入测试成功**
   ```
   INSERT INTO request_wal_hot (...) VALUES (...);
   ```
   不报错

3. **5分钟后有新记录**
   ```
   SELECT COUNT(*) FROM request_wal_hot WHERE created_at > NOW() - INTERVAL '5 minutes';
   ```
   返回 count > 0

---

## 推荐执行顺序

### 🥇 首选：方案2（在252服务器上执行）
简单、安全、无需密码

### 🥈 备选：方案1（一键修复）
如果需要交互式引导

### 🥉 最后：方案3（手动执行）
如果前两种方案都不可行

---

## 现在开始执行

选择一个方案，复制命令，开始修复：

```bash
# 推荐：方案2
scp scripts/fix-252-local.sh root@192.168.0.252:/tmp/
ssh root@192.168.0.252 'chmod +x /tmp/fix-252-local.sh && /tmp/fix-252-local.sh'
ssh root@192.168.0.154 'systemctl restart llm-gateway'

# 等待1-2分钟后验证
ssh root@192.168.0.252 'sudo -u postgres psql -d llm_gateway -c "SELECT COUNT(*), MAX(created_at) FROM request_wal_hot WHERE created_at > NOW() - INTERVAL '\''5 minutes'\'';"'
```

---

## 需要帮助？

- 📖 详细文档: `cat docs/QUICK_FIX_252.md`
- 🔍 故障排查: `docs/QUICK_FIX_252.md` 中的故障排查部分
- 📊 技术分析: `cat docs/issues/REQUEST_LOGGING_FIX_252.md`

---

**预计总时间**: 5-10分钟  
**难度**: ⭐⭐☆☆☆ (简单)  
**风险**: ⭐☆☆☆☆ (极低，脚本是幂等的)
