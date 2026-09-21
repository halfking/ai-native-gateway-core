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

---

## 第二轮（2026-09-18 同日晚）：三项候选事项落地

### ① 252 OOM 理论风险收口（work_mem + memory limit 双层）

先复核再动的实测（2026-09-18 晚）：

- 主机 15.5G total / available 7.2G / swap 4G 已用 ~2.6G；**40+ 容器共享**
  （redclaw 全家桶、memora、acc、nbjl、veritrans、smm、netbird、agent-companion 等）。
- PG 后端每进程 RSS 1.6-1.9GB 是 shared_buffers（3.5G）共享页重复计数；真实私有内存
  （host 侧 smaps Pss/Private）仅 **0.2-3.3MB/连接**，PG 全体进程 PSS 合计 **4.3G**。
  → 1000 idle 全回填的边际成本 ≈ +3G，不构成 OOM；风险集中在极端活跃风暴
  （每连接每个排序/哈希节点消耗 work_mem）。

落地（零重启）：

| 层 | 动作 | 持久性 |
|---|---|---|
| work_mem | `ALTER SYSTEM SET work_mem='8MB'` + `pg_reload_conf()`；新连接即 8MB（`SHOW work_mem` 实证），存量连接随 pgxpool MaxConnLifetime(30min) 自然轮换 | postgresql.auto.conf，重启不丢 |
| memory limit | `podman update --memory 10g --memory-swap 12g pg-252-pg17`；cgroup v1 `memory.limit_in_bytes=10737418240` 实证，当时用量 4.6G，PG 存活 | **不持久化**（见下） |

⚠️ **podman update 坑（4.9.4-rhel）**：只改运行时 cgroup，`podman inspect` 的
`HostConfig.Memory` 仍为 0——容器/主机重启即丢。兜底：root crontab 已加
`@reboot sleep 120 && podman update --memory 10g --memory-swap 12g pg-252-pg17`
（注释标记 pg-252-pg17-memlimit）；人为 `podman restart` 后需手动重跑同一命令。
限值语义：10G RAM + 2G swap；超限由 cgroup OOM kill 杀 PG（确定性爆炸半径，
保护同主机其他 40+ 容器），优于主机全局 OOM 随机杀。应急回缩路径不变
（上文遗留风险 1 的 `max_connections=400` 命令，配合 memory limit 双保险）。

选 8MB 而非更低的理由：252 PG 为 154 生产/245 预发共享库，保留适度排序余量；
极端风暴残余风险由 memory limit 保险丝兜底。

### ② 245/154 上调 LLM_GATEWAY_DB_MAX_CONNS 的决策材料（供 owner）

两台全量扫描（进程 env + systemd unit 文件双路）：**无一设置，全部默认 32**。

- 245（172.16.2.241）：`llmgo-245-canary@8782` 等单元，进程 env 无该变量；
- 154（172.16.2.209）：`llm-gateway-go-canary@8781` 等单元，unit 文件无该变量；
- PG 侧今晨归属：154≈20、245≈18 连接（idle 回填 <32，与默认池上限一致）。

**结论：无需上调。** 当前用量 ~20/实例，对 32 池上限余量充足，对 990 总预算余量更大。
未来如需上调：网关部署 env 加 `LLM_GATEWAY_DB_MAX_CONNS=N` + 重启生效，验证看启动
日志 `postgres connected max_conns=N`；记账约束 Σ(全部网关实例 N，含 252-dev=64) ≤ 990
（1000 − 10 superuser reserved）。

### ③ full 模式 storage factory 桩：评估结论 + 防再犯护栏

评估（源码逐点核对）：

- **接线不可取**：full 分支是 `ErrNotImplemented` 桩——"接线"意味着重新实现
  session/bodies/turns/request_log/state 五类 store 并搬迁 main 装配，工程量大且零
  现实收益（现有 db.Open 池 + `LLM_GATEWAY_DB_MAX_CONNS` 已是受控旋钮）。
- **删桩不可取**：桩支撑工厂分派逻辑测试（`factory_test.go`）并保留未来收敛点；
  full 概念还被活代码依赖（`AlignFullPostgresURL`，main.go:447；`Validate` full 分支）。
- **真混淆源是死配置 + 错误注释**：`config.StorageConfig.ApplyDefaults`（full→200
  默认）**无任何生产调用方**（仅测试调用），其注释"单 pod 总占 PG = 32 + 200 =
  232 conn"与生产事实矛盾——第一轮假阳性在注释层的根源。

落地（注释/告警级，零行为变更）：

| 文件 | 改动 |
|---|---|
| `cmd/gateway/main.go` | 启动期护栏：非 lite 模式且设了 `LLM_GATEWAY_STORAGE_MAX_CONNECTIONS` → Warn 声明其不封顶生产 pool、生效旋钮是 `LLM_GATEWAY_DB_MAX_CONNS`，把误配挡在启动期 |
| `config/storage.go` | `ApplyDefaults` 注释纠错：声明生产不消费本默认值、"232 conn"说法作废、池容量验证以启动日志 max_conns 为准 |
| `storage/factory/stubs.go` | 文件头补池调优指向：生产池走 `LLM_GATEWAY_DB_MAX_CONNS`，本工厂配置不影响生产网关 |

验证：`go build ./...` PASS；`go test ./config/ ./storage/factory/` PASS；
`go vet ./cmd/gateway/ ./config/ ./storage/factory/` 干净。
