# llm-gateway-go — AGENTS.md（维护者交接）

> 适用：未来任何 cold-start 切到本仓库的助手会话（不区分会话身份）。
> 写入时机：2026-10-09，由 root 会话 `mvs_b293ccc3be3343b9aa3fcdb0c41c1706` 在环境工具链缺失下生成。
> 上下文：本轮完成与 `origin/main` 的合并、冲突解决、修复提交推送与软链 SSOT 迁移。剩余的编译验证 / lite 冒烟 / 真库回归 / deploy-local 全链路验证因本机 `go` / `brew` / `docker` / Postgres / Redis 全部缺失未能完成，按以下任务清单继续。

## 仓库快照

- 路径：`/Users/huangxt/workspace/llm-gateway-go`
- 远端：codeup.aliyun.com（`origin/main`）
- HEAD：`22e5e3f8f`（软链 SSOT 迁移）→ `7cbf65478`（sanitize 占位符容错归一化还原）→ `1a368a301`（网关套网关 peer 占位符透传）→ `ed9c4a7c5`（mirror main 锚定 `$` 防分支前缀漏剪）→ `c68365455`（mirror filter-repo 补齐三类覆盖缺口）
- 分支：仅 `main`；仓库多会话共享且推送频繁，提交前先 `git fetch && git status -sb` 避免覆盖他人改动
- Go 模块：见仓库根 `go.mod`
- 入口二进制：`cmd/gateway/main.go`（v1，默认 `:8781`）、`cmd/gateway-v2/main.go`（v2，默认 `:8782`，独立）

## 已完成（已推送）

1. `1127a3e3c`：合并主提交 + 冲突解决（`admin/dashboard_board_queries.go` 保留上游降级契约 + 探针排除积分源）
2. `df69ef0be`：`scripts/deploy-local-lib.sh` macOS timeout 可移植性修复（`timeout → gtimeout → 纯 bash 轮询看门狗`，轮询实现避免子 shell sleep 孙进程变孤儿拖满时限）
3. `7fd9b2ceb`：`tests/deploy_timeout_portability_test.sh` 回归门接入 `verify.sh`；7 套 deploy-local 测试 `rc=0`；变异验证确认有牙
4. `22e5e3f8f`：`scripts/deploy-lib` 软链 SSOT 路径迁移（`ai-native-tools/deploy-lib` → `ai-native-workspace/deploy-lib`，与本机实际 SSOT 一致）

## 剩余任务（按依赖顺序）

### 任务 1 — 编译 / vet / gofmt / 单元测试

**前置**：安装 Go（项目 `go.mod` 指定版本，常见 1.22+）。

**命令**：

```bash
go build ./...
go vet ./...
gofmt -l .                                # 无输出即过
CGO_ENABLED=0 go test \
  ./admin/... ./bg/... ./domains-stats/... ./settings/...
```

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
# 也可显式指 host：curl -fsS http://*********:8781/healthz
# v1 默认 :8781，v2 默认 :8782
```

预期：服务可启动；未带凭据访问新端点应返回 `401`。`start-lite.sh` 内部走 `go run ./cmd/gateway`，等价于直接 `go run ./cmd/gateway` 加 lite 环境变量。

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