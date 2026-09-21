# R20 — FreeDiscovery 例行审计（2026-09-13）

## 一、结论

**R19 更正轮 tip（`ede6589fd`）之后零新提交：origin/main == HEAD == `ede6589fd`，
FreeDiscovery 四条目标路径的区间净 diff 为空。R19 §二 6 项契约 + LOW 观察
（templates GET list :76）逐条以代码现状重新核销（不信任任何归档声明），
全部维持"真实关闭"/"行为等价豁免"；静态门禁与全量测试全绿。
本轮无代码修复，仅落档 + 版本 bump（2098→2099）。**

④ 条件判定：**tip 未被重部署，但运行二进制与 tip 的 Go 代码行为等同**——
8782 容器（`llm-gateway-local-8782`）内 `/opt/llm-gateway-go/gateway` 的
`vcs.revision == a6b535dab45b2a5086f2a228537c96898a50a930`（seq 2098，与
R19 §六记录一致），而 `a6b535dab..ede6589fd` 仅触及 docs / VERSION /
version.json / web/public（menu-config、version.json），**零 Go 代码变更**
（`git diff --name-only` 过滤非 docs/version 路径为空）。healthz
`2.5.4-a6b535da-20260912-2098`、readyz db+redis connected / `status: ready`。
按 R19 §六协议（"R20 起仅在 tip 重部署时复验运行版本身份，无需重复故障
注入"），本轮不重部署、不注入故障；严格 `vcs.revision == tip` 将随下一次
自然部署（seq 2099+）自动成立。

## 二、验证项与证据（命令与结果）

| # | 验证项 | 命令 | 结果 |
|---|---|---|---|
| 1 | 发现 1：import handler 走 fdStatusFor | `rg -n 'fdStatusFor\(err\)' admin/free_discovery.go` | `handleFreeDiscoveryImport`（:404）域错误路径 :430 `writeError(w, fdStatusFor(err), …)`；回归测试 `TestFreeDiscovery_Import_StatusForSentinels` 在（:211） |
| 2 | 发现 2：list 端点走 fdStatusFor | 同上 + `sed -n` 函数体 | tasks list :338、taskResults :394 均在；templates GET list :76 维持直写 500 → LOW 观察不变（见下） |
| 3 | 发现 3：GetTask sentinel 全链路 | `rg -n 'ErrTaskNotFound' domains/freediscovery/ admin/ web/` | 定义 `template_manager.go:32`；GetTask wrap `discovery_engine.go:410`（`%w (id %d)`）；fdStatusFor 404 组 :459-461；三测试函数在位：`TestDiscoveryEngine_GetTask_NotFoundSentinel`（engine :290）、`TestFreeDiscovery_Task_StatusForSentinels`（admin :237）、`TestFreeDiscovery_Task_GetNotFound404_DBFault500`（admin :259）——行号与 R19 更正轮勘误值逐一吻合 |
| 4 | import 契约（404/409） | `rg -n 'ErrImportTask'` | sentinel `import_service.go:48/:53`；fdStatusFor 404 组 :459-460、409 组 :463-464；handler :430 接线 |
| 5 | R18 收尾：fdRequest `/tasks/{id}` 路由 | `sed -n '60,85p' admin/free_discovery_test.go` | switch :68-84 排序正确且含注释：`/scan` → `/results` 后缀 → `/tasks` 精确 → `/import` → `/tasks/` → `/templates`；404/500 子测试 :269-284 走通 |
| 6 | R18 收尾：前端契约行 | `rg -n 'ErrTaskNotFound' web/src/api/free-discovery.ts` | :15 404 行含 `ErrTaskNotFound`（含跨租户）；409 行 :16；docs §4.4 :215 行原样 |
| 7 | LOW 观察豁免复核 | `sed -n '155,185p' domains/freediscovery/template_manager.go` | `TemplateManager.List` 仍只返回包装后的 db 错误（begin tx / setTenantTx / query / scan），无任何 sentinel → templates GET :76 直写 500 与 fdStatusFor default-500 行为等价的豁免条件**继续成立** |
| 8 | 区间净 diff | `git log ede6589fd..origin/main` + `git diff ede6589fd origin/main -- <4 scope 路径>` | 区间 0 提交，diff 恒空 |
| 9 | WIP 隔离 | `git status --porcelain` | 空（0 文件；balance-floor 已随 `1a89c32fe` 入 main，R19 时的 13 文件 WIP 已不存在） |
| 10 | CJK 收口保持 | `rg -l '[\p{Han}]' domains/freediscovery/*.go` | 仅 `url_safety_test.go` fixture；admin 两文件零命中 |
| 11 | gofmt | `gofmt -l`（scope 文件） | 空 |
| 12 | build / vet | `go build/vet ./admin/... ./domains/freediscovery/...` | 均通过 |
| 13 | 全量回归 | `go test -count=1 ./domains/freediscovery/ ./admin/` | ok（freediscovery 2.66s；admin 69.7s） |
| 14 | ④ 运行版本身份 | `docker cp` 出二进制后宿主 `go version -m`（更正轮升级方法，见 §六 #2）+ `git diff --name-only a6b535dab ede6589fd` | `mod … v0.0.0-20260912194516-a6b535dab45b+dirty` → vcs.revision `a6b535dab` 确证（**+dirty**：构建时工作区带 WIP，见 §六 #3）；tip 增量仅 docs/version 文件（非 docs/version 过滤为空）→ 行为等同 |
| 15 | ④ readyz | `curl :8782/healthz` `/readyz` | healthz `2.5.4-a6b535da-20260912-2098` ready；readyz db 2.6ms + redis 4.1ms `status: ready` |

## 三、改动文件与关键行为

- `docs/audit/2026-09-13-r20-freediscovery-routine-audit.md`（本文）。
- 版本 bump 2098→2099（`scripts/bump-version.sh`，lockstep：`version.json` /
  `VERSION` / `web/public/version.json`；web/dist 副本 gitignored）。
- **零代码改动**：6 项契约 + LOW 豁免全部以代码现状核销维持，无新观察项。

## 四、遗留风险

1. **templates GET list 直写 500（LOW 观察，维持）**：本轮 #7 复核 `List`
   错误来源仍无 sentinel，豁免继续成立。判据不变：未来 `List` 引入任何
   sentinel 时必须先改走 fdStatusFor，否则复刻发现 1 的"契约注释与实现脱节"。
2. **运行二进制滞后 tip 两个 docs 提交**：`vcs.revision = a6b535dab`（seq
   2098）≠ tip `ede6589fd`，但增量为纯 docs/version。R21 若发现 tip 已被
   自然重部署，仅需复核 `vcs.revision == 当轮 tip`；若 tip 含 Go 代码变更
   且已部署，则按部署技能身份检查条款全量复核。
3. **`errors.Is` vs `==`**：domain 层多处仍用 `err == sql.ErrNoRows`（R18
   §四.2 遗留，本轮维持原样）。
4. **归档可信度**：R19 更正轮对 R19 的元审计结论（4 命名测试行号勘误、
   冒烟补做）本轮逐行复核仍然成立；原则继续适用——"已关闭"只是线索，
   代码现状才是证据。
5. **并行会话纪律**：工作区当前零 WIP；提交仍严格按路径显式暂存，
   `git add -A` 在本仓库禁止；推送遇拒用 stash(named)→rebase→push→pop 配方。

## 五、收尾登记：推送遇拒后并入并行线（同日，2026-09-13）

**触发**：R20 首推被拒（non-fast-forward）——fetch 与 push 之间并行会话把
前端响应式/组件化工作流推上 origin/main（`a8c3a7aaa` merge，区间
`ede6589fd..a8c3a7aaa` 共 105 文件）。按配方收束后登记如下：

- **处置**：工作区在收束时干净（版本文件已入 R20 提交 `9b68a6853`），
  stash 步为空操作；merge `cc026c93b`（parents：`9b68a6853` +
  `a8c3a7aaa`）系**并行会话在共享检出目录显式执行
  `git pull --commit --rebase=false --log origin main` 产生**——本轮
  `git rebase origin/main` 随后执行时树已最新（输出"当前分支 main 是
  最新的"）。机制归因经更正轮实证修正（§六 #1）：`pull.rebase=false`
  只影响 `git pull`，不改变 `git rebase` 的线性变基行为。
- **入线体检**：`git diff --name-only ede6589fd a8c3a7aaa` 过滤 `.go$`/
  `go.mod`/`go.sum` 为零命中（纯 web/ + 4 个 docs 文件）；4 条 FreeDiscovery
  scope 路径 diff 为空；版本文件未被入线触碰（rebase/merge 零冲突）。
- **证据覆盖复核**：入线零 Go 变更 → §二 #10-#13 门禁证据对合并后 Go 树
  完全覆盖；合并后补跑 `go build ./admin/... ./domains/freediscovery/...`
  通过。
- **结论**：§一/§二 全部结论对合并后 tip `cc026c93b` 维持；本轮 seq 2099
  与入线零版本交互。R21 区间基线经更正轮改定，见 §七。

## 六、更正轮（同日元审计，对 R20 本身，2026-09-13）

**触发**：用户要求对 R20 元审计。逐条复核 R20 全部声明 + 三项实证补充
（reflog 溯源 + scratch 隔离实验、确定性二进制身份、实时行为探针）。

| # | 复核项 | 命令 | 结果 |
|---|---|---|---|
| 1 | §五 merge 机制归因 | `git reflog -6` 溯源 + scratch 仓库 `git -c pull.rebase=false rebase` | **勘误（已就地修正）**：merge `cc026c93b` 由并行会话在共享检出目录显式执行 `git pull --commit --rebase=false --log origin main` 产生——reflog 记录两词 `origin main` 与非常规 flag 组合，与本轮命令 `git rebase origin/main`（单词、带斜杠）不符；且 rebase 输出"当前分支 main 是最新的"证明其执行时 merge 已存在。scratch 实验（复刻 `pull.rebase=false`）中 `git rebase` 仍线性变基成功 → `pull.rebase=false` 只影响 `git pull`，原文"本仓 pull.rebase=false，收束为 merge"归因错误。memory `local-dev-environment` 中同源错误句一并修正（repo 外） |
| 2 | §二 #14 证据路径 | `docker cp llm-gateway-local-8782:/opt/llm-gateway-go/gateway /tmp && go version -m` | 原记录的容器内命令可能静默回落 `grep -a`（容器内无 go 工具链时），结论虽无误但证据路径有歧义 → 升级为宿主 go 确证：`mod github.com/kaixuan/llm-gateway-go v0.0.0-20260912194516-a6b535dab45b+dirty`；容器内 `/opt/.../version.json` seq 2098 与 healthz 一致 |
| 3 | `+dirty` 对"行为等同"的影响 | 同上 + 实时探针 | 部署二进制构建时工作区带未提交改动（balance-floor WIP 期），单凭 vcs.revision 不足以证行为等同 → 补实时探针（无故障注入）：admin token 登录后 `GET /api/free-discovery/tasks/999999` → **HTTP 404** `{"error":{"detail":"freediscovery: task not found (id 999999)"}}`，与 R19 §六真实 PG 证据一致 |
| 4 | 新 tip（`a92706960`）门禁重跑 | 同 §二 #10-#13 | CJK 仅 fixture；gofmt 空；build/vet OK；`go test -count=1` freediscovery 2.68s + admin 67.0s ok；4 命名测试 4/4 PASS |
| 5 | §五 入线体检数字复核 | `git diff --name-only ede6589fd a8c3a7aaa \| wc -l`（=105；docs 4）+ scope diff `ede6589fd..a92706960` = 0 | 全部成立 |

**根因归纳**：并行会话共享同一检出目录（与 `git add -A` 禁令同族风险）是
merge 插入与机制误判的共同背景；方法论教训——reflog/配置的**命令形态相近
不等于因果**，机制归因必须用隔离实验（scratch 仓库）证伪后方可落档。

**更正轮改动文件**：本文档（§二 #14 证据升级、§五 处置句修正、本节、§七
基线改定）；memory `local-dev-environment`（repo 外，同步修正机制句）。
零代码改动。

## 七、下一轮提示词

> 请根据 docs/audit/2026-09-13-r20-freediscovery-routine-audit.md（含 §五
> 收尾登记、§六 更正轮）与 memory 状态，对 FreeDiscovery 做例行审计（R21），
> 区间基线为 R20 落档后的最新 tip（fetch 时 `git rev-parse origin/main`，
> 若无并行新提交即为 R20 更正轮提交）：
> ① 契约逐条核对——R20 §二 表中 6 项 + LOW 观察（templates list :76，豁免
>   判据见 #7）逐条用 rg 对代码现状核销，勿信任任何"已关闭"声明；
> ② 区间净 diff——`git diff <基线> <新tip> -- admin/free_discovery.go
>   admin/free_discovery_test.go domains/freediscovery/ web/src/api/free-discovery.ts`，
>   merge 提交勿用 `git show --stat` 下结论；入线若含并行 merge，先体检
>   是否触碰 scope 与任何 `.go`/`go.mod`/`go.sum`（未触碰则 Go 门禁证据可
>   延续到合并后 tip）；
> ③ 门禁——`rg -l '[\p{Han}]' domains/freediscovery/*.go` 仅允许
>   url_safety_test.go fixture；gofmt/build/vet/test -count=1 全绿；
> ④ 部署身份——若 tip 含 Go 代码变更且已重部署：`go version -m` 核验
>   vcs.revision == 当轮 tip（建议 docker cp 出二进制用宿主 go，容器内
>   无 go 工具链时该命令会静默失败）+ readyz；若增量纯 docs/version 且
>   未重部署：核对行为等同即可（可复用 404 探针），无需故障注入。
>   注意：运行二进制带 `+dirty` 时 vcs.revision 不足以单独定案，需行为
>   探针补证。
> 推送遇拒：先 fetch 检查入线；收束前 `git reflog -5` 确认没有并行会话
> 已在共享检出目录先行 merge（R20 实况），再用显式 `git pull --rebase
> origin main` 收束——`git rebase` 局部命令无法表达"拉取远端"，且本仓
> 并行会话共用工作区，动手前看 reflog。
