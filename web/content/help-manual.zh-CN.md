# AI Native Gateway 帮助手册

> 本手册覆盖从下载安装到日常运维的完整链路。所有命令、路径、默认值均取自当前代码库实现，与线上版本保持同步。

> **遇到问题先翻第 11 章。** 「请求失败」和「某个模型调不通」是值班最高频的两类问题：11.2 是症状速查表（现象 → 首查项 → 修正动作），11.4 给分层排查流程（分层 → 归因 → 交叉验证 → 修），11.5 给模型不可访问的定位与修正办法（判定链、10 道闸、根因对照表）。第 10 章解释每个统计数字的口径，第 12 章是安装激活与数据重置的操作手册。

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

### 2.6 安装后的激活

装完只把进程拉起来了；**激活**是把这台机器与许可证（License）绑定的一步。激活与账号是两件事：激活不产生管理员账号（那是 7.6 节的初始管理员），反过来管理员初始化也不会替你完成激活。激活之后网关才能被中心纳管（注册、心跳、后台自动注册）。

激活有两条路径，二选一即可：**命令行**（`install.sh activate`）适合装机时无人值守；**控制台向导**（`/bootstrap`）适合人工装机。

**命令行激活。** 安装脚本把 `activate` 原样透传给安装器：

```bash
# 申请试用许可证：输出 License Key 与过期时间
bash install.sh activate --mode trial --email ops@example.com --agree

# 在线激活（注册即激活）：向主控注册，凭据落到 ~/.kx-gateway/instance.token（0600）
bash install.sh activate --mode key --license-key sk-xxxxxxxx

# 离线申请：生成加密请求文件，交给主控管理员签发
bash install.sh activate --mode offline-request --license-key sk-xxxxxxxx

# 离线导入：把签发下来的 license.dat 落到安装目录
bash install.sh activate --mode offline-import --license-path ./license.dat

# 主控地址不是默认地址时
bash install.sh activate --mode key --license-key sk-xxxxxxxx --master-url https://llm.example.com
```

| 模式 | 用途 | 必填参数 | 产出 |
| --- | --- | --- | --- |
| `trial` | 申请试用许可证 | `--email`、`--agree` | 打印 License Key 与过期时间 |
| `key` | 在线激活（注册即激活） | `--license-key` | `instance_token` 写入 `~/.kx-gateway/instance.token`，`heartbeat` 子命令可直接使用 |
| `offline-request` | 生成离线激活请求 | `--license-key` | 加密请求文件（含 instance_id、硬件指纹、设备名） |
| `offline-import` | 导入离线许可证 | `--license-path`（默认 `license.dat`） | 许可证落到安装目录的 `license.dat` |

**控制台激活向导。** 入口是首页的「安装激活」链接，或直接访问 `http://<网关地址>:8781/bootstrap`（公开路由，未登录也可进），向导固定五步：

1. **用户协议** — 阅读并同意服务条款与隐私政策。
2. **设备指纹** — 自动取本机指纹（instance_id、hardware_hash、系统与网络信息），确认与目标机器一致后再往下走。
3. **激活 License** — 三选一：用 License Key 激活；一键激活（中心领码，中心不可达时自动改用本地免费许可证）；导入离线许可证。
4. **注册中心** — 向中心登记本机。**中心不可达不阻塞**：此时返回「已记录待补注册」，后台会重试。
5. **完成** — 提示已激活，可进入控制台。

**激活 API。** 向导背后是六个公开端点：

| 端点 | 方法 | 请求体 | 用途 |
| --- | --- | --- | --- |
| `/api/system/bootstrap/status` | GET | — | 查询激活态 |
| `/api/system/bootstrap/fingerprint` | GET | — | 取本机指纹：`hardware_hash`、`instance_id`、`os`、`arch`、`network_summary` |
| `/api/system/bootstrap/activate` | POST | `license_key`、`hardware_hash`、`device_name`、可选 `online` | 用许可证号激活；先走中心，失败回落本地激活器 |
| `/api/system/bootstrap/activate-quick` | POST | `hardware_hash`、`device_name` | 一键激活：中心可达则领码＋验签＋本地导入；中心不可达则生成本地免费许可证 `FREE-<instance_id 前 12 位>`（限 1 台设备、10 年有效期） |
| `/api/system/bootstrap/register-center` | POST | `license_key`、`hardware_hash`、`hostname`、`version`、`region` | 向中心注册或补注册 |
| `/api/system/bootstrap/import-offline` | POST | `signed_license` 或 `activation_code`、可选 `hardware_hash` | 离线导入，本地验签后激活，联网后自动补注册 |

这组端点在管理员登录之前即可调用（首启场景），因此**对外暴露时务必在前面加访问控制**，不要直接挂在公网入口。

**激活状态的语义。** `GET /api/system/bootstrap/status` 的响应字段：

| 字段 | 含义 |
| --- | --- |
| `activated` | 是否已激活 |
| `registered` | 是否已向中心注册 |
| `center_online` | 中心是否可达 |
| `center_url` | 中心地址 |
| `instance_id` | 本机实例 ID |
| `hardware_hash` | 本机指纹哈希 |
| `license_key` | 打码后的许可证号 |
| `device_name` | 设备名 |
| `primary_ip` | 主 IP |
| `service_version` | 服务版本 |
| `activated_at` | 激活时间（RFC3339） |
| `admin_bootstrap_required` | 是否仍需初始管理员引导 |
| `message` | 状态文案 |

`message` 只有三种取值：**已激活；中心可达，后台将自动注册/心跳**｜**已激活（离线可用）；中心不可达时不阻塞本地运转**｜**尚未激活**。

两条硬规则值得记住：

- **一机一实例 ID。** 客户端上报的 `instance_id` 一律忽略，始终以服务端自算值为准（防止他人抢占许可证席位）。
- **本地免费激活仅限本机。** 指纹与本机不符时直接拒绝。

| 状态码 | `error` | 含义 | 处理 |
| --- | --- | --- | --- |
| 403 | `hardware_hash_mismatch` | 指纹与本机不符（常见于容器重建、换机） | 确认在目标机器上操作，重新取指纹后再激活 |
| 409 | `need_deactivate` | 设备席位已被占用（免费许可证限 1 台） | 先解绑既有设备，再重新激活 |

**前端的激活软门。** 未激活时访问控制台会被引导到 `/bootstrap`；这是软门——状态取不到时（接口异常或超时）直接放行，不阻塞使用，结果缓存 15 秒。自助部署若不想被跳转，可在浏览器控制台设置 `localStorage.llmgw_require_bootstrap = '0'`；激活成功后前端会自行写入 `llmgw_activated = '1'`。容器内免激活场景可设环境变量 `LLM_GATEWAY_LICENSE_ACTIVATED=1`，此时状态接口直接返回已激活。

**验证激活结果。**

```bash
curl -s http://127.0.0.1:8781/api/system/bootstrap/status
```

期望 `"activated":true`，且 `message` 以「已激活」开头。激活完成后接着做三件事：① 按 7.6 节初始化管理员并登录；② **做一次数据重置**（第 12 章）——首启向导的第 5 步就是「重置数据」，**默认要重置**：向导把这一步摆在流程里，面板默认已全选全部类别，先预览看清将被删除的行数再确认执行；全新环境可在向导里跳过这一步；③ 按第 4、5 章配置供应商与凭据，再开始正式接入流量。

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
2. 选择模式：**目录模式**（从下拉框选择目录编码，留空显示名时使用目录默认名）或**自定义模式**（勾选「自定义供应商（不在目录中）」，自行填写代码、名称、协议与 Base URL）。
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

> 配额口径提醒：真正卡住路由的配额闸是凭据上的**配额状态一列**（`periodic_exhausted` / `balance_exhausted` / `permanently_exhausted`），由上游的额度与余额响应自动写入；库里另有配额配置表，但当前版本**没有任何代码读它**，往那张表里配窗口不会产生效果。该列也没有枚举约束，写入的值不受数据库校验。

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

**播种出来的账号带「必须改密」标记**，于是第一次登录会撞上一道闸：

```bash
# ① 登录：拿到 access_token 与 llmgw_session Cookie
curl -s -X POST http://127.0.0.1:8781/api/auth/token \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<你的密码>"}'

# ② 改密前，别的管理接口一律 403（只有 /api/auth/me、/api/auth/change-password、/api/auth/logout 放行）
#    响应体：{"error":{"detail":"password change required before accessing other APIs"}}
curl -s -X PUT http://127.0.0.1:8781/api/auth/change-password \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"old_password":"<旧密码>","new_password":"<新密码，至少 8 位>"}'

# ③ 改密后旧 token 立刻失效（401 session revoked: password changed），重新登录拿新 token
```

> **改密后要等 1 秒再重新登录。** 改密会把该用户的「吊销纪元」推进到当前时刻，而 JWT 的 `iat` 只精确到**秒**、纪元是**微秒** ⇒ 同一秒内重新登录，新签的 token 会被判成「早于吊销纪元」，于是又撞上 401 `session revoked: password changed`。**这不是账号坏了，是时间戳精度差**；等一秒再登录即可恢复。

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

**用接口改限流时，三个旋钮要一次给全。** `PATCH /api/keys/<密钥ID>/limits` 是「整组覆盖」语义，缺哪个就报 400：

```bash
curl -s -X PATCH "http://127.0.0.1:8781/api/keys/<密钥ID>/limits" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"rate_limit_rpm":1,"rate_limit_concurrent":8,"rate_limit_tpm":0}'
```

- 缺 `rate_limit_concurrent` ⇒ 400 `rate_limit_concurrent is required`
- 缺 `rate_limit_tpm` ⇒ 400 `rate_limit_tpm is required`
- `0` 表示不限（每分钟 Token 数传 0 即「不执法」），不是「禁止请求」。

改完立即生效，不需要等缓存过期。

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

## 10. 统计与分析

系统里「数字」分布在多个页面：看板讲「现在怎么样」，会话分析讲「谁在用、怎么用」，租户看板讲「这个租户花了多少」。本章把所有统计入口收拢成一张地图，并解释每个数字的口径——**同一件事在不同页面数字不同，多数不是故障，而是口径不同**。遇到跨页面数字对不上时，先翻 10.3 的差异对照表。

### 10.1 统计入口总览

| 页面 | 入口 | 回答的问题 | 时间口径 |
| --- | --- | --- | --- |
| 总览看板 | 「仪表盘」页（`/dashboard`）的「看板」页签 | 系统现在能不能用、今天跑了多少、下一步该做什么 | 默认今日；可选近 7 日 / 近 30 日 / 自定义区间 |
| 会话与统计 | 同页「统计」页签 | 会话怎么增长、成本怎么分布、谁用得最多 | 最近 7 天（默认）或 30 天 |
| 实时流 | 同页「实时流」页签 | 此刻正在发生什么请求 | 实时 |
| 系统自检 | 同页「自检」页签 | 后台任务、探活、依赖健康 | 实时状态 |
| 会话分析中心 | 「会话分析中心」页面（`/admin/session-analytics`） | 用户 / 客户端 / 任务 / 项目四维的长期画像 | 按查询窗口 |
| 租户看板 | 租户管理员登录「仪表盘」页（自动切换） | 本租户的消耗、成功率、活跃模型 | 按查询窗口 |
| 租户用量页 | 「我的用量」页（`/tenant/usage`） | 本租户账单口径的积分与模型明细 | 按账期 |
| PI 错误分析 | 「PI 错误分析」页面（`/admin/pi-analysis`） | 错误的分析状态、误报率、修正动作 | 全量域（不按时间过滤） |
| 探测健康 | 「凭据监控」页的「探测健康」页签 | 上游凭据探活的可用 / 告警 / 不可达分布 | 探活窗口 |
| 对账报表 | 「对账报表」页面（`/admin/reconciliation`） | 网关记账与上游账单是否逐笔一致 | 按对账周期 |

非专业用户在简易模式下打开「仪表盘」，看到的是同一套数字的**简化投影**：今日请求、花费、近 7 日趋势、系统状态与快捷操作——口径与专家面完全相同，只是少了几张图。

### 10.2 看板与统计页签的数字口径

**看板（board）页签的英雄区**四张卡，全部来自同一个汇总接口（时间范围跟页面选择器走）：

| 数字 | 口径 |
| --- | --- |
| 总请求数 | 时间窗口内的全部请求（含成功与失败）；开启「含探测」后为供应商对账口径，与默认的业务口径**不同源**——徽标会明确提示 |
| 成功率 | 成功请求数 ÷ 总请求数（窗口内） |
| 失败请求 | 窗口内 `status != success` 的笔数 |
| 总 Token | 输入 + 输出 Token 之和；卡面副行分别给出输入与输出 |
| 总花费 | 窗口内成本金额合计（USD） |
| 总积分 | 窗口内计费积分合计；算不出来时显示「—」而不是 0（「没算出来」和「消耗为 0」是两回事） |

**看板页签的其余栏目**：近 7 日趋势日柱按 UTC 日聚合（窗口本身按系统时区切日；对账月窗为显式 UTC 口径）；「接入清单」里密钥的请求数与花费是**累计口径**（该密钥签发以来的全部，没有「当日」口径）；「上游健康」是凭据探活的即时分布；「最常用的模型」按所选窗口的请求数排序。

**统计（stats）页签**的核心指标（会话视角，默认最近 7 天，可选 30 天）：总会话数、活跃会话、总成本（带环比）、合规率、平均健康分、平均延迟、总请求数；次级指标有 24h 新增 / 关闭会话、平均每会话成本、违规数。成本明细拆输入成本、输出成本、平均每请求成本与最高成本会话；合规明细区分合规 / 警告 / 违规三档，并单独给出提示注入与 PII 的检测次数。排名列表覆盖三类：Top 客户端（按请求数）、Top 任务（按执行次数）、模型使用（调用次数、成本、Token）。

**术语速查**：「会话」是同一次业务交互的请求链（按会话键聚合多轮请求）；「轮次」是会话里的一次请求-响应；「窗口」是页面上选的时间范围。看板讲请求与钱，统计页签讲会话与质量，两边的「请求数」都是请求粒度、可以互相对拍，但**时间范围不同时数字必然不同**。

### 10.3 跨页面数字为什么可能不一样

| 现象 | 原因 | 是否故障 |
| --- | --- | --- |
| 看板今日请求 ≠ 统计页签的请求总数 | 时间口径不同：看板默认今日（系统时区统计日），统计页签默认最近 7 天 | 否，设计如此 |
| 英雄区数字 > 下方趋势各桶之和 | 开了「含探测」：英雄区变成供应商对账口径（含探测流量），趋势图仍是业务口径；徽标有提示 | 否，徽标已明示 |
| 接入清单的密钥花费 ≠ 看板今日花费 | 密钥清单是**累计口径**，看板是**窗口口径**；且没有按密钥的当日统计 | 否，已知限制 |
| PI 分析统计卡与列表某一瞬间不一致 | 统计卡三段查询非同一事务，并发写入时可能瞬间错位 | 否，管理面可容忍 |
| PI 分析「误报率」与「已分析」对不上 | 分母不同源：误报率分母是**结果域**（已有分析结果的），「已分析」是**队列状态域** | 否，有意设计，结果域才是正确分母 |
| 简易总览与专家看板数字有微小出入 | 页面渲染与取数之间隔了几秒，活流量在涨；历史日严格相等，今日口径允许少量正向漂移 | 否，取数时刻差 |
| 对账报表与看板总花费不一致 | 对账按快照时点冻结，看板实时求和；快照后新流量不回填旧快照 | 否，快照语义 |
| 趋势图日期边界与本地日历差几小时 | 趋势日柱按 **UTC 日**聚合，而窗口按系统时区切日（默认 Asia/Shanghai）——两者边界可能相差数小时 | 部分，已知独立口径 |

**对拍纪律**：跨页面核对数字时，先让两个页面**时间范围一致**（都用今日或都选同一区间）、**探测开关一致**（都含或都不含探测），再看数字。仍不一致且差额明显超出「几笔」量级时，按第 11 章报障，附上两边的截图与时间戳。

### 10.4 模型维度统计在哪看

按模型拆解的统计目前分布在五处，按使用场景选择：

| 场景 | 入口 | 展示内容 |
| --- | --- | --- |
| 看哪个模型最热 | 仪表盘「看板」页签的「最常用的模型」卡 | 按窗口请求数排序：模型名、请求数、花费 |
| 看模型的调用与成本明细 | 仪表盘「统计」页签的「模型使用」排名 | 调用次数、成本、Token 用量 |
| 看某用户用了哪些模型 | 会话分析中心 → 用户详情页的模型用量表 | 该用户按模型的请求、Token 与花费分布 |
| 看租户的模型结构 | 租户看板的「模型请求排行」柱状图与模型明细表 | 按模型的请求数（点击柱条下钻明细）、Token、积分、花费 |
| 看账单口径的模型用量 | 「我的用量」页（`/tenant/usage`） | 按账期的模型行：请求、Token、积分、费用（`by_model`） |

模型行数据同源（都来自请求流水按模型聚合），口径差异同样遵循 10.3 的时间范围规则。独立的「模型分析」专项页面目前未提供——如需更细的模型维度下钻（趋势、错误率、延迟分布），可在会话分析中心按客户端 / 任务视角间接定位后，结合请求日志的模型筛选完成。

### 10.5 会话分析中心四大家族

「会话分析中心」（`/admin/session-analytics`）把长期画像拆成四个视角，入口在左侧导航：

- **用户（users）**：用户列表页（`/admin/session-analytics/users`）给每个归属用户汇总请求数、Token、花费与健康分；点进详情（`/admin/session-analytics/users/:owner`）可见该用户的会话列表、模型用量表与用量矩阵（用户 × 模型的交叉网格）。
- **客户端（clients）**：按客户端标识聚合的画像（`/admin/session-analytics/clients/:id`），回答「这个接入方用得健康吗」：请求数、错误率、延迟、常用模型。
- **任务（tasks）**：按任务标识聚合（`/admin/session-analytics/tasks/:id`），回答「这个自动化任务跑得怎么样」：执行次数、成功率、Token 与成本走势。
- **项目（projects）**：按项目维度归组会话，用于把散落的会话挂到业务项目下对账。

排行卡（会话 / 请求 / 成本 / 模型）在总览页直接给出 Top 名单；四个视角的数据全部来自同一张请求流水按不同维度聚合，**不新造口径**——同一会话在用户视角与客户端视角的请求数必然一致，不一致才需要报障。

### 10.6 对账报表与数据可信度

「对账报表」（`/admin/reconciliation`，超级管理员）回答「账对不对」：网关侧记账（请求流水聚合）与上游供应商账单**双视角对照**，按快照周期逐日核对请求数、Token 与金额，差异行会标出方向与幅度，支持 Excel 导出。看板「总花费」与对账快照的总费用在**同一快照时点**逐字一致（快照存精确分值）；快照之后的新流量只影响看板，不回填快照——这是「对账冻结」的语义，不是数字漂移。

统计数据的三层保障：写入层按请求落流水（业务流量与探测流量分面标记）；聚合层由后台任务定时刷新汇总视图；读取层各端点按口径取数。探测流量默认不计入业务口径——这是「成功率 96%」这类数字可信的前提。

### 10.7 统计术语总表（供查阅）

本表收拢全部统计页面会用到的术语，按「数字、时间、状态、口径」四组排列。每个术语给**页面上的叫法**与一句话定义；口径细节回到 10.2 / 10.3 对照。

**数字类**

| 术语 | 定义 |
| --- | --- |
| 请求数 | 时间窗口内的全部请求笔数（含成功与失败）；探测流量默认不含，开「含探测」后为对账口径 |
| 成功数 / 成功率 | HTTP 200 且模型完整返回的笔数；成功率 = 成功数 ÷ 请求数（防除零：请求数为 0 时显示 0% 而不是报错） |
| 失败请求 | 窗口内未成功返回的笔数 = 请求数 − 成功数 |
| 输入 / 输出 Token | 输入 Token = 发给模型的提示内容（含系统提示）；输出 Token = 模型生成的内容 |
| 缓存读 / 写 Token | 缓存读 = 命中供应商侧缓存的 Token（计费更低）；缓存写 = 本次写入缓存的 Token |
| 总 Token | 四类 Token 之和（输入 + 输出 + 缓存读 + 缓存写） |
| 内部成本（美元） | 网关按上游价目折算的美元成本，**单位是 USD** |
| 外部成本（积分） | 计费体系里的积分消耗，**单位是积分**；与美元是两种货币，**任何时候都不相加** |
| 总花费 | 某一列成本金额的合计；页面若同时出现「美元」与「积分」两列，合计只对同单位成立 |
| 花费（跨单位警示） | 会话分析排行页的「花费」列是两种单位混合的排序键，**只可用于相对比较（谁多谁少），不可当金额读**；页面顶部有说明 |
| 平均每会话成本 / 平均每请求成本 | 成本合计 ÷ 会话数（或请求数）；受窗口口径影响 |
| 环比 | 与上一个等长窗口比较的百分比变化；正数表示增长 |
| 平均延迟 | 窗口内各请求耗时的平均值；看板另有 P95（95% 的请求快于该值） |
| 健康分 | 会话质量得分（后台按错误率、延迟、模型切换、合规问题扣分）：A ≥ 90、B ≥ 75、C ≥ 60、D ≥ 40、F < 40；未评分的会话单独存在，不计入平均值分母 |
| 合规率 | 合规会话占比；明细分合规 / 警告 / 违规三档，另单列提示注入与 PII 检测次数 |
| 误报率 | PI 分析里「判定为攻击但实为误报」的比例；分母是**已有分析结果**的条目（结果域），不是队列条目 |
| 未落定 | 某个维度（供应商 / 凭据 / 密钥）的值为空——通常是探针找不到候选、限流或超时的失败流量；对账筛选里显示为「未落定」桶 |
| 去重会话数 | 按「会话键」去重后的会话行数；同一会话多轮请求只算 1 |

**时间类**

| 术语 | 定义 |
| --- | --- |
| 窗口 | 页面上选的时间范围；所有卡片的数字都只统计窗口内的流量 |
| 统计日 | 统计窗口按系统时区 0 点切日（系统时区可配置，默认 Asia/Shanghai，不是查看者本地时区的自然日）；趋势日柱当前按 UTC 日聚合，边界与统计日可能相差数小时，属已知独立口径 |
| 今日 | 当前统计日（系统时区 0 点起到此刻）；看板默认口径 |
| 近 7 日 / 近 30 日 | 含今天在内的最近 N 个统计日 |
| 实时 | 秒级刷新的即时数据（实时流、系统状态、探活分布） |
| 累计口径 | 该对象**自创建以来**的全部合计（如接入清单里密钥的请求数与花费）；与窗口口径不可直接比较 |
| 探活窗口 | 凭据健康探测的统计时段；探测健康页签的分布反映的是窗口内探活结果 |

**状态类**

| 术语 | 定义 |
| --- | --- |
| 在途 | 请求已发出、尚未返回终态；实时流页的「在途 N」是此刻正在处理的请求 |
| 活跃会话 | 最近 24h 内仍有请求的会话（当前状态快照，不是「24h 内关闭」的反义） |
| 静默会话 | 最近 24h 内没有请求的会话；与「活跃」恰好二分窗口内全部会话 |
| 探测流量 / 业务流量 | 探测流量 = 网关为验证凭据健康发出的合成请求；业务流量 = 真实用户的请求。业务口径默认排除探测——这是成功率可信的前提 |
| 未评分 | 健康分后台任务尚未算到的会话；健康分布图里单独一格，不被静默归入任何档 |
| 陈旧 / 刷新中 | 会话分析中心的数据来自定时刷新的聚合视图；刷新超时（约 90 分钟预算）时页面显示「聚合数据正在刷新」横幅——这是**数据在补算**，不是故障，不需要重试 |
| 降级 | 页面某数据源不可用时以更窄口径展示并明确标注（如「业务轮次 83,617 · 会话累计 73,935」双口径并示）——数字仍是真数，只是覆盖面不同 |

**口径类**

| 术语 | 定义 |
| --- | --- |
| 单一真源 | 每个指标只有一个权威来源（请求流水）；所有页面从同一来源按各自维度聚合，同一会话在不同视角的请求数必然一致 |
| 快照 / 对账冻结 | 对账报表按快照时点冻结数字；快照之后的新流量只影响实时页面，**不回填快照**——这是「对账」语义，不是数字漂移 |
| 含探测 / 不含探测 | 看板英雄区的口径开关；切换后数字来源不同（对账口径 vs 业务口径），徽标会明确提示——跨页对拍时两边开关必须一致 |
| 供应商对账口径 | 与上游账单核对用的口径（含探测流量）；只在对账与开启「含探测」时出现 |
| 队列口径 vs 结果口径 | PI 分析中「待分析 / 已分析」数的是队列条目（状态域），「误报率」的分母是已有分析结果（结果域）——两者不同源是设计使然 |
| 会话入队制 / 发生制 | 会话类统计按「会话首次请求时间」入窗口（队列制）；看板按「请求发生时间」入窗口（发生制）。跨这两类页面对拍时必须先统一时间范围 |
| 同源对拍 | 验证「页面数字对不对」的方法论：页面显示值必须等于**页面自己收到的那份接口响应**，而不是另发一次请求（活流量下必然漂移） |

**对拍提醒**：本表所有术语与各页面顶部 / 抽屉里的口径说明同源。发现某个页面用词与本表冲突时，以页面实际口径说明为准并报障——**文案必须跟后端口径对表**，术语表只是它的索引。

## 11. 常见故障处理

请求失败与「某些模型调不通」是值班最高频的两类问题。本章按 **先分层 → 再看归因 → 最后动手修** 组织：11.2 是症状速查表（先从这里定位），11.4 处理「请求失败」，11.5 处理「模型不可访问」，其余小节是环境、凭据与容量类问题。

### 11.1 先看这四个入口

| 场景 | 入口 | 用途 |
| --- | --- | --- |
| 进程是否存活 | `/healthz` | 快速确认服务在跑（永远 200，不因依赖故障失败） |
| 依赖是否就绪 | `/readyz` | 数据库或 Redis 不通时返回 503（严格门） |
| 跑的是哪个版本 | `/version` | 确认部署版本与构建序号 |
| 请求为什么失败 | 「请求日志」页面 | 按请求 ID 或错误码检索单次请求 |

另外两个带管理令牌的入口，排障时很有用：

| 场景 | 入口 | 说明 |
| --- | --- | --- |
| 内部状态总览 | `/healthz?full=true` | 需要管理令牌；含熔断状态、限流器、数据库与 Redis 延迟 |
| 指标 | `/metrics` | 需要管理令牌；Prometheus 指标 |

命令形式：

```bash
curl -s http://127.0.0.1:8781/healthz
curl -s http://127.0.0.1:8781/readyz
curl -s http://127.0.0.1:8781/version

# 带管理令牌的两条
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" 'http://127.0.0.1:8781/healthz?full=true'
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" http://127.0.0.1:8781/metrics
```

> **`/healthz/full` 不是全量入口。** 它只是鉴权豁免名单里的一个路径，handler 不认它 ⇒ 返回的是与 `/healthz` 一模一样的简版（实测两者同为 88 字节）。全量信息走**查询参数** `?full=true`（实测 1058 字节，含 `circuit` / `concurrency` / `database` / `redis`）。排障要内部状态时务必带 `?full=true`，写成 `/healthz/full` 会以为拿到了全量、其实什么都没多。

> `/readyz` **只测数据库与 Redis 连通，不测候选查询**。历史上出现过「readyz 绿、公网 200，但模型请求全失败」的部署带着绿灯上线。**验收必须包含一次真实模型请求。**

### 11.2 症状速查：现象 → 首查项 → 修正动作

| 现象 | 首查项 | 修正动作 |
| --- | --- | --- |
| 全部请求 503 `no_candidate` | 网关日志里的 `[candidate_diag] db_empty`（含 `likely_cause` 人话结论） | 按 11.5 的根因表逐项处理 |
| 某个模型 400 `invalid_model` | `/v1/models?scope=all` 里有没有这个名字 | 名字拼错或用了上游原名 → 加别名或改用目录里的名字 |
| 某个模型 400 `modality_mismatch` | 该模型的登记模态 | 改登记模态（11.5） |
| 某个模型 503 `model_not_found` | 错误信封的 `kind` / 响应头 `X-Gateway-Last-Kind` | 按 `kind` 分流（11.3、11.4） |
| 401 `missing_key` | 请求是否带了 `Authorization: Bearer <key>` | 补上请求头 |
| 401 `authentication_required` | 「API Key」页 `/keys` 的密钥状态 | 换密钥；核对租户状态 |
| 402 `budget_exceeded` | 该 API Key / 租户的预算 | 管理员加预算（注意 `type` 才是 `insufficient_quota`） |
| 429 `rate_limit_exceeded` | 该 API Key 的 RPM / TPM / 并发设置 | 降客户端并发，或提额；按响应头 `Retry-After` 退避 |
| 429 `key_throttled` | 该密钥是否因异常用量被封禁 | 联系管理员解封 |
| 403 `output_policy_blocked` | 命中的是敏感内容、私网地址还是工具参数 | 调整请求内容或出站策略，**不是路由问题** |
| 499 `routing_client_canceled` | 客户端超时设置 | 调客户端超时；不是上游故障 |
| 500 `routing_schema_error` | 网关版本与迁移状态 | 升级到含修复的版本（网关自身 SQL 缺陷） |
| 503 `routing_circuit_open` | `/api/routing/health` 的熔断凭据 | 先修根因（通常是 SQL 或库），熔断会自愈 |
| 某模型偶尔失败、成功率低 | `/api/candidate-failures/stats` 与候选失败日志 | 按 (凭据, 模型) 对定位，必要时下线该对 |
| 列表里有、却调不通 | `/api/routing/resolve?model=<名>` | 看每个候选的拒绝原因（11.5） |
| 列表里没有、但别人能调 | 列表只出 canonical 名且要求 `provider_models.canonical_id` 非空 | 先用 `?scope=all` 看全量；仍没有就查该模型的 `canonical_id` 是否为空（11.5） |

### 11.3 错误码与真实原因

| `code` | 状态码 | `type` | 含义 | 处置 |
| --- | --- | --- | --- | --- |
| `missing_key` | 401 | `authentication_error` | 请求没带 `Authorization`，或头格式不对 | 补上 `Authorization: Bearer <key>` |
| `authentication_required` | 401 | `invalid_request_error` | 头带了但密钥无效 / 已吊销 / 租户被停 | 换密钥；核对租户状态 |
| `budget_exceeded` | 402 | `insufficient_quota` | 该 API Key / 租户预算耗尽 | 管理员补充预算或充值 |
| `rate_limit_exceeded` | 429 | `rate_limit_error` | **网关侧**限额：RPM / TPM / 并发任一超限 | 降客户端并发，或提额；按响应头 `Retry-After` 退避 |
| `key_throttled` | 429 | `rate_limit_error` | 密钥因异常用量被封禁 | 联系管理员解封 |
| `rate_limit_exhausted` | 429 | `rate_limit_error` | 上游限额已耗尽（部分协议面如此回传） | 退避重试，或增加上游配额 |
| `invalid_model` | 400 | `invalid_request_error` | 这个模型名在目录里根本没有 | 用 `/v1/models?scope=all` 核对（11.5） |
| `modality_mismatch` | 400 | `invalid_request_error` | 模型不支持本次请求的模态 | 换模型或去掉图片等输入；或改登记模态 |
| `body_too_large` | 400 | `invalid_request_error` | 请求体过大 | 精简请求内容 |
| `output_policy_blocked` | 403 | — | 命中输出策略 | 按响应中的规则字段调整内容 |
| `no_candidate` | 503 | `server_error` | 模型存在，但当下没有可路由的候选 | 按 11.5 查凭据/绑定/配额/探针 |
| `model_not_found` | 503 | `server_error` | 有候选但全部尝试失败 | 读 `kind` 或 `X-Gateway-Last-Kind` 判断真实原因 |
| `routing_client_canceled` | 499 | — | 客户端提前断开 | 调整客户端超时，不是上游故障 |
| `routing_circuit_open` | 503 | — | 候选查询熔断已打开 | 稍后重试；持续出现则排查数据库或网关 SQL |
| `routing_connection_error` | 503 | — | 网关到数据库或上游连接失败 | 检查数据库可达性与连接池 |
| `routing_schema_error` | 500 | — | 网关自身的 SQL 缺陷 | 核对迁移版本，属于网关缺陷 |
| `routing_database_error` | 500 | — | 其他数据库错误 | 读日志原文定位 |

**三套词表要分清。**

1. **`code`** —— 网关给的机器可读码，排障时按它查上表。
2. **`type`** —— OpenAI / Anthropic 兼容的**类型名**（`invalid_request_error`、`authentication_error`、`rate_limit_error`、`insufficient_quota`、`server_error`）。它比 `code` 粗，**不要拿 `type` 当错误码查**：`authentication_error` 是 `type`，对应的 `code` 是 `missing_key` 或 `authentication_required`；`insufficient_quota` 也是 `type`，对应的 `code` 是 `budget_exceeded`。
3. **`kind`** —— 上游侧失败归因（`transient`、`timeout`、`auth`、`quota`、`model_unavailable`……），粒度最细，是定位真因的主字段。另有 `routing_*` 开头的**路由侧**码，是网关自己出问题时用的。

`model_not_found` 这个名称容易误导：它表示所有候选都失败，真实原因在 `kind` 里，例如上游过载、并发冲突或配额不足。

### 11.4 请求失败：分层排查

**第 0 步 · 分清是哪一层失败的。** 状态码先给出粗判：

| 状态码 | 归属 | 首查 |
| --- | --- | --- |
| 400 `invalid_model` / `modality_mismatch` | 客户端请求层（模型名或模态） | 11.5 节 |
| 401 `missing_key` / `authentication_required` | 认证层 | 「API Key」页 `/keys` |
| 402 `budget_exceeded` | 租户预算层（**不是**上游配额） | 租户额度页 |
| 403 `output_policy_blocked` | 出站策略层 | 敏感内容 / 私网地址 / 工具参数，不是路由问题 |
| 429 `rate_limit_exceeded` / `key_throttled` | 限流层 | 该 API Key 的 RPM、TPM、并发设置，或上游透传限流 |
| 499 `routing_client_canceled` | 客户端自己断开 | 调客户端超时，**不是**上游故障 |
| 500 `routing_schema_error` / `routing_database_error` | 网关自身（SQL 缺陷 / 数据库） | 本节第 3 步 |
| 502 / 503 / 504 | 上游或路由层 | 本节第 2 步 |
| 503 `no_candidate` / `model_not_found` | 路由层 | 11.5 节 |

**第 1 步 · 拿到这次请求的三样东西。**

1. **request_id**：每个协议面的响应都带 `X-Request-Id` 头，它贯穿所有诊断表，是跨表追踪的主键。
2. **错误信封**：错误响应体形如 `{"error": {"code", "type", "message", "request_id", "kind"?, "gateway_debug"?}}`。`code` 是机器码（查 11.3 的表），`type` 是兼容类型名；**真实原因在 `kind`**——但 `kind` 与 `gateway_debug` **只在特定层出现，不是每个错误都有**。
3. **落库归因**：`request_logs_with_current_month` 视图里的 `error_kind`（失败归因主码）、`failure_detail_code`（失败细分码）、`failure_stage`（卡在哪一步）。实测取值样例：`invalid_model` / `invalid_model` / `upstream`、`no_candidate` / `gw_no_candidate` / `gateway`、`transient` / `transient` / `upstream`。

**错误信封各层的字段对照**（2026-11-17 真实网关实测）：

| 场景 | `code` | `kind` | `gateway_debug` |
| --- | --- | --- | --- |
| 缺 `Authorization` 头 | `missing_key` | 无 | 无 |
| 密钥无效 | `authentication_required` | 有 | 有（`stage=ingress`） |
| 模型名未知 | `invalid_model` | 无 | 无 |
| 零候选 | `no_candidate` | 有 | 无（没试过任何候选，无从调试） |
| 全候选失败 | `model_not_found` | 有 | 有（`stage=execution`、`tried=候选数`、`retryable`） |
| 网关侧限流 | `rate_limit_exceeded` | 无 | 无 |

⇒ **看响应体里没有 `kind` 就别硬找**：这类错误的判据是 `code` 加状态码（限流另看响应头 `Retry-After`）。反过来，**出现了 `gateway_debug` 就说明网关真的尝试过**，里面的 `tried` 是试了几个候选、`stage` 是卡在哪一步（`ingress` 入口 / `execution` 执行）。响应头 `X-Gateway-Last-Kind` 与 `kind` 同源，只在执行层失败时出现。

> `gateway_debug` 是**错误响应体里的字段**，不是响应头——别在响应头里找它。

> **请求级和候选级的 `error_kind` 不是同一个词表**，对不上是正常的：同一批上游 500，响应信封的 `kind` 是 `transient`，而候选失败表里的 `error_kind` 是 `upstream_down`。**信封看整体归因，候选表看这一次尝试的具体原因**，两个都要看，别因为不一致就以为查错了表。

```sql
-- 最近一小时失败的三维分布：主码 × 细分码 × 阶段
SELECT error_kind, failure_detail_code, failure_stage,
       count(*)                       AS failures,
       count(DISTINCT credential_id)  AS credentials,
       count(DISTINCT outbound_model) AS models,
       min(ts) AS first_seen, max(ts) AS last_seen
FROM request_logs_with_current_month
WHERE ts > now() - interval '1 hour'
  AND COALESCE(success, FALSE) = FALSE
GROUP BY 1, 2, 3
ORDER BY failures DESC;
```

> 读面说明：`request_logs_with_current_month` 是**会话视图**（热表与历史表两臂），不是旧的 `request_logs` 表；`success` 可能为 NULL，判失败要用 `COALESCE(success, FALSE) = FALSE`。

**第 2 步 · 按 kind 分流。** `kind` 是网关内部的失败归因词表，值班时先看它属哪一类：

| kind | 含义 | 该做什么 |
| --- | --- | --- |
| `transient` / `network` / `upstream_down` / `timeout` / `stream_timeout` | 上游或网络抖动 | 客户端可退避重试；连续出现则查该凭据 |
| `upstream_overloaded` / `concurrent` | 上游过载或并发冲突 | 降并发或换候选 |
| `rate_limit` / `rate_limit_observed` | 节点被限流 / 上游用 4xx 表达了限流 | 前者退避约 30 秒；后者换兄弟候选，**不要**因此惩罚节点 |
| `auth` / `auth_revoked` | 上游密钥失效或被撤销 | 轮换密钥；`auth_revoked` 是终态，不再跨凭据转移 |
| `quota` / `quota_periodic` / `quota_balance` / `quota_permanent` | 上游额度或余额耗尽 | 充值或等恢复窗口；`quota_permanent` 是终态 |
| `model_unavailable` | 上游确定不提供这个模型，或端点路径配错 | 绑定级处理：下线这一对 (凭据, 模型) |
| `model_not_found` / `model_deprecated` | 模型本身不可用 | 终态，改模型 |
| `request_rejected` | 上游用 4xx 拒收请求且无更细归因 | 多半是客户端参数问题；**不要**当成节点不健康 |
| `context_length_exceeded` | 超上下文窗口 | 网关会尝试裁剪重试一次；仍失败则精简上下文 |
| `content_filter` / `conversion_error` / `upstream_context_loss` / `tool_call_id_mismatch` / `client_bug` | 内容策略、协议转换、上下文丢失、客户端 bug | 终态，逐个按语义排查 |

**第 3 步 · 交叉验证再动手。** 候选级的失败原因比请求级更细：每次尝试一行、带上游响应体。

```sql
-- 某模型的逐次候选失败：谁在试、第几次、上游回了什么
SELECT ts, request_id, credential_id, provider_id, attempt_index,
       error_kind, upstream_status_code, retryable, per_attempt_latency_ms,
       left(coalesce(upstream_response_preview, error_message, ''), 300) AS err_preview
FROM candidate_failure_logs_with_current_month
WHERE ts > now() - interval '24 hours'
  AND raw_model_name = 'glm-5.2'      -- ← 换成实际模型名
ORDER BY ts DESC
LIMIT 100;
```

> 两个坑：父表 `candidate_failure_logs` **不含最近 8 小时**（热表 promote 的保留窗口是 8 小时），查「刚刚」必须用 `candidate_failure_logs_with_current_month` 或热表；跨表 join 时 `routing_decision_log.request_id` 是 uuid、其余表是 text，需要 `::text` 转换。

凭据本身通不通，看探针的**双通道**结果（直连上游 vs 经网关）：

```sql
SELECT started_at, raw_model_name, trigger_kind,
       direct_ok, direct_http_status, direct_err_code, direct_latency_ms,
       gateway_ok, gateway_http_status, gateway_err_code, gateway_latency_ms,
       success, duration_ms
FROM node_probe_runs
WHERE credential_id = 7                -- ← 换成实际凭据 ID
ORDER BY started_at DESC
LIMIT 20;
```

`direct_ok=true` 但 `gateway_ok=false` ⇒ **凭据没问题，是网关侧路径坏了**（鉴权、协议转换或路由）；两路都 false ⇒ 上游或凭据问题。

**现成的诊断端点**（都在 `/api` 下，多数要管理员鉴权）：

| 端点 | 回答什么 |
| --- | --- |
| `GET /api/routing/health` | 哪些凭据熔断中（`circuit_state`、`consecutive_failures`、`cooling_until`） |
| `GET /api/candidate-failures/stats` | 按 (模型, kind) 聚合的候选失败 |
| `GET /api/admin/diagnostics/routing-blocked?provider_id=<供应商ID>` | 「凭据正常但路由不到」的专项诊断；**必须带 `provider_id`**，缺失或不合法返 400 `missing or invalid provider_id query parameter` |
| `GET /api/admin/probe/dashboard` | 探针健康看板，含真实请求 24 小时成功率 |
| `GET /api/routing/resolve?model=<模型名>` | 这个模型的候选选择过程与每个候选被拒的原因 |
| `GET /api/logs/top-problems` | 失败热点榜：凭据维度与模型维度各取失败最多的前 N 个 |

**`/api/logs/top-problems` 的用法。** 这个端点此前**没有注册路由**，请求会落到 `/api/logs/{id}` 通配分支返 404 `request log not found`；现已注册，实测返 200。

| 参数 | 含义 | 默认 |
| --- | --- | --- |
| `from` | 起始时间（RFC3339 或 `YYYY-MM-DD HH:MM:SS`） | 24 小时前 |
| `to` | 结束时间（同上；必须晚于 `from`，否则 400 `to must be after from`） | 现在 |
| `limit` | 每个维度取前 N 个（1..200，超范围自动夹取） | 20 |
| `kind` | 只看某一维度：`credential` 或 `model` | 两个都看 |

返回体形如 `{"items": [...], "meta": {"from", "to", "limit", "count"}}`；每个 item 含 `kind`（`credential`/`model`）、`id`、`label`、`request_count`、`failure_count`、`failure_rate`、`top_failure_kind`、`top_failure_detail`。排序按失败数降序，再按失败率、再按请求数。某个维度查询失败时，该维度被跳过，响应里附 `errors` 数组，其余维度照常返回。

★ **两个读数陷阱**：

1. **样本下限 30**：`HAVING COUNT(*) >= 30` ⇒ 请求数不足 30 的模型/凭据**不上榜**（避免失败率没有统计意义）。所以「榜上没它」**不等于**「它没问题」，只等于「样本不够」。
2. **`<unknown>` 不是模型名**：**入口层失败**（401 鉴权失败、429 限流、ingress 容量拒绝）发生在**模型解析之前** ⇒ 日志里 `client_model` 就是字面量 `<unknown>` ⇒ 模型维度会把这一整桶并成一行。实测：40 次坏 key 请求产生的 48 笔 `authentication_required` 全被并进 `<unknown>`。看到 `<unknown>` 占据榜首，先查**鉴权/限流/入口容量**，不是模型路由问题。

想看更细的失败分布，仍用这两条口径：

```bash
# ① 按 (模型, kind) 聚合的候选失败
curl -s -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8781/api/candidate-failures/stats

# ② 一小时失败的主码 × 细分码 × 阶段分布
psql -c "SELECT error_kind, failure_detail_code, failure_stage, count(*) FROM request_logs_with_current_month WHERE ts > now() - interval '1 hour' AND COALESCE(success, FALSE) = FALSE GROUP BY 1,2,3 ORDER BY 4 DESC"
```

**现成的排障命令**（都在仓库的 `cmd/` 下）：

```bash
go run ./cmd/probe-cred -cred 7 -chat glm-5.2            # 用该凭据直连打一发最小请求
go run ./cmd/verify-model-fetch -provider 14 -json        # 复刻「从供应商读模型」这条路径
go run ./cmd/check-credentials -json                      # 扫描凭据密文格式（只读）
go run ./cmd/check-credentials -fix -dry-run=false        # ★ 真写库：光加 -fix 不写
go run ./cmd/routing-test-client -gateway http://127.0.0.1:8781 -api-key $KEY -models glm-5.2 -clients 5
go run ./cmd/auto-testbench -report reports/auto-testbench # AUTO 路由闭环回归
```

**两个容易踩的坑：**

1. **`check-credentials` 的 `-fix` 默认仍是演练。** `-dry-run` 的默认值是 `true`，所以 `-fix` 单独用只会打印计划中的 UPDATE、**不写库**；真要写必须显式 `-fix -dry-run=false`。不确定时先只跑 `-json` 看 `will_rewrite`。
2. **这五个命令都要连库**（走 `config.Load()` 读 `DATABASE_URL` / `LLM_GATEWAY_DATABASE_URL`）。没设环境变量会直接 `db connect failed` 退出（exit 2）。`probe-cred` 还要能解密凭据密文，因此 `LLM_GATEWAY_SECRET_KEY` / `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY` 要与网关进程用的一致。

`check-credentials -json` 的输出里每个凭据一行，含 `format`（密文格式）、`decrypt_ok`、`will_rewrite`（是否会被 `-fix` 重写）、`plaintext_hash`（明文摘要，不含明文）。

只读 SQL 与接口探针脚本（**写死了 211**，换机器要改脚本内常量）：

```bash
scripts/audit/q-211-readonly-sql.sh "SELECT error_kind, count(*) FROM request_logs_with_current_month WHERE ts > now()-interval '1 hour' AND COALESCE(success,FALSE)=FALSE GROUP BY 1"
ssh 211 'cd <repo>/scripts/audit && python3 api-211-admin-probe.py "/api/routing/resolve?model=glm-5.2" --port 8783'
```

**修完必须打一次真实模型请求。** `/readyz` 不测候选查询，绿灯不等于模型可用。

### 11.5 某些模型无法访问：定位与修正

「这个模型调不通」有四种完全不同的根因，修法也完全不同。先按判定链分层，再动手。

**判定链分四层，每层都有各自的判别点。**

| 层 | 在做什么 | 判别点 |
| --- | --- | --- |
| 1 · 名字解析 | 把客户端的模型名解析成 canonical 名与 raw 名集合 | 解析不到 ⇒ 名字在目录里根本没有 |
| 2 · 候选查询 | 10 道闸逐一过滤，任一道不过则这一对 (凭据, 模型) 落选 | 有目录但 0 候选 ⇒ 逐闸查 |
| 3 · 零候选分流 | 0 候选时给出三个不同结论 | **400 与 503 的差别就是这层的产物** |
| 4 · 执行失败 | 有候选但全部尝试失败 | 真实原因在 `kind` |

**第 3 层的三个结论必须分清——这是最常被误读的地方：**

| 客户端收到 | 真实含义 | 该查什么 |
| --- | --- | --- |
| **400 `invalid_model`** | 这个模型名**在目录里根本没有**（既不在供应商模型表、也不在别名表） | 名字拼写；用 `/v1/models?scope=all` 核对 |
| **400 `modality_mismatch`** | 模型在目录里、绑定也在，但登记的模态服务不了本次请求 | 登记模态 |
| **503 `no_candidate`** | 模型**是存在的**，只是当下没有能用的凭据 | **不要去改目录**，去查凭据、绑定、配额、探针 |

第 3 层的判别依据是一个「模型是否已知」的判定：它只查「供应商侧模型表」与「别名表」两处，**故意不查 canonical 目录**——否则首次见到的模型会被自动播种进目录，把拼写错误从 400 变成 503。

**第 2 层的 10 道闸**（任一道不过就落选）：

| 闸 | 看什么 | 典型根因 |
| --- | --- | --- |
| 名字匹配（5 路） | canonical 名、规范化名、别名、上游原名 | 客户端用了上游原名而目录里没有别名 |
| canonical 未禁用 | 目录里该模型的状态 | 模型被下线 |
| 模态接受 | 目录登记的模态 | 登记成 text 却用来发图 |
| 模态判词 | 核实记录里「读」的结论 | 判词为否 ⇒ 该模态不接单 |
| 租户 | 供应商归属的租户 | 模型挂在别的租户下 |
| 凭据状态 | 凭据是否启用 | 被人工停用 |
| 可路由闸（视图） | 供应商、凭据、绑定、探针四层状态 | 配额耗尽、鉴权失败、熔断、探针判负 |
| 路由范围 | 凭据只接文本还是只接媒体 | 用错凭据类型 |
| 探针坏对 | 该 (凭据, 模型) 对是否被探针判死 | 探测失败被确认 |
| 模型生命周期 | 是否过期下线 | 老模型自然退役 |

**根因 → 修正动作**（修正端点都要求超级管理员；部分写操作要带 `reason` 留审计，以页面表单提示为准）：

| 根因 | 修正动作 | 在哪操作 |
| --- | --- | --- |
| 模型未登记（`offers_total=0`） | 加 raw→canonical 别名，或重跑模型发现 | 「模型」页 `/models`（别名接口 `POST /api/models/<canonical_id>/aliases`，体 `{"raw_name":"<上游原名>"}`） |
| 凭据被人工停用 | 清除人工停用标记 | 「凭据监控」页 `/routing-v2/credentials` |
| 凭据生命周期被置为退役或停用 | 恢复生命周期为 active | 供应商详情的凭据页签 |
| 凭据配额态耗尽 | **先充值或解封**；紧急复位走专用端点 | 供应商详情页 |
| 供应商被停用 | 启用供应商 | 供应商详情页 `/providers/:id` |
| 绑定被人工下线 | 绑定级重新上线（只清人工下线原因，不覆盖自动原因） | 凭据监控页 |
| 模型级被下线 | 恢复该模型的可用标记 | 供应商详情页 → 模型页签 |
| 探针判负 | 复位该凭据状态（会清探针结论，**先确认上游确实恢复**） | 凭据监控页 / 探针健康页 |
| 登记模态不对 | 改该模型的登记模态 | 「模型」页 `/models` |
| 候选查询熔断 | **先修根因**（通常是网关侧 SQL 缺陷），熔断会自愈 | — |

> **加别名这一步实测有效**：对一个未登记的名字，加完 raw→canonical 别名后，客户端请求从 **400 `invalid_model`** 变成 **503 `no_candidate`**（即「名字认得了，只是还没有能用的凭据」），正好推进到下一层排查。
>
> **「手工加绑定」这条路此前是坏的，现在已修复**：`POST /api/providers/<供应商ID>/credentials/<凭据ID>/models`（以及 `/api/providers/<ID>/models/` 带 `credential_id`）此前实测返回 **500 `create failed`**，日志里是 `could not determine data type of parameter $8 (SQLSTATE 42P08)`——`modelcatalog/upsert.go` 的 `insertManualCredentialModelSQL` 里 `$8`（`context_window`）出现在 `CASE WHEN $8 IS NOT NULL` 这种**没有类型上下文**的位置，PostgreSQL 在 Parse 阶段就推断不出类型，整条语句失败。已修（显式转型），实测返 **201** 并落出绑定行。仍要注意：**该模型名会被顺带播种进 `models_canonical`**，且写入是原子的（不会留下半条绑定）。⇒ 三条建立绑定的路（加别名 / 手工加绑定 / 重跑模型发现）现在都可用，**优先用加别名**：它只改名字映射，不动凭据与绑定。
>
> ★ 这类缺陷的排查特征值得记住：**只在"从没被成功调用过的路径"上出现**，且错误是 `SQLSTATE 42P08` 这类 **parse 期**错误 ⇒ 单元测试（尤其是 mock 驱动的）**抓不到**，必须让真实 PostgreSQL 去 parse 那条语句才能发现。

> 最省事的一条路：零候选时网关会打一条 `[candidate_diag] db_empty` 日志，其中 `likely_cause` 直接给出人话结论（例如「模型已登记（12 offer），但 12 条被凭据配额态滤掉 ⇒ 处置：处置配额（充值/解封）」）。**先看这一行，再动手。**

**一条常见误解要澄清：** `/v1/models` 列表与路由候选**不是同一套过滤**，两边都成立：

- 列表**只输出 canonical 名**（`models_canonical.canonical_name`），而且要求 `provider_models.canonical_id` **非空并指向该 canonical**。⇒ **模型能调通但列表里没有**的最常见原因就是它的 `canonical_id` 为空（发现流程没做映射）。
- 列表**排除手工加的绑定**（`admin_protected = TRUE`）、排除隔离级（`quarantine`）凭据、排除探针连败的凭据、排除额度耗尽与被人工下线的项；路由侧不查其中一部分。
- 列表**不按请求模态过滤**；路由侧要过模态闸。⇒ 「列表里有」也不等于「一定能调通」。
- 列表口径默认是**运营可配**的平台设置 `models.list_scope`（缺省 `featured`）：查询参数 `?scope=featured|all` 与等价写法 `?all=true|false` 都能显式指定；**未识别的取值返 400**（不会静默回退）。要全量用 `?scope=all`。

> 实测样例：把 `mock-model-fast` 绑到可用凭据上后，它请求返回 **200**，但 `/v1/models` 与 `/v1/models?scope=all` **都仍然是空数组**——因为该模型的 `provider_models.canonical_id` 为空，列表的第一道 JOIN 就把它滤掉了。**所以「列表里没有」不能当成「路由不通」的证据**，反之亦然。

**改完怎么验证：** 打一次真实模型请求；再看 `/api/routing/resolve?model=<模型名>` 里候选是否非空。注意候选查询有内存缓存，**直接改数据库不会刷新缓存**，要走管理端写操作才会失效缓存。

### 11.6 安装与启动问题

**数据库连接失败**：首次启动时数据库需要十几秒完成初始化，等待后重试；持续失败时查看数据库容器日志与连接串配置。

**Redis 连接被拒**：确认 Redis 已启动且地址、密码、库号正确；精简模式不依赖 Redis，可检查 `LLM_GATEWAY_STORAGE_MODE` 是否符合预期。

**端口被占用**：安装器会检查 8781、5432、6379。修改编排文件中的端口映射即可换端口。

**迁移中断**：启动迁移按登记清单顺序执行。排查时注意三件事：

1. **清单真源是代码**：`installer/internal/dbinit/runner.go` 的 `Runner.StartupFiles`；它的镜像在 `sql/schema/installed_startup_migrations.tsv`（该文件由代码生成，逐条比对，不一致时测试会红）。
2. **真正随安装包分发的 SQL 在 `installer/cmd/llm-gw-installer/embeddata/startup/`**。仓库里的 `sql/migrations/startup/` 是**另一份更大的副本**（含 `.down.sql`，文件数是前者的两倍多），**安装器不跑它** ⇒ 报「缺文件」时要去 embeddata 目录里找，别在 `sql/migrations/startup/` 里空找。
3. **部分迁移带前置断言**：例如 `956_minimax_m31_flash_featured.sql` 要求目标模型**已存在于 `models_canonical` 且 `status='active'`**，否则直接中止并报 `migration 956 aborted: canonical_name=... does not exist or is not active`。而模型目录是由**模型发现**灌入的 ⇒ **全新安装、还没配任何供应商时，目录是空的，启动迁移链会在这一条中止**。这类中止不是文件缺失，而是「前置条件没满足」——先把模型目录补上（跑一次模型发现，或手工登记该模型），再重跑迁移。

**凭据解密失败**：加密主密钥已更换或与密文不匹配。恢复原来的 `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY`；若已丢失，需要重新录入上游密钥。

**探针审计写不进库（全新安装）**：症状是开了系统监控（`LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true`）后，探针结果一条都没落进 `system_probe_runs`，日志里是 `column "total_tokens" of relation "system_probe_runs" does not exist`。根因：该列由 `sql/migrations/domain/346_system_probe_run_tokens.sql` 创建，而**安装器只跑 startup 通道**（`sql/migrations/domain/` 是另一条通道，全新安装不会执行它）⇒ 全新安装的库里这一列不存在，而 `bg/systemmonitor/audit.go` 的写入语句带着它。修法：把该列的 `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` 补进 startup 通道的迁移（这是**登记在案、尚未落地**的缺口），或在等迁移落地前手工执行那一条 `ALTER TABLE`（幂等）。已按 domain 通道建过库的环境不受影响。

### 11.7 凭据侧问题

| 现象 | 排查方向 |
| --- | --- |
| 全部请求失败，提示无可路由候选 | 该供应商下是否还有可用凭据；在凭据监控页看可用性状态 |
| 凭据显示鉴权失败 | 上游密钥是否过期或被吊销；在详情中轮换密钥 |
| 凭据显示限流 | 上游账号配额用尽；等待自动恢复或调整配额 |
| 单个模型 404，其余正常 | 该模型的绑定已停用，在供应商详情的模型页签恢复绑定 |
| 成功率偏低但健康状态正常 | 查看凭据路由记录与错误趋势，定位是哪些模型在失败 |

### 11.8 性能与容量

- **吞吐不足**：先确认是否被网关自身的限流拦截（看响应码 429、`code` 是 `rate_limit_exceeded` 还是 `key_throttled`，以及响应头 `Retry-After` 给出的退避秒数），再考虑上游配额。**429 的错误响应体里没有 `gateway_debug`**，别去找。
- **首字节慢**：到「凭据监控」的探测健康页签查看探针延迟，再到「模型与路由」组的路由全景看候选选择过程。
- **长对话被拒**：网关在请求接近上游上下文上限时会触发压缩；若仍失败，检查该模型的上下文窗口与压缩开关。
- **存储膨胀**：在「数据运维」组的「数据生命周期」查看热、温、冷、过期四层数据的分布与清理策略。

### 11.9 报障时需要提供的信息

提交问题时请附上：网关版本（`/version` 输出）、部署形态（容器或裸机）、操作系统与版本、数据库与 Redis 版本、复现步骤、期望结果与实际结果、相关时间段的脱敏日志，以及失败请求的 `request_id`；连同**错误响应体原样**（`code` 必附；`kind` 与 `gateway_debug` 有就一并附上，没有就注明「这一类错误不带这两个字段」——见 11.4 第 1 步的对照表），以及 `request_logs_with_current_month` 里该请求的 `error_kind` / `failure_detail_code` / `failure_stage`——这几样能让对方不必重新复现。

## 12. 数据重置

拿到一台新环境、跑完一轮压测或演示、或者需要把旧环境的运行痕迹清干净时，都要做一次**数据重置**：先看清各类数据有多少，再预览将要删掉什么，最后才执行。这三步在系统里是三个端点，顺序固定，且**执行必须带上预览签发的令牌**。

**首次激活时它是默认步骤。** 首启向导的第 5 步就是「重置数据」，默认要重置，重置完再继续接入流量；全新环境可在向导里跳过。向导里的入口直达本面板的「数据初始化」页签（`/admin/data-lifecycle?tab=data-init`），面板默认已全选全部类别。

**重置是有权限门槛的破坏性操作。** 三个端点全部要求超级管理员（`super_admin` 角色，JWT 通过 `Authorization: Bearer` 或 `llmgw_session` Cookie 携带），租户管理员（`tenant_admin`）无权调用——它能删掉全部用户、租户与凭据。

### 12.1 安全模型：三条不可让步的属性

| 属性 | 做法 | 挡住的误操作 |
| --- | --- | --- |
| **客户端不能指定表名** | 请求体里只有「类别 key」，类别到表清单的映射写死在服务端注册表里 | 不存在「传个表名就能删任意表」的面 |
| **先预览、后执行，执行须带令牌** | 预览返回受影响行数与 `confirm_token`（HMAC 签名，绑定类别集合与删除计划，**5 分钟有效**）；执行必须原样回传 | 执行的一定是「刚刚预览过的那一批类别与同一份计划」，预览与执行之间换了类别或数据变了都会验不过 |
| **受保护行不删** | 每张表可声明保留谓词：系统 key、超级管理员、默认租户等骨架行永远不删 | 一次重置不会把环境变成「谁也登录不了」 |

### 12.2 数据类别与破坏强度

类别分两级：**运行数据（`operational`）**是运行期产生的历史与遥测，整类可清空，清掉不影响系统可用性；**配置数据（`configuration`）**是业务配置，清空时保留骨架行。

| 类别 key | 强度 | 覆盖的表 |
| --- | --- | --- |
| `request_records` | 运行数据 | `session_turn_details`、`session_bodies`、`session_turns`、`sessions`、`request_wal`、`session_summaries`、`request_stats_minute`、`request_stats_dim_minute` |
| `probe_records` | 运行数据 | `node_probe_runs`、`credential_probe_model_log`、`credential_health_checks`、`model_probe_runs`、`system_probe_runs`、`provider_metrics_minute`、`provider_metrics_hour` |
| `routing_records` | 运行数据 | `routing_decision_log`、`route_decisions`、`routing_audit_log`、`route_incidents`、`candidate_failure_logs`、`routing_health_checks` |
| `credentials` | 配置数据 | `credential_state_log`、`credential_state_nodes`、`credential_probe_configs`、`credential_probes`、`credential_keys`、`credential_quotas`、`credential_quota_usage`、`credential_model_bindings`、`credentials` |
| `models` | 配置数据 | `model_modality_verification`、`model_aliases`、`provider_models`、`model_families`、`models_canonical` |
| `providers` | 配置数据 | `provider_endpoint_protocols`、`provider_quality_configs`、`provider_quality_profiles`、`provider_header_profiles`、`provider_catalog`、`provider_settings`、`providers` |
| `api_keys` | 配置数据 | `key_rpm_daily`、`key_applications`、`api_keys` |
| `users` | 配置数据 | `users` |
| `tenants` | 配置数据 | `tenants` |

**执行顺序是唯一的，且按外键安全排列**（子表先于父表）：

```text
request_records → probe_records → routing_records → credentials →
models → providers → api_keys → users → tenants
```

其中 `session_summaries` 在 `tenants` 之前删（它是 `tenants` 的级联子表，先删干净才不会让级联「偷跑」掉本该计入预览的行数）；`credential_probe_configs` 与 `credential_probes` 是 `credentials` 的限制级外键子表，必须先删，否则删父表会直接撞外键。

### 12.3 保留谓词：哪些行永远不删

| 类别 | 保留内容 | 原因 |
| --- | --- | --- |
| `api_keys` | `is_system = true` 的系统 key | 网关自身在用的内部调用凭据 |
| `users` | 全部 `super_admin` + 当前执行操作者 | 保证重置后还登得进去 |
| `tenants` | `code = 'default'` 的默认租户 + 当前操作者所属租户 | 保证默认租户骨架不被清空 |

其余类别没有保留谓词，整类可清空。

### 12.4 三步操作

**第一步：统计。** `GET /api/admin/data-lifecycle/data-init/catalog` 返回每个类别、每张表的行数、占用空间、最早与最新数据时间、会被删掉的行数（`target_rows`）与被保留的行数（`protected_rows`）；本环境里不存在的表（如老部署没迁移到位）会如实列在 `missing_tables` 里，不会报错。

**第二步：预览。** `POST /api/admin/data-lifecycle/data-init/preview`，请求体 `{"categories": ["request_records", "probe_records"]}`。响应给出每个类别的明细、`total_rows`、`total_protected_rows`、`total_target_rows`、`confirm_token` 与 `token_expires_at`，以及需要人工确认的 `warnings`。

**第三步：执行。** `POST /api/admin/data-lifecycle/data-init/execute`，请求体 `{"categories": [...], "confirm_token": "<第二步拿到的令牌>"}`。响应给出 `actor`、`total_rows_deleted`、每个类别实际删掉的 `rows_deleted` 与 `executed_at`。

**预览行数与实际删除行数可能不同**——级联删除与并发写入都会造成差异，所以响应里 `target_rows`（预览值）与 `rows_deleted`（实际值）分开给出，不要混为一谈。令牌只有 5 分钟有效期，超时或类别集合变动都会让执行被拒（400），需要重新预览。

**在控制台里怎么点。** 同一套三步在界面上的入口是「数据运维」组 → 「数据生命周期」页（`/admin/data-lifecycle`，该入口仅平台运维可见）里的「数据初始化」面板：**默认已全选全部类别**（点「全不选」可以清空，「全选」可以恢复），所以打开即是「准备重置」的姿态 → 点预览 → 看清受影响行数与保留行数 → 确认后执行，界面同样要求带上预览签发的令牌。租户管理员看不到该入口；即便直接调接口，后端也会以 403 拒绝。

### 12.5 命令行演练

```bash
BASE=http://127.0.0.1:8781
TOKEN=<超级管理员 JWT>

# ① 统计：看各类数据有多少
curl -s -H "Authorization: Bearer $TOKEN" \
  "$BASE/api/admin/data-lifecycle/data-init/catalog" | jq '.categories[] | {key, level, rows, bytes_human, protected_rows, target_rows}'

# ② 预览：只清运行数据
TOKEN_JSON=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"categories":["request_records","probe_records","routing_records"]}' \
  "$BASE/api/admin/data-lifecycle/data-init/preview")
echo "$TOKEN_JSON" | jq '{total_rows, total_protected_rows, total_target_rows, token_expires_at}'
CONFIRM=$(echo "$TOKEN_JSON" | jq -r '.confirm_token')

# ③ 执行：带上预览签发的令牌（5 分钟内有效）
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"categories\":[\"request_records\",\"probe_records\",\"routing_records\"],\"confirm_token\":\"$CONFIRM\"}" \
  "$BASE/api/admin/data-lifecycle/data-init/execute" | jq '{actor, total_rows_deleted, results}'
```

要连业务配置一起清（换环境、交接机器），在 `categories` 里追加 `credentials`、`models`、`providers`、`api_keys`、`users`、`tenants` 即可；由于保留谓词生效，系统 key、超级管理员与默认租户仍会留下。

### 12.6 审计留痕

每次执行都会写入 `routing_audit_log`，`action` 为 `data_init_reset`，`after_json` 里记录本次的类别集合、操作者、租户、计划摘要（`plan_digest`）、端点与删除条数。审计与删除在同一事务里落库——审计写失败，整个删除会一并回滚，不会出现「删了但没记录」。

### 12.7 注意事项

- **先备份再重置。** 重置是物理删除，没有回收站；配置数据一旦清掉，供应商、凭据、模型绑定都要重新配。
- **重置期间停止写入。** 并发写入会让预览值与实际值产生偏差；重置前先把上游流量摘掉。
- **令牌不要跨类别复用。** 令牌绑定了类别集合与删除计划，复制到别的类别组合会被拒绝。
- **不要把清理当成常规操作。** 日常保留期收敛用「数据运维」组的「数据生命周期」清理（预览 → 执行两段式），数据重置是换环境、复现故障这类场景的归零动作。
- **只清运行数据通常就够。** 排障复现、演示前后的清理，清 `request_records`、`probe_records`、`routing_records` 三类即可，不必动配置。

