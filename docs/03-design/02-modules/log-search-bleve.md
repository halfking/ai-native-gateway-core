# Log Search (Bleve) — Phase A

> 状态:已落地(2026-08-26,handoff `/tmp/handoff-20260825-195644.md`)
> 范围:`internal/logging/bleve_fanout.go` + `admin/logsearch/` + `cmd/bleve-backfill/`

## 目标

为已有 slog → lumberjack 管道增加一层 Bleve 索引,新增 HTTP
`/admin/logs/search` 端点,支持按 `request_id` / `tenant_id` /
关键字 / 时间区间 / 正则 / 模糊 / 分页查询日志正文,并提供历史
`.gz` 一次性回灌工具。后续阶段 B 的"请求记录搬到文件系统"会
复用同一套 Bleve 索引层,所以这套索引层从设计开始就要为请求级
数据(而非纯日志)留余地。

## 组件

```
slog.JSONHandler ─► BleveFanoutHandler.Handle  (sync, parent first)
                          │
                          ├──► lumberjack + stderr  (unchanged)
                          │
                          └──► ring channel  ─►  background indexer
                                                        │
                                                        ▼
                                                   bleve.Batch
                                                        │
                                                        ▼
                                          $LLM_GATEWAY_LOG_INDEX_DIR/
                                                (scorch, mmap)
```

### 1. `internal/logging/bleve_fanout.go`

- `BleveFanoutHandler`:`slog.Handler` 包装,先同步转发到
  父 handler(保证 lumberjack 写盘不被 Bleve 写入阻塞),再把
  record 异步送入有界 ring channel。
- `bleveIndexer`:后台 goroutine,批量调用 `bleve.Index.Batch`,
  周期性 flush(默认 500ms 或 256 条),记录 `indexed / dropped /
  failed` 三个计数器。
- 失败隔离:channel 满则丢(增计数器),bleve.Batch 失败只走 stderr
  + 计数器,**不会冒泡到 slog 链路**。
- 生命周期:`Init` 时若 `LLM_GATEWAY_LOG_BLEVE_ENABLED=true` 才
  启用;`Shutdown` 关闭 indexer,drain channel,关闭 bleve 索引。

### 2. `admin/logsearch/logsearch.go` + `admin/handler.go`

- `GET /api/admin/logs/search`(admin 权限,跟同前缀的 `/api/admin/logs/*`
  一致):
  - `q` 子串匹配 msg
  - `tenant` / `level` term filter
  - `from` / `to` RFC3339 date-range
  - `regex` 顶层 regex
  - `fuzzy=1` 切换到 fuzzy match
  - `page` / `size`(size 上限 200)
- `GET /api/admin/logs/search/status`:返回 fan-out 计数器
  (`queue_depth / records_indexed / records_dropped / records_failed`)。
- Indexer 未启用时返回 `enabled=false`,UI 端降级即可。

### 3. `cmd/bleve-backfill/`

- 独立 Go 命令,扫 `gateway.log` + `gateway-*.log(.gz)?`,逐行
  JSON 解析 → 同构索引文档。
- 并发:`--workers` (默认 4) 个文件解码 worker + 1 个 batch writer。
- `--since RFC3339` 增量回灌(运维每次只想回灌最近 N 天)。
- `--dry-run` 跳过 Bleve 写入,只统计可索引量。
- 复用与 fan-out 相同的 doc shape(见 `addToBatch` 和
  `decodeFile` 中的字段提升逻辑),保证 live + backfill 的索引是
  同构的。

## 字段映射

每条 slog record 进入 Bleve 时的文档结构:

```json
{
  "ts":         "2026-08-26T10:00:00.123456Z",
  "level":      "info|warn|error|debug",
  "msg":        "<record message>",
  "raw":        "<serialized JSON envelope>",
  "request_id": "<if present, lifted to top level>",
  "tenant_id":  "<if present, lifted to top level>",
  "user_id":    "<if present, lifted to top level>",
  "session_id": "<if present, lifted to top level>",
  "trace_id":   "<if present, lifted to top level>",
  "model":      "<if present, lifted to top level>",
  "provider_id":"<if present, lifted to top level>",
  "method":     "<if present, lifted to top level>",
  "path":       "<if present, lifted to top level>",
  "attrs":      { ... 全量 attrs(供未来高亮/补查) ... }
}
```

文档 ID 是 `ts.UnixNano()`(字符串),所以同一时间戳的记录会
互相覆盖 — 可接受,因为 logger 在毫秒级并发场景下,duplicate
文档不影响计数/排序;后续如要保留每条,可改成 monotonic id。

## 配置(环境变量)

| 变量 | 默认 | 说明 |
|---|---|---|
| `LLM_GATEWAY_LOG_BLEVE_ENABLED` | `false` | 是否启用 fan-out。关闭时 `/api/admin/logs/search` 返回 `enabled=false`。 |
| `LLM_GATEWAY_LOG_INDEX_DIR` | `<log_dir>/../bleve` | Bleve scorch index 目录。绝对路径更稳。 |
| `LLM_GATEWAY_LOG_BLEVE_BUFFER` | `8192` | ring channel 容量,满则丢。 |
| `LLM_GATEWAY_LOG_BLEVE_FLUSH_MS` | `500` | batch flush 周期。 |
| `LLM_GATEWAY_LOG_BLEVE_BATCH` | `256` | 累积这么多条强制 flush。 |

## 失败模式

| 情况 | 行为 |
|---|---|
| Bleve 索引目录无写权限 | `EnableBleveFanout` 走 stderr 报错,**fallback 到纯 lumberjack 管道**。 |
| 索引文件损坏 | `bleve.Open` 失败 → 自动 `bleve.New`(新索引,旧文件被隔离)。 |
| `Index.Batch` 失败 | stderr 报错 + `records_failed` 计数,indexer goroutine 继续。 |
| Channel 满 | 当前 record 被丢弃 + `records_dropped` 计数。slog 链路零感知。 |
| Bleve 搜索语法错(非法 regex) | handler 返回 500 + 错误信息,UI 端展示。 |
| Indexer 未启用 | 端点返回 200 + `enabled=false`,不阻塞 admin UI。 |

## 运维命令

```bash
# 1) 启用 fan-out(写入 logging_init 之前的 env,或者 docker-compose)
export LLM_GATEWAY_LOG_BLEVE_ENABLED=true
export LLM_GATEWAY_LOG_INDEX_DIR=/var/lib/llm-gateway/bleve

# 2) 启动 gateway,索引从此刻开始累加
./gateway

# 3) 一次性回灌历史日志(假设 gateway 已停止或索引独立目录)
./bleve-backfill \
  --log-dir /var/log/llm-gateway \
  --index-dir /var/lib/llm-gateway/bleve \
  --since 2026-01-01 \
  --workers 8 \
  --batch 1024

# 4) 调 admin API
curl -H "Cookie: $LLM_GATEWAY_ADMIN_COOKIE" \
  "https://gateway.example.com/api/admin/logs/search?q=credential_exhausted&tenant=acme&from=2026-08-25T00:00:00Z&size=20"

curl -H "Cookie: $LLM_GATEWAY_ADMIN_COOKIE" \
  "https://gateway.example.com/api/admin/logs/search/status"
```

## 已知限制

- 单进程 fan-out:多 gateway 实例同时跑,各自维护独立索引;
  全局搜索需要后续在 phase B 里把索引也搬到 FS 或共享对象存储。
- 高亮 / 聚合暂未启用:搜索返回 `raw` JSON 行即可,UI 端正则
  抽取高亮片段。
- 不支持向量搜索 / 复杂 boolean:用 bleve default mapping 起步,
  后续若需要可加自定义 `IndexMapping`。
