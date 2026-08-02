# LLM Gateway 本地/单机部署手册（用户下载版）

> 适用对象：下载 `llm-gateway-go-{VERSION}-{os}-{arch}-offline.tar.gz` 离线包，在
> 自己的 Linux / macOS / Windows 机器上部署 LLM Gateway 全栈的用户。
>
> 本文档与离线包内 `install.sh` / `ONE-CLICK.md` 配套：`ONE-CLICK.md` 讲怎么跑起来，
> 本文档逐项讲清**代码 / 环境变量 / 数据库**三个维度的配置含义与验证方法。

---

## 1. 部署拓扑

离线包一次拉起 **PostgreSQL + Redis + llm-gateway-go** 三个组件：

```
┌────────────────────────────── 目标机 ─────────────────────────────┐
│                                                                   │
│   llm-gateway-go (网关, :8781)                                    │
│     ├─ PostgreSQL :5432  (Citus 17 镜像, 存储业务数据)             │
│     ├─ Redis      :6379  (缓存 / 限流 / 会话)                      │
│     └─ HTTPS/HTTP 客户端请求（OpenAI 兼容 /v1/* 接口）              │
│                                                                   │
│   部署方式二选一：                                                 │
│     A. host 模式：gateway 原生二进制 + 宿主机 PG/Redis             │
│     B. compose 模式：全部容器化（Windows 必选 B）                  │
└────────────────────────────────────────────────────────────────────┘
```

| 组件 | 默认端口 | 说明 |
|------|---------|------|
| llm-gateway-go | `8781` | OpenAI 兼容网关，唯一对外入口 |
| PostgreSQL | `5432` | 业务库 `llm_gateway`，用户 `llm_user` |
| Redis | `6379` | 认证串格式 `redis://:<pass>@127.0.0.1:6379/0` |

---

## 2. 代码（离线包结构）

解压 `llm-gateway-go-{VERSION}-{os}-{arch}-offline.tar.gz` 后得到：

```
llm-gateway-go-{VERSION}-{os}-{arch}-offline/
├── bin/
│   ├── gateway                    # 网关主二进制（Linux/macOS；Windows 用容器镜像）
│   └── llm-gw-installer           # 安装器二进制
├── web/                           # 前端静态资源（部署到 nginx 或由安装器托管）
├── sql/baseline/
│   ├── 00-prereqs.sql             # 前置：角色/扩展/基础 schema 权限
│   ├── 01-schema.sql              # 主 schema（表 / 索引 / 约束）
│   └── 02-seed.sql                # 种子数据（初始 provider / settings）
├── install.sh                     # 一键安装入口（自动选择安装器）
├── llm-gw-installer-{os}-{arch}   # 带平台后缀的安装器（install.sh 直接调用）
├── gateway                        # 网关二进制副本（host 模式直接运行）
├── ONE-CLICK.md                   # 部署速查
├── INSTALL.md                     # 构建信息
└── version.json                   # 版本 / build 元数据
```

**三种运行形态（按需选择）：**

| 形态 | 适用 | 启动方式 |
|------|------|---------|
| A. host 原生 | Linux / macOS | `./install.sh install`（安装器生成 env + 拉起服务） |
| B. compose 容器 | Linux / macOS / Windows | `./install.sh install` 内部调 `docker compose up -d` |
| C. 手动 | 有现成 PG/Redis | 只跑 `./gateway`，env 由自己提供 |

> 代码无需编译：包内是已交叉编译的产物。只有从 Git 源码部署才需要 Go 工具链
> （见 `deploy/one-click/lib/bootstrap.sh` 的 `resolve_installer` fallback）。

---

## 3. 环境变量（逐项）

安装器生成 `.env` 到安装目录（默认 `~/llm-gateway` 或 `$LLM_GATEWAY_HOME`）。
**生产环境请逐项核对，敏感值不写死进仓库。**

### 3.1 必填（服务能起）

| KEY | 示例 | 说明 |
|-----|------|------|
| `PORT` | `8781` | 网关监听端口 |
| `NODE_ENV` | `production` | `local` / `dev` / `staging` / `prod` |
| `DB_HOST` | `127.0.0.1` | PG 主机（compose 模式填服务名 `postgres`） |
| `DB_PORT` | `5432` | PG 端口 |
| `DB_NAME` | `llm_gateway` | 数据库名 |
| `DB_USER` | `llm_user` | PG 用户 |
| `DB_PASSWORD` | 强随机串 | PG 密码（≥16 位） |
| `REDIS_PORT` | `6379` | Redis 端口 |
| `REDIS_PASSWORD` | 强随机串 | Redis 密码 |
| `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY` | 32 字节 key | 敏感字段加密密钥。**丢失 = 无法解密存量 credential**，务必备份 |
| `LLM_GATEWAY_API_KEY` | 随机串 | 网关内部鉴权 key |

### 3.2 授权 / 激活（上线前必配）

| KEY | 说明 |
|-----|------|
| `LLM_GATEWAY_CENTER_URL` | 授权中心地址。未设置默认 `https://llm.kxpms.cn` |
| `OPS_COLLECT_URL` | 节点数据汇聚地址（心跳 / 采集） |
| `OPS_NODE_REGION` | 运维节点标识（如 `customer-1` / `245` / `154`） |
| `OPS_INSTANCE_ID` | 可选；默认持久化到 `~/.local/share/kx-gateway/instance.id` |
| `OPS_COLLECT_LICENSE_KEY` | 运维节点注册用 licensee key（license 场景必填） |

激活：浏览器打开 `https://llm.kxpms.cn/maintain/activate` 输入激活码；无外网环境用
`/maintain/offline-activation`。

### 3.3 可选（按需）

| KEY | 默认 | 说明 |
|-----|------|------|
| `LOG_LEVEL` | `debug` | 生产建议 `info` |
| `LOG_FORMAT` | `json` | 建议保持 json 便于采集 |
| `LLM_GATEWAY_LOG_FILE` | 空(stderr) | 文件日志轮转（100MB×10≈1GB） |
| `LLM_GATEWAY_ATTACHMENT_DIR` | `./data/attachments` | 附件目录，建议绝对路径 + 持久卷 |
| `CASDOOR_ENDPOINT` / `CASDOOR_CLIENT_ID` / `CASDOOR_CLIENT_SECRET` | — | Casdoor OIDC 登录（未配置则用网关自带鉴权） |
| `MAINTAIN_SERVICE_URL` + `LLM_GATEWAY_JWT_SECRET` | — | 插件权益门禁（与 maintain 同 HS256 密钥） |

> ⚠️ `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY` 一旦用于生产就不可更换；更换 = 全量
> credential 需重新解密加密。备份该 key 到安全位置（如 1Password / SOPS）。

---

## 4. 数据库

### 4.1 初始化（由安装器自动执行）

`sql/baseline/` 三个文件按序应用到目标 PG：

```
00-prereqs.sql   → 建角色 llm_user、扩展（pgcrypto 等）、schema 归属
01-schema.sql    → 业务表 + 索引 + 约束（幂等，可重复执行）
02-seed.sql      → 种子数据（初始 provider 路由、settings，幂等）
```

手工执行（已有 PG 的场景）：

```bash
export PGPASSWORD=<DB_PASSWORD>
psql -h <DB_HOST> -p <DB_PORT> -U postgres -d postgres -f sql/baseline/00-prereqs.sql
psql -h <DB_HOST> -p <DB_PORT> -U llm_user   -d llm_gateway -f sql/baseline/01-schema.sql
psql -h <DB_HOST> -p <DB_PORT> -U llm_user   -d llm_gateway -f sql/baseline/02-seed.sql
```

> `01-schema.sql` / `02-seed.sql` 幂等；`00-prereqs.sql` 用于初始化 PG 实例，重复执行
> 会因角色已存在而报错，属预期（可加 `DO $$ IF NOT EXISTS` 或忽略）。

### 4.2 应用后验证

```bash
psql -h <DB_HOST> -U llm_user -d llm_gateway -c "\dt"            # 应列出业务表
psql -h <DB_HOST> -U llm_user -d llm_gateway -c \
  "SELECT count(*) FROM provider_credentials;"                   # seed 后应有初始行
```

常见表（以实际 schema 为准）：`provider_credentials`、`request_logs`、`settings`、
`gateway_instances` 等。

### 4.3 备份与迁移

- 每日 `pg_dump -Fc` 全量备份；保留 ≥ 7 天。
- 版本升级只替换 `bin/gateway` + `web/`，**不动数据库**（schema 由升级包内的
  `V{N}__*.sql` migration 增量应用）。
- 变更结构前先评估数据量：小表直接改，大表分批 + `CREATE INDEX CONCURRENTLY`。

---

## 5. 多 OS 安装

| 系统 | 架构 | 方式 | 命令 |
|------|------|------|------|
| Linux | amd64 / arm64 / loong64 | host 或 compose | `./install.sh install` |
| macOS | amd64 / arm64 | host 或 compose（需 Docker Desktop/OrbStack） | `./install.sh install` |
| Windows | amd64 | compose（必选） | `install.ps1`（管理员 PowerShell） |

Linux / macOS：

```bash
curl -fLO https://download.kxpms.cn/llm-gateway-go/latest/llm-gateway-go-<VERSION>-linux-amd64-offline.tar.gz
tar xzf llm-gateway-go-<VERSION>-linux-amd64-offline.tar.gz
cd llm-gateway-go-<VERSION>-linux-amd64-offline
./install.sh install
```

Windows（管理员 PowerShell）：

```powershell
Expand-Archive llm-gateway-go-<VERSION>-windows-amd64-offline.zip
cd llm-gateway-go-<VERSION>-windows-amd64-offline
powershell -ExecutionPolicy Bypass -File install.ps1
```

> 自动化安装脚本：`deploy/one-click/install.sh`（Linux/macOS）与
> `deploy/one-click/install.ps1`（Windows）为权威入口；离线包内置等价脚本。

---

## 6. 部署后验证（L1 → L4）

```bash
# L1 存活
curl -fsS http://127.0.0.1:8781/healthz          # 期望 JSON 含 "ok"

# L2 依赖连通（安装器已跑，手动确认）
psql -h 127.0.0.1 -U llm_user -d llm_gateway -c "SELECT 1;"
redis-cli -a "$REDIS_PASSWORD" ping                # 期望 PONG

# L3 功能链路：发一次真实推理请求
curl -fsS http://127.0.0.1:8781/v1/models -H "Authorization: Bearer $LLM_GATEWAY_API_KEY"

# L4 业务真实：授权中心心跳可见
# 登录 https://llm.kxpms.cn/maintain 查看实例在线；本机 instance_id：
cat ~/.local/share/kx-gateway/instance.id 2>/dev/null || cat ~/llm-gateway/instance_id
```

---

## 7. 常见问题

| 症状 | 原因 | 处理 |
|------|------|------|
| `/healthz` 200 但 `/v1/models` 403 | `LLM_GATEWAY_API_KEY` 未设/不符 | 核对 3.1 表格 |
| 启动即 `pq: password authentication failed` | `DB_PASSWORD` 与 PG 实际不符 | 重跑 baseline 或更新 `.env` |
| 升级后请求全报 `Invalid or expired API key` | `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY` 变更 | **不可改**；恢复旧 key 并重启 |
| `request_logs` 增长过快 | 生产日志量大 | 开日志轮转 + 归档（`archive-request-logs.sh`） |
| Windows 无 `gateway` 二进制 | 设计如此：Windows 走 compose 容器镜像 | 确认 Docker Desktop 运行中 |

---

## 8. 相关资源

- 一键部署速查：`ONE-CLICK.md`（同目录）
- 安装脚本：`deploy/one-click/install.sh` / `install.ps1` / `lib/bootstrap.sh`
- 中心维护方：`ai-native-maintain`（下载站 / 授权中心 / 升级服务）
