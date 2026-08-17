# main.go 拆分重构计划 (2026-07-21)

> 状态：**Step 4 完成** (commits 6d92d69a2, a31ea3534, ab993b199)
> 当前 main.go: **3831 行** (起点 4709 行，**-878 行 / -18.7%**)
> refactor plan owner: ACC Refactor Agent

## 1. 问题陈述

`cmd/gateway/main.go` 单文件从 2026 年 6 月以来持续膨胀到 4709 行（含 god function `main()` 体 3700+ 行 + 大量顶层 helper / adapter / closure），违反：

- **rule 00 §2** 文件组织（单文件 ≤ 300 行）
- **rule 42** 设计粒度（≤ 300 行 / 逻辑点）
- **rule 01 §3** 单 commit ≤ 200 行（main.go 一个文件就不可能保持）

维护痛点：

1. `git blame` 在 main.go 上几乎无意义（每次提交都跨越大段代码）
2. PR review 必须打开一个 4500 行文件 + 9 个周边文件
3. test 覆盖率工具对 main.go 给出"非核心"标签（不写测试）
4. IDE 跳转卡顿（gopls 索引 4500 行 + 90 个 import）

## 2. 目标

| 项 | 起点 | 目标 | 当前 |
|---|---|---|---|
| main.go 行数 | 4709 | **≤ 500** | 3831 (差 3331) |
| 顶层函数文件数 | 0 | 4-8 | 7 (✅) |
| helper 文件最大行数 | n/a | ≤ 300 | 254 (✅) |
| main() 函数体行数 | 3700+ | ≤ 100 | 3700+ (❌ god function) |

**主目标达成**：helper 文件拆分完成。
**残留难点**：`main()` 函数体本身（依赖 ~30 个共享局部变量，无法按阶段直接切分）。

## 3. 拆分步骤

### Step 1-3：抽离 `main()` 函数外的顶层 helper（commit `6d92d69a2`）✅

main.go 4709 → 3866 (-843)。新建 5 个 helper 文件：

| 文件 | 行数 | 内容 |
|---|---|---|
| `main_helpers.go` | 125 | env helpers (positiveDurationEnv / useNewProbeMode / ...) |
| `main_types.go` | 254 | adapters (pendingStoreAdapter / sessionAuthAdapter / irAdapter) + auto_llm_caller |
| `main_settings.go` | 169 | settings sync helpers + parse/parseBool |
| `main_notification.go` | 226 | initApprovalNotifier + DingTalk helpers |
| `main_livestream.go` | 194 | adminLiveRequestFromEntry + incidentUpdateFromResult |

每个文件 ≤ 254 行（rule 42 ≤ 300）。无 behaviour change（编译 + vet 通过）。

### Step 3.5：LSP 误报修复（commit `a31ea3534`）✅

main.go CanonHandlers 字段加 `http.HandlerFunc(...)` 显式包装，让 gopls 类型推导满意。8 个字段，零行为变化（方法值与 HandlerFunc 底层类型相同）。

### Step 4a+4b：抽 main() 体内可独立小闭包（commit `ab993b199`）✅

main.go 3888 → 3831 (-57)。新建 2 个 hoist 文件：

| 文件 | 行数 | 内容 |
|---|---|---|
| `main_jwt_middleware.go` | 74 | resolveJWTSecret + newJWTMiddleware + newRequireSuperAdminMiddleware |
| `main_admin_wrappers.go` | 65 | newWrapAdmin + newWrapSessionAnalytics |

闭包捕获的变量（jwtSecret / pool / cfg.SecretKey）参数化为显式参数，便于测试与复用。

## 4. 残留工作 (Step 4c 及以后)

### Step 4c：抽 main() 体内剩余小闭包（建议）

候选清单（每个 ROI 较低）：
- `adminMw` / `superAdminMw` (main.go:3089) — 6 行，抽到底层 helper
- `publishFn` (main.go:1609) — 5 行，做 routeincident publish
- `retentionCfgProvider` (main.go:2327)
- `healthCheck` (main.go:2840)

预计减肥 ~25 行。**不建议**：剩余可独立抽离的纯函数已不多。

### Step 5（**未启动**）：main() 函数体重构

这是"真·剩余难点"——main() 函数体 3700+ 行依赖 ~30 个共享局部变量：

- `cfg` / `dbConn` / `pool` / `keyring` / `cachePool`
- `redisClientForCache` / `upClient` / `telemetryClient` / `ingestPoller`
- `chatHandler` / `adminHandler` / `stateManager` / `liveStreamHub`
- `backgroundWorkers` / `plugins` / `notification`

**任何"按阶段切分"都需要把这些变量整理成结构体 + 依赖注入**，相当于把 main() 重写成 DI 容器。这是个**独立项目**，建议另起 plan。

## 5. 验证证据

每 step 都验证：
- `go build ./cmd/gateway/...` exit 0
- `go vet ./cmd/gateway/...` exit 0
- pre-commit hook PASS (go vet / SQL / migration / vue-tsc)

## 6. refactor plan 历史

- 2026-07-21：plan 起草 + Step 1-4 完成（提交 3 commits 至 origin/main）
