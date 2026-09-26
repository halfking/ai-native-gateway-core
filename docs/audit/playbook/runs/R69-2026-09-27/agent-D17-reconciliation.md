# D17+对账 子代理报告（窗口：092ab1b61..908256008）

> 主代理复核结论（R69 收口时回填）：发现#1 与 D15#1 同源→F2 修复；发现#2/#3/#4 死代码/重复清理候选→本轮加 Deprecated/遮蔽标注（删除性重构留独立轮次）；发现#5（53 vs 52）微差登记（scanner 实跑超时未出终裁）；发现#6/#8/#9 登记不处置；发现#7→本轮加退役条件标注。机械对账 27 提交无硬差异，门禁通过。

窗口口径修正：SHA 区间实含 **27** 个非 merge 提交（派发说 24）。其中 4 个时间戳早于 09-26 05:00，按 SHA 区间全量对账 27 个。HEAD=908256008，工作树干净。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 | 建议处置 |
|---|---|---|---|---|
| 1 | P2 | `/api/admin/sessions/list` v2 端点从未挂载：生产构造器全仓零调用；`-from` 模式请求该路径落入子路由，sessionID="list" → serveSessionDetail("list")，契约破裂 | admin/session_list_v2.go:34,99,103；admin/handler.go:1110-1111；cmd/sessionforensics/main.go:181 | 接线或按 R43 惯例补 RESERVED 标注 |
| 2 | P3 | session_list.go 全家族生产死代码：NewSessionListAPI 全仓零调用，家族仅 admin/session_list_test.go 可达（418 行）。注意 parseIntParam 是共享活函数（credential_monitor_heatmap.go:164 等在用） | admin/session_list.go:55,60,104,218,255,301 | 登记待清清单；删除性重构单独立项 |
| 3 | P3 | serveSessionTurnsList/serveSessionTurnsListDB 被路由遮蔽成死路径：exact 路由 `/api/admin/sessions/{id}/turns`（handler.go:1171）压过子路由 case "turns"。且 serveSessionTurnsListDB 与 serveSessionTurnsUnifiedDB 重复约 70%。该死路径装配 TurnListItem 不填 IDKind/PrimaryKey | admin/session_turns_v2.go:76-186 vs admin/session_turns_unified.go:21-169；admin/session_state_handlers.go:197-198 | 候选合并：删 case "turns" 分支或两实现收敛 |
| 4 | P3 | turns 双 cursor 双 limit 实现：session_turns.go JSON+HMAC vs session_turns_tree.go 字符串 mac + 三处手写 Atoi limit。unified 经 fallback 已复用 tree 查询，cursor 层仍双轨 | admin/session_turns.go:62-109；admin/session_turns_tree.go:193-200,385-426 | 清理时随 #3 一并收敛 |
| 5 | ⚠️P3 | bc68bd1b0 计数对账偏差：消息与 baseline 注释均称"53 条 WARN"，diff 实加 52 条目行（57 插入 = 52 条目 + 4 注释 + 1 空行）。是否第 53 条与既有基线重复需实跑 scanner 裁决 | bc68bd1b0 diff scripts/scan-secrets.baseline；HEAD 基线 164 条目 | 主代理实跑 strict 扫描核实；minor |
| 6 | ⚠️P3 | ff5b10052 消息↔diff 软差异（092ab1b61 同类）：消息声称"G-1 gofmt 三文件落地"，diff 仅 1 个审计文档；本仓任何分支自 09-21 后无这三文件的提交记录，HEAD 态 gofmt-clean。修复发生在库外在制工作树，文档自身有 pathspec 限定说明，无实际债务残留 | ff5b10052 stat；git log --all -- autoroute/helpers.go 最末=774a3d138 | 记录在案，不计新违规 |
| 7 | P3 | autoroute 三代 scoring 并存：scoring.go（v1，index.go:244,295 + recommend_v2.go:239 在调）、scoring_v2.go（主链）、scoring_simplified.go（flag 臂，默认 false）。迁移中态，退役条件无标注 | autoroute/scoring.go:177；scoring_v2.go:52；scoring_simplified.go:203；feature_flags.go:136 | 函数头补消费方/退役条件标注 |
| 8 | P3 | gofmt 债务在增长：全仓 294 文件不合规（R37 时 272，10 天 +22）。O-6 executors 现存 8 文件（与 0420 doc 记载"9 文件"差 1，无法归因） | gofmt -l . 实测；docs/12小时内修订审计-20260927-0420.md:80 | 独立 chore(gofmt) 批量提交，按包机械批 |
| 9 | P3 | 生成物漂移（O-3 复认）：VERSION/version.json/web/public/version.json = 17316e48（build 2265），落后 HEAD 42 提交；menu-config.json exported_at 停留 09-25 | VERSION、version.json、web/public/version.json、web/public/menu-config.json | 归属 chore(version) 收口 |
| 10 | P3 | gofmt 遮蔽面之外的零调用导出抽检未见新死代码：executor_ollama.go 新增导出符号引用数最低 3；executor_dispatch 抽检 9 符号全部有引用 | domains/streaming/executors/ | 健康 |

## 二、核实为健康的面

- go vet ./... 全仓零告警。
- README 双语一致且与实态吻合：均"50 rules/50 规则"；scan-secrets.config 实测 50 条非注释规则行。
- TTFT 退役无残留：.env.example 无 TTFB/TTFT 行；全仓残余仅 main.go:2119 退役说明注释 + domains/providerprofile 的另一活特性。
- 9f62818c5 契约标注覆盖两条活路径（unified/tree/turns 的 IDKind/PrimaryKey 均在 diff 实证）。
- math.Exp 修复跨 merge 存活：96a6629c2 内容保留于 HEAD autoroute/scoring_v2.go:251,285,308。
- 749/750/751 迁移三件套齐全（up+down+boot 接线+双通道+测试，diff 逐项实证）。
- 9785c2398 F-1..F-5 逐项在 diff 实证。
- ff317a231 双修实证（3s→10s、pronargtypes→proargtypes 逐字）。

## 三、机械对账逐提交判定（27 个非 merge，全量）

908256008 ✅ / ff5b10052 ⚠️（软差异，#6）/ ff317a231 ✅ / 9500df65a ✅ / eff61ecdf ✅ / 9785c2398 ✅ / 1dfe88c08 ✅ / f7632f677 ✅ / 1a3b74ccc ✅ / 4af7123f9 ✅ / 9f62818c5 ✅ / ab6c62d32 ✅ / 595380023 ✅ / fe85f577e ✅ / 45c02e915 ✅ / ac007c2f8 ✅ / 9b00c90c6 ✅ / bc68bd1b0 ⚠️（计数 ±1，#5）/ f936779d1 ✅ / 45bb1f18f ✅ / 96a6629c2 ✅ / 67ce8dc16 ✅ / e03764f2a ✅ / 0f03054b7 ✅ / b085eadce ✅ / f022908bf ✅ / 3244c5af1 ✅

**结论**：无"声称 A 文件实改 B 文件"型硬差异；2 个 ⚠️ 均为软差异。清理候选以 #1 最重，#2/#3 合计约 630 行死/遮蔽代码。

## 四、未覆盖项与原因

- scan-secrets.sh 实跑：脚本执行超时未出终裁（全仓扫描慢），终裁留主代理（主代理后台实跑中）。
- codegraph 工具：环境不可用（注：主代理已重建），零引用判定以全仓 grep 为准。
- 库外在制工作树的 gofmt 与 helpers 抽壳状态：超出本仓边界。
- 4 个时间窗外提交的上一轮审计结论：仅做机械对账。
