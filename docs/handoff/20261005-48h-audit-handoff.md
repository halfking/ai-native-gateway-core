### 交接：48h 滚动审计轮（2026-10-05 02:54–13:40）

**基线**：`llm-gateway-go-2`，ff 到 `acc0511b0`（入站 9 笔合并）。
**产出**：`validate_sessions_v2` 端边界修复 + 迁移通道门四层缺口收口 + 孤儿分支删除。
**审计文档**：`docs/12小时内修订审计-20261005-1310.md`（含全部证据、负控、逐项判定、移交项）。

---

## 下一轮提示词（可直接粘贴）

> 继续 48h 滚动审计，读 `docs/12小时内修订审计-20261005-1310.md` 作为上一轮 handoff。
>
> 1. `git fetch && git merge origin/main`，`go build ./...`。
> 2. **先跑门再看代码**，但**本机 `make` 没装**（Git Bash/msys64/choco 都没有）⇒ `make guards` 是采集失败不是门红。直接跑等价命令：
>    `go test ./internal/rowsguard ./internal/errdiscard ./internal/dbrows ./internal/jsoncol ./internal/paramguard ./internal/sqlguard ./internal/sqlreadguard ./internal/metricguard ./internal/partguard ./internal/routeguard ./internal/ingressguard ./internal/healthstateguard ./internal/billguard ./sql/schema -count=1 -timeout=120s`
>    ⚠️ PowerShell 里必须写成**数组** `@(...)` + `& go test @G`，写成字符串会 `directory not found`。
> 3. `bash` 也要在同一���命令里加进 `$env:PATH="C:\Program Files\Git\bin;$env:PATH"`，否则 `sql/schema` 假红（`exec: "bash": executable file not found in %PATH%`）。
> 4. 窗口内提交逐笔深审，**每条发现必须落到 `文件:行号`**，并做**负控**（改前红/改后绿）——单次绿灯不算证据。
> 5. 落地修正 + 补门 + 更新文档 + 提交 + 合并 + 推送。

---

## 下一轮的优先项（按价值排序）

1. **[P1] `apply-db-revision-sequence` 门的 `exit 1` 是这条门最大的可用性缺陷。**
   本轮连修**五次**才走到底（829 → 819 → 830 → 825-828 → clobber 链）。
   判据本身是对的，但它**一次只告诉你一个**，「首个失败点」因此被四轮交接
   记录成「唯一问题」。**建议先写脚本一次列全所有缺口再动手**，
   或把门改成收集全部失败后一次报出（退出码不变）。
2. **[P1] 829 / 825-828 现在已在通道里，但从未在真实升级路径上跑过。**
   下次升级前请在 staging 库走一遍 `apply-db-revision-sequence.sh`。
   尤其 829：它现在会在**升级**时把 `request_logs_bodies` 的
   `ensure_request_logs_bodies_partition` 换成纯 heap（链序 694→765→829，829 最后）。
3. **[P2] 819 仍是「活文件」**，只靠本轮新增的 `superseded_migrations` 门拦着。
   台账 `docs/db-changelog.md` 应补一句「819 已作废，勿接线」——
   文件头已写，但读台账的人看不到文件头。
4. **[P2] `validate_sessions_v2` 端边界修复未经真库验证**（本机无 `TEST_DATABASE_URL`）。
   参数表已验（`TestQueryBoundsIncludeTheNamedEndDay`），**planner 未验**。
5. **[P2] R44 原样移交、本轮未动**：
   `bg/auto_route_realtime_listener.go:273-277/296-301` singleflight 跳过被记成「已处理」；
   `4315a598c` 只加互斥未加节流；`830.down` 台账戳记无对称删除路径。
6. **[P3] gofmt 欠债**（R44 移交第 8 项）：`apihub/pg_store.go`、
   `apihub/upsert_heartbeat_contract_test.go` 在 HEAD 就未格式化。
7. **[P3] GitHub 镜像推送仍被 PushProtection 阻塞**（第 5 轮未解），需 Owner 点 URL 放行。

---

## 给下一轮的三条方法论提醒（本轮都踩到了）

1. **「门只报一条」≠「只有一条问题」。** 门逐条 `exit 1`，于是首个失败点被
   连续四轮交接写成「唯一问题」。**动手前先把全部缺口一次列全**，
   否则只能一层层揭。
2. **负控会否掉你的诊断，别跳过它。** 我据交接判断「门在错的维度上问」，
   改了门并配三条"回归"用例；负控一跑**全绿**——暴露那个改动对 CLI
   可达输入是 **no-op**、用例不区分新旧，真缺陷在**查询边界**。
   **一个证明不了新旧之别的测试等于没有测试。**
3. **「门红」与「对象错」必须分开。** 门红在 clobber（exit 5）不是说 829
   不该进通道（Owner 判断仍成立），而是说**只做一半**会把升级从
   「静默不投递」变成「直接中止」。这类门替我挡下的东西比门自己报的那条更重要。

---

## 本机环境备忘（新增/复用的坑）

- **`make` 本机根本没装**（Git Bash / msys64 / choco 均无）⇒ `make guards`
  是**采集失败**。直接跑那 14 个包。
- `bash` 用 `C:\Program Files\Git\bin\bash.exe`，且要**在同一条命令里**加进
  `$env:PATH`（每个 Bash 调用都是新 shell，PATH 不跨调用保留）。
- PowerShell：`go test $G` 要写成**数组** `@(...)` + `& go test @G`；
  字符串会被当单个路径 ⇒ `directory not found`。
- PowerShell：`$x=(cmd; if(...))` 这类**括号内不能有分号**，会 ParseError。
  先单跑命令读 `$LASTEXITCODE`。
- `Out-File -Encoding utf8NoBOM` 在 PS 5.1 **不存在**。写文件用 `write` 工具
  或 `[System.IO.File]::WriteAllText`。
- `Select-Object -First N` 提前关闭管道会把原生命令杀掉，`$LASTEXITCODE` 变 -1
  —— 那是**采集失败**，不是门红。
- 删文件走 `rm -- "<path>"`（可恢复）；`Remove-Item` 被硬安全策略拒绝。
- **改大文件用 `edit` 工具会静默插行**（本轮实测：一个「改 typo 再改回来」
  的净零编辑让 393 行的 shell 脚本多出 28 行）。**每次编辑后立刻
  `git diff <file>`**；净零编辑必须是空 diff。多点替换用 node `.mjs`
  （`.mjs` 里先 collect 全部、全部校验、最后一次性 `writeFileSync`），
  `node -e` 在 PS 里会被 `$(` 和 `''` 撕碎。
- 无 `TEST_DATABASE_URL` ⇒ 所有真库门 SKIP；`CGO_ENABLED=0` ⇒ **`-race` 跑不了**。
