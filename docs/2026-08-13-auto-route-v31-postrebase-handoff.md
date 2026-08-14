# Auto-Route v3.1 UX 后置 Rebase 状态交接（2026-08-13）

> 配套文档：`docs/2026-08-13-auto-route-v31-handoff.md`（前序交接，描述 fix 提交与合并）。本文件记录 **rebase 完成、远程 main 对齐之后** 的最新状态。
> 编写时刻：`22c79083`（本地 `main`）/ `867991c8`（本地 `fix/auto-route-default-duplicate-ux`）。

## 1. TL;DR

- `fix(web): prevent duplicate smart routing defaults`（`94435948`）已合入 `main`，并被 `feature/v32-integrated` 后续的 11 个提交通过 rebase 整合，**无冲突、无回退**。
- `main` 现在 HEAD 为 `22c79083`（merge commit，把 rebase 后的 `origin/main` 拉回本地 `main`）。
- `main` 同时携带本会话写的 `docs(session): add auto-route v3.1 ux handoff`（`e23ae2a9`）。
- `fix/auto-route-default-duplicate-ux` 分支当前 HEAD `867991c8`；其修复内容已通过 `94435948` 的等价 patch 存在于 `main`，但 `867991c8` 本身不是 `main` 的祖先提交，可以保留也可清理（见 §4）。
- 前序 handoff 中提到的 4 个测试失败项 **本会话未再跑回归**，需要在下一会话重新核验（见 §3）。

## 2. 当前分支 / 提交拓扑

```
*   22c79083 (HEAD -> main) Merge remote-tracking branch 'origin/main' into feature/v32-integrated
|\
| * e23ae2a9 docs(session): add auto-route v3.1 ux handoff                <--  本会话追加的交接文档
| * 94435948 fix(web): prevent duplicate smart routing defaults           <--  主修复
* | 0c00e8a6 fix(scenarios): fail closed on flash fault injection gaps     \  由 feature/v32-integrated
* | 0292aa64 docs(v3.2): add security review and task closure reports       \  rebase 进 origin/main
* | caceeebf docs(v3.2): mark Migration 513 and 511 as FIXED                /
...  (另 8 个 v3.2 相关提交)
```

`git status` 当前干净；`git rev-parse main` 与 `git rev-parse origin/main` 均为 `22c79083`，**本地 main 与远程 origin/main 完全对齐**。

## 3. 待复核 / 待处理（移交到下一会话）

前序 `docs/2026-08-13-auto-route-v31-handoff.md` 列举了 4 个仓库测试失败项，本会话未重新运行，下一会话请先复跑再决定是否提 issue 或修：

| 失败项 | 所在包 | 类型 | 备注 |
|--------|--------|------|------|
| Redis RPM 并发 | `domains/credential` | 集成 / Redis | 怀疑竞争，前序会话未跑完 |
| Redis sticky double-write | `domains/routing` | 集成 / Redis | 与本次修复无关，疑似历史 flakiness |
| Redis 超时 | `domains/ursm/v2` | 集成 / Redis | 同上，疑似环境而非代码回归 |
| SQL `//` 注释检查 | `internal/sqlguard` | 单元 | 检查器过于严格，需看 raw 输入是否真的无效 |

建议优先复跑命令：
```bash
go test ./domains/credential/... -run TestRedisRPM -count=5
go test ./domains/routing/... -run TestSticky -count=5
go test ./domains/ursm/v2/... -run TestRedis -count=3
go test ./internal/sqlguard/...
```

## 4. 分支清理建议（不强制）

`fix/auto-route-default-duplicate-ux` 分支上的 `867991c8` 不是 `main` 的祖先；其 UX 修复内容已由 `main` 上的 `94435948` 以等价 patch 形式包含。两种处理方式：

- **保留**：分支作为历史存档，便于追溯本次 UX 修复的提交链路。
- **删除**：确认不再需要该历史分支后执行 `git branch -d fix/auto-route-default-duplicate-ux`；如需删除远程追踪，再执行 `git push origin --delete fix/auto-route-default-duplicate-ux`。

我倾向保留分支直到下一会话确认线上无回归。

## 5. 给下一会话的提示词草稿

```
继续 docs/2026-08-13-auto-route-v31-postrebase-handoff.md 的任务：
1. 在 main (22c79083) 上重新跑 §3 表格中 4 个失败项，给出 PASS/FAIL 与简要日志。
2. 若仍 FAIL，按以下分流处理：
   - Redis 相关：贴 stack + go test 输出，标 issue "flaky?"，不动代码。
   - sqlguard `//` 注释：判定现有 SQL 中 `//` 是否为合法字符串字面量，若是则修正 sqlguard 检测器（保留合法用法、剔除真注释）。
3. 若全部 PASS，合并 §4 分支清理建议的执行结果到本交接文档（追加日期与 git ref），然后归档。
4. 不要触碰本会话的 94435948 与 e23ae2a9 两个提交；若必须改动 V3.1 UX 相关前端代码，请新建分支而非在 main 直改。
```

## 6. 关联文件

- 前序交接：`docs/2026-08-13-auto-route-v31-handoff.md`
- 主修复 commit：`94435948`（`fix(web): prevent duplicate smart routing defaults`）
- V3.2 集成基线：`feature/v32-integrated`（包含 LP5/LP6/v3.2 安全评审等提交）
- 本会话新追加交接 commit：`e23ae2a9`
- 当前 main HEAD：`22c79083`

## 7. 复核完成与归档记录（2026-08-14）

> 复核基线：在独立、干净的 detached worktree `/tmp/llm-gateway-go-3-auto-route-22c79083` 上检出 `22c790836cd601a87f92436af8552e31a05799e1`。未改动 `94435948`、`e23ae2a9` 或任何 V3.1 UX 前端代码。

### 7.1 §3 回归结果

| 失败项 | 复跑命令 | 结果 | 简要日志 |
|--------|----------|------|----------|
| Redis RPM 并发 | `go test ./domains/credential/... -run TestRedisRPM -count=5` | **PASS** | `ok github.com/kaixuan/llm-gateway-go/domains/credential 2.469s` |
| Redis sticky double-write | `go test ./domains/routing/... -run TestSticky -count=5` | **PASS** | `ok github.com/kaixuan/llm-gateway-go/domains/routing 0.467s` |
| Redis 超时 | `go test ./domains/ursm/v2/... -run TestRedis -count=3` | **PASS** | `domains/ursm/v2/integration` 通过；其余无匹配测试的子包均返回 `[no tests to run]` 且 exit 0 |
| SQL `//` 注释检查 | `go test ./internal/sqlguard/...` | **PASS** | `ok github.com/kaixuan/llm-gateway-go/internal/sqlguard 1.054s` |

4 项均为 PASS，因此不创建 `flaky?` issue，也无需调整 `internal/sqlguard` 检测器。

### 7.2 §4 分支清理建议执行结果

- **执行结果：保留** `fix/auto-route-default-duplicate-ux`，作为 UX 修复的历史存档；不执行本地或远程分支删除。
- 核验 git ref：分支当前为 `867991c8976ee2fb067a56ad8f780681ce118dd6`；其修复内容与 `main` 上的 `94435948` 等价，但 `867991c8` 不在 `main` 的祖先链上。
- 本节回归的观测时 `main` 为 `39cd69a3`；本文档在 2026-08-14 安全审计时更新，rebase 后当前 `main` 为 `c0ff631a`，后者不属于本节的测试基线。

### 7.3 归档结论

本交接文件保留为历史记录；§3 的四项回归已通过，但 2026-08-14 245 auto-route smoke test 仍受认证服务 503 阻塞，后续会话必须完成 §8.3 才能关闭部署验证。

## 8. 2026-08-14 安全审计与部署验证

### 8.1 已修复

- 在线会话分页 cursor 现在必须使用 `base64(timestamp|hmac)`；无签名旧 cursor 被拒绝。
- `CURSOR_HMAC_SECRET` 不再使用公开默认值；生产环境缺少该密钥时启动校验失败。
- `scripts/test-models.sh` 现在以 `X-Gw-Auto-Decision.chosen_model` 作为 auto-route 的断言来源，并明确拒绝 `minimax-m2.5`。
- 245 部署文档已改为使用 shared envs SSOT 与 `SSH_KEY_245`；`HOST_245` 不是 `deploy-seamless.sh` 的契约变量。

### 8.2 验证结果

- `go test ./admin/... ./config/... ./envinjector/... -race -count=1`：PASS。
- `go test ./autoroute/... -count=1 -timeout=120s`：PASS。
- `go test ./regression/... -count=1 -timeout=120s`：PASS。
- `go test ./... -short -count=1 -timeout=300s`：PASS。
- `bash tests/env_injector_test.sh`：10 passed, 0 failed。

### 8.3 245 smoke-test 阻塞

2026-08-14 对 245 的 `POST /v1/chat/completions`（`model=auto`）返回 HTTP 503 `auth_unavailable`，未产生 `X-Gw-Auto-Decision`，因此不能声称已验证实际 chosen model。该结果应归类为认证服务/运行环境阻塞；恢复可用 API key 与认证 DB 后，必须重新执行 `scripts/test-models.sh` 并确认 header 中 `chosen_model != minimax-m2.5`。
