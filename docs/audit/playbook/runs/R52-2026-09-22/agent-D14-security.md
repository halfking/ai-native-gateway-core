# D14 安全场景横切 子代理报告（窗口：119981c02..5dc4e9f3b）

> 主代理复核结论（2026-09-22）：#1 成立已修（R52-F2：COALESCE($35,0) + 对齐守卫语义断言）；#2 与 D16-#1 同根已修（R52-F3）；#3(a) 经 CachedPlatformString 失效接线后维持 fail-open 语义、(b) 登记顺延（unknown 桶运营可见性）；#4 登记顺延（NFKC/零宽剥离为后续专项，属订阅源受信边界问题）；#5 已修（R52-F15）；#6 已修（R52-F16：DangerLevel 升 Dangerous）；#7 已修（R52-F10：normalizeAgentName 截断 255 rune + 剥控制字符；Normalize 白名单会塌缩合法自定义名，刻意不用，基数治理登记后续）；#8 留档（提交信息失真）；#9 与 D04-#2 同根已修（R52-F5：recover 分支删 running[key] + 行为钉桩）。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P1 | F32 修复后 ingest INSERT 的 COALESCE 错位：NOT NULL 列 stream_chunks_sent 绑裸 $35，nil 即 23502 双行丢失（stream_chunk_count 可空套了 COALESCE($32,0)，NOT NULL 列反而裸绑）；触发路径：POST /api/telemetry/request-log body 省略 stream_chunks_sent → request_logs_hot 插入 23502 → return → defer tx.Rollback 把同事务已插的 usage_ledger 计费行一并回滚。F32 让该 INSERT 自 2026-07 以来首次真正执行，雷首次变活；telemetry 包 Lite sink 已有 streamChunksSentArg nil→0 收口并点名 HTTP path，admin 侧没同步 | admin/telemetry.go:370-385（slot33/36）、354-369（列序）、389-402（args）、404-408（失败 return）；对照 client.go:2734-2747；钉桩缺口：对齐测试只查数量不查语义映射 | COALESCE 移到 slot36；对齐测试补列↔占位语义断言 |
| 2 | P2 | selectNode 持 selectionMu 做 DB IO（D14 §3.1 违例）：overlay 读在锁内，settings DB 抖动时所有节点选择串行排队最长 5s/次 | proxy/manager.go:354-365、445-458 | 锁外快照或 ttl_cache 族 |
| 3 | P2 | 地理围栏 fail-open 双缝：(a) overlay 读 DB 出错 → GetPlatformString 回落 "" → overlay 静默失效无告警；(b) IsRegionBanned 对 Location=="" 恒放行，规避命名残余变体全落进放行桶 | proxy/types.go:244-256、219-231；helpers.go:111-113；manager_region_test.go:15 | (a) Warn+计数；(b) unknown 桶运营可见性 |
| 4 | P2 | Phrases 归一化残余绕过面（节点名来自外部订阅=非受信）：全角拉丁/零宽字符/缩写 H.K./变音符/多语言/西里尔同形字 → Location="" → 放行。R51 只收口 "Hong Kong" 相邻词组 | proxy/parser.go:546-579,581-625 | NFKD/NFKC 归一 + 剥零宽变音；或 guessLocation 失败出 Warn |
| 5 | P3 | RegionStatsReport 的 Banned 统计漏掉 overlay（同 D16-#4） | proxy/manager.go:1990 | 并入 overlay（锁外读） |
| 6 | P3 | proxy.default_banned_regions DangerLevel=Warning → 普通管理员可整体关闭平台级地理围栏（CategorySecurity 安全开关仅 Warning 档） | settings/spec_proxy.go:17；admin/settings.go:236-239 | 升 Dangerous |
| 7 | P3 | X-Agent-Name 客户端可控原样透传未截断：>255 字节 → 22001 整行 INSERT 失败；stats_minute_rollup 按 agent_name 聚合 dim_key（text 无界）→ 维度基数膨胀存储 DoS 面 | telemetry/request_metadata.go:340,359-361；459_request_logs_view_client_perception.sql:35；bg/stats_minute_rollup.go:216-224 | 过 Normalize + 截断；rollup dim_key 归一 |
| 8 | P3 | 5dc4e9f3b 提交信息与 diff 漂移（同 D01-#7） | git show 5dc4e9f3b | 轮文档登记 |
| 9 | P3 | active_probe_worker panic 半更新（六 worker 唯一非幂等者）：panic 时 running[key] 永不 resolve，Submit dedup 使该 (cred,model) 探测对直到进程重启不再被处理——逐 tick recover 把"崩溃"变"卡死"；其余五 worker 逐 tick 重查 DB 幂等；selfcheck advisory unlock 走 defer 安全 | bg/active_probe_worker.go:259-266、283-296、138-144、167,408,447；对照 credential_selfcheck.go:214-243 | recover 分支补 markFailedRetry 或 delete |

补充（主代理复核用）：/api/telemetry/* 三端点均 h.admin 包裹（admin/handler.go:1189-1191）SQL 全参数化；tenant_id 为调用方自报（admin 鉴权下可写任意 tenant 的 usage_ledger/cost 归因行）——属既有 ingest 契约，admin_key 外发范围大时值得登记。

## 二、核实为健康的面

- ingest 37 列 INSERT 参数化完整（数量/占位唯一性由 F32 对齐钉桩守护，仅语义映射错位即 #1）；decision_log INSERT 与 fallback 直写全参数化。
- S4 停写门接线正确同键同默认；usage_ledger 先于 gate 插入为设计意图；门控测试 4 件在位。
- settings HotReload 无竞态（Global 无内存缓存，直读 DB 由头注释钉死）；settingsPut 有 Validate+审计+DangerLevel 权限门；F22 5s 超时堵死无限阻塞。
- overlay 默认值 fail-closed（Spec.Default="HK"，无 DB 行/无 env 也禁 HK）。
- body_client_marker 溢出面：std json 10000 层深度守卫；识别结果收敛 Normalize 白名单（13 合法值），label 基数有界。
- bg 六 worker recover 收口模式一致；node_probe 退避封顶修正为真 min；worker_panic_recover_test.go 钉桩在位（4/6 覆盖，#5/D16-#5 补齐）。
- promote 每表保底 + 幽灵表清理正确（外层遍历全 spec 不提前 break）。
- 注入面：窗口新增 SQL 全参数化；stats_minute_rollup dimKey 拼接源为服务端白名单。

## 三、未覆盖项与原因

- paramledger F1/F12（响应改体正确性）转 D16 覆盖；discovery/autoroute F6/F7/F10 路由正确性未按 D14 清单取样；RLS/GUC 活库行为、Redis ZSET 内容需真机凭据。
- client_type 伪装业务后果分级：落点有界（观测归因+rollup 维度）；若存在按 agent_name 做路由/计费的消费者则 #7 应升 P2——主代理确认无此类消费者。
- discovery.go、autoroute/decision_v2.go、storage/sqlite/turns_store.go、web QueuePerspectivePanel 超出 D14 六个指定安全面。
