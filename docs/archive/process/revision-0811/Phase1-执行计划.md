# Phase 1 紧急修复执行计划

> **已被审计修订取代**：保留用于追溯原始讨论，不作为实施清单。下一阶段只执行 `06-下一阶段实施计划.md` 中的证据与契约任务。

> **执行周期**: Week 1-2 (2026-08-11 ~ 2026-08-24)
> **目标**: 修复关键bug、优化性能瓶颈、补全测试
> **优先级**: P0 (契约/安全/回滚门禁) + P1 (可观测性和性能证据)
> **执行原则**: 先审计和建立基线，再改热路径；所有目标值在基线报告生成后确认。

---

## 任务清单

### Task 1.1: 修复v6分类器回归 (P0)

**现状**:
- Commit `9337ef96f` revert 了一个名为“v6 routing matrix audit + classifier dead-code hookup”的提交。
- revert 的实际 diff 涉及 `specbundle` telemetry hook，而不是已证实的分类器生产接线故障；故障原因待补充证据。

**执行步骤**:
1. [ ] 分析revert原因 (查看 `autoroute/v6_routing_matrix_test.go` 失败case)
2. [ ] 先用现有 `autoroute/` 测试和 `477_auto_route_v6_defaults.sql` 对齐 `TaskType`
   - 文件: `autoroute/task_types_ext.go`
   - 确保分类器输出与路由矩阵输入对齐
3. [ ] 仅在测试复现后修复 `decisionToWire` nil panic；不能预先假设该 panic 属于 v6 回归
4. [ ] 扩展回归测试
   - 新增: `autoroute/v6_classifier_integration_test.go`
   - 覆盖: 10种task type × 5种complexity level
5. [ ] 重新提交 + CI全绿

**验收标准**:
- `go test ./autoroute/... -v` 全部通过
- `model=auto` 请求路由准确率 ≥ 95%
- 无nil panic

**预计工时**: 2天

---

### Task 1.2: SessionCompressor异步化 (P0)

**现状**:
- `SessionCacheV2` 的读写路径需要按实际调用链和 trace 数据确认是否阻塞主路径。
- P95 延迟当前未有可复现本地证据，先建立 baseline，不使用 `1.85s` 作为事实。

**执行步骤**:
1. [ ] 实现L0 LRU缓存 (纯内存，无阻塞)
   ```go
   type L0Cache struct {
       lru *lru.Cache  // 1024条目
       mu  sync.RWMutex
   }
   ```

2. [ ] 先做 L0/L1/L2 命中率、延迟和一致性测量；只有确认阻塞后再设计异步读取
   ```go
   func (sc *SessionCompressor) asyncReadState(ctx, sid) <-chan *SessionState {
       ch := make(chan *SessionState, 1)
       go func() {
           state, _ := sc.readFromRedis(ctx, sid)
           ch <- state
       }()
       return ch
   }
   ```

3. [ ] 异步写回必须先定义队列、回压、顺序、丢弃、重试和回滚，不允许直接启动无生命周期 goroutine
   ```go
   func (sc *SessionCompressor) asyncUpdateCache(ctx, sid, state) {
       go func() {
           sc.writeToRedis(ctx, sid, state)
           sc.writeToPG(ctx, sid, state)  // 后台持久化
       }()
   }
   ```

4. [ ] 超时控制
   - 超时策略必须由请求类型和一致性要求决定
   - 不得在无法读取会话状态时无条件使用空状态，否则会造成上下文丢失

5. [ ] 压测验证
   ```bash
   wrk -t 10 -c 1000 -d 60s benchmark/session_compress.lua
   ```

**验收标准**:
- P95延迟 <1s (↓46%)
- 吞吐量 >2,000 req/s (↑60%)
- 无死锁、无goroutine泄漏

**预计工时**: 3天

---

### Task 1.3: IR Adapter SSRF防护 (P0)

**现状**:
- `domains/ir/gemini.go:156` 未校验URL
- 攻击者可构造 `url="http://169.254.169.254/latest/meta-data/"`

**执行步骤**:
1. [ ] 新增安全模块
   - 文件: `domains/security/ssrf_guard.go`
   - 函数: `SafeFetch(url string) (*http.Response, error)`

2. [ ] 实现IP黑名单
   ```go
   var privateIPRanges = []string{
       "10.0.0.0/8",
       "172.16.0.0/12",
       "192.168.0.0/16",
       "169.254.0.0/16",  // AWS metadata
       "127.0.0.0/8",
   }
   ```

3. [ ] 实现域名黑名单
   ```go
   var blockedHosts = []string{
       "metadata.google.internal",
       "169.254.169.254",
       "metadata.internal",
   }
   ```

4. [ ] 替换所有 `http.Get(url)` 调用
   - 文件: `domains/ir/*.go` (8处)
   - 改为: `security.SafeFetch(url)`

5. [ ] 渗透测试
   - 尝试访问元数据服务
   - 尝试访问内网IP
   - 确认全部被阻断

**验收标准**:
- 私有IP/元数据服务访问被阻断
- 合法URL不受影响
- 单元测试覆盖 ≥ 90%

**预计工时**: 1天

---

### Task 1.4: 免费tier quota追踪优化 (P1)

**现状**:
- 当前窗口粒度: 1分钟
- 429错误率: 15.6%，目标 <5%

**执行步骤**:
1. [ ] 改为滑动窗口
   - 从固定1分钟桶 → 10秒滑动窗口
   - Redis: 用sorted set存储时间戳

2. [ ] 优化quota检查逻辑
   ```go
   func (qt *QuotaTracker) Check(ctx, provider, model) QuotaState {
       now := time.Now()
       // 10秒窗口
       requests := redis.ZCount(key, now.Add(-10*time.Second), now)
       if requests >= limit {
           return QuotaStateLimited
       }
       return QuotaStateAvailable
   }
   ```

3. [ ] 预测性限流
   - 当窗口使用率 > 80% 时，提前触发cooldown
   - 避免突发流量打爆quota

4. [ ] 多级窗口监控
   - 10s / 1min / 1hour / 24h 四级窗口
   - 任一级别触限则标记为limited

**验收标准**:
- 429错误率 <5% (↓68%)
- 免费tier命中率 ≥ 85%
- Redis ops增长 <20%

**预计工时**: 2天

---

### Task 1.5: 补全测试覆盖 (P1)

**现状**:
- `domains/freeresource/` 覆盖率 45.2%
- `domains/ir/` 覆盖率 53.7%

**执行步骤**:
1. [ ] freeresource模块测试
   - `quota_tracker_test.go`: 补充边界case (+15个test)
   - `catalog_omniroute_test.go`: 验证523条目完整性
   - `keyrotator_test.go`: 多Key轮换逻辑覆盖

2. [ ] IR模块测试
   - `gemini_test.go`: CSV base64编码测试
   - `anthropic_test.go`: content blocks标准化测试
   - `adapter_test.go`: 跨协议序列化测试

3. [ ] 集成测试
   - `integration/free_tier_e2e_test.go`: 端到端免费tier流程
   - 覆盖: model=auto → quota check → 多Key轮换 → 429分类 → fallback

**验收标准**:
- `domains/freeresource/` 覆盖率 ≥ 75%
- `domains/ir/` 覆盖率 ≥ 70%
- CI全绿

**预计工时**: 3天

---

### Task 1.6: 慢查询索引优化 (P1)

**现状**:
- Turns列表查询 2.3s
- 缺少复合索引

**执行步骤**:
1. [ ] 分析慢查询
   ```sql
   EXPLAIN ANALYZE
   SELECT t.*, s.title as session_title
   FROM session_turns t
   JOIN sessions s ON t.session_id = s.id
   WHERE s.tenant_id = 'tenant_123'
     AND t.created_at > NOW() - INTERVAL '7 days'
   ORDER BY t.created_at DESC
   LIMIT 50;
   ```

2. [ ] 创建迁移文件
   - 文件: `sql/migrations/startup/078_optimize_turns_indexes.sql`
   ```sql
   -- sessions表复合索引
   CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_sessions_tenant_created
     ON sessions(tenant_id, created_at DESC);

   -- turns表复合索引
   CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_turns_session_created
     ON session_turns(session_id, created_at DESC);

   -- 多租户查询优化
   CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_turns_tenant_created
     ON session_turns(tenant_id, created_at DESC)
     WHERE tenant_id IS NOT NULL;
   ```

3. [ ] 验证查询性能
   - 目标: 查询时间 <200ms

**验收标准**:
- Turns列表查询 <200ms (↓91%)
- `EXPLAIN` 显示使用了索引
- 无全表扫描

**预计工时**: 0.5天

---

### Task 1.7: 内存限制配置 (P1)

**现状**:
- 单实例内存占用 4.2GB (1000并发)
- SessionCacheV2无上限

**执行步骤**:
1. [ ] 配置LRU eviction
   ```go
   type SessionCacheV2 struct {
       cache *lru.Cache  // 设置maxEntries=10000
       maxMemoryMB int   // 软限制3GB
   }
   ```

2. [ ] 内存监控
   ```go
   func (sc *SessionCacheV2) monitorMemory() {
       ticker := time.NewTicker(10 * time.Second)
       for range ticker.C {
           var m runtime.MemStats
           runtime.ReadMemStats(&m)
           if m.Alloc > sc.maxMemoryMB * 1024 * 1024 {
               sc.evictOldest(1000)  // 驱逐最老的1000条
           }
       }
   }
   ```

3. [ ] Metrics暴露
   ```
   llm_gateway_cache_memory_bytes{cache="session_v2"}
   llm_gateway_cache_entries{cache="session_v2"}
   llm_gateway_cache_evictions_total{cache="session_v2",reason="memory|lru"}
   ```

**验收标准**:
- 单实例内存 <3GB (↓29%)
- 无OOM
- cache命中率 >80%

**预计工时**: 1天

---

## 时间规划

### Week 1 (2026-08-11 ~ 2026-08-17)

| 日期 | 任务 | 负责人 | 状态 |
|------|------|--------|------|
| Mon-Tue | Task 1.1 v6分类器修复 | @halfking | ⏳ |
| Wed-Thu | Task 1.3 SSRF防护 | @halfking | ⏳ |
| Fri | Task 1.6 索引优化 | @halfking | ⏳ |
| Mon-Fri | Task 1.5 测试补全 | @ACC-Agent | ⏳ |

### Week 2 (2026-08-18 ~ 2026-08-24)

| 日期 | 任务 | 负责人 | 状态 |
|------|------|--------|------|
| Mon-Wed | Task 1.2 SessionCompressor异步化 | @halfking | ⏳ |
| Thu-Fri | Task 1.4 quota追踪优化 | @halfking | ⏳ |
| Thu | Task 1.7 内存限制 | @halfking | ⏳ |
| Fri | Phase 1验收 + 压测 | @全员 | ⏳ |

---

## 验收标准

### 性能指标

| 指标 | 当前 | Phase 1目标 | 测试方法 |
|------|------|------------|---------|
| P95延迟 | 1.85s | <1.2s | wrk压测 |
| 吞吐量 | 1,247/s | >1,800/s | wrk压测 |
| 429错误率 | 15.6% | <8% | 实际流量观察 |
| 内存占用 | 4.2GB | <3.2GB | runtime.MemStats |
| 测试覆盖 | 70.4% | >73% | go test -cover |

### 功能指标

| 功能 | 验收标准 |
|------|---------|
| v6路由 | `model=auto` 准确率 ≥95%，无panic |
| SSRF防护 | 私有IP/元数据服务访问全部阻断 |
| 慢查询 | Turns列表 <200ms |
| 免费tier | quota检查延迟 <10ms，命中率 ≥85% |

### 质量指标

| 指标 | 目标 |
|------|------|
| CI状态 | 全绿 |
| 静态分析 | 新增问题 = 0 |
| 线上故障 | 0起 |
| 回滚次数 | 0次 |

---

## 风险管理

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|---------|
| 异步化引入死锁 | Medium | High | 严格code review + race detector |
| v6分类器再次回归 | Low | High | 扩展回归测试 + canary发布 |
| 内存限制过激驱逐 | Medium | Medium | 软限制 + 监控告警 |
| 索引创建锁表 | Low | Medium | CONCURRENTLY创建 |

---

## 下一步

Phase 1完成后：
1. **Phase 1总结会议** - 评审指标达成情况
2. **Phase 2启动会议** - RTK+Caveman压缩POC
3. **OmniFree决策** - 如老板批准D1，合并boost分支

---

**文档版本**: v1.0
**创建时间**: 2026-08-11
**负责人**: @halfking
**状态**: ⏳ 执行中
