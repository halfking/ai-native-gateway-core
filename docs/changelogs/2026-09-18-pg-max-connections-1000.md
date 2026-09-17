# 2026-09-18 PG max_connections=1000 扩容 + 网关池上限可配置化

## 背景

用户需求：本地 PG 与 252 PG 的并发连接上限放大到 ≥1000。第一轮落地了 PG 服务端参数
（ALTER SYSTEM）与 `StorageConfig.Full.MaxConnections` 默认值 100→200；本轮按"逐项复核、
不信任声明"原则审计第一轮交付，修正发现的问题。

## 审计发现的问题（本轮修正）

1. **P0 — "storage pool 200 已生效"归因错误**：第一轮以本地 PG `idle 20 / active 11 /
   total 34` 作为"storage 池扩容生效"的证据。逐行复核 `cmd/gateway/storage_mode_init.go:100`
   发现 `initStorageMode` **仅在 lite 模式**构造 storage factory；full 模式（本地/252/245/154
   全部网关）返回 nil runtime，业务查询全部走 `db.Open` 的 pgxpool（硬编码 `MaxConns=32`）。
   因此 `LLM_GATEWAY_STORAGE_MAX_CONNECTIONS=200` 对运行中的 full 模式网关**不产生任何作用**，
   观测到的 34 条连接就是 db 主池 32 + psql + Citus daemon。`docs/storage/deployment-guide.md`
   的既有注释（"full 模式工厂分支保留桩，实际装配走 cmd/gateway 既有路径"）本已写明这一事实，
   第一轮验证时未核对。
   **修复**：给真正的池加上环境变量覆盖——`db.Open` 新增 `poolMaxConnsFromEnv`，读
   `LLM_GATEWAY_DB_MAX_CONNS`（默认 32 保持不变，坏值回落默认并 Warn）；启动日志从
   `"postgres connected"` 升级为带 `max_conns`/`min_conns` 字段，使池上限可用日志实证。

2. **P1 — "max_prepared_transactions 必须 ≥ max_connections 否则启动拒"表述错误**：
   vanilla PostgreSQL 对二者比例无启动约束（`max_prepared_transactions=0` 即禁用 2PC 也能
   启动）。该约束来自 **Citus**（2PC 分布式事务要求 `max_prepared_transactions ≥
   max_connections`，两台 PG 均为 Citus 13.3 镜像），本轮文档与 memory 已按此修正表述。
   参数值本身（1000）无需改动。

3. **P2 — 文档失真**：`config.example.yaml` 与 `docs/storage/deployment-guide.md` 仍写
   默认 100；deployment-guide 未指出 full 模式 gateway 实际 PG 池与 storage factory 无关。
   本轮全部修正（见改动清单）。

4. **流程缺口**：第一轮改了 `dl_write_env` 但未跑 deploy 脚本自带测试。本轮补跑
   `scripts/deploy-local-lib-envload_test.sh` 与 `bootretry_test.sh`，均 PASS；并在加入
   `LLM_GATEWAY_DB_MAX_CONNS` 行后复跑，仍 PASS。

## 改动清单

| 文件 | 改动 |
|---|---|
| `db/db.go` | `cfg.MaxConns = poolMaxConnsFromEnv(32)`；新增 `poolMaxConnsFromEnv`（env 解析，坏值回落默认）；启动日志带 `max_conns`/`min_conns`；注释说明 storage factory 与本池的关系 |
| `db/poolsize_test.go` | 新增 `TestPoolMaxConnsFromEnv`（8 形态：未设置/合法/带空白/非数字/0/负数/溢出/空串） |
| `scripts/deploy-local-lib.sh` | `dl_write_env` 增加 `LLM_GATEWAY_DB_MAX_CONNS` 行；修正两参数的作用域注释 |
| `scripts/deploy-252-gateway.sh` | `gateway.env` 模板加 `LLM_GATEWAY_DB_MAX_CONNS=64`（共享 PG 保守值）；修正 STORAGE_MAX 注释（标注其对 full 模式 gateway 无效） |
| `.env.local`（不入库） | `LLM_GATEWAY_DB_MAX_CONNS=200`（本地单 pod 独占 PG） |
| `config.example.yaml` | `full_storage.max_connections` 示例 100→200，注明作用域限制 |
| `docs/storage/deployment-guide.md` | 默认值三处 100→200；新增"full 模式 gateway 实际 PG 池由 db.Open 管理、用 `LLM_GATEWAY_DB_MAX_CONNS` 调"说明块 |
| `config/storage.go` | （第一轮已推）Full 默认 200 保留——对 lite SQLite `SetMaxOpenConns` 与未来 full 工厂接线仍正确 |

## PG 服务端落地（第一轮已完成，本轮复核）

| 实例 | max_connections | superuser_reserved | max_prepared_transactions |
|---|---|---|---|
| 本地 `llm-gateway-pg`（Citus 17.10） | 300 → **1000** | 3 → **10** | 600 → **1000** |
| 252 `pg-252-pg17`（Citus 17.10, podman） | 100（曾触顶 100/100）→ **1000** | 3 → **10** | 200 → **1000** |

均写入 `postgresql.auto.conf` 并重启容器生效；`SHOW max_connections` 实测 1000。
252 PG 为 245/154/252-dev 共享库，重启后 245（172.16.2.241:8781）与 154（172.16.2.209:8781）
healthz 均 `ready:true`。

## 容量账

- **共享 PG（252）**：可用 1000 − 10 reserved = 990。当前消费者：245 蓝绿（32×活跃单元）、
  154（32×活跃单元）、252-dev（64）、kaixuan-1 经 tunnel 的回退连接、psql/运维。合计 ≪ 990。
  新部署的 full 模式 pod 默认仍占 32，集群行为零变化。
- **本地 PG**：单 pod 独占，db 池 200 + 少量 psql，远低于 990。

## 验证

- `go test ./db/ -run TestPoolMaxConnsFromEnv -count=1`：8/8 PASS
- `go test ./config/... -count=1`：PASS（含默认值 200 断言）
- `bash scripts/deploy-local-lib-envload_test.sh` / `bootretry_test.sh`：PASS
- 本地/252 重部署后启动日志含 `postgres connected max_conns=200/64`（见部署记录）

## 遗留风险

1. **252 内存无护栏**：主机 14GB（available ~6.9GB），PG 容器无 memory limit，
   `work_mem=16MB`。1000 连接不会预分配内存（按需），但极端连接风暴（外部恶意/失控客户端
   打满 1000 活跃）时理论内存需求可超主机。缓解：网关侧池有界（32/64/200）；应急回缩命令
   `ALTER SYSTEM SET max_connections=400;` + `podman restart pg-252-pg17`（无需回滚代码）。
2. **245/154 owner 侧默认未变**：新二进制默认 db 池仍 32（集群预算设计），owner 如需更大
   per-pod 池，自行在部署 env 设 `LLM_GATEWAY_DB_MAX_CONNS` 并按 990 总预算记账。
3. **`docs/audit/2026-09-18-minimax-thinking-incident.md:118`** 仍记载"max_connections=100"
   ——事件时点的历史事实，按惯例不改历史审计文档。
