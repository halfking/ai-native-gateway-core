# FS-backed Request Store — Phase B

> 状态:已落地(2026-08-26,`internal/fsstore/`)。Phase A 的
> Bleve 索引层被复用,目录与索引同构。
>
> 范围:`internal/fsstore/{fsstore,fallback}.go`

## 目标

在 PG/Redis 不可用时,提供一套文件系统层的"请求记录、供应商、
租户、用户、API key"持久化与查询,接口尽量与现有 PG/Redis 调用方
兼容,以便在 `cmd/gateway/main.go` 启动期根据可用性切换数据源。
**不做**双写 — Phase B 明确建议禁用双写以避免脑裂。

## 目录布局

```
${LLM_GATEWAY_FS_ROOT}/
├── index/
│   ├── entities.bleve/          # provider/tenant/user/key 索引
│   └── requests.bleve/          # request_id 倒排索引
├── entities/
│   ├── providers/<provider_id>.json
│   ├── tenants/<tenant_id>.json
│   ├── users/<user_id>.json
│   └── api_keys/<key_id>.json
├── requests/
│   └── YYYY/MM/DD/<request_id>.json
└── bodies/
    └── YYYY/MM/DD/<request_id>/
        ├── request.jsonl.gz
        └── response.jsonl.gz
```

- `entities/` 走"写时复制 + 原子 rename + flock",并发写同一实体
  不会看到半写文件。
- `requests/` 按日期分片,单天目录里 `<request_id>.json` 是元数据
  + body 指针。
- `bodies/` 单独目录,gzip-compressed JSON-lines,元数据里只存
  指针(避免大对象撑爆索引)。
- `index/` 走 Bleve,与 Phase A 共用 scorch 后端,崩溃后通过
  `RebuildIndexes()` 回扫 `entities/` 与 `requests/` 重建。

## 关键设计决策

| 决策点 | 选项 | 选择 | 理由 |
|---|---|---|---|
| 索引分片 | 全局 / 按日期 / 按 tenant | **按日期(实体按 id)** | 实体 keyspace 很小(数千),索引单文件可承受;request_id 倒排按日期是合理的 GC 边界。 |
| 实体写并发 | flock / CAS / 乐观 | **flock + 原子 rename** | POSIX 安全;Windows 走 `os.Rename` 已有原子语义。 |
| Body 编码 | jsonl / protobuf / msgpack | **jsonl.gz** | 与现有 log 格式一致,运维可手查。 |
| 替代 vs 镜像 | 替代 / fallback / 双写 | **fallback(启动期探测,运行期不切换)** | 避免双写脑裂。 |
| 大对象分块 | 不分块 / 4MB | **不分块** | 先简单;body 超 50MB 再分块。 |
| 索引重建触发 | 启动期 / 周期性 / 命令触发 | **`RebuildIndexes()` 显式调用** | 自动周期重建在生产环境不可控,留给运维决定。 |

## 关键 API(摘要)

```go
store, err := fsstore.New(fsstore.Config{Root: "/var/lib/llm-gateway/fs"})
if err != nil { /* FS store unavailable — fall back to PG */ }

defer store.Close()

// 实体元数据
err = store.PutEntity(fsstore.Entity{ID: "openai", Kind: fsstore.KindProvider, Payload: ...})
e, err := store.GetEntity(fsstore.KindProvider, "openai")
ids, err := store.ListEntities(fsstore.KindProvider)

// 请求生命周期
err = store.PutRequest(fsstore.RequestRecord{ID: "req-abc", StartedAt: time.Now(), ...})
rr, err := store.GetRequest("req-abc")
ids, err = store.ListRequestsInDay("2026-08-26")

// 请求 body 分片
err = store.PutBodyPart("req-abc", time.Now(), fsstore.BodyRequest, chunk)
lines, err := store.ReadBodyPart("req-abc", time.Now(), fsstore.BodyRequest)

// 索引重建(启动期或运维触发)
err = store.RebuildIndexes()

// 启动期 fallback 决策
fb, err := fsstore.NewFallback(ctx, pgPool, fsStore)
switch fb.Mode {
case fsstore.ModePG:  /* PG 主,FS 关闭 */
case fsstore.ModeFS:  /* FS 主,PG 关闭(slog.Warn 提示运维) */
}
```

## 失败模式

| 情况 | 行为 |
|---|---|
| `Root` 不存在 | `New` 自动 `MkdirAll`;若权限拒绝返回 error,fallback 跳过 FS。 |
| 索引文件损坏 | `bleve.Open` 失败 → 自动 `bleve.New`(新索引,旧文件被隔离)。 |
| `flock` 在 Windows 不可用 | 退化为单进程 `sync.Mutex`(见 `keyLock`)。**Windows 不承诺跨进程安全**。 |
| `Rename` 失败 | 写盘失败,error 返回,indexer 不更新。 |
| `RebuildIndexes` 中途崩溃 | 下次启动重新跑;每次 `Index()` 是幂等覆盖。 |
| 同时启两个 gateway 写同一实体 | flock 串行化 → 第二个进程看到完整文件;Bleve 索引可能短暂不一致(以文件为真,索引 `RebuildIndexes` 兜底)。 |

## 切换策略(关键决策)

- **启动期探测**:`fsstore.Probe(ctx, pool)` 跑 `SELECT 1`,
  超时 5s。成功 → PG 主;失败 → FS 主,**关闭 PG pool**。
- **运行期不切换**:PG 跑着的时候故障,**不**自动切到 FS
  (否则需要双写,会脑裂)。运维要么重启走 fallback 路径,
  要么直接修 PG。
- **过渡期禁用双写**:不要写"PG 也写一份 FS"。这不是
  fsstore 的限制,是 Phase B 的设计原则 — handoff §6.1 已明说。

## 后续

- Phase C(未规划):把 domain 调用方都改成 `fb.Mode == ModePG ? pg : fs`
  分支。当前 Phase B 提供基础设施,不主动改 PG 调用方
  (那是大范围的工作,需要单独的 plan)。
- Body 体积监控:加 metric `fsstore_body_bytes_written_total{kind}`,
  触发 50MB 阈值告警。
- 跨实例共享:目前 FS 是单实例,后续若要 multi-master 共享,
  引入 etcd/Redis 锁替换本地 flock。
