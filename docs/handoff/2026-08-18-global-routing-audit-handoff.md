# Session Handoff: 全局路由与探针状态机审计

## 1. 任务概要（Mission Summary）

本任务从 glm-5.2 的供应商全部不可路由事故扩展到全模型审计，目标是确认路由、URSM、探针队列、凭证状态和自检页面是否存在同构故障，并将现场证据与后续整改动作固化，供新会话继续实施。

此前已完成 glm-5.2 锁死的第一轮修复并部署到 154/245；本轮复检发现更深层的全局状态一致性问题，尚未修改代码，下一会话应优先处理 P0 问题并重新部署验证。

## 2. 当前方案（Approach / Plan）

采用四层交叉审计：

1. 生产 API：routing resolve、probe system health、queue snapshot、self-check stats、模型节点和时间线。
2. PostgreSQL：`v_routable_credential_models`、`credentials`、`node_probe_state`、`credential_probe_queue`、`node_probe_runs`。
3. Redis db2：URSM tenant/legacy key、ready gate、TTL 和 available 状态。
4. 源码：恢复器、统一探针队列、ProbeService、路由过滤、旧 dashboard view 和部署模式。

原则：以实际请求路由和 URSM tenant key 为 authoritative runtime 事实；DB ready、旧 probe view 或历史成功字段不能单独证明可路由。

## 3. 任务进度（Progress）

- ✅ 已完成：glm-5.2 第一轮修复：pin bypass、queue re-arm/revive、due-state pump、7h TTL、存量 periodic quota 重评、自检 trigger。
- ✅ 已完成：新增测试、构建和受影响包回归测试。
- ✅ 已完成：独立代码审计第一轮，修复 P0/P1 发现。
- ✅ 已完成：154 生产端到端验证；glm-5.2 曾恢复到 sp1、智谱、智码可路由，真实请求返回 `OK`。
- ✅ 已完成：本轮全模型现场取证和跨表审计。
- 🚧 进行中：处理本轮发现的全局 P0/P1 状态机与审计断裂。
- ⏳ 待办：停止 recovery 伪成功；修复 `node_probe_runs` CHECK/静默错误；修复 dashboard 数据源；修复 self-check credential pin；增加 lease heartbeat；重新部署并验证全部模型。

## 4. 当前状态（Current State）

- 工作目录：`/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4`
- 当前分支：`main`
- 最后操作：完成 154 全局路由/探针/URSM/数据库现场审计；工作区在提交文档前干净。
- 正在编辑：本交接文档。
- 代码快照：`✅ committed 1eac33fac @ main` / `pushed: ✅`；本次文档提交后需再次 push。
- 已部署：154 v1615，245 v1616；版本代码基线为提交 `1eac33fac`。

## 5. 下一步（Next Steps）

1. 在 `bg/credential_recovery.go:443-485` 删除或重构直接写 `last_direct_ok/last_gateway_ok=true` 的伪成功 SQL；改为真实 durable probe enqueue，不能推迟真实任务。
2. 增加数据库迁移，扩展 `node_probe_runs.trigger_kind` CHECK 以支持统一队列实际来源，或在写入前做合法映射；同时修改 `bg/probe_service.go:373-398`，禁止吞掉审计 INSERT 错误。
3. 给统一探针增加 lease heartbeat 或将 lease 调整到覆盖 direct+gateway 双轮最坏耗时，确保 `ProbeService.Run` 副作用与 task ownership 一致。
4. 将 `credential_selfcheck.go` 的 credential 级自检改为可信 pin 请求；若不能 pin，则不再把普通路由结果归属到指定 credential。
5. 重建新 probe mode 的 dashboard health contract，读取 `credential_probe_queue`、`node_probe_state`、`node_probe_runs` 和 URSM tenant coverage，明确标记旧数据源。
6. 修复 `/api/routing/resolve` 在 authoritative URSM miss 时的 optimistic default，至少同时返回 `db_eligible` 与 `runtime_routable`。
7. 增加普通请求与 failed probe 的 source-priority 单测/集成测试，保证普通成功不会覆盖仍有效的失败 probe 证据。
8. 在隔离 Redis/key prefix/测试凭证上增加 245 probe-canary，验证入队、claim、双轮探测、审计、URSM 写入和 resolve 一致性。
9. 重新运行 `go test ./bg ./admin ./domains/streaming/executors ./domains/ursm/v2/...`、`go build ./...`，再走 245→154 部署门禁。

## 6. 关键事实（Key Facts）— AUTO-COLLECTED 2026-08-18

- 项目根：`/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4`
- 当前 commit：`d7ebf25f7b37210a5d52c86aed375328065a0000` / branch: `main`
- 最近 commits：
  - `d7ebf25f7 fix(docs): update PROJECT_CONFIG.md to server 154 + redact credentials`
  - `a92ff4d43 fix(ursm): block canonical cleanup and align ledger schema`
  - `1ea1adb2a fix(partition): retain full bodies-hot reloptions after swap`
  - `c6b731def fix(partition): preserve swap metadata and canonical index names`
  - `3b6bdce18 fix(observability): pre-warm credential-reveal metric so /metrics always emits it`
- 待提交改动（auto-collect 时）：工作区干净；本交接文档为本次新增改动。
- 版本：`2.5.0-1eac33fa-20260818-1616`，build_seq `1616`。
- 近期 incident 文档：未发现 `knowledge/incidents/20260818*` 或 `20260817*` 文件；现场证据已在本文件和对话记录固化。
- 已加载凭据 KEY 列表：仅通过 env-injector 加载，未在文档写入 value；包含 SSH/LLM_GATEWAY/CASDOOR/PG 相关变量。
- 最近 3 commits 改动文件数：19 个文件（`git diff --stat HEAD~3 HEAD` 采集结果）。

### 6.1 AI 决策备忘

- 本轮没有直接修改代码；先把跨模型根因和生产证据固化，避免在 recovery 伪成功、审计约束和 dashboard 数据源未厘清前继续做局部解锁。
- 154 是完整后台探针运行节点；245 明确配置为 `LLM_GATEWAY_BG_MODE=data-plane`，不能作为完整探针恢复链路的验证环境。后续需用隔离 canary 补足该发布门禁。
- glm-5.2 当前曾恢复过 sp1/智谱/智码，但这不代表全局状态机可靠；本轮复检已经发现 glm-5.1、gpt-5.5、kimi-k2.6、doubao 等模型存在同构 URSM miss 或全生命周期禁用。

## 7. 阻塞 / 风险（Blockers / Risks）

- **P0：** recovery SQL 每 30 秒伪造 probe 成功并把任务延后 1h；会让所有模型持续出现 DB 健康、URSM 缺键、实际不可路由。
- **P0：** `node_probe_runs.trigger_kind` CHECK 不接受统一队列来源，INSERT 错误被静默吞掉；现役探针无法形成审计证据。
- **P1：** probe lease 30s 小于串行双轮最坏耗时，可能重复执行和丢失结算。
- **P1：** credential self-check 未 pin，结果可能归属错误凭证。
- **P1：** 245 data-plane 不运行完整恢复链，预发不能验证生产核心机制。
- **P2：** 旧 dashboard/view 使用退役 `model_probe_state`，与现役 queue/node/URSM 数据源冲突。
- **P2：** `/api/routing/resolve` 在 authoritative miss 时可能比实际请求乐观。
- **P2：** legacy、default tenant、无 TTL URSM key 并存，coverage 不透明。

## 8. 建议加载的 skills（Suggested Skills）

- `review`
- `comprehensive-code-audit`
- `llm-gateway-test`
- `llm-gateway-deploy-test`
- `deploy-245`
- `deploy-154`
- `session-audit-gate`

## 9. 引用（References）

- 第一轮修复 commit：`1eac33fac fix(probe,routing): break glm-5.2 lockout loop — pin bypass, queue revive/pump, TTL floors`
- 生产部署：154 v1615；245 v1616
- 关键源码：`bg/credential_recovery.go`、`bg/probe_service.go`、`bg/node_probe.go`、`bg/probe_queue.go`、`admin/probe_dashboard.go`、`admin/routing.go`、`domains/ursm/v2/store/record_request.lua`、`domains/ursm/v2/store/apply_probe.lua`
- 关键数据库对象：`v_routable_credential_models`、`v_probe_system_health`、`v_probe_queue_snapshot`、`node_probe_state`、`credential_probe_queue`、`node_probe_runs`
- 相关前序 session：当前对话现场审计记录；新会话应先读取本文件。

## 10. 新会话并行执行提示词（可复制）

### Agent A：恢复器与 URSM 状态机

```text
你负责修复全局状态机 P0。仓库：/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4。
先读取 docs/handoff/2026-08-18-global-routing-audit-handoff.md。
重点审查 bg/credential_recovery.go:443-485：禁止直接伪造 last_direct_ok/last_gateway_ok=true、禁止无真实证据顺延 next_retry_at；改为真实 durable probe enqueue 或仅处理有证据的恢复。检查 URSM tenant key 写入、Recover priority、事务和并发。新增 SQL/行为测试，运行 go test ./bg ./domains/ursm/v2/...，不要部署或修改数据库生产数据。
```

### Agent B：探针审计约束与 lease

```text
你负责探针审计持久化和队列 lease。仓库：/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4。
先读取 docs/handoff/2026-08-18-global-routing-audit-handoff.md。
扩展 node_probe_runs.trigger_kind CHECK 以支持统一队列实际来源，或实现明确的 source-to-trigger 映射；修改 bg/probe_service.go，不能吞掉 audit INSERT 错误。审计 credential_probe_queue 的 30s lease 与 direct+gateway 双轮耗时，增加 heartbeat 或合理 lease，并保证 side effect/ownership 幂等。补迁移、测试、错误指标；不要部署。
```

### Agent C：自检与 dashboard 真相源

```text
你负责自检和 dashboard 数据源一致性。仓库：/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4。
先读取 docs/handoff/2026-08-18-global-routing-audit-handoff.md。
审查 bg/credential_selfcheck.go：credential 级自检必须通过 trusted X-LLM-Pin-Credential 访问目标凭证，否则不得把普通路由结果归属到该凭证。重建 admin/probe_dashboard.go 的 system-health、queue-snapshot、model nodes，使新 probe mode 读取 credential_probe_queue/node_probe_state/node_probe_runs/URSM，并明确标记 legacy 数据源。补单测和 API contract 测试，不部署。
```

### Agent D：路由状态一致性与集成审计

```text
你负责只读审计和测试，不修改代码。仓库：/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4。
先读取 docs/handoff/2026-08-18-global-routing-audit-handoff.md。
审计 record_request.lua 与 apply_probe.lua 的 source-priority：failed probe 后普通成功请求是否能覆盖 available/disabled/cool_until；审计 routing/resolve 与真实 Router 在 URSM miss 时是否给出相同 runtime_routable 结论；审计 legacy/default/K2 key coverage。产出带 file:line、SQL/Redis 证据的报告和集成测试建议，不修改代码。
```

### Agent E：独立回归与发布门禁

```text
你负责验证，不修改业务代码。仓库：/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4。
先读取 docs/handoff/2026-08-18-global-routing-audit-handoff.md。
运行 go build ./...、go test ./bg ./admin ./domains/streaming/executors ./domains/ursm/v2/...；检查迁移、测试和版本状态。设计一个隔离 probe-canary 验证：enqueue -> claim -> direct -> pinned gateway -> node_probe_runs -> URSM tenant key -> routing resolve。检查 245 data-plane 配置为何无法验证后台恢复链，给出不改生产的验证方案。
```
