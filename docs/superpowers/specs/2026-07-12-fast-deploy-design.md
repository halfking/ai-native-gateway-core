# 2026-07-12 用户快速部署方案 — 设计

> 目标：把 `licensing/`、`autoupdate/`、`center/`、安装器和现有 `docs/分发与激活/` 收拢为一份可逐步实施的方案，覆盖**单机 Docker 快速部署 + 主控端 `llm.kxpms.cn` 在线/离线激活 + 实例注册/心跳 + 在线升级/回退**的第一阶段闭环；K3s/K8s、独立数据库和蓝绿切流作为第二阶段接入同一协议。

---

## 一、范围与边界

### 1.1 第一阶段（MVP，本设计落地）

| 模块 | 交付 | 已实现 vs 待补 |
|------|------|----------------|
| 安装器 | `llm-gw-installer` 命令族（doctor/install/uninstall/version） | 已实现 `installer/cmd/llm-gw-installer`；需补 `activate/upgrade/rollback/heartbeat` |
| License 协议 | 在线 + 离线两种激活，写入 license.dat | 已实现 `licensing/` 算法；需定 License Authority URL + 公钥注入方式 |
| 主控端服务 | `llm.kxpms.cn` 暴露 `/api/v1/license/*`、`/api/v1/instances/*`、`/api/v1/updates/*` | **不存在**；用 `cmd/license-authority` 新增进程，复用现有 `licensing/` + `center/` + `autoupdate/` 包 |
| 实例注册 | 客户实例启动后调用 `/api/v1/instances/register` | 待补客户端注册逻辑 + 主控端 receiver |
| 心跳 | 60s 一次，写 `instance_heartbeats` 表 | `center/` 心跳协议已实现；需补客户端 sender 与 30s/2min 离线判定 |
| 升级检查 | 客户实例每 6h 拉 `/api/v1/updates/latest` | 待补客户端检查循环；复用现有 `autoupdate/` DB 与 admin API |
| 升级执行 | 备份 → 替换 → 标记 instance upgraded_at；失败自动回退 | 已实现 `autoupdate/installer.go` 与 `rollback.go`；缺状态机 |
| 回退入口 | 客户端 `upgrade rollback --to <version>` | 需补 CLI 子命令 + License/中心联动 |

### 1.2 第二阶段（不属本设计，先留接口）

- K3s/K8s 模式（Helm Chart + Operator）
- DB 独立部署模式
- 蓝绿切流守护进程（独立 upgrader 进程）
- 客户端 UI（设置页 / 升级面板）
- 计费引擎、telemetry 采集

> 第二阶段必须**复用**第一阶段的协议与字段，不引入新的 API 形状。

### 1.3 明确不做

- 不改造客户机器的网关核心代码路径（`cmd/gateway` 主进程）。
- 不引入新的加密算法（继续用现有 RSA-PKCS1v15 / SHA-256）。
- 不实现客户端 UI（先用 CLI + 安装报告 + 主控端后台）。
- 不替换 `autoupdate` 的现有备份/回退实现，只补充状态机和编排。

---

## 二、目标部署形态（第一阶段）

```
┌─────────────────────── 客户机器 ───────────────────────┐
│  ~/llm-gateway/                                         │
│   ├── bin/llm-gw-installer   (CLI: doctor/install/      │
│   │                          activate/upgrade/rollback/   │
│   │                          heartbeat)                   │
│   ├── compose.yml / .env                                │
│   ├── kx-llm-gateway-go / kx-citus / kx-redis (docker) │
│   ├── license.dat            (RSA-signed JSON)            │
│   ├── VERSION                                           │
│   └── reports/upgrade-log.jsonl                         │
└──────────────────────┬─────────────────────────────────┘
                       │ HTTPS (TLS 1.3, 公钥验签)
┌──────────────────────▼─────────────────────────────────┐
│  主控端 llm.kxpms.cn — cmd/license-authority            │
│   ├─ /api/v1/license/*   (复用 licensing/ 包)            │
│   ├─ /api/v1/instances/* (复用 center/ 包)              │
│   ├─ /api/v1/updates/*   (复用 autoupdate/ 包)          │
│   └─ 复用同一 PostgreSQL (licenses/license_devices/     │
│      gateway_instances/instance_heartbeats/releases/    │
│      release_status)                                    │
└─────────────────────────────────────────────────────────┘
```

---

## 三、关键设计决策

### 3.1 主控端如何部署？

| 选项 | 优点 | 缺点 | 结论 |
|------|------|------|------|
| A. 把 `licensing/` + `center/` + `autoupdate/` 三个 admin handler 装进同一个 `cmd/gateway` 进程 | 0 新增进程 | 与"双版本策略"（13 号文档）冲突：客户端二进制也会含 admin API | 不选 |
| **B. 新增 `cmd/license-authority` 进程，只暴露 `/api/v1/*`** | 复用现有包；客户端二进制天然不含此路径；部署简单（一个 Go 进程 + 一个 systemd unit） | 多一个二进制 | **选 B** |
| C. 嵌入 `llm.kxpms.cn` 前端（Next.js 等） | 统一发布 | 与当前运维平台 API 设计冲突（运维用 Echo） | 不选 |

**结论**：`cmd/license-authority` 是独立 Go 二进制，与 `cmd/gateway` 复用同一模块（`licensing/`、`center/`、`autoupdate/`），但只挂载 `/api/v1/*` 路由。它监听 `:8443`，由 Nginx/TLS 终结对外（`llm.kxpms.cn`），对内调用 admin API 时使用 `cmd/gateway` 的 `/api/admin/*`。

### 3.2 公网安全边界

主控端对外只暴露：

```
POST /api/v1/license/trial         # 试用激活
POST /api/v1/license/activate      # License Key 激活
POST /api/v1/license/refresh       # 24h 签名刷新
GET  /api/v1/license/crl           # CRL 查询
POST /api/v1/license/offline/issue # 主控端管理员审批后产出 license.dat
POST /api/v1/instances/register    # 客户实例注册
POST /api/v1/instances/heartbeat   # 心跳
POST /api/v1/instances/status      # 状态上报
GET  /api/v1/updates/latest        # 当前客户实例可升级的最新 release
GET  /api/v1/updates/manifest      # 升级包清单（含镜像 / tarball URL）
POST /api/v1/updates/report        # 客户实例回写升级结果
POST /api/v1/updates/rollback      # 客户实例回写回退结果
```

所有 `/api/v1/license/activate*` 与 `/api/v1/instances/*` 必须用客户 `instance_id` + `instance_token`（见 3.4）做请求签名；服务端拒绝未签名或签名错的请求。

### 3.3 License 协议对齐当前代码

| 字段 | 来源 | 是否已实现 |
|------|------|-----------|
| LicenseKey 格式 | 代码 `licensing/admin_api.go:generateLicenseKey()` → `LIC-<32hex>` | ✅ 已实现 |
| 签名 | `crypto.SignLicense()` RSA-PKCS1v15 + SHA-256 | ✅ 已实现 |
| 离线请求 | `licensing/offline.go` + `center_api.go:CreateOfflineRequest` | ✅ 已实现 |
| 主控端 license 写入路径 | 由 `cmd/license-authority` 暴露 `/api/v1/license/offline/issue`，写库 + 调 `crypto.SignLicense` | ❌ 待补 |
| 客户端 license 验签 | 需新增 `licensing/verify_local.go`：`os.ReadFile("license.dat")` + RSA 公钥验签 + 过期/撤销/指纹 | ❌ 待补 |

**客户端主二进制（`cmd/gateway`）在启动时增加 License 校验钩子**：
- 读取 `/var/lib/kx-gateway/license.dat`（由安装器或激活子命令写入）
- 验签 + 检查 `RevokedAt` + `ExpiresAt`
- 失败：返回 503 + UI 重定向到 `/setup`（第二阶段 UI；MVP 阶段 CLI 提示并以受限模式启动——仅 `/api/healthz` 与 `/api/system/license/activate` 可用）

### 3.4 实例注册 + 心跳协议

#### InstanceToken

主控端在 `register` 成功时签发 `instance_token`（JWT, 24h TTL），续期通过 `heartbeat` 完成。

```json
// POST /api/v1/instances/register
// 请求
{
  "instance_id": "uuid",
  "hostname": "prod-app-01",
  "ip_address": "10.0.0.5",
  "region": "cn-north-1",
  "version": "v1.13.0-770",
  "build_seq": 770,
  "license_key": "LIC-...",
  "hardware_hash": "...",
  "public_key": "base64"     // 客户端持有的 Ed25519 公钥，用于后续命令签名校验
}
```

服务端：① 校验 license（license_key + hardware_hash 与 `license_devices` 对得上）② 写 `gateway_instances` + 返回 `instance_token` + `server_public_key`（主控端 Ed25519 公钥）。

后续所有 `/api/v1/instances/*` 请求必须：

```
Authorization: Bearer <instance_token>
X-Instance-ID: <uuid>
X-Signature: ed25519(<body>, <instance_private_key>)  // 防 token 泄漏后的重放
X-Timestamp: <unix>      // ±300s
```

> **实施注意**：当前 `center/store_pgx.go` 已使用 `gateway_instances` 表，仅缺 `instance_token`、`public_key`、`current_version`、`build_seq` 字段。

#### 心跳协议

客户端每 60s 上报一次 `POST /api/v1/instances/heartbeat`：

```json
{
  "instance_id": "uuid",
  "version": "v1.13.0-770",
  "uptime_secs": 86400,
  "go_version": "go1.22",
  "num_goroutine": 120,
  "alloc_mb": 512.0,
  "total_alloc_mb": 1024.0,
  "sys_mb": 2048.0,
  "cpu_cores": 8,
  "current_concurrency": 32,
  "last_5min_tps": 12.5,
  "last_5min_p50_ms": 380,
  "last_5min_p99_ms": 1100,
  "last_5min_success_pct": 99.5,
  "license_key_hash": "abc123..."      // SHA256(license_key)[:16]
}
```

服务端：写 `instance_heartbeats` + 更新 `gateway_instances.last_heartbeat` + `MonitorInstances()`（`center/server.go:86`）自动判定 online/degraded/offline。

> **状态判定**（沿用 `center/server.go:60-78`）：
> - `now - last_heartbeat ≤ 30s` → online
> - `30s < now - last_heartbeat ≤ 2min` → degraded
> - `> 2min` → offline

### 3.5 升级检查 + 执行

#### 检查

```
GET /api/v1/updates/latest
Headers: X-Instance-ID, Authorization: Bearer <instance_token>

Response:
{
  "version": "v1.14.0",
  "build_seq": 800,
  "channel": "stable",
  "mandatory": false,
  "manifest_url": "https://llm.kxpms.cn/api/v1/updates/manifest?v=1.14.0",
  "sha256": "abc...",
  "size_mb": 128,
  "release_notes_url": "https://llm.kxpms.cn/release-notes/v1.14.0"
}
```

#### 执行（沿用 `autoupdate/installer.go` + `rollback.go`，但状态机与现状对齐）

客户端状态枚举与 `autoupdate/types.go:StatusPending/.../StatusRollback` **完全复用**，状态机新增的 `verifying/backing_up/reporting` 通过 `upgrade_logs.status` 字段在现有枚举内表达，避免新建枚举：

```
upgrade start --to v1.14.0
   ├─ status=pending       → 主控端写 upgrade_logs(status=pending)
   ├─ status=downloading   → GET /api/v1/updates/manifest → 下载新二进制到 bin/llm-gateway-go.new
   ├─ status=ready_to_restart
   │     ├─ SHA256 校验 + 启动测试（--version），失败 → status=failed → 自动 rollback
   │     └─ 备份当前二进制到 backups/manual/<old_version>_<ts>
   ├─ status=upgrading     → os.Rename 原子替换 + 写 VERSION
   ├─ status=success       → POST /api/v1/updates/report {status: success}
   └─ done

upgrade rollback --to v1.13.0
   ├─ status=pending       → 主控端写 upgrade_logs(status=pending, retry_count++)
   ├─ status=upgrading     → 在 backups/manual/ 找匹配版本 → 原子还原 + 写 VERSION
   ├─ status=rolled_back   → POST /api/v1/updates/rollback {status: rolled_back}
   └─ done
```

> **表选择**：升级执行状态写 `instance_release_status`，长期审计/历史写 `upgrade_logs`。
> 两个表都已存在（`autoupdate/store_pgx.go:170, 224, 240`），无须新建表；状态枚举与字段命名严格沿用现有。

### 3.6 离线授权（air-gapped）

完全断网环境：

1. 客户机生成 `activation.req`（含 fingerprint + license_key + instance_id + request_id + ts）。
   > 当前 `licensing/center_api.go:CreateOfflineRequest` 已实现请求生成；缺 client-side CLI 入口。
2. 通过 U 盘拷贝到联网电脑。
3. 联网电脑打开 `https://llm.kxpms.cn/offline` 上传 `activation.req` → 主控端 `/api/v1/license/offline/approve`（管理员已审批）→ 返回 `activation.resp`（即 license.dat base64）。
4. U 盘拷回，CLI `activate --offline-import <file>` 写入 license.dat。

### 3.7 安装器扩展（最小改动）

保持 `installer/cmd/llm-gw-installer` 现有 CLI 形态，新增子命令：

```
llm-gw-installer activate          # 调 4 个入口之一（trial/key/offline）
llm-gw-installer heartbeat         # 一次性或 daemon
llm-gw-installer upgrade check     # 查询 /api/v1/updates/latest
llm-gw-installer upgrade apply     # 下载 + 备份 + 替换 + 报告
llm-gw-installer upgrade rollback  # 回退 + 报告
llm-gw-installer status            # 显示 license / 当前版本 / 升级窗口
```

> `doctor/install/uninstall/version` 保持不动；新增子命令全部走 Cobra。

### 3.8 安装报告

复用 `installer/internal/report/writer.go`，在 install 成功后：

- 把 `instance_id`、`hostname`、`ip`、`license_key_hash`、`hardware_hash` 加密写入 `install-report.md`（`instance_id` 用于后续注册主控端）。
- 新增 `reports/upgrade-log.jsonl` 记录升级历史。

---

## 四、数据模型（与现有 schema 对齐）

| 表 | 用途 | 字段补全 |
|----|------|----------|
| `licenses` | 已存在 | 无需新增 |
| `license_devices` | 已存在（374） | 无需新增 |
| `offline_activation_requests` | 已存在（374+375） | 无需新增 |
| `gateway_instances` | 已存在（`center/store_pgx.go:23-40`） | 新增：`instance_token`、`public_key`、`current_version`、`build_seq`、`license_key_hash`、`hardware_hash` |
| `instance_heartbeats` | 已存在（`center/store_pgx.go`） | 无需新增；客户端心跳协议对齐 `HeartbeatPayload` |
| `releases` | 已存在（`autoupdate/types.go`） | 无需新增 |
| `release_status` | 已存在（`autoupdate/types.go:50`） | 客户端升级完成后回写 |
| `commands` | 已存在（`center/admin_api.go:IssueCommand`） | 主控端下发（第二阶段使用） |
| `instance_commands` | 新增 | 记录命令接收与执行结果（第二阶段） |

迁移文件命名沿用 `sql/migrations/startup/376_*.sql`：

```sql
-- 376_gateway_instances_auth.sql
ALTER TABLE gateway_instances
    ADD COLUMN IF NOT EXISTS instance_token    TEXT,
    ADD COLUMN IF NOT EXISTS public_key        TEXT,
    ADD COLUMN IF NOT EXISTS current_version   TEXT,
    ADD COLUMN IF NOT EXISTS build_seq         INT,
    ADD COLUMN IF NOT EXISTS license_key_hash  TEXT,
    ADD COLUMN IF NOT EXISTS hardware_hash     TEXT,
    ADD COLUMN IF NOT EXISTS last_heartbeat_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_status       TEXT;
CREATE INDEX IF NOT EXISTS idx_gi_license ON gateway_instances (license_key_hash);
```

> **实例↔license 绑定**：`register` 成功时同步写 `license_devices(instance_id=..., hardware_hash=...)`，
> 防止 instance_token 与 license 设备关系脱钩（`licensing/device_manager.go` 已有 `ActivateDevice`/`DeactivateDevice`）。

---

## 五、API 契约（第一阶段必交付）

### 5.1 主控端 `/api/v1/license/*`

| Method | Path | 入参 | 出参 | 权限 |
|--------|------|------|------|------|
| POST | `/api/v1/license/trial` | email, fingerprint, instance_id | signed_license | 公开（rate-limited） |
| POST | `/api/v1/license/activate` | license_key, fingerprint, instance_id | signed_license | 公开 |
| POST | `/api/v1/license/refresh` | license_key, hardware_hash | new signed_license | instance_token |
| GET | `/api/v1/license/crl` | since | revoked list | 公开 |
| POST | `/api/v1/license/offline/issue` | request_id | signed_license | admin_only |

### 5.2 主控端 `/api/v1/instances/*`

| Method | Path | 入参 | 出参 | 权限 |
|--------|------|------|------|------|
| POST | `/api/v1/instances/register` | 见 3.4 | instance_token, server_public_key | 公开 |
| POST | `/api/v1/instances/heartbeat` | 见 3.4 | ok | instance_token |
| POST | `/api/v1/instances/status` | status, metrics | ok | instance_token |
| GET | `/api/v1/instances/commands/pending` | - | [command] | instance_token |
| POST | `/api/v1/instances/commands/:id/result` | success, output, error | ok | instance_token |

### 5.3 主控端 `/api/v1/updates/*`

| Method | Path | 入参 | 出参 | 权限 |
|--------|------|------|------|------|
| GET | `/api/v1/updates/latest` | channel, current_version | 见 3.5 | instance_token |
| GET | `/api/v1/updates/manifest` | version | tarball URL, sha256 | 公开 |
| POST | `/api/v1/updates/report` | instance_id, version, status, error, duration_ms | ok | instance_token |
| POST | `/api/v1/updates/rollback` | instance_id, to_version, reason | ok | instance_token |

### 5.4 客户端 License 验证（内部）

```
load license.dat → ParseSignedJSON → RSA verify → check expired → check revoked → fingerprint match
```

### 5.5 客户端 ↔ 主控端错误码

| HTTP | code | 含义 | 客户端处理 |
|------|------|------|----------|
| 400 | invalid_request | 字段缺失 | 终止，重看 CLI 帮助 |
| 401 | invalid_signature | 签名错 | 重新生成密钥对，retry 1 次 |
| 403 | license_revoked | 已撤销 | UI/CLI 提示续费 |
| 409 | device_limit_exceeded | 超过 max_devices | 提示先停用旧设备 |
| 410 | license_expired | 已过期 | 进入 trial/community 降级模式 |
| 429 | rate_limited | 调用过频 | 指数退避 |
| 500 | internal_error | 服务端故障 | retry 3 次（间隔 5/30/120s） |
| 503 | main_control_offline | 主控端不可达 | 离线模式：只允许使用最近 24h 内的 license |

---

## 六、安全与权限

1. **公钥注入**：客户端启动时从 `/var/lib/kx-gateway/server.pub` 读取主控端 Ed25519 公钥（首次安装时由 `activate` 写入）；本地持有客户端 Ed25519 密钥对（首启生成）。
2. **请求签名**：所有 `/api/v1/instances/*`、`/api/v1/updates/*` 必须含 `X-Signature`。
3. **重放防护**：服务端拒绝 `|now - X-Timestamp| > 300s` 的请求；缓存 nonce 5 分钟。
4. **离线容忍**：当主控端 503 时，客户端使用最近 24h 内有效的 license.dat + 最近 1 次缓存的 release manifest；超过 24h 进入受限模式。
5. **CRL 检查**：客户端每 6h 拉一次 `/api/v1/license/crl`；失败则使用本地缓存（最长 30 天）。
6. **审计**：主控端写 `instance_events` 表，记录 register/activate/heartbeat/upgrade/rollback 事件。

---

## 七、失败处理（runbook）

| 场景 | 检测 | 客户端动作 | 主控端动作 |
|------|------|----------|----------|
| 主控端离线 | HTTP 503 / TCP timeout | 使用本地 license.dat（24h 宽限）+ 缓存 manifest | n/a |
| License 验签失败 | RSA 验签失败 | 阻止启动，进入受限模式 | 写 `instance_events` |
| License 过期 | ExpiresAt < now | 进入 community 降级（1 租户 / 仅基础 API） | 写 `instance_events` |
| License 撤销 | CRL 命中 | 阻止启动，要求重新激活 | n/a |
| 心跳 30s 没回 | 客户端 retry 3 次 | 标记 degraded，本地缓存更长宽限 | 主控端 MonitorInstances() 自动 degraded |
| 心跳 2min 没回 | 客户端持续重试 | 同上 | 主控端 offline + 通知 admin |
| 升级下载失败 | HTTP/磁盘错 | 重试 3 次；失败上报 `report: failed` | 记录失败实例 |
| 升级替换失败 | rename 错 | 自动回退 + 上报 `report: rolled_back` | 记录失败实例 |
| 升级后健康检查失败 | 5s 内 /healthz 非 200 | 自动回退 + 上报 `report: rolled_back` | 同上 |
| 指纹不匹配 | MatchScore < 0.6 | 阻止启动 | 标记可疑设备 |

---

## 八、增量文件清单（仅本设计）

### 8.1 新建

```
cmd/license-authority/
├── main.go                         # 注册 /api/v1/* 路由
└── README.md

licensing/
├── verify_local.go                 # 客户端 license.dat 验签
└── enrollment.go                   # instance_token 签发 + 校验

center/
├── enrollment_handler.go           # /api/v1/instances/register
├── heartbeat_handler.go            # /api/v1/instances/heartbeat
└── client_signer.go                # Ed25519 签名/验签辅助

autoupdate/
├── manifest_serve.go               # GET /api/v1/updates/manifest
└── result_handler.go               # POST /api/v1/updates/report, /rollback

installer/cmd/llm-gw-installer/
├── activate.go                     # 子命令
├── heartbeat.go                    # 子命令
└── upgrade.go                      # 子命令（check/apply/rollback）

installer/internal/enrollment/
├── client.go                       # 持有 client keypair + 调 /api/v1/instances/*
└── signer.go                       # Ed25519 签发

installer/internal/upgrader/
├── state.go                        # 状态机
├── check.go
├── apply.go
└── rollback.go

sql/migrations/startup/
└── 376_gateway_instances_auth.sql

deploy/systemd/
├── kx-gateway.service              # systemd unit（已有则增强）
└── kx-gateway-upgrader.service     # 第一阶段并入 kx-gateway.service 即可
```

### 8.2 修改

- `licensing/admin_api.go` → 新增 `PATCH /licenses/:id/assign-public-key`（写 license-level public_key）
- `installer/cmd/llm-gw-installer/main.go` → 注册新子命令
- `installer/internal/report/writer.go` → 在 install-report 中加入 instance_id
- `docs/分发与激活/05-注册激活流程.md`、`09-分发与下载.md`、`11-实施路线图.md`、`12-执行计划与并发任务.md` → 把"待实现"标注为已实现（本设计落地后）

---

## 九、与现有文档的关系

| 文档 | 与本设计的关系 | 处理 |
|------|----------------|------|
| `01-产品概述与商业模式.md` | 业务模型稳定，不变 | 不动 |
| `02-用户旅程.md` | 拆分阶段后需增加 MVP 阶段描述 | 增量补充 "MVP 第一阶段" 章节 |
| `03-部署架构.md` | M1/M2 单机模式可行；M3/K8s 推第二阶段 | 调整模式标记 |
| `04-License算法与验证.md` | 算法稳定；补 `verify_local.go` 描述 | 增量补 1 节 |
| `05-注册激活流程.md` | 拆 MVP 后流程简化 | 重写 "客户端流程" 章节 |
| `06-自动升级与蓝绿部署.md` | MVP 阶段不做蓝绿；先做"备份-替换-回退" | 标注 MVP 范围 |
| `07-运行状态采集.md` | 与 heartbeat payload 重叠 | 合并字段表 |
| `08-防盗版与安全.md` | 补签名/重放/Cache 部分 | 增量补 1 节 |
| `09-分发与下载.md` | 补 release tarball 内新增的 `enroll/` 工具 | 增量补 |
| `10-多租户与计费架构.md` | 仍属第二阶段 | 不动 |
| `11-实施路线图.md` | 改为基于本设计的 MVP 里程碑 | 重写 |
| `12-执行计划与并发任务.md` | 任务全部更新 | 重写 |
| `13-双版本构建与分发策略.md` | 14 号方案不再适用（客户版不再嵌 admin） | 标注"已并入 14-用户快速部署方案" |

---

## 十、里程碑与第一阶段交付

| W | 里程碑 | 验收 |
|---|--------|------|
| W1 | `cmd/license-authority` 跑通，4 个 `/api/v1/license/*` + `/api/v1/instances/register` 可用 | `curl activate` 拿到 signed_license |
| W2 | 客户端 `activate` 4 个入口可写 license.dat；`verify_local.go` 启动校验通过 | `llm-gateway` 启动拒绝无 license，签名错拒绝 |
| W3 | 心跳 60s 一次；主控端 MonitorInstances 自动 online/degraded/offline | 关闭客户端，2 分钟内主控端 offline |
| W4 | 升级 check/apply/rollback；结果回写 `release_status` | 升级失败 5s 内自动回退 + 主控端可见 |
| W5 | 离线授权链路；CRL 检查；客户端降级模式 | 断网 24h 内仍可用，过期进入 community |

---

## 十一、风险与回退

| 风险 | 缓解 |
|------|------|
| 主控端被攻击导致伪造 instance_token | 私钥离线存储 + 短期 TTL（24h）+ Ed25519 签名防重放 |
| 客户端 `verify_local.go` bug 导致误拒合法 license | 启动时如 license.dat 校验失败，**回退到试用模式**而非直接 503 |
| 升级替换失败导致网关无法启动 | `installer upgrade apply` 校验新二进制可执行后再 rename；失败自动 restore |
| 心跳风暴（瞬时大量实例） | 服务端 `/api/v1/instances/heartbeat` 用令牌桶限流（默认 60/min/instance） |
| 数据库 schema 漂移 | 所有 ALTER 走 `IF NOT EXISTS` + down.sql 在 376 单独维护 |