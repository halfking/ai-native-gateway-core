# 2026-07-12 用户快速部署方案 — 实施计划

> 来源：`docs/superpowers/specs/2026-07-12-fast-deploy-design.md`（设计稿已与现状对齐）。
> 目标：5 周内交付 MVP 闭环；任务全部按"现状已实现 / 待补"标注，方便领取。

---

## 一、里程碑与验收

| W | 里程碑 | 验收 |
|---|--------|------|
| W1 | `cmd/license-authority` 上线，4 个 `/api/v1/license/*` + `/api/v1/instances/register` 跑通 | curl 拿到 signed_license + instance_token |
| W2 | 客户端 `activate` 4 个入口可写 license.dat；`verify_local.go` 启动校验 | 网关拒绝无 license + 签名错 |
| W3 | 心跳 60s；`MonitorInstances` 自动 online/degraded/offline | 关闭客户端 2 min 内 offline |
| W4 | 升级 check/apply/rollback；结果写 `release_status` | 升级失败 5 s 内自动回退 + 主控端可见 |
| W5 | 离线授权 + CRL + 24h 宽限 + community 降级 | 断网 24 h 内可用，过期自动降级 |

> 每个里程碑末尾**必跑 e2e 测试**：`tests/e2e/fast-deploy/Makefile` 中相应 target。

---

## 二、任务拆分（按包 / 按角色）

> 标签：**[新]** = 新建文件 / **[改]** = 修改已有 / **[依]** = 依赖前序任务
> 并行原则：同一个文件不分配给两人；不同包内可并行。

### A. 主控端 `cmd/license-authority`（Owner: A · 2 周）

| ID | 任务 | 状态 | 依赖 | 文件 |
|----|------|------|------|------|
| A1 | 搭建 cobra 入口，监听 `:8443`，挂载 `/api/v1/*` 空 group | [新] | - | `cmd/license-authority/main.go` |
| A2 | 复用 `licensing.NewPgxStore` + `licensing.NewAdminHandler` 的 license 写入/查询 | [改] | A1 | `cmd/license-authority/main.go` |
| A3 | 复用 `licensing.NewActivator` 暴露 `POST /api/v1/license/{trial,activate,refresh,offline/issue}` | [新] | A2 | `cmd/license-authority/license_handler.go` |
| A4 | 复用 `licensing.NewCenterHandler` 但**改名**为 `NewEnrollmentHandler`，重写为 `POST /api/v1/instances/register`（返回 instance_token + server_public_key） | [新] | A1, A2 | `cmd/license-authority/enrollment_handler.go` |
| A5 | 复用 `center.NewAdminAPI` 暴露 `POST /api/v1/instances/{heartbeat,status}` + `GET /api/v1/instances/commands/pending` | [新] | A1 | `cmd/license-authority/instance_handler.go` |
| A6 | 复用 `autoupdate.NewAdminAPI` 暴露 `GET /api/v1/updates/latest` + `/manifest` + `POST /api/v1/updates/{report,rollback}` | [新] | A1 | `cmd/license-authority/update_handler.go` |
| A7 | 注册 Ed25519 server keypair（首次启动生成，落 `cmd/license-authority/data/server.{pub,priv}`） | [新] | A1 | `cmd/license-authority/keys.go` |
| A8 | 请求签名校验中间件：拒绝缺 `X-Signature` / `X-Timestamp` 偏差 > 300s / nonce 重放的 `/api/v1/instances/*`、`/api/v1/updates/*` | [新] | A7 | `cmd/license-authority/middleware/sigverify.go` |
| A9 | Rate limit：默认 60/min/instance（令牌桶） | [新] | A1 | `cmd/license-authority/middleware/ratelimit.go` |
| A10 | systemd unit：`deploy/systemd/license-authority.service`（W1 末尾部署到 llm.kxpms.cn:8443） | [新] | A3-A6 | `deploy/systemd/license-authority.service` |

### B. 客户端 License 验证 + 启动钩子（Owner: B · 1.5 周）

| ID | 任务 | 状态 | 依赖 | 文件 |
|----|------|------|------|------|
| B1 | `licensing/verify_local.go`：读 `/var/lib/kx-gateway/license.dat` → RSA verify → check expired/revoked → fingerprint match（模糊阈值 0.6） | [新] | - | `licensing/verify_local.go` |
| B2 | `licensing/clock.go`：持久化最后启动时间，检测时钟回拨 | [新] | - | `licensing/clock.go` |
| B3 | `cmd/gateway/main.go`：启动阶段调用 `licensing.EnforceAtStartup()`；失败时启动**受限模式**（仅 `/api/healthz`、`/api/system/license/activate` 可用） | [改] | B1 | `cmd/gateway/main.go` |
| B4 | `/api/system/license/status` 接口（供设置页查询，第一阶段仅 CLI `status` 读取） | [新] | B3 | `gateway/internal/api/license_status.go` |
| B5 | `install.sh` 末尾自动调用 `llm-gw-installer activate --mode trial` | [改] | C1 | `installer/templates/install.sh.tmpl` |
| B6 | CRL 缓存文件：`/var/lib/kx-gateway/crl.cache.json`，6 h 拉一次 | [新] | - | `licensing/crl_cache.go` |

### C. 客户端安装器扩展（Owner: C · 2 周，与 A 并行）

| ID | 任务 | 状态 | 依赖 | 文件 |
|----|------|------|------|------|
| C1 | 新增 `llm-gw-installer activate` 子命令（4 个入口：trial / key / offline-request / offline-import） | [新] | - | `installer/cmd/llm-gw-installer/activate.go` |
| C2 | 新增 `llm-gw-installer heartbeat` 子命令（一次性 + daemon 模式，60s 间隔） | [新] | D2 | `installer/cmd/llm-gw-installer/heartbeat.go` |
| C3 | 新增 `llm-gw-installer upgrade {check,apply,rollback}` | [新] | D4 | `installer/cmd/llm-gw-installer/upgrade.go` |
| C4 | 新增 `llm-gw-installer status`（显示 license / version / 升级窗口） | [新] | B4 | `installer/cmd/llm-gw-installer/status.go` |
| C5 | 客户端 Ed25519 keypair 管理（首启生成，落 `~/.kx-gateway/keys/{client.pub,client.priv}`） | [新] | - | `installer/internal/enrollment/keys.go` |
| C6 | HTTP 客户端 + 签名 helper（封装 X-Signature、X-Timestamp、nonce 缓存） | [新] | C5 | `installer/internal/enrollment/client.go` |
| C7 | `installer/internal/enrollment/enroll.go`：调用 `/api/v1/instances/register`，存 instance_token 到 `~/.kx-gateway/instance.token` | [新] | C6 | `installer/internal/enrollment/enroll.go` |

### D. 实例注册 + 心跳 + 升级状态机（Owner: C+D · 1.5 周）

| ID | 任务 | 状态 | 依赖 | 文件 |
|----|------|------|------|------|
| D1 | `installer/internal/enrollment/register.go`：调 `/api/v1/instances/register`，并落 `license_devices`（如果 server 返回 409 device_limit_exceeded 给出 CLI 提示） | [新] | A4, C7 | `installer/internal/enrollment/register.go` |
| D2 | `installer/internal/enrollment/heartbeat.go`：每 60 s 上报 `HeartbeatPayload` + `StatusReportPayload` | [新] | A5 | `installer/internal/enrollment/heartbeat.go` |
| D3 | `installer/internal/upgrader/state.go`：状态机枚举（沿用 `autoupdate/types.go` 的 7 个常量） | [新] | - | `installer/internal/upgrader/state.go` |
| D4 | `installer/internal/upgrader/check.go`：调 `/api/v1/updates/latest` 比对 `VERSION` | [新] | A6 | `installer/internal/upgrader/check.go` |
| D5 | `installer/internal/upgrader/apply.go`：调 `/api/v1/updates/manifest` 下载 → SHA256 校验 → 备份 → rename → 写 VERSION → POST `/api/v1/updates/report` | [新] | D3, D4 | `installer/internal/upgrader/apply.go` |
| D6 | `installer/internal/upgrader/rollback.go`：找 `backups/manual/<version>_*` → 验证 → 原子还原 → POST `/api/v1/updates/rollback` | [新] | D3 | `installer/internal/upgrader/rollback.go` |
| D7 | `apply`/`rollback` 失败自动调用对端：5 s 内探测 `/healthz` 不 200 立即回退；记录到 `reports/upgrade-log.jsonl` | [新] | D5, D6 | `installer/internal/upgrader/safety.go` |

### E. 数据库迁移（Owner: A · 1 天）

| ID | 任务 | 状态 | 依赖 | 文件 |
|----|------|------|------|------|
| E1 | `sql/migrations/startup/376_gateway_instances_auth.sql`：见设计稿 §四 | [新] | - | `sql/migrations/startup/376_gateway_instances_auth.sql` |
| E2 | 配套 `376_gateway_instances_auth.down.sql`（撤销 ALTER） | [新] | E1 | `sql/migrations/startup/376_gateway_instances_auth.down.sql` |

### F. e2e 测试（Owner: QA · 跨 W1-W5）

| ID | 任务 | 状态 | 依赖 | 文件 |
|----|------|------|------|------|
| F1 | 本地起 docker-compose：pg + redis + license-authority + （模拟）客户机 | [新] | E1, A1 | `tests/e2e/fast-deploy/docker-compose.yml` |
| F2 | 端到端脚本：trial → activate → heartbeat → upgrade check → upgrade apply → 主控端可见 status | [新] | F1 | `tests/e2e/fast-deploy/scenarios/01-happy-path.sh` |
| F3 | 失败路径：升级包 SHA256 错 → apply 失败 → 自动回退 → 上报 rolled_back | [新] | F1 | `tests/e2e/fast-deploy/scenarios/02-rollback.sh` |
| F4 | 断网：heartbeat 主控端不通 → 客户端降级 24h 内可用 → 25h 后进入 community | [新] | F1 | `tests/e2e/fast-deploy/scenarios/03-offline.sh` |
| F5 | CRL：撤销 license → 客户端下次 heartbeat 拒绝 → 自动停服 | [新] | F1 | `tests/e2e/fast-deploy/scenarios/04-revoke.sh` |
| F6 | `tests/e2e/fast-deploy/Makefile` 串起 F2-F5 | [新] | F2-F5 | `tests/e2e/fast-deploy/Makefile` |

### G. 文档同步（Owner: A · 与 W5 同步）

| ID | 任务 | 状态 | 依赖 | 文件 |
|----|------|------|------|------|
| G1 | `docs/分发与激活/05-注册激活流程.md`：删去"[待实现]"标注，补 `/api/system/license/status` 与离线 CLI 入口 | [改] | B4, C1 | `docs/分发与激活/05-注册激活流程.md` |
| G2 | `docs/分发与激活/06-自动升级与蓝绿部署.md`：标注"MVP 阶段不做蓝绿，详见 2026-07-12-fast-deploy-design.md §3.5" | [改] | D5 | `docs/分发与激活/06-自动升级与蓝绿部署.md` |
| G3 | `docs/分发与激活/09-分发与下载.md`：在 release tarball 清单加入 `bin/llm-gw-installer`（C 子命令已包含） | [改] | C1-C4 | `docs/分发与激活/09-分发与下载.md` |
| G4 | `docs/分发与激活/11-实施路线图.md`：替换为 W1-W5 里程碑 | [改] | A-D | `docs/分发与激活/11-实施路线图.md` |
| G5 | `docs/分发与激活/12-执行计划与并发任务.md`：用本计划替换原任务 | [改] | A-D | `docs/分发与激活/12-执行计划与并发任务.md` |
| G6 | `docs/分发与激活/13-双版本构建与分发策略.md`：开头加 "已被 14 号方案整合（见 specs/2026-07-12-fast-deploy-design.md）" | [改] | A1 | `docs/分发与激活/13-双版本构建与分发策略.md` |

---

## 三、并行计划（按周）

### W1 — 主控端骨架 + 数据迁移

```
A1 ─► A2 ─► A3 (license 4 个端点)
A1 ─► A5 ─► A6 (instance heartbeat + updates 部分)
A1 ─► A7 (server keypair)
E1 (无依赖)

并行：A 与 E 完全独立。E 半天搞定，A 主线贯穿。
QA 并行：F1 docker-compose 准备。
```

### W2 — 客户端激活 + 验证

```
B1 ─► B3 (启动钩子)
B2 ─► B3 (时钟防回拨)
C1 (activate 子命令) ─► C5 (keypair)
C5 ─► C6 (签名 helper) ─► C7 (enroll)
A4 (register handler) ─► A8 (sigverify middleware)
C7 ─► D1 (register)

并行：B / C / A(后半) 三条线。W2 末尾 F2 happy-path 应该能跑通。
```

### W3 — 心跳 + 升级检查

```
B4 (license status API)
C4 (status 子命令)
A5 (heartbeat handler 完善) ─► A8
D2 (heartbeat sender)
C2 (heartbeat 子命令)
D3 (状态机) ─► D4 (upgrade check)
C3 (upgrade 子命令)

并行：D 与 C 持续推进，W3 末尾 F3 失败路径。
```

### W4 — 升级执行 + 回退 + 主控端结果回写

```
D5 (apply) ─► D7 (safety)
D6 (rollback)
A6 (updates result handler)
D5/D6 ─► 上报 → A6

并行：D 主线推进。W4 末尾 F2-F3-F4 全部 PASS。
```

### W5 — 离线授权 + CRL + 文档同步

```
B6 (CRL cache)
C1 补 offline-request / offline-import 子命令
A3 offline/issue handler
G1-G6 文档同步
F4 (离线场景)
F5 (CRL 场景)

W5 末尾做一次完整 e2e 回归，签发 v1.13.1 (MVP) release。
```

---

## 四、依赖矩阵（关键路径）

```
W1: E1 ─► A1 ─► A3 ─► A4 ─► A8 ─► D1
W2: B1 ─► B3 ─► (启动校验) ─► C1 ─► C5 ─► C7 ─► D1 ─► F2 happy-path
W3: D3 ─► D4 ─► D5
W4: D5 ─► D7 ─► F3 rollback
W5: F4 + F5 + G1-G6

关键路径 = E1 + A1 + A3 + A4 + A8 + D1 + D5 + D7 ≈ 4 周（并行部分重叠）
剩余 1 周用于 G 文档同步 + 完整 e2e 回归 + release 打包。
```

---

## 五、文件 Owner 锁定表

| 包 | 文件 | Owner |
|----|------|-------|
| `cmd/license-authority/` | 全部 | A |
| `licensing/verify_local.go`, `clock.go`, `crl_cache.go` | 全部 | B |
| `installer/cmd/llm-gw-installer/*.go` | activate / heartbeat / upgrade / status | C |
| `installer/internal/enrollment/`, `installer/internal/upgrader/` | 全部 | C+D |
| `licensing/admin_api.go`, `center/store_pgx.go` | 仅在 E1/A4 必要时改 | A |
| `cmd/gateway/main.go` | 仅在 B3 时改 | B |
| `gateway/internal/api/license_status.go` | 全部 | B |
| `sql/migrations/startup/376_*.sql` | 全部 | E |
| `tests/e2e/fast-deploy/` | 全部 | QA |
| `docs/分发与激活/{05,06,09,11,12,13}*.md` | 全部 | G |

> 冲突解决：同文件被多人改时由 Owner 决定；若 Owner 也是多人，由 PM 仲裁。

---

## 六、风险登记

| 风险 | 等级 | 缓解 | 责任人 |
|------|------|------|--------|
| `cmd/license-authority` 与 `cmd/gateway` 共享同一 PostgreSQL 时的连接池竞争 | 中 | 各自用独立 pool size；监控连接数 | A |
| 客户端 Ed25519 keypair 丢失（重装系统） | 中 | `activate --reissue` 重新签发 instance_token；旧 token 立即失效（CRL 类比） | B+C |
| 升级 apply 期间网关不可用 | 中 | 第一阶段接受 5-30 s 中断；升级窗口显示在 status；后续阶段（蓝绿）再零中断 | C |
| `license_devices` 与 `gateway_instances` 在 register 事务中部分成功 | 低 | 同事务 + 失败回滚；新增 `instance_events` 审计 | A |
| 主控端被攻击导致伪造 instance_token | 高 | server private key 离线（HSM/Vault 后续）；24h TTL + 签名防重放 | A |
| e2e 网络抖动导致心跳超时误判 | 低 | 测试用 docker network；调高 offline 阈值到 5 min | QA |

---

## 七、完成判定（Definition of Done）

W5 末尾必须全部满足：

1. `make e2e-fast-deploy` 4 个场景全部 PASS
2. `license-authority` systemd unit 在 `llm.kxpms.cn:8443` 跑通 curl 自检
3. 客户机 5 分钟内可完成：`activate trial → status 看到 license → heartbeat 看到 instance online → upgrade check 看到 v1.14.0 → upgrade apply 5s 内完成 → 主控端 release_status=success`
4. 关闭客户端 2 分钟后，主控端 `MonitorInstances` 显示 offline
5. 撤销 license 后客户端下次心跳被拒，UI/CLI 提示续费
6. 断网 24 h 内客户端仍可用，过期自动降级到 community
7. 文档 `docs/分发与激活/{05,06,09,11,12,13}.md` 与实现同步

---

## 八、后续阶段（不在本计划）

- 第二阶段：K3s/K8s Helm Chart + Operator + DB 独立部署
- 蓝绿守护进程（独立 upgrader 进程）
- 客户端 UI（设置页 / 升级面板）
- 计费引擎 + telemetry 采集
- `13-双版本构建与分发策略.md` 的剩余主张（build tags、双二进制）

> 这些都复用本计划定义的 `/api/v1/*` 协议与 `gateway_instances` / `license_devices` 表结构。
---

## 附录：审计后任务补充

### 新增任务（基于 v2 修订）

#### A 组补充（主控端）

| ID | 任务 | 状态 | 依赖 | 文件 |
|----|------|------|------|------|
| A11 | Redis 集成：nonce 缓存（5 min TTL，防重放） | [新] | A8 | `cmd/license-authority/middleware/redis_nonce.go` |
| A12 | EdDSA JWT 签名（golang-jwt/jwt/v5 + Ed25519） | [新] | A7 | `cmd/license-authority/jwt_eddsa.go` |
| A13 | refresh_token 签发与校验（`POST /api/v1/instances/refresh`） | [新] | A12 | `cmd/license-authority/refresh_handler.go` |

#### B 组补充（客户端验证）

| ID | 任务 | 状态 | 依赖 | 文件 |
|----|------|------|------|------|
| B7 | refresh_token 自动续期（cron 每 6 天，存 `~/.kx-gateway/refresh.token`） | [新] | B3 | `licensing/token_refresh.go` |
| B8 | M1 离线模式本地 cron（每 6h 校验 license.dat，禁用心跳） | [新] | B1 | `licensing/offline_cron.go` |

#### C 组补充（安装器）

| ID | 任务 | 状态 | 依赖 | 文件 |
|----|------|------|------|------|
| C8 | M1 离线升级包处理（tar.gz 解压 + docker load + SQL + 回退） | [新] | C3 | `installer/internal/upgrader/offline_apply.go` |

#### D 组修订（实例注册）

| ID | 任务修订 | 变更 |
|----|---------|------|
| D1 | register 写 license_devices 时支持 `instance_type` / `deployment_id` | 新增字段校验 |

#### E 组补充（数据库）

| ID | 任务 | 状态 | 依赖 | 文件 |
|----|------|------|------|------|
| E3 | 377_instance_heartbeats_partition.sql（按月分区 + cron 管理） | [新] | E1 | `sql/migrations/startup/377_instance_heartbeats_partition.sql` |
| E4 | 377_instance_heartbeats_partition.down.sql（撤销分区） | [新] | E3 | `sql/migrations/startup/377_instance_heartbeats_partition.down.sql` |

#### F 组补充（e2e）

| ID | 任务 | 状态 | 依赖 | 文件 |
|----|------|------|------|------|
| F7 | M1 离线场景（无主控端，U 盘激活 + 升级） | [新] | C8, B8 | `tests/e2e/fast-deploy/scenarios/05-m1-offline.sh` |
| F8 | M2 心跳中断 → 120s 自动 offline | [新] | D2 | `tests/e2e/fast-deploy/scenarios/06-heartbeat-timeout.sh` |
| F9 | refresh_token 过期 → 自动重新 register | [新] | B7 | `tests/e2e/fast-deploy/scenarios/07-token-refresh.sh` |

### 修订后的 W1-W5 里程碑

| W | 里程碑 | 新增验收 |
|---|--------|---------|
| W1 | 主控端骨架 + DB 迁移 | + Redis nonce 缓存 + EdDSA JWT |
| W2 | 客户端激活 + 启动校验 | + refresh_token 自动续期 |
| W3 | 心跳 + M1 离线激活 | + M1 本地 cron 校验（禁用心跳） |
| W4 | 升级 check/apply/rollback | + M1 离线升级包（tar.gz） |
| W5 | e2e + 文档 | + F7 M1 离线场景 + F8 心跳超时 + F9 token 续期 |

### 修订后的文件 Owner

| 包 | 新增文件 | Owner |
|----|---------|-------|
| `cmd/license-authority/middleware/` | redis_nonce.go | A |
| `cmd/license-authority/` | jwt_eddsa.go, refresh_handler.go | A |
| `licensing/` | token_refresh.go, offline_cron.go | B |
| `installer/internal/upgrader/` | offline_apply.go | C |
| `sql/migrations/startup/` | 377_*.sql | E |
| `tests/e2e/fast-deploy/scenarios/` | 05-m1-offline.sh, 06-heartbeat-timeout.sh, 07-token-refresh.sh | QA |

### v2 风险更新

| 风险 | 等级 | v1 缺失 | v2 缓解 |
|------|------|---------|---------|
| M1 license.dat 1 年到期无法续期 | 高 | 未提及 | 管理员提前 30 天重签 + 自动告警 |
| refresh_token 90 天丢失需重新注册 | 中 | 未提及 | 提示用户备份 + 支持 re-register |
| M3 sidecar 资源开销（10MB/pod） | 中 | 未提及 | 可接受；后续可用 daemonset |
| Redis 单点故障导致 nonce 缓存失效 | 中 | 未提及 | Redis Sentinel / 回退到内存 |
| 377 分区表 cron 脚本失败导致 OOM | 低 | 未提及 | 监控 + 手动清理脚本 |

### Definition of Done 补充

W5 末尾新增验收：

8. M1 离线场景：无主控端连接，U 盘激活 + 升级全流程通过
9. M2 心跳中断 120s 后主控端显示 offline
10. refresh_token 第 6 天自动续期，90 天过期后重新 register
11. 377 分区表已创建，过去 3 个月数据可查，91 天前数据已自动清理
12. Redis nonce 缓存命中率 > 99%（`INFO stats`）
