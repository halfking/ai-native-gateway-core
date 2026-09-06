# env 154 blue-green 部署失败现场恢复 + 627 columnar-safe 重写（2026-08-31）

**环境**：env 154 (<env:HOST_154_IP>:25022)　**端点**： `http://127.0.0.1:8782`　**单元**：`llm-gateway-go-canary@8782.service`

## 事件

`scripts/deploy-seamless.sh deploy 154` 在 candidate 阶段 abort — 错误日志只显示 "未通过 healthz/readyz"，但具体哪个 probe 失败、curl exit 多少、body 内容、journal 上下文全部缺失。同期发现 env 154 上 `llm-gateway-go.service`（旧单元）正在端口 8781 上跑一个**没有 active 业务流量**的进程 + canary unit 在 8781 上做无意义的 restart-loop，端口 6060 已被占用导致 candidate 阶段 bind 失败。

## 根因

### 1. 列存（columnar）禁止 UPDATE/CTID，原始 migration 627 写法在 PG 上不可执行

`scripts/migrations/627_candidate_failure_logs_aggregation_id.sql` 在历史回填阶段写了：

```sql
UPDATE candidate_failure_logs_hot SET aggregation_id = ... WHERE id IN (SELECT id FROM ... FOR UPDATE);
```

PG 列存（access method `_heap` 的 columnar 变体）只支持追加，UPDATE/CTID 操作直接报 `cannot perform UPDATE on columnar relation` 或 `tuple UPDATE not supported`。审计实施报告 `docs/fixes/2026-08-31-data-closure-audit-implementation.md` 在 env 上 apply migration 时失败，但同一迁移在 dry-run + 单元测试通过——dry-run 用的是 heap 表，掩盖了真实执行环境差异。

### 2. canary unit 在 env 154 上实际跑的契约 ≠ repo 里描述的契约

repo 里 `deploy/llm-gateway-go-canary@.service` 是 slot-based：`ExecStart=/opt/llm-gateway-go/slots/%i/llm-gateway-go`，但 env 154 上系统管理员改成 `WorkingDirectory=/opt/llm-gateway-go` + `ExecStart=/opt/llm-gateway-go/llm-gateway-go`（跟随 active symlink），slot-based 是 dead code。同时 `Restart=no` 让 bind/DB 失败直接退出，不进入 restart-loop——表面上"干净"，但实际掩盖了端口冲突的真实状态。

### 3. deploy-seamless probe 失败原因被折叠成单行 "未通过 healthz/readyz"

旧实现：

```bash
remote_probe healthz/readyz; [[ -d ok ]] || abort "未通过 healthz/readyz"
```

操作员无法区分：

- curl exit code（timeout vs DNS vs refused vs 5xx）
- HTTP status + body 内容（gateway 是否 503、body 是否含 "request detail store not configured"）
- journal 最后 N 行（bind 失败？OOM？配置错？）

根因可归到 `scripts/deploy-lib/targets.sh` / `scripts/deploy-seamless.sh` 的设计选择——probe 全部成功后批量打日志，单一失败时折叠到一行。

## 修复

### 1. 627 列存安全重写：视图侧 `COALESCE(aggregation_id, -id)` + bigint-min 水印种子

**为什么不改 schema？** 列存列加是元数据变更（允许），但列重写/默认值需要 DETACH→DROP→RECREATE→ATTACH（M562 模式），现有列存分区有真实数据不可丢。视图合成方案 0 数据移动、0 停机。

**改动**：

- 父表 `candidate_failure_logs` 上 `ADD COLUMN aggregation_id bigint`（允许）
- **删除** UPDATE/CTID 回填块
- 视图 `candidate_failure_logs_unified` 投影改为 `COALESCE(aggregation_id, -id) AS aggregation_id`：`id` 是 BIGSERIAL，负值永不和 `candidate_failure_logs_hot_aggregation_id_seq` 的正值冲突
- 水印一次性种到 bigint-min（`-9223372036854775808`）：第一次 tick 把所有历史桶（含合成负值）重放到 `provider_error_details`；之后水印自然上移到 `max(synthesized_neg, real_seq_positive)`，新行按原逻辑继续推进
- 聚合器 SQL（`bg/provider_error_aggregator.go:147-244`）**不动**——`WHERE aggregation_id > last_source_id` 对负数正常工作

**down 注释明示**：down 后历史行又回到 aggregator 视野外（视图 COALESCE 不再合成），这是**设计意图**——牺牲回滚能力换取 forward 正确性。

**installer 重 embed 627 down**：commit `599dde3cf` 同步 `scripts/migrations/627_*.sql.embeddata.go`，避免运行时与 SQL 不一致。

### 2. env 154 现场恢复

```bash
ssh 154 '
  systemctl disable llm-gateway-go.service          # 释放 8781/6060
  kill -9 <legacy pid>
  systemctl stop    llm-gateway-go-canary@8781.service
  systemctl disable llm-gateway-go-canary@8781.service
  ln -sfn releases/1834-3b349793 current
  systemctl restart llm-gateway-go-canary@8782.service
  bash install-blue-green-assets.sh
'
```

恢复后 `curl http://127.0.0.1:8782/version` 返回 200，`build_seq=1834 git_sha=3b349793`，active 端口 8782/6060 干净。

### 3. deploy-seamless probe 可观测性

新增 `remote_probe <url> <timeout>` helper：

- 单 endpoint 轮询直到 2xx 或超时
- 失败时返回「超时 + 最后一次 curl exit + body」

probe 循环拆开：

- 每个 probe 单独跑，分别打印 OK
- 失败时先打印 curl 退出码 + body，再拉 candidate 服务最后 30 行 journal，最后 abort

post-handoff 阶段也改成同样的格式——确保切完 symlink 后的回退路径同样可观测。

配置文件：`PROBE_TIMEOUT_SECS`（默认 30s）。

**URL 安全契约**（commit `130abc6a2`）：`remote_probe` 只接受 `http://127.0.0.1:<port>/<path>` 形式，端口必须在已知健康探针白名单（8781/8782/9090），路径必须 `/healthz|/readyz|/version`——防止恶意 URL 经 helper 打到外网。

### 4. canary unit 契约 pin

repo `deploy/llm-gateway-go-canary@.service`：

- 从 slot-based `ExecStart=/opt/llm-gateway-go/slots/%i/llm-gateway-go` 改为跟随 active symlink + `Restart=on-failure` + `RestartSec=5s`
- 与 env 154 实际跑的契约对齐

`scripts/install-blue-green-assets.sh`：检测 env 上老 unit，先备份到 `<unit>.pre-blue-green-assets-<ts>` 再装新契约。

`scripts/deploy-lib/targets.sh`：

- 删除 `candidate_binary` 字段（dead config，从来没被读过）
- 注释里写明恢复路径（如果未来重新启用 slot-based）

**为什么删而不是 wire 它？** canary unit 已经决定跟随 active symlink（不是 slot）；slot-based 设计是未实现的"应该可以"的代码——再 wire 一个已经决定不用的概念只会增加 dead code 表面。

## 验证

| 项目 | 结果 |
|---|---|
| `git diff --check` | ✅ |
| `gofmt -l` | ✅ 0 输出 |
| `go vet ./...` | ✅ |
| `go build ./...` | ✅ |
| PG dry-run（heap 表） | ✅ |
| PG apply 627（列存，env 154） | ✅ `column aggregation_id` 已加，视图 COALESCE 投影就位 |
| env 154 `/version` (8782) | ✅ 200 `{"build_seq":1834,"git_sha":"3b349793",...}` |
| env 154 PID 27560 uptime | ✅ 03:15:59，无 restart |
| env 154 NRestarts 属性 | ✅ 空（0 次 restart） |
| env 154 端口 8781 | ✅ 无 listener（已释放） |
| env 154 systemd units | ✅ 仅 `llm-gateway-go.service` + `llm-gateway-go-canary@.service`，slot 8781 unit 已 disable |

## 后续动作

- ✅ 5 commits + 1 merge pushed to `origin/main`（`be24a1a92` → `86839eedf`）
- ⏳ 154 上 1839-d1b950f3 release 已上传但 current 仍 1834——下一次 `deploy-seamless.sh deploy 154` 验证新 probe diagnostics 输出格式
- ⏳ audit-data-closure-3 把这次 incident 纳入"列存安全迁移"模式库（`scripts/migrations/*.sql` 全文审查 → 不允许列存表上 UPDATE/CTID）
- ⏳ `docs/deploy/06-deployment.md` 增补"列存 migration 写法"附录

## 经验教训

### 1. dry-run 不等于 dry-run-in-prod

> 627 migration 在 dry-run + 单测通过，但真实列存环境拒绝执行。

**未来防御**：

- CI 增加 "apply migration to ephemeral columnar fixture" 步骤（fixture 走 cstore_fdw + 1MB 数据）
- migration 模板加 `BEFORE-APPLY` 注释：「如果本表是 columnar，禁止使用 UPDATE/CTID；改用视图合成 + 水印」

### 2. dead code 比 missing code 更危险

> slot-based canary unit 在 repo 里描述得头头是道，但 env 154 早就跑了另一份契约。slot-based 是 dead code；它让 deploy 脚本误以为 env 上存在 slot 目录。

**未来防御**：

- `tests/deploy_blue_green_contract_test.sh` 加一条：「repo unit 描述必须匹配实际部署契约」（grep unit 内容 + ssh env 比对）
- 不允许 repo + env 长期分叉；要么升级 env，要么把 env 的写进 repo

### 3. probe 失败原因的折叠 = 现场恢复的"黑暗模式"

> 单行 "未通过 healthz/readyz" 让操作员无法分辨 bind/DB/timeout/5xx。

**未来防御**：

- probe helper 必须显式打印 curl exit + body + 关联 journal
- URL 白名单强制（防 SSRF 走 probe helper 出去）
- L4 验证（业务真实请求）作为部署最后一步，而非 healthz

### 4. 同类事故历史

| 时间 | 事故 | 同模式 |
|---|---|---|
| 2026-08-25 | env 154 端口冲突导致 candidate 阶段 abort | ✅ bind 失败未观测 |
| 2026-08-27 | Request Detail 503（旧 binary + 新代码不兼容） | ✅ deploy 后未做业务 smoke |
| 2026-08-31 | **本次**：627 列存 UPDATE 失败 + env 154 unit 契约分叉 + probe 折叠 | ✅ 完全新模式（dry-run vs prod 差异） |

**结论**：列存 migration + deploy 契约 + probe 可观测性三者联动——单点改进都不够，必须同时收紧。