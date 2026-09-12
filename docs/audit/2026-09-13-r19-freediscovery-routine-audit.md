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
| 1 | 发现 1：import handler 走 fdStatusFor | `rg -n 'fdStatusFor' admin/free_discovery.go` + 查看函数体 | `handleFreeDiscoveryImport`（:404）域错误路径 :430 `writeError(w, fdStatusFor(err), …)`；回归测试 `TestFreeDiscovery_Import_StatusForSentinels` 在（test :209-218） |
| 2 | 发现 2：list 端点走 fdStatusFor | `git show 89706ba3e` + `rg -n 'writeError\|fdStatusFor'` | 原发现范围两端点均在：tasks list :338、taskResults :394；templates GET list :76 直写 500 → 本轮新 LOW 观察（见 §一，行为等价） |
| 3 | 发现 3：GetTask sentinel 全链路 | `rg -n 'ErrTaskNotFound' domains/freediscovery/ admin/ docs/ web/` | 定义 `template_manager.go:32`；GetTask wrap `discovery_engine.go:410`（`sql.ErrNoRows → %w (id %d)`）；fdStatusFor :461 → 404；三个回归测试在（engine test :288-324、admin test :231-257） |
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
2. **balance-floor 冒烟顺延（必做）**：合入后的下一轮（R20）必须补做本机部署
   冒烟——FreeDiscovery 任务详情 404（不存在任务）/ 500（db fault）两条真实
   PG 路径；sqlmock 覆盖不能替代 E2E。
3. **`errors.Is` vs `==`**：domain 层多处仍用 `err == sql.ErrNoRows`（R18 §四.2
   遗留，本轮维持原样）。
4. **归档可信度**：本轮 6 项核销再次以代码现状为准（含对 R18 自身两项收尾的
   复核）；该原则继续适用——memory/handoff 的"已关闭"只是线索，不是证据。
5. **并行会话纪律**：balance-floor WIP（13 文件）仍悬置在工作区；提交严格按
   路径显式暂存，`git add -A` 在本仓库禁止；推送遇拒用 stash(named)→rebase→
   push→pop 配方。

## 五、下一轮提示词

> 请根据 docs/audit/2026-09-13-r19-freediscovery-routine-audit.md 与 memory 状态，
> 对 FreeDiscovery 做例行审计（R20）：
> ① 契约逐条核对——R19 §二 表中 6 项 + LOW 观察（templates list :76）逐条用
>   `rg` 对代码现状核销，勿信任任何"已关闭"声明；
> ② 区间净 diff——`git diff <R19 tip> <新tip> -- admin/free_discovery.go
>   admin/free_discovery_test.go domains/freediscovery/ web/src/api/free-discovery.ts`，
>   merge 提交勿用 `git show --stat` 下结论；
> ③ 门禁——`rg -l '[\p{Han}]' domains/freediscovery/*.go` 仅允许 url_safety_test.go
>   fixture；gofmt/build/vet/test -count=1 全绿；
> ④ **若 balance-floor 已合入：必做本机部署冒烟**——FreeDiscovery 任务详情
>   404/500 两条真实 PG 路径 + readyz，再按本轮格式落档 docs/audit/ 并 bump
>   版本提交推送。
