# LLM Gateway 本地部署测试指南

**版本**: v1.0  
**日期**: 2026-09-06  
**适用场景**: 本地开发、功能验证、部署前测试  
**基于**: `scripts/deploy-local.sh` + 现有测试基础设施

---

## 执行摘要

本指南提供 LLM Gateway 本地部署的**完整验证流程**，覆盖：

- ✅ **一键部署** — 使用 `deploy-local.sh` 自动化部署本地实例
- ✅ **分层验证** — L1-L4 健康检查 + 功能链路 + 业务场景
- ✅ **Mock 供应商** — 可控的测试环境，无需真实 API 密钥
- ✅ **故障注入** — 验证错误处理、重试、降级能力
- ✅ **完整报告** — 自动生成验证报告，包含所有检查点

---

## 目录

1. [快速开始](#1-快速开始)
2. [前置条件](#2-前置条件)
3. [部署流程](#3-部署流程)
4. [验证层级](#4-验证层级)
5. [测试场景](#5-测试场景)
6. [故障排查](#6-故障排查)
7. [清理与重置](#7-清理与重置)
8. [高级用法](#8-高级用法)

---

## 1. 快速开始

### 1.1 最简部署（推荐新手）

```bash
# 克隆仓库并进入目录
cd llm-gateway-go

# 首次部署：自动创建数据库、Redis、部署网关
./scripts/deploy-local.sh deploy

# 等待部署完成（约 2-3 分钟）
# 验证部署是否成功
./scripts/deploy-local.sh verify

# 查看运行状态
./scripts/deploy-local.sh status
```

**预期输出**:
```
[deploy-local] ━━━ 部署完成 ━━━
active=2.5.3-6bf41e56-20260906-1965
active_port=8782
database=docker (shared)
redis=docker (shared)
健康检查: ✓ PASS
就绪检查: ✓ PASS
版本验证: ✓ PASS
凭据解密: ✓ PASS
```

### 1.2 查看日志

```bash
# 实时查看网关日志
./scripts/deploy-local.sh logs

# 或直接查看日志文件
tail -f ~/kaixuan/llm-gateway-go/logs/gateway-8782.log
```

### 1.3 停止服务

```bash
./scripts/deploy-local.sh stop
```

---

## 2. 前置条件

### 2.1 系统要求

| 组件 | 版本要求 | 用途 |
|------|---------|------|
| **macOS** | 10.15+ | 开发环境 (Linux 和 Windows 请参考部署脚本适配) |
| **Go** | 1.21+ | 编译网关二进制 |
| **Docker** | 20.10+ | 运行 PostgreSQL 和 Redis 容器 |
| **curl** | 7.0+ | HTTP 验证和健康检查 |
| **jq** | 1.6+ | JSON 解析（可选，用于高级验证） |

### 2.2 检查系统环境

```bash
# 检查 Go 版本
go version  # 应显示 go1.21 或更高

# 检查 Docker 状态
docker info  # 应显示 Docker daemon 运行中

# 检查端口占用（避免冲突）
lsof -i :8782  # 网关端口（应为空）
lsof -i :5432  # PostgreSQL 端口
lsof -i :6379  # Redis 端口
```

### 2.3 环境变量配置

#### 必需配置（首次部署前创建 `.env.local`）

```bash
# 复制示例配置（如果项目提供）
cp .env.local.example .env.local

# 或手动创建 .env.local
cat > .env.local <<'EOF'
# PostgreSQL 配置（本地开发，由 deploy-local.sh 自动创建）
LLM_GATEWAY_DATABASE_URL=postgres://llm_gateway:GENERATED_PASSWORD@127.0.0.1:5432/llm_gateway?sslmode=disable

# Redis 配置（本地开发，由 deploy-local.sh 自动创建）
LLM_GATEWAY_REDIS_ADDR=127.0.0.1:6379

# JWT 签名密钥（必需，用于 admin 会话）
LLM_GATEWAY_SECRET_KEY=$(openssl rand -hex 32)

# 凭据加密密钥（必需，用于存储 API 密钥）
LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=$(openssl rand -hex 32)

# Admin 初始密码（可选，首次部署时设置）
LLM_GATEWAY_SEED_ADMIN_PASSWORD=admin123456

# 日志级别（可选）
LLM_GATEWAY_LOG_LEVEL=info
EOF

chmod 600 .env.local
```

**重要**: 
- `LLM_GATEWAY_SECRET_KEY` 是 **P0 必需**，缺失会导致部署失败（无法签发 admin token）
- `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY` 是 **P0 必需**，缺失且数据库已有加密凭据时会导致解密失败
- `.env.local` 已在 `.gitignore` 中，**不要提交到版本控制**

---

## 3. 部署流程

### 3.1 完整部署流程（自动化）

`deploy-local.sh` 执行以下步骤：

```
1. 版本分配
   ├─ 通过 bump-version.sh 自动递增 build_seq
   └─ 生成唯一 release 版本号（如 2.5.3-abc1234-20260906-1966）

2. 资源检测与准备
   ├─ 检测现有 PostgreSQL 容器（llm-gateway-pg）
   ├─ 检测现有 Redis 容器（llm-gateway-redis）
   ├─ 如不存在，自动创建并启动（Docker Compose）
   └─ 数据目录自动迁移到 ~/kaixuan/postgres 和 ~/kaixuan/redis（共享）

3. 数据库初始化与迁移
   ├─ 检测空数据库，应用 schema 快照（sql/schema/*.sql）
   ├─ 应用 revision sequence（sql/revisions/*.sql）
   └─ 执行 Go 迁移（cmd/gateway migrate）

4. 构建与打包
   ├─ 编译 Go 后端（CGO_ENABLED=0）
   ├─ 构建前端（npm run build in web/）
   └─ 打包为 release bundle（~/kaixuan/llm-gateway-go/bin/<version>/）

5. 启动 Candidate 实例
   ├─ 在候选端口（8781）启动新版本
   ├─ 健康检查（/healthz, /readyz）
   ├─ 版本验证（/version）
   └─ 凭据解密冒烟测试（调用 admin API 验证 provider credentials）

6. 零停机切换（如果配置了 Nginx 上游）
   ├─ 更新 upstream 配置指向候选端口
   └─ 原 active 实例继续处理现有连接，新连接转到候选

7. 原地重启（如果没有 Nginx 代理）
   ├─ 停止 active 实例（8782）
   ├─ 在 active 端口启动新版本
   └─ 验证成功后停止候选实例

8. 符号链接更新
   ├─ ~/kaixuan/llm-gateway-go/bin/current -> <new-version>
   └─ 记录部署状态（deployment-state, active-port）

9. 清理旧版本
   └─ 保留最近 3 个 release，删除更旧的
```

### 3.2 部署模式选择

#### 模式 A: Docker 运行时（推荐生产模拟）

```bash
# 在 Docker 容器中运行网关（隔离环境）
export LLM_GATEWAY_RUNTIME_IMAGE=alpine:3.22
./scripts/deploy-local.sh deploy

# Docker 模式特点：
# - 网关在 alpine 容器中运行，与宿主隔离
# - PG/Redis 通过 host.docker.internal 访问
# - 日志、附件、备份挂载到宿主目录
# - 更接近生产环境（K8s/Docker Compose）
```

#### 模式 B: Native 进程（默认，调试友好）

```bash
# 在宿主机直接运行网关进程
./scripts/deploy-local.sh deploy

# Native 模式特点：
# - 网关作为系统进程运行（nohup）
# - 可直接 attach 调试器
# - 性能开销更小
# - 适合快速迭代开发
```

### 3.3 部署选项

```bash
# 干运行（查看部署计划，不实际执行）
./scripts/deploy-local.sh --dry-run

# 跳过前端构建（节省时间，复用已有 web/dist）
./scripts/deploy-local.sh deploy --no-frontend

# 自定义安装根目录
./scripts/deploy-local.sh deploy --root /opt/custom-gateway

# 调整健康检查超时（默认 60 秒）
./scripts/deploy-local.sh deploy --timeout 120

# 清理遗留的 ~/Downloads/llm-gateway-files（迁移后）
./scripts/deploy-local.sh deploy --cleanup-downloads
```

---

## 4. 验证层级

### 4.1 L1: 基础健康检查

**目标**: 验证网关进程启动并响应 HTTP 请求

```bash
# 自动验证（推荐）
./scripts/deploy-local.sh verify

# 手动验证
PORT=8782  # 从 status 命令获取 active_port

# 存活检查
curl -f http://127.0.0.1:${PORT}/healthz
# 预期: {"status":"ok","version":"...","ready":true}

# 就绪检查
curl -f http://127.0.0.1:${PORT}/readyz
# 预期: {"status":"ready","database":"ok","redis":"ok"}

# 版本检查
curl -s http://127.0.0.1:${PORT}/version | jq .
# 预期:
# {
#   "version": "2.5.3-6bf41e56-20260906-1965",
#   "git_sha": "6bf41e56",
#   "build_seq": 1965,
#   "build_date": "20260906"
# }
```

**通过标准**: 所有端点返回 2xx，JSON 格式正确

### 4.2 L2: 依赖连通性

**目标**: 验证网关能正常访问 PostgreSQL 和 Redis

```bash
# PostgreSQL 连通性
curl -s http://127.0.0.1:${PORT}/readyz | jq .database
# 预期: "ok"

# Redis 连通性
curl -s http://127.0.0.1:${PORT}/readyz | jq .redis
# 预期: "ok"

# 手动验证数据库（可选）
docker exec llm-gateway-pg psql -U llm_gateway -d llm_gateway -c "SELECT COUNT(*) FROM providers;"
# 预期: 返回数字（0 或更多）

# 手动验证 Redis（可选）
docker exec llm-gateway-redis redis-cli PING
# 预期: PONG
```

**通过标准**: 数据库和缓存均可访问

### 4.3 L3: 功能链路验证

**目标**: 验证核心业务功能可正常工作

#### L3.1 Admin API 认证

```bash
# 获取 admin token（需要先在 UI 登录或调用登录接口）
TOKEN=$(curl -s -X POST http://127.0.0.1:${PORT}/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123456"}' | jq -r .token)

echo "Admin Token: $TOKEN"

# 验证 token 有效性
curl -s http://127.0.0.1:${PORT}/api/admin/providers \
  -H "Authorization: Bearer $TOKEN" | jq .
# 预期: 返回 providers 列表（可能为空）
```

#### L3.2 Chat Completions 端点

```bash
# 创建测试 provider 和 credential（需要 admin token）
# 注意：本地测试可以使用 mock provider

curl -X POST http://127.0.0.1:${PORT}/api/admin/providers \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "test-openai",
    "type": "openai",
    "base_url": "http://localhost:18080/v1",
    "enabled": true
  }'

# 测试 chat completions（需要配置好 provider 和 credential）
curl -X POST http://127.0.0.1:${PORT}/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

#### L3.3 Streaming 验证

```bash
# SSE streaming 测试
curl -N -X POST http://127.0.0.1:${PORT}/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "Count to 5"}],
    "stream": true
  }'

# 预期：逐行输出 SSE 事件
# data: {"id":"...","choices":[{"delta":{"content":"1"}}]}
# data: {"id":"...","choices":[{"delta":{"content":" "}}]}
# ...
# data: [DONE]
```

**通过标准**: 
- Admin API 可正常认证和查询
- Chat completions 返回合法响应（200 或明确的 4xx/5xx 错误）
- Streaming 能正常推送 SSE 事件并以 `[DONE]` 结束

### 4.4 L4: 业务场景验证

**目标**: 验证关键业务逻辑和边界情况

#### L4.1 多模型路由

```bash
# 配置多个 credentials，验证负载均衡
# （需要预先通过 admin API 配置）

for i in {1..10}; do
  curl -s -X POST http://127.0.0.1:${PORT}/v1/chat/completions \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer YOUR_API_KEY" \
    -d '{
      "model": "gpt-3.5-turbo",
      "messages": [{"role": "user", "content": "Test '$i'"}]
    }' > /tmp/resp-$i.json
done

# 检查日志，验证请求分布到不同 credentials
grep "selected_credential" ~/kaixuan/llm-gateway-go/logs/gateway-8782.log | tail -10
```

#### L4.2 错误处理与重试

```bash
# 模拟 provider 故障（需要使用 mock provider）
# 验证网关能正确重试和切换节点

# 1. 配置 mock provider 返回 503
# 2. 发送请求
curl -v -X POST http://127.0.0.1:${PORT}/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "mock-model",
    "messages": [{"role": "user", "content": "Test"}]
  }'

# 3. 检查日志，验证重试逻辑
grep -E "retry|fallback|switched" ~/kaixuan/llm-gateway-go/logs/gateway-8782.log | tail -20
```

#### L4.3 会话持久化

```bash
# 发送带会话的请求
RESPONSE=$(curl -s -X POST http://127.0.0.1:${PORT}/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "Remember: my name is Alice"}]
  }')

# 提取 session_id（如果响应包含）
SESSION_ID=$(echo "$RESPONSE" | jq -r '.session_id // empty')

# 继续会话
curl -s -X POST http://127.0.0.1:${PORT}/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "X-Session-ID: $SESSION_ID" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "What is my name?"}]
  }' | jq .

# 验证数据库中的会话记录
docker exec llm-gateway-pg psql -U llm_gateway -d llm_gateway \
  -c "SELECT id, turn_count, created_at FROM sessions WHERE id = '$SESSION_ID';"
```

**通过标准**:
- 多个 credentials 的请求分布符合预期（权重、健康度）
- 错误时能正确重试，日志包含完整的 error_kind 和 fallback 记录
- 会话能正确持久化到数据库，后续请求能复用上下文

---

## 5. 测试场景

### 5.1 使用 Mock Provider（推荐本地测试）

#### 5.1.1 启动 Mock Provider

```bash
# 使用已弃用的 mock（简单场景）
cd tests/_deprecated_local_poc_2026-07-12/mock-llm
python3 server.py --port 18080
```

或使用更强大的 Mock（支持场景编排）:

```bash
# 使用 tests/local/mocks（如果存在）
cd tests
go run ./local/mocks/main.go --port 18080
```

#### 5.1.2 配置 Mock Provider

```bash
# 通过 Admin API 添加 mock provider
curl -X POST http://127.0.0.1:8782/api/admin/providers \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "mock-openai",
    "type": "openai",
    "base_url": "http://localhost:18080/v1",
    "enabled": true,
    "models": [
      {"name": "mock-gpt-3.5", "context_window": 4096, "cost_per_1k_input": 0},
      {"name": "mock-gpt-4", "context_window": 8192, "cost_per_1k_input": 0}
    ]
  }'

# 添加 credential
curl -X POST http://127.0.0.1:8782/api/admin/providers/1/credentials \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "api_key": "mock-key-12345",
    "enabled": true,
    "priority": 1
  }'
```

#### 5.1.3 测试 Mock Provider

```bash
# 正常请求
curl -X POST http://127.0.0.1:8782/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer mock-key-12345" \
  -d '{
    "model": "mock-gpt-3.5",
    "messages": [{"role": "user", "content": "Hello"}]
  }' | jq .
```

### 5.2 场景测试矩阵

根据 `docs/05-testing/02-test-plans/testing/comprehensive-test-plan.md`，以下场景是必测的：

#### 场景 1: Provider 逐渐变慢

**目的**: 验证延迟检测和权重调整

```bash
# 1. 配置两个 credentials：cred-A（正常），cred-B（正常）
# 2. 配置 Mock Provider：cred-B 延迟从 500ms 提升到 5s
# 3. 发送 100 个请求
# 4. 验证：cred-B 权重降低，流量倾斜到 cred-A

# 检查 URSM 状态
docker exec llm-gateway-redis redis-cli KEYS "ursm:v2:node:t:*" | head -10
docker exec llm-gateway-redis redis-cli HGETALL "ursm:v2:node:t:default:1:mock-gpt-3.5"
```

#### 场景 2: Provider 突发 5xx

**目的**: 验证快速失败切换

```bash
# 1. 配置 cred-A 开始返回 500
# 2. 验证：3 次失败后标记为 Unhealthy
# 3. 验证：后续请求全部路由到 cred-B
# 4. 恢复 cred-A
# 5. 验证：检测成功后恢复流量

# 监控日志
tail -f ~/kaixuan/llm-gateway-go/logs/gateway-8782.log | grep -E "unhealthy|degraded|switched"
```

#### 场景 3: Quota 耗尽

**目的**: 验证 429 处理和定时恢复

```bash
# 1. 配置 Mock 返回 429，Retry-After: 60
# 2. 验证：cred 标记为 Unhealthy，记录 QuotaRecoverAt
# 3. 等待 65 秒
# 4. 验证：自动触发检测，恢复 Active

# 检查 URSM 恢复时间
docker exec llm-gateway-redis redis-cli HGET "ursm:v2:node:t:default:1:mock-gpt-3.5" "availability_recover_at"
```

#### 场景 4: 所有 Provider 故障

**目的**: 验证优雅降级

```bash
# 1. 所有 credentials 返回 503
# 2. 验证：客户端收到 503，错误消息明确
# 3. 恢复一个 provider
# 4. 验证：流量立即切换

curl -X POST http://127.0.0.1:8782/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer mock-key-12345" \
  -d '{"model": "mock-gpt-3.5", "messages": [{"role": "user", "content": "Test"}]}' \
  | jq .error
# 预期: {"error": {"message": "No healthy providers available", "type": "service_unavailable"}}
```

### 5.3 压力测试（可选）

```bash
# 使用 Apache Bench 或 wrk 进行压力测试
ab -n 1000 -c 10 -T 'application/json' \
  -p /tmp/request.json \
  -H "Authorization: Bearer mock-key-12345" \
  http://127.0.0.1:8782/v1/chat/completions

# 或使用项目自带的压测脚本
./scripts/comprehensive-data-test-current.sh --target http://127.0.0.1:8782

# 监控资源使用
docker stats llm-gateway-local-8782  # Docker 模式
# 或
ps aux | grep gateway  # Native 模式
```

---

## 6. 故障排查

### 6.1 部署失败

#### 问题: 版本号冲突

```
[deploy-local] error: release already exists: ~/kaixuan/llm-gateway-go/bin/2.5.3-xxx
```

**解决方案**:
```bash
# 方案 A: 删除冲突的 release
rm -rf ~/kaixuan/llm-gateway-go/bin/2.5.3-xxx

# 方案 B: 手动递增版本号
./scripts/bump-version.sh

# 重新部署
./scripts/deploy-local.sh deploy
```

#### 问题: 缺少 SECRET_KEY

```
[deploy-local] error: LLM_GATEWAY_SECRET_KEY is empty
```

**解决方案**:
```bash
# 生成并添加到 .env.local
echo "LLM_GATEWAY_SECRET_KEY=$(openssl rand -hex 32)" >> .env.local
./scripts/deploy-local.sh deploy
```

#### 问题: 数据库迁移失败

```
[deploy-local] error: database migration failed
```

**解决方案**:
```bash
# 查看迁移日志
cat ~/kaixuan/llm-gateway-go/run/gateway-migrate.log

# 如果是脏数据，重置数据库
docker exec llm-gateway-pg psql -U llm_gateway -d llm_gateway -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"

# 重新部署
./scripts/deploy-local.sh deploy
```

### 6.2 运行时错误

#### 问题: 健康检查超时

```
[deploy-local] error: candidate failed health/readiness/version gates
```

**解决方案**:
```bash
# 1. 检查网关日志
tail -100 ~/kaixuan/llm-gateway-go/logs/gateway-8781.log

# 2. 检查端口是否被占用
lsof -i :8781

# 3. 检查 Docker 容器状态
docker ps -a | grep llm-gateway

# 4. 手动测试健康端点
curl -v http://127.0.0.1:8781/healthz
```

#### 问题: 凭据解密失败

```
[deploy-local] error: candidate failed credential decrypt smoke
```

**解决方案**:
```bash
# 检查加密密钥是否正确
grep LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY .env.local

# 如果更换了密钥，需要重新加密所有凭据
# （这是破坏性操作，仅用于本地开发）
docker exec llm-gateway-pg psql -U llm_gateway -d llm_gateway \
  -c "UPDATE credentials SET secret_ciphertext = NULL;"

# 重新部署
./scripts/deploy-local.sh deploy
```

#### 问题: Redis 连接失败

```
readyz: {"redis":"connection_refused"}
```

**解决方案**:
```bash
# 检查 Redis 容器
docker ps | grep redis

# 启动 Redis（如果未运行）
docker start llm-gateway-redis

# 或重新创建
docker-compose -f docker-compose.dev-research.yml up -d redis

# 验证连接
docker exec llm-gateway-redis redis-cli PING
```

### 6.3 常见错误代码

| 错误 | 可能原因 | 解决方案 |
|------|---------|---------|
| `connection refused (PG)` | PostgreSQL 未启动 | `docker start llm-gateway-pg` |
| `authentication failed` | 数据库密码不匹配 | 检查 `.env.local` 中的 `DATABASE_URL` |
| `token generation failed` | SECRET_KEY 缺失 | 设置 `LLM_GATEWAY_SECRET_KEY` |
| `decrypt failed` | ENCRYPTION_KEY 不匹配 | 重置凭据或恢复正确的密钥 |
| `bind: address already in use` | 端口冲突 | 停止占用端口的进程或使用 `--root` 改变端口 |
| `no healthy providers` | 所有 credentials 不可用 | 检查 provider 配置和上游连通性 |

---

## 7. 清理与重置

### 7.1 停止服务（保留数据）

```bash
# 停止网关进程
./scripts/deploy-local.sh stop

# 停止 Docker 服务（可选）
docker stop llm-gateway-pg llm-gateway-redis
```

### 7.2 完全重置（删除所有数据）

```bash
# 警告：这将删除所有数据，包括配置、会话、日志

# 1. 停止所有服务
./scripts/deploy-local.sh stop
docker stop llm-gateway-pg llm-gateway-redis

# 2. 删除容器和卷
docker rm -f llm-gateway-pg llm-gateway-redis
docker volume rm $(docker volume ls -q | grep llm-gateway)

# 3. 删除本地数据
rm -rf ~/kaixuan/llm-gateway-go
rm -rf ~/kaixuan/postgres
rm -rf ~/kaixuan/redis

# 4. 清理临时文件
rm -f /tmp/llm-gateway-*.log
rm -f /tmp/kx-llm-gateway-build.lock

# 5. 重新部署（从头开始）
./scripts/deploy-local.sh deploy
```

### 7.3 回滚到上一个版本

```bash
# 查看可用版本
ls -lt ~/kaixuan/llm-gateway-go/bin/ | head -10

# 回滚到上一个 verified 版本
./scripts/deploy-local.sh rollback

# 或回滚到指定版本
./scripts/deploy-local.sh rollback 2.5.3-abc1234-20260905-1964
```

---

## 8. 高级用法

### 8.1 自定义部署根目录

```bash
# 部署到自定义目录（适合多实例测试）
export LLM_GATEWAY_ROOT=/opt/gateway-instance-1
./scripts/deploy-local.sh deploy --root /opt/gateway-instance-1

# 实例 2
export LLM_GATEWAY_ROOT=/opt/gateway-instance-2
export LLM_GATEWAY_ACTIVE_PORT=8783
./scripts/deploy-local.sh deploy --root /opt/gateway-instance-2
```

### 8.2 连接外部数据库

```bash
# 跳过本地数据库创建，使用远程 PG
export LLM_GATEWAY_DATABASE_URL="postgres://user:pass@remote-host:5432/dbname?sslmode=require"
./scripts/deploy-local.sh deploy

# 注意：外部数据库必须已经初始化 schema
```

### 8.3 启用调试模式

```bash
# 设置详细日志
export LLM_GATEWAY_LOG_LEVEL=debug
./scripts/deploy-local.sh deploy

# 或在 .env.local 中设置
echo "LLM_GATEWAY_LOG_LEVEL=debug" >> .env.local

# 查看详细日志
tail -f ~/kaixuan/llm-gateway-go/logs/gateway-8782.log | grep -E "DEBUG|TRACE"
```

### 8.4 性能分析

```bash
# 启用 pprof（默认端口 6060）
export LLM_GATEWAY_PPROF_ENABLED=true
./scripts/deploy-local.sh deploy

# 访问 pprof 端点
go tool pprof http://127.0.0.1:6060/debug/pprof/heap
go tool pprof http://127.0.0.1:6060/debug/pprof/profile?seconds=30

# 或使用浏览器查看
open http://127.0.0.1:6060/debug/pprof/
```

### 8.5 集成测试自动化

```bash
# 完整的部署 + 验证流程（CI/CD 友好）
#!/bin/bash
set -euo pipefail

# 1. 部署
./scripts/deploy-local.sh deploy --no-frontend --timeout 120

# 2. 等待就绪
for i in {1..30}; do
  if curl -sf http://127.0.0.1:8782/readyz >/dev/null; then
    echo "Gateway ready"
    break
  fi
  sleep 2
done

# 3. 运行集成测试
go test ./tests/integration/... -v -count=1

# 4. 运行 E2E 测试
./scripts/comprehensive-data-test-current.sh --target http://127.0.0.1:8782

# 5. 验证并生成报告
./scripts/deploy-local.sh verify
echo "All tests passed"
```

### 8.6 多环境配置管理

```bash
# 为不同环境创建配置文件
# .env.local.dev
cat > .env.local.dev <<'EOF'
LLM_GATEWAY_LOG_LEVEL=debug
LLM_GATEWAY_ENABLE_PROFILING=true
LLM_GATEWAY_SECRET_KEY=dev-secret-key
EOF

# .env.local.staging
cat > .env.local.staging <<'EOF'
LLM_GATEWAY_LOG_LEVEL=info
LLM_GATEWAY_SECRET_KEY=staging-secret-key
EOF

# 部署时选择配置
ln -sf .env.local.dev .env.local
./scripts/deploy-local.sh deploy
```

---

## 附录 A: 快速参考

### 常用命令

```bash
# 部署
./scripts/deploy-local.sh deploy

# 验证
./scripts/deploy-local.sh verify

# 状态
./scripts/deploy-local.sh status

# 日志
./scripts/deploy-local.sh logs

# 停止
./scripts/deploy-local.sh stop

# 回滚
./scripts/deploy-local.sh rollback

# 重启
./scripts/deploy-local.sh start
```

### 重要路径

```bash
# 部署根目录
~/kaixuan/llm-gateway-go/

# Releases
~/kaixuan/llm-gateway-go/bin/<version>/

# 当前版本符号链接
~/kaixuan/llm-gateway-go/bin/current -> <version>

# 日志
~/kaixuan/llm-gateway-go/logs/gateway-8782.log

# 运行时状态
~/kaixuan/llm-gateway-go/run/

# 附件和备份
~/kaixuan/llm-gateway-go/attachments/
~/kaixuan/llm-gateway-go/backups/

# 共享服务
~/kaixuan/postgres/  # PostgreSQL data
~/kaixuan/redis/     # Redis data
```

### 环境变量

| 变量 | 用途 | 默认值 |
|------|------|--------|
| `LLM_GATEWAY_ROOT` | 安装根目录 | `~/kaixuan/llm-gateway-go` |
| `LLM_GATEWAY_ACTIVE_PORT` | Active 端口 | `8782` |
| `LLM_GATEWAY_DATABASE_URL` | PostgreSQL DSN | 自动生成 |
| `LLM_GATEWAY_REDIS_ADDR` | Redis 地址 | `127.0.0.1:6379` |
| `LLM_GATEWAY_SECRET_KEY` | JWT 签名密钥 | **必需** |
| `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY` | 凭据加密密钥 | **必需** |
| `LLM_GATEWAY_LOG_LEVEL` | 日志级别 | `info` |

---

## 附录 B: 测试检查清单

### 部署前检查

- [ ] Go 1.21+ 已安装
- [ ] Docker 已启动
- [ ] 端口 5432, 6379, 8782 未被占用
- [ ] `.env.local` 已配置，包含 SECRET_KEY 和 ENCRYPTION_KEY
- [ ] 磁盘空间充足（至少 5GB）

### 部署后验证

- [ ] `deploy-local.sh deploy` 执行成功
- [ ] `deploy-local.sh status` 显示 active 版本
- [ ] `curl http://127.0.0.1:8782/healthz` 返回 200
- [ ] `curl http://127.0.0.1:8782/readyz` 显示 database 和 redis 为 ok
- [ ] 日志无 ERROR 或 PANIC
- [ ] PostgreSQL 容器运行中
- [ ] Redis 容器运行中

### 功能验证

- [ ] Admin API 登录成功
- [ ] 可以创建 provider 和 credential
- [ ] Chat completions 返回正常响应
- [ ] Streaming 能正常推送 SSE 事件
- [ ] 错误时有明确的 error_kind 日志
- [ ] 会话持久化到数据库

### 性能验证

- [ ] 内存使用稳定（无明显泄漏）
- [ ] CPU 使用合理（< 50% 空闲时）
- [ ] 响应延迟 P95 < 5s（本地 mock）
- [ ] 并发 10 QPS 无错误

---

## 附录 C: 相关文档

- [部署脚本源码](../scripts/deploy-local.sh)
- [全面测试方案](./05-testing/02-test-plans/testing/comprehensive-test-plan.md)
- [测试矩阵](./05-testing/01-strategy/test-matrix.md)
- [会话优化 v4 测试方案](./05-testing/02-test-plans/会话优化v4/客户端会话保持-测试方案与用例.md)
- [三环境统一验证](./05-testing/02-test-plans/testing/three-env-unified-verification.md)

---

**文档版本**: v1.0  
**最后更新**: 2026-09-06 14:30 UTC+8  
**维护者**: LLM Gateway Team  
**反馈**: 发现问题请在项目仓库提交 Issue
