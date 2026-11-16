# AI Native Gateway 帮助手册

> 本手册覆盖从下载安装到日常运维的完整链路。所有命令、路径、默认值均取自当前代码库实现，与线上版本保持同步。

## 1. 下载

### 1.1 获取源码

网关的安装、部署、升级均以源码树为入口，克隆后即可安装。

```bash
git clone https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git
cd llm-gateway-go
```

备用镜像（GitHub）：

```bash
git clone https://github.com/halfking/ai-native-gateway-core.git
```

### 1.2 官方安装脚本

不克隆源码时，直接执行官方脚本即可完成安装。脚本地址可通过 `MAINTAIN_BASE` 环境变量覆盖，默认指向官方分发服务。

```bash
# 标准安装（交互式）
curl -fsSL "https://llmgo.kxpms.cn/maintain-api/distribution/install-scripts/install" | bash

# 精简模式：单进程 + SQLite，不依赖外部数据库
curl -fsSL "https://llmgo.kxpms.cn/maintain-api/distribution/install-scripts/host" | bash

# 完整模式：PostgreSQL + Redis 栈
curl -fsSL "https://llmgo.kxpms.cn/maintain-api/distribution/install-scripts/docker" | bash

# 升级到最新发布版本
curl -fsSL "https://llmgo.kxpms.cn/maintain-api/distribution/install-scripts/upgrade" | bash -s -- run
```

### 1.3 Go 与 npm 渠道

```bash
# Go 生态安装（需要 Go 1.27.4）
go install github.com/kaixuan/llm-gateway-go/installer/cmd/llm-gw-installer@latest

# npm 渠道（需要 Node 18 及以上；在源码树内会优先使用本地包）
npm install -g ./npm/llm-gw-installer
```

npm 公共 registry 版本 `@kaixuan/llm-gw-installer` 尚未发布，请使用上面的源码树内安装方式。

### 1.4 离线包与容器镜像

完全离线的环境可下载离线包：

```bash
curl -fLO https://download.kxpms.cn/llm-gateway-go/latest/llm-gateway-go-<VERSION>-linux-amd64-offline.tar.gz
```

下载门户：<https://llmgo.kxpms.cn/download>

容器镜像名称为 `kx-llm-gateway-go`（网关）、`kx-citus`（PostgreSQL）、`kx-redis`（Redis）。镜像拉取按四级回退链进行，任一环节命中即停止：

1. 本地离线包 `images/*.tar.gz`
2. 内部仓库 `registry.kxpms.cn`
3. 阿里云镜像 `registry.cn-hangzhou.aliyuncs.com`
4. Docker Hub `registry-1.docker.io`

可通过 `KX_REGISTRY`、`KX_REGISTRY_USERNAME`、`KX_REGISTRY_PASSWORD`、`KX_REGISTRY_INSECURE`、`APP_IMAGE_TAG` 覆盖默认行为。

## 2. 安装

### 2.1 安装前的环境体检

安装前先让脚本检查本机条件，该命令不下载也不写入任何文件。

```bash
bash install.sh doctor     # 平台、Go、Node、npm、Docker、curl、端口占用情况
bash install.sh version    # 已安装版本与可用更新方式
```

Windows 用户在 PowerShell 中执行 `./install.ps1`，在 CMD 中执行 `install.bat`（后者会转交给 PowerShell 脚本）。

### 2.2 前置条件

| 项目 | 要求 | 说明 |
| --- | --- | --- |
| Go | 1.27.4 | 源码构建与 Go 渠道安装需要 |
| Node.js | 18 及以上 | 仅 npm 渠道需要 |
| PostgreSQL | 14 及以上 | 完整模式的持久化存储 |
| Redis | 7 及以上 | 完整模式的热点状态与限流 |
| Docker | 完整模式必需 | macOS 需 OrbStack 或 Docker Desktop；Windows 需 Docker Desktop 与 WSL2 |
| 内存 | 4GB 及以上 | 快速启动栈的最低要求 |
| 磁盘 | 10GB 及以上 | 含数据库与日志 |

网关默认监听端口为 **8781**，安装器会检查 8781、5432、6379 三个端口是否被占用。

### 2.3 两种存储模式

安装时需要选择存储规模，模式决定了依赖与初始化行为。

| 模式 | 存储 | 适用场景 | 拉取镜像 | 是否初始化表结构 |
| --- | --- | --- | --- | --- |
| `full`（默认） | PostgreSQL + Redis | 生产、多副本、高并发 | 网关 + 数据库 + Redis | 是，等待数据库就绪后执行初始化 |
| `lite` | SQLite + 本地文件 | 单机、内网、评估、持续集成 | 仅网关 | 否，由 SQLite 自动建表 |

选择优先级：命令行参数 `--mode lite|full` 最高，其次是配置文件中的 `STORAGE_MODE`，最后是交互式向导选项。在无终端环境（持续集成）下未指定时默认使用 `full`。

### 2.4 安装步骤

```bash
# 交互式安装：先选渠道，再选规模
bash install.sh

# 明确指定：源码渠道 + 精简模式 + 跳过所有提问
bash install.sh --channel source --mode lite --yes

# 非交互式安装
NO_INTERACTIVE=1 bash install.sh --mode full
```

安装器本体也可单独使用：

```bash
llm-gw-installer doctor      # 检测系统、Docker、网络、端口
llm-gw-installer install     # 一键安装与部署
llm-gw-installer uninstall --purge   # 卸载，--purge 同时删除数据
llm-gw-installer version
```

安装相关子命令会原样透传给安装器：`upgrade`（升级）、`uninstall`（卸载）、`activate`（激活）、`heartbeat`（心跳）。`doctor` 与 `version` 由引导脚本自身处理，用于回答「这台机器能装什么」和「我现在是什么版本」。

### 2.5 关键环境变量

| 变量 | 作用 |
| --- | --- |
| `LLM_GATEWAY_DATABASE_URL` | PostgreSQL 连接串，完整模式必填 |
| `LLM_GATEWAY_REDIS_ADDR` | Redis 地址，形如 `host:6379` |
| `LLM_GATEWAY_REDIS_PASSWORD` | Redis 密码，可为空 |
| `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY` | 上游密钥的加密主密钥（32 字节），更换后旧密文无法解密 |
| `LLM_GATEWAY_SECRET_KEY` | 会话与签名密钥 |
| `LLM_GATEWAY_API_KEY` | 初始网关调用密钥 |
| `LLM_GATEWAY_ADMIN_API_KEY` | 管理接口调用密钥 |
| `LLM_GATEWAY_LISTEN` | 监听地址，默认 `:8781` |
| `LLM_GATEWAY_STORAGE_MODE` | `full` 或 `lite`，由安装器写入配置文件 |
| `LLM_GATEWAY_MASTER_URL` | 授权与心跳服务地址，默认 `https://llm.kxpms.cn` |

## 3. 部署

### 3.1 快速启动（Docker Compose）

```bash
git clone https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git ai-native-gateway
cd ai-native-gateway

cp .env.quickstart.example .env
# 按文件内注释生成随机值后写入 .env
openssl rand -hex 16      # POSTGRES_PASSWORD
openssl rand -hex 16      # REDIS_PASSWORD
openssl rand -hex 32      # CREDENTIAL_ENCRYPTION_KEY
openssl rand -base64 32   # SECRET_KEY

docker compose -f docker-compose.quickstart.yml up -d
curl http://localhost:8781/healthz     # 期望返回 {"status":"ok"}
open http://localhost:8781/admin
```

停止与清理：

```bash
docker compose -f docker-compose.quickstart.yml down     # 停止，保留数据
docker compose -f docker-compose.quickstart.yml down -v  # 同时删除数据卷，会清空数据库
```

### 3.2 源码部署

```bash
go build -o gateway ./cmd/gateway
LLM_GATEWAY_CONFIG_FILE=config.example.yaml ./gateway
curl http://localhost:8781/healthz
```

### 3.3 编排文件选择

| 文件 | 场景 | 关键差异 |
| --- | --- | --- |
| `docker-compose.quickstart.yml` | 评估与本地自包含 | 自带 PostgreSQL 与 Redis，端口仅绑定回环地址 |
| `docker-compose.persistent.yml` | 连接已有数据库的持久化部署 | 不含数据库容器，多个环境变量强制必填 |
| `docker-compose.v4-local.yml` | 复用宿主机数据库 | 使用宿主机数据库，Redis 使用独立库号隔离 |
| `docker-compose.deploy-test.yml` | 客户实例本地仿真 | 自带数据库镜像，与宿主机隔离 |
| `docker-compose.yml` | 含监控的主栈 | 额外包含 Prometheus 与 Grafana |

### 3.4 数据库迁移

数据库迁移文件位于 `sql/migrations/`，命名规则为 `NNN_名称.sql`，回滚文件为同名加 `.down.sql`。启动迁移由安装器按登记顺序执行，登记清单在 `sql/schema/installed_startup_migrations.tsv`，该文件由安装器生成，不要手工编辑。

```bash
# 查看登记的启动迁移顺序
column -t -s$'\t' sql/schema/installed_startup_migrations.tsv | head -30

# 校验登记清单与安装器一致（不一致即报错）
cd installer && go test ./internal/dbinit/... -run TestStartupManifest
```

网关进程启动时会再执行一次幂等的结构校验，确保数据库处于可用状态。

### 3.5 升级与回滚

升级前先做三段检查：进程存活、依赖就绪、版本身份。任何一段不通过就应停止升级。

```bash
bash scripts/lifecycle/preflight.sh \
  --base-url http://127.0.0.1:8781 \
  --expected-version <目标版本> \
  --check-db
```

管理台的「更新与激活」入口提供版本检查、发布、灰度与回滚操作；客户侧也可通过只读接口查询更新状态。

### 3.6 部署后验证

```bash
# 进程存活（匿名可访问）
curl -s http://127.0.0.1:8781/healthz

# 依赖就绪：数据库与 Redis 都通才返回 200，否则返回 503
curl -s http://127.0.0.1:8781/readyz

# 版本身份：版本号、提交号、构建序号、构建时间
curl -s http://127.0.0.1:8781/version

# 详细健康（需管理密钥）：熔断、限流、代理、数据库与 Redis 延迟
curl -s -H "Authorization: Bearer $LLM_GATEWAY_ADMIN_API_KEY" http://127.0.0.1:8781/healthz/full
```

| 端点 | 含义 | 鉴权 |
| --- | --- | --- |
| `/healthz` | 进程存活，附带数据库与 Redis 的连通情况 | 匿名 |
| `/readyz` | 严格就绪门，依赖不通返回 503 | 匿名 |
| `/healthz/full` | 详细健康，含熔断状态与依赖延迟 | 管理密钥 |
| `/version` | 版本身份信息 | 匿名 |
| `/metrics` | Prometheus 指标 | 管理密钥 |
| `/api/health/system` | 系统健康摘要 | 匿名 |
| `/api/v2/capabilities` | 能力清单 | 匿名 |

## 4. 供应商配置

### 4.1 概念

供应商（Provider）代表一个上游模型服务方，例如 OpenAI、Anthropic、Gemini 或自建的推理服务。供应商本身不保存密钥，密钥保存在凭据中（见第 5 章）。

### 4.2 新增供应商

1. 进入「模型与路由」组的「供应商」页面（`/providers`），点击「添加供应商」。
2. 选择模式：
   - **目录模式**：从下拉框选择目录编码，留空显示名时使用目录默认名。
   - **自定义模式**：勾选「自定义供应商（不在目录中）」，自行填写代码、名称、协议与 Base URL。
3. 填写 Base URL 后点击「探测」，系统会检查可达性与协议；探测不通过不允许提交。
4. 保存。

| 字段 | 含义 | 必填 |
| --- | --- | --- |
| 选择目录 | 目录编码，来自内置供应商目录 | 目录模式必填 |
| 显示名 | 展示名称，留空则使用目录默认名 | 否 |
| 供应商代码 | 自定义供应商的唯一标识 | 自定义模式必填 |
| 供应商名称 | 自定义供应商的展示名 | 自定义模式必填 |
| 协议 | 出站协议，见下表 | 可选 |
| Base URL | 上游服务根地址 | 自定义模式必填 |
| 备注 | 内部说明 | 否 |

### 4.3 协议类型

| 协议 | 含义 |
| --- | --- |
| `openai-completions` | OpenAI 兼容的对话补全，DeepSeek 等同族厂商也走这一协议 |
| `openai-responses` | OpenAI Responses 协议 |
| `anthropic-messages` | Anthropic Messages 协议 |
| `gemini-generate` | Gemini 生成内容协议 |
| `ollama-native` | 本地部署推理服务的原生协议 |

### 4.4 供应商属性

供应商的属性分为两类维度：

- **部署形态**：`cloud`（云端）或 `local`（本地部署）。
- **来源分类**：`official`（官方）、`official_proxy`（官方代理）、`third_party_relay`（第三方中转）、`aggregator`（聚合平台）、`self_host`（自建）。

超时、速率与并发限制**不在供应商行上**，它们属于凭据级配置。

### 4.5 供应商详情页

进入 `/providers/{供应商ID}`，详情页包含凭据、模型、质量、日志、错误明细、诊断、探测、设置八个页签：

- **模型**：维护该供应商提供的模型清单；模型与凭据的绑定关系在这里调整，含权重、人工优先级与路由档位。
- **探测**：查看与触发该供应商的探活记录。
- **设置**：修改名称、Base URL、协议、部署形态、来源分类、折扣率、出口策略与备注。

## 5. 凭据管理

### 5.1 概念

凭据（Credential）是网关调用某个供应商时使用的上游密钥。一个供应商可以挂多条凭据，网关会在它们之间做选择与故障转移。

> 重要区分：**API Key** 是客户端调用网关的通行证，**凭据** 是网关调用上游的密钥。两者方向相反，不要混用。详见第 8 章。

### 5.2 添加凭据

1. 在供应商详情页的「凭据」页签，或在供应商列表页的凭据抽屉中，点击「添加凭据」。
2. 填写以下内容后保存，保存后系统会自动触发一次探活。

| 字段 | 含义 | 必填 |
| --- | --- | --- |
| API Key | 上游密钥明文，提交后加密存储 | 是 |
| 标签 | 便于识别的凭据名称 | 否 |
| 并发上限 | 该凭据同时可处理的请求数，0 表示不限 | 否 |
| 指纹槽 | 指纹槽位数，建议取并发数的四分之一 | 否 |

一条凭据还可以配置备用密钥（多密钥轮转），在凭据详情中通过密钥子资源增删改。

### 5.3 凭据状态

| 维度 | 取值 | 含义 |
| --- | --- | --- |
| 运行状态 | `active`、`cooling`、`degraded`、`quarantine`、`quota_expired`、`disabled` | 凭据当前是否参与路由 |
| 生命周期 | `active`、`disabled`、`suspended`、`retired` | 人工设定的生命周期 |
| 可用性 | `ready`、`cooling`、`rate_limited`、`auth_failed`、`unreachable`、`suspended` | 探测与限流推导出的可用性 |
| 配额状态 | `ok`、`cooling`、`periodic_exhausted`、`balance_exhausted`、`permanently_exhausted` | 额度与余额情况 |
| 健康状态 | `unknown`、`healthy`、`warning`、`unreachable` | 探活结论 |
| 熔断状态 | `closed`、`open`、`half_open` | 连续失败触发的熔断器状态 |

### 5.4 凭据监控

「模型与路由」组的「凭据监控」页面（`/routing-v2/credentials`）提供全局视图：

- 顶部四张指标卡：凭据总数、可用数、异常数、失效模型总数。
- 表格列：凭据、供应商、可用性、健康、可用模型数、最近成功率、失效模型数、并发。
- 快捷筛选：「失效模型」筛出存在不可用模型的凭据，「低成功率」筛出成功率低于一半的凭据。
- 批量操作：提升（恢复为可用）与降级（置为冷却并定时恢复），两者都会写入审计日志。

其他页签：凭据热力图（按时间粒度看可用性分布）、凭据路由记录、错误趋势、探测健康（仅超级管理员可见）。

### 5.5 探测与自动处置

探测由后台任务驱动，不是前端定时器：

| 任务 | 周期 | 作用 |
| --- | --- | --- |
| 凭据探测 | 默认每小时一轮，快速复探间隔默认 5 分钟 | 真实发起一次最小对话请求，更新健康与可用性 |
| 凭据恢复 | 30 秒 | 清理到期的冷却状态，恢复为可用 |
| 自动修复 | 5 分钟 | 复探已失效的「凭据 + 模型」组合 |
| 逐凭据自检 | 5 分钟一轮、15 分钟窗口 | 每日逐凭据体检 |

探测失败的处置规则：

| 探测结果 | 处置 |
| --- | --- |
| 上游 5xx 或响应无法解析 | 标记为警告，仅记录观察，不影响路由 |
| 鉴权失败 | 标记为不可达并置为鉴权失败 |
| 额度耗尽 | 标记为警告并置为配额耗尽状态 |
| 上游限流 | 标记为警告并置为限流，约 60 秒后自动恢复 |
| 单个模型不存在 | 只停用该模型的绑定，其余模型照常路由 |
| 探针请求本身参数不合法 | 仅记录观察，不误伤整条凭据 |

相关环境变量：`LLM_GATEWAY_CRED_PROBE_V2_INTERVAL`（探测周期）、`LLM_GATEWAY_CRED_PROBE_V2_FAST_REPROBE_DELAY`（快速复探间隔）。

### 5.6 密钥轮换与保护

- **轮换**：在凭据详情中替换 API Key。轮换后系统会排队一次探针验证，轮换不改变凭据身份与已有模型绑定。
- **查看明文**：具备密钥管理权限时可以在详情中回显明文，用于核对。
- **加密存储**：上游密钥使用 AES-256-GCM 信封加密落库，主密钥来自 `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY`（或 `LLM_GATEWAY_SECRET_KEY`）。写入前会做一次加解密自检，自检失败则拒绝落库。更换主密钥后，旧的加密凭据将无法解密。

## 6. 租户管理

### 6.1 概念

租户是数据隔离与计费的基本单位。数据库的绝大部分表都带有租户维度，网关按行级安全策略隔离。`default` 是平台默认租户，承担整站运维视图，且**不允许停用或挂起**。

### 6.2 创建租户

1. 进入「租户用户」组的「租户管理」页面（`/tenants`），点击「新建租户」。
2. 填写租户编码、租户名称、状态、联系邮箱与描述后创建。

| 字段 | 含义 | 必填 |
| --- | --- | --- |
| 租户编码 | 租户唯一标识，创建后不可改 | 是 |
| 租户名称 | 展示名称 | 是 |
| 状态 | `active`、`trial`、`suspended`、`expired`、`disabled` | 否 |
| 联系邮箱 | 通知与联系地址 | 否 |
| 描述 | 备注说明 | 否 |

创建租户时系统会在同一事务中自动创建一个租户管理员账号：用户名为「租户编码 + user」，初始密码为 `Tenant租户编码@2026`，并标记为必须修改密码。

### 6.3 租户详情

进入 `/tenants/{租户编码}`，包含九个页签：概览、用户、密钥、模型管控、统计、计费审计、钱包、订单、账本。

- **用户**：为该租户创建成员账号，支持启用与停用。
- **密钥**：查看该租户下的 API Key。
- **模型管控**：维护该租户可用的模型范围（按模型名授权，可添加与移除）。
- **钱包**：查看与调整租户余额，支持发放积分。
- **订单**：处理充值订单并确认到账。
- **账本**：逐笔流水。

### 6.4 租户门户

非默认租户的成员登录后会看到「我的服务」组：我的模型、套餐与充值、月账单、我的消耗、我的账户。这五个入口只对非默认租户可见，平台侧账号不显示。

### 6.5 生命周期

- **启用与停用**：在租户列表或详情中修改状态。停用后该租户成员无法登录。
- **删除**：当前管理台不提供删除入口。确需清理时通过停用处理，避免留下悬空引用。

## 7. 用户管理

### 7.1 角色

| 角色 | 说明 |
| --- | --- |
| `super_admin` | 超级管理员，可见并管理全站数据，跨租户操作 |
| `tenant_admin` | 租户管理员，只能管理本租户内的用户、密钥与配置 |
| `user` | 普通成员，由外部身份源映射而来，在管理台用户目录中不可写 |

平台运维视图（`platformOps`）是「超级管理员且处于默认租户」的视图状态，不是独立的用户角色。

### 7.2 用户列表

进入「租户用户」组的「用户管理」页面（`/users`）：

- 筛选：按租户、角色、状态筛选，支持搜索。
- 排序：默认按最近登录、近 30 天请求数、近 30 天消耗积分排序。
- 操作：新建用户、启用或禁用、删除、重置密码。

### 7.3 新建用户

| 字段 | 含义 | 必填 |
| --- | --- | --- |
| 用户名 | 登录名，租户内唯一 | 是 |
| 密码 | 不少于 8 位并满足复杂度要求 | 是 |
| 租户 | 所属租户 | 是 |
| 显示名 | 展示名称 | 否 |
| 邮箱 | 联系邮箱 | 否 |
| 角色 | `super_admin` 或 `tenant_admin` | 否 |

创建后账号标记为必须修改密码，用户首次登录时会被要求改密。租户管理员不能创建或提升为超级管理员。

### 7.4 启用、禁用与重置密码

- **禁用**：把账号置为禁用状态，账号保留但不能登录；再次启用即恢复。
- **重置密码**：管理员可直接重设密码，重置后同样要求用户首次登录改密。
- **删除**：永久移除该用户。删除前请确认其名下没有仍在使用的 API Key。

### 7.5 登录认证

| 方式 | 说明 |
| --- | --- |
| 本地账号 | 默认方式，密码以 bcrypt 存储；登录成功签发会话令牌并写入 HttpOnly Cookie，令牌不落在浏览器本地存储中 |
| 外部身份源 | 可切换为 Casdoor 等 OIDC 身份源，通过 `AUTH_PROVIDER`、`CASDOOR_ENDPOINT`、`CASDOOR_CLIENT_ID` 等环境变量配置。外部身份源中的管理员角色映射为超级管理员，组织管理员映射为租户管理员，其余映射为普通成员 |

### 7.6 初始管理员

当系统还没有任何用户时，会自动播种一个超级管理员：用户名 `admin`，归属默认租户，密码取 `LLM_GATEWAY_SEED_ADMIN_PASSWORD`；未设置该变量时随机生成并打印在启动日志中。

## 8. API Key 设置

### 8.1 概念

API Key 是客户端调用网关的通行证，形如 `sk-` 加 48 位随机串。它绑定租户、应用与归属用户，并携带预算与限流配置。

### 8.2 签发密钥

1. 进入「租户用户」组的「密钥管理」页面（`/keys`），点击「签发密钥」。
2. 填写表单。系统会对「租户 + 应用 + 别名」做防抖查重，重复时直接拒绝签发。
3. 保存后明文密钥只返回这一次，请立即保存；之后也可用「查看」取回。

| 字段 | 含义 | 默认值 |
| --- | --- | --- |
| 租户 | 归属租户，非默认租户登录时锁定为自身 | `default` |
| 应用代码 | 所属应用，决定该密钥可用的模型范围与客户端画像 | `default` |
| 密钥别名 | 同一租户与应用下的唯一标识 | 必填 |
| 归属用户 | 用量归属到哪个用户 | 后端兜底为 `admin` |
| 预算上限 | 累计花费上限，超出返回 402 | 不限 |
| 每分钟请求数 | 该密钥的 RPM 上限 | 见下方限流口径 |
| 并发数 | 同时在途请求上限 | 不设上限 |
| 每分钟 Token 数 | 每分钟 Token 预检上限 | 不执法 |
| 备注 | 创建原因说明 | `admin manual creation` |

### 8.3 限流口径

限流有三档，语义各不相同，配置前务必分清：

| 旋钮 | 留空 | 设为 0 | 设为正数 |
| --- | --- | --- | --- |
| 每分钟请求数 | 取所属档位默认值 | **显式不限** | 按设定值 |
| 并发数 | **不设上限** | 不设上限 | 按设定值 |
| 每分钟 Token 数 | **不执法** | 不执法 | 按设定值 |

平台级默认值可在「密钥管理」页面的「默认速率限制配置」中调整：每分钟请求数 60、并发数 20、每分钟 Token 数不限。租户级可以再覆盖平台默认值。

写入上限：每分钟请求数不超过 10000，并发数不超过 1000，每分钟 Token 数不超过 10 亿，预算不超过 100000。

### 8.4 密钥状态与失效

| 状态 | 含义 |
| --- | --- |
| `active` | 可用 |
| `pending` | 等待管理员批准（自助申请流程） |
| `disabled` | 人工禁用，可重新启用 |
| `throttled` | 因异常用量被临时封禁 |
| `revoked` | 已吊销，终态 |

- **吊销**：删除即置为已吊销状态，不可恢复；需要重新签发。
- **轮换**：先吊销旧密钥再签发新密钥。
- **过期**：`expires_at` 到期后密钥自动不可用。

自助申请产生的密钥在「密钥申请」页面（`/key-applications`）中处理，可按待审、已批、已拒、过期四个页签筛选并批准或拒绝，批准后可查看密钥明文。

### 8.5 与上游凭据的差别

| 对比项 | API Key（本章） | 凭据（第 5 章） |
| --- | --- | --- |
| 方向 | 客户端到网关 | 网关到上游供应商 |
| 内容 | 网关生成的随机串 | 上游供应商签发的真实密钥 |
| 管理页面 | `/keys`、`/key-applications` | `/providers`、`/routing-v2/credentials` |
| 限流含义 | 限制「谁在调我」 | 限制「我调上游能调多快」 |

## 9. 客户端调用

### 9.1 对外接口

网关对外提供多协议兼容入口，客户端只需替换 Base URL 与密钥。

| 端点 | 协议 |
| --- | --- |
| `POST /v1/chat/completions` | OpenAI 兼容对话补全 |
| `POST /v1/completions` | OpenAI 兼容文本补全（旧版） |
| `POST /v1/embeddings` | OpenAI 兼容向量 |
| `GET /v1/models` | 模型列表 |
| `POST /v1/responses` | OpenAI Responses 协议 |
| `POST /v1/messages` | Anthropic 兼容消息 |
| `POST /v1/messages/count_tokens` | Anthropic Token 计数 |
| `POST /v1/models/{model}:generateContent` | Gemini 生成内容 |
| `POST /v1/images/generations` | 图片生成 |
| `POST /v1/audio/transcriptions` | 语音转写 |

> 注意：`/v1/models` 是模型列表，而带尾斜杠的 `/v1/models/` 是 Gemini 的生成内容入口，两者不要混淆。

### 9.2 认证方式

按以下优先级读取密钥：

1. `Authorization: Bearer <key>`（通用写法，推荐）
2. `x-api-key: <key>`（Anthropic 客户端习惯）
3. `x-goog-api-key: <key>`（Google 客户端习惯）
4. `?key=<key>`（Gemini 兜底）

### 9.3 Base URL

Base URL 形式为「域名（或地址）+ 端口 + `/v1`」。本地默认 `http://localhost:8781/v1`，生产环境由反向代理吸收端口。OpenAI 兼容端点使用带 `/v1` 的地址；Anthropic 客户端使用不带 `/v1` 的地址，因为其端点本身就是 `/v1/messages`。

### 9.4 调用示例

命令行调用：

```bash
curl http://localhost:8781/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "glm-4-flash",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "Hello!"}
    ],
    "max_tokens": 256
  }'
```

Python：

```python
from openai import OpenAI

client = OpenAI(
    api_key="sk-xxx",
    base_url="http://localhost:8781/v1",
)

response = client.chat.completions.create(
    model="glm-4-flash",
    messages=[
        {"role": "system", "content": "You are a helpful assistant."},
        {"role": "user", "content": "Hello!"},
    ],
    max_tokens=256,
)

print(response.choices[0].message.content)
```

Node.js：

```javascript
import OpenAI from "openai";

const client = new OpenAI({
  apiKey: "sk-xxx",
  baseURL: "http://localhost:8781/v1",
});

const response = await client.chat.completions.create({
  model: "glm-4-flash",
  messages: [{ role: "user", "content": "Hello!" }],
  max_tokens: 256,
});

console.log(response.choices[0].message.content);
```

流式调用：

```bash
curl -N http://localhost:8781/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{"model":"glm-4-flash","messages":[{"role":"user","content":"Count to 5."}],"stream":true,"max_tokens":64}'
```

### 9.5 模型选择

请求体中的 `model` 字段指定模型名，可选值来自 `GET /v1/models` 返回的 `data[].id`。该接口默认只返回当前可路由的模型；数据库短暂不可用时会返回上一次的结果，属于降级行为而非故障。

### 9.6 流式响应形态

| 协议 | 事件形态 |
| --- | --- |
| OpenAI 对话补全 | 纯 `data:` 帧，结束标志为 `data: [DONE]` |
| Anthropic 消息 | 带事件名：`message_start`、`content_block_start`、`content_block_delta`、`content_block_stop`、`message_delta`、`message_stop` |
| OpenAI Responses | 带事件名：`response.created`、`response.output_item.added`、`response.content_part.added`、`response.output_text.delta` |
| 网关自有事件 | `event: error`（策略拦截或保活失败）、`event: rate_limit_waiting`（限流排队中） |

### 9.7 错误响应格式

```json
{
  "error": {
    "message": "Rate limit exceeded",
    "type": "rate_limit_error",
    "code": "rate_limit_exceeded",
    "request_id": "req-xxx",
    "kind": "rate_limit_exceeded",
    "gateway_debug": { "stage": "key_concurrency", "limit": 8, "in_flight": 8 }
  }
}
```

字段说明：`message` 为可读描述；`type` 为错误类别；`code` 为具体错误码；`kind` 为真实成因；`request_id` 可用于在请求日志中检索同一次请求；`gateway_debug` 仅在部分路径出现。Anthropic 协议的错误信封为 `{"type":"error","error":{...}}`，语义相同。

响应头始终带 `X-Request-Id`；限流响应还会带 `Retry-After` 与 `X-RateLimit-*` 系列头，客户端据此退避。

## 10. 常见故障处理

### 10.1 先看这四个入口

| 场景 | 入口 | 用途 |
| --- | --- | --- |
| 进程是否存活 | `/healthz` | 快速确认服务在跑 |
| 依赖是否就绪 | `/readyz` | 数据库或 Redis 不通时返回 503 |
| 跑的是哪个版本 | `/version` | 确认部署版本与构建序号 |
| 请求为什么失败 | 「请求日志」页面 | 按请求 ID 或错误码检索单次请求 |

命令形式：

```bash
curl -s http://127.0.0.1:8781/healthz
curl -s http://127.0.0.1:8781/readyz
curl -s http://127.0.0.1:8781/version
```

### 10.2 常见错误码与处置

| 错误码 | 状态码 | 含义 | 处置 |
| --- | --- | --- | --- |
| `authentication_error` | 401 | 密钥缺失、无效或过期 | 更换密钥；核对租户状态 |
| `insufficient_quota` | 402 | 预算或额度耗尽 | 管理员补充预算或充值 |
| `rate_limit_error` | 429 | 超过每分钟请求数限制 | 按 `Retry-After` 退避重试 |
| `rate_limit_exceeded` | 429 | 超过并发上限 | 降低客户端并发 |
| `rate_limit_exhausted` | 429 | 上游限流透传 | 退避重试，或增加上游配额 |
| `key_throttled` | 429 | 密钥因异常用量被封禁 | 联系管理员解封 |
| `invalid_model` | 400 | 网关不认这个模型名 | 用 `/v1/models` 核对模型名 |
| `modality_mismatch` | 400 | 模型不支持本次请求的模态 | 换模型或去掉图片等输入 |
| `body_too_large` | 400 | 请求体过大 | 精简请求内容 |
| `output_policy_blocked` | 403 | 命中输出策略 | 按响应中的规则字段调整内容 |
| `no_candidate` | 503 | 没有可路由的候选 | 按响应中的 `alternatives` 换模型 |
| `model_not_found` | 503 | 所有候选都失败了 | 读 `kind` 或 `X-Gateway-Last-Kind` 判断真实原因 |
| `routing_client_canceled` | 499 | 客户端提前断开 | 调整客户端超时，不是上游故障 |
| `routing_circuit_open` | 503 | 候选查询熔断已打开 | 稍后重试，持续出现则排查数据库 |
| `routing_connection_error` | 503 | 网关到数据库或上游连接失败 | 检查数据库可达性与连接池 |
| `routing_schema_error` | 500 | 网关自身的 SQL 缺陷 | 核对迁移版本，属于网关缺陷 |
| `routing_database_error` | 500 | 其他数据库错误 | 读日志原文定位 |

`model_not_found` 这个名称容易误导：它表示所有候选都失败，真正原因在 `error.kind` 里，例如上游过载、并发冲突或配额不足。

### 10.3 安装与启动问题

**数据库连接失败**：首次启动时数据库需要十几秒完成初始化，等待后重试；持续失败时查看数据库容器日志与连接串配置。

**Redis 连接被拒**：确认 Redis 已启动且地址、密码、库号正确；精简模式不依赖 Redis，可检查 `LLM_GATEWAY_STORAGE_MODE` 是否符合预期。

**端口被占用**：安装器会检查 8781、5432、6379。修改编排文件中的端口映射即可换端口。

**迁移中断**：启动迁移按登记清单顺序执行，若在某个编号中断，检查该迁移文件是否登记在 `sql/schema/installed_startup_migrations.tsv` 中，并确认对应副本已随安装包分发。

**凭据解密失败**：加密主密钥已更换或与密文不匹配。恢复原来的 `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY`；若已丢失，需要重新录入上游密钥。

### 10.4 请求失败排查顺序

1. 在「请求日志」页面用请求 ID 找到这次请求，确认落到的供应商与凭据。
2. 看失败分类：客户端取消、上游故障、限流、策略拦截还是网关自身错误。
3. 若是上游故障，到「凭据监控」看该凭据的健康与可用性。
4. 若是限流，检查该密钥的每分钟请求数、并发与预算配置。
5. 若是模型名问题，用 `/v1/models` 核对可用模型。

### 10.5 凭据侧问题

| 现象 | 排查方向 |
| --- | --- |
| 全部请求失败，提示无可路由候选 | 该供应商下是否还有可用凭据；在凭据监控页看可用性状态 |
| 凭据显示鉴权失败 | 上游密钥是否过期或被吊销；在详情中轮换密钥 |
| 凭据显示限流 | 上游账号配额用尽；等待自动恢复或调整配额 |
| 单个模型 404，其余正常 | 该模型的绑定已停用，在供应商详情的模型页签恢复绑定 |
| 成功率偏低但健康状态正常 | 查看凭据路由记录与错误趋势，定位是哪些模型在失败 |

### 10.6 性能与容量

- **吞吐不足**：先确认是否被网关自身的限流拦截（看响应码 429 与 `gateway_debug`），再考虑上游配额。
- **首字节慢**：到「凭据监控」的探测健康页签查看探针延迟，再到「模型与路由」组的路由全景看候选选择过程。
- **长对话被拒**：网关在请求接近上游上下文上限时会触发压缩；若仍失败，检查该模型的上下文窗口与压缩开关。
- **存储膨胀**：在「数据运维」组的「数据生命周期」查看热、温、冷、过期四层数据的分布与清理策略。

### 10.7 报障时需要提供的信息

提交问题时请附上：网关版本（`/version` 输出）、部署形态（容器或裸机）、操作系统与版本、数据库与 Redis 版本、复现步骤、期望结果与实际结果、相关时间段的脱敏日志，以及失败请求的 `request_id`。
