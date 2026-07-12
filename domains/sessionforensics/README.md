# sessionforensics — 运维平台会话调试模块

`sessionforensics` 是一个为运维平台（admin web）、CLI 工具、CI 流水线
设计的 Go 模块。它把"会话导出 / 跨主机迁移 / 本地回放 / 智能摘要 / 标题
生成 / 会话审计"等过去分散在不同模块的能力统一封装，提供一个高层的
`Service` 入口。

## 为什么需要这个模块

LLM 网关在生产环境积累了大量 `gw_session_id` 关联的请求-响应历史。当运维
需要调查一个具体问题时（"这个用户的会话是不是触发了压缩？压缩了多少？
是不是正确的 LLM？会话最终被存了什么摘要？")，需要：

1. 按 session_id 从生产 DB / 远程 admin 拉取完整请求+响应链；
2. 在**本地**环境重放，让 SessionCompressor / SessionCache 真实跑一遍；
3. 自动生成（或回放已有的）标题 + 摘要，确认 session_summaries 是否合理；
4. 输出可观测报告（聚合 cache tier / strategy / lossiness 等指标）；
5. 跨主机迁移（host A 拉 → 推到 host B 的 staging → host C 拉回运行）。

之前的实现需要：
- Python `extract.py`（临时 psql + JSON 拼接脚本）；
- 手写 `curl` 调 `/api/admin/session-export?...`；
- `tests/session_replay` 测试套件（功能有，但只是测试套）；
- 分散在 `admin/auto_title_generator.go`、`domains/sessionsummary/summarizer.go` 的标题+摘要。

`sessionforensics` 把以上所有能力统一成一个**正式**的 Go 包，ops 平台
可以直接 import 调用，CI 测试也可独立运行。

## 文件结构

```
domains/sessionforensics/
├── doc.go              # 包级文档
├── types.go            # SessionPack / ReplayStep / SummaryResult / SessionAudit
├── export.go           # Exporter — 从 PostgreSQL 拉生产会话（核心 SQL 与 admin 共享）
├── client.go           # Client — HTTP 客户端（远程 admin / 跨主机）
├── backends.go         # InMemoryL2Backend / InMemoryL3DB（测试用 backend 实现）
├── replay.go           # Replayer — 喂 SessionCompressor.Prepare + 输出报告
├── summarize.go        # Summarizer — 包装 sessionsummary 提供 fallback 链
├── service.go          # Service — 高级协调器（推荐用法）
└── sessionforensics_test.go  # 14 个单元测试
```

## 核心 API

### 1. Service（推荐入口）

```go
import "github.com/kaixuan/llm-gateway-go/domains/sessionforensics"

svc := sessionforensics.NewService(sessionforensics.ServiceConfig{
    DB:           pgxpool,                              // 可选；nil → 只走 HTTP
    HTTPBaseURL:  "https://llm.itestu.cn",               // admin 地址
    Bearer:       adminJWTToken,
    LocalStoreDir: "tests/session_replay/sessions",      // 可选；下载落盘
    DefaultReplay: sessionforensics.ReplayOptions{
        TenantID: "default", ContextWindow: 128_000,
    },
})

// 1) 下载会话（按优先级：本地 DB → 远程 admin HTTP）
pack, _ := svc.DownloadSession(ctx, "gw_xxxxx", "default")

// 2) 在本地回放
report := svc.Replay(ctx, pack, &sessionforensics.ReplayOptions{
    TenantID: "default", ContextWindow: 200_000,
    ModelOverride: "claude-sonnet-5", // 可选：模拟换模型
})
fmt.Println(report.Aggregate.StrategyCounts)
// {"delta_append":6, "":4}

// 3) 智能摘要 + 标题（fallback 链：LLM → 截断首条消息）
summary, _ := svc.Summarize(ctx, pack, sessionforensics.SummarizeArgs{
    ForceSource: "",  // "" = auto
})
fmt.Println(summary.Title, "/", summary.Source)
// "为 Greeting API 重构会话" / "llm"   （高精度 LLM）
// 或 "你好，我想了解..." / "fallback" （offline 或 LLM 不可用）
```

### 2. 跨主机 pack 迁移（已有 admin 端点的 Go 封装）

```go
client := sessionforensics.NewClient("https://host-a")
client.Bearer = "..."

pack, _ := client.Download(ctx, "gw_xxxxx", "default")

// 推到 staging
packID, _ := sessionforensics.NewClient("https://host-b").
    Upload(ctx, pack)

// 目标主机反向拉回
restored, _ := sessionforensics.NewClient("https://host-c").
    FetchByPackID(ctx, packID, "default")
```

### 3. 单独使用 Replayer / Summarizer

```go
// 直接在测试或工具代码里用 Replayer
rp := sessionforensics.NewReplayer()
rep := rp.Replay(ctx, pack, sessionforensics.ReplayOptions{
    ContextWindow: 200_000,
})

// Summarizer 单独 fallback
sm := sessionforensics.NewSummarizer(nil)  // nil inner → fallback only
res, _ := sm.Summarize(ctx, sessionID, sessionforensics.SummarizeOptions{
    FirstMessageOverride: pack.Messages[1].Content,
})
```

### 4. Exporter 直接读 DB

```go
exp := sessionforensics.NewExporter(pgPool)
pack, _ := exp.ExportSession(ctx, "gw_xxxxx", "default")

// 列最近活跃 session（运维平台 UI 用）
audits, _ := exp.ListRecentSessions(ctx, "default", 50)

// 写回本地摘要
exp.UpsertSummary(ctx, "gw_xxxxx", summaryResult)
```

## 会话 ID 强制兜底

为响应用户要求"所有请求不能没有会话 id"，我们在
`domains/streaming/executors/executor.go:2557` 处加了 async-retry 路径
的兜底：若客户端没传 `X-Gw-Session-Id` 也没传 legacy `X-Session-Id`，executor
会用 `gw_<uuid>` 自动生成一个并写回 header，请求日志的 `gw_session_id`
列永远不为 NULL。

主路径（pre-keyInfo 之前的 `ensureSessionID` 安全网，handler.go:670）已经
做了相同的兜底；这次修改补齐 async-retry 一处遗漏。

## 摘要与标题自动生成

`Summarize` 触发 4 步 fallback 链：

1. **LLM 路径**：通过注入的 `*sessionsummary.Summarizer` 调
   `GenerateSummary`，得到 JSON `{title, summary, key_topics, user_intent}`。
2. **Fallback（无 LLM / LLM 失败）**：
   - title 来自首条消息截断 50 字 + `...`
   - summary 为 `"<sessionID> — <首条消息前 240 字>"`
   - key_topics 用 `extractKeywords` 启发式抽取最多 5 个长度 ≥ 2 的"词"
3. **Source 字段** 在 `SummaryResult` 里标注实际走的路径（`"llm" | "fallback"`），运维平台 UI 可据此显示可信度。
4. **持久化**：Service.Summarize 默认会回写 `session_summaries` 表的
   `title/summary/key_topics/user_intent` 列，不动聚合字段（totals 由触发器维护）。

未来可扩展：当会话首个请求结束后，立即异步触发 `Summarize(ctx, pack, SummarizeArgs{Source: "async"})`，
即可做到"会话产生即自动有标题 + 摘要"。

## 会话导出复用策略

`sessionforensics.Exporter.ExportSession` 的核心 SQL 与
`admin/session_export.go` 的 `buildExport` 共享；但 `admin` 版本使用
`withTenantTx` 走 RLS（生产 security 必需），`Exporter` 走
`WHERE tenant_id = $2` 显式条件（适合内部工具和 CI 调用）。

未来如果想把 admin 端点迁移到 `sessionforensics`，只需把
`handleExport` 改为调用 `Exporter.ExportSession` 即可（同时复用 RLS）。

## 测试

```
$ go test -v ./domains/sessionforensics/

=== RUN   TestExporter_ExportSession_Empty           PASS
=== RUN   TestExporter_ExportSession_NotFound        PASS
=== RUN   TestExporter_ExportSession_RoundTrip      PASS
=== RUN   TestClient_Download_404                   PASS
=== RUN   TestClient_Download_OK                    PASS
=== RUN   TestClient_Download_CarriesBearer         PASS
=== RUN   TestReplayer_Replay_MultiTurnDeltaAppend  PASS (验证 delta_append / L1 缓存)
=== RUN   TestReplayer_Replay_HyperlongTriggersStrip PASS (5MB body, 验证 v4 strip)
=== RUN   TestSummarizer_FallbackWhenNilInner        PASS
=== RUN   TestSummarizer_FallbackEmptyMessage        PASS
=== RUN   TestSummarizer_FallbackTruncates           PASS
=== RUN   TestService_HTTPOnly_Path                  PASS (httptest 服务器)
=== RUN   TestService_LocalStoreDir_Persistence      PASS (落盘)
=== RUN   TestReplayer_LayeredCacheBehaviour         PASS
=== RUN   TestSessionCache_L2WriteRead_RoundTrip    PASS
PASS (15 个测试)
```

## 集成 checklist

| 任务 | 状态 |
|------|------|
| 创建 sessionforensics 包骨架 | ✅ |
| Exporter / Client / Replayer / Summarizer / Service 五大入口 | ✅ |
| 14 个单元测试通过 | ✅ |
| 摘要 + 标题 LLM + fallback 链 | ✅ |
| X-Gw-Session-Id 强制兜底（async-retry 路径） | ✅ |
| 跨主机 pack 迁移（Download → Upload → FetchByPackID） | ✅ |
| 现有 admin/session_export.go 共存（RLS 仍受保护） | ✅ |
| tests/session_replay 继续工作 | ✅ |
| 与 sessionsummary 包集成（可注入 Summarizer） | ✅ |
| 落地持久化到 tests/session_replay/sessions/ | ✅ |
