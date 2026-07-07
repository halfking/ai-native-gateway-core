# 本地压力测试完整指南

本文档描述如何在本地部署 LLM Gateway 并进行全面的压力测试和场景验证。

## 目录

1. [测试架构](#测试架构)
2. [快速开始](#快速开始)
3. [完整测试流程](#完整测试流程)
4. [手动操作指南](#手动操作指南)
5. [测试场景说明](#测试场景说明)
6. [故障排查](#故障排查)

---

## 测试架构

```
┌─────────────────┐
│  压力测试客户端   │  (20+ 并发客户端)
│  loadtest-      │
│  stress.py      │
└────────┬────────┘
         │
         ↓
┌─────────────────┐
│   LLM Gateway   │  (localhost:8080)
│   路由/故障转移  │
└────────┬────────┘
         │
         ↓ (路由到多个 mock 供应商)
         │
    ┌────┴────┬────────┬────────┐
    ↓         ↓        ↓        ↓
┌────────┐┌────────┐┌────────┐┌────────┐
│ Mock-A ││ Mock-B ││ Mock-C ││ Mock-D │  (10+ mock 供应商)
│ :19080 ││ :19081 ││ :19082 ││ :19083 │
└────────┘└────────┘└────────┘└────────┘
```

### 核心组件

1. **Mock 供应商服务** (`scripts/mocks/llm-mock-upstream/server-v2.py`)
   - 支持 10 种状态模式：healthy, slow, rate_limited, quota_exceeded, auth_error, server_error, timeout, connection_refused, broken_stream, flaky
   - 动态状态切换 (通过 REST API)
   - 请求计数器和历史记录
   - 状态持久化

2. **压力测试客户端** (`scripts/loadtest-stress.py`)
   - 支持 20+ 并发客户端
   - 多轮会话模拟
   - Sticky routing 验证
   - 性能指标收集 (P50, P95, P99 延迟)
   - 动态故障注入

3. **状态编排工具** (`scripts/mock-state-orchestrator.sh`)
   - 批量控制 mock 状态
   - 健康检查
   - 指标查看

---

## 快速开始

### 前置条件

1. **依赖安装**
   ```bash
   # Python 依赖
   pip3 install aiohttp
   
   # 系统工具
   brew install jq postgresql  # macOS
   # 或
   apt-get install jq postgresql-client  # Linux
   ```

2. **数据库准备**
   - PostgreSQL 运行中 (本地或 Docker)
   - 已执行 `sql/01-schema.sql` 和 `sql/02-seed.sql`

### 方式 1：快速启动（推荐用于日常测试）

```bash
# 启动 4 个 mock + gateway，运行简单压力测试
cd /Users/xutaohuang/workspace/llm-gateway-go-3
./scripts/quick-start-loadtest.sh
```

这个脚本会：
- 启动 4 个 mock 供应商 (19080-19083)
- 提示你执行 SQL 注入 credentials
- 启动 gateway
- 运行 10 客户端 × 20 轮的快速测试

### 方式 2：完整压力测试（推荐用于发布前验证）

```bash
# 运行完整的压力测试套件
cd /Users/xutaohuang/workspace/llm-gateway-go-3
./scripts/comprehensive-loadtest.sh all
```

这个脚本会：
- 启动 12 个 mock 供应商
- 自动注入 credentials 到数据库
- 启动 gateway
- 运行基准测试 (25 客户端 × 100 轮)
- 运行故障注入测试 (slow, rate_limited, mixed faults)
- 运行动态故障注入测试
- 生成详细测试报告

---

## 完整测试流程

### Step 1: 检查依赖

```bash
./scripts/comprehensive-loadtest.sh check
```

### Step 2: 编译 Gateway

```bash
./scripts/comprehensive-loadtest.sh build
```

### Step 3: 启动 Mock 供应商

```bash
# 启动 12 个 mock 实例 (端口 19080-19091)
./scripts/comprehensive-loadtest.sh start-mocks

# 验证 mock 状态
./scripts/mock-state-orchestrator.sh health-all
```

### Step 4: 注入 Credentials

```bash
# 自动生成并执行 SQL
./scripts/comprehensive-loadtest.sh inject-creds

# 或者手动执行现有的 SQL
psql -f sql/scripts/04-loadtest-mock-credentials.sql
```

### Step 5: 启动 Gateway

```bash
./scripts/comprehensive-loadtest.sh start-gateway

# 验证 gateway
curl http://localhost:8080/healthz
```

### Step 6: 运行测试

```bash
# 基准测试 (所有 mock 健康)
./scripts/comprehensive-loadtest.sh test-baseline

# 故障注入测试 (静态故障)
./scripts/comprehensive-loadtest.sh test-faults

# 动态故障注入测试 (运行中随机注入故障)
./scripts/comprehensive-loadtest.sh test-dynamic
```

### Step 7: 生成报告

```bash
./scripts/comprehensive-loadtest.sh report
```

报告会保存在 `/tmp/loadtest-report-*.md`

### Step 8: 清理

```bash
./scripts/comprehensive-loadtest.sh cleanup
```

---

## 手动操作指南

### 1. 启动单个 Mock 供应商

```bash
cd scripts/mocks/llm-mock-upstream

MOCK_PORT=19080 \
MOCK_TOKEN=mock-A \
MOCK_STATE_FILE=/tmp/mock-state-19080.json \
python3 server-v2.py
```

### 2. 控制 Mock 状态

```bash
# 设置单个 mock 为 slow 模式 (TTL 30秒)
./scripts/mock-state-orchestrator.sh set http://localhost:19080 slow 30

# 设置所有 mock 为健康状态
./scripts/mock-state-orchestrator.sh set-all healthy

# 查看单个 mock 状态
./scripts/mock-state-orchestrator.sh get http://localhost:19080

# 查看单个 mock 指标
./scripts/mock-state-orchestrator.sh metrics http://localhost:19080

# 重置单个 mock
./scripts/mock-state-orchestrator.sh reset http://localhost:19080

# 重置所有 mock
./scripts/mock-state-orchestrator.sh reset-all

# 查看所有 mock 健康状态
./scripts/mock-state-orchestrator.sh health-all
```

### 3. 手动运行压力测试

```bash
# 基础测试 (直连 mock，不经过 gateway)
python3 scripts/loadtest-stress.py \
    --clients 20 \
    --rounds 50 \
    --mode direct \
    --mocks "http://localhost:19080,http://localhost:19081,http://localhost:19082,http://localhost:19083"

# 通过 gateway 测试
python3 scripts/loadtest-stress.py \
    --clients 20 \
    --rounds 50 \
    --mode gateway \
    --gateway "http://localhost:8080" \
    --api-key "your-api-key" \
    --model "gpt-4o"

# 带故障注入的测试
python3 scripts/loadtest-stress.py \
    --clients 20 \
    --rounds 50 \
    --mode gateway \
    --gateway "http://localhost:8080" \
    --model "gpt-4o" \
    --fault-injection
```

### 4. 使用 Docker Compose 启动 Mock

```bash
cd scripts/mocks
docker-compose up -d

# 查看日志
docker-compose logs -f

# 停止
docker-compose down
```

---

## 测试场景说明

### 场景 1: 基准性能测试

**目标**: 测试 gateway 在理想状态下的性能上限

**配置**:
- 所有 mock: healthy 模式
- 客户端: 25 个并发
- 请求轮次: 100 轮/客户端
- 总请求数: 2,500

**预期结果**:
- 成功率: > 99%
- Sticky 准确率: > 95%
- P95 延迟: < 1000ms
- P99 延迟: < 1500ms

### 场景 2: Slow 故障测试

**目标**: 验证 gateway 对慢供应商的超时和故障转移

**配置**:
- 30% mock: slow 模式 (5-10s 延迟)
- 70% mock: healthy 模式

**预期结果**:
- 成功率: > 95% (gateway 应路由到健康的 mock)
- Sticky 准确率: 降低 (部分会话需要故障转移)
- P95 延迟: 增加，但不应阻塞全部请求

### 场景 3: Rate Limit 故障测试

**目标**: 验证 gateway 对 429 错误的处理

**配置**:
- 20% mock: rate_limited 模式
- 80% mock: healthy 模式

**预期结果**:
- 成功率: > 90%
- Gateway 应自动重试到其他 mock
- 请求分布应偏向健康的 mock

### 场景 4: 混合故障测试

**目标**: 模拟生产环境中的复杂故障

**配置**:
- 30% mock: flaky 模式 (30% 概率失败)
- 20% mock: server_error 模式 (50% 概率 500)
- 50% mock: healthy 模式

**预期结果**:
- 成功率: > 85%
- Gateway 应智能路由到最健康的 mock
- 验证熔断器和健康检查机制

### 场景 5: 动态故障注入测试

**目标**: 测试 gateway 对突发故障的应对能力

**配置**:
- 初始: 所有 mock healthy
- 运行中: 每 10 轮随机注入故障 (TTL 5秒)

**预期结果**:
- 成功率: > 80%
- Gateway 应动态调整路由策略
- Sticky 会话应在故障后自动转移

---

## 测试指标说明

### 1. 成功率 (Success Rate)

```
成功率 = (成功请求数 / 总请求数) × 100%
```

- **> 99%**: 优秀 (理想状态)
- **95-99%**: 良好 (轻度故障)
- **85-95%**: 可接受 (中度故障)
- **< 85%**: 需要调查 (重度故障)

### 2. Sticky 准确率 (Sticky Accuracy)

```
Sticky 准确率 = (命中相同 mock 的次数 / 总轮次) × 100%
```

- **> 95%**: 优秀 (路由一致性高)
- **70-95%**: 良好 (有故障转移)
- **< 70%**: 需要调查 (路由策略问题)

### 3. 延迟指标

- **P50 (中位数)**: 50% 请求的延迟
- **P95**: 95% 请求的延迟
- **P99**: 99% 请求的延迟

基准值 (healthy 状态):
- P50: < 500ms
- P95: < 1000ms
- P99: < 1500ms

### 4. Mock 分布 (Mock Distribution)

验证请求是否均匀分布到所有健康的 mock。

理想情况 (4 个 mock 均健康):
```json
{
  "mock-00": 625,  // 25%
  "mock-01": 625,  // 25%
  "mock-02": 625,  // 25%
  "mock-03": 625   // 25%
}
```

---

## 故障排查

### 问题 1: Mock 启动失败

**症状**: `curl http://localhost:19080/healthz` 超时

**排查**:
```bash
# 检查端口是否被占用
lsof -i :19080

# 查看 mock 日志
tail -f /tmp/mock-19080.log

# 检查 Python 依赖
python3 -c "import aiohttp; print('OK')"
```

**解决**:
```bash
# 杀死占用端口的进程
lsof -ti :19080 | xargs kill -9

# 重新安装依赖
pip3 install aiohttp

# 重新启动
MOCK_PORT=19080 MOCK_TOKEN=mock-A python3 server-v2.py
```

### 问题 2: Gateway 连接数据库失败

**症状**: Gateway 启动时报 "connection refused"

**排查**:
```bash
# 检查 PostgreSQL 是否运行
psql -h localhost -p 5432 -U postgres -c "SELECT 1"

# 检查 .env 配置
cat .env | grep DB_
```

**解决**:
```bash
# 启动 PostgreSQL (Docker)
docker run -d --name postgres \
  -e POSTGRES_PASSWORD=password \
  -p 5432:5432 postgres:15

# 或启动本地 PostgreSQL
brew services start postgresql
```

### 问题 3: Credentials 注入失败

**症状**: SQL 执行报错 "violates foreign key constraint"

**排查**:
```bash
# 检查 schema 是否已应用
psql -c "\dt" | grep providers
```

**解决**:
```bash
# 按顺序执行 SQL
psql -f sql/01-schema.sql
psql -f sql/02-seed.sql
psql -f sql/scripts/04-loadtest-mock-credentials.sql
```

### 问题 4: 压力测试成功率过低

**症状**: 成功率 < 50%

**排查**:
```bash
# 检查 gateway 日志
tail -f /tmp/gateway-loadtest.log

# 检查 mock 状态
./scripts/mock-state-orchestrator.sh health-all

# 检查 mock 指标
for port in 19080 19081 19082 19083; do
  curl http://localhost:$port/admin/metrics | jq
done
```

**解决**:
```bash
# 重置所有 mock 到健康状态
./scripts/mock-state-orchestrator.sh reset-all

# 降低并发和轮次
python3 scripts/loadtest-stress.py --clients 5 --rounds 10 --mode gateway --gateway http://localhost:8080 --model gpt-4o
```

### 问题 5: Sticky Routing 准确率过低

**症状**: Sticky 准确率 < 50%

**可能原因**:
1. Gateway 未启用 session sticky routing
2. Mock 状态不稳定 (频繁故障转移)
3. 负载均衡算法配置错误

**排查**:
```bash
# 检查 gateway 配置
cat .env | grep STICKY

# 检查请求头是否包含 session ID
curl -H "X-Gw-Session-Id: test-123" http://localhost:8080/v1/chat/completions
```

---

## 高级用法

### 1. 自定义测试场景

创建 `scripts/loadtest-scenarios/custom.sh`:

```bash
#!/usr/bin/env bash
# 自定义测试场景

# 设置特定的 mock 状态
curl -X POST http://localhost:19080/admin/state -d '{"mode":"slow","ttl_seconds":60}'
curl -X POST http://localhost:19081/admin/state -d '{"mode":"rate_limited","ttl_seconds":60}'

# 运行测试
python3 scripts/loadtest-stress.py \
    --clients 30 \
    --rounds 200 \
    --mode gateway \
    --gateway "http://localhost:8080" \
    --model "gpt-4o"

# 重置 mock
curl -X POST http://localhost:19080/admin/reset
curl -X POST http://localhost:19081/admin/reset
```

### 2. 多模型测试

```bash
# 测试 gpt-4o
python3 scripts/loadtest-stress.py --model "gpt-4o" --clients 10 --rounds 50 --mode gateway --gateway http://localhost:8080

# 测试 gpt-4o-mini
python3 scripts/loadtest-stress.py --model "gpt-4o-mini" --clients 10 --rounds 50 --mode gateway --gateway http://localhost:8080
```

### 3. 长时间稳定性测试

```bash
# 运行 10 轮，每轮多个场景
./scripts/loadtest-multiround.sh 10 "baseline,slow,mixed"
```

### 4. 监控和观察

```bash
# 实时监控 mock 计数器
watch -n 1 'for port in 19080 19081 19082 19083; do echo "=== Mock $port ==="; curl -s http://localhost:$port/admin/metrics | jq -c "{token,counters}"; done'

# 实时监控 gateway (如果有 metrics 端点)
watch -n 1 'curl -s http://localhost:8080/metrics'
```

---

## 配置参数

### comprehensive-loadtest.sh 配置

编辑脚本开头的配置变量:

```bash
NUM_MOCK_PROVIDERS=12        # mock 供应商数量 (建议 4-20)
NUM_CLIENTS=25               # 并发客户端数 (建议 10-50)
NUM_ROUNDS_PER_CLIENT=100    # 每客户端请求轮次 (建议 50-200)
GATEWAY_PORT=8080            # Gateway 端口
DB_HOST=localhost            # 数据库主机
DB_PORT=5432                 # 数据库端口
DB_NAME=llm_gateway          # 数据库名称
DB_USER=postgres             # 数据库用户
```

### loadtest-stress.py 参数

```bash
python3 scripts/loadtest-stress.py --help

选项:
  --clients N          并发客户端数 (默认: 20)
  --rounds N           每客户端请求轮次 (默认: 50)
  --mode MODE          测试模式: direct 或 gateway (默认: direct)
  --mocks URLS         Mock URLs (逗号分隔)
  --gateway URL        Gateway URL
  --api-key KEY        API Key
  --model MODEL        模型名称 (默认: loadtest-fake-gpt-4o)
  --fault-injection    启用动态故障注入
```

---

## 总结

这套测试方案提供了：

✅ **完整的本地部署** - 一键启动 mock + gateway  
✅ **灵活的压力测试** - 支持 20+ 客户端并发  
✅ **多种故障场景** - 覆盖 10 种故障模式  
✅ **动态故障注入** - 模拟生产环境随机故障  
✅ **详细的性能指标** - P50/P95/P99 延迟、成功率、Sticky 准确率  
✅ **自动化测试流程** - 从部署到报告一条龙  

现在你可以轻松地在本地进行全面的压力测试和场景验证！
