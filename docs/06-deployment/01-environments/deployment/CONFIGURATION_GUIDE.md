# LLM Gateway 配置管理指南

> 统一管理敏感配置，确保环境一致性。
> 所有敏感值在文档中以 `__{CAT}_{N}__` 占位符表示，真实值存储在仓库外。

## 1. 配置架构总览

```
┌─────────────────────────────────────────────────────────┐
│                   文档 (docs/*.md)                        │
│          所有敏感值 → __PUB_IP_1__ 等占位符               │
│          映射文件: ~/.llm-gateway/docs-sensitive.json     │
└──────────────────────┬──────────────────────────────────┘
                       │
┌──────────────────────┼──────────────────────────────────┐
│        环境变量层    │   scripts/load-env.sh 统一加载     │
├──────────────────────┼──────────────────────────────────┤
│  SOPS 加密 (生产)     │  明文 (开发/本地)                 │
│  .env.184.enc        │  .env.local                      │
│  .env.71.enc         │  cp .env.example .env.local      │
│  git 提交 (加密状态)  │  .gitignore 排除                  │
└──────────────────────┴──────────────────────────────────┘
                       │
┌──────────────────────┴──────────────────────────────────┐
│             运行时环境 (服务器 / 容器)                     │
│  /etc/llm-gateway-go/env  (systemd)                     │
│  k8s secret 注入环境变量  (k3s)                          │
│  docker-compose env_file (.env.local)                    │
└─────────────────────────────────────────────────────────┘
```

## 2. 环境文件层级

| 层级 | 文件 | 位置 | 加密 | Git | 用途 |
|------|------|------|------|-----|------|
| L1 | `.env.184.enc` | 项目根 | ✅ SOPS (age) | ✅ 提交 | 184 生产环境 |
| L1 | `.env.71.enc` | 项目根 | ✅ SOPS (age) | ✅ 提交 | 71 生产环境 |
| L2 | `.env.local` | 项目根 | ❌ 明文 | ❌ `.gitignore` | 本地开发 |
| L3 | `~/.llm-gateway/docs-sensitive.json` | `$HOME` | ❌ 明文 | ❌ 仓库外 | 文档占位符→真值映射 |
| L4 | `/etc/llm-gateway-go/env` | 服务器 | ✅ chmod 600 | ❌ 服务器本地 | 运行时环境变量 |

### 2.1 加载优先级（高→低）

1. **进程环境变量**（systemd unit / k8s secret 注入）
2. **`/etc/llm-gateway-go/env`**（服务器本地，chmod 600）
3. **`.env.local`**（开发环境）
4. **程序默认值**（config.go 中的 fallback）

### 2.2 SOPS 加密文件

```bash
# 解密查看
sops -d .env.184.enc

# 安全编辑（自动解密→编辑→加密）
sops .env.184.enc

# 在 CI/脚本中解密并导出
eval $(sops -d .env.184.enc)
```

### 2.3 本地开发环境

```bash
# 初始化
cp .env.example .env.local

# 从映射文件查询占位符对应的真实值
cat ~/.llm-gateway/docs-sensitive.json | jq '.mappings["__PUB_IP_1__"].value'

# 编辑 .env.local 填入真实值后加载
source scripts/load-env.sh
# 脚本自动检测并加载 .env.local
```

## 2.4 免费会话分析模型

会话分析默认使用三个稳定模型别名：

- `embedding-fast`：1024 维 embedding，用于会话聚类与低成本意图分析。
- `summary-fast`：短输出中文总结和 rolling session summary。
- `intent-fast`：低置信度意图分类与标签生成。

仅当以下两个环境变量同时存在时，网关才启用外部分析模型：

```bash
LLM_GATEWAY_ANALYSIS_BASE_URL=https://analysis-provider.example/v1
LLM_GATEWAY_ANALYSIS_API_KEY=__ANALYSIS_API_KEY_1__
```

该 endpoint 必须同时兼容 OpenAI `/v1/chat/completions` 与 `/v1/embeddings`，生产环境只允许公网 HTTPS 且不跟随重定向。本地 loopback HTTP 测试必须显式设置 `LLM_GATEWAY_ANALYSIS_ALLOW_INSECURE_LOCAL=true`。未配置时，请求级摘要保持规则模式，向量聚类退化为规则粗聚类，不产生模型费用。

推荐路由顺序：Cloudflare Workers AI 或 SiliconFlow embedding 作为 `embedding-fast`，智谱 `GLM-4.7-Flash` 作为 `summary-fast`/`intent-fast`，Groq `llama-3.1-8b-instant` 作为显式开启的低价故障兜底。免费模型和额度会变化，生产绑定前必须用账号实测 `/models`、批量上限、RPM/TPM、429 行为和每日额度。

100–1000 QPS 下任何公开免费 API 都无法保证全量生成式处理。系统应保持“规则与缓存 → 批量 embedding → 低置信度生成模型”的漏斗；免费额度耗尽时降级到规则结果，而不是无限重试。不得通过创建多个账号规避厂商免费限额。

## 3. 文档占位符管理

### 3.1 映射文件

- **路径**: `~/.llm-gateway/docs-sensitive.json`
- **格式**:
```json
{
  "version": "1",
  "generated_at": "2026-07-05",
  "mappings": {
    "__PUB_IP_1__": {
      "value": "<server_public_ip>",
      "category": "pub_ip",
      "files": ["FAQ_AND_TROUBLESHOOTING.md", "BUILD_AND_DEPLOY_GUIDE.md"]
    },
    "__API_KEY_1__": {
      "value": "<api_key_value>",
      "category": "api_key",
      "files": ["FAQ_AND_TROUBLESHOOTING.md"]
    }
  },
  "by_category": {
    "pub_ip": ["__PUB_IP_1__", "__PUB_IP_2__"],
    "api_key": ["__API_KEY_1__"]
  }
}
```

### 3.2 占位符命名规范

| 类别 | 前缀 | 示例 |
|------|------|------|
| 公网 IP | `__PUB_IP_N__` | `__PUB_IP_1__` = `14.103.112.184` |
| 内网 IP | `__PRIV_IP_N__` | `__PRIV_IP_1__` = `172.31.0.2` |
| 生产域名 | `__DOMAIN_N__` | `__DOMAIN_1__` = `llmgo.kxpms.cn` |
| SSH 密码 | `__SSH_PWD_N__` | SSH 服务器密码 |
| DB 密码 | `__DB_PWD_N__` | 数据库密码 |
| API Key | `__API_KEY_N__` | 第三方 API 密钥 |
| 用户名 | `__USER_N__` | 系统/开发人员用户名 |
| 端口 | `__PORT_N__` | 服务端口号 |
| Git 仓库 URL | `__REPO_URL_N__` | 内部 Git 仓库地址 |
| 本地路径 | `__LOCAL_PATH_N__` | 本地开发目录 |
| 服务端路径 | `__SERVER_PATH_N__` | 服务器部署目录 |
| SSH 目标 | `__SSH_TARGET_N__` | SSH 用户@主机 |
| 管理密码 | `__ADMIN_PWD_N__` | 管理后台密码 |

### 3.3 添加新占位符

```bash
# 1. 在 scripts/redact-docs.py 中添加新值
p("pub_ip", "new.ip.address.here")

# 2. 重新运行脱敏脚本
python3 scripts/redact-docs.py

# 3. 验证映射文件已更新
cat ~/.llm-gateway/docs-sensitive.json | jq '.mappings | keys'
```

### 3.4 查找占位符对应的真实值

```bash
# 查询单个
jq -r '.mappings["__PUB_IP_1__"].value' ~/.llm-gateway/docs-sensitive.json

# 搜索包含某个值的占位符
jq '[.mappings | to_entries[] | select(.value.value | contains("14.103")) | {key, value: .value.value}]' ~/.llm-gateway/docs-sensitive.json

# 查看某个文件被替换了哪些信息
jq '[.mappings | to_entries[] | select(.value.files[] | contains("FAQ_AND_TROUBLESHOOTING.md")) | {placeholder: .key}]' ~/.llm-gateway/docs-sensitive.json
```

## 4. 部署集成

### 4.1 环境变量验证

所有部署脚本和 systemd unit 都应使用环境变量，而非硬编码值。

```bash
# 启动前验证配置完整性
./scripts/verify-config.sh --strict

# 加载环境变量
source scripts/load-env.sh
```

### 4.2 deploy-184.sh 配置

`deploy-184.sh` 从以下来源读取配置（按优先级）：
1. 环境变量（`$LLM_GATEWAY_184_HOST` 等）
2. `.env.184.enc`（SOPS 解密）
3. 脚本内默认值

```bash
# 覆盖默认值示例
LLM_GATEWAY_184_HOST=__PUB_IP_1__ ./deploy-184.sh
```

### 4.3 systemd unit 配置

生产服务器上，环境变量从 `/etc/llm-gateway-go/env` 文件加载：

```ini
[Service]
EnvironmentFile=/etc/llm-gateway-go/env
```

`/etc/llm-gateway-go/env` 由部署脚本自动维护：
```
LLM_GATEWAY_DATABASE_URL=postgres://...
LLM_GATEWAY_API_KEY=sk-...
LLM_GATEWAY_ADMIN_API_KEY=sk-...
```

### 4.4 实时请求流缓存

管理端 `GET /api/admin/live-stream` 为每个作用域维护 delta baseline。活跃 SSE
订阅会保留其 baseline；无订阅作用域会在超时后释放以限制内存使用。

```bash
# 使用 Go duration 格式（30s / 10m / 1h）。
LLM_GATEWAY_LIVE_STREAM_CACHED_TTL=10m

# 可选；未设置时自动跟随 CACHED_TTL。
LLM_GATEWAY_LIVE_STREAM_CACHED_CLEANUP_INTERVAL=10m
```

运行时可通过 live stream stats 观察 `cached_snapshot_entries`、
`active_scope_subscriptions`、`cached_snapshot_empty_skips` 与
`cached_snapshot_evictions`，用于排查 Redis 空读或 baseline 回收。

## 5. 故障恢复

### 5.1 映射文件丢失

如果 `~/.llm-gateway/docs-sensitive.json` 丢失：

```bash
# 从 SOPS 加密文件中恢复已知值
sops -d .env.184.enc | grep -E '^(LLM_GATEWAY_|DB_|REDIS_)'

# 从文档中的占位符反向查找上下文
grep -r '__PUB_IP_1__' docs/ | head -5
```

### 5.2 服务器配置重建

```bash
# 从 SOPS 重新生成服务器环境文件
sops -d .env.184.enc > /etc/llm-gateway-go/env
chmod 600 /etc/llm-gateway-go/env
```

## 6. 安全规则

1. **禁止**将真实敏感值提交到 Git 仓库
2. **禁止**在文档中使用真实 IP/密码/Key
3. 所有文档必须使用 `__{CAT}_{N}__` 占位符
4. `~/.llm-gateway/` 目录不进仓库
5. 服务器 `/etc/llm-gateway-go/env` 权限为 `600`
6. 定期轮换密码后同时更新 SOPS 文件和映射文件
