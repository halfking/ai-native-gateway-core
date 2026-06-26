# minimax-m3 系统改进与测试计划

**目标**: 修正系统问题，确保 request_logs 正常记录，并在生产环境 71 上完成测试验证

**日期**: 2026-06-26

---

## 📋 问题清单

### 1. ✅ minimax-m3 配置已完成
- provider_models: 已添加
- credential_model_bindings: 已添加
- credential_model_index: 已填充
- 数据库配置完整

### 2. ⚠️ 测试环境限制
- 无法解密 API key
- 无法完成端到端测试

### 3. 🔍 request_logs 问题
- **现状**: 本地测试环境 request_logs 正常插入
- **问题**: 需要确认生产环境 71 上是否也正常记录

### 4. 🏗️ 架构问题
- autoroute.Index 使用进程内存，多机无法共享
- 需要改进为 Redis 共享方案

---

## 🎯 改进方案

### 阶段 1: 验证生产环境 71 的 request_logs

#### 1.1 检查最近的 request_logs

```bash
# SSH 到生产环境 71
ssh llm-gateway-71

# 检查 request_logs
docker exec production_postgres psql -U kxuser -d llm_gateway << 'SQL'
-- 最近 1 小时的请求
SELECT 
    COUNT(*) AS total_requests,
    COUNT(*) FILTER (WHERE success = true) AS success_count,
    COUNT(*) FILTER (WHERE success = false) AS failed_count,
    MAX(ts) AS latest_request
FROM request_logs
WHERE ts > now() - interval '1 hour';

-- 最近 10 条记录
SELECT 
    request_id,
    ts,
    client_model,
    credential_id,
    request_status,
    success,
    latency_ms
FROM request_logs
ORDER BY ts DESC
LIMIT 10;
SQL
```

#### 1.2 测试新请求并验证插入

```bash
# 发送测试请求
curl -X POST https://llmgateway.internal.example.com/v1/chat/completions \
  -H "Authorization: Bearer YOUR_PROD_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "测试 request_logs 记录"}],
    "max_tokens": 10
  }'

# 等待 5 秒
sleep 5

# 检查是否插入
docker exec production_postgres psql -U kxuser -d llm_gateway -c "
SELECT request_id, ts, client_model, request_status, success
FROM request_logs
WHERE ts > now() - interval '1 minute'
ORDER BY ts DESC;
"
```

#### 1.3 如果没有插入，检查原因

```bash
# 检查 gateway 日志
docker logs production_gateway | grep "audit: request completed" | tail -20

# 检查数据库连接
docker exec production_gateway env | grep POSTGRES

# 检查是否有写入错误
docker logs production_gateway | grep -i "request_logs\|INSERT.*failed\|database.*error" | tail -20
```

---

### 阶段 2: 测试 minimax-m3 路由（生产环境 71）

#### 2.1 验证配置已同步

```sql
-- 检查 provider_models
SELECT pm.id, p.code, pm.raw_model_name, pm.outbound_model_name, pm.canonical_id
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id
WHERE pm.raw_model_name = 'minimax-m3';
-- 期望: 3 行

-- 检查 credential_model_bindings
SELECT 
    c.id,
    c.label,
    pm.raw_model_name,
    cmb.available,
    cmb.routing_tier
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE pm.raw_model_name = 'minimax-m3';
-- 期望: N 行（N = 活跃 minimax 凭据数）

-- 检查 model_offers
SELECT credential_id, raw_model_name, available, routing_tier
FROM model_offers
WHERE raw_model_name = 'minimax-m3';
-- 期望: N 行
```

#### 2.2 测试 minimax-m3 请求

```bash
# 发送测试请求
curl -X POST https://llmgateway.internal.example.com/v1/chat/completions \
  -H "Authorization: Bearer YOUR_PROD_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "minimax-m3",
    "messages": [{"role": "user", "content": "你好"}],
    "max_tokens": 20
  }'
```

#### 2.3 检查路由决策

```sql
SELECT 
    request_id,
    ts,
    model,
    chosen_credential_id,
    chosen_provider_id,
    tier,
    candidates_tried,
    latency_ms
FROM routing_decision_log
WHERE model = 'minimax-m3'
ORDER BY ts DESC
LIMIT 5;
```

**期望结果**：
- ✅ `candidates_tried > 0`
- ✅ `chosen_credential_id` 非空

#### 2.4 检查 request_logs

```sql
SELECT 
    request_id,
    ts,
    client_model,
    outbound_model,
    credential_id,
    provider_id,
    request_status,
    success,
    latency_ms,
    error_kind
FROM request_logs
WHERE client_model = 'minimax-m3'
ORDER BY ts DESC
LIMIT 5;
```

**期望结果**：
- ✅ 有记录插入
- ✅ `credential_id` 非空（如果路由成功）
- ✅ `request_status` = 'success' 或上游错误

---

### 阶段 3: 改进架构（Redis 共享路由索引）

#### 3.1 设计目标

当前问题：
- autoroute.Index 存储在进程内存
- 多个 gateway 实例各自维护独立索引
- 索引更新不同步

改进方案：
- 使用 Redis 作为索引共享存储
- 任一实例 Refresh 后，通过 Redis PUBSUB 通知所有实例
- 所有实例从 Redis 加载最新索引

#### 3.2 实现方案

**选项 A: Redis 作为主存储**
```go
// autoroute/index_redis.go
type RedisBackedIndex struct {
    redis *redis.Client
    local *Index // 本地缓存
}

func (idx *RedisBackedIndex) Refresh(ctx context.Context) error {
    // 1. 从 DB 查询
    candidates := queryFromDB()
    
    // 2. 序列化并写入 Redis
    data, _ := json.Marshal(candidates)
    idx.redis.Set(ctx, "autoroute:index", data, 10*time.Minute)
    
    // 3. 发布通知
    idx.redis.Publish(ctx, "autoroute:refresh", "updated")
    
    // 4. 更新本地缓存
    idx.local.entries = candidates
    return nil
}

func (idx *RedisBackedIndex) Subscribe(ctx context.Context) {
    pubsub := idx.redis.Subscribe(ctx, "autoroute:refresh")
    for msg := range pubsub.Channel() {
        // 从 Redis 加载最新索引
        idx.loadFromRedis(ctx)
    }
}
```

**选项 B: 只通知，不存储完整索引**
```go
// 通过 Redis PUBSUB 通知，各实例自行从 DB 加载
func (idx *Index) Refresh(ctx context.Context) error {
    // 1. 从 DB 查询并更新本地索引
    idx.refreshFromDB(ctx)
    
    // 2. 通过 Redis 通知其他实例
    if idx.redis != nil {
        idx.redis.Publish(ctx, "autoroute:refresh", time.Now().String())
    }
    return nil
}
```

**推荐**: 选项 B（更简单，避免 Redis 存储大量数据）

#### 3.3 实施步骤

1. **添加 Redis 客户端到 autoroute.Index**
```go
// autoroute/index.go
type Index struct {
    mu          sync.RWMutex
    entries     []Candidate
    byCanonical map[int][]*Candidate
    lastRefresh time.Time
    pool        *pgxpool.Pool
    redis       *redis.Client // 新增
}

func (idx *Index) SetRedis(client *redis.Client) {
    idx.mu.Lock()
    defer idx.mu.Unlock()
    idx.redis = client
}
```

2. **在 Refresh 时发布通知**
```go
func (idx *Index) Refresh(ctx context.Context) error {
    // ... 现有逻辑 ...
    
    // 通知其他实例
    if idx.redis != nil {
        if err := idx.redis.Publish(ctx, "autoroute:refresh", time.Now().String()).Err(); err != nil {
            slog.Warn("failed to publish refresh notification", "error", err)
        }
    }
    return nil
}
```

3. **启动订阅监听器**
```go
// cmd/gateway/main.go
if redisClient != nil {
    go func() {
        pubsub := redisClient.Subscribe(context.Background(), "autoroute:refresh")
        defer pubsub.Close()
        
        for msg := range pubsub.Channel() {
            slog.Info("received autoroute refresh notification", "from", msg.Payload)
            if err := autoIdx.Refresh(context.Background()); err != nil {
                slog.Warn("failed to refresh after notification", "error", err)
            }
        }
    }()
}
```

4. **配置 Redis 连接**
```bash
# .env
LLM_GATEWAY_REDIS_ADDR=redis:6379
```

---

## 🧪 测试计划

### 测试 1: request_logs 实时性测试

**目标**: 验证 request_logs 在生产环境 71 上正常插入

**步骤**:
1. 发送 10 个测试请求（不同模型）
2. 每次请求后等待 2 秒
3. 查询 request_logs 验证插入
4. 记录插入延迟

**期望**:
- 所有请求都有记录
- 插入延迟 < 5 秒

---

### 测试 2: minimax-m3 路由测试

**目标**: 验证 minimax-m3 在生产环境正常路由

**步骤**:
1. 验证配置已同步（provider_models, bindings 等）
2. 发送 minimax-m3 请求
3. 检查 routing_decision_log.candidates_tried
4. 检查 request_logs.credential_id
5. 如果成功，验证上游响应

**期望**:
- candidates_tried > 0
- credential_id 非空
- 成功调用上游或返回上游错误（非 no_candidate）

---

### 测试 3: Redis 共享索引测试（改进后）

**目标**: 验证多个 gateway 实例共享索引

**步骤**:
1. 启动 2 个 gateway 实例（A 和 B）
2. 在实例 A 上触发手动 Refresh
3. 观察实例 B 的日志，确认收到通知并刷新
4. 验证两个实例的索引一致

**期望**:
- 实例 B 收到 Redis PUBSUB 通知
- 实例 B 自动刷新索引
- 两个实例的候选列表一致

---

## 📊 监控指标

### 新增指标

1. **request_logs 插入延迟**
```sql
-- 对比 request 完成时间和 DB 插入时间
SELECT 
    request_id,
    EXTRACT(EPOCH FROM (ts - created_at)) AS insert_delay_seconds
FROM request_logs
WHERE ts > now() - interval '1 hour'
ORDER BY insert_delay_seconds DESC
LIMIT 10;
```

2. **索引刷新同步延迟**
```
-- 从 Redis PUBSUB 发布到其他实例刷新完成的时间
-- 需要在代码中记录时间戳
```

3. **多实例索引一致性**
```
-- 定期检查各实例的索引快照是否一致
GET /api/admin/auto-route/index (每个实例)
```

---

## 🚀 部署计划

### 仅部署到生产环境 71

1. **配置同步检查**
```bash
ssh llm-gateway-71
cd /path/to/llm-gateway-go

# 检查当前配置
docker exec production_postgres psql -U kxuser -d llm_gateway -f /tmp/check_minimax_config.sql
```

2. **如果配置缺失，执行 SQL**
```bash
# 上传配置 SQL
scp minimax_m3_config.sql llm-gateway-71:/tmp/

# 执行
ssh llm-gateway-71
docker exec -i production_postgres psql -U kxuser -d llm_gateway < /tmp/minimax_m3_config.sql
```

3. **重启 gateway（如果需要）**
```bash
docker restart production_gateway
```

4. **验证**
```bash
# 测试请求
curl -X POST https://llmgateway.internal.example.com/v1/chat/completions \
  -H "Authorization: Bearer PROD_KEY" \
  -d '{"model":"minimax-m3",...}'

# 检查日志
docker logs production_gateway | tail -100
```

---

## 📝 回滚计划

如果出现问题：

1. **删除 minimax-m3 配置**
```sql
DELETE FROM credential_model_bindings WHERE provider_model_id IN (
    SELECT id FROM provider_models WHERE raw_model_name = 'minimax-m3'
);
DELETE FROM provider_models WHERE raw_model_name = 'minimax-m3';
```

2. **清除索引**
```sql
DELETE FROM credential_model_index WHERE raw_model = 'minimax-m3';
```

3. **重启 gateway**
```bash
docker restart production_gateway
```

---

## ✅ 检查清单

### 部署前
- [ ] 备份生产数据库
- [ ] 验证配置 SQL 语法正确
- [ ] 准备回滚方案

### 部署中
- [ ] 在 71 上检查现有配置
- [ ] 执行配置 SQL（如需要）
- [ ] 重启 gateway（如需要）

### 部署后
- [ ] 验证 request_logs 正常插入
- [ ] 测试 minimax-m3 路由
- [ ] 检查 routing_decision_log
- [ ] 检查 gateway 日志无错误
- [ ] 监控 5 分钟，确认稳定

---

**下一步**: 请告诉我：
1. 你在 71 上测试的是什么请求？有没有 request_id？
2. 是否需要我准备配置 SQL 脚本用于部署？
3. 是否立即开始 Redis 共享索引的改进？
