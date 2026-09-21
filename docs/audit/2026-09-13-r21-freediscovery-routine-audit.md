# R21 — FreeDiscovery 例行审计（2026-09-13）

## 一、结论

**区间 b855a5956..5a524b8eb（取证时 origin/main tip）：零 Go 变更、
FreeDiscovery 四条 scope 路径 diff 为空。R20 §二 6 项契约 + LOW 观察
（templates GET list :76，豁免判据 #7）逐条以代码现状 rg 核销（不信任任何
"已关闭"声明），全部行号与 R20 记录逐一吻合，维持"真实关闭"/"行为等价
豁免"。静态门禁与全量测试全绿。④ 实时探针双实例通过，运行版本身份双验。
本轮零代码改动，仅落档；版本 bump 缺席（原因见 §三）。**

区间构成（审计期间远端三连跳，已按最终 tip 重算）：并行线推送
`7ac729d60`（fix(web)，3 文件，以 merge `5e8da3a0d` 收束）→ `087fba395`
（chore(version)：seq 2100 "构建漂移同步"，git_sha 记 1822ebdf，含
menu-config exported_at）→ `5a524b8eb`（handoff：700 根修观察轮，记载
2098 单容器被外部 SIGKILL 无接管等）。7 文件全部为 web/版本/docs，
`.go$|go.mod|go.sum` 过滤零命中。本检出另有并行线两个**未推送**提交
`a0a6a48dc`（balance-floor drawer 表单，10 文件全 web——commit message
的 "(web,admin)" 名不副实，`git show --stat` 实证）与 `fa1124b18`
（fix(web): unblock vite build，审计进行中由并行会话在本检出直接提交，
收编了 App.vue WIP），与本轮取证互不触碰。

④ 条件判定：**行为等同成立且实时探针补证完成**——a6b535dab..5a524b8eb
零 Go 变更（#16），已部署二进制的 Go 代码与 tip 恒等；审计期间并行会话
完成一轮 blue-green：旧 2098 容器 04:53 被停（5a524b8eb handoff 记为
"外部 SIGKILL"），取证收尾时 **:8782 恢复服务（仍 2098 已知好二进制）**、
**:8781 预热新构建 2101**。双实例探针均 **HTTP 404 + 精确 detail**，与
R19 §六 / R20 §六 #3 证据一致；2101 二进制经 docker cp + 宿主
`go version -m` 确证 `vcs.revision=fa1124b181cc… (+dirty)`（+dirty 已按
R20 §六 #3 协议用行为探针补证）。

## 二、验证项与证据（命令与结果）

| # | 验证项 | 命令 | 结果 |
|---|---|---|---|
| 1 | 发现 1：import handler 走 fdStatusFor | `rg -n 'fdStatusFor\(err\)' admin/free_discovery.go` + `rg -n 'handleFreeDiscoveryImport'` | `handleFreeDiscoveryImport`（:404）域错误路径 :430 `writeError(w, fdStatusFor(err), …)`；回归测试 `TestFreeDiscovery_Import_StatusForSentinels`（test :211）在位 |
| 2 | 发现 2：list 端点走 fdStatusFor | 同上 + `sed -n '70,80p'` | tasks list :338、taskResults :394 均在；templates GET list **:76 仍直写** `writeError(w, http.StatusInternalServerError, …)` → LOW 观察不变（见 #7） |
| 3 | 发现 3：GetTask sentinel 全链路 | `rg -n 'ErrTaskNotFound' domains/ admin/ web/` | 定义 `template_manager.go:32`；GetTask wrap `discovery_engine.go:410`（`%w (id %d)`）；fdStatusFor 404 组 :459-461；三测试在位：`TestDiscoveryEngine_GetTask_NotFoundSentinel`（engine :290）、`TestFreeDiscovery_Task_StatusForSentinels`（admin :237）、`TestFreeDiscovery_Task_GetNotFound404_DBFault500`（admin :259）——行号与 R20 逐一吻合 |
| 4 | import 契约（404/409） | `rg -n 'ErrImportTask'` | sentinel `import_service.go:48/:53`；fdStatusFor 404 组 :459-460、409 组 :463-464；handler :430 接线 |
| 5 | R18 收尾：fdRequest `/tasks/{id}` 路由 | `sed -n '60,86p' admin/free_discovery_test.go` | switch :68-84 排序正确且含注释：`/scan` → `/results` 后缀 → `/tasks` 精确 → `/import` → `/tasks/` → `/templates`；404/500 子测试 :269-284 走通 |
| 6 | R18 收尾：前端契约行 | `sed -n '12,20p' web/src/api/free-discovery.ts` + `rg -n 'ErrTaskNotFound' docs/freediscovery-configuration.md` | :15 404 行含 `ErrTaskNotFound`（含跨租户）；409 行 :16；docs §4.4 :215 行原样（R20 所记 docs 文件名为 freediscovery-configuration.md） |
| 7 | LOW 观察豁免复核 | `sed -n '150,190p' domains/freediscovery/template_manager.go` + sentinel 过滤 | `TemplateManager.List` 仍只返回包装后的 db 错误（begin tx / setTenantTx / query / scan），**无任何 sentinel** → templates GET :76 直写 500 与 fdStatusFor default-500 行为等价的豁免条件继续成立 |
| 8 | 区间净 diff（tree-to-tree，merge 勿用 show --stat） | `git diff --name-only b855a5956 5a524b8eb` | 7 文件：VERSION / version.json / web/public/version.json / web/public/menu-config.json / web/src/App.vue / web/src/views/RequestLogsView.vue / docs/handoff/2026-09-12-request-logs-view-raw-model-name-rootfix.md；`.go$/go.mod/go.sum` 过滤零命中；**4 条 FreeDiscovery scope 路径 diff 为空** |
| 9 | 本检出并行提交体检 | `git show --stat a0a6a48dc` + `git diff --name-only 5a524b8eb HEAD` | a0a6a48dc = 10 文件全 web（providers.ts + 8 locale + CredsTab.vue），163 插入零删除；fa1124b18 = App.vue/RequestLogsView 修复（并行会话审计中途提交）；均零触碰 scope；与远端线文件重叠仅 App.vue/RequestLogsView（fa1124b18 即其收编/续修） |
| 10 | WIP 隔离 | `git status --porcelain` | 取证中动态 5→4→3 文件（并行会话逐个收编），落档时余 3：VERSION / version.json / web/public/version.json（seq-2101 部署簿记 WIP，指向 fa1124b1）——并行会话部署流程在途产物，本轮不动、不入线 |
| 11 | CJK 收口保持 | `rg -l '[\p{Han}]' domains/freediscovery/*.go` + admin 两文件 | 仅 `url_safety_test.go` fixture；admin 零命中 |
| 12 | gofmt | `gofmt -l`（scope 文件） | 空 |
| 13 | build / vet | `go build/vet ./admin/... ./domains/freediscovery/...` | 均通过 |
| 14 | 全量回归 | `go test -count=1 ./domains/freediscovery/ ./admin/` | ok（freediscovery 2.76s；admin 66.5s）。Go 树在全区间恒等（#8/#9），证据对合并后 tip 完全覆盖 |
| 15 | ④ :8782 身份（2098 已知好） | `docker cp` + 宿主 `go version -m`（停止前容器）+ healthz/readyz | `vcs.revision=a6b535dab… +dirty`、version.json seq 2098，与 R20 §二 #14 一致；恢复服务后 healthz `2.5.4-a6b535da-…-2098`、readyz db 1.7ms + redis 1.2ms `status: ready` |
| 16 | ④ 零 Go 变更证明 | `git diff --name-only a6b535dab 5a524b8eb \| grep -E '\.go$\|go\.mod$\|go\.sum$'` | 零命中（增量 = 并行前端线纯 web + docs/version；R20 的"纯 docs/version"表述仅对其当时基线 ede6589fd 成立，本轮以"零 Go 变更"为准） |
| 17 | ④ :8781 新构建身份（2101） | 同 #15 方法（`llm-gateway-local-8781`） | `vcs.revision=fa1124b181cce1016d62c8c9547b8696ea533beb +dirty`、version.json seq 2101——即并行会话本检出提交 fa1124b18；healthz `2.5.4-fa1124b1-…-2101` |
| 18 | ④ 实时 404 探针（双实例） | admin 登录（`POST /api/auth/token`）→ `GET /api/free-discovery/tasks/999999` | **:8782（2098）与 :8781（2101）均 HTTP 404** `{"error":{"detail":"freediscovery: task not found (id 999999)"}}`——契约实测成立且预验证下一次部署；+dirty 双实例均按 R20 §六 #3 协议以行为探针补证 |

## 三、改动文件

- `docs/audit/2026-09-13-r21-freediscovery-routine-audit.md`（本文；因审计
  期间远端 tip 三连跳与并行部署 settle，落档前按最终事实整体刷新并
  amend 一次）。
- **零代码改动**；**版本 bump 缺席**：origin 已随 087fba395 落 seq 2100
  （git_sha 记 1822ebdf，"构建漂移同步"），本检出 WIP 另持 seq 2101
  （fa1124b1）部署簿记在途——本轮任何版本文件写入都会覆盖并行会话的
  活跃簿记，故跳过；R22 从实际落档值续。
- **收束方法（本轮新协议，共享检出有活跃并行会话时适用）**：不动共享
  WIP、不执行 stash 配方（并行会话分钟级触碰文件/提交，stash 窗口碰撞
  风险实存，且本轮实证其会话内直接提交 fa1124b18）——`git add` 显式
  路径仅本文 → `git worktree add --detach /tmp/r21-push` → worktree 内
  fetch + `git rebase origin/main`（仅 replay 本文提交；a0a6a48dc、
  fa1124b18 属并行线，由其自行推送）→ `git push origin HEAD:main` →
  `git worktree remove`。共享检出的 WIP 与分支指针全程未被触碰。

## 四、遗留风险

1. **templates GET list 直写 500（LOW 观察，维持）**：#7 复核 `List` 仍无
   sentinel，豁免继续成立。判据不变：`List` 引入任何 sentinel 时必须先改
   走 fdStatusFor。
2. **2101 cutover 待观察（R22 首项）**：取证收尾时 :8782 仍服务 2098、
   :8781 预热 2101，切换由并行会话掌控。R22 需核验 :8782 最终服务版本 +
   `go version -m` 身份 + 探针复跑（本轮已在 :8781 预验证）。注意：若并
   行会话推送前 rebase 重写 fa1124b18，已部署 2101 的 vcs.revision 将指
   向悬空 sha，版本串需按映射核对。
3. **本检出积压**：a0a6a48dc + fa1124b18 仍未推送（并行线持有）；版本
   WIP（seq 2101）未提交——均为并行会话在途工作，勿代管。
4. **`errors.Is` vs `==`**：domain 层多处仍用 `err == sql.ErrNoRows`（R18
   §四.2 遗留，维持原样）。
5. **归档可信度**：原则继续适用——"已关闭"只是线索，代码现状才是证据；
   本轮 7/7 逐条 rg 核销即为执行样例。

## 五、下一轮提示词

> 请根据 docs/audit/2026-09-13-r21-freediscovery-routine-audit.md 与
> memory 状态，对 FreeDiscovery 做例行审计（R22），区间基线为本轮落档
> 提交（fetch 时 `git rev-parse origin/main`）：
> ① 契约逐条核对——R21 §二 表中 6 项 + LOW 观察（templates list :76，
>   豁免判据 #7）逐条 rg 核销，勿信任"已关闭"声明；
> ② 区间净 diff——`git diff <基线> <新tip> -- admin/free_discovery.go
>   admin/free_discovery_test.go domains/freediscovery/
>   web/src/api/free-discovery.ts`，merge 勿用 `git show --stat` 下结论；
>   入线先体检 `.go`/`go.mod`/`go.sum`（未触碰则 Go 门禁证据延续）；
> ③ 门禁——CJK 仅 url_safety_test.go fixture；gofmt/build/vet/
>   test -count=1 全绿；
> ④ 部署身份（R22 首项，R21 遗留 #2）——核验 :8782 cutover 终态：当前
>   服务版本串 + docker cp 出二进制用宿主 `go version -m` 核验
>   vcs.revision + 复跑 404 探针（`POST /api/auth/token` 登录 →
>   `GET /api/free-discovery/tasks/999999` → 404 精确 detail）。+dirty
>   需行为探针补证；若 fa1124b18 已被并行 rebase 重写，注意悬空 sha 映射。
> 推送遇拒：fetch 体检 + `git reflog -5` 后显式 `git pull --rebase origin
> main`；共享检出有活跃并行会话时优先 R21 §三的 worktree 零侵入收束。
> 版本 bump 从实际落档值续（R21 因 seq-2100/2101 簿记在途而缺席）。
