# 双模式存储热区层方案(全模式本地文件镜像 + 三级读路径)

- 日期: 2026-09-24
- 状态: 方案定稿,待实施
- 前置: ADR-0019(docs/adr/2026-09-05-dual-mode-storage-package-layout.md)、双模式存储已全量落地(9 项 lite-consistency/repair 测试全绿;R63 §三.12 勘误:原记 12,实为 consistency_lite_test.go 6 项 + consistency_repair_test.go 3 项)
- 本文取代: 会话早期讨论中的 "dual-storage-implementation-tasks.md"(该文档基于"从零实现"的错误前提,已废弃)

---

## 1. 审计结论:现有实现 vs 本轮需求

### 1.1 已落地能力(不需要重做,审计核实于 2026-09-24)

| 能力 | 代码锚点 | 状态 |
|---|---|---|
| 双模式工厂(full=PG+Redis / lite=SQLite+File+Memory) | `storage/factory/factory.go` | ✅ 生产可用(lite 分支真实实现,full 分支 store 为桩,真实 full 路径绕过工厂) |
| 启动期模式装配 + Redis env 收口 | `cmd/gateway/storage_mode_init.go` | ✅ 仅 lite 装配,full 返回 nil runtime |
| SQLite 存储(session/turns/request_logs + catalog 四表) | `storage/sqlite/`(含 cgo/nocgo 双驱动) | ✅ WAL+PRAGMA 调优 |
| 文件 bodies 存储(异步写队列 + gzip/zstd 双编解码) | `storage/file/async_writer.go`、`bodies_store.go`、`codec.go` | ✅ 优雅关闭排空队列 |
| 进程内状态存储(替代 Redis KV) | `storage/lite/state_store.go`(MemoryStateStore) | ✅ TTL + 快照恢复 |
| L1.5 本地文件缓存(TTL/空间上限/最旧先删淘汰/记账自愈/原子写) | `domains/session/v2/cache_v2_file.go` | ✅ 但**仅 lite 模式注入** SessionCacheV2 |
| 后台清理(cache 1h / bodies 6h / 行级 retention 6h / 一致性对账) | `bg/cache_trimmer.go`、`bg/bodies_trimmer.go`、`bg/lite_retention_worker.go`、`bg/consistency_worker.go` | ✅ |
| 存储配置(YAML + `LLM_GATEWAY_*` env 双通道) | `config/storage.go` | ✅ CacheMaxSizeGB 默认 10GB、TTL 24h |
| 存储指标 + 管理端点 | `monitoring/storage_metrics.go`、`cmd/gateway/storage_metrics_endpoint.go` | ✅ |

### 1.2 本轮需求差距(真正的增量工作)

| # | 需求(用户原话归纳) | 现状差距 |
|---|---|---|
| G1 | **全量模式也要本地热区**:至少 7 小时的请求与会话完整信息保存在服务器目录,目录架构与 lite 一致 | full 模式 `storageRuntime==nil`,无任何本地文件层;`newSessionCacheV2` 走历史构造,不注入 FileCache |
| G2 | **三级查询统一**:先查内存、再查本地文件、再查数据库,两种模式一致,削减热点区 DB 请求 | lite 已是 L1→L1.5→L3;full 是 L1→L2(Redis)→L3(PG),缺文件层 |
| G3 | **空间上限默认 1G,系统设置中可配置,定时清理先移除旧的** | 默认 10GB 且只支持 YAML/env 启动期配置;未接入 settings_kv 热重载;清理顺序已满足(mtime 最旧先删) |
| G4 | **内存保存会话文件目录索引,直至文件过期** | FileCache 每次 Get 都 `os.Stat`+`ReadFile`,无内存目录索引 |
| G5 | **请求(request)与会话(session)两类完整信息都要镜像** | FileBodiesStore 仅覆盖 session bodies;request body 无本地镜像写入路径 |

---

## 2. 目标架构

```
                       ┌──────────────────────────────────────────┐
                       │            读路径(两模式统一)             │
                       │   L1 内存 → L1.5 本地文件 → (L2) → L3     │
                       └──────────────────────────────────────────┘

 lite 模式(现状保持):                     full 模式(本轮新增热区):
 ┌────────────────────────┐               ┌────────────────────────────┐
 │ L1 CompressionMetaCache│               │ L1 CompressionMetaCache    │
 │ L1.5 FileCache         │               │ L1.5 FileCache(热区索引)   │  ← 新增
 │ L3 SQLite + 文件bodies │               │ L2 Redis 治理缓存(保留)    │
 └────────────────────────┘               │ L3 PostgreSQL              │
                                          ├────────────────────────────┤
                                          │ 本地热区目录(写镜像):      │
                                          │  data/hotzone/             │
                                          │   ├─ sessions/...(同lite)  │  ← 新增
                                          │   └─ requests/...          │  ← 新增
                                          └────────────────────────────┘
```

热区目录布局与 lite 完全一致(复用同一套路径构建代码),全量模式额外多一个 `requests/` 子树承载 request body 镜像。

---

## 3. 增量工作分解(可执行)

### H1 FileCache 内存目录索引(G4)

**现状锚点**: `domains/session/v2/cache_v2_file.go`(Get 走 os.Stat,无索引)

**改动**:
1. FileCache 增加字段 `index map[string]*indexEntry`(key=`tenantID+"/"+sessionID`,entry 含 path/size/modTime/expireAt),与 `sizeUsed` 同受 `fc.mu` 保护。
2. `Set` 成功 rename 后登记索引;`Delete`/`removeExpired`/`removeIfUnchanged`/`ensureSpaceLocked` 淘汰时同步摘除;Get 命中索引可跳过 Stat(mtime/size 以索引为准,读后仍校验内容可解析)。
3. 启动 `NewFileCache` 的 Walk 阶段顺带填充索引;过期判断从"每次 Stat"变为"索引 expireAt + 惰性剔除",TTL 到期条目在下一次访问或低频扫描(复用 bg.CacheTrimmer 周期)时剔除。
4. 记账自愈路径(ensureSpaceLocked 的 healed 分支)同步重建索引。

**不变式**: 内存索引只是磁盘实况的加速视图——任何"索引说有、磁盘说无"的情形一律按 miss 处理并摘除索引(与现有 fail-open 风格一致)。锁策略沿用"Set 全程持锁"约定,禁止在 ensureSpaceLocked 内二次加锁。

**验收**: 既有 `cache_v2_file_test.go` 全绿;新增索引一致性测试(并发 Set/Delete/过期交错后,索引与磁盘 Walk 结果一致);基准测试显示 Get 热路径减少 1 次 Stat 系统调用。

### H2 full 模式热区装配(G1)

**现状锚点**: `cmd/gateway/storage_mode_init.go`(`initStorageMode` 仅 lite 分支创建 runtime;`newSessionCacheV2` 在 r==nil 时走 `v2.NewSessionCacheV2` 历史路径)

**2026-09-28 审计修正**: 读链侧已就绪,本项剩余工作量从 1.5 天缩为约 0.5 天——
`cache_v2.go` 的 `Get` 已实现"两模式共用 L1.5"(读链 L1→L1.5→L2→L3,分片锁内
读+回填,L15Hit/L15Miss 指标已埋),代码注释直接点名"full 由 H2 装配点可选注入";
`NewSessionCacheV2WithMode(full, fileCache)` 已支持保留 L2 + 注入 L1.5(此前方案
写"需确认 full 分支保留 L2"系未验证声称,现予实证)。**仅欠装配点**。

**改动**:
1. 新增 `HotZoneConfig`(config/storage.go):`Enabled`(默认 full=true, lite=true 复用现有 L1.5)、`Dir`(默认 `./data/hotzone`)、`RetentionHours`(默认 7)、`MaxSizeGB`(默认 1)、`RequestMirror`(默认 true)。
2. `initStorageMode` 中,full 且 HotZone.Enabled 时也创建 storageRuntime(新字段 `hotZoneOnly=true` 与 lite runtime 区分:full 不创建 SQLite 工厂、不收口 Redis env、不启动 LiteRetentionWorker/ConsistencyWorker)。
3. full 分支创建:FileCache(dir=HotZone.Dir/sessions? 否——缓存文件是 SessionStateV2 JSON,与 lite 的 CacheDir 语义一致,用 `HotZone.Dir/cache`)+ FileBodiesStore 复用实例(`HotZone.Dir/session_bodies`,codec 沿用 gzip)。
4. `newSessionCacheV2` full 分支改走 `v2.NewSessionCacheV2WithMode(db, redisAddr, redisDB, storage.StorageModeFull, r.fileCache)`(cache_v2.go:101 已实证:full+fileCache 保留 L2,Get 读链自动进 L1.5,无需改 cache_v2.go)。
5. 启动日志新增 hotzone 配置快照;Shutdown 复用现有 trimmer 生命周期。

**不变式**: `LLM_GATEWAY_STORAGE_MODE` 未设置且 HotZone.Enabled 默认值=full 开启——这是**行为变更点**,必须以 settings/env 双保险提供 `LLM_GATEWAY_HOTZONE_ENABLED=false` 一键关闭,且回滚零残留(热区目录只是缓存,可直接删)。

**验收**: full 模式启动后 `data/hotzone/` 出现与 lite 同构目录;`/metrics/storage` 暴露 L1.5 指标;关闭开关后行为与当前 main 完全一致(回归对照)。

### H3 请求侧镜像写入(G5)

**现状锚点**: `storage/file/bodies_store.go`(仅 session bodies);request body 当前只进 PG(`request_logs_bodies_hot` 8 小时热表 → 月分区)

**2026-09-28 审计修正**: 接线点实勘为**两处**,原方案"streaming handler 的 bodies
写入事务提交后"表述不准确——
- session bodies:`SessionBodiesWriter.WriteBodies / WriteBodiesInTx / WriteFinalFullInTx`
  (domains/session/v2/bodies_writer.go:259/273/361)的调用方;
- request bodies:`domains/hooks/observability/telemetry/` 内
  `INSERT INTO request_logs_bodies_hot` 落库处(body_summary 等路径)。

**改动**:
1. 新增 `storage/file/request_mirror.go`:轻量镜像器,入参 (requestID, tenantID, direction, body),路径 `{hotzoneDir}/requests/{tenantID}/{date}/{requestID}.{req|resp|out}.json.gz`,复用 AsyncFileWriter 与 codec。
2. 接线点(上述两处,fire-and-forget 投递镜像,失败仅计数不阻断主链路——与 FileBodiesStore 的 fail-open 语义一致)。
3. 读路径:admin 请求详情查询**不**改——镜像只服务 G2 的"文件命中"与灾备人工取数,不参与 L3 回源(避免文件缺失导致详情 404 的语义分裂)。
4. 保留期与空间上限由 H4 的 trimmer 统一管(7 小时 / 1GB,含 requests 子树)。

**验收**: 压测请求后 `requests/` 子树出现完整三件套;文件可 gunzip 还原与 PG `request_logs_bodies` 内容一致(抽查对账);PG 不可用时镜像仍持续写入(降级可观测)。

### H4 热区清理与系统设置接入(G3)

**现状锚点**: `bg/cache_trimmer.go`(按 mtime 清 CacheDir)、`config/storage.go`(默认 10GB)、`settings/spec_storage.go`(已有 storage 族 spec 注册链,lite 侧已在 storage_mode_init.go 注册)

**改动**:
1. 新 spec(settings/spec_storage.go,CategoryStorage,热重载):
   - `storage.hotzone_enabled`(bool,默认 true)
   - `storage.hotzone_max_size_gb`(int,默认 1,范围 1–100)
   - `storage.hotzone_retention_hours`(int,默认 7,范围 1–168)
2. 空间上限统一收敛到 FileCache.ensureSpaceLocked + 新增 HotZoneTrimmer(每 30 分钟:按 mtime 删过期 → 超限时最旧先删,**先移除旧的**语义与现有一致),遍历范围=整个 `HotZone.Dir`(cache+session_bodies+requests 三个子树共享 1GB 预算)。
3. 热重载:hotconfig 轮询到变更后调用 `fileCache.ResizeMax(newBytes)`(新方法,只增不减立即生效;缩容交由下一轮 trimmer 执行)与 trimmer 的 retention 原子替换。**2026-09-28 实证**:hotconfig 确有 30s ticker 轮询(hotconfig.go:63)。遗留验证点(实施期确认,不预先假设):`settings.GetPlatformBool/Int` 的读取路径是否自带缓存及其失效机制——若每次透传查询则热重载天然成立,若有缓存需确认与 hotconfig 轮询的关系。
4. 默认值变更:CacheMaxSizeGB 在 full 热区语境下取 HotZone.MaxSizeGB(1GB),lite 的 10GB 默认不动。

**验收**: 运行时改 `storage.hotzone_max_size_gb=1` 后 ≤1 轮询周期内 trimmer 生效;磁盘占用峰值 ≤ 配置值+单文件误差;`settings_kv` 审计链可回滚。

### H5 监控与文档

1. `monitoring/storage_metrics.go` 增加 hotzone 维度:mirror_writes_total/mirror_write_errors/hotzone_hit_total/L1.5 命中率按 mode 分列。
2. `docs/storage/README.md` 增补"全量模式热区"章节;ADR-0019 追加 Amendment(热区层不改变工厂接口,仅运行时装配差异)。
3. `docs/storage/deployment-guide.md` 补 hotzone 配置矩阵(env/YAML/settings 三通道优先级:settings > env > YAML > default,与现有 settings 语义对齐)。

---

## 4. 实施顺序与里程碑

| 阶段 | 内容 | 依赖 | 预估 |
|---|---|---|---|
| P1 | H1 FileCache 索引(纯增量,两模式共享收益) | 无 | 1 天 |
| P2 | H4 设置 spec + HotZoneTrimmer + ResizeMax | 无 | 1 天 |
| P3 | H2 full 热区装配(仅装配点,读链已就绪) | P2 | 0.5 天 |
| P4 | H3 请求镜像器 + 双接线点 | P2 | 1.5 天 |
| P5 | H5 指标分维度 + 文档 + 全量回归(9 项 lite 套件 + full 热区新增 E2E) | P3,P4 | 1 天 |

总计约 5 个工作日。P1/P2 可双线并行。

## 5. 风险与回滚

| 风险 | 缓解 |
|---|---|
| full 模式默认开启热区属于行为变更 | `LLM_GATEWAY_HOTZONE_ENABLED=false` 一键关闭;热区纯缓存语义,删除目录即回滚 |
| 热区磁盘写放大(full 模式 PG 已双写) | 1GB 硬上限 + fail-open;镜像写入带熔断计数(连续失败 N 次后静默跳过并告警) |
| FileCache 索引与磁盘漂移 | 索引仅作加速视图,任何不一致按 miss 兜底;记账自愈路径已有先例(2026-09-05 审计 B3) |
| SessionCacheV2 full+fileCache 新组合回归 | 该构造路径新增单测:断言 L2 仍在读链、L1.5 miss 后穿透 L2 |

## 6. 验收清单

- [x] lite 模式 9 项既有套件全绿(回归门禁)——单测级 P1P2/P3P4 轮闭环
- [x] full 模式:`data/hotzone/` 目录结构与 lite 同构,7h/1GB 默认值生效——单测级 + 2026-09-30 部署级(local,装配日志+磁盘实测;session_bodies 目录因无写入方未创建,既有挂账如实记)
- [x] 读路径顺序实测:内存 → 本地文件 → (Redis) → PG,可通过指标区分命中层——单测级 + 2026-09-30 部署级(跨重启 L1.5 hits=3 实测;镜像对账口径补全见当日演练文档 O2/O3)
- [x] `storage.hotzone_max_size_gb` 热重载生效且 trimmer 先删最旧——单测级(P2 轮钉死);部署级未重放(30m trim 间隔 > 演练时长)
- [x] 关闭热区开关后 full 模式行为与当前 main 一致——单测级 + 2026-09-30 部署级(kill-switch 重建容器对照)
- [x] PG 故障演练:热区持续写入,恢复后无数据结构性损坏——单测级 + 2026-09-30 部署级(PG 硬停镜像 errors=0 持写、恢复 639 表完好;chat 请求遥测条目丢失观察项挂账 O5,与热区正交)

部署级证据全文: docs/audit/2026-09-30-hotzone-e2e-deploy-drill.md(含部署缺口 D1:管线
env 白名单缺 STORAGE_MODE/HOTZONE_*,走 deploy-local 的环境热区不激活,须容器创建时手工注入)。

---

## 7. 批判式审计记录

### 2026-09-28 本轮(实代码核查,非转述)

| # | 原方案声称 | 核查方式 | 结论 |
|---|---|---|---|
| A1 | "需确认 WithMode 的 full 分支保留 L2" | 读 cache_v2.go:93-130 实现与 Get 读链 | **声称过时**。读链已实现"两模式共用 L1.5"(注释直接引用"H2 装配点"),L15Hit/L15Miss 指标已埋;仅欠装配点。H2 工时 1.5→0.5 天 |
| A2 | H3 接线点="streaming handler 的 bodies 写入事务提交后" | grep 定位实际 INSERT/Write 调用 | **表述不准确**。实为两处:SessionBodiesWriter 三方法(bodies_writer.go:259/273/361)调用方 + telemetry 包 request_logs_bodies_hot 落库处。已修正 |
| A3 | hotconfig 轮询热重载机制存在 | 读 hotconfig.go:63 | **属实**。30s ticker 实证。settings.GetPlatformXX 读取路径是否带缓存未验证,标注为 H4 实施期验证点 |
| A4 | "9 项 lite 套件全绿"(R64 勘误后的计数) | `go test ./storage/ -run "TestLiteConsistency|..." -count=1 -v` 实跑 | **实证成立**。6 顶层测试(含 4 子测试)全 PASS,ok 0.833s |
| A5 | 文档锚点文件在 09-24→09-28 窗口内的漂移 | `git diff --stat HEAD origin/main -- <锚点文件集>` | **无漂移**。远程 6 个新提交(R78 守卫加固等)均不触碰锚点文件集,方案锚点仍有效 |
| A6 | 文档日期 2026-09-24 | git log 核对提交时间(09-24 22:52) | 无误。期间 R64(891542ff1)已勘误测试计数 12→9,本方案沿用 |

**方法论注记**:本轮 A1 即"只声明未验证"的实锤——原方案把未验证项留给了
实施阶段("需要确认"),而该确认在文档提交后 4 天内已被并行工作完成。教训:
方案中的"待确认"项应在提交前清零或显式标注验证方式与责任人;否则文档会
在多写入者并行推进下迅速失真(本次恰好是朝"工作已变少"方向失真)。

---

## 8. 实施状态(2026-09-30 审计轮登记)

| 阶段 | 状态 | 提交 |
|---|---|---|
| P1(H1 FileCache 索引) | ✅ 完成 | `ced9eb9b1`(索引+测试)、`50847b6b8`(补基准,验收项原缺失) |
| P2(H4 spec+Trimmer+ResizeMax+热重载接线) | ✅ 完成 | `9b9061276`/`87dbead05`/`31e468a66` + 后继加固 `964c0060a`(非正配置 fail-safe)、`d141c8036`(子树白名单/symlink/TOCTOU)、`4ba3a3be3`(ResizeMax 缩容描述订正) |
| P3(H2 full 装配) | ✅ 完成 | `5250eede0`(dispatcher 化 initStorageMode + hotZoneOnly runtime + main.go 六处 liteMode() 门控改写 + 运行期开关语义钉死) |
| P4(H3 镜像接线) | ✅ 完成 | `4edc7574f`(两接线点:telemetry persistRequestLog 顶部 + SessionWriterV2;装配闭包注入)、`b67fe45b4`(行为钉死:三件套路径断言 + PG 不可用仍写入)、同日修订轮(批判式复审 F-A:镜像从 INSERT 后挪至 **tx.Commit 成功后**根修孤儿镜像,TestSessionWriterMirror_CommitFailsNoMirror 钉死;F-B:三件套磁盘路径 e2e 测试;见审计 §三.5) |
| P5(H5 指标分维度) | ✅ 完成 | `205864527`(l1_5_by_mode/mirror.by_mode/hotzone.hit_total_by_mode + SetStorageMode/SetHotZoneEnabled 接线,闭合 hotzone_enabled 恒 false 预存缺口) |
| P5(H5 文档:README 增补/ADR amendment/deployment-guide 配置矩阵) | ✅ 完成 | R36(2026-09-30)落地:README「全量模式热区」章节 + ADR-0019 Amendment(2026-09-30) + deployment-guide hotzone 配置矩阵(env/YAML/settings 三通道)+ F4 对账口径入运维文档 |
| E2E 部署级演练(§五.2 唯一实质遗留) | ✅ 完成 | 2026-09-30 local 全栈:full+热区开启发真实流量,①三子树+gunzip 对账(F4 豁免复现)②/metrics/storage 三键(跨重启 L1.5 hits=3)③kill-switch 容器对照 ④PG 硬停镜像 errors=0 持写+恢复健康。新发现部署缺口 D1(env 白名单缺 STORAGE_MODE/HOTZONE_*)与 D2(.env.local 命令式 ADMIN_API_KEY 令部署中止);证据: docs/audit/2026-09-30-hotzone-e2e-deploy-drill.md |

批判式审计全文: docs/audit/2026-09-30-hotzone-p1p2-critical-audit.md
(F1 过期相记账超删已由 21 轮修复 / F2 ced9eb9b1 提交信息 gauge 声明失实留档 /
F4 本条口径修订:§3-H4 改动2 的"遍历范围=整个 HotZone.Dir"实施时改为
**三个受管子树白名单**(cache/session_bodies/requests)——整树遍历会按配额
删除 root 下未知文件,白名单方向为少删不误删;热重载接线实证:settings
GetPlatform* 无缓存直查、hotconfig 仅轮 `llmgw_%` 前缀键,故 §3-H4 改动3
的 hotconfig 接线假设对 storage.hotzone_* 不成立,实施采用 trimmer tick 直查)。
P3+P4+P5(指标)批判式审计: docs/audit/2026-09-30-hotzone-p3p4-critical-audit.md
(§6 验收清单逐项状态 + P3 运行期开关语义定义 + P4 镜像激发位置论证与遗留)。
