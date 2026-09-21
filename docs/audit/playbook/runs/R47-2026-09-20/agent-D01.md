# D01 路由核心增量 子代理报告（窗口：45412e919..HEAD；净新增重点 = R46 修复 commit 01c276a6a）

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P3 | F8④ 负权重 clamp 不彻底：W_HEADROOM/W_CAPACITY 为负时无守卫、无 clamp，负权重直接乘进 composite——penalty 变奖励方向反转。clampEnvWeight 只用在首跳抽签四权重 | domains/streaming/executors/router_scoring.go:78,88,96；clamp 仅 :600-605,634-635 | 两处补 clampEnvWeight（F8④ 收尾，预存在缺陷） |
| 2 | P3 | F1 新鲜度上限与 window 耦合：Info 判陈旧用 time.Since(snapAt)>=t.window，window 可被 env 调大（86400s 时冻结快照最长 24h 参与评分） | sticky_load.go:311、:92-95 | 新鲜度上限与语义窗解耦：min(window, 固定上限) |
| 3 | P3 | activity 保留窗 max(10min, window) 未跟随 RECENCY_HORIZON env（horizon 900s 且 window≤600s 时 recency 信号 10-15min 段静默归零） | sticky_load.go:156,179-184；horizon 读取 router_scoring.go:530 | 保留窗取 max(10min, window, horizon) |
| 4 | P3 | F2 注释与 CASE 语义缝隙：tier-1 判据 provider_id=p.id 不校验 scope，scope='tenant' 且带 provider_id=p.id 混合行落供应商档（CHECK 不禁止，现网 tenant 行 provider_id NULL 不触发） | provider/client.go:1579-1584 vs :1573-1574 | 注释精确化或 tier-1 补 scope 判据 |
| 5 | P3（预存在） | 快照整体替换语义：Refresh 成功后 snapshot 只含本次请求凭据集，蓝绿双活+多模型交错时另一模型 3s TTL 内 covered=false 回落单实例内存 | sticky_load.go:287-289 | 并入方案 B 批量快照时改 merge-into-snapshot |
| 6 | P3（预存在） | pricing_plans 全库无索引，pp_fb LATERAL 每 candidate 行顺序扫描；F2 守卫边际成本可忽略（候选查询 30s 缓存摊薄） | provider/client.go:1560-1586；01-schema.sql:11109 | 按家规真库 EXPLAIN 后再议索引 |

## 二、核实为健康的面
- F1 window=0/负/NaN 防线完整（构造钳回默认，无零值逃逸）；单飞 defer 复位正确（panic/失败路径均复位，重试频率上界 ~4/s）；Info 并发安全（值拷贝读，无撕裂）；Close sync.Once 无死锁环。
- F2 SQL 三段否定边界安全（scope NOT NULL+CHECK；IS NOT NULL 前置使 <> NULL 短路）；F8②③④⑪⑫ 落地属实；sticky 保持/迁移与键一致性同 R46 留档；修复 commit 未触碰主评分/派发链。

## 三、评分热路径性能专项方案（R46 §五#1，供主代理裁决）
现状：每次 calculateLoadScore ~12-13 次 env+ParseFloat；Info() 每请求 ~6Tn 次、每次 2 把锁 + O(s) 扫描；n=10,T=2 即 ~480 次 env 读/240 次锁操作/请求。附带 candidatePressure 重复计算、:154 采样日志占 10%。
- A1 每请求解析一次装入 StrategyInput（最小侵入，保留 t.Setenv 语义）；
- A2 NewRouter 时解析入 Router（atomic.Pointer），每请求 env 读归零（需过"先 NewRouter 后 Setenv"测试面）；
- B planCandidates 单次 Snapshot(ids) 批量共享（收益最高，Info 调用 6Tn→Tn，同请求全候选对齐同一时刻；顺手修发现#5）；
- C 全局 mu 分片（O(s) 非瓶颈，真增量计数器易出少计 bug 仅在 gauge 证明 s 偏大后做）；
- D 基准先行：BenchmarkPlanByTier(n=10,s=50) + 路由 p99 前后对照。

## 四、未覆盖项与原因
- pp_fb LATERAL 真库 EXPLAIN（无活库连接）；-race 复跑（只读审计）；ShadowStrategy.Score 消费链（标准部署 nil）；benchmark 数字（读码推算上界）。
