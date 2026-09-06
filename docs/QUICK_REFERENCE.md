# LLM Gateway Go — 快速参考手册

> **版本**: v2.5.3  
> **最后更新**: 2026-09-06  
> **适用场景**: 快速查询、问题排查、日常开发  

---

## 📋 目录

- [项目信息](#项目信息)
- [目录结构速查](#目录结构速查)
- [常用命令](#常用命令)
- [核心API端点](#核心api端点)
- [配置参考](#配置参考)
- [数据库速查](#数据库速查)
- [常见问题](#常见问题)
- [故障排查](#故障排查)

---

## 项目信息

### 基本信息
```
项目名称:     LLM Gateway Go
当前版本:     v2.5.3-c1fd9f4c-20260905-1957
Go版本:       1.27.1
主入口:       cmd/gateway/main.go
默认端口:     8781 (Gateway 与 Admin 同端口，/api/admin/*)
```

### 仓库地址
```bash
# 主开发仓库（日常使用）
git remote add origin https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git

# 公开镜像仓库
git remote add github git@github.com:halfking/SI-LLM-Gateway.git
```

### 代码统计（基于真实扫描，2026-09-06）
```
Go源文件:      22,921 个    (find . -name "*.go" -type f | wc -l)
Admin API:       453 个    (find ./admin -name "*.go" -type f | wc -l)
后台Worker:      217 个    (find ./bg -name "*.go" -type f | wc -l)
领域模块:        64 个    (ls ./domains/ | wc -l)
数据库迁移:     757 个    (find ./sql/migrations -name "*.sql" | wc -l)
单元测试:     5,000+ 个    (*_test.go 文件)
可执行程序:      30 个    (ls ./cmd/ | wc -l)
```

---

## 目录结构速查

### 顶层目录
```
├── cmd/           # 可执行程序（30个）
│   ├── gateway/   # 主网关程序 ⭐
│   └── tools/     # 工具程序
├── domains/       # 领域模型（64个）⭐
├── admin/         # Admin API（453文件）⭐
├── bg/            # 后台Worker（217文件）⭐
├── web/           # Vue管理面板 ⭐
├── installer/     # 安装器（独立模块）⭐
├── adapter/       # 协议适配器
├── internal/      # 内部共享代码
├── middleware/    # HTTP中间件
├── migrations/    # 数据库迁移（主迁移在 db/migrations/ 和 sql/migrations/）
├── scripts/       # 自动化脚本
├── docs/          # 文档（110+文件）
└── deploy/        # 部署配置
```

### 核心领域模块
```
domains/
├── streaming/          # 流式处理 ⭐
├── dispatch/           # 调度分发 ⭐
├── routing/            # 路由决策 ⭐
├── credential/         # 凭据管理 ⭐
├── ursm/              # 统一路由状态管理 ⭐
├── session/           # 会话管理
├── authentication/    # 认证
├── security/          # 安全引擎
├── hooks/             # 钩子系统
├── autoroute/         # 自动路由（model=auto）
└── ...               # 其他75+领域
```

---

## 常用命令

### 编译与运行
```bash
# 编译网关
go build -o gateway ./cmd/gateway

# 编译安装器
cd installer && go build -o llm-gw-installer ./cmd/llm-gw-installer

# 运行网关（开发模式；无命令行 flag，配置经环境变量传入）
LLM_GATEWAY_CONFIG_FILE=<yaml 配置文件> LLM_GATEWAY_LISTEN=:8781 ./gateway

# 运行网关（生产模式）
LLM_GATEWAY_CONFIG_FILE=/etc/llm-gateway/config.yaml ./gateway
```

### 测试
```bash
# 运行所有单元测试
go test ./... -short

# 运行特定模块测试
go test ./domains/streaming/...

# 运行集成测试（需要PostgreSQL/Redis）
go test ./... -tags=integration

# 生成覆盖率报告
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out

# 运行基准测试
go test -bench=. -benchmem ./domains/streaming/
```

### Linter与格式化
```bash
# 运行所有linter
./scripts/lint.sh

# Go格式化
gofmt -w .

# Go imports
goimports -w .

# 多租户作用域检查
go run ./cmd/tools/lint-tenant-scope

# PostgreSQL RLS检查
go run ./cmd/tools/lint-pg-rls

# OpenTelemetry租户标签检查
go run ./cmd/tools/lint-otel-tenant
```

### 数据库操作
```bash
# 运行迁移（升级）
migrate -path migrations -database "postgres://..." up

# 回滚最后一次迁移
migrate -path migrations -database "postgres://..." down 1

# 查看迁移状态
migrate -path migrations -database "postgres://..." version

# 创建新迁移
migrate create -ext sql -dir migrations -seq add_new_feature
```

### 部署相关
```bash
# 检查License状态
./installer/llm-gw-installer license status

# 激活License（在线）
./installer/llm-gw-installer activate --mode online --license-key LIC-xxx

# 激活License（离线）
./installer/llm-gw-installer activate --mode offline --request-file activation.req

# 检查更新
./installer/llm-gw-installer upgrade check

# 在线升级
./installer/llm-gw-installer upgrade apply --to v2.5.3

# 回滚
./installer/llm-gw-installer upgrade rollback --to v2.5.2
```

### 工具命令
```bash
# 检查凭据健康
./cmd/check-credentials/check-credentials --tenant-id xxx

# 探测凭据
./cmd/probe-cred/probe-cred --credential-id xxx --model gpt-4

# 配置导出
./cmd/cfg_dump/cfg_dump --output config.yaml

# 会话取证
./cmd/sessionforensics/sessionforensics --session-id xxx

# 质量计算
./cmd/calc_quality/calc_quality --model-id xxx --days 7
```

---

## 核心API端点

### 数据面（LLM调用）

#### OpenAI协议
```bash
# Chat Completions
POST http://localhost:8781/v1/chat/completions
Content-Type: application/json
Authorization: Bearer sk-xxx

{
  "model": "gpt-4",
  "messages": [{"role": "user", "content": "Hello"}],
  "stream": true
}

# Completions（Legacy）
POST http://localhost:8781/v1/completions

# Embeddings
POST http://localhost:8781/v1/embeddings
```

#### Anthropic协议
```bash
# Messages
POST http://localhost:8781/v1/messages
Content-Type: application/json
x-api-key: sk-ant-xxx
anthropic-version: 2023-06-01

{
  "model": "claude-3-opus-20240229",
  "messages": [{"role": "user", "content": "Hello"}],
  "max_tokens": 1024,
  "stream": true
}
```

#### Gemini协议
```bash
# Generate Content
POST http://localhost:8781/v1/models/gemini-pro:generateContent
Content-Type: application/json
x-goog-api-key: xxx

{
  "contents": [{"parts": [{"text": "Hello"}]}]
}
```

### 控制面（Admin API）

#### 认证
```bash
# 登录
POST http://localhost:8781/api/auth/token
{
  "username": "admin",
  "password": "xxx"
}

# 响应
{
  "token": "eyJhbGc...",
  "expires_at": "2026-09-07T10:00:00Z"
}
```

#### 租户管理
```bash
# 创建租户
POST http://localhost:8781/api/admin/tenants
Authorization: Bearer eyJhbGc...

# 查询租户列表
GET http://localhost:8781/api/admin/tenants?page=1&page_size=20

# 租户详情
GET http://localhost:8781/api/admin/tenants/:id

# 更新租户
PUT http://localhost:8781/api/admin/tenants/:id

# 删除租户
DELETE http://localhost:8781/api/admin/tenants/:id
```

#### 凭据管理
```bash
# 凭据监控总览
GET http://localhost:8781/api/credentials/monitor-summary

# 凭据健康热力图
GET http://localhost:8781/api/credentials/heatmap

# 查看凭据密钥
POST http://localhost:8781/api/credentials/:id/reveal

# 轮换凭据主 Key
POST http://localhost:8781/api/credentials/:id/set-key
```

#### 监控仪表盘
```bash
# 总览
GET http://localhost:8781/api/admin/dashboard/board

# 实时会话监控
GET http://localhost:8781/api/admin/dashboard/session-active

# 统计数据
GET http://localhost:8781/api/admin/stats
```

#### 审计查询
```bash
# 审计操作日志（super_admin）
GET http://localhost:8781/api/admin/audit-logs?page=1&size=50

# 会话审计记录
GET http://localhost:8781/api/admin/session-audit?session_id=xxx

# 导出会话审计
GET http://localhost:8781/api/admin/session-audit/export?tenant_id=xxx
```

---

## 配置参考

### 环境变量
```bash
# 数据库
export DB_HOST=localhost
export DB_PORT=5432
export DB_NAME=llm_gateway
export DB_USER=postgres
export DB_PASSWORD=xxx
export DB_SSLMODE=disable
export DB_MAX_CONNS=100

# Redis
export LLM_GATEWAY_REDIS_ADDR=127.0.0.1:6379
export REDIS_PORT=6379
export REDIS_PASSWORD=xxx
export REDIS_DB=0

# 网关
export LLM_GATEWAY_LISTEN=:8781
export LLM_GATEWAY_LOG_LEVEL=info

# 对象存储（OSS/S3）
export OSS_ACCESS_KEY_ID=xxx
export OSS_ACCESS_KEY_SECRET=xxx
export OSS_BUCKET=llm-gateway

# 完整变量清单以 config/config.go 为准
```

### 配置文件示例
```yaml
# config.yaml
server:
  listen: ":8781"
  mode: production
  shutdown_timeout: 30s

database:
  host: localhost
  port: 5432
  name: llm_gateway
  user: postgres
  password: xxx
  sslmode: disable
  max_conns: 100
  min_conns: 10

redis:
  host: localhost
  port: 6379
  password: xxx
  db: 0
  pool_size: 50

admin:
  enabled: true
  listen: ":8782"
  cors_origins: ["http://localhost:3000"]

logging:
  level: info
  format: json
  output: stdout

telemetry:
  enabled: true
  otel_endpoint: http://localhost:4318
  prometheus_port: 9090

license:
  file: /etc/llm-gateway/license.dat
  online_mode: true
  heartbeat_interval: 60s
```

---

## 数据库速查

### 核心表
```sql
-- 租户
SELECT * FROM tenants WHERE id = 'xxx';

-- 凭据
SELECT * FROM credentials WHERE tenant_id = 'xxx' AND status = 'active';

-- 请求日志
SELECT * FROM requests 
WHERE tenant_id = 'xxx' 
  AND created_at >= NOW() - INTERVAL '1 day'
ORDER BY created_at DESC 
LIMIT 100;

-- 会话
SELECT * FROM sessions WHERE id = 'sess_xxx';

-- 路由尝试
SELECT * FROM routing_attempts 
WHERE request_id = 'req_xxx'
ORDER BY attempt_number;

-- 凭据健康
SELECT c.name, ch.status, ch.error_type, ch.last_check_at
FROM credentials c
JOIN credential_health ch ON c.id = ch.credential_id
WHERE c.tenant_id = 'xxx';

-- 用量统计
SELECT DATE(created_at) as date,
       COUNT(*) as requests,
       SUM(input_tokens) as input_tokens,
       SUM(output_tokens) as output_tokens,
       SUM(cost_usd) as cost_usd
FROM usage_records
WHERE tenant_id = 'xxx'
  AND created_at >= NOW() - INTERVAL '7 days'
GROUP BY DATE(created_at)
ORDER BY date DESC;
```

### 常用查询

#### 实时请求监控
```sql
SELECT 
  r.id,
  r.tenant_id,
  r.client_model,
  r.outbound_model,
  r.provider,
  r.status,
  r.latency_ms,
  r.created_at
FROM requests r
WHERE r.created_at >= NOW() - INTERVAL '5 minutes'
ORDER BY r.created_at DESC;
```

#### 路由成功率分析
```sql
SELECT 
  provider,
  outbound_model,
  COUNT(*) as total,
  SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) as success,
  ROUND(100.0 * SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) / COUNT(*), 2) as success_rate
FROM requests
WHERE created_at >= NOW() - INTERVAL '1 day'
GROUP BY provider, outbound_model
ORDER BY total DESC;
```

#### 成本Top租户
```sql
SELECT 
  t.id,
  t.name,
  COUNT(r.id) as requests,
  SUM(r.input_tokens + r.output_tokens) as total_tokens,
  SUM(r.cost_usd) as total_cost
FROM tenants t
JOIN requests r ON t.id = r.tenant_id
WHERE r.created_at >= NOW() - INTERVAL '7 days'
GROUP BY t.id, t.name
ORDER BY total_cost DESC
LIMIT 10;
```

---

## 常见问题

### Q1: 如何添加新凭据？
```bash
# 通过Admin API（凭据挂在供应商下：创建供应商后在其下添加凭据，密钥经 /api/credentials/ 管理）
curl -X POST http://localhost:8781/api/providers \
  -H "Authorization: Bearer xxx" \
  -H "Content-Type: application/json" \
  -d '{
    "code": "openai",
    "display_name": "OpenAI"
  }'

# 通过SQL
INSERT INTO credentials (tenant_id, provider, name, api_key, status)
VALUES ('xxx', 'openai', 'OpenAI Key 1', 'sk-xxx', 'active');
```

### Q2: 如何查看某个请求的详细日志？
```bash
# 通过Admin API
curl http://localhost:8781/api/admin/connection-registry/req_xxx \
  -H "Authorization: Bearer xxx"

# 通过SQL
SELECT * FROM requests WHERE id = 'req_xxx';
SELECT * FROM routing_attempts WHERE request_id = 'req_xxx';
```

### Q3: 如何重启Gateway？
```bash
# systemd
sudo systemctl restart llm-gateway

# Docker
docker restart llm-gateway

# K8s
kubectl rollout restart deployment/llm-gateway

# 手动（优雅关闭）
kill -SIGTERM <pid>  # 等待30s处理完请求
```

### Q4: 如何临时禁用某个凭据？
```sql
UPDATE credentials 
SET status = 'disabled' 
WHERE id = 'cred_xxx';
```

### Q5: 如何清理旧的审计日志？
```bash
# 使用内置Worker（自动）
# bg/audit_trimmer.go 会自动清理90天以上的数据

# 手动清理
DELETE FROM requests 
WHERE created_at < NOW() - INTERVAL '90 days';
```

---

## 故障排查

### 服务无法启动

**症状**: Gateway启动失败

**排查步骤**:
```bash
# 1. 检查端口占用
lsof -i :8781

# 2. 检查配置文件
./gateway --config config.yaml --validate

# 3. 检查数据库连接
psql -h localhost -U postgres -d llm_gateway -c "SELECT 1;"

# 4. 检查Redis连接
redis-cli -h localhost -p 6379 ping

# 5. 查看日志
tail -f /var/log/llm-gateway/gateway.log

# 6. 检查License
./installer/llm-gw-installer license status
```

### 请求失败率高

**症状**: 大量请求返回500错误

**排查步骤**:
```sql
-- 1. 查看错误类型分布
SELECT error_type, COUNT(*) 
FROM requests 
WHERE status = 'error' 
  AND created_at >= NOW() - INTERVAL '1 hour'
GROUP BY error_type
ORDER BY COUNT(*) DESC;

-- 2. 查看凭据健康状态
SELECT provider, status, COUNT(*)
FROM credentials
GROUP BY provider, status;

-- 3. 查看路由失败原因
SELECT failure_reason, COUNT(*)
FROM routing_attempts
WHERE success = false
  AND created_at >= NOW() - INTERVAL '1 hour'
GROUP BY failure_reason;
```

```bash
# 4. 手动探测凭据
./cmd/probe-cred/probe-cred --credential-id cred_xxx --model gpt-4

# 5. 查看Worker日志
tail -f /var/log/llm-gateway/workers.log | grep "active_probe"
```

### 延迟突然升高

**症状**: P95延迟从500ms升到5s

**排查步骤**:
```sql
-- 1. 查看慢请求
SELECT id, outbound_model, provider, latency_ms, created_at
FROM requests
WHERE latency_ms > 5000
  AND created_at >= NOW() - INTERVAL '1 hour'
ORDER BY latency_ms DESC
LIMIT 20;

-- 2. 查看数据库连接数
SELECT COUNT(*) FROM pg_stat_activity WHERE datname = 'llm_gateway';

-- 3. 查看慢查询
SELECT query, calls, mean_exec_time, max_exec_time
FROM pg_stat_statements
ORDER BY mean_exec_time DESC
LIMIT 10;
```

```bash
# 4. 查看Redis延迟
redis-cli --latency

# 5. 查看系统负载
top
iostat -x 1

# 6. 查看网络延迟
ping <upstream-api-host>
```

### 内存泄漏

**症状**: 内存持续增长，最终OOM

**排查步骤**:
```bash
# 1. 查看进程内存
ps aux | grep gateway

# 2. Go内存profile
curl http://localhost:8781/debug/pprof/heap > heap.prof
go tool pprof -http=:8080 heap.prof

# 3. 查看goroutine数量
curl http://localhost:8781/debug/pprof/goroutine?debug=1

# 4. 强制GC
curl -X POST http://localhost:8781/debug/gc

# 5. 重启服务（临时）
sudo systemctl restart llm-gateway
```

---

## 相关文档

- [项目总览](./PROJECT_OVERVIEW.md) - 完整的项目介绍
- [功能模块指南](./MODULES_GUIDE.md) - 详细的模块说明
- [架构设计](./03-design/01-architecture/architecture/ARCHITECTURE.md) - 架构详细设计
- [部署指南](./06-deployment/README.md) - 部署与运维
- [故障排查](./troubleshooting/) - 详细故障排查手册

---

**最后更新**: 2026-09-06  
**维护团队**: LLM Gateway Team
