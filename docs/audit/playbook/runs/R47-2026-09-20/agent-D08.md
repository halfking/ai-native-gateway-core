# D08（proxy 优先级接线）+ D14（安全横切取样）+ D17（代码卫生）联合子代理报告（窗口：45412e919..HEAD）

## 一、发现（候选）
| # | 级别候选 | 发现 | 证据 | 建议处置 |
|---|---|---|---|---|
| 1 | P2 | LiteRetentionWorker 会话清理 check-then-act 竞态（同 D05#2/D06-3） | bg/lite_retention_worker.go:82,104,112 | turns DELETE 加 updated_at 复核或单事务 |
| 2 | P3 | proxy 订阅 Priority 写入无范围校验（负值成"最高优先级"；与 manual_priority 0..99 惯例不一致）；F7 测试缺口（负值/缺 map 条目） | admin/proxy.go:350,391,461-462 | create/update 加 0..99 校验 + 补测试 |
| 3 | P3 | F8⑦ 注释过保："各方法入口 clamp"但 BucketSuccessRates 入口无 clamp（生产唯一调用链已钳，注释失实+契约缺口） | domains/providerprofile/adapters.go:114 vs 241-262 | 入口补 clamp 或注释改精确 |
| 4 | P3 | 死代码：stickyLoadEnvInt 全仓零调用（注释指向不存在本文件的消费）——**主代理复核推翻：router_scoring.go:518 stickySessionCapacity 活调用，恢复** | domains/streaming/executors/sticky_load.go:360-370 | ~~删除~~ 不采纳 |
| 5 | P3 | 零调用接缝：StickyLoadStore.Remove（注释"留给 admin 清理路径"，该路径不存在；仅测试消费） | domains/ursm/v2/cache/sticky_load.go:62-67 | RESERVED 注释或删除 |
| 6 | P3 | StickyLoadTracker.Close 生产零接线（偏离 D-L1 join 基准，进程退出兜底） | cmd/gateway/main.go:1436 | 停机序列补 Close |
| 7 | P3 | bandit 删除残留三处注释漂移：①router_scoring.go:86 banditOrder 已删；②router.go:187 把已删机制当现役对照；③router.go:1044-1047 引用已删除的 router_bandit_test.go | 各文件 | 顺手更正 |
| 8 | P3 | manager.go:662 注释引用不存在的 LoadCache（实为 ReloadCache） | proxy/manager.go:662 | 改正 |

## 二、核实为健康的面
- F7 排序实现（键序/缺省回落 0/无 NaN 可能/快照 selectionMu 内取/优先级维护闭环 UpdateSubscription→ReloadCache→refreshAllSubscriptionBans；LoadBalancer 有状态策略无生产绕过）；F7 两测试断言有效。
- taskprofile sink 对照 logs_omit_body 纪律（结构上不触 Header/Body；detail 全固定键；panic 隔离在 emitAudit 侧）。
- lite_retention_worker SQL 全参数化、单位一致、装配守卫、重跑幂等。
- analytics uuidVariants 注入面干净（纯字符串归一、输出仅绑定参数、零拼接、tenant 占位偏移正确）。
- provider/client.go LATERAL（静态 SQL 全参数化、scope NOT NULL+CHECK、Scan 位序对齐、无双变体漂移）。
- sticky_load 并发原语（双锁无嵌套、单飞 defer+recover、closeOnce 幂等、goroutine recover、Redis ZREMRANGE+EXPIRE 自毁）。
- bandit 删除测试意图承接核实（被删断言由 TestConcurrencyScore_ReadsDispatchLiveLoad 承接——系存量测试，R46"新测试"表述不完全精确但承接成立）。
- F8⑦ 其余达标；窗口其余改动面抽查（admin 读面统一、session_turns_v2 JOIN 正则守卫、affinity NOT EXISTS 静态 SQL）。

## 三、未覆盖项
fakeStoreForBans 逐行；-race/EXPLAIN/冒烟复跑（R46 已留档）；refreshSubscriptionBans 单订阅路径不刷优先级（全部写路径经 ReloadCache 覆盖，直改 DB 属运维纪律）。
