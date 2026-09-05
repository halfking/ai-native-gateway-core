# 特性需求与任务总结：会话快照增强 + 请求详情修复 + schema 655 对账

> 状态：已完成并合入 main（本文档为 2026-09-05 审计轮的最终汇总）
> 关联提交：`fc28e27b8`（FR-1）→ `2b0384d64`（FR-2/FR-4 + 审计 A4/A5/A6）→ `6d79a2863`（审计 A1/A2/A3）→ `f27e412b9`（审计 A7）→ 本轮审计修正（字段计数守卫等）

## 1. 任务背景

总览页「实时请求流」点击色块展开请求详情时出现两个故障（2026-09-04 本地环境截图）：

1. `/api/logs/:id: HTTP 404 request log not found` —— 请求时间、供应商等字段为空
2. `sessions/:id/snapshot: HTTP 500 query snapshot failed: column ss.session_key does not exist (SQLSTATE 42703)`

## 2. 特性需求整理

### FR-1 会话快照端点字段增强（已完成 · fc28e27b8）

`GET /api/admin/sessions/<id>/snapshot` 从 10 个字段扩展到 23 个（fc28e27b8 新增 13 个）：

| 类别 | 字段 |
|------|------|
| Token 指标 | `total_tokens` |
| 轮次信息 | `last_turn_no`, `last_request_summary`, `last_response_summary` |
| 时间戳 | `created_at`, `updated_at`, `closed_at` |
| 状态 | `status`（active/closed） |
| 分类 | `task_type`, `client_type`, `topic`, `intent` |
| 标签 | `user_tags[]` |

约束：全量向后兼容（新字段均 `omitempty`）、单查询无额外 JOIN、`admin/session_snapshot_test.go` 覆盖 JSON 序列化与字段存在性。

### FR-2 快照 500 修复 —— migration 655 schema 对账（已完成）

根因：共享库上 memora/kxmemory 部署用最小结构（`session_id` PK + `summary_json`）覆盖了
`public.session_summaries`，丢失 canonical 全部统计列。admin 读路径（serveSessionSnapshot /
turns_sessions / session_management_api / 会话健康 worker / auto-route workers）引用
`ss.session_key` 等列报 42703。

修复组成：

- `sql/migrations/startup/655_session_summaries_schema_reconcile.sql`：幂等补齐 canonical +
  V353 + 483 + 606 全部列、生成列、CHECK 约束、触发器 ON CONFLICT 仲裁唯一索引（守卫式
  WARNING，不阻塞启动）；前置 `tenant_id` 幂等补列（审计修复项）。
- `db/session_summaries_schema.go` `ensureSessionSummariesCanonical`：Go 侧镜像，网关启动
  自愈，接入 `applyMigrationsOnce`。
- installer 发布链路：`go:embed` + `copySQLBackup` + `setupSQLDir` 三处注册 655，
  `stats_migrations_test.go` expected 清单同步。

### FR-3 请求日志 404 修复 —— 业务请求落库恢复（已完成，环境侧）

同一次覆盖事件的写路径后果：`request_logs_hot` 的 AFTER INSERT 触发器
`trg_update_session_summary` 写 canonical 列失败 → 整条 INSERT 回滚 → 带 `gw_session_id`
的业务请求日志不落库 → 详情面板 404。655 对账 + 触发器恢复后，业务请求正常落库（本地实测
3 分钟 12 条）。

### FR-4 请求详情抽屉健壮性（已完成 · 2b0384d64 + 本次审计修复）

`web/src/components/detail/UnifiedRequestSessionDrawer.vue`：

- **快照竞态守卫**：`loadSeq` 序号 + `AbortController`，快速切换 A→B 时 A 的晚到快照/正文
  不写入 B 的视图（对齐 `useRequestDetailLoader` 既有模式）。
- **首屏 metadata-only**：`omitBody` 拉轻量首包，概览 QA 卡用 preview 兜底。
- **按需正文加载**（本次审计新增）：对话/压缩/原始JSON tab 切入时 `ensureBodies` 拉完整
  body，`bodiesLoadedFor` 去重；正文加载中显示 loading 提示。

## 3. 审计发现与修正（2026-09-05 本轮）

| # | 问题 | 严重度 | 处置 |
|---|------|--------|------|
| A1 | 抽屉 `getRequestLogDetail(id, { omit_body: true })` 参数名错误（应为 camelCase `omitBody`），`vue-tsc` 报 TS2561，首屏优化实际未生效 | 高 | 已修正参数名 |
| A2 | omitBody 生效后对话/压缩/原始 tab 将无正文（抽屉无二阶段加载） | 高（回归隐患） | 新增 `ensureBodies` 按需加载 + loading 提示 |
| A3 | 抽屉头注释声称"后端不识别 omit_body"，与 `admin/logs.go:836` 实际行为矛盾 | 低 | 注释已更新 |
| A4 | （上轮已修）installer 未嵌入 655；`TestStartupFilesAreAllEmbedded` 无法发现漏登记 | 高 | embed/映射/测试清单齐备，本轮复核通过 |
| A5 | （上轮已修）655 假设 `tenant_id` 存在，最小 shape 下整事务回滚 | 高 | 前置幂等补列，本轮复核通过 |
| A6 | （上轮已修）baseline 三处镜像仍是 310 旧触发器（`NEW.session_key`/`created_at`/`total_cost`） | 高 | 已同步 563 版本，本轮复核通过 |
| A7 | （2026-09-05 复核补修）`web/src/api/logs.ts` 仍残留"后端不识别 ?omit_body=1"过时注释（A3 同类漏网点），与 `admin/logs.go:836` 实际识别矛盾 | 低 | 注释已更新为 omitBody 分阶段加载语义 |
| A8 | （2026-09-05 二轮审计修正）文档与测试注释将快照字段数误记为 24（实际 23 = 10+13）；字段计数测试未真正断言数量；关联提交链重复且未落哈希；根目录 SNAPSHOT 文档索引写法会误导；logs.ts 新注释未点名 outbound_body 同被 omitBody 跳过 | 低 | 全部修正：字段数改 23 并以 reflect 断言固化（TestSessionSnapshotV2FieldCount）、提交链去重补哈希、索引标注路径、注释补全字段级影响 |

说明：仓库中不存在 `legacy_session_summaries` 表及引用；此前报告中的"读写分裂"为本地库
瞬态状态，非代码事实。

## 4. 验证记录

- `go build ./...` 通过
- `go test ./db/... ./sql/...` 全部通过
- installer 独立模块 `go build ./... && go test ./cmd/llm-gw-installer ./internal/dbinit` 通过
- `vue-tsc --noEmit` 抽屉相关错误清零
- `vitest`：RequestLogDrawer / useRequestDetailLoader / appNav 共 38 用例通过
- `gateway migrate` 本地库幂等执行通过
- **2026-09-05 环境侧端到端复验（真 main 二进制 2.5.0.1935 / vcs.revision=b08ea0607）全部通过**：
  全新业务流量实测 `GET /api/logs/:id` 200（FR-3 落库恢复；写侧为 `request_logs_hot` 热表，
  父表 `request_logs` 看不到新行属正常拓扑）、`?omit_body=1` 首包 200 且 `outbound_body`
  正确省略（FR-4）、`GET /api/admin/sessions/:id/snapshot` 200 无 42703（FR-1/FR-2；
  `public.sessions` 无行的会话按设计回退 2 字段 200）。复验过程发现并修复本地部署链路
  「编译失败被静默吞掉 → 复用陈旧二进制」隐患（CGO=0 构建断裂 + set -e 赋值语境吞错），
  详见 `docs/audit/2026-09-05-stale-binary-deploy.md` 与
  `docs/06-deployment/01-environments/local-8782-env-state-20260905.md`。

## 5. 相关文档索引

- `docs/SNAPSHOT_ENDPOINT_ENHANCEMENT.md` / `docs/SNAPSHOT_API_QUICK_REF.md` —— FR-1 字段清单与示例（2026-09-05 自仓库根目录移入 docs/）
- `docs/db-changelog.md` —— 655/656 部署记录
- `docs/COMPLETION-REPORT-20260904.md` —— 事故复盘与部署结论
- `sql/objects/functions/update_session_summary.sql` —— 触发器 canonical 版本（勿回退 310 形态）
