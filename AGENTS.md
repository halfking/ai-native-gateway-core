# llm-gateway-go — AGENTS.md（维护者交接）

> 适用：未来任何 cold-start 切到本仓库的助手会话（不区分会话身份）。
> 写入时机：2026-10-09，由 root 会话 `mvs_b293ccc3be3343b9aa3fcdb0c41c1706` 在环境工具链缺失下生成。
> 上下文：本轮完成与 `origin/main` 的合并、冲突解决、修复提交推送与软链 SSOT 迁移。剩余的编译验证 / lite 冒烟 / 真库回归 / deploy-local 全链路验证因本机 `go` / `brew` / `docker` / Postgres / Redis 全部缺失未能完成，按以下任务清单继续。

## 仓库快照

- 路径：`/Users/huangxt/workspace/llm-gateway-go`
- 远端：codeup.aliyun.com（`origin/main`）
- HEAD：`7817f1c1a`（审计 pass：第二十节结论 + 第九之二节更正）→ `7dd0b4b67`（sanitize 五项修复）→ `e3e2b4ef`（migration 849 `SET LOCAL statement_timeout`）→ `d44a29f77`（web-mobile lockfile）→ `558953c52`（bash 3.2 兼容）→ `2bf5b3870`（deploy-seamless 依赖安装判据）
- 分支：仅 `main`；仓库多会话共享且推送频繁，提交前先 `git fetch && git status -sb` 避免覆盖他人改动
- Go 模块：见仓库根 `go.mod`
- 入口二进制：`cmd/gateway/main.go`（v1，`:8781`）、`cmd/gateway-v2/main.go`（v2，`:8782`，独立）

> **端口以部署现场为准，不要照抄本文件的默认值。** 两台机器都用蓝绿部署，
> 实际监听端口由 `/opt/llm-gateway-go/run/active-upstream.conf` 决定，
> nginx 通过 `upstream llm_local { include … }` 引用它。
> 2026-10-10 实测：**154 与 245 均运行在 `:8782`**，`slots/` 下只有 `8782 ->` 一个软链。
> 探活前先读该文件确认端口，不要假定 8781 还在监听——直连 `127.0.0.1:8781` 会
> 得到 connection refused，看起来像故障，实际服务健康。
> 经 nginx 的 `curl http://127.0.0.1/healthz` 是与部署形态一致的探活方式。

## 已完成（已推送）

1. `1127a3e3c`：合并主提交 + 冲突解决（`admin/dashboard_board_queries.go` 保留上游降级契约 + 探针排除积分源）
2. `df69ef0be`：`scripts/deploy-local-lib.sh` macOS timeout 可移植性修复（`timeout → gtimeout → 纯 bash 轮询看门狗`，轮询实现避免子 shell sleep 孙进程变孤儿拖满时限）
3. `7fd9b2ceb`：`tests/deploy_timeout_portability_test.sh` 回归门接入 `verify.sh`；7 套 deploy-local 测试 `rc=0`；变异验证确认有牙
4. `22e5e3f8f`：`scripts/deploy-lib` 软链 SSOT 路径迁移（`ai-native-tools/deploy-lib` → `ai-native-workspace/deploy-lib`，与本机实际 SSOT 一致）
5. `7817f1c1a`：审计 pass（2026-10-10 23:30）——`go build`/`go vet`/`security/*`+`streaming/*` 9 包全绿；**第九之二节「占位符 fail-open 透传」经 9 种拼写变体探针复验确认已不复现**（当前 HEAD 全程 fail-closed），该节结论已更正；仓库卫生干净。审计结论详见 `docs/audit/2026-10-10-sanitize-e2e-154-multimodel-report.md` 第二十节

## 剩余任务（按依赖顺序）

### 任务 1 — 编译 / vet / gofmt / 单元测试

**前置**：安装 Go（项目 `go.mod` 指定版本，常见 1.22+）。

**命令**（先 `export PATH="$HOME/sdk/go/bin:$PATH"`，见任务 2 的工具链提示）：

```bash
go build ./...
go vet ./...
gofmt -l .                                # 无输出即过
CGO_ENABLED=0 go test \
  ./admin/... ./bg/... ./domains-stats/... ./settings/...
```

> **`gofmt -l .` 目前输出 376 个文件，这是上游长期存量，不是你的回归。**
> 2026-10-10 审计实测：`go build ./...` rc=0、`go vet ./...` 零告警，
> 而 `gofmt -l .` 有 376 个未格式化文件。**不要顺手全量格式化**——
> 那会产生巨型 diff 并在多会话共享仓库里制造冲突面。只保证你本次改动的
> `.go` 文件已格式化（提交前跑 `gofmt -l <你改的文件>`）。

**已验证绿的子集**（2026-10-10 审计，`go test ./security/... ./domains/streaming/...`，rc=0）：

| 包 | 耗时 |
|---|---|
| `security/sanitize` | 5.2s |
| `domains/streaming` | 167.4s |
| `domains/streaming/executors` | 50.1s |
| `security/armor` `guardian` `sensitive`、`streaming/{integrity,state,executors/webcookie}` | ok |

sanitize / streaming 这条线在当前 HEAD 是全绿的，可直接作为改动后的回归门。

**已知故意红的负向闸门，不要修复也不要加白名单**（维护者策略）：

- `admin/request_logs_bodies_retirement_gate_test.go` 中的 `TestV1BodiesReadersAreAssessed`：27 文件 / 39 调用点，故意红，判据是「登记表覆盖全部 bodies 读方」。

**主门长期红的原因与对策**：

- `verify.sh:51` 在 `set -e` 下无条件 `go test ./...`；CI 上 `verify-ci.yml` 未对主作业加 `continue-on-error`。在故意红的负向闸门下，主门长期为红。这是**维护者策略**，不是回归，不要主动改 CI 或加白名单；任务 1 走的是子集 `./admin/... ./bg/... ./domains-stats/... ./settings/...`，可在不上 CI 的情况下给出"非负向闸门子集全绿"的事实。

### 任务 2 — lite 模式本地冒烟（最低门槛）

**前置**：仅 Go + SQLite（无需 docker / Postgres / Redis）。

**步骤**：

```bash
LLM_GATEWAY_STORAGE_MODE=lite \
LLM_GATEWAY_LITE_DB_PATH=/tmp/gateway-lite.db \
bash scripts/start-lite.sh
# 等待 /healthz 返回 ready=true
curl -fsS http://localhost:8781/healthz
# 也可显式指 host：curl -fsS http://<host>:8781/healthz
# 本地 lite 走 v1(:8781)。远程机器端口另见仓库快照处的蓝绿端口说明。
```

预期：服务可启动；未带凭据访问新端点应返回 `401`。`start-lite.sh` 内部走 `go run ./cmd/gateway`，等价于直接 `go run ./cmd/gateway` 加 lite 环境变量。

> **本机 Go 不在 `PATH` 上。** 工具链在 `~/sdk/go/bin/go`（1.27.1），
> 同类非标准前缀还有 `~/sdk/bash5/bin/bash`（5.2.0）。
> 跑任何 `go` 命令前先 `export PATH="$HOME/sdk/go/bin:$PATH"`。
> 参见第十九节：非标准前缀目录是本仓多次「某依赖不存在」误判的直接原因。

### 任务 3 — 真库回归（full 模式）

**前置**：安装 docker desktop（或 orbstack）+ Postgres + Redis。`brew` 不在时可用纯 docker run。

**示例**（macOS，无 brew）：

```bash
docker run -d --name llmgw-pg    -e POSTGRES_******************* -p 5432:5432 postgres:16
docker run -d --name llmgw-redis                                       -p 6379:6379 redis:7
```

**步骤**：

```bash
LLM_GATEWAY_STORAGE_MODE=full \
LLM_GATEWAY_DB_DSN='postgres://[REDACTED]@[REDACTED]:5432/postgres?sslmode=disable' \
LLM_GATEWAY_REDIS_ADDR=[REDACTED]:6379 \
bash scripts/deploy-local.sh
bash scripts/post-deploy-verify.sh     # SSOT 在 ~/workspace/ai-native-workspace/deploy-lib
```

连接信息与管理员凭据由用户侧决定，建议写 `.env.local`（不入 git）。

### 任务 4 — deploy-local 全链路回归门

```bash
bash scripts/deploy_timeout_portability_test.sh   # rc=0
bash tests/deploy_local_contract_test.sh           # 仅在 SSOT 在场时跑（CI runner 上不存在，会挂主门，因此本轮未接入 verify.sh）
```

## 关键约束

- **合并需符合业务逻辑**：避免重复实现功能；保留上游降级契约，使用探针排除的积分源。
- **测试断言需剥离注释后再比对**；按 helper 参数化（不要内联字符串）。`goFuncBody` 已重命名为 `goFuncBodyStrict` 修复与上游测试文件重名问题。
- **部署脚本 macOS timeout 可移植**：禁止裸 `timeout` 调用，必须走 `scripts/deploy-local-lib.sh` 暴露的 `_dl_timeout` 封装。
- **回归门设计**：`tests/deploy_timeout_portability_test.sh` 零外部依赖，含静态断言（禁裸 timeout 调用并报行号）+ 行为断言（仅在确实无 `timeout` / `gtimeout` 时跑）。
- **CGO**：测试用 `CGO_ENABLED=0` 避免环境问题。
- **依赖安装属用户侧决策**：本助手不做越权安装。
- **负向闸门不擅自加白名单**：维护者裁决。
- **SSOT 软链**：`scripts/deploy-lib` 已迁移到 `ai-native-workspace/deploy-lib`。其他机器拉到此 commit 后按需 rebind：
  - 若仍在 `ai-native-tools/` 下：`git checkout HEAD~1 -- scripts/deploy-lib`
  - 若也已迁到 `ai-native-workspace/`：无需任何操作

## 仓库内 `handoff` 命名的语义警告

仓库内 `domains/streaming/handoff_*`、`domains/hostedtask/handoff.go`、`admin/handoff_logs_handler.go`、`settings/handoff_specs.go` 中的 "handoff" 都是**业务流交接**（流式分块确认、托管任务交接、settings 1m 契约测试），不是"会话间交接"。下次遇到用户口语"用 handoff 技能把提示词放到会话里"这种说法，按本 `AGENTS.md` 对待；不要调用任何 builtin 名为 handoff 的工具（builtin 列表里也没有）。

## 已落地的回归门

```bash
bash tests/deploy_timeout_portability_test.sh
```

期望 `rc=0`；失败即 deploy-lib 出现了裸 `timeout` 调用或未走 `_dl_timeout` 封装。