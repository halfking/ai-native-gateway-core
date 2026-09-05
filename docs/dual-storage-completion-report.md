# 双模式存储架构 — 任务完成报告

- **日期**: 2026-09-05
- **任务来源**: [dual-storage-implementation-tasks.md](dual-storage-implementation-tasks.md)（规划文档，15 个任务、6 个任务组）
- **配套设计文档**: [design/](design/)（lite 模式存储设计、实施清单、速查）
- **运维文档**: [storage/README.md](storage/README.md)、[storage/deployment-guide.md](storage/deployment-guide.md)、[storage/troubleshooting.md](storage/troubleshooting.md)
- **布局决策**: [adr/2026-09-05-dual-mode-storage-package-layout.md](adr/2026-09-05-dual-mode-storage-package-layout.md)

## 一、任务总结

实现网关双模式存储：**full 模式**（PostgreSQL + Redis，生产/多实例）与 **lite 模式**
（SQLite + 本地文件 + 内存，本地开发/单机零外部依赖）。执行方式为主代理编排 +
13 个子代理分 5 波并行，随后经三轴审计（Standards / Spec / 正确性与安全）并修复
全部 P0/P1 问题。

| 层 | 组件 | lite 实现 | full 实现 |
|----|------|-----------|-----------|
| 接口 | `storage`（SessionStore/BodiesStore/TurnsStore/RequestLogStore/StateStore） | — | — |
| 工厂 | `storage/factory` | SQLite + FileBodiesStore（惰性单例）+ MemoryStateStore（惰性单例） | ErrNotImplemented 桩（生产 full 路径由 cmd/gateway 既有 pgx/redis 装配承担，不经工厂） |
| 元数据 | `storage/sqlite` | WAL + DSN/ConnectHook 双通道 PRAGMA、参数化查询 | — |
| 大对象 | `storage/file` | 异步原子写（tmp+rename、优雅排空）+ gzip 分层目录 | — |
| KV | `storage/memory` | TTL + 双检惰性删除 + janitor | — |
| 缓存 | `domains/session/v2` | L1(内存) → L1.5(FileCache) → L3 | L1 → L2(Redis) → L3 |
| 后台 | `bg` | CacheTrimmer(1h)/BodiesTrimmer(6h) | 既有 retention worker |
| 观测 | `monitoring` + `GET /metrics/storage` | 各层命中率、L3 延迟、写入量（admin token 鉴权） | 同左（file_cache 为 null） |

## 二、特性需求清单（验收状态）

### 功能需求

| # | 需求 | 状态 |
|---|------|------|
| F1 | 统一存储接口 + 双模式工厂，按配置切换 | ✅ |
| F2 | lite：SQLite 会话/轮次/请求日志元数据存储（WAL、UPSERT、分页） | ✅ |
| F3 | lite：bodies gzip 文件存储（分层目录、范围读、会话级删除、压缩率 >70%，实测 96.7%） | ✅ |
| F4 | lite：内存 KV 状态存储（TTL、1000 并发压测、-race） | ✅ |
| F5 | 异步文件写入器（批量、原子写、优雅关闭排空队列、>5000 writes/s，实测 ~12K） | ✅ |
| F6 | L1.5 文件缓存（TTL、按字节容量 + LRU 淘汰、进程重启后仍命中） | ✅ |
| F7 | SessionCacheV2 多层链路 L1→L1.5→L2→L3，双层回填、全层失效 | ✅ |
| F8 | 双模式配置（YAML + `LLM_GATEWAY_*` env、默认值、Validate、env-only 部署） | ✅ |
| F9 | 主程序按模式装配：lite 旁路 PG、FileCache、trimmer 启动、SIGTERM 优雅关闭 | ✅ 真机冒烟通过 |
| F10 | 后台清理（缓存按小时、bodies 按保留期删会话目录） | ✅ |
| F11 | 存储指标暴露 + 缓存各层命中打点 + bodies 写入打点 | ✅（打点为审计后补接线） |
| F12 | 端到端集成测试 + 性能基准（持久化一致性、100+100 并发、配置切换） | ✅ |

### 非功能需求

| # | 需求 | 状态 |
|---|------|------|
| N1 | lite 命中延迟（缓存 <10ms） | ✅ L1 199ns / L1.5 19μs |
| N2 | 并发安全（全包 `-race`） | ✅ |
| N3 | full 模式行为零变化（`initStorageMode` 非 lite 返回 nil，装配原样） | ✅ 审计逐行核对 |
| N4 | 生产镜像可构建（CGO） | ✅ 修复后（见审计 A1） |
| N5 | 单测覆盖率 >80% | ✅ factory 93% / file 88% / memory 98% / sqlite 85% / monitoring 97%（bg 为含历史文件的整包口径） |

### 规格覆盖度

Spec 轴审计实测：规划文档 53 条验收标准达成 **约 48 条（≈90%）**。未达成项均为
规划假设与真实代码库不符的有意边界，已记录：lite 请求路径未整体切换到 SQLite
（路由/凭证等子系统仍走既有 no-DB 降级，属后续波次）；StateStore 未接入 HTTP
装配层；lite→full 数据迁移测试（full 侧工厂为桩）。

**B2 接线更新（2026-09-05 审计后续工作包）**：telemetry 请求日志管道已桥接到
存储工厂——`telemetry.Client.SetRequestLogSink` 注入缝 + `cmd/gateway/lite_telemetry_sink.go`
实现：每条请求日志（in_progress INSERT 与终态 UPDATE）幂等 UPSERT 到 SQLite
`request_logs`；终态且带会话 ID 与原文的条目额外记一轮会话 journal（`sessions` 行 +
`session_turns` 元数据 + FileBodies `turn_N` 原文，轮号重启后从落盘最大值续排）。
lite 部署的请求/会话内容不再空转，`/metrics/storage` writes 反映真实写入。
仍未接线：StateStore（MemoryStateStore 进程内 KV，无天然生产消费者）、
`retention.request_logs_days` 清理执行者（SQLite request_logs 仍无自动 trim）。

## 三、审计记录与修复对照

三轴并行审计：Standards（规范符合性）、Spec（规格符合性）、正确性与安全。

### 已修复

| 编号 | 级别 | 发现 | 修复 |
|------|------|------|------|
| A1 | P0 | 生产 `Dockerfile` 以 `CGO_ENABLED=0` 构建 `./cmd/gateway`，引入 CGO 包 go-sqlite3 后**编译期直接失败** | Dockerfile 改 `CGO_ENABLED=1` + 显式 `apk add gcc musl-dev`；同步修正 `Dockerfile.local-arm64`、`tests/e2e/test_01_build_pipeline.sh`、`DEPLOYMENT_RULES.md` 等活跃构建路径；`cmd/gateway-v2` 核实仅依赖纯 Go 接口包、不受影响 |
| A2 | P1 | `FileBodiesStore` 路径遍历：tenantID/sessionID 含 `../` 时 `filepath.Join` Clean 可逃逸 baseDir，Write/Delete（RemoveAll）可读写删任意路径 | 新增 `validPathID` 白名单校验（拒绝空/`.`/`..`/路径分隔符），Write/Read/Delete 入口统一拦截 + 回归测试 `TestPathTraversalRejected` |
| A3 | P1 | 监控指标零打点：`Record*` 无生产调用方，`/metrics/storage` 恒为零，运维必然误读 | `cache_v2.go` Get 各分支注入 L1/L1.5/L2 命中打点 + L3 延迟；`FileBodiesStore.Write` 接 `RecordWrite`（字节数 + 错误） |
| A4 | P1 | `FileCache.sizeUsed` 与 CacheTrimmer 并行删除产生永久漂移：长期运行后每次 Set 触发全树 Walk 并误删活跃文件 | `ensureSpaceLocked` 溢出遍历时以磁盘实况重置记账（自愈）+ 回归测试 |
| A5 | P1 | `config.example.yaml` 默认 `storage_mode: "lite"`，而该文件被复制为镜像内 config.yaml，照抄即静默禁用 PG | 默认改 `full`，注释说明切换方法 |
| A6 | P1 | 新增根级 `storage/`、`monitoring/` 包与 ADR-0002 包布局收敛方向冲突 | 补 ADR-0019 记录受控例外、依赖方向约束与未来迁移路径（不做高风险重构） |
| A7 | P2 | ConnectHook 直接拼接 PRAGMA 名，配置可注入任意 SQL 片段 | `OpenSQLite` 入口 `validPragmaName` 白名单校验 + 注入拒绝测试 |
| A8 | P2 | `request_logs.has_body` 列可空但裸 `int64` 扫描，NULL 行报错 | SELECT 改 `COALESCE(has_body, 0)` |
| A9 | P2 | `NormalizeMode` 不 trim，`" lite "` 落入 full 分支无提示 | `strings.TrimSpace` + 测试 |
| A10 | P2 | `sqlitePragmasFromLiteConfig` 匿名 struct 参数在 config 加字段时静默漂移 | config 侧定义具名类型 `SQLitePragmasConfig`，装配层直接引用 |

### 已记录、不在本期修复（P2/边界）

- 中文错误串占比低但存在（新代码 36/97），仓库惯例为英文错误串，统一工作后续做；
- trimmer `Start` 为阻塞式（规格要求），与 bg 包非阻塞惯例不同（文件头已注明）；
- `retention.request_logs_days` 有配置无执行者：lite 模式 request_logs 表暂无自动
  清理 worker，建议后续补 request log trimmer 或装配期拒绝该字段；
- `LiteStorageConfig` 无 SQLite 连接数上限配置（由 busy_timeout 兜底）；
- `GetRequest` 无租户范围限定（尚未 API 化，未来暴露接口时必须补 tenant 校验）；
- `DeleteSession` 不级联清理 turns/bodies（依赖 BodiesTrimmer 时间兜底）；
- `sqlite_pragmas` 已可配置且已接线到工厂，但 env 侧无对应变量。

## 四、验证结果

- `go build ./...` 全仓通过；`go vet`（storage/...、config、monitoring、tests/integration、cmd/gateway、bg、domains/session/v2）干净；gofmt 干净。
- `-race` 测试全绿：`storage/...`、`config`、`monitoring`、`tests/integration`、`cmd/gateway`、`domains/session/v2`（Cache）、`bg`（Trimmer）。
- lite 模式真机冒烟（无 PG/Redis）：启动 `/healthz` 200；`GET /metrics/storage` 返回真实 JSON；SQLite 落盘且 WAL 生效；trimmer 启动；SIGTERM 优雅关闭 exit 0。
- 跨平台：`GOOS=linux/windows` 交叉编译确认 vendor 变更未破坏其他包；sqlite 为 CGO 包，交叉编译失败属预期（容器内构建，主 Dockerfile 已改 CGO=1）。

## 五、交付物清单

**代码**：`storage/`（接口/类型/错误 + `factory/`、`sqlite/`、`file/`、`memory/`）、
`domains/session/v2/cache_v2.go`（多层集成）、`cache_v2_file.go`、`config/storage.go`、
`cmd/gateway/storage_mode_init.go`、`storage_metrics_endpoint.go`、`main.go`（4 处最小插入 + 1 行端点注册）、
`bg/cache_trimmer.go`、`bg/bodies_trimmer.go`、`monitoring/storage_metrics.go`、
`tests/integration/dual_mode_test.go`、`benchmark_test.go`、`scripts/start-{lite,full}.sh`、
`Dockerfile`（CGO 修复）、`config.example.yaml`、`go.mod`（go-sqlite3）+ vendor 同步。

**B2 接线（2026-09-05）**：`domains/hooks/observability/telemetry/client.go`
（RequestLogSink 注入缝，full 模式零变化）、`cmd/gateway/lite_telemetry_sink.go`
（lite sink：request_logs UPSERT + 会话 journal + FileBodies 原文）、
`storage/sqlite/request_log_store.go`（INSERT→幂等 UPSERT）、
`storage/file/async_writer.go`（fsync 后 rename，B5）。

**测试**：新增 60+ 测试用例（含 -race、路径遍历/注入/记账自愈回归）与 5 个基准。

**文档**：`docs/storage/`（README / deployment-guide / troubleshooting）、`docs/design/`
（既有设计文档）、本报告、`docs/adr/2026-09-05-dual-mode-storage-package-layout.md`。
