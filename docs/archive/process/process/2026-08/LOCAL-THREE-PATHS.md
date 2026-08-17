# 本地环境三条路径对照表

> 自 2026-08-03 起, 本地相关流程严格分为三路, 凭证 / 数据库 / 容器均不共享。

| 路径 | 用途 | 启动脚本 | 数据库 | 凭证来源 | 凭证文件 |
|------|------|---------|--------|---------|----------|
| **Dev Research** | 研发本地集成 (共享 host PG) | `./scripts/local-up.sh` | host `llm-gateway-pg` (db=`llm_gateway_test_dev`) | `.env.dev-research` (手动提供, 复用 host PG 凭证) | `.env.dev-research` (chmod 0600) |
| **Customer Instance** | 客户实例本地模拟 (独立 DB) | `./scripts/customer-instance-up.sh` | 独立容器 `kx-citus-test` (端口 55432) | **部署时临时生成** (openssl rand -hex 24) | `.env.deploy-test` (chmod 0600, gitignored) |
| **One-Click Customer Instance** | 客户/生产实例部署 (独立 DB) | `deploy/one-click/install.sh` | 独立容器 `kx-citus` (端口 5432) | **部署时临时生成** (secrets.GeneratePassword) | `~/llm-gateway/.env` (chmod 600) |

## 选择规则

- **改 gateway 代码 / 跑集成测试 / 多 commit 联调**: 选 **Dev Research** (复用 host PG, 数据共享)
- **验证 "全新部署" 的真实体验** (DB 独立、密码临时生成、reset 重新跑): 选 **Customer Instance**
- **真实部署到客户机器 / 服务器**: 选 **One-Click Customer Instance**

## 三者关键差异

### 数据库隔离

| 路径 | DB 拓扑 |
|------|--------|
| Dev Research | 复用 host 上共享 PG, 多研发共享同一实例的不同库 |
| Customer Instance | 本机独立容器, 不与 host / 其他容器共享 |
| One-Click | 目标机器独立容器 (生产/客户环境) |

### 凭证生成

| 路径 | 何时生成 | 是否固化到脚本 |
|------|--------|---------------|
| Dev Research | 手动填 `.env.dev-research` (沿用 host 共享 PG 约定) | 否 |
| Customer Instance | 每次 `customer-instance-up.sh` 自动生成 (除 `--reuse`) | **否, 仅写入 .env.deploy-test** |
| One-Click | `install.sh --skip-prompt` 自动生成 (除 `--config` 提供) | **否, 仅写入 ~/llm-gateway/.env** |

### 容器名 (避免冲突)

| 路径 | 容器前缀 | 端口 |
|------|---------|------|
| Dev Research | `r112_*` (历史命名) | PG=host:5432 / Redis=6379 / GW=8781+8782 |
| Customer Instance | `kx-citus-test` / `kx-redis-test` / `deploy-test-gateway` | PG=55432 / Redis=16379 / GW=18781 |
| One-Click | `kx-citus` / `kx-redis` / `kx-llm-gateway-go` | PG=5432 / Redis=6379 / GW=8781 |

## 文件组织

```
docker-compose.dev-research.yml      # Dev Research 路径 (共享 host PG)
docker-compose.deploy-test.yml      # Customer Instance 路径 (独立容器)
docker-compose.yml                  # 生产主栈 (与 install.sh 配套)

scripts/local-up.sh                 # Dev Research 启动
scripts/local-r112-migrate.sh       # Dev Research migrations
scripts/local-r112-smoke.sh         # Dev Research smoke
scripts/local-test.sh               # Dev Research 三层测试
scripts/customer-instance-up.sh     # Customer Instance 启动 (新)

.env.dev-research.example           # Dev Research 凭证模板 (入仓)
.env.deploy-test                    # Customer Instance 凭证 (gitignored, 运行时生成)

deploy/one-click/install.sh         # One-Click Customer Instance (不变)
```

## 迁移时间表

- 2026-08-03: 新增 `customer-instance-up.sh` + `docker-compose.deploy-test.yml`, Dev Research 凭证改由 `.env.dev-research` 注入
- 后续: 渐进迁移 `local-deploy-test.sh` 内部用户到新路径, 评估废弃时机