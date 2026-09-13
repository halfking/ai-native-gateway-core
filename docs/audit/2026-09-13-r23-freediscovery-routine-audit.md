# R23 — FreeDiscovery 例行审计（2026-09-13）

## 一、结论

**当前仓库状态（复核时）**：`HEAD=origin/main=93f44df5e`，工作树 clean；
`VERSION`、`version.json`、`web/public/version.json` 的内容仍为 seq-2100，
但不再是未提交 WIP。R23 下面的区间结论仍指向当时取证 tip；截至当前
`origin/main`，FreeDiscovery scope 相对 `d4f433931` 仍为零差异。

**区间 4cfaacdf3..d4f433931（取证时 origin/main tip）：FreeDiscovery
四条 scope 路径 diff 为空。R22 §二 表中 6 项契约 + LOW 观察
（templates GET list :76，豁免判据 #7）逐条以代码现状 rg 核销（不信任任何
"已关闭"声明），全部行号与 R22 记录逐一吻合，维持"真实关闭"/"行为等价
豁免"。静态门禁与全量测试全绿。**

**④ 部署身份（R22 遗留 #2，本轮闭环）：Docker Desktop 已恢复健康**
（daemon API 正常，`docker ps`/`docker cp` 可达）。:8782 容器
`llm-gateway-local-8782` 运行中（Up 2 hours），healthz 返回版本串
`2.5.4-c4da30d5-20260913-2101`，对应运行时版本元数据中的 git sha
`c4da30d5d`（区间内第 4 个 commit，balance-floor guard 部署验证闭环 handoff）。
**但容器二进制 `go version -m` 显示 `(devel)` 且无 vcs.* 字段**；这只能
确认当前观测到的构建结果，不能单独证明二进制 provenance，也不足以断言
唯一构建根因。healthz 的 `git_sha` 是运行时 `version.json` 部署元数据，
不是二进制 provenance；后续需以二进制 sha256↔构建产物或镜像 digest
补齐同源校验。**404 探针复跑通过**（POST `/api/auth/token` 使用受控
本地凭证成功 → GET
`/api/free-discovery/tasks/999999` → HTTP 404 + detail `"freediscovery:
task not found (id 999999)"`），GetTask sentinel 全链路行为验证绿灯。
本地 VERSION 文件持 `2.5.4-1822ebdf-20260912-2100` 未同步（seq-2100），
版本 bump 仍缺席（与 R21/R22 同因，部署簿记未 settle 入主线）；实际
部署元数据版本 seq-2101 对应 `c4da30d5d`，距 seq-2100 基线 `1822ebdfb`
相隔 40 个 commit（均在本区间 4cfaacdf3..d4f433931 之前）——说明并行线在
R22 期间完成 seq-2101 部署元数据对应的容器重启，但未补推部署簿记 commit 入主线。

区间构成：origin 自 4cfaacdf3（R22 落档 tip，`b62a28c52` R22 audit 文档的
父 commit）新增 15 个 commit 至 `d4f433931`（若把基线端点计入则涉及
16 个 commit；14:28+0800，handoff），
含 balance-floor guard 一组 7 修（`14b405807` migration 701 form、
`4d14b615b` planTypes、`66a9f8e6a` 清下限回池、`77956aeb5` changelog、
`5df12411f` changelog 闭环、`c4da30d5d` handoff）、anthropic 死桥退役
（`97aa179ab` refactor）、R22 审计 balance-floor guard 残留 #1 修复
（`de9c19f75` recordBalanceCheck 删除）、web 暗色皮肤 3 修
（`14b405807`、`c0b6e6b2b`、`b5924e0b3`）+ 3 个 handoff/docs/audit
文档，**`.go$|go.mod|go.sum` 过滤 23 个 Go 文件全部位于 FreeDiscovery
scope 之外**（bg/balance_floor_guard、bg/balance_quota_probe、
domains/transformation/anthropic/{capturer_test,passthrough_stream*,
to_openai_stream*,stream_support}）— **4 条 FreeDiscovery scope 路径
diff 为空**，与 R22 状况一致。本检出 WIP 为空（干净工作树），无残留部署
簿记。

## 二、验证项与证据（命令与结果）

| # | 验证项 | 命令 | 结果 |
|---|---|---|---|
| 1 | 发现 1：import handler 走 fdStatusFor | `rg -n 'fdStatusFor\(err\)' admin/free_discovery.go` | 9 处命中（:96/:122/:138/:148/:317/:338/:366/:394/:430），`handleFreeDiscoveryImport` :430 在列；行号与 R22 相同 |
| 2 | 发现 2：list 端点走 fdStatusFor | `sed -n '70,80p' admin/free_discovery.go` | tasks list :338、taskResults :394 均 `fdStatusFor`；templates GET list **:76 仍直写** `http.StatusInternalServerError` → LOW 观察不变（见 #7） |
| 3 | 发现 3：GetTask sentinel 全链路 | `rg -n 'ErrTaskNotFound' domains/ admin/` | 定义 `template_manager.go:32`；GetTask wrap `discovery_engine.go:410`；fdStatusFor 404 组 :461；三测试在位（engine :301、admin :243/:257）；行号与 R22 相同 |
| 4 | import 契约（404/409） | `rg -n 'ErrImportTask' admin/ domains/` | sentinel `import_service.go:48` ErrImportTaskNotFound；fdStatusFor 404 组 :460、409 组 :464；handler :430 接线；行号与 R22 相同 |
| 5 | R18 收尾：fdRequest `/tasks/{id}` 路由 | `sed -n '60,86p' admin/free_discovery_test.go` | switch 排序 `/scan` → `/results` 后缀 → `/tasks` 精确 → `/import` → `/tasks/` → `/templates` → default，注释完整；行号与 R22 相同 |
| 6 | R18 收尾：前端契约行 | `sed -n '12,20p' web/src/api/free-discovery.ts` | :14-19 块状注释含 `ErrTaskNotFound` 404 行、ErrImportTaskNotFound、409 三组；行号与 R22 相同 |
| 7 | LOW 观察豁免复核 | `sed -n '150,200p' domains/freediscovery/template_manager.go` | `TemplateManager.List` :155-191 仍只返回包装后的 db 错误，**无任何 sentinel** → templates GET :76 直写 500 与 fdStatusFor default-500 行为等价的豁免条件继续成立 |
| 8 | 区间净 diff（tree-to-tree，merge 勿用 show --stat） | `git diff --name-only 4cfaacdf3 d4f433931 -- admin/free_discovery.go admin/free_discovery_test.go domains/freediscovery/ web/src/api/free-discovery.ts` | 空输出；**4 条 FreeDiscovery scope 路径 diff 为空**；总区间 49 文件（按 `git diff --name-only` 计），`.go$/go.mod/go.sum` 过滤 23 文件全部位于 scope 之外 |
| 9 | WIP 隔离 | `git status --porcelain` | 空输出 — **干净工作树**，R22 残留的 seq-2100 部署簿记 WIP（4 文件）已清除 |
| 10 | CJK 收口保持 | `rg -l '[\p{Han}]' domains/freediscovery/*.go admin/free_discovery.go admin/free_discovery_test.go` | 仅 `url_safety_test.go` fixture；admin 两文件零命中 |
| 11 | gofmt | `gofmt -l`（scope 文件） | 空 |
| 12 | build / vet | `go build/vet ./admin/... ./domains/freediscovery/...` | 均通过（exit 0） |
| 13 | 全量回归 | `go test -count=1 ./domains/freediscovery/ ./admin/` | ok（freediscovery 2.622s；admin 66.606s） |
| 14 | ④ Docker daemon 健康 | `docker info` | 正常（Server: Containers 50 Running 47 Stopped 3 Images 347）— **daemon 已恢复**，R22 的 500 故障已解决 |
| 15 | ④ :8782 cutover 终态 — 端口可达性 | `docker ps --filter "publish=8782"` + `curl http://127.0.0.1:8782/healthz` | 容器 `llm-gateway-local-8782` (0680b4667452) Up 2 hours，端口 `127.0.0.1:8782->8782/tcp`；healthz 返回 `{"status":"ok","version":"2.5.4-c4da30d5-20260913-2101","git_sha":"c4da30d5","build_seq":2101,"build_date":"20260913","ready":true}` — **:8782 服务可达** |
| 16 | ④ :8782 cutover 终态 — 二进制身份 | `docker cp llm-gateway-local-8782:/opt/llm-gateway-go/gateway /tmp/gateway-8782-binary` + `go version -m /tmp/gateway-8782-binary` | 容器 entrypoint `/opt/llm-gateway-go/gateway`；`go version -m` 显示 `go1.27.1`、`mod github.com/kaixuan/llm-gateway-go (devel)`，**无 vcs.* 字段**（build 时未注入 VCS 元数据，与 R20/R21/memory 记录一致）— **无法从二进制直接取证 vcs.revision** |
| 17 | ④ :8782 cutover 终态 — 版本串校准 | healthz `git_sha` 字段 + `git log --oneline --all \| grep c4da30d5` | healthz 报告运行时元数据 `git_sha: c4da30d5`，对应 `c4da30d5d docs(handoff): balance-floor guard 部署验证闭环`（区间内第 4 个 commit，1822ebdfb seq-2100 基线后 +40 commit）— **部署元数据版本 seq-2101 对应 c4da30d5d；不等同于二进制 provenance** |
| 18 | ④ 实时 404 探针 | `POST /api/auth/token`（受控凭证） + `GET /api/free-discovery/tasks/999999` | 登录成功（access_token + user role super_admin）；GET tasks/999999 → **HTTP 404** + `{"error":{"detail":"freediscovery: task not found (id 999999)"}}` — **404 探针通过**，GetTask sentinel 全链路行为验证绿灯；审计文档不记录凭证 |
| 19 | ④ :8781 清理 | `docker ps --filter "publish=8781"` | 空输出 — :8781 预热实例已清理（R22 已记录 2101 收束） |
| 20 | ④ 本地 VERSION 同步 | `cat VERSION` | `2.5.4-1822ebdf-20260912-2100` — 本地 VERSION 文件仍持 seq-2100，与部署元数据版本 seq-2101 不同步（版本 bump 缺席，与 R21/R22 同因） |

## 三、改动文件

- `docs/audit/2026-09-13-r23-freediscovery-routine-audit.md`（本文）。
- **零代码改动**；**版本 bump 缺席**：区间内无 deploy/version 簿记 commit
  入线（并行线已完成 seq-2101 部署但未补推簿记），本检出 WIP 为空——与
  R21/R22 同因，跳过；下一轮从实际落档值续。
- **收束方法**：本检出工作树干净（无 WIP），直接在主线追加本文：
  `git add docs/audit/2026-09-13-r23-freediscovery-routine-audit.md` →
  `git commit -m "docs(audit): R23 FreeDiscovery routine audit"` →
  `git pull --rebase origin main`（若需要）→ `git push origin main`。

## 四、遗留风险

1. **templates GET list 直写 500（LOW 观察，维持）**：#7 复核 `List` 仍无
   sentinel，豁免继续成立。判据不变：`List` 引入任何 sentinel 时必须先改
   走 fdStatusFor。
2. **二进制 provenance 未闭环（P1，维持）**：容器二进制 `go version -m`
   显示 `(devel)` 且无 vcs.* 字段。healthz `git_sha` 来自运行时
   `version.json`，只能证明部署元数据（seq-2101 对应 `c4da30d5d`），不能
   替代二进制身份；404 探针仅证明运行行为。后续需补二进制 sha256 与构建
   产物/镜像 digest 的同源校验，或让构建产物包含可核验的 VCS 元数据。
3. **版本 bump 缺席（低优先级，维持）**：seq-2101 部署元数据已落地但未补推簿记
   commit 入主线，本地 VERSION 文件仍持 seq-2100。下一轮部署时从实际
   部署元数据 seq-2101 续号（seq-2102）。
4. **`errors.Is` vs `==`**：domain 层多处仍用 `err == sql.ErrNoRows`（R18
   §四.2 遗留，维持原样）。
5. **归档可信度**：原则继续适用——"已关闭"只是线索，代码现状才是证据；
   本轮 7/7 逐条 rg 核销即为执行样例。

## 五、下一轮提示词

> 请根据 docs/audit/2026-09-13-r23-freediscovery-routine-audit.md 与
> memory 状态，对 FreeDiscovery 做例行审计（R24），区间基线为本轮落档
> 提交（本次修正落档 tip 为 `03e79e2c9`；fetch 时仍须执行
> `git rev-parse origin/main`）：
> ① 契约逐条核对——R23 §二 表中 6 项 + LOW 观察（templates list :76，
>   豁免判据 #7）逐条 rg 核销，勿信任"已关闭"声明；
> ② 区间净 diff——`git diff <基线> <新tip> -- admin/free_discovery.go
>   admin/free_discovery_test.go domains/freediscovery/
>   web/src/api/free-discovery.ts`，merge 勿用 `git show --stat` 下结论；
>   入线先体检 `.go`/`go.mod`/`go.sum`（未触碰则 Go 门禁证据延续）；
> ③ 门禁——CJK 仅 url_safety_test.go fixture；gofmt/build/vet/
>   test -count=1 全绿；

> ④ 部署身份——核验 :8782 cutover 终态：当前服务版本串（healthz 的
>   `version.json` 元数据）+ `docker ps` 确认 LISTEN + 复跑 404 探针
>   （`POST /api/auth/token` 使用受控凭证 → `GET
>   /api/free-discovery/tasks/999999` → 404 精确 detail）。容器二进制
>   `go version -m` 若无 vcs.*，不得把 healthz git_sha 当作二进制
>   provenance；需补 binary sha256↔构建产物或镜像 digest 同源校验。
> 推送遇拒：fetch 体检 + `git reflog -5` 后显式 `git pull --rebase origin
> main`。版本 bump 从实际落档值续（若并行线补推 seq-2102 部署，以其为准；
> 否则跳过）。
