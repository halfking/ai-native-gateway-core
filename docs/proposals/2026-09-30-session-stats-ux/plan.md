# 会话与统计 tab 优化方案

> 2026-09-30 · 页面：`/dashboard?tab=stats`
> 对标：Langfuse（session = 多轮 trace 回放 + 过滤）、Open WebUI / LibreChat（标题检索、在线与历史分栏）、本仓生产接口 `GET /api/admin/sessions`

## 1. 现状

统计 tab 由两块叠放：

1. `SessionStatsPanel`：KPI、趋势、成本、合规、排行。数据来自 dashboard overview，`totalTokens` 固定为 0 且不展示。
2. `SessionDrilldownPanel`：只渲染 `OnlineSessionsPanel`（`GET /api/admin/sessions/online`）。列只有会话键、标题、最近状态、模型、延迟、最近活动。选中后整表换成轮次时间线，地址栏不带 `session`，刷新即丢失。

在线列表注释写明：后端没给用户、轮次、健康度，所以不显示。这句话只对 **online** 端点成立。

生产 `GET /api/admin/sessions`（`admin/session_state_handlers.go` `handleListSessions`）已经返回：

- `title` / `status`（默认只返回 active；`status=all|stopped` 才含历史）
- `total_turns`、prompt/completion tokens、`total_cost_usd`
- `health_score` / `health_grade`
- `current_model`、`tags`、`last_active`
- 查询参数：`status`、`limit`（上限 200）、`sort=health|cost|tokens|created_at`、`health_grade`

前端 `getSessionList` 仍按已废弃的 v1 聚合形状要 `q` / `hours` / `page`，生产 handler 不读这些参数。该函数目前没有页面调用。

`sessions.management` 文案已在各 locale 里，没有对应页面。

## 2. 对标后要补的能力

| 来源 | 做法 | 本页落点 |
|------|------|----------|
| Langfuse | 一会话多轮；列表可按用户/模型/标签过滤；选中写入 URL，可分享、可后退 | 目录列表 + `?session=` 深链 |
| Open WebUI / LibreChat | 在线侧栏与历史检索分开；按标题和标识搜索 | 「在线 / 目录」切换；目录内按标题、会话键、模型、标签过滤 |
| 本仓接口 | 不新造列、不把延迟 0 当未知 | 目录列只用 `handleListSessions` 已有字段；在线列表保持「未知显示 —」 |

已经落地、并且和下面审计一致的行为：

- 目录搜索 `q` 只查 `session_summaries`（会话键、标题、`primary_model`），再和本次 Redis 窗口合并。
- 已返回行再按 `session_id`、`title`、`current_model`、`tags` 过滤。模型能匹配，是因为用量补齐先把空的 `current_model` 填上，不是因为扫了请求日志。
- 不扫描 `request_logs` 做前导 `ILIKE`。本地约 232 万行实测约 16 秒，不能挂在列表接口上。

明确不做：

- 不把废弃 v1 的 `q`/`hours`/`page` 当成搜索。`getSessionList` 仍没有页面调用。
- 不显示用户/项目列，除非改 `handleListSessions` 的响应。那是 P3，要先做 rule 57 影响矩阵。
- 不把 `totalTokens: 0` 填进 KPI。overview 没有可靠 token 合计时继续隐藏。
- 不把目录说成「全部历史」。`status=all` 仍是 Redis 里 active∪stopped，上限 200。摘要表里有、Redis 里没有的会话，只有搜索命中才补进行。只存在于请求日志、摘要里也没有的会话，搜索找不到。

## 3. 实施切片

### P1（本轮）

- 统计 tab 下钻区增加「在线 | 目录」。
- 目录调用 `listCatalogSessions` → `GET /api/admin/sessions?status=&limit=200`。
- 状态芯片：全部 / 活跃 / 已停止（重新请求，因为服务端默认只给 active）。
- 搜索框过滤当前结果的 `session_id`、`title`、`current_model`、`tags`。
- 选中会话写入 `?tab=stats&session=<id>&slist=online|catalog`，返回列表时去掉 `session`，保留 `slist`。浏览器后退回到列表。
- 文案进 `sessions.catalog`，8 个 locale 同步。
- 组件测试：目录过滤、状态参数、深链打开时间线、返回后回到进入时的列表。

### P2（已做，范围比最初写的窄）

- `handleListSessions` 接受 `q`。检索表是 `session_summaries`，不是请求日志。摘要命中会把空白标题/模型写回已在窗口内的行，再过滤，避免标题命中被丢掉。
- Redis 轮次、prompt、completion、费用各自为 0 时，用 `request_logs_hot` ∪ `request_logs` 按 `gw_session_id` 补齐。非 0 值不覆盖。查询失败打 `slog.Warn`，列表仍返回 Redis 窗口。
- 健康等级过滤参数 `health_grade` 本来就有，本轮没有加新的筛选芯片。
- 统计 KPI 的 Token 卡仍关闭。overview 没有可靠合计。

### P3

- 列表补 `owner` / `project`（需先改响应契约并做 rule 57 影响矩阵）。
- 从目录行深链到请求日志，按 session 过滤。

## 4. 验证

- `go test ./admin/ -run 'TestOverlayCatalogUsage|TestFilterCatalogItems|TestLikeContains|TestMergeCatalogSearchHits|TestCatalogSearchSQL'`
- `web` 下 vitest：`SessionCatalogPanel`、`SessionDrilldownPanel`。
- 浏览器打开 `http://localhost:8782/dashboard?tab=stats` 前，要重新构建前端和 linux/arm64 网关，并替换容器里的二进制和静态文件。容器不是源码挂载。只改仓库不算 8782 已生效。

## 5. 2026-09-30 审计（对上一轮实现）

上一轮文档把 P2 写成「服务端检索突破 200 条窗口」，代码并没有做到。本轮按库内实测改代码，并改写上面第 2、3 节。

| 问题 | 证据 | 处理 |
|------|------|------|
| `q` 对 `request_logs_with_current_month` 做前导 `ILIKE` | 本地 `llm-gateway-pg`，约 232 万行，`EXPLAIN ANALYZE` 约 16 秒；精确等值同样要扫视图，约 14 秒 | 检索改到 `session_summaries`。同机摘要表 333227 行，同样模式约 317ms，并加 2 秒超时 |
| 用量查询走同一视图 | 单个 `gw_session_id` 约 815ms，时间花在 `session_turns_*` 联接 | 改为 `request_logs_hot` ∪ `request_logs`，同一会话约 1ms，加 5 秒超时 |
| 检索失败被吞掉，界面仍像搜索成功 | `query` 出错直接返回原列表 | `slog.Warn`，调用方仍可用已加载行的内存过滤 |
| 标题命中被丢掉 | 先合并 id，再按行上的 title/model 过滤；摘要标题没有写回行 | `mergeCatalogSearchHits` 补空白标题和模型。空 `session_key` 在 SQL 和 Go 都丢掉 |
| Redis 四个用量字段必须全 0 才补齐 | 任一字段非 0 时，其余 0 字段保持 0 | 按字段补齐，非 0 不覆盖 |
| `likeContains` 按字节截到 80 | 多字节字符会被截断成非法 UTF-8 | 按 rune 截断 |
| 摘要未命中的会话被标成 `active` | 合成行写死状态 | 没有会话记录时状态留空，界面显示 — |
| 方案正文仍写「只过滤已加载的 200 条、明确不做服务端 q」 | 与代码相反 | 本节之前的方案已改成现在的行为 |

没有在本轮改掉、也不能写成已完成的事：

- 模型子串搜索覆盖不到「不在本次 Redis 窗口、且摘要 `primary_model` 为空」的会话。`gw_13543929-a852-440e-a96f-138e7bff99ea` 的摘要 `primary_model` 就是空的，模型来自请求日志补齐。这种会话只有已经出现在列表里才能按模型滤出。
- `session_summaries` 可以比请求日志旧。同一会话摘要 prompt 42，日志合计 prompt 99、completion 24、费用 0。目录数字在 Redis 为 0 时跟日志，不跟摘要。
- 健康等级仍来自 `session_summaries`。健康 D 可以和一次成功轮次同时出现。
- 摘要 `ILIKE` 仍是顺序扫描，约 317ms。表再变大时 2 秒超时会失败并退回已加载行，不会再打 16 秒的日志扫描。
- P3 的 owner/project、KPI Token 卡、目录上的 `health_grade` 芯片都还没做。
- 单测不连数据库。SQL 只做了字符串约束，防止把请求日志扫描加回去。16 秒和 1 毫秒的数字来自本机 `EXPLAIN ANALYZE`，不是单测。
