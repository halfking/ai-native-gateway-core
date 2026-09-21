# D16 流程闭环 + E6 取证特派 子代理报告（窗口：468a1ce82..HEAD，merge 51d6147c7）

> R44 轮原文落盘（子代理只读报告，主代理已逐条亲读复核，处置见轮文档）。
> 拓扑勘定：基线 468a1ce82 位于 origin 侧线（→3e8fd1658→fde958650=R43）；本地侧 6 提交在 ffe3f9fff 一线，分叉点 9769a18c4，汇合点 51d6147c7。

## 一、发现候选（待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | **P1（E6 实锤，R43 遗留#4）** | `Client.ReplayFallback` 直接调 insertRequestLog/updateRequestLog，两函数事务内执行 final-success claim SQL，但**绕过 persistRequestLog 的 onPersisted hooks** → 置位 is_final_success=TRUE 而 sessionv2mirror hook 永远收不到终态信号 → G2 缺行。全仓库唯一"置位成功却无 hook"的结构性路径；台账 §09-15 猜测的"后台 job 直接 SQL UPDATE"定性不准（无后台 job），真凶是**回放路径** | domains/hooks/observability/telemetry/client.go:627-636（绕行点）；client.go:1659-1660、2264-2265（claim 调用）；client.go:1023-1036（被绕过的 hook 触发点） | 修复而非登记 E6（§三.3 方案） |
| 2 | P3 | 台账 151 行 `>>>>>>> origin/main` 冲突标记残留在 HEAD blob，由 merge 4476ec95b 引入并沿途携带 | docs/03-design/04-data-design/storage-observation-ledger.md:151 | 删除该行 |
| 3 | P3（口径） | 版本三文件三值一致但钉在 faa1c072/2140，未随 merge 重生成——部署期产物属预期，下次部署前勿误读 /healthz git_sha | version.json:2 等 | 无需动作，留档防误判 |

## 二、核实为健康的面（合并缝逐项共存证据）

- **A1 cmd/gateway/main.go**：Jev 接线（HEAD :4942-4946 buildAutoFallbackClassifier 在 fallback 槽）与 R43 两处注释纠偏（:439-455 STORAGE_MAX、:4997-5005 taskprofile overlay）共存；`git diff 468a1ce82..HEAD` 仅 3 个互不重叠 hunk，自动合并语义正确。
- **A2 routing_optimizer_init.go**：R43 的"optimizer 侧 store 保持只读"注释 + admin 侧 SetRecorder 装配（admin/handler.go:1364-1366）完整存活；Jev 在此文件无装配，无冲突面；wiring_guard_test 双钉桩在 HEAD 树内。
- **A3 autoroute 包**：classifier_jev.go 零漂移（git diff bcda0f26c HEAD 为空）；无重复声明/无 init；指标函数指针赋值点唯一且启动期单次幂等，槽位互斥无并发双写；env 语义互斥（gate+key 缺一即回落 LLM fallback）。
- **A4 taskprofile 面**：registry v3.1 + R43 六类 + SetRecorder + wiring_guard_test 全在；web taskProfile.ts 合并最终形态与 origin 逐字节相同（diff fde958650→51d6147c7 为空），双侧同一修复收敛无损。
- **A5 版本三文件**：同值 faa1c072/2140，字段一致。

## 三、E6 取证专节

### 1. UPDATE 点清单（全仓库 SET is_final_success 逐点分类）

| # | file:line | 语句 | 走不走 entry 管道 | hook 触发 | 调用链与频率 |
|---|---|---|---|---|---|
| 1 | client.go:2484-2498（claim SQL） | savepoint 包裹 + promoted NOT EXISTS 守卫 | **管道内**：唯一调用点 insertRequestLog:1659-1660 / updateRequestLog:2264-2265（shouldClaimFinalSuccess 门控），commit 后 persistRequestLog:1023-1036 触发 onPersisted（10 处注册 hook） | 正常触发 | 每条终态成功请求一次；每会话至多一条（部分唯一索引） |
| 2 | client.go:627-636 ReplayFallback → 直调 update(634)/insert(636) | 非裸 SQL 但等价裸 SQL 效果 | **绕过**——claim 照常执行，onPersisted 零触发 | ✗ | 触发器：POST /internal/telemetry/fallback-buffer/replay（telemetry_fallback_buffer_handler.go:148）与 admin db_degradation RecoverFile/RecoverAll（db_degradation_handlers.go:137/165，GenericRecovery write fn = ReplayFallback，main.go:5403-5409）；进 fallback 的入口 = DB 降级（persistRequestLog:1004-1008）与写失败（client.go:761-776/897-913）。台账两天各 1 行与"降级窗口积累→恢复后回放"时序吻合 |
| 3 | sql/scripts/report_duplicate_session_success.sql:97 | 手工 keeper UPDATE | 裸 SQL 管道外 | ✗ | 纯手工极低频 |
| 4 | 迁移/demote（695:95、697:77、698:354、promote 函数:56） | SET is_final_success=FALSE | 管道外 | 不适用 | 降向写，与 G2 无关 |
| 5 | client.go:1789-1823 CorrectEstimatedUsage | usage 修正裸 UPDATE（不触 is_final_success） | 管道外 | 不触发 | 台账 §09-15 嫌疑经核验排除 |

### 2. hook gate 与最小侵入接线点

- gate：sessionv2mirror/hook.go:71 `!entry.Success && !isTerminalFailure` 静默跳过；outbox 登记只发生在过 gate 之后，故"outbox 空"与两种静默情形均相容。
- onPersisted 触发点唯一（persistRequestLog:1023-1036）；ReplayFallback 在 :630-632 已反序列化出完整 RequestLogEntry（fallback 写入保留 bodies，:1052-1054 注释自证），无需行反查。
- 正常管道 claim 与 hook 是"同事务 SQL + commit 后 hook"串联，is_final_success=TRUE ⇒ 必有一次 Success=true 的 entry 流过 hook；唯一例外就是 ReplayFallback。

### 3. 方案倾向：补发触发（不登记 E6）

- 推荐修法（最小侵入，S 级）：抽出 persistRequestLog 的 hook 段为 `firePersistedHooks(entry)`，ReplayFallback 成功后调用（约 20 行）；hook 恰好一次成立——进 fallback 的 entry 此前必然没写过 DB，hooks 从未对它触发过。回放会让其余 9 处 hook 看到回放条目——这些条目此前本来就漏计，补上是修正而非双计（turns 按 request_id 幂等）。
- 工作量 ≤0.5 天含钉桩；回归测试：回放带 gw_session_id 的终态 success entry → 断言 mirror hook 收到。
- 不选登记 E6 的理由：修复面积小、定位唯一；登记例外会把"每日回填兜底"永久化，且重放器架构对该类行天然无效。E6 可作过渡期登记，修复落地后关闭。

### 4. 台账 151 行冲突标记（已确认）

- HEAD blob 第 151 行即 `>>>>>>> origin/main`（git show HEAD 实证）；引入者 merge 4476ec95b（into docs/auto-multidim-progress-rfc），标记只占 1 行、前后内容完整无丢失，删除即可。

## 四、未覆盖项与原因

- 44728e27/403f405b 两行的确切回放触发者（ring-buffer replay 端点 vs RecoverAll）仓库内无审计日志可查，需网关访问日志佐证；不排除个别行另有成因，靠修复后 GLOBAL_G2 连续归零闭环。
- dbdegradation 磁盘文件自动恢复：GenericRecovery 仅 admin HTTP 触发，未见启动期自动 Recover；仓外 cron 属部署库范围。
- classifier_v3/decision_v2 与 jev 行为面重叠属 D11 域口径。
