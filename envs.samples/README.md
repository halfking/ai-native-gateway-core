# envs.samples — 环境变量示例目录

本目录是 llm-gateway-go **全部环境变量配置的单一汇总入口**（2026-10-01 整理）。
每个文件对应一类使用场景，值全部为**示例值**，禁止直接用于生产：

- IP 一律使用文档保留段（`203.0.113.0/24` 公网、`10.x.x.x` 内网）；
- 密钥/token 一律为 `example-*` / `changeme-*` 形式的假值；
- 正式使用前必须替换为真实值（生成命令见下文「密钥生成」）。

> 历史注：根目录 `.env.example` / `.env.local.example` / `.env.dev-research.example` /
> `.env.quickstart.example` 与 `deploy/prometheus/.env.example` 仍然有效（compose 与
> 脚本按这些文件名加载），本目录是其超集汇总与文档化，二者不冲突。

## 文件清单

| 文件 | 场景 | 典型用法 |
| --- | --- | --- |
| `01-gateway-core.env.sample` | 网关进程运行时核心：监听/DB/Redis/密钥/超时/日志/存储 | 复制合并进部署机 `.env` 或 systemd `EnvironmentFile` |
| `02-gateway-features.env.sample` | 可选功能开关：存活恢复/Goal/Handoff/托管任务/URSM v2/压缩/配额巡检等 | 按需挑变量加进 `.env`，全部有代码内默认值 |
| `03-integrations.env.sample` | 第三方集成：Casdoor/Memora/分析模型/ASM outbox/maintain/ACC/License | 按启用的集成挑变量 |
| `04-notifications.env.sample` | 通知渠道：钉钉/飞书/企业微信/候选失败告警 | 按启用的渠道挑变量 |
| `05-docker-compose.env.sample` | 根目录 `docker-compose.yml` 栈（PG/Redis 容器、卷挂载、镜像 tag） | 复制为根目录 `.env` |
| `06-deploy-local.env.sample` | `scripts/deploy-local.sh` 本地蓝绿部署 | 复制为 `.env.local` 后 `source` |
| `07-ops-servers.env.sample` | 运维占位符：服务器 IP/SSH 端口与密钥/多环境 DSN | 填入本地 shell 环境或部署工具变量组 |
| `08-monitoring.env.sample` | `deploy/prometheus` 监控栈（Grafana/alertmanager/exporter） | 复制为 `deploy/prometheus/.env` |
| `09-testing.env.sample` | 测试入口约定（RLS 门禁/契约测试/回放/live 上游） | `export` 后跑对应 `make test-*` 目标 |
| `10-installer.env.sample` | 安装器与 llm-launcher（无头安装/激活/镜像仓库） | 安装命令前 `export` |

## 使用方法

```bash
# 1) 容器栈（根目录 compose）
cp envs.samples/05-docker-compose.env.sample .env

# 2) 本地脚本部署
cp envs.samples/06-deploy-local.env.sample .env.local
vim .env.local                 # 填真实值（密钥必须固定，见文件内说明）
set -a; source .env.local; set +a
./scripts/deploy-local.sh deploy

# 3) 裸跑网关（systemd / 手动）
#    按 01 → 02/03/04 按需合并出部署机 /etc/llm-gateway-go/env（chmod 600）
```

加载优先级（见 `config/config.go`）：**环境变量 > YAML 配置（`LLM_GATEWAY_CONFIG_FILE`）> 代码默认值**。
`LLM_GATEWAY_DATABASE_URL` 等核心项均有无前缀 fallback（`DATABASE_URL`、`SECRET_KEY`、
`CREDENTIAL_ENCRYPTION_KEY`），同名设置一个即可。

## 密钥生成

```bash
# 会话签名密钥（设置后不可随意更换，否则已签发会话全部失效）
openssl rand -base64 32

# 凭据加密密钥（base64url、32 字节；一旦设置绝不可更换，
# 否则库里 credentials.secret_ciphertext 全部无法解密——参考 2026-09-05 provider 587 事故）
openssl rand -base64 32 | tr '+/' '-_' | tr -d '='

# 各类 API key
openssl rand -hex 16 / openssl rand -hex 32
```

## 安全规则

1. **真实值不入库**：本目录内置 `.gitignore` 只放行 `*.sample`/`README.md`，
   在本目录里复制出的任何真实 env 不会被 git 跟踪；其他位置同理勿提交。
2. **生产环境**：敏感配置走 SOPS 加密（`envinjector/` 包 + `.env.<target>.enc`，
   `SOPS_AGE_KEY_FILE` 指定 age 私钥，默认 `~/.config/sops/age/keys.txt`），
   或 k8s secret / `/etc/llm-gateway-go/env`（chmod 600）注入。
3. 文档与脚本中的 `<env:KEY>` 是敏感信息脱敏占位符约定，由外部 envs loader
   （`~/workspace/ai-native-tools/envs/loader.sh`）注入真实值。

## 维护（新增变量时同步本目录）

重新审计全仓库环境变量的命令：

```bash
# Go 代码（排除 vendor 与测试；含 envOrDefault/getEnvBool 等辅助读取函数）
git grep -hoE '(os\.Getenv|os\.LookupEnv|envOrDefault|envOr|getEnvBool|getEnvInt|getEnvFloat|parseIntEnv|parseEnvDuration)\("[^"]+"' -- '*.go' ':(exclude)vendor/**' ':(exclude)*_test.go' \
  | sed -E 's/.*\("//;s/"//' | sort -u

# settings 规格表（goal/handoff 等 specs 的 EnvName 列）
git grep -h 'EnvName:' -- 'settings/*_specs.go'

# compose 插值 / 脚本
grep -hoE '\$\{[A-Z0-9_]+' docker-compose*.yml | tr -d '${' | sort -u
```

核心读取点：`config/config.go`（主配置）、`config/storage.go`、`config/runtime_role.go`、
`cmd/gateway/main*.go`、`internal/logging/`、`scripts/deploy-local-lib.sh`、`installer/`。
