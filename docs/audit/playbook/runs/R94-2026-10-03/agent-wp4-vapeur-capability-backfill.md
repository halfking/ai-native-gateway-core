# WP-4 vapeur能力回填 子代理报告（窗口 a0da9066d..HEAD）

窗口内两个目标提交均确认：`ece68f148`（回填任务 + 读错误处置，10 文件 +1733）、`242114d08`（透传消 GET，12 文件 +770）。注：任务描述中的文件归属与 git 实际有出入——`credentialfpslot/node_state.go`、`capability_state_passthrough_test.go`、`router_state_passthrough_test.go`、`router.go` 的改动实际都在 `242114d08`；`responses_stream_bridge.go` 两个提交均未触碰（窗口内由 41af920d5 改动）。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | 中 | **蓝绿双实例重复探测成本×2，且未复用仓内现成的 distlock**。`Run` 无 leader 选举/租约，两实例各自 30min tick 各扫同 50 行。仓库已有 `acquireSweepDistLock`（R31 审计正是为「蓝绿双实例双跑 sweep 翻倍负载」而加），回填未用。文档遗留#5只承认「upsert 未实测、结论不撕裂」，未承认出网成本翻倍——而 kill switch 的立项理由恰恰是成本（~400 次/天/凭据，双实例即 ~800） | bg/capability_backfill.go:224-259、bg/auto_route_distlock.go:1-20、cmd/gateway/main.go:4490,4673 | 接入 `acquireSweepDistLock`（**本轮已落地**：SetDistLock + BackfillOnce 门 + main.go 两处接线 + follower 跳过钉测） |
| 2 | 中低 | **无证据绑定的探测频率反转 + batchLimit 饥饿**。无证据（5xx/网络错/401/model_not_found）→ 不写行 → `cap.id IS NULL` 恒真 → 每 30min 都到期，且 `ORDER BY cap.last_tested_at ASC NULLS FIRST` 使其永远排最前。后果①：失败绑定 48 探/天 vs 健康 4 探/天（文档§八.8 称 staleAfter=6h 是隐式成本约束，对无证据行不成立）；后果②：若「永远无证据」的绑定 > batchLimit=50，每轮恒取同一批 50 行，已过期健康行的刷新被永久饿死 | bg/capability_backfill.go:337-343（ORDER BY + LIMIT）、:415-426（无证据不写） | **本轮已落地**：进程内 attempt 退避台账（1h）+ 扫描窗口 ×4 反饥饿 + 三钉桩（退避/让位/follower），变异验证 M-A/M-B'/M-C 承重 |
| 3 | 低-中 | **探测 body 用 RawModel 而非 OutboundModel，与 node_probe 约定相反**。backfill 照抄 probeWithRetry 的 responses 分支（RawModel 作 modelField），而 node_probe.probeDirect 明确「Use the outbound name for the upstream body; fall back to raw」。对 outbound≠raw 的映射型中转（正是 vapeur 类），上游不认 raw 名 → 404 model_not_found → 无证据不写 → 该绑定永远写不进能力位且每 30min 白探（叠加发现2）。属继承既有 responses 探测路径的既有行为；dueBinding.OutboundModel 已取回但 probe 闭包未用 | bg/capability_backfill.go:118-122、bg/probe_http.go:407-408、bg/node_probe.go:2800-2806 | **本轮已落地**：默认 probe 闭包改 OutboundModel→RawModel 回退（node_probe 同款口径），注释留痕 |
| 4 | 低 | **filterHealthyNodes 注释与代码不符**。注释称「Attached on the healthy branch only」，实际挂载发生在健康判定之前 | domains/streaming/executors/router.go:1326-1341 | **本轮已订正注释** |
| 5 | 低 | **新列 supports_native_responses_known 缺 SQL 形状/契约门**：needle 列表与扫描顺序断言均不含该列，列序错位无门可拦 | provider/candidate_priority_test.go:76-93、provider/client.go:1636-1638,1899-1901 | **本轮已补**：投影 needle + Scan 序断言（known 紧跟 supports） |
| 6 | 信息 | 透传快照的微小陈旧窗口：MGET 与 executor 闸门之间写入的能力位对当前请求不可见（请求内毫秒级，下一请求即见，无错误路由） | domains/streaming/executors/executor_chat.go:452-460 | 无需行动；已由注释如实声明 |
| 7 | 信息 | GetNodeStatesBatch 从不返回 nil 元素（缺键填零值 state），nil 分支生产路径不可达；「nil=无快照」契约实际由零值 state 等价承担 | credentialfpslot/node_state.go:250-268/417-421、router.go:1352-1356 | 文档补一句即可，代码无需动 |

## 二、核实为健康的面

- **设计文档逐条核对（ece68f148）**：三条不可让步约束全部落地且有钉测——复用 singleResponsesPing、无证据一行不写（TestBackfillLeavesRowUntouchedWithoutEvidence）、流式键字面量不出现（TestBackfillNeverWritesStreamCapability）。触发时机=启动即跑 + 30min ticker；kill switch 双重重读（10 取值钉测）。幂等性=upsert ON CONFLICT + RowsAffected 校验。失败处置=单条失败 continue 不拖垮整轮。**与既有探测的关系**：全树 grep 确认 credential_model_capabilities SQL 写者只有 backfill 一个——不存在 SQL 双写打架。准入闸门 capabilityBackfillAdmit 纯函数 15 子用例 + 软删除端到端（挡在解密与出网之前）；decrypt 与 node_probe 同一纪律（IsV1Envelope 门、ciphertext 不进日志）。
- **读错误处置（问题2）**：GetSupportsResponses 错误路径=err 上抛不降级；executor 侧 resolveDurableResponsesVerdict 三态合成（ReadErr 与 Known 两半）：读错→回落 SQL+degraded=WARN；Redis 有结论→用 Redis；无结论→SQL。健康节点不误判、失活节点不误复活；负向结论只关协议腿、不碰节点健康。**并发**：NodeState 全存 Redis，写走原子 Lua；`*NodeState` 挂载后仅请求内单 goroutine 顺序访问；SupportsResponsesKnown nil 接收者安全。
- **透传优化（问题4）**：GetNodeState 是纯 GET——读侧无 TTL 续期、无 expire 调用，消掉该 GET 无副作用损失；期限判定保留 Redis TIME；挂载/消费两侧判据齐全；不 mutate 调用方切片、fail-open 不挂载、逐候选对齐均有钉测。
- **executor_chat.go / router.go 最终态（问题5）**：窗口内三提交改动区不相交，合并自洽；rescuePinnedCandidate 回捞 nil state 正确回落自读；authoritative URSM v2 路径不挂载自读，契约一致。
- **测试运行**：go test ./bg/ -run CapabilityBackfill、./credentialfpslot/、./domains/streaming/executors/ 相关全 PASS；三包全量 + provider + cmd/gateway 亦全绿。质量符合 conventions §9：判据用 go-redis hook 数 key GET/TIME 而非钉 return；fixture 为实测上游帧；多处 setup 自检。附带核实：admin 既有红灯 TestNoUnregisteredVPaddedColumnReader 在 HEAD 已转绿。
- **provider/client.go（问题7）**：新增 SupportsNativeResponsesKnown 三态投影与 RoutedNodeState（json:"-"）。import 无环；候选缓存序列化点不受影响；SELECT 列序与 Scan 序对齐（仅缺形状门，见发现#5）。

## 三、未覆盖项与原因

1. 多实例双跑实测：无第二实例环境，发现#1/#2 基于代码路径推演。
2. 饥饿场景无测试（需 >50 条无证据绑定）——本轮补的钉桩用注入接缝覆盖了同型行为。
3. SQL 写路径对真实网关数据未跑（刻意不播种，维持文档口径）。
4. responses_stream_bridge.go 两提交均未触碰，无法审。
5. TIME 搭车（2→0 往返）文档声明为单独一轮的改动面，未审。
6. 回填的每日硬预算闸门：文档遗留#3 已如实登记（kill switch 是止血阀不是预算）。
