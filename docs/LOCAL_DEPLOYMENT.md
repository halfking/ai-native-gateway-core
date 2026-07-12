# LLM Gateway 本地部署完整说明

## 问题总结

在本地部署过程中遇到的主要问题：
1. **数据库连接配置**: 需要使用 `LLM_GATEWAY_DATABASE_URL` 环境变量
2. **Schema依赖**: db.Open() 会执行大量 ensureXXXTable() 检查，缺少任何一个表都会导致数据库连接失败
3. **表结构不匹配**: 现有的简化建表SQL与代码期望的完整schema不一致

## 解决方案

### 选项 1: 无数据库模式（推荐用于前端开发）

```bash
cd /Users/xutaohuang/workspace/llm-gateway-go-4
bash scripts/deploy-minimal-nodb.sh
```

**特点**:
- ✅ 无需数据库
- ✅ 启动快速
- ✅ 适合前端开发
- ❌ 无登录功能
- ❌ 无Dashboard API数据

**访问**: http://localhost:8781/

---

### 选项 2: 完整数据库部署（需要完整迁移）

由于db/db.go中的Open()函数会检查约50+张表的schema，需要执行所有迁移脚本。

**步骤**:

1. 执行所有startup迁移（161个文件）
```bash
cd sql/migrations/startup
for f in *.sql; do
    PGPASSWORD=redclaw psql -h localhost -p 5434 -U redclaw -d redclaw -f "$f" 2>&1 | grep -iE "error|rollback" || true
done
```

2. 重启服务
```bash
bash scripts/local-deploy.sh restart
```

**注意**: 这需要10-20分钟执行所有迁移。

---

### 选项 3: Docker Compose 一键部署（待创建）

创建 `docker-compose.yml`:

```yaml
version: '3.8'
services:
  postgres:
    image: pgvector/pgvector:pg17
    environment:
      POSTGRES_USER: llmgw
      POSTGRES_PASSWORD: llmgw
      POSTGRES_DB: llmgw
    ports:
      - "5433:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data
      - ./sql/migrations/startup:/docker-entrypoint-initdb.d
  
  redis:
    image: redis:7-alpine
    ports:
      - "6380:6379"
  
  gateway:
    build: .
    ports:
      - "8781:8781"
    environment:
      LLM_GATEWAY_DATABASE_URL: postgres://llmgw:llmgw@postgres:5432/llmgw?sslmode=disable
      LLM_GATEWAY_REDIS_ADDR: redis:6379
      LLM_GATEWAY_ADMIN_PASSWORD: Veritrans&9527
      LLM_GATEWAY_CORS_ORIGINS: "*"
      LLM_GATEWAY_ENV: development
    depends_on:
      - postgres
      - redis

volumes:
  pgdata:
```

---

## 当前状态

**已创建的脚本**:
- ✅ `scripts/local-deploy.sh` - 基础部署脚本
- ✅ `scripts/deploy-minimal-nodb.sh` - 无数据库模式
- ✅ `scripts/deploy-from-scratch.sh` - 从头创建数据库
- ✅ `scripts/quick-fix-db.sh` - 快速修复现有数据库
- ✅ `scripts/init-minimal-db.sh` - 最小化表初始化

**数据库表状态** (redclaw数据库):
- ✅ users, tenants, auth_users
- ✅ sessions, session_summaries
- ✅ providers, applications, credentials  
- ✅ request_logs_hot, request_logs
- ✅ session_module_executions_hot (Dashboard)
- ✅ dashboard_access_events_hot (Dashboard)
- ❌ 还缺少约40+张表导致数据库连接失败

**服务状态**:
- ✅ 编译成功 (49MB)
- ✅ 健康检查通过 http://localhost:8781/healthz
- ⚠️ 数据库连接失败（缺少表）
- ❌ 登录失败 (database not configured)

---

## 推荐操作

### 用于开发测试
```bash
# 使用无数据库模式
bash scripts/deploy-minimal-nodb.sh

# 访问前端
open http://localhost:8781/
```

### 用于完整功能验证
```bash
# 1. 执行完整迁移（需要时间）
cd sql/migrations/startup
for sql in $(ls *.sql | sort -V); do
    echo "Executing: $sql"
    PGPASSWORD=redclaw psql -h localhost -p 5434 -U redclaw -d redclaw -f "$sql" > "/tmp/migration_$sql.log" 2>&1
done

# 2. 重启服务
cd ../../../
bash scripts/local-deploy.sh restart

# 3. 测试登录
curl -X POST http://localhost:8781/api/auth/token \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"Veritrans&9527"}'
```

---

## 登录凭证

- **用户名**: admin
- **密码**: Veritrans&9527
- **Bcrypt Hash**: `$2b$12$Qqs0L1OgsNok8IYW4rBe8ekHq.1Nz42/ehUhaTvtl6s182hhIdDTK`

---

## 问题排查

### 查看服务日志
```bash
tail -f /tmp/llm-gateway.log
```

### 检查数据库连接
```bash
PGPASSWORD=redclaw psql -h localhost -p 5434 -U redclaw -d redclaw -c "\dt"
```

### 查看缺失的表
```bash
tail -100 /tmp/llm-gateway.log | grep "does not exist"
```

---

## 后续优化建议

1. **简化db.Open()逻辑**: 
   - 将ensureXXXTable()改为非阻塞警告
   - 或添加 `LLM_GATEWAY_SKIP_SCHEMA_CHECK` 环境变量

2. **创建Docker镜像**:
   - 预装所有迁移的数据库镜像
   - 一键启动完整环境

3. **分离认证表**:
   - auth_users 独立管理
   - 不依赖完整业务表结构

4. **添加迁移管理工具**:
   - 使用 golang-migrate 或类似工具
   - 自动追踪已执行的迁移

---

生成时间: 2026-07-10 23:00
