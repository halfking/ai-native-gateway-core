# R22 — FreeDiscovery 例行审计（2026-09-13）

## 一、结论

**区间 1e0f71c70..4cfaacdf3（取证时 origin/main tip）：FreeDiscovery
四条 scope 路径 diff 为空。R21 §二 表中 6 项契约 + LOW 观察
（templates GET list :76，豁免判据 #7）逐条以代码现状 rg 核销（不信任任何
"已关闭"声明），全部行号与 R21 记录逐一吻合，维持"真实关闭"/"行为等价
豁免"。静态门禁与全量测试全绿。**

**④ 部署身份（R21 遗留 #2）：本轮不可验证**——R21 取证收尾后并行线完成
2098 容器外部 SIGKILL（5a524b8eb handoff 已记）→ 2101 预热实例收束
（:8781 现已无 LISTEN）→ 当前 :8782 不再服务（curl healthz `000/empty`）；
Docker daemon 同时处于不健康状态（`docker info` API 返回 `500 Internal
Server Error`，`/containers/json` 同症），无法 `docker ps`/`docker cp`
取证容器二进制。区间内 git 上亦未携带新的 deploy 簿记 commit（最后一条
seq-2100 簿记 `087fba395` 仍为当前落档值），即并行线并未补推部署。
代码侧契约/门禁全绿，scope 路径在 34 个 Go 文件 delta 中保持零变更；
live 探针与 `go version -m` 身份核验待 Docker Desktop 恢复后由 R23
首项补做。

区间构成：origin 自 1e0f71c70（R21 落档首推 `44adf6106` 的本地 amend）
推进 18 个 commit 至 `4cfaacdf3`（10:26+0800，zcode），含并行线 R21
24h 修正轮审计（`e7a98ffff`）、R20 24h（`96b6b6a89`）、R16 P2-1 收口
（`36db8b222`）、streaming 三修（`227d9cc64`）、admin force-recover
/reset-quota/balance-floor 一组 5 修（`152195f25`/`362f1fb42`/
`aa75c1966`/`4a2462b7b`/`8f462f2c1`）、前端 P3-5/P5/审计门禁脚本
一组 4 修（`781fa7587`/`76306b145`/`9d5865ce0`/`54b60fbb6`/`dafcbc873`）
+ 4 个 handoff 文档/审计文档，**`.go$|go.mod|go.sum` 过滤 34 个 Go 文件
全部位于 FreeDiscovery scope 之外**（admin/diagnostics_credential、
admin/provider_cred_lifecycle、admin/provider_offer_force_recover、
bg/balance_floor_guard、bg/candidate_failure_monitor、db/db、
domains/credential/limiter、domains/sessiondigest/{digest,digest_test}、
domains/streaming/{anthropic_bridge, anthropic_bridge_test,
anthropic_stream, executors/executor_chat, responses_bridge,
responses_bridge_test, stream_holdback_regression_test}、
internal/ir/{anomaly_reporter, anomaly_test, extensions_restore,
ir_json_trip_test, qwen_content_test, response, response_protocols,
response_test, serialize_responses_extension_loss_test,
serialize_responses_stream_test, serialize_responses_test, stream,
tools_provider_specific_test, unknown_content_block_test}、
sql/migrations/startup/{695_…, baseline_ensure_functions_contract_test,
baseline_promote_functions_contract_test}）— **4 条 FreeDiscovery
scope 路径 diff 为空**，与 R21 状况一致；本检出 R21 残留的并行 WIP
（VERSION/version.json/web/public/{version,menu-config}.json 4 文件，
seq-2100）维持不变，无新增部署簿记 commit 入线。

## 二、验证项与证据（命令与结果）

| # | 验证项 | 命令 | 结果 |
|---|---|---|---|
| 1 | 发现 1：import handler 走 fdStatusFor | `rg -n 'fdStatusFor\(err\)' admin/free_discovery.go` + `rg -n 'handleFreeDiscoveryImport'` | `handleFreeDiscoveryImport`（:404）域错误路径 :430 `writeError(w, fdStatusFor(err), …)`；fdStatusFor 9 处命中均含 :430；回归测试 `TestFreeDiscovery_Import_StatusForSentinels`（test :211）在位 |
| 2 | 发现 2：list 端点走 fdStatusFor | 同上 + `sed -n '70,80p'` | tasks list :338、taskResults :394 均 `writeError(w, fdStatusFor(err), …)`；templates GET list **:76 仍直写** `writeError(w, http.StatusInternalServerError, …)` → LOW 观察不变（见 #7） |
| 3 | 发现 3：GetTask sentinel 全链路 | `rg -n 'ErrTaskNotFound' domains/ admin/` | 定义 `template_manager.go:32`；GetTask wrap `discovery_engine.go:410`（`%w (id %d)`）；fdStatusFor 404 组 :459-461（含 ErrTaskNotFound 在第三个 case）；三测试在位：`TestDiscoveryEngine_GetTask_NotFoundSentinel`（engine :290）、`TestFreeDiscovery_Task_StatusForSentinels`（admin :237）、`TestFreeDiscovery_Task_GetNotFound404_DBFault500`（admin :259）——行号与 R21 逐一吻合 |
| 4 | import 契约（404/409） | `rg -n 'ErrImportTask'` | sentinel `import_service.go:48` ErrImportTaskNotFound；fdStatusFor 404 组 :460、409 组 :464；handler :430 接线 |
| 5 | R18 收尾：fdRequest `/tasks/{id}` 路由 | `sed -n '60,86p' admin/free_discovery_test.go` | switch 排序 `/scan` → `/results` 后缀 → `/tasks` 精确 → `/import` → `/tasks/` → `/templates` → default，注释完整，404/500 子测试 :269-284 走通 |
| 6 | R18 收尾：前端契约行 | `sed -n '12,20p' web/src/api/free-discovery.ts` + `sed -n '212,220p' docs/freediscovery-configuration.md` | :14-19 块状注释含 `ErrTaskNotFound` 404 行、ErrImportTaskNotFound、409 三组；docs §4.4 :215 行原样（R21 所记 docs 文件名为 freediscovery-configuration.md） |
| 7 | LOW 观察豁免复核 | `sed -n '150,200p' domains/freediscovery/template_manager.go` | `TemplateManager.List` 仍只返回包装后的 db 错误（begin tx / setTenantTx / query / scan），**无任何 sentinel** → templates GET :76 直写 500 与 fdStatusFor default-500 行为等价的豁免条件继续成立 |
| 8 | 区间净 diff（tree-to-tree，merge 勿用 show --stat） | `git diff --name-only 1e0f71c70 4cfaacdf3 -- admin/free_discovery.go admin/free_discovery_test.go domains/freediscovery/ web/src/api/free-discovery.ts` | 空输出；**4 条 FreeDiscovery scope 路径 diff 为空**；总区间 126 文件、`.go$/go.mod/go.sum` 过滤 34 文件全部位于 scope 之外 |
| 9 | WIP 隔离 | `git status --porcelain` | 4 文件（VERSION/version.json/web/public/{version,menu-config}.json），seq-2100 部署簿记 WIP，与 R21 取证收尾时同状态——并行会话未补推部署（区间无 deploy/version commit） |
| 10 | CJK 收口保持 | `rg -l '[\p{Han}]' domains/freediscovery/*.go admin/free_discovery.go admin/free_discovery_test.go` | 仅 `url_safety_test.go` fixture；admin 两文件零命中 |
| 11 | gofmt | `gofmt -l`（scope 文件） | 空 |
| 12 | build / vet | `go build/vet ./admin/... ./domains/freediscovery/...` | 均通过（exit 0） |
| 13 | 全量回归 | `go test -count=1 ./domains/freediscovery/ ./admin/` | ok（freediscovery 2.51s；admin 136.4s） |
| 14 | ④ :8782 cutover 终态 — 端口可达性 | `curl -m 5 http://127.0.0.1:8782/healthz` + `lsof -nP -iTCP -sTCP:LISTEN \| grep -E '8781\|8782'` | `:8782`/`lsof` 无 gateway LISTEN（Docker Desktop backend 占用 8782 端口返回代理/握手失败），`:8781` 亦无 LISTEN；healthz `000/empty` — **gateway 当前不可达** |
| 15 | ④ :8782 cutover 终态 — Docker daemon 健康 | `docker info` / `docker context use default` / `DOCKER_HOST=unix:///var/run/docker.sock docker info` | `Server: ERROR: request returned 500 Internal Server Error for API route and version .../v1.55/info`，`/containers/json` 同症 — **daemon API 不健康**，无法 `docker ps`/`docker cp` 取证容器二进制 |
| 16 | ④ :8782 cutover 终态 — 替代证据 | `git log --oneline 1e0f71c70..origin/main` + `git diff --name-only 1e0f71c70 4cfaacdf3 \| rg -E 'sql/migrations\|version\.json\|VERSION'` | 区间内无 deploy/version 簿记 commit（最后一条 `087fba395` 仍为当前落档值 seq-2100），`sql/migrations/` 仅 695 升级 + baseline_ensure/promote contract test 修，无新增 schema 变更触发蓝绿重部署 |
| 17 | ④ 实时 404 探针（双实例） | `curl -m 5 -X POST http://127.0.0.1:878{1,2}/api/auth/token` | 不可达 — **本轮缺探针证据**，待 Docker Desktop 恢复 + gateway 重启后由 R23 首项补做；scope 路径零变更下，契约层面 404/500 行为维持（fdStatusFor 409/404 组未动 + fdRequest 路由未动） |

## 三、改动文件

- `docs/audit/2026-09-13-r22-freediscovery-routine-audit.md`（本文）。
- **零代码改动**；**版本 bump 缺席**：区间内无 deploy/version 簿记 commit
  入线（并行线亦未补推 seq-2101 部署），本检出 WIP 仍持 seq-2100
  簿记——与 R21 同因，跳过；R23 从实际落档值续。
- **收束方法（本轮沿用 R21 §三 worktree 零侵入协议）**：本检出
  暂存 4 个并行 WIP 文件均为部署簿记相关，落档仅追加本文——
  `git add docs/audit/2026-09-13-r22-freediscovery-routine-audit.md` →
  `git worktree add --detach /tmp/r22-push` → worktree 内
  `git fetch origin main` + `git rebase origin/main`（仅 replay 本文提交，
  并行线 WIP/部署簿记继续由并行会话持有）→ `git push origin HEAD:main`
  → `git worktree remove`。共享检出的 WIP 与分支指针全程未被触碰。

## 四、遗留风险

1. **templates GET list 直写 500（LOW 观察，维持）**：#7 复核 `List` 仍无
   sentinel，豁免继续成立。判据不变：`List` 引入任何 sentinel 时必须先改
   走 fdStatusFor。
2. **④ :8782 cutover 终态未核验（R23 首项，R22 升级遗留）**：R21 遗留
   #2 在 R22 进一步恶化——`:8782` 现已无 gateway LISTEN（curl healthz
   `000/empty`），Docker Desktop daemon API 持续 500，**live 探针与
   `go version -m` 身份取证双不可达**。R23 须按 R21 配方恢复现场
   （重启 Docker Desktop → `docker ps` 核 `:8782`/`8781` 容器 →
   `docker cp` → 宿主 `go version -m` → POST `/api/auth/token` →
   GET `/api/free-discovery/tasks/999999` → 404 精确 detail），
   并复跑 seq-2101+（如已重部署）身份核验。如 fa1124b18 仍
   未推送（区间内 `fa1124b18` 不可见，但本地 reflog 仍持 `1e0f71c70` 之上
   `fa1124b18`/`a0a6a48dc` 两个未推送 commit），则 vcs.revision 仍可能
   指向 `fa1124b181cc…` 悬空 sha（视并行线是否再次 rebase 而定）——按
   R21 §四 #2 协议以并行实际推送值映射为准。
3. **本检出积压**：R21 残留的 `a0a6a48dc` + `fa1124b18` 仍未推送（并行线
   持有，reflog HEAD@{2}/HEAD@{3} 可见）；本检出 WIP（seq-2100 4 文件）
   未提交——均为并行会话在途工作，勿代管。
4. **`errors.Is` vs `==`**：domain 层多处仍用 `err == sql.ErrNoRows`（R18
   §四.2 遗留，维持原样）。
5. **归档可信度**：原则继续适用——"已关闭"只是线索，代码现状才是证据；
   本轮 7/7 逐条 rg 核销即为执行样例。
6. **Docker Desktop 健康**：daemon API 500 在 R22 期间持续
   （`/info`/`/containers/json` 同症），影响所有依赖 docker 的取证脚本
   （不仅 R 审计，亦包括 deploy-local.sh/health-check/post-deploy-verify）。
   不在 FreeDiscovery scope，但下一轮任何部署/容器核验均需先解决此
   upstream 故障。

## 五、下一轮提示词

> 请根据 docs/audit/2026-09-13-r22-freediscovery-routine-audit.md 与
> memory 状态，对 FreeDiscovery 做例行审计（R23），区间基线为本轮落档
> 提交（fetch 时 `git rev-parse origin/main`）：
> ① 契约逐条核对——R22 §二 表中 6 项 + LOW 观察（templates list :76，
>   豁免判据 #7）逐条 rg 核销，勿信任"已关闭"声明；
> ② 区间净 diff——`git diff <基线> <新tip> -- admin/free_discovery.go
>   admin/free_discovery_test.go domains/freediscovery/
>   web/src/api/free-discovery.ts`，merge 勿用 `git show --stat` 下结论；
>   入线先体检 `.go`/`go.mod`/`go.sum`（未触碰则 Go 门禁证据延续）；
> ③ 门禁——CJK 仅 url_safety_test.go fixture；gofmt/build/vet/
>   test -count=1 全绿；
> ④ 部署身份（R23 首项，R22 遗留 #2）——**Docker Desktop 恢复健康
>   （daemon API 不再 500）为前置**；核验 :8782 cutover 终态：当前
>   服务版本串 + `docker ps` 确认 LISTEN + docker cp 出二进制用宿主
>   `go version -m` 核验 vcs.revision + 复跑 404 探针（`POST
>   /api/auth/token` 登录 → `GET /api/free-discovery/tasks/999999` →
>   404 精确 detail）。+dirty 需行为探针补证；若 fa1124b18 仍未推送
>   且被并行 rebase 重写，注意悬空 sha 映射（视并行实际推送值校准）。
>   若现场仍未恢复，纳入遗留并明确何时可恢复取证。
> 推送遇拒：fetch 体检 + `git reflog -5` 后显式 `git pull --rebase origin
> main`；共享检出有活跃并行会话时优先 R21 §三的 worktree 零侵入收束。
> 版本 bump 从实际落档值续（R21/R22 因 部署未 settle 连续两轮缺席）。
