# 子审计 C — error-playbook 全家桶 meta-audit（第 62 增量审计轮）

- 审计对象：窗口 `c6bf29aea..c446de9a4` 内 error-playbook 五批改动（83cfb9633 / 0c3474646 / 98221b41d / 812bdce65+49543a07d / f712ea685）
- 审计方法：只读（git 对象级 diff 复算 + 当前态逐文件精读 + go build/vet/test 实跑 + web 门禁实跑）；**未连接任何生产/远程库**。真库契约腿因本地 kx-citus（127.0.0.1:15433）TCP 可达但协议停摆（psql 亦挂起）无法独立复跑，静态复核代替，见 §8。
- 结论速览：**无新增 P0/P1**。f712ea685 的 7 项修复全部真实、自洽、在位；83cfb9633 的 F1-F7 全部在位且未被 worker 大改破坏。发现 **P2×3**（修复零回归钉测 ×1、采样器写仓库工作树 ×1、探针行数对拍 limit 错位潜伏假红 ×1）与 P3×9。

---

## 1. Q1 — f712ea685 errorplaybook diff 逐项 meta-audit

### 1.1 逐项判定表

| 项 | 判定 | 证据 |
|---|---|---|
| F1 摄取 SQL 两缺陷 | ✅ 修复真实、自洽 | 诊断前提核实：`sql/migrations/startup/037_candidate_failure_logs.sql:28` `provider_id INT NOT NULL` ⇒ `COALESCE(provider_id,'')` 必然 22P02。修法① `provider_id::text`：`domains/errorplaybook/worker.go:243`、`logcontext.go:34`；`error_message COALESCE`：worker.go:242。修法② `pq.Array(&samples/&providers/&models)`：worker.go:271 |
| F1 ② pq.Array 驱动兼容 | ✅ 成立 | `piSQLDB = stdlib.OpenDB(*dbConn.Pool().Config().ConnConfig)`（`cmd/gateway/main.go:7772`）＝ pgx v5 stdlib（go.mod:20 pgx v5.9.2）——未特判 OID 数组按 string 返回，`pq.Array` 的 Scanner 接受 string/[]byte 文本格式均可解析。依赖在场：go.mod:30 `lib/pq v1.12.3`；同手法仓内先例：`domains/autocombo/resolver.go:107`（Scan 侧 `pq.Array(&spec.TierFilter)`）、`domains/freeresource/quota_tracker.go:135` |
| F5 worker 吞错 | ✅ | `worker.go:66-78`：`runOnce` 返回值接住 → `slog.Error("error_playbook: worker round failed", "error", err)`；sleep 改 `select { ctx.Done / time.After }`（worker.go:72-76） |
| F5 日志敏感性 | ✅ 无敏感回显 | err 链为 SQL 层错误：22P02 回显的是常量字面 `""`；Scan 错误回显 Go 类型名；查询参数（时间戳）不在错误文本里。`candidate_failure_logs_hot.error_message`（供应商错误原文，可能夹带请求片段）只作为**数据列**被读取，不出现在任何错误回显路径 |
| F6 水位线 | ✅ 方向正确，边界见 §1.2 | 不再采信 FetchWindow maxTS：worker.go:101-104；只按已处理分组推进：worker.go:119-135（`processedWatermark`）；推进块：worker.go:179-192（空窗推进到 `until`，防窗口单调变宽）；SQL `ORDER BY MAX(ts) ASC`：worker.go:249 |
| F7 title NOT NULL | ✅ | `store.go:483` `title = COALESCE(NULLIF($2,'')::text, title)`（原 `NULLIF($2,'')::text` 撞 TEXT NOT NULL） |
| F8 typed-nil | ✅ 且 errorplaybook 装配面无同族残留 | `main.go:7859-7867`：`autoIndexRefresher`（main.go:367，`*bg.AutoIndexRefresher`）与 `modelCredibilityWorker`（main.go:4301，`*bg.ModelCredibilityWorker`）都是**具体指针**，判空发生在装箱之前 ⇒ 消费侧 `deps.IndexRefresh != nil`（error_playbook_init.go:241/254）恢复真实语义。同族全查见 §1.3 |
| F22 timeout_ms | ✅ | `actions.go:336-359` `apiPlanTimeoutMS`（float64/float32/int/int32/int64/json.Number 全认）+ `actions.go:418` 接入。链路闭合：归一化产 int（plan.go:210 `params["timeout_ms"] = timeoutMS`，int）、DB JSON 往返产 float64 |
| F23 logcontext | ✅ | `logcontext.go:34` `provider_id::text`；其余 Scan 形状安全（int64/`sql.NullString`） |

### 1.2 F6 新水位线语义的边界精算（本审计重点）

当前态逐点推演（窗口半开 `ts > $1 AND ts <= $2`，SQL 按 `MAX(ts) ASC` + `LIMIT 20`，worker cap = `MaxEventsPerRound`，默认 10）：

1. **无重复** ✅：watermark 只推进到**已处理**分组的 max LastSeen；下轮窗口 `ts > watermark`，已处理事件不会再被取。半开窗保证不重。
2. **边界损失①（推迟分组的事件欠计）**：设本轮处理了 ASC 序前 10 名（最旧），`W1 = max(已处理 lastTS)`。第 11~20 名（推迟）分组 `MAX(ts) >= W1`——**组不会丢**（下轮重读），但其 `ts ∈ (旧水位, W1]` 的事件永远落在新窗口之外 ⇒ 该窗口切片的事件**只进过本轮未处理的聚合、再无重读机会** ⇒ Occurrences 欠计。`MAX(ts) == W1` 精确同刻的推迟分组，其整个窗口切片丢失。这是 83cfb9633 轮 R4（`ts > maxTS` 严格大于同刻漏计）的同族残留收窄形：R4 是「全组 maxTS 推进 ⇒ 组消失」，现在是「组保住、组内 W1 之前事件欠计」。ts 为微秒精度，同刻概率极低，影响=聚合欠计而非原始行丢失 ⇒ **P3 登记不修**（建议注释钉明该边界即可）。
3. **时钟回拨** ✅：`until = now-2s` 可能小于 watermark ⇒ 空窗 ⇒ `nextWatermark = until` 不晚于 watermark ⇒ `if nextWatermark.After(w.watermark)` 不推进 ⇒ 水位不回退。回拨期间落库的 `ts < watermark` 事件 inherent 被跳过（与旧语义相同，非本轮回归）。
4. **空窗推进** ✅：`processed == 0 && len(events) == 0 ⇒ nextWatermark = until`（worker.go:186-189）。`len(events) > 0 && processed == 0` 不可能（`limit = maxInt(cfg.MaxEventsPerRound, 1)` ≥ 1，cap 检查在 HandleEvent 之前 ⇒ 非空窗至少处理 1 个）。
5. **ErrNotProvisioned 早退**（worker.go:136-140）：`return nil` 在推进块之前 ⇒ 水位不动 ⇒ 已处理的 1..k-1 个事件下轮重处理（重复 hit/留痕行）。但 ListEnabled/RecordExecution 的 ErrNotProvisioned 是表级全有全无 ⇒ k-1 混合态实际不可达 ⇒ 可忽略。
6. **瞬时失败 at-most-once**：HandleEvent 非 provisioned 失败只 WARN（worker.go:141-143），`processedWatermark` 已在 res.Err 检查**之前**推进（worker.go:132-135）⇒ 失败事件不重试、水位照走。属水位语义的固有属性（旧语义更差——按全组 maxTS 走），登记即可。
7. **重启回看**：watermark 仅内存、初始 `now-15min`（worker.go:55）⇒ 重启后 15 分钟窗口重读 ⇒ 已处理事件重复 hit/awaiting 行（自动执行被条目冷却期挡住，`engine.go:123`）。与旧版一致。
8. **吞吐**：ASC + 只按已处理推进把「消费 20 组/轮」变成「真正处理 ≤10 组/轮」；错误风暴下积压按每轮 10 组排空（interval 默认 5min ⇒ 2 组/分钟），>120 组/小时的风暴会持续积压（窗口重扫成本随之增长）——正确性换吞吐的有意取舍，登记。

### 1.3 F8 同族残留全查（errorplaybook 相关装配每一处 + cmd/gateway Deps 字面量扫描）

| 装配点 | 判定 |
|---|---|
| `pbDeps.IndexRefresh/CredibilityScan` | ✅ 已修（main.go:7859-7867，判空在装箱前） |
| `pbDeps.ForceRecoverCredential` | ✅ `adminHandler != nil` 先行（main.go:7866-7868），adminHandler 为具体指针 `*admin.Handler`（main.go:3501） |
| `pi-analysis/trigger` 路由守卫 | ✅ `piErrorAnalyzerGlobal != nil`（error_playbook_init.go:197），变量为具体指针 `*promptinjection.ErrorAnalyzer`（main.go:249），赋值点 main.go:7806 亦在 `piCollector != nil` 守卫内 |
| `NewEngine(store, actions, llm, …)` | ✅ llm 具体指针，`if llm := newPlaybookLLMClient(); llm != nil` 非 nil 分支装箱 / else 分支传字面 `nil`（error_playbook_init.go:135-141），无 typed-nil 可能 |
| worker 装配 | ✅ `NewWorker(engine, NewSQLSource(sqlDB), …)` 全具体指针且 sqlDB 在 initErrorPlaybook:119 判空 |
| 全局变量回填 | ✅ `if pbStore != nil { … }`（main.go:7869-7873）；pbEngine/pbActions 构造路径恒非 nil |
| `SetModelCredibilityBackend`/`SetAutoIndexRefresher`（匿名结构接口参数，admin/handler.go:873/904） | ✅ 调用点均判空（main.go:6612-6614 / 6826-6828） |
| 窗口外粗扫（`Deps{` 字面量：main.go:3250/3271 compression、8056 hostedtask CallbackDeps） | 未经窗口、消费方自带 nil 语义（注释自述「nil = memo disabled」），不在本窗归责面 |

**结论：errorplaybook 装配链上无同族残留。**（F8 修的是唯一一处。）

### 1.4 残留缺口（新登记）

- **C-P3-1（诚实性缺口）内置兜底路由「HasRoute=真、执行必失败」**：`actions.go:96-109` `builtinRoutes()` 恒含 `POST /api/admin/pi-analysis/trigger` 占位实现（analyzer 未注入 ⇒ Handler 返回普通 `fmt.Errorf`），而 `HasRoute`（actions.go:133-144）对 builtin 恒真 ⇒ PI 分析器未接线时，引用该路由的 api 方案**归一化期放行、执行期以非 422 错误失败**——「依赖缺项 ⇒ 不进闭集 ⇒ 归一化期 fail-fast（不宣传不可执行动作）」对该路由不成立（f712ea685 修复的正是这一类守卫失效，占位路由是漏网的同族点）。影响面：PI 智能体未配置的部署（生产 211 已配置 ⇒ 当前不受影响）。建议：analyzer 缺项时 `buildErrorPlaybookExtraRoutes` 也不让 builtin 占位进 `HasRoute`（如把「真实实现在场」作为闭集登记条件），或占位 Handler 改回 `NewPlanInvalid`（422 语义）。

---

## 2. Q2 — 83cfb9633 F1-F7 现存性核验（+ 与 f712ea685 的叠加闭环）

逐项当前态在位确认：

| 项 | 状态 | 证据 |
|---|---|---|
| F1 逆序回滚 | ✅ 在位 | `engine.go:320`（失败即回滚）+ `engine.go:353-375` `rollbackCompletedSteps`（快照逆序、无快照步骤不产生回滚噪音 `engine.go:360-362`、回滚失败追加 `/executions/{id}/revert` 人工指引 `engine.go:324-327`）；钉测 TestExecuteEntryAutoRollbackOnStepFailure / RevertFailureSurfacesManualHint / FailureWithoutSnapshotNoRollbackNoise（engine_test.go:435/462/485） |
| F2 提炼标记行 | ✅ 在位 | `UpdatePlanFromRefinement` 写 `trigger_source='refinement'` 留痕、不碰计数/last_status（store.go:492-503）；`ListRefineCandidates` 用 `NOT EXISTS(标记行 AND created_at > last_executed_at)`（store.go:527-532）；995 的 BEFORE UPDATE 触发器前提核实（995_error_playbook.sql:102-114）。真库契约测试含「提炼后退出候选 + 再失败重获资格」两腿（store_realdb_test.go:224-260） |
| F3 HasLLM 前置守卫 | ✅ | worker.go:150 `cfg.AutoAnalyze && w.engine.HasLLM()`；钉测 TestWorkerRefinePassSkippedWithoutLLM（engine_test.go:531） |
| F4 RouteChecker 归一化 fail-fast | ✅ | plan.go:48-50 + plan.go:91-94；正负样本钉测（error_playbook_extra_routes_test.go:52-77） |
| F5 原子 upsert | ✅ | store.go:285-301 单条 `INSERT … ON CONFLICT (error_type, COALESCE(match_pattern,'')) DO UPDATE … RETURNING id` |
| F6 留痕保留期 | ✅ | worker.go:164-177（24h 节流 + `PruneExecutions(playbookRetention=30d)`）+ store.go:590-604 + 钉测（engine_test.go:524-527） |
| F7 /stats SQL 聚合 | ✅ | store.go:730-782 `EntrySummary`（COUNT(*) FILTER + GROUP BY，两条 `rows.Err()` 终检齐）+ handler 接线 admin/error_playbook_handler.go:480 |

**worker.go 两提交叠加闭环**：f712ea685 对 worker.go 的 F5/F6 大改**没有**触碰 F3 守卫段（worker.go:150-162）、prune 段（worker.go:164-177）与引擎侧 F1/F2 —— 三者共存于当前 runOnce，逻辑闭环成立。

**新登记交互面**：

- **C-P3-2 PruneExecutions 会清掉 refinement 标记行**：`PruneExecutions` 按 `created_at < NOW()-30d` 无差别删除（store.go:594-596），包括 F2 的提炼标记行 ⇒ 一条 `last_status='failed'` 且 30 天内未再执行、标记行已过保留期的条目会重新入围提炼候选 ⇒ 每约 30 天多烧一次 LLM 提炼（内存 dedup 只挡进程存活期）。低危、有界（每条目每周期一次）⇒ 登记不修；若要修，可在 PruneExecutions 上加 `AND trigger_source <> 'refinement'` 的保留口径或给标记行独立保留期。

---

## 3. Q3 — 0c3474646 闭集

1. **4 条路由登记齐** ✅：`error_playbook_init.go:195-292` —— pi-analysis/trigger（197-219，需 analyzer 在场）+ credentials/force-recover（220-240）+ auto-route/refresh（241-253）+ model-credibility/trigger（254-290）；单一真源抽取 `admin/diagnostics_credential.go` `ForceRecoverCredential`（0c3474646 diff，HTTP handler 与闭集共用）。
2. **「依赖缺项 ⇒ 不进闭集」在 F8 后真实成立** ✅：判空在装箱前（main.go:7859-7867）⇒ 缺项时 `PlaybookDeps{}` 对应字段为真 nil ⇒ `buildErrorPlaybookExtraRoutes` 不登记（error_playbook_init.go:220/241/254）⇒ 归一化期 fail-fast（plan.go:91-94）。钉测 TestExtraRoutesOmittedWhenDepsNil 实跑绿。
3. **422 body 闸链路** ✅：`NewPlanInvalid` 导出构造器（plan.go:37-39）→ 三条路由 body 闸（error_playbook_init.go:229-231/265-267/275-277）→ admin handler 四处 `errors.As → 422`（admin/error_playbook_handler.go:184-186/251-253/320-325/463-465）。钉测 TestExtraRoutesBodyGates 实跑绿。
4. **实跑**：`go test ./cmd/gateway/ -run 'ExtraRoutes|ErrorPlaybook'` 4 组钉测 + 路由可达 + fail-closed 全 PASS。
5. 残留：§1.4 C-P3-1（builtin 占位路由诚实性）。

---

## 4. Q4 — 前端 98221b41d 复核（12h 轮已 ✅，本审计补查）

1. **navCounts 计数锁** ✅：`web/src/config/appNav.ts:269` 菜单项在车；`navCounts.test.ts` EXPECTED_ITEMS=68 / PATHS=67 / FOLDED=54 与提交自述 67→68/66→67/53→54 一致；实跑 `vitest` navCounts 76/76 + PlanEditor 4/4 + emptyStateKeyResolution 10/10 全绿。
2. **revert 交互闭环** ✅（带一处 UX 缺口）：按钮仅 `succeeded/failed` 可见（ErrorPlaybookDetailView.vue:166-172）＝ 后端 409 门（error_playbook_handler.go:412-414）对齐；成功后 `loadExecutions()` 刷新（DetailView.vue:444-454）；422 原文经 `revertError` 透出（DetailView.vue:143-147）。**C-P3-3：revert 成功零回执**——`revertExecution` 返回的 `reverted` 步骤数组被丢弃（api/errorPlaybook.ts:253-255 返回值未用），执行行状态本身不因回滚改变 ⇒ 时间线重渲染后视觉无任何变化，管理员无法确认「回滚确实发生了」（成功/失败两个出口只有失败有横幅）。建议：成功后以 banner 回显 `reverted` 摘要。
3. **三形态方案编辑校验面** ✅：PlanEditor client 轻校验（JSON parse / MAX_STEPS=8 对齐 `maxPlanSteps`，PlanEditor.vue:145）+ 重校验交后端 NormalizePlan（422 原文透出）——诚实路径，与「权限真源在后端」同构。api 路径前缀闸/动作白名单由后端唯一裁决，前端不复刻（无两套规则打架）。
4. **权限口径登记**：菜单 `super: true, hideForTenant: true`（appNav.ts:269），而 `AdminMiddleware`（admin/auth.go:87-141）不按 role 分级、任何通过 Verify 的管理 JWT 都放行 ⇒ 前端比后端更收紧，安全侧无泄漏；口径差异登记（若产品要给 tenant_admin 开放，改前端两处即可，后端已放行）。

---

## 5. Q5 — Maestro 资产

1. **判定口径单点化** ✅（探针类）：`assert-error-playbook.mjs:16` `import { summarize, EXIT_FAIL } from './result.mjs'`，退出码 0/1/2 注释与 61 轮 ㉗ 纪律一致（文件头 :10-11）。采样器 `observe-error-playbook.mjs` 不走 result.mjs（观察类、exit 0/1/3 自有协议、无 PASS/FAIL 判定）——不违反「探针不得自写口径」的实质（它不做产品判定），口径分叉登记即可。
2. **C-P2-1 采样器把证据写进仓库工作树**：`observe-error-playbook.mjs:30-31` `OBS/SUMMARY = resolve(REPO, 'docs/audit/evidence/…')`，REPO 从脚本位置反推。自动化（automation-3094bae8，每 2h）若在部署树（或任何参与部署 dirty 判据的 checkout）上跑：JSONL 是未跟踪文件 ⇒ `git status --porcelain` 出现新行（⑧ 教训：porcelain 含未跟踪文件；dirty 判据的 exclude 清单只有 VERSION/version.json/menu-config.json 四处）⇒ **下一次部署版本身份带 `-dirty`**（审计链锚点打折）；且 48h 窗口大概率覆盖下一次部署。修法：落盘改 `/var/lib/llm-gateway-observe/`（或 env 覆盖口 + 默认仓外），或至少 `--untracked-files=no` 级别的豁免登记。
3. **C-P2-2 探针行数对拍 limit 错位（潜伏假红）**：探针 `entries?limit=100`（assert-error-playbook.mjs:81）vs 页面 `pageSize=50`（ErrorPlaybookListView.vue:250）vs 对拍 `domData.rows === entries.length`（:133-134）。条目 ≤50 时绿（当前种子 5 条）；**条目一旦超过 50**（自动分析持续落新条目、手工录入、PI 分析落库）DOM 行=50 vs API N ⇒ 必假红。这是 61 轮 ⑯-④「对拍基准必须与被测页同源」家族的体积版。修法：探针改抓页面自己收到的那份响应（CDP Network 域），或对拍时用 `Math.min(pageSize, total)` 口径。
4. **对拍基准自述与实现不符（措辞级）**：探针的 stats/entries 是**探针在页面上下文另发的一次 fetch**（assert:76-85，same-origin 会话内），不是 CDP Network 截获的「页面自己收到的那份响应」；49543a07d 证据文件自称「数字对拍=页面自己收到的那份响应」——本域数据近乎静态（total_entries 缓变）故实测绿，但**措辞应订正**，避免下一代探针照抄这个「同源」实现（C-P3-4）。
5. **假绿家族对照（61 轮 ㉑）**：✅ 语种钉住（localStorage + `<html lang>` 读回，钉不住 ⇒ skip=量具失效，assert:44-73）；✅ 落定判据锚「手册条目」统计卡——该卡 `v-if="stats"` 只在数据到达后渲染（ListView.vue:24）且 `load()` 先 entries 后 stats（ListView.vue:322-341）⇒ 不是「占位符也是非空」形态；状态判据未用纯文案裸判（都带 locale 钉住前提）。残留噪音：`hasRevertHint: … || true` 恒真死字段（assert:176）与未消费的 `entryType`（assert:177）——不参与判定、无假绿效果，删掉以免误导后人（C-P3-5）。
6. **采样器其余**：种子凭据硬编码进仓内脚本（observe:49-51 `demoadmin/Demo@admin123`；凭据已在 docs/211-user-auth-final-verification.md 公开，扩散面+1，C-P3-6）；`/executions?limit=100` 无时间窗 ⇒ 跨轮重复聚合（收口 md 自认「同一次留痕会在多轮重复计」，误报率代理有偏，观察口径可接受，C-P3-7）；48h 收口判定 `n<RUN_BUDGET && elapsedH<48` 逻辑正确（时间预算兜底轮次缺失）。
7. **flow17** ✅：52 步、落定判据锚数据区特有文案、表头 `.*….*`、tapOn 改点种子标题（49543a07d 修的三处量具错都有归档）；`_shared/login.yaml` env 全转发（含 ADMIN_DISPLAY_NAME）符合 d72b85b58 闭合约定。

---

## 6. Q6 — 并发面与门禁实跑

| 项 | 结果 |
|---|---|
| `go build ./domains/errorplaybook/ ./cmd/gateway/ ./admin/` | ✅ |
| `go vet` 三包 | ✅ 0 输出 |
| `go test ./domains/errorplaybook/` | ✅ ok |
| `go test -race ./domains/errorplaybook/` | ✅ ok 1.145s，**0 race**（engine.mu 覆盖 pending/lastAnalyzed/lastRefined/lastRun；worker 单 goroutine 循环 + bg.SpawnLoop 监督） |
| `go test ./cmd/gateway/ -run 'ExtraRoutes\|ErrorPlaybook\|Playbook'` | ✅ 全 PASS（含 TestErrorPlaybookRoutesReachableOverHTTP 11 条 / FailClosedNoAuth） |
| web：navCounts + PlanEditor + emptyStateKeyResolution | ✅ 90/90 |

---

## 7. Q7 — 995 与 998/999 耦合面（只登记）

- **无表级耦合**：`error_playbook_entries/executions` 的读写方仅 errorplaybook 包 + settings specs（settings/error_playbook_specs.go）+ installer 登记 + sqlguard 交付测试（internal/sqlguard/migration_995_delivery_test.go）；997/998/999 只动 tasks/session_summaries 域，双向零引用。
- **共享面三点**（提炼不会撞会话总结链路的 schema，撞的是资源面）：① `candidate_failure_logs_hot`——streaming executors（domains/streaming/executors/candidate_failure_logger.go:67）写、errorplaybook SQLSource/logcontext/PI analyzer 读；② `piSQLDB` 连接与 `piAgentCtx` 停机链（main.go:7772/7869）；③ `LLM_GATEWAY_ANALYSIS_*` 端点与 PI 智能体/总结链路共用额度（未知错误分析每类 2 次 LLM + 提炼若干次——R3 既有登记）。
- 部署耦合：f712ea685 已补 997/998/999 九面投递登记（installer embeddata/map 与 sqlguard 交付测试；本轮实测 installer 包内 998/999 embed 变量与 map 在场）。

---

## 8. 发现清单（P0~P3）与归因

| 级别 | 编号 | 一句话 | 位置 | 归因 |
|---|---|---|---|---|
| P0 | — | 无（f712ea685 的 3 个 P0 修复经源码级复核全部真实；但**未部署**——211 现役 0c3474646@seq3194 早于 f712ea685，摄取链在生产仍带着两缺陷跑，error_playbook_executions=0 与主控活体一致；收口动作=部署 f712ea685 之后的树） | worker.go:242-243/271 | — |
| P1 | — | 无新增 | — | — |
| P2 | C-P2-1 | 48h 灰度采样器把 JSONL/收口 md 写进仓库工作树，若在部署树上跑 ⇒ 下次部署版本身份 -dirty | maestro/tools/observe-error-playbook.mjs:30-31 | 812bdce65（窗口内引入） |
| P2 | C-P2-2 | 探针行数对拍 limit 错位（探针 100 vs 页面 pageSize 50）⇒ 条目 >50 后必假红 | maestro/tools/assert-error-playbook.mjs:81,133 ↔ web/src/views/admin/error-playbook/ErrorPlaybookListView.vue:250 | 812bdce65（窗口内引入） |
| P2 | C-P2-3 | f712ea685 的 F1/F22 修复与 F6 新水位线语义**零回归钉测**（FetchWindow SQL 形状无文本/真库钉测、apiPlanTimeoutMS 纯函数无单测、cap 推迟/重读/空窗推进无 worker 钉测）——修复轮自己宣称的「只有假 store」缺口在修复后依旧 | worker.go:232-305 / actions.go:336-359 / engine_test.go:506-560 | f712ea685（窗口内引入） |
| P3 | C-P3-1 | builtin 占位路由 HasRoute 恒真、analyzer 缺项时执行必失败 ⇒ 「缺项不进闭集」对它不成立 | domains/errorplaybook/actions.go:96-109,133-144 | 0c3474646 前的既有形状（窗口内未修） |
| P3 | C-P3-2 | PruneExecutions 连提炼标记行一起清 ⇒ 陈旧失败条目每 ~30 天重新入围提炼、多烧一次 LLM | store.go:594-596 ↔ store.go:527-532 | 83cfb9633+f712ea685 组合（窗口内引入） |
| P3 | C-P3-3 | revert 成功零回执（返回的 reverted 步骤被丢弃、时间线视觉无变化） | ErrorPlaybookDetailView.vue:444-454 | 98221b41d（窗口内引入） |
| P3 | C-P3-4 | 证据文件自称「页面自己收到的那份响应」与探针实现（另发 fetch）不符 | docs/audit/evidence/2026-11-22-error-playbook-round-evidence.txt ↔ assert:76-85 | 49543a07d（窗口内引入，措辞） |
| P3 | C-P3-5 | 探针死字段（`|| true` 恒真 + 未消费 entryType） | assert-error-playbook.mjs:176-177 | 812bdce65（窗口内引入） |
| P3 | C-P3-6 | 种子凭据硬编码进仓内采样脚本 | observe-error-playbook.mjs:49-51 | 812bdce65（窗口内引入） |
| P3 | C-P3-7 | 采样器 /executions 无时间窗 ⇒ 跨轮重复聚合、比率代理有偏（收口 md 已自认） | observe-error-playbook.mjs:66 | 812bdce65（窗口内引入，已部分自披露） |
| P3 | C-P3-8 | F6 边界：推迟分组在新水位线之前的事件聚合欠计（MAX(ts)==W1 同刻时窗口切片整段丢）——83cfb9633 轮 R4 的收窄形残留 | worker.go:119-192,249 | 83cfb9633 登记 R4 的同族（基线 + 本轮收窄） |
| P3 | C-P3-9 | 0c3474646 把 menu-config.json 的 `exported_at` 构建戳带进后端提交（构建侧漏，非内容） | 0c3474646:web/public/menu-config.json 仅戳变 | 0c3474646（窗口内引入，卫生） |

**基线存量（非窗口引入，登记不修）**：水位线 at-most-once / 重启 15min 重读（设计属性）；AdminMiddleware role 口径与前端收紧的差异；`menu-config.json` 生产 401 待查（49543a07d 已登记）；auto_execute=false 灰度档（owner 默认）。

---

## 9. 修复建议（按性价比排序）

1. **C-P2-3**：补三个便宜钉测——`apiPlanTimeoutMS` 纯函数表测（int/float64/json.Number 四行）；worker cap 推迟钉测（fakeSource 造 12 组 + limit=10，断言 `w.watermark == 已处理 max LastSeen` 且推迟组下轮重读）；FetchWindow SQL 文本钉测（`provider_id::text` + `pq.Array` 承装 + `ORDER BY MAX(ts) ASC`）——纯文本断言即可挡住同族回退。
2. **C-P2-1**：采样器落盘位置加 env 覆盖口（默认仓外路径）；至少在 README/AGENTS 登记自动化运行目录与部署树的关系。
3. **C-P2-2**：探针行数对拍改 `min(pageSize,total)` 口径或改 CDP Network 截获页面自身响应（顺手修 C-P3-4 的措辞）。
4. **C-P3-1**：analyzer 缺项时把 builtin 占位路由移出 `HasRoute` 闭集（或占位改 `NewPlanInvalid`）。
5. 其余 P3 按登记处置，不扩修。

## 10. 审计环境备注

- 审计基线 HEAD=`c446de9a4`（全程未变）。**审计期间共享工作树被并行列车实时修改**（997/998/999 迁移文件 + streaming 四文件 + db-changelog，属会话总结列车在途件）；本报告全部 commit 级结论走 git 对象（`git show`），工作树级读取的文件均经 `git status` 对拍确认不在并行修改集内 ⇒ 证据不受污染。
- 真库契约腿：本地 kx-citus 15433 TCP 可达但协议停摆（`psql -c 'SELECT 1'` 亦挂起）⇒ 真库腿本轮无法独立复跑；F2 标记判据的 SQL 语义经 schema 静态复核（995 触发器 + NOT EXISTS 判据 + 83cfb9633 轮自录真库两腿）判定成立。下次 kx-citus 恢复后建议补跑一次 `TEST_ERROR_PLAYBOOK_DSN=… go test ./domains/errorplaybook/ -run TestRealDB -count=1` 作回归锚。
