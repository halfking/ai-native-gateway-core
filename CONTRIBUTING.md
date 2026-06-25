# Contributing to SI-LLM-Gateway

感谢你考虑为 SI-LLM-Gateway 做出贡献！🎉

## 目录

- [开发环境](#开发环境)
- [开发流程](#开发流程)
- [代码规范](#代码规范)
- [测试要求](#测试要求)
- [提交规范](#提交规范)
- [Pull Request 流程](#pull-request-流程)
- [多租户改动专项](#多租户改动专项)

---

## 开发环境

### 必备工具

- **Go** 1.21+ (`go version`)
- **PostgreSQL** 14+ (本地或 Docker)
- **Node.js** 18+ (前端开发)
- **pnpm** 8+ (`npm install -g pnpm`)
- **Python** 3.10+ (控制面 / 运维脚本)

### 初始化

```bash
# 1. Fork & clone
git clone https://codeup.aliyun.com/<your-fork>/llm-gateway-go.git
cd llm-gateway-go

# 2. 安装 git 钩子
./scripts/pre-commit-install.sh   # pre-commit: go vet + SQL lint
./scripts/install-githooks.sh     # pre-push: 敏感信息扫描

# 3. 拉取子模块（如有）
git submodule update --init --recursive

# 4. 启动本地依赖 (PG + Redis)
docker compose up -d postgres redis

# 5. 初始化 DB schema
make db-init  # 或 ./scripts/db-init.sh
```

---

## 开发流程

1. **从 `main` 创建分支**
   ```bash
   git checkout -b feat/your-feature
   ```
2. **编写代码 + 测试**（每个 PR 应有对应测试）
3. **本地验证**
   ```bash
   go test ./...
   go vet ./...
   ./scripts/scan-secrets.sh --mode=strict --paths=.
   ```
4. **Commit + Push**（参考 [提交规范](#提交规范)）
5. **开 PR**（参考 [PR 流程](#pull-request-流程)）

---

## 代码规范

### Go 风格

- 遵循 [Effective Go](https://go.dev/doc/effective_go) + [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments)
- 使用 `gofmt` / `goimports` 格式化
- 导出符号必须有 doc 注释（`// FooBar does ...`）
- 错误处理：用 `fmt.Errorf("context: %w", err)` 包装

### 包结构

```
relay/        # HTTP 请求中继
routing/      # 路由规划与执行
circuit/      # 熔断器
pool/         # 身份绑定连接池
identity/     # 身份管理
upstream/     # 上游 LLM 客户端
audit/        # 审计 pipeline
middleware/   # HTTP 中间件
admin/        # 管理 API (super_admin only)
apihub/       # API 资产中心 (Q3 2026)
armor/        # Model Armor (Q4 2026)
a2a/          # A2A 协议 (Q1 2027)
```

### 命名约定

| 类别 | 约定 | 示例 |
|------|------|------|
| 包名 | 全小写，无下划线 | `routing`, `auth` |
| 接口 | 方法名 + er | `Reader`, `Executor` |
| 私有 struct | camelCase | `routePlan` |
| 导出 struct | PascalCase | `RoutePlan` |
| 常量 | PascalCase 或 UPPER_SNAKE | `MaxRetries` 或 `MAX_RETRIES` |
| 环境变量 | `LLM_GATEWAY_<MODULE>_<KEY>` | `LLM_GATEWAY_DB_DSN` |

---

## 测试要求

### 必跑

```bash
go test ./...                    # 全部测试
go test -race ./...              # race detector (推荐)
./scripts/scan-secrets.sh --mode=strict
```

### 覆盖率

- 核心模块 (`relay/`, `routing/`, `circuit/`) 应保持 **≥ 80%** 覆盖率
- 新增功能必须带单元测试
- 集成测试加 `//go:build integration` tag，需要真实依赖

### 测试命名

```go
func TestExecutor_PickCandidate(t *testing.T) { ... }
func TestExecutor_PickCandidate_NoCredentials(t *testing.T) { ... }
func TestExecutor_PickCandidate_AllDisabled(t *testing.T) { ... }
```

---

## 提交规范

使用 [Conventional Commits](https://www.conventionalcommits.org/)：

```
<type>(<scope>): <short description>

[optional body]

[optional footer(s)]
```

### Type

| Type | 用途 |
|------|------|
| `feat` | 新功能 |
| `fix` | Bug 修复 |
| `docs` | 文档改动 |
| `refactor` | 重构（无新功能 / 修复） |
| `test` | 测试相关 |
| `chore` | 杂项（依赖、CI、构建） |
| `perf` | 性能优化 |

### Scope

- `relay` / `routing` / `circuit` / `pool` / `identity` / `audit` 等模块名
- 或 `db` / `web` / `deploy` / `ci`

### 示例

```
feat(routing): multi-level sticky routing to prevent cross-model pollution

The previous single-level sticky logic (gw_session_id -> credential)
caused credential thrashing when a session re-prompted with a different
model. Now we add (model_name, tenant_id) as a second-level stickiness
key, so a session can hold the same upstream credential across model
switches within the same tenant.

See docs/2026-06-25-multi-level-sticky-implementation-report.md

Closes #123
```

---

## Pull Request 流程

1. **PR 标题**：与 commit message 一致 (`feat(scope): ...`)
2. **PR 描述**：
   - 背景 / 问题
   - 解决方案
   - 测试方式
   - 截图 (如涉及 UI)
   - 关联 issue
3. **Reviewer 要求**：
   - 至少 1 位 TL/架构师 approve
   - 多租户改动需 TL + 安全负责人双 approve
4. **CI 必须通过**：
   - `go test ./...`
   - `go vet ./...`
   - `scan-secrets.sh` 干净
   - migration 编号唯一
5. **Squash merge** 到 `main`

---

## 多租户改动专项

**所有**涉及数据库的改动必须满足：

1. **RLS 验证**：
   ```bash
   make lint-pg-rls  # 自动检查新表是否启用 RLS
   ```
2. **Tenant scope linter**：
   ```bash
   make lint-tenant-scope-llmgw
   ```
3. **OTel tenant attribute**：
   ```bash
   make lint-otel-tenant
   ```
4. **新增表必填字段**：`tenant_id UUID NOT NULL`
5. **跨租户读返回 `ErrNotFound`**（不返回 403，避免信息泄露）

详见 [`AGENTS.md`](AGENTS.md) §多租户审计。

---

## 部署相关改动

任何 `deploy/`、`cmd/gateway/main.go`、CI 脚本的改动：

- 必须在测试环境 (71 host docker) 跑通 `make deploy-test`
- 必须有 `deploy-checkpoint.sh pre/post` 双 checkpoint
- 必须在 184 k3s 跑 `pre-deploy-verification` 4 步门
- **未经明确授权，禁止部署到 71 / 184 生产**

---

## 文档

- 重大功能更新同步到 `docs/` 目录
- 季度规划更新到 `docs/产品方案/`
- README 反映产品定位变化时，同步更新 `docs/产品方案/2026-06-23-llmgw-future-roadmap-and-product-design.md`

---

## 行为准则

- 尊重他人，友好沟通
- 接受建设性批评
- 关注社区利益
- 详见 `CODE_OF_CONDUCT.md`（如有）

---

## 联系方式

- 项目仓库：https://github.com/halfking/SI-LLM-Gateway
- 内部仓库：https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go
- 内部 IM：halfking
- 邮件：dev@kxpms.cn
