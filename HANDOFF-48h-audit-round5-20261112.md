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
8. ★★★★ **「凭据监控摘要 500」的第二层根因（本轮最有价值的根因修正）：同一个函数体有【两份载体】，而 boot 的 ensure 链在迁移之后跑 ⇒ 只改迁移会被运行时 SSOT 覆盖回去**：
   - **矛盾读数**：245 重部署日志显示「切换前应用 1 个 pending 迁移 → `962`（sha256=`8d267fadfedb`）」、psql 输出 `BEGIN / CREATE FUNCTION / COMMENT / CREATE FUNCTION / COMMENT / INSERT 0 1 / COMMIT`（**无 ERROR**）、`schema_migrations` 与 `llm_gateway_migration_checksums` **两本记账都有 962 行**（`applied_at` 18:35:22）⇒ ★ **但 `pg_proc.prosrc` 仍是旧谓词** ⇒ **账本说修好了、运行面没修好**。
   - **排除清单**：同名多 schema（仅 public 一份）｜迁移跑了别的库（两本记账就在该库）｜`ON_ERROR_ROLLBACK` 吞错（输出无 ERROR）｜deploy runner 用了别的 `DATABASE_URL`（`_env_file_for_target()` 对 245 返回 `/opt/llm-gateway-go/.env`，与手工查询同源）。
   - **真因**：函数体同时存在于 ① SQL 迁移文件 ② Go 运行时 SSOT（`db/db.go` 的 `recentSuccessRateEnsureSQL`、`db/recent_cache_hit_rate_ensure.go` 的 `recentCacheHitRateEnsureSQL`）⇒ **网关 boot 的 `ensureRoutingRecentSuccessRate` / `ensureRecentCacheHitRate`（`db.go:670/677`）用 `CREATE OR REPLACE` 在迁移之后又写一遍** ⇒ 每台重启一次就把修好的函数体擦回去。
   - **实施**（`23afcd6ef`）：改**载体 ②**（真正生效的那一份）的谓词；新增**跨包等价门** `TestMigration962_RuntimeSSOTCarriesTheSamePredicate`（三方不变式：索引表达式 == 迁移谓词 == SSOT 谓词；且两份载体**归一化空白后逐字相等**）；改造 890/915 两道「逐字等价门」（**折叠一条被钉住的 delta** 再比 ⇒ delta 之外的漂移仍红，**并补正向断言**「SSOT 必须携带索引同形态、旧形态不得回归」，否则折叠＝开了一个「允许退回」的门）；**不需要新迁移**（boot 自愈本身就会收敛）。
   - **变异验证 ×3 全红**：M1 SSOT 退回旧谓词（＝生产实测形态）⇒ 890 门 + 962 跨包门红；M2 只退迁移 ⇒ 962 形状门 + 跨包门红；M3 SSOT 语义微改（采样门槛 `>0`⇒`>=0`）⇒ 915 门 + 跨包门红。
   - ★ **为什么能溜到生产**：上一轮那道 962 形状门**只测载体 ①** ⇒ 在「改了迁移、没改 SSOT」这个形态下**完全绿** ⇒ ★ **门测的是迁移文件，真正生效的是 SSOT**。
9. ★★★★ **第三层缺口＝startup 迁移有【三条投递通道】，962 漏了「211 seamless 通道」那一条**：
   - **三条通道**：① 台账 `docs/db-changelog.md`（门 `TestMigrationRegistryStaysContinuous`）② installer 通道 `installer/internal/dbinit/runner.go` 的 `StartupFiles`（门 `TestStartupManifest`）③ ★ **211 部署通道 `scripts/apply-db-revision-sequence.sh` 的 `files=(...)` 显式清单（此前无门覆盖）** ⇒ 962 只登记了 ①② ⇒ **211 从未应用它**。
   - **211 实测（不对称读数）**：`prosrc` **已带索引同形谓词**（`index_aligned=t`，功能面是好的，boot 自愈写对了）｜★ `schema_migrations` 停在 **961**｜★ `gateway_db_revision_sequences` **无 962 行** ⇒ **211 记账链断层**。
   - **判定依据＝机器比对**：磁盘 632 条 / 清单 327 条，但**编号 ≥900 的全部在清单、唯独 962 不在** ⇒ **疏漏而非取舍**。
   - **实施**：962 补进清单 + `ledger_artifact_probe`（用 `position()` 不用 `LIKE` —— 函数体含正则字面量）＋ **新增第三通道门**（覆盖 / 解析精度 / 例外表带理由）＋ 变异臂（移除清单条目 ⇒ 红并精确定位）。
   - ★ **「记账断层 ≠ 功能缺失」**：两轴必须分别取读数再决定处置哪一轴（见主档 §六之三之三）。
10. ★★★★ **补齐通道登记 ≠ 补齐通道契约**：把 962 补进 `files=(...)` 后，**211 部署当场 abort（EXIT=1）** ⇒ 仓内既有的 **clobber 守卫**（`intentional_function_chains` 白名单）要求登记「有意链」：
   `'recent_cache_hit_rate|915_recent_cache_hit_rate.sql|962_recent_success_rate_index_aligned.sql|'`
   （★ 签名/返回契约/采样窗口全不变 ⇒ 调用方零改动；★ 同名 `recent_success_rate` 不需要——890 已被整行注释移出自动重放列表）⇒ 补登后 `bash scripts/apply-db-revision-sequence_test.sh` ⇒ **contract passed rc=0**。
   - ★ **通道不是「一个数组」，而是「数组 + 守卫 + 探针 + 账本」四件套**。
   - ★ **部署 abort 时线上仍是上一版**（fail-closed 的可靠形态）⇒ 部署失败后必须先确认「线上跑的是哪一版」。
   - ★★ **仓内本来就有那道门**：上一轮全量套件的第二个红包（`scripts/ursmcheck` 的 `TestTsvHollowoutReadersRejectEmptyRegistry`，100 s）**已逐字报出**「962 exists but is missing from the channel files=(...) array」⇒ 本轮只读了包名没读失败项 ⇒ **每道红都要读到失败原文**。

## 发布真值与验证

| 目标 | 应用 sha / 槽位 | 验收 |
|---|---|---|
| **211** | `422e2c90e` / 槽 **b** @8783（Docker 蓝绿；nginx upstream 8781→8783；goacos 已登记；公网 200） | ★ **冒烟全绿 8/8**：`/healthz` 200、`/readyz` 200、`POST /api/auth/token` **200 + token（289 字符）**、`/api/credentials/monitor-summary` **200（55 846 B）**、`/api/admin/topology/providers-today` 200、`/api/admin/request-anomalies` 200、`/api/system/bootstrap/status` 200；★ **记账链闭合**：`schema_migrations` 960/961/**962**（19:11:16，无断层）＋ `gateway_db_revision_sequences` 有 962（sha 逐字一致）＋ 两个函数 `index_aligned=t`；★ 部署后 PG 慢日志**已无 10 s 级查询**（最大 4.2 s＝族 B） |
| **245** | `3027-422e2c90`（systemd + 符号链接 release；旧 `3023-0e76c24c`；总 224 s / 切换 16 s；nginx 路由身份仲裁通过） | 冒烟 7/8 绿；★ `/api/credentials/monitor-summary` **仍 500**（`duration_ms=15007`）⇒ ★★ **真凶已二分定位到 `total_requests` 的两臂 COUNT**（不是谓词！）⇒ **本轮登记不修**（裁决面＝口径），见「遗留与下一轮」第 5 条 |
| **154** | `3028-422e2c90`（旧 `3021-86cc4a24`；总 202 s / 切换 13 s；**活跃端口 8781**） | 冒烟 7/8 绿；★ 同一条 `/api/credentials/monitor-summary` **500**（与 245 共用 252 同一套库 ⇒ 同一失败、同一个真凶） |

★ **三台部署均为 EXIT=0**；211 的部署器登录冒烟按纪律未跑（不把口令放进环境变量/命令行）⇒ 改用仓内 `scripts/audit/deploy-smoke-admin.sh`（口令只从环境变量读、文件内零凭据字面量）在目标机上跑 ⇒ 211 部署日志里那条 `WARN: 登录冒烟未执行` 是**预期的**。
★ **部署面两次 abort（fail-closed 的可靠形态）**：211 第二次部署在 DB 修订序列阶段 abort（EXIT=1，clobber 守卫报「962 与 915 重定义同一函数」）⇒ 按消息逐字补登「有意链」后契约 `passed`；★ **abort 时切流没有发生 ⇒ 线上仍是上一版** ⇒ 部署失败后必须先确认「线上跑的是哪一版」。

★ **部署方式**：211 走**冻结树部署**（`git archive HEAD` 出冻结树 → 冻结树内构建前端 → `LLM_GATEWAY_ROOT` 指向它 + `DD_SKIP_WEB_BUILD=1`）⇒ ★ **部署物可从 commit 复现**，且不受并行会话在途编辑干扰（工作树并发编辑会让「构建→同步」必然踩前端新鲜度门 —— 本轮真实踩了两次）；245/154 走 systemd + release 包。★ 211 的 ssh 通道用 **`211-mesh`**（100.106.67.198，683 KB/s）—— 本机到 `192.168.31.211` 直连在 netbird relay 1/5 时不通。

★ **「凭据监控摘要 500」的实施与验证（主档 §六之三 / §六之三之二）**：迁移 `962` 把 `recent_success_rate` / `recent_cache_hit_rate` 的谓词改成与既有索引 `idx_session_turns_hot_credential_ts` **逐字同形** ⇒ 真库事务内对照 **13184 ms → 94 ms**（140 倍）⇒ 结构化钉测 + 变异臂红 ⇒ ★★ **但本轮部署后端点仍 500** ⇒ ⇒ **本轮用逐段二分纠正了归因**（详见「遗留与下一轮」第 5 条）：谓词面**确实**修好了（1965 次调用 → 0.044 ms/次、该段 882 ms），但**真凶**是 `total_requests` 的两臂 COUNT（141 万行母表 ⇒ 单凭据 12 s 超时）⇒ ★ **该端点的 500 未修复**，登记不修（裁决面＝口径）。

★ **「凭据监控摘要 500」的第二层根因与实施（主档 §六之三之二）**：同一个函数体有**两份载体**（SQL 迁移文件 + Go 运行时 SSOT `db.recentSuccessRateEnsureSQL` / `db.recentCacheHitRateEnsureSQL`），而 **boot 的 ensure 链在迁移之后跑** ⇒ 只改迁移会被覆盖回去 ⇒ 245 实测「迁移 962 已记账、函数体仍是旧形态」⇒ 修 SSOT + 新增跨包等价门 + 改造 890/915 逐字等价门（折叠钉住的 delta + 正向断言）⇒ 变异 ×3 全红 ⇒ **211/245/154 三台的函数体现在都是索引同形形态**。

★ **第三/四层缺口（主档 §六之三之三）**：startup 迁移有**三条投递通道**，962 漏了「211 seamless 通道」⇒ 补 `files=(...)` + `ledger_artifact_probe` ⇒ ★ 又撞 clobber 守卫（EXIT=1）⇒ 补 `intentional_function_chains`「有意链」⇒ 契约 `passed` ⇒ **211 的记账链已闭合（960/961/962 无断层）**。
★ ★ **★ 仓内本来就有那道门**：上一轮全量套件的第二个红包（`ursmcheck` 的 `TestTsvHollowoutReadersRejectEmptyRegistry`）**已逐字报出**「962 漏 channel files=(...)」⇒ 上一轮只读了包名没读失败项 ⇒ ⇒ **每道红都要读到失败原文**。

## 遗留与下一轮

1. ★ **ASR/TTS 在 211 上仍会 503**（待运营处置那 6 个凭据：主档 §4.4 的 SQL + 复核口径）。
2. ★ **F1 口径分叉**（`status='active'` vs `status IN (active,cooling,degraded)`）＝潜伏分叉，活体影响当前 0 ⇒ 登记不修。
3. ★ **PG 慢查询本轮已取证**（主档 §六）：三族全部指向本仓定时任务（半点物化视图刷新 2.4 s / 每小时桶聚合 6.7 s / 单次凭据列表 10.75 s），**网关热路径在 500ms 阈值之上零记录**；★ 但上一轮的「`session_bodies` jsonb::text 全表扫描」惯犯**未复现也未排除**（未取 `pg_stat_statements`）。
4. ★ **凭据列表在生产数据量下是秒级慢查询**（单次 10.75 s；`/api/credentials/monitor-summary` 返回 56 KB）⇒ 本轮**登记不修**（动 admin 读面需与运营口径对齐）⇒ **下轮优化首项**。
5. ★★★★★ **【本轮最重要的遗留】`/api/credentials/monitor-summary` 在 245/154 上**仍然 500**（实测 `duration_ms=15007`）—— 真凶已二分定位，但**没修**（落在口径/读面裁决面）：
   - ★ **真凶**＝`total_requests` 的两臂 COUNT（`session_turns_hot` ∪ `session_turns`）⇒ 252 库母表 **1,411,419 行 / 7 分区** ⇒ **单凭据计数 12 s 超时** ⇒ 74 个凭据各一次 ⇒ 必越 15 s。
   - ★ **不是**「索引没被用上」：计划显示索引**确实命中**，但该凭据在 2026_09/2026_10 各有 **8.9 万/4.2 万行**匹配 ⇒ `Bitmap **Heap** Scan` 要回表十几万行才能数 ⇒ **根因是「要数完」，不是「找不到」**。
   - ★ **同口径的任何改法都过不了 15 s**（`GROUP BY` 形态 **23.4 s** 实测）⇒ 形状不是问题。
   - ★ **四条路径（都动口径或架构 ⇒ 裁决面＝运营/产品）**：① 口径变更（改近窗计数）② 换 `credential_model_call_history` 预聚合（1min 桶，但 24 h 窗口径）③ **异步物化（口径零变化，工程量最大）** ④ 接受 500 ⇒ **建议按 ③ 立项**（唯一不触碰裁决面的路）。
   - ★ **本轮已修好的那一半（必要但不充分）**：谓词与索引同形 ⇒ **1965 次 LATERAL 调用从「每次全窗扫描 ~13 s 级」变成 0.044 ms/次**（`Index Scan using idx_session_turns_hot_credential_ts` 命中）⇒ 那一段 **882 ms**；并**顺带修掉 211 的族 C（10.75 s）** ⇒ 211 部署后 PG 慢日志已无 10 s 级查询。
   - ★★ **上一轮主档 §六之三 的「500 = 谓词不是索引键表达式」归因已被纠正**（那是必要条件、不是充分条件）⇒ 其「端点由 500 变 200」的声称**作废**。
5. ★ **245/154 的运行面取证**：见主档 §六之二。
6. ★ **注入检测**与**敏感信息脱敏/恢复**两域**未完成逐条审计** ⇒ 下轮首项（本轮只覆盖了仓内门与 211 运行面的可机检部分）。
7. ★ **`llm-gateway-go-cursor` 的未合并分支仍在**（机器人正在该 clone 跑套件）⇒ 待机器人收工后由属主处置。
8. ★ **并行会话仍在改工作树**：本轮观测到的在途件＝8 语种 `tenants.ts`、`TenantDetailView.vue`、`element-dark.css`、`theme.dark-skin.test.ts`、`scripts/deploy-211.sh`（双通道 ssh）、`VERSION`/`version.json`/`web/public/version.json`/`web/public/menu-config.json`、`docs/db-changelog.md` ⇒ ★ **其 i18n 修复已由本轮代落库**（`86cc4a24d`，因为它修的正是 origin/main 上的红），**其余在途件一律未纳入**（不毁别人在途）⇒ 其落地后需再跑颜色/i18n 基线门。
9. ★ **`internal/bucket` 休眠框架**：本轮只修了它的测试假红；adapter 层没有 clock 注入 ⇒ 彻底解法（可注入时钟）留给该框架真正接线那一轮。
10. ★ **提交信息自陈「有红仍推」的提交，其责任面在提交方**（`638fdea33` 明写「唯一红为并行会话 WIP 的 hardcoded_cjk 棘轮」仍照推）⇒ 下一轮应把「推送前跑全量」的纪律落到 CI/钩子，而不是只靠审计轮自觉。

## 资产与记录

- 主档：`docs/audit/2026-11-12-48h审计第五轮-主档.md`
- **本轮提交序列（前半段）**：`00351f53a`（merge）→ `bca112f9a`（go mod tidy）→ `a11c76ff3`（prompt-optimizer 落库）→ `f860792f0`（桶自述文档+注记）→ `5e43eeb56`（既有红收口）→ `667d566c8`（候选空归因诊断+主档）→ `7ef9dd6e3`（桶测试窗口假红）→ `fedd65144`（第五轮 handoff）→ `86cc4a24d`（i18n CJK 棘轮真红修复）→ `838d2594f`（迁移 962）→ `0e76c24cf`（主档回填 + LESSONS §696/§697）
- **本轮提交序列（续，本会话）**：
  | 提交 | 内容 |
  |---|---|
  | `23afcd6ef` | fix(db)：**两层载体漂移**（迁移文件 vs Go 运行时 SSOT）⇒ 改 SSOT + 跨包等价门 + 890/915 门改造（折叠 delta + 正向断言）⇒ 变异 ×3 全红 |
  | `e5f44819a` | fix(db)：**第三条投递通道漏登记** ⇒ 211 通道 `files=(...)` 补 962 + `ledger_artifact_probe` + 新增第三通道门 ⇒ 变异臂红 |
  | `422e2c90e` | fix(db)：**clobber 守卫「有意链」补登** ⇒ 契约 `passed` rc=0 |
  | `59657b9f2` | docs(audit)：**★ 纠正上一轮归因** + 三台部署读数 + LESSONS §698/§699/§700 |
  | `a5e6a87d5` | Merge：并行会话的 4 笔（`d21037e99` 自动同步 / `7a8e43030` 三处红收口 / `e3db4a8d5` IR 变异脚本 shell 站点 / `291912dbf` 两份错误模式审计报告）⇒ 合并后复跑关键门（`internal/sqlguard` / `sql/migrations/startup` / `db` + 契约脚本）全绿 |
- 211 取证快照：`/tmp/lgw-audit-1015/gateway-current.json.log`（75MB / 135065 行 / 2026-11-11T20:23Z→11-12T02:15Z）
- 本轮全量套件日志：`/tmp/lgw_verify4.log`（2 红包，@`0e76c24cf`）→ `/tmp/lgw_verify5.log`（EXIT=0 / 382 包 ok，@`422e2c90e`）→ `/tmp/lgw_verify6.log`（合并后终态，@`a5e6a87d5`）
- 本轮部署日志：`/tmp/deploy_211_3024.log`（EXIT=0）→ `/tmp/deploy_211_3025.log`（**EXIT=1**，clobber 守卫 abort）→ `/tmp/deploy_211_3026.log`（EXIT=0）／`/tmp/deploy_245_3026.log`（EXIT=0）／`/tmp/deploy_154_3027.log`（EXIT=0）

## 下一轮提示词

```
对 llm-gateway-go 做下一轮 48h 审计与完善，优先做三件（按顺序）：
1) ★★★【本轮未修、下轮首项】`/api/credentials/monitor-summary` 在 245/154 上**仍然 500**（实测
   duration_ms=15007）。主档 §六之三之三 与 §八第 12 条已把真凶二分清楚：**不是** `recent_success_rate`
   的谓词（那一面本轮已修好：1965 次调用 → 0.044 ms/次、Index Scan 命中），而是 **`total_requests` 的
   两臂 COUNT**（252 库 `session_turns` 母表 1,411,419 行 / 7 分区 ⇒ 单凭据计数 12 s 超时 ⇒ 74 个凭据
   各一次 ⇒ 必越 15 s；索引**确实命中**，但 `Bitmap Heap Scan` 要回表十几万行 ⇒ 根因是「要数完」）。
   ⇒ 本轮**登记不修**（裁决面＝运营/产品：那个数字是什么意思）。下轮请**先出四条路径的代价与口径影响**
   （① 改近窗口径 ② 换 credential_model_call_history 预聚合（1min 桶，24h 窗口径）③ 异步物化
   汇总表（**口径零变化**，工程量最大，建议按此立项）④ 接受 500），**由运营/产品拍板后再实施**；
   实施时必须带真库对照（252 上 EXPLAIN ANALYZE + 端点 HTTP 计时，211 与 245/154 两侧都要）。
2) 注入检测与敏感信息脱敏/恢复两域的**逐条审计**（本轮仍未完成）：先读主档 §八第 6 条与 docs/audit/**
   既有登记，找出**攻击面 vs 检测面**的缺口与**泄漏面**，每条给出 file:line + 可复现触发路径；
   先跑仓内门（make guards / scripts/run-injection-test.sh），再判「门绿但门测的不是那件事」的形态；
   修复要带钉测 + 变异验证。
3) 运营面收口：按第五轮主档 §4.4 的 SQL 与复核口径，把 211 上 6 个「status='disabled' ∧
   lifecycle_status='active'」的凭据（whisper×2 / tts-macos-say / glm 家族 / minimax 家族）与 F1 口径分叉
   **由运营拍板**后实施，并复测 ASR/TTS 端点不再 503；注意 DB 改完要复核候选数与 5xx 族。

纪律（本轮新增，必须照做）：
· ★ **全量套件红了之后，「失败包名」不是读数，「失败项的原文」才是** —— 上一轮的第二个红包
  （ursmcheck 的 TestTsvHollowoutReadersRejectEmptyRegistry）**本来就已经逐字报出了**「962 漏 channel
  files=(...) array」，却被当成普通待办 ⇒ 每道红都要读到失败原文，且在读之前不要开始猜。
· ★ **同一条慢查询有多个候选热点 ⇒ 必须逐段二分**（把候选逐个摘掉再测），并接受「我上一轮的归因可能是
  错的」；★ **「修好了一个真缺陷」≠「端点变绿」**（验收分机制层与端点层两层）。
· ★ **改一个对象前先问「它还有几份副本、谁最后写」**（迁移文件 / Go 运行时 SSOT / 基线 schema /
  投递通道清单 / clobber 白名单）—— ★ 通道＝数组 + 守卫 + 探针 + 账本 四件套，补一处不算补齐。
· ★ **新增 startup 迁移必须三条通道同时登记**（台账 / installer StartupFiles / 211 的
  apply-db-revision-sequence 清单）＋ 若重定义了既有函数，还要在 `intentional_function_chains` 登「有意链」。
· 合并 origin/main 后**先跑全量套件**（go test ./... -count=1 -timeout=2400s）；台账漂移一律走仓内官方
  生成器重算（禁止手改台账）；子分支裁决逐文件进行；并行会话的在途文件**不纳入提交也不删除**；
  ★ 推送前必须**再 fetch**（并行会话每几十分钟推一次，本轮就被非 FF 拒过一次）⇒ 合并后复跑关键门再推。
· 部署 211→245→154 并用 admin/Veritrans&9527 验证（口令只经 stdin 传给仓内
  scripts/audit/deploy-smoke-admin.sh，**不进命令行/环境变量/仓库**）；部署用冻结树
  （git archive HEAD → 冻结树内重建前端 → LLM_GATEWAY_ROOT + DD_SKIP_WEB_BUILD=1），
  部署进程用 nohup bash -c '…' & 且父 shell 立即退出。
```
