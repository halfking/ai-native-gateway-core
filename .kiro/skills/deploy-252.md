# 252 / 154 部署技能边界

## 当前结论

> **252 当前是数据库与基础设施节点，不是 `llm-gateway-go` 运行节点。**
>
> 2026-08-16 的只读核查确认：252 上运行 PostgreSQL/PG17 等 Podman 容器及其他平台设施；不存在 `llm-gateway-go.service`、Go gateway 进程或 `8780/8781` gateway 监听端口。

因此 252 的 gateway runtime contract 保持：

```text
support: deferred
service_manager: ""
service_name: ""
binary_path: ""
web_path: ""
health_url: ""
rollback_policy: refuse
```

canonical CLI 会在构建、锁、SSH 或任何文件/远端操作之前拒绝 252 的 gateway `deploy`、`rollback` 和其他变更动作。252 没有独立的 canonical `verify` 子命令；健康检查应通过 245/154 的正式部署与验证流程执行。`deploy-to-252.sh` 是冻结的历史入口，不得用于上传 gateway 二进制、执行 migration、替换文件或重启服务。

## 正确的网关晋级路径

```text
local → dev → 245 / llmgo.kxpms.cn → 154 / llm.kxpms.cn
```

- **245**：预生产/晋级门禁，使用 `llm-gateway-go.service` 与本机 `http://127.0.0.1:8781/healthz` 合约。
- **154**：生产网关，使用 `llm-gateway-go.service` 与本机 `http://127.0.0.1:8781/healthz` 合约。
- **252**：数据库/基础设施节点；必要时可作为 154 的 SSH 跳板，但不是 gateway 部署目标。
- 245 gate 未通过时，禁止执行 154 晋级。

### 只读查看 252 契约

```bash
./scripts/deploy.sh plan 252
```

### 252 变更动作的预期结果

以下命令应在任何构建、锁、SSH 或远端操作前返回退出码 `64`：

```bash
./scripts/deploy.sh deploy 252
./scripts/deploy.sh rollback 252
./scripts/deploy.sh deploy 184       # 历史别名，解析到 252
```

独立旧入口同样 fail-closed：

```bash
./deploy-to-252.sh                  # 返回 64，不连接 252
```

## 252 基础设施职责

252 上的 PostgreSQL/PG17 是网关及其他服务可能使用的后端基础设施。数据库 migration 必须通过独立、已授权的数据库变更流程处理；不得借助已冻结的 gateway 部署脚本隐式上传二进制、重启服务或重复执行 migration。migration 519 已在 252 登记并验证，本任务不重复执行。

252 的数据库、容器和平台服务应按照各自的基础设施 runbook 管理；本文件不定义其 systemd、容器编排或 gateway healthz 合约。

## 相关命令与文档

- `scripts/deploy.sh plan 252`：查看 deferred contract，纯只读。
- `scripts/deploy.sh deploy 245`：245 canonical 晋级入口，须先通过环境注入与门禁。
- `scripts/deploy-154.sh`：154 生产部署入口，须在 245 gate 通过后执行。
- `deploy-to-252.sh`：冻结的历史入口，任何调用均 fail-closed。
- `scripts/deploy-lib/targets.sh`：canonical target contract 与 deferred 门禁。
- `~/.agents/skills/llm-gateway-deploy-test/SKILL.md`：local → dev → 245 → 154 的当前晋级规范。

## 历史说明

- 184 已退役，并作为历史别名解析到 deferred 的 252。
- 旧的“252 + 154”同时部署流程不属于当前晋级路径。
- 如果未来恢复 252 gateway runtime，必须先重新确认服务管理器、二进制路径、环境文件、健康 URL、认证和回滚契约，再建立新的 canonical target；不得解除本文件或旧脚本的 fail-closed 保护。
