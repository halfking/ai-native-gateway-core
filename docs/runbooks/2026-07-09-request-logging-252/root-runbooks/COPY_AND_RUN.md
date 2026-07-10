# 🚨 直接执行 - 复制以下命令

由于网络连接问题，请你**直接在终端执行以下命令**：

## 步骤1: 修复252数据库（2分钟）

```bash
ssh root@192.168.0.252 "sudo -u postgres psql -d llm_gateway << 'EOSQL'
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
SELECT * FROM request_wal_hot UNION ALL SELECT * FROM request_wal;

SELECT 'SUCCESS: Tables created' as status;

COMMIT;
EOSQL"
```

**预期输出**: 
```
BEGIN
CREATE TABLE
CREATE TABLE
DROP VIEW
CREATE VIEW
    status     
---------------
 SUCCESS: Tables created
COMMIT
```

---

## 步骤2: 验证表已创建（30秒）

```bash
ssh root@192.168.0.252 'sudo -u postgres psql -d llm_gateway -c "SELECT COUNT(*) as columns FROM information_schema.columns WHERE table_name = '\''request_wal_hot'\'';"'
```

**预期输出**: `17` (17列)

---

## 步骤3: 重启154服务（1分钟）

```bash
ssh root@192.168.0.154 'systemctl restart llm-gateway && systemctl status llm-gateway | head -10'
```

**预期输出**: `active (running)`

---

## 步骤4: 发送测试请求（1分钟）

```bash
curl -X POST https://llm.kxpms.cn/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}'
```

---

## 步骤5: 等待2分钟后验证（2分钟后）

```bash
sleep 120

ssh root@192.168.0.252 'sudo -u postgres psql -d llm_gateway -c "SELECT COUNT(*) as new_records, MAX(created_at) as latest FROM request_wal_hot WHERE created_at > NOW() - INTERVAL '\''5 minutes'\'';"'
```

**预期输出**: `new_records > 0` ✅

---

## 步骤6: 查看最新记录

```bash
ssh root@192.168.0.252 'sudo -u postgres psql -d llm_gateway -c "SELECT request_id, status, client_model, created_at FROM request_wal_hot ORDER BY created_at DESC LIMIT 5;"'
```

---

## 🎯 快速验证（一行命令）

如果你想一次执行所有步骤（除了测试请求），复制这个：

```bash
ssh root@192.168.0.252 "sudo -u postgres psql -d llm_gateway -c 'CREATE TABLE IF NOT EXISTS request_wal_hot (request_id varchar(64) NOT NULL, tenant_id varchar(64) NOT NULL, gw_session_id varchar(128), status varchar(20) DEFAULT '\''pending'\'' NOT NULL, stage smallint DEFAULT 0 NOT NULL, client_model varchar(100), upstream_provider_id bigint, upstream_credential_id bigint, completion_tokens integer, prompt_tokens integer, created_at timestamptz DEFAULT now() NOT NULL, completed_at timestamptz, upstream_request_at timestamptz, upstream_response_at timestamptz, error text, compression_strategy varchar(50), compression_meta jsonb, PRIMARY KEY (request_id, created_at)); CREATE TABLE IF NOT EXISTS request_wal_bodies (request_id varchar(64) PRIMARY KEY, outbound_body text, compression_meta jsonb, created_at timestamptz DEFAULT now() NOT NULL); DROP VIEW IF EXISTS request_wal_with_current_month; CREATE VIEW request_wal_with_current_month AS SELECT * FROM request_wal_hot UNION ALL SELECT * FROM request_wal; SELECT '\''Tables created'\'' as status;'" && ssh root@192.168.0.154 'systemctl restart llm-gateway'
```

---

## ❌ 如果SSH也不可用

直接登录252服务器，执行：

```bash
sudo -u postgres psql -d llm_gateway

-- 然后粘贴以下SQL：
CREATE TABLE IF NOT EXISTS request_wal_hot (
    request_id varchar(64) NOT NULL,
    tenant_id varchar(64) NOT NULL,
    gw_session_id varchar(128),
    status varchar(20) DEFAULT 'pending' NOT NULL,
    stage smallint DEFAULT 0 NOT NULL,
    client_model varchar(100),
    upstream_provider_id bigint,
    upstream_credential_id bigint,
    completion_tokens integer,
    prompt_tokens integer,
    created_at timestamptz DEFAULT now() NOT NULL,
    completed_at timestamptz,
    upstream_request_at timestamptz,
    upstream_response_at timestamptz,
    error text,
    compression_strategy varchar(50),
    compression_meta jsonb,
    PRIMARY KEY (request_id, created_at)
);

CREATE TABLE IF NOT EXISTS request_wal_bodies (
    request_id varchar(64) PRIMARY KEY,
    outbound_body text,
    compression_meta jsonb,
    created_at timestamptz DEFAULT now() NOT NULL
);

DROP VIEW IF EXISTS request_wal_with_current_month;
CREATE VIEW request_wal_with_current_month AS
SELECT * FROM request_wal_hot UNION ALL SELECT * FROM request_wal;

-- 验证
\dt request_wal_hot
SELECT COUNT(*) FROM request_wal_hot;

-- 退出
\q
```

然后重启154服务器：
```bash
ssh root@192.168.0.154 'systemctl restart llm-gateway'
```

---

## ✅ 成功标志

执行步骤5后，如果看到 `new_records > 0`，说明修复成功！

---

**现在请复制上面的命令，在你的终端中执行。**
