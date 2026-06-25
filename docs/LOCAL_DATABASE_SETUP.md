# 本地开发数据库配置

## 数据库连接信息

### 生产环境
```
Host: __INTERNAL_K8S_HOST__
Port: 5432
Database: llm_gateway
Username: llm_gateway
Password: 4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg
Connection String: postgres://llm_gateway:4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg@__INTERNAL_K8S_HOST__:5432/llm_gateway?sslmode=disable
```

### 本地开发环境设置

#### 方案 1: 使用 SSH 隧道连接生产数据库（推荐用于查询）

```bash
# 创建 SSH 隧道
ssh -L 5433:__INTERNAL_K8S_HOST__:5432 root@__HOST_71_IP__

# 然后在本地连接
psql "postgres://llm_gateway:4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg@localhost:5433/llm_gateway?sslmode=disable"
```

#### 方案 2: 在本地启动 PostgreSQL + Citus（推荐用于开发）

```bash
# 使用 Docker Compose 启动本地数据库
cat > docker-compose.yml << 'EOF'
version: '3.8'

services:
  postgres:
    image: citusdata/citus:12.1
    container_name: llm-gateway-db
    environment:
      POSTGRES_USER: llm_gateway
      POSTGRES_PASSWORD: localdev123
      POSTGRES_DB: llm_gateway
    ports:
      - "5432:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data
      - ./db/migrations:/docker-entrypoint-initdb.d
    command: >
      postgres
      -c shared_preload_libraries=citus,citus_columnar
      -c max_connections=200

volumes:
  pgdata:
EOF

# 启动
docker-compose up -d

# 连接
psql "postgres://llm_gateway:localdev123@localhost:5432/llm_gateway?sslmode=disable"
```

#### 方案 3: 从生产环境同步数据到本地

```bash
#!/bin/bash
# sync_db_to_local.sh

# 1. 从生产环境导出 schema 和数据
ssh root@__HOST_71_IP__ << 'ENDSSH'
export PGPASSWORD='4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg'

# 导出 schema
pg_dump -h __INTERNAL_K8S_HOST__ -U llm_gateway -d llm_gateway \
  --schema-only \
  --no-owner --no-acl \
  > /tmp/llm_gateway_schema.sql

# 导出最近 7 天的数据（避免数据量过大）
pg_dump -h __INTERNAL_K8S_HOST__ -U llm_gateway -d llm_gateway \
  --data-only \
  --no-owner --no-acl \
  --table=request_logs \
  --table=usage_ledger \
  --table=credit_ledger \
  --table=tool_usage_stats \
  --where="ts > NOW() - INTERVAL '7 days'" \
  > /tmp/llm_gateway_data.sql 2>/dev/null || true

# 压缩
tar czf /tmp/llm_gateway_dump.tar.gz /tmp/llm_gateway_*.sql
ENDSSH

# 2. 下载到本地
scp root@__HOST_71_IP__:/tmp/llm_gateway_dump.tar.gz /tmp/

# 3. 解压
tar xzf /tmp/llm_gateway_dump.tar.gz -C /tmp/

# 4. 导入到本地数据库
export PGPASSWORD='localdev123'
psql -h localhost -U llm_gateway -d llm_gateway < /tmp/tmp/llm_gateway_schema.sql
psql -h localhost -U llm_gateway -d llm_gateway < /tmp/tmp/llm_gateway_data.sql

echo "✅ 数据库同步完成"
```

## 配置应用连接本地数据库

### 环境变量配置

创建 `.env.local` 文件：

```bash
# .env.local
export LLM_GATEWAY_DATABASE_URL="postgres://llm_gateway:localdev123@localhost:5432/llm_gateway?sslmode=disable"
export LLM_GATEWAY_PORT=8781
export LLM_GATEWAY_LOG_LEVEL=debug

# 其他配置...
```

使用：
```bash
source .env.local
go run ./cmd/llm-gateway-go
```

### 或使用配置文件

创建 `config.local.yaml`：

```yaml
database_url: "postgres://llm_gateway:localdev123@localhost:5432/llm_gateway?sslmode=disable"
port: 8781
log_level: debug
```

使用：
```bash
go run ./cmd/llm-gateway-go --config config.local.yaml
```

## 数据库查询工具推荐

### 1. psql（命令行）
```bash
psql "postgres://llm_gateway:4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg@__INTERNAL_K8S_HOST__:5432/llm_gateway?sslmode=disable"
```

### 2. DBeaver（GUI，免费）
```
Connection Type: PostgreSQL
Host: __INTERNAL_K8S_HOST__
Port: 5432
Database: llm_gateway
Username: llm_gateway
Password: 4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg
```

### 3. DataGrip（JetBrains，付费）
```
同上配置
```

### 4. pgAdmin（Web GUI，免费）
```bash
docker run -p 5050:80 \
  -e PGADMIN_DEFAULT_EMAIL=admin@local.com \
  -e PGADMIN_DEFAULT_PASSWORD=admin \
  dpage/pgadmin4

# 访问 http://localhost:5050
# 添加服务器连接配置
```

## 常用查询

### 查看所有表
```sql
\dt

-- 或
SELECT tablename, pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename))
FROM pg_tables 
WHERE schemaname = 'public'
ORDER BY pg_total_relation_size(schemaname||'.'||tablename) DESC;
```

### 查看最近的请求
```sql
SELECT 
    request_id,
    client_model,
    success,
    latency_ms,
    prompt_tokens,
    completion_tokens,
    LEFT(response_preview, 50) as preview,
    ts
FROM request_logs
ORDER BY ts DESC
LIMIT 10;
```

### 查看分区信息
```sql
SELECT 
    parent.relname as table_name,
    child.relname as partition_name,
    pg_get_expr(child.relpartbound, child.oid) as range,
    am.amname as storage,
    pg_size_pretty(pg_relation_size(child.oid)) as size
FROM pg_inherits
JOIN pg_class parent ON pg_inherits.inhparent = parent.oid
JOIN pg_class child ON pg_inherits.inhrelid = child.oid
LEFT JOIN pg_am am ON child.relam = am.oid
WHERE parent.relname IN ('request_logs', 'usage_ledger', 'credit_ledger', 'tool_usage_stats')
ORDER BY parent.relname, child.relname;
```

## 安全提示

⚠️ **生产数据库密码已暴露在此文档中**

建议：
1. 仅在内网或 VPN 中访问生产数据库
2. 不要在公网提交包含密码的文件
3. 考虑定期轮换数据库密码
4. 使用只读账户进行查询
5. 生产环境操作前务必备份

## 快速开始

**最简单的方式**（使用 SSH 隧道）：

```bash
# 终端 1: 创建隧道
ssh -L 5433:__INTERNAL_K8S_HOST__:5432 root@__HOST_71_IP__

# 终端 2: 连接数据库
psql "postgres://llm_gateway:4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg@localhost:5433/llm_gateway?sslmode=disable"

# 查询数据
llm_gateway=# SELECT COUNT(*) FROM request_logs;
llm_gateway=# SELECT * FROM request_logs ORDER BY ts DESC LIMIT 5;
```

完成！
