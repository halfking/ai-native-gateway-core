# R93 域D 报告：s4-audit 会话修复提交独立复审（第三十三轮）

审计人：分域子代理 D（s4fixes 独立复审）。HEAD=f609ecab1，窗口 a0da9066d..HEAD。全程只读。

**方法**：提交信息不可信，逐笔直读 diff + 直读现行代码验证机制；钉测不仅看存在性还逐条判断断言是否恒真/空转；提交声称的变异验证在 `git archive HEAD` 出的 /tmp 副本上**独立重做**（未触碰仓库工作区）；关键门实跑（含 `-race`、`-tags integration`）。
**方法披露**：M-P2E 换序变异因脚本路径失误在真实仓库 `installer/internal/dbinit/runner.go` 上执行了一次，当分钟内 `git checkout --` 完全还原（`git status` 复核无残留），变异红观测在还原前取得，证据有效。

## 一、逐提交判定

### 38497afed D03-①+D02+D01 —— **成立（窗口内最大笔，四问逐项核）**

**a) Recover persist 与主动路径同源同参：成立。**
- `domains/hooks/compression/recovery_coordinator.go:273-291`：`GetOrLoad`→`buildSessionState(prev, rebuilt, persistRes, true, now)`→`hydrateSanitizeInfo`→`SetCutMarker`→`Set`，与主动路径 `session_compressor.go:942`（updateCache）、`:1008`（CommitFinal）同一条工厂函数、同一参数形状。身份字段（LastOutboundHash/MsgCount/TokenEstimate/CompressedSnapshot/CompressedMsgs/CompressedTokens，buildSessionState `session_compressor.go:1016-1055`）全部由 rebuilt 派生；`CompressionSourceSnapshot=SnapshotForBody(body)`（进压缩的原 4xx 体）与 Prepare 主路径语义一致；CompressedPrefixHash 与 CommitFinal 同款 `prefix.Stabilize(TailTurns:1)`（recovery_coordinator.go:284 vs session_compressor.go:1005）。strategy 不入 SessionState 本体，经 `SetCutMarker`→`CutStrategy`（session_cache.go:919）落盘，新旧路径一致。
- didCompress=true 与旧代码手工两时间戳（LastCompressedAt/RecentlyCompressedAt=now）等价，由 buildSessionState:1039-1042 统一落。
- 差异点（观察，无碍 diff 守卫）：persistRes 不带 RawSnapshot/AlignmentMap，故这两族按 buildSessionState 的 copy-forward 语义继承 prev（主动路径是新值覆盖）；diff 守卫读的 LastOutboundHash/MsgCount 均为 rebuilt 派生，不受影响。

**b) 摘要打标对旧缓存体的向后兼容：成立。**
- `cachedSummaryText`（recovery_coordinator.go:696-715）第一腿 `startsWithPrefix(content, smartWindowSummaryPrefix)`→strip 前缀，与改动前代码逐字等价——无 marker 行的老 L1 体行为不变；marker 腿是纯新增。
- 层二机制成立：`findDeltaAnchor`（diff.go:195-227）只在 `isSummaryMarkerMsg`（diff.go:367，要求内容以 `CompactionMarkerPrefix="[smm_v1:"` 开头，session_cache.go:96）判真时走"排除摘要+唯一连续子序列"分支；无标记摘要消息必然锚点失配 fail-open——打标的必要性在代码层证实。
- marker 口径：`CutMarker.SummaryMarker` 改为 injectSummaryMarker 返回的 content 派生串（= `BuildSummaryMarker(smartWindowSummaryPrefix+summary)`），与体内 marker 行同串——否则下一轮 buildSessionState:1045-1050 的 `bytesContainSummaryMarker` 校验会把它清掉。该口径被更新后的 `TestRecoveryCoordinator_LLMSummary_EmitsSmmMarker` 钉住。
- 机械分支不打标 + `TestRecoveryCoordinator_MechanicalFallback_NoSmmMarker` 双趟断言在场（recovery_coordinator_test.go:305），154 号 marker-churn 回归面守住。

**c) 钉测真存在且断言承重：成立（全部逐条读过，无一恒真）。**
- `TestRecoveryCoordinator_PersistedStateMatchesRebuiltBody`（recovery_prepare_delta_test.go:31-107）：llm+mechanical 双分支，7 组断言（L1 体相等/hash/count/token/compressed 族/marker 一致/marker 在体内）全部非空转。
- `TestRecoveryCoordinator_NextPrepareDeltaAppendsOntoRecoveredBody`（:121-176）：Recover→Prepare 端到端，断言 `delta_append`、MsgCount=rebuilt+1、新轮在、摘要在、**"unique turn 00" 漏回负断言**。
- loss 四条（internal/ir/serialize_reasoning_loss_pin_test.go）：responses budget 正例 + effort-only 负例 + ollama 四字段正例 + 无 reasoning 负例；夹具经 `resetDedupAndInstall`（anomaly_test.go:66）清 dedup 后真捕获。
- PSOR 双向（cache_v2_test.go:775-807）：typed-nil→键不存在；`[]int{1,3}`→键值原样。
- **变异独立复现（/tmp 副本，非声称转述）**：
  - 变异①（把 recovery_coordinator.go 还原为父提交 38497afed~1 形态）→ 状态钉测双分支红（`LastOutboundHash is stale`）+ delta 钉测红（`strategy="" outbound_nil=true`）——**审计症状精确复现，与提交声称一致**。
  - 变异②（保留 buildSessionState 状态同步、仅删 injectSummaryMarker 块）→ 状态钉测绿 + delta 钉测红（锚点失配 fail-open）——与提交声称一致。
  - 还原 HEAD 形态 → compression / session/v2 / internal/ir 三包全绿。

**d) D01 typed-nil PSOR：修复方向正确、实际影响核实。**
- 消费陷阱在代码层证实：`validatePersistedProvenance`（recovery_coordinator.go:426）`if psor := meta["pre_sanitize_offset_range"]; psor != nil` 对 interface 装箱的 typed-nil `[]int` 判真→`metadataIntPair` 失败→整体 false→`recoverFromV2Metadata` 弃用。条件导出（cache_v2.go:404-414，nil 不写键）后键缺失走"无 PSOR 亦可"路径，V2 冷启动增量恢复恢复可用。其余 PSOR 读取方（session_compressor.go:1442/1447 的 `intPairMeta`）为 presence-lookup，键缺失兼容，与文档声称一致。
- D02 接线声称核实：`SerializeResponsesRequest` 生产 caller 在场——executor_chat.go:1338 → `chatBodyToResponsesBody`（responses_mode_bridge.go:149,160）→ ir 序列化，注释反写属实；`ParseOpenAI` 只产 `Reasoning.Effort` 不产 BudgetTokens（parse_openai.go:137-139）→"桥路径零噪声"成立。Ollama 序列化器本体确不消费 req.Thinking/req.Reasoning（serialize_ollama.go 全文 grep 仅新报告函数引用），五臂上报即生产行为。

**残留（P3-1，见发现清单）**：Phase-0 incremental_cache / recoverFromV2Metadata 复用路径的 `IncrementalBuild` 重建摘要为 `smartWindowSummaryPrefix + SummaryText`，**不带 [smm_v1:] 行**（cut_marker.go:379-381）；二次 4xx 经此路径恢复成功后，handler 的 `CommitFinal`（handler.go:4195，body=无标记增量体）会经 buildSessionState:1045-1050 清掉 state.SummaryMarker → 下一轮 Prepare fail-open 一轮。同族残留、fail-open 安全方向、非本轮回归。

### e474c6331 §9.59 值层对账门补 v1 _hot 面 —— **主体成立；自检判据不成立（P2-1）**

- 主体修复为真：`TestDualWriteValueParity` 与模型差异臂均补 `UNION ALL ... FROM public.request_logs_hot`（dual_write_value_parity_integration_test.go:112-121/:195-198），配对面从父表扩到两面；判红条件真实存在（success 零容忍 Errorf + 0.05% 阈值 + 模型归一化 2% 阈值，:168-:235）。表/列在迁移中核实（request_logs_hot 见 526/602 族，is_auto_request 等列在 816/739 族）。
- 真库执行条件判定：`//go:build integration` + `TEST_PG_URL` 才执行；两门 skip 文案自明且不冒领证据（"TEST_PG_URL unset —— 跳过不构成…证据" / "库里没有…本门不构成证据"）；本机 `-tags integration` 实跑 SKIP 干净、vet 通过。252 执行数字（配对 12,798 / 全集 16,550 / 内部 3,722）与 §9.59 文档（2026-09-30-session-request-data-re-audit.md:8030-8127）自洽；**本机不可达 252，无法独立复核执行本身**，此点如实登记。
- **P2-1（发现）：`TestDualWriteParityCoverageReport` 的"唯一会判红的自检"是恒等式。** `total == parentFace+hotFace`（:316-321）中 src 只取 CTE 两个臂的字面量 `'parent'/'hot'`，三数同源于同一 j 行集（s 按 request_id GROUP BY 去重，LEFT JOIN 不放大）→ 恒等成立，**与 CTE 内含不含 _hot 臂无关**：删掉 _hot 臂后 hotFace=0、total=parentFace，等式依旧成立、门仍绿。注释与提交信息声称的"若有人再把 request_logs_hot 从 v 里删掉……而这一条会（红）"不成立——这正是"声称承重实际恒真"形态。有效写法应为 `hotFace > 0`（且 `parentFace > 0`）类断言。缓解：覆盖率各格数字仍逐行 Log 打出（人工可见），且主体值层门不受影响。另注：同一提交因"自指护栏=装饰"删除了另一道护栏，此为同型错误再犯。

### 0bda13c41 P2-A detached 流租约中止 + P2-C lastArmed 三钉测 —— **P2-A 成立；P2-C 钉测承重但带数据竞争打红 CI race 门（P1-1）**

- P2-A 全链核实成立：载体 `dispatch.WithDetachedStreamAbort`（detached_abort.go:25-35）→ forwarder 另铸 `WithCancel(WithoutCancel(fwdCtx))` 并钉值（forwarder.go:541-550，renewer 中止回调双 cancel）→ executor `paramsCopy.R = paramsCopy.R.WithContext(dispatchContexts[0])`（executor_dispatch.go:694，fwdCtx 即 params.R.Context()）→ `upstreamContext` 取值并回 `abortAwareContext`（executor_chat.go:2727-2781）。abortAwareContext 并发面正确：done 单次关闭、err atomic.Value、base.Done 兜底回退 base.Err()、watcher 生命周期被 base 的 2h 上限+调用方 cancel 封死。钉测双臂真：客户端断开免疫（80ms 负窗）+ 租约中止可达且 Err 非 DeadlineExceeded；`TestUpstreamContext_NoAbortStampKeepsLegacyDetachedBehavior` 钉旧路径不回归。**变异 M-P2A（撤并回臂）在 /tmp 副本独立复现红**。
- P2-C：`credForwarder.now` 时钟缝（forwarder.go:44-55）+ 三钉测的时钟数学逐拍核对成立（T1 入口武装+整 TTL 门；T2 成功重置锚隔离 M1 冻结；T3 用 (S+TTL,F+TTL)=(T0+27,T0+35) 窗内推进隔离 M2 首失败锚）——设计承重。
- **P1-1（发现）：钉测引入数据竞争，打红仓库既有 race 门。** `g.renewErr` 由测试 goroutine 写（lease_renewer_lastarmed_test.go:149、:174），循环 goroutine 在 `Renew` 无锁读（lease_renewer_test.go:53），两 goroutine 间无 happens-before 边（renewedCh 只单向 loop→test）。实跑 `go test -race ./domains/dispatch -run TestLastArmed` **2/2 复现 DATA RACE**（TSan 报告指向上述三处）。`make test-race-core` = `go test -race ./internal/ir ./domains/dispatch ./domains/streaming/...`（Makefile:34,70）由 CI `sessionforensics-ci.yml:264`（push/PR main）执行——**该提交起此门必红**；窗口内无后续提交修复。修法一行：Renew 的 renewErr 读写过 `g.mu`（或 atomic）。次要观察（P3-5）：相邻测试语句间存在 5ms tick 缝，理论上有极小概率多一拍成功使 T3 误红（未观测到）。

### 41af920d5 P2-B/P2-E/P2-F（P2-D 在 fe40c0802 配套）—— **成立**

- P2-B：机制链完整核实——终止帧 `frameUsage` 以 chat 形状透传 usage（responses_stream_bridge.go:109-245）→ relay 按 `ir.ParseOpenAIStreamChunk` 解析（stream.go:1040 附近）→ `capture.ObserveChunk` 落 promptTokens/completionTokens（hooks/audit/stream.go:96-118）→ 记账。修复前 `_ = json.Unmarshal` 解析后弃用+终止帧无 usage，记账恒 0 属实。钉测末帧三值+首帧无 usage；**变异 M-P2B（usage 置 nil）独立复现红**（"no terminal frame carries usage"）。
- P2-D：`partition_date >= (now()-24h)::date` 语义保持成立——session_turns `partition_date DATE NOT NULL DEFAULT CURRENT_DATE`、`PARTITION BY RANGE (partition_date)`（430_sessions_v2_schema.sql:163,175），默认写侧 write-date ≥ (now()-24h)::date 由 ts 谓词蕴含；session_turns_hot 为普通表无分区（526_session_turns_hot.sql，仅 ts/tenant 索引）不受影响。EXPLAIN 叶子数（7→4、SeqScan 6→3）需真库，本机不可复核，如实登记。
- P2-E：`TestHandoffFamilyPrerequisitesRegistered` 读 `dbinit.Runner.StartupFiles` 实序（runner.go:162/:180，517 后 527），缺失 Fatalf + 换序 Errorf；**换序变异红已实证**（"527(pos 20) must come after 517(pos 21)"）。installer 子模块测试实跑绿。
- P2-F：815 SQL 实况核实——第一 DO 块 4 处守卫 `RETURN`（815_request_logs_view_stage_band_cff.sql:63/:82/:96/:105），其后**第二个独立 DO 块无条件执行**且 `cnt <> 118` 即 `RAISE EXCEPTION`（:332-347）→ 旧注释"skips when the 680-incident wrapper shape is absent"确实失实，runner.go:648-658 订正为 fail-closed 实况，"SQL 本体留 Owner、登记不修"处置如实。

### 830f2f221 integration 标签树编译缝根修 —— **成立（两树同源实证）**

- 移动符号为纯 Go 字面量表（admin/request_logs_v1_direct_tables_test.go:19-23），无真库依赖；原 `!integration` 文件改为引用说明。
- **两树同源实证**：`go vet -tags=integration ./admin/` rc=0；`go vet ./admin/` rc=0；`TestIntegrationTaggedTreeCompiles` 实跑 **PASS（type-checking 370 packages）**——该门对全仓 integration 标签树做含测试文件的类型检查，HEAD 无任何跨标签 undefined 引用残留，即"integration 树还引用别处无标签符号"的答案为否。
- 消费方测试绿：TestNoUnregisteredVPaddedColumnReader、TestNoVPaddedColumnReaderRemains、TestIndirectRequestLogsReadersAreDeclaredWell PASS。

### 91466ce63 docs P3 批 —— **成立**

- settle 常量核对：`settleInterval = 5 * time.Minute` / `settleBatchSize = 500`（bg/auto_route_settle_worker.go:44/:51），三处"30 秒/100 行"文案订正与代码一致；门测试只断言 SQL 字面量，文案订正不伤门。
- 索引审计 9→6：订正名单（journey_recent/journey_model_recent/journey_node_recent/journey_retry_at/tenant_request/uq_tenant_request_seq）与文档 §3 索引表（2026-10-02-request-state-transitions-indexes.md）自洽；与 R92 域D 已实证的 attnum 6/11 结论一致。
- retention.go:84 头注释订正与 R92 域D P3-1 发现（实际 pkey 首列 Index Only Scan）一致。

### docs/merge/build_seq 批 —— **相符**

- 9fb513312：新增 docs/12小时内修订审计-20261003-0015.md；其 §一 三项修复声明与代码实况逐条独立验证相符（含两层根修、死函数实况反写、typed-nil 双向钉测），§二 变异声明经本复审独立复现属实。§9.59 小笔误（见 P3-4）。
- 76b5106d4：VERSION/version.json → `2.5.8-bb3d1343-20261002-2399`、build_seq 2399，diff 与信息一致。
- fccbf08e4：四处订正逐条在场——§9.61 守卫表第三列"字符类 [0-9a-fA-F:.] 合法？"+订正块、§9.62.4 与 §9.63 标题"不需要任何迁移"（:6517/:7025）、§9.60 节号说明（:5701-5702）。
- e9abad10f：R32 轮文档（docs/24h审计第三十二轮-20261002.md）+ runs/R92 四域归档在场；轮文档 §二 对 P2-A/P2-C 的描述与代码一致（其"钉测保持绿"清单按无 race 口径成立，race 门问题为本轮新发现）。
- b58b42b8a / dbc2e760d / e9488ab7b / 57f65f7c6：纯合并；冲突消解面（executor_chat.go、installer/internal/dbinit/runner.go）在 HEAD 已由上述测试/vet/compile 门间接验证收敛正确。

## 二、发现清单

- **P1-1** 0bda13c41 P2-C 钉测数据竞争：`g.renewErr` 测试 goroutine 写（domains/dispatch/lease_renewer_lastarmed_test.go:149/:174）vs 循环 goroutine 无锁读（domains/dispatch/lease_renewer_test.go:53），无 happens-before 边。`go test -race ./domains/dispatch -run TestLastArmed` 2/2 翻红；CI `sessionforensics-ci.yml:264 → make test-race-core`（含 ./domains/dispatch）自该提交起必红，窗口内未修。建议：Renew 内 renewErr 读写过 g.mu 或改 atomic。
- **P2-1** e474c6331 `TestDualWriteParityCoverageReport` 自检恒等式：`total == parentFace+hotFace` 由 CTE 字面量构造保证恒真（src 仅两值、s 去重不放大行集），删除 _hot 臂后依旧绿——注释/提交声称的"防取样面被静默改窄"判红不存在。建议改为 `hotFace == 0 → Fatalf`（可附 parentFace 同断言）。
- **P3-1** D03-① 残留：二次 4xx 走 Phase-0 incremental_cache / recoverFromV2Metadata 时 `IncrementalBuild` 产物摘要不带 [smm_v1:] 行（cut_marker.go:379-381），恢复成功后 CommitFinal 清 state.SummaryMarker → 下一轮 Prepare fail-open 一轮（安全方向，非回归）。
- **P3-2** `cachedSummaryText` 对旧缓存体（无 marker）的向后兼容腿无直接单测钉（行为等价经直读逐字确认，风险低）。
- **P3-3** Recover 的 persistRes 不带 RawSnapshot/AlignmentMap → 二者按 copy-forward 继承 prev，与主动路径"新值覆盖"语义存在差异；不影响 diff 守卫字段，登记备查。
- **P3-4**（观察）12h 文档 §9.59 配对数 12,802（:8058）与正文/提交信息 12,798 差 4（查询时点差或笔误，不影响判据）。
- **P3-5**（观察）P2-C 三钉测相邻语句间存在 5ms tick 缝的理论误红窗口（多一拍成功致 T3 假红），未观测到。

## 三、结论

十四笔受审提交：**38497afed、41af920d5、830f2f221、91466ce63 判定成立**（变异声称全部独立复现，无虚构）；**e474c6331 主体成立、自检判据不成立（P2-1）**；**0bda13c41 P2-A 成立、P2-C 钉测承重但引入数据竞争打红 CI race 门（P1-1）**；docs/merge 批相符。本轮没有发现"提交信息声称的测试完全不存在"形态的虚构，但存在两例"声称承重的断言实际较弱/恒真/带竞态"（P1-1、P2-1），正是复审重点形态。
