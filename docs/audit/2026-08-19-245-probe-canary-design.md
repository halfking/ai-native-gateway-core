# 245 Probe-Canary 设计（仅评审稿）— 2026-08-19

## 状态与安全门禁

本文只记录隔离要求和验证顺序，**不是可直接执行的部署脚本**。当前代码尚未实现 `LLM_GATEWAY_CANARY_*` 的进程内 fail-closed 范围强制；在该能力完成并通过独立测试前，不得启动 `BG_MODE=full` canary，不得复制生产环境文件，不得连接共享生产 PostgreSQL/Redis，也不得执行任何手工 INSERT、DELETE 或 `FLUSHDB`。

任何后续执行必须由 ops 单独审批，并使用短期凭据、专用主机/容器、专用 Redis DB 或实例，以及经过代码强制限制的 canary tenant、credential 和模型白名单。文档中不保存连接串、密码、token 或真实主机操作命令。

## 目标链路

在隔离环境验证：

`enqueue → claim → direct → pinned gateway → node_probe_runs audit → URSM v2 K2 key → routing resolve`

验收要求：

- `node_probe_runs` 同时记录真实 `direct_ok`、`gateway_ok` 和 canonical `trigger_kind`；
- queue lease 在双轮探测及副作用阶段持续有效，租约丢失时旧 owner 不再写入副作用；
- URSM K2 key 只出现在 canary Redis namespace，runtime routing 与 dashboard 的 source-labelled 结果一致；
- canary credential 不得出现在生产 tenant 或生产候选集合中；
- audit、unknown-source、lease-lost 和 heartbeat 指标在观察窗口内符合发布门禁。

## 隔离模型（待实现后复核）

| 维度 | 245 生产实例 | 245 Canary 实例 |
|---|---|---|
| 监听端口 | 8781 | 独立端口（由 ops 分配） |
| background mode | 生产配置 | 仅在范围强制完成后启用 full |
| Redis | 生产实例/DB | 专用实例或明确隔离 DB |
| PostgreSQL | 生产 public schema | 推荐独立数据库；若共享，必须由代码和数据库策略双重限制 |
| tenant / credential | 生产全集 | 专用 canary tenant 和凭证集合 |
| model | 生产全集 | 明确 allowlist |
| notify | 生产 channel | 独立 channel 或不启用监听 |

**禁止的隔离假设：** 仅设置环境变量、仅使用不同端口、仅约定 credential ID，均不能替代代码层和数据库层的 fail-closed 约束。当前 `CredentialSelfcheckWorker` 的选择查询是全局的，因此在范围强制上线前，复制生产环境运行 full mode 会触碰生产凭证。

## 实现前置条件

1. 新增并测试 canary scope：所有 queue enqueue、self-check 选择、node probe submit、URSM 写入和 routing resolve 都拒绝 scope 外的 tenant/credential/model；缺失或格式错误的 scope 配置必须拒绝启动。
2. 将 scope 约束落到数据库策略或独立 canary 数据库，确保应用 bug 不能写入生产凭证；为 queue、node probe state、node probe runs 和 URSM key 建立只读审计查询。
3. 让启动流程支持专用配置文件，但不从生产 `.env` 复制未筛选的凭据；所有 API、PG、Redis secret 通过受控 secret manager 注入。
4. 将 `node_probe_runs.trigger_kind` 与 `credential_probe_queue.source` 的迁移、down guard 和测试作为同一发布单元。

## 验证顺序（ops 执行清单，非命令）

1. **Binary/schema**：确认运行版本包含 migration 538、baseline/installer schema 一致，且启动日志显示 scope 校验通过。
2. **Liveness**：访问版本和健康接口；确认失败是认证失败而不是数据库不可用。
3. **Enqueue/claim**：只通过受限管理 API 创建带唯一 task ID 的 `node_probe`；确认 queue 状态从 ready 到 running，source 为 `admin` 或批准的 canary source。
4. **双轮探测**：确认 direct 请求和 pinned gateway 请求都指向同一 credential；不得把普通路由成功当作 credential self-check 证据。
5. **审计**：按 canary task ID、tenant 和 credential 做只读查询，确认 `node_probe_runs` 有一行且 trigger kind、双轮结果、耗时完整。
6. **URSM**：在专用 Redis 中只读扫描 `ursm:v2:node:k2:*`，解析 K2 tenant segment，确认没有 scope 外 key。
7. **Routing parity**：对 canary tenant 做 resolve；runtime authoritative routing 必须 fail-closed，admin 结果必须明确标记 URSM miss/legacy 数据源，不能用 optimistic 默认掩盖缺 key。
8. **观察窗口**：至少观察 24 小时；`audit_persist_failed_total`、`audit_unknown_source_total` 和 `lease_lost_total` 应为 0，heartbeat 与 queue completion 持续有记录。

## 回滚原则

- 先停止 canary 实例，再由 ops 使用 task ID / canary tenant 的精确过滤清理测试数据；禁止跨 credential 批量删除。
- Redis 清理只能针对已确认的专用 canary DB/实例；禁止对共享或未确认 DB 执行 `FLUSHDB`。
- migration 538 down 在存在 `selfcheck` queue rows 时必须拒绝执行；先按审批流程 drain/remap，再执行回滚并复核两套约束。
- 245 生产 8781 和 154 生产实例不在本 canary 回滚范围内。

## 发布门禁

| 阶段 | 通过条件 |
|---|---|
| Canary | scope fail-closed 测试通过、全链路验证通过、24h 无审计/租约异常 |
| 245 全量 | canary 通过且 ops 明确批准 |
| 154 生产 | 245 稳定观察完成，新增 audit/lease 指标为零 |

参考：`docs/handoff/2026-08-19-global-routing-state-machine-remediation.md`、`docs/audit/2026-08-18-deploy-gate-and-canary-design.md`、`deploy-245` skill。
