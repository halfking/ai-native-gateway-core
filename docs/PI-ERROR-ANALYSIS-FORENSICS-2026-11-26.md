# PI 错误分析：取证、修正方案与复测

**日期**：2026-11-26
**范围**：`/admin/pi-analysis` 页面及其后端（`domains/promptinjection` 分析链、`admin/pi_analysis_*`、`internal/logging` 近期日志缓冲、`settings/pi_analysis_specs.go`）
**前情**：`docs/PI-ERROR-ANALYSIS-AUDIT-2026-11-23.md`（功能线全景与缺口）
**迁移**：无（零 schema 变更、零新增环境变量）

---

## 一、这次补的是什么

2026-11-23 的审计指出分析链「只在错误消息上空推」。2026-11-25 在三台生产上实测，页面可读内容少的直接原因是**解析器**：

- 旧解析器是严格 JSON 解码。模型输出只要有一处不合形（被截断、尾逗号、多一层嵌套、中文键），整条分析就被丢成占位结论 `llm_parse_failed`。
- 当时的存量里 15 条分析有 14 条是这样丢的（211 上 2 条、245/154 共 12 条）。
- 把这些原始响应喂给新解析器，15 条全部读出（本地探针，数据未入库）。

在此基础上补齐了四件事：

1. **点击看前因后果**：详情弹窗里的「取证」把请求账本、逐次上游尝试、同一会话里前后相邻的请求、同型错误在时间窗内的分布、服务日志、错误手册条目放在一起。
2. **出错后自动检索**：后台分析在调模型之前先取证（同一套采集器），所以每条新分析都带着这些材料；请求失败类错误由发现器自动入队。
3. **修正方案**：自动动作与人工步骤分开，人工步骤按类别（配置 / 代码 / 运维 / 上游 / 客户端）与风险标注，附是否需要部署、回滚办法和验证方法。
4. **复测**：对同一条错误重跑规则、复查出错的那一层、统计之后有没有再发生，结论落成留痕。

## 二、端点

均挂在 `/api/admin/pi-analysis` 下，经 `AdminMiddleware`。

| 方法 | 路径 | 作用 | 成功码 |
|---|---|---|---|
| GET | `/queue/{id}/evidence` | 按需取证 | 200 |
| POST | `/queue/{id}/analyze` | 定向分析这一条（后台执行）；分析中返回 409 | 202 |
| POST | `/queue/{id}/verify` | 复测并留痕；请求体 `{"include_llm":true}` 可选 | 200 |
| POST | `/results/{id}/reparse` | 用当前解析器重读这一条（不调模型） | 200 |
| POST | `/results/reparse-failed` | 批量重读所有 `llm_parse_failed` 的存量结果（每次最多 500 条） | 200 |

`GET /results/{id}` 的响应多了一个 `fix_plan_view`：

```json
{"auto_actions": [...], "auto_fixable": true, "blockers": []}
```

`blockers` 取自 `view_only_type | analysis_unparsable | no_actions | low_confidence | already_applied`。页面上「应用修正」按钮的可用状态直接取自这里，与 `ApplyFix` 的闸门同源。

复测的结论闭集是 `not_reproducing | reproducing | inconclusive`，由三项检查聚合：

| 检查项 | 做什么 | 何时有判定力 |
|---|---|---|
| `rules_replay` | 把存档输入重过一遍本地规则层，与当初命中的规则对比 | 规则误报 / 启发式误触 / 非预期行为三类 |
| `layer_recheck` | 出错的那一层现在通不通（金丝雀计数、策略加载；判官失败类可选调一次判官） | 层失败类 |
| `recurrence` | 参照时点（已应用的修正，否则取分析完成时刻）之后同型错误有没有再出现 | 始终 |

一项都定不了时结论是 `inconclusive`，不是通过。

## 三、权限

| 动作 | 超级管理员 | 租户管理员（同租户） | 租户管理员（他租户） |
|---|---|---|---|
| 读队列 / 结果（既有端点，本轮未改） | 允许 | 允许（既有口径：不按租户过滤） | 允许（同上） |
| 取证 `evidence` | 全量 | **服务日志只保留提到本请求号 / 会话号的行** | 403 |
| 定向分析 / 复测 / 重解析 | 允许 | 403 | 403 |
| apply / revert / trigger（既有写端点） | 允许 | **403**（2026-11-26 补） | 403 |

两点说明：

- 本仓管理面里 `tenant_admin` 是只读角色（`RequireSuperAdminForWrite` 的约定）。页面在路由层标了 `requiresSuper`，但那只是体验层。2026-11-26 之前 apply / revert / trigger 在后端没有任何角色闸，而 apply 改的是全平台 PI 规则——本轮在派发点补上了。
- 进程内日志缓冲是整个实例共用的。「错误时刻附近的 WARN/ERROR」那一遍检索不带租户条件，所以对非超级管理员做了收窄；收窄掉几行会在 `notes` 与 `service_log_meta.hidden_unrelated` 里如实写明。

**既有读端点不按租户过滤这一点本轮没有改**，如实登记：队列与结果列表（含 `input_text`）对任何能过 `AdminMiddleware` 的调用方可见。是否收窄属独立决策。

## 四、设置项

| 键 | 默认 | 含义 |
|---|---|---|
| `pi_analysis.request_error_discovery_enabled` | `true` | 请求失败发现器开关 |
| `pi_analysis.request_error_threshold` | `5` | 时间窗内达到多少次即入队 |
| `pi_analysis.request_error_window_minutes` | `60` | 统计窗口（分钟） |

作用域均为平台级。发现器按 `(error_kind, provider_id, model)` 分组轮询 `candidate_failure_logs_hot`，每轮最多入队 5 条。两类不计入：

- 取消类失败（客户端主动取消等）——那不是故障。
- **全部来自探测流量的分组**。判据与请求账本的业务隔离同源：`origin_stage` 属于 `internal/probekind` 的探测阶段、`task_type = 'probe_triggered'`、或请求号以 `probe-` 开头。只要组里有一条是业务请求就照常入队；判不出来（账本不在场、超时、请求号多到没看全）时不跳过。

  缘由（2026-11-26 部署前在预发布库实测）：`upstream_down`（供应商 18 的 nvidia 系模型）那一组看着像上游故障，到请求账本里一查是 `node_probe`——节点探测在打一个「需要出口代理而没有」的供应商。探测健康归探测子系统管，不是「请求过程中发生的错误」。

## 五、发给分析模型的材料

分析模型端点沿用既有配置。此前提示词里已有错误消息、命中规则、策略配置与前 500 字节输入。本轮新增「取证摘要」，整体限长 6000 字符，逐项中和框架闭合标签后放在 `<untrusted_input>` 里：

| 材料 | 发给模型的内容 |
|---|---|
| 请求账本 | 模型、协议、状态、失败阶段、上游状态码、供应商 / 凭据编号、耗时、tokens、逐次路由摘要、请求预览（最多 200 字） |
| 上游尝试 | 时间、供应商、模型、状态码、错误类型、错误消息、是否可重试、耗时 |
| 会话上下文 | 相邻请求的模型、状态、错误类型、状态码、耗时、tokens（不含内容） |
| 同型错误分布 | 供应商 / 模型 / 次数 / 最近一次时间 |
| 服务日志 | **只发事件名（`msg`）与级别、时间，不发属性** |
| 错误手册 | 标题、根因、计划类型、命中 / 成功 / 失败计数 |

要点：

- 服务日志只发 `msg`，是为了不让别的请求的属性（键、标识符）进入提示词。
- 请求预览属于用户内容。对请求失败类错误来说这是新增的外发面（此前这类请求的内容不会进分析提示词），长度上限 200 字。若不接受，去掉 `piEvidenceRequestCols` 里的 `request_preview` 即可。
- 打捞出来的分析（模型输出有缺损）置信度封顶 0.79，低于自动应用的门 0.80。
- 分析回答的输出额度是 6000 token（`analyzerMaxTokens`）。分析模型带推理，推理过程的 token 也计入输出额度；额度用完时回答会在半路被截断。
  截断的回答按「丢掉没写完的那个成员、补齐括号」修复后读出，断点之前的完整成员都保留，并标记为截断、按路径登记丢失的成员。
  `parse_diagnostics.finish_reason` 记模型的结束原因（客户端没报而输出确已截断时记 `length` 并标 `finish_reason_source = inferred_from_truncated_output`）。
- 自动应用不会自行发生：`auto_fixable` 只是让按钮可用，仍需人点。

## 六、已知边界

- **服务日志只覆盖本进程、启动之后**。蓝绿切流或重启后，旧进程的日志不在缓冲里。取证响应的 `service_log_meta` 带缓冲覆盖窗口的两端（`oldest_at` / `newest_at`）与 `covers_window`；窗口不覆盖时界面明说，不把「没搜到」显示成「没发生」。
- **自动修正只覆盖规则层**：禁用规则、调整严重度、更新正则三类白名单动作。配置变更、代码变更、部署步骤只作为方案文本给人看。
- **复测不做请求重放**。方案词汇里有 `request_replay`，但复测目前只实现上表三项。
- **复发统计的口径是时间**：参照时点之后没有同型错误即 `not_reproducing`。对偶发的上游故障，这只说明「之后没再出现」，不说明已修复。
- **取证每一路都可能缺席**。表不在场、查询超时、没有请求号，都会在 `degraded` 与 `notes` 里标出；界面把「没取到」与「取到了但为空」分开显示。
- **时间上限有两层**：每一路最多 3 秒；一次取证的总预算是 8 秒（调用方给了更早的截止时间就以它为准）。预算用尽后还没跑的各路不再查询，直接标 `evidence budget exhausted` 并写出实际生效的预算。超时说明分两种：「这一路自己超时」是单路上限不够；「预算用尽」是库里当时就是慢。判超时看的是这一步的截止时间有没有到，不看驱动报了什么错（不同驱动在截止后报的错不一样）。
- **存量结果不会自动重读**。只有 `llm_parse_failed` 的行可批量重读；其余旧结果没有 `fix_plan`，要用定向分析重跑才会带上取证材料。

## 七、上线后要做的与怎么验

1. **批量重读存量**（每台一次）：

   ```sh
   curl -sS -X POST "$BASE/api/admin/pi-analysis/results/reparse-failed" -H "Authorization: Bearer $TOKEN"
   ```

   返回 `{scanned, reparsed, still_unparsable, failed, reparsed_queue_ids}`。只动占位结论的行；读不出的一条不改。

2. **看一条取证**：`GET /api/admin/pi-analysis/queue/{id}/evidence`，确认 `request` 有值、`degraded` 为空或与预期一致。

3. **跑一次复测**：`POST /api/admin/pi-analysis/queue/{id}/verify`，确认返回的 `checks` 三项状态与 `verdict`。

4. **确认发现器在跑**：出现达到阈值的请求失败后，队列里应有 `error_type = request_failed` 的行：

   ```sql
   SELECT error_type, status, count(*) FROM error_analysis_queue
   WHERE created_at > now() - interval '1 hour' GROUP BY 1, 2 ORDER BY 3 DESC;
   ```

## 八、回滚

- 零迁移、零 schema 变更。回滚到上一版镜像 / 二进制即可；`analysis_details` 里新增的键（`fix_plan`、`parse_diagnostics`、`model_output`）旧版本不读，不冲突。
- 只想停掉请求失败自动入队：把 `pi_analysis.request_error_discovery_enabled` 置 `false`。
- 近期日志缓冲随进程存在，最坏占用约 48MB（WARN 环 4096 × 8KiB ＋ INFO 环 8192 × 2KiB），无独立开关。

## 九、顺带修掉的两处

- **`internal/logging` 的 `TestRotationProducesBackup` 是个既有的间歇失败**。根因是测试没等 lumberjack 的后台协程：它写下的量足以轮转 4 次，而 `MaxBackups=3`，第 4 个备份出现时最老的那个必然被淘汰——测试读的恰好是最老的；另外测试结束时后台压缩还在往临时目录里写，与 `TempDir` 的清理相撞（`testing.go: TempDir RemoveAll cleanup: … directory not empty`）。现在先等压缩与清理结束再取文件列表，并在收尾时把全局 logger 指回 stderr。
- **PI 处理面的写端点缺后端权限闸**（见第三节）。
