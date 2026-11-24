# llm-gateway-go 一键安装器（Installer）

> Go 单二进制跨平台安装器，支持 Windows / Linux / macOS / 国产 OS / 国产 CPU。
> 内置 4 层镜像源兜底：离线包 → 内部 registry → 国内 mirror → 官方源。

## 目录结构

```
installer/
├── cmd/llm-gw-installer/
│   ├── main.go              # Cobra CLI 入口
│   └── embeddata/           # go:embed 资源（compose.yml / SQL / 模板）
├── internal/
│   ├── envdetect/           # OS/arch/docker/网络探测
│   ├── imgsrc/              # 4 层镜像源 fallback
│   ├── prompt/              # 13 步交互向导
│   ├── secrets/             # 随机密码 + .env 写入
│   ├── dockerutil/          # compose 封装 + 健康检查
│   ├── dbinit/              # SQL schema 应用
│   └── report/              # 部署报告生成
├── templates/               # 模板源文件（同步到 embeddata/）
├── sql/                     # 复用 deploy/sql/
├── go.mod
└── README.md
```

## 快速开发

```bash
# 编译当前平台
GOPROXY=https://goproxy.cn,direct go build -o /tmp/llm-gw-installer ./cmd/llm-gw-installer/

# 测试
/tmp/llm-gw-installer doctor
/tmp/llm-gw-installer install

# 跨平台编译
make cross-compile   # 见下方 Makefile（可选）
```

## 子命令

```
llm-gw-installer doctor      # 检测环境（OS/docker/网络/端口）
llm-gw-installer install     # 一键安装并部署（检测到已有安装时自动保留原配置，见下节）
llm-gw-installer upgrade     # 升级管理（--action check / apply / rollback）
llm-gw-installer uninstall   # 卸载（--purge 彻底清理）
```

## 安装（install）与更新（update/upgrade）的区别

更新不是重新安装 —— 两条硬性语义（2026-11-19 过滤条件与源码更新链路审计轮固化）：

1. **保留原有配置与密钥**：`install` 检测到安装目录已有 `.env` 时，默认把它作为
   配置基底（缺什么补什么，不会重新生成 POSTGRES/API/JWT/加密密钥 —— 加密密钥
   一旦轮换，既有凭据密文不可解）。需要彻底重置配置时显式加 `--fresh-config`
   （或先 `uninstall`）。升级通道（`upgrade`）本身不碰 `.env`。
2. **数据库已在 ⇒ 只补增量**：`install` 的数据库初始化（InitSchema）检测到
   `schema_migrations` 台账非空时进入增量模式 —— 跳过基线三件
   （00-prereqs/01-schema/02-seed，它们是全新安装的一次性底座），startup 迁移
   只应用台账未登记的 pending 项。种子全部 `ON CONFLICT DO NOTHING`，重放与
   跳过都不会覆盖用户改动；新版本的种子投递按仓内纪律走更高编号的 startup 迁移。

镜像版本是「版本物」不是「用户配置」：复用 `.env` 时若环境变量 `APP_IMAGE_TAG`
显式给出新 tag，会覆盖 `.env` 里的旧值，避免「更新」拉回旧镜像。

`upgrade` 的两条路径与迁移投递面（重要）：

- `--offline --offline-package <pkg.tar.gz>`（**docker compose 安装的正确通道**）：
  备份二进制+compose+.env → docker load 镜像 → 逐个执行包内 `migrations/*.sql`
  （数值序）→ 替换二进制 → 重启 → 健康检查失败自动回滚。
- 在线（默认，**仅 systemd 单机布局**）：下载二进制 → SHA256 → 备份 → 替换 →
  `systemctl restart`。检测到 `compose.yml` 时会 fail-fast 并指路离线包。
  在线路径**不投递 SQL 迁移文件链** —— 新版本的启动迁移靠网关重启时
  `db.Open → ApplyMigrations` 的 Go ensure 链自愈；落在
  `sql/migrations/startup` 文件链里的结构变更必须走离线包。

## 跨平台编译

```bash
# Linux/macOS
GOOS=linux  GOARCH=amd64 go build -o dist/llm-gw-installer-linux-amd64 ./cmd/llm-gw-installer/
GOOS=linux  GOARCH=arm64 go build -o dist/llm-gw-installer-linux-arm64 ./cmd/llm-gw-installer/
GOOS=linux  GOARCH=loong64 go build -o dist/llm-gw-installer-linux-loong64 ./cmd/llm-gw-installer/
GOOS=darwin GOARCH=amd64 go build -o dist/llm-gw-installer-darwin-amd64 ./cmd/llm-gw-installer/
GOOS=darwin GOARCH=arm64 go build -o dist/llm-gw-installer-darwin-arm64 ./cmd/llm-gw-installer/
GOOS=windows GOARCH=amd64 go build -o dist/llm-gw-installer-windows-amd64.exe ./cmd/llm-gw-installer/
GOOS=windows GOARCH=arm64 go build -o dist/llm-gw-installer-windows-arm64.exe ./cmd/llm-gw-installer/
```

## 存储模式（installer lite / full 选择）

llm-gw-installer 支持两种存储模式，安装时选择其一：

| 模式 | 存储后端 | 适用场景 | 安装时拉取的镜像 | 是否初始化 schema |
|------|----------|----------|------------------|------------------|
| `full`（默认） | PostgreSQL (kx-citus) + Redis | 标准生产 / 多副本 / 高并发 | kx-llm-gateway-go + kx-citus + kx-redis | 是（等待 PG ready + InitSchema） |
| `lite` | SQLite + 本地 | 单机 / 开发 / CI / 演示 | 仅 kx-llm-gateway-go | 否（SQLite 自动建表） |

### 选择方式（按优先级）

1. CLI flag：`--mode lite` 或 `--mode full`（最高优先级）
2. 配置文件（`--config /path/to/install.env`）：
   ```
   STORAGE_MODE=lite
   LLM_GATEWAY_MASTER_URL=https://llm.kxpms.cn
   INSTALL_SKIP_ACTIVATION=0
   ```
3. 交互向导：进入安装步骤时提示 `[1] full  [2] lite`，默认 `1`

非 TTY（CI / `--skip-prompt`）且未提供 `--config` 时，默认 `full`。

### lite 模式的 install 行为差异

| 步骤 | full | lite |
|------|------|------|
| 1. 环境检测 | 同 | 同 |
| 2. 配置（wizard / config） | 同 | 同（新增 storage mode / master URL） |
| 3. 拉取镜像 | kx-citus + kx-redis + kx-llm-gateway-go | **仅 kx-llm-gateway-go**（跳过 citus/redis） |
| 4. 写 .env | 写入全部键（含 `LLM_GATEWAY_STORAGE_MODE=full` / `LLM_GATEWAY_MASTER_URL` / `INSTALL_SKIP_ACTIVATION` 三个新键） | 同 full，仅 `LLM_GATEWAY_STORAGE_MODE=lite` |
| 5. 目录结构 | 同 | 同（db/data / redis/data 目录仍创建但不使用） |
| 6. compose.yml | 完整 3 服务 | **剥离 kx-citus + kx-redis**，llm-gateway-go 移除 `depends_on` 与 PG/Redis env |
| 7. 启动容器 | 3 容器 | 仅 kx-llm-gateway-go |
| 8. 初始化数据库 | 等待 PG ready + InitSchema（407 startup 迁移） | **跳过**（SQLite 由 app 自动建表） |
| 9. 健康检查 | 5 项全检 | 仅校验容器 + /healthz；PG/Redis/Schema 不适用、强制置通过 ✅（报告中显示为 true） |

### 新增 install flags

```
--mode string           存储模式: full | lite（默认空 → 走 wizard / 默认 full）
--master-url string     主控端 URL（默认 https://llm.kxpms.cn）
--skip-activation       bool，跳过 install 末尾的自动激活调用（默认 false）
```

### 新增 .env 键（写入 `{installDir}/.env`）

| Key | 默认 | 含义 |
|-----|------|----------|
| `LLM_GATEWAY_STORAGE_MODE` | `full` | 运行时由 `cmd/gateway` 的 `storage_mode_init` 读取；lite 走 SQLite，full 走 PG/Redis |
| `LLM_GATEWAY_MASTER_URL` | `https://llm.kxpms.cn` | license 激活 + 心跳上报的目标 URL |
| `INSTALL_SKIP_ACTIVATION` | `0` | 是否跳过 install 末尾的自动注册激活调用（逻辑见 activation.RunAutoActivate） |

## 环境变量

| 变量 | 默认值 | 用途 |
|---|---|---|
| `KX_REGISTRY` | `registry.kxpms.cn` | 自定义内部 registry |
| `KX_REGISTRY_USERNAME` | 空 | 自定义 registry 用户名 |
| `KX_REGISTRY_PASSWORD` | 空 | 自定义 registry 密码 |
| `KX_REGISTRY_INSECURE` | `false` | 允许 HTTP |
| `APP_IMAGE_TAG` | 从 MANIFEST 读 | 应用镜像 tag 覆盖 |
| `GOPROXY` | `https://goproxy.cn,direct` | go module 代理 |

## 镜像源 fallback 链

```
[1] 离线包 images/*.tar.gz（最高优先级）
    ↓ 失败
[2] registry.kxpms.cn（内部 registry）
    ↓ 失败
[3] registry.cn-hangzhou.aliyuncs.com（阿里云 mirror）
    ↓ 失败
[4] registry-1.docker.io（官方 docker hub）
    ↓ 全部失败
❌ 清晰报错
```

## 单元测试

```bash
go test ./...
```

## 嵌入资源

所有 SQL 文件、compose 模板、报告模板都通过 `go:embed` 内嵌到二进制中：

```go
//go:embed embeddata/compose.yml
var composeYAML []byte

//go:embed embeddata/00-prereqs.sql
var sqlPrereqs []byte
// ...
```

修改 templates 后需同步到 `cmd/llm-gw-installer/embeddata/`。

## 集成测试

端到端测试需要真实 docker 环境。建议在 CI 中跑：

```bash
make test-e2e
```

测试场景：
1. 场景 A：离线包完整 → 直接 load 成功
2. 场景 B：离线包损坏 + 内网通 → 从 registry.kxpms.cn 拉取
3. 场景 C：内网不通 + 公网通 → 从 aliyun mirror 拉取
4. 场景 D：完全断网 → 清晰报错
5. 国产 OS：自动装 docker + 全流程

## 已知限制

- **HarmonyOS NEXT**：不支持（没有 Linux 容器支持）
- **macOS**：需要用户手动装 OrbStack 或 Docker Desktop
- **Windows**：需要用户手动装 Docker Desktop + WSL2
