# R91 域 B 报告：reorder 4 红定位 + dispatch 新代码深审

HEAD=ad34b2bc5，2026-10-02。所有引用均来自本次实际读取/实跑；仓库零修改，临时库已 DROP。

## 一、发现（候选，待主代理复核）

**B-#1（P1）redis_enforce 续约租约会被 acquire 滑窗剪除——健康长流被误判 "lease lost" 而 fail-closed 中止**（条件缺陷：仅 `LLM_GATEWAY_DISPATCH_GOVERNOR_BACKEND=redis_enforce` 时生效，默认 local 故休眠）
- 证据链：`domains/dispatch/redis_backend.go:113-117`（renew Lua 用 `ZSCORE` 取回旧 score 后 `ZADD key score token`——**score 永远冻结在首次 acquire 时刻**，只靠 `PEXPIRE` 续整个 key 的 TTL）；而 `redis_backend.go:75-77`（acquire Lua 每次执行 `ZREMRANGEBYSCORE key -inf cutoff`，`cutoff = TIME[1]*1000 - ttl_ms`，按 member score 剪除"过期"成员）。两者组合：任何运行超过一个 TTL 的流，只要同 key 上来任何一个新 Acquire，其 token 即被剪除；下一次 Renew 走 `redis_backend.go:635-637` 返回 `ErrLeaseLost` 双重包装 → `domains/dispatch/lease_renewer.go:88-95` abort 健康流。这正是本次接线要解决的问题本身（lease_renewer.go:6-9 自述），修完反而把"静默漏计"变成"确定性杀流"（LLM 流 >30s 默认 TTL 极常见）。
- 实证（一次性 miniredis 探针，/tmp/audit31_renewprobe，两段 Lua 逐字复制自上述行）：ttl=1s、每 333ms Renew 共 7 次全部成功（`renewals_succeeded_before_B=7`），t=2331ms 同 key 新 Acquire 直接准入（limit=1，证明旧 token 已被剪），下一次 Renew 返回 0（`final_renew=0` → ErrLeaseLost）。复现确定性 100%。
- 现有钉测为什么没拦住：`redis_backend_integration_test.go:144-201`（TestRedisEnforceLongStreamRenewal）用 `mr.FastForward`（只减 TTL、不推进脚本 TIME，已读 miniredis v2.38.0 `miniredis.go:363-371`、`cmd_server.go:138-153` 确认），acquire→probe 实际墙钟仅 ~2ms，永不超过一个 TTL，从不构造"续约中的流 + 并发 Acquire"场景。
- 候选修法：renew Lua 把 score 刷成当前时间（与 acquire cutoff 同钟一致），代价：改 `redisEnforceRenewLua` + 补一条"续约跨 TTL 后并发 Acquire 不剪除"钉测。
- 【主代理处置 2026-10-02】已修并升级：首版修法（`TIME[1]*1000` 秒粒度）被新钉测抓出亚秒盲区（170ms 窗口内 score 根本不推进），二修为全毫秒时钟（与 redisRateAcquireLua 同形）；钉测 TestRedisEnforceRenewAdvancesScoreAgainstPruning 改跨秒循环形态，变异 M1 红/还原绿。见轮文档 §二#1。

**B-#2（P2，测试缺陷——推翻第三十轮 §四#3"共享库状态依赖"结论）4 个 reorder 集成测试红是构造性红，与共享库状态无关**
- 实证：用 `pg_dump --schema-only` 把共享 dev 库（llm_gateway）结构复刻到随机名一次性库（零数据），`LLM_GATEWAY_PG_URL` 指向它实跑：4/4 全红，失败模式与共享库完全一致（Happy: `expected at least one audit row`；其余三个：`initial revision = "2:...", want prefix "1:"`）。
- 根因 2a（IntegrationHappy，`admin/routing_candidate_binding_test.go:517`）：断言 `after_json ? $1` 用 jsonb **key 存在**操作符检查 rawModel，但 `admin/routing.go:1540-1548` 写入的 after_json 顶层 key 是 `{scope, expected_revision, scope_version, items, raw_model}`，rawModel 是 **value**——谓词恒假。共享库实证：`routing_audit_log` 里 9 条历史 reorder 审计行全部在（action/after_json 落库正常），`after_json ? (after_json->>'raw_model')` 全为 false。**生产写路径健康，测试谓词错**。修法：改 `after_json->>'raw_model' = $1`。
- 根因 2b（其余三个，`admin/routing_scope_revision_test.go:115/163/202`）：fixture（`routing_candidate_binding_test.go:361-379`）对每个 credential 单独发一条 binding INSERT；migration 541 的触发器是 **statement-level**（`sql/migrations/startup/541_candidate_binding_scope_revision.sql:257-273` `FOR EACH STATEMENT`），2 条 INSERT = 初始 version 2，后续期望全部偏 1。修法（推荐 1）：fixture 合并为单条多行 INSERT → 初始 version=1，三个测试算术不动。
- 附带更正：`bg/audit_trimmer.go`（90 天 TTL、24h tick）与 fixture cleanup 都不是清理者；routing_audit_log 无 RLS、无触发器（共享库实测 `relrowsecurity=false`、0 trigger）。第三十轮"audit 行被清/并发会话互踩"猜测不成立。
- 溯源：4 个测试分别诞生于 bc788bfac（2026-08-19）与 770e853a3（同日，541 落地），断言与 541 语句级触发器语义自始不匹配，属出生即红（此前各轮仅在设了 LLM_GATEWAY_PG_URL 时才暴露）。
- 【主代理处置 2026-10-02】按推荐法 1+2a 已修，共享库 4/4 绿 ×3 连跑。见轮文档 §二#3。

**B-#3（P3）"ErrLeaseLost 双重包装"契约没有真 governor 钉测，钉测名不符实**
- `domains/dispatch/lease_renewer_test.go:206-216`（TestRenewErrorContractOnRealGovernor）实际只断言测试文件自己构造的合成错误，从未调用 `redisEnforceGovernor.Renew`；真 governor 侧两条集成钉测（`redis_backend_integration_test.go:229-235`、`:254-260`）只断言 `ErrGovernorUnavailable`，全仓无任何测试对真 Renew 输出断言 `errors.Is(err, ErrLeaseLost)`。变异推演：若 redis_backend.go:619/636 回归为只包 ErrGovernorUnavailable，全套测试依然绿，循环把确定丢失当瞬态（多熬一个 TTL 才 abort）。
- 【主代理处置 2026-10-02】已在两条真 governor 集成钉测各补 `errors.Is(err, ErrLeaseLost)` 断言。

**B-#4（P3）确定丢失 abort 在 pre-first-byte 场景同样终结整个请求，且下游把 governor 故障记成"客户端取消"**
- abort 取消 fwdCtx（lease_renewer.go:94）→ forwardFunc 返回 `context.Canceled`（forwarder.go:546）→ 未发字节 → `routeFailover`（forwarder.go:570-572）→ mover 的 `PlanAfterFailure` 把 canceled 判为终态（`domains/dispatch/planner.go:239-240` `ActionClientCanceled → NextActionFailed`），不切健康兄弟凭据；观测面同样记为客户端取消（`pipeline.go:1819-1822`）。文件头注释（lease_renewer.go:22-25）只论证了 post-first-byte 终态，pre-first-byte 的"损失一次可救请求"是未言明的取舍。是否要修属产品裁决；最低价修法是 abort 时给 outcome 打专用 ErrorKind（如 `lease_lost`）使其可路由/可观测，而非裸 ctx.Canceled。
- 【主代理处置 2026-10-02】登记不修（产品裁决面）。

**B-#5（P3，杂项）**
- release 指标硬编码 `"ready"` 且失败不区分（forwarder.go:537 + redis_backend.go:590-592 错误静默）——release_total 恒 ready，语义弱；`forwarder.go:431`、`:478` 两条 Release 路径完全无 release 指标（少计）。无基数风险。
- observer 默认翻转（`cmd/gateway/main_dispatch_backend.go:153-160`）不只影响指标：它同时激活 `capacity_aware_sort.go:104` 消费的 `SnapshotForCred` 缓存数据面（off 时 fail-open 为 no-op，`governor_snapshot_observer.go:43-54`）——默认 observe 后 soft-penalty 排序从"永不生效"变"按真实快照降级"，是路由行为变化，建议在变更通告中显式说明。另 `governor_snapshot_observer.go:5-7` 头注释仍写 "OPT-IN"，已过期。
- 【主代理处置 2026-10-02】注释已订正（写入默认 observe + 路由行为面提示）；release 指标弱登记不修。

## 二、核实为健康的面

1. **续约循环并发安全与停止顺序**：`forwarder.go:522-544` 每 attempt 派生 fwdCtx；`releaseOnce` 内先 `stopLeaseRenewer()`（close(stopCh) 后阻塞等 goroutine 退出）再 `gov.Release`——"停止严格先于释放"成立；renew goroutine 退出先于 `cf.wg.Done`；panic 路径经 defers 亦安全。`-race -count=3` 下 9 条钉测全绿。
2. **TTL fail-closed 计时边界**：`lease_renewer.go:69,96-113`。abort 阈值 = 首次瞬态失败后 3×interval；服务端租约必然先于 abort 到期（无误杀提前量）；成功即重置 degradedAt；时钟为 Go 单调钟。无 off-by-one。
3. **双重包装生产侧透传**：redis_backend.go:619-620 与 :636-637 均为 `%w ErrLeaseLost + %w ErrGovernorUnavailable`，瞬态只包 ErrGovernorUnavailable；`errors.Is` 链路全兼容；Renew 错误的唯一消费者就是续约循环。
4. **governor closed-enum 映射无基数风险**：`acquireMetricResult`（forwarder.go:370-379）只产 ready/saturated/unknown，`RecordRelease` 只发 "ready"，全部落在白名单；越界走 warn+drop 计数。注：Lua `invalid_token` 结果在 forwarder 接线中不可达（被映射为 unknown），属覆盖空缺非基数风险。
5. **observer off 路径真无副作用**："off"→不 `SetGovernorSnapshotObserver`→nil 字段不启 goroutine，缓存不写、快照查询 fail-open。
6. **9 条钉测整体承重**（mutation 思维推演）：删 abort 接线/TTL 窗口/degradedAt 重置/stop-before-release/误启循环均有对应红。唯一不承重点即 B-#3 的合成错误契约钉（本轮已补）。
7. **其余窗口改动**：`admin/proxy.go:916-1010` GET/PUT 回带 selection_advisory 纯标注；`internal/ir/serialize_openai.go:733-746` 新增 `case "function"` 与解析对称、新钉测充分；`bg/scan_scheduler.go`、`discovery_engine.go` 为纯注释标注。
8. **门禁复跑**：`go build`/`go vet`（dispatch/cmd/admin/ir）零输出；`go test ./domains/dispatch -count=1` 绿；9 钉测 `-race -count=3` 绿。

## 三、未覆盖项与原因

1. **redis_enforce 在真实部署是否已开启未核实**：B-#1 的 P1 定级以"该模式开启即触发"为前提；本机只能确认 env 默认是 local。生产 env 不在审计范围。
2. **共享库历史 9 条 reorder 审计行的写入会话归属**未深挖；对结论无影响。
3. **`TestRedisEnforceLongStreamRenewal` 的理论性跨秒边界翻红**（score 秒粒度 + probe ttl 参数传了 `spec.Limit`）：实测 55 连绿（FastForward 不推进墙钟），未列为缺陷，仅并入 B-#1 的"钉测不覆盖跨 TTL 场景"。（主代理注：B-#1 修复后 renew score 已全毫秒推进，该理论面同步消解。）
4. migration 569/571/578 与 fixture 交互只做读码级确认。
5. `fakeLeaseGovernor` 与真 governor 的 Renew 语义差异只影响测试保真度。
6. 第三十一轮其他会话在共享库上的并发活动仅记录存在。
