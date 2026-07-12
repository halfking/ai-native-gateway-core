# R1.12 本地环境部署指南

## 概述

R1.12 本地环境是完整的 llm-gateway-go 开发测试栈，包含：
- PostgreSQL (Citus 11.3) 数据库
- Redis 缓存
- OpenAI-compatible mock upstream
- Gateway v1 & v2
- Vue 3 前端 SPA

## 快速启动

```bash
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
docker-compose -f docker-compose.local-r112.yml up -d
```

访问: http://localhost:8781

## 数据库初始化

### 首次启动

首次启动需要初始化数据库 schema 和 seed 数据：

```bash
# 1. 应用 baseline schema
docker exec r112_postgres psql -U kxuser -d llm_gateway -f /dev/stdin < deploy/sql/schemas/baseline/01-schema.sql

# 2. 应用 seed 数据
docker exec r112_postgres psql -U kxuser -d llm_gateway -f /dev/stdin < deploy/sql/schemas/baseline/02-seed.sql

# 3. 确保 must_change_password 列存在
docker exec r112_postgres psql -U kxuser -d llm_gateway -c "ALTER TABLE users ADD COLUMN IF NOT EXISTS must_change_password BOOLEAN NOT NULL DEFAULT false;"

# 4. 重置 admin 密码（使用 Go bcrypt）
cd /tmp && cat > hashpw.go << 'HASHEOF'
package main
import (
	"fmt"
	"golang.org/x/crypto/bcrypt"
)
func main() {
	hash, _ := bcrypt.GenerateFromPassword([]byte("Veritrans&9527"), bcrypt.DefaultCost)
	fmt.Println(string(hash))
}
HASHEOF

HASH=$(cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go && go run /tmp/hashpw.go)
docker exec r112_postgres psql -U kxuser -d llm_gateway -c "UPDATE users SET password_hash = '$HASH', must_change_password = false WHERE username = 'admin';"
```

### 密码哈希注意事项

⚠️ **重要**: 必须使用 Go 的 `golang.org/x/crypto/bcrypt` 生成密码哈希（前缀 `$2a$`），不能使用 Python 的 bcrypt（前缀 `$2b$`）。Go 的 bcrypt 库不识别 `$2b$` 前缀。

## 默认凭据

- **用户名**: admin
- **密码**: Veritrans&9527
- **角色**: super_admin

## 端口映射

- `8781`: Gateway v1 (主入口)
- `8782`: Gateway v2 (需要 `LLM_GATEWAY_V2_ENABLED=1`)
- `5432`: PostgreSQL
- `6379`: Redis

## 健康检查

```bash
# Gateway 健康检查
curl http://localhost:8781/healthz

# 数据库连接检查
docker exec r112_postgres psql -U kxuser -d llm_gateway -c "SELECT 1;"

# Redis 连接检查
docker exec r112_redis redis-cli ping
```

## 重置环境

```bash
# 停止并删除所有容器
docker ps -a --format "{{.Names}}" | grep r112 | xargs -I {} docker rm -f {}

# 删除数据卷（清空数据库）
docker volume rm r112_pg_data r112_redis_data

# 重新启动
docker-compose -f docker-compose.local-r112.yml up -d
```

## 常见问题

### 1. 登录返回 401 "Invalid credentials"

**原因**: 密码哈希格式不匹配（Python bcrypt vs Go bcrypt）

**解决**:
```bash
HASH=$(cd /path/to/llm-gateway-go && go run /tmp/hashpw.go)
docker exec r112_postgres psql -U kxuser -d llm_gateway -c "UPDATE users SET password_hash = '$HASH' WHERE username = 'admin';"
```

### 2. 登录返回 403 "password change required"

**原因**: `must_change_password` 字段为 `true`

**解决**:
```bash
docker exec r112_postgres psql -U kxuser -d llm_gateway -c "UPDATE users SET must_change_password = false WHERE username = 'admin';"
```

### 3. Gateway 日志显示 "postgres disabled"

**原因**: `request_logs` 表不存在

**解决**: 应用 baseline schema（见上文"数据库初始化"）

### 4. Port 8781 already in use

**原因**: 旧容器未清理

**解决**:
```bash
docker ps -a --format "{{.Names}}" | grep r112 | xargs -I {} docker rm -f {}
docker-compose -f docker-compose.local-r112.yml up -d
```

## UI 验收测试

使用 Playwright 进行端到端测试：

```bash
cd /path/to/llm-gateway-go
python3 << 'PYEOF'
from playwright.sync_api import sync_playwright
import time, json

with sync_playwright() as p:
    browser = p.chromium.launch(headless=False)
    page = browser.new_page(viewport={"width": 1280, "height": 900})
    
    # 1. Landing page
    page.goto('http://localhost:8781/')
    time.sleep(2)
    
    # 2. Login
    page.click('button:has-text("Sign in")')
    page.fill('input[type="text"]', 'admin')
    page.fill('input[type="password"]', 'Veritrans&9527')
    page.click('button:has-text("登录")')
    time.sleep(5)
    
    # 3. Navigate to modules
    page.goto('http://localhost:8781/admin/modules')
    time.sleep(3)
    
    # 4. Click WeChat module
    page.click('text=微信机器人')
    time.sleep(2)
    
    browser.close()
PYEOF
```

## 架构说明

```
┌─────────────────────────────────────────┐
│  Browser (localhost:8781)               │
└──────────────┬──────────────────────────┘
               │
               ▼
┌─────────────────────────────────────────┐
│  r112_gateway (Go)                      │
│  - Vue SPA static serving               │
│  - /api/auth/* (login, me)              │
│  - /api/admin/* (modules, config)       │
│  - /v1/* (LLM gateway)                  │
└──┬───────────────┬──────────────────────┘
   │               │
   ▼               ▼
┌──────────────┐ ┌──────────────┐
│ r112_postgres│ │ r112_redis   │
│ (Citus 11.3) │ │              │
└──────────────┘ └──────────────┘
```

## 相关文档

- `docker-compose.local-r112.yml` - Compose 配置
- `deploy/sql/schemas/baseline/01-schema.sql` - 完整 schema
- `deploy/sql/schemas/baseline/02-seed.sql` - Seed 数据
- `web/` - Vue 3 前端源码
