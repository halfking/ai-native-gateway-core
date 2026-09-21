# D04 多层队列/并发/限流/负载均衡 子代理报告(窗口:0a015af51^..b75c91900)

## 一、发现(候选,待主代理复核)

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P3 | settle worker 合成轮过滤的返回计数与 Prometheus 指标口径分裂:合成轮 abandon 后 abandoned++ 进返回值(sweep 日志)但不递增 autoRouteSettledTotal{result="abandoned"}(注释声明故意)——sweep 日志的 abandoned 数系统性大于指标面板 | bg/auto_route_settle_worker.go:449-457 对比 :436-441、:246-248 | 接受留档或统一口径 |
| 2 | P3 | credential_probe_v2 对 manual 保护行先打 vendor API 再丢弃结果:probeBalance(真实出网)在 UPDATE 前执行,manual-24h 谓词只在 UPDATE WHERE 处拦截——每小时每 manual 行白耗一次 vendor 余额调用 | bg/credential_probe_v2.go:579-590 | 仅效率损失;可在 probe 前读快照短路 |
| 3 | P3 | refresh-balance 失败路径写 balance_error+checked_at,floor guard 失败路径不写 balance_error——两个 writer 失败落库口径不同(有意设计) | admin/provider_credential_balance.go:90-97 对比 bg/balance_floor_guard.go:999-1009 | 接受,注释互指 |
| 4 | P3(流程卫生) | e9d46b37e 与 6276a3ff9 为同题同补丁双落(两父分支各一份),经 merge 合流。已核实无重复应用、无腐蚀残留:净效果恰好一次应用;builder 唯一定义+唯一调用,测试无重复 | admin/auto_route.go:594(唯一调用)、:724-744(唯一定义);admin/auto_route_task_dist_test.go:18,45 | 无需动作 |

无 P0/P1/P2 候选。

## 二、核实为健康的面

- **Sprintf 腐蚀修复本体正确且收敛**:根因——businessRequestFilter 经自身 Sprintf 返回含裸 % 的片段(NOT LIKE 'probe-%%' → 渲染后单 %);修复把 business/tenant 片段改走 %s 参数位(builder 4 个 %s 对 4 参数,tenant $1 占位与 auditTenantArgs 对位不变)。回归测试钉住无 %! 产物、双轴展开、probe 过滤恰一次。
- **窗口触碰文件无同类 Sprintf-SQL 腐蚀残留**:handler 其余三处 frag 拼接均为非 Sprintf 裸串拼接;taskExpr 的 Sprintf 只嵌常量;bg 侧全部 Sprintf 均为 $%d 占位构建器/日志格式化/display-only;wtSyntheticExclude 唯一 Sprintf 消费点走参数位。
- **合成轮过滤与 D06 G2 单一事实源一致**:Go 侧过滤复用 IsSyntheticActor,与 mr LATERAL、loadTaskBaselines 的 SQL 谓词同源;telemetry.IsInternalAutoEntry 仍是 claim/mirror 双门唯一判定;R39"ss join 不滤合成轮(保守方向)"未恶化。
- **R39 探测队列毒化封死未被重开**:mirror CASE WHEN 守卫、队列 gateway-side 固定 15m backoff、processBatch 前置解密熔断门三件套全在 HEAD;窗口内后续提交均未触碰 probe 毒化面。
- **worker 并发/leader 选举/优雅退出无回归**:Start 幂等 CAS、Stop once+done、run/safeSweep 双层 panic recover、独立 distlock key(TTL>sweep 周期)、probe queue worker 优雅退出、writeReward/abandon 保留 settled_at IS NULL DB 幂等守卫。
- **派发清单修正**:清单中 6 文件窗口内零 commit 触碰;窗口实际 D04 改动面仅 6 文件。
- **检查清单其余条目**:MaxQueueWaitMS 契约未破;窗口无新 LLM 出口路径(余额刷新为 providercap GET-only 零 token+egress 守卫);manual PATCH 正确同时 stamp source+checked_at;窗口无新增队列入口/权重逻辑改动。

## 三、未覆盖项与原因

三门未实跑(只读约束);集成测试需 PG 容器 -tags=integration;probe 毒化封死运行时验证需双实例;Governor 五层信号量窗口内零改动未追溯。
