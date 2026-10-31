# LLM Gateway Go — 快速参考手册

> **版本**: 2.5.8-5e14ede2-20261020-2794
  
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
当前版本:     见仓库根 VERSION 文件（构建时生成，如 2.5.4-8125a06a-20260917-2121）
Go版本:       1.27.2
主入口:       cmd/gateway/main.go
默认端口:     8781 (Gateway 与 Admin 同端口，/api/admin/*)
```

### 仓库地址
```bash
# 主开发仓库（日常使用）
git remote add origin https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git

# 公开镜像仓库
git remote add github git@github.com:halfking/ai-native-gateway-core.git
```

### 代码统计（基于真实扫描，2026-10-31 @495a1c758）
```
Go源文件:       11,225 个    (find . -name "*.go" -type f | wc -l；全树含 vendor+installer)
Admin API:       790 个    (find ./admin -name "*.go" -type f | wc -l)
后台Worker:      434 个    (find ./bg -name "*.go" -type f | wc -l)
领域模块:        69 个    (ls ./domains/ | wc -l)
数据库迁移:     1,180 个    (find ./sql/migrations -name "*.sql" | wc -l；2026-10-19 订正，877/878 迁移入库后)
单元测试:     3,703 个    (find . -name "*_test.go" | wc -l；全树含 vendor+installer)
可执行程序:      49 个    (go list ./cmd/... main 包口径；顶层 cmd/ 目录 34 个，go list 全量 51 含 2 个非 main 辅助包——R48-B4 订正)
```

---

## 目录结构速查

### 顶层目录
```
├── cmd/           # 可执行程序（34个）
│   ├── gateway/   # 主网关程序 ⭐
│   └── tools/     # 工具程序
├── domains/       # 领域模型（69个）⭐
├── admin/         # Admin API（790文件）⭐
├── bg/            # 后台Worker（434文件）⭐
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
└── ...               # 其他58领域
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
# Go格式化
gofmt -w .

# Go imports
goimports -w .

# 多租户门禁（Makefile 目标）：
# make test-rls   —— 真实 PostgreSQL RLS 门禁（需 TEST_DATABASE_URL，本地手动跑，未接 CI）
# make test       —— 全量单元测试（CI 的 verify.sh 跑的就是它）
# 口径见 README.md「Contributing」与 CONTRIBUTING.md
```

### 数据库操作
```bash
# ⚠ 本项目**不使用 golang-migrate**（go.mod 里没有该依赖），
#    迁移由 Go 原生 runner 统一执行：installer/internal/dbinit（Runner.StartupFiles）
#    —— 主迁移在 sql/migrations/startup/，注册进 Runner.StartupFiles 后随网关/安装器启动自动跑。

# 新建迁移（放 sql/migrations/startup/，并在 runner.go 的 StartupFiles 里登记）
#   约定见 AGENTS.md「新增迁移」一节
```

⚠ 仓库根的 `migrations/` 目录**不是**迁移入口（里面只有 2 个零散文件），
按 `migrate -path migrations` 操作会写到永远不会被执行的地方。

### 部署相关
```bash
# 子命令全集：doctor / install / uninstall / version / activate / heartbeat / upgrade

# 激活License（key 模式）
./installer/llm-gw-installer activate --mode key --license-key LIC-xxx

# 激活License（离线：先 offline-request 生成请求，再 offline-import 导入许可文件）
./installer/llm-gw-installer activate --mode offline-request --license-key LIC-xxx
./installer/llm-gw-installer activate --mode offline-import --license-path license.dat

# 检查更新
./installer/llm-gw-installer upgrade --action check

# 在线升级
./installer/llm-gw-installer upgrade --action apply

# 回滚
./installer/llm-gw-installer upgrade --action rollback
```

### 工具命令
```bash
# 检查凭据健康（go run 或 go build 后执行；flag 为单横线 Go 风格）
go run ./cmd/check-credentials -tenant xxx

# 探测凭据（-chat 触发最小 chat completion 探测）
go run ./cmd/probe-cred -cred 123 -chat gpt-4

# 配置导出（打印到 stdout；-print-secrets 需同时设 CFG_DUMP_ALLOW_SECRETS=1）
go run ./cmd/cfg_dump

# 会话取证（子命令式：download / replay / summarize / list / migrate）
go run ./cmd/sessionforensics list --tenant=default --limit=20

# 质量计算（无 flag，DSN 在 main.go 内配置）
go run ./cmd/calc_quality
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

#### 多模态与文本加工（本地模型优先）
```bash
# 嵌入（本地 qwen3-embedding:8b = 4096 维）
POST /v1/embeddings          {"model":"qwen3-embedding:8b","input":"..."}

# 语音转写（whisper.cpp 双档：whisper-large-v3-turbo / whisper-large-v3）
POST /v1/audio/transcriptions   multipart: file, model, language

# 语音合成（本地 macOS say 垫片）
POST /v1/audio/speech        {"model":"tts-macos-say","input":"...","voice":"alloy"}

# 内容/会话总结（短文本单次；长文本 map→reduce→render 纪要管线；
# asm 在线时委托其智能体端点，响应 engine 字段区分 asm | gateway）
POST /v1/summarize           {"model":"qwen3.8:27b-mlx","map_model":"qwen2.5:7b","text":"...","style":"minutes"}

# 标题总结（≤20 字动宾，实体择优）
POST /v1/summarize/title     {"model":"qwen2.5:7b","text":"..."}

# 任务分类（受控词表，可带 vocab 覆盖）
POST /v1/classify            {"model":"qwen2.5:7b","text":"..."}
```

## 配置参考

### 环境变量
```bash
# 数据库（唯一入口，DSN 整串）
export LLM_GATEWAY_DATABASE_URL=postgres://user:pass@localhost:5432/llm_gateway?sslmode=disable

# Redis
export LLM_GATEWAY_REDIS_ADDR=127.0.0.1:6379
export LLM_GATEWAY_REDIS_PASSWORD=xxx
export LLM_GATEWAY_REDIS_DB=0

# 网关
export LLM_GATEWAY_LISTEN=:8781
export LLM_GATEWAY_LOG_LEVEL=info
export LLM_GATEWAY_API_KEY=xxx
export LLM_GATEWAY_ADMIN_API_KEY=xxx
export LLM_GATEWAY_SECRET_KEY=xxx
export LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=xxx

# 完整变量清单以 config/config.go 为准
```

### 配置文件示例
```yaml
# config.yaml —— 扁平顶层键结构（完整样例见仓库根 config.example.yaml）
listen: ":8781"
log_level: "info"
static_dir: "web/dist"
bg_mode: "full"
runtime_role: "active"          # active 拥有后台 worker；traffic-only 用于蓝绿候选
deploy_env: "development"
default_language: "en"
database_url: "postgres://user:pass@localhost:5432/llm_gateway?sslmode=disable"
redis_addr: "127.0.0.1:6379"
# secret_key / credential_encryption_key / api_key / admin_api_key 等敏感项
# 建议不经文件，统一走 LLM_GATEWAY_* 环境变量注入（环境变量优先于本文件）
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
SELECT * FROM request_logs 
WHERE tenant_id = 'xxx' 
  AND ts >= NOW() - INTERVAL '1 day'
ORDER BY ts DESC 
LIMIT 100;

-- 会话
SELECT * FROM sessions WHERE id = 'sess_xxx';

-- 路由尝试
-- 注：独立 routing_attempts 表已不存在，请求级路由轨迹内联在 request_logs.routing_attempts（jsonb）
SELECT request_id, routing_attempts
FROM request_logs
WHERE request_id = 'req_xxx';

-- 凭据健康
-- 注：健康状态为 credentials 表自带列（原 credential_health 独立表已不存在）
SELECT id, label, status, health_status, health_error, health_checked_at
FROM credentials
WHERE tenant_id = 'xxx';

-- 用量统计
SELECT DATE(occurred_at) as date,
       COUNT(*) as requests,
       SUM(prompt_tokens) as input_tokens,
       SUM(completion_tokens) as output_tokens,
       SUM(cost_amount) as cost_usd
FROM usage_facts
WHERE tenant_id = 'xxx'
  AND occurred_at >= NOW() - INTERVAL '7 days'
GROUP BY DATE(occurred_at)
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
  r.provider_id,
  r.upstream_status_code,
  r.latency_ms,
  r.ts
FROM request_logs r
WHERE r.ts >= NOW() - INTERVAL '5 minutes'
ORDER BY r.ts DESC;
```

#### 路由成功率分析
```sql
SELECT 
  provider_id,
  outbound_model,
  COUNT(*) as total,
  SUM(CASE WHEN success THEN 1 ELSE 0 END) as success,
  ROUND(100.0 * SUM(CASE WHEN success THEN 1 ELSE 0 END) / COUNT(*), 2) as success_rate
FROM request_logs
WHERE ts >= NOW() - INTERVAL '1 day'
GROUP BY provider_id, outbound_model
ORDER BY total DESC;
```

#### 成本Top租户
```sql
SELECT 
  t.id,
  t.name,
  COUNT(r.id) as requests,
  SUM(r.prompt_tokens + r.completion_tokens) as total_tokens,
  SUM(r.cost_usd) as total_cost
FROM tenants t
JOIN request_logs r ON t.id = r.tenant_id
WHERE r.ts >= NOW() - INTERVAL '7 days'
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

# 通过SQL（请求轨迹落在 request_logs；路由尝试内联在 request_logs.routing_attempts（jsonb），
# 没有独立的 requests / routing_attempts 表）
SELECT * FROM request_logs WHERE request_id = 'req_xxx';
SELECT routing_attempts FROM request_logs WHERE request_id = 'req_xxx';
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

# 手动清理（与内置 Worker 同一批审计表）
DELETE FROM routing_overrides_audit WHERE ts < NOW() - INTERVAL '90 days';
DELETE FROM routing_audit_log      WHERE ts < NOW() - INTERVAL '90 days';
DELETE FROM armor_judgments        WHERE created_at < NOW() - INTERVAL '90 days';
```

---

## 故障排查

### 服务无法启动

**症状**: Gateway启动失败

**排查步骤**:
```bash
# 1. 检查端口占用
lsof -i :8781

# 2. 检查配置文件（无命令行 flag；配置经 LLM_GATEWAY_CONFIG_FILE 环境变量加载）
LLM_GATEWAY_CONFIG_FILE=config.yaml ./gateway
# 日志出现 "config: loaded YAML file" 即配置有效；出现 "config: failed to load YAML file" 则回退仅环境变量

# 3. 检查数据库连接
psql -h localhost -U postgres -d llm_gateway -c "SELECT 1;"

# 4. 检查Redis连接
redis-cli -h localhost -p 6379 ping

# 5. 查看日志
tail -f /var/log/llm-gateway/gateway.log

# 6. 检查版本/激活状态（⚠ 没有 `license` 子命令）
./installer/llm-gw-installer version
./installer/llm-gw-installer activate --mode key --license-key <KEY>
```

### 请求失败率高

**症状**: 大量请求返回500错误

**排查步骤**:
```sql
-- 1. 查看错误类型分布（request_logs 用 success 布尔列 + error_kind 维度）
SELECT error_kind, COUNT(*) 
FROM request_logs 
WHERE success = false 
  AND ts >= NOW() - INTERVAL '1 hour'
GROUP BY error_kind
ORDER BY COUNT(*) DESC;

-- 2. 查看凭据健康状态
SELECT provider, status, COUNT(*)
FROM credentials
GROUP BY provider, status;

-- 3. 查看路由失败原因（routing_attempts 是 request_logs 上的 jsonb 数组列，
--    每项含 result / error_kind / stage / provider_id 等字段）
SELECT a->>'error_kind' AS error_kind, COUNT(*)
FROM request_logs, LATERAL jsonb_array_elements(routing_attempts) AS a
WHERE a->>'result' <> 'success'
  AND ts >= NOW() - INTERVAL '1 hour'
GROUP BY 1
ORDER BY 2 DESC;
```

```bash
# 4. 手动探测凭据（⚠ 实际只有 -cred / -chat 两个 flag）
go run ./cmd/probe-cred -cred 123 -chat gpt-4

# 5. 查看Worker日志
tail -f /var/log/llm-gateway/workers.log | grep "active_probe"
```

### 延迟突然升高

**症状**: P95延迟从500ms升到5s

**排查步骤**:
```sql
-- 1. 查看慢请求
SELECT request_id, outbound_model, provider_id, latency_ms, ts
FROM request_logs
WHERE latency_ms > 5000
  AND ts >= NOW() - INTERVAL '1 hour'
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

# 2. Go内存profile（pprof 挂在独立端口，需设 LLM_GATEWAY_PPROF_LISTEN=127.0.0.1:<port>，主端口 :8781 无 /debug/*）
curl http://127.0.0.1:<pprof端口>/debug/pprof/heap > heap.prof
go tool pprof -http=:8080 heap.prof

# 3. 查看goroutine数量
curl http://127.0.0.1:<pprof端口>/debug/pprof/goroutine?debug=1

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
