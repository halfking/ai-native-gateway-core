# hotzone P3+P4+P5(指标)批判式审计(2026-09-30)

- 审计对象: docs/storage/2026-09-24-hotzone-dual-mode-plan.md 的 P3(H2 full 装配)、P4(H3 镜像接线)、P5(H5 指标分维度 + 文档子项,均已关闭)
- 提交谱系: `5250eede0`(P3 装配+门控+测试)→ `4edc7574f`(P4 两接线点)→ `b67fe45b4`(P4 测试)→ `205864527`(P5 指标+接线+gofmt 补正);哈希均为 push 落定后终校值
- 方法: 逐验收项对照代码锚点 + 子代理并行实勘(telemetry 侧实勘纠正了主代理的锚点假设,见 §三 F1)+ HEAD=205864527 干净 worktree(/tmp/hotzone-p3p4,独立 GOCACHE=/tmp/gocache-hotzone)全量回归
- 状态: P3/P4 关闭;P5 指标与文档子项(README/ADR/deployment-guide)全部关闭。F5(第三落库点接镜像)2026-10-01 关闭,**经同轮批判式审计补修 F5-R1(S4 停写门漏项,首版实现确有缺陷)**;F3/F4/F5-R2/F5-R3 为留档项(设计决定或需 owner 拍板,非本轮缺口)。**F5 验证等级已由「仅单测」升为「部署级实证」**(2026-10-01 245 演练:admin 流量落 req/resp 两件套 + gunzip 内容一致 + F5-R1 关停 S4 镜像同步停;证据见 §六)。注意:245 现网无**自然** admin 调用方,F5 收益需外部 HTTP 调用方才会自然发生。

---

## 一、结论

P3+P4 交付成立:H2 装配点与 H3 两接线点全部落地且行为有钉死测试;执行中发现
并修正了两处"方案锚点与真实代码结构不符"——(1) main.go 六处把
`storageRt != nil` 当 lite 判据的门控(方案未列出的隐藏工作量);(2) telemetry
镜像激发位置若照方案字面放 insert 门内,P4 两条核心验收直接失败(§三 F1)。
P5 指标分维度落地并顺带闭合了 hotzone_enabled 恒 false 的预存缺口;文档子项
诚实挂账未做。

## 二、验收矩阵(逐条带锚点,当前 HEAD=205864527 实读)

### P3(H2 full 热区装配)

| 项 | 验收要求 | 锚点 | 判定 |
|---|---|---|---|
| P3-1 | initStorageMode full+热区启用 → hotZoneOnly runtime | initStorageMode dispatcher(storage_mode_init.go:178);initFullHotZoneStorageMode(:220),rt.hotZoneOnly=true(:294 区域) | ✅ |
| P3-2 | 不建 SQLite 工厂 | rt.factory 恒 nil(该分支无 factory 赋值);TestInitStorageModeFullHotZoneAssembly 断言 `require.Nil(t, rt.factory)` | ✅ |
| P3-3 | 不收口 Redis env | disableRedisEnv 首行 `if r == nil \|\| r.hotZoneOnly { return }`(:576 区域);TestInitStorageModeFullHotZoneKeepsRedisEnv 断言 env 原样 | ✅ |
| P3-4 | 不启 LiteRetention/Consistency worker | hotZoneOnly 分支无 consistencyWorker/cacheTrimmer 赋值;测试断言三者 nil | ✅ |
| P3-5 | FileCache 落 HotZone.Dir/cache | cacheDir := filepath.Join(hz.Dir, "cache")(:262 区域);v2.NewFileCache(cacheDir, retention, maxBytes);测试断言目录在盘上 + Stats()["max_size_bytes"]==1GB + TTL==7h | ✅ |
| P3-6 | FileBodiesStore 复用实例落 HotZone.Dir/session_bodies | bodiesStore := filestore.NewFileBodiesStore(bodiesDir, 0)(:276);Shutdown 排空(Shutdown 内 bodiesStore.Close) | ✅ |
| P3-7 | newSessionCacheV2 走 WithMode(full, fileCache) | newSessionCacheV2(:607)`storage.StorageMode(r.mode)` 通配 lite/full;TestInitStorageModeFullHotZoneAssembly 以「fileCache 种入后 newSessionCacheV2 Get 命中」实锚 L1.5 进读链 | ✅ |
| P3-8 | 运行期 storage.hotzone_enabled=false 语义显式定义+单测 | 语义=停清不卸载(trimmer 每 tick WithReload 直查,P2 既有)+ 装配门仅 config 通道(§三 D2);TestInitStorageModeFullHotZoneSettingsKvNotAssemblyGate 用 fake settings_kv 钉死「false 不阻断装配」 | ✅(语义与目标建议有偏差,论证见 D2) |
| P3-9 | full 启动出现 data/hotzone 目录 + 关闭开关后行为与 main 一致的对照测试 | TestInitStorageModeFullHotZoneAssembly(目录断言)/ TestInitStorageModeFullHotZoneDisabledMatchesLegacy(nil runtime+不落盘+历史构造)/ TestInitStorageModeFullHotZoneEnvKillSwitch(env 一键关闭端到端) | ✅ |
| P3-10 | main.go 门控判据(方案未列,执行中发现) | 六处 `storageRt != nil` → `storageRt.liteMode()`(= r!=nil && !hotZoneOnly,:563):PG 跳过(main.go:505 区域)、STORAGE_MAX_CONNS 护栏、Redis 装配门(:923 区域)、lite auth 门、lite telemetry sink、no-PG V2 缓存分支 | ✅ 隐藏工作量,漏改任一处都会让 full+热区进程误跳过 PG/Redis 装配 |

### P4(H3 请求镜像接线)

| 项 | 验收要求 | 锚点 | 判定 |
|---|---|---|---|
| P4-1 | 接线点1=SessionBodiesWriter 三方法调用方 | WriteBodiesInTx/WriteFinalFullInTx 所在事务 **tx.Commit 成功后**投递(session_writer_v2.go:775 `w.mirrorTurnBodies(bodiesRec)` + :776-780 `finalFullWritten` 门控 `w.mirrorFinalFull(req)`;同日修订轮 R-A 从 INSERT 后挪至此,原 :680/:717 位置有孤儿镜像缺陷);seam=BodyMirrorFunc/SetBodyMirror(:128/:133,与 SetMemoraWriter 同契约);request_id='final_full:<session>' 与 PG 行口径一致 | ✅(修订后) |
| P4-2 | 接线点2=telemetry request_logs_bodies_hot 落库处 | Client.SetBodyMirror(client.go:656,atomic.Value 照 SetRequestLogSink 款式);激发=persistRequestLog 顶部(:1126,**任何 PG 往返与 degraded 早退之前**),论证见 F1;换算与 upsertRequestLogBodies 同源(strPtrToJSON/jsonOrNull) | ✅(位置与方案字面不同,F1) |
| P4-3 | fire-and-forget、失败仅计数不阻断 | 复用编码层 MirrorAsync+RecordMirrorWrite(request_mirror.go);v2 侧 mirrorTurnBodies/mirrorFinalFull 无错误通道;TestSessionWriterMirror_WriteFailsNoMirror 证明主链路失败零镜像(镜像只在成功后激发) | ✅ |
| P4-4 | 读路径不参与 L3 回源 | admin 详情路径零改动;request_mirror.go 头注 ✖ 项显式记录「有意不接」(方案 §3-H3.3) | ✅ |
| P4-5 | 测试:PG 不可用时镜像仍写入 | TestPersistRequestLogMirrorsBeforePGRoundtrip(Begin 直接失败,镜像已投递、持久化错误原样返回)/ TestPersistRequestLogMirrorInDegradedMode(degraded 早退前投递) | ✅ |
| P4-6 | 测试:三件套路径断言 | telemetry:TestMirrorRequestBodiesMapsThreePieces(req/resp/out 映射+tenant+requestID+at)、SkipsEmptyAndNull(null/{} 跳过矩阵)、TenantFallback;v2:TestSessionWriterMirror_TurnBodies/FinalFull(out 方向 requestID='final_full:sess_atomic')+ mirrorableTurnPayload 矩阵 | ✅ |
| P4-7 | 装配侧:RequestMirror 创建+闭包注入两消费方 | requestMirror = NewRequestMirror(hz.Dir, 0)(storage_mode_init.go:282,HotZone.RequestMirrorEnabled 门);bodyMirrorFn(:548);main.go telemetryClient.SetBodyMirror(lite sink 块之外)+ sessionV2Writer.SetBodyMirror(V2 writer 构造后);Shutdown 排空 requestMirror | ✅ |
| P4-8 | TODO(wiring-pending) 收账 | request_mirror.go 头注改接线状态:1/2/4 完成、3 有意不做 | ✅ |

### P5(H5 指标分维度,文档子项除外)

| 项 | 验收要求 | 锚点 | 判定 |
|---|---|---|---|
| P5-1 | mirror_writes_total/mirror_write_errors 按 mode 分列 | StorageMetrics.modeMirrorOK/Err [2]atomic(storage_metrics.go:74-76);RecordMirrorWrite 按进程戳落桶(:170 区域,签名与无维度口径不变);Snapshot mirror.by_mode | ✅ |
| P5-2 | hotzone_hit_total / L1.5 命中率按 mode 分列 | RecordL15HitForMode/MissForMode(:107 区域);唯一生产埋点 cache_v2.go:229/233 切换;Snapshot l1_5_by_mode(含 hit_rate)+ hotzone.hit_total_by_mode(与 l1_5 同源别名,不设独立计数器避免双计数漂移) | ✅ |
| P5-3 | 未接线路径行为兼容 | modeIndexOf 未盖章(0)/未知标签 → -1 不落桶,仅累计无维度计数;TestStorageMetricsUnknownModeNotBucketed | ✅ |
| P5-4 | hotzone_enabled 恒 false 预存缺口闭合(审计 F2 关联) | SetStorageMode/SetHotZoneEnabled 接线:full 装配(:226/:228 单 defer 出口)、lite(:323/:466/:468);TestStorageMetricsHotZoneEnabledFlag | ✅ |
| P5-5 | 并发安全 | 固定两槽 [2]atomic 而非 map+锁;TestStorageMetricsModeConcurrent(100×50 -race 精确可加) | ✅ |
| P5-6 | README 增补/ADR amendment/deployment-guide 配置矩阵 | R36(`e968d73f3`)落地:README「全量模式热区」章节(`docs/storage/README.md:86`)、deployment-guide hotzone 配置矩阵三通道(`docs/storage/deployment-guide.md:230-245`,含「装配门只认 config 通道」语义)、ADR-0019 Amendment(`docs/adr/2026-09-05-dual-mode-storage-package-layout.md`,+12 行) | ✅ |

## 三、发现表

| # | 严重度 | 发现 | 处置 |
|---|---|---|---|
| F1 | 锚点失实(子代理实勘纠正,已按实况实施) | 目标/方案给的 telemetry 锚点「`if logsWrite {` 块(约 :1197)」实际在 **insertRequestLog** 内且 updateRequestLog 有第二个同款门;若照字面只在 insert 门内接线:(a) resp/out 永不镜像——三件套在终态 update 才齐;(b) tx.Begin 在门之前,PG 不可用时根本走不到门内,P4 两条验收直接失败。**实施改为 persistRequestLog 顶部(degraded 早退之前)+ 显式 requestLogsWriteEnabled() 同键同门**——唯一同时满足「PG 不可用仍写入」与「S4 停写镜像同步停」的位置;insert/update/lite sink 三路全覆盖 | 已落地(client.go:1126)+测试钉死(P4-5 全组);教训:接线类任务的锚点必须实勘调用图,不能转述文档 |
| F2 | 隐藏工作量(方案未列,执行中发现) | main.go 六处 `storageRt != nil` lite 判据在 hotZoneOnly runtime(非 nil 但非 lite)下全部失真——PG 跳过、Redis 装配、lite sink、auth 门等;漏改任一处即生产事故级 | 全部改写 `storageRt.liteMode()` 并在 storageRuntime.liteMode 与 main.go 注释钉死「禁止直接判 storageRt != nil」;P3 提交信息与本文档双留档 |
| D1 | 语义偏差(判定合理,测试钉死) | 目标建议「storage.hotzone_enabled 运行期 false=仅停新装配,已装配不动」——但装配点在 PG 之前,**settings_kv 读不到**;且 spec 的 env 通道经 EnvNameAuto 与 config env 同名(LLM_GATEWAY_HOTZONE_ENABLED)无增量能力。实际语义:settings 的运行期效果=停清不卸载(trimmer 每 tick 直查,P2 语义);「停新装配」由 config 通道下次启动表达。TestInitStorageModeFullHotZoneSettingsKvNotAssemblyGate 钉死 | 已实现+单测;偏差理由写入 initFullHotZoneStorageMode 注释与本文档 |
| D2 | 设计决定(方案留白) | 不跑 config.Validate() 作为装配门——其 full 分支强依赖 full_storage 段(postgres_url/redis_url 必填),env-only 存量 full 部署会被阉割掉热区(main 对 full 的 Validate 也是 non-fatal 同先例);改用最小安全检查:热区根目录已存在且为 symlink/普通文件 → 告警降级历史装配(不拒绝启动) | TestInitStorageModeFullHotZoneRootNotDir 双子用例钉死 |
| D3 | 设计决定(方案留白) | full 热区 FileCache TTL 无独立旋钮,实现取 TTL=HotZone.RetentionHours(读侧过期与删侧 retention 同界);settings 运行期调小 retention 时删侧先行——缓存纯语义无损(miss 回源 PG),lite 侧 resolveCacheTrimRetention 钳制不适用(两者同源即天然对齐) | 注释留档;如需独立 TTL 旋钮属新需求 |
| F3 | 挂账(不阻断) | 重复镜像写放大:telemetry worker 对失败持久化重试,同一 entry 二次进入 persistRequestLog 会重复投递;同 (tenant,requestID,direction) 路径 gzip 覆盖、内容一致,幂等无害但多一次写。ReplayFallback(:664-674)绕过 persistRequestLog,重放不触发镜像(灾备重放不补镜像,可接受) | 留档;量级可观测(mirror.by_mode 计数) |
| F4 | 挂账(对账口径) | ① 镜像投递的是换算后、body summary 摘要前的原文——开启 requestBodiesSummaryEnabled 的 tenant 上镜像=全文、PG=摘要信封,「gunzip 与 PG 内容一致」抽查会对不上;② strPtrToJSON 把空串/非法 JSON 收敛 "{}" 且镜像侧跳过 "{}",PG 侧 "{}" 照落库——对账脚本需豁免 null/{} 行 | 留档;对账脚本设计时按此口径 |
| F5-R1 | **缺陷,已修(F5 首版漏项)** | F5 首版把镜像投递放在 `persistRequestLog` 入口且**不接** S4 停写门 `storage.request_logs_write_enabled`,而 telemetry client 侧明确 `if requestLogsWriteEnabled() { c.mirrorRequestBodies(entry) }`。镜像写的是 `request_logs_bodies_hot` 的**同一批正文**,运维关停该键是为止血该家族磁盘占用(F3 重复写、4.3GB/24k 行量级),镜像绕门即「停写却仍落盘」且两侧行为分裂 | 已修:抽 `admin.requestLogsWriteEnabled()` helper(键字面量只此一处,原内联门一并收敛),镜像投递纳入同键同门;`TestF5AdminIngestMirrorHonorsStopWriteGate`(判「门关零投递」而非「门开有投递」——后者在缺陷存在时也成立,钉不住)+ `TestRequestLogsWriteEnabledSingleKeyLit` 结构性钉桩;两处均经变异验证转红 |
| F5-R2 | **已裁决并落地(2026-10-01 owner 拍板「删」)** | `admin.keepAllBodies()`(`admin/telemetry.go:243`)是**死代码**——全仓仅定义、无调用,但其文档声称「bodies 只保留失败行,可省 ~90% 磁盘」。实测 admin ingest 对 `RequestBody/ResponseBody` 无任何丢弃逻辑(仅 `admin/logs.go:1036` 读路径置 nil)。即 admin 侧全量落正文,与文档描述的策略不符 | **owner 裁决:删**。死代码的危害不在于多一个函数,而在于它带着「省 90% 磁盘」的**误导性文档**留在树里、运维照此做磁盘决策。已删函数+随之孤立的 `os` import,并把留存策略文档**移到真实生效点**(`persistRequestLog` 的 `upsertRequestLogBodies` 调用处),说明「成功/失败行一律落全量正文、唯一止血手段是 S4 键」。守护 `TestKeepAllBodiesDeadCodeRemoved` 判**剥注释后**代码不得再出现该 env 旋钮(钉失败特征而非「函数不存在」——后者会被改名/别名绕过);行为面「成功行也落正文」已由既有 `TestTelemetryIngestRequestLogStopWriteGate` 钉住,故不重复。**零生产行为变更** |
| F5-R3 | **观察项(对账口径,F4 延伸)** | 同一 `request_id` 若既经 telemetry client 直连 PG、又经 admin HTTP ingest 投递,镜像会**两次落到不同 tenant 目录**(client 用 `ApplicationCode\|\|TenantID`、admin 用 `nonEmptyDefault(TenantID)`);`upsertRequestLogBodies` 侧有 `ON CONFLICT DO NOTHING`,但镜像层无跨写方去重 | 留档:对账脚本按 F4 口径再放宽为「镜像按 (tenant,request_id,direction) 多写方去重后比对」;不引入运行时去重(跨写方去重需要共享状态,代价高于收益) |
| F5 | **缺陷,已修(2026-10-01 owner 拍板「接镜像」)** | admin HTTP ingest(/api/telemetry,admin/telemetry.go)也调 upsertRequestLogBodies(仅 req/resp),未接镜像——方案 §3-H3 说「两处」,此为第三处 | `admin.SetIngesterBodyMirror` 注入(atomic.Pointer 承载,main goroutine 写 / ingest worker 读),`persistRequestLog` **入口**投递(与 telemetry client 同边界,PG 停机窗口仍落镜像);tenant 取 `nonEmptyDefault(TenantID)`,与同事务 PG 行 tenant_id 严格同源(本路径无 application code 字段,故不复制 O2 的租户分裂);仅 req/resp 两件套。镜像换算/可镜像判定下沉 `storage/file.ConvertBodyPayload`+`MirrorablePayload` 作单一事实源,telemetry 落库侧 `strPtrToJSON` 与 `mirrorableBody` 同步转调(消除两侧各写一份的失配面)。测试 `admin/telemetry_ingest_body_mirror_test.go`(变异验证:摘掉入口投递即红、摘掉 S4 门即红)。**验证等级:2026-10-01 已升为部署级实证**——245 演练(admin 流量 req/resp 两件套落镜像、gunzip 内容与 PG 一致;F5-R1 关停 S4 键时镜像 0 落盘而计费保留),证据见 §六;但 245 现网无**自然** admin 调用方(无外部 HTTP 生产者),收益需调用方接入才自然发生。 |
| F6 | 观察项 | gofmt:CJK 注释行首全角括号会被 gofmt 归一,首轮提交文件未逐个过 gofmt(P5 提交补正);turn_writer.go/turn_writer_test.go/empty_response_metrics_test.go 等历史文件本就未格式化,未碰 | 本轮触碰文件已全部 gofmt 干净 |

## 三.5、同日修订轮(交付后批判式复审,实勘三点 + 修复两项)

复审方法:对本轮总结中"声明过"的三个关键点逐一实勘,不接受声明本身。

| # | 判定 | 实勘内容 | 结论与处置 |
|---|---|---|---|
| R-A | **缺陷,已修** | v2 镜像调用点在 `WriteBodiesInTx`/`WriteFinalFullInTx` INSERT 成功后,但 **tx.Commit(:762)之前**——commit 失败回滚时 PG 无 bodies 行而镜像已落盘 = 孤儿镜像,破坏「镜像与 PG 行共存亡」的对账承诺(F4 口径进一步破缺)。首轮测试只钉了「INSERT 失败零镜像」,恰好没覆盖 commit 失败这一段 | 镜像挪到 `committed = true` 之后(session_writer_v2.go:775-780,finalFullWritten 置位 :714);新增 TestSessionWriterMirror_CommitFailsNoMirror(全编排成功 + ExpectCommit().WillReturnError → 零镜像)钉死;request_mirror.go 头注同步「事务提交成功后」语义。**教训:首轮测试的失败注入只选了 INSERT 一段,"写入成功"与"事务提交成功"是两个语义,验收词句含糊时按更严者实施** |
| R-B | **验收缺口,已补** | 纪律 4 要求「三件套**路径**断言」,首轮只断言了镜像调用参数(direction/tenant/requestID/payload),磁盘路径完全依赖编码层既有测试——接线层对"路径真的长这样"零覆盖 | 新增 TestSessionWriterMirror_DiskPathEndToEnd:真 RequestMirror 注入(装配闭包同款适配),Write 后 Close() 排空异步队列,断言 `requests/{tenant}/{date}/{requestID}.{req|resp|out}.json.gz` 三文件存在、gunzip 内容与 PG 行同源(含 "hi"/"hello")、目录恰三件(direction 集合闭合) |
| R-C | **疑点排除** | 头号质疑:「full 模式 L1.5 有没有生产写路径?若只装不写,P3 读链空转」 | 排除:cache_v2.go Get 的 L3 回源回填(:275-287「l1_5 非空时回填」,mode-blind)与 Set(:315-320,mode-blind)均无 lite 守卫——full+fileCache 装配后 L3 miss 回源即回填 L1.5,cache/ 子树有真实写入方;Invalidate 同理 mode-blind |

修订后 P4-1 验收锚点更新:镜像激发点 = tx.Commit 成功后(:775/:776-780),语义从「写入成功后」收紧为「事务提交成功后」。

## 四、回归证据(HEAD=205864527,干净 worktree /tmp/hotzone-p3p4)

```
go test -race -count=1 ./storage/ -run "TestLiteConsistency|TestLiteReconcile|TestLiteBody|TestLiteRepair|TestRepairDoubleConfirm|TestRepairGrace|TestRepairDeleteWithout" -v
  → 9/9 PASS(ok 1.7s)
go test -race -count=1 ./cmd/gateway/ → ok 2.7s(含 P3 新增 8 用例 + 既有全量)
go test -race -count=1 ./domains/session/v2/ → ok 2.8s(含镜像 5 用例)
go test -race -count=1 ./domains/hooks/observability/telemetry/ → ok 5.0s(含镜像 8 用例)
go test -race -count=1 ./storage/file/ → ok 10.5s
go test -race -count=1 ./monitoring/ → ok 1.6s(含 per-mode 4 用例)
go vet(monitoring/cmd/v2/telemetry/storage) → 净
go build ./cmd/gateway/ → 净
```

注:主树存在并行会话未跟踪 WIP(bg/cmd 构建失败),全部构建/测试走干净 worktree;
期间 Go 共享构建缓存两度损坏(缺 cache entry),worktree 改用独立
GOCACHE=/tmp/gocache-hotzone 隔离后稳定。

## 五、遗留风险与挂账

1. ~~P5 文档子项未做~~ **已由并行 R36 轮闭环(e968d73f3),本轮复核属实**:README「全量模式热区」章节(README.md:86-92)、deployment-guide hotzone 配置矩阵(:230-243,含「装配门只认 config 通道」语义,与本文档 D1 一致)、ADR-0019 Amendment(2026-09-30)。
2. ~~**E2E 部署级验证未做**~~ **已闭环(2026-09-30 当日,local 全栈)**:full+热区开关对照、/metrics/storage 三新键实读(跨重启 L1.5 hits=3)、PG 硬停演练(镜像 errors=0 持写、恢复无结构性损坏)全部实证;新发现部署缺口 D1(管线 env 白名单缺 STORAGE_MODE/HOTZONE_*)。证据: docs/audit/2026-09-30-hotzone-e2e-deploy-drill.md
3. F3 重复镜像写放大、F4 对账口径豁免(见 §三)。~~F5 admin ingest 第三落库点~~ **2026-10-01 已接镜像关闭**(owner 拍板,见 §三 F5 行)。
4. full 热区 bodiesStore 实例已装配但**尚无写入方**(session bodies 镜像走的是 RequestMirror/requests 子树;FileBodiesStore 的消费方属后续波次)——目录生命周期已归 Shutdown/trimmer 管,先行装配。
5. 主树并行会话 WIP 未跟踪文件仍在,任何全包测试继续走干净 worktree。

## 六、Handoff(下一轮入口)

- ~~部署演练~~ **已完成(2026-09-30 当日)**:见 docs/audit/2026-09-30-hotzone-e2e-deploy-drill.md
  (新遗留:管线 env 白名单 D1、.env.local D2、镜像对账口径 O2/O3、PG 停机遥测丢失 O5)。
- ~~F5 决策:admin ingest 是否接镜像,owner 拍板。~~ **已决策并落地(2026-10-01)**:owner 选「接镜像,补齐第三落库点」,实现与测试见 §三 F5 行。
- ~~**F5 未验证项(勿当已闭环)**~~ **已收口(2026-10-01 部署级实证)**:① 部署级实证 ✅(见上方 245 演练段:admin 流量落镜像 req/resp 两件套、gunzip 内容一致);② 真实流量 ✅(自铸 admin JWT 造流量成功——环境无**自然**调用方,但端点能力已实证);③ F5-R1「关停 S4 镜像同步停」✅(门关:镜像 0 / PG 正文 0 / 计费保留)。**验证等级由「单测」升为「部署级实证」**。注意:245 现网无**自然** admin 调用方,F5 的收益需有外部 HTTP 调用方才会自然发生。
  - **2026-10-01 245 环境侧只读勘查(owner 明确授权后执行)——结论:演练当时无法进行,原因已取证**:
    - 245 实际形态与 handoff 假设**不符**:无 `llm-gateway-go.service`(该 unit 不存在),而是**蓝绿 slot 形态**——`llmgo-245-canary@8781`(active running)与 `llmgo-245-canary@8782`(failed),`llmgo-245.service` 为 inactive dead。「重启服务」需按 slot 语义操作。
    - **部署版本不含 F5-R1**:`/opt/llm-gateway-go/slots/8781` → `releases/2369-bdc13ccc`,VERSION `2.5.8-bdc13ccc-20260930-2369`。`bdc13ccc` commit-date 2026-10-01 02:34,含 F5(`f9947d3a3` 01:32)但**不含 F5-R1**(`5cd64600e` 02:56,晚 22 分钟)。即 F5-R1 的 S4 停写门修复在 245 上**根本没上线**——演练的对照项无从谈起。
    - **热区未装配**:`.env` 无 `LLM_GATEWAY_STORAGE_MODE`/`LLM_GATEWAY_HOTZONE_ENABLED`,运行进程 environ 亦无;全盘 `find` 无任何 `hotzone` 目录。镜像子树 `requests/` 不存在。
    - **待办②(/api/telemetry 真实流量)已取证——结论:245 上该端点无真实流量**:`request_logs_bodies_hot` 24h 9,135 行、1h 770 行、`max_ts` 距查询 2 分钟,ingest 链路本身是活的;但**全部来自 telemetry client 与 health probe,无一条来自 admin HTTP ingest**。四路交叉才定性(前两轮取证法各有误,教训见下):
      1. nginx 全部 access log 对 `api/telemetry` 命中 **0**(逐个 access_log 文件 grep,含全部轮转 .gz)。
      2. `journalctl` 近 6h 命中 26 行,其中 24 行是 client 路径的 `probe-direct-*` 告警
         (`telemetry success_response_body_missing` / `JSON field discarded`),余 2 行是勘查者
         自己的 curl 探测(04:02,user_agent=curl/7.61.1,status=401)。**无第三方调用**。
      3. 24h 内 `application_id IS NULL` 的 3,915 行:3,886 行是 `probe-*`;余 29 行
         `request_mode=chat|anthropic`(client 路径特征,**非** admin 路径的 mode 空值特征),
         且时间戳止于 01:03(即当前构建部署时刻),之后无新增。
      4. 端点本身**是活的**、路由已装配:nginx `llmgateway.internal.example.com` → `127.0.0.1:8781`,
         直连 `POST /api/telemetry/request-log` 返回 401 `authentication required`
         → 不是「端点不存在」,而是**无人携带凭据调用**。
    - **154 同样不具备条件**(只读勘查):`llm-gateway-go-canary@8782`(active)/`@8781`(failed),
      release `2356-2d750fb4`(`2.5.8-2d750fb4-20260930-2356`,比 245 更旧)、无 `STORAGE_MODE`、
      无 hotzone 目录。**245/154 两台均不可用于该演练。**
    - **取证法纠错(本轮自身失误,值得记)**:
      - ① 首轮判「journalctl ingest 日志 0 条」用了过窄的 grep 模式
        (`telemetry ingest`),而现网日志文案是 `telemetry success_response_body_missing`
        等,故「0」是**模式写错的假零**,不是事实。按模式取数得到 0 时,须先确认模式能匹配到
        已知的正例(此处即我自己 curl 出的那 2 行),否则 0 无信息量。
      - ② 次轮判「nginx 0 命中」时,差点据此断言「端点无流量」——但 `/api/telemetry`
        实际**在 nginx 路由内且端点存活**。nginx 0 只能说明「未经该 vhost」,
        不能证明「无人调用」;必须叠加直连探测(401 存活)+ 按写入方特征拆 PG + 日志三路。
    - **综合判定:F5 在 245 上是「接线已部署但无收益也无镜像产物」**——既无 admin 流量可打,也无热区可落。要收口需先①部署含 F5-R1 的新构建 ②显式开启 `STORAGE_MODE=full` + 热区 ③**制造** admin `/api/telemetry` 流量(环境无自然生产者,须外部 HTTP 调用方)。三者均为新决策,非本轮授权范围。

    - **2026-10-01 F5 部署级演练已完成(owner 明确授权后执行;证据如下)**:
      - **部署**:`scripts/deploy-seamless.sh deploy 245 --seq 2370` → release `2372-d2af305a`(含 F5 + F5-R1,
        两者均已在二进制内,`strings` 验 `request_logs_write_enabled` 键字面量存在)。蓝绿切流至 8782,健康 200。
      - **热区开启**:临时 `LLM_GATEWAY_STORAGE_MODE=full`(.env 备份 `.env.bak.f5drill.20261001-045524`)→
        启动日志实证 `data/hotzone/requests/` 子树出现,且
        `storage hotzone: admin ingest request body mirror wired` —— **F5 admin 镜像确认装配**。
      - **admin 流量**:`/api/auth/token` 的 env 密码与 users 表漂移无法登录,改以 `LLM_GATEWAY_SECRET_KEY`
        现铸 15 分钟 HS256 JWT 携带 `super_admin` 身份(不改任何密码/不改 users 表),
        POST `/api/telemetry/request-log` 造 3 条 `f5-drill-*` 真实流量(均 200 queued)。
      - **F5 落盘实证(部署级)**:3 条 drill 的 req/resp **两件套全部落镜像**,gunzip 内容与提交原文一致:
        `data/hotzone/requests/default/2026-09-30/f5-drill-*.{req,resp}.json.gz`;
        `gunzip -c` 得 `{"messages":[{"role":"user","content":"f5 drill ...-1 q"}]}` / `{"content":"f5 drill ...-1 a"}`。
        PG `request_logs_bodies_hot` 同步有对应 req/resp 行 —— **镜像与 PG 语义一致**。
      - **F5-R1 部署级实证(核心对照)**:把 `storage.request_logs_write_enabled` 置 `false`(settings_kv
        平台行)并重启后,再打 `f5-drill-ROFF-*` 流量(均 200 queued):
        - 热区镜像落盘 **0** 个文件(镜像随门同步停)✅
        - PG `request_logs_bodies_hot` **0** 行(request_logs 族整体跳过)✅
        - `usage_ledger_hot` **2** 行(计费行保留,门只停 request_logs 族)✅
        ⇒ **F5-R1「关停 S4 键时 admin 镜像同步停」取得部署级实证**,不再只是单测推断。
      - **恢复原状**:删除 drill 写入的 settings 行(回「键缺席=默认 true」)、从备份还原 .env
        (移除 `STORAGE_MODE=full`)、删除 PG 全部 `f5-drill%` 行(bodies/logs/ledger,0/0/0 验证)与
        磁盘 `f5-drill*` 镜像文件(0 验证);重启后复核:`STORAGE_MODE` 不在进程 environ、热区无新写、
        镜像装配线已消失、8782 healthz=200。环境回到演练前状态(仅留 `.env.bak.f5drill.*` 审计备份)。
      - **待办②结论(由「无流量」改写)**:本轮用自铸 admin JWT **制造**了 admin 流量并成功落镜像,
        证明 F5 在部署级**确实有收益**;此前 245「无 admin 流量」的观察是**环境现状**(无自然调用方),
        不是「端点坏掉」。真实流量有无仍取决于是否有外部 HTTP 调用方——但**能力已实证可用**。- ~~**F5-R2 留档待 owner 拍板**~~ **已裁决并落地(2026-10-01)**:owner 选「删」——清掉 `keepAllBodies()` 死代码、承认 admin 全量留存、把留存策略文档移到真实生效点。零生产行为变更。详见 §二 F5-R2 行。
- **F5-R1 教训(接线类缺陷的通用形态)**:「同一语义门在两处调用点各自内联键字面量」是分裂入口。三个镜像消费方接入时,凡是**复用同一个 settings 键**的判定一律抽 helper 收口,并加一条「键字面量只出现一次」的结构性钉桩——行为用例钉不住这类缺陷。
- 若做「full 热区 session_bodies 写入方」(§五.4),复用 rt.bodiesStore 实例,勿再建第二实例(AsyncFileWriter 双实例会双倍写 worker)。
- 对账脚本(若建):按 F4 口径豁免 null/{} 行,并注意 v2 侧镜像以 tx.Commit 为界(修订轮 R-A)、telemetry 侧以 persistRequestLog 入口为界(天然含 PG-down 投递)——两侧孤儿语义不同向,脚本按「镜像可能多于 PG」单向容错。
