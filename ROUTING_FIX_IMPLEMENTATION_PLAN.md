# 路由节点状态问题 - 实施计划

> **创建日期**: 2026-08-13  
> **目标**: 修复"网关中节点无效，但直连供应商可行"问题  
> **预计完成**: 2026-08-14

---

## 📋 实施任务清单

### Phase 0: 准备工作 (30分钟)

- [ ] **0.1** 备份当前 154 服务器二进制文件
  ```bash
  bash scripts/deploy-seamless.sh status 154
  ```

- [ ] **0.2** 备份当前数据库连接配置
  ```bash
  env-injector inject aliyun-gateway-154
  ```

- [ ] **0.3** 创建 Git 分支
  ```bash
  cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
  git checkout -b fix/routing-db-failsafe-20260813
  ```

- [ ] **0.4** 确认当前代码库状态
  ```bash
  git status
  git log --oneline -5
  ```

---

### Phase 1: P0 修复 - 数据库 Fail-Safe (2-3小时)

#### 任务 1.1: 增加数据库连接池配置 (30分钟)

**文件**: `internal/config/config.go`

- [ ] **1.1.1** 找到数据库配置结构体
  ```bash
  grep -n "type.*Config.*struct" internal/config/config.go
  grep -n "MaxConns" internal/config/config.go
  ```

- [ ] **1.1.2** 添加新的连接池配置字段
  ```go
  // 在 DatabaseConfig 结构体中添加
  MaxConns          int           `yaml:"max_conns" default:"50"`
  MinConns          int           `yaml:"min_conns" default:"10"`
  MaxConnLifetime   time.Duration `yaml:"max_conn_lifetime" default:"1h"`
  MaxConnIdleTime   time.Duration `yaml:"max_conn_idle_time" default:"30m"`
  HealthCheckPeriod time.Duration `yaml:"health_check_period" default:"1m"`
  ```

- [ ] **1.1.3** 更新数据库初始化代码
  ```go
  // 在创建连接池的地方 (可能在 internal/database/postgres.go)
  config, err := pgxpool.ParseConfig(connString)
  config.MaxConns = cfg.MaxConns
  config.MinConns = cfg.MinConns
  config.MaxConnLifetime = cfg.MaxConnLifetime
  config.MaxConnIdleTime = cfg.MaxConnIdleTime
  config.HealthCheckPeriod = cfg.HealthCheckPeriod
  ```

- [ ] **1.1.4** 更新配置文件
  ```yaml
  # Deployment configuration must use the managed environment for the target.
  database:
    max_conns: 50
    min_conns: 10
    max_conn_lifetime: 1h
    max_conn_idle_time: 30m
    health_check_period: 1m
  ```

- [ ] **1.1.5** 编译测试
  ```bash
  go build -o gateway ./cmd/gateway
  ```

---

#### 任务 1.2: 实现本地路由计划缓存 (60分钟)

**新增文件**: `domains/routing/plan_cache.go`

- [ ] **1.2.1** 创建 PlanCache 结构体
  ```bash
  cat > domains/routing/plan_cache.go << 'PLANECACHE'
  package routing
  
  import (
      "sync"
      "time"
  )
  
  // PlanCache provides a fail-safe in-memory cache for routing plans.
  type PlanCache struct {
      mu     sync.RWMutex
      plans  map[string][]Candidate
      expiry map[string]time.Time
      ttl    time.Duration
  }
  
  func NewPlanCache(ttl time.Duration) *PlanCache {
      return &PlanCache{
          plans:  make(map[string][]Candidate),
          expiry: make(map[string]time.Time),
          ttl:    ttl,
      }
  }
  
  // Get returns cached candidates, allowing stale data if allowStale=true.
  func (c *PlanCache) Get(model string, allowStale bool) ([]Candidate, bool) {
      c.mu.RLock()
      defer c.mu.RUnlock()
      
      candidates, ok := c.plans[model]
      if !ok {
          return nil, false
      }
      
      expiry, ok := c.expiry[model]
      if !ok {
          return nil, false
      }
      
      if time.Now().After(expiry) {
          if !allowStale {
              return nil, false
          }
      }
      
      return candidates, true
  }
  
  // Set updates the cache.
  func (c *PlanCache) Set(model string, candidates []Candidate) {
      c.mu.Lock()
      defer c.mu.Unlock()
      
      c.plans[model] = candidates
      c.expiry[model] = time.Now().Add(c.ttl)
  }
  PLANECACHE
  ```

- [ ] **1.2.2** 修改 Planner 结构体添加缓存
  ```bash
  # 找到 Planner 定义
  grep -n "type Planner struct" domains/routing/*.go
  ```
  
  ```go
  // 添加字段
  type Planner struct {
      db        *pgxpool.Pool
      planCache *PlanCache  // 新增
      logger    *slog.Logger
  }
  
  // 修改构造函数
  func NewPlanner(db *pgxpool.Pool, logger *slog.Logger) *Planner {
      return &Planner{
          db:        db,
          planCache: NewPlanCache(60 * time.Second),  // 60秒 TTL
          logger:    logger,
      }
  }
  ```

- [ ] **1.2.3** 编译测试
  ```bash
  go build -o gateway ./cmd/gateway
  ```

---

#### 任务 1.3: 添加数据库查询重试机制 (60分钟)

**文件**: `domains/routing/planner.go`

- [ ] **1.3.1** 找到 PlanCandidatesWithContext 函数
  ```bash
  grep -n "func.*PlanCandidatesWithContext" domains/routing/*.go
  ```

- [ ] **1.3.2** 添加重试辅助函数
  ```go
  // 在文件末尾添加
  func isRetryableDBError(err error) bool {
      if err == nil {
          return false
      }
      
      // 不重试 ErrNoRows (这是正常的"没有记录"，不是连接错误)
      if errors.Is(err, pgx.ErrNoRows) {
          return false
      }
      
      // 重试以下错误
      errStr := err.Error()
      return errors.Is(err, context.DeadlineExceeded) ||
             strings.Contains(errStr, "connection refused") ||
             strings.Contains(errStr, "timeout") ||
             strings.Contains(errStr, "connection reset") ||
             strings.Contains(errStr, "broken pipe")
  }
  ```

- [ ] **1.3.3** 提取数据库查询到独立函数
  ```go
  func (p *Planner) queryDatabaseWithRetry(ctx context.Context, model string) ([]Candidate, error) {
      var candidates []Candidate
      var lastErr error
      
      // 最多重试 3 次
      for attempt := 0; attempt < 3; attempt++ {
          // 执行实际的数据库查询
          rows, err := p.db.Query(ctx, `
              SELECT provider_id, credential_id, priority, weight
              FROM routing_plans
              WHERE model = $1 AND enabled = true
              ORDER BY priority DESC, weight DESC
          `, model)
          
          if err != nil {
              lastErr = err
              
              // 判断是否可重试
              if isRetryableDBError(err) && attempt < 2 {
                  // 指数退避: 50ms, 100ms, 150ms
                  backoff := time.Duration(50*(attempt+1)) * time.Millisecond
                  p.logger.Warn("db query failed, retrying",
                      "model", model,
                      "attempt", attempt+1,
                      "error", err,
                      "backoff", backoff)
                  time.Sleep(backoff)
                  continue
              }
              
              // 不可重试或已达最大次数
              return nil, fmt.Errorf("db query failed after %d attempts: %w", attempt+1, err)
          }
          
          // 成功，解析结果
          defer rows.Close()
          for rows.Next() {
              var c Candidate
              if err := rows.Scan(&c.ProviderID, &c.CredentialID, &c.Priority, &c.Weight); err != nil {
                  return nil, fmt.Errorf("scan row failed: %w", err)
              }
              candidates = append(candidates, c)
          }
          
          if err := rows.Err(); err != nil {
              lastErr = err
              if isRetryableDBError(err) && attempt < 2 {
                  backoff := time.Duration(50*(attempt+1)) * time.Millisecond
                  time.Sleep(backoff)
                  continue
              }
              return nil, fmt.Errorf("rows iteration failed: %w", err)
          }
          
          // 查询成功
          return candidates, nil
      }
      
      return nil, lastErr
  }
  ```

- [ ] **1.3.4** 修改 PlanCandidatesWithContext 使用缓存
  ```go
  func (p *Planner) PlanCandidatesWithContext(ctx context.Context, model string) ([]Candidate, error) {
      // 先尝试数据库查询 (带重试)
      candidates, err := p.queryDatabaseWithRetry(ctx, model)
      if err == nil {
          // 查询成功，更新缓存
          if len(candidates) > 0 {
              p.planCache.Set(model, candidates)
          }
          return candidates, nil
      }
      
      // 数据库查询失败，记录错误
      p.logger.Error("database query failed, trying cache",
          "model", model,
          "error", err)
      
      // 尝试从缓存读取 (允许过期数据)
      if cached, ok := p.planCache.Get(model, true); ok {
          p.logger.Warn("database unavailable, serving stale plan from cache",
              "model", model,
              "cached_count", len(cached),
              "note", "data may be up to 60s stale")
          return cached, nil
      }
      
      // 数据库失败且缓存也没有
      return nil, fmt.Errorf("db_empty and no cache available: %w", err)
  }
  ```

- [ ] **1.3.5** 编译测试
  ```bash
  go build -o gateway ./cmd/gateway
  ```

- [ ] **1.3.6** 运行单元测试
  ```bash
  go test -v ./domains/routing/...
  ```

---

### Phase 2: P1 优化 - LRU 缓存配置 (30分钟)

#### 任务 2.1: 调整 URSM v2 缓存参数

**文件**: `domains/ursm/v2/config.go` 或配置文件

- [ ] **2.1.1** 找到 LRU 配置
  ```bash
  grep -rn "LRUMirrorSize" domains/ursm/
  grep -rn "LRUMirrorSoftTTL" domains/ursm/
  ```

- [ ] **2.1.2** 修改默认值或配置文件
  ```go
  // 如果是代码中的默认值
  LRUMirrorSize:    300_000,  // 从 100_000 增加到 300_000
  LRUMirrorSoftTTL: 60 * time.Second,  // 从 30s 增加到 60s
  ```
  
  或
  
  ```yaml
  # 如果是配置文件 (/etc/llm-gateway-go/config.yaml)
  ursm_v2:
    lru_mirror_size: 300000
    lru_mirror_soft_ttl: 60s
  ```

- [ ] **2.1.3** 编译测试
  ```bash
  go build -o gateway ./cmd/gateway
  ```

---

### Phase 3: 集成测试 (60分钟)

#### 任务 3.1: 本地测试

- [ ] **3.1.1** 准备测试环境
  ```bash
  # 确保本地有 PostgreSQL
  docker run -d --name test-postgres \
    -e POSTGRES_USER=llm_gateway \
    -e POSTGRES_PASSWORD=test123 \
    -e POSTGRES_DB=llm_gateway \
    -p 5432:5432 \
    postgres:17
  
  # 创建测试表
  psql -h localhost -U llm_gateway -d llm_gateway -c "
  CREATE TABLE IF NOT EXISTS routing_plans (
      id SERIAL PRIMARY KEY,
      model VARCHAR(255) NOT NULL,
      provider_id INT NOT NULL,
      credential_id INT NOT NULL,
      priority INT DEFAULT 0,
      weight INT DEFAULT 100,
      enabled BOOLEAN DEFAULT true,
      created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
      updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
  );
  
  INSERT INTO routing_plans (model, provider_id, credential_id, priority, weight)
  VALUES 
    ('gpt-5.6-luna', 1, 10, 100, 100),
    ('gpt-5.6-terra', 1, 10, 100, 100),
    ('claude-opus-5', 2, 20, 90, 100);
  "
  ```

- [ ] **3.1.2** 测试正常查询
  ```bash
  # 启动网关 (本地)
  ./gateway -config config.local.yaml
  
  # 测试请求
  curl -X POST http://localhost:8080/v1/chat/completions \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer test-key" \
    -d '{
      "model": "gpt-5.6-luna",
      "messages": [{"role": "user", "content": "test"}]
    }'
  ```

- [ ] **3.1.3** 测试数据库故障场景
  ```bash
  # 停止数据库模拟故障
  docker stop test-postgres
  
  # 测试请求 (应该从缓存返回)
  curl -X POST http://localhost:8080/v1/chat/completions \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer test-key" \
    -d '{
      "model": "gpt-5.6-luna",
      "messages": [{"role": "user", "content": "test"}]
    }'
  
  # 检查日志是否有 "serving stale plan from cache"
  ```

- [ ] **3.1.4** 测试恢复
  ```bash
  # 重启数据库
  docker start test-postgres
  
  # 等待 5 秒
  sleep 5
  
  # 测试请求 (应该从数据库返回)
  curl -X POST http://localhost:8080/v1/chat/completions \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer test-key" \
    -d '{
      "model": "gpt-5.6-luna",
      "messages": [{"role": "user", "content": "test"}]
    }'
  ```

---

#### 任务 3.2: 154 服务器部署测试

- [ ] **3.2.1** 编译生产二进制
  ```bash
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o gateway ./cmd/gateway
  ```

- [ ] **3.2.2** 使用 canonical promotion runner 部署到 245，产出 gate 记录。
- [ ] **3.2.3** 通过 245 gate 后，经人工确认再使用同一 gate 晋级到 154。
- [ ] **3.2.4** 使用 `env-injector inject aliyun-gateway-154` 管理运行时配置；不直接覆盖服务器配置文件。
- [ ] **3.2.5** 用 `https://llm.kxpms.cn` 验证健康检查和已认证业务 smoke。
- [ ] **3.2.6** 保留 30 分钟日志与指标观察记录。

---

### Phase 4: 提交代码 (30分钟)

- [ ] **4.1** 运行测试套件
  ```bash
  go test ./...
  ```

- [ ] **4.2** 代码格式化
  ```bash
  go fmt ./...
  goimports -w .
  ```

- [ ] **4.3** 提交代码
  ```bash
  git add .
  git commit -m "fix(routing): add database failsafe and retry mechanism

  - Add PlanCache for fail-safe routing plan caching
  - Add database query retry with exponential backoff
  - Increase database connection pool size (10 → 50)
  - Optimize URSM v2 LRU cache (100k → 300k, 30s → 60s)
  
  Fixes: routing returns 'No available provider' when DB is temporarily unreachable
  Root cause: db_empty error with no fallback mechanism
  Solution: Three-layer fail-safe (DB retry → local cache → URSM LRU)
  
  Ref: ROUTING_NODE_STATUS_AUDIT_20260813.md
  Ref: ROUTING_NODE_DIAGNOSIS_DEEP_DIVE.md"
  ```

- [ ] **4.4** 推送到远程
  ```bash
  git push origin fix/routing-db-failsafe-20260813
  ```

- [ ] **4.5** 创建 Pull Request
  - 标题: `[P0] 修复路由数据库故障导致无候选节点问题`
  - 描述: 引用 ROUTING_NODE_DIAGNOSIS_DEEP_DIVE.md
  - 标签: `bug`, `P0`, `routing`, `database`

---

## 📊 验收标准

### 功能验收

- [ ] **F1**: 正常情况下，路由查询从数据库返回
- [ ] **F2**: 数据库短暂不可达时 (< 150ms)，通过重试恢复
- [ ] **F3**: 数据库持续不可达时 (> 150ms)，从本地缓存返回 (允许旧数据)
- [ ] **F4**: 缓存命中时，响应时间 < 10ms
- [ ] **F5**: 日志中包含缓存状态信息 ("serving stale plan from cache")

### 性能验收

- [ ] **P1**: 正常请求延迟 < 50ms (P99)
- [ ] **P2**: 数据库重试次数 < 5% 的请求
- [ ] **P3**: 缓存命中率 > 80%
- [ ] **P4**: 内存增长 < 100MB (PlanCache 占用)

### 可靠性验收

- [ ] **R1**: 数据库故障时，零请求失败 (100% 从缓存服务)
- [ ] **R2**: 数据库恢复后，自动切回数据库查询
- [ ] **R3**: 连续 24 小时无 "No available provider" 错误
- [ ] **R4**: 监控指标正常上报

---

## 🔄 回滚计划

### 快速回滚 (< 2 分钟)

```bash
# 使用项目标准回滚入口，恢复上一个已验证 release。
bash scripts/deploy-seamless.sh rollback 154
```

### 回滚配置文件 (如果需要)

```bash
env-injector inject aliyun-gateway-154
```

---

## 📝 文档更新

- [ ] 更新 `README.md` - 添加 Fail-Safe 机制说明
- [ ] 更新 `docs/architecture.md` - 添加路由缓存架构图
- [ ] 更新 `docs/operations.md` - 添加故障恢复流程
- [ ] 创建 `CHANGELOG.md` 条目

---

## 🎯 时间表

| Phase | 预计时间 | 实际时间 | 状态 |
|-------|---------|---------|------|
| Phase 0: 准备 | 30 min | - | ⏳ |
| Phase 1: P0 修复 | 2-3 hours | - | ⏳ |
| Phase 2: P1 优化 | 30 min | - | ⏳ |
| Phase 3: 集成测试 | 60 min | - | ⏳ |
| Phase 4: 提交代码 | 30 min | - | ⏳ |
| **总计** | **4.5-5.5 hours** | - | ⏳ |

**预计开始时间**: 2026-08-13 14:00  
**预计完成时间**: 2026-08-13 19:30

---

## ✅ 完成检查清单

- [ ] 所有代码已提交并推送
- [ ] Pull Request 已创建并 review
- [ ] 154 服务器已部署新版本
- [ ] 服务运行正常 (24小时监控)
- [ ] 监控指标已配置
- [ ] 文档已更新
- [ ] 团队已通知

---

**实施负责人**: AI Agent  
**审核人**: Tech Lead  
**最后更新**: 2026-08-13
