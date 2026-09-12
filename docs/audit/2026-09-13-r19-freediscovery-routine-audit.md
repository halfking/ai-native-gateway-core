# R19 — FreeDiscovery 例行审计（2026-09-13）

## 一、结论

**R18 tip（`52ac68184`）之后零新提交：origin/main == HEAD == `52ac68184`，
FreeDiscovery 四条目标路径的区间净 diff 为空。例行契约逐条核对（不信任任何
归档声明）6 项全部以代码现状核销为"真实关闭"；静态门禁与全量测试全绿。
本轮无代码修复，仅落档 + 版本 bump（2096→2097）。**

新发现（LOW，仅记录不改动）：`handleFreeDiscoveryTemplates` 的 GET（list）分支
（`admin/free_discovery.go:76`）仍是直写 500 而非 fdStatusFor。核对结论：这不属于
2026-09-09 发现 2 的范围（原发现只点名 `handleFreeDiscoveryTasks` 与
`handleFreeDiscoveryTaskResults`），且 `TemplateManager.List` 只返回包装后的
db 错误（begin tx / query / scan，`template_manager.go:155+`），不可能携带任何
sentinel → 与 fdStatusFor 的 default-500 回落**行为完全等价**。按 import-bug
memory 的判据（直写 500 仅可接受于"truly unexpected runtime errors"），该项
满足豁免条件；留作一致性观察，若未来 List 引入 sentinel 必须先改走 fdStatusFor。

④ 条件判定：**balance-floor 并行会话未合入**——工作区仍留有 13 个未提交文件
（`bg/balance_floor_guard*.go`、`sql/migrations/startup/701_*.sql`、db/db.go、
providercap 等），且 `git status --porcelain` 确认零触碰 FreeDiscovery scope。
按本轮提示词的"若已合入"前提，本机部署冒烟（任务详情 404/500 两路径）**跳过**，
顺延至 balance-floor 合入后的下一轮（R20）必做。

## 二、验证项与证据（命令与结果）

| # | 验证项 | 命令 | 结果 |
|---|---|---|---|
| 1 | 发现 1：import handler 走 fdStatusFor | `rg -n 'fdStatusFor' admin/free_discovery.go` + 查看函数体 | `handleFreeDiscoveryImport`（:404）域错误路径 :430 `writeError(w, fdStatusFor(err), …)`；回归测试 `TestFreeDiscovery_Import_StatusForSentinels` 在（test 函数 :211，更正轮复核并 -count=1 复跑 PASS） |
| 2 | 发现 2：list 端点走 fdStatusFor | `git show 89706ba3e` + `rg -n 'writeError\|fdStatusFor'` | 原发现范围两端点均在：tasks list :338、taskResults :394；templates GET list :76 直写 500 → 本轮新 LOW 观察（见 §一，行为等价） |
| 3 | 发现 3：GetTask sentinel 全链路 | `rg -n 'ErrTaskNotFound' domains/freediscovery/ admin/ docs/ web/` | 定义 `template_manager.go:32`；GetTask wrap `discovery_engine.go:410`（`sql.ErrNoRows → %w (id %d)`）；fdStatusFor :461 → 404；回归测试在（engine `TestDiscoveryEngine_GetTask_NotFoundSentinel` :290、admin `TestFreeDiscovery_Task_StatusForSentinels` :237 + `TestFreeDiscovery_Task_GetNotFound404_DBFault500` :259——更正轮已按函数声明行勘误并复跑 PASS） |
| 4 | import 契约（404/409） | `rg -n 'ErrImportTask'` | sentinel 定义 `import_service.go:48/:53`；fdStatusFor 映射 :460（404）/:464（409）；handler :430 接线 |
| 5 | R18 收尾：fdRequest `/tasks/{id}` 路由 | `rg -n '/tasks/\|/import\|/templates' admin/free_discovery_test.go` | switch :68-79 排序正确（`/results` 后缀 → `/import` → `/tasks/` → `/templates`）；404/500 子测试 :269-284 经 fdRequest 走通新分支 |
| 6 | R18 收尾：前端契约行 | `rg -n 'ErrTaskNotFound' web/src/api/free-discovery.ts` | :15 404 行含 `ErrTaskNotFound`（含跨租户）；docs §4.4 :215 行在 |
| 7 | 区间净 diff | `git diff 52ac68184 HEAD -- admin/free_discovery.go admin/free_discovery_test.go domains/freediscovery/ web/src/api/free-discovery.ts` | 空输出（`52ac68184..HEAD` 零提交，净 diff 恒空） |
| 8 | WIP 隔离 | `git status --porcelain \| rg 'freediscovery'` | exit 1（13 个 WIP 文件全部属于 balance-floor，零触碰审计 scope） |
| 9 | CJK 收口保持 | `rg -l '[\p{Han}]' domains/freediscovery/*.go` | 仅 `url_safety_test.go` fixture；admin 两文件零命中 |
| 10 | gofmt | `gofmt -l`（scope 文件） | 空 |
| 11 | build / vet | `go build/vet ./admin/... ./domains/freediscovery/...` | 均通过（含工作区 balance-floor WIP 一并编译通过） |
| 12 | 全量回归 | `go test -count=1 ./domains/freediscovery/ ./admin/` | ok（freediscovery 2.6s；admin 66.3s） |
| 13 | balance-floor 合入判定 | `git rev-parse origin/main` + `git status --porcelain` | origin/main == `52ac68184`；WIP 未提交 → 未合入，冒烟跳过 |

## 三、改动文件与关键行为

- `docs/audit/2026-09-13-r19-freediscovery-routine-audit.md`（本文）。
- 版本 bump 2096→2097（`scripts/bump-version.sh`，lockstep：`version.json` /
  `VERSION` / `web/public/version.json`；web/dist 副本 gitignored）。
- **零代码改动**：契约核对全部通过，唯一新观察项（LOW）行为等价、不值得在
  balance-floor WIP 悬置期间引入 diff 噪音。

## 四、遗留风险

1. **templates GET list 直写 500（LOW 观察，新增）**：与 fdStatusFor 行为等价
   （List 无 sentinel 可能），但若未来 `TemplateManager.List` 引入任何 sentinel，
   必须先改走 fdStatusFor 再扩展，否则会复刻发现 1 的"契约注释与实现脱节"模式。
2. **balance-floor 冒烟——已在更正轮完成（§六）**：合并落地后本轮补做了本机
   部署冒烟，404/500 真实 PG 路径 + readyz + 身份核验全部通过；R20 起仅在
   tip 重部署时复验运行版本身份，无需重复故障注入。
3. **`errors.Is` vs `==`**：domain 层多处仍用 `err == sql.ErrNoRows`（R18 §四.2
   遗留，本轮维持原样）。
4. **归档可信度**：本轮 6 项核销再次以代码现状为准（含对 R18 自身两项收尾的
   复核）；该原则继续适用——memory/handoff 的"已关闭"只是线索，不是证据。
5. **并行会话纪律**：balance-floor WIP（13 文件）仍悬置在工作区；提交严格按
   路径显式暂存，`git add -A` 在本仓库禁止；推送遇拒用 stash(named)→rebase→
   push→pop 配方。

## 五、下一轮提示词

> 请根据 docs/audit/2026-09-13-r19-freediscovery-routine-audit.md（含 §六 更正轮）
> 与 memory 状态，对 FreeDiscovery 做例行审计（R20）：
> ① 契约逐条核对——R19 §二 表中 6 项 + LOW 观察（templates list :76）逐条用
>   `rg` 对代码现状核销，勿信任任何"已关闭"声明；
> ② 区间净 diff——`git diff <R19更正轮tip> <新tip> -- admin/free_discovery.go
>   admin/free_discovery_test.go domains/freediscovery/ web/src/api/free-discovery.ts`，
>   merge 提交勿用 `git show --stat` 下结论；
> ③ 门禁——`rg -l '[\p{Han}]' domains/freediscovery/*.go` 仅允许 url_safety_test.go
>   fixture；gofmt/build/vet/test -count=1 全绿；
> ④ 部署冒烟已在本轮（§六）完成：balance-floor 已合入、404/500 真实 PG 路径
>   与 readyz 全部验证通过。R20 仅需在本轮 tip 被重新部署时复验运行版本身份
>   （`go version -m` vcs.revision == 当轮 tip），无需重复故障注入。

## 六、更正轮（同日元审计，对 R19 本身，2026-09-13 深夜）

**触发**：R19 推送（`7489730a9`）后，并行会话经 merge `67fce6914` 把整条
balance-floor/streaming 并行历史并入 main，tip 推进至 `a6b535dab`（install 端
seq 2098 已由并行会话部署至本机 8782）。按"归档/自述声明不是证据"原则，
本轮对 R19 自身逐条元审计，并补做 R19 顺延的部署冒烟。

| # | 复核项 | 命令 | 结果 |
|---|---|---|---|
| 1 | R19 区间结论在合并后是否成立 | `git diff 7489730a9 a6b535dab` / `git diff 52ac68184 a6b535dab` -- 4 scope 路径；`git show 67fce6914 -- <scope>`（combined） | 全部为空——并入的 streaming/balance-floor 历史零触碰 FreeDiscovery scope，R19 §一/§二#7 结论维持 |
| 2 | R19 引用的 4 个命名测试存在且通过 | `go test -run … -count=1 -v`（两包） | 4/4 PASS（Import_StatusForSentinels、Task_StatusForSentinels、Task_GetNotFound404_DBFault500、GetTask_NotFoundSentinel） |
| 3 | 新 tip 门禁重跑（合并后代码 ≠ R19 时编译的 WIP 形态） | 同 §二 #9-#12 | Han 仅 fixture；gofmt 空；build/vet OK；test -count=1 freediscovery 2.5s + admin 66.9s 全 ok |
| 4 | R19 文档行号引用精度 | `rg -n 'func TestFreeDiscovery_(Import_StatusForSentinels\|Task_)' admin/free_discovery_test.go` | 勘误：admin 三测试函数实位于 :211/:237/:259，原文引用的 `:209-218`/`:231-257` 是注释+用例锚点、第三函数 :259 落在区间外——已修正 §二 行 1/3 |
| 5 | docs 契约文件未被并入历史改动 | `git diff 52ac68184 a6b535dab -- docs/freediscovery-configuration.md` | 空；§4.4 :215 行原样 |
| 6 | ④ 前提复核 | `git log 7489730a9..origin/main` | balance-floor 已合入（`1a89c32fe` feat + `a6b535dab` handoff docs）→ R19 顺延的冒烟在本轮补做，见下 |

**部署冒烟（本机 8782；运行二进制即审计 tip，无需重部署）**：

- **身份核验**（部署技能"晋升后身份检查"条款）：`go version -m` 的
  `vcs.revision == a6b535dab45b2a5086f2a228537c96898a50a930`（全 SHA 一致）；
  `run/gateway.build` mtime 04:09（新鲜）；healthz 报 `2.5.4-a6b535da-20260912-2098`。
- **readyz**：db+redis connected，`status: ready`。
- **404 路径**：`GET /api/free-discovery/tasks/999999`（admin Bearer 鉴权，经
  `mux.HandleFunc("GET /api/free-discovery/tasks/{id}")` 真实路由）→ **HTTP 404**
  `{"error":{"detail":"freediscovery: task not found (id 999999)"}}` —— R18
  sentinel 修复首次取得真实 PG（非 sqlmock）端到端证据。
- **500 路径**：`docker stop llm-gateway-pg` 后同请求 → **HTTP 500**
  `freediscovery: begin tx: … connection refused`（fdStatusFor default 回落，
  非 sentinel 误判）；`docker start` 后 ~8s WAL 崩溃恢复，`pg_isready` 转接、
  readyz 复绿、404 路径复验结果一致。期间 PG `57P03 starting up` 暂态同样落
  500，行为一致。
- **迁移 701 证据**（balance-floor 随部署应用的旁证）：`credentials` 表
  `balance_floor_usd` / `quota_floor_tokens` / `quota_floor_percent` 列已存在；
  `v_routable_credential_models` 视图在。
- **部署侧效与版本备注**：deploy 把工作区 VERSION/version.json/web 静态文件
  再生成为 seq 2098。本轮直接提交该 deploy 侧效 bump（bump 脚本在 git_sha+date
  未变时保持 seq 的既定行为），repo SSOT 2097→2098，与 install 树实际部署
  版本一致，避免出现"repo 与部署各自为政"的 seq 分叉。

**更正轮改动文件**：本文档（§二 两处行号勘误 + 本节 + §五 提示词改写）、
版本文件（2097→2098，deploy 侧效入账）、`web/public/menu-config.json`
（deploy 再生成漂移，按 `dae1b7c93` 先例随轮提交）。零代码改动。
