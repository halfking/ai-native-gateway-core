# 下一阶段总执行提示词：环境验收、数据闭环与发布硬化

> 用法：复制本文件中“主代理提示词”全文到一个新会话。
> 只启动一个主代理；由主代理按 Wave 依赖自动划分、调用和回收并行子代理。
> 子代理不直接 commit、push、merge、部署或执行生产迁移。

---

## 主代理提示词

你是本阶段唯一的主代理（Coordinator），负责在 `llm-gateway-go` 仓库完成环境验收、数据闭环、可观测性和发布硬化。你可以调用多个子代理，但必须由你统一调度、审查、整合和汇报。

### 1. 项目基线

- 工作目录：当前会话绑定的 `llm-gateway-go` 仓库根目录；不要假定固定绝对路径。
- 基线分支：开始前读取当前分支和远端；不得擅自把 feature 分支当成 `main`。
- 必读入口：
  1. `docs/06-deployment/01-environments/README.md`
  2. `docs/06-deployment/01-environments/customer-install/README.md`
  3. `docs/06-deployment/README.md`
  4. `deploy/README.md`
  5. `docs/staging-validation-progress-20260829.md`
  6. `docs/audit/2026-08-29-deployment-contract-audit.md`
  7. `docs/audit/2026-08-29-hardening-audit-report.md`
  8. `docs/audit/2026-08-29-journal-provider-followup-audit.md`
  9. `docs/next-phase-prompt.md`
  10. 本提示词全文

### 2. 开工前硬性检查

先执行并记录：

```bash
git status --short
git rev-parse HEAD
git branch --show-current
git reflog -5 --oneline
git fetch origin main
```

建立协作者 WIP 清单：

- 已修改文件、未跟踪文件、stash、其他 worktree；
- 将它们列入禁止修改范围；
- 禁止 `reset`、`restore`、`clean`、无条件 `stash` 或覆盖协作者 WIP；
- 子代理必须使用独立 worktree/branch，不能共享主工作区。

### 3. 证据等级

每个结论必须标注以下等级之一：

- `IMPLEMENTED`：代码/脚本/manifest 存在并通过静态或单元检查。
- `LOCAL_VERIFIED`：本地或隔离依赖中可重复通过。
- `REAL_DEPENDENCY_VERIFIED`：在指定真实 PostgreSQL、Redis、provider、Prometheus、浏览器或集群取得证据。
- `RELEASE_READY`：真实验证、回滚演练、监控门禁和责任人记录全部完成。
- `manual_required`：缺少授权、环境、凭据、真实依赖或人工决策。
- `blocked`：存在明确技术阻塞。

不得将 mock、dry-run、历史 handoff 或单次本地测试升级成真实环境证据。

### 4. 不可违反的安全和发布约束

- 不输出或提交 DSN、密码、token、cookie、App Secret、私钥或生产敏感数据。
- 不执行未授权的 SSH、数据库迁移、重启、生产流量、真实 provider 调用或凭据轮换。
- 真实环境任务必须先确认目标、授权窗口、备份、回滚路径和数据脱敏方案；否则返回 `manual_required`。
- migration 必须有 upgrade/down/re-up、checksum/dirty、RLS/role 和兼容性证据。
- binary rollback 不能替代 data rollback。
- 不得将当前单端口 stop/start 发布描述为“2 秒零中断”。
- 不得因为一个子代理报告成功就宣称整阶段完成。
- 找不到预期 API、字段、脚本或文件时停止并报告，不自创接口。
- 同一文件只能有一个子代理 owner；发生冲突立即停止，不自动 merge。
- 子代理不 commit、push、merge；由主代理统一整合。主代理也只有在用户明确授权后才执行 commit/push/deploy。

### 5. 当前硬阻塞和已知边界

- Session V2 staging：`session_turns` 回填 `ON CONFLICT` 契约失败；`request_logs`/`request_logs_bodies` 查询曾超时；一致性和前端验证尚未闭环。
- 客户安装文档已统一端口和入口，但 Windows host、Windows Docker 仍 `NOT_RELEASE_READY`。
- `/healthz` 是 liveness；`/readyz` 是 DB+Redis 严格 readiness；`/version` 是 release identity。
- Phase 1 readiness/rollback 已有代码收敛；Phase 2 双端口蓝绿、Nginx 原子切流、worker 角色隔离和 SSE drain 尚未证明。
- 真实 PostgreSQL、Redis、Prometheus、浏览器、provider/live traffic 验证不能由本地测试替代。

---

## 6. Wave 0：只读契约审计（全部可并行）

先启动以下只读子代理。它们不能改代码或文档，只返回证据和阻塞。

### W0-A：Session V2 staging/schema 契约

审查 `session_turns`、`session_bodies`、`request_logs_bodies`、backfill SQL/tool、`validate_sessions_v2`、migration 顺序和当前 staging 报告。输出：字段/唯一约束矩阵、查询计划风险、10/100 会话验证前置条件、`file:line` 证据。

### W0-B：环境/部署/平台矩阵

核对 `docs/06-deployment/01-environments/`、`scripts/user/`、`scripts/lifecycle/`、`scripts/deploy/`、`deploy/k8s/`、Compose 和根 README。输出：入口、目录、端口、依赖、artifact、OS/CPU、支持状态和矛盾清单。

### W0-C：Tool result missing 链路

只读审计 `tool_use`、`tool_result`、`interrupted`、`discard_events`、Anthropic streaming 和现有 SSE frame validator。输出：245/154 数据基线命令、可能丢失点、协议 fixture 缺口和安全边界。

### W0-D：指标/告警契约

核对 success-empty-response、provider error、candidate failure、JournalSnapshot、SSE/malformed frame 指标及现有 Prometheus/Grafana 资产。输出：规范指标名、标签、重复计数风险、告警阈值候选。

### W0-E：零中断发布边界

只读审计 `deploy-seamless.sh`、Nginx active-upstream、systemd/launchd、SSE connection/drain 和 worker 生命周期。输出：当前 stop/start 证据、双端口方案、共享文件 ownership 和回滚风险。

Wave 0 完成条件：主代理汇总所有结果，冻结接口/指标/环境命名和文件 ownership；有冲突或缺证据的项标记 `blocked`/`manual_required`。

---

## 7. Wave 1：Session V2 本地实现和可重复测试

Wave 0 后启动。P1-A 与 P1-B 文件不重叠时并行；P1-C 等待两者稳定。

### P1-A：修复 validate_sessions_v2

允许文件：`cmd/tools/validate_sessions_v2/**` 及对应测试/fixture。修复独立 `request_logs_bodies` 关联，覆盖空 body、缺 body、多租户和时间排序。不得接触真实 staging 凭据。

### P1-B：修复 backfill 与 turns 契约

允许文件：Session V2 backfill tool、SQL function、相关 migration/test。修复 `ON CONFLICT` 与真实唯一约束不一致问题；查询按 `(gw_session_id, ts, request_id)` 可分页，turn number 在应用层计算；禁止全表无界扫描。

### P1-C：本地 10 会话 harness

等待 P1-A/P1-B 后，创建脱敏 fixture 和可重复批量回填/双读校验 harness。验证成功、失败、空 body、重复执行幂等和差异报告。真实 staging 执行留给 Wave 4。

每个子代理至少运行受影响测试、`go build`、`go vet`；必要时 `-race`。返回 `IMPLEMENTED` 或 `LOCAL_VERIFIED`，不宣称 staging 完成。

---

## 8. Wave 2：可观测性、完整性和生命周期硬化（可并行）

### P2-A：Tool result completeness validator

实现或补齐 tool-call 完整性验证，记录 `incomplete_tool_call`，增加规范 `incomplete_tool_call_total` 指标，接入 Anthropic fixture 和 synthetic interrupted/discard cases。不得在未完成根因分析时自动改变业务结果。

### P2-B：success-empty-response 运维闭环

冻结唯一指标名和标签，补 Prometheus rule、Grafana dashboard、告警路由配置和 7 天观察记录模板；确认不与 `has_response_body` 重复计数。保留当前保守 settlement 语义，是否把 success 改 false 必须由业务 owner 决策。

### P2-C：Proxy 监控体系

补 subscription refresh、node health、latency、failure、selection、decrypt、transport-cache 指标，创建 `deploy/grafana/proxy-dashboard.json`、`deploy/prometheus/proxy-rules.yml` 和 `docs/operations/proxy-ops-manual.md`。先做监控，再做性能优化。

### P2-D：JournalSnapshot receipts 容量和指标

为 receipts 增加 TTL/LRU/容量上限和序列化大小限制，增加 `stored/applied/deduplicated` 指标；补生产 adapter 单元测试。必须保证 tenant authorization、super_admin 审计和幂等语义不回退。

### P2-E：fire-and-forget 生命周期审计

审查并补齐以下高风险点：

```text
connection_registry.go:356
durable_stream.go:92
handler.go:2270, 2415, 5675
handler_autocombo.go:216
handler_state_init.go:37
format_cache.go:54
executors/health_tracker.go:79,119
```

优先采用 context、WaitGroup/errgroup、明确 cancel 和 panic recovery；不要把 goroutine 改成无界重试。

---

## 9. Wave 3：前端和协议验证（按文件 ownership 并行）

### P3-A：Queue filter

补人工测试脚本/结果模板，覆盖单模型、供应商/原厂、清空、多条件组合、空结果和性能观察。浏览器不可用时标记 `manual_required`。

### P3-B：ErrorDetailTab

验证双主题、请求取消竞态、实际 API 路径和错误态；补前端测试，运行 `vue-tsc` 与 targeted Vitest。

### P3-C：Journal/attempt Admin UI

实现或设计 snapshot/attempt 序列展示、跨租户访问审计日志和权限边界；不能暴露未授权租户数据。

### P3-D：Streaming live interop

建立 OpenAI Responses SDK live interop harness，覆盖 malformed SSE、EOF、cancel、retry、empty response 和 tool call。没有真实 provider 时只交付本地 fixture 与 `manual_required`。

---

## 10. Wave 4：真实依赖验证（默认只读，需逐项授权）

Wave 1–3 的本地证据完成后，主代理按授权状态启动以下任务；不能把它们当作普通本地开发任务。

### V4-A：154/staging PostgreSQL

执行前确认授权、备份和窗口。验证 migration 614–617 及当前实际所需后续 migration、RLS、`session_turns`/`session_bodies` 回填、10→100 会话双读/对账、provider error 聚合、candidate failure hot table、TTL 和 rollback。大查询先 `LIMIT`，使用 statement timeout 和可回滚步骤。

### V4-B：Redis/Prometheus/provider

验证 Redis/queue/fallback/recovery、Prometheus rule 实际加载、provider/TCP/SDK live interop、指标标签和错误率/p99。凭据只从 secret manager/目标环境读取，不写报告。

### V4-C：245/154 canary

在批准窗口执行 preflight、migration compatibility、10%→50%→100% canary（若授权）、rollback、release identity、SSE drain 和 maintenance page 清理。记录 GO/NO-GO，不宣称零中断除非有时间窗口证据。

### V4-D：GLM-5.2 观察

完成 24 小时每小时监控、一周复查、CPU/cache 证据、降级关联分析、CHANGELOG 和部署记录；版本 `git_sha` 必须与实际发布 artifact 对齐。

所有 V4 任务都必须返回目标、时间窗、命令摘要、脱敏证据、结果和未验证项；无条件或无授权时返回 `manual_required`。

---

## 11. Wave 5：Phase 2 双端口蓝绿（串行发布门禁）

仅当 Wave 4 Phase 1 证据通过后进入设计/实现：

1. candidate/active 双端口并行启动，不能提前杀 active。
2. Nginx `active-upstream` 原子切流，并验证配置 reload/rollback。
3. worker 与 traffic receiver 角色隔离，明确共享 DB/Redis/queue 的 ownership。
4. SSE 长连接 drain、timeout、cancel、retry 和新连接切换测试。
5. candidate 启动失败、readiness 失败和切流失败自动恢复旧版本。
6. 以真实观测证明切换窗口，不以脚本存在替代证据。

在所有条件满足前，发布结论只能是 `PARTIAL`/`GO_WITH_LIMITATIONS`，禁止使用“2 秒零中断”和 `RELEASE_READY`。

---

## 12. 子代理统一交付契约

每个子代理必须：

- 使用独立 worktree/branch；
- 开工前记录 `git status --short`、`git rev-parse HEAD`、`git branch --show-current`、`git reflog -5 --oneline`；
- 只修改分配的允许文件；
- 将协作者 WIP 和其他任务文件视为禁止范围；
- 不执行 `reset`、`restore`、`stash`、`clean`；
- 不 commit、push、merge、部署或执行真实 migration；
- 预期标识符不存在、同文件冲突、依赖/授权缺失时立即返回 `blocked` 或 `manual_required`；
- 不输出 secret、DSN、token、cookie 或生产数据；
- 完成后返回：状态等级、diff stat、文件清单、测试命令/结果、未验证项、风险、阻塞和下一步。

---

## 13. 主代理整合 SOP

1. 汇总 Wave 0 证据并冻结 ownership、接口、指标和环境名。
2. 只在依赖满足时启动下一 Wave；同文件任务严格串行。
3. 每个批次结束检查完整 `git status`、未跟踪文件、冲突标记和允许文件范围。
4. 本地整合至少执行：

```bash
go build ./...
go vet ./...
go test -count=1 ./affected/package/...
go test -race -count=1 ./affected/package/...
cd web && npx vue-tsc --noEmit && npm test
```

5. 运行 migration unique/down/SQL contract、shell `bash -n`、多文档 K8s YAML 解析和链接检查。
6. 单独记录 `IMPLEMENTED`、`LOCAL_VERIFIED`、`REAL_DEPENDENCY_VERIFIED`、`RELEASE_READY`，禁止证据越级。
7. 所有真实环境任务结束后生成阶段 handoff，包含 GO/NO-GO、人工决策、回滚状态和未验证项。
8. 只有用户明确授权后，主代理才可统一 commit/push/merge/deploy；提交不得包含协作者 WIP 或 secrets。

## 14. 完成定义

本阶段只有在以下条件同时满足时才可标记整体完成：

- Session V2 回填、双读、对账和前端验证有真实环境证据；
- Tool result missing 有基线、validator、测试和监控；
- 指标命名、dashboard、告警和观察窗口完成；
- 245/154 canary、rollback、release identity 和 migration 证据齐全；
- 真实 PG/Redis/provider/browser/Prometheus 验证有脱敏记录；
- 共享文件、未跟踪文件、冲突和协作者 WIP 均已解释；
- 没有把单端口发布描述为 2 秒零中断；
- 所有阻塞或人工授权项明确记录；
- 主代理生成下一阶段 handoff 和最终 GO/NO-GO。

如果任一条件未满足，输出 `GO_WITH_LIMITATIONS` 或 `NO-GO`，不要伪造成功。
