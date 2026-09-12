# R17 — FreeDiscovery 合并后回归审计（2026-09-13）

## 一、执行摘要

**结论：零回归。** 审计范围 `40f982a87..3b76590e6`（origin/main 在 FreeDiscovery CJK
sweep 收口推送之后新增的 2 个提交）。合并提交 `3b76590e6` 对
`admin/free_discovery.go`、`admin/free_discovery_test.go`、`domains/freediscovery/`
及 `web/public/{version,menu-config}.json` 的**净改动全部为空**——实质新增内容仅
`76ae2bf71` 的 request-logs handoff 文档。静态门禁与全量测试在新 tip 上全绿。
本地已快进同步至 `3b76590e6` 并与 origin/main 一致，工作树干净。

## 二、误报根因（方法论要点）

初查 `git show --stat 3b76590e6` 时，stat 显示 free_discovery 两文件 +69/−31，
形似"合并引入了本模块改动"。实际是 **merge stat 以第一父提交（76ae2bf71）为基准**，
显示的是从另一条线视角看到的所有差异——其中 free_discovery 的改动来自**第二父提交
（40f982a87，即我们自己的推送）**。正确探针：

```bash
# 净改动 = 相对我们已审计基线的 diff；为空即合并零影响
git diff 40f982a87 3b76590e6 -- admin/free_discovery.go admin/free_discovery_test.go domains/freediscovery/
# 组合 diff（git show 默认）只显示与双亲都不同的冲突消解；为空 = 无手工消解
git show 3b76590e6 -- admin/free_discovery.go
```

两条探针结论一致：组合 diff 为空（无冲突消解），净 diff 为空（完全采纳我方版本）。

## 三、验证项与证据

| # | 验证项 | 命令 | 结果 |
|---|---|---|---|
| 1 | 并行提交发现 | `git fetch` + `rev-list --left-right --count` | 落后 2（0 本地独有，可 ff）；`76ae2bf71` 纯 docs（1 文件），`3b76590e6` merge |
| 2 | 合并净改动 | `git diff 40f982a87 3b76590e6 -- <scope>` | scope 内 4+3 文件路径全部为空 |
| 3 | CJK 收口保持 | `grep -nP '[\p{Han}]' admin/free_discovery*.go` | 零命中；`domains/freediscovery/*.go` 仅 `url_safety_test.go:116` length-bomb fixture（`strings.Repeat("中", 250)`，故意保留）；README.md 属 repo 级 zh 惯例 |
| 4 | 格式 | `gofmt -l`（scope 文件） | 空 |
| 5 | 编译/静态 | `go build` + `go vet ./admin/... ./domains/freediscovery/...` | 通过 |
| 6 | 测试（强制刷新） | `go test -count=1 ./admin/... ./domains/freediscovery/...` | 全 ok：admin 66.8s、freediscovery 4.3s、dashboardapi/distlock/dashboarddegrade 通过 |
| 7 | 同步状态 | `git merge --ff-only origin/main` | 40f982a87 → 3b76590e6，工作树干净，HEAD == origin/main |

交叉确认：`76ae2bf71`（700 根修审计轮）diff 中 `freediscovery` 零提及，与本模块无交集。

## 四、改动文件与关键行为

本轮为纯验证轮，**无代码修复**（未发现需要修复的缺陷）。提交内容：

- `docs/audit/2026-09-13-r17-freediscovery-post-merge-audit.md`（本文，新增）
- `VERSION` / `version.json` / `web/public/version.json`：`scripts/bump-version.sh`
  常规递增 → `2.5.4-3b76590e-20260912-2094`（seq 2093→2094）

## 五、遗留风险

1. **无模块级遗留**。FreeDiscovery 范围内 build/vet/test/CJK/gofmt 全部干净。
2. **流程性风险（复发已知）**：并行会话频繁 merge/rebase 会改写本地历史，memory/文档
   中的 SHA 引用会失效；merge stat 以第一父为基准的误报（§二）会再次出现。任何回归
   判断一律以 `git diff <已审计基线> <新tip> -- <scope>` 的净 diff 为准。
3. **范围外提醒（勿扩 scope）**：`admin/` 其余 ~300 文件仍含 zh 注释，属仓库既有惯例，
   不在 FreeDiscovery 收口范围；R15 遗留的本机 pg 687 迁移行动项（2026-10-01 前）
   与本模块无关，仍在别处跟踪。

## 六、下一轮提示词

> 请根据 docs/audit/2026-09-13-r17-freediscovery-post-merge-audit.md 与 memory 中
> free-resource-discovery-feature.md 的状态，对 FreeDiscovery 模块做例行回归审计：
> 先 `git fetch` 评估 origin/main 是否有新提交；若有，用
> `git diff <上一审计tip> <新tip> -- admin/free_discovery.go admin/free_discovery_test.go domains/freediscovery/`
> 判断净改动（勿用 `git show <merge> --stat` 直接下结论，见 R17 §二误报根因）；
> 随后运行 grep（`[\p{Han}]` 仅允许 url_safety_test.go:116 fixture 与 README.md）、
> gofmt、`go build/vet/test -count=1 ./admin/... ./domains/freediscovery/...`；
> 核实 git status 干净、与 origin/main 同步；如有问题先修复再按本轮格式落档
> docs/audit/，并 bump 版本号提交推送。
