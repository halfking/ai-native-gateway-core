# JournalSnapshot 与供应商错误：下一阶段环境门禁

> 最后验证：2026-08-29。适用于 `main` 中 JournalSnapshot durable receipt、candidate failure、provider error 聚合、credential error detail 的后续闭环。

## 1. 目标与边界

本阶段目标是取得 **真实 PostgreSQL、UI 和预发** 证据，并关闭已知契约缺口；不是直接执行生产发布。

- 245 是预发晋级门；245 gate 没有通过、没有保存记录时，禁止 154 操作。
- 任何数据库写入仅可在授权的隔离/预发数据库进行；不能猜测、导出或覆盖凭据。
- 不得使用 `LLM_GATEWAY_SKIP_INJECT`、`SSHPASS` 或备用私钥。
- 不得声称“零中断蓝绿”或“生产验证通过”，除非相应证据已经生成。

## 2. 环境准备

### 本地代码与版本

```bash
git fetch origin main
git switch main
git pull --ff-only

git rev-parse --short HEAD
cat version.json
```

`/version`、`/healthz` 和 `web/public/version.json` 必须在构建/部署前指向同一 release identity。若不一致，先停止并修复版本流水线；不要手工猜测 build sequence。

### 数据库和 Redis

真实 PG 测试必须使用授权的 `TEST_DATABASE_URL` 或运维提供的隔离/预发 DSN；不得连接生产库做试验。测试前确认：

1. migration 618（JournalSnapshot receipt）和 620（provider error tenant/bucket）已在目标 schema 应用；
2. candidate failure hot 表具备 `session_id`、`per_attempt_latency_ms`；
3. 用两个专用测试 tenant（例如 `audit-alpha`/`audit-beta`）和独立 request/credential ID；
4. Redis 使用隔离 namespace 或独立实例；
5. 所有测试数据都有可定位的前缀，执行后清理。

### 245/154 环境

远程验证前必须先通过 SSOT 注入：

```bash
env-injector inject aliyun-frontend-245
env-injector inject aliyun-gateway-154
```

按顺序执行：`local → llm.itestu.cn → 245 → 154`。只允许以业务域名验证，不能把公网 IP 当服务入口。

## 3. 必须产出的证据

将每次真实环境验证保存至不包含秘密的记录目录：commit、version、UTC 时间、命令、脱敏输出、失败原因和 gate 状态。

### 3.1 PostgreSQL：P0 数据隔离与聚合重放

验证以下结果：

- tenant admin 请求 tenant B 的 credential ID 返回与不存在相同的 404；super_admin 的 all-tenant 语义仅在明确角色下生效。
- `candidate_failure_logs_hot` 的 tenant A/B 相同 provider/model/error fingerprint 生成不同的 `provider_error_details` rows。
- 在同一十分钟 bucket 重跑聚合器，`occurrences` 不增加；新增 source failure 后仅更新该 bucket 的实际 count。
- provider error 表的 RLS/FORCE RLS 生效；worker 的事务局部 bypass 不残留到连接池。
- JournalSnapshot receipt 对相同 `(tenant_id, request_id, snapshot_version)` + 相同 payload 是 no-op；同一 identity 不同 hash 产生冲突；lease reclaim 不重复投影。
- candidate failure hot → promote → parent/view 后，admin detail 查询仍能看到相同 tenant 的数据。

真实 PG integration 未配置时，应让测试明确 `Skip` 并记录缺少的授权输入；不能将 skip 标为 pass。

### 3.2 API 与前端

后端必须覆盖：

- `/api/vendors/credentials/{id}/error-detail?hours=1|24|168` 的 200、400、404、各 DB 查询错误、rows scan/error 与 tenant A/B 越权。
- 后端错误 envelope 的 `error.code` / `error.detail` 与前端解析约定一致；若要引入 `message`/`trace_id`，必须先版本化契约并全链路测试。

前端必须覆盖：

- URL encoding、默认和三个 hours window、400/404/500；
- credential/hours 快速切换时旧响应不能覆盖新数据；组件卸载后不更新 state；
- daylight/night 两主题下 ErrorDetailTab token 存在且可读；
- 无 credential、空数据、错误状态的可访问性和布局。

### 3.3 监控与历史完整性

- 选定一个 response-body-missing counter 作为 alert/dashboard SSOT；不得对同一事件双计数而没有文档说明。
- 设计有界、低频、只读的历史 body-missing 扫描，不得回写 columnar 历史表。
- 记录 provider aggregation 的 bucket、RLS 与 writer failure 监控查询；避免把 tenant、request ID 等高基数值放入 Prometheus label。

## 4. 并行任务与串行门禁

可以并行，但只有主代理负责协调、共享事实、合并结果和最终 git 操作。

| 任务 | 可并行 | 交付物 | 串行依赖 |
| --- | --- | --- | --- |
| A：PG/RLS integration | 是 | integration test、脱敏执行记录 | migration 620 先在隔离库应用 |
| B：API tenant/error contract | 是 | pgxmock/route tests、契约说明 | A 不阻塞代码测试 |
| C：前端竞态与主题 | 是 | Vitest、浏览器截图/记录 | 需要可用 Vite/测试环境 |
| D：指标/历史扫描设计 | 是 | dashboard/alert/scan design 与测试 | 不得写历史表 |
| E：文档对齐 | 是 | ADR/audit/LP/runbook 更新 | 以 A-D 的已验证事实为准 |
| F：245 canary | 否 | gate 记录、commit/version/hash | A-C 与 local 验证通过后 |
| G：154 promotion | 否 | 154 gate 记录 | 仅在同一 245 record pass 后 |

## 5. 本地与发布门禁

所有合并前至少执行：

```bash
go test ./admin ./bg ./domains/requestjourney ./domains/dispatch ./domains/streaming/executors ./sql/migrations/startup -count=1
go test -race ./admin ./bg ./domains/requestjourney ./domains/dispatch ./domains/streaming/executors -count=1
go build ./...
go vet ./...
bash ~/.agents/skills/llm-gateway-deploy-test/test.sh --env local --dry-run
```

远程 apply 仅使用技能规定的晋级命令与 record-dir。245 gate 失败、缺失或 commit/version/hash 不匹配时必须停止，不能运行 154。

## 6. 当前已知未闭合项

- JournalSnapshot 仍需要真实 PG lease/reclaim/partial projection integration。
- provider aggregation 仍需要真实 PG RLS/bucket replay proof。
- ErrorDetailTab 缺前端竞态、浏览器双主题与可访问性证据。
- response-body-missing 缺历史扫描、告警阈值和 dashboard SSOT。
- 当前单端口发布仍不是 2 秒零中断蓝绿；Phase 2 双端口/Nginx 原子切流是独立任务。
