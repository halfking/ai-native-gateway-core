# ADR-0002: Target Go package layout（根级平铺 → 分层结构）

- **Status**: Proposed（分阶段执行，本轮未动 Go 包路径）
- **Date**: 2026-08-17
- **Authors**: repo restructure round `chore/repo-restructure-2026-08`
- **Related**: [../architecture/REPO_LAYOUT.md](../architecture/REPO_LAYOUT.md)、
  [../architecture/ARCHITECTURE_REFACTOR_GUIDE.md](../architecture/ARCHITECTURE_REFACTOR_GUIDE.md)、
  根目录 `AUDIT_24H_20260817.md`（B1 门禁）

## Context

约 45 个 Go 包平铺在仓库根目录（admin/、bg/、provider/、settings/ …），加上
`internal/`（36 子包）与 `domains/`（56 限界上下文），新人无法从目录名推断分层。
2026-08-17 结构治理（稳妥版）已清理死包与杂物，但**刻意不动 Go 包路径**，原因：

1. **双向依赖**（文件级引用数，grep 实测）：
   - `domains → internal` 97 处，`internal → domains` 17 处
   - `domains → provider` 76 处，`provider → domains/credential` 1 处（环）
   - `domains → upstream` 17 处，`upstream → domains/identity` 1 处（`//nolint:depguard historical violation`）
   - `domains → autoroute` 17 处，`autoroute → domains/ursm/v2/cache` 1 处（环）
   - `domains` 内部互引 **479 文件**（最大耦合源）
2. `cmd/gateway/main.go` 约 5100 行手写装配 100+ 包，任何移动最先在这里爆。
3. depguard 已存在且已标注一批 `historical violation, B1 routing.go CQRS will fix`。
4. `installer/` 是嵌套独立 module；`tests/`、`examples/` 反向 import 主 module。
5. vendor 模式（147 模块）只含依赖，自身包移动只需改 import + 构建验证，无需重新 vendor。

## Decision

目标布局（**仅记录，不即执行**；`domain/`+`domains/` 两层设计保留）：

```
cmd/                    # 入口不动（gateway 主服务）
domain/                 # 零依赖内核不动
domains/                # 56 限界上下文不动（先做内部解耦）
internal/
  platform/             # errorsx eventbus secret durable pending config db i18n …
  telemetry/            # telemetry metrics internal/observability …
  control/              # admin api apihub bg center settings licensing maas
                        # autoupdate discovery registry metatools catalog tenantops …
  cred/                 # credentialfpslot credentialhealth pool resolve provider upstream
  route/                # autoroute ratelimit（拆环后）
web/  deploy/  scripts/ sql/  # 非 Go，已在 2026-08-17 治理归位
```

### 迁移路线（前置条件 → 波次）

- **前置 B1**（已在 AUDIT_24H 计划中）：`routing.go` CQRS 化拆
  `domains↔autoroute`、`domains↔internal` 双向依赖；`_to_be_deleted/`
  的删除门禁（预发通过+观察期）以 B1 为节点。
- **波次 1（低风险）**：无环基础件 → `internal/platform/`：
  `errorsx eventbus fault hotconfig disguise cache adapter`（每个先 grep 复核 0 反向引用）。
- **波次 2**：观测类 `telemetry metrics`（仅被 domains/cmd 引用）。
- **波次 3**：控制面子系统（admin/bg/settings/…，体量大但方向单一）。
- **波次 4**：凭证栈与路由（B1 完成后）。
- **每波次机械步骤**：`git mv` → 全仓 `sed` 改 import（`github.com/kaixuan/llm-gateway-go/<old>` → `<new>`）
  → `go build ./... && go vet ./...` → `go test ./...` → 单独 commit（可 revert）。

### 明确不做

- 不把 `domains/` 打散进 `internal/`；限界上下文目录名是领域语言，保留。
- 不合并 `domain/` 与 `domains/`（语义不同：内核 vs 上下文）。
- 不在本轮处理 gin/echo、zerolog/zap/slog 三套日志并存（依赖治理另行立项）。

## Consequences

- 正向：根目录从 ~45 个 Go 目录收敛到 ~8 个；`internal/` 恢复 Go 语义；
  depguard 规则可按新分层收敛。
- 代价：每波次触及数百至上千 import 行；PR diff 噪声大（需与功能 PR 错峰）；
  外部 clone（如 installer、tests）不受影响（自身 module/测试包）。
- 回滚：每波次独立 commit，`git revert` 即回。
