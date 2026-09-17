# D04 — 多层队列、限流并发与负载均衡

> 领域编号: D04 ｜ 最近更新: 2026-09-17 (初版) ｜ 状态: v1

## 1. 领域边界

**管**：前端并发进入与出口 LLM 限流/并发的衔接；总待处理队列与各分维队列；处理操作解耦；路由逻辑与错误处理逻辑的封装；权重负载均衡；分布式场景下重复扫描/重复计数的防治。
**不管**：供应商错误分类与回传（D08）；节点状态（D09）；压缩重试的语义（D05）。

## 2. 参考基线

设计文档：
- `docs/架构优化v6/10-dual-backend-queue.md` — 双后端队列（内存|Redis）与分布式调度定稿
- `docs/03-design/01-architecture/architecture/routing-and-state.md` — TPM 资源租约、retry budget
- `docs/03-design/02-feature-design/design/CHANNEL_QUALITY_ROUTING_DESIGN.md` — 通道质量路由

代码入口：
- `domains/dispatch/` — 派发与 Governor
- `ratelimit/`、`pool/`、`durable/`、`cache/`
- `bg/` — 后台 worker（settle/affinity/aggregator 等，leader 选举模式）

## 3. 检查清单

1. **五层信号量 + Governor**（并发/RPM/TPM）约束链完整：任何新增出口路径都经过 Governor，不存在绕过限流直连供应商的调用点。
2. **队列满行为**：拒绝 + Retry-After，非阻塞；窗口内新增入口（端点/任务类型）不引入无界等待。
3. **权重负载均衡**：权重 0/负数/NaN 全部钳位；候选空 → 降级模式 → 全 cooling 放行三级兜底保持可达。
4. **解耦**：总待处理队列与分维队列的操作互不持锁交叉；路由决策与错误处理封装为可单测的纯逻辑（新代码不把两者内联在 transport 层）。
5. **分布式重复**：双实例下会重复扫的 worker 都接入 leader 选举（Redis token-bucket / acquireSweepDistLock 模式，main.go:3584 起源）或 DB 幂等收敛；新 worker 落地时必须二选一并在代码注明。
6. **等待时限**：MaxQueueWaitMS 默认 0（无服务端时限）是已知债（R30 遗留 #6）——不恶化、且热键可调上限 60s 契约不破。
7. **retry budget**：重试消耗预算而非无限放大（雪崩防护），新增重试点纳入预算。

## 4. 历史回归点（轮末回注区）

- [R31] settle/affinity worker 双实例重复扫 → JOIN 翻倍+计数虚高（并发）— 修复 aea284108；acquireSweepDistLock 门控 + 钉桩×8
- [R30] 聚合器 DELETE+重插 NULL 维度 global 行双实例重复 → SUM 双倍（并发）— 修复 f19ba5d5a；advisory_xact_lock
- [R30] 权重 0/负数边界钳位、三级兜底 —— 健康面基准
- [R30 遗留#6] Governor 并发等待默认无服务端时限 —— 开放债，防恶化

- [R36] 预算豁免分支用 errors.Is(err, context.DeadlineExceeded) 会把 http.Client.Timeout（Go ≥1.23 包装为 DeadlineExceeded，go1.27 实证）当预算截断 → 挂起供应商永不自动禁用；豁免只准判父 ctx.Err()；durable_contract/action_bridge 的 UNUSED 标注曾与事实不符（已接线 dormant），照头注释清理前必须 grep 调用方

## 5. 子代理派发提示词

```text
你是 D04（多层队列/限流并发/负载均衡）只读审计子代理。工作目录：本仓库根。
第一步：Read docs/audit/playbook/conventions.md 和 docs/audit/playbook/domains/D04-queue-concurrency.md 全文。
第二步：按域文档 §3 检查清单逐条核对，审计窗口：<窗口>；改动文件清单：<该域相关子集>。
重点：窗口内新增的出口调用点是否过 Governor；新增 worker 是否有 leader 选举或幂等收敛。
只读不改。输出按 conventions.md §4 结构，每条发现带 file:line 与触发路径。
```

### R39 回注（2026-09-17，settle 合成轮过滤批）
- 合成轮口径边界留档：settle 的 session_summaries join（request_count/error_count/health_score）不滤合成轮，方向保守（少归因）；收敛须连写侧一起改并同步两个集成测试期望（F2，接受）。
- writeReward/abandon 不查 RowsAffected：Redis-off 双实例 sweep 指标可虚高（DB 状态不重复，声明接受）。
- Go 侧 IsSyntheticActor trim / SQL 谓词不 trim 的前置条件=写入口 origin_mw TrimSpace；新写者必须同样 trim，禁止单侧"修复"（shadow_actors.go 已注释钉死）。
