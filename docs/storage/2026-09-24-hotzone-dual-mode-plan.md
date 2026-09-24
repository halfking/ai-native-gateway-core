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

**改动**:
1. 新增 `HotZoneConfig`(config/storage.go):`Enabled`(默认 full=true, lite=true 复用现有 L1.5)、`Dir`(默认 `./data/hotzone`)、`RetentionHours`(默认 7)、`MaxSizeGB`(默认 1)、`RequestMirror`(默认 true)。
2. `initStorageMode` 中,full 且 HotZone.Enabled 时也创建 storageRuntime(新字段 `hotZoneOnly=true` 与 lite runtime 区分:full 不创建 SQLite 工厂、不收口 Redis env、不启动 LiteRetentionWorker/ConsistencyWorker)。
3. full 分支创建:FileCache(dir=HotZone.Dir/sessions? 否——缓存文件是 SessionStateV2 JSON,与 lite 的 CacheDir 语义一致,用 `HotZone.Dir/cache`)+ FileBodiesStore 复用实例(`HotZone.Dir/session_bodies`,codec 沿用 gzip)。
4. `newSessionCacheV2` full 分支改走 `v2.NewSessionCacheV2WithMode(db, redisAddr, redisDB, storage.StorageModeFull, r.fileCache)`——需要在 `cache_v2.go` 确认 WithMode 的 full 分支保留 L2 Redis(当前实现按 mode 摘除 L2,full+fileCache 组合是新增路径,读顺序应为 L1→L1.5→L2→L3,写回填 L1+L1.5)。
5. 启动日志新增 hotzone 配置快照;Shutdown 复用现有 trimmer 生命周期。

**不变式**: `LLM_GATEWAY_STORAGE_MODE` 未设置且 HotZone.Enabled 默认值=full 开启——这是**行为变更点**,必须以 settings/env 双保险提供 `LLM_GATEWAY_HOTZONE_ENABLED=false` 一键关闭,且回滚零残留(热区目录只是缓存,可直接删)。

**验收**: full 模式启动后 `data/hotzone/` 出现与 lite 同构目录;`/metrics/storage` 暴露 L1.5 指标;关闭开关后行为与当前 main 完全一致(回归对照)。

### H3 请求侧镜像写入(G5)

**现状锚点**: `storage/file/bodies_store.go`(仅 session bodies);request body 当前只进 PG(`request_logs_bodies_hot` 8 小时热表 → 月分区)

**改动**:
1. 新增 `storage/file/request_mirror.go`:轻量镜像器,入参 (requestID, tenantID, direction, body),路径 `{hotzoneDir}/requests/{tenantID}/{date}/{requestID}.{req|resp|out}.json.gz`,复用 AsyncFileWriter 与 codec。
2. 接线点:流式终态落库处(streaming handler 的 bodies 写入事务提交后,fire-and-forget 投递镜像,失败仅计数不阻断主链路——与 FileBodiesStore 的 fail-open 语义一致)。
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
3. 热重载:hotconfig 轮询到变更后调用 `fileCache.ResizeMax(newBytes)`(新方法,只增不减立即生效;缩容交由下一轮 trimmer 执行)与 trimmer 的 retention 原子替换。
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
| P3 | H2 full 热区装配(mode-aware 缓存接线) | P2 | 1.5 天 |
| P4 | H3 请求镜像器 + 接线 | P2 | 1.5 天 |
| P5 | H5 指标/文档 + 全量回归(9 项 lite 套件 + full 热区新增 E2E) | P3,P4 | 1 天 |

总计约 6 个工作日。P1/P2 可双线并行。

## 5. 风险与回滚

| 风险 | 缓解 |
|---|---|
| full 模式默认开启热区属于行为变更 | `LLM_GATEWAY_HOTZONE_ENABLED=false` 一键关闭;热区纯缓存语义,删除目录即回滚 |
| 热区磁盘写放大(full 模式 PG 已双写) | 1GB 硬上限 + fail-open;镜像写入带熔断计数(连续失败 N 次后静默跳过并告警) |
| FileCache 索引与磁盘漂移 | 索引仅作加速视图,任何不一致按 miss 兜底;记账自愈路径已有先例(2026-09-05 审计 B3) |
| SessionCacheV2 full+fileCache 新组合回归 | 该构造路径新增单测:断言 L2 仍在读链、L1.5 miss 后穿透 L2 |

## 6. 验收清单

- [ ] lite 模式 9 项既有套件全绿(回归门禁)
- [ ] full 模式:`data/hotzone/` 目录结构与 lite 同构,7h/1GB 默认值生效
- [ ] 读路径顺序实测:内存 → 本地文件 → (Redis) → PG,可通过指标区分命中层
- [ ] `storage.hotzone_max_size_gb` 热重载生效且 trimmer 先删最旧
- [ ] 关闭热区开关后 full 模式行为与当前 main 一致
- [ ] PG 故障演练:热区持续写入,恢复后无数据结构性损坏
