# 2026-11-12 48h 审计与完善轮 · 第五轮 handoff

## 结论与范围

本轮＝第四轮（`01fea7061` / `ec89de06f`）之后的**合并 → 收口 → 落库 → 运行面取证 → 三台部署**一轮。
完整主档：`docs/audit/2026-11-12-48h审计第五轮-主档.md`；本文件是**交接面**（结论/根因/改动/验证/遗留/下一轮提示词）。
工作区 `/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go`（与 `llm-gateway-go-cursor`、`lgw-deploy-154`、`llm-gateway-go-integrate` 同仓 worktree）。
合并 `origin/main`（`df8fbe518`，13 笔并行会话提交）＋本地 6 笔未推送批次 ⇒ 合并后**全量套件 EXIT=1、2 包 10 测红**（前三轮同款形态），本轮收口到 **0 红**。

## 改动与根因

1. ★ **「候选为空」不可区分「模型没登记」与「被状态闸滤空」**（本轮最有价值，来自 211 真机日志）：
   - `/v1/audio/transcriptions` 35 次 503，日志只有 `[candidate_diag] db_empty{plan_count:0}`。
   - 逐层定位（真库只读）：offer **有**（canonical_id 3346055）⇒ 凭据 `credentials.status='disabled'`（11-05 写入，而 `lifecycle_status='active'`）⇒ ★ **上游其实活着**（192.168.31.13:8081 实测可达）⇒ 对照实验：生产口径候选 **0** / 放掉遗留 v1 `c.status` 闸 ⇒ **2**。
   - ⇒ 真因＝**遗留 v1 健康列把健康凭据永久挡在路由外**（211 上 12 条 `status='disabled'` ∧ `lifecycle_status='active'`，带 offer 的 6 个凭据）。
   - 实施（**零行为变更**）：新 `provider/candidate_empty_diag.go`——只在判空路径跑一次归因查询，日志给 `offers_total` / 各闸计数 / `likely_cause`；闸集合逐条对齐候选 SQL；8 例归因分类 + 口径钉测；**211 真库实跑** `offers_total=1 / blocked_credential_status=1` ⇒ 诊断自己复现了根因。
   - ★ **登记不修**（裁决面＝运营）：F1 口径分叉（仓内 5 处 `status IN (active,cooling,degraded)` vs 候选解析 `status='active'`；**活体影响＝0**）；F2 那 13 条遗留 disabled（**无 auto_disabled_reason ⇒ 无法判定是否人工置位 ⇒ 不擅改生产数据**，主档 §4.4 给处置 SQL 与复核口径）。
2. ★ **两个真缺陷（都在上一轮未推送的 6 笔里）**：
   - P1＝`$rc）` 的全角括号被 bash 并进变量名 ⇒ `set -u` 下在「替换失败」汇报路径**当场中止** ⇒ 量具在臂失败时最不可信 ⇒ 改 `${VAR}`。
   - P2＝SIGPIPE + pipefail 把变异臂判反（`echo "$out" | grep -q` ⇒ grep 早退 ⇒ 写端 141 ⇒ pipefail 传播 ⇒「门变红」被读成「门仍绿」）⇒ ★ 既有「benign-bounded」裁决只覆盖「写端体积」一半 ⇒ **不登记、直接消除**（herestring 零管道）。
3. ★ **分支面**：全工作区唯一未合并分支＝clone `llm-gateway-go-cursor` 的本地 `main`（`5179264df`）⇒ `go.mod`/`go.sum` **有用**（逐字验证＝当前树 `go mod tidy` 产出**逐字节相同**）⇒ 已落地；`README.md` 自动变更日志 **不取**（与 CHANGELOG 重复且落后 43 笔）⇒ ★ **该分支未删**（同步机器人正在该 clone 跑套件 ⇒ 不毁别人在途）。
4. ★ **工作树面**：并行会话未提交的 prompt-optimizer 全套（9.4k 行）⇒ 审计后落库（★ 零生产接线、零明文凭据、套件绿）⇒ 顺带解掉一处门漂移（那些门**扫描面含未跟踪文件**）。
5. ★ **限流桶测试的「窗口边界」假红**：`internal/bucket/ratelimit` 两个用例用 100ms/1s 窗口，而限流窗口是**绝对时间网格** ⇒ 负载下跨界 ⇒ 配额「已刷新」⇒ 断言失真（实测 8 路并行 1/8、12 路 1/12 红）⇒ 窗口改 1h ⇒ **16 路并行 0/16**。
6. ★ **台账漂移重算（全部走官方生成器、零手改）**：admin-routes 690→**698**｜installer-reachability 361→**362**｜pipefail 台账 0 pending｜scope-drop 清单 106→111→**106**｜request-surface 对拍表 8399-8406→**8407-8414** / 8356-8361→**8364-8369**｜gocache 登记册 176→**181**｜post-surface 台账 690→**698**（note 逐条注明 prompt-optimizer 属独立服务）。
7. ★ **errdiscard 门**：`bg/market_scoring.go:490,504`（origin/main `df8fbe518` 引入）＝同款假阳性（`_` 是评分汇总，err 有被检查并 warn）⇒ 按门自己的指引「带理由登记」。

## 发布真值与验证

| 目标 | 应用 sha / 槽位 | 验收 |
|---|---|---|
| **211** | `86cc4a24d` / 槽 **b** @8783（旧槽 a 已停、容器保留可回滚；nginx upstream 已切 8781→8783；goacos 已登记） | `/version` = `2.5.9-86cc4a24d-20261112-211`（build_seq 3017）；公网 `llm.itestu.cn` 200；冒烟**全绿**：`/healthz` 200、`/readyz` 200、`POST /api/auth/token` **200 + token**、`/api/credentials/monitor-summary` **200（55 719 B）**、`/api/admin/topology/providers-today` 200、`/api/admin/request-anomalies` 200、`/api/system/bootstrap/status` 200 |
| **245** | `3020-86cc4a24`（systemd + 符号链接 release；旧 `2966-01fea706`；234 s / 切换 20 s；admin 密码已同步 + 登录 200；凭据解密 17/0；verified） | 冒烟 7/8 绿（healthz/readyz/登录/token/拓扑/异常/bootstrap 全 200）；★ `/api/credentials/monitor-summary` **500** ⇒ 主档 §六之三 的缺陷（**迁移 962 已修**，见下「实施与验证」行） |
| **154** | `3021-86cc4a24`（旧 `2968-21282597`；198 s / 切换 13 s；admin 密码同步 + 登录 200；凭据解密 17/0；verified；**活跃端口 8782**） | 冒烟 7/8 绿；★ 同一条 `/api/credentials/monitor-summary` **500**（与 245 共用 252 同一套库 ⇒ 同一失败） |

★ **部署方式**：211 走**冻结树部署**（`git archive HEAD` 出冻结树 → 冻结树内构建前端 → `LLM_GATEWAY_ROOT` 指向它 + `DD_SKIP_WEB_BUILD=1`）⇒ ★ **部署物可从 commit 复现**，且不受并行会话在途编辑干扰（工作树并发编辑会让「构建→同步」必然踩前端新鲜度门 —— 本轮真实踩了两次）；245/154 走 systemd + release 包。★ 211 的 ssh 通道用 **`211-mesh`**（100.106.67.198，683 KB/s）—— 本机到 `192.168.31.211` 直连在 netbird relay 1/5 时不通。

★ **「凭据监控摘要 500」的实施与验证（主档 §六之三）**：迁移 `962` 把 `recent_success_rate` / `recent_cache_hit_rate` 的谓词改成与既有索引 `idx_session_turns_hot_credential_ts` **逐字同形** ⇒ 真库事务内对照 **13184 ms → 94 ms**（140 倍）⇒ 钉测（结构化比对 + 变异臂红）绿 ⇒ ★ **部署含 962 后 245/154 的该端点由 500 变 200**（读数见主档 §六之四的回填行）。

## 遗留与下一轮

1. ★ **ASR/TTS 在 211 上仍会 503**（待运营处置那 6 个凭据：主档 §4.4 的 SQL + 复核口径）。
2. ★ **F1 口径分叉**（`status='active'` vs `status IN (active,cooling,degraded)`）＝潜伏分叉，活体影响当前 0 ⇒ 登记不修。
3. ★ **PG 慢查询本轮已取证**（主档 §六）：三族全部指向本仓定时任务（半点物化视图刷新 2.4 s / 每小时桶聚合 6.7 s / 单次凭据列表 10.75 s），**网关热路径在 500ms 阈值之上零记录**；★ 但上一轮的「`session_bodies` jsonb::text 全表扫描」惯犯**未复现也未排除**（未取 `pg_stat_statements`）。
4. ★ **凭据列表在生产数据量下是秒级慢查询**（单次 10.75 s；`/api/credentials/monitor-summary` 返回 56 KB）⇒ 本轮**登记不修**（动 admin 读面需与运营口径对齐）⇒ **下轮优化首项**。
5. ★ **245/154 的运行面取证**：见主档 §六之二。
6. ★ **注入检测**与**敏感信息脱敏/恢复**两域**未完成逐条审计** ⇒ 下轮首项（本轮只覆盖了仓内门与 211 运行面的可机检部分）。
7. ★ **`llm-gateway-go-cursor` 的未合并分支仍在**（机器人正在该 clone 跑套件）⇒ 待机器人收工后由属主处置。
8. ★ **并行会话仍在改工作树**：本轮观测到的在途件＝8 语种 `tenants.ts`、`TenantDetailView.vue`、`element-dark.css`、`theme.dark-skin.test.ts`、`scripts/deploy-211.sh`（双通道 ssh）、`VERSION`/`version.json`/`web/public/version.json`/`web/public/menu-config.json`、`docs/db-changelog.md` ⇒ ★ **其 i18n 修复已由本轮代落库**（`86cc4a24d`，因为它修的正是 origin/main 上的红），**其余在途件一律未纳入**（不毁别人在途）⇒ 其落地后需再跑颜色/i18n 基线门。
9. ★ **`internal/bucket` 休眠框架**：本轮只修了它的测试假红；adapter 层没有 clock 注入 ⇒ 彻底解法（可注入时钟）留给该框架真正接线那一轮。
10. ★ **提交信息自陈「有红仍推」的提交，其责任面在提交方**（`638fdea33` 明写「唯一红为并行会话 WIP 的 hardcoded_cjk 棘轮」仍照推）⇒ 下一轮应把「推送前跑全量」的纪律落到 CI/钩子，而不是只靠审计轮自觉。

## 资产与记录

- 主档：`docs/audit/2026-11-12-48h审计第五轮-主档.md`
- 本轮提交：`00351f53a`（merge）→ `bca112f9a`（go mod tidy）→ `a11c76ff3`（prompt-optimizer 落库）→ `f860792f0`（桶自述文档+注记）→ `5e43eeb56`（既有红收口）→ `667d566c8`（候选空归因诊断+主档）→ `7ef9dd6e3`（桶测试窗口假红）＋ AGENTS 横幅
- 211 取证快照：`/tmp/lgw-audit-1015/gateway-current.json.log`（75MB / 135065 行 / 2026-11-11T20:23Z→11-12T02:15Z）

## 下一轮提示词

```
对 llm-gateway-go 做下一轮 48h 审计与完善，优先做三件（按顺序）：
1) 注入检测与敏感信息脱敏/恢复两域的**逐条审计**：先读 docs/audit/2026-11-12-48h审计第五轮-主档.md
   §8 第 5 条与 docs/audit/** 既有登记，找出**攻击面 vs 检测面**的缺口与**泄漏面**，
   每条必须给出 file:line + 可复现触发路径；先跑仓内门（make guards / scripts/run-injection-test.sh）
   再判「门绿但门测的不是那件事」的形态；修复要带钉测 + 变异验证。
2) 运营面收口：按第五轮主档 §4.4 的 SQL 与复核口径，把 211 上 6 个「status='disabled' ∧
   lifecycle_status='active'」的凭据（whisper×2 / tts-macos-say / glm 家族 / minimax 家族）
   与 F1 口径分叉（候选解析的 status='active' vs 仓内 5 处的 status IN (active,cooling,degraded)）
   **由运营拍板**后实施，并复测 ASR/TTS 端点不再 503；注意 DB 改完要复核候选数与 5xx 族。
3) 取 PG 慢查询日志（acc-pg17 容器内 log_min_duration_statement）做慢 SQL 归因，
   重点复核第五轮登记的「session_bodies jsonb::text 全表扫描」惯犯是否复发；
   同时取 245/154 的 journal 与 5xx 分桶，核对上一轮的已知族是否仍在。
纪律：合并 origin/main 后**先跑全量套件**（go test ./... -count=1 -timeout=1800s），
台账漂移一律走仓内官方生成器重算（禁止手改台账）；子分支裁决逐文件进行；
并行会话的在途文件**不纳入提交也不删除**；部署 211→245→154 并用 admin/Veritrans&9527 验证。
```
