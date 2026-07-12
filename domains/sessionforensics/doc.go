// Package sessionforensics 是面向运维平台（admin web）和 CI 工具的
// 会话调试 / 回放 / 摘要 工具集。
//
// 它汇集了三个曾分散在不同模块的能力：
//
//  1. 会话导出：从生产 request_logs + request_logs_bodies + session_summaries
//     拉到一台机器，形成可下载的迁移包；
//  2. 会话回放：把导出包喂给本机的 compression.SessionCompressor.Prepare，
//     验证 delta-append / 缓存命中 / tools_cached / strip / windowing 的实际行为；
//  3. 标题 + 摘要：触发 sessionsummary 包对导出会话做 LLM 摘要（含高精度标题），
//     写到 session_summaries（同时支持旧 session_titles 双写）。
//
// 设计目标：
//
//   - 一套 Go API 既能被 admin/session_export.go 复用，也能在 CI / 测试中用
//     `NewClient()` 直接调；不再用 Python 一行一行 psql 的方式拉数据。
//   - 与已有的 admin/session_export.go 共享导出 build 函数（buildExportFromDB），
//     避免代码重复。
//   - 不依赖真实 Redis / DB：所有层都可以注入 mock，便于离线回放测试。
//
// 子包文档：
//
//   - types.go         — 公开数据结构（SessionPack、ReplayReport、Summary 等）
//   - export.go        — 从 PostgreSQL 拉生产会话的核心逻辑
//   - client.go        — HTTP 客户端：跨机拉取 / 推到 staging
//   - replay.go        — 把 pack 喂给 SessionCompressor + 输出报告
//   - summarize.go     — 标题 + 摘要的自动触发与 fallback 链
//   - audit.go         — 计算会话管理指标（MissingId 计数、Compression 命中）
//
// 依赖方向：
//
//	sessionforensics → compression  (回放)
//	sessionforensics → session      (CreateV2)
//	sessionforensics → sessionsummary (摘要生成)
//	sessionforensics → admin        (admin API 内部复用)
package sessionforensics
