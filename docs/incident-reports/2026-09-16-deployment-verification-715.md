# 部署验证报告：715 pending 状态迁移（2026-09-16）

关联：`2026-09-16-apiclaude-apigpt-suyun-recovery-failure.md`（事件报告）·
修复 commit `bba08b922` · 本轮接线修复 commit `6f3d03073`

## TL;DR

1. **发现并修复了部署阻断缺陷**：迁移 715 只有 SQL 文件、没有 Go 启动链 ensure
   镜像。直接部署 bba08b922 的话，新代码写出的首条 `state='pending'` 会撞旧
   CHECK 约束（23514），observer 重试耗尽后事件追踪整体静默失效。已在
   `6f3d03073` 补齐接线 + 契约/接线/真库三层测试。
2. **迁移语义已在真实 PostgreSQL 17 上验证通过**（对 252 共享集群上的一次性
   库实测，验证后已清理）：旧约束升级、pending 写入、同路由互斥、幂等、
   down 危险路径、down→up 循环全部符合预期。
3. **真实环境部署被阻塞**：本机无 245 凭据（公网 25022 publickey 拒绝）、
   无 Docker/CGO 构建链、deploy-lib 符号链接目标缺失。runbook 见下，需持证
   操作员在一台具备构建能力的宿主上执行。
4. **两个与本次修复无关的既有缺陷被顺带发现**（详见"新发现"）。

## 一、部署前验证结果

| 项 | 结果 | 证据 |
|---|---|---|
| 修复代码在 main | ✅ | `bba08b922`（HEAD）；任务清单中的 `294383292` 是 amend 前的悬空提交，无分支包含，代码内容与 bba08b922 一致 |
| credentialstate 单测 | ✅ | `go test ./domains/credentialstate/...`（含新增 manager_available_persist_test.go） |
| routeincident 单测 | ✅ | `go test ./domains/routeincident/...`（含 pending 状态转换 4 项新测试） |
| 715 SQL 契约测试 | ✅ | `migration_715_test.go`（事务性、四态 CHECK、索引谓词、down 告警） |
| 715 接线静态断言 | ✅ | 同文件：applyMigrationsOnce 必须在 ensureRouteIncidentSchema 之后调用 ensure 镜像 |
| **真库生命周期矩阵** | ✅ | `migration_715_integration_test.go` @ PG 17.10：389 旧约束库升级、pending 插入、同路由第二条 pending/active 23505、非法 state 23514、幂等重放、pending 存在时 down 必须失败且 schema 保持新形态、清理后 down 成功且旧 CHECK 拒绝 pending、down→up 循环 |
| linux/amd64 构建 | ⚠️ | 本机不可行（见阻塞项）；部署脚本原生支持容器 CGO 回退 |
| 全新安装链路 | ⛔ SKIP | 既有缺陷（见"新发现"#1），与 715 无关，存量库不受影响 |

## 二、新发现（与本次修复无关，建议另开任务）

1. **全新安装启动链中断（严重，存量库不受影响）**：`sql/schema/01-schema.sql`
   快照（2026-09-14 dump）里 `request_logs_hot` 与分区父表 `request_logs`
   列类型系统性漂移（hot 侧 `protocol_conversion` bool→text、`customer_id`
   int8→text、`content_safety_score` jsonb→float8 等十余列）。
   `db.Open` → `ensureRequestLogsCurrentMonthView` 重建 canonical 视图时
   `UNION types text and boolean` 报 42804，启动链在到达 routeincident
   ensure 之前中止。实测路径：干净一次性库 → 00-prereqs + 01-schema（psql
   容忍语义）→ `db.Open` 稳定复现。`migration_715_integration_test.go` 的
   FreshChain 用例对该签名显式 SKIP，漂移修复后自动转为完整回归断言。
2. **迁移账本 `schema_migrations` 不在启动链内创建**：只有运维脚本
   （apply-db-revision-sequence.sh 等）和测试创建它。全新库裸 `db.Open`
   会在 701 的 stamp 处 42703。生产环境因引导脚本先建表而未暴露。

## 三、任务清单与仓库实际的三处出入（监控/验证口径已按实际修正）

| 任务清单写法 | 仓库实际 | 影响 |
|---|---|---|
| 迁移文件 `sql/migrations/startup/715_...sql` 手工执行 | 启动迁移 = Go ensure 链，SQL 文件仅是镜像文档 | 不要手工 psql 执行；二进制启动时自动应用（6f3d03073 之后） |
| 验证 SQL 查 `credential_states` 表 | 表不存在；实际是 `credential_state_log`（列 `credential_id, raw_model_name, available, recover_at, ...`） | 监控脚本已用真实表名 |
| 异常处理查 `settings.route_incidents` 的 `failure_to_active` | 该配置不存在；阈值硬编码 `DefaultThresholds()`=3（`store.go:121` 直接传默认值） | 阈值行为异常时核对代码版本，而非找配置 |
| 事件报告前置条件"代码先于迁移部署" | 与约束语义相反（约束必须先放行 pending）；接线后启动链内自动有序 | 715 SQL 头部注释已更正 |

## 四、部署 Runbook（待持证操作员执行）

前置：本报告时点 main = `6f3d03073`（含接线修复）。构建宿主需满足
deploy-seamless.sh 的构建矩阵（Linux/macOS + Docker，或
`LLM_GATEWAY_PREBUILT_BINARY` 带外产物）。

```bash
# 1. 测试环境（245 预发布；迁移由二进制启动链自动应用，无需手工 psql）
bash scripts/deploy-245.sh

# 2. 部署后立即核验（迁移就绪 = 异常处理第 1 条的自检）
ssh <245> 'journalctl -u llmgo-245.service --since "5 minutes ago" | grep -c 23514'
#   预期 0。若出现 23514：说明库还是旧约束（启动链被跳过/失败），先排查
#   ApplyMigrations 日志，不要回滚代码——回滚反而会让 pending 写入消失，
#   但事件追踪在约束修复前仍是坏的。

# 3. 观察（24h，每 2-4 小时一个 checkpoint）
export LLM_GATEWAY_245_DB_PASSWORD=...
bash scripts/monitor-245-incident-pending.sh $(date +%Y%m%d-%H%M)
#   报告落 .handoff/incident-pending-<label>.md，四项指标 + 结论模板内嵌。

# 4. 245 稳定后再走 154 生产（同一脚本族）
bash scripts/deploy-seamless.sh deploy 154
```

回滚（异常处理第 3 条的修正版）：
- 代码回滚即可安全独立执行：旧代码不写 pending，约束放行 pending 无副作用。
- **反向（先 down 迁移再回滚代码）有前置条件**：必须先把 `state='pending'`
  行清理/改写为 `recovered`，否则 down 的 `ADD CONSTRAINT` 校验旧 CHECK 时
  23514 整体失败（down 文件头部已警示，真库测试已钉住该行为）。

## 五、监控指标口径（前 24 小时）

- **M1 事件可见性**：`SELECT state, COUNT(*) FROM route_incidents GROUP BY state`。
  预期 pending:active ≈ 2:1（阈值 3 之下每条路由积压 ≤2 条 pending）。
- **M2 凭证恢复率**：`credential_state_log` 近 24h 有失败的凭证对中
  `available=true` 占比 >95%；`available=false AND recover_at IS NULL`
  计数应为 0（Bug 1 修复的直接证据：失败路径现在会持久化冷却时间戳）。
- **M3 误报率**：近 24h `failure_streak<3` 即自愈（从未可见）的事件占比，
  与 09-16 事件报告基线对比应降 60%+。部署后首个 checkpoint 先记录基线读数。
- **M4 迁移就绪**：`schema_migrations` 有 '715' 行 + CHECK/索引谓词含
  pending + journald 无 23514。

## 六、阻塞项（需要用户提供）

1. **245 访问凭据**：本机 `~/.ssh/id_ed25519`（86133-nbjl-win）未列入 245
   `authorized_keys`（公网 `8.136.114.245:25022` 拒绝）；252 亦无跳转密钥。
2. **构建宿主**：本机无 Docker 守护进程、无 zig/交叉 CGO 工具链；
   `deploy-lib` 符号链接目标 `../../../../ai-native-tools/deploy-lib` 在
   本机不存在（seamless 脚本无法运行）。此前部署显然出自其他宿主。
3. LLM_GATEWAY_245_DB_PASSWORD（监控脚本与 checkpoint 脚本共用）。
