# hotzone P3+P4+P5(指标)批判式审计(2026-09-30)

- 审计对象: docs/storage/2026-09-24-hotzone-dual-mode-plan.md 的 P3(H2 full 装配)、P4(H3 镜像接线)、P5(H5 指标分维度;文档子项未做,见 §六)
- 提交谱系: `5250eede0`(P3 装配+门控+测试)→ `4edc7574f`(P4 两接线点)→ `b67fe45b4`(P4 测试)→ `205864527`(P5 指标+接线+gofmt 补正);哈希均为 push 落定后终校值
- 方法: 逐验收项对照代码锚点 + 子代理并行实勘(telemetry 侧实勘纠正了主代理的锚点假设,见 §三 F1)+ HEAD=205864527 干净 worktree(/tmp/hotzone-p3p4,独立 GOCACHE=/tmp/gocache-hotzone)全量回归
- 状态: P3/P4 关闭;P5 指标部分关闭、文档子项(README/ADR/deployment-guide)未做

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
| P5-6 | README 增补/ADR amendment/deployment-guide 配置矩阵 | — | ⬜ 未做(§六挂账) |

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
| F5 | 挂账(第三落库点) | admin HTTP ingest(/api/telemetry,admin/telemetry.go:446-478)也调 upsertRequestLogBodies(仅 req/resp),未接镜像——方案 §3-H3 说「两处」,此为第三处;ingest 入口是否有真实流量需 owner 拍板后再接 | 留账待 owner 决策 |
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
3. F3 重复镜像写放大、F4 对账口径豁免、F5 admin ingest 第三落库点(见 §三)。
4. full 热区 bodiesStore 实例已装配但**尚无写入方**(session bodies 镜像走的是 RequestMirror/requests 子树;FileBodiesStore 的消费方属后续波次)——目录生命周期已归 Shutdown/trimmer 管,先行装配。
5. 主树并行会话 WIP 未跟踪文件仍在,任何全包测试继续走干净 worktree。

## 六、Handoff(下一轮入口)

- ~~部署演练~~ **已完成(2026-09-30 当日)**:见 docs/audit/2026-09-30-hotzone-e2e-deploy-drill.md
  (新遗留:管线 env 白名单 D1、.env.local D2、镜像对账口径 O2/O3、PG 停机遥测丢失 O5)。
- F5 决策:admin ingest 是否接镜像,owner 拍板。
- 若做「full 热区 session_bodies 写入方」(§五.4),复用 rt.bodiesStore 实例,勿再建第二实例(AsyncFileWriter 双实例会双倍写 worker)。
- 对账脚本(若建):按 F4 口径豁免 null/{} 行,并注意 v2 侧镜像以 tx.Commit 为界(修订轮 R-A)、telemetry 侧以 persistRequestLog 入口为界(天然含 PG-down 投递)——两侧孤儿语义不同向,脚本按「镜像可能多于 PG」单向容错。
