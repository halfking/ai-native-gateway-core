# R24 — FreeDiscovery 例行审计（2026-09-14）

## 一、结论

**基线与新 tip（fetch 后实测，不采信文档历史 tip）**：`git fetch origin main`
后 `origin/main = bee319a8f`；R24 取证区间为 **`d4f433931`（R23 取证
tip）.. `bee319a8f`**（R23 落档后的 docs 提交——本地
`93f44df5e`/`e38559ec4`/`13ed52e52` 与远端重复的
`03e79e2c9`/`604ffe482`——均不触碰 scope，两个起点的 scope diff 已验证
等价）。审计进行中**并行会话在同一检出持续推进**：本地 HEAD 先经 merge
到 `67336292e`（工作树曾在两次 rg 之间翻转，`git reflog` 证实），本文
取证即以 `bee319a8f` tree 为准；落档前 origin/main 又前进至
`fd61c26a2`（本地 HEAD 为其祖先，fast-forward 收口），落档前增量见下节。

**② scope diff 本轮不再为空**：probe recovery 收口波及 FreeDiscovery，
4 条 scope 路径 4 文件 **+139/−4**：

1. `domains/freediscovery/template_manager.go`（+38/−4）：新增 sentinel
   `ErrInvalidTenantID`；`setTenantTx` 对非法 tenant_id 由"静默重映射到
   `default`"改为**拒绝**（R20 §二.5 P2 收口——跨租户数据混合风险）；
   新增 `isValidTenantID` 白名单（`[A-Za-z0-9_-]{1,64}`）；`escapeTenantID`
   保留兼容（rg 证实已无生产调用方，仅自身测试）。
2. `domains/freediscovery/template_manager_test.go`（+57）：
   `TestIsValidTenantID`（13 用例）+ `TestSetTenantTx_RejectsInvalidTenantID`
   （guard 先于 ExecContext 生效）。
3. `admin/free_discovery.go`（+44）：新增 bg worker 访问器
   `FreeDiscoveryEngine`/`FreeDiscoveryTemplates`、
   `ScanSchedulerStatusProvider` + `SetScanSchedulerStatus` +
   `handleFreeDiscoveryScanSchedulerStatus`（未接线时 503）——即 :74 起
   42 行插入块（全文件行号 +42 偏移主因）；`fdStatusFor` 在模糊兜底**之前**
   新增 `ErrInvalidTenantID → 400` 组。
4. `admin/free_discovery_test.go`（+4）：`TestFDStatusFor` 补
   `ErrInvalidTenantID → 400` 断言。

配套接线（scope 外但契约相关）：`admin/handler.go` 注册
`GET /api/free-discovery/scan-scheduler/status`（`h.admin(...)` 鉴权包装，
2026-09-14 注释）；`admin/routing.go` 改动与本 scope 无关（credential
force-enable probe 字段）。**改动评审通过**：拒绝路径先校验后内插（白名单
保证内联安全）；新 400 组位于模糊兜底前（specific-before-fuzzy）；新端点
带 admin 鉴权。

**落档前增量（`bee319a8f..fd61c26a2`，4 commit，已按同口径补审）**：
`870fac658` fix(bg) 惰性 plan 下限边界（bg 域，scope 外）；
`4ed97a213` fix(migrations,probeutil) migration 703 入升级通道
（scope 外）；`ccedc986c` fix(freediscovery) **CJK gate 恢复**——scope
内仅注释级改动（`§二` → `§2` 三处，行数不变，本文全部契约行号在
`fd61c26a2` 继续有效）；`fd61c26a2` docs(audit) R23.1 修正轮。增量后
go.mod/go.sum 仍零触碰；门禁在落档 tip 复跑（见 #13–#17）。

**① 7/7 契约按新行号逐条 rg 核销**（R23 行号 +42，`fdStatusFor` 尾部 +44），
详见 §二 #1–#7。`web/src/api/free-discovery.ts` 本轮零改动，前端契约注释
（:12–20）继续吻合（新 400 组落在既有"400 校验"行内，无需改注释）。

**③ 门禁**：go.mod/go.sum 区间**未触碰**（依赖门禁证据延续）；gofmt 空；
build/vet 通过；`go test -count=1` 全绿（freediscovery 2.596s；
admin 66.362s）；三个新增/相关测试点名单跑 PASS。**CJK 门禁在取证 tip
（bee319a8f）出现 LOW 偏差**：R23 时代"仅 url_safety_test.go fixture"
被打破——3 处 `§二` 审计交叉引用记号（`admin/free_discovery_test.go:331`、
`template_manager.go:405`、`template_manager_test.go:58`），内容为
"R20 §二.5/P2" 引用而非中文散文。**落档前已闭环**：origin/main 后续提交
`ccedc986c fix(freediscovery): restore CJK gate — ASCII section refs in
R20 comments` 将其规范化为 `§2`，本检出 fast-forward 至 `fd61c26a2` 后
复验 **CJK 恢复零命中**（仅 url_safety_test.go fixture），gofmt/build/
vet/test 在落档 tip 复跑全绿（freediscovery 2.380s；admin 66.300s）。

**④ 部署身份**：:8782 容器 `llm-gateway-local-8782`（0680b4667452）
Up 10 hours，镜像 `kx-llm-gateway-local:2.5.4.2101`；healthz 返回
`2.5.4-c4da30d5-20260913-2101`（git_sha `c4da30d5`、build_seq 2101）——
**区间内无重新部署，与 R23 终态相同**。容器二进制 `go version -m`：
go1.27.1、`mod …(devel)`、**无 vcs.\* 字段**（与 R20/R21/R23 一致，按
指示记录为 provenance 遗留，不断言构建根因）；本轮新增取证基线：二进制
sha256 `56b50a53062fbbf1ec19826a4791b3b10304fd49785e50173794777f2dd0b4d5`、
镜像 `sha256:490f603aeeb8…f46d22f`（本地镜像，无 RepoDigests）。行为侧
交叉验证：**已部署二进制不含本轮新路由**（GET
`/api/free-discovery/scan-scheduler/status` → 404 page not found），
证实部署早于本轮 scope 改动——healthz git_sha 是部署元数据、不是二进制
provenance 的结论再次成立。**404 探针通过**：POST `/api/auth/token`
（容器 env 内受控凭证，登录 users 表 admin/super_admin/id=41；.env.local
两组密码均不可用——users 表哈希只与容器 env 值匹配）→ GET
`/api/free-discovery/tasks/999999` → **HTTP 404 + detail
`"freediscovery: task not found (id 999999)"`**。:8781 无 LISTEN。

**版本 bump 缺席（维持）**：本地 `VERSION`/`version.json`/
`web/public/version.json` 仍持 seq-2100（`1822ebdf`），实际部署元数据
seq-2101 对应 `c4da30d5d`；区间内无部署簿记 commit 入线，跳过 bump，
**下一轮从实际部署元数据 seq-2101 续号（seq-2102）**。

区间构成：`d4f433931..bee319a8f` 共 76 文件（tree-to-tree
`git diff --name-only` 计），`.go` 过滤 38 文件中 34 个位于 scope 之外
（bg/credential_probe_v2、bg/scan_scheduler、bg/periodic_quota_probe、
domains/streaming/*、errorsx/、internal/ir/、upstream/、installer/、
sql/migrations 测试等，对应 probe recovery 收口（`f8322dc04`、
`e352c670d`、`4ea8ab0af`）、streaming integrity（`cefea8791`）、
web 修正（`33066e289`）及 docs）。本检出工作树 clean。

## 二、验证项与证据（命令与结果）

| # | 验证项 | 命令 | 结果 |
|---|---|---|---|
| 1 | 发现 1：import handler 走 fdStatusFor | `rg -n 'fdStatusFor\(err\)' admin/free_discovery.go` | 9 处命中（:138/:164/:180/:190/:359/:380/:408/:436/:472），import handler :446→:472 在列；行号 = R23 +42 |
| 2 | 发现 2：list 端点走 fdStatusFor；templates GET 直写 | `rg -n 'StatusInternalServerError' admin/free_discovery.go` + handler 定位 | tasks list :380、taskResults :436 均 `fdStatusFor`；templates GET list **:118 仍直写**（原 :76）→ LOW 观察不变（见 #7） |
| 3 | 发现 3：GetTask sentinel 全链路 | `rg -n 'ErrTaskNotFound' domains/ admin/` | 定义 `template_manager.go:32`；GetTask wrap `discovery_engine.go:410`；fdStatusFor 404 组 :502-503；测试 engine :301、admin :243/:257（+ wrapped 用例 :244） |
| 4 | import 契约（404/409） | `rg -n 'ErrImportTask' admin/ domains/` | sentinel `import_service.go:48/:53`；fdStatusFor 404 组 :502、409 组 :506-507；handler :472 接线 |
| 5 | R18 收尾：fdRequest `/tasks/{id}` 路由 | `sed -n '60,86p' admin/free_discovery_test.go` | switch 排序不变（/scan → /results 后缀 → /tasks 精确 → /import → /tasks/ → /templates → default）；本区间测试文件改动仅在 :328+ |
| 6 | R18 收尾：前端契约行 | `git diff d4f433931 bee319a8f -- web/src/api/free-discovery.ts` | **零改动**；:12–20 注释与 fdStatusFor 现状吻合（新 400 组属既有"400 校验"行） |
| 7 | LOW 观察豁免复核 | `awk '155..193p' domains/freediscovery/template_manager.go` | `TemplateManager.List` :155-191 仍只返回包装后的 db 错误，无任何 sentinel → templates GET :118 直写 500 与 default-500 行为等价的豁免继续成立（新 sentinel `ErrInvalidTenantID` 属 setTenantTx，与 List 无关） |
| 8 | 本轮新增：ErrInvalidTenantID 链路 | scope diff + `rg -n 'ErrInvalidTenantID' admin/ domains/` | 定义 `template_manager.go:395`；setTenantTx 拒绝路径 :403；fdStatusFor 400 组 :509；测试 tm_test（13 用例）+ fd_test :331-334；**R20 §二.5 P2 真实关闭** |
| 9 | 本轮新增：scan-scheduler 状态端点 | `git diff … -- admin/handler.go` + 路由 rg | `GET /api/free-discovery/scan-scheduler/status` 以 `h.admin(...)` 注册；未接线 503；已部署二进制无此路由（404 page not found）→ 部署早于本轮改动 |
| 10 | 区间净 diff（tree-to-tree） | `git diff --name-status d4f433931 bee319a8f -- <4 scope 路径>` | **4 文件 +139/−4**（见 §一）；`13ed52e52..bee319a8f` 同结果（docs 提交不触 scope）；总区间 76 文件，.go 38 中 34 个在 scope 外 |
| 11 | go.mod / go.sum | `git diff --stat d4f433931 bee319a8f -- go.mod go.sum` | **空** — 依赖未触碰，门禁证据延续 |
| 12 | WIP 隔离 | `git status --porcelain` | 空（clean）；审计中段并行会话 merge 致工作树瞬时翻转，reflog 证实后以 bee319a8f tree 为准 |
| 13 | CJK | `rg -l '[\p{Han}]' domains/freediscovery/*.go admin/free_discovery*.go` | 取证 tip（bee319a8f）：4 文件——url_safety_test.go（既有 fixture）+ **3 处 `§二` 记号**（:331/:405/:58，LOW 偏差）；落档 tip（fd61c26a2，`ccedc986c` 修复后）：**恢复仅 url_safety_test.go fixture**，`§2` 已就位（同行号） |
| 14 | gofmt | `gofmt -l`（scope 文件） | 空 |
| 15 | build / vet | `go build/vet ./admin/... ./domains/freediscovery/...` | 均通过 |
| 16 | 全量回归 | `go test -count=1 ./domains/freediscovery/ ./admin/` | bee319a8f：ok（2.596s；66.362s）；fd61c26a2 复跑：ok（2.380s；66.300s） |
| 17 | 新增测试点名单跑 | `go test -count=1 -v -run 'TestIsValidTenantID\|TestSetTenantTx_RejectsInvalidTenantID\|TestFDStatusFor' …` | 3/3 PASS |
| 18 | ④ Docker/容器终态 | `docker ps --filter "publish=8782"` + healthz | 容器 Up 10 hours，镜像 `kx-llm-gateway-local:2.5.4.2101`；healthz `2.5.4-c4da30d5-20260913-2101`（seq-2101，与 R23 终态一致，区间内无重新部署）；:8781 无 LISTEN |
| 19 | ④ 二进制 provenance | `docker cp …/gateway /tmp` + `go version -m` + `shasum -a 256` + `docker inspect` | go1.27.1、`(devel)`、**无 vcs.\***（provenance 遗留维持，不断言构建根因）；二进制 sha256 `56b50a53…ddb4d5`；镜像 `sha256:490f603a…f46d22f`（无 RepoDigests）；本轮新增路由在部署二进制上 404 → 部署早于 scope 改动 |
| 20 | ④ 实时 404 探针 | `POST /api/auth/token`（容器 env 受控凭证）+ `GET /api/free-discovery/tasks/999999` | 登录成功（users 表 admin / super_admin / id=41；.env.local 值不可用，容器 env 值有效）；GET → **HTTP 404** + `{"error":{"detail":"freediscovery: task not found (id 999999)"}}` — **探针通过**；审计文档不记录凭证 |
| 21 | ④ 版本簿记 | `cat VERSION version.json web/public/version.json` | 均持 `2.5.4-1822ebdf-20260912-2100` — 与部署元数据 seq-2101 不同步；bump 缺席，下一轮从 seq-2101 续（seq-2102） |

## 三、改动文件

- `docs/audit/2026-09-14-r24-freediscovery-routine-audit.md`（本文）。
- **零代码改动**（本轮 scope 变更来自并行线的 probe recovery 收口，审计
  评审通过、无需修）；**版本 bump 缺席**（同 R21–R23，部署簿记未入线），
  下一轮从实际部署元数据 seq-2101 续号。
- **收束方法**：工作树 clean，显式路径提交本文；推送遇拒按 memory 流程：
  fetch 体检来量是否触 scope/Go 门禁 → `git reflog -5` → 显式
  `git pull --rebase origin main` → push；并行会话活跃时改用 worktree
  zero-invasive push（commit by explicit path → detach worktree →
  cherry-pick → push）。

## 四、遗留风险

1. **templates GET list 直写 500（LOW 观察，维持）**：行号 :76 → **:118**
   （+42 偏移）。#7 复核 `List` 仍无 sentinel，豁免继续成立；判据不变：
   `List` 引入任何 sentinel 时必须先改走 fdStatusFor。
2. **二进制 provenance 未闭环（P1，维持）**：`go version -m` 显示
   `(devel)` 且无 vcs.\* 字段（R20/R21/R23/R24 四轮一致）。healthz
   `git_sha`（seq-2101 ↔ `c4da30d5d`）是运行时部署元数据；本轮补记
   二进制 sha256 与镜像 digest 作为未来同源校验基线；"部署不含新路由"
   的行为侧交叉验证可作为补充证据模式复用。不断言构建根因。
3. **版本 bump 缺席（低优先级，维持）**：部署元数据 seq-2101 未回填簿记；
   下一轮部署从 seq-2101 续号（seq-2102），勿从本地 VERSION 的 seq-2100 续。
4. **CJK 门禁（本轮发现、落档前已闭环）**：取证 tip（bee319a8f）曾有 3 处
   `§二` 审计引用记号进入 Go 注释（并行线引入）；`ccedc986c` 已规范化为
   `§2`，fd61c26a2 复验零命中。R25 起恢复"零命中（仅 url_safety_test.go
   fixture）"口径例行检查。
5. **`errors.Is` vs `==`**：domain 层多处仍用 `err == sql.ErrNoRows`（R18
   §四.2 遗留，维持原样）。
6. **`escapeTenantID` 成为死代码（info）**：生产调用方已清零（仅测试），
   注释声称"为仍在拼接的调用方保留"与现状不符；可在后续清扫轮删除。
7. **部署滞后（info）**：seq-2101 部署早于本轮 scope 变更（新 sentinel、
   新端点均未上线）；例行审计不触发部署，待并行线下一次部署（应为
   seq-2102）后在新一轮复验 `ErrInvalidTenantID → 400` 的运行时行为。
8. **归档可信度**：原则继续适用——"已关闭"只是线索，代码现状才是证据；
   本轮 7/7 逐条核销 + 2 项新增链路评审即为执行样例。

## 五、下一轮提示词

> 请根据 docs/audit/2026-09-14-r24-freediscovery-routine-audit.md 与
> memory 状态，对 FreeDiscovery 做例行审计（R25）。先 `git fetch origin
> main` + `git rev-parse origin/main` 取实际 tip，勿信本文历史 tip；
> 区间基线为本轮落档提交：
> ① 契约逐条核对——R24 §二 表 8 项契约（含新增 #8 ErrInvalidTenantID
>   链路、#9 scan-scheduler 端点）+ LOW 观察（templates list 直写 :118，
>   豁免判据 #7 `List` 无 sentinel）逐条 rg 核销；注意行号基线已含 +42
>   偏移；
> ② 区间净 diff——`git diff <基线> <新tip> -- admin/free_discovery.go
>   admin/free_discovery_test.go domains/freediscovery/
>   web/src/api/free-discovery.ts`，merge 勿用 `git show --stat` 下结论；
>   入线先体检 `.go`/`go.mod`/`go.sum`；
> ③ 门禁——gofmt/build/vet/`test -count=1` 全绿；CJK 检查零命中口径
>   （`ccedc986c` 修复后应保持，仅 url_safety_test.go fixture）；
> ④ 部署身份——若并行线已部署 seq-2102：验证 healthz git_sha 对应 commit、
>   复跑 404 探针（容器 env 受控凭证 → GET tasks/999999 → 精确 404
>   detail）、并对**已部署二进制**验证 `ErrInvalidTenantID → 400` 运行时
>   行为与新端点上线（遗留 #7）；若未部署，照 R24 记录"部署早于改动"；
>   二进制无 vcs.\* 仍记 provenance 遗留，可用二进制 sha256/镜像 digest
>   同源比对；healthz git_sha 不单独当二进制 provenance；审计文档不得
>   记录任何密码（登录用容器 env 值，.env.local 两组值均无效）；
> ⑤ 版本 bump 从实际部署元数据续号（seq-2102 起）；MiniMax token_plan
>   实测与 252/生产验证分别记录，不得互相替代；
> ⑥ 推送遇拒：fetch 体检 + `git reflog -5`，并行会话活跃时用 worktree
>   zero-invasive push（见 memory local-dev-environment）。
