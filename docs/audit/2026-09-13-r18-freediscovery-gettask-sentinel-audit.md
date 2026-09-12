# R18 — FreeDiscovery GetTask sentinel 补漏审计（2026-09-13）

## 一、结论 / 根因

**结论：R17 判定"零回归"正确，但 R17 只审了 `40f982a87..3b76590e6` 区间——它没有
覆盖"更早轮次声称已修但实际未修"的欠账。本轮例行复审按 memory 验证配方逐项核对
时发现：2026-09-09 handler 审计的发现 1（GetTask 缺 sentinel error，MEDIUM）从未
真正修复，已在本轮补齐并推送（`26c9530c4` + 本轮 follow-up 提交）。**

根因（三层叠加）：

1. **代码层**：`handleFreeDiscoveryTask` 的注释承诺 "repository sentinels
   (ErrTaskNotFound) surface as 404"，但 `ErrTaskNotFound` 从未被创建。`GetTask`
   在 `sql.ErrNoRows` 时返回裸 `fmt.Errorf("freediscovery: task %d not found", id)`，
   而 `fdStatusFor` 的子串回退表里没有 "not found" → 默认落 500。即**查询不存在的
   任务得到 500（服务端错误语义），且与 handler 注释、审计契约（404）三处互相矛盾**。
2. **流程层**：归档记忆（freediscovery-handler-improvements）写着"两项审计发现全部
   关闭（82a68462a / 89706ba3e）"，但 89706ba3e 只修了发现 2（list 端点）。发现 1
   被错误标记为已关闭，之后每轮审计都信任了该归档不再核查。
3. **方法层**：R17 的区间审计（`git diff <baseline> <tip> -- <scope>`）只能证明
   "区间内无回归"，不能证明"历史欠账不存在"。例行审计需要同时保留**契约逐条核对**
   （审计发现清单 × 代码现状）与**区间净 diff** 两种探针。

本轮 follow-up 审计（对 `26c9530c4` 本身）另发现两处收尾遗漏，已一并修复：
前端契约注释 `web/src/api/free-discovery.ts` 的 404 行缺 `ErrTaskNotFound`；
测试助手 `fdRequest` 的 default 分支把 `/tasks/N` 误路由到
`handleFreeDiscoveryTemplateByID`（未来用助手测任务详情会静默打错 handler）。

## 二、验证项与证据（测试命令与结果）

| # | 验证项 | 命令 | 结果 |
|---|---|---|---|
| 1 | sentinel 存在且接线 | `rg -n 'ErrTaskNotFound' domains/freediscovery/ admin/free_discovery.go` | template_manager.go 定义；GetTask wrap；fdStatusFor → 404 |
| 2 | GetTask 行为区分 | `go test ./domains/freediscovery/ -run TestDiscoveryEngine_GetTask_NotFoundSentinel -v` | PASS（ErrNoRows → sentinel；db fault 不匹配 sentinel） |
| 3 | handler 状态码契约 | `go test ./admin/ -run 'TestFreeDiscovery_Task' -v` | PASS（sentinel/裸包装 → 404；db fault → 500；经 `fdRequest` 的 `/tasks/{id}` 路由） |
| 4 | 全量回归 | `go test -count=1 ./domains/freediscovery/ ./admin/` | ok（freediscovery ~2.6s；admin 全量 66.4s） |
| 5 | 静态门禁 | `gofmt -l`（scope 文件）+ `go build/vet ./admin/... ./domains/freediscovery/...` | 空 / 通过 |
| 6 | CJK 收口保持 | `rg -l '[\p{Han}]' domains/freediscovery/*.go` | 仅 `url_safety_test.go` fixture；admin free_discovery 两文件零命中 |
| 7 | 契约文档同步 | `rg -n 'ErrTaskNotFound' docs/freediscovery-configuration.md web/src/api/free-discovery.ts` | §4.4 表有行；前端错误约定 404 行已补 |
| 8 | 推送状态 | `git rev-parse HEAD origin/main` | 本轮 tip == origin/main |

## 三、改动文件与关键行为

**上一提交 `26c9530c4`（sentinel 主修复）：**
- `domains/freediscovery/template_manager.go`：新增 `ErrTaskNotFound` sentinel。
- `domains/freediscovery/discovery_engine.go`：`GetTask` 将 `sql.ErrNoRows` 包装为
  `ErrTaskNotFound (id %d)`（errors.Is 可判别，消息保留 id）；`createTask` 的模板
  竞态路径包装 `ErrTemplateNotFound`（同为裸 error 落 500 的暗病，竞态时 500→404）。
- `admin/free_discovery.go`：`fdStatusFor` 404 分支追加 `ErrTaskNotFound`；数据库
  故障仍落 500（default 不变）。
- `admin/free_discovery_test.go` / `domains/freediscovery/discovery_engine_test.go`：
  三个回归测试（见 §二 #2/#3）。
- `docs/freediscovery-configuration.md` §4.4 错误码速查补 `ErrTaskNotFound` 行。
- 版本 bump 2094→2095。

**本轮 follow-up 提交（审计收尾）：**
- `admin/free_discovery_test.go`：`fdRequest` 助手补 `Contains("/tasks/")` 分支 →
  `handleFreeDiscoveryTask`（插在 `/import` 与 `/templates` 之后，保证
  `/tasks/N/results`、`/tasks/N/import` 优先匹配不被劫持）；404/500 子测试改走
  `fdRequest`，使新路由分支成为被测行为（若路由仍走 default→templateByID，mock
  期望 discovery_tasks 查询会直接失败）。
- `web/src/api/free-discovery.ts`：文件头错误约定 404 行补 `ErrTaskNotFound`。
- `docs/audit/2026-09-13-r18-freediscovery-gettask-sentinel-audit.md`（本文）。
- 版本 bump 2095→2096。

## 四、遗留风险

1. **审计归档可信度**：任何"已修复"结论必须以代码现状 + 测试为准；memory/handoff
   的归档声明只是线索。本轮已在 memory 中修正该条目。
2. **`errors.Is` vs `==`**：domain 层多处仍用 `err == sql.ErrNoRows`（本轮保持原样
   未扩散改动）；若未来 driver 包装错误链，这些分支会静默失效。属全包一致性改造，
   不在本轮 scope。
3. **`build_date` 为 UTC 日期**：本地时区当天傍晚 bump 会落到前一日的 UTC 日期
   （如 20260912），属脚本既定行为（`date -u`），非缺陷。
4. **并行会话**：balance-floor 特性正在同一 worktree 活跃开发（10+ 文件未提交）。
   本轮所有提交均显式按路径暂存；`git add -A` 在此仓库禁止（见 memory R18 教训）。
   推送遇拒时用 stash(named)→rebase→push→pop。
5. **未做本机部署冒烟**：本轮为 sentinel 状态码修复，sqlmock 层已覆盖 404/500 两条
   路径；真实 PG 的 E2E 留待 balance-floor 会话合入后的下一轮统一验证，避免与其
   WIP 抢占 ：8782 部署位。

## 五、下一轮提示词

> 请根据 docs/audit/2026-09-13-r18-freediscovery-gettask-sentinel-audit.md 与
> memory 中 free-resource-discovery-feature.md / freediscovery-handler-improvements.md
> 的状态，对 FreeDiscovery 做例行审计（R19）：
> ① 契约逐条核对——审计发现清单（handler-improvements 三项 + import 契约）逐条
>   用 `rg` 对代码现状核销，勿信任任何"已关闭"声明；
> ② 区间净 diff——`git diff <上一审计tip> <新tip> -- admin/free_discovery.go
>   admin/free_discovery_test.go domains/freediscovery/ web/src/api/free-discovery.ts`，
>   merge 提交勿用 `git show --stat` 下结论（R17 §二）；
> ③ 门禁——`rg -l '[\p{Han}]' domains/freediscovery/*.go` 仅允许 url_safety_test.go
>   fixture；gofmt/build/vet/test -count=1 全绿；
> ④ 若 balance-floor 并行会话已合入，补做一次本机部署冒烟（FreeDiscovery 任务详情
>   404/500 两条路径），再按本轮格式落档 docs/audit/ 并 bump 版本提交推送。
